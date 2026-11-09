package fitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// A pipeline of mode once is a job: its source's snapshot read in chunks
// where the source is a Chunker, its progress kept in the checkpoint, and,
// with a schedule, started again.

// Progress is a job's, as its checkpoint keeps it and Status shows it.
type Progress struct {
	// Started is when the job began, its first run if it was resumed.
	Started time.Time `json:"started"`
	// Finished is when it ended, every record acknowledged; nil until
	// then. The next run of a finished job is a new job.
	Finished *time.Time `json:"finished,omitempty"`
	// Read counts the source's records read; after a restart, those
	// past the checkpoint count again when read again.
	Read int64 `json:"read"`
	// Written counts the source's records every sink acknowledged (one a
	// transform dropped among them), Failed those on_error skipped or
	// wrote to the dead letter sink.
	Written int64 `json:"written"`
	Failed  int64 `json:"failed"`
	// Rows is the records the source reckons its chunks hold, 0 when it
	// cannot tell; ETA, how long the rest should take at the rate of the
	// run under way, comes from it.
	Rows int64  `json:"rows,omitempty"`
	ETA  string `json:"eta,omitempty"`
	// ChunksDone counts the chunks whose every record is acknowledged.
	ChunksDone int `json:"chunks_done,omitempty"`
	// Chunks is the plan of a snapshot split in chunks.
	Chunks []Chunk `json:"chunks,omitempty"`
}

// errJobDone is a job that finished and is not to run again.
var errJobDone = errors.New("sync: the job has finished")

// start loads the checkpoint for a run, and the job's progress for a
// pipeline of mode once: a finished job's next run is a new job when again
// is true, and errJobDone otherwise. A new job of a source that splits its
// snapshot starts from its start; one of any other source from where the
// last ended, so a file or a cursor copies what is new.
func (p *pipe) start(again bool) (Position, error) {
	from, prog, err := p.store.load()
	if err != nil {
		return from, err
	}
	if !p.cfg.Once() {
		return p.sinkCheckpoint(from)
	}
	if prog != nil && prog.Finished != nil {
		if !again {
			p.noteSaved(from)
			return from, errJobDone
		}
		if len(prog.Chunks) > 0 {
			from = nil
		}
		prog = nil
	}
	if prog == nil {
		prog = &Progress{Started: p.rt.clock().Now().UTC()}
		// A new job of a Chunker plans its chunks now, so that the run
		// knows it reads a snapshot. A job begun before its source was
		// split goes on unsplit.
		if c, ok := p.src.(Chunker); ok && from == nil {
			ctx, cancel := context.WithCancel(context.Background())
			stopping := p.stopping // a restart (control.go) makes another once the loop ends
			go func() {
				select {
				case <-stopping:
					cancel()
				case <-ctx.Done():
				}
			}()
			plan, err := c.Chunks(ctx)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("planning the chunks: %w", err)
			}
			if len(plan) > MaxChunks {
				return nil, fmt.Errorf("the source planned %d chunks, past the %d allowed", len(plan), MaxChunks)
			}
			prog.Chunks = plan
			for _, c := range plan {
				prog.Rows += c.Rows
			}
		}
		// The new job is saved at once, its plan with it, so that one
		// interrupted before its first checkpoint is known to be. One
		// from the source's start is of a lineage of its own.
		if from == nil {
			p.store.lineage = newLineage()
		}
		if err := p.store.save(from, prog); err != nil {
			return nil, err
		}
	}
	p.mu.Lock()
	p.job = prog
	p.mu.Unlock()
	if len(prog.Chunks) > 0 {
		return from, nil
	}
	return p.sinkCheckpoint(from)
}

// interrupted reports whether the pipeline's checkpoint is of a job not
// finished, which a schedule resumes at once.
func (p *pipe) interrupted() bool {
	from, prog, err := p.store.load()
	return err != nil || prog != nil && prog.Finished == nil || prog == nil && from != nil
}

// jobChunks returns the job's plan.
func (p *pipe) jobChunks() []Chunk {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == nil {
		return nil
	}
	return p.job.Chunks
}

// progress returns the job's progress as of now, or of r, the run under way;
// nil for a pipeline that is not a job.
func (p *pipe) progress(r *run, at *markState, finished bool) *Progress {
	p.mu.Lock()
	if p.job == nil {
		p.mu.Unlock()
		return nil
	}
	prog := *p.job
	p.mu.Unlock()
	if r == nil {
		return &prog
	}
	r.mu.Lock()
	if at == nil {
		at = &markState{r.mark, r.markDone, r.markFailed}
	}
	prog.Read, prog.Written, prog.Failed = r.read, at.done-at.failed, at.failed
	done, rate := at.done, float64(at.done-r.startDone)/time.Since(r.started).Seconds()
	mark := at.pos
	r.mu.Unlock()
	if left := prog.Rows - done; prog.Rows > 0 && left > 0 && rate > 0 {
		prog.ETA = time.Duration(float64(left) / rate * float64(time.Second)).Round(time.Second).String()
	} else {
		prog.ETA = ""
	}
	prog.ChunksDone = 0
	if len(prog.Chunks) > 0 {
		var cp chunkPosition
		if mark != nil && json.Unmarshal(mark, &cp) == nil {
			prog.ChunksDone = countRanges(cp.Done)
		}
	}
	if finished {
		now := p.rt.clock().Now().UTC()
		prog.Finished, prog.ETA, prog.ChunksDone = &now, "", len(prog.Chunks)
	}
	return &prog
}

