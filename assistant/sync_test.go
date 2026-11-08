package assistant

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fit/config"
)

// The records of fakeSync: a row of the person's data, which the model is
// shown only as kinds unless data.send_records is set.
const (
	syncSecretRow = "alice@example.com"
	syncRecord    = `{"source":"mysql","namespace":"shop.orders","op":"update","key":{"map":{"id":{"int":7}}},` +
		`"before":null,"after":{"map":{"id":{"int":7},"email":"` + syncSecretRow + `","paid":true,"total":{"decimal":"12.30"},` +
		`"tags":["a",{"int":1}],"doc":{"json":{"x":1}},"at":{"time":"2026-11-08T10:00:00Z"}}},"tx":{"id":"t1","last":true},"position":{"gtid":"g:5"}}`
)

// fakeSync is the Admin API's /sync endpoints, as admin/sync.go answers
// them, with the requests it got.
type fakeSync struct {
	mu   sync.Mutex
	reqs []string
	bad  bool // answers that are not JSON
}

func (f *fakeSync) handler() http.Handler {
	mux := http.NewServeMux()
	answer := func(w http.ResponseWriter, r *http.Request, body string) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		bad := f.bad
		f.mu.Unlock()
		if bad {
			body = "not json"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("GET /sync/pipelines", func(w http.ResponseWriter, r *http.Request) {
		answer(w, r, `{"pipelines": [{"name": "orders", "state": "stopped", "error": "sink es: 403", "errors": 3, "read": 10, "acknowledged": 7,
			"failed": 1, "throughput": 0.5, "lag_seconds": 1.5, "queue": {"records": 3, "bytes": 90, "max_records": 10, "max_bytes": 1000},
			"checkpoint": {"gtid": "g:4"}, "dead_letters": {"written": 2, "not_written": 0, "replayed": 0, "store": {"path": "/x", "kept": 2}}},
			{"name": "logs", "state": "running", "errors": 0, "read": 1, "acknowledged": 1, "failed": 0, "throughput": 0, "queue": {}}]}`)
	})
	mux.HandleFunc("GET /sync/pipelines/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "orders" {
			w.WriteHeader(http.StatusNotFound)
			answer(w, r, `{"error": "sync: no pipeline `+r.PathValue("name")+`"}`)
			return
		}
		answer(w, r, `{"name": "orders", "state": "stopped", "checkpoint": {"gtid": "g:4"}, "queue": {"records": 3}, "sinks": [{"name": "es", "errors": 3}]}`)
	})
	mux.HandleFunc("POST /sync/pipelines/{name}/test", func(w http.ResponseWriter, r *http.Request) {
		answer(w, r, `{"ok": false, "results": [{"part": "source", "module": "mysql", "tested": true, "ok": true, "took_ms": 3},
			{"part": "sink es", "module": "elasticsearch", "tested": true, "ok": false, "error": "403 Forbidden", "took_ms": 5}]}`)
	})
	mux.HandleFunc("POST /sync/pipelines/{name}/dry-run", func(w http.ResponseWriter, r *http.Request) {
		answer(w, r, `{"from": {"gtid": "g:4"}, "records": [{"in": `+syncRecord+`, "out": [`+syncRecord+`]},
			{"in": `+syncRecord+`, "out": [], "error": "transform 1 (cast): total is not a number"}], "timed_out": true}`)
	})
	mux.HandleFunc("GET /sync/pipelines/{name}/dead-letters", func(w http.ResponseWriter, r *http.Request) {
		answer(w, r, `{"letters": [{"id": 4, "saved": "2026-11-08T10:00:00Z", "error": "403", "at": "sink es", "attempts": 3, "record": `+syncRecord+`},
			{"id": 5, "saved": "2026-11-08T10:00:01Z", "record": {"line": "`+syncSecretRow+`", "n": 2}}], "store": {"path": "/x", "kept": 2}, "more": true}`)
	})
	return mux
}

func (f *fakeSync) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return ""
	}
	return f.reqs[len(f.reqs)-1]
}

