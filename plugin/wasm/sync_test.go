package wasm

import (
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// syncParams returns params for the test plugin at path, with conf as its
// config.
func syncParams(path string, conf map[string]any, params map[string]any) map[string]any {
	c, _ := json.Marshal(conf)
	out := map[string]any{"path": path, "config": string(c)}
	for k, v := range params {
		out[k] = v
	}
	return out
}

// loadSync loads the module wasm of namespace ns with params.
func loadSync(t testing.TB, ns string, params map[string]any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"module": "wasm", "params": params})
	v, err := config.ParseValue(b, "module")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(ns, ref, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m, nil
}

func mustTransform(t testing.TB, plugin string, conf, params map[string]any) *SyncTransform {
	t.Helper()
	m, err := loadSync(t, fitsync.Transforms, syncParams(plugin, conf, params))
	if err != nil {
		t.Fatal(err)
	}
	return m.(*SyncTransform)
}

func js(t testing.TB, v interface{ MarshalJSON() ([]byte, error) }) string {
	t.Helper()
	b, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func records(from, to int64) []*fitsync.Record {
	var recs []*fitsync.Record
	for n := from; n < to; n++ {
		recs = append(recs, fitsync.GenerateRecord("shop.orders", n, 0, 1))
	}
	return recs
}

func keyID(r *fitsync.Record) int64 {
	v, _ := r.Key.Get("id")
	return v.AsInt()
}

// TestSyncTransform checks a transform plugin drops, changes and splits
// records, in order, whether a batch is one call or several on several
// instances.
func TestSyncTransform(t *testing.T) { eachGuest(t, testSyncTransform) }

func testSyncTransform(t *testing.T, plugin string) {
	for _, params := range []map[string]any{nil, {"batch_records": 3, "instances": 3}} {
		tr := mustTransform(t, plugin, map[string]any{"mode": "transform", "fail": -1}, params)
		in := records(0, 12)
		var got []string
		var fails int
		err := tr.ApplyBatch(in, func(i int, o *fitsync.Record) {
			if keyID(o) != keyID(in[i]) {
				t.Errorf("record %d emitted as %d's", keyID(o), i)
			}
			seen, _ := o.After.Get("seen")
			got = append(got, fmt.Sprintf("%d:%s:%v", keyID(o), o.Namespace, seen.AsBool()))
		}, func(int, error) { fails++ })
		if err != nil || fails > 0 {
			t.Fatal(err, fails)
		}
		// An id%5 of 0 drops the record, 1 splits it, and the rest change
		// its row (6, a delete, is split: it has no row after).
		want := "1:shop.orders:false 1:shop.orders.copy:false 2:shop.orders:true 3:shop.orders:true 4:shop.orders:true " +
			"6:shop.orders:false 6:shop.orders.copy:false 7:shop.orders:true 8:shop.orders:true 9:shop.orders:true " +
			"11:shop.orders:false 11:shop.orders.copy:false"
		if g := strings.Join(got, " "); g != want {
			t.Errorf("%v: %s", params, g)
		}
		if js(t, in[2]) != js(t, fitsync.GenerateRecord("shop.orders", 2, 0, 1)) {
			t.Error("the source's record changed")
		}
	}
	// Every part and value type crosses unchanged, both ways.
	tr := mustTransform(t, plugin, map[string]any{"mode": "route", "fail": -1}, nil)
	in := records(0, 14)
	in[3].Tx = fitsync.Tx{ID: "tx \"7\"\n", First: true}
	in[4].Namespace = "shop.ördérs\t\x01"
	if err := tr.ApplyBatch(in, func(i int, o *fitsync.Record) {
		want := js(t, in[i])
		if in[i].Namespace == "shop.orders" {
			want = strings.Replace(want, `"namespace":"shop.orders"`, `"namespace":"orders"`, 1)
		}
		if got := js(t, o); got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}, func(_ int, err error) { t.Error(err) }); err != nil {
		t.Fatal(err)
	}
	// A record the plugin refuses fails alone; Apply takes one record.
	tr = mustTransform(t, plugin, map[string]any{"mode": "refuse", "fail": 3}, nil)
	var failed []int
	n := 0
	if err := tr.ApplyBatch(records(2, 5), func(int, *fitsync.Record) { n++ }, func(i int, err error) {
		failed = append(failed, i)
		if !strings.Contains(err.Error(), "record 3 refused") {
			t.Error(err)
		}
	}); err != nil || n != 2 || len(failed) != 1 || failed[0] != 1 {
		t.Errorf("%v %d %v", err, n, failed)
	}
	if err := tr.Apply(records(3, 4)[0], func(*fitsync.Record) {}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Error(err)
	}
}

// TestSyncTransformFailures checks a call that runs past its timeout,
// asks for more memory than it may have, traps or reaches for HTTP fails
// each record of its batch, and the instance it stopped is made again.
func TestSyncTransformFailures(t *testing.T) { eachGuest(t, testSyncTransformFailures) }

func testSyncTransformFailures(t *testing.T, plugin string) {
	for mode, want := range map[string]string{
		"loop": "past its timeout", "grow": "", "trap": "", "http": "may make HTTP requests",
	} {
		tr := mustTransform(t, plugin, map[string]any{"mode": mode, "fail": 4}, map[string]any{"instances": 1, "timeout": "200ms", "max_memory_bytes": 64 << 20})
		var failed []int
		err := tr.ApplyBatch(records(2, 6), func(int, *fitsync.Record) {}, func(i int, err error) {
			failed = append(failed, i)
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %v", mode, err)
			}
		})
		if err != nil || len(failed) != 4 {
			t.Errorf("%s: %v %v", mode, err, failed)
		}
		n := 0
		if err := tr.ApplyBatch(records(5, 8), func(int, *fitsync.Record) { n++ }, func(_ int, err error) { t.Errorf("%s: %v", mode, err) }); err != nil || n != 3 {
			t.Errorf("%s, again: %v %d", mode, err, n)
		}
	}
}

