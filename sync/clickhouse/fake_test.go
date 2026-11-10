package syncclickhouse

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// fakeCH is a ClickHouse HTTP interface whose answers a test gives: answer
// is called with each request's settings and SQL (the query param, or the
// body when there is none) and its body, and returns the status, the error
// code and the body. It keeps the requests.
type fakeCH struct {
	mu     sync.Mutex
	reqs   []fakeReq
	answer func(q url.Values, sql, body string) (int, int, string)
	srv    *httptest.Server
}

type fakeReq struct {
	q         url.Values
	sql, body string
	auth      string
}

func newFakeCH(t *testing.T, answer func(q url.Values, sql, body string) (int, int, string)) *fakeCH {
	f := &fakeCH{answer: answer}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	q := r.URL.Query()
	sql, body := q.Get("query"), string(b)
	if sql == "" {
		sql = body
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, fakeReq{q: q, sql: sql, body: body, auth: r.Header.Get("Authorization")})
	answer := f.answer
	f.mu.Unlock()
	status, code, out := http.StatusOK, 0, ""
	if answer != nil {
		status, code, out = answer(q, sql, body)
	}
	if code != 0 {
		w.Header().Set("X-ClickHouse-Exception-Code", strconv.Itoa(code))
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, out)
}

// set changes the answers.
func (f *fakeCH) set(answer func(q url.Values, sql, body string) (int, int, string)) {
	f.mu.Lock()
	f.answer = answer
	f.mu.Unlock()
}

func (f *fakeCH) requests() []fakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeReq(nil), f.reqs...)
}

// loadMod loads a clickhouse module of ns with params, cleaned up with the
// test.
func loadMod(t *testing.T, ns string, params map[string]any) any {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(ns, config.ModuleRef{Module: "clickhouse", Params: v}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m
}

func fakeSink(t *testing.T, f *fakeCH, params map[string]any) *Sink {
	p := map[string]any{"urls": []string{f.srv.URL}}
	for k, v := range params {
		p[k] = v
	}
	return loadMod(t, fitsync.Sinks, p).(*Sink)
}

func fakeSource(t *testing.T, f *fakeCH, params map[string]any) *Source {
	p := map[string]any{"urls": []string{f.srv.URL}}
	for k, v := range params {
		p[k] = v
	}
	return loadMod(t, fitsync.Sources, p).(*Source)
}

func rowOf(id int64, v string) fitsync.Value {
	return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)})
}

func keyOf(id int64) fitsync.Value {
	return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)})
}

func rec(n int, op fitsync.Op, id int64, v string) *fitsync.Record {
	r := &fitsync.Record{Namespace: "db.t", Op: op, Key: keyOf(id), CommitTime: time.Unix(1_700_000_000+int64(n), 0),
		Position: fitsync.Position(`{"n":` + strconv.Itoa(n) + `}`)}
	if op == fitsync.OpDelete {
		r.Before = rowOf(id, v)
	} else {
		r.After = rowOf(id, v)
	}
	return r
}

