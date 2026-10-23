// Package workers checks M3's first exit criterion: no middleware, nor
// handler, puts a blocking call on a fib worker. Each module serves a
// request whose answer waits on something slow, the handler it wraps, the
// auth service it asks or the upstream it proxies to, and its ServeHTTP must
// return long before that answers: a call that waited would hold the worker
// running it for as long. Every module fit registers needs a case, and each
// has a benchmark.
package workers_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/fcgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
	_ "github.com/lesismal/fit/modules/standard"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// slow is how long the slow part of each case takes to answer, and quick how
// soon ServeHTTP must return: far apart, so that a loaded machine does not
// blur them.
const (
	slow  = 600 * time.Millisecond
	quick = 200 * time.Millisecond
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".test_late", New: func() any { return new(late) }})
}

// late is a handler that answers after Delay, from a timer, as a handler
// waiting on an upstream does.
type late struct {
	Delay config.Duration `json:"delay"`
}

func (l *late) ServeHTTP(c *fibhttp.Context) {
	c.Retain()
	time.AfterFunc(l.Delay.Std(), func() {
		_ = c.Respond(200, "text/plain", []byte(strings.Repeat("late ", 400)))
		c.Release()
	})
}

// env is what the cases need: an auth service and an upstream that answer
// after delay, a static root, a JWT secret and a user's password hash.
type env struct {
	auth, app, fcgi string
	root            string
	secret          string
	token           string
	hash            string
}

func newEnv(t testing.TB, delay time.Duration) *env {
	t.Helper()
	wait := func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		time.Sleep(delay)
		_, _ = io.WriteString(w, "ok")
	}
	auth := httptest.NewServer(stdhttp.HandlerFunc(wait))
	app := httptest.NewServer(stdhttp.HandlerFunc(wait))
	t.Cleanup(auth.Close)
	t.Cleanup(app.Close)
	// A FastCGI application as slow, Go's own.
	fcgiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = fcgi.Serve(fcgiLn, stdhttp.HandlerFunc(wait)) }()
	t.Cleanup(func() { _ = fcgiLn.Close() })
	dir := t.TempDir()
	// The request's path, /api/index.html, which only rewrite strips.
	if err := os.Mkdir(filepath.Join(dir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api", "index.html"), []byte(strings.Repeat("static ", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("a secret of the test's, 32 bytes or more"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return &env{auth: auth.Listener.Addr().String(), app: app.Listener.Addr().String(), fcgi: fcgiLn.Addr().String(), root: dir,
		secret: secret, token: hs256(fmt.Sprintf(`{"sub":"me","exp":%d}`, time.Now().Add(time.Hour).Unix()), "a secret of the test's, 32 bytes or more"), hash: string(hash)}
}

// hs256 signs claims with secret.
func hs256(claims, secret string) string {
	enc := base64.RawURLEncoding
	signed := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc.EncodeToString([]byte(claims))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	return signed + "." + enc.EncodeToString(mac.Sum(nil))
}

// tc is a module's case: the route's middleware and handler, what the request
// carries, and the status it gets. inline says the module answers without
// what is slow, as a redirect does.
type tc struct {
	module     string
	middleware string
	handler    string
	header     map[string]string
	status     int
	inline     bool
}

func cases(e *env) []tc {
	lateHandler := fmt.Sprintf(`{"module": "test_late", "params": {"delay": %q}}`, slow.String())
	mw := func(name, params string) tc {
		return tc{module: name, middleware: `{"module": "` + name + `", "params": ` + params + `}`, handler: lateHandler, status: 200}
	}
	basic := mw("basic_auth", `{"users": [{"username": "me", "password_hash": "`+e.hash+`"}]}`)
	basic.header = map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("me:pw"))}
	jwt := mw("jwt", `{"hmac_secret_file": "`+e.secret+`", "algorithms": ["HS256"]}`)
	jwt.header = map[string]string{"Authorization": "Bearer " + e.token}
	compress := mw("compress", `{}`)
	compress.header = map[string]string{"Accept-Encoding": "gzip"}
	redirect := mw("redirect", `{"to": "/elsewhere"}`)
	redirect.status, redirect.inline = 302, true
	return []tc{
		mw("rewrite", `{"strip_prefix": ["/api"]}`),
		redirect,
		mw("headers", `{"request": {"set": {"X-A": "1"}}, "response": {"set": {"X-B": "2"}}}`),
		mw("rate_limit", `{"rate": 1000000000, "burst": 1000000000}`),
		mw("concurrency_limit", `{"max": 1000000}`),
		mw("body_limit", `{"max_bytes": 1000}`),
		basic,
		mw("client_cert", `{"optional": true}`),
		// The auth service is the slow part, and the handler answers at
		// once.
		{module: "forward_auth", middleware: `{"module": "forward_auth", "params": {"upstream": "auth"}}`,
			handler: `{"module": "respond", "params": {"body": "in"}}`, status: 200},
		jwt,
		mw("circuit_breaker", `{}`),
		compress,
		// The slow handler's response is not kept, so every request goes
		// through to it.
		mw("cache", `{"zone": "workers"}`),
		{module: "proxy", handler: `{"module": "proxy", "params": {"upstream": "app"}}`, status: 200},
		{module: "fastcgi", handler: `{"module": "fastcgi", "params": {"address": "` + e.fcgi + `", "root": "/srv"}}`, status: 200},
		{module: "static", handler: `{"module": "static", "params": {"root": "` + e.root + `"}}`, status: 200, inline: true},
		{module: "respond", handler: `{"module": "respond", "params": {"body": "x"}}`, status: 200, inline: true},
		{module: "test_late", handler: lateHandler, status: 200},
	}
}

