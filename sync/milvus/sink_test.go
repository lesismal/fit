package syncmilvus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
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
		"source": map[string]any{"module": "generate"}, "sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "milvus", "params": params}}}}}}})
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

// fake is Milvus's RESTful API keeping entities by collection and primary
// key; an entity holding refuse is refused (1804), and busy answers so
// many requests with code 8 (rate limited).
type fake struct {
	mu       sync.Mutex
	srv      *httptest.Server
	entities map[string]map[string]string
	bodies   []string
	auth     string
	refuse   string
	busy     int
	answer   string
	// pkType is the type describe answers for a collection's primary key;
	// describe answers no fields when it is empty, and code 100 when
	// noColl.
	pkType string
	noColl bool
}

func newFake(t *testing.T) *fake {
	f := &fake{entities: map[string]map[string]string{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

var inList = regexp.MustCompile(`^(\w+) in \[(.*)\]$`)

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	f.bodies = append(f.bodies, r.URL.Path+" "+string(b))
	f.auth = r.Header.Get("Authorization")
	if f.answer != "" {
		_, _ = w.Write([]byte(f.answer))
		return
	}
	if f.busy > 0 {
		f.busy--
		_, _ = w.Write([]byte(`{"code":8,"message":"rate limit exceeded"}`))
		return
	}
	var req struct {
		CollectionName string
		Filter         string
		Data           []map[string]json.RawMessage
	}
	if err := json.Unmarshal(b, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	c := f.entities[req.CollectionName]
	if c == nil {
		c = map[string]string{}
		f.entities[req.CollectionName] = c
	}
	switch r.URL.Path {
	case "/v2/vectordb/collections/describe":
		switch {
		case f.noColl:
			_, _ = w.Write([]byte(`{"code":100,"message":"can't find collection"}`))
		case f.pkType != "":
			_, _ = w.Write([]byte(`{"code":0,"data":{"fields":[{"name":"v","type":"FloatVector"},{"name":"id","type":"` + f.pkType + `","primaryKey":true}]}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
		return
	case "/v2/vectordb/entities/upsert":
		for _, d := range req.Data {
			e, _ := json.Marshal(d)
			if f.refuse != "" && strings.Contains(string(e), f.refuse) {
				_, _ = w.Write([]byte(`{"code":1804,"message":"fail to deal the insert data"}`))
				return
			}
		}
		for _, d := range req.Data {
			e, _ := json.Marshal(d)
			c[string(d["id"])] = string(e)
		}
	case "/v2/vectordb/entities/delete":
		m := inList.FindStringSubmatch(req.Filter)
		if m == nil {
			_, _ = w.Write([]byte(`{"code":1100,"message":"bad filter"}`))
			return
		}
		var ids []json.RawMessage
		_ = json.Unmarshal([]byte("["+m[2]+"]"), &ids)
		for _, id := range ids {
			delete(c, string(id))
		}
	}
	_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
}

func fld(name string, v fitsync.Value) fitsync.Field { return fitsync.Field{Name: name, Value: v} }

func rec(op fitsync.Op, key fitsync.Value, fields ...fitsync.Field) *fitsync.Record {
	r := &fitsync.Record{Namespace: "docs", Op: op, Key: key}
	row := fitsync.Map(fields...)
	if op == fitsync.OpDelete {
		r.Before = row
	} else {
		r.After = row
	}
	return r
}

func id(n int64) fitsync.Value { return fitsync.Map(fld("id", fitsync.Int(n))) }

// TestWrite upserts each key's last record and deletes by filter, by
// collection, with the token.
func TestWrite(t *testing.T) {
	f := newFake(t)
	t.Setenv("MILVUS_TOKEN", "root:Milvus")
	s, err := load(t, map[string]any{"url": f.srv.URL, "token_env": "MILVUS_TOKEN", "collection": "c_{namespace}", "database": "db", "max_request_bytes": 300})
	if err != nil {
		t.Fatal(err)
	}
	f.entities["c_docs"] = map[string]string{"9": "old"}
	v := fitsync.Array(fitsync.Float(0.5), fitsync.Int(1))
	batch := []*fitsync.Record{
		rec(fitsync.OpInsert, id(1), fld("id", fitsync.Int(100)), fld("t", fitsync.String("a")), fld("embedding", v)),
		rec(fitsync.OpInsert, id(2), fld("embedding", v)),
		rec(fitsync.OpUpdate, id(1), fld("t", fitsync.String("b")), fld("embedding", v)),
		rec(fitsync.OpDelete, id(9)),
		{Op: fitsync.OpDDL},
		rec(fitsync.OpInsert, id(3), fld("embedding", v)),
		rec(fitsync.OpDelete, id(3)),
	}
	f.busy = 1
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	got := f.entities["c_docs"]
	if len(got) != 2 || got["1"] != `{"embedding":[0.5,1],"id":1,"t":"b"}` || got["2"] != `{"embedding":[0.5,1],"id":2}` {
		t.Fatalf("%v", got)
	}
	// The busy answer to describe, describe again, the delete.
	if f.auth != "Bearer root:Milvus" || f.bodies[1] != `/v2/vectordb/collections/describe {"collectionName":"c_docs","dbName":"db"}` ||
		!strings.Contains(f.bodies[2], `"dbName":"db"`) || !strings.Contains(f.bodies[2], `"filter":"id in [9,3]"`) {
		t.Fatal(f.auth, f.bodies)
	}
	// String keys, fields kept, user and password.
	t.Setenv("MILVUS_PW", "pw")
	str, err := load(t, map[string]any{"url": f.srv.URL, "username": "u", "password_env": "MILVUS_PW", "collection": "s", "primary_key": "pk", "fields": []string{"v"}})
	if err != nil {
		t.Fatal(err)
	}
	two := fitsync.Map(fld("a", fitsync.String(`x"y`)), fld("b", fitsync.Int(2)))
	if err := str.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, two, fld("v", fitsync.Int(1)), fld("w", fitsync.Int(2))), rec(fitsync.OpDelete, fitsync.String("k"))}); err != nil {
		t.Fatal(err)
	}
	if f.auth != "Bearer u:pw" || !strings.Contains(f.bodies[len(f.bodies)-2], `pk in [\"k\"]`) || !strings.Contains(f.bodies[len(f.bodies)-1], `{"pk":"x\"y:2","v":1}`) {
		t.Fatal(f.auth, f.bodies)
	}
	if err := errors.Join(s.Test(context.Background()), str.Test(context.Background())); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.bodies[len(f.bodies)-2], `/v2/vectordb/collections/list {"dbName":"db"}`) || f.bodies[len(f.bodies)-1] != `/v2/vectordb/collections/describe {"collectionName":"s"}` {
		t.Fatal(f.bodies)
	}
	f.answer = `{"code":100,"message":"can't find collection"}`
	if err := str.Test(context.Background()); err == nil || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	if err := str.Write(context.Background(), batch[:1]); err == nil || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	f.answer = `not json`
	if err := str.Write(context.Background(), batch[:1]); err == nil || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	s.ParallelWrites()
}

// TestRefused finds the entity Milvus refuses.
func TestRefused(t *testing.T) {
	f := newFake(t)
	s, err := load(t, map[string]any{"url": f.srv.URL, "collection": "c"})
	if err != nil {
		t.Fatal(err)
	}
	f.refuse = `"bad"`
	batch := []*fitsync.Record{rec(fitsync.OpInsert, id(1), fld("v", fitsync.Int(1))), rec(fitsync.OpInsert, id(2), fld("v", fitsync.String("bad"))), rec(fitsync.OpInsert, id(3), fld("v", fitsync.Int(3)))}
	err = s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || len(f.entities["c"]) != 1 {
		t.Fatal(err, f.entities)
	}
	for st, permanent := range map[int]bool{400: true, 404: false, 503: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(st) }))
		defer srv.Close()
		s, _ := load(t, map[string]any{"url": srv.URL, "collection": "c"})
		if err := s.Write(context.Background(), batch[:1]); err == nil || fitsync.IsPermanent(err) != permanent {
			t.Fatal(st, err)
		}
	}
}

