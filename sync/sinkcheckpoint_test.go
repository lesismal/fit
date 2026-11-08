package fitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lesismal/fit/module"
)

// keptDB is a keeper sink's server: the records written and the
// checkpoint, changed together, as one transaction would.
type keptDB struct {
	mu      sync.Mutex
	recs    []*Record
	pos     Position
	lineage string
	err     error
	reads   int
	block   chan struct{}
}

var keptDBs sync.Map

func keptDBOf(name string) *keptDB {
	v, _ := keptDBs.LoadOrStore(name, &keptDB{})
	return v.(*keptDB)
}

func (db *keptDB) ids() []int64 {
	db.mu.Lock()
	defer db.mu.Unlock()
	return ids(db.recs)
}

func (db *keptDB) readCount() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.reads
}

func (db *keptDB) set(f func(db *keptDB)) {
	db.mu.Lock()
	f(db)
	db.mu.Unlock()
}

// keeper is a CheckpointSink of the tests'.
type keeper struct {
	DB   string `json:"db"`
	Keep bool   `json:"keep"`

	pipeline, lineage string
	// held are a source transaction's records not committed yet.
	held []*Record
}

func (k *keeper) KeepsCheckpoint() bool { return k.Keep }

func (k *keeper) ReadCheckpoint(ctx context.Context, pipeline, lineage string) (Position, string, error) {
	db := keptDBOf(k.DB)
	db.mu.Lock()
	defer db.mu.Unlock()
	db.reads++
	if db.block != nil {
		db.mu.Unlock()
		<-ctx.Done()
		db.mu.Lock()
		return nil, "", ctx.Err()
	}
	if db.err != nil {
		return nil, "", db.err
	}
	k.pipeline, k.lineage, k.held = pipeline, lineage, nil
	return db.pos, db.lineage, nil
}

func (k *keeper) Write(ctx context.Context, batch []*Record) error {
	db := keptDBOf(k.DB)
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(k.held) > 0 && (batch[0].Tx.First || batch[0].Tx.ID != k.held[0].Tx.ID) {
		k.held = nil // the run went back to its checkpoint
	}
	list := append(k.held, batch...)
	cut := len(list)
	for cut > 0 && !list[cut-1].boundary() {
		cut--
	}
	db.recs = append(db.recs, list[:cut]...)
	k.held = append([]*Record(nil), list[cut:]...)
	if k.pipeline == "" {
		return nil
	}
	for _, r := range list[:cut] {
		if r.Position != nil && r.boundary() {
			db.pos, db.lineage = r.Position, k.lineage
		}
	}
	return nil
}

func init() {
	module.Register(module.Info{ID: Sinks + ".keeper", New: func() any { return new(keeper) }, Doc: "Tests'."})
}

func keeperName(t *testing.T) string {
	name := t.Name() + fmt.Sprint(rand.Int())
	t.Cleanup(func() { keptDBs.Delete(name) })
	return name
}

// checkOnce fails unless got holds 0..n-1 once each, in order.
func checkOnce(t *testing.T, got []int64, n int64) {
	t.Helper()
	if int64(len(got)) != n {
		t.Fatalf("%d records, want %d: %v", len(got), n, got)
	}
	for i, id := range got {
		if id != int64(i) {
			t.Fatalf("record %d is %d", i, id)
		}
	}
}

// TestSinkCheckpointAcrossKills kills a pipeline whose sink keeps the
// checkpoint at random points, after the sink's write and before the
// file's checkpoint among them, and starts it again each time: every
// record reaches the sink once.
func TestSinkCheckpointAcrossKills(t *testing.T) {
	for _, mode := range []string{"stream", "once"} {
		t.Run(mode, func(t *testing.T) {
			const n = 400
			db := keeperName(t)
			dir := t.TempDir()
			cfg := syncConfig(t, dir, fmt.Sprintf(`{"name": "p", "mode": %q, "checkpoint_interval": "1ms", "queue": {"max_records": 30},
				"source": {"module": "generate", "params": {"records": %d, "tx_size": 3}},
				"sinks": [{"name": "k", "batch": {"max_records": 7, "max_wait": "1ms"}, "sink": {"module": "keeper", "params": {"db": %q, "keep": true}}}]}`, mode, n, db))
			points := []Point{CrashWritten, CrashAcknowledged, CrashCheckpointWritten, CrashCheckpointSaved, CrashDispatched}
			rnd := rand.New(rand.NewPCG(uint64(len(mode)), 7))
			for i := 0; ; i++ {
				at, nth := points[i%len(points)], int32(1+rnd.IntN(20))
				var seen atomic.Int32
				rt := NewRuntime(Options{Crash: func(_ string, p Point) bool { return p == at && seen.Add(1) == nth }})
				if err := rt.Apply(cfg); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "the pipeline", func() bool { st := state(rt, "p").State; return st == "killed" || st == "finished" })
				st := state(rt, "p").State
				rt.Close(context.Background())
				if st == "finished" {
					break
				}
			}
			checkOnce(t, keptDBOf(db).ids(), n)
		})
	}
}