func TestSinkWrites(t *testing.T) {
	t.Setenv("FIT_TEST_CH_PW", "secret")
	f := newFakeCH(t, nil)
	s := fakeSink(t, f, map[string]any{"username": "u", "password_env": "FIT_TEST_CH_PW", "database": "db0",
		"settings": map[string]string{"async_insert": "1"}})
	s.ParallelWrites()
	batch := []*fitsync.Record{
		rec(1, fitsync.OpInsert, 1, "a"),
		rec(2, fitsync.OpUpdate, 1, "b"),
		{Namespace: "db.t", Op: fitsync.OpDDL},
		rec(3, fitsync.OpDelete, 1, "b"),
		{Namespace: "db.t", Op: fitsync.OpDelete, Key: keyOf(2), CommitTime: time.Unix(5, 0)},
		{Namespace: "db.u", Op: fitsync.OpMessage, After: rowOf(9, "m")},
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 3 { // the DDL record ends a run
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	if r.sql != "INSERT INTO `db`.`t` FORMAT JSONEachRow" || r.q.Get("async_insert") != "1" || r.q.Get("database") != "db0" ||
		!strings.HasPrefix(r.q.Get("insert_deduplication_token"), "fit-") || r.auth == "" || r.q.Get("date_time_input_format") != "best_effort" {
		t.Errorf("%+v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.body+reqs[1].body), "\n")
	want := []string{
		`{"id":1,"v":"a","_version":1700000001000000000,"_deleted":0}`,
		`{"id":1,"v":"b","_version":1700000002000000000,"_deleted":0}`,
		`{"id":1,"v":"b","_version":1700000003000000000,"_deleted":1}`,
		`{"id":2,"_version":5000000000,"_deleted":1}`,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s", r.body)
	}
	if reqs[2].sql != "INSERT INTO `db`.`u` FORMAT JSONEachRow" || reqs[2].q.Get("insert_deduplication_token") != "" {
		t.Errorf("%+v", reqs[2])
	}
}

func TestSinkDeletesAndPlain(t *testing.T) {
	f := newFakeCH(t, nil)
	s := fakeSink(t, f, map[string]any{"deletes": "delete", "plain": true, "table": "x_{$namespace}"})
	batch := []*fitsync.Record{rec(1, fitsync.OpInsert, 1, "a"), rec(2, fitsync.OpDelete, 1, "a"), rec(3, fitsync.OpDelete, 2, "a"), rec(4, fitsync.OpInsert, 3, "c")}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 3 || reqs[0].q.Get("insert_deduplicate") != "0" || reqs[0].body != `{"id":1,"v":"a"}`+"\n" ||
		reqs[1].sql != "DELETE FROM `x_db`.`t` WHERE (`id` = 1) OR (`id` = 2)" {
		t.Fatalf("%+v", reqs)
	}
	// ignore
	f2 := newFakeCH(t, nil)
	s2 := fakeSink(t, f2, map[string]any{"deletes": "ignore"})
	if err := s2.Write(context.Background(), []*fitsync.Record{rec(1, fitsync.OpDelete, 1, "a")}); err != nil || len(f2.requests()) != 0 {
		t.Fatal(err, f2.requests())
	}
	// A delete whose key is no map fails for good.
	err := s.Write(context.Background(), []*fitsync.Record{{Namespace: "t", Op: fitsync.OpDelete, Key: fitsync.Int(1)}})
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	// A failing DELETE.
	f.set(func(url.Values, string, string) (int, int, string) { return 500, 999, "boom" })
	err = s.Write(context.Background(), []*fitsync.Record{rec(1, fitsync.OpInsert, 1, "a"), rec(2, fitsync.OpDelete, 1, "a")})
	if !errors.As(err, &be) || be.Index != 0 || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	f.set(func(q url.Values, sql, _ string) (int, int, string) {
		if strings.HasPrefix(sql, "DELETE") {
			return 500, 999, "boom"
		}
		return 200, 0, ""
	})
	err = s.Write(context.Background(), []*fitsync.Record{rec(1, fitsync.OpInsert, 1, "a"), rec(2, fitsync.OpDelete, 1, "a")})
	if !errors.As(err, &be) || be.Index != 1 {
		t.Fatal(err)
	}
}

func TestSinkBuildErrors(t *testing.T) {
	f := newFakeCH(t, nil)
	s := fakeSink(t, f, map[string]any{"max_request_bytes": 60})
	sv := fakeSink(t, f, map[string]any{"version_field": "ver"})
	for _, c := range []struct {
		s    *Sink
		r    *fitsync.Record
		want string
	}{
		{s, &fitsync.Record{Namespace: "t", Op: fitsync.Op(99)}, "operation"},
		{s, &fitsync.Record{Namespace: "", Op: fitsync.OpInsert, After: rowOf(1, "a")}, "table is empty"},
		{s, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, After: fitsync.Int(1)}, "not a map"},
		{s, &fitsync.Record{Namespace: "t", Op: fitsync.OpDelete}, "row before, or key"},
		{s, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, After: rowOf(1, strings.Repeat("x", 100))}, "max_request_bytes"},
		{s, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.NaN())})}, "JSON number"},
		{sv, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, After: rowOf(1, "a")}, "no ver"},
		{sv, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "ver", Value: fitsync.Int(-1)})}, "not a uint"},
	} {
		err := c.s.Write(context.Background(), []*fitsync.Record{c.r})
		if err == nil || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
	sp := fakeSink(t, f, map[string]any{"plain": true, "deletes": "ignore"})
	if err := sp.Write(context.Background(), []*fitsync.Record{{Namespace: "t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.Inf(1))})}}); err == nil {
		t.Error("an infinite float written plain")
	}
}

func TestSinkVersions(t *testing.T) {
	s := &Sink{VersionField: "ver"}
	at := time.Unix(20, 5)
	for _, c := range []struct {
		row, key fitsync.Value
		want     uint64
	}{
		{fitsync.Map(fitsync.Field{Name: "ver", Value: fitsync.Uint(7)}), fitsync.Null(), 7},
		{fitsync.Map(fitsync.Field{Name: "ver", Value: fitsync.Int(8)}), fitsync.Null(), 8},
		{fitsync.Map(fitsync.Field{Name: "ver", Value: fitsync.Time(at)}), fitsync.Null(), uint64(at.UnixNano())},
		{fitsync.Map(fitsync.Field{Name: "ver", Value: fitsync.Null()}), fitsync.Map(fitsync.Field{Name: "ver", Value: fitsync.Uint(3)}), 3},
	} {
		v, err := s.version(&fitsync.Record{Key: c.key}, c.row)
		if err != nil || v != c.want {
			t.Errorf("%v %v", v, err)
		}
	}
	// Without a commit time, a snapshot's row: 0, below any change.
	if v, _ := (&Sink{}).version(&fitsync.Record{}, fitsync.Null()); v != 0 {
		t.Errorf("%d", v)
	}
	if v, _ := (&Sink{}).version(&fitsync.Record{CommitTime: at}, fitsync.Null()); v != uint64(at.UnixNano()) {
		t.Errorf("%d", v)
	}
}

func TestSinkSplitsAndBadRows(t *testing.T) {
	var inserts []string
	var mu sync.Mutex
	fail := ""
	f := newFakeCH(t, func(q url.Values, sql, body string) (int, int, string) {
		mu.Lock()
		defer mu.Unlock()
		inserts = append(inserts, body)
		lines := strings.Split(strings.TrimSpace(body), "\n")
		switch fail {
		case "row":
			for i, l := range lines {
				if strings.Contains(l, `"bad"`) {
					return 400, 27, "Code: 27. DB::Exception: Cannot parse input: (at row " + strconv.Itoa(i+1) + ")"
				}
			}
		case "norow":
			return 400, 6, "Code: 6. Cannot parse text"
		case "again":
			if len(inserts) > 1 {
				return 500, 999, "down"
			}
			return 400, 27, "(at row 3)"
		case "first":
			return 400, 27, "(at row 1)"
		}
		return 200, 0, ""
	})
	s := fakeSink(t, f, map[string]any{"max_request_bytes": 150})
	var batch []*fitsync.Record
	for i := range 6 {
		batch = append(batch, rec(i+1, fitsync.OpInsert, int64(i), "v"))
	}
	if err := s.Write(context.Background(), batch); err != nil || len(inserts) != 3 {
		t.Fatalf("%v: %d inserts", err, len(inserts))
	}
	var be *fitsync.BatchError
	whole := fakeSink(t, f, nil)
	check := func(mode string, idx int, permanent bool, n int) {
		t.Helper()
		mu.Lock()
		fail, inserts = mode, nil
		mu.Unlock()
		b := []*fitsync.Record{rec(1, fitsync.OpInsert, 1, "v"), rec(2, fitsync.OpInsert, 2, "v"), rec(3, fitsync.OpInsert, 3, "bad")}
		err := whole.Write(context.Background(), b)
		if !errors.As(err, &be) || be.Index != idx || fitsync.IsPermanent(err) != permanent || len(inserts) != n {
			t.Errorf("%s: %v (%d inserts)", mode, err, len(inserts))
		}
	}
	check("row", 2, true, 2)
	check("norow", 0, true, 1)
	check("again", 0, false, 2)
	check("first", 0, true, 1)
	mu.Lock()
	fail = ""
	mu.Unlock()
	f.set(func(url.Values, string, string) (int, int, string) { return 500, 241, "memory" })
	if err := s.Write(context.Background(), batch[:1]); !errors.As(err, &be) || fitsync.IsPermanent(err) {
		t.Errorf("%v", err)
	}
}

func TestLiterals(t *testing.T) {
	for _, c := range []struct {
		v    fitsync.Value
		want string
	}{
		{fitsync.Null(), "NULL"},
		{fitsync.Bool(true), "true"},
		{fitsync.Uint(5), "5"},
		{fitsync.Float(0.5), "0.5"},
		{fitsync.Bytes([]byte("a\x00\r")), `'a\0\r'`},
		{fitsync.Array(fitsync.Int(1)), "'[1]'"},
	} {
		if got, err := Literal(c.v); err != nil || got != c.want {
			t.Errorf("%v: %s %v", c.v, got, err)
		}
	}
	if _, err := Literal(fitsync.Float(math.NaN())); err == nil {
		t.Error("NaN")
	}
	if _, err := Literal(fitsync.Array(fitsync.Float(math.NaN()))); err == nil {
		t.Error("NaN in an array")
	}
	if _, err := keyCondition(fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.Inf(1))})); err == nil {
		t.Error("an infinite key")
	}
	if clip([]byte(strings.Repeat("x", 600))) != strings.Repeat("x", 500)+"..." {
		t.Error("clip")
	}
	e := &ServerError{Status: 500, Code: 1, Message: "m"}
	if e.Error() != "ClickHouse answered 500: m" || e.Row() != 0 || e.DataError() {
		t.Error(e)
	}
}

