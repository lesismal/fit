package syncinflux

import (
	"compress/gzip"
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

// load provisions the sink of params.
func load(t *testing.T, params map[string]any) (*Sink, error) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": "x", "pipelines": []any{map[string]any{"name": "p",
		"source": map[string]any{"module": "generate"}, "sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "influxdb", "params": params}}}}}}})
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

func mustLoad(t *testing.T, params map[string]any) *Sink {
	t.Helper()
	s, err := load(t, params)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// fake is an InfluxDB keeping the lines written, refusing those holding
// refuse.
type fake struct {
	mu      sync.Mutex
	srv     *httptest.Server
	lines   []string
	reqs    []*http.Request
	status  int // answers every request with it, when set
	refuse  string
	refusal int
	buckets string
}

func newFake(t *testing.T) *fake {
	f := &fake{refusal: 400, buckets: `{"buckets": [{"name": "b"}]}`}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch r.URL.Path {
	case "/api/v2/buckets":
		_, _ = w.Write([]byte(f.buckets))
		return
	case "/api/v3/configure/database", "/query":
		_, _ = w.Write([]byte(`[]`))
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		z, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body = z
	}
	b, _ := io.ReadAll(body)
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	for _, l := range lines {
		if f.refuse != "" && strings.Contains(l, f.refuse) {
			http.Error(w, `{"code": "invalid"}`, f.refusal)
			return
		}
	}
	f.lines = append(f.lines, lines...)
	w.WriteHeader(http.StatusNoContent)
}

func (f *fake) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

func rec(op fitsync.Op, fields ...fitsync.Field) *fitsync.Record {
	r := &fitsync.Record{Namespace: "db.m", Op: op, CommitTime: time.Unix(1700000000, 5).UTC()}
	if op == fitsync.OpDelete {
		r.Before = fitsync.Map(fields...)
	} else {
		r.After = fitsync.Map(fields...)
	}
	return r
}

func fld(name string, v fitsync.Value) fitsync.Field { return fitsync.Field{Name: name, Value: v} }

// TestWriteV2 writes points to /api/v2/write, gzipped, with the token, and
// leaves deletes and DDL out.
func TestWriteV2(t *testing.T) {
	f := newFake(t)
	t.Setenv("INFLUX_TOKEN", "tok")
	s := mustLoad(t, map[string]any{"url": f.srv.URL, "org": "o", "bucket": "b", "token_env": "INFLUX_TOKEN", "tags": []string{"host"}, "precision": "ms"})
	batch := []*fitsync.Record{
		rec(fitsync.OpInsert, fld("host", fitsync.String("a b")), fld("v", fitsync.Int(1)), fld("u", fitsync.Uint(2)), fld("f", fitsync.Float(1.5)),
			fld("ok", fitsync.Bool(true)), fld("s", fitsync.String(`q"\`)), fld("d", fitsync.Decimal("2.50")), fld("n", fitsync.Null()),
			fld("b", fitsync.Bytes([]byte{1})), fld("j", fitsync.JSON([]byte(`{"a":1}`)))),
		rec(fitsync.OpDelete, fld("host", fitsync.String("a")), fld("v", fitsync.Int(1))),
		{Op: fitsync.OpDDL},
		rec(fitsync.OpUpdate, fld("v", fitsync.Int(3))),
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	want := []string{`db.m,host=a\ b v=1i,u=2u,f=1.5,ok=true,s="q\"\\",d=2.5,b="AQ==",j="{\"a\":1}" 1700000000000`, `db.m v=3i 1700000000000`}
	if got := f.written(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q", got)
	}
	r := f.reqs[0]
	if r.URL.Path != "/api/v2/write" || r.URL.Query().Get("bucket") != "b" || r.URL.Query().Get("org") != "o" || r.URL.Query().Get("precision") != "ms" ||
		r.Header.Get("Authorization") != "Token tok" || r.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal(r.URL, r.Header)
	}
	if err := s.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.buckets = `{"buckets": []}`
	if err := s.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "bucket b") {
		t.Fatal(err)
	}
	f.status = 401
	if err := s.Test(context.Background()); err == nil {
		t.Fatal("401")
	}
	if err := s.Write(context.Background(), batch); err == nil || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	s.Cleanup()
}

// TestWriteV3AndV1 writes to /api/v3/write_lp and /write, as their params
// say.
func TestWriteV3AndV1(t *testing.T) {
	f := newFake(t)
	t.Setenv("INFLUX_TOKEN", "tok")
	t.Setenv("INFLUX_PW", "pw")
	no := false
	v3 := mustLoad(t, map[string]any{"url": f.srv.URL, "api": "v3", "database": "db", "token_env": "INFLUX_TOKEN", "precision": "us", "gzip": no})
	v1 := mustLoad(t, map[string]any{"url": f.srv.URL, "api": "v1", "database": "db", "retention_policy": "rp", "username": "u", "password_env": "INFLUX_PW", "precision": "s"})
	big := rec(fitsync.OpInsert, fld("u", fitsync.Uint(5)))
	if err := v3.Write(context.Background(), []*fitsync.Record{big}); err != nil {
		t.Fatal(err)
	}
	if err := v1.Write(context.Background(), []*fitsync.Record{big}); err != nil {
		t.Fatal(err)
	}
	if got := f.written(); got[0] != "db.m u=5u 1700000000000000" || got[1] != "db.m u=5i 1700000000" {
		t.Fatalf("%q", got)
	}
	q3, q1 := f.reqs[0].URL.Query(), f.reqs[1].URL.Query()
	if f.reqs[0].URL.Path != "/api/v3/write_lp" || q3.Get("db") != "db" || q3.Get("precision") != "microsecond" || q3.Get("accept_partial") != "false" ||
		f.reqs[0].Header.Get("Authorization") != "Bearer tok" || f.reqs[0].Header.Get("Content-Encoding") != "" {
		t.Fatal(f.reqs[0].URL, f.reqs[0].Header)
	}
	if f.reqs[1].URL.Path != "/write" || q1.Get("rp") != "rp" || q1.Get("precision") != "s" || !strings.HasPrefix(f.reqs[1].Header.Get("Authorization"), "Basic ") {
		t.Fatal(f.reqs[1].URL, f.reqs[1].Header)
	}
	if err := v1.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, fld("u", fitsync.Uint(math.MaxUint64)))}); !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	if err := errors.Join(v3.Test(context.Background()), v1.Test(context.Background())); err != nil {
		t.Fatal(err)
	}
	if f.reqs[len(f.reqs)-1].URL.Query().Get("q") != "SHOW DATABASES" {
		t.Fatal(f.reqs[len(f.reqs)-1].URL)
	}
	dead := mustLoad(t, map[string]any{"url": "http://127.0.0.1:1", "api": "v3", "database": "db", "timeout": "1s"})
	if err := dead.Test(context.Background()); err == nil {
		t.Fatal("no server")
	}
}

// TestRefused finds the point the server refuses, writes those before it,
// and splits a batch past max_request_bytes.
func TestRefused(t *testing.T) {
	for _, st := range []int{400, 422, 413} {
		f := newFake(t)
		f.refuse, f.refusal = "v=4i", st
		s := mustLoad(t, map[string]any{"url": f.srv.URL, "bucket": "b", "max_request_bytes": 40})
		var batch []*fitsync.Record
		for i := range 8 {
			batch = append(batch, rec(fitsync.OpInsert, fld("v", fitsync.Int(int64(i)))))
		}
		err := s.Write(context.Background(), batch)
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 4 || !fitsync.IsPermanent(err) {
			t.Fatal(st, err)
		}
		if got := f.written(); len(got) < 4 || got[len(got)-1] != "db.m v=3i 1700000000000000005" {
			t.Fatal(st, got)
		}
	}
	f := newFake(t)
	s := mustLoad(t, map[string]any{"url": f.srv.URL, "bucket": "b", "max_request_bytes": 10})
	err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, fld("v", fitsync.Int(1)))})
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 0 || !strings.Contains(err.Error(), "max_request_bytes") {
		t.Fatal(err)
	}
}

// TestBadRecords fails, for good, the records that cannot be points, those
// before them written.
func TestBadRecords(t *testing.T) {
	f := newFake(t)
	s := mustLoad(t, map[string]any{"url": f.srv.URL, "bucket": "b", "deletes": "fail", "time_field": "at", "measurement": "{kind}"})
	good := rec(fitsync.OpInsert, fld("kind", fitsync.String("k")), fld("at", fitsync.Int(10)), fld("v", fitsync.Int(1)))
	for _, bad := range []*fitsync.Record{
		rec(fitsync.OpDelete, fld("kind", fitsync.String("k"))),
		{Op: fitsync.Op(99)},
		rec(fitsync.OpInsert, fld("at", fitsync.Int(10)), fld("v", fitsync.Int(1))),
		rec(fitsync.OpInsert, fld("kind", fitsync.String("")), fld("at", fitsync.Int(10)), fld("v", fitsync.Int(1))),
		rec(fitsync.OpInsert, fld("kind", fitsync.String("k")), fld("v", fitsync.Int(1))),
		rec(fitsync.OpInsert, fld("kind", fitsync.String("k")), fld("at", fitsync.Bool(true)), fld("v", fitsync.Int(1))),
		rec(fitsync.OpInsert, fld("kind", fitsync.String("k\n")), fld("at", fitsync.Int(1)), fld("v", fitsync.Int(1))),
		rec(fitsync.OpInsert, fld("kind", fitsync.String("k")), fld("at", fitsync.Int(1)), fld("v", fitsync.Float(math.Inf(1)))),
		{Op: fitsync.OpInsert, After: fitsync.Int(1)},
	} {
		n := len(f.written())
		err := s.Write(context.Background(), []*fitsync.Record{good, bad})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
			t.Fatalf("%+v: %v", bad, err)
		}
		if got := f.written(); len(got) != n+1 || got[n] != `k kind="k",v=1i 10` {
			t.Fatalf("%q", got)
		}
	}
}

func TestProvisionErrors(t *testing.T) {
	t.Setenv("INFLUX_TOKEN", "tok")
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"url": "http://x"}, "bucket"},
		{map[string]any{"url": "http://x", "bucket": "b", "database": "d"}, "database"},
		{map[string]any{"url": "http://x", "api": "v3"}, "database"},
		{map[string]any{"url": "http://x", "api": "v3", "database": "d", "org": "o"}, "org"},
		{map[string]any{"url": "http://x", "api": "v1"}, "database"},
		{map[string]any{"url": "http://x", "api": "v1", "database": "d", "bucket": "b"}, "org"},
		{map[string]any{"url": "http://x", "bucket": "b", "token_env": "INFLUX_TOKEN", "username": "u"}, "token_env"},
		{map[string]any{"url": "http://x", "bucket": "b", "token_env": "INFLUX_NONE"}, "token_env"},
		{map[string]any{"url": "http://x", "bucket": "b", "username": "u", "password_env": "INFLUX_TOKEN"}, "username"},
		{map[string]any{"url": "http://x", "api": "v1", "database": "d", "username": "u", "password_env": "INFLUX_NONE"}, "password_env"},
		{map[string]any{"url": "http://x", "bucket": "b", "username": "u"}, "username"},
		{map[string]any{"url": "http://x", "api": "v1", "database": "d", "username": "a:b", "password_env": "INFLUX_TOKEN"}, "username"},
		{map[string]any{"url": "http://x", "bucket": "b", "tags": []string{"a", "a"}}, "tags"},
		{map[string]any{"url": "http://x", "bucket": "b", "fields": []string{"t"}, "time_field": "t"}, "fields"},
		{map[string]any{"url": "http://x", "bucket": "b", "measurement": "{"}, "measurement"},
		{map[string]any{"url": "ftp://x", "bucket": "b"}, "url"},
	} {
		_, err := load(t, c.params)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.params, err)
		}
	}
}

// TestPoints covers the line protocol's values, flavors, precisions and
// times.
func TestPoints(t *testing.T) {
	p := &Point{Measurement: "m,x", Tags: []Tag{{"t=1", "v 1"}}, Fields: []fitsync.Field{fld("a b", fitsync.Int(-1)), fld("u", fitsync.Uint(1)), fld("f", fitsync.Float(2)), fld("d", fitsync.Decimal("1.5"))}, Time: time.Unix(1, 2)}
	for fl, want := range map[Flavor]string{
		Influx:   `m\,x,t\=1=v\ 1 a\ b=-1i,u=1u,f=2,d=1.5 1000000002`,
		Influx1:  `m\,x,t\=1=v\ 1 a\ b=-1i,u=1i,f=2,d=1.5 1000000002`,
		TDengine: `m\,x,t\=1=v\ 1 a\ b=-1i64,u=1u64,f=2f64,d=1.5f64 1000000002`,
	} {
		b, err := AppendLine(nil, p, Nanoseconds, fl)
		if err != nil || string(b) != want+"\n" {
			t.Errorf("%d: %q %v", fl, b, err)
		}
	}
	at := time.Unix(1, 2_003_004)
	for prec, want := range map[Precision]int64{Nanoseconds: 1002003004, Microseconds: 1002003, Milliseconds: 1002, Seconds: 1} {
		if prec.Stamp(at) != want {
			t.Errorf("%s: %d", prec, prec.Stamp(at))
		}
	}
	for _, bad := range []*Point{
		{Measurement: ""},
		{Measurement: "m", Tags: []Tag{{"t", `x\`}}},
		{Measurement: "m", Fields: []fitsync.Field{fld("", fitsync.Int(1))}},
		{Measurement: "m", Fields: []fitsync.Field{fld("d", fitsync.Decimal("x"))}},
		{Measurement: "m", Fields: []fitsync.Field{fld("f", fitsync.Float(math.NaN()))}},
		{Measurement: "m", Fields: []fitsync.Field{fld("a", fitsync.Array(fitsync.Float(math.NaN())))}},
	} {
		if _, err := AppendLine(nil, bad, Nanoseconds, Influx); err == nil {
			t.Errorf("%+v", bad)
		}
	}
	b, err := AppendLine(nil, &Point{Measurement: "m", Fields: []fitsync.Field{fld("a", fitsync.Array(fitsync.Int(1))), fld("b", fitsync.Bytes([]byte("\xff")))}}, Seconds, Influx)
	if err != nil || string(b) != "m a=\"[1]\",b=\"/w==\" -62135596800\n" {
		t.Errorf("%q %v", b, err)
	}
	for v, want := range map[*fitsync.Value]time.Time{
		ptr(fitsync.Time(at)):                       at,
		ptr(fitsync.Uint(5)):                        time.Unix(0, 5),
		ptr(fitsync.String("2026-01-02T03:04:05Z")): time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		ptr(fitsync.Date(2026, time.January, 2)):    time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		ptr(fitsync.Int(-5)):                        time.Unix(0, -5),
	} {
		got, err := timeOf(*v)
		if err != nil || !got.Equal(want) {
			t.Errorf("%v: %v %v", *v, got, err)
		}
	}
	if _, err := timeOf(fitsync.Uint(math.MaxUint64)); err == nil {
		t.Error("past 2262")
	}
	m, errs := NewMapping("", []string{"b", "a"}, []string{""}, "")
	if len(errs) != 1 || strings.Join(m.TagNames(), ",") != "a,b" {
		t.Error(errs, m.TagNames())
	}
	// The fields named, those present.
	m, _ = NewMapping("x", nil, []string{"a", "b"}, "")
	pt, err := m.Point(&fitsync.Record{Op: fitsync.OpInsert, After: fitsync.Map(fld("a", fitsync.Int(1)), fld("c", fitsync.Int(2)))}, at)
	if err != nil || len(pt.Fields) != 1 || !pt.Time.Equal(at) {
		t.Error(pt, err)
	}
	if s, err := plain(fitsync.JSON([]byte("1"))); err != nil || s != "1" {
		t.Error(s, err)
	}
	if _, err := plain(fitsync.Float(math.NaN())); err == nil {
		t.Error(err)
	}
}

func ptr(v fitsync.Value) *fitsync.Value { return &v }

// TestNoField: a row of a time alone is no point.
func TestNoField(t *testing.T) {
	m, _ := NewMapping("m", nil, nil, "at")
	if _, err := m.Point(rec(fitsync.OpInsert, fld("at", fitsync.Int(1)), fld("n", fitsync.Null())), time.Now()); err == nil || !strings.Contains(err.Error(), "no field") {
		t.Fatal(err)
	}
}
