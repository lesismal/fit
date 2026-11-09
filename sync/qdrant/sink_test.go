package syncqdrant

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
		"source": map[string]any{"module": "generate"}, "sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "qdrant", "params": params}}}}}}})
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

// fake is Qdrant keeping points by collection and ID; a point whose JSON
// holds refuse is refused (400).
type fake struct {
	mu     sync.Mutex
	srv    *httptest.Server
	points map[string]map[string]string
	reqs   []string
	refuse string
	status int
}

func newFake(t *testing.T) *fake {
	f := &fake{points: map[string]map[string]string{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.String()+" "+r.Header.Get("api-key"))
	if f.status != 0 {
		http.Error(w, `{"status":{"error":"no"}}`, f.status)
		return
	}
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(`{"result":{}}`))
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	coll := parts[2]
	var body struct{ Points []json.RawMessage }
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, &body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if f.points[coll] == nil {
		f.points[coll] = map[string]string{}
	}
	if strings.HasSuffix(r.URL.Path, "/delete") {
		for _, id := range body.Points {
			delete(f.points[coll], string(id))
		}
	} else {
		for _, p := range body.Points {
			if f.refuse != "" && strings.Contains(string(p), f.refuse) {
				http.Error(w, `{"status":{"error":"Wrong input: Vector dimension error"}}`, 400)
				return
			}
		}
		for _, p := range body.Points {
			var pt struct{ ID json.RawMessage }
			_ = json.Unmarshal(p, &pt)
			f.points[coll][string(pt.ID)] = string(p)
		}
	}
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func fld(name string, v fitsync.Value) fitsync.Field { return fitsync.Field{Name: name, Value: v} }

func vec(fs ...float64) fitsync.Value {
	var es []fitsync.Value
	for _, f := range fs {
		es = append(es, fitsync.Float(f))
	}
	return fitsync.Array(es...)
}

func rec(op fitsync.Op, id int64, fields ...fitsync.Field) *fitsync.Record {
	r := &fitsync.Record{Namespace: "docs", Op: op, Key: fitsync.Map(fld("id", fitsync.Int(id)))}
	row := fitsync.Map(append([]fitsync.Field{fld("id", fitsync.Int(id))}, fields...)...)
	if op == fitsync.OpDelete {
		r.Before = row
	} else {
		r.After = row
	}
	return r
}

// TestWrite upserts each ID's last record and deletes, by collection, with
// the API key, waiting for them.
func TestWrite(t *testing.T) {
	f := newFake(t)
	t.Setenv("QDRANT_KEY", "k")
	s, err := load(t, map[string]any{"url": f.srv.URL, "api_key_env": "QDRANT_KEY", "collection": "c_{namespace}", "max_request_bytes": 200})
	if err != nil {
		t.Fatal(err)
	}
	f.points["c_docs"] = map[string]string{"9": "old"}
	batch := []*fitsync.Record{
		rec(fitsync.OpInsert, 1, fld("t", fitsync.String("a")), fld("embedding", vec(0.5, 1))),
		rec(fitsync.OpInsert, 2, fld("embedding", fitsync.JSON([]byte(`[1, 2]`)))),
		rec(fitsync.OpUpdate, 1, fld("t", fitsync.String("b")), fld("embedding", fitsync.Array(fitsync.Int(1), fitsync.Uint(2), fitsync.Decimal("0.5")))),
		rec(fitsync.OpDelete, 9),
		{Op: fitsync.OpDDL},
		rec(fitsync.OpInsert, 3, fld("embedding", vec(1, 1))),
		rec(fitsync.OpDelete, 3),
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	got := f.points["c_docs"]
	if len(got) != 2 || got["1"] != `{"id":1,"vector":[1,2,0.5],"payload":{"id":1,"t":"b"}}` || got["2"] != `{"id":2,"vector":[1,2],"payload":{"id":2}}` {
		t.Fatalf("%v", got)
	}
	if !strings.HasPrefix(f.reqs[0], "POST /collections/c_docs/points/delete?wait=true k") || !strings.HasPrefix(f.reqs[1], "PUT /collections/c_docs/points?wait=true k") {
		t.Fatal(f.reqs)
	}
	// A named vector, a payload of some fields.
	named, err := load(t, map[string]any{"url": f.srv.URL, "collection": "n", "vector_name": "text", "vector_field": "v", "payload": []string{"t"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := named.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 5, fld("t", fitsync.String("x")), fld("u", fitsync.Int(1)), fld("v", vec(1)))}); err != nil {
		t.Fatal(err)
	}
	if p := f.points["n"]["5"]; p != `{"id":5,"vector":{"text":[1]},"payload":{"t":"x"}}` {
		t.Fatal(p)
	}
	if err := errors.Join(s.Test(context.Background()), named.Test(context.Background())); err != nil {
		t.Fatal(err)
	}
	if last := f.reqs[len(f.reqs)-1]; !strings.HasPrefix(last, "GET /collections/n ") || !strings.HasPrefix(f.reqs[len(f.reqs)-2], "GET /collections ") {
		t.Fatal(f.reqs)
	}
	f.status = 404
	if err := s.Test(context.Background()); err == nil {
		t.Fatal("404")
	}
	if err := s.Write(context.Background(), batch); err == nil || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	s.ParallelWrites()
}

// TestRefused finds the point Qdrant refuses.
func TestRefused(t *testing.T) {
	f := newFake(t)
	s, err := load(t, map[string]any{"url": f.srv.URL, "collection": "c"})
	if err != nil {
		t.Fatal(err)
	}
	f.refuse = `"vector":[7]`
	batch := []*fitsync.Record{rec(fitsync.OpInsert, 1, fld("embedding", vec(1))), rec(fitsync.OpInsert, 2, fld("embedding", vec(7))), rec(fitsync.OpInsert, 3, fld("embedding", vec(3)))}
	err = s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || len(f.points["c"]) != 1 {
		t.Fatal(err, f.points)
	}
}

// TestBadRecords fails the records that cannot be points.
func TestBadRecords(t *testing.T) {
	f := newFake(t)
	s, err := load(t, map[string]any{"url": f.srv.URL, "collection": "{coll}", "max_request_bytes": 200})
	if err != nil {
		t.Fatal(err)
	}
	good := rec(fitsync.OpInsert, 1, fld("coll", fitsync.String("c")), fld("embedding", vec(1)))
	c := fld("coll", fitsync.String("c"))
	for _, bad := range []*fitsync.Record{
		{Op: fitsync.Op(99)},
		{Op: fitsync.OpInsert, After: fitsync.Map(c)},
		rec(fitsync.OpInsert, 2, fld("embedding", vec(1))),
		rec(fitsync.OpInsert, 2, fld("coll", fitsync.String("a/b")), fld("embedding", vec(1))),
		{Op: fitsync.OpInsert, Key: fitsync.Int(2), After: fitsync.Int(1)},
		rec(fitsync.OpInsert, 2, c),
		rec(fitsync.OpInsert, 2, c, fld("embedding", fitsync.String("x"))),
		rec(fitsync.OpInsert, 2, c, fld("embedding", fitsync.Array())),
		rec(fitsync.OpInsert, 2, c, fld("embedding", fitsync.Array(fitsync.String("x")))),
		rec(fitsync.OpInsert, 2, c, fld("embedding", vec(math.NaN()))),
		rec(fitsync.OpInsert, 2, c, fld("embedding", fitsync.JSON([]byte(`{}`)))),
		rec(fitsync.OpInsert, 2, c, fld("embedding", vec(1)), fld("f", fitsync.Float(math.Inf(1)))),
		rec(fitsync.OpInsert, 2, c, fld("embedding", vec(1)), fld("big", fitsync.String(strings.Repeat("x", 200)))),
	} {
		n := len(f.points["c"])
		err := s.Write(context.Background(), []*fitsync.Record{good, bad})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
			t.Fatalf("%+v: %v", bad, err)
		}
		if len(f.points["c"]) != max(n, 1) {
			t.Fatal(f.points)
		}
	}
}

func TestPointID(t *testing.T) {
	for key, want := range map[*fitsync.Value]string{
		ptr(fitsync.Int(5)):  "5",
		ptr(fitsync.Uint(6)): "6",
		ptr(fitsync.Map(fld("id", fitsync.String("5C56C793-69F3-4FBF-87E6-C4BF54C28C26")))): `"5c56c793-69f3-4fbf-87e6-c4bf54c28c26"`,
		ptr(fitsync.Int(-1)):       `"1bad6b8c-f971-81fc-aab8-543e81f77571"`,
		ptr(fitsync.String("abc")): `"ba7816bf-8f01-8fea-8141-40de5dae2223"`,
		ptr(fitsync.String("5c56c793-69f3-4fbf-87e6-c4bf54c28c2g")): `"13ae51d6-3c24-8ad4-b93b-88b9355ee86e"`,
		ptr(fitsync.String("5c56c793x69f3-4fbf-87e6-c4bf54c28c26")): `"836db880-db59-85b5-b098-fcfe3db41f4c"`,
	} {
		got, err := PointID(*key)
		if err != nil || got != want {
			t.Errorf("%v: %s %v", *key, got, err)
		}
	}
	if _, err := PointID(fitsync.Null()); err == nil {
		t.Error("a null key")
	}
}

func ptr(v fitsync.Value) *fitsync.Value { return &v }

func TestProvisionErrors(t *testing.T) {
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"url": "http://x", "collection": "{"}, "collection"},
		{map[string]any{"url": "http://x", "collection": "c", "api_key_env": "QDRANT_NONE"}, "api_key_env"},
		{map[string]any{"url": "ftp://x", "collection": "c"}, "url"},
	} {
		if _, err := load(t, c.params); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.params, err)
		}
	}
	dead, err := load(t, map[string]any{"url": "http://127.0.0.1:1", "collection": "c", "timeout": "1s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dead.Test(context.Background()); err == nil {
		t.Fatal("no server")
	}
}
