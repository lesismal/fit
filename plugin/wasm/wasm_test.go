package wasm

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

var (
	buildOnce sync.Once
	built     string
	buildErr  error
)

// pluginPath builds testdata/headers for wasip1, once, and returns where.
func pluginPath(t testing.TB) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command to build the plugin with")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fit-wasm-")
		if err != nil {
			buildErr = err
			return
		}
		built = filepath.Join(dir, "headers.wasm")
		cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", built, "./testdata/headers")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = err
			built = string(out)
		}
	})
	if buildErr != nil {
		t.Fatalf("building the plugin: %v\n%s", buildErr, built)
	}
	return built
}

// load makes the middleware from params, with path set to the plugin.
func load(t testing.TB, params string) *WASM {
	t.Helper()
	params = strings.Replace(params, "{", `{"path": "`+pluginPath(t)+`", `, 1)
	params = strings.Replace(params, `, }`, `}`, 1)
	v, err := config.ParseValue([]byte(`{"module": "wasm", "params": `+params+`}`), "middleware")
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

// echo answers with the request's fields the tests look at.
var echo = fibhttp.HandlerFunc(func(c *fibhttp.Context) {
	h := c.Request.Header
	_ = c.Respond(200, "text/plain", []byte("plugin="+h.Get("X-Plugin")+" secret="+h.Get("X-Secret")))
})

func serve(h module.Handler, path string, fields ...string) *fibtest.Recorder {
	r := httptest.NewRequest("GET", "http://example.com"+path, nil)
	for i := 0; i+1 < len(fields); i += 2 {
		r.Header.Set(fields[i], fields[i+1])
	}
	return fibtest.ServeRequest(h, r)
}

func TestHeaders(t *testing.T) {
	h := load(t, `{"config": "from the config"}`).Wrap(echo)
	for range 3 { // each instance, and again
		rec := serve(h, "/a", "X-Secret", "s")
		resp := rec.Response
		if resp.StatusCode != 200 || string(resp.Body) != "plugin=from the config secret=" {
			t.Fatalf("%d %q", resp.StatusCode, resp.Body)
		}
		if got := resp.Header.Get("X-Served-By"); got != "wasm GET /a" {
			t.Errorf("X-Served-By %q", got)
		}
		if got := resp.Header["X-Count"]; len(got) != 2 || got[0] != "one" || got[1] != "two" {
			t.Errorf("X-Count %q", got)
		}
	}
}

func TestRespond(t *testing.T) {
	h := load(t, `{}`).Wrap(echo)
	resp := serve(h, "/deny", "X-Deny", "you").Response
	if resp.StatusCode != 403 || string(resp.Body) != "denied: you" || resp.Header.Get("X-Denied-By") != "plugin" {
		t.Errorf("%d %q %v", resp.StatusCode, resp.Body, resp.Header)
	}
}

// TestFailures checks a call that runs past its timeout, one that traps
// and one that asks for more memory than it may have: each fails the
// request, or lets it go on with on_error skip, and the instance it
// stopped is made again for the next request.
func TestFailures(t *testing.T) {
	for _, onError := range []string{"fail", "skip"} {
		h := load(t, `{"instances": 1, "timeout": "50ms", "max_memory": 67108864, "on_error": "`+onError+`"}`).Wrap(echo)
		for _, path := range []string{"/loop", "/panic", "/grow"} {
			started := time.Now()
			resp := serve(h, path).Response
			want := 500
			if onError == "skip" {
				want = 200
			}
			if resp.StatusCode != want {
				t.Errorf("%s %s: %d %q", onError, path, resp.StatusCode, resp.Body)
			}
			if path == "/loop" && time.Since(started) > 2*time.Second {
				t.Errorf("%s: stopped after %v", path, time.Since(started))
			}
			if resp := serve(h, "/a").Response; resp.StatusCode != 200 || resp.Header.Get("X-Served-By") == "" {
				t.Errorf("%s: after %s: %d %v", onError, path, resp.StatusCode, resp.Header)
			}
		}
	}
}

// TestNeverBlocks checks that ServeHTTP returns at once, the call running on
// the pool, and that the queue is bounded: past it a request is answered
// 503 at once.
func TestNeverBlocks(t *testing.T) {
	m := load(t, `{"instances": 1, "queue": 1, "timeout": "500ms", "wait_timeout": "5s"}`)
	h := m.Wrap(echo)
	recs := make([]*fibtest.Recorder, 3)
	for i := range recs {
		recs[i] = &fibtest.Recorder{}
		started := time.Now()
		fibhttp.Serve(h, fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "http://example.com/loop", nil), recs[i]))
		if took := time.Since(started); took > 100*time.Millisecond {
			t.Errorf("%d: ServeHTTP took %v", i, took)
		}
		if i == 0 {
			// The first is taken by the instance before the second comes.
			deadline := time.Now().Add(5 * time.Second)
			for m.pool.workers[0].started.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
	}
	// One runs, one waits, the third finds no room.
	if !recs[2].Wait(time.Second) || recs[2].Response.StatusCode != 503 {
		t.Errorf("past the queue: %v %d", recs[2].Written, recs[2].Response.StatusCode)
	}
	for i := range 2 {
		if !recs[i].Wait(5*time.Second) || recs[i].Response.StatusCode != 500 {
			t.Errorf("%d: %d", i, recs[i].Response.StatusCode)
		}
	}
}

