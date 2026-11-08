package fitsync

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// Dead letters kept in a bounded file, listed, replayed and dropped from the
// Admin API (I189). The sink dead_letters keeps each dead letter as a line
// of <state_dir>/<pipeline>.dead_letters.jsonl; a replay writes the records
// they hold to the sinks they failed at again, through the transforms when
// a transform failed them, and drops those written.

func init() {
	module.Register(module.Info{ID: Sinks + ".dead_letters", New: func() any { return new(DeadLetters) },
		Doc: "Keeps a pipeline's dead letters in a file of its state_dir, bounded by max_records and max_bytes, each with the record, its error, where it failed and the attempts made, so that the Admin API and fit sync dead-letters list them, replay them to the sinks once the cause is fixed, and drop them. Only a pipeline's dead_letter sink."})
}

// The bounds of a dead_letters sink, unless its params say otherwise.
const (
	DefaultDeadLetterRecords = 10000
	DefaultDeadLetterBytes   = 64 << 20
)

// The errors of the dead letters' controls.
var (
	// ErrNoDeadLetters is a pipeline whose dead_letter sink is not a
	// dead_letters sink, which keeps none fit can read back.
	ErrNoDeadLetters = errors.New("the pipeline keeps no dead letters fit can read: its dead_letter sink is not dead_letters")
	// ErrDeadLettersFull is a dead letter a full dead_letters sink refused.
	ErrDeadLettersFull = errors.New("the dead letters are full")
)

// DeadLetters is the sink dead_letters.
type DeadLetters struct {
	Path       string `json:"path,omitempty" doc:"The file the dead letters are kept in, a JSON line each, a relative path being under the sync's state_dir; <state_dir>/<pipeline>.dead_letters.jsonl when left out. Two pipelines may not share one."`
	MaxRecords int    `json:"max_records,omitempty" min:"0" default:"10000" doc:"The most dead letters kept. 10000 when left out or 0."`
	MaxBytes   int64  `json:"max_bytes,omitempty" min:"0" default:"67108864" doc:"The most bytes of dead letters kept, as their lines in the file. 64 MiB when left out or 0."`
	WhenFull   string `json:"when_full,omitempty" enum:"refuse,drop_oldest" default:"\"refuse\"" doc:"What a dead letter that does not fit does: refuse fails its write, so that after its attempts the pipeline stops, its checkpoint before the record, and nothing is lost; drop_oldest drops the oldest dead letters to make room, counting them. refuse when left out."`

	mu   sync.Mutex
	path string
	st   *dlStore
	err  error
}

// bind gives d the file of the pipeline name, under dir, and opens it.
func (d *DeadLetters) bind(dir, name string) {
	path := d.Path
	switch {
	case path == "":
		path = filepath.Join(dir, name+".dead_letters.jsonl")
	case !filepath.IsAbs(path):
		path = filepath.Join(dir, path)
	}
	d.mu.Lock()
	d.path = path
	d.mu.Unlock()
	_, _ = d.store()
}

// store returns d's file, opened at the first call that can.
func (d *DeadLetters) store() (*dlStore, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.st != nil {
		return d.st, nil
	}
	if d.path == "" {
		return nil, errors.New("dead_letters is only a pipeline's dead_letter sink")
	}
	d.st, d.err = acquireStore(d.path)
	if d.err != nil {
		return nil, d.err
	}
	d.st.setBounds(d.MaxRecords, d.MaxBytes, d.WhenFull == "drop_oldest")
	return d.st, nil
}

// Write keeps batch's records.
func (d *DeadLetters) Write(_ context.Context, batch []*Record) error {
	st, err := d.store()
	if err != nil {
		return err
	}
	docs := make([][]byte, 0, len(batch))
	for i, r := range batch {
		doc, err := r.AppendJSON(nil)
		if err != nil {
			return &BatchError{Index: i, Err: Permanent(err)}
		}
		docs = append(docs, doc)
	}
	return st.add(docs)
}

// Test opens the file, once the pipeline has named it.
func (d *DeadLetters) Test(context.Context) error {
	d.mu.Lock()
	unbound := d.path == ""
	d.mu.Unlock()
	if unbound {
		return nil
	}
	_, err := d.store()
	return err
}

