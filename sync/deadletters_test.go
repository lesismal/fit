package fitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// holdSource makes records records, then waits until it is stopped: a
// stream caught up.
type holdSource struct {
	Records int64 `json:"records"`
	// Lag, when set, is told as the source's lag in offsets.
	Lag int64 `json:"lag"`
}

func (h *holdSource) Run(ctx context.Context, r *Run) error {
	if h.Lag > 0 {
		r.ReportLag(h.Lag, LagOffsets)
	}
	if h.Records > 0 {
		if err := (&Generate{Records: h.Records}).Run(ctx, r); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func init() {
	module.Register(module.Info{ID: Sources + ".hold", New: func() any { return new(holdSource) }, Doc: "Tests'."})
}

// failID returns a memory hook failing the record of id for good, while on
// is true.
func failID(id int64, on *atomic.Bool) func(context.Context, []*Record) error {
	return func(_ context.Context, batch []*Record) error {
		if !on.Load() {
			return nil
		}
		for i, r := range batch {
			if keyID(r) == id {
				return &BatchError{Index: i, Err: Permanent(errors.New("a bad row"))}
			}
		}
		return nil
	}
}

func countID(recs []*Record, id int64) int {
	n := 0
	for _, r := range recs {
		if keyID(r) == id {
			n++
		}
	}
	return n
}

// TestDeadLetterReplay: a record a sink fails is kept as a dead letter,
// listed, and once the cause is fixed, replayed to that sink once, and
// dropped; the other sink does not get it again.
func TestDeadLetterReplay(t *testing.T) {
	s, sn := memStore(t)
	o, on := memStore(t)
	var failing atomic.Bool
	failing.Store(true)
	s.SetHook(failID(7, &failing))
	dir := t.TempDir()
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, dir, fmt.Sprintf(`{"name": "p", "on_error": "dead_letter", "retry": {"max_attempts": 2, "backoff": "1ms"},
		"source": {"module": "hold", "params": {"records": 20}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}},
		          {"name": "o", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}],
		"dead_letter": {"name": "dl", "sink": {"module": "dead_letters"}}}`, sn, on))
	waitFor(t, "the records", func() bool { return state(rt, "p").Acknowledged == 20 })
	st := state(rt, "p")
	if st.DeadLetters == nil || st.DeadLetters.Written != 1 || st.DeadLetters.Store == nil || st.DeadLetters.Store.Kept != 1 ||
		st.DeadLetters.Store.Path != filepath.Join(dir, "p.dead_letters.jsonl") {
		t.Fatalf("%+v", st.DeadLetters)
	}
	page, err := rt.DeadLetters("p", 0, 0)
	if err != nil || len(page.Letters) != 1 {
		t.Fatalf("%v %+v", err, page)
	}
	l := page.Letters[0]
	var rec Record
	if err := rec.UnmarshalJSON(l.Record); err != nil || keyID(&rec) != 7 || l.At != "sink m" || l.Attempts != 1 || !strings.Contains(l.Error, "a bad row") {
		t.Fatalf("%v %+v", err, l)
	}
	if countID(s.Records(), 7) != 0 || countID(o.Records(), 7) != 1 {
		t.Fatal("before the replay")
	}

	// Still failing: kept.
	r, err := rt.ReplayDeadLetters(context.Background(), "p", nil)
	if err != nil || len(r.Replayed) != 0 || len(r.Failed) != 1 || !strings.Contains(r.Failed[0].Err, "a bad row") {
		t.Fatalf("%v %+v", err, r)
	}
	failing.Store(false)
	r, err = rt.ReplayDeadLetters(context.Background(), "p", []uint64{l.ID, 99})
	if err != nil || len(r.Replayed) != 1 || r.Replayed[0] != l.ID || len(r.Failed) != 1 || r.Failed[0].ID != 99 {
		t.Fatalf("%v %+v", err, r)
	}
	if countID(s.Records(), 7) != 1 || countID(o.Records(), 7) != 1 {
		t.Fatalf("after the replay: %d, %d", countID(s.Records(), 7), countID(o.Records(), 7))
	}
	waitFor(t, "the restart", func() bool { return state(rt, "p").State == "running" })
	st = state(rt, "p")
	if st.DeadLetters.Replayed != 1 || st.DeadLetters.Store.Kept != 0 {
		t.Fatalf("%+v", st.DeadLetters)
	}
	// Nothing left: no restart, nothing written.
	if r, err := rt.ReplayDeadLetters(context.Background(), "p", nil); err != nil || len(r.Replayed)+len(r.Failed) != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	if countID(s.Records(), 7) != 1 {
		t.Fatal("written twice")
	}
	// Kept across a restart of fit: the file is read again.
	data, err := os.ReadFile(filepath.Join(dir, "p.dead_letters.jsonl"))
	if err != nil || !strings.Contains(string(data), `"removed":[1]`) {
		t.Fatalf("%v %s", err, data)
	}
}

// TestDeadLetterReplayThroughTransforms: a record a transform failed goes
// through the transforms again, once they are fixed, to every sink; the
// dead letters stay across a reload that changes the pipeline.
func TestDeadLetterReplayThroughTransforms(t *testing.T) {
	s, sn := memStore(t)
	o, on := memStore(t)
	dir := t.TempDir()
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	pipeline := func(fail int) string {
		return fmt.Sprintf(`{"name": "p", "on_error": "dead_letter",
		"source": {"module": "hold", "params": {"records": 10}},
		"transforms": [{"module": "test", "params": {"fail": %d, "split": 3}}],
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}},
		          {"name": "o", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}],
		"dead_letter": {"name": "dl", "sink": {"module": "dead_letters", "params": {"path": "letters.jsonl", "max_records": 5}}}}`, fail, sn, on)
	}
	applyOne(t, rt, dir, pipeline(6))
	waitFor(t, "the records", func() bool { return state(rt, "p").Acknowledged == 10 })
	if countID(s.Records(), 6) != 0 {
		t.Fatal("6 written")
	}
	applyOne(t, rt, dir, pipeline(0))
	page, err := rt.DeadLetters("p", 0, 10)
	if err != nil || len(page.Letters) != 1 || !strings.HasPrefix(page.Letters[0].At, "transform 0 (test)") || page.Store.MaxRecords != 5 {
		t.Fatalf("%v %+v", err, page)
	}
	r, err := rt.ReplayDeadLetters(context.Background(), "p", nil)
	if err != nil || len(r.Replayed) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	// 6 splits in two.
	if countID(s.Records(), 6) != 2 || countID(o.Records(), 6) != 2 {
		t.Fatalf("%d, %d", countID(s.Records(), 6), countID(o.Records(), 6))
	}
	if _, err := os.Stat(filepath.Join(dir, "letters.jsonl")); err != nil {
		t.Fatal(err)
	}
}

// TestDeadLetterControls: what the controls refuse, and drop.
func TestDeadLetterControls(t *testing.T) {
	_, sn := memStore(t)
	_, dn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	dir := t.TempDir()
	if err := rt.Apply(syncConfig(t, dir, fmt.Sprintf(`{"name": "mem", "on_error": "dead_letter", "source": {"module": "hold"},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}],
		"dead_letter": {"name": "dl", "sink": {"module": "memory", "params": {"store": %q}}}}`, sn, dn),
		fmt.Sprintf(`{"name": "p", "on_error": "dead_letter", "source": {"module": "hold"},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}],
		"dead_letter": {"name": "dl", "sink": {"module": "dead_letters"}}}`, sn))); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		func() error { _, err := rt.DeadLetters("mem", 0, 0); return err }(),
		func() error { _, err := rt.DropDeadLetters("mem", nil); return err }(),
		func() error { _, err := rt.ReplayDeadLetters(context.Background(), "mem", nil); return err }(),
	} {
		if !errors.Is(err, ErrNoDeadLetters) {
			t.Errorf("%v", err)
		}
	}
	if _, err := rt.DeadLetters("none", 0, 0); !errors.Is(err, ErrNoPipeline) {
		t.Error(err)
	}
	if st := state(rt, "mem"); st.DeadLetters == nil || st.DeadLetters.Store != nil {
		t.Errorf("%+v", st.DeadLetters)
	}

	// Letters of every kind, written to the file as a sink would.
	_, st, err := func() (*pipe, *dlStore, error) { rt.mu.Lock(); defer rt.mu.Unlock(); return rt.deadLetterStore("p") }()
	if err != nil {
		t.Fatal(err)
	}
	letter := func(at string, rec string) []byte {
		r := &Record{Op: OpMessage, After: Map(Field{"error", String("e")}, Field{"at", String(at)}, Field{"attempts", Int(3)})}
		if rec != "" {
			r.After = Map(append(r.After.Fields(), Field{"record", JSON([]byte(rec))})...)
		}
		b, _ := r.AppendJSON(nil)
		return b
	}
	good := `{"namespace": "n", "op": "insert", "key": {"int": 1}}`
	if err := st.add([][]byte{
		[]byte(`{"op": "message", "after": "not a letter"}`),
		letter("sink gone", good),
		letter("somewhere", good),
		letter("sink m", `[1, 2]`),
		letter("sink m", good),
	}); err != nil {
		t.Fatal(err)
	}
	page, err := rt.DeadLetters("p", 1, 2)
	if err != nil || len(page.Letters) != 2 || page.Letters[0].ID != 2 || !page.More || page.Store.Kept != 5 {
		t.Fatalf("%v %+v", err, page)
	}
	r, err := rt.ReplayDeadLetters(context.Background(), "p", nil)
	if err != nil || len(r.Replayed) != 1 || r.Replayed[0] != 5 || len(r.Failed) != 4 {
		t.Fatalf("%v %+v", err, r)
	}
	for i, want := range []string{"holds no record", "no sink gone", "not a sink or a transform", "holds no record"} {
		if !strings.Contains(r.Failed[i].Err, want) {
			t.Errorf("%d: %s", i, r.Failed[i].Err)
		}
	}
	if n, err := rt.DropDeadLetters("p", []uint64{1, 42}); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := rt.DropDeadLetters("p", nil); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := rt.DropDeadLetters("p", nil); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}

