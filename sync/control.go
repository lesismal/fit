package fitsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// What the Admin API and fit sync do with the running pipelines (I188):
// their state, pause and resume, a new snapshot, the checkpoint shown, set
// and reset, a test of the connectors' connections, and a dry run.

// The errors of the Runtime's controls.
var (
	// ErrNoPipeline is a pipeline the runtime does not run.
	ErrNoPipeline = errors.New("no such pipeline")
	// ErrConfirm is a change of the checkpoint that would skip or replay
	// records, asked without confirm.
	ErrConfirm = errors.New("it would skip or replay records: confirm it")
)

// registry is the runtimes of the sync roles running, by the engine the
// Admin API names, as the streaming role's channels are found.
var (
	regMu    sync.Mutex
	registry atomic.Pointer[map[*fib.Engine]*Runtime]
)

// Register makes rt the runtime Lookup(engine) returns.
func Register(engine *fib.Engine, rt *Runtime) {
	swapRuntime(engine, func(*Runtime) *Runtime { return rt })
}

// Unregister forgets engine's runtime if it is rt.
func Unregister(engine *fib.Engine, rt *Runtime) {
	swapRuntime(engine, func(cur *Runtime) *Runtime {
		if cur == rt {
			return nil
		}
		return cur
	})
}

func swapRuntime(engine *fib.Engine, next func(*Runtime) *Runtime) {
	regMu.Lock()
	defer regMu.Unlock()
	m := map[*fib.Engine]*Runtime{}
	if old := registry.Load(); old != nil {
		for k, v := range *old {
			m[k] = v
		}
	}
	if n := next(m[engine]); n != nil {
		m[engine] = n
	} else {
		delete(m, engine)
	}
	registry.Store(&m)
}

// Lookup returns the runtime of the sync role whose modules dial through
// engine, the engine the Admin API names, or nil.
func Lookup(engine *fib.Engine) *Runtime {
	if r := registry.Load(); r != nil {
		return (*r)[engine]
	}
	return nil
}

// pauseGate holds a pipeline's source while it is paused.
type pauseGate struct {
	mu sync.Mutex
	// ch is open while the pipeline is paused, and closed by Resume.
	ch chan struct{}
}

func (g *pauseGate) wait() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ch
}

func (g *pauseGate) paused() bool { return g.wait() != nil }