// Cleanup closes the file, once no other dead_letters sink has it open.
func (d *DeadLetters) Cleanup() {
	d.mu.Lock()
	st := d.st
	d.st = nil
	d.mu.Unlock()
	if st != nil {
		st.release()
	}
}

// status is the file's, as Status shows it.
func (d *DeadLetters) status() *DeadLetterStore {
	st, err := d.store()
	if err != nil {
		d.mu.Lock()
		path := d.path
		d.mu.Unlock()
		return &DeadLetterStore{Path: path, Err: err.Error()}
	}
	return st.status()
}

// dlStores are the dead letters' files open, by path: the sinks of a
// pipeline reloaded share its file with those they replace.
var (
	dlMu     sync.Mutex
	dlStores = map[string]*dlStore{}
)

func acquireStore(path string) (*dlStore, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dlMu.Lock()
	defer dlMu.Unlock()
	if s := dlStores[abs]; s != nil {
		s.refs++
		return s, nil
	}
	s, err := openStore(abs)
	if err != nil {
		return nil, err
	}
	s.refs = 1
	dlStores[abs] = s
	return s, nil
}

func (s *dlStore) release() {
	dlMu.Lock()
	defer dlMu.Unlock()
	if s.refs--; s.refs > 0 {
		return
	}
	delete(dlStores, s.path)
	s.mu.Lock()
	_ = s.f.Close()
	s.mu.Unlock()
}

// dlStore is a dead letters' file: a line for each dead letter added, and
// one for each set of them removed, compacted when the removed take more
// room than the kept.
type dlStore struct {
	path string
	refs int // dlMu's

	mu      sync.Mutex
	f       *os.File
	letters []storedLetter // by id
	next    uint64
	// bytes is the kept letters' lines', size the file's.
	bytes, size int64
	dropped     int64

	maxRecords int
	maxBytes   int64
	dropOldest bool
}

type storedLetter struct {
	id    uint64
	saved time.Time
	doc   []byte
	line  int64
}

// dlLine is a line of the file: a dead letter added, or some removed.
type dlLine struct {
	ID      uint64          `json:"id,omitempty"`
	Saved   time.Time       `json:"saved,omitzero"`
	Letter  json.RawMessage `json:"letter,omitempty"`
	Removed []uint64        `json:"removed,omitempty"`
}

// compactSlack is how many bytes of removed letters the file may hold
// beyond those it keeps before it is compacted; a variable for tests.
var compactSlack int64 = 1 << 20

// openStore reads the file at path, made when missing; a last line cut
// short, as by a crash while it was written, is cut off.
func openStore(path string) (*dlStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	s := &dlStore{path: path, f: f, next: 1}
	if err := s.load(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("the dead letters %s: %w", path, err)
	}
	s.setBounds(0, 0, false)
	return s, nil
}

func (s *dlStore) load() error {
	rd := bufio.NewReader(s.f)
	var off int64
	for {
		line, err := rd.ReadBytes('\n')
		if err == io.EOF {
			break // a line without its end is cut off
		}
		if err != nil {
			return err
		}
		var l dlLine
		if err := json.Unmarshal(line, &l); err != nil {
			if _, more := rd.Peek(1); more == io.EOF {
				break // the last line, cut off
			}
			return fmt.Errorf("the line at %d: %w", off, err)
		}
		n := int64(len(line))
		off += n
		switch {
		case len(l.Removed) > 0:
			s.drop(l.Removed)
		case l.ID > 0 && len(l.Letter) > 0:
			s.letters = append(s.letters, storedLetter{id: l.ID, saved: l.Saved, doc: l.Letter, line: n})
			s.bytes += n
			s.next = max(s.next, l.ID+1)
		}
	}
	if err := s.f.Truncate(off); err != nil {
		return err
	}
	s.size = off
	slices.SortFunc(s.letters, func(a, b storedLetter) int { return compareIDs(a.id, b.id) })
	return nil
}

