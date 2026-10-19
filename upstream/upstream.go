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
// The pool spreads requests over its addresses in turn. Load balancing
// (I19) and health checks (I20) replace that with a selection of their own.
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
	DefaultMaxIdleConns   = 32
	DefaultIdleTimeout    = 60 * time.Second
	DefaultConnectTimeout = 10 * time.Second
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
	next      atomic.Uint32

	// inFlight counts requests whose callback has not returned, and closing
	// says Close was called: the client is closed once both hold.
	inFlight  atomic.Int64
	closing   atomic.Bool
	closeOnce sync.Once
}

// New returns a pool for the upstream c, which config.Config.Validate has
// passed, dialing through engine, which must be running. A nil engine makes a
// pool that fails every request with ErrNoEngine, for a config that is only
// checked. Close the pool before stopping the engine.
func New(engine *fib.Engine, c *config.Upstream) *Pool {
	p := &Pool{name: c.Name, addresses: c.Addresses}
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

// Name returns the upstream's name.
func (p *Pool) Name() string { return p.name }

// Addresses returns the upstream's addresses. The caller must not change
// them.
func (p *Pool) Addresses() []string { return p.addresses }

// Do sends req to one of the upstream's addresses, taken in turn, and calls
// callback exactly once with the response or the reason there is none. It
// sets req.URL's scheme and host to the address and leaves the rest of the
// request, its Host field included, as it is. See fib's Client.Do for how the
// body is sent and the callback run: the callback must not block. A pool
// that Close was called on still takes requests until those in flight are
// done, so that a request that started on it can still use it.
func (p *Pool) Do(req *stdhttp.Request, callback func(*fibhttp.ClientResponse, error)) {
	if p.client == nil {
		callback(nil, ErrNoEngine)
		return
	}
	addr := p.addresses[0]
	if n := len(p.addresses); n > 1 {
		addr = p.addresses[int((p.next.Add(1)-1)%uint32(n))]
	}
	req.URL.Scheme = "http"
	req.URL.Host = addr
	p.inFlight.Add(1)
	p.client.Do(req, func(resp *fibhttp.ClientResponse, err error) {
		callback(resp, err)
		if p.inFlight.Add(-1) == 0 && p.closing.Load() {
			p.closeClient()
		}
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