// syncAssistant is an assistant over fakeSync, its config running the
// pipeline orders.
func syncAssistant(t *testing.T, sendRecords bool) (*Assistant, *fakeSync) {
	t.Helper()
	fs := &fakeSync{}
	cfg := &config.Config{Sync: &config.Sync{StateDir: "/var/lib/fit/sync", Pipelines: []config.SyncPipeline{
		{Name: "logs"}, {Name: "orders", OnError: "dead_letter"}}}}
	a := toolsAssistant(t, cfg, func(o *Options) {
		o.API = fs.handler()
		o.Settings.Data.SendRecords = sendRecords
	})
	return a, fs
}

// TestSyncTools: the sync tools read the pipelines, their tests, dry runs
// and dead letters through the Admin API; the records' values reach the
// model as kinds only, unless data.send_records is set.
func TestSyncTools(t *testing.T) {
	a, fs := syncAssistant(t, false)
	out := mustRun(t, a, "sync_pipelines", `{}`)
	var all struct {
		Pipelines []map[string]any `json:"pipelines"`
	}
	if err := json.Unmarshal([]byte(out), &all); err != nil || len(all.Pipelines) != 2 || all.Pipelines[0]["dead_letters_kept"] != 2.0 ||
		all.Pipelines[0]["error"] != "sink es: 403" || all.Pipelines[0]["queue"] != nil || all.Pipelines[0]["checkpoint"] != nil ||
		all.Pipelines[1]["dead_letters_kept"] != nil {
		t.Fatalf("%v %s", err, out)
	}
	out = mustRun(t, a, "sync_pipelines", `{"pipeline": "orders"}`)
	if !strings.Contains(out, `"status":{"name":"orders"`) || !strings.Contains(out, `"config":{"name":"orders"`) || !strings.Contains(out, `"on_error":"dead_letter"`) {
		t.Error(out)
	}
	wantErr(t, a, "sync_pipelines", `{"pipeline": "nope"}`, "no pipeline nope")
	wantErr(t, a, "sync_pipelines", `[`, "not what the tool takes")

	out = mustRun(t, a, "sync_test", `{"pipeline": "orders"}`)
	if !strings.Contains(out, `"error":"403 Forbidden"`) || fs.last() != "POST /sync/pipelines/orders/test " {
		t.Error(out, fs.last())
	}
	wantErr(t, a, "sync_test", `{}`, "pipeline is a pipeline's name")
	wantErr(t, a, "sync_test", `[`, "not what the tool takes")

	out = mustRun(t, a, "sync_dry_run", `{"pipeline": "orders", "from": "start", "limit": 2}`)
	if strings.Contains(out, syncSecretRow) || strings.Contains(out, "12.30") || !strings.Contains(out, `"email":"(string)"`) ||
		!strings.Contains(out, `"total":"(decimal)"`) || !strings.Contains(out, `"paid":"(bool)"`) || !strings.Contains(out, `"tags":["(string)","(int)"]`) ||
		!strings.Contains(out, `"doc":"(json)"`) || !strings.Contains(out, `"namespace":"shop.orders"`) || !strings.Contains(out, `"position":{"gtid":"g:5"}`) ||
		!strings.Contains(out, `"values":"each value is shown as its kind only`) || !strings.Contains(out, "total is not a number") ||
		!strings.Contains(out, `"timed_out":true`) {
		t.Error(out)
	}
	if got := fs.last(); got != `POST /sync/pipelines/orders/dry-run {"from":"start","limit":2,"timeout":"5s"}` {
		t.Error(got)
	}
	mustRun(t, a, "sync_dry_run", `{"pipeline": "orders"}`)
	if got := fs.last(); !strings.Contains(got, `"limit":5`) {
		t.Error(got)
	}
	wantErr(t, a, "sync_dry_run", `{"pipeline": "orders", "from": "later"}`, "from is checkpoint or start")
	wantErr(t, a, "sync_dry_run", `{"pipeline": "orders", "limit": 21}`, "limit is 1 to 20")
	wantErr(t, a, "sync_dry_run", `{}`, "pipeline is a pipeline's name")
	wantErr(t, a, "sync_dry_run", `[`, "not what the tool takes")

	out = mustRun(t, a, "sync_dead_letters", `{"pipeline": "orders", "after": 3, "limit": 2}`)
	if strings.Contains(out, syncSecretRow) || !strings.Contains(out, `"at":"sink es"`) || !strings.Contains(out, `"record":{"line":"(string)","n":"(number)"}`) ||
		!strings.Contains(out, `"more":true`) || !strings.Contains(out, `"values"`) {
		t.Error(out)
	}
	if got := fs.last(); got != "GET /sync/pipelines/orders/dead-letters?after=3&limit=2 " {
		t.Error(got)
	}
	mustRun(t, a, "sync_dead_letters", `{"pipeline": "orders"}`)
	if got := fs.last(); got != "GET /sync/pipelines/orders/dead-letters?limit=20 " {
		t.Error(got)
	}
	wantErr(t, a, "sync_dead_letters", `{"pipeline": "orders", "limit": 101}`, "limit is 1 to 100")
	wantErr(t, a, "sync_dead_letters", `{}`, "pipeline is a pipeline's name")
	wantErr(t, a, "sync_dead_letters", `[`, "not what the tool takes")

	// Answers that are not JSON.
	fs.mu.Lock()
	fs.bad = true
	fs.mu.Unlock()
	wantErr(t, a, "sync_pipelines", `{}`, "not what it should be")
	wantErr(t, a, "sync_dry_run", `{"pipeline": "orders"}`, "the dry run's answer")
	wantErr(t, a, "sync_dead_letters", `{"pipeline": "orders"}`, "the dead letters' answer")
	if out := mustRun(t, a, "sync_test", `{"pipeline": "orders"}`); out != "not json" {
		t.Error(out)
	}
}

