// Package listener binds the listeners of a runtime graph on one fib engine
// and dispatches the requests they take to the graph's routes.
//
// Every listener is a binding of the same engine, so all of them share one
// event loop, task pool and buffer pool. A request takes a reference to the
// graph that is current when it arrives and keeps it until its handler
// returns, so a graph swapped in later serves only the requests that start
// after the swap.
//
// Reload moves the server to a new graph: a listener whose name, address and
// protocols are unchanged keeps its binding and its connections; the others
// are bound before the graph is swapped in, and those the new graph lacks are
// drained after, finishing the requests they have.
package listener

import (
	"context"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/http3"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// DefaultDrainTimeout is how long a listener a reload removed may take to
// finish the requests it has, unless SetDrainTimeout says otherwise.
const DefaultDrainTimeout = 30 * time.Second

// Server serves the listeners of a Runtime's graph.
type Server struct {
	rt      *runtime.Runtime
	engine  *fib.Engine
	running chan error

	mu    sync.Mutex // orders reloads
	bound []*binding // the current graph's listeners, in its order
	drain time.Duration
}

// binding is a listener the engine serves.
type binding struct {
	// index and udpIndex are the engine's bindings for the listener's TCP
	// protocols and for HTTP/3, or -1 for one it does not have.
	index     int
	udpIndex  int
	name      string
	address   string
	protocols []string
	tlsKey    string
	// maxHeaderBytes and maxBodyBytes are the limits its handlers were made
	// with; a reload that changes them binds it anew.
	maxHeaderBytes int
	maxBodyBytes   int64
	// addr is where it listens: its TCP address, or for HTTP/3 alone its
	// UDP one; udpAddr is HTTP/3's.
	addr    net.Addr
	udpAddr net.Addr
}

// same reports whether b serves the listener l as it is: the same name,
// address, protocols and certificates.
func (b *binding) same(l *runtime.Listener) bool {
	return b.name == l.Name && b.address == l.Address && slices.Equal(b.protocols, l.Protocols) && b.tlsKey == l.TLSKey &&
		b.maxHeaderBytes == l.MaxHeaderBytes && b.maxBodyBytes == l.MaxBodyBytes
}

// Addr is where a listener listens.
type Addr struct {
	Name string
	Addr net.Addr
	// UDPAddr is where HTTP/3 listens, for a listener that serves it.
	UDPAddr   net.Addr
	Protocols []string
}

// Start binds every listener of rt's current graph and starts serving them.
func Start(rt *runtime.Runtime) (*Server, error) {
	g := rt.Current()
	if g == nil {
		return nil, errors.New("listener: the runtime has no graph")
	}
	defer g.Release()

	cfg := fib.DefaultConfig()
	cfg.Name = "fit"
	// What waits for clients, across every connection, is bounded; a client
	// that stops reading loses its response after the write stall timeout,
	// so it cannot hold the budget for long.
	cfg.MaxPendingBytes = g.MaxPendingBytes
	// Connections past max_conns are closed as they are accepted, and
	// counted in Stats.ConnsRefused.
	cfg.MaxConns = g.MaxConns
	engine, err := fib.New(cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{rt: rt, engine: engine, running: make(chan error, 1), drain: DefaultDrainTimeout}
	for _, l := range g.Listeners {
		b, err := s.bind(l, false)
		if err != nil {
			_ = engine.Close()
			return nil, err
		}
		s.bound = append(s.bound, b)
	}
	go func() { s.running <- engine.Run() }()
	return s, nil
}

// Stats returns the listeners' engine's counters: connections, those
// max_conns refused, bytes queued for clients, and the pauses the write
// watermark and max_pending_bytes caused.
func (s *Server) Stats() fib.Stats { return s.engine.Stats() }

// SetDrainTimeout sets how long a listener a reload removed may take to
// finish the requests it has before its connections are closed, and, in the
// runtime, how long requests may keep using a graph once it is swapped out.
func (s *Server) SetDrainTimeout(d time.Duration) {
	s.mu.Lock()
	s.drain = d
	s.mu.Unlock()
	s.rt.SetDrainTimeout(d)
}

// bind has the engine serve l. shared says another of the server's own
// bindings may be listening on the address, which fib's SO_REUSEPORT lets
// the two share while one replaces the other.
func (s *Server) bind(l *runtime.Listener, shared bool) (*binding, error) {
	b := &binding{index: -1, udpIndex: -1, name: l.Name, address: l.Address, protocols: l.Protocols, tlsKey: l.TLSKey,
		maxHeaderBytes: l.MaxHeaderBytes, maxBodyBytes: l.MaxBodyBytes}
	fail := func(err error) (*binding, error) {
		s.unbind(b)
		return nil, fmt.Errorf("listener %q: %w", l.Name, err)
	}
	h3 := slices.Contains(l.Protocols, config.ProtocolH3)
	tcp := slices.ContainsFunc(l.Protocols, func(p string) bool { return p != config.ProtocolH3 })
	udpAddress := l.Address
	var tcpDispatcher *dispatcher
	if tcp {
		if !shared {
			if err := checkFree(l.Address); err != nil {
				return fail(err)
			}
		}
		tcpDispatcher = &dispatcher{rt: s.rt, name: l.Name}
		if h3 {
			tcpDispatcher.announceH3()
		}
		var h fib.Handler = fibhttp.NewHandlerWithConfig(httpConfig(l), tcpDispatcher)
		if l.TLS != nil {
			// fib's TLS hands the HTTP handler plaintext, which serves HTTP/2
			// or HTTP/1.1 as ALPN chose.
			h = fibtls.NewServer(l.TLS, h)
		}
		var err error
		if b.index, b.addr, err = s.addBinding(fib.Binding{Addr: l.Address, Handler: h}); err != nil {
			return fail(err)
		}
		// HTTP/3 goes on the port TCP got, which is the one Alt-Svc names.
		host, _, _ := net.SplitHostPort(l.Address)
		udpAddress = net.JoinHostPort(host, strconv.Itoa(b.addr.(*net.TCPAddr).Port))
	}
	if h3 {
		if !shared {
			if err := checkFreeUDP(udpAddress); err != nil {
				return fail(err)
			}
		}
		h := http3.NewHandlerWithConfig(http3Config(l), &dispatcher{rt: s.rt, name: l.Name})
		var err error
		if b.udpIndex, b.udpAddr, err = s.addBinding(fib.Binding{Network: "udp", Addr: udpAddress, Handler: h}); err != nil {
			return fail(err)
		}
		if tcpDispatcher != nil {
			tcpDispatcher.setAltSvc(http3.AltSvc(b.udpAddr.(*net.UDPAddr).Port))
		}
	}
	if b.addr == nil {
		b.addr = b.udpAddr
	}
	return b, nil
}

// addBinding adds a binding to the engine and returns its index and the
// address it got.
func (s *Server) addBinding(fb fib.Binding) (int, net.Addr, error) {
	i, err := s.engine.AddBinding(fb)
	if err != nil {
		return -1, nil, err
	}
	addrs, err := s.engine.BindingAddrs(i)
	if err != nil || len(addrs) == 0 {
		_ = s.engine.RemoveBinding(i)
		return -1, nil, fmt.Errorf("no address bound: %v", err)
	}
	return i, addrs[0], nil
}

// unbind removes b's bindings at once.
func (s *Server) unbind(b *binding) {
	for _, i := range [...]int{b.index, b.udpIndex} {
		if i >= 0 {
			_ = s.engine.RemoveBinding(i)
		}
	}
}

// http3Config is the fib HTTP/3 config for a listener with tls.
func http3Config(l *runtime.Listener) http3.Config {
	c := http3.DefaultConfig()
	c.TLSConfig = http3.ConfigureTLS(l.TLS)
	c.StreamRequestBody = true
	c.StreamRequestBodyThreshold = StreamThreshold
	c.MaxHeaderBytes = l.MaxHeaderBytes
	c.MaxBodyBytes = l.MaxBodyBytes
	c.MaxStreamedBodyBytes = l.MaxBodyBytes
	return c
}

// Reload makes g the runtime's current graph, taking over the caller's
// reference to it, and moves the listeners to g's: those g has unchanged are
// kept, the new and changed ones bound first, and those g has no more drained
// once g is current. If a listener cannot be bound, nothing changes: the
// bindings made for g are removed, g is released, and the error returned. It
// returns the listeners it bound and those it removed.
func (s *Server) Reload(g *runtime.Graph) (added, removed []Addr, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept, fresh, gone, early []*binding
	next := make([]*binding, len(g.Listeners))
	for i, l := range g.Listeners {
		for _, b := range s.bound {
			if b.same(l) {
				next[i] = b
				kept = append(kept, b)
				break
			}
		}
	}
	for _, b := range s.bound {
		if !slices.Contains(kept, b) {
			gone = append(gone, b)
		}
	}
	for i, l := range g.Listeners {
		if next[i] != nil {
			continue
		}
		shared := slices.ContainsFunc(gone, func(b *binding) bool { return b.address == l.Address })
		b, err := s.bind(l, shared)
		if err != nil && shared {
			// A system without SO_REUSEPORT: the old listener stops listening
			// first. Its connections stay open and finish what they have, but
			// it cannot be put back if the reload fails after this.
			var old []*binding
			gone = slices.DeleteFunc(gone, func(b *binding) bool {
				if b.address == l.Address {
					old = append(old, b)
					return true
				}
				return false
			})
			s.retire(old, 0)
			early = append(early, old...)
			b, err = s.bind(l, false)
		}
		if err != nil {
			for _, b := range fresh {
				s.unbind(b)
			}
			s.bound = slices.DeleteFunc(s.bound, func(b *binding) bool { return slices.Contains(early, b) })
			g.Release()
			return nil, nil, err
		}
		next[i] = b
		fresh = append(fresh, b)
	}
	s.rt.Swap(g)
	s.bound = next
	s.retire(gone, s.drain)
	return addrsOf(fresh), addrsOf(gone), nil
}

// retire stops the listeners bs taking connections and lets the ones they
// have finish, for at most drain, in the background. With no drain, the
// listeners stop at once and their connections are left open, to end as
// they would.
func (s *Server) retire(bs []*binding, drain time.Duration) {
	for _, b := range bs {
		if drain <= 0 {
			s.unbind(b)
			continue
		}
		for _, i := range [...]int{b.index, b.udpIndex} {
			if i < 0 {
				continue
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), drain)
				defer cancel()
				_ = s.engine.ShutdownBinding(ctx, i)
			}()
		}
	}
}

