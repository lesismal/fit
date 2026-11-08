package fitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
)

// ID is the role's, and its member's path in the config.
const ID = "sync"

func init() {
	app.Register(app.Info{
		ID: ID,
		Member: func(c *config.Config) any {
			if c.Sync == nil {
				return nil
			}
			return c.Sync
		},
		New: func(h *app.Host) (app.Role, error) {
			engine := h.Upstream
			if engine == nil {
				engine = h.Engine
			}
			r := &role{rt: NewRuntime(Options{Log: h.Log, StopTimeout: h.DrainTimeout(), Engine: engine}), engine: engine}
			Register(engine, r.rt)
			return r, nil
		},
		Check: func(c *config.Config) error { return Check(c.Sync) },
	})
}

// role runs the config's pipelines; the Admin API finds its runtime by
// engine.
type role struct {
	rt     *Runtime
	engine *fib.Engine
}

func (r *role) Apply(member any) error {
	s, ok := member.(*config.Sync)
	if !ok {
		return fmt.Errorf("sync: a member of type %T", member)
	}
	return r.rt.Apply(s)
}

func (r *role) Stop(ctx context.Context) {
	Unregister(r.engine, r.rt)
	r.rt.Close(ctx)
}

// Check loads every module s names, which checks their params, and lets
// them go.
func Check(s *config.Sync) error {
	if s == nil {
		return nil
	}
	var errs config.Errors
	for i := range s.Pipelines {
		m, err := load(&s.Pipelines[i], nil)
		if err != nil {
			errs = append(errs, config.AsErrors(err)...)
			continue
		}
		m.cleanup()
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Options are a Runtime's.
type Options struct {
	// Log is where pipelines say what they do; nil says nothing.
	Log *logs.Logger
	// StopTimeout bounds how long a pipeline stopped, by a reload or Close
	// without a deadline, may take to write what it holds: 30s when 0.
	StopTimeout time.Duration
	// Crash, for tests, is asked at each Point of each pipeline; true kills
	// the pipeline there, as kill -9 would: it writes and saves nothing
	// more, its checkpoint included, and is not restarted.
	Crash func(pipeline string, at Point) bool
	// Engine is the fib engine modules dial their own connections through,
	// as a WASM connector's HTTP client does; nil gives them none.
	Engine *fib.Engine
	// Clock is the time jobs' schedules go by: the system's when nil.
	Clock Clock
	// SourceAttempts, when above 0, is how many times in a row a source
	// may fail before its pipeline stops, rather than restarting for ever.
	SourceAttempts int
}

// Runtime runs pipelines, and moves them to each config applied.
type Runtime struct {
	opts  Options
	mu    sync.Mutex // held for a whole Apply or Close
	pipes map[string]*pipe
	// oneRun is RunPipeline's: a job runs now, anew when it had finished,
	// and once, whatever its schedule.
	oneRun bool
}

// NewRuntime returns a Runtime running no pipeline yet.
func NewRuntime(opts Options) *Runtime {
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = 30 * time.Second
	}
	return &Runtime{opts: opts, pipes: map[string]*pipe{}}
}

// modules are a pipeline's modules, loaded.
type modules struct {
	src        Source
	transforms []Transform
	sinks      []Sink
	dl         Sink
}

func (m *modules) cleanup() {
	module.Cleanup(m.src)
	for _, t := range m.transforms {
		module.Cleanup(t)
	}
	for _, s := range m.sinks {
		module.Cleanup(s)
	}
	if m.dl != nil {
		module.Cleanup(m.dl)
	}
}

// load loads a pipeline's modules.
func load(p *config.SyncPipeline, env *module.Env) (*modules, error) {
	m := &modules{}
	var errs config.Errors
	get := func(ns string, ref config.ModuleRef) any {
		v, err := module.Load(ns, ref, env)
		if err != nil {
			errs = append(errs, config.AsErrors(err)...)
			return nil
		}
		return v
	}
	if v := get(Sources, p.Source); v != nil {
		if m.src, _ = v.(Source); m.src == nil {
			errs = append(errs, p.Source.At.Field("module").Errorf("%s is not a source", p.Source.Module))
		}
	}
	for _, ref := range p.Transforms {
		if v := get(Transforms, ref); v != nil {
			t, ok := v.(Transform)
			if !ok {
				errs = append(errs, ref.At.Field("module").Errorf("%s is not a transform", ref.Module))
			}
			m.transforms = append(m.transforms, t)
		}
	}
	if len(errs) == 0 {
		errs = append(errs, checkChain(p.Transforms, m.transforms)...)
	}
	sink := func(s *config.SyncSink) Sink {
		v := get(Sinks, s.Sink)
		if v == nil {
			return nil
		}
		k, ok := v.(Sink)
		if !ok {
			errs = append(errs, s.Sink.At.Field("module").Errorf("%s is not a sink", s.Sink.Module))
		}
		return k
	}
	for i := range p.Sinks {
		k := sink(&p.Sinks[i])
		if _, ok := k.(*DeadLetters); ok {
			errs = append(errs, p.Sinks[i].Sink.At.Field("module").Errorf("dead_letters is only a pipeline's dead_letter sink"))
		}
		m.sinks = append(m.sinks, k)
	}
	if p.DeadLetter != nil {
		m.dl = sink(p.DeadLetter)
	}
	if len(errs) > 0 {
		m.cleanup()
		return nil, errs
	}
	return m, nil
}

// key is what a pipeline is restarted for when it changes: its settings and
// the state directory.
func key(dir string, p *config.SyncPipeline) string {
	b, _ := json.Marshal(p) // a config always marshals
	return dir + "\x00" + string(b)
}

// Apply moves the runtime to s, nil for none: it starts the pipelines s adds,
// stops those it removes, and restarts those whose settings changed, each
// stopped pipeline writing what it holds and its checkpoint first. The
// others run on. An error loading a pipeline's modules leaves every
// pipeline as it was.
func (rt *Runtime) Apply(s *config.Sync) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	want := map[string]*config.SyncPipeline{}
	dir := ""
	if s != nil {
		dir = s.StateDir
		for i := range s.Pipelines {
			want[s.Pipelines[i].Name] = &s.Pipelines[i]
		}
	}
	var stop []*pipe
	for name, p := range rt.pipes {
		if w := want[name]; w == nil || key(dir, w) != p.key {
			stop = append(stop, p)
		}
	}
	var start []*pipe
	var errs []error
	for name, w := range want {
		if p := rt.pipes[name]; p != nil && key(dir, w) == p.key {
			continue
		}
		m, err := load(w, &module.Env{Engine: rt.opts.Engine})
		if err != nil {
			errs = append(errs, fmt.Errorf("pipeline %s: %w", name, err))
			continue
		}
		start = append(start, rt.newPipe(dir, w, m))
	}
	if len(errs) > 0 {
		for _, p := range start {
			p.modules.cleanup()
		}
		return errors.Join(errs...)
	}
	for _, p := range stop {
		rt.stopPipe(context.Background(), p)
		delete(rt.pipes, p.cfg.Name)
	}
	for _, p := range start {
		rt.pipes[p.cfg.Name] = p
		rt.opts.Log.Infof("pipeline %s starts", p.cfg.Name)
		go p.loop()
	}
	return nil
}

