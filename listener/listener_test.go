package listener

import (
	"context"
	"errors"
	"io"
	stdhttp "net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/respond"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
)

// blocker answers once the test lets it, to hold a request in flight.
type blocker struct{}

var (
	blockerEntered = make(chan struct{}, 1)
	blockerRelease = make(chan struct{})
	blockerCleaned atomic.Int32
)

func (blocker) ServeHTTP(c *fibhttp.Context) {
	blockerEntered <- struct{}{}
	<-blockerRelease
	_ = c.Respond(stdhttp.StatusOK, "text/plain", []byte("done\n"))
}

func (blocker) Cleanup() { blockerCleaned.Add(1) }

func init() {
	module.Register(module.Info{ID: module.Handlers + ".test_blocker", New: func() any { return new(blocker) }})
}

// start compiles doc, swaps it into a new Runtime and serves it.
func start(t *testing.T, doc string) (*runtime.Runtime, *Server) {
	t.Helper()
	rt := new(runtime.Runtime)
	swap(t, rt, doc)
	s, err := Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
	})
	return rt, s
}

func swap(t *testing.T, rt *runtime.Runtime, doc string) {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		t.Fatal(err)
	}
	rt.Swap(g)
}

// client speaks HTTP/1.1 when h2c is false, and HTTP/2 with prior knowledge
// otherwise.
func client(h2c bool) *stdhttp.Client {
	tr := &stdhttp.Transport{Protocols: new(stdhttp.Protocols)}
	if h2c {
		tr.Protocols.SetUnencryptedHTTP2(true)
	} else {
		tr.Protocols.SetHTTP1(true)
	}
	return &stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}
}

func get(t *testing.T, c *stdhttp.Client, url, host string) (*stdhttp.Response, string) {
	t.Helper()
	req, err := stdhttp.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

const testRoutes = `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [
    {"match": {"path": ["/hello"]}, "handler": {"module": "respond", "params": {"body": "hello\n"}}},
    {"match": {"host": ["example.com"]}, "priority": -1, "handler": {"module": "respond", "params": {"status": 201, "body": "example\n"}}},
    {"match": {"host": ["*.example.com"], "path": ["/hello"]}, "handler": {"module": "respond", "params": {"status": 202, "body": "wild\n"}}}
  ]
}`

func TestServe(t *testing.T) {
	_, s := start(t, testRoutes)
	addrs := s.Addrs()
	if len(addrs) != 1 || addrs[0].Name != "web" {
		t.Fatalf("addrs: %+v", addrs)
	}
	base := "http://" + addrs[0].Addr.String()
	for _, h2c := range []bool{false, true} {
		proto := map[bool]string{false: "HTTP/1.1", true: "HTTP/2.0"}[h2c]
		c := client(h2c)
		for _, tt := range []struct {
			path, host string
			status     int
			body       string
		}{
			{"/hello", "", 200, "hello\n"},
			{"/other", "example.com", 201, "example\n"},
			{"/hello", "example.com", 200, "hello\n"},
			{"/hello", "www.example.com", 202, "wild\n"},
			{"/other", "", 404, "404 page not found\n"},
		} {
			resp, body := get(t, c, base+tt.path, tt.host)
			if resp.Proto != proto || resp.StatusCode != tt.status || body != tt.body {
				t.Errorf("%s %s%s: %s %d %q", proto, tt.host, tt.path, resp.Proto, resp.StatusCode, body)
			}
		}
	}
}

func TestProtocols(t *testing.T) {
	_, s := start(t, `{
  "listeners": [
    {"name": "h1", "address": "127.0.0.1:0"},
    {"name": "h2c", "address": "127.0.0.1:0", "protocols": ["h2c"]}
  ],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]
}`)
	h1 := "http://" + s.Addrs()[0].Addr.String() + "/"
	h2 := "http://" + s.Addrs()[1].Addr.String() + "/"
	if resp, body := get(t, client(false), h1, ""); resp.Proto != "HTTP/1.1" || body != "ok" {
		t.Errorf("HTTP/1.1 on h1: %s %q", resp.Proto, body)
	}
	if resp, body := get(t, client(true), h2, ""); resp.Proto != "HTTP/2.0" || body != "ok" {
		t.Errorf("h2c on h2c: %s %q", resp.Proto, body)
	}
	if _, err := client(true).Get(h1); err == nil {
		t.Error("h2c on an http/1.1 listener succeeded")
	}
	if _, err := client(false).Get(h2); err == nil {
		t.Error("HTTP/1.1 on an h2c listener succeeded")
	}
}

func TestSwapChangesRoutes(t *testing.T) {
	const doc = `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "%s"}}}]
}`
	rt, s := start(t, strings.Replace(doc, "%s", "one", 1))
	url := "http://" + s.Addrs()[0].Addr.String() + "/"
	c := client(false)
	if _, body := get(t, c, url, ""); body != "one" {
		t.Fatalf("before swap: %q", body)
	}
	swap(t, rt, strings.Replace(doc, "%s", "two", 1))
	if _, body := get(t, c, url, ""); body != "two" {
		t.Fatalf("after swap: %q", body)
	}
}

func TestShutdownWaitsForInFlight(t *testing.T) {
	for _, h2c := range []bool{false, true} {
		t.Run(map[bool]string{false: "http1", true: "h2c"}[h2c], func(t *testing.T) {
			testShutdown(t, h2c)
		})
	}
}

func testShutdown(t *testing.T, h2c bool) {
	rt := new(runtime.Runtime)
	swap(t, rt, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [{"handler": {"module": "test_blocker"}}]
}`)
	s, err := Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + s.Addrs()[0].Addr.String() + "/"
	cleaned := blockerCleaned.Load()

	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := client(h2c).Get(url)
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		got <- result{string(body), err}
	}()
	select {
	case <-blockerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached its handler")
	}

	shutdown := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdown <- s.Shutdown(ctx)
	}()
	select {
	case err := <-shutdown:
		t.Fatalf("Shutdown returned with a request in flight: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	blockerRelease <- struct{}{}
	if r := <-got; r.err != nil || r.body != "done\n" {
		t.Fatalf("in-flight request: %q %v", r.body, r.err)
	}
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return once the request was done")
	}
	if _, err := client(h2c).Get(url); err == nil {
		t.Fatal("a request after Shutdown succeeded")
	}
	if err := s.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Close after Shutdown: %v", err)
	}
	rt.Close()
	if n := blockerCleaned.Load() - cleaned; n != 1 {
		t.Fatalf("the graph's module was cleaned up %d times", n)
	}
}

func TestStartWithoutGraph(t *testing.T) {
	if _, err := Start(new(runtime.Runtime)); err == nil {
		t.Fatal("Start with no graph succeeded")
	}
}

func TestBindFails(t *testing.T) {
	_, s := start(t, `{"listeners": [{"name": "a", "address": "127.0.0.1:0"}]}`)
	rt := new(runtime.Runtime)
	swap(t, rt, `{"listeners": [{"name": "b", "address": "`+s.Addrs()[0].Addr.String()+`"}]}`)
	defer rt.Close()
	if s2, err := Start(rt); err == nil {
		_ = s2.Close()
		t.Fatal("binding an address in use succeeded")
	}
}
