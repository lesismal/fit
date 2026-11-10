package fitsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// quietSource emits Records records, then waits for the pipeline to take
// Positions position-only records past them, as a source does reading a
// log of other tables' changes, then emits After records more. Direct
// emits the position-only records itself, ungated, between its records,
// and each run waits, when Gate is set, for the dispatcher to hold its
// first batch, so that its records and position-only ones come in one.
type quietSource struct {
	Records   int  `json:"records"`
	Positions int  `json:"positions"`
	After     int  `json:"after"`
	Direct    bool `json:"direct"`
	Hold      bool `json:"hold"`
}

// quietGate holds the dispatcher after its first batch until the source
// has emitted everything.
var quietGate struct {
	sync.Mutex
	emitted chan struct{}
}

func quietPos(n int) Position { return Position(fmt.Sprintf(`{"n":%d}`, n)) }

func (s *quietSource) Run(ctx context.Context, r *Run) error {
	n := 0
	if r.From != nil {
		var p struct{ N int }
		if err := json.Unmarshal(r.From, &p); err != nil {
			return err
		}
		n = p.N
	}
	data := func(k int) error {
		for range k {
			n++
			if err := r.Emit(&Record{Namespace: "t", Op: OpInsert, Key: Map(Field{Name: "id", Value: Int(int64(n - 1))}), Position: quietPos(n)}); err != nil {
				return err
			}
		}
		return nil
	}
	if err := data(s.Records); err != nil {
		return err
	}
	if s.Direct {
		// One of no position moves nothing.
		if err := r.Emit(&Record{posOnly: true}); err != nil {
			return err
		}
	}
	for taken := 0; taken < s.Positions; {
		if s.Direct {
			n++
			taken++
			if err := r.Emit(&Record{Position: quietPos(n), posOnly: true}); err != nil {
				return err
			}
			if err := data(1); err != nil {
				return err
			}
			continue
		}
		if err := r.EmitPosition(func() Position { n++; taken++; return quietPos(n) }); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	if err := data(s.After); err != nil {
		return err
	}
	quietGate.Lock()
	if quietGate.emitted != nil {
		close(quietGate.emitted)
		quietGate.emitted = nil
	}
	quietGate.Unlock()
	if s.Hold {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func init() {
	module.Register(module.Info{ID: Sources + ".quiet", New: func() any { return new(quietSource) }, Doc: "Tests'."})
}

func quietPipeline(sn, params, extra string) string {
	return fmt.Sprintf(`{"name": "p", "checkpoint_interval": "2ms"%s,
		"source": {"module": "quiet", "params": %s},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, extra, params, sn)
}

func readCheckpoint(t *testing.T, dir string) Checkpoint {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c Checkpoint
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if c.Position != nil {
		c.Position, _ = appendCompact(nil, c.Position)
	}
	if c.Wrote != nil {
		c.Wrote, _ = appendCompact(nil, c.Wrote)
	}
	return c
}

// TestPositionOnlyQuietTable: a source whose tables are quiet behind a busy
// log moves the checkpoint with position-only records, which no sink sees
// and which count as no record read or written.
func TestPositionOnlyQuietTable(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, dir, quietPipeline(sn, `{"records": 3, "positions": 2, "hold": true}`, ""))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the checkpoint past the records", func() bool { return string(state(rt, "p").Checkpoint) == `{"n":5}` })
	st := state(rt, "p")
	if st.Read != 3 || st.Acknowledged != 3 || st.Sinks[0].Records != 3 || st.Failed != 0 {
		t.Fatalf("%+v", st)
	}
	checkExactlyOnce(t, s.Records(), 3)
	c := readCheckpoint(t, dir)
	if string(c.Position) != `{"n":5}` || string(c.Wrote) != `{"n":3}` || c.Lineage == "" {
		t.Fatalf("%+v", c)
	}

	// Restarted, it reads on from the position-only record's position,
	// and a record moves the checkpoint past it: Wrote is gone.
	if err := rt.Apply(syncConfig(t, dir, quietPipeline(sn, `{"after": 2}`, ""))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the records after", func() bool { return state(rt, "p").State == "finished" })
	if got := ids(s.Records()); fmt.Sprint(got) != "[0 1 2 5 6]" {
		t.Fatalf("%v", got)
	}
	c2 := readCheckpoint(t, dir)
	if string(c2.Position) != `{"n":7}` || c2.Wrote != nil || c2.Lineage != c.Lineage {
		t.Fatalf("%+v", c2)
	}
}

// TestPositionOnlyInBatch: position-only records among a batch's records
// skip the transforms and the sinks, and each is acknowledged in its
// place: a record split in two keeps its numbering, and the checkpoint
// ends at the last record's position.
func TestPositionOnlyInBatch(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	ch := make(chan struct{})
	quietGate.Lock()
	quietGate.emitted = ch
	quietGate.Unlock()
	var once sync.Once
	rt := NewRuntime(Options{Crash: func(_ string, p Point) bool {
		if p == CrashDispatched {
			once.Do(func() { <-ch })
		}
		return false
	}})
	defer rt.Close(context.Background())
	extra := `, "transforms": [{"module": "test", "params": {"split": 2}}]`
	if err := rt.Apply(syncConfig(t, dir, quietPipeline(sn, `{"records": 1, "positions": 3, "after": 1, "direct": true}`, extra))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
	recs := s.Records()
	// ids 0, 2, 4, 6, 7 at positions 1, 3, 5, 7, 8, the others
	// position-only; the even ones split.
	if got := ids(recs); fmt.Sprint(got) != "[0 0 2 2 4 4 6 6 7]" {
		t.Fatalf("%v", got)
	}
	for _, r := range recs {
		if r.posOnly || r.Op != OpInsert {
			t.Fatalf("a sink had %+v", r)
		}
	}
	st := state(rt, "p")
	if string(st.Checkpoint) != `{"n":8}` || st.Read != 5 || st.Acknowledged != 5 {
		t.Fatalf("%+v", st)
	}
	if c := readCheckpoint(t, dir); c.Wrote != nil {
		t.Fatalf("%+v", c)
	}
}

// TestPositionOnlyKeeper: a sink keeping the checkpoint holds the last
// record's position, which position-only records passed: the run starts
// from the file's, which Wrote says is past the sink's by them alone.
func TestPositionOnlyKeeper(t *testing.T) {
	db := keeperName(t)
	k := keptDBOf(db)
	dir := t.TempDir()
	pipe := func(params string) string {
		return fmt.Sprintf(`{"name": "p", "checkpoint_interval": "2ms",
			"source": {"module": "quiet", "params": %s},
			"sinks": [{"name": "k", "batch": {"max_wait": "1ms"}, "sink": {"module": "keeper", "params": {"db": %q, "keep": true}}}]}`, params, db)
	}
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, dir, pipe(`{"records": 3, "positions": 2, "hold": true}`))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the checkpoint past the records", func() bool { return string(state(rt, "p").Checkpoint) == `{"n":5}` })
	k.mu.Lock()
	kept := string(k.pos)
	k.mu.Unlock()
	if kept != `{"n":3}` {
		t.Fatalf("the sink keeps %s", kept)
	}
	if err := rt.Apply(syncConfig(t, dir, pipe(`{"after": 2}`))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the records after", func() bool { return state(rt, "p").State == "finished" })
	if got := k.ids(); fmt.Sprint(got) != "[0 1 2 5 6]" {
		t.Fatalf("%v", got)
	}
}

// TestLineage: a stream's checkpoint has a lineage from its first start,
// which its records carry to the sinks and restarts keep; a reset, a
// position set by hand and a new job make another; a checkpoint saved
// before lineages keeps none until it is reset.
func TestLineage(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	gen := func(n int, extra string) *config.Sync {
		return syncConfig(t, dir, fmt.Sprintf(`{"name": "p", "checkpoint_interval": "1ms"%s,
			"source": {"module": "generate", "params": {"records": %d}},
			"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, extra, n, sn))
	}
	lineages := func() map[string]int {
		out := map[string]int{}
		for _, r := range s.Records() {
			out[r.Lineage()]++
		}
		s.Reset()
		return out
	}
	run := func(rt *Runtime, cfg *config.Sync) {
		t.Helper()
		if err := rt.Apply(cfg); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the run", func() bool { return state(rt, "p").State == "finished" })
	}
	rt := NewRuntime(Options{})
	defer func() { rt.Close(context.Background()) }()
	run(rt, gen(3, ""))
	l1 := readCheckpoint(t, dir).Lineage
	if got := lineages(); l1 == "" || got[l1] != 3 || len(got) != 1 {
		t.Fatalf("%q %v", l1, got)
	}

	// Kept across a restart.
	rt.Close(context.Background())
	rt = NewRuntime(Options{})
	run(rt, gen(5, ""))
	if got := lineages(); readCheckpoint(t, dir).Lineage != l1 || got[l1] != 2 || len(got) != 1 {
		t.Fatalf("%v", got)
	}

	// A reset makes another.
	if _, err := rt.ResetCheckpoint("p", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run anew", func() bool { return state(rt, "p").State == "finished" && len(s.Records()) == 5 })
	l2 := readCheckpoint(t, dir).Lineage
	if got := lineages(); l2 == "" || l2 == l1 || got[l2] != 5 {
		t.Fatalf("%q %v", l2, got)
	}

	// So does a position set by hand.
	if _, err := rt.SetCheckpoint("p", json.RawMessage(`{"n": 3}`), true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run from 3", func() bool { return state(rt, "p").State == "finished" && len(s.Records()) == 2 })
	l3 := readCheckpoint(t, dir).Lineage
	if got := lineages(); l3 == "" || l3 == l2 || got[l3] != 2 {
		t.Fatalf("%q %v", l3, got)
	}

	// A checkpoint saved before lineages keeps none.
	rt.Close(context.Background())
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte(`{"version": 1, "pipeline": "p", "position": {"n": 3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt = NewRuntime(Options{})
	run(rt, gen(4, ""))
	if got := lineages(); readCheckpoint(t, dir).Lineage != "" || got[""] != 1 || len(got) != 1 {
		t.Fatalf("%v", got)
	}

	// A new job makes one; a job of chunks run again from its start,
	// another; one resumed keeps its own.
	job := syncConfig(t, dir, fmt.Sprintf(`{"name": "p", "checkpoint_interval": "1ms", "mode": "once",
		"source": {"module": "generate", "params": {"records": 2, "chunk_records": 1}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))
	rt.Close(context.Background())
	if err := os.Remove(filepath.Join(dir, "p.json")); err != nil {
		t.Fatal(err)
	}
	rt = NewRuntime(Options{})
	run(rt, job)
	j1 := readCheckpoint(t, dir).Lineage
	if got := lineages(); j1 == "" || got[j1] != 2 {
		t.Fatalf("%q %v", j1, got)
	}
	rt.Close(context.Background())
	if st, err := RunPipeline(context.Background(), job, "p", Options{}); err != nil || st.State != "finished" {
		t.Fatal(st, err)
	}
	j2 := readCheckpoint(t, dir).Lineage
	if j2 == "" || j2 == j1 {
		t.Fatalf("%q %q", j1, j2)
	}
	data, err := os.ReadFile(filepath.Join(dir, "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["job"].(map[string]any), "finished")
	data, _ = json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, "p.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err := RunPipeline(context.Background(), job, "p", Options{}); err != nil || st.State != "finished" {
		t.Fatal(st, err)
	}
	if j3 := readCheckpoint(t, dir).Lineage; j3 != j2 {
		t.Fatalf("%q %q", j2, j3)
	}
}

// EmitPosition does nothing without the pipeline's Positions, as in a dry
// run.
func TestEmitPositionGate(t *testing.T) {
	r := &Run{}
	called := false
	if err := r.EmitPosition(func() Position { called = true; return quietPos(1) }); err != nil || called {
		t.Fatal(err, called)
	}
}
