package upstream

import (
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	engine, err := fib.New(fib.DefaultConfig())
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

// decode loads a config whose one upstream is doc, without its name.
func decode(t *testing.T, doc string) (*config.Upstream, error) {
	t.Helper()
	c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": ":0"}], "upstreams": [{"name": "u", ` + doc[1:] + `]}`))
	if err != nil {
		return nil, err
	}
	return &c.Upstreams[0], nil
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
	p := mustNew(t, startEngine(t), c)
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
			// Never blocks: a hook that waited would stall the server's
			// connections once more closed than the channel holds.
			select {
			case b.closed <- struct{}{}:
			default:
			}
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

// releaser returns a channel for handlers to wait on and the func that
// closes it, which may be called more than once. A test defers the func, so
// that one that fails while handlers wait does not leave httptest's Close,
// in the cleanup, waiting for them for good.
func releaser() (<-chan struct{}, func()) {
	ch := make(chan struct{})
	var once sync.Once
	return ch, func() { once.Do(func() { close(ch) }) }
}

// settle waits until b has n requests under way. A request's callback runs
// before Pool.Do calls Done, so do can return while the request still counts
// on its backend.
func settle(t *testing.T, b *Backend, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for b.Active() != n {
		if time.Now().After(deadline) {
			t.Fatalf("%s has %d requests under way, want %d", b.Address, b.Active(), n)
		}
		time.Sleep(time.Millisecond)
	}
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
	release, unblock := releaser()
	defer unblock()
	arrived := make(chan struct{}, 2)
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
			arrived <- struct{}{}
		}
		<-release
		_, _ = io.WriteString(w, "ok")
	})
	p := newPool(t, `{"addresses": [%s], "max_conns": 2}`, b)
	var results []<-chan result
	for range 2 {
		results = append(results, goDo(t, p, "/first"))
	}
	for range 2 {
		select {
		case <-arrived:
		case got := <-results[0]:
			t.Fatalf("a first request ended before it was let go: %+v", got)
		case got := <-results[1]:
			t.Fatalf("a first request ended before it was let go: %+v", got)
		case <-time.After(10 * time.Second):
			t.Fatal("the two first requests did not reach the backend")
		}
	}
	for range 6 {
		results = append(results, goDo(t, p, "/later"))
	}
	// The six later requests wait for one of the two connections.
	time.Sleep(100 * time.Millisecond)
	if n := b.opened.Load(); n != 2 {
		t.Fatalf("with max_conns 2, %d connections opened", n)
	}
	unblock()
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
	p := mustNew(t, startEngine(t), c)
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
	p := mustNew(t, startEngine(t), c)
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
	p := mustNew(t, startEngine(t), c)
	defer p.Close()
	if got := p.Addresses(); len(got) != 1 || got[0] != "127.0.0.1:1" {
		t.Fatalf("addresses %v", got)
	}
}

func TestCloseDrains(t *testing.T) {
	release, unblock := releaser()
	defer unblock()
	arrived := make(chan struct{})
	b := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path == "/slow" {
			close(arrived)
			<-release
		}
		_, _ = io.WriteString(w, "ok")
	})
	p := newPool(t, `{"addresses": [%s]}`, b)
	slow := goDo(t, p, "/slow")
	<-arrived
	p.Close()
	// Requests still go while one is in flight: one that started on the
	// pool's graph may yet need it.
	if got := do(t, p, "/"); got.err != nil || got.status != 200 {
		t.Fatalf("a request while draining: %+v", got)
	}
	unblock()
	if got := <-slow; got.err != nil || got.body != "ok" {
		t.Fatalf("the request in flight at Close: %+v", got)
	}
	timeout := time.After(5 * time.Second)
	for b.open.Load() > 0 {
		select {
		case <-b.closed:
		case <-timeout:
			t.Fatal("the connections stayed open after draining")
		}
	}
	if got := do(t, p, "/"); got.err == nil {
		t.Fatal("a drained pool sent a request")
	}
}

func TestNoEngine(t *testing.T) {
	c, err := decode(t, `{"addresses": ["127.0.0.1:1"]}`)
	if err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, nil, c)
	defer p.Close()
	if got := do(t, p, "/"); got.err != ErrNoEngine {
		t.Fatalf("got %+v", got)
	}
	if p.Name() != "u" || len(p.Addresses()) != 1 {
		t.Fatalf("name %q, addresses %v", p.Name(), p.Addresses())
	}
}

func TestPolicies(t *testing.T) {
	a, b := newBackend(t, hello("a")), newBackend(t, hello("b"))
	p := newPool(t, `{"addresses": [%s], "weights": [3, 1]}`, a, b)
	counts := map[string]int{}
	for range 40 {
		got := do(t, p, "/")
		if got.err != nil {
			t.Fatal(got.err)
		}
		counts[got.body[:1]]++
	}
	if counts["a"] != 30 || counts["b"] != 10 {
		t.Fatalf("weights 3 and 1 over 40 requests: %v", counts)
	}
	for _, bk := range p.Backends() {
		settle(t, bk, 0)
	}

	// least_conn sends new requests away from an address that holds some.
	release, unblock := releaser()
	defer unblock()
	slow := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		<-release
		_, _ = io.WriteString(w, "s")
	})
	fast := newBackend(t, hello("f"))
	p = newPool(t, `{"addresses": [%s], "load_balancing": "least_conn"}`, slow, fast)
	held := []<-chan result{goDo(t, p, "/"), goDo(t, p, "/")} // one to each, both idle
	deadline := time.Now().Add(5 * time.Second)
	for p.Backends()[0].Active() != 1 || p.Backends()[1].Active() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("under way: %d and %d", p.Backends()[0].Active(), p.Backends()[1].Active())
		}
		time.Sleep(5 * time.Millisecond)
	}
	for range 20 {
		// The last request to the fast address may count on it still,
		// which would tie the two.
		settle(t, p.Backends()[1], 0)
		if got := do(t, p, "/"); got.err != nil || got.body[:1] != "f" {
			t.Fatalf("with the slow address busy: %+v", got)
		}
	}
	unblock()
	for _, ch := range held {
		<-ch
	}
}

func mustNew(t *testing.T, engine *fib.Engine, c *config.Upstream) *Pool {
	t.Helper()
	p, err := New(engine, c)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTLSCAFileErrors(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		file, want string
	}{
		{filepath.Join(t.TempDir(), "missing.pem"), "no such file or directory"},
		{empty, "holds no PEM certificate"},
	} {
		c, err := decode(t, `{"addresses": ["127.0.0.1:1"], "protocol": "h2", "tls_ca_file": "`+tt.file+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = New(nil, c)
		if err == nil || !strings.Contains(err.Error(), "upstreams[0].tls_ca_file: ") || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", tt.file, err)
		}
	}
}

// TestAcquire: a pool held twice stays open until both holds are given
// back.
func TestAcquire(t *testing.T) {
	b := newBackend(t, hello("a"))
	c, err := decode(t, `{"addresses": ["`+b.addr+`"]}`)
	if err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, startEngine(t), c)
	p.Acquire()
	p.Close()
	if r := do(t, p, "/"); r.err != nil || r.body[:1] != "a" {
		t.Fatalf("after one Close of two holds: %+v", r)
	}
	p.Close()
	if r := do(t, p, "/"); r.err == nil {
		t.Fatal("served after the last hold was given back")
	}
	p.Close() // more than held: nothing more happens
}