// TestReplayToRetries: a replay's write is tried again as retry says, from
// the record that failed, and a replay interrupted keeps what it did not
// write.
func TestReplayToRetries(t *testing.T) {
	s, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, t.TempDir(), fmt.Sprintf(`{"name": "p", "on_error": "dead_letter", "retry": {"max_attempts": 3, "backoff": "1ms"},
		"source": {"module": "hold"},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_records": 2}}],
		"dead_letter": {"name": "dl", "sink": {"module": "dead_letters"}}}`, sn))
	rt.mu.Lock()
	p := rt.pipes["p"]
	rt.mu.Unlock()
	recs := func(n int) []replayed {
		var out []replayed
		for i := range n {
			out = append(out, replayed{GenerateRecord("", int64(i), 0, 1), i})
		}
		return out
	}
	calls := 0
	s.SetHook(func(_ context.Context, batch []*Record) error {
		calls++
		switch calls {
		case 1:
			return errors.New("once")
		case 2:
			return &BatchError{Index: 1, Err: errors.New("the second")}
		}
		return nil
	})
	why := make([]string, 5)
	p.replayTo(context.Background(), 0, recs(5), why)
	if strings.Join(why, "") != "" || len(s.Records()) != 5 {
		t.Fatalf("%q %v", why, ids(s.Records()))
	}
	m := p.sinkMeter(0)
	if m.retries.Load() != 2 || m.errors.Load() != 2 || m.records.Load() != 5 {
		t.Fatalf("%d %d %d", m.retries.Load(), m.errors.Load(), m.records.Load())
	}
	s.SetHook(func(context.Context, []*Record) error { return errors.New("down") })
	why = make([]string, 2)
	p.replayTo(context.Background(), 0, recs(2), why)
	if !strings.Contains(why[0], "sink m: down") || !strings.Contains(why[1], "down") {
		t.Fatalf("%q", why)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	why = make([]string, 2)
	p.replayTo(ctx, 0, recs(2), why)
	if !strings.Contains(why[1], "interrupted") {
		t.Fatalf("%q", why)
	}
}

