package runtime

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/respond"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

// probe is a handler that counts its cleanups and fails a test that serves
// through it after it was cleaned up.
type probe struct {
	ID      string `json:"id"`
	cleaned atomic.Int32
	served  atomic.Int64
}

func (p *probe) ServeHTTP(c *fibhttp.Context) {
	p.served.Add(1)
	_ = c.WriteResponse(fibhttp.Response{Body: []byte(p.ID)})
}

func (p *probe) Cleanup() { p.cleaned.Add(1) }

// probes records every probe provisioned, so tests can check their cleanups.
var probes sync.Map // *probe → struct{}

func (p *probe) Provision(*module.Context) error {
	probes.Store(p, struct{}{})
	return nil
}

// tag is a middleware that adds its name to the response body, to show the
// order middleware runs in.
type tag struct {
	Name string `json:"name"`
	Fail bool   `json:"fail"`
}

func (t *tag) Provision(*module.Context) error {
	if t.Fail {
		return fmt.Errorf("tag %s failed", t.Name)
	}
	return nil
}

func (t *tag) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		c.Request.Header.Add("X-Trace", t.Name)
		next.ServeHTTP(c)
	})
}

// trace is a handler that answers with the X-Trace values the middleware
// added.
type trace struct{}

func (trace) ServeHTTP(c *fibhttp.Context) {
	_ = c.WriteResponse(fibhttp.Response{Body: []byte(strings.Join(c.Request.Header.Values("X-Trace"), ","))})
}

func init() {
	module.Register(module.Info{ID: module.Handlers + ".test_probe", New: func() any { return new(probe) }})
	module.Register(module.Info{ID: module.Handlers + ".test_trace", New: func() any { return new(trace) }})
	module.Register(module.Info{ID: module.Middlewares + ".test_tag", New: func() any { return new(tag) }})
	// A module in the handlers namespace that is not a handler.
	module.Register(module.Info{ID: module.Handlers + ".test_inert", New: func() any { return new(struct{}) }})
}

func compile(t *testing.T, doc string) (*Graph, error) {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return Compile(c)
}