// TestSinkCheckpointLineage: the sink's checkpoint yields to a reset and to
// a position set by hand, and a stream whose file is lost takes it.
func TestSinkCheckpointLineage(t *testing.T) {
	db := keeperName(t)
	k := keptDBOf(db)
	dir := t.TempDir()
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	cfg := syncConfig(t, dir, fmt.Sprintf(`{"name": "p", "checkpoint_interval": "1ms",
		"source": {"module": "generate", "params": {"records": 10}},
		"sinks": [{"name": "k", "batch": {"max_wait": "1ms"}, "sink": {"module": "keeper", "params": {"db": %q, "keep": true}}}]}`, db))
	if err := rt.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run", func() bool { return state(rt, "p").State == "finished" })
	checkOnce(t, k.ids(), 10)
	c, err := rt.Checkpoint("p")
	if err != nil || c.Lineage == "" || c.Lineage != k.lineage || string(c.Position) != `{"n":10}` {
		t.Fatalf("%+v %v; the sink's %s", c, err, k.lineage)
	}

	// The sink ahead of the file, as after a crash between them: the run
	// starts from the sink's.
	k.set(func(db *keptDB) { db.pos = Position(`{"n": 12}`) })
	cfg2 := syncConfig(t, dir, fmt.Sprintf(`{"name": "p", "checkpoint_interval": "1ms",
		"source": {"module": "generate", "params": {"records": 15}},
		"sinks": [{"name": "k", "batch": {"max_wait": "1ms"}, "sink": {"module": "keeper", "params": {"db": %q, "keep": true}}}]}`, db))
	if err := rt.Apply(cfg2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run from 12", func() bool { return state(rt, "p").State == "finished" && len(k.ids()) == 13 })
	if got := k.ids(); got[10] != 12 || got[12] != 14 {
		t.Fatalf("%v", got)
	}

	// A reset: the file of no position says so, and the sink's is not
	// taken; a second reset needs no confirm.
	if _, err := rt.ResetCheckpoint("p", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run anew", func() bool { return state(rt, "p").State == "finished" && len(k.ids()) == 28 })
	if got := k.ids(); got[13] != 0 {
		t.Fatalf("%v", got)
	}
	if _, err := rt.ResetCheckpoint("p", false); !errors.Is(err, ErrConfirm) {
		t.Fatalf("a reset of a checkpoint: %v", err)
	}
	k.set(func(db *keptDB) { db.recs = nil })

	// A position set by hand starts a lineage: the sink's is older.
	if _, err := rt.SetCheckpoint("p", json.RawMessage(`{"n": 5}`), true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run from 5", func() bool { return state(rt, "p").State == "finished" && len(k.ids()) == 10 })
	if got := k.ids(); got[0] != 5 {
		t.Fatalf("%v", got)
	}

	// The file lost: the stream starts from the sink's.
	rt.Close(context.Background())
	if err := os.Remove(filepath.Join(dir, "p.json")); err != nil {
		t.Fatal(err)
	}
	k.set(func(db *keptDB) { db.recs, db.pos = nil, Position(`{"n":13}`) })
	rt = NewRuntime(Options{})
	defer rt.Close(context.Background())
	if err := rt.Apply(cfg2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run from 13", func() bool { return state(rt, "p").State == "finished" && len(k.ids()) == 2 })
	if got := k.ids(); got[0] != 13 {
		t.Fatalf("%v", got)
	}

	// A reset with no checkpoint needs no confirm.
	rt.Close(context.Background())
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte(`{"version": 1, "pipeline": "p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt = NewRuntime(Options{})
	defer rt.Close(context.Background())
	k.set(func(db *keptDB) { db.recs = nil })
	if err := rt.Apply(cfg2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a run anew", func() bool { return state(rt, "p").State == "finished" && len(k.ids()) == 15 })
	if _, err := rt.ResetCheckpoint("p", true); err != nil {
		t.Fatal(err)
	}
}

// TestSinkCheckpointNotShared: with a sink that keeps no checkpoint beside
// one that does, or two keeping different positions, the file serves.
func TestSinkCheckpointNotShared(t *testing.T) {
	for _, second := range []string{"memory", "keeper-off", "keeper"} {
		t.Run(second, func(t *testing.T) {
			db, db2 := keeperName(t), keeperName(t)+"2"
			t.Cleanup(func() { keptDBs.Delete(db2) })
			_, sn := memStore(t)
			k := keptDBOf(db)
			dir := t.TempDir()
			other := fmt.Sprintf(`{"module": "memory", "params": {"store": %q}}`, sn)
			switch second {
			case "keeper-off":
				other = fmt.Sprintf(`{"module": "keeper", "params": {"db": %q}}`, db2)
			case "keeper":
				other = fmt.Sprintf(`{"module": "keeper", "params": {"db": %q, "keep": true}}`, db2)
			}
			cfg := func(n int) string {
				return fmt.Sprintf(`{"name": "p", "checkpoint_interval": "1ms", "source": {"module": "generate", "params": {"records": %d}},
				"sinks": [{"name": "k", "batch": {"max_wait": "1ms"}, "sink": {"module": "keeper", "params": {"db": %q, "keep": true}}},
				{"name": "o", "batch": {"max_wait": "1ms"}, "sink": %s}]}`, n, db, other)
			}
			rt := NewRuntime(Options{})
			defer rt.Close(context.Background())
			applyOne(t, rt, dir, cfg(10))
			waitFor(t, "the run", func() bool { return state(rt, "p").State == "finished" })
			k.set(func(db *keptDB) { db.pos = Position(`{"n":12}`) })
			applyOne(t, rt, dir, cfg(15))
			waitFor(t, "the run from the file", func() bool { return state(rt, "p").State == "finished" && len(k.ids()) == 15 })
			checkOnce(t, k.ids(), 15)
		})
	}
}

// TestSinkCheckpointErrors: a sink failing to read its checkpoint, or
// keeping one that is not JSON, retries the pipeline; a job in chunks does
// not ask.
func TestSinkCheckpointErrors(t *testing.T) {
	db := keeperName(t)
	k := keptDBOf(db)
	k.set(func(db *keptDB) { db.err = errors.New("the server is down") })
	dir := t.TempDir()
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	sink := fmt.Sprintf(`"sinks": [{"name": "k", "batch": {"max_wait": "1ms"}, "sink": {"module": "keeper", "params": {"db": %q, "keep": true}}}]`, db)
	applyOne(t, rt, dir, `{"name": "p", "retry": {"backoff": "1ms"}, "source": {"module": "generate", "params": {"records": 3}}, `+sink+`}`)
	waitFor(t, "the retry", func() bool { st := state(rt, "p"); return st.State == "retrying" && st.Err != "" })
	k.set(func(db *keptDB) { db.err, db.pos, db.lineage = nil, Position(`{`), "x" })
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte(`{"version": 1, "pipeline": "p", "lineage": "x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a retry of the position", func() bool { return strings.Contains(state(rt, "p").Err, "sink k's checkpoint") })
	k.set(func(db *keptDB) { db.pos = nil })
	waitFor(t, "the run", func() bool { return state(rt, "p").State == "finished" })
	checkOnce(t, k.ids(), 3)
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&pipe{modules: rt.pipes["p"].modules, store: &store{dir: dir, name: "p"}, cfg: rt.pipes["p"].cfg, rt: rt}).sinkCheckpoint(nil); err == nil {
		t.Fatal("a file not JSON")
	}

	// A job in chunks keeps to its file.
	rt.Close(context.Background())
	k.set(func(db *keptDB) { db.recs, db.reads = nil, 0 })
	rt = NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, t.TempDir(), `{"name": "j", "mode": "once", "source": {"module": "generate", "params": {"records": 20, "chunk_records": 5}}, `+sink+`}`)
	waitFor(t, "the job", func() bool { return state(rt, "j").State == "finished" })
	if k.readCount() != 0 || len(k.ids()) != 20 {
		t.Fatalf("%d reads, %d records", k.readCount(), len(k.ids()))
	}
}

// TestSinkCheckpointStops: a pipeline stopped while its sink reads its
// checkpoint stops.
func TestSinkCheckpointStops(t *testing.T) {
	db := keeperName(t)
	k := keptDBOf(db)
	k.set(func(db *keptDB) { db.block = make(chan struct{}) })
	rt := NewRuntime(Options{})
	applyOne(t, rt, t.TempDir(), fmt.Sprintf(`{"name": "p", "source": {"module": "generate", "params": {"records": 3}},
		"sinks": [{"name": "k", "sink": {"module": "keeper", "params": {"db": %q, "keep": true}}}]}`, db))
	waitFor(t, "the read", func() bool { return k.readCount() > 0 })
	rt.Close(context.Background())
}
