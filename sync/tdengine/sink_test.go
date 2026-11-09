package synctdengine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
)

func init() { fibrest.BackoffFirst, fibrest.BackoffMost = time.Millisecond, 2*time.Millisecond }

func load(t *testing.T, params map[string]any) (*Sink, error) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": "x", "pipelines": []any{map[string]any{"name": "p",
		"source": map[string]any{"module": "generate"}, "sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "tdengine", "params": params}}}}}}})
	cfg, err := config.Load(b)
	if err != nil {
		return nil, err
	}
	v, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(v) })
	return v.(*Sink), nil
}

// fake is taosAdapter, keeping the bodies written; a body holding refuse
// is refused as TDengine refuses a bad row, one holding busy fails with a
// 500 of another kind until busy is cleared.
type fake struct {
	mu     sync.Mutex
	srv    *httptest.Server
	bodies []string
	paths  []string
	refuse string
	busy   int
	answer string // /rest/sql's answer, when set
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	f.paths = append(f.paths, r.URL.String())
	if u, p, _ := r.BasicAuth(); u != "root" || p != "pw" {
		http.Error(w, `{"code":"internal error","message":"[0x357] Authentication failure"}`, 500)
		return
	}
	if f.busy > 0 {
		f.busy--
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	sql := strings.HasPrefix(r.URL.Path, "/rest/sql")
	if f.refuse != "" && strings.Contains(string(b), f.refuse) {
		if sql {
			_, _ = w.Write([]byte(`{"code":534,"desc":"syntax error"}`))
		} else {
			http.Error(w, `{"code":"internal error","message":"[0x3002] SML type mismatch"}`, 500)
		}
		return
	}
	f.bodies = append(f.bodies, string(b))
	if sql {
		if f.answer != "" {
			_, _ = w.Write([]byte(f.answer))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":[[1]]}`))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fake) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func fld(name string, v fitsync.Value) fitsync.Field { return fitsync.Field{Name: name, Value: v} }

func rec(op fitsync.Op, fields ...fitsync.Field) *fitsync.Record {
	r := &fitsync.Record{Namespace: "m", Op: op, CommitTime: time.Unix(1700000000, 0)}
	if op == fitsync.OpDelete {
		r.Before = fitsync.Map(fields...)
	} else {
		r.After = fitsync.Map(fields...)
	}
	return r
}

func params(f *fake, extra map[string]any) map[string]any {
	p := map[string]any{"url": f.srv.URL, "username": "root", "password_env": "TD_PW", "database": "db", "tags": []string{"dev"}}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// TestSchemaless writes lines of TDengine's flavor to /influxdb/v1/write,
// and finds the row TDengine refuses.
func TestSchemaless(t *testing.T) {
	t.Setenv("TD_PW", "pw")
	f := newFake(t)
	s, err := load(t, params(f, nil))
	if err != nil {
		t.Fatal(err)
	}
	batch := []*fitsync.Record{
		rec(fitsync.OpInsert, fld("dev", fitsync.String("d1")), fld("v", fitsync.Int(1)), fld("u", fitsync.Uint(2)), fld("f", fitsync.Float(1.5))),
		rec(fitsync.OpDelete, fld("dev", fitsync.String("d1"))),
		{Op: fitsync.OpDDL},
		rec(fitsync.OpUpdate, fld("dev", fitsync.String("d2")), fld("v", fitsync.String("x"))),
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if got := f.written(); len(got) != 1 || got[0] != "m,dev=d1 v=1i64,u=2u64,f=1.5f64 1700000000000\nm,dev=d2 v=\"x\" 1700000000000\n" {
		t.Fatalf("%q", got)
	}
	if f.paths[0] != "/influxdb/v1/write?db=db&precision=ms" {
		t.Fatal(f.paths)
	}
	f.refuse = "v=2i64"
	var recs []*fitsync.Record
	for i := range 4 {
		recs = append(recs, rec(fitsync.OpInsert, fld("v", fitsync.Int(int64(i)))))
	}
	err = s.Write(context.Background(), recs)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 2 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	// A 503 is tried again.
	f.refuse, f.busy = "", 2
	if err := s.Write(context.Background(), recs[:1]); err != nil {
		t.Fatal(err)
	}
	if err := s.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.paths[len(f.paths)-1], "/rest/sql/db") {
		t.Fatal(f.paths)
	}
}

// TestSQL writes INSERTs into subtables, USING the super table, splits
// them by max_request_bytes and finds the row refused.
func TestSQL(t *testing.T) {
	t.Setenv("TD_PW", "pw")
	f := newFake(t)
	s, err := load(t, params(f, map[string]any{"mode": "sql", "subtable": "d_{dev}", "precision": "ns", "measurement": "{namespace}", "time_field": "at", "max_request_bytes": 300}))
	if err != nil {
		t.Fatal(err)
	}
	at := fitsync.Time(time.Unix(1, 5))
	batch := []*fitsync.Record{
		rec(fitsync.OpInsert, fld("dev", fitsync.String("d1")), fld("at", at), fld("v", fitsync.Int(1)), fld("s", fitsync.String(`it's \`)), fld("b", fitsync.Bytes([]byte{1})),
			fld("ok", fitsync.Bool(true)), fld("u", fitsync.Uint(3)), fld("f", fitsync.Float(0.5)), fld("d", fitsync.Decimal("1.25"))),
		rec(fitsync.OpInsert, fld("dev", fitsync.String("d2")), fld("at", at), fld("v", fitsync.Int(2))),
		rec(fitsync.OpInsert, fld("dev", fitsync.String("d3")), fld("at", at), fld("v", fitsync.Int(3))),
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	got := f.written()
	want := "INSERT INTO `db`.`d_d1` USING `db`.`m` (`dev`) TAGS ('d1') (`ts`, `v`, `s`, `b`, `ok`, `u`, `f`, `d`) VALUES (1000000005, 1, 'it\\'s \\\\', 'AQ==', true, 3, 0.5, 1.25)"
	if len(got) != 2 || !strings.HasPrefix(got[0], want) || !strings.HasPrefix(got[1], "INSERT INTO `db`.`d_d3`") {
		t.Fatalf("%q", got)
	}
	f.refuse = "VALUES (1000000005, 2)"
	err = s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	// A subtable named from its tags.
	s2, err := load(t, params(f, map[string]any{"mode": "sql", "tags": []string{"dev", "zone"}, "time_column": "t"}))
	if err != nil {
		t.Fatal(err)
	}
	f.refuse = ""
	if err := s2.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, fld("dev", fitsync.String("d1")), fld("v", fitsync.Int(1)))}); err != nil {
		t.Fatal(err)
	}
	got = f.written()
	if last := got[len(got)-1]; !strings.HasPrefix(last, "INSERT INTO `db`.`t_") || !strings.Contains(last, "(`dev`, `zone`) TAGS ('d1', NULL) (`t`, `v`)") {
		t.Fatal(last)
	}
}

// TestAnswers reads TDengine's errors: refused, other errors, and answers
// that are not TDengine's.
func TestAnswers(t *testing.T) {
	t.Setenv("TD_PW", "pw")
	f := newFake(t)
	s, err := load(t, params(f, map[string]any{"mode": "sql"}))
	if err != nil {
		t.Fatal(err)
	}
	one := []*fitsync.Record{rec(fitsync.OpInsert, fld("v", fitsync.Int(1)))}
	f.answer = `{"code":9731,"desc":"Table does not exist"}`
	if err := s.Write(context.Background(), one); err == nil || fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "0x2603") {
		t.Fatal(err)
	}
	if err := s.Test(context.Background()); err == nil {
		t.Fatal("an error")
	}
	f.answer = `not json`
	if err := s.Write(context.Background(), one); err == nil || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	t.Setenv("TD_PW", "bad")
	bad, err := load(t, params(f, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Write(context.Background(), one); err == nil || fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "0x357") {
		t.Fatal(err)
	}
	for code, want := range map[int]bool{0x216: true, 0x2603: false, 0x2662: false, 0x2650: true, 0x3002: true, 0x357: false} {
		if refused(code) != want {
			t.Errorf("0x%x", code)
		}
	}
	for _, c := range []struct {
		resp fibrest.Response
		code int
	}{
		{fibrest.Response{Status: 204}, 0},
		{fibrest.Response{Status: 500}, -1},
		{fibrest.Response{Status: 500, Body: []byte(`{"message":"plain"}`)}, -1},
		{fibrest.Response{Status: 500, Body: []byte(`{"code":"internal error","message":"[0x3002] SML"}`)}, 0x3002},
	} {
		if code, _ := errorOf(&c.resp); code != c.code {
			t.Errorf("%s: %d", c.resp.Body, code)
		}
	}
	dead, _ := load(t, map[string]any{"url": "http://127.0.0.1:1", "username": "root", "password_env": "TD_PW", "database": "db", "timeout": "1s"})
	if err := dead.Test(context.Background()); err == nil {
		t.Fatal("no server")
	}
	if err := dead.Write(context.Background(), one); err == nil {
		t.Fatal("no server")
	}
	dead.Cleanup()
}

// TestBadRecords fails the records that cannot be rows, those before them
// written.
func TestBadRecords(t *testing.T) {
	t.Setenv("TD_PW", "pw")
	f := newFake(t)
	sql, err := load(t, params(f, map[string]any{"mode": "sql", "deletes": "fail", "subtable": "{sub}", "max_request_bytes": 200}))
	if err != nil {
		t.Fatal(err)
	}
	good := rec(fitsync.OpInsert, fld("sub", fitsync.String("s")), fld("v", fitsync.Int(1)))
	for _, bad := range []*fitsync.Record{
		rec(fitsync.OpDelete, fld("sub", fitsync.String("s"))),
		{Op: fitsync.Op(99)},
		rec(fitsync.OpInsert, fld("v", fitsync.Int(1))),
		rec(fitsync.OpInsert, fld("sub", fitsync.String("a`b")), fld("v", fitsync.Int(1))),
		rec(fitsync.OpInsert, fld("sub", fitsync.String("s")), fld("v", fitsync.Float(math.NaN()))),
		rec(fitsync.OpInsert, fld("sub", fitsync.String("s")), fld("v", fitsync.Decimal("x"))),
		rec(fitsync.OpInsert, fld("sub", fitsync.String("s")), fld("v", fitsync.String("a\x00"))),
		rec(fitsync.OpInsert, fld("sub", fitsync.String("s")), fld("v", fitsync.String(strings.Repeat("x", 300)))),
		rec(fitsync.OpInsert, fld("sub", fitsync.String("s")), fld("dev", fitsync.String("a\x00")), fld("v", fitsync.Int(1))),
		rec(fitsync.OpInsert, fld("sub", fitsync.String("s")), fld("v`", fitsync.Int(1))),
	} {
		n := len(f.written())
		err := sql.Write(context.Background(), []*fitsync.Record{good, bad})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
			t.Fatalf("%+v: %v", bad, err)
		}
		if len(f.written()) != n+1 {
			t.Fatal(f.written())
		}
	}
	sml, err := load(t, params(f, map[string]any{"fields": []string{"v"}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := sml.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, fld("v", fitsync.Float(math.Inf(1))))}); !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
}

func TestProvisionErrors(t *testing.T) {
	t.Setenv("TD_PW", "pw")
	base := map[string]any{"url": "http://x", "username": "root", "password_env": "TD_PW", "database": "db"}
	for _, c := range []struct {
		extra map[string]any
		want  string
	}{
		{map[string]any{"database": "a`b"}, "database"},
		{map[string]any{"mode": "sql", "time_column": "a`"}, "time_column"},
		{map[string]any{"username": "a:b"}, "username"},
		{map[string]any{"password_env": "TD_NONE"}, "password_env"},
		{map[string]any{"tags": []string{"a", "a"}}, "tags"},
		{map[string]any{"subtable": "x"}, "subtable"},
		{map[string]any{"mode": "sql", "subtable": "{"}, "subtable"},
		{map[string]any{"url": "ftp://x"}, "url"},
	} {
		p := map[string]any{}
		for k, v := range base {
			p[k] = v
		}
		for k, v := range c.extra {
			p[k] = v
		}
		if _, err := load(t, p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.extra, err)
		}
	}
	s, err := load(t, base)
	if err != nil {
		t.Fatal(err)
	}
	s.ParallelWrites()
}
