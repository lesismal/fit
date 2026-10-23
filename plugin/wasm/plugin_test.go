package wasm

import (
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

// The tests' own modules, encoded by encode_test.go: misbehaving plugins a
// compiler would not build.

// trap is a body that traps.
var trap = []byte{opUnreachable}

// trapUnlessRoot traps when the first param is not the root context's id.
var trapUnlessRoot = cat([]byte{opLocalGet, 0}, i32(pwRootContext), []byte{opI32Ne, opIf, blockEmpty, opUnreachable, opEnd})

// fitModule is a plugin of fit's ABI whose fit_on_request returns action,
// and whose _initialize runs init, when it is set.
func fitModule(action int32, init []byte) []byte {
	m := wasmModule{funcs: []wasmFunc{{export: exportOnRequest, results: 1, body: i32(action)}}}
	if init != nil {
		m.funcs = append(m.funcs, wasmFunc{export: "_initialize", body: init})
	}
	return m.encode()
}

// proxyModule is a Proxy-Wasm plugin that does nothing, with bodies for the
// functions bodies names instead, and without those whose body is nil.
func proxyModule(bodies map[string][]byte) []byte {
	m := wasmModule{pages: 1}
	for _, f := range []wasmFunc{
		{export: "proxy_abi_version_0_2_1"},
		{export: pwOnContextCreate, params: 2},
		{export: pwOnRequestHeaders, params: 3, results: 1, body: i32(0)},
		{export: pwOnDone, params: 1, results: 1, body: i32(1)},
		{export: pwOnLog, params: 1},
		{export: pwOnDelete, params: 1},
		{export: pwAllocate, params: 1, results: 1, body: i32(envAlloc)},
		{export: pwOnVMStart, params: 2, results: 1, body: i32(1)},
		{export: pwOnConfigure, params: 2, results: 1, body: i32(1)},
	} {
		if body, ok := bodies[f.export]; ok {
			if body == nil {
				continue
			}
			f.body = body
		}
		m.funcs = append(m.funcs, f)
	}
	return m.encode()
}

// loadCode makes the middleware for the plugin code, with params besides its
// path.
func loadCode(t *testing.T, code []byte, params map[string]any) (*WASM, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.wasm")
	if err := os.WriteFile(path, code, 0o644); err != nil {
		t.Fatal(err)
	}
	if params == nil {
		params = map[string]any{}
	}
	params["path"] = path
	data, _ := json.Marshal(map[string]any{"module": "wasm", "params": params})
	v, err := config.ParseValue(data, "middleware")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(module.Middlewares, ref, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*WASM), nil
}

func TestLoadErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		code []byte
		err  string
	}{
		{"no exports", wasmModule{}.encode(), "exports neither"},
		{"_initialize traps", fitModule(0, trap), "unreachable"},
		{"Proxy-Wasm without proxy_on_done", proxyModule(map[string][]byte{pwOnDone: nil}), "does not export proxy_on_done"},
		{"the root context traps", proxyModule(map[string][]byte{pwOnContextCreate: trap}), "unreachable"},
		{"proxy_on_vm_start traps", proxyModule(map[string][]byte{pwOnVMStart: trap}), "unreachable"},
		{"proxy_on_vm_start refuses", proxyModule(map[string][]byte{pwOnVMStart: i32(0)}), "refused its vm_config"},
		{"proxy_on_configure traps", proxyModule(map[string][]byte{pwOnConfigure: trap}), "unreachable"},
		{"proxy_on_configure refuses", proxyModule(map[string][]byte{pwOnConfigure: i32(0)}), "refused its config"},
	} {
		_, err := loadCode(t, tt.code, map[string]any{"instances": 2})
		if err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.err)
		}
	}
	// Without the optional callbacks, the plugin loads.
	if _, err := loadCode(t, proxyModule(map[string][]byte{pwOnVMStart: nil, pwOnConfigure: nil}), nil); err != nil {
		t.Error(err)
	}
}

// next answers what tells it ran.
var next = fibhttp.HandlerFunc(func(c *fibhttp.Context) {
	_ = c.Respond(200, "text/plain", []byte("next"))
})