// writeCert writes a self-signed certificate and its key to dir.
func writeCert(t *testing.T, dir string) (string, string) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fit"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	cert, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	_ = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cert, keyFile
}

func TestConnParams(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeCert(t, dir)
	junk := filepath.Join(dir, "junk")
	_ = os.WriteFile(junk, []byte("junk"), 0o600)
	t.Setenv("FIT_TEST_CH_EMPTY", "")
	if err := load(t, fitsync.Sinks, "clickhouse", map[string]any{"urls": []string{"https://x"}, "ca_file": cert, "cert_file": cert, "key_file": key, "server_name": "fit"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{}, "no URLs"},
		{map[string]any{"password_env": "1bad", "username": "u"}, "not an environment variable"},
		{map[string]any{"password_env": "FIT_TEST_CH_EMPTY"}, "needs username"},
		{map[string]any{"username": "a:b"}, "no ':'"},
		{map[string]any{"ca_file": filepath.Join(dir, "none")}, "no such file"},
		{map[string]any{"ca_file": junk}, "no PEM"},
		{map[string]any{"cert_file": cert}, "go together"},
		{map[string]any{"cert_file": junk, "key_file": junk}, "cert_file"},
		{map[string]any{"settings": map[string]string{"a-b": "1"}}, "not a setting"},
	} {
		p := map[string]any{"urls": []string{"http://x"}}
		if _, ok := c.params["urls"]; !ok && c.want == "no URLs" {
			p = map[string]any{"urls": []string{}}
		}
		for k, v := range c.params {
			p[k] = v
		}
		if err := load(t, fitsync.Sinks, "clickhouse", p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.params, err, c.want)
		}
	}
}