// held waits while the pipeline is paused, and reports false when ctx ends
// first.
func (p *pipe) held(ctx context.Context) bool {
	for {
		ch := p.pause.wait()
		if ch == nil {
			return ctx.Err() == nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}

// rateSeconds is how many seconds of acknowledgements meter keeps, and
// rateWindow over how many the throughput is reckoned.
const (
	rateSeconds = 12
	rateWindow  = 10
)

// meter counts what a pipeline does, for Status.
type meter struct {
	read, done, failed, errors atomic.Int64

	mu sync.Mutex
	// secs and counts are the records acknowledged in each of the last
	// seconds, by the second (Unix time) they fell in.
	secs   [rateSeconds]int64
	counts [rateSeconds]int64
	// commit is the commit time of the last record acknowledged that has
	// one, acked when it was.
	commit, acked time.Time
}

// passed counts n records the watermark passed, failed of them taken by
// on_error, the last with a commit time at commit.
func (m *meter) passed(n, failed int64, commit time.Time) {
	m.done.Add(n)
	m.failed.Add(failed)
	now := time.Now()
	sec := now.Unix()
	m.mu.Lock()
	i := sec % rateSeconds
	if m.secs[i] != sec {
		m.secs[i], m.counts[i] = sec, 0
	}
	m.counts[i] += n
	if !commit.IsZero() {
		m.commit, m.acked = commit, now
	}
	m.mu.Unlock()
}

// rate is the records acknowledged per second over the last rateWindow
// whole seconds.
func (m *meter) rate(now time.Time) float64 {
	sec := now.Unix()
	var n int64
	m.mu.Lock()
	for i := range m.secs {
		if s := m.secs[i]; s < sec && s >= sec-rateWindow {
			n += m.counts[i]
		}
	}
	m.mu.Unlock()
	return float64(n) / rateWindow
}

// noteErr records err as the pipeline's last error, and counts it.
func (p *pipe) noteErr(err error) {
	p.meter.errors.Add(1)
	p.mu.Lock()
	p.err, p.errAt = err.Error(), time.Now()
	p.mu.Unlock()
}

// fill adds to st what the meter, the pause and cur, the run under way if
// any, say.
func (p *pipe) fill(st *Status, cur *run) {
	st.Paused = p.pause.paused()
	if st.Paused && st.State == "running" {
		st.State = "paused"
	}
	if cur != nil {
		cur.mu.Lock()
		st.Queue.Records, st.Queue.Bytes = cur.inRecords, cur.inBytes
		cur.mu.Unlock()
	}
	st.Queue.MaxRecords, st.Queue.MaxBytes = p.cfg.Records(), p.cfg.Bytes()
	m := &p.meter
	st.Read, st.Acknowledged, st.Failed, st.Errors = m.read.Load(), m.done.Load(), m.failed.Load(), m.errors.Load()
	st.Throughput = m.rate(time.Now())
	m.mu.Lock()
	if !m.commit.IsZero() {
		st.LastCommit = m.commit
		st.LagSeconds = max(m.acked.Sub(m.commit).Seconds(), 0)
	}
	m.mu.Unlock()
}

// pipe returns the pipeline named name; rt.mu is held.
func (rt *Runtime) pipe(name string) (*pipe, error) {
	if p := rt.pipes[name]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("%w %q", ErrNoPipeline, name)
}

// Pipeline returns the state of the pipeline named name.
func (rt *Runtime) Pipeline(name string) (Status, error) {
	rt.mu.Lock()
	p, err := rt.pipe(name)
	rt.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	return p.status(), nil
}

// Pause holds the source of the pipeline named name at its next record: what
// it has read is still written, and the checkpoint still saved as the sinks
// acknowledge it, so that a paused pipeline's checkpoint is all it read.
// It stays paused until Resume, across restarts of its source and controls
// that restart it, but not across a reload that changes it or a restart of
// fit.
func (rt *Runtime) Pause(name string) (Status, error) {
	rt.mu.Lock()
	p, err := rt.pipe(name)
	if err == nil {
		p.pause.mu.Lock()
		if p.pause.ch == nil {
			p.pause.ch = make(chan struct{})
			rt.opts.Log.Infof("pipeline %s is paused", name)
		}
		p.pause.mu.Unlock()
	}
	rt.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	return p.status(), nil
}

// Resume lets the source of the pipeline named name go on; a pipeline that
// stopped, on an error (on_error stop, a dead letter not written) or killed,
// is started again from its checkpoint.
func (rt *Runtime) Resume(name string) (Status, error) {
	rt.mu.Lock()
	p, err := rt.pipe(name)
	if err == nil {
		p.pause.mu.Lock()
		if p.pause.ch != nil {
			close(p.pause.ch)
			p.pause.ch = nil
			rt.opts.Log.Infof("pipeline %s resumes", name)
		}
		p.pause.mu.Unlock()
		p.mu.Lock()
		state := p.state
		p.mu.Unlock()
		if state == "stopped" || state == "killed" {
			rt.opts.Log.Infof("pipeline %s starts again from its checkpoint", name)
			err = rt.restart(p, false, nil)
		}
	}
	rt.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	return p.status(), nil
}

// restart stops p, writing what it holds and its checkpoint, calls between
// if any, and runs p again: with force, a job runs at once and anew if it
// had finished, whatever its schedule. rt.mu is held. p runs again even
// when between fails.
func (rt *Runtime) restart(p *pipe, force bool, between func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), rt.opts.StopTimeout)
	defer cancel()
	p.stopCtx = ctx
	close(p.stopping)
	<-p.done
	var err error
	if between != nil {
		err = between()
	}
	// The loop has ended, and every goroutine of its runs with it: the
	// pipe is the caller's alone until the next loop starts.
	p.stopping, p.done, p.stopCtx, p.force = make(chan struct{}), make(chan struct{}), nil, force
	p.mu.Lock()
	p.state, p.cur, p.next = "starting", nil, time.Time{}
	p.mu.Unlock()
	go p.loop()
	return err
}

