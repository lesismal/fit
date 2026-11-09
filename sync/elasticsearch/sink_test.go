package syncelastic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() { backoffFirst, backoffMost = time.Millisecond, 5*time.Millisecond }

// fakeES is a cluster of one node keeping documents in memory, whose
// answers a test can change.
type fakeES struct {
	mu           sync.Mutex
	distribution string
	docs         map[string]string // index/_id -> document
	routing      map[string]string
	writes       []string // index/_id of each item written, in order
	bulks        int
	queries      []string
	auth         string
	// busy answers the next busy bulks 429 whole.
	busy int
	// item answers an item by its index/_id, and the times it was seen:
	// 0 to apply it.
	item func(id string, seen int) int
	seen map[string]int
	srv  *httptest.Server
}

func newFake(t *testing.T) *fakeES {
	f := &fakeES{docs: map[string]string{}, routing: map[string]string{}, seen: map[string]int{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeES) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	if r.URL.Path == "/" {
		dist := ""
		if f.distribution != "" {
			dist = fmt.Sprintf(`, "distribution": %q`, f.distribution)
		}
		fmt.Fprintf(w, `{"name": "n1", "version": {"number": "8.19.3"%s}, "tagline": "You Know, for Search"}`, dist)
		return
	}
	if r.URL.Path != "/_bulk" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-ndjson" {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}
	f.bulks++
	f.queries = append(f.queries, r.URL.RawQuery)
	if f.busy > 0 {
		f.busy--
		http.Error(w, `{"status": 429}`, http.StatusTooManyRequests)
		return
	}
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(nil, 1<<24)
	var items []string
	for sc.Scan() {
		var head map[string]map[string]string
		if err := json.Unmarshal(sc.Bytes(), &head); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for action, meta := range head {
			id := meta["_index"] + "/" + meta["_id"]
			if meta["_id"] == "" {
				id = fmt.Sprintf("%s/auto-%d", meta["_index"], len(f.docs))
			}
			var doc string
			if action != "delete" {
				sc.Scan()
				doc = sc.Text()
			}
			f.seen[id]++
			st := 0
			if f.item != nil {
				st = f.item(id, f.seen[id])
			}
			_, exists := f.docs[id]
			switch {
			case st != 0:
			case action == "delete" && !exists:
				st = 404
			case action == "delete":
				delete(f.docs, id)
				st = 200
			case action == "create" && exists:
				st = 409
			default:
				f.docs[id], f.routing[id] = doc, meta["routing"]
				st = 201
			}
			if st < 300 {
				f.writes = append(f.writes, action+" "+id)
			}
			res := fmt.Sprintf(`{"%s": {"_id": %q, "status": %d`, action, id, st)
			if st >= 300 {
				res += fmt.Sprintf(`, "error": {"type": "e%d", "reason": "status %d"}`, st, st)
			}
			items = append(items, res+"}}")
		}
	}
	fmt.Fprintf(w, `{"took": 1, "errors": true, "items": [%s]}`, strings.Join(items, ","))
}

func (f *fakeES) doc(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.docs[id]
}

func load(t *testing.T, params string) *Sink {
	t.Helper()
	s, err := loadErr(params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(*Sink)
}

func loadErr(params string) (any, error) {
	src := fmt.Sprintf(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate"}, "sinks": [{"name": "s", "sink": {"module": "elasticsearch", "params": %s}}]}]}}`, params)
	c, err := config.Load([]byte(src))
	if err != nil {
		return nil, err
	}
	return module.Load(fitsync.Sinks, c.Sync.Pipelines[0].Sinks[0].Sink, nil)
}

func row(id int64, v string) fitsync.Value {
	return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)})
}

func rec(op fitsync.Op, id int64, v string) *fitsync.Record {
	r := &fitsync.Record{Namespace: "shop.orders", Op: op, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}),
		CommitTime: time.Date(2026, 11, 8, 23, 30, 0, 0, time.UTC)}
	if op == fitsync.OpDelete {
		r.Before = row(id, v)
	} else {
		r.After = row(id, v)
	}
	return r
}

func TestDocID(t *testing.T) {
	for _, c := range []struct {
		key  fitsync.Value
		want string
	}{
		{fitsync.Int(-5), "-5"},
		{fitsync.String("a:b"), "a:b"},
		{fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Uint(7)}), "7"},
		{fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.String(`x:y\z`)}, fitsync.Field{Name: "b", Value: fitsync.Int(2)}), `x\:y\\z:2`},
		{fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Date(2026, 1, 2)}, fitsync.Field{Name: "b", Value: fitsync.Bytes([]byte{0xff, 0})}), "2026-01-02:/wA="},
		{fitsync.Array(fitsync.String("a"), fitsync.Int(1)), `["a",1]`},
	} {
		if got, ok := DocID(c.key); !ok || got != c.want {
			t.Errorf("DocID(%v) = %q, want %q", c.key, got, c.want)
		}
	}
	if _, ok := DocID(fitsync.Null()); ok {
		t.Error("a null key has an _id")
	}
}

