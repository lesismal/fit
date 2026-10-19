package upstream

import (
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
)

// startEngine runs a fib engine with no listener, for the pool's client.
func startEngine(t *testing.T) *fib.Engine {
	t.Helper()
	engine, err := fib.NewEngine(fib.DefaultConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run() }()
	t.Cleanup(func() {
		engine.Stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
		_ = engine.Close()
	})
	return engine
}

func decode(t *testing.T, doc string) (*Config, error) {
	t.Helper()
	v, err := config.ParseValue([]byte(doc), "upstream")
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := v.Decode(&c); err != nil {
		return nil, err
	}
	return &c, c.Check()
}

// newPool decodes doc, with %s standing for the backends' addresses, and
// returns its pool.
func newPool(t *testing.T, doc string, backends ...*backend) *Pool {
	t.Helper()
	addrs := make([]string, len(backends))
	for i, b := range backends {
		addrs[i] = `"` + b.addr + `"`
	}
	c, err := decode(t, strings.Replace(doc, "%s", strings.Join(addrs, ", "), 1))
	if err != nil {
		t.Fatal(err)
	}
	p := New(startEngine(t), c)
	t.Cleanup(p.Close)
	return p
}

// backend is a test server that counts the connections it sees.
type backend struct {
	*httptest.Server
	addr    string
	opened  atomic.Int64
	open    atomic.Int64
	maxOpen atomic.Int64
	closed  chan struct{} // gets a value as each connection closes
}

func newBackend(t *testing.T, h stdhttp.HandlerFunc) *backend {
	t.Helper()
	b := &backend{closed: make(chan struct{}, 64)}
	b.Server = httptest.NewUnstartedServer(h)
	b.Config.ConnState = func(_ net.Conn, state stdhttp.ConnState) {
		switch state {
		case stdhttp.StateNew:
			b.opened.Add(1)
			n := b.open.Add(1)
			for {
				m := b.maxOpen.Load()
				if n <= m || b.maxOpen.CompareAndSwap(m, n) {
					break
				}
			}
		case stdhttp.StateClosed, stdhttp.StateHijacked:
			b.open.Add(-1)
			b.closed <- struct{}{}
		}
	}
	b.Start()
	t.Cleanup(b.Close)
	b.addr = b.Listener.Addr().String()
	return b
}

func hello(name string) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_, _ = io.WriteString(w, name+" "+r.Method+" "+r.URL.RequestURI()+" "+r.Host)
	}
}

// result is what a request's callback was given.
type result struct {
	status int
	body   string
	err    error
}

// do sends a request through p and waits for its callback.
func do(t *testing.T, p *Pool, target string) result {
	t.Helper()
	return <-goDo(t, p, target)
}

// goDo sends a request through p and returns where its result will arrive.
func goDo(t *testing.T, p *Pool, target string) <-chan result {
	t.Helper()
	req, err := stdhttp.NewRequest("GET", "http://placeholder"+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "example.com"
	ch := make(chan result, 1)
	p.Do(req, func(resp *fibhttp.ClientResponse, err error) {
		if err != nil {
			ch <- result{err: err}
			return
		}
		body, err := io.ReadAll(resp.Body)
		ch <- result{status: resp.StatusCode, body: string(body), err: err}
	})
	return ch
}

func TestReuse(t *testing.T) {
	b := newBackend(t, hello("a"))
	p := newPool(t, `{"addresses": [%s]}`, b)
	for i := range 20 {
		got := do(t, p, "/x?i=1")
		if got.err != nil || got.status != 200 || got.body != "a GET /x?i=1 example.com" {
			t.Fatalf("request %d: %+v", i, got)
		}
	}
	if n := b.opened.Load(); n != 1 {
		t.Fatalf("20 requests in turn opened %d connections, want 1", n)
	}
}

func TestNoIdle(t *testing.T) {
	b := newBackend(t, hello("a"))
	p := newPool(t, `{"addresses": [%s], "max_idle_conns": 0}`, b)
	for range 3 {
		if got := do(t, p, "/"); got.err != nil || got.status != 200 {
			t.Fatal(got)
		}
	}
	if n := b.opened.Load(); n != 3 {
		t.Fatalf("3 requests with no idle connections kept opened %d connections, want 3", n)
	}
}

func TestMaxConns(t *testing.T) {
	var inFlight, maxInFlight atomic.Int64
	release := make(chan struct{})
	var arrived sync.WaitGroup
	arrived.Add(2)
	b := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		if r.URL.Path == "/first" {
			arrived.Done()
		}
		<-release
		_, _ = io.WriteString(w, "ok")
	})
	p := newPool(t, `{"addresses": [%s], "max_conns": 2}`, b)
	var results []<-chan result
	for range 2 {
		results = append(results, goDo(t, p, "/first"))
	}
	arrived.Wait()
	for range 6 {
		results = append(results, goDo(t, p, "/later"))
	}
	// The six later requests wait for one of the two connections.
	time.Sleep(100 * time.Millisecond)
	if n := b.opened.Load(); n != 2 {
		t.Fatalf("with max_conns 2, %d connections opened", n)
	}
	close(release)
	for i, ch := range results {
		if got := <-ch; got.err != nil || got.status != 200 || got.body != "ok" {
			t.Fatalf("request %d: %+v", i, got)
		}
	}
	if n, m := b.maxOpen.Load(), maxInFlight.Load(); n != 2 || m != 2 {
		t.Fatalf("at most %d connections open and %d requests in flight, want 2 and 2", n, m)
	}
}