func TestCalls(t *testing.T) {
	for _, tt := range []struct {
		name   string
		code   []byte
		params map[string]any
		status int
		body   string
	}{
		{"fit_on_request stops without an answer", fitModule(actionStop, nil), nil, 200, ""},
		{"fit_on_request goes on", fitModule(0, nil), nil, 200, "next"},
		{"waited too long", fitModule(0, nil), map[string]any{"wait_timeout": "1ns"}, 503, "503 Service Unavailable\n"},
		{"Proxy-Wasm goes on", proxyModule(nil), nil, 200, "next"},
		{"Proxy-Wasm pauses without an answer", proxyModule(map[string][]byte{pwOnRequestHeaders: i32(pwActionPause)}), nil, 500, "500 Internal Server Error\n"},
		{"skipped", proxyModule(map[string][]byte{pwOnRequestHeaders: i32(pwActionPause)}), map[string]any{"on_error": "skip"}, 200, "next"},
		{"a request's context traps", proxyModule(map[string][]byte{pwOnContextCreate: trapUnlessRoot}), nil, 500, "500 Internal Server Error\n"},
		{"proxy_on_request_headers traps", proxyModule(map[string][]byte{pwOnRequestHeaders: trap}), nil, 500, "500 Internal Server Error\n"},
		{"proxy_on_done traps", proxyModule(map[string][]byte{pwOnDone: trap}), nil, 500, "500 Internal Server Error\n"},
	} {
		if tt.params == nil {
			tt.params = map[string]any{}
		}
		tt.params["instances"] = 1
		m, err := loadCode(t, tt.code, tt.params)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		h := m.Wrap(next)
		for range 2 { // and again, on the instance made again
			resp := fibtest.ServeRequest(h, httptest.NewRequest("GET", "http://example.com/", nil)).Response
			if resp.StatusCode != tt.status || string(resp.Body) != tt.body {
				t.Errorf("%s: %d %q, want %d %q", tt.name, resp.StatusCode, resp.Body, tt.status, tt.body)
			}
		}
	}
}

// nopHolder is a hold on no config.
type nopHolder struct{}

func (nopHolder) Retain()  {}
func (nopHolder) Release() {}

// serveOn runs w.serve for a request, as the pool would, and returns what it
// answered.
func serveOn(w *worker) *fibtest.Recorder {
	rec := &fibtest.Recorder{}
	fibhttp.Serve(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		c.Retain()
		w.serve(&call{m: w.p.m, c: c, next: next, queued: time.Now()})
	}), fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "http://example.com/", nil), rec))
	rec.Wait(10 * time.Second)
	return rec
}

// newWorker makes a worker, outside a pool's goroutines, for code.
func newWorker(t *testing.T, code []byte) *worker {
	t.Helper()
	m := &WASM{Path: "test.wasm", Timeout: config.Duration(time.Minute), WaitTimeout: config.Duration(time.Minute), graph: nopHolder{}}
	var err error
	if m.plugin, err = loadPlugin(code, 1<<20); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.plugin.close)
	return &worker{p: &pool{m: m}}
}

// TestInstanceLost checks a request on a worker whose instance could not be
// made again: it fails.
func TestInstanceLost(t *testing.T) {
	w := newWorker(t, fitModule(0, trap))
	if rec := serveOn(w); rec.Response.StatusCode != 500 {
		t.Errorf("%d %q", rec.Response.StatusCode, rec.Response.Body)
	}
	if w.mod != nil {
		t.Error("an instance kept")
	}
}

// TestContextIDs checks that the ids of a Proxy-Wasm instance's request
// contexts wrap around past the root's.
func TestContextIDs(t *testing.T) {
	w := newWorker(t, proxyModule(nil))
	if err := w.instantiate(); err != nil {
		t.Fatal(err)
	}
	defer w.close()
	w.pw.nextID = math.MaxUint32
	if rec := serveOn(w); rec.Response.StatusCode != 200 || string(rec.Response.Body) != "next" {
		t.Errorf("%d %q", rec.Response.StatusCode, rec.Response.Body)
	}
	if w.pw.nextID != pwRootContext+1 {
		t.Errorf("next id %d", w.pw.nextID)
	}
}

// TestClientGone checks that a request whose client left while it waited
// is dropped, without a call.
func TestClientGone(t *testing.T) {
	w := newWorker(t, fitModule(0, nil))
	sr := &fibhttp.StreamRequest{Request: *httptest.NewRequest("GET", "http://example.com/", nil)}
	rec := &fibtest.Recorder{}
	c := sr.Context(nil, rec, nil)
	fibhttp.Serve(fibhttp.HandlerFunc(func(c *fibhttp.Context) { c.Retain() }), c)
	sr.Cancel(errors.New("gone"))
	deadline := time.Now().Add(10 * time.Second)
	for c.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the request was not cancelled")
		}
		time.Sleep(time.Millisecond)
	}
	w.serve(&call{m: w.p.m, c: c, next: next, queued: time.Now()})
	if rec.Written || w.mod != nil {
		t.Errorf("served: %v %d, instance %v", rec.Written, rec.Response.StatusCode, w.mod)
	}
}

// TestCloseQueued checks that the calls still queued as the pool closes are
// answered 503.
func TestCloseQueued(t *testing.T) {
	m := &WASM{graph: nopHolder{}}
	p := &pool{m: m, calls: make(chan *call, 1), stop: make(chan struct{})}
	rec := &fibtest.Recorder{}
	fibhttp.Serve(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		c.Retain()
		if !p.submit(&call{m: m, c: c, next: next, queued: time.Now()}) {
			t.Error("not queued")
		}
	}), fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "http://example.com/", nil), rec))
	p.close()
	if !rec.Wait(10*time.Second) || rec.Response.StatusCode != 503 {
		t.Errorf("%v %d", rec.Written, rec.Response.StatusCode)
	}
}
