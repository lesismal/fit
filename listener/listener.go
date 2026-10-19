// Package listener binds the listeners of a runtime graph on one fib engine
// and dispatches the requests they take to the graph's routes.
//
// Every listener is a binding of the same engine, so all of them share one
// event loop, task pool and buffer pool. A request takes a reference to the
// graph that is current when it arrives and keeps it until its handler
// returns, so a graph swapped in later serves only the requests that start
// after the swap.
package listener

import (
	"context"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"slices"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// Server serves the listeners of a Runtime's graph.
type Server struct {
	engine  *fib.Engine
	addrs   []Addr
	running chan error
}

// Addr is where a listener listens.
type Addr struct {
	Name      string
	Addr      net.Addr
	Protocols []string
}

// Start binds every listener of rt's current graph and starts serving them.
// The listeners are fixed from then on: a graph swapped in later changes the
// routes of the listeners it shares with this one by name, and a listener it
// lacks answers 404.
func Start(rt *runtime.Runtime) (*Server, error) {
	g := rt.Current()
	if g == nil {
		return nil, errors.New("listener: the runtime has no graph")
	}
	defer g.Release()

	cfg := fib.DefaultConfig()
	cfg.Name = "fit"
	bindings := make([]fib.Binding, len(g.Listeners))
	for i, l := range g.Listeners {
		bindings[i] = fib.Binding{
			Addr:    l.Address,
			Handler: fibhttp.NewHandlerWithConfig(httpConfig(l.Protocols), &dispatcher{rt: rt, name: l.Name}),
		}
	}
	for _, l := range g.Listeners {
		if err := checkFree(l.Address); err != nil {
			return nil, fmt.Errorf("listener %q: %w", l.Name, err)
		}
	}
	engine, err := fib.New(cfg, bindings...)
	if err != nil {
		return nil, err
	}

	s := &Server{engine: engine, running: make(chan error, 1)}
	for i, l := range g.Listeners {
		addrs, err := engine.BindingAddrs(i)
		if err != nil || len(addrs) == 0 {
			_ = engine.Close()
			return nil, fmt.Errorf("listener %q: no address bound: %v", l.Name, err)
		}
		s.addrs = append(s.addrs, Addr{Name: l.Name, Addr: addrs[0], Protocols: l.Protocols})
	}
	go func() { s.running <- engine.Run() }()
	return s, nil
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

// Addrs returns where each listener listens, in the graph's order: with the
// port the kernel chose for an address whose port is 0.
func (s *Server) Addrs() []Addr { return s.addrs }

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