// notCovered are the modules this package cannot serve, with where their
// own tests check the same.
var notCovered = map[string]string{
	// A CONNECT needs a connection to switch: fib's
	// TestConnectProxyDoesNotWait checks that its handler returns while
	// the dial is under way.
	"forward_proxy": "fib's TestConnectProxyDoesNotWait",
	// A plugin is built for the test with the go command:
	// plugin/wasm's TestNeverBlocks checks that the call runs on the
	// module's pool and ServeHTTP returns at once.
	"wasm": "plugin/wasm's TestNeverBlocks",
}

// handlerOf compiles a config of c's route and returns its handler.
func handlerOf(t testing.TB, e *env, c tc) module.Handler {
	t.Helper()
	mws := "[]"
	if c.middleware != "" {
		mws = "[" + c.middleware + "]"
	}
	cfg, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": ":0"}],
  "upstreams": [{"name": "auth", "addresses": ["` + e.auth + `"]}, {"name": "app", "addresses": ["` + e.app + `"]}],
  "routes": [{"middleware": ` + mws + `, "handler": ` + c.handler + `}]}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(cfg, runtime.Env{Engine: engine})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	t.Cleanup(func() {
		rt.Close()
		stop()
	})
	return g.Listeners[0].Routes[0].Handler
}

func request(c tc) *stdhttp.Request {
	r := httptest.NewRequest("GET", "/api/index.html", nil)
	for k, v := range c.header {
		r.Header.Set(k, v)
	}
	return r
}

// TestEveryModuleCovered fails for a middleware or handler fit registers
// that has no case here.
func TestEveryModuleCovered(t *testing.T) {
	var have []string
	for _, c := range cases(&env{}) {
		have = append(have, c.module)
	}
	for _, ns := range []string{module.Middlewares, module.Handlers} {
		for _, info := range module.Namespace(ns) {
			if name := info.Name(); !slices.Contains(have, name) && notCovered[name] == "" {
				t.Errorf("%s %q has no case: add one to cases", ns, name)
			}
		}
	}
}

// TestNoModuleBlocks serves each case once its first request has warmed it
// up: ServeHTTP returns within quick, and the answer, unless the module
// gives it itself, comes once the slow part has answered.
func TestNoModuleBlocks(t *testing.T) {
	e := newEnv(t, slow)
	for _, c := range cases(e) {
		t.Run(c.module, func(t *testing.T) {
			h := handlerOf(t, e, c)
			for i := range 2 {
				rec := &fibtest.Recorder{}
				start := time.Now()
				fibhttp.Serve(h, fibhttp.NewStreamContext(nil, request(c), rec))
				returned := time.Since(start)
				if !rec.Wait(10 * time.Second) {
					t.Fatal("no answer")
				}
				answered := time.Since(start)
				if rec.Response.StatusCode != c.status {
					t.Fatalf("status %d, want %d", rec.Response.StatusCode, c.status)
				}
				if i == 0 {
					continue // a first request may load what later ones share
				}
				if returned > quick {
					t.Errorf("ServeHTTP returned after %v, past %v: it waited on a worker", returned, quick)
				}
				if !c.inline && answered < slow*3/4 {
					t.Errorf("answered after %v, before the slow part (%v) could have", answered, slow)
				}
			}
		})
	}
}

// BenchmarkModule serves each case with nothing slow: the auth service and
// the upstream answer at once, and the wrapped handler inline.
func BenchmarkModule(b *testing.B) {
	e := newEnv(b, 0)
	for _, c := range cases(e) {
		if strings.Contains(c.handler, "test_late") {
			c.handler = `{"module": "respond", "params": {"body": "` + strings.Repeat("x", 2000) + `"}}`
		}
		b.Run(c.module, func(b *testing.B) {
			h := handlerOf(b, e, c)
			r := request(c)
			b.ReportAllocs()
			for b.Loop() {
				// basic_auth takes Authorization off the request it passes on.
				for k, v := range c.header {
					r.Header.Set(k, v)
				}
				if rec := fibtest.ServeRequest(h, r); rec.Response.StatusCode != c.status {
					b.Fatalf("status %d", rec.Response.StatusCode)
				}
			}
		})
	}
}