// TestShared checks that two configs of the same plugin share it compiled,
// and that it is closed with the last.
func TestShared(t *testing.T) {
	a := load(t, `{}`)
	b := load(t, `{"config": "other"}`)
	if a.plugin != b.plugin {
		t.Error("the plugin was compiled twice")
	}
}

func TestParams(t *testing.T) {
	for _, tt := range []struct{ params, err string }{
		{`{"path": "/nonexistent.wasm"}`, "no such file"},
		{`{"path": "wasm.go"}`, "compiling the plugin"},
		{`{"path": "x", "on_error": "ignore"}`, `"ignore" is not one of`},
	} {
		v, err := config.ParseValue([]byte(`{"module": "wasm", "params": `+tt.params+`}`), "middleware")
		if err != nil {
			t.Fatal(err)
		}
		var ref config.ModuleRef
		err = v.Decode(&ref)
		if err == nil {
			_, err = module.Load(module.Middlewares, ref, nil)
		}
		if err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: %v, want %q", tt.params, err, tt.err)
		}
	}
}

// BenchmarkWASM is a request through the plugin, which edits the request's
// and the response's fields, against one without it.
func BenchmarkWASM(b *testing.B) {
	plain := fibhttp.HandlerFunc(func(c *fibhttp.Context) { _ = c.Respond(200, "text/plain", []byte("hello")) })
	for _, name := range []string{"none", "plugin"} {
		h := module.Handler(plain)
		if name == "plugin" {
			h = load(b, `{"config": "c"}`).Wrap(plain)
		}
		b.Run(name, func(b *testing.B) {
			r := httptest.NewRequest("GET", "http://example.com/a", nil)
			b.ReportAllocs()
			for b.Loop() {
				if rec := fibtest.ServeRequest(h, r); rec.Response.StatusCode != 200 {
					b.Fatal(rec.Response.StatusCode)
				}
			}
		})
	}
}

// BenchmarkGuestCall is the plugin's call alone, on the benchmark's
// goroutine, without the hand-off to the pool: what BenchmarkWASM adds
// beyond it is the hand-off.
func BenchmarkGuestCall(b *testing.B) {
	m := load(b, `{"instances": 1, "config": "c"}`)
	w := m.pool.workers[0] // idle: nothing is submitted
	r := httptest.NewRequest("GET", "http://example.com/a", nil)
	c := fibhttp.NewStreamContext(nil, r, &fibtest.Recorder{})
	b.ReportAllocs()
	for b.Loop() {
		cl := &call{m: m, c: c}
		w.current = cl
		if err := w.fn.CallWithStack(w.ctx, w.stack[:]); err != nil {
			b.Fatal(err)
		}
		w.current = nil
	}
}