func addrsOf(bs []*binding) []Addr {
	addrs := make([]Addr, len(bs))
	for i, b := range bs {
		addrs[i] = Addr{Name: b.name, Addr: b.addr, UDPAddr: b.udpAddr, Protocols: b.protocols}
	}
	return addrs
}

// checkFree fails when another socket already listens on address. fib
// listens with SO_REUSEPORT, so that its loops share the accepts, and the
// kernel would let a second process's listener share the address too,
// splitting connections between two servers without a word. A plain bind,
// which a reuseport listener refuses, tells. Port 0 picks a free port.
func checkFree(address string) error {
	if _, port, err := net.SplitHostPort(address); err == nil && port == "0" {
		return nil
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	return ln.Close()
}

// checkFreeUDP is checkFree for HTTP/3's UDP port.
func checkFreeUDP(address string) error {
	if _, port, err := net.SplitHostPort(address); err == nil && port == "0" {
		return nil
	}
	pc, err := net.ListenPacket("udp", address)
	if err != nil {
		return err
	}
	return pc.Close()
}

// StreamThreshold is the body size past which a request body streams to its
// handler rather than being read whole first.
const StreamThreshold = 64 << 10

// httpConfig is the fib HTTP config for a listener's TCP protocols.
func httpConfig(l *runtime.Listener) fibhttp.Config {
	protocols := l.Protocols
	c := fibhttp.DefaultConfig()
	// A body that streams is bounded as one read whole is: past the
	// threshold every body streams, and MaxBodyBytes alone would leave those
	// unbounded.
	c.MaxHeaderBytes = l.MaxHeaderBytes
	c.MaxBodyBytes = l.MaxBodyBytes
	c.MaxStreamedBodyBytes = l.MaxBodyBytes
	// A body past the threshold reaches its handler as it arrives, so that
	// the proxy can pass an upload on without holding it whole; smaller ones
	// arrive whole, as most do.
	c.StreamRequestBody = true
	c.StreamRequestBodyThreshold = StreamThreshold
	h1 := slices.Contains(protocols, config.ProtocolHTTP1)
	h2 := slices.Contains(protocols, config.ProtocolH2C) || slices.Contains(protocols, config.ProtocolH2)
	switch {
	case h1 && !h2:
		c.DisableHTTP2 = true
	case h2 && !h1:
		c.HTTP2Only = true
	}
	return c
}

// Addrs returns where each listener of the current graph listens, in the
// graph's order: with the port the kernel chose for an address whose port is
// 0.
func (s *Server) Addrs() []Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return addrsOf(s.bound)
}

