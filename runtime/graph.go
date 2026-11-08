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
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/l4"
	"github.com/lesismal/fit/logs"
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
	// Tracer samples requests and records their spans, or is nil.
	Tracer *observe.Tracer
	// Metrics is where the listeners' requests are counted, or nil when
	// the config does not count them.
	Metrics *observe.Metrics
	// MaxPendingBytes bounds the output waiting across the listeners'
	// connections; see config.Limits. The listeners' engine takes it when it
	// starts.
	MaxPendingBytes int64
	// MaxConns bounds the client connections open across the listeners;
	// see config.Limits. The engine takes it when it starts, as it does
	// MaxPendingBytes.
	MaxConns int
	// MaxWriteQueueBytes caps what one connection may have queued for its
	// client; see config.Limits. The listeners' engine takes it when it
	// starts.
	MaxWriteQueueBytes int64
	// Engine is the config's engine settings, or nil; the listeners'
	// engine takes them when it starts, as it does MaxPendingBytes.
	Engine *config.Engine
	// ACME obtains the certificates of the names the listeners list in
	// tls.acme, ACMENames, or is nil. It outlives the graph: once the graph
	// is current, ACME is told to manage ACMENames, none or some, and it
	// answers HTTP-01 challenges on the listeners in the clear.
	ACME      *tlsmgr.ACME
	ACMENames []string
	// CertFiles holds the SHA-256 of each certificate, key and CA file the
	// listeners and upstreams loaded, by path, for a tlsmgr.FileWatcher; nil
	// when they loaded none.
	CertFiles map[string][sha256.Size]byte
	// AdminAccess and ConsoleAccess are the client addresses the Admin API
	// and admin.console serve: admin.access, or admin.console.access, then
	// the global access; nil serves everyone. Unlike the rest of admin they
	// follow every config applied, which sets them on the gates the two
	// accept through.
	AdminAccess, ConsoleAccess *access.Policy

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
	// Metrics is where requests are counted when the config has metrics,
	// which the caller owns; nil counts none.
	Metrics *observe.Metrics
	// AccessLog is the log made from the config's access_log, which the
	// caller owns and closes, or nil.
	AccessLog *observe.AccessLog
	// Log is fit's own log, made from the config's log, which the caller
	// owns and closes, or nil. A config applied sets its loggers' levels,
	// and the access log's.
	Log *logs.Root
	// Tracer is made from the config's telemetry.traces, which the caller
	// owns and closes, or nil.
	Tracer *observe.Tracer
	// ACME is the certificate manager made from the config's acme, which
	// the caller owns and closes, or nil. Nil obtains no certificates for
	// the names listeners list in tls.acme, for a config that is only
	// checked, as fit validate does.
	ACME *tlsmgr.ACME
	// OCSP keeps the OCSP responses of the certificates from files, which
	// their handshakes get stapled; the caller owns and closes it. Nil
	// staples none.
	OCSP *tlsmgr.OCSP
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
	// MPTCP, when set, has the listener's TCP connections made with
	// Multipath TCP where the kernel offers it (Linux 5.6+,
	// net.mptcp.enabled), falling back to plain TCP where it does not; a
	// listener without it is plain TCP, whatever GODEBUG says. Its HTTP/3,
	// which is UDP, has none.
	MPTCP bool
	// Certificates picks the certificate of each TLS handshake, or is nil
	// for a listener in the clear.
	Certificates tlsmgr.Manager
	// ClientAuth, when not nil, is how a TLS listener verifies its clients'
	// certificates.
	ClientAuth *tlsmgr.ClientAuth
	// MaxHeaderBytes and MaxBodyBytes bound the requests it takes; see
	// config.Limits.
	MaxHeaderBytes int
	MaxBodyBytes   int64
	// RedirectHTTPS, when not 0, is the HTTPS port every request this
	// listener in the clear takes is redirected to, but for ACME's HTTP-01
	// challenges; it serves no routes. HTTPS by default makes such a
	// listener.
	RedirectHTTPS int
	// Routes are the listener's routes: highest priority first, and routes
	// of equal priority in the order written.
	Routes []*Route
	// Router picks the route for a request, by package router's rules.
	Router *router.Router[*Route]
	// Requests count the requests each route answers on the listener, by
	// Route.Index, and Unmatched those no route takes; nil without
	// Graph.Metrics.
	Requests  []*observe.RequestMetrics
	Unmatched *observe.RequestMetrics

	// L4 is what the connections of a listener that serves tcp or udp are
	// proxied by, or nil for one that serves neither.
	L4 *l4.Entry
	// ProxyTrusted, for a listener that does not serve tcp or udp alone, are
	// the addresses it takes PROXY protocol headers from, before anything
	// else; none when it is empty. L4 reads those of one that does.
	ProxyTrusted l4.Trusted
	// SOCKS, for a listener that serves socks, is who may use it and where
	// to.
	SOCKS *l4.Rules

	// Access is the client addresses the listener serves: its own access,
	// then the global one; nil serves everyone.
	Access *access.Policy
	// AcceptAccess is the policy the listener's sockets ask at accept, for
	// TCP connections and new UDP peers, or nil when they serve every peer
	// there. It is Access, or a udp or lone tcp service's policy before it,
	// unless the address is known only later, after a PROXY header, or a
	// route or service of the listener decides for itself: then nil, and
	// CheckRequests, the L4 entry and SOCKSAccess decide.
	AcceptAccess *access.Policy
	// CheckRequests has every HTTP request checked against RouteAccess, by
	// its route, or Access when no route takes it; a request refused is
	// answered 403.
	CheckRequests bool
	RouteAccess   []*access.Policy
	// SOCKSAccess is what the listener's socks checks a client against
	// when AcceptAccess has not.
	SOCKSAccess *access.Policy
}