func TestIndexTemplate(t *testing.T) {
	f := newFake(t)
	s := load(t, fmt.Sprintf(`{"urls": [%q], "index": "logs-{namespace}-{yyyy.MM.dd}-{HH}{{x", "time_zone": "Asia/Shanghai"}`, f.srv.URL))
	if got := s.indexOf(rec(fitsync.OpInsert, 1, "a"), time.Now()); got != "logs-shop.orders-2026.11.09-07{x" {
		t.Errorf("index %q", got)
	}
	r := rec(fitsync.OpInsert, 1, "a")
	r.CommitTime = time.Time{}
	now := time.Date(2025, 2, 3, 0, 0, 0, 0, time.UTC)
	if got := s.indexOf(r, now); got != "logs-shop.orders-2025.02.03-08{x" {
		t.Errorf("index without a commit time %q", got)
	}
	for _, bad := range []string{`"{namespace"`, `"{yyyy/MM}"`, `"{}"`, `"{name}"`} {
		if _, err := loadErr(fmt.Sprintf(`{"urls": [%q], "index": %s}`, f.srv.URL, bad)); err == nil {
			t.Errorf("index %s loaded", bad)
		}
	}
}

func TestProvisionRefuses(t *testing.T) {
	t.Setenv("FIT_TEST_ES_KEY", "a2V5")
	for _, c := range []struct{ params, want string }{
		{`{"urls": []}`, "no URLs"},
		{`{"urls": ["ftp://x"]}`, "not an http or https URL"},
		{`{"urls": ["http://x"], "username": "u"}`, "go together"},
		{`{"urls": ["http://x"], "username": "u", "password_env": "FIT_TEST_ES_UNSET"}`, "is not set"},
		{`{"urls": ["http://x"], "username": "u", "api_key_env": "FIT_TEST_ES_KEY"}`, "not both"},
		{`{"urls": ["http://x"], "api_key_env": "1X"}`, "not an environment variable"},
		{`{"urls": ["http://x"], "ca_file": "/nonexistent.pem"}`, "nonexistent"},
		{`{"urls": ["http://x"], "cert_file": "c.pem"}`, "go together"},
		{`{"urls": ["http://x"], "action": "update"}`, "update"},
	} {
		if _, err := loadErr(c.params); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.params, err, c.want)
		}
	}
}

// TestWrite writes inserts, updates and deletes, written again as a replay
// writes them, and DDL, which is skipped.
func TestWrite(t *testing.T) {
	f := newFake(t)
	t.Setenv("FIT_TEST_ES_PASSWORD", "pw")
	s := load(t, fmt.Sprintf(`{"urls": [%q], "username": "elastic", "password_env": "FIT_TEST_ES_PASSWORD", "pipeline": "p1", "routing": "v"}`, f.srv.URL))
	batch := []*fitsync.Record{rec(fitsync.OpInsert, 1, "a"), rec(fitsync.OpInsert, 2, "b"), rec(fitsync.OpUpdate, 1, "a"),
		{Namespace: "shop.orders", Op: fitsync.OpDDL, After: fitsync.String("ALTER TABLE")}, rec(fitsync.OpDelete, 2, "b"), rec(fitsync.OpDelete, 3, "c")}
	batch[2].After = fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}, fitsync.Field{Name: "v", Value: fitsync.String("a")}, fitsync.Field{Name: "n", Value: fitsync.Float(1.5)})
	for range 2 {
		if err := s.Write(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
	if d := f.doc("shop.orders/1"); d != `{"id":1,"v":"a","n":1.5}` {
		t.Errorf("document 1: %s", d)
	}
	if len(f.docs) != 1 || f.routing["shop.orders/1"] != "a" {
		t.Errorf("documents %v, routing %v", f.docs, f.routing)
	}
	if f.auth != "Basic ZWxhc3RpYzpwdw==" || !strings.Contains(f.queries[0], "pipeline=p1") {
		t.Errorf("auth %q, query %q", f.auth, f.queries[0])
	}
	// A message without a key gets an _id from the cluster; an update without one fails.
	err := s.Write(context.Background(), []*fitsync.Record{{Namespace: "m", Op: fitsync.OpMessage, After: row(9, "x")}, {Namespace: "m", Op: fitsync.OpUpdate, After: row(9, "y")}})
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "no key") {
		t.Errorf("%v", err)
	}
}