func TestConnFailover(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	busy := newFakeCH(t, func(url.Values, string, string) (int, int, string) { return 503, 0, "busy" })
	ok := newFakeCH(t, func(url.Values, string, string) (int, int, string) { return 200, 0, "1\n" })
	s := loadMod(t, fitsync.Sinks, map[string]any{"urls": []string{down.URL, busy.srv.URL, ok.srv.URL + "/"}}).(*Sink)
	b, err := s.conn.Do(context.Background(), url.Values{"query": {"SELECT 1"}}, nil)
	if err != nil || string(b) != "1\n" || s.conn.cur.Load() != 2 {
		t.Fatalf("%q %v %d", b, err, s.conn.cur.Load())
	}
	// Every URL failing: the last error.
	s2 := loadMod(t, fitsync.Sinks, map[string]any{"urls": []string{busy.srv.URL, down.URL}}).(*Sink)
	if _, err := s2.conn.Do(context.Background(), url.Values{}, nil); err == nil || !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "refused") {
		t.Errorf("%v", err)
	}
	s3 := loadMod(t, fitsync.Sinks, map[string]any{"urls": []string{down.URL, busy.srv.URL}}).(*Sink)
	var se *ServerError
	if _, err := s3.conn.Do(context.Background(), url.Values{}, nil); !errors.As(err, &se) || se.Status != 503 {
		t.Errorf("%v", err)
	}
	// A context ended.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.conn.Do(ctx, url.Values{}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	s.conn.Close()
	s.conn.Close()
}

