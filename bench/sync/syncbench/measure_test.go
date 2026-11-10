package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	warmWait, warmSettle = 50*time.Millisecond, 50*time.Millisecond
}

// fakePprof serves fit's profiles, or fails them with code.
func fakePprof(t *testing.T, code int) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code != http.StatusOK {
			http.Error(w, "off", code)
			return
		}
		io.WriteString(w, "profile of "+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// copier is a mover of the files scenario: started, it copies the lines
// of the files in in to one file in out, every few milliseconds, until
// the test ends.
func copier(t *testing.T, in, out string) func() {
	var once sync.Once
	stop := make(chan struct{})
	var wg sync.WaitGroup
	t.Cleanup(func() { close(stop); wg.Wait() })
	return func() {
		once.Do(func() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				offs := map[string]int64{}
				for {
					select {
					case <-stop:
						return
					case <-time.After(5 * time.Millisecond):
					}
					ents, _ := os.ReadDir(in)
					for _, e := range ents {
						data, _ := os.ReadFile(filepath.Join(in, e.Name()))
						end := int64(bytes.LastIndexByte(data, '\n') + 1)
						if end > offs[e.Name()] {
							f, _ := os.OpenFile(filepath.Join(out, "moved.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
							f.Write(data[offs[e.Name()]:end])
							f.Close()
							offs[e.Name()] = end
						}
					}
				}
			}()
		})
	}
}

func TestMeasureFiles(t *testing.T) {
	ctx := ctxT(t)
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	fd, dockerURL := newFakeDocker(t)
	start := copier(t, in, out)
	fd.onCall = func(method, path string) {
		if strings.HasSuffix(path, "/start") {
			start()
		}
	}
	args := []string{"-scenario", "files", "-mover", "fit", "-variant", "v", "-in", in, "-out-dir", out,
		"-records", "3000", "-files", "2", "-containers", "fit", "-docker", dockerURL, "-start",
		"-steady-rate", "200", "-steady-for", "1s", "-interval", "10ms", "-settle", "50ms",
		"-pprof", fakePprof(t, http.StatusOK), "-profile-for", "1s", "-profile-dir", dir,
		"-out", filepath.Join(dir, "r.json")}
	o := opts(t, "prepare", args...)
	if err := prepare(ctx, o); err != nil {
		t.Fatal(err)
	}
	o = opts(t, "measure", args...)
	if err := measureCmd(ctx, o, io.Discard); err != nil {
		t.Fatal(err)
	}
	r := readResult(t, filepath.Join(dir, "r.json"))
	if r.Error != "" || r.Counted != 3000 || r.RecordsPerSec <= 0 || r.CPUCores <= 0 || r.RSSPeakBytes != 1<<20 {
		t.Errorf("result: %+v", r)
	}
	if r.Lag == nil || r.Lag.Samples == 0 || r.Lag.Rate != 200 {
		t.Errorf("lag: %+v", r.Lag)
	}
	if len(r.Profiles) != 2 || r.Profiles[0] != "fit-v-cpu.pprof" || r.Profiles[1] != "fit-v-heap.pprof" {
		t.Errorf("profiles: %q", r.Profiles)
	}
	if fd.called("/start") != 1 {
		t.Errorf("starts: %d", fd.called("/start"))
	}
}

// esSink is an index whose count is the rows of db, as a mover that keeps
// up would make it; while the mover is paused it does not move, and rows
// written before it started are not in it.
type esSink struct {
	db     *fakeDB
	paused atomic.Bool
	frozen atomic.Int64
	skip   int64
	fail   atomic.Bool
}

func (e *esSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if e.fail.Load() {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	n := e.db.n()
	if e.paused.Load() {
		n = e.frozen.Load()
	}
	fmt.Fprintf(w, `{"count": %d}`, max(0, n-e.skip))
}

func streamArgs(t *testing.T, esURL, dockerURL string, more ...string) []string {
	return append([]string{"-scenario", "mysql-es", "-mover", "canal", "-es", esURL, "-records", "500",
		"-tx-rows", "5", "-writers", "3", "-containers", "canal,adapter", "-docker", dockerURL,
		"-steady-rate", "100", "-steady-for", "1s", "-interval", "10ms", "-settle", "30ms"}, more...)
}

func TestMeasureStream(t *testing.T) {
	ctx := ctxT(t)
	for _, pause := range []bool{true, false} {
		db := &fakeDB{}
		useDB(t, db)
		es := &esSink{db: db, skip: 0}
		srv := httptest.NewServer(es)
		defer srv.Close()
		fd, dockerURL := newFakeDocker(t)
		fd.onCall = func(method, path string) {
			switch {
			case strings.HasSuffix(path, "/pause"):
				es.frozen.Store(db.n())
				es.paused.Store(true)
			case strings.HasSuffix(path, "/unpause"):
				es.paused.Store(false)
			}
		}
		o := opts(t, "measure", streamArgs(t, srv.URL, dockerURL, fmt.Sprintf("-pause=%v", pause))...)
		r := measure(ctx, o)
		if r.Error != "" || r.Counted != 500 || r.Lag == nil || r.Lag.Samples == 0 || r.StartupSeconds <= 0 {
			t.Errorf("pause %v: %+v", pause, r)
		}
		if pause && (fd.called("/pause") != 2 || fd.called("/unpause") != 2) {
			t.Errorf("pauses: %d, unpauses: %d", fd.called("/pause"), fd.called("/unpause"))
		}
		// Every row went in once, in transactions of five: the warm-up's,
		// the measured, the steady phase's.
		if n := db.n(); n < 600 {
			t.Errorf("rows written: %d", n)
		}
	}
}

func TestMeasureBatch(t *testing.T) {
	ctx := ctxT(t)
	db := &fakeDB{}
	useDB(t, db)
	fd, dockerURL := newFakeDocker(t)
	fd.onCall = func(method, path string) {
		if strings.HasSuffix(path, "/start") {
			go func() {
				for range 10 {
					time.Sleep(20 * time.Millisecond)
					db.exec(context.Background(), strings.Repeat("VALUES (", 100))
				}
			}()
		}
	}
	o := opts(t, "measure", "-scenario", "mysql-batch", "-mover", "datax", "-records", "1000", "-containers", "datax",
		"-docker", dockerURL, "-start", "-interval", "10ms", "-settle", "10ms")
	r := measure(ctx, o)
	if r.Error != "" || r.Counted != 1000 || r.RecordsPerSec <= 0 || r.Lag != nil {
		t.Errorf("batch: %+v", r)
	}
}

// Everything done before the first count still gives a throughput, over
// the time from the start.
func TestMeasureAtOnce(t *testing.T) {
	ctx := ctxT(t)
	db := &fakeDB{}
	useDB(t, db)
	fd, dockerURL := newFakeDocker(t)
	fd.onCall = func(method, path string) {
		if strings.HasSuffix(path, "/start") {
			db.exec(context.Background(), strings.Repeat("VALUES (", 10))
		}
	}
	o := opts(t, "measure", "-scenario", "mysql-batch", "-mover", "fit", "-records", "10", "-containers", "fit",
		"-docker", dockerURL, "-start", "-interval", "10ms", "-settle", "0s")
	if r := measure(ctx, o); r.Error != "" || r.Counted != 10 || r.RecordsPerSec <= 0 {
		t.Errorf("at once: %+v", r)
	}
}

func TestMeasureFailures(t *testing.T) {
	ctx := ctxT(t)
	_, dockerURL := newFakeDocker(t)
	fail := func(name string, o *options, want string) {
		t.Helper()
		if r := measure(ctx, o); !strings.Contains(r.Error, want) {
			t.Errorf("%s: %q, want %q", name, r.Error, want)
		}
	}
	db := &fakeDB{}
	useDB(t, db)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"count": 0}`)
	}))
	defer dead.Close()
	fail("a mover that never delivers", opts(t, "measure", streamArgs(t, dead.URL, dockerURL, "-startup-timeout", "150ms")...),
		"waiting for the warm-up rows: the sink had none")
	es := &esSink{db: db}
	live := httptest.NewServer(es)
	defer live.Close()
	fail("-pause without containers", opts(t, "measure", "-scenario", "mysql-es", "-mover", "m", "-es", live.URL,
		"-interval", "10ms"), "-pause and -start need")
	fail("a bad -docker", opts(t, "measure", streamArgs(t, live.URL, "tcp://x")...), "-docker")
	db.fail = "VALUES (1,"
	fail("the warm-up rows not written", opts(t, "measure", streamArgs(t, live.URL, dockerURL)...), "fake")
	db.fail = "VALUES (6,"
	fail("the rows not written", opts(t, "measure", streamArgs(t, live.URL, dockerURL)...), "writing the rows")
	fail("the rows not written, live", opts(t, "measure", streamArgs(t, live.URL, dockerURL, "-pause=false")...), "writing the rows")
	db.fail = "VALUES (520,"
	fail("the steady rows not written", opts(t, "measure", streamArgs(t, live.URL, dockerURL)...), "the steady phase")
	db.fail = ""
	useDB(t, &fakeDB{})
	fail("rows that never come", opts(t, "measure", "-scenario", "mysql-batch", "-mover", "m", "-records", "1",
		"-timeout", "30ms", "-interval", "10ms", "-steady-rate", "0"), "the sink had 0 of 1 records")
	useDB(t, db)
	fd2, dockerURL2 := newFakeDocker(t)
	fd2.fail = "/stats"
	fail("stats that fail", opts(t, "measure", streamArgs(t, live.URL, dockerURL2)...), "no such container")
	fd2.fail = "/pause"
	fail("a pause that fails", opts(t, "measure", streamArgs(t, live.URL, dockerURL2)...), "no such container")
	fd2.fail = "/start"
	fail("a start that fails", opts(t, "measure", "-scenario", "mysql-batch", "-mover", "m", "-containers", "x",
		"-docker", dockerURL2, "-start"), "no such container")
	db.fail = "COUNT"
	fail("a count that fails before the start", opts(t, "measure", "-scenario", "mysql-batch", "-mover", "m", "-containers", "x",
		"-docker", dockerURL, "-start"), "counting the sink")
	fail("a count that fails", opts(t, "measure", "-scenario", "mysql-batch", "-mover", "m", "-interval", "10ms"), "counting the sink")
	db.fail = ""
	es.fail.Store(true)
	fail("an index that fails at the warm-up", opts(t, "measure", streamArgs(t, live.URL, dockerURL)...), "counting the sink")
	es.fail.Store(false)
	mysqlDial = func(context.Context, string, string, string) (execer, error) { return nil, fmt.Errorf("refused") }
	fail("a source that refuses", opts(t, "measure", streamArgs(t, live.URL, dockerURL)...), "refused")
	fail("a sink that refuses", opts(t, "measure", "-scenario", "mysql-batch", "-mover", "m"), "refused")
}

// The warm-up waits for the count to settle, and fails when the sink
// fails meanwhile or the count never settles.
func TestWarm(t *testing.T) {
	ctx := ctxT(t)
	db := &fakeDB{}
	var n atomic.Int64
	var failAfter atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f := failAfter.Load(); f > 0 && n.Load() >= f {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		// The count rises on every call.
		fmt.Fprintf(w, `{"count": %d}`, n.Add(1))
	}))
	defer srv.Close()
	o := opts(t, "measure", "-scenario", "mysql-es", "-mover", "m", "-es", srv.URL, "-interval", "5ms", "-startup-timeout", "200ms")
	ctr, _ := openCounter(ctx, o)
	m := &meter{o: o, ctr: ctr}
	src := &sqlSource{conns: []execer{db}, table: "t", txRows: 1}
	if _, _, _, err := m.warm(ctx, src); err == nil || !strings.Contains(err.Error(), "did not settle") {
		t.Errorf("a count that never settles: %v", err)
	}
	n.Store(0)
	failAfter.Store(3)
	if _, _, _, err := m.warm(ctx, src); err == nil {
		t.Error("a sink that fails once seen: no error")
	}
	n.Store(0)
	failAfter.Store(2)
	if _, _, _, err := m.warm(ctx, src); err == nil {
		t.Error("a sink that fails before: no error")
	}
}

// lateSource is a source whose transactions reach the sink late: the
// first once the second is written, the second late after that.
type lateSource struct {
	mu     sync.Mutex
	writes []time.Time
}

func (l *lateSource) write(context.Context, int, int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writes = append(l.writes, time.Now())
	return nil
}

func (l *lateSource) close() {}

// count is the transactions at the sink: none until the second is
// written, the first then, the second late after.
func (l *lateSource) count(late time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case len(l.writes) < 2:
		return 0
	case time.Since(l.writes[1]) < late:
		return 1
	}
	return len(l.writes)
}

// A warm-up transaction that arrives after an earlier one raised the count,
// later than warmSettle, is in the count the warm-up returns: S9's 10
// extra rows of 11-10.
func TestWarmLateTransaction(t *testing.T) {
	ctx := ctxT(t)
	src := &lateSource{}
	late := warmSettle * 3 / 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"count": %d}`, src.count(late))
	}))
	defer srv.Close()
	o := opts(t, "measure", "-scenario", "mysql-es", "-mover", "m", "-es", srv.URL, "-interval", "5ms", "-startup-timeout", "5s")
	ctr, _ := openCounter(ctx, o)
	m := &meter{o: o, ctr: ctr}
	base, next, ready, err := m.warm(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if base != 2 || next != 2*o.txRows {
		t.Errorf("base %d, next %d: want 2 and %d", base, next, 2*o.txRows)
	}
	src.mu.Lock()
	arrived := src.writes[1].Add(late)
	src.mu.Unlock()
	if ready.IsZero() || ready.Before(arrived) {
		t.Errorf("ready %v, before the second transaction arrived at %v", ready, arrived)
	}
}

