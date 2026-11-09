package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func opts(t *testing.T, cmd string, args ...string) *options {
	t.Helper()
	var stderr bytes.Buffer
	o, err := parseOptions(cmd, args, &stderr)
	if err != nil {
		t.Fatalf("parseOptions(%q): %v: %s", args, err, stderr.String())
	}
	return o
}

func TestParseOptions(t *testing.T) {
	o := opts(t, "measure", "-scenario", "mysql-es", "-mover", "fit", "-containers", "a,b")
	if !o.sc.stream || o.sc.sink != "es" || len(o.containers) != 2 || o.records != 100000 {
		t.Errorf("options: %+v", o)
	}
	for _, args := range [][]string{
		{"-scenario", "nope"},
		{"-scenario", "files", "-records", "0"},
		{"-scenario", "files", "-line-bytes", "8"},
		{"-scenario", "files", "-interval", "0"},
		{"-scenario", "files", "-bogus"},
	} {
		if _, err := parseOptions("prepare", args, io.Discard); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
	if _, err := parseOptions("measure", []string{"-scenario", "files"}, io.Discard); err == nil {
		t.Error("measure without -mover: no error")
	}
}

func TestRun(t *testing.T) {
	ctx := ctxT(t)
	for _, c := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"nope"}, 2},
		{[]string{"prepare", "-h"}, 0},
		{[]string{"measure", "-scenario", "x"}, 2},
		{[]string{"merge", "-h"}, 0},
		{[]string{"merge"}, 2},
		{[]string{"merge", "-x"}, 2},
		{[]string{"merge", "/does/not/exist.json"}, 1},
		{[]string{"prepare", "-scenario", "files", "-in", "/dev/null/in"}, 1},
	} {
		if got := run(ctx, c.args, io.Discard, io.Discard); got != c.code {
			t.Errorf("run(%q) = %d, want %d", c.args, got, c.code)
		}
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	if got := run(ctx, []string{"prepare", "-scenario", "files", "-in", in, "-out-dir", out, "-records", "10"}, io.Discard, io.Discard); got != 0 {
		t.Fatalf("prepare: %d", got)
	}
	var stdout bytes.Buffer
	// Nothing moves the lines: the measurement fails, and says so.
	if got := run(ctx, []string{"measure", "-scenario", "files", "-mover", "m", "-in", in, "-out-dir", out,
		"-records", "10", "-timeout", "50ms", "-steady-rate", "0"}, &stdout, io.Discard); got != 0 {
		t.Fatalf("measure: %d", got)
	}
	if !strings.Contains(stdout.String(), `"error": "the sink had 0 of 10 records`) {
		t.Errorf("measure: %s", stdout.String())
	}
}

func TestRowsAndLines(t *testing.T) {
	if d := digits(3, 120); len(d) != 120 || d[11] != '-' || d != digits(3, 120) || d == digits(4, 120) {
		t.Errorf("digits: %q", d)
	}
	if r := row(5); !strings.HasPrefix(r, "(5,39595,'") {
		t.Errorf("row: %s", r)
	}
	if l := line(42, 64); len(l) != 64 || l[63] != '\n' || !bytes.Contains(l, []byte(" 42 ")) {
		t.Errorf("line: %q", l)
	}
	// A line shorter than its head is cut to its size.
	if l := line(1, 16); len(l) != 16 || l[15] != '\n' {
		t.Errorf("short line: %q", l)
	}
}

func TestFileSource(t *testing.T) {
	dir := t.TempDir()
	s := &fileSource{dir: dir, files: 3, size: 100}
	if err := s.write(context.Background(), 0, 20000); err != nil {
		t.Fatal(err)
	}
	if err := s.write(context.Background(), 20000, 1); err != nil {
		t.Fatal(err)
	}
	s.close()
	c := &fileCounter{dir: dir}
	if n, err := c.count(context.Background()); err != nil || n != 20001 {
		t.Errorf("count: %d, %v", n, err)
	}
	bad := &fileSource{dir: filepath.Join(dir, "missing"), files: 1, size: 100}
	if err := bad.write(context.Background(), 0, 1); err == nil {
		t.Error("a missing directory: no error")
	}
}

