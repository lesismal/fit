package fitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
)

// The test's own modules: a transform that drops, splits or fails records
// by their id, and a source that fails once.

type testTransform struct {
	Drop  int64 `json:"drop,omitempty"`
	Split int64 `json:"split,omitempty"`
	Fail  int64 `json:"fail,omitempty"`
}

func (x *testTransform) Apply(r *Record, emit func(*Record)) error {
	id := keyID(r)
	switch {
	case x.Drop > 0 && id%x.Drop == 0:
	case x.Fail > 0 && id == x.Fail:
		return fmt.Errorf("record %d refused", id)
	case x.Split > 0 && id%x.Split == 0:
		emit(r)
		c := r.Clone()
		c.Namespace += ".copy"
		emit(c)
	default:
		emit(r)
	}
	return nil
}

var flakyFails atomic.Int32

type flakySource struct {
	Records int64 `json:"records"`
	FailAt  int64 `json:"fail_at"`
}

func (f *flakySource) Run(ctx context.Context, r *Run) error {
	if flakyFails.Load() > 0 {
		emit := r.Emit
		n := int64(0)
		r.Emit = func(rec *Record) error {
			if n++; n > f.FailAt && flakyFails.Add(-1) >= 0 {
				return errors.New("the connection broke")
			}
			return emit(rec)
		}
	}
	return (&Generate{Records: f.Records}).Run(ctx, r)
}

func init() {
	module.Register(module.Info{ID: Transforms + ".test", New: func() any { return new(testTransform) }, Doc: "Tests'."})
	module.Register(module.Info{ID: Sources + ".flaky", New: func() any { return new(flakySource) }, Doc: "Tests'."})
}

func keyID(r *Record) int64 {
	v, _ := r.Key.Get("id")
	return v.AsInt()
}

