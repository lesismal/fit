package fitsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/module"
)

// The test's connectors with connection tests: a source whose test fails,
// a sink with none, and a sink whose test passes with a warning.

type failingTestSource struct {
	Records int64 `json:"records"`
}

func (f *failingTestSource) Run(ctx context.Context, r *Run) error {
	return (&Generate{Records: f.Records}).Run(ctx, r)
}

func (*failingTestSource) Test(context.Context) error { return errors.New("connection refused") }

type untestedSink struct{}

func (untestedSink) Write(context.Context, []*Record) error { return nil }

type warnedSink struct{ untestedSink }

func (warnedSink) Test(context.Context) error { panic("TestWarnings is called") }

func (warnedSink) TestWarnings(context.Context) ([]string, error) {
	return []string{"a column loses data"}, nil
}

// stubbornSource emits one record and then waits a while, whatever its
// context says.
type stubbornSource struct{}

func (stubbornSource) Run(_ context.Context, r *Run) error {
	_ = r.Emit(GenerateRecord("", 0, 0, 1))
	time.Sleep(200 * time.Millisecond)
	return nil
}

type notAModule struct{}

func init() {
	module.Register(module.Info{ID: Sources + ".untestable", New: func() any { return new(failingTestSource) }, Doc: "Tests'."})
	module.Register(module.Info{ID: Sinks + ".untested", New: func() any { return new(untestedSink) }, Doc: "Tests'."})
	module.Register(module.Info{ID: Sinks + ".warned", New: func() any { return new(warnedSink) }, Doc: "Tests'."})
	module.Register(module.Info{ID: Sources + ".stubborn", New: func() any { return new(stubbornSource) }, Doc: "Tests'."})
	module.Register(module.Info{ID: Sources + ".notsource", New: func() any { return new(notAModule) }, Doc: "Tests'."})
	module.Register(module.Info{ID: Transforms + ".nottransform", New: func() any { return new(notAModule) }, Doc: "Tests'."})
}

