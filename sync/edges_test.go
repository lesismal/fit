package fitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
)

// planner is the tests' Chunker whose plan fails, or holds more chunks
// than a job may.
type planner struct {
	Fail bool `json:"fail"`
	N    int  `json:"chunks"`
}

func (p *planner) Chunks(context.Context) ([]Chunk, error) {
	if p.Fail {
		return nil, errors.New("the table is gone")
	}
	return make([]Chunk, p.N), nil
}

func (p *planner) ReadChunk(context.Context, Chunk, Position, func(*Record) error) error { return nil }

func (p *planner) Run(context.Context, *Run) error { return nil }

func init() {
	module.Register(module.Info{ID: Sources + ".planner", New: func() any { return new(planner) }, Doc: "Tests'."})
	// Modules of the wrong kind, each in the namespace of another.
	module.Register(module.Info{ID: Sources + ".wrongsource", New: func() any { return new(Memory) }, Doc: "Tests'."})
	module.Register(module.Info{ID: Transforms + ".wrongtransform", New: func() any { return new(Memory) }, Doc: "Tests'."})
	module.Register(module.Info{ID: Sinks + ".wrongsink", New: func() any { return new(Generate) }, Doc: "Tests'."})
}

// syncBuffer is a log's output read while the pipeline writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func testLog(t *testing.T) (*logs.Logger, *syncBuffer) {
	t.Helper()
	out := new(syncBuffer)
	root, err := logs.New(nil, out, out)
	if err != nil {
		t.Fatal(err)
	}
	return root.Logger(config.LoggerSync), out
}

func TestRecordJSONMalformed(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{`{"source":`, "source: EOF"},
		{`{"source":5}`, "source: 5 is not a string"},
		{`{"key":`, "key: EOF"},
		{`{"key":{}}`, "key: } where a name was expected"},
		{`{"key":{"json":`, "key: json: unexpected EOF"},
		{`{"key":{"map":{"a":{"int":`, "key: map: a: int: EOF"},
		{`{"after":{"map":{"a":1,`, "after: map: a: the number 1 does not say its kind"},
		{`{"before":[null,`, "before: unexpected end of JSON input"},
		{`{"tx":5}`, "tx: json: cannot unmarshal number"},
		{`{"position":`, "position: unexpected EOF"},
		{`{"source":"a"`, "unexpected end of JSON input"},
		{`{"source":"a"} {}`, "more after the value"},
	} {
		var r Record
		err := r.UnmarshalJSON([]byte(tc.doc))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.doc, err, tc.want)
		}
	}
	// What cannot be written says where.
	bad := JSON([]byte("{bad"))
	if _, err := Array(Int(1), bad).AppendJSON(nil); err == nil {
		t.Error("an array holding bad JSON was written")
	}
	if _, err := (Value{kind: 99}).AppendJSON(nil); err == nil || !strings.Contains(err.Error(), "kind 99") {
		t.Errorf("%v", err)
	}
	if _, err := appendPlain(nil, Value{kind: 99}); err == nil {
		t.Error("a value of no kind was written plain")
	}
	recs := []*Record{{Op: OpInsert, After: Int(1)}, {Op: OpInsert, After: bad}}
	if _, err := AppendBatchJSON(nil, recs); err == nil || !strings.HasPrefix(err.Error(), "record 1: ") {
		t.Errorf("%v", err)
	}
	if _, err := (&JSONCodec{Form: "record"}).Encode(nil, recs); err == nil {
		t.Error("a bad record was encoded")
	}
	for _, doc := range []string{`1 2`, `[1,`, `{"a":[`} {
		if _, err := ParsePlain([]byte(doc)); err == nil {
			t.Errorf("%s: no error", doc)
		}
	}
}

func TestLaneOf(t *testing.T) {
	var buf []byte
	key := func(id int64) *Record { return &Record{Key: Map(Field{"id", Int(id)})} }
	if got := laneOf(key(5), 1, &buf); got != 0 {
		t.Fatalf("one writer: lane %d", got)
	}
	// A key always goes to the same lane, and keys spread over all of them.
	seen := map[int]bool{}
	for id := range int64(64) {
		l := laneOf(key(id), 4, &buf)
		if l < 0 || l >= 4 || laneOf(key(id), 4, &buf) != l {
			t.Fatalf("id %d: lane %d, then %d", id, l, laneOf(key(id), 4, &buf))
		}
		seen[l] = true
	}
	if len(seen) != 4 {
		t.Fatalf("64 keys went to lanes %v", seen)
	}
	// Without a key, by the record's sequence.
	for seq := range uint64(8) {
		if got := laneOf(&Record{seq: seq}, 3, &buf); got != int(seq%3) {
			t.Fatalf("seq %d: lane %d", seq, got)
		}
	}
	// A key that cannot be written goes to the first.
	if got := laneOf(&Record{Key: JSON([]byte("{bad"))}, 4, &buf); got != 0 {
		t.Fatalf("a bad key: lane %d", got)
	}
}