func TestFileCounter(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	c := &fileCounter{dir: dir}
	if n, err := c.count(ctx); n != 0 || err != nil {
		t.Errorf("empty: %d, %v", n, err)
	}
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	p := filepath.Join(dir, "a")
	os.WriteFile(p, []byte("1\n2\n3"), 0o644)
	if n, _ := c.count(ctx); n != 2 {
		t.Errorf("count: %d", n)
	}
	os.WriteFile(p, []byte("1\n2\n3\n4\n5\n"), 0o644)
	if n, _ := c.count(ctx); n != 5 {
		t.Errorf("grown: %d", n)
	}
	// A file that shrank is read again from its start.
	os.WriteFile(p, []byte("x\n"), 0o644)
	if n, _ := c.count(ctx); n != 1 {
		t.Errorf("shrunk: %d", n)
	}
	c.close()
	if _, err := (&fileCounter{dir: filepath.Join(dir, "none")}).count(ctx); err == nil {
		t.Error("missing directory: no error")
	}
	// A file that cannot be read.
	if err := (&fileCounter{}).read(filepath.Join(dir, "none"), &fileCount{}); err == nil {
		t.Error("missing file: no error")
	}
	if err := (&fileCounter{buf: make([]byte, 8)}).read(dir, &fileCount{}); err == nil {
		t.Error("a directory read as a file: no error")
	}
}

func TestSQLSource(t *testing.T) {
	ctx := context.Background()
	for _, multi := range []bool{false, true} {
		dbs := []*fakeDB{{}, {}}
		s := &sqlSource{conns: []execer{dbs[0], dbs[1]}, table: "t", txRows: 3, multi: multi}
		if err := s.write(ctx, 0, 10); err != nil {
			t.Fatal(err)
		}
		if n := dbs[0].n() + dbs[1].n(); n != 10 {
			t.Errorf("multi %v: %d rows", multi, n)
		}
		s.close()
		if dbs[0].closed != 1 {
			t.Error("not closed")
		}
		// A failure stops the writes and is returned.
		dbs[1].fail, dbs[0].fail = "VALUES (5,", "VALUES (5,"
		if err := s.write(ctx, 0, 10); err == nil {
			t.Errorf("multi %v: no error", multi)
		}
	}
	for _, fail := range []string{"START", "COMMIT"} {
		s := &sqlSource{conns: []execer{&fakeDB{fail: fail}}, table: "t", txRows: 3}
		if err := s.write(ctx, 0, 3); err == nil {
			t.Errorf("%s failing: no error", fail)
		}
	}
}

func TestBulkInsert(t *testing.T) {
	ctx := context.Background()
	a, b := &fakeDB{}, &fakeDB{}
	if err := bulkInsert(ctx, []execer{a, b}, "t", 2500); err != nil {
		t.Fatal(err)
	}
	if a.n()+b.n() != 2500 {
		t.Errorf("%d rows", a.n()+b.n())
	}
	if err := bulkInsert(ctx, []execer{&fakeDB{fail: "INSERT"}}, "t", 3000); err == nil {
		t.Error("no error")
	}
}

func TestDialPool(t *testing.T) {
	ctx := context.Background()
	db := &fakeDB{}
	n := 0
	_, err := dialPool(ctx, 3, func(context.Context) (execer, error) {
		if n++; n == 3 {
			return nil, fmt.Errorf("refused")
		}
		return db, nil
	})
	if err == nil || db.closed != 2 {
		t.Errorf("dialPool: %v, %d closed", err, db.closed)
	}
}

func TestOpenSource(t *testing.T) {
	ctx := context.Background()
	db := &fakeDB{}
	useDB(t, db)
	for sc, want := range map[string]string{"files": "*main.fileSource", "pg-kafka": "*main.sqlSource", "mysql-es": "*main.sqlSource"} {
		src, err := openSource(ctx, opts(t, "prepare", "-scenario", sc), 2)
		if err != nil || fmt.Sprintf("%T", src) != want {
			t.Errorf("%s: %T, %v", sc, src, err)
		}
	}
	mysqlDial = func(context.Context, string, string, string) (execer, error) { return nil, fmt.Errorf("refused") }
	pgDial = func(context.Context, string, string, string, string) (execer, error) {
		return nil, fmt.Errorf("refused")
	}
	for _, sc := range []string{"pg-kafka", "mysql-es"} {
		if _, err := openSource(ctx, opts(t, "prepare", "-scenario", sc), 1); err == nil {
			t.Errorf("%s: no error", sc)
		}
	}
	if _, err := openCounter(ctx, opts(t, "prepare", "-scenario", "mysql-batch")); err == nil {
		t.Error("batch counter: no error")
	}
}