// serviceAccess is a service of a listener and its access list; names says
// it takes connections by server name.
type serviceAccess struct {
	svc   *l4.Service
	list  *access.List
	names bool
}

// compileAccess sets l's policies from the global list, l's own, its
// routes', by Route.Index, and its services'. proxy says l takes PROXY
// headers, which name the address only after the accept.
func (l *Listener) compileAccess(global, own *access.List, routes []*access.List, services []serviceAccess, proxy bool) {
	l.Access = access.Chain(own, global)
	routed := false
	for _, r := range l.Routes {
		routed = routed || !routes[r.Index].Empty()
	}
	serviced := false
	for _, s := range services {
		serviced = serviced || !s.list.Empty()
	}
	if l.Access == nil && !routed && !serviced {
		return
	}
	if l.L4 != nil {
		policies := make(map[*l4.Service]*access.Policy, len(services))
		for _, s := range services {
			policies[s.svc] = access.Chain(s.list, own, global)
		}
		l.L4.SetAccess(l.Access, policies)
	}
	switch {
	case !proxy && !routed && !serviced:
		l.AcceptAccess = l.Access
		return
	case !proxy && !routed && len(services) == 1 && !services[0].names && !config.Sniffs(l.Protocols):
		// A lone service takes every connection, or every udp session:
		// its policy is known at accept.
		l.AcceptAccess = access.Chain(services[0].list, own, global)
		return
	}
	// Decided later, by what takes the connection.
	if l.L4 != nil {
		l.L4.CheckAccess()
	}
	l.SOCKSAccess = l.Access
	if l.ServesHTTP() {
		l.CheckRequests = true
		l.RouteAccess = make([]*access.Policy, len(routes))
		for _, r := range l.Routes {
			l.RouteAccess[r.Index] = l.Access
			if !routes[r.Index].Empty() {
				l.RouteAccess[r.Index] = access.Chain(routes[r.Index], own, global)
			}
		}
	}
}

// RequestAccess returns the policy a request route takes on l is checked
// against, route being nil for one no route takes.
func (l *Listener) RequestAccess(route *Route) *access.Policy {
	if route == nil {
		return l.Access
	}
	return l.RouteAccess[route.Index]
}