// TestCreate counts a document already there as written.
func TestCreate(t *testing.T) {
	f := newFake(t)
	s := load(t, fmt.Sprintf(`{"urls": [%q], "action": "create", "index": "logs-{yyyy.MM}"}`, f.srv.URL))
	for range 2 {
		if err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 1, "a"), rec(fitsync.OpUpdate, 1, "b")}); err != nil {
			t.Fatal(err)
		}
	}
	if d := f.doc("logs-2026.11/1"); d != `{"id":1,"v":"a"}` {
		t.Errorf("document %q", d)
	}
}

// TestOneBadDocument fails only the record the cluster refuses, as a
// permanent BatchError at it; those after it are written.
func TestOneBadDocument(t *testing.T) {
	f := newFake(t)
	f.item = func(id string, _ int) int {
		if id == "shop.orders/3" {
			return 400
		}
		return 0
	}
	s := load(t, fmt.Sprintf(`{"urls": [%q]}`, f.srv.URL))
	var batch []*fitsync.Record
	for i := range 6 {
		batch = append(batch, rec(fitsync.OpInsert, int64(i), "v"))
	}
	err := s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 3 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "e400: status 400") {
		t.Fatalf("%v", err)
	}
	if len(f.docs) != 5 || f.bulks != 1 {
		t.Errorf("%d documents in %d bulks", len(f.docs), f.bulks)
	}
}

// TestItemsSentAgain sends items the cluster was busy or failing for again,
// alone and with the later writes of their documents, so that a document's
// last write stays last.
func TestItemsSentAgain(t *testing.T) {
	f := newFake(t)
	f.item = func(id string, seen int) int {
		switch {
		case id == "shop.orders/1" && seen <= 3:
			return 429
		case id == "shop.orders/2" && seen == 1:
			return 503
		}
		return 0
	}
	s := load(t, fmt.Sprintf(`{"urls": [%q]}`, f.srv.URL))
	batch := []*fitsync.Record{rec(fitsync.OpInsert, 1, "a"), rec(fitsync.OpInsert, 2, "b"), rec(fitsync.OpInsert, 3, "c"),
		rec(fitsync.OpUpdate, 1, "a2"), rec(fitsync.OpDelete, 3, "c")}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if d := f.doc("shop.orders/1"); d != `{"id":1,"v":"a2"}` || f.doc("shop.orders/3") != "" || f.doc("shop.orders/2") == "" {
		t.Errorf("documents %v", f.docs)
	}
	want := "index shop.orders/3,delete shop.orders/3,index shop.orders/2,index shop.orders/1,index shop.orders/1,index shop.orders/1"
	if got := strings.Join(f.writes, ","); got != want {
		t.Errorf("writes %s\nwant %s", got, want)
	}
	// A 5xx that lasts fails the write at its record, after DefaultItemAttempts.
	f.item = func(id string, _ int) int {
		if id == "shop.orders/2" {
			return 500
		}
		return 0
	}
	f.bulks = 0
	err := s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || fitsync.IsPermanent(err) || f.bulks != DefaultItemAttempts {
		t.Errorf("%v after %d bulks", err, f.bulks)
	}
}

// TestBusyCluster waits out a cluster answering 429 to whole bulks, losing
// nothing.
func TestBusyCluster(t *testing.T) {
	f := newFake(t)
	f.busy = 8
	s := load(t, fmt.Sprintf(`{"urls": [%q]}`, f.srv.URL))
	if err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 1, "a")}); err != nil {
		t.Fatal(err)
	}
	if f.bulks != 9 || f.doc("shop.orders/1") == "" {
		t.Errorf("%d bulks, %v", f.bulks, f.docs)
	}
	// It waits only while the pipeline runs.
	f.busy = 1 << 30
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Write(ctx, []*fitsync.Record{rec(fitsync.OpInsert, 2, "a")}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%v", err)
	}
}

