package syncfile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	fitsync "github.com/lesismal/fit/sync"
)

func runtimeWindows() bool { return runtime.GOOS == "windows" }

// writer writes numbered lines to dir/app.log, rotating it now and then by
// rename and by copy and truncation, and deleting rotated files once every
// line they hold was delivered.
type writer struct {
	dir     string
	f       *os.File
	n       int
	inFile  int // lines written since the file was started
	rotated []rotatedFile
	ops     int
}

type rotatedFile struct {
	path string
	last int // the highest line number it holds
}

func (w *writer) path() string { return filepath.Join(w.dir, "app.log") }

func (w *writer) open() {
	f, err := os.OpenFile(w.path(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		panic(err)
	}
	w.f = f
}

func (w *writer) line() {
	if _, err := fmt.Fprintf(w.f, "line %06d\n", w.n); err != nil {
		panic(err)
	}
	w.n++
	w.inFile++
}

// rename rotates as logrotate's default does.
func (w *writer) rename() {
	w.f.Close()
	w.ops++
	to := filepath.Join(w.dir, fmt.Sprintf("app.log.r%d", w.ops))
	if err := os.Rename(w.path(), to); err != nil {
		panic(err)
	}
	w.rotated = append(w.rotated, rotatedFile{to, w.n - 1})
	w.open()
	w.inFile = 0
}

// copyTruncate rotates as logrotate's copytruncate does; the copy is made
// under another name and renamed into place, so that it is whole when
// found.
func (w *writer) copyTruncate() {
	w.ops++
	tmp, to := filepath.Join(w.dir, "copy.tmp"), filepath.Join(w.dir, fmt.Sprintf("app.log.c%d", w.ops))
	src, err := os.Open(w.path())
	if err != nil {
		panic(err)
	}
	dst, err := os.Create(tmp)
	if err != nil {
		panic(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		panic(err)
	}
	src.Close()
	dst.Close()
	if err := os.Rename(tmp, to); err != nil {
		panic(err)
	}
	if err := w.f.Truncate(0); err != nil {
		panic(err)
	}
	w.rotated = append(w.rotated, rotatedFile{to, w.n - 1})
	w.inFile = 0
}

// deleteDelivered deletes the oldest rotated file, once every line up to
// its last was delivered.
func (w *writer) deleteDelivered(delivered func(int) bool) {
	if len(w.rotated) == 0 || !delivered(w.rotated[0].last) {
		return
	}
	if err := os.Remove(w.rotated[0].path); err != nil {
		panic(err)
	}
	w.rotated = w.rotated[1:]
}

// delivery is what the memory store holds, deduplicated by key.
type delivery struct {
	mu     sync.Mutex
	byKey  map[string]int
	lines  map[int][]string
	upTo   int // every line before it was delivered
	stored int
}

func (d *delivery) update(s *fitsync.MemStore) {
	d.mu.Lock()
	defer d.mu.Unlock()
	recs := s.Records()
	for _, r := range recs[d.stored:] {
		v, _ := r.After.Get("line")
		n, err := strconv.Atoi(strings.TrimPrefix(fitsync.TextOf(v), "line "))
		if err != nil {
			panic(fmt.Sprintf("a bad line %q", fitsync.TextOf(v)))
		}
		key := fitsync.TextOf(must(r.Key.Get("file"))) + "@" + strconv.FormatInt(must(r.Key.Get("offset")).AsInt(), 10)
		if old, ok := d.byKey[key]; ok {
			if old != n {
				panic(fmt.Sprintf("key %s holds line %d and line %d", key, old, n))
			}
			continue
		}
		d.byKey[key] = n
		d.lines[n] = append(d.lines[n], key)
	}
	d.stored = len(recs)
	for len(d.lines[d.upTo]) > 0 {
		d.upTo++
	}
}

func must(v fitsync.Value, _ bool) fitsync.Value { return v }

// TestTailAcrossRotationsAndCrashes writes numbered lines while rotating the
// file by rename and by copy and truncation and deleting rotated files, and
// kills the pipeline at random points: every line is delivered, and no line
// under two keys, so that a sink writing by key holds each line once.
func TestTailAcrossRotationsAndCrashes(t *testing.T) {
	if runtimeWindows() {
		t.Skip("renaming and truncating a file another handle holds open follows other rules on Windows")
	}
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rnd := rand.New(rand.NewPCG(seed, 11))
	dir := t.TempDir()
	logDir, stateDir := filepath.Join(dir, "logs"), filepath.Join(dir, "state")
	if err := os.Mkdir(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := fmt.Sprintf("tail-%d", seed)
	s := fitsync.MemoryStore(store)
	t.Cleanup(s.Reset)
	pipeline := fmt.Sprintf(`{"name": "tail", "checkpoint_interval": "1ms",
		"source": {"module": "file", "params": {"paths": [%s], "scan_interval": "15ms", "poll_interval": "2ms",
			"close_after": "3s", "fingerprint_bytes": 64}},
		"queue": {"max_records": %d},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q, "max_records": 1000000}},
			"batch": {"max_records": %d, "max_wait": "1ms"}}]}`,
		jsonString(filepath.Join(logDir, "app.log*")), 20+rnd.IntN(200), store, 1+rnd.IntN(50))
	c, err := config.Load(fmt.Appendf(nil, `{"sync": {"state_dir": %s, "pipelines": [%s]}}`, jsonString(stateDir), pipeline))
	if err != nil {
		t.Fatal(err)
	}
	if err := fitsync.Check(c.Sync); err != nil {
		t.Fatal(err)
	}
	d := &delivery{byKey: map[string]int{}, lines: map[int][]string{}}
	delivered := func(n int) bool {
		d.update(s)
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.upTo > n
	}

	const total = 4000
	wrnd := rand.New(rand.NewPCG(seed, 13))
	w := &writer{dir: logDir}
	w.open()
	var writing sync.WaitGroup
	var wrote atomic.Int64
	writing.Go(func() {
		for w.n < total {
			w.line()
			wrote.Store(int64(w.n))
			if w.inFile >= 20 && wrnd.IntN(60) == 0 {
				if wrnd.IntN(2) == 0 {
					w.rename()
				} else {
					w.copyTruncate()
				}
			}
			if w.n%100 == 0 {
				w.deleteDelivered(delivered)
			}
			if w.n%25 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	})

	// replayable are the records a run wrote past the checkpoint it saved,
	// killed or stopped (the last records read may carry no position), which
	// the next run may read again, from a copy under another key, too.
	replayable := map[string]bool{}
	kills := 0
	points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
		fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
	for round := 0; wrote.Load() < total || round < 4; round++ {
		at, nth := points[rnd.IntN(len(points))], int32(1+rnd.IntN(300))
		var seen atomic.Int32
		rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
			return p == at && seen.Add(1) == nth
		}})
		if err := rt.Apply(c.Sync); err != nil {
			t.Fatal(err)
		}
		from := len(s.Records())
		deadline := time.Now().Add(time.Duration(50+rnd.IntN(200)) * time.Millisecond)
		for time.Now().Before(deadline) && status(rt) != "killed" {
			time.Sleep(time.Millisecond)
		}
		killed := status(rt) == "killed"
		rt.Close(context.Background())
		if killed {
			kills++
		}
		uncovered(t, stateDir, s.Records()[from:], replayable)
	}
	writing.Wait()
	w.f.Close()
	rt := fitsync.NewRuntime(fitsync.Options{})
	if err := rt.Apply(c.Sync); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !delivered(total - 1) {
		if time.Now().After(deadline) {
			d.mu.Lock()
			t.Fatalf("lines up to %d delivered of %d (%d ops)", d.upTo, total, w.ops)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // anything read twice would show now
	rt.Close(context.Background())
	d.update(s)
	for n := range total {
		keys := d.lines[n]
		covered := 0
		for _, k := range keys {
			if !replayable[k] {
				covered++
			}
		}
		if len(keys) == 0 || covered > 1 || len(keys) > 1 && covered == len(keys) {
			t.Fatalf("line %d delivered under %d keys, %d of them saved by a checkpoint: %v", n, len(keys), covered, keys)
		}
	}
	t.Logf("%d records stored, %d lines, %d rotations, %d kills, %d records past a run's checkpoint",
		len(s.Records()), total, w.ops, kills, len(replayable))
}

// uncovered adds to replayable the keys of recs the checkpoint saved in
// stateDir does not cover.
func uncovered(t *testing.T, stateDir string, recs []*fitsync.Record, replayable map[string]bool) {
	t.Helper()
	var ck struct{ Position position }
	if data, err := os.ReadFile(filepath.Join(stateDir, "tail.json")); err == nil {
		if err := json.Unmarshal(data, &ck); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range recs {
		key := fitsync.TextOf(must(r.Key.Get("file")))
		off := must(r.Key.Get("offset")).AsInt()
		covered := false
		for _, f := range ck.Position.Files {
			covered = covered || f.Key == key && f.Off > off
		}
		if !covered {
			replayable[key+"@"+strconv.FormatInt(off, 10)] = true
		}
	}
}

func status(rt *fitsync.Runtime) string {
	for _, st := range rt.Status() {
		return st.State
	}
	return ""
}