func compareIDs(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (s *dlStore) setBounds(records int, bytes int64, dropOldest bool) {
	if records <= 0 {
		records = DefaultDeadLetterRecords
	}
	if bytes <= 0 {
		bytes = DefaultDeadLetterBytes
	}
	s.mu.Lock()
	s.maxRecords, s.maxBytes, s.dropOldest = records, bytes, dropOldest
	s.mu.Unlock()
}

// drop forgets the letters ids names, and returns those it had; s.mu is
// held, or s not shared yet.
func (s *dlStore) drop(ids []uint64) []uint64 {
	var gone []uint64
	s.letters = slices.DeleteFunc(s.letters, func(l storedLetter) bool {
		if slices.Contains(ids, l.id) {
			gone = append(gone, l.id)
			s.bytes -= l.line
			return true
		}
		return false
	})
	return gone
}

// add keeps docs, dead letters' canonical JSON, each on a line, the file
// synced before it returns. A letter that does not fit fails as a
// *BatchError, those before it kept, unless the oldest are dropped for it.
func (s *dlStore) add(docs [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var buf []byte
	var added []storedLetter
	var dropped []uint64
	var failed error
	count, bytes := len(s.letters), s.bytes
	oldest := 0
	now := time.Now().UTC()
	for i, doc := range docs {
		id := s.next + uint64(len(added))
		line, err := json.Marshal(dlLine{ID: id, Saved: now, Letter: doc})
		if err != nil {
			failed = &BatchError{Index: i, Err: Permanent(err)}
			break
		}
		line = append(line, '\n')
		n := int64(len(line))
		if n > s.maxBytes {
			failed = &BatchError{Index: i, Err: Permanent(fmt.Errorf("a dead letter of %d bytes is more than max_bytes %d", n, s.maxBytes))}
			break
		}
		for count+1 > s.maxRecords || bytes+n > s.maxBytes {
			if !s.dropOldest || oldest >= len(s.letters)+len(added) {
				failed = &BatchError{Index: i, Err: fmt.Errorf("%w: %d dead letters, %d bytes in %s", ErrDeadLettersFull, count, bytes, s.path)}
				break
			}
			// The oldest kept, or one this batch added before.
			var l storedLetter
			if oldest < len(s.letters) {
				l = s.letters[oldest]
			} else {
				l = added[oldest-len(s.letters)]
			}
			oldest++
			dropped = append(dropped, l.id)
			count--
			bytes -= l.line
		}
		if failed != nil {
			break
		}
		added = append(added, storedLetter{id: id, saved: now, doc: doc, line: n})
		buf = append(buf, line...)
		count++
		bytes += n
	}
	if len(added) == 0 {
		return failed
	}
	if len(dropped) > 0 {
		// After the letters added, since it may drop some of them.
		rm, _ := json.Marshal(dlLine{Removed: dropped}) // numbers always marshal
		buf = append(append(buf, rm...), '\n')
	}
	if err := s.write(buf); err != nil {
		return err
	}
	s.letters = append(s.letters, added...)
	s.next += uint64(len(added))
	s.bytes += sumLines(added)
	if len(dropped) > 0 {
		s.drop(dropped)
		s.dropped += int64(len(dropped))
	}
	return failed
}

func sumLines(ls []storedLetter) int64 {
	n := int64(0)
	for _, l := range ls {
		n += l.line
	}
	return n
}

// write appends buf to the file and syncs it; on an error the file is cut
// back to what it was. s.mu is held.
func (s *dlStore) write(buf []byte) error {
	_, err := s.f.Write(buf)
	if err == nil {
		err = s.f.Sync()
	}
	if err != nil {
		_ = s.f.Truncate(s.size)
		return fmt.Errorf("the dead letters %s: %w", s.path, err)
	}
	s.size += int64(len(buf))
	return nil
}

// remove drops the letters ids names, durably, and returns how many it had.
func (s *dlStore) remove(ids []uint64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var have []uint64
	for _, l := range s.letters {
		if slices.Contains(ids, l.id) {
			have = append(have, l.id)
		}
	}
	if len(have) == 0 {
		return 0, nil
	}
	rm, _ := json.Marshal(dlLine{Removed: have})
	if err := s.write(append(rm, '\n')); err != nil {
		return 0, err
	}
	s.drop(have)
	if s.size > 2*s.bytes+compactSlack {
		if err := s.compact(); err != nil {
			return len(have), err
		}
	}
	return len(have), nil
}

// compact writes the kept letters to a new file, renamed over the old.
// s.mu is held.
func (s *dlStore) compact() error {
	var buf []byte
	for _, l := range s.letters {
		line, _ := json.Marshal(dlLine{ID: l.id, Saved: l.saved, Letter: l.doc}) // it was read or written so
		buf = append(append(buf, line...), '\n')
	}
	tmp := s.path + ".tmp"
	err := os.WriteFile(tmp, buf, 0o600)
	if err == nil {
		var f *os.File
		if f, err = os.OpenFile(tmp, os.O_RDWR|os.O_APPEND, 0o600); err == nil {
			if err = f.Sync(); err == nil {
				err = os.Rename(tmp, s.path)
			}
			if err == nil {
				_ = s.f.Close()
				s.f, s.size = f, int64(len(buf))
				return nil
			}
			_ = f.Close()
		}
	}
	_ = os.Remove(tmp)
	return fmt.Errorf("compacting the dead letters %s: %w", s.path, err)
}

func (s *dlStore) status() *DeadLetterStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &DeadLetterStore{Path: s.path, Kept: len(s.letters), Bytes: s.bytes, MaxRecords: s.maxRecords, MaxBytes: s.maxBytes, Dropped: s.dropped}
}

// pick returns the letters ids names, every one when ids is nil, and the
// ids it does not have.
func (s *dlStore) pick(ids []uint64) ([]storedLetter, []uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ids == nil {
		return slices.Clone(s.letters), nil
	}
	var out []storedLetter
	var missing []uint64
	for _, id := range ids {
		i, ok := slices.BinarySearchFunc(s.letters, id, func(l storedLetter, id uint64) int { return compareIDs(l.id, id) })
		if ok {
			out = append(out, s.letters[i])
		} else {
			missing = append(missing, id)
		}
	}
	return out, missing
}

// page returns up to limit letters after the id after.
func (s *dlStore) page(after uint64, limit int) ([]storedLetter, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, _ := slices.BinarySearchFunc(s.letters, after+1, func(l storedLetter, id uint64) int { return compareIDs(l.id, id) })
	end := min(i+limit, len(s.letters))
	return slices.Clone(s.letters[i:end]), end < len(s.letters)
}

// DeadLetter is a dead letter as the Admin API shows it.
type DeadLetter struct {
	ID    uint64    `json:"id"`
	Saved time.Time `json:"saved"`
	// Error, At and Attempts are why the record failed, where (sink <name>,
	// transform <n> (<module>)) and after how many attempts.
	Error    string `json:"error,omitempty"`
	At       string `json:"at,omitempty"`
	Attempts int64  `json:"attempts,omitempty"`
	// Record is the record that failed, in its canonical JSON; the whole
	// letter for one not in a dead letter's form.
	Record json.RawMessage `json:"record"`
}

// letter reads l's dead letter, and the record it holds; rec is nil when
// the letter is not in a dead letter's form.
func (l *storedLetter) letter() (DeadLetter, *Record) {
	d := DeadLetter{ID: l.id, Saved: l.saved, Record: json.RawMessage(l.doc)}
	r, err := ParseRecord(l.doc)
	if err != nil {
		return d, nil
	}
	text := func(name string) string {
		v, _ := r.After.Get(name)
		return v.Text()
	}
	d.Error, d.At = text("error"), text("at")
	n, _ := r.After.Get("attempts")
	d.Attempts = n.AsInt()
	doc, ok := r.After.Get("record")
	if !ok {
		return d, nil
	}
	d.Record = json.RawMessage(doc.Text())
	rec, err := ParseRecord([]byte(doc.Text()))
	if err != nil {
		return d, nil
	}
	return d, rec
}

// DeadLetterPage is a page of a pipeline's dead letters.
type DeadLetterPage struct {
	Letters []DeadLetter     `json:"letters"`
	Store   *DeadLetterStore `json:"store"`
	// More says there are letters after the last of this page.
	More bool `json:"more,omitempty"`
}

// The bounds of a page of dead letters.
const (
	DefaultDeadLetterPage = 100
	MaxDeadLetterPage     = 1000
)

// deadLetterStore returns the file of the pipeline named name's dead
// letters. rt.mu is held.
func (rt *Runtime) deadLetterStore(name string) (*pipe, *dlStore, error) {
	p, err := rt.pipe(name)
	if err != nil {
		return nil, nil, err
	}
	d, ok := p.dl.(*DeadLetters)
	if !ok {
		return nil, nil, fmt.Errorf("%w (pipeline %s)", ErrNoDeadLetters, name)
	}
	st, err := d.store()
	return p, st, err
}

// DeadLetters returns the dead letters of the pipeline named name, up to
// limit (DefaultDeadLetterPage when 0, at most MaxDeadLetterPage) after the
// id after, oldest first.
func (rt *Runtime) DeadLetters(name string, after uint64, limit int) (*DeadLetterPage, error) {
	rt.mu.Lock()
	_, st, err := rt.deadLetterStore(name)
	rt.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultDeadLetterPage
	}
	ls, more := st.page(after, min(limit, MaxDeadLetterPage))
	out := &DeadLetterPage{Letters: make([]DeadLetter, 0, len(ls)), Store: st.status(), More: more}
	for i := range ls {
		d, _ := ls[i].letter()
		out.Letters = append(out.Letters, d)
	}
	return out, nil
}

