// Package syncfile is sync's files: the source file, which tails files as
// Filebeat does, following rotation by rename and by copy and truncation,
// and the sink file, which writes records as lines through fit's log file
// sink, rotated and kept as the logs are. See docs/decisions/036-sync.md.
package syncfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".file", New: func() any { return new(Source) },
		Doc: "Tails files as Filebeat does: the paths' globs are scanned again each scan_interval, each file known by its device, inode and a fingerprint of its head, so a renamed file is followed, a truncated one read again from its start, a copy taken before a truncation read on from where the original was, and a deleted one finished first. Each line, or each multiline event, goes through the codec into records; the checkpoint holds every file's identity and offset."})
	module.Register(module.Info{ID: fitsync.Sinks + ".file", New: func() any { return new(Sink) },
		Doc: "Writes records as lines to a file through fit's log file sink, by a codec or a template, rotated, kept and compressed as rotate says, as the logs are; a batch is acknowledged once it is in the file and synced."})
}

// The defaults of the source's params.
const (
	DefaultScanInterval     = 10 * time.Second
	DefaultPollInterval     = 250 * time.Millisecond
	DefaultCloseAfter       = time.Minute
	DefaultMaxLineBytes     = 1 << 20
	DefaultMaxOpenFiles     = 256
	DefaultMaxFiles         = 100000
	DefaultFingerprintBytes = 1024
	DefaultMultilineLines   = 500
	// readBudget is the most a file is read in one round before the others
	// get their turn.
	readBudget = 4 << 20
	// commitEvery is the most records between two positions.
	commitEvery = 1000
	// maxGhosts bounds the truncated files' heads kept for their copies.
	maxGhosts = 1024
)

var errNotRegular = errors.New("not a regular file")

// Source is the source file.
type Source struct {
	Paths            []string          `json:"paths" fit:"required" doc:"The files to read: paths or globs (*, ? and [...] within one path element), matched again each scan_interval, so new files are found."`
	ScanInterval     config.Duration   `json:"scan_interval,omitempty" default:"\"10s\"" doc:"How often the globs are matched again, to find new, renamed, truncated and deleted files. 10s when left out."`
	PollInterval     config.Duration   `json:"poll_interval,omitempty" default:"\"250ms\"" doc:"How long the source waits, once every open file is read to its end, before reading them again. 250ms when left out."`
	CloseAfter       config.Duration   `json:"close_after,omitempty" default:"\"1m\"" doc:"How long a file may go without growing before its partial last line (or multiline event) is taken as it is, and the file closed; a closed file is opened again when a scan finds it grown. 1m when left out."`
	MaxLineBytes     int               `json:"max_line_bytes,omitempty" min:"0" max:"67108864" default:"1048576" doc:"The longest line, or multiline event, in bytes: a longer one is cut there, its rest skipped, and counted. 1 MiB when left out or 0."`
	Multiline        *Multiline        `json:"multiline,omitempty" doc:"Joins lines into events: a line matching start begins an event, and the lines after it, up to the next, belong to it, as a stack trace does. Each line is a record of its own when left out."`
	MaxOpenFiles     int               `json:"max_open_files,omitempty" min:"0" max:"65536" default:"256" doc:"The most files open at once: past it, a file read to its end is closed to open another. 256 when left out or 0."`
	MaxFiles         int               `json:"max_files,omitempty" min:"0" max:"10000000" default:"100000" doc:"The most files known at once, each in the checkpoint; files found past it are not read, and a warning says so. 100000 when left out or 0."`
	FingerprintBytes int               `json:"fingerprint_bytes,omitempty" min:"0" max:"65536" default:"1024" doc:"How many bytes of a file's head its fingerprint covers: a file whose device and inode are known but whose head differs is a new one, and a new file whose head matches a file just truncated is its copy, read on from where the original was. 1024 when left out or 0."`
	Codec            *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes records of a line or an event: line when left out, a message record holding it as after's line."`

	codec fitsync.Codec
	start *regexp.Regexp
	cut   atomic.Int64
	// peak is the most files open at once, for tests.
	peak atomic.Int64
}