// Close stops every pipeline, waiting for each to write what it holds and
// its checkpoint until ctx ends at most.
func (rt *Runtime) Close(ctx context.Context) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range rt.pipes {
		wg.Add(1)
		go func() { defer wg.Done(); rt.stopPipe(ctx, p) }()
	}
	wg.Wait()
	clear(rt.pipes)
}

// stopPipe stops p and lets its modules go; the caller takes it out of
// rt.pipes.
func (rt *Runtime) stopPipe(ctx context.Context, p *pipe) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rt.opts.StopTimeout)
		defer cancel()
	}
	p.stopCtx = ctx
	close(p.stopping)
	<-p.done
	p.modules.cleanup()
}

// Status is a pipeline's state, as the Admin API shows it.
type Status struct {
	Name string `json:"name"`
	// State is starting, running, paused (running, its source held by
	// Pause), retrying (its source failed, and it starts again after a
	// backoff), finished (its source ended, all written), stopped
	// (on_error stop, a dead letter not written, or a source failing past
	// RunPipeline's attempts), scheduled (a job waiting for its schedule's
	// next time) or killed (by a crash hook).
	State string `json:"state"`
	// Paused says Pause holds the source, whatever the state.
	Paused bool `json:"paused,omitempty"`
	// Err is the last error, if any, and ErrAt when it came.
	Err   string    `json:"error,omitempty"`
	ErrAt time.Time `json:"error_at,omitzero"`
	// Errors counts the errors since the pipeline started: writes that
	// failed (each attempt), records a transform failed, dead letters not
	// written, and the source's failures.
	Errors int64 `json:"errors"`
	// Checkpoint is the position last saved.
	Checkpoint Position `json:"-"`
	// Job is the progress of a pipeline of mode once, nil for others.
	Job *Progress `json:"job,omitempty"`
	// Next is when a scheduled job starts next, while it waits.
	Next time.Time `json:"next,omitzero"`
	// Queue is what the queue holds now: records read and not yet
	// acknowledged by every sink, and its bounds.
	Queue QueueStatus `json:"queue"`
	// Read counts the source's records read since the pipeline started,
	// Acknowledged those every sink acknowledged (one on_error took
	// among them), Failed those on_error took. Records read again after a
	// restart count again.
	Read         int64 `json:"read"`
	Acknowledged int64 `json:"acknowledged"`
	Failed       int64 `json:"failed"`
	// Throughput is the records acknowledged per second, over the last
	// ten seconds.
	Throughput float64 `json:"throughput"`
	// LastCommit is the commit time of the last record acknowledged that
	// has one, and LagSeconds how long after it that record was
	// acknowledged: none when the source's records carry no commit time.
	LastCommit time.Time `json:"last_commit,omitzero"`
	LagSeconds float64   `json:"lag_seconds,omitempty"`
	// ReadBytes is the size of the records read, as the queue reckons it.
	ReadBytes int64 `json:"read_bytes"`
	// SourceLag is how far behind its server's head the source last said
	// it is, for sources that tell it.
	SourceLag *SourceLag `json:"source_lag,omitempty"`
	// Sinks are what each sink did, in the config's order.
	Sinks []SinkStatus `json:"sinks,omitempty"`
	// DeadLetters are what became of the dead letters, with a dead_letter
	// sink.
	DeadLetters *DeadLetterStatus `json:"dead_letters,omitempty"`
}