// DropDeadLetters drops the dead letters of the pipeline named name that
// ids names, every one when ids is nil, and returns how many it dropped.
func (rt *Runtime) DropDeadLetters(name string, ids []uint64) (int, error) {
	rt.mu.Lock()
	_, st, err := rt.deadLetterStore(name)
	rt.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if ids == nil {
		ls, _ := st.pick(nil)
		ids = make([]uint64, 0, len(ls))
		for _, l := range ls {
			ids = append(ids, l.id)
		}
	}
	n, err := st.remove(ids)
	if n > 0 {
		rt.opts.Log.Warnf("pipeline %s: %d dead letters dropped", name, n)
	}
	return n, err
}

// Replay is what a replay of dead letters did.
type Replay struct {
	// Replayed are the dead letters written to their sinks, and dropped.
	Replayed []uint64 `json:"replayed"`
	// Failed are those that failed again, or could not be replayed, and
	// are kept.
	Failed []ReplayFailure `json:"failed,omitempty"`
	// Status is the pipeline's, started again.
	Status Status `json:"status"`
}

// ReplayFailure is a dead letter a replay did not write.
type ReplayFailure struct {
	ID  uint64 `json:"id"`
	Err string `json:"error"`
}

// ReplayDeadLetters writes the records of the dead letters of the pipeline
// named name that ids names, every one when ids is nil, to the sinks they
// failed at again, and drops those written. A record a sink failed is
// written to that sink alone, as the transforms made it; one a transform
// failed goes through the transforms again, and what they make to every
// sink. The pipeline is stopped meanwhile, writing what it holds and its
// checkpoint first, so that the replay is the sinks' only writer, and
// started again after, as Resume would, one stopped included. A write is
// tried as the pipeline's retry says; a letter still failing, or whose
// sink the pipeline no longer has, is kept and reported. A replay writes
// the record as it was: a later change of its key that reached the sink
// is overwritten.
func (rt *Runtime) ReplayDeadLetters(ctx context.Context, name string, ids []uint64) (*Replay, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p, st, err := rt.deadLetterStore(name)
	if err != nil {
		return nil, err
	}
	letters, missing := st.pick(ids)
	out := &Replay{Replayed: []uint64{}}
	for _, id := range missing {
		out.Failed = append(out.Failed, ReplayFailure{id, "no such dead letter"})
	}
	if len(letters) > 0 {
		err = rt.restart(p, false, func() error {
			done, failed := p.replay(ctx, letters)
			out.Failed = append(out.Failed, failed...)
			n, err := st.remove(done)
			p.meter.replayed.Add(int64(len(done)))
			out.Replayed = append(out.Replayed, done...)
			rt.opts.Log.Infof("pipeline %s: %d dead letters replayed and dropped, %d kept", name, n, len(failed))
			return err
		})
	}
	slices.SortFunc(out.Failed, func(a, b ReplayFailure) int { return compareIDs(a.ID, b.ID) })
	out.Status = p.status()
	return out, err
}