// Snapshot runs the pipeline named name again from the source's start: its
// checkpoint is removed and it restarts at once, so that a source starts as
// its params say (a database's snapshot then its changes), and a job starts
// anew, whatever its schedule. It replays what the sinks have, so it takes
// confirm when there is a checkpoint.
func (rt *Runtime) Snapshot(name string, confirm bool) (Status, error) {
	return rt.dropCheckpoint(name, confirm, true)
}

// ResetCheckpoint removes the checkpoint of the pipeline named name and
// restarts it, so that the source starts where its params say: which skips
// or replays records, so it takes confirm when there is a checkpoint. A job
// with a schedule waits for its next time.
func (rt *Runtime) ResetCheckpoint(name string, confirm bool) (Status, error) {
	return rt.dropCheckpoint(name, confirm, false)
}

func (rt *Runtime) dropCheckpoint(name string, confirm, force bool) (Status, error) {
	rt.mu.Lock()
	p, err := rt.pipe(name)
	if err == nil {
		err = p.checkConfirm(confirm, func(c *Checkpoint) bool { return c == nil || c.Position == nil && c.Job == nil })
	}
	if err == nil {
		what := "reset"
		if force {
			what = "snapshot"
		}
		err = rt.restart(p, force, func() error {
			// What the stop saved is past what was confirmed, which was
			// dropping it all.
			if err := p.store.remove(); err != nil {
				return err
			}
			// Sinks that keep the checkpoint keep it of the lineage
			// dropped: a file of none says it was dropped, not lost.
			if p.keepsCheckpoint() {
				p.store.lineage = newLineage()
				if err := p.store.save(nil, nil); err != nil {
					return err
				}
			}
			p.noteSaved(nil)
			p.mu.Lock()
			p.job = nil
			p.mu.Unlock()
			rt.opts.Log.Warnf("pipeline %s: its checkpoint is removed (%s): it starts again from where its source starts", name, what)
			return nil
		})
	}
	rt.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	return p.status(), nil
}

// checkConfirm returns ErrConfirm, with what the checkpoint is, unless
// confirm is true or same says the change leaves the checkpoint as it is.
func (p *pipe) checkConfirm(confirm bool, same func(*Checkpoint) bool) error {
	if confirm {
		return nil
	}
	c, err := p.store.read()
	if err != nil {
		return err
	}
	if same(c) {
		return nil
	}
	return fmt.Errorf("%w: the checkpoint of pipeline %s is %s, saved %s", ErrConfirm, p.cfg.Name, c.Position, c.Saved.Format(time.RFC3339))
}

// Checkpoint returns the checkpoint of the pipeline named name, as its file
// holds it; nil when none was saved yet.
func (rt *Runtime) Checkpoint(name string) (*Checkpoint, error) {
	rt.mu.Lock()
	p, err := rt.pipe(name)
	rt.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return p.store.read()
}

// SetCheckpoint sets the checkpoint of the pipeline named name to pos, JSON
// in the source's own form as Checkpoint shows it, and restarts the
// pipeline from it: the pipeline is stopped first, writing what it holds,
// so that its checkpoint is not saved over pos. A job's progress is kept,
// finished or not, and the job goes on from pos at once. A position other
// than the one saved skips or replays records, so it takes confirm.
func (rt *Runtime) SetCheckpoint(name string, pos json.RawMessage, confirm bool) (*Checkpoint, error) {
	pos = bytes.TrimSpace(pos)
	if len(pos) == 0 || bytes.Equal(pos, []byte("null")) {
		return nil, errors.New("the position is empty: reset the checkpoint to remove it")
	}
	compact, err := appendCompact(nil, pos)
	if err != nil {
		return nil, fmt.Errorf("the position is not JSON: %w", err)
	}
	same := func(c *Checkpoint) bool {
		if c == nil {
			return false
		}
		cur, err := appendCompact(nil, c.Position)
		return err == nil && bytes.Equal(cur, compact)
	}
	rt.mu.Lock()
	p, err := rt.pipe(name)
	if err == nil {
		err = p.checkConfirm(confirm, same)
	}
	if err == nil {
		err = rt.restart(p, false, func() error {
			if err := p.checkConfirm(confirm, same); err != nil {
				return err // the checkpoint moved while it stopped
			}
			_, prog, err := p.store.load()
			if err != nil {
				return err
			}
			if prog != nil {
				prog.Finished = nil
			}
			p.store.lineage = newLineage() // a position set by hand starts a lineage
			if err := p.store.save(Position(compact), prog); err != nil {
				return err
			}
			p.noteSaved(Position(compact))
			rt.opts.Log.Warnf("pipeline %s: its checkpoint is set to %s", name, compact)
			return nil
		})
	}
	rt.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return p.store.read()
}