// pipeline returns a config of one pipeline, its JSON in body.
func syncConfig(t *testing.T, dir string, pipelines ...string) *config.Sync {
	t.Helper()
	src := fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [%s]}}`, dir, strings.Join(pipelines, ","))
	c, err := config.Load([]byte(src))
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	if err := Check(c.Sync); err != nil {
		t.Fatal(err)
	}
	return c.Sync
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func state(rt *Runtime, name string) Status {
	for _, s := range rt.Status() {
		if s.Name == name {
			return s
		}
	}
	return Status{}
}

func savedN(t *testing.T, dir, name string) int64 {
	t.Helper()
	n, err := readN(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// readN reads the count a generate pipeline's checkpoint holds.
func readN(dir, name string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var c struct{ Position struct{ N int64 } }
	err = json.Unmarshal(data, &c)
	return c.Position.N, err
}

func memStore(t *testing.T) (*MemStore, string) {
	name := t.Name() + fmt.Sprint(rand.Int())
	s := MemoryStore(name)
	t.Cleanup(s.Reset)
	return s, name
}

// ids returns the ids of recs, in order.
func ids(recs []*Record) []int64 {
	out := make([]int64, len(recs))
	for i, r := range recs {
		out[i] = keyID(r)
	}
	return out
}

// checkExactlyOnce fails unless recs hold 0..n-1 once each, in order.
func checkExactlyOnce(t *testing.T, recs []*Record, n int64) {
	t.Helper()
	got := ids(recs)
	if int64(len(got)) != n {
		t.Fatalf("%d records, want %d", len(got), n)
	}
	for i, id := range got {
		if id != int64(i) {
			t.Fatalf("record %d is %d", i, id)
		}
	}
}

func TestTwoSinksCheckpointIsTheMinimum(t *testing.T) {
	dir := t.TempDir()
	a, an := memStore(t)
	b, bn := memStore(t)
	release := make(chan struct{})
	b.SetHook(func(ctx context.Context, batch []*Record) error {
		for _, r := range batch {
			if keyID(r) >= 300 {
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return nil
	})
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	c := syncConfig(t, dir, fmt.Sprintf(`{"name": "p", "mode": "once", "checkpoint_interval": "10ms",
		"source": {"module": "generate", "params": {"records": 1000, "tx_size": 10}},
		"queue": {"max_records": 2000},
		"sinks": [{"name": "a", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_records": 7, "max_wait": "1ms"}},
		          {"name": "b", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_records": 50}}]}`, an, bn))
	if err := rt.Apply(c); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "sink a to have every record", func() bool { return len(a.Records()) == 1000 })
	waitFor(t, "a checkpoint", func() bool { return savedN(t, dir, "p") > 0 })
	time.Sleep(50 * time.Millisecond)
	n := savedN(t, dir, "p")
	if n > 300 || n%10 != 0 {
		t.Fatalf("the checkpoint is at %d, past b's 300 or inside a transaction", n)
	}
	close(release)
	waitFor(t, "the pipeline to finish", func() bool { return state(rt, "p").State == "finished" })
	if n := savedN(t, dir, "p"); n != 1000 {
		t.Fatalf("checkpoint %d", n)
	}
	checkExactlyOnce(t, a.Records(), 1000)
	checkExactlyOnce(t, b.Records(), 1000)
}

func TestSinkFailsMidBatch(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	var calls atomic.Int32
	s.SetHook(func(ctx context.Context, batch []*Record) error {
		switch calls.Add(1) {
		case 1:
			return &BatchError{Index: 3, Err: errors.New("record 3 conflicts")}
		case 2, 3:
			return errors.New("the server is gone")
		}
		return nil
	})
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, dir, fmt.Sprintf(`{"name": "p", "mode": "once",
		"source": {"module": "generate", "params": {"records": 200, "tx_size": 4}},
		"retry": {"max_attempts": 5, "backoff": "1ms"},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_records": 10, "max_wait": "1ms"}}]}`, sn))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pipeline to finish", func() bool { return state(rt, "p").State == "finished" })
	checkExactlyOnce(t, s.Records(), 200)
	if calls.Load() < 4 {
		t.Fatalf("%d writes", calls.Load())
	}
}

// TestCrashAnywhere kills the pipeline at random points, as kill -9 would,
// then runs it again to its end: nothing is lost, and only what came after
// the checkpoint saved before the kill is written twice.
func TestCrashAnywhere(t *testing.T) {
	points := []Point{CrashEmitted, CrashDispatched, CrashWritten, CrashAcknowledged, CrashCheckpointWritten, CrashCheckpointSaved}
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rnd := rand.New(rand.NewPCG(seed, 7))
	const total = 400
	for round := range 24 {
		at, nth := points[round%len(points)], int32(1+rnd.IntN(60))
		t.Run(fmt.Sprintf("%s-%d", at, nth), func(t *testing.T) {
			dir := t.TempDir()
			s, sn := memStore(t)
			pipeline := fmt.Sprintf(`{"name": "p", "mode": "once", "checkpoint_interval": "1ms",
				"source": {"module": "generate", "params": {"records": %d, "tx_size": %d}},
				"queue": {"max_records": %d},
				"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_records": %d, "max_wait": "1ms"}}]}`,
				total, 1+rnd.IntN(9), 5+rnd.IntN(100), sn, 1+rnd.IntN(30))
			var seen atomic.Int32
			var ckpts []int64
			var mu sync.Mutex
			rt := NewRuntime(Options{Crash: func(_ string, p Point) bool {
				if p == CrashCheckpointSaved {
					mu.Lock()
					n, _ := readN(dir, "p")
					ckpts = append(ckpts, n)
					mu.Unlock()
				}
				return p == at && seen.Add(1) == nth
			}})
			if err := rt.Apply(syncConfig(t, dir, pipeline)); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the kill or the end", func() bool { st := state(rt, "p").State; return st == "killed" || st == "finished" })
			rt.Close(context.Background())
			before := savedN(t, dir, "p")
			firstRun := len(s.Records())
			mu.Lock()
			for _, n := range ckpts {
				if p := mustTx(pipeline); n%p != 0 && n != total {
					t.Fatalf("checkpoint %d inside a transaction of %d", n, p)
				}
			}
			mu.Unlock()

			rt = NewRuntime(Options{})
			if err := rt.Apply(syncConfig(t, dir, pipeline)); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
			rt.Close(context.Background())
			got := ids(s.Records())
			count := map[int64]int{}
			for _, id := range got {
				count[id]++
			}
			for id := int64(0); id < total; id++ {
				switch {
				case count[id] == 0:
					t.Fatalf("record %d was lost (checkpoint %d, %d written before the kill)", id, before, firstRun)
				case id < before && count[id] != 1:
					t.Fatalf("record %d, before the checkpoint %d, was written %d times", id, before, count[id])
				case count[id] > 2:
					t.Fatalf("record %d written %d times", id, count[id])
				}
			}
			if n := savedN(t, dir, "p"); n != total {
				t.Fatalf("final checkpoint %d", n)
			}
			// What the second run wrote starts at the checkpoint.
			if second := got[firstRun:]; len(second) > 0 && second[0] != before {
				t.Fatalf("the second run started at %d, the checkpoint was %d", second[0], before)
			}
		})
	}
}

func mustTx(pipeline string) int64 {
	var p struct {
		Source struct {
			Params struct {
				TxSize int64 `json:"tx_size"`
			}
		}
	}
	_ = json.Unmarshal([]byte(pipeline), &p)
	return max(p.Source.Params.TxSize, 1)
}

func TestStalledSinkHoldsTheSource(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	s.SetHook(func(ctx context.Context, _ []*Record) error { <-ctx.Done(); return ctx.Err() })
	var byRecords, byBytes atomic.Int64
	rt := NewRuntime(Options{Crash: func(name string, p Point) bool {
		switch {
		case p != CrashEmitted:
		case name == "records":
			byRecords.Add(1)
		default:
			byBytes.Add(1)
		}
		return false
	}})
	size := int64(GenerateRecord("", 1, 0, 1).Size())
	if err := rt.Apply(syncConfig(t, dir, fmt.Sprintf(`{"name": "records", "source": {"module": "generate"},
		"queue": {"max_records": 50},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn),
		fmt.Sprintf(`{"name": "bytes", "source": {"module": "generate"},
		"queue": {"max_bytes": %d},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, 20*size, sn))); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := byRecords.Load(); n != 50 {
		t.Fatalf("%d records emitted past a stalled sink, with max_records 50", n)
	}
	if n := byBytes.Load(); n < 15 || n > 21 {
		t.Fatalf("%d records emitted past a stalled sink, with max_bytes for 20", n)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	rt.Close(ctx)
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("closing took %s", d)
	}
	if len(s.Records()) != 0 {
		t.Fatal("a stalled sink kept records")
	}
}

func TestOnceAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	pipeline := fmt.Sprintf(`{"name": "job", "mode": "once", "source": {"module": "generate", "params": {"records": 100, "tx_size": 3}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, sn)
	for range 2 {
		rt := NewRuntime(Options{})
		if err := rt.Apply(syncConfig(t, dir, pipeline)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the job to finish", func() bool { return state(rt, "job").State == "finished" })
		if st := state(rt, "job"); string(st.Checkpoint) != `{"n":100}` {
			t.Fatalf("%+v", st)
		}
		rt.Close(context.Background())
	}
	checkExactlyOnce(t, s.Records(), 100)
	data, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil || !strings.Contains(string(data), `"version": 1`) {
		t.Fatalf("%v %s", err, data)
	}
}

func TestOnError(t *testing.T) {
	failing := func(ctx context.Context, batch []*Record) error {
		for i, r := range batch {
			if keyID(r) == 7 {
				return &BatchError{Index: i, Err: errors.New("a bad row")}
			}
		}
		return nil
	}
	pipeline := func(policy, sink, dl string) string {
		extra := ""
		if dl != "" {
			extra = fmt.Sprintf(`, "dead_letter": {"name": "dl", "sink": {"module": "memory", "params": {"store": %q}}}`, dl)
		}
		return fmt.Sprintf(`{"name": "p", "mode": "once", "on_error": %q, "retry": {"max_attempts": 2, "backoff": "1ms"},
			"source": {"module": "generate", "params": {"records": 20}},
			"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]%s}`, policy, sink, extra)
	}
	without7 := func(t *testing.T, recs []*Record) {
		got := ids(recs)
		if len(got) != 19 || got[7] != 8 {
			t.Fatalf("%v", got)
		}
	}
	t.Run("skip", func(t *testing.T) {
		s, sn := memStore(t)
		s.SetHook(failing)
		rt := NewRuntime(Options{})
		defer rt.Close(context.Background())
		if err := rt.Apply(syncConfig(t, t.TempDir(), pipeline("skip", sn, ""))); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
		without7(t, s.Records())
	})
	t.Run("dead_letter", func(t *testing.T) {
		s, sn := memStore(t)
		d, dn := memStore(t)
		s.SetHook(failing)
		rt := NewRuntime(Options{})
		defer rt.Close(context.Background())
		if err := rt.Apply(syncConfig(t, t.TempDir(), pipeline("dead_letter", sn, dn))); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
		without7(t, s.Records())
		dls := d.Records()
		if len(dls) != 1 || dls[0].Op != OpMessage {
			t.Fatalf("%d dead letters", len(dls))
		}
		after := dls[0].After
		msg, _ := after.Get("error")
		at, _ := after.Get("at")
		doc, _ := after.Get("record")
		var orig Record
		if err := orig.UnmarshalJSON([]byte(doc.Text())); err != nil || keyID(&orig) != 7 ||
			!strings.Contains(msg.Text(), "a bad row") || at.Text() != "sink m" {
			t.Fatalf("%v: %+v", err, dls[0])
		}
	})
	t.Run("dead_letter_fails", func(t *testing.T) {
		s, sn := memStore(t)
		d, dn := memStore(t)
		s.SetHook(failing)
		d.SetHook(func(context.Context, []*Record) error { return errors.New("no room") })
		dir := t.TempDir()
		rt := NewRuntime(Options{})
		defer rt.Close(context.Background())
		if err := rt.Apply(syncConfig(t, dir, pipeline("dead_letter", sn, dn))); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the stop", func() bool { return state(rt, "p").State == "stopped" })
		if st := state(rt, "p"); !strings.Contains(st.Err, "no room") || savedN(t, dir, "p") > 7 {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("stop", func(t *testing.T) {
		s, sn := memStore(t)
		s.SetHook(failing)
		dir := t.TempDir()
		rt := NewRuntime(Options{})
		defer rt.Close(context.Background())
		if err := rt.Apply(syncConfig(t, dir, pipeline("stop", sn, ""))); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the stop", func() bool { return state(rt, "p").State == "stopped" })
		if n := savedN(t, dir, "p"); n != 7 {
			t.Fatalf("checkpoint %d, want 7, just before the record that failed", n)
		}
		if st := state(rt, "p"); !strings.Contains(st.Err, "sink m: a bad row") {
			t.Fatalf("%+v", st)
		}
	})
}

func TestTransforms(t *testing.T) {
	s, sn := memStore(t)
	d, dn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	dir := t.TempDir()
	if err := rt.Apply(syncConfig(t, dir, fmt.Sprintf(`{"name": "p", "mode": "once", "on_error": "dead_letter",
		"source": {"module": "generate", "params": {"records": 30, "tx_size": 5}},
		"transforms": [{"module": "test", "params": {"drop": 3}}, {"module": "test", "params": {"split": 5, "fail": 13}}],
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}],
		"dead_letter": {"name": "dl", "sink": {"module": "memory", "params": {"store": %q}}}}`, sn, dn))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
	var want []int64
	for id := int64(0); id < 30; id++ {
		switch {
		case id%3 == 0, id == 13:
		case id%5 == 0:
			want = append(want, id, id)
		default:
			want = append(want, id)
		}
	}
	if got := ids(s.Records()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, r := range s.Records() {
		if r.pipeline() != "p" {
			t.Fatalf("a record reached its sink of pipeline %q", r.pipeline())
		}
	}
	if dl := d.Records(); len(dl) != 1 || keyID(dl[0]) != 13 {
		t.Fatalf("%d dead letters", len(dl))
	}
	if n := savedN(t, dir, "p"); n != 30 {
		t.Fatalf("checkpoint %d", n)
	}
}

func TestSourceFailureRestarts(t *testing.T) {
	flakyFails.Store(2)
	defer flakyFails.Store(0)
	s, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, t.TempDir(), fmt.Sprintf(`{"name": "p", "mode": "once", "retry": {"backoff": "1ms"},
		"source": {"module": "flaky", "params": {"records": 50, "fail_at": 20}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
	got := ids(s.Records())
	seen := map[int64]bool{}
	for _, id := range got {
		seen[id] = true
	}
	if len(seen) != 50 || got[len(got)-1] != 49 {
		t.Fatalf("%v", got)
	}
}

func TestReloadRestartsOnlyChanged(t *testing.T) {
	dir := t.TempDir()
	_, sn := memStore(t)
	p := func(name, wait string) string {
		return fmt.Sprintf(`{"name": %q, "checkpoint_interval": "10ms", "source": {"module": "generate", "params": {"interval": "1ms"}},
			"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q, "max_records": 1000000}}, "batch": {"max_wait": %q}}]}`, name, sn, wait)
	}
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, dir, p("a", "1ms"), p("b", "1ms"))); err != nil {
		t.Fatal(err)
	}
	pipes := func() map[string]*pipe {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		m := map[string]*pipe{}
		for k, v := range rt.pipes {
			m[k] = v
		}
		return m
	}
	before := pipes()
	waitFor(t, "b's checkpoint", func() bool { return savedN(t, dir, "b") > 0 })
	if err := rt.Apply(syncConfig(t, dir, p("a", "1ms"), p("b", "2ms"), p("c", "1ms"))); err != nil {
		t.Fatal(err)
	}
	after := pipes()
	if after["a"] != before["a"] || after["b"] == before["b"] || after["c"] == nil {
		t.Fatal("the reload did not restart only b and start c")
	}
	select {
	case <-before["b"].done:
	default:
		t.Fatal("the old b runs on")
	}
	if err := rt.Apply(syncConfig(t, dir, p("a", "1ms"))); err != nil {
		t.Fatal(err)
	}
	if after := pipes(); len(after) != 1 || after["a"] != before["a"] {
		t.Fatal("b and c were not removed")
	}
	// A config whose module fails to load changes nothing.
	bad := syncConfig(t, dir, p("a", "5ms"))
	bad.Pipelines[0].Sinks[0].Sink.Module = "nothing"
	if err := rt.Apply(bad); err == nil || pipes()["a"] != before["a"] {
		t.Fatalf("%v", err)
	}
	if err := rt.Apply(nil); err != nil || len(pipes()) != 0 {
		t.Fatalf("%v", err)
	}
}

func TestCheck(t *testing.T) {
	for _, tc := range []struct{ pipeline, want string }{
		{`{"name": "p", "source": {"module": "nothing"}, "sinks": [{"name": "m", "sink": {"module": "memory"}}]}`, `no module "nothing" in sync.sources`},
		{`{"name": "p", "source": {"module": "generate", "params": {"recs": 1}}, "sinks": [{"name": "m", "sink": {"module": "memory"}}]}`, "recs"},
		{`{"name": "p", "source": {"module": "generate"}, "sinks": [{"name": "m", "sink": {"module": "generate"}}]}`, `no module "generate" in sync.sinks`},
		{`{"name": "p", "source": {"module": "generate"}, "transforms": [{"module": "memory"}], "sinks": [{"name": "m", "sink": {"module": "memory"}}]}`, `no module "memory" in sync.transforms`},
	} {
		c, err := config.Load([]byte(`{"sync": {"state_dir": "x", "pipelines": [` + tc.pipeline + `]}}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := Check(c.Sync); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.pipeline, err)
		}
	}
	if Check(nil) != nil {
		t.Fatal("nil")
	}
}

func TestCheckpointFile(t *testing.T) {
	dir := t.TempDir()
	s := &store{dir: filepath.Join(dir, "state"), name: "p"}
	if pos, _, err := s.load(); err != nil || pos != nil {
		t.Fatalf("%v %s", err, pos)
	}
	if err := s.save(Position(`{"n":3}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, ".p.json.tmp-123"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pos, _, err := s.load(); err != nil || string(pos) != `{"n":3}` {
		t.Fatalf("%v %s", err, pos)
	}
	if left, _ := filepath.Glob(filepath.Join(s.dir, ".p.json.tmp-*")); len(left) != 0 {
		t.Fatalf("%v left", left)
	}
	for _, bad := range []string{`{"version": 2, "position": 1}`, `{`, `{"version": 0}`} {
		if err := os.WriteFile(s.path(), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.load(); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	// A checkpoint that cannot be read keeps the pipeline from starting,
	// rather than starting it from scratch.
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	_, sn := memStore(t)
	if err := rt.Apply(syncConfig(t, s.dir, fmt.Sprintf(`{"name": "p", "retry": {"backoff": "1ms"}, "source": {"module": "generate"},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, sn))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pipeline to retry", func() bool {
		st := state(rt, "p")
		return st.State == "retrying" && strings.Contains(st.Err, "version 0")
	})
}

func TestStdout(t *testing.T) {
	var buf strings.Builder
	var mu sync.Mutex
	stdout = writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.WriteString(string(p)) })
	defer func() { stdout = os.Stdout }()
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, t.TempDir(), `{"name": "p", "mode": "once", "source": {"module": "generate", "params": {"records": 3}},
		"sinks": [{"name": "out", "sink": {"module": "stdout"}}]}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
	mu.Lock()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	mu.Unlock()
	if len(lines) != 3 {
		t.Fatalf("%q", lines)
	}
	for i, l := range lines {
		var r Record
		if err := r.UnmarshalJSON([]byte(l)); err != nil || keyID(&r) != int64(i) || !r.After.Equal(GenerateRecord("", int64(i), 3, 1).After) {
			t.Fatalf("%v: %s", err, l)
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestMemoryFull(t *testing.T) {
	s, sn := memStore(t)
	m := &Memory{Store: sn, MaxRecords: 3}
	batch := []*Record{GenerateRecord("", 0, 0, 1), GenerateRecord("", 1, 0, 1)}
	if err := m.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	var be *BatchError
	if err := m.Write(context.Background(), batch); !errors.As(err, &be) || be.Index != 1 || len(s.Records()) != 3 {
		t.Fatalf("%v", err)
	}
}

// TestRole runs a pipeline as the role sync, as fit run does, through the
// app manager: started by a config, stopped by one that leaves it out.
func TestRole(t *testing.T) {
	s, sn := memStore(t)
	dir := t.TempDir()
	load := func(doc string) *config.Config {
		c, err := config.Load([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		if err := app.Check(c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m := app.NewManager(nil, nil, root, nil)
	defer m.Stop(context.Background())
	if err := m.Apply(load(fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [{"name": "p", "mode": "once",
		"source": {"module": "generate", "params": {"records": 20}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}]}}`, dir, sn))); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Running(), ID) {
		t.Fatalf("running %v", m.Running())
	}
	waitFor(t, "the records", func() bool { return len(s.Records()) == 20 })
	waitFor(t, "the checkpoint", func() bool { return savedN(t, dir, "p") == 20 })
	if err := m.Apply(load(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`)); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(m.Running(), ID) {
		t.Fatal("the role runs on")
	}
	if _, err := config.Load([]byte(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate"}, "sinks": [{"name": "m", "sink": {"module": "memory"}}]}]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := (&role{rt: NewRuntime(Options{})}).Apply("x"); err == nil {
		t.Fatal("a member of the wrong type")
	}
}