// markState is a run's mark and the records done and failed up to it, taken
// together, so that a checkpoint's counts are its position's.
type markState struct {
	pos          Position
	done, failed int64
}

// chunkPosition is a chunked job's position: the chunks done, as ranges of
// their indexes ("0-41,43"), and where each chunk under way is at.
type chunkPosition struct {
	Done string    `json:"done"`
	At   []chunkAt `json:"at,omitempty"`
}

type chunkAt struct {
	Chunk int             `json:"chunk"`
	From  json.RawMessage `json:"from"`
}

// positionEvery is how many records a chunked job emits between two that
// carry its position, besides each chunk's last.
const positionEvery = 100

// chunkRun reads a job's chunks, several at once.
type chunkRun struct {
	c     Chunker
	plan  []Chunk
	r     *Run
	mu    sync.Mutex
	done  []bool
	at    map[int]Position
	next  int
	since int
}

// runChunks is the source of a job whose source is a Chunker: it reads the
// chunks start planned, or the checkpoint holds, parallel at a time, each
// record of a chunk handed on only once the next is read, so that a chunk's
// last carries the position saying the chunk is done. A job with no chunks
// is read whole.
func (p *pipe) runChunks(ctx context.Context, c Chunker, r *Run, parallel int) error {
	plan := p.jobChunks()
	if len(plan) == 0 {
		return c.Run(ctx, r)
	}
	cr := &chunkRun{c: c, plan: plan, r: r, done: make([]bool, len(plan)), at: map[int]Position{}}
	if err := cr.resume(r.From); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for g := range min(parallel, len(plan)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i, from, ok := cr.take()
				if !ok {
					return
				}
				if err := cr.read(ctx, i, from, g+1); err != nil {
					once.Do(func() { first = err })
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	return first
}

// resume takes the chunks done, and where those under way are, from pos.
func (cr *chunkRun) resume(pos Position) error {
	if pos == nil {
		return nil
	}
	var cp chunkPosition
	if err := json.Unmarshal(pos, &cp); err != nil {
		return fmt.Errorf("the job's checkpoint %s: %w", pos, err)
	}
	bad := false
	forRanges(cp.Done, func(i int) {
		if i < 0 || i >= len(cr.done) {
			bad = true
			return
		}
		cr.done[i] = true
	})
	for _, a := range cp.At {
		if a.Chunk < 0 || a.Chunk >= len(cr.done) {
			bad = true
			continue
		}
		cr.at[a.Chunk] = Position(a.From)
	}
	if bad {
		return fmt.Errorf("the job's checkpoint %s names chunks past the %d planned", pos, len(cr.plan))
	}
	return nil
}

// take returns the next chunk to read, and where from.
func (cr *chunkRun) take() (int, Position, bool) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	for cr.next < len(cr.plan) && cr.done[cr.next] {
		cr.next++
	}
	if cr.next == len(cr.plan) {
		return 0, nil, false
	}
	i := cr.next
	cr.next++
	return i, cr.at[i], true
}

// read reads chunk i from from, holding each record back until the next;
// its records go to the sinks' writer lane, the reader's, which a key read
// once keeps.
func (cr *chunkRun) read(ctx context.Context, i int, from Position, lane int) error {
	var held *Record
	err := cr.c.ReadChunk(ctx, cr.plan[i], from, func(rec *Record) error {
		if held != nil {
			if err := cr.pass(i, held, false); err != nil {
				return err
			}
		}
		rec.Tx = Tx{}
		rec.lane = lane
		held = rec
		return nil
	})
	if err != nil {
		return err
	}
	return cr.pass(i, held, true)
}

// pass emits rec, of chunk i, its last when last is true, rec nil when the
// chunk had none, with the job's position when it is due. Emitting under
// the lock keeps the positions in the order of the records.
func (cr *chunkRun) pass(i int, rec *Record, last bool) error {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if last {
		cr.done[i] = true
		delete(cr.at, i)
	} else if rec.Position != nil {
		cr.at[i] = rec.Position
	}
	if rec == nil {
		return nil
	}
	if cr.since++; last || cr.since >= positionEvery {
		rec.Position, cr.since = cr.position(), 0
	} else {
		rec.Position = nil
	}
	return cr.r.Emit(rec)
}

// position returns the job's position as of the records emitted so far.
func (cr *chunkRun) position() Position {
	b := append([]byte(nil), `{"done":"`...)
	b = appendRanges(b, cr.done)
	b = append(b, '"')
	if len(cr.at) > 0 {
		b = append(b, `,"at":[`...)
		for k, i := range slices.Sorted(maps.Keys(cr.at)) {
			if k > 0 {
				b = append(b, ',')
			}
			b = strconv.AppendInt(append(b, `{"chunk":`...), int64(i), 10)
			b = append(append(append(b, `,"from":`...), cr.at[i]...), '}')
		}
		b = append(b, ']')
	}
	return Position(append(b, '}'))
}

// appendRanges appends the indexes set in done as ranges: 0-41,43.
func appendRanges(b []byte, done []bool) []byte {
	first := true
	for i := 0; i < len(done); i++ {
		if !done[i] {
			continue
		}
		j := i
		for j+1 < len(done) && done[j+1] {
			j++
		}
		if !first {
			b = append(b, ',')
		}
		first = false
		b = strconv.AppendInt(b, int64(i), 10)
		if j > i {
			b = strconv.AppendInt(append(b, '-'), int64(j), 10)
		}
		i = j
	}
	return b
}

// forRanges calls f with each index ranges names; an index it cannot read
// is -1.
func forRanges(s string, f func(int)) {
	if s == "" {
		return
	}
	for part := range strings.SplitSeq(s, ",") {
		a, b, isRange := strings.Cut(part, "-")
		lo, err1 := strconv.Atoi(a)
		hi, err2 := lo, error(nil)
		if isRange {
			hi, err2 = strconv.Atoi(b)
		}
		if err1 != nil || err2 != nil || hi < lo || hi-lo > MaxChunks {
			f(-1)
			return
		}
		for i := lo; i <= hi; i++ {
			f(i)
		}
	}
}

func countRanges(s string) int {
	n := 0
	forRanges(s, func(int) { n++ })
	return n
}

// laneOf returns the writer of n a record goes to: by its key's hash, so
// that a key always goes to the same one, or by its sequence when it has
// no key.
func laneOf(rec *Record, n int, buf *[]byte) int {
	if n == 1 {
		return 0
	}
	if rec.Key.IsNull() {
		return int(rec.seq % uint64(n))
	}
	b, err := rec.Key.AppendJSON((*buf)[:0])
	*buf = b
	if err != nil {
		return 0
	}
	h := uint64(14695981039346656037) // FNV-1a
	for _, c := range b {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return int(h % uint64(n))
}

// Clock is the time a Runtime's schedules go by; tests give a fake one.
type Clock interface {
	Now() time.Time
	// After returns a channel that receives once d has passed, and a
	// function that stops it.
	After(d time.Duration) (<-chan time.Time, func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) After(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

func (rt *Runtime) clock() Clock {
	if rt.opts.Clock != nil {
		return rt.opts.Clock
	}
	return realClock{}
}

// wait waits for the schedule's next time, and reports false when the
// pipeline is stopped first, or the schedule names no time to come.
func (p *pipe) wait() bool {
	sched, loc, err := p.cfg.Schedule()
	if err != nil || sched == nil {
		return false
	}
	clock := p.rt.clock()
	now := clock.Now()
	next := sched.Next(now, loc)
	if next.IsZero() {
		p.rt.opts.Log.Warnf("pipeline %s: its schedule %q names no time in the next five years", p.cfg.Name, p.cfg.Job.Schedule)
		<-p.stopping
		return false
	}
	p.mu.Lock()
	p.next, p.state = next, "scheduled"
	p.mu.Unlock()
	p.rt.opts.Log.Infof("pipeline %s waits for its schedule: it runs next at %s", p.cfg.Name, next.Format(time.RFC3339))
	c, stop := clock.After(next.Sub(now))
	select {
	case <-c:
		return true
	case <-p.stopping:
		stop()
		return false
	}
}

// RunPipeline runs the pipeline of s named name, on its own, until it ends,
// as fit sync run does: a job finished before starts anew, its schedule is
// not waited for, and a source failing past the pipeline's attempts fails
// it rather than restarting it for ever. When ctx ends the pipeline is
// stopped, writing what it holds and its checkpoint first, within the
// options' StopTimeout. It returns the pipeline's state at its end.
func RunPipeline(ctx context.Context, s *config.Sync, name string, opts Options) (Status, error) {
	if s == nil {
		return Status{}, errors.New("the config has no sync")
	}
	i := slices.IndexFunc(s.Pipelines, func(p config.SyncPipeline) bool { return p.Name == name })
	if i < 0 {
		return Status{}, fmt.Errorf("the config has no pipeline %q", name)
	}
	cfg := &s.Pipelines[i]
	m, err := load(cfg, &module.Env{Engine: opts.Engine})
	if err != nil {
		return Status{}, err
	}
	rt := NewRuntime(opts)
	rt.oneRun = true
	p := rt.newPipe(s.StateDir, cfg, m)
	go p.loop()
	select {
	case <-p.done:
		p.modules.cleanup()
	case <-ctx.Done():
		rt.stopPipe(context.Background(), p)
	}
	return p.status(), nil
}