// read returns the checkpoint file, nil when there is none.
func (s *store) read() (*Checkpoint, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Checkpoint
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("the checkpoint %s: %w", s.path(), err)
	}
	if pos, err := appendCompact(nil, c.Position); err == nil {
		c.Position = pos
	}
	return &c, nil
}

// remove removes the checkpoint file, if any, durably.
func (s *store) remove() error {
	if err := os.Remove(s.path()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ParsePipeline decodes data, a pipeline's config as sync.pipelines[] holds
// one, and checks it as a config's is checked: its names and references
// here, its modules when they are loaded. Errors name the field by its
// path in a config holding only it, /sync/pipelines/0/....
func ParsePipeline(data []byte) (*config.SyncPipeline, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' || !json.Valid(data) {
		return nil, errors.New("the pipeline is not a JSON object")
	}
	doc := make([]byte, 0, len(data)+64)
	doc = append(doc, `{"sync": {"state_dir": ".", "pipelines": [`...)
	doc = append(doc, data...)
	doc = append(doc, "]}}"...)
	c, err := config.Load(doc)
	if err != nil {
		return nil, err
	}
	return &c.Sync.Pipelines[0], nil
}

// Tester is a source or sink that can check that it reaches what it reads
// or writes, as a database's connector connects and authenticates, without
// reading or writing records. Test may be called while the module runs,
// and must return once ctx ends.
type Tester interface {
	Test(ctx context.Context) error
}

// TestResult is the test of one of a pipeline's connectors.
type TestResult struct {
	// Part is source, sink <name> or dead_letter <name>; Module the
	// connector's.
	Part   string `json:"part"`
	Module string `json:"module"`
	// Tested is false for a connector that has no test; OK says the test
	// passed, Err why it did not.
	Tested bool    `json:"tested"`
	OK     bool    `json:"ok"`
	Err    string  `json:"error,omitempty"`
	Took   float64 `json:"took_ms"`
}

// Passed reports whether every connector tested passed.
func Passed(res []TestResult) bool {
	for _, t := range res {
		if t.Tested && !t.OK {
			return false
		}
	}
	return true
}

// TestTimeout bounds a connection test when its context has no deadline.
const TestTimeout = 10 * time.Second

// Test tests the connections of the pipeline named name, through the
// modules it runs: its source, sinks and dead_letter sink.
func (rt *Runtime) Test(ctx context.Context, name string) ([]TestResult, error) {
	// Held so that a reload does not let the modules go while they are
	// tested; the test is bounded.
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p, err := rt.pipe(name)
	if err != nil {
		return nil, err
	}
	return testModules(ctx, p.cfg, p.modules), nil
}

// TestPipeline tests the connections of cfg, a pipeline that need not run:
// its modules are loaded, with engine to dial through, tested and let go.
func TestPipeline(ctx context.Context, cfg *config.SyncPipeline, engine *fib.Engine) ([]TestResult, error) {
	m, err := load(cfg, &module.Env{Engine: engine})
	if err != nil {
		return nil, err
	}
	defer m.cleanup()
	return testModules(ctx, cfg, m), nil
}

func testModules(ctx context.Context, cfg *config.SyncPipeline, m *modules) []TestResult {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, TestTimeout)
		defer cancel()
	}
	type part struct {
		name, module string
		v            any
	}
	parts := []part{{"source", cfg.Source.Module, m.src}}
	for i, s := range m.sinks {
		parts = append(parts, part{"sink " + cfg.Sinks[i].Name, cfg.Sinks[i].Sink.Module, s})
	}
	if m.dl != nil {
		parts = append(parts, part{"dead_letter " + cfg.DeadLetter.Name, cfg.DeadLetter.Sink.Module, m.dl})
	}
	out := make([]TestResult, len(parts))
	var wg sync.WaitGroup
	for i, pt := range parts {
		out[i] = TestResult{Part: pt.name, Module: pt.module}
		t, ok := pt.v.(Tester)
		if !ok {
			out[i].Err = "the module has no connection test: a dry run reads from a source"
			continue
		}
		out[i].Tested = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			err := t.Test(ctx)
			out[i].Took = float64(time.Since(start).Microseconds()) / 1000
			if err != nil {
				out[i].Err = err.Error()
			} else {
				out[i].OK = true
			}
		}()
	}
	wg.Wait()
	return out
}

