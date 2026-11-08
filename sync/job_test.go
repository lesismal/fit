package fitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/module"
)

// counter is the tests' sink for jobs of many records: it counts how often
// each id was written rather than keeping the records.
type counter struct {
	Store string `json:"store"`
}

type counts struct {
	mu      sync.Mutex
	n       []uint8
	written atomic.Int64
	// hook, when set, is called with each batch, as MemStore's is.
	hook func(batch []*Record) error
}

var (
	countersMu sync.Mutex
	counters   = map[string]*counts{}
)

func counterStore(name string, size int) *counts {
	countersMu.Lock()
	defer countersMu.Unlock()
	c := &counts{n: make([]uint8, size)}
	counters[name] = c
	return c
}

func (c *counter) ParallelWrites() {}

func (c *counter) Write(_ context.Context, batch []*Record) error {
	countersMu.Lock()
	s := counters[c.Store]
	countersMu.Unlock()
	var err error
	if s.hook != nil {
		if err = s.hook(batch); err != nil {
			var be *BatchError
			if !errors.As(err, &be) {
				return err
			}
			batch = batch[:be.Index]
		}
	}
	s.mu.Lock()
	for _, r := range batch {
		s.n[keyID(r)]++
	}
	s.mu.Unlock()
	s.written.Add(int64(len(batch)))
	return err
}

func init() {
	module.Register(module.Info{ID: Sinks + ".counter", New: func() any { return new(counter) }, Doc: "Tests'."})
}

// readJob reads a job's checkpoint: its progress and the chunks done.
func readJob(t *testing.T, dir, name string) (*Progress, []bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var c Checkpoint
	if err := json.Unmarshal(data, &c); err != nil || c.Job == nil {
		t.Fatalf("%v %s", err, data)
	}
	done := make([]bool, len(c.Job.Chunks))
	if c.Job.Finished != nil {
		for i := range done {
			done[i] = true
		}
		return c.Job, done
	}
	var cp chunkPosition
	if len(c.Position) == 0 {
		return c.Job, done
	}
	if err := json.Unmarshal(c.Position, &cp); err != nil {
		t.Fatalf("%v %s", err, c.Position)
	}
	forRanges(cp.Done, func(i int) { done[i] = true })
	return c.Job, done
}