// TestBadRecords fails the records that cannot be entities.
func TestBadRecords(t *testing.T) {
	f := newFake(t)
	s, err := load(t, map[string]any{"url": f.srv.URL, "collection": "{coll}", "max_request_bytes": 400})
	if err != nil {
		t.Fatal(err)
	}
	c := fld("coll", fitsync.String("c"))
	good := rec(fitsync.OpInsert, id(1), c)
	for _, bad := range []*fitsync.Record{
		{Op: fitsync.Op(99)},
		rec(fitsync.OpInsert, fitsync.Null(), c),
		rec(fitsync.OpInsert, id(2)),
		rec(fitsync.OpInsert, id(2), fld("coll", fitsync.String(""))),
		{Op: fitsync.OpInsert, Key: id(2), After: fitsync.Int(1)},
		rec(fitsync.OpInsert, id(2), c, fld("f", fitsync.Float(math.NaN()))),
		rec(fitsync.OpInsert, id(2), c, fld("big", fitsync.String(strings.Repeat("x", 300)))),
	} {
		err := s.Write(context.Background(), []*fitsync.Record{good, bad})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
			t.Fatalf("%+v: %v", bad, err)
		}
		if len(f.entities["c"]) != 1 {
			t.Fatal(f.entities)
		}
	}
}