// TestDeadLettersSink: the sink's file, its bounds, and what it refuses.
func TestDeadLettersSink(t *testing.T) {
	dir := t.TempDir()
	rec := func(n int) *Record { return GenerateRecord("", int64(n), 0, 1) }
	d := &DeadLetters{MaxRecords: 2}
	if err := d.Write(context.Background(), []*Record{rec(0)}); err == nil {
		t.Error("an unbound sink wrote")
	}
	if err := d.Test(context.Background()); err != nil {
		t.Error(err)
	}
	d.bind(dir, "p")
	defer d.Cleanup()
	if err := d.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Write(context.Background(), []*Record{rec(0), rec(1), rec(2)}); !errors.Is(err, ErrDeadLettersFull) {
		t.Fatalf("%v", err)
	} else if be := (*BatchError)(nil); !errors.As(err, &be) || be.Index != 2 {
		t.Fatalf("%v", err)
	}
	if err := d.Write(context.Background(), []*Record{rec(3)}); !errors.Is(err, ErrDeadLettersFull) {
		t.Fatalf("%v", err)
	}
	// A second sink of the same file shares it; its bounds are the last.
	d2 := &DeadLetters{Path: filepath.Join(dir, "p.dead_letters.jsonl"), WhenFull: "drop_oldest", MaxRecords: 2, MaxBytes: 2000}
	d2.bind("elsewhere", "q")
	if err := d2.Write(context.Background(), []*Record{rec(4), rec(5), rec(6)}); err != nil {
		t.Fatal(err)
	}
	st := d.status()
	if st.Kept != 2 || st.Dropped != 3 {
		t.Fatalf("%+v", st)
	}
	big := &Record{After: String(strings.Repeat("x", 3000))}
	if err := d2.Write(context.Background(), []*Record{big}); err == nil || !IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	if err := d2.Write(context.Background(), []*Record{{Position: Position("not JSON")}}); err == nil || !IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	d2.Cleanup()
	d.Cleanup()

	// Read again: what was kept, a last line cut short dropped.
	path := filepath.Join(dir, "p.dead_letters.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id": 99, "letter`)
	_ = f.Close()
	s, err := acquireStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ls, _ := s.pick(nil)
	if len(ls) != 2 || ls[0].id != 4 || ls[1].id != 5 || s.next != 6 {
		t.Fatalf("%+v next %d", ls, s.next)
	}
	// Compacted when the removed take more room than the kept.
	saved := compactSlack
	compactSlack = 0
	defer func() { compactSlack = saved }()
	if n, err := s.remove([]uint64{4}); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "\n") != 1 || strings.Contains(string(data), "removed") {
		t.Fatalf("%s", data)
	}
	if err := s.add([][]byte{[]byte(`{"op": "message"}`)}); err != nil {
		t.Fatal(err)
	}
	s.release()
	s, err = acquireStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if ls, _ := s.pick(nil); len(ls) != 2 || ls[1].id != 6 {
		t.Fatalf("%+v", ls)
	}
	s.release()

	// A line broken in the middle is refused, and so the sink's writes.
	if err := os.WriteFile(path, []byte("{bad\n"+`{"removed": [1]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireStore(path); err == nil {
		t.Fatal("a broken file read")
	}
	d3 := &DeadLetters{}
	d3.bind(dir, "p")
	if err := d3.Test(context.Background()); err == nil {
		t.Fatal("test of a broken file")
	}
	if st := d3.status(); st.Err == "" {
		t.Fatalf("%+v", st)
	}
	if err := d3.Write(context.Background(), []*Record{rec(0)}); err == nil {
		t.Fatal("wrote to a broken file")
	}
	// Somewhere no file can be.
	if _, err := acquireStore(filepath.Join(path, "x", "y")); err == nil {
		t.Fatal("a file under a file")
	}
	if _, err := json.Marshal(d3.status()); err != nil {
		t.Fatal(err)
	}
}

// TestDeadLettersOnlyAsDeadLetter: dead_letters among a pipeline's sinks is
// refused.
func TestDeadLettersOnlyAsDeadLetter(t *testing.T) {
	c, err := config.Load([]byte(`{"sync": {"state_dir": ".", "pipelines": [{"name": "p", "source": {"module": "hold"},
		"sinks": [{"name": "d", "sink": {"module": "dead_letters"}}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(c.Sync); err == nil || !strings.Contains(err.Error(), "only a pipeline's dead_letter sink") {
		t.Fatalf("%v", err)
	}
}

// TestDeadLettersWriteFails: a file that cannot be written fails the add
// and the removal, and keeps what it had.
func TestDeadLettersWriteFails(t *testing.T) {
	s, err := acquireStore(filepath.Join(t.TempDir(), "d.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.add([][]byte{[]byte(`{"op": "message"}`)}); err != nil {
		t.Fatal(err)
	}
	_ = s.f.Close()
	if err := s.add([][]byte{[]byte(`{"op": "message"}`)}); err == nil {
		t.Error("added to a closed file")
	}
	if n, err := s.remove([]uint64{1}); err == nil || n != 0 {
		t.Error("removed from a closed file")
	}
	if n, err := s.remove([]uint64{7}); err != nil || n != 0 {
		t.Error(n, err)
	}
	if st := s.status(); st.Kept != 1 {
		t.Fatalf("%+v", st)
	}
	dlMu.Lock()
	delete(dlStores, s.path)
	dlMu.Unlock()
}
