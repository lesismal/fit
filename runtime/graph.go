// Package runtime compiles a config into the immutable graph the data plane
// serves from, and swaps graphs atomically when the config changes.
//
// A Graph is never changed once compiled. Whatever serves a request or a
// connection takes a reference to the current graph and keeps using it until
// it is done, so a config change affects only what starts after it. When
// the last reference to a graph that is no longer current goes, its modules
// are cleaned up.
package runtime

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/router"
	"github.com/lesismal/fit/tlsmgr"
	"github.com/lesismal/fit/upstream"
)

// Graph is a compiled config.
type Graph struct {
	// Version is the graph's number, given by Runtime.Swap; 0 for a graph
	// that was never swapped in.
	Version uint64
	// Listeners are the config's listeners, in the order written.
	Listeners []*Listener
	// AccessLog is where a line goes for each request, or nil.
	AccessLog *observe.AccessLog
	// MaxPendingBytes bounds the output waiting across the listeners'
	// connections; see config.Limits. The listeners' engine takes it when it
	// starts.
	MaxPendingBytes int64

	refs      atomic.Int64
	modules   []any // every provisioned module, in the order provisioned
	upstreams map[string]*upstream.Pool
	// upstreamKeys say, by name, what each pool was made from, so that the
	// next graph can take over the pools of the upstreams it leaves as they
	// are.
	upstreamKeys map[string]string

	mu         sync.Mutex
	cleaned    bool        // the modules are cleaned up
	drainTimer *time.Timer // set once the graph is swapped out
}

// Env is what compiling a config needs from the process it runs in.
type Env struct {
	// Engine is the running fib engine upstream pools dial through. Nil
	// makes pools that fail every request, for a config that is only
	// checked, as fit validate does.
	Engine *fib.Engine
	// AccessLog is the log made from the config's access_log, which the
	// caller owns and closes, or nil.
	AccessLog *observe.AccessLog
	// Previous is the graph being replaced, or nil. The new graph shares
	// its pools for the upstreams whose config, and CA file, are the same,
	// so that their connections and their backends' health carry over.
	Previous *Graph
}

// Listener is a listener and the routes it serves.
type Listener struct {
	Name      string
	Address   string
	Protocols []string
	// TLS is the listener's TLS configuration, or nil for one in the
	// clear, and TLSKey a fingerprint of its certificates and protocols.
	TLS    *tls.Config
	TLSKey string
	// MaxHeaderBytes and MaxBodyBytes bound the requests it takes; see
	// config.Limits.
	MaxHeaderBytes int
	MaxBodyBytes   int64
	// Routes are the listener's routes: highest priority first, and routes
	// of equal priority in the order written.
	Routes []*Route
	// Router picks the route for a request, by package router's rules.
	Router *router.Router[*Route]
}

// Route is a compiled route.
type Route struct {
	Name     string
	Priority int
	// Hosts and Paths are what the route matches, as written; empty matches
	// everything. Listener.Router gives them meaning.
	Hosts []string
	Paths []string
	// Handler is the route's handler with its middleware already wrapped
	// around it.
	Handler module.Handler
}

// Compile builds a Graph from c, which must already have passed
// config.Load's checks. It makes a pool for each upstream, dialing through
// env's engine, then loads and provisions every module the config names, and
// reports every module that fails, not only the first. On failure, the
// modules already provisioned are cleaned up and the pools closed.
//
// The Graph returned holds one reference, its compiler's: Release it, or
// hand it to Runtime.Swap.
func Compile(c *config.Config, env Env) (*Graph, error) {
	g := &Graph{AccessLog: env.AccessLog, MaxPendingBytes: c.Limits.PendingBytes(),
		upstreams: make(map[string]*upstream.Pool, len(c.Upstreams)), upstreamKeys: make(map[string]string, len(c.Upstreams))}
	g.refs.Store(1)
	var errs config.Errors
	for i := range c.Upstreams {
		u := &c.Upstreams[i]
		key, keyed := upstreamKey(u)
		if keyed {
			g.upstreamKeys[u.Name] = key
			if prev := env.Previous; prev != nil && prev.upstreamKeys[u.Name] == key {
				if p := prev.upstreams[u.Name]; p != nil {
					p.Acquire()
					g.upstreams[u.Name] = p
					continue
				}
			}
		}
		p, err := upstream.New(env.Engine, u)
		if err != nil {
			errs = append(errs, config.AsErrors(err)...)
			continue
		}
		g.upstreams[u.Name] = p
	}
	if len(errs) > 0 {
		// The routes that name a broken upstream would only add to these.
		cleanupModules(g)
		return nil, errs
	}
	menv := &module.Env{Upstreams: g.upstreams, WriteStall: c.Limits.WriteStall(), Graph: g}

	routes := make([]*Route, len(c.Routes))
	for i, rc := range c.Routes {
		r, err := g.compileRoute(rc, menv)
		if err != nil {
			errs = append(errs, config.AsErrors(err)...)
			continue
		}
		routes[i] = r
	}
	if len(errs) > 0 {
		cleanupModules(g)
		return nil, errs
	}

	for _, lc := range c.Listeners {
		l := &Listener{Name: lc.Name, Address: lc.Address, Protocols: lc.Protocols,
			MaxHeaderBytes: c.Limits.HeaderBytes(), MaxBodyBytes: c.Limits.BodyBytes()}
		if len(l.Protocols) == 0 {
			l.Protocols = config.DefaultProtocols
			if lc.TLS != nil {
				l.Protocols = config.DefaultTLSProtocols
			}
		}
		if lc.TLS != nil {
			var err error
			if l.TLS, l.TLSKey, err = tlsmgr.ServerConfig(lc.TLS, l.Protocols); err != nil {
				errs = append(errs, config.AsErrors(err)...)
				continue
			}
		}
		for i, rc := range c.Routes {
			if len(rc.Listeners) == 0 || slices.Contains(rc.Listeners, lc.Name) {
				l.Routes = append(l.Routes, routes[i])
			}
		}
		slices.SortStableFunc(l.Routes, func(a, b *Route) int { return b.Priority - a.Priority })
		l.Router = newRouter(l.Routes)
		g.Listeners = append(g.Listeners, l)
	}
	if len(errs) > 0 {
		cleanupModules(g)
		return nil, errs
	}
	return g, nil
}