func TestPrimaryKey(t *testing.T) {
	for key, want := range map[*fitsync.Value]string{
		ptr(fitsync.Int(-5)):             "-5",
		ptr(fitsync.Uint(6)):             "6",
		ptr(id(7)):                       "7",
		ptr(fitsync.String("a")):         `"a"`,
		ptr(fitsync.Bytes([]byte{0xff})): `"/w=="`,
	} {
		got, err := PrimaryKey(*key)
		if err != nil || got != want {
			t.Errorf("%v: %s %v", *key, got, err)
		}
	}
	if _, err := PrimaryKey(fitsync.Null()); err == nil {
		t.Error("a null key")
	}
	for code, want := range map[int]bool{1100: true, 1804: true, 100: false, 8: false, 65535: false} {
		if refused(code) != want {
			t.Errorf("%d", code)
		}
	}
}

func ptr(v fitsync.Value) *fitsync.Value { return &v }

func TestProvisionErrors(t *testing.T) {
	t.Setenv("MILVUS_TOKEN", "t")
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"url": "http://x", "collection": "{"}, "collection"},
		{map[string]any{"url": "http://x", "collection": "c", "primary_key": "a b"}, "primary_key"},
		{map[string]any{"url": "http://x", "collection": "c", "token_env": "MILVUS_TOKEN", "username": "u"}, "token_env"},
		{map[string]any{"url": "http://x", "collection": "c", "token_env": "MILVUS_NONE"}, "token_env"},
		{map[string]any{"url": "http://x", "collection": "c", "username": "u", "password_env": "MILVUS_NONE"}, "password_env"},
		{map[string]any{"url": "http://x", "collection": "c", "username": "u"}, "username"},
		{map[string]any{"url": "http://x", "collection": "c", "username": "a:b", "password_env": "MILVUS_TOKEN"}, "username"},
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

// TestDeleteKeyType deletes by the key as the collection's primary key's
// type, which describe gives at the first delete: Milvus converts an
// upsert's key, but a filter's literal of the other type is refused.
func TestDeleteKeyType(t *testing.T) {
	f := newFake(t)
	s, err := load(t, map[string]any{"url": f.srv.URL, "collection": "c"})
	if err != nil {
		t.Fatal(err)
	}
	describes := func() (n int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, b := range f.bodies {
			if strings.HasPrefix(b, "/v2/vectordb/collections/describe ") {
				n++
			}
		}
		return n
	}
	last := func() string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.bodies[len(f.bodies)-1]
	}
	f.pkType = "VarChar"
	if err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpDelete, id(9)), rec(fitsync.OpDelete, fitsync.String("k"))}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last(), `"filter":"id in [\"9\",\"k\"]"`) || describes() != 1 {
		t.Fatal(f.bodies)
	}
	// Described once: the type is kept.
	f.pkType = "Int64"
	if err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpDelete, id(8))}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last(), `id in [\"8\"]`) || describes() != 1 {
		t.Fatal(f.bodies)
	}
	// An Int64 key given as a string of digits.
	n, err := load(t, map[string]any{"url": f.srv.URL, "collection": "n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpDelete, fitsync.String("42")), rec(fitsync.OpDelete, fitsync.String("k")), rec(fitsync.OpDelete, id(7))}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last(), `id in [42,\"k\",7]`) {
		t.Fatal(f.bodies)
	}
	// No collection yet: the key as it is, described again next time.
	f.noColl = true
	m, err := load(t, map[string]any{"url": f.srv.URL, "collection": "m"})
	if err != nil {
		t.Fatal(err)
	}
	before := describes()
	for range 2 {
		if err := m.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpDelete, id(1))}); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(last(), `id in [1]`) || describes() != before+2 {
		t.Fatal(f.bodies)
	}
	// describe not answered, after the tries: the batch fails, to be tried
	// again.
	f.noColl, f.answer = false, `{"code":8,"message":"rate limit exceeded"}`
	if err := m.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpDelete, id(1))}); err == nil || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	dead, err := load(t, map[string]any{"url": "http://127.0.0.1:1", "collection": "c", "timeout": "1s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dead.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpDelete, id(1))}); err == nil || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	for _, c := range []struct{ typ, pk, want string }{
		{"VarChar", `5`, `"5"`}, {"VarChar", `"a"`, `"a"`}, {"Int64", `"-5"`, `-5`}, {"Int64", `"x"`, `"x"`},
		{"Int64", `"99999999999999999999"`, `"99999999999999999999"`}, {"", `5`, `5`}, {"VarChar", ``, ``},
	} {
		if got := string(literalFor(c.typ, []byte(c.pk))); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.typ, c.pk, got, c.want)
		}
	}
}