func TestMySQLConn(t *testing.T) {
	ctx := ctxT(t)
	addr := fakeMySQL(t, func() int64 { return 42 })
	c, err := dialMySQL(ctx, addr, "root", "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if err := c.exec(ctx, "CREATE TABLE x"); err != nil {
		t.Error(err)
	}
	if err := c.exec(ctx, "FAIL"); err == nil {
		t.Error("FAIL: no error")
	}
	if n, err := c.count(ctx, "SELECT COUNT(*) FROM x"); n != 42 || err != nil {
		t.Errorf("count: %d, %v", n, err)
	}
	if _, err := c.count(ctx, "FAIL"); err == nil {
		t.Error("count FAIL: no error")
	}
	empty, _ := dialMySQL(ctx, fakeMySQL(t, nil), "root", "pw")
	if _, err := empty.count(ctx, "SELECT COUNT(*) FROM x"); err == nil {
		t.Error("an empty count: no error")
	}
	empty.close()
	if _, err := dialMySQL(ctx, "127.0.0.1:1", "root", "pw"); err == nil {
		t.Error("refused: no error")
	}
}

func TestPGConn(t *testing.T) {
	ctx := ctxT(t)
	addr := fakePG(t, func() int64 { return 7 })
	c, err := dialPG(ctx, addr, "postgres", "pw", "shop")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if err := c.exec(ctx, "CREATE TABLE x"); err != nil {
		t.Error(err)
	}
	if err := c.exec(ctx, "FAIL"); err == nil {
		t.Error("FAIL: no error")
	}
	if n, err := c.count(ctx, "SELECT count(*) FROM x"); n != 7 || err != nil {
		t.Errorf("count: %d, %v", n, err)
	}
	if _, err := c.count(ctx, "SELECT none"); err == nil {
		t.Error("no rows: no error")
	}
	if _, err := c.count(ctx, "FAIL"); err == nil {
		t.Error("count FAIL: no error")
	}
	if _, err := dialPG(ctx, "127.0.0.1:1", "u", "p", "d"); err == nil {
		t.Error("refused: no error")
	}
}

func TestToInt(t *testing.T) {
	for _, v := range []any{int64(3), uint64(3), []byte("3"), "3"} {
		if n, err := toInt(v); n != 3 || err != nil {
			t.Errorf("toInt(%#v) = %d, %v", v, n, err)
		}
	}
	if _, err := toInt(1.5); err == nil {
		t.Error("a float: no error")
	}
}