// Test passes: generate reads nothing from anywhere.
func (*Generate) Test(context.Context) error { return nil }

// Test passes: the store is in memory.
func (*Memory) Test(context.Context) error { return nil }

// Test passes: standard output is always there.
func (*Stdout) Test(context.Context) error { return nil }

// DryRunOptions bound a dry run.
type DryRunOptions struct {
	// Limit is how many source records to read: DefaultDryRunRecords when
	// 0, at most MaxDryRunRecords.
	Limit int
	// Timeout is how long to wait for them: DefaultDryRunTimeout when 0,
	// at most MaxDryRunTimeout. A stream with fewer new records answers
	// with those it read by then.
	Timeout time.Duration
	// FromStart reads from where the source starts, as without a
	// checkpoint, rather than from the checkpoint.
	FromStart bool
}

// The bounds of a dry run.
const (
	DefaultDryRunRecords = 10
	MaxDryRunRecords     = 1000
	DefaultDryRunTimeout = 5 * time.Second
	MaxDryRunTimeout     = time.Minute
	// MaxDryRunBytes bounds the records a dry run reads, by their size.
	MaxDryRunBytes = 4 << 20
)

// dryRunGrace is how long a source is waited for once a dry run has what it
// wants, before it is answered without it; a variable for tests.
var dryRunGrace = 5 * time.Second