// newRouter builds the router for a listener's routes.
func newRouter(routes []*Route) *router.Router[*Route] {
	rs := make([]router.Route[*Route], len(routes))
	for i, r := range routes {
		rs[i] = router.Route[*Route]{Hosts: r.Hosts, Paths: r.Paths, Priority: r.Priority, Value: r}
	}
	return router.New(rs)
}

func (g *Graph) compileRoute(rc config.Route, env *module.Env) (*Route, error) {
	var errs config.Errors
	m, err := module.Load(module.Handlers, rc.Handler, env)
	var h module.Handler
	if err != nil {
		errs = append(errs, config.AsErrors(err)...)
	} else {
		g.modules = append(g.modules, m)
		var ok bool
		if h, ok = m.(module.Handler); !ok {
			errs = append(errs, rc.Handler.At.Field("module").Errorf("%s.%s is not a handler", module.Handlers, rc.Handler.Module))
		}
	}
	mws := make([]module.Middleware, len(rc.Middleware))
	for i, ref := range rc.Middleware {
		m, err := module.Load(module.Middlewares, ref, env)
		if err != nil {
			errs = append(errs, config.AsErrors(err)...)
			continue
		}
		g.modules = append(g.modules, m)
		var ok bool
		if mws[i], ok = m.(module.Middleware); !ok {
			errs = append(errs, ref.At.Field("module").Errorf("%s.%s is not a middleware", module.Middlewares, ref.Module))
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i].Wrap(h)
	}
	r := &Route{Name: rc.Name, Priority: rc.Priority, Handler: h}
	if rc.Match != nil {
		r.Hosts, r.Paths = rc.Match.Host, rc.Match.Path
	}
	return r, nil
}

// cleanupModules cleans up g's modules in the reverse of the order they were
// provisioned, so that a module is cleaned up before any it was built on,
// and then closes the upstream pools the modules used. A pool closes once
// the requests still in flight on it are done.
func cleanupModules(g *Graph) {
	g.mu.Lock()
	g.cleaned = true
	if g.drainTimer != nil {
		g.drainTimer.Stop()
	}
	g.mu.Unlock()
	for i := len(g.modules) - 1; i >= 0; i-- {
		module.Cleanup(g.modules[i])
	}
	for _, p := range g.upstreams {
		p.Close()
	}
}

// upstreamKey is what decides whether two graphs can share an upstream's
// pool: its config, and the contents of its CA file, which a reload is
// expected to read again. It reports false when the CA file cannot be read,
// for upstream.New to report.
func upstreamKey(u *config.Upstream) (string, bool) {
	data, err := json.Marshal(u)
	if err != nil {
		return "", false
	}
	key := string(data)
	if u.TLSCAFile != "" {
		pem, err := os.ReadFile(u.TLSCAFile)
		if err != nil {
			return "", false
		}
		sum := sha256.Sum256(pem)
		key += "\x00" + hex.EncodeToString(sum[:])
	}
	return key, true
}

// retire starts g's drain timer, now that it is swapped out: if requests
// still use it after drain, its modules are told to end them. A graph whose
// last request is done first is cleaned up and never drained.
func (g *Graph) retire(drain time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cleaned || drain < 0 {
		return
	}
	g.drainTimer = time.AfterFunc(drain, g.drain)
}

// drain asks g's modules to end what they have under way.
func (g *Graph) drain() {
	g.mu.Lock()
	cleaned := g.cleaned
	g.mu.Unlock()
	if cleaned {
		return
	}
	// The drained requests release g as they end, which may clean it up
	// meanwhile; Drain may run beside Cleanup.
	for _, m := range g.modules {
		if d, ok := m.(module.Drainer); ok {
			d.Drain()
		}
	}
}

// Upstream returns the pool of the upstream named name.
func (g *Graph) Upstream(name string) (*upstream.Pool, bool) {
	p, ok := g.upstreams[name]
	return p, ok
}

// Retain takes another reference to g, which must already hold one that the
// caller keeps until Retain returns.
func (g *Graph) Retain() {
	if !g.tryRetain() {
		panic("runtime: Retain on a released graph")
	}
}

// tryRetain takes a reference to g unless its last one is already gone, in
// which case it is being cleaned up and must not be used.
func (g *Graph) tryRetain() bool {
	for {
		n := g.refs.Load()
		if n <= 0 {
			return false
		}
		if g.refs.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// Release gives back a reference to g. Giving back the last one cleans up
// g's modules.
func (g *Graph) Release() {
	switch n := g.refs.Add(-1); {
	case n == 0:
		cleanupModules(g)
	case n < 0:
		panic("runtime: graph released more times than retained")
	}
}