func TestESCounter(t *testing.T) {
	ctx := context.Background()
	var answer atomic.Value
	answer.Store("200 {\"count\": 12}")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, body, _ := strings.Cut(answer.Load().(string), " ")
		var n int
		fmt.Sscan(code, &n)
		w.WriteHeader(n)
		io.WriteString(w, body)
	}))
	defer srv.Close()
	c, err := openCounter(ctx, opts(t, "prepare", "-scenario", "mysql-es", "-es", srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if n, err := c.count(ctx); n != 12 || err != nil {
		t.Errorf("count: %d, %v", n, err)
	}
	answer.Store("404 {}")
	if n, err := c.count(ctx); n != 0 || err != nil {
		t.Errorf("404: %d, %v", n, err)
	}
	for _, a := range []string{"500 broken", "200 {}", "200 nope"} {
		answer.Store(a)
		if _, err := c.count(ctx); err == nil {
			t.Errorf("%s: no error", a)
		}
	}
	bad := &esCounter{client: http.DefaultClient, url: "http://127.0.0.1:1/x"}
	if _, err := bad.count(ctx); err == nil {
		t.Error("refused: no error")
	}
	bad.url = ":"
	if _, err := bad.count(ctx); err == nil {
		t.Error("a bad URL: no error")
	}
}

func TestLokiCounter(t *testing.T) {
	ctx := context.Background()
	var answer atomic.Value
	answer.Store("200 # HELP loki_distributor_lines_received_total The total number of lines received.\n" +
		"loki_distributor_lines_received_total{tenant=\"a\"} 12\nloki_distributor_lines_received_total{tenant=\"b\"} 3e+00\nother 7\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			w.WriteHeader(404)
			return
		}
		code, body, _ := strings.Cut(answer.Load().(string), " ")
		var n int
		fmt.Sscan(code, &n)
		w.WriteHeader(n)
		io.WriteString(w, body)
	}))
	defer srv.Close()
	c, err := openCounter(ctx, opts(t, "prepare", "-scenario", "files-loki", "-loki", srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if n, err := c.count(ctx); n != 15 || err != nil {
		t.Errorf("count: %d, %v", n, err)
	}
	answer.Store("200 nothing\n")
	if n, err := c.count(ctx); n != 0 || err != nil {
		t.Errorf("none: %d, %v", n, err)
	}
	for _, a := range []string{"500 broken", "200 loki_distributor_lines_received_total x"} {
		answer.Store(a)
		if _, err := c.count(ctx); err == nil {
			t.Errorf("%s: no error", a)
		}
	}
	bad := &lokiCounter{client: http.DefaultClient, url: "http://127.0.0.1:1/metrics"}
	if _, err := bad.count(ctx); err == nil {
		t.Error("refused: no error")
	}
	bad.url = ":"
	if _, err := bad.count(ctx); err == nil {
		t.Error("a bad URL: no error")
	}
}

func TestKafkaCounters(t *testing.T) {
	ctx := ctxT(t)
	k := newFakeKafka(t)
	c, err := openCounter(ctx, opts(t, "prepare", "-scenario", "mysql-kafka", "-kafka", k.addr, "-kafka-topic", "t"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	cc, err := openCounter(ctx, opts(t, "prepare", "-scenario", "mysql-kafka", "-kafka", k.addr, "-kafka-topic", "t", "-kafka-format", "canal"))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.close()
	// No topic yet: nothing counted.
	for _, c := range []counter{c, cc} {
		if n, err := c.count(ctx); n != 0 || err != nil {
			t.Errorf("%T before the topic: %d, %v", c, n, err)
		}
	}
	k.add("t", `{"data":[{"id":"1"},{"id":"2"}]}`, `{"data":[{"id":"3"}]}`, `{"data":null}`, `{"data":[{},{},{}]}`, `{"data":[{}]}`)
	if n, err := c.count(ctx); n != 5 || err != nil {
		t.Errorf("messages: %d, %v", n, err)
	}
	if n, err := cc.count(ctx); n != 7 || err != nil {
		t.Errorf("canal rows: %d, %v", n, err)
	}
	k.add("t", `{"data":[{}]}`)
	if n, err := cc.count(ctx); n != 8 || err != nil {
		t.Errorf("canal rows, again: %d, %v", n, err)
	}
	k.add("t", `not json`)
	if _, err := cc.count(ctx); err == nil {
		t.Error("a message that is not JSON: no error")
	}
	// A broker that went away.
	dead, _ := openCounter(ctx, opts(t, "prepare", "-scenario", "mysql-kafka", "-kafka", "127.0.0.1:1", "-kafka-format", "canal"))
	if _, err := dead.count(ctx); err == nil {
		t.Error("canal, no broker: no error")
	}
	dead.close()
	dead, _ = openCounter(ctx, opts(t, "prepare", "-scenario", "mysql-kafka", "-kafka", "127.0.0.1:1"))
	if _, err := dead.count(ctx); err == nil {
		t.Error("no broker: no error")
	}
	dead.close()
	if _, err := openCounter(ctx, opts(t, "prepare", "-scenario", "mysql-kafka", "-kafka", "")); err == nil {
		t.Error("no brokers: no error")
	}
}

func TestSQLCounter(t *testing.T) {
	ctx := context.Background()
	db := &fakeDB{rows: 9}
	useDB(t, db)
	c, err := openCounter(ctx, opts(t, "prepare", "-scenario", "mysql-batch"))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := c.count(ctx); n != 9 || err != nil {
		t.Errorf("count: %d, %v", n, err)
	}
	c.close()
	if db.closed != 1 {
		t.Error("not closed")
	}
}

// fakeDocker is the Docker API's part measure uses, for the containers it
// names: each container's CPU grows by a millisecond a call to its stats.
type fakeDocker struct {
	mu     sync.Mutex
	calls  []string
	cpu    map[string]int64
	fail   string
	v1     bool
	onCall func(method, path string)
}

func newFakeDocker(t *testing.T) (*fakeDocker, string) {
	d := &fakeDocker{cpu: map[string]int64{}}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return d, srv.URL
}

func (d *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	call := r.Method + " " + r.URL.Path
	d.calls = append(d.calls, call)
	fail := d.fail != "" && strings.Contains(call, d.fail)
	on := d.onCall
	d.mu.Unlock()
	if on != nil {
		on(r.Method, r.URL.Path)
	}
	if fail {
		http.Error(w, "no such container", http.StatusNotFound)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/stats") {
		name := strings.Split(r.URL.Path, "/")[3]
		d.mu.Lock()
		d.cpu[name] += int64(time.Millisecond)
		cpu := d.cpu[name]
		v1 := d.v1
		d.mu.Unlock()
		mem := `{"anon": 1048576}`
		if v1 {
			mem = `{"rss": 2097152}`
		}
		if name == "garbled" {
			io.WriteString(w, "{")
			return
		}
		fmt.Fprintf(w, `{"cpu_stats": {"cpu_usage": {"total_usage": %d}}, "memory_stats": {"stats": %s}}`, cpu, mem)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *fakeDocker) called(s string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if strings.Contains(c, s) {
			n++
		}
	}
	return n
}

func TestDocker(t *testing.T) {
	ctx := context.Background()
	fd, url := newFakeDocker(t)
	d, err := newDocker(url+"/", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []func(context.Context, string) error{d.start, d.pause, d.unpause} {
		if err := f(ctx, "a"); err != nil {
			t.Error(err)
		}
	}
	u, err := d.sample(ctx, []string{"a", "b"})
	if err != nil || u.anon != 2<<20 || u.cpu != 2*time.Millisecond {
		t.Errorf("sample: %+v, %v", u, err)
	}
	fd.v1 = true
	if u, _ := d.stats(ctx, "a"); u.anon != 2<<20 {
		t.Errorf("cgroup v1: %+v", u)
	}
	if _, err := d.stats(ctx, "garbled"); err == nil {
		t.Error("garbled stats: no error")
	}
	fd.fail = "/b/"
	if _, err := d.sample(ctx, []string{"a", "b"}); err == nil {
		t.Error("a missing container: no error")
	}
	for _, e := range []string{"tcp://x", "%zz"} {
		if _, err := newDocker(e, time.Second); err == nil {
			t.Errorf("%q: no error", e)
		}
	}
	dead, _ := newDocker("http://127.0.0.1:1", time.Second)
	if err := dead.pause(ctx, "a"); err == nil {
		t.Error("refused: no error")
	}
	if _, err := dead.call(ctx, "BAD METHOD", "/x"); err == nil {
		t.Error("a bad method: no error")
	}
}

// A Unix socket's endpoint reaches the server listening on it.
func TestUnixEndpoint(t *testing.T) {
	dir, err := os.MkdirTemp("", "sb")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no Unix sockets:", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"cpu_stats": {"cpu_usage": {"total_usage": 5}}, "memory_stats": {"stats": {"anon": 3}}}`)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	d, err := newDocker("unix://"+sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := d.stats(context.Background(), "x"); err != nil || u.anon != 3 {
		t.Errorf("stats over a Unix socket: %+v, %v", u, err)
	}
}

func TestLagOf(t *testing.T) {
	if l := lagOf(10, nil); l.Rate != 10 || l.Samples != 0 {
		t.Errorf("no lags: %+v", l)
	}
	l := lagOf(10, []float64{3, 1, 2, 100})
	if l.P50 != 2 || l.P99 != 3 || l.Max != 100 || l.Mean != 26.5 {
		t.Errorf("lags: %+v", l)
	}
}

func readResult(t *testing.T, path string) *Result {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}