// TestConnTest: SELECT 1 in the database, and the source's table with
// LIMIT 0.
func TestConnTest(t *testing.T) {
	var mu sync.Mutex
	one, limit := "1\n", ""
	f := newFakeCH(t, func(q url.Values, sql, _ string) (int, int, string) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case sql == "SELECT 1":
			if q.Get("database") != "db" {
				return 404, 81, "Code: 81. DB::Exception: Database x does not exist"
			}
			return 200, 0, one
		case sql == "SELECT * FROM (SELECT * FROM `t`) LIMIT 0" && q.Get("date_time_output_format") == "iso":
			if limit != "" {
				return 404, 60, limit
			}
			return 200, 0, ""
		}
		return 400, 62, "unexpected " + sql
	})
	ctx := context.Background()
	sink := fakeSink(t, f, map[string]any{"database": "db"})
	src := fakeSource(t, f, map[string]any{"database": "db", "table": "t", "key": []string{"id"}})
	if err := sink.Test(ctx); err != nil {
		t.Fatalf("sink: %v", err)
	}
	if err := src.Test(ctx); err != nil {
		t.Fatalf("source: %v", err)
	}
	if err := fakeSink(t, f, nil).Test(ctx); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("no database: %v", err)
	}
	mu.Lock()
	limit = "Code: 60. DB::Exception: Table db.t does not exist"
	mu.Unlock()
	if err := src.Test(ctx); err == nil || !strings.Contains(err.Error(), "Table db.t does not exist") {
		t.Errorf("no table: %v", err)
	}
	mu.Lock()
	one = "<html>"
	mu.Unlock()
	if err := src.Test(ctx); err == nil || !strings.Contains(err.Error(), "not ClickHouse") {
		t.Errorf("not ClickHouse: %v", err)
	}
}

// The answers' header lines.
const pageHead = `["id", "c", "gone", "__fit_0", "__fit_1"]
["UInt64", "DateTime", "Bool", "String", "String"]
`

func pageRow(id int, gone bool) string {
	c := "2026-01-01T00:00:0" + strconv.Itoa(id) + "Z"
	return `[` + strconv.Itoa(id) + `, "` + c + `", ` + strconv.FormatBool(gone) + `, "2026-01-01 00:00:0` + strconv.Itoa(id) + `", "` + strconv.Itoa(id) + "\"]\n"
}