// TestBusyWarns: a 429 that lasts, to whole bulks or to items, as a
// read-only index of the flood-stage disk watermark answers, is logged as a
// warning with the cluster's answer, not waited out in silence.
func TestBusyWarns(t *testing.T) {
	var mu sync.Mutex
	var warned []string
	every, old := busyWarnEvery, warnf
	busyWarnEvery = 10 * time.Millisecond
	warnf = func(format string, args ...any) {
		mu.Lock()
		warned = append(warned, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	t.Cleanup(func() { busyWarnEvery, warnf = every, old })
	got := func() string {
		mu.Lock()
		defer mu.Unlock()
		s := strings.Join(warned, "\n")
		warned = nil
		return s
	}
	f := newFake(t)
	s := load(t, fmt.Sprintf(`{"urls": [%q]}`, f.srv.URL))
	// Whole bulks: the 5ms backoff, 30 times past 10ms.
	f.busy = 30
	if err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 1, "a")}); err != nil {
		t.Fatal(err)
	}
	if w := got(); !strings.Contains(w, "answered 429 for") || !strings.Contains(w, `{"status": 429}`) {
		t.Fatalf("warned %q", w)
	}
	// Items.
	f.item = func(id string, seen int) int {
		if seen <= 30 {
			return 429
		}
		return 0
	}
	if err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 2, "b")}); err != nil {
		t.Fatal(err)
	}
	if w := got(); !strings.Contains(w, "answered 429 for") || !strings.Contains(w, "e429: status 429") {
		t.Fatalf("warned %q", w)
	}
	// A short 429 is not warned of.
	busyWarnEvery = time.Hour
	f.busy = 2
	if err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 3, "c")}); err != nil {
		t.Fatal(err)
	}
	if w := got(); w != "" {
		t.Fatalf("warned %q", w)
	}
}

// TestFailover goes on to the next URL when one does not answer, or
// answers 503, and splits a batch into bulks of max_request_bytes.
func TestFailover(t *testing.T) {
	f := newFake(t)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	s := load(t, fmt.Sprintf(`{"urls": [%q, %q, %q], "max_request_bytes": 200}`, closed.URL, down.URL, f.srv.URL))
	var batch []*fitsync.Record
	for i := range 10 {
		batch = append(batch, rec(fitsync.OpInsert, int64(i), "v"))
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if len(f.docs) != 10 || f.bulks < 3 {
		t.Errorf("%d documents in %d bulks", len(f.docs), f.bulks)
	}
	f.srv.Close()
	err := s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 0 || fitsync.IsPermanent(err) {
		t.Errorf("%v", err)
	}
}

func TestOpenSearch(t *testing.T) {
	f := newFake(t)
	f.distribution = "opensearch"
	t.Setenv("FIT_TEST_ES_KEY", "a2V5")
	s := load(t, fmt.Sprintf(`{"urls": [%q], "api_key_env": "FIT_TEST_ES_KEY"}`, f.srv.URL))
	if err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 1, "a")}); err == nil || !strings.Contains(err.Error(), "no API keys") {
		t.Errorf("%v", err)
	}
	f.distribution = ""
	s = load(t, fmt.Sprintf(`{"urls": [%q], "api_key_env": "FIT_TEST_ES_KEY"}`, f.srv.URL))
	if err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 1, "a")}); err != nil || f.auth != "ApiKey a2V5" {
		t.Errorf("%v, %q", err, f.auth)
	}
	info, _ := s.conn.Info(context.Background())
	if info.OpenSearch() || info.Version != "8.19.3" {
		t.Errorf("%+v", info)
	}
}

// TestPipelineSkipsBadDocument runs a pipeline whose sink refuses a
// document for good: on_error skip takes it at once, without the retries,
// and the rest is written.
func TestPipelineSkipsBadDocument(t *testing.T) {
	f := newFake(t)
	f.item = func(id string, _ int) int {
		if id == "generate.records/10" {
			return 400
		}
		return 0
	}
	src := fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [{"name": "p", "mode": "once", "on_error": "skip",
		"retry": {"max_attempts": 50, "backoff": "1s"},
		"source": {"module": "generate", "params": {"records": 50}},
		"sinks": [{"name": "es", "sink": {"module": "elasticsearch", "params": {"urls": [%q]}}, "batch": {"max_records": 20, "max_wait": "1ms"}}]}]}}`,
		t.TempDir(), f.srv.URL)
	c, err := config.Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	st, err := fitsync.RunPipeline(context.Background(), c.Sync, "p", fitsync.Options{})
	if err != nil || st.State != "finished" {
		t.Fatalf("%v %+v", err, st)
	}
	// A permanent refusal is not retried: the bad document was sent once.
	// (Counted, not timed: a run under the race detector on a busy runner
	// may take longer than the backoff it skips.)
	f.mu.Lock()
	sent := f.seen["generate.records/10"]
	f.mu.Unlock()
	if sent != 1 {
		t.Errorf("the refused document was sent %d times", sent)
	}
	// 50 records, a seventh of them deletes of documents never written.
	if n := len(f.docs); n != 50-7-1 {
		t.Errorf("%d documents", n)
	}
	if st.Job == nil || st.Job.Failed != 1 {
		t.Errorf("job %+v", st.Job)
	}
}