// Multiline is how the source joins lines into events.
type Multiline struct {
	Start    string `json:"start" fit:"required" doc:"A regular expression (RE2) a line beginning an event matches."`
	MaxLines int    `json:"max_lines,omitempty" min:"0" max:"100000" default:"500" doc:"The most lines of an event: the line past it begins another. 500 when left out or 0."`
}

// Provision loads the codec and compiles the patterns.
func (s *Source) Provision(ctx *module.Context) error {
	for i, p := range s.Paths {
		if _, err := filepath.Match(p, ""); err != nil || p == "" {
			return ctx.Params.Field("paths").Index(i).Errorf("%q is not a path or a glob", p)
		}
	}
	if m := s.Multiline; m != nil {
		re, err := regexp.Compile(m.Start)
		if err != nil {
			return ctx.Params.Field("multiline").Field("start").Errorf("%v", err)
		}
		s.start = re
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// loadCodec loads a codec ref, line when there is none.
func loadCodec(ctx *module.Context, ref *config.ModuleRef) (fitsync.Codec, error) {
	if ref == nil {
		return &fitsync.LineCodec{}, nil
	}
	v, err := ctx.Load(fitsync.Codecs, *ref)
	if err != nil {
		return nil, err
	}
	c, ok := v.(fitsync.Codec)
	if !ok {
		module.Cleanup(v)
		return nil, ref.At.Field("module").Errorf("%s is not a codec", ref.Module)
	}
	return c, nil
}

// Cleanup lets the codec go.
func (s *Source) Cleanup() { module.Cleanup(s.codec) }

// LinesCut returns how many lines or events were cut at max_line_bytes.
func (s *Source) LinesCut() int64 { return s.cut.Load() }

func dur(d config.Duration, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d.Std()
}

func orInt(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

func (s *Source) maxLine() int { return orInt(s.MaxLineBytes, DefaultMaxLineBytes) }
func (s *Source) fpBytes() int { return orInt(s.FingerprintBytes, DefaultFingerprintBytes) }

// fileID is a file's identity: its device and inode, or what stands for
// them where there are none.
type fileID struct{ dev, ino uint64 }

// tracked is a file the source knows.
type tracked struct {
	id    fileID
	fp    uint64
	fpLen int
	path  string
	// key names the file in its records' keys: its identity and a hash of
	// its first line, or those of the file it is a copy of.
	key string
	// off is what records were made of, or skipped: the checkpoint's offset.
	// read is where reading goes on: off and the multiline event held.
	off, read int64
	// skip says the rest of a line cut at max_line_bytes is skipped.
	skip  bool
	event []byte
	lines int
	cut   bool

	f      *os.File
	size   int64
	active time.Time
	idle   bool
	gone   bool
	forget bool
	// copyOf is the file this one is a copy of, held until that file is
	// truncated, or close_after passes.
	copyOf    *tracked
	copySince time.Time
	head      []byte
}

// ghost is the head, offset and key of a file found truncated, for its
// copy, which goes on from there under the same key, so that a line read
// again after a crash has the key it had.
type ghost struct {
	fp    uint64
	fpLen int
	off   int64
	key   string
	at    time.Time
}

// tail is one Run of the source.
type tail struct {
	s      *Source
	r      *fitsync.Run
	files  map[fileID]*tracked
	list   []*tracked
	ghosts []ghost
	opened []*tracked // the files open
	buf    []byte
	hbuf   []byte
	held   *fitsync.Record
	since  int
	full   bool
	log    *logs.Logger
}

// Run reads the files from the checkpoint on.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	t := &tail{s: s, r: r, files: map[fileID]*tracked{}, buf: make([]byte, max(s.maxLine()+1, 64<<10)),
		hbuf: make([]byte, s.fpBytes()),
		log:  logs.Named(config.LoggerSync)}
	defer t.closeAll()
	if r.From != nil {
		if err := t.restore(r.From); err != nil {
			return err
		}
	}
	scanEvery, poll := dur(s.ScanInterval, DefaultScanInterval), dur(s.PollInterval, DefaultPollInterval)
	var nextScan time.Time
	for {
		now := time.Now()
		if !now.Before(nextScan) {
			t.scan(now)
			nextScan = now.Add(scanEvery)
		}
		busy, err := t.round(ctx, now)
		if err == nil {
			err = t.commit()
		}
		if err != nil {
			return err
		}
		if r.Once && !busy && t.caughtUp() {
			return nil
		}
		if !busy {
			tm := time.NewTimer(min(poll, max(time.Until(nextScan), 0)))
			select {
			case <-tm.C:
			case <-ctx.Done():
				tm.Stop()
				return ctx.Err()
			}
		}
	}
}

func hash(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

// head reads the first n bytes of f.
func (t *tail) head(f *os.File, n int) ([]byte, bool) {
	b := t.hbuf[:n]
	m, err := f.ReadAt(b, 0)
	return b, m == n && (err == nil || err == io.EOF)
}

// headMatches reports whether the file f now reads, fh or opened at path,
// still starts with f's fingerprint.
func (t *tail) headMatches(f *tracked, fh *os.File, path string) bool {
	if f.fpLen == 0 {
		return true
	}
	if fh == nil {
		var err error
		if fh, err = openRead(path); err != nil {
			return true // looked at again at the next scan
		}
		defer fh.Close()
		if id, _, err := fileIDOf(fh); err != nil || id != f.id {
			return true
		}
	}
	b, ok := t.head(fh, f.fpLen)
	return ok && hash(b) == f.fp
}

// reset starts f again from its beginning: it was truncated, or its
// identity is a new file's. Its head is kept a while, for a copy of it.
func (t *tail) reset(f *tracked, now time.Time) {
	if f.fpLen > 0 && f.off > 0 {
		if len(t.ghosts) == maxGhosts {
			t.ghosts = t.ghosts[1:]
		}
		t.ghosts = append(t.ghosts, ghost{fp: f.fp, fpLen: f.fpLen, off: f.off, key: f.key, at: now})
	}
	t.log.Infof("sync file: %s was truncated or replaced at offset %d; reading it from its start", f.path, f.off)
	f.key, f.fp, f.fpLen, f.off, f.read, f.skip, f.event, f.lines, f.cut = "", 0, 0, 0, 0, false, nil, 0, false
	f.active = now
}

// scan matches the globs: files found are known by their identity, those
// truncated or replaced start again, and those gone are forgotten once
// closed.
func (t *tail) scan(now time.Time) {
	type found struct {
		path string
		id   fileID
		size int64
	}
	var news []found
	seen := map[fileID]bool{}
	for _, pat := range t.s.Paths {
		matches, _ := filepath.Glob(pat)
		for _, p := range matches {
			id, size, err := statID(p)
			if err != nil || seen[id] {
				continue
			}
			seen[id] = true
			f := t.files[id]
			if f == nil {
				news = append(news, found{p, id, size})
				continue
			}
			f.path, f.gone = p, false
			if f.f == nil { // an open file is checked as it is read
				if size < f.read || size != f.size && !t.headMatches(f, nil, p) {
					t.reset(f, now)
				}
				f.size = size
			}
		}
	}
	closeAfter := dur(t.s.CloseAfter, DefaultCloseAfter)
	for _, f := range t.list {
		switch {
		case seen[f.id]:
		case f.f == nil:
			f.forget = true
		default:
			f.gone = true
		}
		if x := f.copyOf; x != nil {
			if g, ok := t.ghostOf(f.head); ok {
				f.off, f.read, f.copyOf, f.head = min(g.off, f.size), min(g.off, f.size), nil, nil
				f.key = g.key
			} else if x.forget || now.Sub(f.copySince) >= closeAfter {
				f.copyOf, f.head = nil, nil // not a copy after all: read whole
			}
		}
	}
	want := t.s.fpBytes()
	for _, n := range news {
		if len(t.list) >= orInt(t.s.MaxFiles, DefaultMaxFiles) {
			if !t.full {
				t.full = true
				t.log.Warnf("sync file: max_files %d reached; %s and later files are not read", len(t.list), n.path)
			}
			break
		}
		f := &tracked{id: n.id, path: n.path, size: n.size, active: now}
		var head []byte
		if n.size > 0 {
			if fh, err := openRead(n.path); err == nil {
				if b, ok := t.head(fh, int(min(int64(want), n.size))); ok {
					head = b
					f.fp, f.fpLen = hash(b), len(b)
				}
				fh.Close()
			}
		}
		if g, ok := t.ghostOf(head); ok {
			f.off, f.read = min(g.off, n.size), min(g.off, n.size)
			f.key = g.key
		} else {
			for _, x := range t.list {
				if !x.forget && x.copyOf == nil && headIs(head, x.fp, x.fpLen) {
					f.copyOf, f.copySince, f.head = x, now, append([]byte(nil), head...)
					break
				}
			}
		}
		t.files[n.id] = f
		t.list = append(t.list, f)
	}
	kept := t.ghosts[:0]
	for _, g := range t.ghosts {
		if now.Sub(g.at) < closeAfter {
			kept = append(kept, g)
		}
	}
	t.ghosts = kept
	t.prune()
}

// headIs reports whether head starts with the fpLen bytes whose hash is fp.
func headIs(head []byte, fp uint64, fpLen int) bool {
	return fpLen > 0 && fpLen <= len(head) && hash(head[:fpLen]) == fp
}

// ghostOf takes the ghost head starts as.
func (t *tail) ghostOf(head []byte) (ghost, bool) {
	for i, g := range t.ghosts {
		if headIs(head, g.fp, g.fpLen) {
			t.ghosts = append(t.ghosts[:i], t.ghosts[i+1:]...)
			return g, true
		}
	}
	return ghost{}, false
}

// prune forgets the files marked so.
func (t *tail) prune() {
	kept := t.list[:0]
	for _, f := range t.list {
		if f.forget {
			delete(t.files, f.id)
			for _, c := range t.list {
				if c.copyOf == f {
					c.copyOf = nil
				}
			}
			continue
		}
		kept = append(kept, f)
	}
	clear(t.list[len(kept):])
	t.list = kept
}

// round reads each file that has something to read, and reports whether
// any had.
func (t *tail) round(ctx context.Context, now time.Time) (bool, error) {
	busy := false
	for _, f := range t.list {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if f.forget || f.copyOf != nil || f.f == nil && (f.size <= f.read || !t.openFile(f, now)) {
			continue
		}
		n, err := t.readFile(f, now)
		if err != nil {
			return false, err
		}
		busy = busy || n > 0
	}
	t.prune()
	return busy, nil
}

// openFile opens f, closing an idle file first when max_open_files are
// open, and reports whether it did.
func (t *tail) openFile(f *tracked, now time.Time) bool {
	if len(t.opened) >= orInt(t.s.MaxOpenFiles, DefaultMaxOpenFiles) {
		var victim *tracked
		for _, o := range t.opened {
			if o.idle && (victim == nil || o.gone && !victim.gone || o.gone == victim.gone && o.active.Before(victim.active)) {
				victim = o
			}
		}
		if victim == nil {
			return false
		}
		if victim.gone {
			if err := t.final(victim); err != nil {
				return false
			}
		}
		t.closeFile(victim)
	}
	fh, err := openRead(f.path)
	if err != nil {
		return false
	}
	if id, _, err := fileIDOf(fh); err != nil || id != f.id {
		fh.Close()
		return false
	}
	f.f, f.active, f.idle = fh, now, false
	t.opened = append(t.opened, f)
	if n := int64(len(t.opened)); n > t.s.peak.Load() {
		t.s.peak.Store(n)
	}
	return true
}

// closeFile closes f; what it held of an event is read again when it is
// opened again, and a file gone is forgotten.
func (t *tail) closeFile(f *tracked) {
	if f.f == nil {
		return
	}
	f.f.Close()
	f.f = nil
	for i, o := range t.opened {
		if o == f {
			t.opened[i] = t.opened[len(t.opened)-1]
			t.opened = t.opened[:len(t.opened)-1]
			break
		}
	}
	if f.lines > 0 {
		f.read, f.event, f.lines, f.cut, f.skip = f.off, nil, 0, false, false
	}
	if f.gone {
		f.forget = true
	}
}

func (t *tail) closeAll() {
	for _, f := range t.list {
		t.closeFile(f)
	}
}

// readFile reads what f has, up to a budget, and returns how much it read.
func (t *tail) readFile(f *tracked, now time.Time) (int, error) {
	_, size, err := fileIDOf(f.f)
	if err != nil {
		t.closeFile(f)
		return 0, nil
	}
	if size < f.read || size != f.size && !t.headMatches(f, f.f, "") {
		t.reset(f, now)
	} else if size > f.size {
		f.active = now // growing, if only a partial line
	}
	f.size = size
	if want := t.s.fpBytes(); f.fpLen < want && size > int64(f.fpLen) {
		if b, ok := t.head(f.f, int(min(int64(want), size))); ok {
			f.fp, f.fpLen = hash(b), len(b)
		}
	}
	total, stalled := 0, false
	for f.read < f.size && total < readBudget {
		n, err := f.f.ReadAt(t.buf[:min(int64(len(t.buf)), f.size-f.read)], f.read)
		if n == 0 {
			if err != nil && err != io.EOF {
				t.log.Warnf("sync file: read %s: %v", f.path, err)
			}
			stalled = true
			break
		}
		used, err := t.lines(f, t.buf[:n], false)
		if err != nil {
			return total, err
		}
		if used == 0 {
			stalled = true
			break
		}
		total += used
	}
	if total > 0 {
		f.active = now
	}
	inactive := now.Sub(f.active) >= dur(t.s.CloseAfter, DefaultCloseAfter)
	held := f.read < f.size || f.lines > 0
	if held && (inactive || t.r.Once && (stalled || f.read >= f.size)) {
		if err := t.final(f); err != nil {
			return total, err
		}
		total++
	}
	f.idle = f.read >= f.size
	if f.idle && f.lines == 0 && inactive && !t.r.Once {
		t.closeFile(f)
	}
	return total, nil
}

// final takes what f holds past its last line as its last line, and the
// event held as complete: the file has ended, or stopped growing.
func (t *tail) final(f *tracked) error {
	n, _ := f.f.ReadAt(t.buf[:min(int64(len(t.buf)), max(f.size-f.read, 0))], f.read)
	_, err := t.lines(f, t.buf[:n], true)
	return err
}

// keyOf returns the file's part of its records' keys: its device, inode
// and a hash of its first line (up to fingerprint_bytes), which tells the
// file's contents before and after a truncation apart, and is the same
// when the file is read again after a crash.
func (t *tail) keyOf(f *tracked) string {
	if f.key == "" {
		var first []byte
		if f.f != nil {
			b := t.hbuf[:min(int64(len(t.hbuf)), f.size)]
			n, _ := f.f.ReadAt(b, 0)
			first = b[:n]
			if i := bytes.IndexByte(first, '\n'); i >= 0 {
				first = first[:i]
			}
		}
		f.key = fmt.Sprintf("%d:%d:%x", f.id.dev, f.id.ino, hash(first))
	}
	return f.key
}

// advance moves past n bytes read; off follows when no event is held.
func (t *tail) advance(f *tracked, n int) {
	f.read += int64(n)
	if f.lines == 0 {
		f.off = f.read
	}
}

// lines makes records of the whole lines data holds, data being f's bytes
// from f.read on, and returns how many bytes it used. final takes a last
// line without its newline, and the event held, as complete.
func (t *tail) lines(f *tracked, data []byte, final bool) (int, error) {
	maxLine := t.s.maxLine()
	i := 0
	for i < len(data) {
		rest := data[i:]
		j := bytes.IndexByte(rest, '\n')
		if f.skip {
			n := len(rest)
			if j >= 0 {
				n, f.skip = j+1, false
			}
			t.advance(f, n)
			i += n
			continue
		}
		var line []byte
		n, cut := 0, false
		switch {
		case j >= 0:
			line, n = rest[:j], j+1
			if len(line) > maxLine {
				line, cut = line[:maxLine], true
			}
		case len(rest) > maxLine:
			line, n, cut = rest[:maxLine], maxLine, true
			f.skip = true
		case final:
			line, n = rest, len(rest)
		default:
			return i, nil
		}
		if cut {
			t.cutOne(f, "a line")
		}
		if err := t.line(f, bytes.TrimSuffix(line, []byte{'\r'}), n); err != nil {
			return i, err
		}
		i += n
	}
	if final && f.lines > 0 {
		return i, t.flushEvent(f)
	}
	return i, nil
}

// cutOne counts a line or event cut, and says so for the first and every
// thousandth.
func (t *tail) cutOne(f *tracked, what string) {
	if n := t.s.cut.Add(1); n == 1 || n%1000 == 0 {
		t.log.Warnf("sync file: %s of %s at offset %d is longer than max_line_bytes %d, and was cut (%d so far)", what, f.path, f.read, t.s.maxLine(), n)
	}
}

// line takes a line of n bytes at f.read: a unit of its own, or part of
// a multiline event.
func (t *tail) line(f *tracked, line []byte, n int) error {
	if t.s.start == nil {
		start := f.read
		t.advance(f, n)
		return t.unit(f, line, start)
	}
	isStart := t.s.start.Match(line)
	if f.lines > 0 && (isStart || f.lines >= orInt(t.s.Multiline.MaxLines, DefaultMultilineLines)) {
		if err := t.flushEvent(f); err != nil {
			return err
		}
	}
	if f.lines > 0 {
		f.event = append(f.event, '\n')
	}
	if room := t.s.maxLine() - len(f.event); len(line) > room {
		line = line[:max(room, 0)]
		if !f.cut {
			f.cut = true
			t.cutOne(f, "an event")
		}
	}
	f.event = append(f.event, line...)
	f.lines++
	f.read += int64(n)
	return nil
}

// flushEvent makes records of the event held.
func (t *tail) flushEvent(f *tracked) error {
	start := f.off
	data := f.event
	f.off, f.lines, f.cut, f.event = f.read, 0, false, f.event[:0]
	return t.unit(f, bytes.TrimSuffix(data, []byte{'\n'}), start)
}

// unit makes records of a line or event that began at start, f's state
// already past it.
func (t *tail) unit(f *tracked, data []byte, start int64) error {
	var recs []*fitsync.Record
	if err := t.s.codec.Decode(data, func(r *fitsync.Record) { recs = append(recs, r) }); err != nil {
		recs = []*fitsync.Record{{Op: fitsync.OpMessage, After: fitsync.Map(
			fitsync.Field{Name: "line", Value: fitsync.String(string(data))},
			fitsync.Field{Name: "error", Value: fitsync.String(err.Error())})}}
	}
	key := t.keyOf(f)
	for i, r := range recs {
		r.Source, r.Namespace, r.Position, r.Tx = "file", f.path, nil, fitsync.Tx{}
		if r.Key.IsNull() {
			fs := []fitsync.Field{{Name: "file", Value: fitsync.String(key)}, {Name: "offset", Value: fitsync.Int(start)}}
			if i > 0 {
				fs = append(fs, fitsync.Field{Name: "n", Value: fitsync.Int(int64(i))})
			}
			r.Key = fitsync.Map(fs...)
		}
		if t.held != nil {
			if err := t.r.Emit(t.held); err != nil {
				return err
			}
		}
		t.held = r
		t.since++
	}
	if t.since >= commitEvery {
		return t.commit()
	}
	return nil
}

// commit hands on the record held with the position of every file, as the
// source is once the record is read.
func (t *tail) commit() error {
	if t.held == nil {
		return nil
	}
	pos, err := t.position()
	if err != nil {
		return err
	}
	r := t.held
	r.Position, t.held, t.since = pos, nil, 0
	return t.r.Emit(r)
}

// caughtUp reports whether every file is read to its end.
func (t *tail) caughtUp() bool {
	for _, f := range t.list {
		if f.copyOf == nil && (f.read < f.size || f.lines > 0) {
			return false
		}
	}
	return true
}

// position is the checkpoint's form.
type position struct {
	Files []posFile `json:"files"`
	// Ghosts are the heads of files truncated lately, so that a copy found
	// after a restart is still read on from where its original was.
	Ghosts []posGhost `json:"ghosts,omitempty"`
}

type posGhost struct {
	FP    string `json:"fp"`
	FPLen int    `json:"fp_len"`
	Off   int64  `json:"off"`
	Key   string `json:"key"`
}

type posFile struct {
	Dev   uint64 `json:"dev"`
	Ino   uint64 `json:"ino"`
	FP    string `json:"fp,omitempty"`
	FPLen int    `json:"fp_len,omitempty"`
	Path  string `json:"path"`
	Off   int64  `json:"off"`
	Skip  bool   `json:"skip,omitempty"`
	Key   string `json:"key,omitempty"`
}

// position returns every file's identity and offset; files not read yet,
// and copies held, are left out, as they are found again the same way.
func (t *tail) position() (fitsync.Position, error) {
	p := position{Files: []posFile{}}
	for _, f := range t.list {
		if f.copyOf != nil || f.off == 0 && !f.skip {
			continue
		}
		p.Files = append(p.Files, posFile{Dev: f.id.dev, Ino: f.id.ino, FP: strconv.FormatUint(f.fp, 16),
			FPLen: f.fpLen, Path: f.path, Off: f.off, Skip: f.skip && f.lines == 0, Key: f.key})
	}
	for _, g := range t.ghosts {
		p.Ghosts = append(p.Ghosts, posGhost{FP: strconv.FormatUint(g.fp, 16), FPLen: g.fpLen, Off: g.off, Key: g.key})
	}
	return json.Marshal(p)
}

// restore takes the files a checkpoint holds.
func (t *tail) restore(from fitsync.Position) error {
	var p position
	if err := json.Unmarshal(from, &p); err != nil {
		return fmt.Errorf("file: the checkpoint: %w", err)
	}
	now := time.Now()
	for _, g := range p.Ghosts {
		if fp, err := strconv.ParseUint(g.FP, 16, 64); err == nil && len(t.ghosts) < maxGhosts {
			t.ghosts = append(t.ghosts, ghost{fp: fp, fpLen: g.FPLen, off: g.Off, key: g.Key, at: now})
		}
	}
	for _, pf := range p.Files {
		fp, err := strconv.ParseUint(pf.FP, 16, 64)
		if err != nil || pf.Off < 0 {
			return fmt.Errorf("file: the checkpoint's %s: a bad fingerprint or offset", pf.Path)
		}
		id := fileID{pf.Dev, pf.Ino}
		if t.files[id] != nil {
			continue
		}
		f := &tracked{id: id, fp: fp, fpLen: pf.FPLen, path: pf.Path, off: pf.Off, read: pf.Off, skip: pf.Skip, size: -1, key: pf.Key}
		t.files[id] = f
		t.list = append(t.list, f)
	}
	return nil
}