func TestSourceRun(t *testing.T) {
	var mu sync.Mutex
	pages := []string{pageHead + pageRow(1, false) + pageRow(2, true), pageHead + pageRow(3, false)}
	f := newFakeCH(t, func(q url.Values, sql, _ string) (int, int, string) {
		mu.Lock()
		defer mu.Unlock()
		if len(pages) == 0 {
			return 200, 0, pageHead
		}
		p := pages[0]
		pages = pages[1:]
		return 200, 0, p
	})
	s := fakeSource(t, f, map[string]any{"table": "db.t", "key": []string{"id"}, "cursor": "c", "deleted_column": "gone",
		"page_rows": 2, "poll_interval": "1ms", "settings": map[string]string{"max_threads": "2"}})
	defer s.Cleanup()
	var got []*fitsync.Record
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	err := s.Run(ctx, &fitsync.Run{Once: true, Emit: func(r *fitsync.Record) error { got = append(got, r); return nil }})
	if err != nil || len(got) != 3 || got[1].Op != fitsync.OpDelete || got[0].Namespace != "db.t" ||
		!got[2].CommitTime.Equal(time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC)) ||
		string(got[2].Position) != `{"after":[{"t":"DateTime","v":"2026-01-01 00:00:03"},{"t":"UInt64","v":"3"}]}` {
		t.Fatalf("%v %d %+v", err, len(got), got)
	}
	reqs := f.requests()
	if q := reqs[1]; q.q.Get("param_a0") != "2026-01-01 00:00:02" || q.q.Get("param_a1") != "2" || q.q.Get("max_threads") != "2" ||
		!strings.Contains(q.sql, "WHERE (`c`, `id`) > ({a0:DateTime}, {a1:UInt64}) ORDER BY `c`, `id` LIMIT 2") {
		t.Errorf("%+v", q)
	}
	// From a position, polling until the context ends.
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() {
		for len(f.requests()) < 6 {
			time.Sleep(time.Millisecond)
		}
		cancel2()
	}()
	err = s.Run(ctx2, &fitsync.Run{From: got[2].Position, Emit: func(*fitsync.Record) error { return nil }})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	// Emit's error ends it: on a server of its own, since a request the
	// cancelled run sent may still reach the first.
	f2 := newFakeCH(t, func(url.Values, string, string) (int, int, string) { return 200, 0, pageHead + pageRow(1, false) })
	s2 := fakeSource(t, f2, map[string]any{"table": "db.t", "key": []string{"id"}, "cursor": "c"})
	stop := errors.New("stop")
	if err := s2.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return stop }}); !errors.Is(err, stop) {
		t.Errorf("%v", err)
	}
	// Bad positions and answers.
	for _, from := range []string{`nope`, `{"after":[{},{},{}]}`} {
		if err := s.Run(context.Background(), &fitsync.Run{From: fitsync.Position(from)}); err == nil {
			t.Errorf("%s: no error", from)
		}
	}
	f.set(func(url.Values, string, string) (int, int, string) { return 500, 60, "no table" })
	if err := s.Run(context.Background(), &fitsync.Run{Once: true}); err == nil || !strings.Contains(err.Error(), "no table") {
		t.Errorf("%v", err)
	}
}

func TestSourceWithoutCursor(t *testing.T) {
	f := newFakeCH(t, func(q url.Values, sql, _ string) (int, int, string) {
		return 200, 0, "[\"id\", \"v\", \"__fit_0\"]\n[\"Int64\", \"String\", \"String\"]\n[1, \"a\", \"1\"]\n"
	})
	s := fakeSource(t, f, map[string]any{"query": "SELECT * FROM t;", "key": []string{"id"}})
	var n int
	if err := s.Run(context.Background(), &fitsync.Run{Emit: func(r *fitsync.Record) error {
		n++
		if r.Namespace != "clickhouse" || !r.CommitTime.IsZero() {
			t.Errorf("%+v", r)
		}
		return nil
	}}); err != nil || n != 1 {
		t.Fatalf("%v %d", err, n)
	}
	if sql := f.requests()[0].sql; !strings.Contains(sql, "FROM (SELECT * FROM t) AS `__fit` ORDER BY `id` LIMIT 10000") {
		t.Error(sql)
	}
}