// replayed is a record a replay writes to a sink, and the letter it comes
// from.
type replayed struct {
	rec    *Record
	letter int
}

// replay writes letters' records, as ReplayDeadLetters says, while the
// pipeline is stopped, and returns the ids of those written and those not.
func (p *pipe) replay(ctx context.Context, letters []storedLetter) ([]uint64, []ReplayFailure) {
	why := make([]string, len(letters))
	perSink := make([][]replayed, len(p.sinks))
	var through []*Record
	var throughOf []int
	for i := range letters {
		d, rec := letters[i].letter()
		switch {
		case rec == nil:
			why[i] = "not a dead letter: it holds no record"
		case strings.HasPrefix(d.At, "transform "):
			rec.seq = uint64(len(through))
			through = append(through, rec)
			throughOf = append(throughOf, i)
		case strings.HasPrefix(d.At, "sink "):
			k := slices.IndexFunc(p.cfg.Sinks, func(s config.SyncSink) bool { return s.Name == d.At[len("sink "):] })
			if k < 0 {
				why[i] = "the pipeline has no " + d.At + " any more"
				continue
			}
			perSink[k] = append(perSink[k], replayed{rec, i})
		default:
			why[i] = fmt.Sprintf("it failed at %q, which is not a sink or a transform", d.At)
		}
	}
	if len(through) > 0 {
		c := newStages(p.transforms)
		c.run(through)
		k := 0
		for j := range through {
			start := k
			for k < len(c.outs) && c.outs[k].src == j {
				k++
			}
			if err := c.errs[j]; err != nil {
				why[throughOf[j]] = fmt.Sprintf("transform %d (%s): %v", c.where[j], p.cfg.Transforms[c.where[j]].Module, err)
				continue
			}
			for _, o := range c.outs[start:k] {
				for s := range perSink {
					perSink[s] = append(perSink[s], replayed{o.rec, throughOf[j]})
				}
			}
		}
		c.done()
	}
	for s, recs := range perSink {
		p.replayTo(ctx, s, recs, why)
	}
	var done []uint64
	var failed []ReplayFailure
	for i, l := range letters {
		if why[i] == "" {
			done = append(done, l.id)
		} else {
			failed = append(failed, ReplayFailure{l.id, why[i]})
		}
	}
	return done, failed
}