// Shutdown stops taking connections and waits for the ones open to finish:
// idle connections are closed at once, and busy ones once the response in
// flight is written, with HTTP/2 GOAWAY. If ctx ends first, the connections
// still open are closed and ctx's error returned.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.engine.Shutdown(ctx)
	if runErr := s.close(); err == nil {
		err = runErr
	}
	return err
}

// Close stops the server and closes every connection at once.
func (s *Server) Close() error {
	s.engine.Stop()
	return s.close()
}

func (s *Server) close() error {
	err := <-s.running
	s.running <- err // for a later Close or Shutdown
	_ = s.engine.Close()
	return err
}

// dispatcher serves one listener's requests from whatever graph is current.
type dispatcher struct {
	rt   *runtime.Runtime
	name string
	// altSvc, for a listener that also serves HTTP/3, is the Alt-Svc value
	// announcing it, set once its port is known; addAltSvc is the hook that
	// adds it, made once so that registering it allocates nothing.
	altSvc    atomic.Pointer[[]string]
	addAltSvc func(int, stdhttp.Header)
	// found is where the listener was in the last graph d looked it up in,
	// so that the requests after it find it there at once, without
	// scanning the graph's listeners.
	found atomic.Pointer[listenerAt]
}

// listenerAt says a listener is the index'th of the graph of a version.
type listenerAt struct {
	version uint64
	index   int
}