// TestControlsOnABrokenCheckpoint: a checkpoint that cannot be read fails
// the controls that read it, and a reset that cannot remove it says so.
func TestControlsOnABrokenCheckpoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, dir, fmt.Sprintf(`{"name": "p", "retry": {"backoff": "1h"},
		"source": {"module": "generate", "params": {"records": 5}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, sn))
	waitFor(t, "the retry", func() bool { return state(rt, "p").State == "retrying" })
	if _, err := rt.Checkpoint("p"); err == nil {
		t.Error("checkpoint")
	}
	if _, err := rt.SetCheckpoint("p", json.RawMessage(`{"n": 1}`), false); err == nil || errors.Is(err, ErrConfirm) {
		t.Errorf("set: %v", err)
	}
	if _, err := rt.SetCheckpoint("p", json.RawMessage(`{"n": 1}`), true); err == nil {
		t.Error("set, confirmed")
	}
	if _, err := rt.ResetCheckpoint("p", false); err == nil || errors.Is(err, ErrConfirm) {
		t.Errorf("reset: %v", err)
	}
	if _, err := rt.DryRun(context.Background(), "p", DryRunOptions{}); err == nil {
		t.Error("dry run")
	}
	// A directory in the file's place: unreadable, and not removed.
	path := filepath.Join(dir, "p.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Checkpoint("p"); err == nil {
		t.Error("checkpoint of a directory")
	}
	if _, err := rt.ResetCheckpoint("p", true); err == nil {
		t.Error("reset of a directory")
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ResetCheckpoint("p", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pipeline", func() bool { return state(rt, "p").State == "finished" })
}

// TestPausedPipelineStops: a pipeline paused stops when asked.
func TestPausedPipelineStops(t *testing.T) {
	_, sn := memStore(t)
	rt := NewRuntime(Options{})
	applyOne(t, rt, t.TempDir(), fmt.Sprintf(`{"name": "p", "source": {"module": "generate"},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q, "max_records": 1000000000}}}]}`, sn))
	if _, err := rt.Pause("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Pause("p"); err != nil {
		t.Fatal(err)
	}
	rt.Close(context.Background())
}

// TestDryRunRefusals: the modules a dry run cannot use, and a source that
// does not stop when asked.
func TestDryRunRefusals(t *testing.T) {
	sink := `"sinks": [{"name": "m", "sink": {"module": "untested"}}]`
	for _, p := range []string{
		`{"name": "q", "source": {"module": "nope"}, ` + sink + `}`,
		`{"name": "q", "source": {"module": "notsource"}, ` + sink + `}`,
		`{"name": "q", "source": {"module": "generate"}, "transforms": [{"module": "nope"}], ` + sink + `}`,
		`{"name": "q", "source": {"module": "generate"}, "transforms": [{"module": "nottransform"}], ` + sink + `}`,
		`{"name": "q", "source": {"module": "generate"}, "transforms": [{"module": "fields", "params": {"drop": ["id"]}}, {"module": "key", "params": {"fields": ["id"]}}], ` + sink + `}`,
	} {
		c, err := ParsePipeline([]byte(p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if _, err := DryRunPipeline(context.Background(), c, nil, nil, DryRunOptions{}); err == nil {
			t.Errorf("%s: no error", p)
		}
	}
	old := dryRunGrace
	dryRunGrace = 10 * time.Millisecond
	defer func() { dryRunGrace = old }()
	c, err := ParsePipeline([]byte(`{"name": "q", "source": {"module": "stubborn"}, ` + sink + `}`))
	if err != nil {
		t.Fatal(err)
	}
	d, err := DryRunPipeline(context.Background(), c, nil, nil, DryRunOptions{Limit: 1})
	if err != nil || len(d.Records) != 1 || d.Ended || d.TimedOut {
		t.Fatalf("%+v %v", d, err)
	}
	// The test of a pipeline of the built-in connectors passes.
	c, err = ParsePipeline([]byte(`{"name": "q", "source": {"module": "generate"}, "sinks": [{"name": "m", "sink": {"module": "memory"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res, err := TestPipeline(context.Background(), c, nil); err != nil || !Passed(res) {
		t.Fatalf("%+v %v", res, err)
	}
	if Passed([]TestResult{{Tested: true}}) {
		t.Fatal("a failed test passed")
	}
}

func applyOne(t *testing.T, rt *Runtime, dir, pipeline string) {
	t.Helper()
	if err := rt.Apply(syncConfig(t, dir, pipeline)); err != nil {
		t.Fatal(err)
	}
}

func checkpointFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return data
}

// TestPauseResume: a pause holds the source, while what it read is written
// and the checkpoint saved up to it; a resume lets it go on.
func TestPauseResume(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, dir, fmt.Sprintf(`{"name": "p", "checkpoint_interval": "5ms",
		"source": {"module": "generate", "params": {"interval": "1ms"}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))
	waitFor(t, "records", func() bool { return len(s.Records()) > 20 })
	if _, err := rt.Pause("none"); !errors.Is(err, ErrNoPipeline) {
		t.Fatalf("pause of none: %v", err)
	}
	st, err := rt.Pause("p")
	if err != nil || !st.Paused || st.State != "paused" {
		t.Fatalf("%+v %v", st, err)
	}
	// Everything read gets written, and the checkpoint reaches it.
	waitFor(t, "the queue to drain and the checkpoint to cover it", func() bool {
		st := state(rt, "p")
		n := int64(len(s.Records()))
		return st.Queue.Records == 0 && st.Read == n && savedN(t, dir, "p") == n
	})
	n := len(s.Records())
	time.Sleep(50 * time.Millisecond)
	if got := len(s.Records()); got != n {
		t.Fatalf("paused, the sink went from %d to %d records", n, got)
	}
	if st := state(rt, "p"); st.Acknowledged != int64(n) || st.Read != int64(n) || st.Queue.MaxRecords == 0 {
		t.Fatalf("%+v", st)
	}
	st, err = rt.Resume("p")
	if err != nil || st.Paused || st.State != "running" {
		t.Fatalf("%+v %v", st, err)
	}
	waitFor(t, "records after the resume", func() bool { return len(s.Records()) > n+20 })
	checkExactlyOnce(t, s.Records()[:n+20], int64(n+20))
}

// TestStatusCounts: the counters, throughput, lag and checkpoint Status
// shows, and its JSON.
func TestStatusCounts(t *testing.T) {
	dir := t.TempDir()
	_, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, dir, fmt.Sprintf(`{"name": "p", "checkpoint_interval": "5ms",
		"source": {"module": "generate", "params": {"interval": "1ms", "tx_size": 2}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))
	waitFor(t, "a throughput", func() bool {
		st := state(rt, "p")
		return st.Throughput > 0 && st.Checkpoint != nil && !st.LastCommit.IsZero()
	})
	st, err := rt.Pipeline("p")
	if err != nil || st.Read < st.Acknowledged || st.Acknowledged == 0 || st.Errors != 0 || st.Failed != 0 || st.LagSeconds <= 0 {
		t.Fatalf("%+v %v", st, err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	_ = json.Unmarshal(data, &v)
	if v["name"] != "p" || v["state"] != "running" || v["checkpoint"].(map[string]any)["n"] == nil || v["queue"].(map[string]any)["max_records"] != 10000.0 {
		t.Fatalf("%s", data)
	}
	if _, ok := v["error"]; ok {
		t.Fatalf("%s", data)
	}
	if _, err := rt.Pipeline("none"); !errors.Is(err, ErrNoPipeline) {
		t.Fatal(err)
	}
}

// TestCheckpointControls: the checkpoint shown, set and reset, and a new
// snapshot, each moving it only with confirm.
func TestCheckpointControls(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, dir, fmt.Sprintf(`{"name": "p", "mode": "once",
		"source": {"module": "generate", "params": {"records": 100}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))
	finished := func(n int) func() bool {
		return func() bool { return state(rt, "p").State == "finished" && len(s.Records()) == n }
	}
	waitFor(t, "the job", finished(100))
	c, err := rt.Checkpoint("p")
	if err != nil || c == nil || string(c.Position) != `{"n":100}` || c.Job == nil || c.Job.Finished == nil {
		t.Fatalf("%+v %v", c, err)
	}

	// Set: another position needs confirm; the same one does not.
	if _, err := rt.SetCheckpoint("p", json.RawMessage(`{"n": 60}`), false); !errors.Is(err, ErrConfirm) || !strings.Contains(err.Error(), `{"n":100}`) {
		t.Fatalf("set without confirm: %v", err)
	}
	if _, err := rt.SetCheckpoint("p", json.RawMessage(`{"n" : 100}`), false); err != nil {
		t.Fatalf("set to the same: %v", err)
	}
	if _, err := rt.SetCheckpoint("p", json.RawMessage(`null`), true); err == nil {
		t.Fatal("set to null")
	}
	if _, err := rt.SetCheckpoint("p", json.RawMessage(`{"n":`), true); err == nil {
		t.Fatal("set to a broken JSON")
	}
	// The job, finished, goes on from the position set.
	waitFor(t, "the job to finish again", finished(100))
	if c, err = rt.SetCheckpoint("p", json.RawMessage(`{"n": 60}`), true); err != nil || string(c.Position) != `{"n":60}` {
		t.Fatalf("%+v %v", c, err)
	}
	waitFor(t, "the job from 60", finished(140))
	if got := ids(s.Records()[100:]); got[0] != 60 || got[39] != 99 {
		t.Fatalf("%v", got)
	}

	// Reset: needs confirm while there is a checkpoint; the job then runs
	// anew.
	if _, err := rt.ResetCheckpoint("p", false); !errors.Is(err, ErrConfirm) {
		t.Fatalf("reset without confirm: %v", err)
	}
	if _, err := rt.ResetCheckpoint("p", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the job anew", finished(240))
	if got := ids(s.Records()[140:]); got[0] != 0 || got[99] != 99 {
		t.Fatalf("%v", got)
	}

	// Snapshot: the same, at once.
	if _, err := rt.Snapshot("p", false); !errors.Is(err, ErrConfirm) {
		t.Fatalf("snapshot without confirm: %v", err)
	}
	if _, err := rt.Snapshot("p", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the snapshot", finished(340))
	for _, f := range []func() error{
		func() error { _, err := rt.Checkpoint("none"); return err },
		func() error { _, err := rt.SetCheckpoint("none", json.RawMessage(`{}`), true); return err },
		func() error { _, err := rt.ResetCheckpoint("none", true); return err },
		func() error { _, err := rt.Snapshot("none", true); return err },
		func() error { _, err := rt.Resume("none"); return err },
		func() error { _, err := rt.Test(context.Background(), "none"); return err },
		func() error { _, err := rt.DryRun(context.Background(), "none", DryRunOptions{}); return err },
	} {
		if err := f(); !errors.Is(err, ErrNoPipeline) {
			t.Errorf("%v", err)
		}
	}
}

// TestSnapshotOfAScheduledJob: a new snapshot runs a job at once, without
// waiting for its schedule; a reset without a checkpoint needs no confirm.
func TestSnapshotOfAScheduledJob(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, dir, fmt.Sprintf(`{"name": "p", "mode": "once", "job": {"schedule": "0 0 1 1 *"},
		"source": {"module": "generate", "params": {"records": 10}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))
	waitFor(t, "the schedule", func() bool { return state(rt, "p").State == "scheduled" })
	if _, err := rt.ResetCheckpoint("p", false); err != nil {
		t.Fatalf("a reset without a checkpoint: %v", err)
	}
	waitFor(t, "the schedule again", func() bool { return state(rt, "p").State == "scheduled" })
	if len(s.Records()) != 0 {
		t.Fatal("a reset ran a scheduled job")
	}
	if _, err := rt.Snapshot("p", false); err != nil {
		t.Fatalf("a snapshot without a checkpoint: %v", err)
	}
	waitFor(t, "the snapshot", func() bool { return len(s.Records()) == 10 && state(rt, "p").State == "scheduled" })
}

// TestResumeAStoppedPipeline: resume starts again a pipeline on_error
// stopped, from its checkpoint.
func TestResumeAStoppedPipeline(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	s.SetHook(func(_ context.Context, batch []*Record) error {
		for i, r := range batch {
			if keyID(r) == 30 {
				return &BatchError{Index: i, Err: Permanent(errors.New("refused"))}
			}
		}
		return nil
	})
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, dir, fmt.Sprintf(`{"name": "p", "mode": "once",
		"source": {"module": "generate", "params": {"records": 50}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))
	waitFor(t, "the stop", func() bool { return state(rt, "p").State == "stopped" })
	if st := state(rt, "p"); st.Errors == 0 || !strings.Contains(st.Err, "refused") || st.ErrAt.IsZero() {
		t.Fatalf("%+v", st)
	}
	s.SetHook(nil)
	if _, err := rt.Resume("p"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the job", func() bool { return state(rt, "p").State == "finished" })
	got := ids(s.Records())
	if got[len(got)-1] != 49 || len(got) < 50 {
		t.Fatalf("%v", got)
	}
}

// TestDryRun: a dry run reads through the transforms and leaves the
// checkpoint and the sinks as they were.
func TestDryRun(t *testing.T) {
	dir := t.TempDir()
	s, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, dir, fmt.Sprintf(`{"name": "p", "mode": "once", "on_error": "skip",
		"source": {"module": "generate", "params": {"records": 20}},
		"transforms": [{"module": "test", "params": {"drop": 3, "split": 5, "fail": 4}}],
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))
	waitFor(t, "the job", func() bool { return state(rt, "p").State == "finished" })
	before, n := checkpointFile(t, dir, "p"), len(s.Records())

	d, err := rt.DryRun(context.Background(), "p", DryRunOptions{FromStart: true, Limit: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Records) != 6 || d.Ended || d.TimedOut || d.From != nil {
		t.Fatalf("%+v", d)
	}
	outs := []int{0, 1, 1, 0, 0, 2} // 0 and 3 dropped, 4 failed, 5 split
	for i, r := range d.Records {
		if len(r.Out) != outs[i] || (i == 4) != (r.Err != "") || !bytes.Contains(r.In, fmt.Appendf(nil, `"id":{"int":%d}`, i)) {
			t.Errorf("record %d: %d out, error %q, %s", i, len(r.Out), r.Err, r.In)
		}
	}
	if !strings.Contains(d.Records[4].Err, "transform 0 (test)") {
		t.Errorf("%s", d.Records[4].Err)
	}
	// From the checkpoint, at the source's end: nothing, and the end.
	d, err = rt.DryRun(context.Background(), "p", DryRunOptions{})
	if err != nil || len(d.Records) != 0 || !d.Ended || string(d.From) != `{"n":20}` {
		t.Fatalf("%+v %v", d, err)
	}
	if !bytes.Equal(before, checkpointFile(t, dir, "p")) || len(s.Records()) != n {
		t.Fatal("the dry run changed the checkpoint or the sink")
	}

	// A stream with no new records times out with what it read.
	c, err := ParsePipeline([]byte(`{"name": "q", "source": {"module": "generate", "params": {"records": 3}},
		"sinks": [{"name": "m", "sink": {"module": "untested"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d, err = DryRunPipeline(context.Background(), c, nil, nil, DryRunOptions{Limit: 10})
	if err != nil || len(d.Records) != 3 || !d.Ended {
		t.Fatalf("%+v %v", d, err)
	}
	c, err = ParsePipeline([]byte(`{"name": "q", "source": {"module": "generate", "params": {"interval": "1h"}},
		"sinks": [{"name": "m", "sink": {"module": "untested"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d, err = DryRunPipeline(context.Background(), c, nil, nil, DryRunOptions{Limit: 10, Timeout: 20 * time.Millisecond})
	if err != nil || len(d.Records) != 1 || !d.TimedOut || d.Ended {
		t.Fatalf("%+v %v", d, err)
	}
	// A source that fails says so.
	c, err = ParsePipeline([]byte(`{"name": "q", "source": {"module": "generate", "params": {"records": 3}},
		"sinks": [{"name": "m", "sink": {"module": "untested"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if d, err = DryRunPipeline(context.Background(), c, Position(`{"n": "x"}`), nil, DryRunOptions{}); err != nil || d.SourceErr == "" {
		t.Fatalf("%+v %v", d, err)
	}
}

// TestParsePipeline: a pipeline posted is checked as a config's is.
func TestParsePipeline(t *testing.T) {
	for _, bad := range []string{``, `[]`, `{"name": "x"`, `{"name": "x"}, {"name": "y"}`, `{"name": "bad name", "source": {"module": "generate"}, "sinks": [{"name": "m", "sink": {"module": "memory"}}]}`} {
		if _, err := ParsePipeline([]byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

// TestConnectionTests: each connector's test, through the running modules
// or a pipeline's config.
func TestConnectionTests(t *testing.T) {
	dir := t.TempDir()
	_, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	p := fmt.Sprintf(`{"name": "p", "on_error": "dead_letter",
		"source": {"module": "untestable", "params": {"records": 1}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}, {"name": "u", "sink": {"module": "untested"}},
			{"name": "w", "sink": {"module": "warned"}}],
		"dead_letter": {"name": "d", "sink": {"module": "stdout"}}}`, sn)
	applyOne(t, rt, dir, p)
	check := func(res []TestResult, err error) {
		t.Helper()
		if err != nil || len(res) != 5 {
			t.Fatalf("%+v %v", res, err)
		}
		want := []TestResult{
			{Part: "source", Module: "untestable", Tested: true, Err: "connection refused"},
			{Part: "sink m", Module: "memory", Tested: true, OK: true},
			{Part: "sink u", Module: "untested"},
			{Part: "sink w", Module: "warned", Tested: true, OK: true, Warnings: []string{"a column loses data"}},
			{Part: "dead_letter d", Module: "stdout", Tested: true, OK: true},
		}
		for i, w := range want {
			g := res[i]
			if g.Part != w.Part || g.Module != w.Module || g.Tested != w.Tested || g.OK != w.OK || w.Err != "" && g.Err != w.Err || !w.Tested && g.Err == "" ||
				strings.Join(g.Warnings, "|") != strings.Join(w.Warnings, "|") {
				t.Errorf("%d: %+v, want %+v", i, g, w)
			}
		}
	}
	check(rt.Test(context.Background(), "p"))
	c, err := ParsePipeline([]byte(p))
	if err != nil {
		t.Fatal(err)
	}
	check(TestPipeline(context.Background(), c, nil))
	c.Source.Module = "none"
	if _, err := TestPipeline(context.Background(), c, nil); err == nil {
		t.Fatal("a source that is not")
	}
}

// TestRegistry: the runtime is found by its engine.
func TestRegistry(t *testing.T) {
	var e fib.Engine
	rt := NewRuntime(Options{})
	if Lookup(&e) != nil {
		t.Fatal("found before")
	}
	Register(&e, rt)
	if Lookup(&e) != rt {
		t.Fatal("not found")
	}
	Unregister(&e, NewRuntime(Options{}))
	if Lookup(&e) != rt {
		t.Fatal("another's unregister took it")
	}
	Unregister(&e, rt)
	if Lookup(&e) != nil {
		t.Fatal("found after")
	}
}