func TestIdleEviction(t *testing.T) {
	b := newBackend(t, hello("a"))
	p := newPool(t, `{"addresses": [%s], "idle_timeout": "100ms"}`, b)
	if got := do(t, p, "/"); got.err != nil {
		t.Fatal(got.err)
	}
	start := time.Now()
	select {
	case <-b.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle connection was not closed")
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("the idle connection closed after %v, before its timeout", elapsed)
	}
	if got := do(t, p, "/"); got.err != nil || got.status != 200 {
		t.Fatal(got)
	}
	if n := b.opened.Load(); n != 2 {
		t.Fatalf("a request after the eviction opened %d connections in all, want 2", n)
	}
}

func TestClose(t *testing.T) {
	b := newBackend(t, hello("a"))
	p := newPool(t, `{"addresses": [%s]}`, b)
	if got := do(t, p, "/"); got.err != nil {
		t.Fatal(got.err)
	}
	p.Close()
	select {
	case <-b.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close left the idle connection open")
	}
	if got := do(t, p, "/"); got.err == nil {
		t.Fatal("a closed pool sent a request")
	}
}

func TestRoundRobin(t *testing.T) {
	a, b := newBackend(t, hello("a")), newBackend(t, hello("b"))
	p := newPool(t, `{"addresses": [%s]}`, a, b)
	counts := map[string]int{}
	for range 10 {
		got := do(t, p, "/")
		if got.err != nil {
			t.Fatal(got.err)
		}
		counts[got.body[:1]]++
	}
	if counts["a"] != 5 || counts["b"] != 5 {
		t.Fatalf("10 requests over two addresses went %v", counts)
	}
	if a.opened.Load() != 1 || b.opened.Load() != 1 {
		t.Fatalf("opened %d and %d connections, want 1 and 1", a.opened.Load(), b.opened.Load())
	}
}

func TestConnectFailure(t *testing.T) {
	// A port nothing listens on: the connect is refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c, err := decode(t, `{"addresses": ["`+addr+`"], "connect_timeout": "1s"}`)
	if err != nil {
		t.Fatal(err)
	}
	p := New(startEngine(t), c)
	defer p.Close()
	select {
	case got := <-goDo(t, p, "/"):
		if got.err == nil {
			t.Fatalf("a refused connect gave %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a refused connect gave no answer")
	}
}

func TestConnectTimeout(t *testing.T) {
	// An address in TEST-NET-1 (RFC 5737), which nothing answers: the connect
	// either times out or is refused at once by a host with no route to it.
	c, err := decode(t, `{"addresses": ["192.0.2.1:81"], "connect_timeout": "200ms"}`)
	if err != nil {
		t.Fatal(err)
	}
	p := New(startEngine(t), c)
	defer p.Close()
	start := time.Now()
	select {
	case got := <-goDo(t, p, "/"):
		if got.err == nil {
			t.Fatalf("got %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connect_timeout did not end the connect")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the connect failed after %v, with a timeout of 200ms", elapsed)
	}
}

func TestDefaults(t *testing.T) {
	c, err := decode(t, `{"addresses": ["127.0.0.1:1"]}`)
	if err != nil {
		t.Fatal(err)
	}
	p := New(startEngine(t), c)
	defer p.Close()
	if got := p.Addresses(); len(got) != 1 || got[0] != "127.0.0.1:1" {
		t.Fatalf("addresses %v", got)
	}
}

func TestConfigErrors(t *testing.T) {
	tests := []struct {
		doc  string
		want string
	}{
		{`{}`, `1:1: upstream.addresses: missing required field`},
		{`{"addresses": []}`, `1:15: upstream.addresses: an upstream needs at least one address`},
		{`{"addresses": ["http://a:80"]}`, `1:16: upstream.addresses[0]: http://a:80 is a URL; write the host and port alone, as in "10.0.0.1:8080"`},
		{`{"addresses": ["a"]}`, `1:16: upstream.addresses[0]: "a" is not a host and port, as in "10.0.0.1:8080"`},
		{`{"addresses": [":80"]}`, `1:16: upstream.addresses[0]: ":80" has no host`},
		{`{"addresses": ["a:0", "a:x", "a:70000"]}`, "1:16: upstream.addresses[0]: \"a:0\" has no valid port\n" +
			"1:23: upstream.addresses[1]: \"a:x\" has no valid port\n" +
			"1:30: upstream.addresses[2]: \"a:70000\" has no valid port"},
		{`{"addresses": ["a:1", "a:1"]}`, `1:23: upstream.addresses[1]: "a:1" is listed twice`},
		{`{"addresses": ["a:1"], "max_conns": -1}`, `1:37: upstream.max_conns: -1 is less than the minimum 0`},
		{`{"addresses": ["a:1"], "idle_timeout": "soon"}`, `1:40: upstream.idle_timeout: "soon" is not a duration, such as "10s" or "1m30s"`},
		{`{"addresses": ["a:1"], "connect_timeout": 5}`, `1:43: upstream.connect_timeout: expected a string, got a number`},
	}
	for _, tt := range tests {
		_, err := decode(t, tt.doc)
		if err == nil || err.Error() != tt.want {
			t.Errorf("%s:\n got %v\nwant %s", tt.doc, err, tt.want)
		}
	}
	for _, ok := range []string{`{"addresses": ["[::1]:8080", "backend:80", "10.0.0.1:65535"], "max_idle_conns": 0}`} {
		if _, err := decode(t, ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}