// QueueStatus is a pipeline's queue, as Status shows it.
type QueueStatus struct {
	Records    int   `json:"records"`
	Bytes      int64 `json:"bytes"`
	MaxRecords int   `json:"max_records"`
	MaxBytes   int64 `json:"max_bytes"`
}

// MarshalJSON writes the checkpoint as the JSON it is.
func (s Status) MarshalJSON() ([]byte, error) {
	type plain Status
	return json.Marshal(struct {
		plain
		Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
	}{plain(s), json.RawMessage(s.Checkpoint)})
}

// Status returns every pipeline's state, by name.
func (rt *Runtime) Status() []Status {
	rt.mu.Lock()
	ps := make([]*pipe, 0, len(rt.pipes))
	for _, p := range rt.pipes {
		ps = append(ps, p)
	}
	rt.mu.Unlock()
	out := make([]Status, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.status())
	}
	slices.SortFunc(out, func(a, b Status) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out
}

// pipe is a pipeline as the config sets it, and the goroutine that runs it,
// again after its source fails.
type pipe struct {
	*modules
	rt       *Runtime
	cfg      *config.SyncPipeline
	key      string
	store    *store
	stopping chan struct{}
	stopCtx  context.Context
	done     chan struct{}

	mu    sync.Mutex
	state string
	err   string
	saved Position
	job   *Progress
	cur   *run
	next  time.Time
	errAt time.Time

	// The rest is the Admin API's (control.go): the pipeline paused, the
	// next loop forced to run a job at once, and the counters Status shows.
	pause pauseGate
	force bool
	meter meter
	// sinkMeters count what each sink does, and errLog limits the error
	// lines (metrics.go).
	sinkMeters []sinkMeter
	errLog     errLog
}