func TestSyncLoadErrors(t *testing.T) {
	if _, err := loadSync(t, fitsync.Transforms, map[string]any{"path": "nope.wasm"}); err == nil {
		t.Error("a missing plugin loaded")
	}
	bare := filepath.Join(t.TempDir(), "bare.wasm") // exports no sync call
	if err := os.WriteFile(bare, fitModule(0, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSync(t, fitsync.Sinks, map[string]any{"path": bare}); err == nil || !strings.Contains(err.Error(), "does not export fit_sync_write") {
		t.Error(err)
	}
	if _, err := loadSync(t, fitsync.Codecs, map[string]any{"path": bare}); err == nil || !strings.Contains(err.Error(), "fit_sync_decode") {
		t.Error(err)
	}
	if _, err := loadSync(t, fitsync.Sources, map[string]any{"path": goGuest(t), "allow_hosts": []string{""}}); err == nil || !strings.Contains(err.Error(), "names no host") {
		t.Error(err)
	}
}

// TestSyncCodec reads a line format of the plugin's own from a file, and
// writes the records back the same.
func TestSyncCodec(t *testing.T) { eachGuest(t, testSyncCodec) }

func testSyncCodec(t *testing.T, plugin string) {
	m, err := loadSync(t, fitsync.Codecs, syncParams(plugin, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	c := m.(fitsync.Codec)
	file := filepath.Join(t.TempDir(), "prices.txt")
	const lines = "1|apple|1.20\n2|pear|0.95\n3|plum|12.00\n"
	if err := os.WriteFile(file, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var recs []*fitsync.Record
	if err := c.Decode(data, func(r *fitsync.Record) { recs = append(recs, r) }); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || js(t, recs[1]) != `{"namespace":"lines","op":"insert","key":{"map":{"id":{"int":2}}},"after":{"map":{"id":{"int":2},"name":"pear","price":{"decimal":"0.95"}}}}` {
		t.Fatalf("%d %s", len(recs), js(t, recs[1]))
	}
	out, err := c.Encode([]byte("#\n"), recs)
	if err != nil || string(out) != "#\n"+lines {
		t.Errorf("%v %q", err, out)
	}
	if err := c.Decode([]byte("not a line"), func(*fitsync.Record) {}); err == nil || !strings.Contains(err.Error(), "is not id|name|price") {
		t.Error(err)
	}
}

// engine returns a fib engine that runs until the test ends.
func engine(t *testing.T) *fib.Engine {
	t.Helper()
	cfg := fib.DefaultConfig()
	cfg.LogSettings = false
	e, err := fib.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = e.Run(); close(done) }()
	t.Cleanup(func() { e.Stop(); <-done; _ = e.Close() })
	return e
}

// pagedAPI is a paged HTTP API of items, and a sink of posted batches.
type pagedAPI struct {
	mu     sync.Mutex
	items  []string
	posted []json.RawMessage
}

func (a *pagedAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == http.MethodPost {
		var batch []json.RawMessage
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &batch); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		a.posted = append(a.posted, batch...)
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	type item struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	var page struct {
		Items []item `json:"items"`
		More  bool   `json:"more"`
	}
	page.Items = []item{}
	for id := after + 1; id <= len(a.items) && len(page.Items) < 2; id++ {
		page.Items = append(page.Items, item{id, a.items[id-1]})
	}
	page.More = after+len(page.Items) < len(a.items)
	_ = json.NewEncoder(w).Encode(page)
}

func (a *pagedAPI) add(names ...string) {
	a.mu.Lock()
	a.items = append(a.items, names...)
	a.mu.Unlock()
}

func (a *pagedAPI) postedCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.posted)
}

func syncConfig(t *testing.T, dir string, pipeline map[string]any) *config.Sync {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": dir, "pipelines": []any{pipeline}}})
	c, err := config.Load(b)
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if err := fitsync.Check(c.Sync); err != nil {
		t.Fatal(err)
	}
	return c.Sync
}

// runOnce runs pipeline until it stops, and returns its status.
func runOnce(t *testing.T, opts fitsync.Options, s *config.Sync) fitsync.Status {
	t.Helper()
	rt := fitsync.NewRuntime(opts)
	defer rt.Close(t.Context())
	if err := rt.Apply(s); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		st := rt.Status()[0]
		switch st.State {
		case "finished", "stopped":
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %s: %s", st.State, st.Err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func memSink(store string) map[string]any {
	return map[string]any{"name": "m", "sink": map[string]any{"module": "memory", "params": map[string]any{"store": store}}, "batch": map[string]any{"max_wait": "1ms"}}
}

// TestSyncConnectors reads a paged HTTP API through a source plugin into a
// sink plugin, which posts it back, and a memory sink; then, run again,
// goes on from the checkpoint, reading only what is new.
func TestSyncConnectors(t *testing.T) { eachGuest(t, testSyncConnectors) }

func testSyncConnectors(t *testing.T, plugin string) {
	a := &pagedAPI{}
	a.add("a", "b", "c", "d", "e")
	srv := httptest.NewServer(a)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	store := fitsync.MemoryStore(t.Name())
	defer store.Reset()
	dir := t.TempDir()
	cfg := syncConfig(t, dir, map[string]any{"name": "p", "mode": "once",
		"source": map[string]any{"module": "wasm", "params": syncParams(plugin, map[string]any{"url": srv.URL + "/items"}, map[string]any{"allow_hosts": []string{u.Hostname()}})},
		"sinks": []any{memSink(t.Name()), map[string]any{"name": "api", "sink": map[string]any{"module": "wasm",
			"params": syncParams(plugin, map[string]any{"url": srv.URL + "/sink"}, map[string]any{"allow_hosts": []string{u.Host}})}}}})
	opts := fitsync.Options{Engine: engine(t)}
	if st := runOnce(t, opts, cfg); st.State != "finished" || string(st.Checkpoint) != `{"after":5}` {
		t.Fatalf("%+v", st)
	}
	if got := ids(store.Records()); got != "1 2 3 4 5" || a.postedCount() != 5 {
		t.Fatalf("%s, %d posted", got, a.postedCount())
	}
	a.add("f", "g", "h")
	// A finished job runs again when asked (fit sync run, or a schedule),
	// as a new job going on from where the last ended.
	if st, err := fitsync.RunPipeline(t.Context(), cfg, "p", opts); err != nil || st.State != "finished" || string(st.Checkpoint) != `{"after":8}` {
		t.Fatalf("%v %+v", err, st)
	}
	if got := ids(store.Records()); got != "1 2 3 4 5 6 7 8" || a.postedCount() != 8 {
		t.Fatalf("%s, %d posted", got, a.postedCount())
	}
	if r := store.Records()[6]; js(t, r) != `{"source":"wasm","namespace":"api.items","op":"insert","key":{"map":{"id":{"int":7}}},"after":{"map":{"id":{"int":7},"name":"g"}},"position":{"after":7}}` {
		t.Errorf("%s", js(t, r))
	}
}

// TestSyncTransformInAPipeline runs a transform plugin in a pipeline to its
// end, its checkpoint past every record.
func TestSyncTransformInAPipeline(t *testing.T) { eachGuest(t, testSyncTransformInAPipeline) }

func testSyncTransformInAPipeline(t *testing.T, plugin string) {
	store := fitsync.MemoryStore(t.Name())
	defer store.Reset()
	st := runOnce(t, fitsync.Options{}, syncConfig(t, t.TempDir(), map[string]any{"name": "p", "mode": "once",
		"source":     map[string]any{"module": "generate", "params": map[string]any{"records": 20, "tx_size": 3}},
		"transforms": []any{map[string]any{"module": "wasm", "params": syncParams(plugin, map[string]any{"mode": "transform", "fail": -1}, map[string]any{"batch_records": 4})}},
		"sinks":      []any{memSink(t.Name())}}))
	if st.State != "finished" || string(st.Checkpoint) != `{"n":20}` {
		t.Fatalf("%+v", st)
	}
	if got := ids(store.Records()); got != "1 1 2 3 4 6 6 7 8 9 11 11 12 13 14 16 16 17 18 19" {
		t.Error(got)
	}
}

func ids(recs []*fitsync.Record) string {
	var s []string
	for _, r := range recs {
		s = append(s, strconv.FormatInt(keyID(r), 10))
	}
	return strings.Join(s, " ")
}

// TestSyncFailuresKeepTheCheckpoint checks a plugin past its time or its
// memory, or calling a host not allowed, fails its batch, which stops the
// pipeline with on_error stop, and the checkpoint stays before it.
func TestSyncFailuresKeepTheCheckpoint(t *testing.T) { eachGuest(t, testSyncFailuresKeepTheCheckpoint) }

func testSyncFailuresKeepTheCheckpoint(t *testing.T, plugin string) {
	a := &pagedAPI{}
	srv := httptest.NewServer(a)
	defer srv.Close()
	retry := map[string]any{"max_attempts": 1}
	for name, tc := range map[string]struct {
		transform, sink map[string]any
		want            string
	}{
		"time":   {transform: map[string]any{"mode": "loop", "fail": 30}, want: "past its timeout"},
		"memory": {transform: map[string]any{"mode": "grow", "fail": 30}},
		"host":   {sink: map[string]any{"url": srv.URL + "/sink"}, want: "not in allow_hosts"},
	} {
		t.Run(name, func(t *testing.T) {
			store := fitsync.MemoryStore(t.Name())
			defer store.Reset()
			dir := t.TempDir()
			p := map[string]any{"name": "p", "mode": "once", "retry": retry,
				"source": map[string]any{"module": "generate", "params": map[string]any{"records": 60}},
				"sinks":  []any{memSink(t.Name())}}
			if tc.transform != nil {
				p["transforms"] = []any{map[string]any{"module": "wasm", "params": syncParams(plugin, tc.transform, map[string]any{"timeout": "200ms", "max_memory_bytes": 64 << 20, "batch_records": 8})}}
			}
			if tc.sink != nil {
				p["sinks"] = append(p["sinks"].([]any), map[string]any{"name": "api", "sink": map[string]any{"module": "wasm",
					"params": syncParams(plugin, tc.sink, map[string]any{"allow_hosts": []string{"example.com"}})}})
			}
			st := runOnce(t, fitsync.Options{Engine: engine(t)}, syncConfig(t, dir, p))
			if st.State != "stopped" || !strings.Contains(st.Err, tc.want) {
				t.Fatalf("%+v", st)
			}
			var n int64
			if st.Checkpoint != nil {
				var cp struct{ N int64 }
				if err := json.Unmarshal(st.Checkpoint, &cp); err != nil {
					t.Fatal(err)
				}
				n = cp.N
			}
			limit := int64(30)
			if tc.sink != nil {
				limit = 0
			}
			if n > limit {
				t.Errorf("the checkpoint is at %d, past %d", n, limit)
			}
			for _, r := range store.Records() {
				if tc.transform != nil && keyID(r) >= 30 {
					t.Errorf("record %d written", keyID(r))
				}
			}
		})
	}
}

func TestAllowedHosts(t *testing.T) {
	p := &syncPool{opts: syncOpts{allow: []string{"api.example.com", "127.0.0.1:8080", "*.example.org", "[::1]"}}}
	for raw, want := range map[string]bool{
		"https://api.example.com/x": true, "http://API.example.com:81/": true, "http://example.com/": false,
		"http://127.0.0.1:8080/": true, "http://127.0.0.1/": false, "http://a.b.example.org/": true,
		"http://example.org/": false, "http://[::1]:9/": true,
	} {
		u, _ := url.Parse(raw)
		if p.allowed(u) != want {
			t.Errorf("%s: %v", raw, !want)
		}
	}
}

// BenchmarkSyncTransform compares the WASM transform with the built-in
// route doing the same work, renaming the namespace, over batches of 256
// records, in a plugin of each guest that can be built here (go, tinygo,
// rust; see syncGuests): wasm-<guest>-<instances>.
func BenchmarkSyncTransform(b *testing.B) {
	in := records(0, 256)
	b.Run("builtin", func(b *testing.B) {
		m, err := loadBuiltin(b, `{"module": "route", "params": {"map": {"shop.orders": "orders"}}}`)
		if err != nil {
			b.Fatal(err)
		}
		tr := m.(fitsync.Transform)
		b.ReportAllocs()
		for b.Loop() {
			for _, r := range in {
				if err := tr.Apply(r, func(*fitsync.Record) {}); err != nil {
					b.Fatal(err)
				}
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(in)), "ns/record")
	})
	for _, g := range syncGuests {
		for _, instances := range []int{1, 4} {
			b.Run(fmt.Sprintf("wasm-%s-%d", g.name, instances), func(b *testing.B) {
				tr := mustTransform(b, g.plugin(b), map[string]any{"mode": "route", "fail": -1}, map[string]any{"instances": instances, "batch_records": 64})
				b.ReportAllocs()
				for b.Loop() {
					if err := tr.ApplyBatch(in, func(int, *fitsync.Record) {}, func(_ int, err error) { b.Fatal(err) }); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(in)), "ns/record")
			})
		}
	}
}

func loadBuiltin(t testing.TB, ref string) (any, error) {
	v, err := config.ParseValue([]byte(ref), "transform")
	if err != nil {
		t.Fatal(err)
	}
	var r config.ModuleRef
	if err := v.Decode(&r); err != nil {
		t.Fatal(err)
	}
	return module.Load(fitsync.Transforms, r, nil)
}