// listener returns d's listener in g, or nil when g has none of its name.
// Only the first request on each graph scans g's listeners for it.
func (d *dispatcher) listener(g *runtime.Graph) *runtime.Listener {
	if at := d.found.Load(); at != nil && at.version == g.Version {
		return g.Listeners[at.index]
	}
	for i, l := range g.Listeners {
		if l.Name == d.name {
			d.found.Store(&listenerAt{version: g.Version, index: i})
			return l
		}
	}
	return nil
}

// announceH3 has d add an Alt-Svc field to every response, once setAltSvc
// has said what.
func (d *dispatcher) announceH3() {
	d.addAltSvc = func(_ int, h stdhttp.Header) {
		if v := d.altSvc.Load(); v != nil {
			h["Alt-Svc"] = *v
		}
	}
}

func (d *dispatcher) setAltSvc(value string) { d.altSvc.Store(&[]string{value}) }

var notFound = []byte("404 page not found\n")

func (d *dispatcher) ServeHTTP(c *fibhttp.Context) {
	g := d.rt.Current()
	if g == nil {
		_ = c.Respond(stdhttp.StatusServiceUnavailable, "", nil)
		return
	}
	defer g.Release()
	if d.addAltSvc != nil {
		c.OnHeader(d.addAltSvc)
	}
	var route *runtime.Route
	if l := d.listener(g); l != nil {
		route, _ = l.Router.Match(c.Request.Host, c.Request.URL.Path)
	}
	if log := g.AccessLog; log != nil {
		d.logRequest(c, log, route)
	}
	if route == nil {
		_ = c.Respond(stdhttp.StatusNotFound, "text/plain; charset=utf-8", notFound)
		return
	}
	route.Handler.ServeHTTP(c)
}

// logRequest has the access log write a line for c's request once its
// response is finished. The log outlives the graph, so a response finished
// after the graph is released still has somewhere to go.
func (d *dispatcher) logRequest(c *fibhttp.Context, log *observe.AccessLog, route *runtime.Route) {
	r := c.Request
	e := observe.Track(c)
	e.Start, e.Listener, e.BytesIn = time.Now(), d.name, r.ContentLength
	if body := c.Body(); body != nil {
		e.BytesIn = int64(len(body))
	} else if e.BytesIn < 0 {
		e.BytesIn = 0
	}
	if route != nil {
		e.Route = route.Name
	}
	c.OnFinish(func(status int, _ stdhttp.Header, size int64) { log.Log(e, r, status, size) })
}