// replayTo writes recs to sink s in batches as its config bounds them,
// trying each as the pipeline's retry says; a record still failing sets
// why of its letter.
func (p *pipe) replayTo(ctx context.Context, s int, recs []replayed, why []string) {
	cfg := &p.cfg.Sinks[s]
	m := p.sinkMeter(s)
	for len(recs) > 0 {
		n, size := 0, int64(0)
		for n < len(recs) && n < cfg.Records() && (n == 0 || size+int64(recs[n].rec.Size()) <= cfg.Bytes()) {
			size += int64(recs[n].rec.Size())
			n++
		}
		batch := recs[:n]
		items := make([]item, n)
		list := make([]*Record, n)
		for i, r := range batch {
			items[i], list[i] = item{r.rec, r.rec.Size()}, r.rec
		}
		for i, attempt := 0, 1; i < n; {
			if err := ctx.Err(); err != nil {
				for _, r := range batch[i:] {
					why[r.letter] = "the replay was interrupted: " + err.Error()
				}
				break
			}
			err := p.sinks[s].Write(ctx, list[i:])
			m.wrote(items[i:], err)
			if err == nil {
				break
			}
			var be *BatchError
			if errors.As(err, &be) && be.Index >= 0 && be.Index < n-i {
				if be.Index > 0 {
					i += be.Index
					attempt = 1
				}
				err = be.Err
			}
			if attempt < p.cfg.Attempts() && !IsPermanent(err) && ctx.Err() == nil {
				m.retries.Add(1)
				t := time.NewTimer(p.cfg.Backoff(attempt))
				select {
				case <-t.C:
				case <-ctx.Done():
				}
				t.Stop()
				attempt++
				continue
			}
			why[batch[i].letter] = fmt.Sprintf("sink %s: %v", cfg.Name, err)
			m.failed.Add(1)
			i++
			attempt = 1
		}
		recs = recs[n:]
	}
}