// ServesHTTP reports whether l serves HTTP, and so routes.
func (l *Listener) ServesHTTP() bool { return config.ServesHTTP(l.Protocols) && l.RedirectHTTPS == 0 }

// RequestMetrics returns where a request route answered on l is counted,
// or nil; route is nil for a request no route takes.
func (l *Listener) RequestMetrics(route *Route) *observe.RequestMetrics {
	if route == nil {
		return l.Unmatched
	}
	if l.Requests == nil {
		return nil
	}
	return l.Requests[route.Index]
}

// Route is a compiled route.
type Route struct {
	Name string
	// Index is the route's place in the config's routes.
	Index    int
	Priority int
	// Hosts and Paths are what the route matches, as written; empty matches
	// everything. Listener.Router gives them meaning.
	Hosts []string
	Paths []string
	// Matchers are the route's other matchers, built from its config.
	Matchers []router.Matcher
	// Handler is the route's handler with its middleware already wrapped
	// around it.
	Handler module.Handler
	// Inline says that Handler answers every request before it returns:
	// a module.Inline handler with no middleware around it.
	Inline bool
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
	g := &Graph{AccessLog: env.AccessLog, Tracer: env.Tracer, ACME: env.ACME, MaxPendingBytes: c.Limits.PendingBytes(), MaxConns: c.Limits.Conns(), MaxWriteQueueBytes: c.Limits.QueueBytes(), Engine: c.Engine,
		upstreams: make(map[string]*upstream.Pool, len(c.Upstreams)), upstreamKeys: make(map[string]string, len(c.Upstreams))}
	if c.Metrics != nil || c.Telemetry != nil && c.Telemetry.Metrics != nil {
		g.Metrics = env.Metrics
	}
	g.refs.Store(1)
	var errs config.Errors
	if err := app.Check(c); err != nil {
		errs = append(errs, config.AsErrors(err)...)
	}
	if w := c.WebRTC; w != nil && w.TURN != nil && w.TURN.TLS != nil {
		// webrtc.turn's certificates, loaded here to be checked and watched
		// as a listener's are; the role loads them again when it applies.
		if certs, err := tlsmgr.LoadStatic(w.TURN.TLS.Config()); err != nil {
			errs = append(errs, config.AsErrors(err)...)
		} else {
			g.watchFiles(certs.Files())
		}
	}
	// The roles' certificate files, as the tunnel hub's listeners' and the
	// LLM mesh's: the roles read them when they apply, so a change to them
	// has the config applied again. One that cannot be read now is not
	// watched; the role says why when it applies.
	for _, p := range app.CertFiles(c) {
		if data, err := os.ReadFile(p); err == nil {
			g.watchFiles(map[string][sha256.Size]byte{p: sha256.Sum256(data)})
		}
	}
	for i := range c.Upstreams {
		u := &c.Upstreams[i]
		key, files, keyed := upstreamKey(u)
		if keyed {
			g.upstreamKeys[u.Name] = key
			g.watchFiles(files)
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
	menv := &module.Env{Upstreams: g.upstreams, WriteStall: c.Limits.WriteStall(), Graph: g, Engine: env.Engine, MediaAuth: c.MediaAuth,
		MediaLibrary: c.MediaLibrary}

	routes := make([]*Route, len(c.Routes))
	for i, rc := range c.Routes {
		r, err := g.compileRoute(rc, menv)
		if err != nil {
			errs = append(errs, config.AsErrors(err)...)
			continue
		}
		r.Index = i
		routes[i] = r
	}
	if len(errs) > 0 {
		cleanupModules(g)
		return nil, errs
	}

	// The access lists: the global one, and each route's and service's.
	files := map[string][sha256.Size]byte{}
	global, err := access.Compile(c.Access, files)
	if err != nil {
		errs = append(errs, config.AsErrors(err)...)
	}
	routeLists := make([]*access.List, len(c.Routes))
	for i := range c.Routes {
		if routeLists[i], err = access.Compile(c.Routes[i].Access, files); err != nil {
			errs = append(errs, config.AsErrors(err)...)
		}
	}
	serviceLists := make([]*access.List, len(c.Services))
	for i := range c.Services {
		if serviceLists[i], err = access.Compile(c.Services[i].Access, files); err != nil {
			errs = append(errs, config.AsErrors(err)...)
		}
	}
	listenerLists := make(map[string]*access.List, len(c.Listeners))
	for i := range c.Listeners {
		lc := &c.Listeners[i]
		if listenerLists[lc.Name], err = access.Compile(lc.Access, files); err != nil {
			errs = append(errs, config.AsErrors(err)...)
		}
	}
	// The Admin API's and the consoles' own: compiled here for their files
	// to be checked and watched; the roles compile their consoles' again
	// when they apply.
	// A config without admin still has its global access apply to the
	// Admin API and console fit started with.
	var adminList, consoleList *access.List
	if a := c.Admin; a != nil {
		if adminList, err = access.Compile(a.Access, files); err != nil {
			errs = append(errs, config.AsErrors(err)...)
		}
		if a.Console != nil {
			if consoleList, err = access.Compile(a.Console.Access, files); err != nil {
				errs = append(errs, config.AsErrors(err)...)
			}
		}
	}
	g.AdminAccess, g.ConsoleAccess = access.Chain(adminList, global), access.Chain(consoleList, global)
	for _, rc := range c.RoleConsoles() {
		if _, err = access.Compile(rc.Access, files); err != nil {
			errs = append(errs, config.AsErrors(err)...)
		}
	}
	g.watchFiles(files)
	if len(errs) > 0 {
		cleanupModules(g)
		return nil, errs
	}
	// The services each listener's entry hands connections to, with their
	// lists, for its access.
	listenerServices := map[string][]serviceAccess{}

	trusted := map[string]l4.Trusted{}
	for _, lc := range c.Listeners {
		if pp := lc.ProxyProtocol; pp != nil {
			for _, s := range pp.Trusted {
				// Checked by Validate.
				p, _ := router.ParsePrefix(s)
				trusted[lc.Name] = append(trusted[lc.Name], p)
			}
		}
	}
	// A listener that serves tcp beside other protocols takes the PROXY
	// header before it tells them apart; its entry reads none.
	l4Alone := map[string]bool{}
	for _, lc := range c.Listeners {
		l4Alone[lc.Name] = !config.Sniffs(lc.Protocols)
	}
	entries := map[string]*l4.Entry{}
	for i := range c.Services {
		sc := &c.Services[i]
		svc := l4.NewService(sc, g.upstreams[sc.Upstream], env.Engine)
		// A drain closes its connections, as it does the proxy's.
		g.modules = append(g.modules, svc)
		for _, name := range sc.Listeners {
			if entries[name] == nil {
				var t l4.Trusted
				if l4Alone[name] {
					t = trusted[name]
				}
				entries[name] = l4.NewEntry(t)
			}
			entries[name].Add(svc, sc.ServerNames)
			listenerServices[name] = append(listenerServices[name], serviceAccess{svc, serviceLists[i], len(sc.ServerNames) > 0})
		}
	}

	// The listeners written, or those HTTPS by default makes.
	listeners, redirect := c.Effective()
	for _, lc := range listeners {
		l := &Listener{Name: lc.Name, Address: lc.Address, Protocols: lc.Protocols, MPTCP: lc.MPTCP, RedirectHTTPS: redirect[lc.Name],
			MaxHeaderBytes: c.Limits.HeaderBytes(), MaxBodyBytes: c.Limits.BodyBytes()}
		if len(l.Protocols) == 0 {
			l.Protocols = config.DefaultProtocols
			if lc.TLS != nil {
				l.Protocols = config.DefaultTLSProtocols
			}
		}
		if t := lc.TLS; t != nil {
			var static *tlsmgr.Static
			if len(t.Certificates) > 0 {
				certs, err := tlsmgr.LoadStatic(t)
				if err != nil {
					errs = append(errs, config.AsErrors(err)...)
					continue
				}
				static = certs
				g.watchFiles(certs.Files())
				if env.OCSP != nil {
					static.Staple(env.OCSP)
				}
			}
			if t.ClientAuth != nil {
				ca, err := tlsmgr.LoadClientAuth(t.ClientAuth)
				if err != nil {
					errs = append(errs, config.AsErrors(err)...)
					continue
				}
				l.ClientAuth = ca
				g.watchFiles(ca.Files())
			}
			switch {
			case len(t.ACME) == 0 && !t.OnDemand:
				l.Certificates = static
			case env.ACME == nil:
				// A config only checked, as fit validate does: the names'
				// certificates are not obtained, and only the files' served.
				l.Certificates = tlsmgr.NewCerts(static, nil, nil, false)
			default:
				l.Certificates = tlsmgr.NewCerts(static, env.ACME, t.ACME, t.OnDemand)
				for _, name := range t.ACME {
					if !slices.Contains(g.ACMENames, name) {
						g.ACMENames = append(g.ACMENames, name)
					}
				}
			}
		}
		l.L4 = entries[lc.Name]
		if l.L4 == nil || !l4Alone[lc.Name] {
			l.ProxyTrusted = trusted[lc.Name]
		}
		if lc.SOCKS != nil {
			l.SOCKS = l4.NewRules(lc.SOCKS)
		}
		for i, rc := range c.Routes {
			if !l.ServesHTTP() {
				break // it serves no routes
			}
			if len(rc.Listeners) == 0 || slices.Contains(rc.Listeners, lc.Name) {
				l.Routes = append(l.Routes, routes[i])
			}
		}
		if m := g.Metrics; m != nil && config.ServesHTTP(l.Protocols) {
			l.Requests = make([]*observe.RequestMetrics, len(routes))
			for _, r := range l.Routes {
				l.Requests[r.Index] = m.Requests(l.Name, metricsName(r))
			}
			l.Unmatched = m.Requests(l.Name, "")
		}
		slices.SortStableFunc(l.Routes, func(a, b *Route) int { return b.Priority - a.Priority })
		l.Router = newRouter(l.Routes)
		l.compileAccess(global, listenerLists[lc.Name], routeLists, listenerServices[lc.Name], len(trusted[lc.Name]) > 0)
		g.Listeners = append(g.Listeners, l)
	}
	if len(errs) > 0 {
		cleanupModules(g)
		return nil, errs
	}
	return g, nil
}

// authenticates reports whether m lets a request through only once it has
// authenticated the caller.
func authenticates(m module.Middleware) bool {
	a, ok := m.(module.Authenticator)
	return ok && a.Authenticates()
}

// newRouter builds the router for a listener's routes.
func newRouter(routes []*Route) *router.Router[*Route] {
	rs := make([]router.Route[*Route], len(routes))
	for i, r := range routes {
		rs[i] = router.Route[*Route]{Hosts: r.Hosts, Paths: r.Paths, Matchers: r.Matchers, Priority: r.Priority, Value: r}
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
	if _, ok := h.(module.NeedsAuthentication); ok && !slices.ContainsFunc(mws, authenticates) {
		return nil, rc.Handler.At.Field("module").Errorf("%s.%s serves only callers its route authenticates: the route needs a middleware that does, jwt, basic_auth, forward_auth, or client_cert without optional",
			module.Handlers, rc.Handler.Module)
	}
	_, inline := h.(module.Inline)
	inline = inline && len(mws) == 0
	if u, ok := h.(module.Unwrapped); ok && len(mws) == 0 {
		u.Unwrapped()
	}
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i].Wrap(h)
	}
	if len(mws) > 0 {
		h = connectionFirst(h)
	}
	r := &Route{Name: rc.Name, Priority: rc.Priority, Handler: h, Inline: inline}
	if rc.Match != nil {
		r.Hosts, r.Paths = rc.Match.Host, rc.Match.Path
		var err error
		if r.Matchers, err = matchers(rc.Match); err != nil {
			return nil, rc.Match.At.Errorf("%v", err)
		}
	}
	return r, nil
}

// matchers builds the matchers m asks for besides hosts and paths, in the
// order cheapest first; config.Validate has checked them.
func matchers(m *config.Match) ([]router.Matcher, error) {
	var ms []router.Matcher
	if len(m.Method) > 0 {
		ms = append(ms, router.Methods(m.Method...))
	}
	if len(m.RemoteIP) > 0 {
		ip, err := router.RemoteIP(m.RemoteIP...)
		if err != nil {
			return nil, err
		}
		ms = append(ms, ip)
	}
	if len(m.Header) > 0 {
		ms = append(ms, router.Headers(m.Header))
	}
	if len(m.Query) > 0 {
		ms = append(ms, router.Query(m.Query))
	}
	if len(m.PathRegexp) > 0 {
		re, err := router.PathRegexp(m.PathRegexp...)
		if err != nil {
			return nil, err
		}
		ms = append(ms, re)
	}
	if m.Expr != "" {
		e, err := router.ParseExpr(m.Expr)
		if err != nil {
			return nil, err
		}
		ms = append(ms, e)
	}
	return ms, nil
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
	for _, l := range g.Listeners {
		if r, ok := l.Certificates.(interface{ Release() }); ok {
			r.Release()
		}
	}
	if m := g.Metrics; m != nil {
		for _, l := range g.Listeners {
			for _, r := range l.Requests {
				if r != nil {
					m.Release(r)
				}
			}
			if l.Unmatched != nil {
				m.Release(l.Unmatched)
			}
		}
	}
}

// metricsName is the name r's requests are counted under: its own, or, for
// a route without one, "#" and its place in the config's routes.
func metricsName(r *Route) string {
	if r.Name != "" {
		return r.Name
	}
	return "#" + strconv.Itoa(r.Index)
}

// upstreamKey is what decides whether two graphs can share an upstream's
// pool: its config, and the contents of its TLS files, CA, certificate and
// key, which a reload is expected to read again. files are those files'
// sums, by path, for the watcher. It reports false when a file cannot be
// read, for upstream.New to report.
func upstreamKey(u *config.Upstream) (key string, files map[string][sha256.Size]byte, ok bool) {
	data, err := json.Marshal(u)
	if err != nil {
		return "", nil, false
	}
	key = string(data)
	for _, path := range [...]string{u.TLSCAFile, u.TLSCertFile, u.TLSKeyFile} {
		if path == "" {
			continue
		}
		pem, err := os.ReadFile(path)
		if err != nil {
			return "", nil, false
		}
		sum := sha256.Sum256(pem)
		if files == nil {
			files = map[string][sha256.Size]byte{}
		}
		files[path] = sum
		key += "\x00" + hex.EncodeToString(sum[:])
	}
	return key, files, true
}

// watchFiles adds files, sums by path, to those g.CertFiles has watched.
func (g *Graph) watchFiles(files map[string][sha256.Size]byte) {
	for p, sum := range files {
		if g.CertFiles == nil {
			g.CertFiles = map[string][sha256.Size]byte{}
		}
		if prev, ok := g.CertFiles[p]; ok && prev != sum {
			// Changed while the config was compiled, so that two listeners
			// loaded it differently: a sum no file has has the watcher load
			// it again.
			sum = [sha256.Size]byte{}
		}
		g.CertFiles[p] = sum
	}
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

// Upstreams returns the graph's upstream pools, by name.
func (g *Graph) Upstreams() []*upstream.Pool {
	pools := make([]*upstream.Pool, 0, len(g.upstreams))
	for _, p := range g.upstreams {
		pools = append(pools, p)
	}
	slices.SortFunc(pools, func(a, b *upstream.Pool) int { return strings.Compare(a.Name(), b.Name()) })
	return pools
}

// Modules returns every module the graph provisioned, handlers and
// middleware, in the order provisioned. A route's Handler is its chain,
// wrapped, so this is where a handler's own value is found. The slice is
// the graph's: it is not to be changed.
func (g *Graph) Modules() []any { return g.modules }

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