func TestSourceSnapshotNever(t *testing.T) {
	answer := `[3, "9", "UInt64"]`
	f := newFakeCH(t, func(q url.Values, sql, _ string) (int, int, string) {
		if strings.Contains(sql, "max(") {
			return 200, 0, answer
		}
		return 200, 0, "[\"id\", \"c\", \"__fit_0\", \"__fit_1\"]\n[\"UInt64\", \"Nullable(UInt64)\", \"String\", \"String\"]\n"
	})
	s := fakeSource(t, f, map[string]any{"table": "t", "key": []string{"id"}, "cursor": "c", "snapshot": "never"})
	if err := s.Run(context.Background(), &fitsync.Run{Once: true}); err != nil {
		t.Fatal(err)
	}
	if q := f.requests()[1]; q.q.Get("param_a0") != "9" || !strings.Contains(q.sql, "WHERE `c` > {a0:UInt64}") {
		t.Errorf("%+v", q)
	}
	answer = `[0, "0", "UInt64"]`
	if after, err := s.greatestCursor(context.Background()); err != nil || after != nil {
		t.Errorf("%v %v", after, err)
	}
	for _, a := range []string{`[1]`, `["x", "1", "UInt64"]`} {
		answer = a
		if err := s.Run(context.Background(), &fitsync.Run{Once: true}); err == nil {
			t.Errorf("%s: no error", a)
		}
	}
	f.set(func(url.Values, string, string) (int, int, string) { return 500, 1, "down" })
	if err := s.Run(context.Background(), &fitsync.Run{Once: true}); err == nil {
		t.Error("no error")
	}
}