// With data.send_records, the records go as they are.
func TestSyncToolsSendRecords(t *testing.T) {
	a, _ := syncAssistant(t, true)
	for _, args := range []string{`{"pipeline": "orders"}`} {
		if out := mustRun(t, a, "sync_dry_run", args); !strings.Contains(out, syncSecretRow) || strings.Contains(out, `"values"`) {
			t.Error(out)
		}
		if out := mustRun(t, a, "sync_dead_letters", args); !strings.Contains(out, syncSecretRow) || strings.Contains(out, `"values"`) {
			t.Error(out)
		}
	}
	// Without the sync role, the Admin API answers 404.
	none := toolsAssistant(t, &config.Config{})
	for _, c := range [][2]string{{"sync_pipelines", `{}`}, {"sync_pipelines", `{"pipeline": "x"}`}, {"sync_test", `{"pipeline": "x"}`},
		{"sync_dry_run", `{"pipeline": "x"}`}, {"sync_dead_letters", `{"pipeline": "x"}`}} {
		wantErr(t, none, c[0], c[1], "answered 404")
	}
	// A member's config is not this fit's: no config with its status.
	a2, _ := syncAssistant(t, false)
	a2.o.Config = func() *config.Config { return &config.Config{} }
	if out := mustRun(t, a2, "sync_pipelines", `{"pipeline": "orders"}`); strings.Contains(out, `"config"`) {
		t.Error(out)
	}
}

func TestHideValues(t *testing.T) {
	for in, want := range map[string]string{
		`nope`:                          `"(not JSON)"`,
		`"text"`:                        `"(string)"`,
		`{"line": "x", "op": "insert"}`: `{"line":"(string)","op":"(string)"}`,
		`{"op": "delete", "key": {"int": 1}, "before": {"map": {"a": null, "b": {"map": {"c": {"bytes": "AA=="}}}}}}`: `{"before":{"map":{"a":null,"b":{"map":{"c":"(bytes)"}}}},"key":"(int)","op":"delete"}`,
		`{"after": {"map": {"x": {"uint": 1}, "y": {"a": 1, "b": 2}}}}`:                                               `{"after":{"map":{"x":"(uint)","y":{"a":"(number)","b":"(number)"}}}}`,
		`{"after": {"map": "odd"}}`: `{"after":{"map":"(string)"}}`,
	} {
		if got := string(hideValues(json.RawMessage(in))); got != want {
			t.Errorf("hideValues(%s) = %s, want %s", in, got, want)
		}
	}
}
