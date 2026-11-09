package syncembed

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
)

func init() { fibrest.BackoffFirst, fibrest.BackoffMost = time.Millisecond, 2*time.Millisecond }

func load(t *testing.T, params map[string]any, env *module.Env) (*Embed, error) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": "x", "pipelines": []any{map[string]any{"name": "p",
		"source": map[string]any{"module": "generate"}, "transforms": []any{map[string]any{"module": "embed", "params": params}},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}}}}})
	cfg, err := config.Load(b)
	if err != nil {
		return nil, err
	}
	v, err := module.Load(fitsync.Transforms, cfg.Sync.Pipelines[0].Transforms[0], env)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(v) })
	return v.(*Embed), nil
}

// api is an embeddings API: a text's vector is its length and its first
// byte; a text holding "refuse" is refused (400), and status, when set,
// answers every request.
type api struct {
	mu     sync.Mutex
	srv    *httptest.Server
	inputs [][]string
	auth   string
	body   map[string]any
	status int
	answer string
	calls  atomic.Int32
}

func newAPI(t *testing.T) *api {
	a := &api{}
	a.srv = httptest.NewServer(a)
	t.Cleanup(a.srv.Close)
	return a
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.calls.Add(1)
	var req struct {
		Model string
		Input []string
	}
	var raw map[string]any
	b := make([]byte, 1<<20)
	n, _ := r.Body.Read(b)
	_ = json.Unmarshal(b[:n], &req)
	_ = json.Unmarshal(b[:n], &raw)
	a.mu.Lock()
	a.inputs = append(a.inputs, req.Input)
	a.auth, a.body = r.Header.Get("Authorization"), raw
	status, answer := a.status, a.answer
	a.mu.Unlock()
	if r.URL.Path != "/v1/embeddings" {
		http.Error(w, r.URL.Path, 404)
		return
	}
	if status != 0 {
		http.Error(w, "no", status)
		return
	}
	if answer != "" {
		_, _ = w.Write([]byte(answer))
		return
	}
	var data []map[string]any
	for i, in := range req.Input {
		if strings.Contains(in, "refuse") {
			http.Error(w, `{"error": "bad input"}`, 400)
			return
		}
		data = append(data, map[string]any{"index": len(req.Input) - 1 - i, "embedding": vectorOf(req.Input[len(req.Input)-1-i])})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func vectorOf(s string) []float64 { return []float64{float64(len(s)), float64(s[0])} }

func fld(name string, v fitsync.Value) fitsync.Field { return fitsync.Field{Name: name, Value: v} }

func rec(op fitsync.Op, fields ...fitsync.Field) *fitsync.Record {
	r := &fitsync.Record{Namespace: "docs", Op: op}
	if op == fitsync.OpDelete {
		r.Before = fitsync.Map(fields...)
	} else {
		r.After = fitsync.Map(fields...)
	}
	return r
}

// run puts in through e, returning what each record became and the errors.
func run(e *Embed, in []*fitsync.Record) ([]*fitsync.Record, map[int]error, error) {
	out := make([]*fitsync.Record, len(in))
	errs := map[int]error{}
	err := e.ApplyBatch(in, func(i int, o *fitsync.Record) { out[i] = o }, func(i int, err error) { errs[i] = err })
	return out, errs, err
}

func vecOf(t *testing.T, r *fitsync.Record, field string) []float64 {
	t.Helper()
	v, ok := r.After.Get(field)
	if !ok {
		t.Fatalf("no %s in %v", field, r.After)
	}
	var out []float64
	for _, e := range v.Elems() {
		out = append(out, e.AsFloat())
	}
	return out
}

// TestEmbed sets each insert's and update's vector, sends each text once,
// passes deletes and records without text, and takes vectors from the
// cache the second time.
func TestEmbed(t *testing.T) {
	a := newAPI(t)
	t.Setenv("EMBED_KEY", "k")
	e, err := load(t, map[string]any{"url": a.srv.URL + "/v1", "model": "m", "api_key_env": "EMBED_KEY", "fields": []string{"title", "body"},
		"separator": " | ", "dimensions": 2, "batch_records": 2, "max_parallel": 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := []*fitsync.Record{
		rec(fitsync.OpInsert, fld("title", fitsync.String("a")), fld("body", fitsync.String("bb"))),
		rec(fitsync.OpUpdate, fld("title", fitsync.String("c")), fld("embedding", fitsync.Int(1)), fld("body", fitsync.Null())),
		rec(fitsync.OpDelete, fld("title", fitsync.String("a"))),
		{Op: fitsync.OpDDL},
		rec(fitsync.OpInsert, fld("other", fitsync.String("x"))),
		rec(fitsync.OpInsert, fld("title", fitsync.Int(42))),
		rec(fitsync.OpMessage, fld("title", fitsync.String("a")), fld("body", fitsync.String("bb"))),
		{Op: fitsync.OpInsert, After: fitsync.String("not a map")},
	}
	out, errs, err := run(e, in)
	if err != nil || len(errs) > 0 {
		t.Fatal(err, errs)
	}
	if v := vecOf(t, out[0], "embedding"); len(v) != 2 || v[0] != 6 || v[1] != 'a' {
		t.Fatal(v)
	}
	if v := vecOf(t, out[1], "embedding"); v[0] != 1 || v[1] != 'c' || out[1].After.Fields()[1].Name != "embedding" {
		t.Fatal(out[1].After)
	}
	if out[2] != in[2] || out[3] != in[3] || out[4] != in[4] || out[7] != in[7] {
		t.Fatal("records passed changed")
	}
	if v := vecOf(t, out[5], "embedding"); v[0] != 2 || v[1] != '4' {
		t.Fatal(v)
	}
	if v := vecOf(t, out[6], "embedding"); v[0] != 6 {
		t.Fatal(v)
	}
	if _, ok := in[0].After.Get("embedding"); ok {
		t.Fatal("the source's record changed")
	}
	a.mu.Lock()
	inputs, auth, body := a.inputs, a.auth, a.body
	a.mu.Unlock()
	if len(inputs) != 2 || len(inputs[0])+len(inputs[1]) != 3 || auth != "Bearer k" || body["model"] != "m" || body["dimensions"] != 2.0 || body["encoding_format"] != "float" {
		t.Fatal(inputs, auth, body)
	}
	// Again: every vector is cached.
	if _, errs, err := run(e, in); err != nil || len(errs) > 0 || a.calls.Load() != 2 || e.cache.Len() != 3 {
		t.Fatal(err, errs, a.calls.Load(), e.cache.Len())
	}
	var got []*fitsync.Record
	if err := e.Apply(in[0], func(o *fitsync.Record) { got = append(got, o) }); err != nil || len(got) != 1 {
		t.Fatal(err, got)
	}
}

// TestFailures fails the records of a request that fails, or a text
// refused, alone; a text too long; and every record once the transform is
// let go.
func TestFailures(t *testing.T) {
	a := newAPI(t)
	e, err := load(t, map[string]any{"url": a.srv.URL + "/v1", "model": "m", "fields": []string{"t"}, "max_text_bytes": 10, "attempts": 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := []*fitsync.Record{rec(fitsync.OpInsert, fld("t", fitsync.String("ok"))), rec(fitsync.OpInsert, fld("t", fitsync.String("refuse"))),
		rec(fitsync.OpInsert, fld("t", fitsync.String("too long a text"))), rec(fitsync.OpInsert, fld("t", fitsync.String("fine")))}
	out, errs, err := run(e, in)
	if err != nil || len(errs) != 2 || errs[1] == nil || !fitsync.IsPermanent(errs[1]) || errs[2] == nil || !strings.Contains(errs[2].Error(), "max_text_bytes") || out[0] == nil || out[3] == nil {
		t.Fatal(err, errs, out)
	}
	var one error
	if err := e.Apply(in[1], func(*fitsync.Record) { t.Fatal("emitted") }); err == nil {
		t.Fatal("no error")
	} else {
		one = err
	}
	if !fitsync.IsPermanent(one) {
		t.Fatal(one)
	}
	fresh := func(s string) []*fitsync.Record {
		return []*fitsync.Record{rec(fitsync.OpInsert, fld("t", fitsync.String(s)))}
	}
	for _, c := range []struct {
		status    int
		answer    string
		permanent bool
		calls     int32
	}{
		{500, "", false, 2},
		{401, "", false, 1},
		{0, "not json", false, 1},
		{0, `{"data": []}`, false, 1},
		{0, `{"data": [{"index": 3, "embedding": [1]}]}`, false, 1},
		{0, `{"data": [{"index": 0, "embedding": []}]}`, false, 1},
	} {
		a.mu.Lock()
		a.status, a.answer = c.status, c.answer
		a.mu.Unlock()
		before := a.calls.Load()
		_, errs, err := run(e, fresh(fmt.Sprint("x", c.status, len(c.answer))))
		if err != nil || errs[0] == nil || fitsync.IsPermanent(errs[0]) != c.permanent || a.calls.Load()-before != c.calls {
			t.Errorf("%d %s: %v %v %d", c.status, c.answer, err, errs, a.calls.Load()-before)
		}
	}
	a.mu.Lock()
	a.status, a.answer = 0, ""
	a.mu.Unlock()
	dead, err := load(t, map[string]any{"url": "http://127.0.0.1:1/v1", "model": "m", "fields": []string{"t"}, "attempts": 1, "timeout": "1s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, errs, _ := run(dead, fresh("a")); errs[0] == nil || !strings.Contains(errs[0].Error(), "127.0.0.1:1") {
		t.Fatal(errs)
	}
	e.Cleanup()
	if _, _, err := run(e, fresh("after")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestLLMProxy calls this config's llm.proxy's API, on loopback, with its
// certificate pinned when it has one.
func TestLLMProxy(t *testing.T) {
	if _, err := load(t, map[string]any{"model": "m", "fields": []string{"t"}}, nil); err == nil || !strings.Contains(err.Error(), "llm.proxy") {
		t.Fatal(err)
	}
	a := newAPI(t)
	port := a.srv.URL[strings.LastIndexByte(a.srv.URL, ':')+1:]
	e, err := load(t, map[string]any{"model": "m", "fields": []string{"t"}}, &module.Env{LLMProxy: &config.LLMProxyAPI{Address: "0.0.0.0:" + port}})
	if err != nil {
		t.Fatal(err)
	}
	if _, errs, err := run(e, []*fitsync.Record{rec(fitsync.OpInsert, fld("t", fitsync.String("hi")))}); err != nil || len(errs) > 0 {
		t.Fatal(err, errs)
	}
	tlsSrv := httptest.NewTLSServer(a)
	defer tlsSrv.Close()
	cert := filepath.Join(t.TempDir(), "cert.pem")
	_ = os.WriteFile(cert, append([]byte("junk\n"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})...), 0o600)
	tport := tlsSrv.URL[strings.LastIndexByte(tlsSrv.URL, ':')+1:]
	e, err = load(t, map[string]any{"model": "m", "fields": []string{"t"}}, &module.Env{LLMProxy: &config.LLMProxyAPI{Address: ":" + tport, CertFile: cert}})
	if err != nil {
		t.Fatal(err)
	}
	if _, errs, err := run(e, []*fitsync.Record{rec(fitsync.OpInsert, fld("t", fitsync.String("tls")))}); err != nil || len(errs) > 0 {
		t.Fatal(err, errs)
	}
	for _, c := range []struct {
		api  config.LLMProxyAPI
		want string
	}{
		{config.LLMProxyAPI{Address: "[::]:8080"}, "http://[::1]:8080/v1"},
		{config.LLMProxyAPI{Address: "10.0.0.1:8080"}, "http://10.0.0.1:8080/v1"},
	} {
		if u, pin, err := ProxyURL(&c.api); err != nil || u != c.want || pin != nil {
			t.Errorf("%v: %s %v", c.api, u, err)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.pem")
	_ = os.WriteFile(bad, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}), 0o600)
	empty := filepath.Join(t.TempDir(), "empty.pem")
	_ = os.WriteFile(empty, []byte("x"), 0o600)
	for _, api := range []config.LLMProxyAPI{{Address: "nohost"}, {Address: ":1", CertFile: bad}, {Address: ":1", CertFile: empty}, {Address: ":1", CertFile: filepath.Join(t.TempDir(), "none")}} {
		if _, err := load(t, map[string]any{"model": "m", "fields": []string{"t"}}, &module.Env{LLMProxy: &api}); err == nil || !strings.Contains(err.Error(), "llm.proxy") {
			t.Errorf("%v: %v", api, err)
		}
	}
}

func TestProvisionErrors(t *testing.T) {
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"url": "http://x", "model": "m", "fields": []string{}}, "fields"},
		{map[string]any{"url": "http://x", "model": "m", "fields": []string{"t"}, "api_key_env": "EMBED_NONE"}, "api_key_env"},
		{map[string]any{"url": "ftp://x", "model": "m", "fields": []string{"t"}}, "url"},
	} {
		if _, err := load(t, c.params, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.params, err)
		}
	}
}

// TestCache drops the least used vectors past its bound.
func TestCache(t *testing.T) {
	c := newCache(2 * (entryOverhead + 8))
	k := func(b byte) [32]byte { return [32]byte{b} }
	c.put(k(1), []float32{1, 1})
	c.put(k(2), []float32{2, 2})
	if _, ok := c.get(k(1)); !ok {
		t.Fatal("1")
	}
	c.put(k(3), []float32{3, 3})
	if _, ok := c.get(k(2)); ok {
		t.Fatal("2 kept")
	}
	if _, ok := c.get(k(1)); !ok || c.Len() != 2 {
		t.Fatal("1 dropped", c.Len())
	}
	c.put(k(1), []float32{9, 9})
	c.put(k(4), make([]float32, 1000))
	if v, _ := c.get(k(1)); v[0] != 1 || c.Len() != 2 {
		t.Fatal(v, c.Len())
	}
}
