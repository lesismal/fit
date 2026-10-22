package wasm

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

var (
	filtersMu sync.Mutex
	filters   = map[string]string{}
)

// filterPath builds the Proxy-Wasm filter testdata/proxywasm/name, an
// example of the Proxy-Wasm Go SDK, unchanged, once, and returns where.
func filterPath(t testing.TB, name string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command to build the filter with")
	}
	filtersMu.Lock()
	defer filtersMu.Unlock()
	if p, ok := filters[name]; ok {
		return p
	}
	dir, err := os.MkdirTemp("", "fit-proxywasm-")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, name+".wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, "./"+name)
	cmd.Dir = filepath.Join("testdata", "proxywasm")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=-mod=mod")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", name, err, b)
	}
	filters[name] = out
	return out
}

// loadFilter makes the middleware for filter name, with params besides its
// path.
func loadFilter(t testing.TB, name string, params map[string]any) *WASM {
	t.Helper()
	params["path"] = filterPath(t, name)
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
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*WASM)
}

// host answers with the request's host.
var host = fibhttp.HandlerFunc(func(c *fibhttp.Context) {
	_ = c.Respond(200, "text/plain", []byte(c.Request.Host))
})

// TestProxyWasmRouting runs the SDK's http_routing example, which reads its
// plugin configuration and routes a request to the canary by changing its
// :authority when a die, which the configuration's first byte overrides,
// comes up even.
func TestProxyWasmRouting(t *testing.T) {
	for config, want := range map[string]string{"2": "example.com-canary", "1": "example.com"} {
		m := loadFilter(t, "http_routing", map[string]any{"config": config})
		if !m.plugin.proxyWasm {
			t.Fatal("not taken for a Proxy-Wasm filter")
		}
		h := m.Wrap(host)
		for range 3 {
			resp := fibtest.ServeRequest(h, httptest.NewRequest("GET", "http://example.com/x", nil)).Response
			if resp.StatusCode != 200 || string(resp.Body) != want {
				t.Fatalf("config %q: %d %q, want %q", config, resp.StatusCode, resp.Body, want)
			}
		}
	}
}

// TestProxyWasmProperties runs the SDK's properties example, which answers
// 401 to a request without the field a route's property names.
func TestProxyWasmProperties(t *testing.T) {
	m := loadFilter(t, "properties", map[string]any{"properties": []map[string]any{
		{"path": []string{"route_metadata", "filter_metadata", "envoy.filters.http.wasm", "auth"}, "value": "x-auth"},
	}})
	h := m.Wrap(host)
	if resp := fibtest.ServeRequest(h, httptest.NewRequest("GET", "http://example.com/x", nil)).Response; resp.StatusCode != 401 {
		t.Errorf("without the field: %d %q", resp.StatusCode, resp.Body)
	}
	r := httptest.NewRequest("GET", "http://example.com/x", nil)
	r.Header.Set("X-Auth", "yes")
	if resp := fibtest.ServeRequest(h, r).Response; resp.StatusCode != 200 {
		t.Errorf("with it: %d %q", resp.StatusCode, resp.Body)
	}
	// Without the property, every request goes on.
	h = loadFilter(t, "properties", map[string]any{}).Wrap(host)
	if resp := fibtest.ServeRequest(h, httptest.NewRequest("GET", "http://example.com/x", nil)).Response; resp.StatusCode != 200 {
		t.Errorf("no property: %d", resp.StatusCode)
	}
}

func TestPairs(t *testing.T) {
	pairs := [][2]string{{":path", "/a"}, {"x-a", "1"}, {"x-b", ""}}
	got, ok := deserializePairs(serializePairs(pairs))
	if !ok || !slices.Equal(got, pairs) {
		t.Errorf("%v %v", got, ok)
	}
	for _, bad := range [][]byte{{1, 0, 0}, {1, 0, 0, 0, 5, 0, 0, 0, 1, 0, 0, 0}, {0xff, 0xff, 0, 0}} {
		if _, ok := deserializePairs(bad); ok {
			t.Errorf("%v decoded", bad)
		}
	}
}

// BenchmarkProxyWasm is a request through the SDK's http_routing example,
// whose die is overridden to leave the request as it is.
func BenchmarkProxyWasm(b *testing.B) {
	h := loadFilter(b, "http_routing", map[string]any{"config": "1"}).Wrap(host)
	r := httptest.NewRequest("GET", "http://example.com/a", nil)
	b.ReportAllocs()
	for b.Loop() {
		if rec := fibtest.ServeRequest(h, r); rec.Response.StatusCode != 200 {
			b.Fatal(rec.Response.StatusCode)
		}
	}
}
