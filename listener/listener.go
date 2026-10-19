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
	"sync"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

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
	index     int
	name      string
	address   string
	protocols []string
	addr      net.Addr
}

// same reports whether b serves the listener l as it is.
func (b *binding) same(l *runtime.Listener) bool {
	return b.name == l.Name && b.address == l.Address && slices.Equal(b.protocols, l.Protocols)
}

// Addr is where a listener listens.
type Addr struct {
	Name      string
	Addr      net.Addr
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

// SetDrainTimeout sets how long a listener a reload removed may take to
// finish the requests it has before its connections are closed.
func (s *Server) SetDrainTimeout(d time.Duration) {
	s.mu.Lock()
	s.drain = d
	s.mu.Unlock()
}

// bind has the engine serve l. shared says another of the server's own
// bindings may be listening on the address, which fib's SO_REUSEPORT lets
// the two share while one replaces the other.
func (s *Server) bind(l *runtime.Listener, shared bool) (*binding, error) {
	if !shared {
		if err := checkFree(l.Address); err != nil {
			return nil, fmt.Errorf("listener %q: %w", l.Name, err)
		}
	}
	h := fibhttp.NewHandlerWithConfig(httpConfig(l.Protocols), &dispatcher{rt: s.rt, name: l.Name})
	i, err := s.engine.AddBinding(fib.Binding{Addr: l.Address, Handler: h})
	if err != nil {
		return nil, fmt.Errorf("listener %q: %w", l.Name, err)
	}
	addrs, err := s.engine.BindingAddrs(i)
	if err != nil || len(addrs) == 0 {
		_ = s.engine.RemoveBinding(i)
		return nil, fmt.Errorf("listener %q: no address bound: %v", l.Name, err)
	}
	return &binding{index: i, name: l.Name, address: l.Address, protocols: l.Protocols, addr: addrs[0]}, nil
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
				_ = s.engine.RemoveBinding(b.index)
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
			_ = s.engine.RemoveBinding(b.index)
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), drain)
			defer cancel()
			_ = s.engine.ShutdownBinding(ctx, b.index)
		}()
	}
}

func addrsOf(bs []*binding) []Addr {
	addrs := make([]Addr, len(bs))
	for i, b := range bs {
		addrs[i] = Addr{Name: b.name, Addr: b.addr, Protocols: b.protocols}
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

// StreamThreshold is the body size past which a request body streams to its
// handler rather than being read whole first.
const StreamThreshold = 64 << 10

// httpConfig is the fib HTTP config for a listener serving protocols.
func httpConfig(protocols []string) fibhttp.Config {
	c := fibhttp.DefaultConfig()
	// A body past the threshold reaches its handler as it arrives, so that
	// the proxy can pass an upload on without holding it whole; smaller ones
	// arrive whole, as most do.
	c.StreamRequestBody = true
	c.StreamRequestBodyThreshold = StreamThreshold
	h1 := slices.Contains(protocols, config.ProtocolHTTP1)
	h2c := slices.Contains(protocols, config.ProtocolH2C)
	switch {
	case h1 && !h2c:
		c.DisableHTTP2 = true
	case h2c && !h1:
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
}

var notFound = []byte("404 page not found\n")

func (d *dispatcher) ServeHTTP(c *fibhttp.Context) {
	g := d.rt.Current()
	if g == nil {
		_ = c.Respond(stdhttp.StatusServiceUnavailable, "", nil)
		return
	}
	defer g.Release()
	var route *runtime.Route
	for _, l := range g.Listeners {
		if l.Name == d.name {
			route, _ = l.Router.Match(c.Request.Host, c.Request.URL.Path)
			break
		}
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
	e := &observe.Entry{Start: time.Now(), Listener: d.name, BytesIn: r.ContentLength}
	if body := c.Body(); body != nil {
		e.BytesIn = int64(len(body))
	} else if e.BytesIn < 0 {
		e.BytesIn = 0
	}
	if route != nil {
		e.Route = route.Name
	}
	observe.Attach(r, e)
	c.OnFinish(func(status int, _ stdhttp.Header, size int64) { log.Log(e, r, status, size) })
}
