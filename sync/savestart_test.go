package fitsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fit/module"
)

// startSource saves its start, {"start": true}, before its records, as a
// source of a snapshot then a stream does; EmitFirst emits a record before
// it. It logs each run's From, and whether the run had Start.
type startSource struct {
	Records   int  `json:"records"`
	Twice     bool `json:"twice"`
	EmitFirst bool `json:"emit_first"`
}

var startLog struct {
	sync.Mutex
	from   []string
	starts []bool
	errs   []error
}

func (s *startSource) Run(ctx context.Context, r *Run) error {
	startLog.Lock()
	startLog.from = append(startLog.from, string(r.From))
	startLog.starts = append(startLog.starts, r.Start != nil)
	startLog.Unlock()
	if s.EmitFirst {
		// Killed by the crash hook here, the run's SaveStart says so.
		_ = r.Emit(&Record{Namespace: "t", Op: OpInsert, Key: Map(Field{Name: "id", Value: Int(-1)})})
	}
	err := r.SaveStart(Position(`{"start":true}`))
	if err == nil && s.Twice {
		err = r.SaveStart(Position(`{"start":2}`))
	}
	startLog.Lock()
	startLog.errs = append(startLog.errs, err)
	startLog.Unlock()
	if err != nil {
		return err
	}
	for i := range s.Records {
		b, _ := json.Marshal(map[string]int{"n": i + 1})
		if err := r.Emit(&Record{Namespace: "t", Op: OpInsert, Key: Map(Field{Name: "id", Value: Int(int64(i))}), Position: b}); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	module.Register(module.Info{ID: Sources + ".starter", New: func() any { return new(startSource) }, Doc: "Tests'."})
}

func resetStartLog() {
	startLog.Lock()
	startLog.from, startLog.starts, startLog.errs = nil, nil, nil
	startLog.Unlock()
}

func startPipeline(sn, params, extra string) string {
	return fmt.Sprintf(`{"name": "p", "checkpoint_interval": "1ms", "retry": {"backoff": "1ms"}%s,
		"source": {"module": "starter", "params": %s},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, extra, params, sn)
}

func readPosition(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c Checkpoint
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	b, err := appendCompact(nil, Position(c.Position))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A run killed once it saved its start, before any record, starts again
// from that start, which the source is given as From.
func TestSaveStartKilledBeforeFirstCheckpoint(t *testing.T) {
	resetStartLog()
	dir := t.TempDir()
	s, sn := memStore(t)
	cfg := syncConfig(t, dir, startPipeline(sn, `{"records": 20}`, ""))
	var mu sync.Mutex
	saved := 0
	rt := NewRuntime(Options{Crash: func(_ string, p Point) bool {
		mu.Lock()
		defer mu.Unlock()
		if p == CrashCheckpointSaved {
			saved++
			return saved == 1
		}
		return false
	}})
	if err := rt.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the kill", func() bool { return state(rt, "p").State == "killed" })
	rt.Close(context.Background())
	if got := readPosition(t, dir); got != `{"start":true}` {
		t.Fatalf("the checkpoint after the kill: %s", got)
	}
	if n := len(s.Records()); n != 0 {
		t.Fatalf("%d records written before the kill", n)
	}

	rt = NewRuntime(Options{})
	if err := rt.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
	rt.Close(context.Background())
	startLog.Lock()
	from, starts := startLog.from, startLog.starts
	startLog.Unlock()
	if len(from) != 2 || from[0] != "" || from[1] != `{"start":true}` || !starts[0] || !starts[1] {
		t.Fatalf("the runs' From %q, Start %v", from, starts)
	}
	if got := readPosition(t, dir); got != `{"n":20}` {
		t.Fatalf("the final checkpoint: %s", got)
	}
	if n := len(s.Records()); n != 20 {
		t.Fatalf("%d records", n)
	}
}

// A second SaveStart in a run is not saved: the run has its start.
func TestSaveStartOnce(t *testing.T) {
	resetStartLog()
	dir := t.TempDir()
	var mu sync.Mutex
	var seen []string
	rt := NewRuntime(Options{Crash: func(_ string, p Point) bool {
		if p == CrashCheckpointSaved {
			b, _ := os.ReadFile(filepath.Join(dir, "p.json"))
			mu.Lock()
			seen = append(seen, string(b))
			mu.Unlock()
		}
		return false
	}})
	defer rt.Close(context.Background())
	_, sn := memStore(t)
	if err := rt.Apply(syncConfig(t, dir, startPipeline(sn, `{"records": 0, "twice": true}`, ""))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
	if got := readPosition(t, dir); got != `{"start":true}` {
		t.Fatalf("the checkpoint: %s", got)
	}
	if st := state(rt, "p"); string(st.Checkpoint) != `{"start":true}` {
		t.Fatalf("the status's checkpoint: %s", st.Checkpoint)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, s := range seen {
		if strings.Contains(s, `"start": 2`) || strings.Contains(s, `"start":2`) {
			t.Fatalf("the second start was saved: %s", s)
		}
	}
}

// Killed while its start is written, the run saves nothing: SaveStart
// returns errStopped and the checkpoint file is not there.
func TestSaveStartKilledWriting(t *testing.T) {
	resetStartLog()
	dir := t.TempDir()
	rt := NewRuntime(Options{Crash: func(_ string, p Point) bool { return p == CrashCheckpointWritten }})
	_, sn := memStore(t)
	if err := rt.Apply(syncConfig(t, dir, startPipeline(sn, `{"records": 5}`, ""))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the kill", func() bool { return state(rt, "p").State == "killed" })
	rt.Close(context.Background())
	if _, err := os.Stat(filepath.Join(dir, "p.json")); !os.IsNotExist(err) {
		t.Fatalf("the checkpoint file: %v", err)
	}
	startLog.Lock()
	defer startLog.Unlock()
	if len(startLog.errs) != 1 || startLog.errs[0] != errStopped {
		t.Fatalf("SaveStart's errors %v", startLog.errs)
	}
}

// A run already killed saves no start.
func TestSaveStartAfterKill(t *testing.T) {
	resetStartLog()
	dir := t.TempDir()
	rt := NewRuntime(Options{Crash: func(_ string, p Point) bool { return p == CrashEmitted }})
	_, sn := memStore(t)
	if err := rt.Apply(syncConfig(t, dir, startPipeline(sn, `{"records": 5, "emit_first": true}`, ""))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the kill", func() bool { return state(rt, "p").State == "killed" })
	rt.Close(context.Background())
	if _, err := os.Stat(filepath.Join(dir, "p.json")); !os.IsNotExist(err) {
		t.Fatalf("the checkpoint file: %v", err)
	}
	startLog.Lock()
	defer startLog.Unlock()
	if len(startLog.errs) != 1 || startLog.errs[0] != errStopped {
		t.Fatalf("SaveStart's errors %v", startLog.errs)
	}
}

// A start that cannot be saved fails the run, which is tried again.
func TestSaveStartFails(t *testing.T) {
	resetStartLog()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		f.Close()
		t.Skip("the directory is writable all the same (root)")
	}
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	_, sn := memStore(t)
	if err := rt.Apply(syncConfig(t, dir, startPipeline(sn, `{"records": 5}`, ""))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retry", func() bool {
		st := state(rt, "p")
		return st.State == "retrying" && strings.Contains(st.Err, "saving the source's start")
	})
}

// A job (mode once) and a dry run have no Start: SaveStart does nothing.
func TestSaveStartNotInJobs(t *testing.T) {
	resetStartLog()
	dir := t.TempDir()
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	s, sn := memStore(t)
	if err := rt.Apply(syncConfig(t, dir, startPipeline(sn, `{"records": 3}`, `, "mode": "once"`))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
	startLog.Lock()
	starts := startLog.starts
	startLog.Unlock()
	if len(starts) != 1 || starts[0] {
		t.Fatalf("Start given: %v", starts)
	}
	if n := len(s.Records()); n != 3 {
		t.Fatalf("%d records", n)
	}
	var r Run
	if err := r.SaveStart(Position(`{}`)); err != nil {
		t.Fatal(err)
	}
	called := false
	r = Run{From: Position(`{}`), Start: func(Position) error { called = true; return nil }}
	if err := r.SaveStart(Position(`{}`)); err != nil || called {
		t.Fatal("SaveStart with a checkpoint called Start")
	}
	r.From = nil
	if err := r.SaveStart(nil); err != nil || called {
		t.Fatal("SaveStart of nil called Start")
	}
}