func mustCompile(t *testing.T, doc string) *Graph {
	t.Helper()
	g, err := compile(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func body(h module.Handler) string {
	return string(fibtest.Serve(h, "GET", "/").Response.Body)
}

func TestCompile(t *testing.T) {
	g := mustCompile(t, `{
  "listeners": [
    {"name": "web", "address": ":0"},
    {"name": "api", "address": ":0", "protocols": ["h2c"]}
  ],
  "routes": [
    {"name": "low", "handler": {"module": "test_probe", "params": {"id": "low"}}},
    {"name": "api-only", "listeners": ["api"], "priority": 5, "match": {"path": ["/v1/*"]}, "handler": {"module": "test_probe", "params": {"id": "api"}}},
    {"name": "high", "priority": 5, "match": {"host": ["a.com"]}, "handler": {"module": "test_probe", "params": {"id": "high"}}},
    {"name": "chain", "priority": -1, "middleware": [{"module": "test_tag", "params": {"name": "outer"}}, {"module": "test_tag", "params": {"name": "inner"}}], "handler": {"module": "test_trace"}}
  ]
}`)
	defer g.Release()

	names := func(l *Listener) string {
		var ns []string
		for _, r := range l.Routes {
			ns = append(ns, r.Name)
		}
		return strings.Join(ns, " ")
	}
	web, api := g.Listeners[0], g.Listeners[1]
	if web.Name != "web" || strings.Join(web.Protocols, ",") != config.ProtocolHTTP1 || names(web) != "high low chain" {
		t.Fatalf("web: %+v routes %q", web, names(web))
	}
	if api.Protocols[0] != config.ProtocolH2C || names(api) != "api-only high low chain" {
		t.Fatalf("api: %+v routes %q", api, names(api))
	}
	if r := api.Routes[0]; r.Priority != 5 || r.Paths[0] != "/v1/*" || body(r.Handler) != "api" {
		t.Fatalf("api-only: %+v", r)
	}
	if r := web.Routes[0]; r.Hosts[0] != "a.com" || r.Paths != nil {
		t.Fatalf("high: %+v", r)
	}
	if got := body(web.Routes[2].Handler); got != "outer,inner" {
		t.Fatalf("middleware order: %q", got)
	}
	// Routes are shared between the listeners that serve them.
	if web.Routes[1] != api.Routes[2] {
		t.Fatal("a route serving two listeners was compiled twice")
	}
	// Each listener's router picks among its own routes.
	for _, tt := range []struct {
		l          *Listener
		host, path string
		want       string
	}{
		{web, "a.com", "/v1/x", "high"},
		{web, "b.com", "/v1/x", "low"},
		{api, "a.com", "/v1/x", "high"}, // equal priority: the host is more specific
		{api, "b.com", "/v1/x", "api-only"},
		{api, "b.com", "/x", "low"},
	} {
		if r, ok := tt.l.Router.Match(tt.host, tt.path); !ok || r.Name != tt.want {
			t.Errorf("%s: %s%s: got %v, want %s", tt.l.Name, tt.host, tt.path, r, tt.want)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	probes.Clear()
	_, err := compile(t, `{
  "listeners": [{"name": "web", "address": ":0"}],
  "routes": [
    {"handler": {"module": "test_probe", "params": {"id": "kept"}}},
    {"handler": {"module": "nope"}},
    {"handler": {"module": "test_inert"}},
    {"middleware": [{"module": "test_tag", "params": {"name": "x", "fail": true}}, {"module": "respond"}], "handler": {"module": "test_probe", "params": {"id": "also kept"}}},
    {"handler": {"module": "respond", "params": {"status": 99}}}
  ]
}`)
	want := strings.Join([]string{
		`5:28: routes[1].handler.module: there is no module "nope" in http.handlers (it has "respond", "test_inert", "test_probe", "test_trace")`,
		`6:28: routes[2].handler.module: http.handlers.test_inert is not a handler`,
		`7:54: routes[3].middleware[0].params: tag x failed`,
		`7:95: routes[3].middleware[1].module: there is no module "respond" in http.middleware (it has "test_tag")`,
		`8:60: routes[4].handler.params.status: 99 is less than the minimum 100`,
	}, "\n")
	if err == nil || err.Error() != want {
		t.Fatalf("errors:\n%v\nwant:\n%s", err, want)
	}
	// The modules that were provisioned before the failure are cleaned up.
	var kept int
	probes.Range(func(k, _ any) bool {
		if p := k.(*probe); strings.Contains(p.ID, "kept") {
			kept++
			if p.cleaned.Load() != 1 {
				t.Errorf("probe %q cleaned %d times", p.ID, p.cleaned.Load())
			}
		}
		return true
	})
	if kept != 2 {
		t.Fatalf("%d probes kept, want 2", kept)
	}
}

func probeOf(g *Graph) *probe { return g.modules[0].(*probe) }

func TestSwapAndRelease(t *testing.T) {
	const doc = `{"listeners": [{"name": "web", "address": ":0"}], "routes": [{"handler": {"module": "test_probe", "params": {"id": "%d"}}}]}`
	var r Runtime
	if r.Current() != nil {
		t.Fatal("a new Runtime has a graph")
	}

	g1 := mustCompile(t, fmt.Sprintf(doc, 1))
	r.Swap(g1)
	held := r.Current() // a request in flight on g1
	if held != g1 || g1.Version != 1 {
		t.Fatalf("Current = %p version %d, want g1 version 1", held, held.Version)
	}

	g2 := mustCompile(t, fmt.Sprintf(doc, 2))
	r.Swap(g2)
	if g2.Version != 2 {
		t.Fatalf("g2 version %d", g2.Version)
	}
	if probeOf(g1).cleaned.Load() != 0 {
		t.Fatal("g1 was cleaned up while still in use")
	}
	held.Release()
	if probeOf(g1).cleaned.Load() != 1 {
		t.Fatal("g1 was not cleaned up after its last release")
	}

	cur := r.Current()
	cur.Retain()
	cur.Release()
	cur.Release()
	p2 := probeOf(g2)
	r.Close()
	if r.Current() != nil || p2.cleaned.Load() != 1 {
		t.Fatalf("after Close: current %v, g2 cleaned %d", r.Current(), p2.cleaned.Load())
	}

	// A graph compiled but never swapped in is cleaned up by its release.
	g3 := mustCompile(t, fmt.Sprintf(doc, 3))
	p3 := probeOf(g3)
	g3.Release()
	if p3.cleaned.Load() != 1 {
		t.Fatal("an unswapped graph was not cleaned up")
	}
}

func TestReleaseMisuse(t *testing.T) {
	g := mustCompile(t, `{"listeners": [{"name": "web", "address": ":0"}]}`)
	g.Release()
	for _, misuse := range []struct {
		name string
		f    func()
	}{{"Retain", g.Retain}, {"Release", g.Release}, {"Retain", g.Retain}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s on a released graph did not panic", misuse.name)
				}
			}()
			misuse.f()
		}()
	}
}

// TestConcurrentSwaps swaps graphs while readers serve through whatever is
// current. Run with -race, it checks that no reader ever uses a graph that
// was cleaned up, and that every graph is cleaned up exactly once.
func TestConcurrentSwaps(t *testing.T) {
	const (
		readers = 8
		swaps   = 300
	)
	var r Runtime
	var all []*probe
	newGraph := func(i int) *Graph {
		g := mustCompile(t, fmt.Sprintf(`{"listeners": [{"name": "web", "address": ":0"}], "routes": [{"handler": {"module": "test_probe", "params": {"id": "c%d"}}}]}`, i))
		all = append(all, probeOf(g))
		return g
	}
	r.Swap(newGraph(0))

	var stop atomic.Bool
	var wg sync.WaitGroup
	var failures atomic.Int64
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				g := r.Current()
				p := probeOf(g)
				if p.cleaned.Load() != 0 {
					failures.Add(1)
				}
				fibtest.Serve(g.Listeners[0].Routes[0].Handler, "GET", "/")
				if p.cleaned.Load() != 0 {
					failures.Add(1)
				}
				g.Release()
			}
		}()
	}
	for i := 1; i <= swaps; i++ {
		r.Swap(newGraph(i))
	}
	stop.Store(true)
	wg.Wait()
	r.Close()

	if n := failures.Load(); n > 0 {
		t.Fatalf("readers used a cleaned-up graph %d times", n)
	}
	var served int64
	for _, p := range all {
		if n := p.cleaned.Load(); n != 1 {
			t.Fatalf("probe %s cleaned up %d times", p.ID, n)
		}
		served += p.served.Load()
	}
	if served == 0 {
		t.Fatal("the readers served nothing")
	}
}