// TestJobResumesByChunk copies a million records, in chunks read four at a
// time, kills the job half way as kill -9 would, and runs it again: the
// chunks its checkpoint had done are not read again, and every record is
// written.
func TestJobResumesByChunk(t *testing.T) {
	total, chunk := int64(1_000_000), int64(10_000)
	if raceEnabled || testing.Short() {
		total, chunk = 200_000, 2_000 // the race detector is slow; still 100 chunks
	}
	dir := t.TempDir()
	name := t.Name()
	c := counterStore(name, int(total))
	pipeline := fmt.Sprintf(`{"name": "copy", "mode": "once", "checkpoint_interval": "5ms",
		"source": {"module": "generate", "params": {"records": %d, "chunk_records": %d}},
		"job": {"max_parallel": 4},
		"sinks": [{"name": "c", "sink": {"module": "counter", "params": {"store": %q}}, "batch": {"max_records": 1000, "max_wait": "1ms"}}]}`,
		total, chunk, name)
	points := []Point{CrashEmitted, CrashDispatched, CrashWritten, CrashAcknowledged, CrashCheckpointWritten, CrashCheckpointSaved}
	at := points[time.Now().UnixNano()%int64(len(points))]
	t.Logf("killed at %s", at)
	var checkpoints atomic.Int32
	rt := NewRuntime(Options{Crash: func(_ string, p Point) bool {
		if p == CrashCheckpointSaved {
			checkpoints.Add(1)
		}
		return p == at && c.written.Load() >= total/2
	}})
	if err := rt.Apply(syncConfig(t, dir, pipeline)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the kill", func() bool { return state(rt, "copy").State == "killed" })
	rt.Close(context.Background())
	if checkpoints.Load() == 0 {
		t.Fatal("no checkpoint before the kill")
	}
	prog, done := readJob(t, dir, "copy")
	ndone := 0
	for _, d := range done {
		if d {
			ndone++
		}
	}
	if ndone == 0 || ndone == len(done) || prog.Finished != nil || len(prog.Chunks) != int(total/chunk) || prog.Written == 0 {
		t.Fatalf("after the kill: %d of %d chunks done, %+v", ndone, len(done), *prog)
	}
	firstRun := c.written.Load()

	rt = NewRuntime(Options{})
	if err := rt.Apply(syncConfig(t, dir, pipeline)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the job to finish", func() bool { return state(rt, "copy").State == "finished" })
	rt.Close(context.Background())
	again := 0
	for id, n := range c.n {
		i := int64(id) / chunk
		switch {
		case n == 0:
			t.Fatalf("record %d was lost", id)
		case done[i] && n != 1:
			t.Fatalf("record %d, of chunk %d done before the kill, was written %d times", id, i, n)
		case n > 2:
			t.Fatalf("record %d was written %d times", id, n)
		case n == 2:
			again++
		}
	}
	// What was written twice is at most the chunks under way at the kill
	// (four), past their positions saved.
	if int64(again) > 4*chunk {
		t.Fatalf("%d records written twice", again)
	}
	end, _ := readJob(t, dir, "copy")
	if end.Finished == nil || end.Written != total || end.Failed != 0 || end.ChunksDone != len(end.Chunks) || end.Rows != total {
		t.Fatalf("at the end: %+v", *end)
	}
	t.Logf("%d chunks of %d done at the kill, %d records written before it, %d twice", ndone, len(done), firstRun, again)
}

// TestJobProgressAndFailures runs a chunked job whose sink fails some
// records, skipped by on_error: the progress counts them, and a finished
// job is not run again until asked.
func TestJobProgressAndFailures(t *testing.T) {
	dir := t.TempDir()
	name := t.Name()
	c := counterStore(name, 1000)
	c.hook = func(batch []*Record) error {
		for i, r := range batch {
			if keyID(r)%100 == 7 {
				return &BatchError{Index: i, Err: errors.New("a bad row")}
			}
		}
		return nil
	}
	pipeline := fmt.Sprintf(`{"name": "j", "mode": "once", "on_error": "skip", "retry": {"max_attempts": 1},
		"source": {"module": "generate", "params": {"records": 1000, "chunk_records": 64}},
		"sinks": [{"name": "c", "sink": {"module": "counter", "params": {"store": %q}}, "batch": {"max_records": 50, "max_wait": "1ms"}}]}`, name)
	cfg := syncConfig(t, dir, pipeline)
	st, err := RunPipeline(context.Background(), cfg, "j", Options{})
	if err != nil || st.State != "finished" || st.Job == nil || st.Job.Failed != 10 || st.Job.Written != 990 || st.Job.Read != 1000 ||
		st.Job.ChunksDone != 16 || st.Job.Finished == nil {
		t.Fatalf("%v %+v %+v", err, st, st.Job)
	}
	// The runtime does not run a finished job again; RunPipeline does.
	rt := NewRuntime(Options{})
	if err := rt.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the job's state", func() bool { return state(rt, "j").State == "finished" })
	rt.Close(context.Background())
	if c.written.Load() != 990 {
		t.Fatalf("%d written", c.written.Load())
	}
	if st, err = RunPipeline(context.Background(), cfg, "j", Options{}); err != nil || st.State != "finished" || st.Job.Failed != 10 || c.written.Load() != 1980 {
		t.Fatalf("%v %+v, %d written", err, st, c.written.Load())
	}
	if _, err := RunPipeline(context.Background(), cfg, "nope", Options{}); err == nil {
		t.Fatal("no error for a pipeline not there")
	}
}

// TestJobWithoutChunks runs a job whose source is not split, and one whose
// source fails past RunPipeline's attempts.
func TestJobWithoutChunks(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	cfg := syncConfig(t, dir, fmt.Sprintf(`{"name": "plain", "mode": "once", "job": {"max_parallel": 8},
		"source": {"module": "generate", "params": {"records": 100, "tx_size": 10}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, sn),
		fmt.Sprintf(`{"name": "broken", "mode": "once", "retry": {"max_attempts": 2, "backoff": "1ms"},
		"source": {"module": "flaky", "params": {"records": 100, "fail_at": 0}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, sn+"-broken"))
	st, err := RunPipeline(context.Background(), cfg, "plain", Options{})
	if err != nil || st.State != "finished" || st.Job.Written != 100 || len(st.Job.Chunks) != 0 {
		t.Fatalf("%v %+v", err, st)
	}
	checkExactlyOnce(t, s.Records(), 100)
	flakyFails.Store(100)
	defer flakyFails.Store(0)
	st, err = RunPipeline(context.Background(), cfg, "broken", Options{SourceAttempts: 2})
	if err != nil || st.State != "stopped" || st.Err == "" {
		t.Fatalf("%v %+v", err, st)
	}
	// Stopped from outside, a job keeps its checkpoint and is not done.
	ctx, cancel := context.WithCancel(context.Background())
	slow := syncConfig(t, dir, fmt.Sprintf(`{"name": "slow", "mode": "once", "checkpoint_interval": "1ms",
		"source": {"module": "generate", "params": {"records": 100000, "chunk_records": 10, "interval": "1ms"}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn+"-slow"))
	// Stopped once 50 records are written, more than the four chunks of 10
	// under way can hold, so that one chunk at least is done.
	slowStore := MemoryStore(sn + "-slow")
	defer slowStore.Reset()
	go func() {
		for len(slowStore.Records()) < 50 && ctx.Err() == nil {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	st, err = RunPipeline(ctx, slow, "slow", Options{})
	if err != nil || st.State == "finished" || st.Job == nil || st.Job.Finished != nil {
		t.Fatalf("%v %+v", err, st)
	}
	if prog, _ := readJob(t, dir, "slow"); prog.Finished != nil || prog.Written == 0 {
		t.Fatalf("%+v", prog)
	}
}

// fakeClock is a Clock the test moves.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	c  chan time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) After(d time.Duration) (<-chan time.Time, func() bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := fakeWaiter{f.now.Add(d), make(chan time.Time, 1)}
	f.waiters = append(f.waiters, w)
	return w.c, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, o := range f.waiters {
			if o.c == w.c {
				f.waiters = append(f.waiters[:i], f.waiters[i+1:]...)
				return true
			}
		}
		return false
	}
}

func (f *fakeClock) waiting() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// advance moves the clock by d, firing the waiters due.
func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	keep := f.waiters[:0]
	for _, w := range f.waiters {
		if w.at.After(f.now) {
			keep = append(keep, w)
			continue
		}
		w.c <- f.now
	}
	f.waiters = keep
}

// gate is the tests' source that holds each run until let go, to see that
// runs never overlap.
type gate struct{}

var (
	gateRuns, gateRunning, gateMost atomic.Int32
	gateRelease                     = make(chan struct{}, 16)
)

func (gate) Run(ctx context.Context, r *Run) error {
	gateRuns.Add(1)
	n := gateRunning.Add(1)
	defer gateRunning.Add(-1)
	for {
		m := gateMost.Load()
		if n <= m || gateMost.CompareAndSwap(m, n) {
			break
		}
	}
	select {
	case <-gateRelease:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.Emit(GenerateRecord("", int64(gateRuns.Load()), 0, 1))
}

func init() {
	module.Register(module.Info{ID: Sources + ".gate", New: func() any { return new(gate) }, Doc: "Tests'."})
}

// TestSchedule runs a job every minute by a fake clock: a run still going
// when its next time passes is not overlapped, the times it missed are
// skipped, and a finished job's next run is a new job.
func TestSchedule(t *testing.T) {
	gateRuns.Store(0)
	gateRunning.Store(0)
	gateMost.Store(0)
	dir := t.TempDir()
	_, sn := memStore(t)
	clock := &fakeClock{now: time.Date(2026, 11, 8, 10, 0, 30, 0, time.UTC)}
	rt := NewRuntime(Options{Clock: clock})
	cfg := fmt.Sprintf(`{"name": "cron", "mode": "once",
		"job": {"schedule": "* * * * *", "time_zone": "Asia/Shanghai"},
		"source": {"module": "gate"},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn)
	if err := rt.Apply(syncConfig(t, dir, cfg)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the job to wait for its time", func() bool { return clock.waiting() == 1 })
	if st := state(rt, "cron"); st.State != "scheduled" || !st.Next.Equal(time.Date(2026, 11, 8, 10, 1, 0, 0, time.UTC)) || gateRuns.Load() != 0 {
		t.Fatalf("%+v, %d runs", st, gateRuns.Load())
	}
	clock.advance(30 * time.Second)
	waitFor(t, "the first run", func() bool { return gateRunning.Load() == 1 })
	// Five minutes pass while it runs: nothing else starts.
	for range 5 {
		clock.advance(time.Minute)
	}
	time.Sleep(20 * time.Millisecond)
	if gateRuns.Load() != 1 || clock.waiting() != 0 {
		t.Fatalf("%d runs while the first went on, %d waiting", gateRuns.Load(), clock.waiting())
	}
	gateRelease <- struct{}{}
	waitFor(t, "the job to wait again", func() bool { return clock.waiting() == 1 && state(rt, "cron").State == "scheduled" })
	if st := state(rt, "cron"); !st.Next.Equal(time.Date(2026, 11, 8, 10, 7, 0, 0, time.UTC)) || st.Job == nil || st.Job.Finished == nil {
		t.Fatalf("%+v", st)
	}
	clock.advance(time.Minute)
	waitFor(t, "the second run", func() bool { return gateRuns.Load() == 2 })
	gateRelease <- struct{}{}
	waitFor(t, "the job to wait again", func() bool { return clock.waiting() == 1 && state(rt, "cron").State == "scheduled" })
	rt.Close(context.Background())
	if clock.waiting() != 0 {
		t.Fatal("a stopped pipeline still waits")
	}

	// Started again with its job finished, it waits for the next time;
	// stopped while a run goes on, it starts again at once.
	rt = NewRuntime(Options{Clock: clock})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, dir, cfg)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the job to wait", func() bool { return clock.waiting() == 1 && state(rt, "cron").State == "scheduled" })
	clock.advance(time.Minute)
	waitFor(t, "the third run", func() bool { return gateRunning.Load() == 1 && gateRuns.Load() == 3 })
	rt.Close(context.Background())
	rt = NewRuntime(Options{Clock: clock})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, dir, cfg)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the interrupted job to go on", func() bool { return gateRunning.Load() == 1 && gateRuns.Load() == 4 })
	gateRelease <- struct{}{}
	waitFor(t, "the job to wait again", func() bool { return clock.waiting() == 1 && state(rt, "cron").State == "scheduled" })
	if gateMost.Load() != 1 {
		t.Fatalf("%d runs at once", gateMost.Load())
	}
}

func TestRanges(t *testing.T) {
	done := []bool{true, true, true, false, true, false, true, true}
	s := string(appendRanges(nil, done))
	if s != "0-2,4,6-7" || countRanges(s) != 6 {
		t.Fatalf("%q %d", s, countRanges(s))
	}
	cr := &chunkRun{plan: make([]Chunk, 3), done: make([]bool, 3), at: map[int]Position{}}
	for _, bad := range []string{`{"done":"0-5"}`, `{"done":"x"}`, `{"done":"", "at":[{"chunk":9,"from":{}}]}`, `[`} {
		if err := cr.resume(Position(bad)); err == nil {
			t.Errorf("%s resumed", bad)
		}
	}
}