func TestSmallValueEdges(t *testing.T) {
	if Permanent(nil) != nil {
		t.Error("Permanent(nil) is an error")
	}
	if MemoryStore("") != MemoryStore("default") {
		t.Error("the unnamed store is not the default one")
	}
	if Array(Int(1), Int(2)).Equal(Array(Int(1), Int(3))) || !Array(Int(1)).Equal(Array(Int(1))) {
		t.Error("arrays compare wrong")
	}
	e := &Envelope{}
	if v := e.metaValue(&Record{}, "commit_time"); !v.IsNull() {
		t.Errorf("commit_time of a record without one: %v", v)
	}
	if v := e.metaValue(&Record{}, "tx_id"); !v.IsNull() {
		t.Errorf("tx_id of a record without one: %v", v)
	}
	for _, tc := range []struct {
		v    Value
		to   Kind
		want Value
	}{
		{Float(3), KindInt, Int(3)},
		{Float(3), KindUint, Uint(3)},
		{Float(-2), KindInt, Int(-2)},
		{String("false"), KindBool, Bool(false)},
	} {
		got, err := convert(tc.v, tc.to)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%v to %s: %v %v", tc.v, tc.to, got, err)
		}
	}
	g := &Generate{Records: MaxChunks + 1, ChunkRecords: 1}
	if _, err := g.Chunks(context.Background()); err == nil || !strings.Contains(err.Error(), "chunks") {
		t.Errorf("%v", err)
	}
	emit := func(*Record) error { return nil }
	if err := g.ReadChunk(context.Background(), Chunk{Lo: []byte("x"), Hi: []byte("1")}, nil, emit); err == nil || !strings.Contains(err.Error(), "the chunk") {
		t.Errorf("%v", err)
	}
	if err := g.ReadChunk(context.Background(), Chunk{Lo: []byte("0"), Hi: []byte("1")}, Position(`[]`), emit); err == nil || !strings.Contains(err.Error(), "the position") {
		t.Errorf("%v", err)
	}
}

// TestLoadWrongKinds: a module of another kind in a pipeline is refused,
// and a config with one such pipeline starts none of the others.
func TestLoadWrongKinds(t *testing.T) {
	dir := t.TempDir()
	_, sn := memStore(t)
	good := fmt.Sprintf(`{"name": "good", "source": {"module": "generate", "params": {"interval": "1ms"}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, sn)
	bad := `{"name": "bad", "source": {"module": "wrongsource"}, "transforms": [{"module": "wrongtransform"}],
		"sinks": [{"name": "m", "sink": {"module": "wrongsink"}}]}`
	c, err := config.Load(fmt.Appendf(nil, `{"sync": {"state_dir": %q, "pipelines": [%s, %s]}}`, dir, good, bad))
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	err = rt.Apply(c.Sync)
	for _, want := range []string{"wrongsource is not a source", "wrongtransform is not a transform", "wrongsink is not a sink"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v, want %q", err, want)
		}
	}
	if st := rt.Status(); len(st) != 0 {
		t.Fatalf("%+v runs", st)
	}
	if _, err := RunPipeline(context.Background(), c.Sync, "bad", Options{}); err == nil || !strings.Contains(err.Error(), "is not a source") {
		t.Errorf("%v", err)
	}
	if _, err := RunPipeline(context.Background(), nil, "bad", Options{}); err == nil {
		t.Error("a nil config ran")
	}
}

// TestStatusSorted: the pipelines' states come by name.
func TestStatusSorted(t *testing.T) {
	dir := t.TempDir()
	var ps []string
	for _, n := range []string{"c", "a", "d", "b"} {
		_, sn := memStore(t)
		ps = append(ps, fmt.Sprintf(`{"name": %q, "source": {"module": "generate", "params": {"interval": "10ms"}},
			"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, n, sn))
	}
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, dir, ps...)); err != nil {
		t.Fatal(err)
	}
	for range 20 { // the pipelines are kept in a map: each call orders them anew
		var names []string
		for _, s := range rt.Status() {
			names = append(names, s.Name)
		}
		if !slices.Equal(names, []string{"a", "b", "c", "d"}) {
			t.Fatalf("%v", names)
		}
	}
}