func TestSourceChunks(t *testing.T) {
	count := `[5]`
	skips := []string{`["2", "UInt64", "a", "String"]`, `["4", "UInt64", "b", "String"]`, ``}
	var mu sync.Mutex
	f := newFakeCH(t, func(q url.Values, sql, _ string) (int, int, string) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(sql, "count()"):
			return 200, 0, count
		case strings.Contains(sql, "OFFSET"):
			if len(skips) == 0 {
				return 200, 0, ""
			}
			a := skips[0]
			skips = skips[1:]
			return 200, 0, a
		}
		return 200, 0, "[\"id\", \"k\", \"__fit_0\", \"__fit_1\"]\n[\"UInt64\", \"String\", \"String\", \"String\"]\n[1, \"a\", \"1\", \"a\"]\n"
	})
	s := fakeSource(t, f, map[string]any{"table": "t", "key": []string{"id", "k"}, "chunk_rows": 2, "page_rows": 1})
	plan, err := s.Chunks(context.Background())
	if err != nil || len(plan) != 3 || plan[0].Lo != nil || plan[2].Hi != nil || plan[2].Rows != 1 ||
		string(plan[1].Lo) != `[{"t":"UInt64","v":"2"},{"t":"String","v":"a"}]` {
		t.Fatalf("%v %+v", err, plan)
	}
	var n int
	from := fitsync.Position(`{"after":[{"t":"UInt64","v":"2"},{"t":"String","v":"a"}]}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for len(f.requests()) < 20 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	err = s.ReadChunk(ctx, plan[1], from, func(*fitsync.Record) error { n++; return nil })
	if err == nil || n == 0 {
		t.Errorf("%v %d", err, n)
	}
	reqs := f.requests()
	last := reqs[len(reqs)-1]
	if !strings.Contains(last.sql, "(`id`, `k`) > ({a0:UInt64}, {a1:String}) AND (`id`, `k`) >= ({l0:UInt64}, {l1:String}) AND (`id`, `k`) < ({h0:UInt64}, {h1:String})") {
		t.Errorf("%s", last.sql)
	}
	f.set(func(url.Values, string, string) (int, int, string) {
		return 200, 0, "[\"id\", \"k\", \"__fit_0\", \"__fit_1\"]\n[\"UInt64\", \"String\", \"String\", \"String\"]\n"
	})
	if err := s.ReadChunk(context.Background(), plan[0], nil, func(*fitsync.Record) error { return nil }); err != nil {
		t.Error(err)
	}
	stop := errors.New("stop")
	f.set(func(url.Values, string, string) (int, int, string) {
		return 200, 0, "[\"id\", \"k\", \"__fit_0\", \"__fit_1\"]\n[\"UInt64\", \"String\", \"String\", \"String\"]\n[1, \"a\", \"1\", \"a\"]\n"
	})
	if err := s.ReadChunk(context.Background(), plan[0], nil, func(*fitsync.Record) error { return stop }); !errors.Is(err, stop) {
		t.Error(err)
	}
	if err := s.ReadChunk(context.Background(), fitsync.Chunk{Lo: json.RawMessage(`x`)}, nil, nil); err == nil {
		t.Error("a bad chunk")
	}
	if err := s.ReadChunk(context.Background(), plan[0], fitsync.Position(`x`), nil); err == nil {
		t.Error("a bad position")
	}
	f.set(func(url.Values, string, string) (int, int, string) { return 500, 1, "down" })
	if err := s.ReadChunk(context.Background(), plan[0], nil, nil); err == nil {
		t.Error("no error")
	}
	// Plans that cannot be.
	f.set(nil)
	mu.Lock()
	skips = nil
	mu.Unlock()
	for _, c := range []struct {
		answer func(url.Values, string, string) (int, int, string)
		ok     bool
	}{
		{func(url.Values, string, string) (int, int, string) { return 200, 0, `[0]` }, true},
		{func(url.Values, string, string) (int, int, string) { return 200, 0, `[1000000]` }, false},
		{func(url.Values, string, string) (int, int, string) { return 200, 0, `x` }, false},
		{func(url.Values, string, string) (int, int, string) { return 500, 1, `down` }, false},
		{func(q url.Values, sql, _ string) (int, int, string) {
			if strings.Contains(sql, "OFFSET") {
				return 500, 1, "down"
			}
			return 200, 0, `[5]`
		}, false},
		{func(q url.Values, sql, _ string) (int, int, string) {
			if strings.Contains(sql, "OFFSET") {
				return 200, 0, `["1"]`
			}
			return 200, 0, `[5]`
		}, false},
	} {
		f.set(c.answer)
		s1 := fakeSource(t, f, map[string]any{"table": "t", "key": []string{"id"}, "chunk_rows": 1})
		plan, err := s1.Chunks(context.Background())
		if (err == nil) != c.ok || c.ok && plan != nil {
			t.Errorf("%v %v", plan, err)
		}
	}
	s0 := fakeSource(t, f, map[string]any{"table": "t", "key": []string{"id"}})
	if plan, err := s0.Chunks(context.Background()); plan != nil || err != nil {
		t.Error(plan, err)
	}
}

func TestParseRowsErrors(t *testing.T) {
	for _, body := range []string{
		`["id"]`,
		"x\n[\"UInt64\"]\n",
		"[\"id\", \"__fit_0\"]\n[\"UInt64\", \"String\"]\nx\n",
		"[\"id\", \"__fit_0\"]\n[\"UInt64\", \"String\"]\n[\"x\", \"1\"]\n",
		"[\"id\", \"__fit_0\"]\n[\"UInt64\", \"String\"]\n[1, null]\n",
	} {
		if _, err := parseRows([]byte(body), []string{"id"}); err == nil {
			t.Errorf("%q: no error", body)
		}
	}
	for _, v := range []fitsync.Value{fitsync.Int(1), fitsync.Uint(2), fitsync.String("x"), fitsync.Bool(false)} {
		want := v.Kind() != fitsync.KindString && !v.Equal(fitsync.Bool(false))
		if truthy(v) != want {
			t.Errorf("%v", v)
		}
	}
}

func TestToValueErrors(t *testing.T) {
	for _, c := range []struct{ typ, raw string }{
		{"UInt8", `-1`}, {"Float64", `"x"`}, {"Bool", `1`}, {"Date", `1`}, {"Date", `"x"`},
		{"DateTime", `1`}, {"DateTime", `"x"`}, {"String", `1`}, {"Array(Int8)", `{}`}, {"Array(Int8)", `["x"]`},
		{"Map(String, String)", `{`},
	} {
		if _, err := ToValue(c.typ, json.RawMessage(c.raw)); err == nil {
			t.Errorf("%s %s: no error", c.typ, c.raw)
		}
	}
	if v, err := ToValue("Float32", json.RawMessage(`"inf"`)); err != nil || !math.IsInf(v.AsFloat(), 1) {
		t.Error(v, err)
	}
	if v, err := ToValue("Int64", json.RawMessage(`"-9"`)); err != nil || v.AsInt() != -9 {
		t.Error(v, err)
	}
}