// DryRun is what a dry run read, and what the transforms made of it.
type DryRun struct {
	// From is the position the source read from, none for its start.
	From json.RawMessage `json:"from,omitempty"`
	// Records are the source's records, each with what the transforms
	// made of it.
	Records []DryRunRecord `json:"records"`
	// Ended says the source reached its end; TimedOut that the timeout
	// came before the limit; SourceErr is the source's error, if it
	// failed.
	Ended     bool   `json:"ended,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	SourceErr string `json:"source_error,omitempty"`
}

// DryRunRecord is a source record of a dry run, in its canonical JSON, and
// what the transforms made of it: the records the sinks would be handed,
// or the error of the transform that failed it.
type DryRunRecord struct {
	In  json.RawMessage   `json:"in"`
	Out []json.RawMessage `json:"out"`
	Err string            `json:"error,omitempty"`
}

var errEnough = errors.New("sync: the dry run has its records")

// DryRun reads the first records of the pipeline named name, from its
// checkpoint, through its transforms, and returns them, writing nothing and
// saving no position: its source and transforms are loaded anew for it, so
// that the pipeline runs on meanwhile, and no sink is loaded. A job's
// snapshot split in chunks is read from its start, since its checkpoint is
// the job's.
func (rt *Runtime) DryRun(ctx context.Context, name string, opts DryRunOptions) (*DryRun, error) {
	rt.mu.Lock()
	p, err := rt.pipe(name)
	rt.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var from Position
	if !opts.FromStart {
		pos, prog, err := p.store.load()
		if err != nil {
			return nil, err
		}
		if prog == nil || len(prog.Chunks) == 0 {
			from = pos
		}
	}
	return DryRunPipeline(ctx, p.cfg, from, rt.opts.Engine, opts)
}

// DryRunPipeline reads the first records of cfg's source from from (nil for
// its start) through its transforms, as Runtime.DryRun does, for a pipeline
// that need not run; engine is what its modules dial through.
func DryRunPipeline(ctx context.Context, cfg *config.SyncPipeline, from Position, engine *fib.Engine, opts DryRunOptions) (*DryRun, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultDryRunRecords
	}
	limit = min(limit, MaxDryRunRecords)
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultDryRunTimeout
	}
	timeout = min(timeout, MaxDryRunTimeout)
	env := &module.Env{Engine: engine}
	v, err := module.Load(Sources, cfg.Source, env)
	if err != nil {
		return nil, err
	}
	src, ok := v.(Source)
	if !ok {
		module.Cleanup(v)
		return nil, cfg.Source.At.Field("module").Errorf("%s is not a source", cfg.Source.Module)
	}
	var ts []Transform
	cleanup := func() {
		module.Cleanup(src)
		for _, t := range ts {
			module.Cleanup(t)
		}
	}
	for _, ref := range cfg.Transforms {
		v, err := module.Load(Transforms, ref, env)
		if err != nil {
			cleanup()
			return nil, err
		}
		t, ok := v.(Transform)
		if !ok {
			module.Cleanup(v)
			cleanup()
			return nil, ref.At.Field("module").Errorf("%s is not a transform", ref.Module)
		}
		ts = append(ts, t)
	}
	if errs := checkChain(cfg.Transforms, ts); len(errs) > 0 {
		cleanup()
		return nil, errs
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var mu sync.Mutex
	var recs []*Record
	size := 0
	emit := func(r *Record) error {
		mu.Lock()
		defer mu.Unlock()
		if len(recs) >= limit || size >= MaxDryRunBytes {
			return errEnough
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.seq = uint64(len(recs))
		recs = append(recs, r)
		size += r.Size()
		if len(recs) >= limit || size >= MaxDryRunBytes {
			cancel()
		}
		return nil
	}
	// The source is named apart from the pipeline, so that one found by
	// its pipeline's name, as the http source, is not taken for it.
	run := &Run{From: from, Once: true, Emit: emit, Pipeline: cfg.Name + "#dry-run"}
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, run) }()
	out := &DryRun{From: json.RawMessage(from)}
	var srcErr error
	ended := false
	select {
	case srcErr = <-done:
		ended = true
	case <-ctx.Done():
		t := time.NewTimer(dryRunGrace)
		select {
		case srcErr = <-done:
			ended = true
		case <-t.C:
			// A source that does not return when asked is let go once it
			// does, and answered without.
			later := cleanup
			go func() { <-done; later() }()
			cleanup = func() {}
		}
		t.Stop()
	}
	mu.Lock()
	got := recs
	full := len(got) >= limit || size >= MaxDryRunBytes
	recs, limit = nil, 0 // a source still running emits nothing more
	mu.Unlock()
	timedOut := !full && errors.Is(ctx.Err(), context.DeadlineExceeded)
	// The source's own end or error counts only when it came before the
	// dry run had what it wanted or timed out, which ends the source too.
	if ended && !full && !timedOut {
		if srcErr == nil {
			out.Ended = true
		} else if !errors.Is(srcErr, errEnough) {
			out.SourceErr = srcErr.Error()
		}
	}
	out.TimedOut = timedOut
	c := newStages(ts)
	c.run(got)
	k := 0
	out.Records = make([]DryRunRecord, 0, len(got))
	for i, r := range got {
		d := DryRunRecord{Out: []json.RawMessage{}}
		d.In, _ = r.AppendJSON(nil)
		start := k
		for k < len(c.outs) && c.outs[k].src == i {
			k++
		}
		if err := c.errs[i]; err != nil {
			d.Err = fmt.Sprintf("transform %d (%s): %v", c.where[i], cfg.Transforms[c.where[i]].Module, err)
		} else {
			for _, o := range c.outs[start:k] {
				b, _ := o.rec.AppendJSON(nil)
				d.Out = append(d.Out, b)
			}
		}
		out.Records = append(out.Records, d)
	}
	c.done()
	cleanup()
	return out, nil
}