// TestJobPlanFails: a job whose plan fails, or is too big, does not start
// and says why.
func TestJobPlanFails(t *testing.T) {
	dir := t.TempDir()
	_, sn := memStore(t)
	cfg := syncConfig(t, dir,
		fmt.Sprintf(`{"name": "fails", "mode": "once", "retry": {"max_attempts": 1, "backoff": "1ms"}, "source": {"module": "planner", "params": {"fail": true}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, sn),
		fmt.Sprintf(`{"name": "big", "mode": "once", "retry": {"max_attempts": 1, "backoff": "1ms"}, "source": {"module": "planner", "params": {"chunks": %d}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, MaxChunks+1, sn))
	for name, want := range map[string]string{"fails": "planning the chunks: the table is gone", "big": "past the 100000 allowed"} {
		st, err := RunPipeline(context.Background(), cfg, name, Options{SourceAttempts: 1})
		if err != nil || st.State == "finished" || !strings.Contains(st.Err, want) {
			t.Errorf("%s: %v %+v, want %q", name, err, st, want)
		}
		if _, err := os.Stat(filepath.Join(dir, name+".json")); !os.IsNotExist(err) {
			t.Errorf("%s: a checkpoint was saved: %v", name, err)
		}
	}
}

// TestScheduleNamesNoTime: a job whose schedule never comes waits, saying
// so, until it is stopped.
func TestScheduleNamesNoTime(t *testing.T) {
	log, out := testLog(t)
	_, sn := memStore(t)
	rt := NewRuntime(Options{Log: log, Clock: &fakeClock{now: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)}})
	if err := rt.Apply(syncConfig(t, t.TempDir(), fmt.Sprintf(`{"name": "never", "mode": "once", "job": {"schedule": "0 0 30 2 *"},
		"source": {"module": "generate", "params": {"records": 1}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, sn))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the warning", func() bool { return strings.Contains(out.String(), "names no time in the next five years") })
	if st := state(rt, "never"); st.State == "scheduled" || st.State == "running" {
		t.Fatalf("%+v", st)
	}
	stopped := make(chan struct{})
	go func() {
		rt.Close(context.Background())
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Minute):
		t.Fatal("the pipeline waiting for no time did not stop")
	}
}

// TestCheckpointUnsaved: a checkpoint that cannot be saved is logged and
// kept, and saved once it can be.
func TestCheckpointUnsaved(t *testing.T) {
	dir := t.TempDir()
	// The store's own errors: its directory is a file; its file a directory.
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&store{dir: file, name: "p"}).save(Position(`{}`), nil); err == nil {
		t.Error("saved under a file")
	}
	s := &store{dir: filepath.Join(dir, "state"), name: "q"}
	if err := os.MkdirAll(filepath.Join(s.path(), "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.save(Position(`{}`), nil); err == nil {
		t.Error("saved over a directory")
	}
	if left, _ := filepath.Glob(filepath.Join(s.dir, ".q.json.tmp-*")); len(left) != 0 {
		t.Fatalf("%v left", left)
	}

	log, out := testLog(t)
	_, sn := memStore(t)
	rt := NewRuntime(Options{Log: log})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, s.dir, fmt.Sprintf(`{"name": "p", "checkpoint_interval": "1ms",
		"source": {"module": "generate", "params": {"interval": "1ms"}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, "p.json")
	waitFor(t, "a checkpoint", func() bool { n, _ := readN(s.dir, "p"); return n > 0 })
	// The checkpoint's file becomes a directory, which a save cannot
	// replace: put in between two saves.
	waitFor(t, "the file to become a directory", func() bool {
		_ = os.Remove(path)
		return os.MkdirAll(filepath.Join(path, "x"), 0o700) == nil
	})
	waitFor(t, "the failed save logged", func() bool { return strings.Contains(out.String(), "saving its checkpoint") })
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the checkpoint saved again", func() bool { n, err := readN(s.dir, "p"); return err == nil && n > 0 })
}