func TestMeasureContext(t *testing.T) {
	db := &fakeDB{}
	useDB(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := opts(t, "measure", "-scenario", "mysql-batch", "-mover", "m", "-records", "5")
	if r := measure(ctx, o); r.Error == "" {
		t.Error("a canceled context: no error")
	}
}

func TestProfiles(t *testing.T) {
	ctx := ctxT(t)
	dir := t.TempDir()
	r := &Result{}
	m := &meter{o: opts(t, "measure", "-scenario", "files", "-mover", "fit", "-pprof", fakePprof(t, http.StatusNotFound),
		"-profile-dir", dir, "-profile-for", "1s")}
	m.profile(ctx, r)()
	m.heap(ctx, r)
	if len(r.Profiles) != 0 {
		t.Errorf("profiles off: %q", r.Profiles)
	}
	m.o.pprof = "tcp://x"
	m.heap(ctx, r)
	m.o.pprof = fakePprof(t, http.StatusOK)
	m.o.profileDir = filepath.Join(dir, "missing")
	m.heap(ctx, r)
	if len(r.Profiles) != 0 {
		t.Errorf("profiles not saved: %q", r.Profiles)
	}
	if _, err := m.fetch(ctx, "%zz", "x"); err == nil {
		t.Error("a bad path: no error")
	}
	// A server not listening yet is asked again, for a while.
	m.o.pprof = "http://127.0.0.1:1"
	old := time.Now()
	m.profile(ctx, r)()
	if time.Since(old) < 5*time.Second || len(r.Profiles) != 0 {
		t.Errorf("a server that never listens: %q after %s", r.Profiles, time.Since(old))
	}
}

func TestMeasureCmdOut(t *testing.T) {
	o := opts(t, "measure", "-scenario", "files", "-mover", "m", "-in", t.TempDir(), "-out-dir", "/does/not/exist",
		"-out", "/does/not/exist/r.json")
	if err := measureCmd(context.Background(), o, io.Discard); err == nil {
		t.Error("an unwritable -out: no error")
	}
	var b bytes.Buffer
	o.out = ""
	if err := measureCmd(context.Background(), o, &b); err != nil || !json.Valid(b.Bytes()) {
		t.Errorf("stdout: %v, %s", err, b.String())
	}
}

func TestPrepare(t *testing.T) {
	ctx := ctxT(t)
	db := &fakeDB{}
	useDB(t, db)
	var puts atomic.Int32
	var putCode atomic.Int32
	putCode.Store(200)
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
			w.WriteHeader(int(putCode.Load()))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer es.Close()
	if err := prepare(ctx, opts(t, "prepare", "-scenario", "mysql-es", "-es", es.URL)); err != nil || puts.Load() != 1 {
		t.Errorf("mysql-es: %v, %d PUTs", err, puts.Load())
	}
	putCode.Store(400)
	if err := prepare(ctx, opts(t, "prepare", "-scenario", "mysql-es", "-es", es.URL)); err == nil {
		t.Error("an index refused: no error")
	}
	if err := prepare(ctx, opts(t, "prepare", "-scenario", "mysql-es", "-es", "http://127.0.0.1:1")); err == nil {
		t.Error("no Elasticsearch: no error")
	}
	if err := esCreate(ctx, ":"); err == nil {
		t.Error("a bad URL: no error")
	}
	if err := prepare(ctx, opts(t, "prepare", "-scenario", "pg-kafka")); err != nil {
		t.Error(err)
	}
	if err := prepare(ctx, opts(t, "prepare", "-scenario", "mysql-kafka")); err != nil {
		t.Error(err)
	}
	if err := prepare(ctx, opts(t, "prepare", "-scenario", "mysql-batch", "-records", "1500", "-writers", "2")); err != nil || db.n() != 1500 {
		t.Errorf("mysql-batch: %v, %d rows", err, db.n())
	}
	for _, c := range [][2]string{{"pg-kafka", "DROP"}, {"mysql-batch", "DROP"}, {"mysql-batch", "CREATE TABLE shop"}} {
		db.fail = c[1]
		if err := prepare(ctx, opts(t, "prepare", "-scenario", c[0])); err == nil {
			t.Errorf("%s failing %s: no error", c[0], c[1])
		}
	}
	db.fail = "INSERT"
	if err := prepare(ctx, opts(t, "prepare", "-scenario", "mysql-batch")); err == nil {
		t.Error("rows not written: no error")
	}
	db.fail = ""
	n := 0
	mysqlDial = func(context.Context, string, string, string) (execer, error) {
		if n++; n > 2 {
			return nil, fmt.Errorf("refused")
		}
		return db, nil
	}
	if err := prepare(ctx, opts(t, "prepare", "-scenario", "mysql-batch")); err == nil {
		t.Error("writers refused: no error")
	}
	mysqlDial = func(context.Context, string, string, string) (execer, error) { return nil, fmt.Errorf("refused") }
	pgDial = func(context.Context, string, string, string, string) (execer, error) {
		return nil, fmt.Errorf("refused")
	}
	for _, sc := range []string{"pg-kafka", "mysql-kafka"} {
		if err := prepare(ctx, opts(t, "prepare", "-scenario", sc)); err == nil {
			t.Errorf("%s refused: no error", sc)
		}
	}
	// The files scenario's directories.
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o644)
	if err := prepare(ctx, opts(t, "prepare", "-scenario", "files", "-in", filepath.Join(dir, "in"), "-out-dir", file)); err == nil {
		t.Error("an out dir that is a file: no error")
	}
	if err := emptyDir(file); err == nil {
		t.Error("emptyDir of a file: no error")
	}
}
