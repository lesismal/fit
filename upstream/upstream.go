// Package upstream keeps the connections fit makes to the servers it
// proxies to.
//
// An upstream is a set of addresses and the limits on the connections to
// them. Its Pool sends requests over HTTP/1.1 through fib's asynchronous
// client, which keeps the connections to each address alive between requests
// and reuses them, caps how many are open to one address (requests past the
// cap wait for one), keeps a number of them idle, and closes one that stays
// idle too long. Nothing waits on the network on the caller's goroutine: a
// response, or the reason there is none, arrives in a callback.
//
// The pool spreads requests over its addresses by its upstream's load
// balancing policy: round robin, smooth weighted round robin, or least
// connections; see Balancer.
package upstream

import (
	"errors"
	stdhttp "net/http"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
)

// The defaults config.Upstream's fields stand for when left out.
const (
	DefaultMaxIdleConns          = 32
	DefaultIdleTimeout           = 60 * time.Second
	DefaultConnectTimeout        = 10 * time.Second
	DefaultResponseHeaderTimeout = 60 * time.Second
	DefaultResponseIdleTimeout   = 60 * time.Second
	// StreamThreshold is the response body size past which a body streams
	// to the callback rather than arriving whole.
	StreamThreshold = 64 << 10
)

// ErrNoEngine is what a request through a pool made without an engine gets.
var ErrNoEngine = errors.New("upstream: the pool has no engine to dial through")

// Pool sends requests to an upstream's addresses, over connections it keeps.
// It is safe for concurrent use.
type Pool struct {
	name      string
	client    *fibhttp.Client // nil without an engine
	addresses []string
	backends  []*Backend
	balancer  Balancer
	timeouts  Timeouts

	// inFlight counts requests picked and not yet done, and closing says
	// Close was called: the client is closed once there are none.
	inFlight  atomic.Int64
	closing   atomic.Bool
	closeOnce sync.Once
}

// New returns a pool for the upstream c, which config.Config.Validate has
// passed, dialing through engine, which must be running. A nil engine makes a
// pool that fails every request with ErrNoEngine, for a config that is only
// checked. Close the pool before stopping the engine.
func New(engine *fib.Engine, c *config.Upstream) *Pool {
	p := &Pool{name: c.Name, addresses: c.Addresses, timeouts: Timeouts{
		ResponseHeader: orDefault(c.ResponseHeaderTimeout.Std(), DefaultResponseHeaderTimeout),
		ResponseIdle:   orDefault(c.ResponseIdleTimeout.Std(), DefaultResponseIdleTimeout),
	}}
	for i, addr := range c.Addresses {
		weight := 1
		if i < len(c.Weights) {
			weight = c.Weights[i]
		}
		p.backends = append(p.backends, &Backend{Address: addr, Weight: weight})
	}
	p.balancer = NewBalancer(c.LoadBalancing, p.backends)
	if engine == nil {
		return p
	}
	cc := fibhttp.DefaultClientConfig()
	// A request is bounded by the caller's context; the proxy's timeouts
	// come with I17.
	cc.Timeout = 0
	// A body past the threshold is handed over as it arrives, paced by its
	// reader, rather than buffered whole; smaller ones arrive whole.
	cc.StreamResponseBody = true
	cc.StreamResponseBodyThreshold = StreamThreshold
	cc.MaxConnsPerHost = c.MaxConns
	cc.MaxIdleConnsPerHost = DefaultMaxIdleConns
	if c.MaxIdleConns != nil {
		cc.MaxIdleConnsPerHost = *c.MaxIdleConns
	}
	cc.IdleConnTimeout = c.IdleTimeout.Std()
	if cc.IdleConnTimeout == 0 {
		cc.IdleConnTimeout = DefaultIdleTimeout
	}
	cc.DialTimeout = c.ConnectTimeout.Std()
	if cc.DialTimeout == 0 {
		cc.DialTimeout = DefaultConnectTimeout
	}
	p.client = fibhttp.NewClient(engine, cc)
	return p
}

// Timeouts are how long a proxy waits for parts of an upstream's response.
// The pool does not enforce them; the proxy does, since it is the one that
// knows when a response has begun and a body has gone quiet.
type Timeouts struct {
	ResponseHeader time.Duration
	ResponseIdle   time.Duration
}

func orDefault(d, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d
}

// Timeouts returns the upstream's response timeouts.
func (p *Pool) Timeouts() Timeouts { return p.timeouts }

// Name returns the upstream's name.
func (p *Pool) Name() string { return p.name }

// Addresses returns the upstream's addresses. The caller must not change
// them.
func (p *Pool) Addresses() []string { return p.addresses }

// Backends returns the upstream's backends, in the order of its addresses.
// The caller must not change them.
func (p *Pool) Backends() []*Backend { return p.backends }

// Pick chooses the backend for a request by the upstream's policy and counts
// the request as under way on it until Done. A pool that Close was called on
// still serves requests until those under way are done, so that a request
// that started on it can still use it.
func (p *Pool) Pick() *Backend {
	b := p.balancer.Pick()
	b.active.Add(1)
	p.inFlight.Add(1)
	return b
}

// Done ends the request Pick counted on b, once its response, body and all,
// is over or has failed. It must be called exactly once a Pick.
func (p *Pool) Done(b *Backend) {
	b.active.Add(-1)
	if p.inFlight.Add(-1) == 0 && p.closing.Load() {
		p.closeClient()
	}
}

// Send sends req to b and calls callback exactly once with the response or
// the reason there is none. It sets req.URL's scheme and host to b's address
// and leaves the rest of the request, its Host field included, as it is. See
// fib's Client.Do for how the body is sent and the callback run: the
// callback must not block.
func (p *Pool) Send(b *Backend, req *stdhttp.Request, callback func(*fibhttp.ClientResponse, error)) {
	if p.client == nil {
		callback(nil, ErrNoEngine)
		return
	}
	req.URL.Scheme = "http"
	req.URL.Host = b.Address
	p.client.Do(req, callback)
}

// Do is Pick, Send and Done for a caller that is done with the response
// when its callback returns, as one that reads a body that arrived whole is.
func (p *Pool) Do(req *stdhttp.Request, callback func(*fibhttp.ClientResponse, error)) {
	b := p.Pick()
	p.Send(b, req, func(resp *fibhttp.ClientResponse, err error) {
		callback(resp, err)
		p.Done(b)
	})
}

// Close closes the pool once the requests in flight are done, as a graph's
// cleanup does when the last request using the graph has returned: a request
// whose response arrives later can still finish. The idle connections are
// closed then, and later requests fail.
func (p *Pool) Close() {
	p.closing.Store(true)
	if p.inFlight.Load() == 0 {
		p.closeClient()
	}
}

func (p *Pool) closeClient() {
	if p.client != nil {
		p.closeOnce.Do(p.client.Close)
	}
}

// StartEngine starts a fib engine with no listener, for pools to dial
// through, and returns it with the func that stops it. Stop it after closing
// the pools.
func StartEngine() (*fib.Engine, func(), error) {
	cfg := fib.DefaultConfig()
	cfg.Name = "fit-upstream"
	engine, err := fib.New(cfg)
	if err != nil {
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run() }()
	stop := func() {
		engine.Stop()
		<-done
		_ = engine.Close()
	}
	return engine, stop, nil
}