// status returns the pipeline's state, its job's progress as of now.
func (p *pipe) status() Status {
	p.mu.Lock()
	st := Status{Name: p.cfg.Name, State: p.state, Err: p.err, ErrAt: p.errAt, Checkpoint: p.saved}
	if st.State == "scheduled" {
		st.Next = p.next
	}
	cur := p.cur
	p.mu.Unlock()
	st.Job = p.progress(cur, nil, false)
	p.fill(&st, cur)
	return st
}

func (rt *Runtime) newPipe(dir string, cfg *config.SyncPipeline, m *modules) *pipe {
	p := &pipe{modules: m, rt: rt, cfg: cfg, key: key(dir, cfg), stopping: make(chan struct{}), done: make(chan struct{}), state: "starting"}
	p.store = &store{dir: dir, name: cfg.Name}
	p.sinkMeters = make([]sinkMeter, len(cfg.Sinks))
	if d, ok := m.dl.(*DeadLetters); ok {
		d.bind(dir, cfg.Name)
	}
	if h := rt.opts.Crash; h != nil {
		p.store.crash = func(pt Point) bool { return h(cfg.Name, pt) }
	}
	return p
}

func (p *pipe) setState(state string, err error) {
	p.mu.Lock()
	p.state = state
	if err != nil {
		p.err = err.Error()
		p.errAt = time.Now()
	}
	p.mu.Unlock()
}

func (p *pipe) noteSaved(pos Position) {
	p.mu.Lock()
	p.saved = pos
	p.mu.Unlock()
}

// loop runs the pipeline, and a job again at each time of its schedule, a
// job interrupted resumed at once; until it is stopped, or ends with no
// schedule.
func (p *pipe) loop() {
	defer close(p.done)
	sched, _, _ := p.cfg.Schedule()
	if p.rt.oneRun || !p.cfg.Once() {
		sched = nil
	}
	again := p.rt.oneRun || p.force
	if sched != nil && !p.force && !p.interrupted() {
		if !p.wait() {
			return
		}
		again = true
	}
	for {
		switch p.runOnce(again) {
		case outStopped, outKilled:
			return
		}
		if sched == nil || !p.wait() {
			return
		}
		again = true
	}
}

// runOnce runs the pipeline from its checkpoint, again after a backoff each
// time its source fails, until it ends or is stopped: a job finished is run
// anew only when again is true.
func (p *pipe) runOnce(again bool) outcome {
	log := p.rt.opts.Log
	for attempt := 1; ; {
		from, err := p.start(again)
		switch {
		case errors.Is(err, errJobDone):
			p.setState("finished", nil)
			return outFinished
		case errors.Is(err, errCrashed):
			p.setState("killed", nil)
			return outKilled
		}
		if err == nil {
			p.noteSaved(from)
			p.setState("running", nil)
			if from != nil {
				log.Infof("pipeline %s runs from its checkpoint %s", p.cfg.Name, from)
			} else {
				log.Infof("pipeline %s runs from where its source starts", p.cfg.Name)
			}
			r := newRun(p)
			p.mu.Lock()
			p.cur = r
			p.mu.Unlock()
			var out outcome
			out, err = r.do(from)
			p.mu.Lock()
			p.cur = nil
			p.mu.Unlock()
			if r.saved {
				attempt = 1
			}
			switch out {
			case outFinished:
				p.setState("finished", nil)
				log.Infof("pipeline %s finished", p.cfg.Name)
				return out
			case outStopped:
				return out
			case outKilled:
				p.setState("killed", nil)
				log.Warnf("pipeline %s was killed", p.cfg.Name)
				return out
			case outFailed:
				p.setState("stopped", err)
				log.Errorf("pipeline %s stopped: %v", p.cfg.Name, err)
				return out
			}
		}
		p.noteErr(err)
		if n := p.rt.opts.SourceAttempts; n > 0 && attempt >= n {
			p.setState("stopped", err)
			log.Errorf("pipeline %s stopped after %d attempts: %v", p.cfg.Name, attempt, err)
			return outFailed
		}
		p.setState("retrying", err)
		wait := p.cfg.Backoff(attempt)
		log.Warnf("pipeline %s: %v; it starts again from its checkpoint in %s", p.cfg.Name, err, wait)
		attempt++
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-p.stopping:
			t.Stop()
			return outStopped
		}
	}
}
