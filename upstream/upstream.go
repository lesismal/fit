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
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	stdhttp "net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
)

// The defaults config.Upstream's fields stand for when left out.
const (
	DefaultMaxIdleConns          = 32
	DefaultIdleTimeout           = 60 * time.Second
	DefaultConnectTimeout        = 10 * time.Second
	DefaultResponseHeaderTimeout = 60 * time.Second
	DefaultResponseIdleTimeout   = 60 * time.Second
	DefaultMaxFails              = 1
	DefaultFailTimeout           = 10 * time.Second
	DefaultMaxConns              = 256
	DefaultMaxPending            = 1024
	DefaultResponseTimeout       = time.Hour
	// StreamThreshold is the response body size past which a body streams
	// to the callback rather than arriving whole.
	StreamThreshold = 64 << 10
)

// ErrNoEngine is what a request through a pool made without an engine gets.
var ErrNoEngine = errors.New("upstream: the pool has no engine to dial through")

// ErrClosed is what a request gets that is sent through a pool after its
// last hold was given back with Close. The proxy answers it with 503, and it
// counts against no backend's health.
var ErrClosed = errors.New("upstream: the pool is closed")

// ErrBusy is what a request gets when max_pending requests already wait for
// a connection to its address: fib's client refuses it at once. The proxy
// answers it with 503, and it counts against no backend's health.
var ErrBusy = fibhttp.ErrTooManyPending

// Pool sends requests to an upstream's addresses, over connections it keeps.
// It is safe for concurrent use.
type Pool struct {
	name      string
	client    *fibhttp.Client // nil without an engine
	scheme    string          // http, or https for h2
	addresses []string
	backends  []*Backend
	balancer  Balancer
	sticky    *sticky // nil without a sticky cookie
	timeouts  Timeouts
	// maxFails failures within failTimeout take a backend out of rotation
	// for failTimeout.
	maxFails    int
	failTimeout time.Duration
	// prober runs the active health check, or is nil without one.
	prober *prober

	// inFlight counts requests picked and not yet done, and closing says
	// Close was called: the client is closed once there are none.
	inFlight atomic.Int64
	closing  atomic.Bool
	// responses times the responses' headers, or their failures, from the
	// requests' start.
	responses observe.Histogram
	closeOnce sync.Once
	// users counts the graphs that use the pool: New's caller, and one more
	// for each Acquire. Close gives one back.
	users atomic.Int64
}

// New returns a pool for the upstream c, which config.Config.Validate has
// passed, dialing through engine, which must be running. A nil engine makes a
// pool that fails every request with ErrNoEngine, for a config that is only
// checked. Close the pool before stopping the engine. Its error, about the
// TLS settings, carries the path and position of the field.
func New(engine *fib.Engine, c *config.Upstream) (*Pool, error) {
	p := &Pool{scheme: "http", name: c.Name, addresses: c.Addresses, timeouts: Timeouts{
		ResponseHeader: orDefault(c.ResponseHeaderTimeout.Std(), DefaultResponseHeaderTimeout),
		ResponseIdle:   orDefault(c.ResponseIdleTimeout.Std(), DefaultResponseIdleTimeout),
	}}
	p.users.Store(1)
	for i, addr := range c.Addresses {
		weight := 1
		if i < len(c.Weights) {
			weight = c.Weights[i]
		}
		p.backends = append(p.backends, &Backend{Address: addr, Weight: weight})
	}
	p.balancer = NewBalancer(c.LoadBalancing, p.backends, c.HashKey)
	if c.StickyCookie != nil {
		p.sticky = newSticky(c.StickyCookie, p.backends)
	}
	p.maxFails = DefaultMaxFails
	if c.MaxFails != nil {
		p.maxFails = *c.MaxFails
	}
	p.failTimeout = orDefault(c.FailTimeout.Std(), DefaultFailTimeout)
	maxConns := c.MaxConns
	if maxConns <= 0 {
		maxConns = DefaultMaxConns
	}
	maxPending := c.MaxPending
	if maxPending <= 0 {
		maxPending = DefaultMaxPending
	}
	var tlsConfig *tls.Config
	if c.Protocol == "h2" {
		p.scheme = "https"
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.TLSServerName}
		if c.TLSCAFile != "" {
			pem, err := os.ReadFile(c.TLSCAFile)
			if err != nil {
				return nil, c.At.Field("tls_ca_file").Errorf("%v", err)
			}
			tlsConfig.RootCAs = x509.NewCertPool()
			if !tlsConfig.RootCAs.AppendCertsFromPEM(pem) {
				return nil, c.At.Field("tls_ca_file").Errorf("%s holds no PEM certificate", c.TLSCAFile)
			}
		}
	}
	if engine == nil {
		return p, nil
	}
	cc := fibhttp.DefaultClientConfig()
	// The whole exchange, body included; the proxy bounds its parts.
	cc.Timeout = orDefault(c.ResponseTimeout.Std(), DefaultResponseTimeout)
	// A body past the threshold is handed over as it arrives, paced by its
	// reader, rather than buffered whole; smaller ones arrive whole.
	cc.StreamResponseBody = true
	cc.StreamResponseBodyThreshold = StreamThreshold
	// A body of no declared length is handed over from its header, so that
	// one that trickles, as server-sent events do, is passed on as it comes.
	cc.StreamUnknownLengthAtHeader = true
	cc.MaxConnsPerHost = maxConns
	// Past max_pending waiting for a connection to an address, fib's client
	// refuses a request at once with ErrBusy.
	cc.MaxPendingPerHost = maxPending
	// The client bounds the wait for the response's header, from the
	// request's end, with the request's one timer.
	cc.ResponseHeaderTimeout = p.timeouts.ResponseHeader
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
	// h2c speaks HTTP/2 to http:// addresses with prior knowledge; h2 offers
	// h2 and http/1.1 through ALPN, which the client does when its TLS
	// config names no protocols. HTTP/2 multiplexes requests over each
	// connection, up to the streams the server allows at once, and max_conns
	// bounds the connections.
	cc.UnencryptedHTTP2 = c.Protocol == "h2c"
	cc.TLSConfig = tlsConfig
	p.client = fibhttp.NewClient(engine, cc)
	if c.HealthCheck != nil {
		p.prober = newProber(engine, c, p.scheme, tlsConfig, p.backends)
		p.prober.start()
	}
	return p, nil
}

// Timeouts are how long a proxy waits for parts of an upstream's response.
// The pool's client bounds the wait for the header; the proxy bounds the
// silences in a body, since it is the one that knows when one has begun.
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

// Pick chooses the backend for r by the upstream's sticky cookie and policy,
// and counts the request as under way on it until Done. r may be nil, for a
// request with nothing to go by. Every Pick needs its Done, even one whose
// Send fails at once, as it does with ErrClosed.
func (p *Pool) Pick(r *stdhttp.Request) *Backend {
	p.inFlight.Add(1)
	var b *Backend
	if p.sticky != nil {
		b = p.sticky.pick(r)
	}
	if b == nil {
		b = p.balancer.Pick(r)
	}
	b.active.Add(1)
	return b
}

// SetCookie returns the Set-Cookie field a response to r, which went to b,
// carries: the sticky cookie naming b, unless r's names it already; or ""
// without a sticky cookie.
func (p *Pool) SetCookie(r *stdhttp.Request, b *Backend) string {
	if p.sticky == nil || (r != nil && cookieValue(r.Header, p.sticky.name) == p.sticky.ids[b]) {
		return ""
	}
	return p.sticky.setCookie[b]
}

// Done ends the request Pick counted on b, once its response, body and all,
// is over or has failed. It must be called exactly once a Pick. failed says
// the upstream failed it — could not be reached, broke off, or timed out —
// which counts towards taking b out of rotation; a request the client gave
// up on, or whose body the client failed to send, is no failure of b's.
func (p *Pool) Done(b *Backend, failed bool) {
	if failed {
		b.failures.Add(1)
		b.failed(p.maxFails, p.failTimeout)
	} else {
		b.succeeded()
	}
	b.requests.Add(1)
	b.active.Add(-1)
	p.done()
}

// ObserveResponse counts the time d a request took to get its response's
// header, or to fail without one; hint is observe.Histogram.Observe's.
func (p *Pool) ObserveResponse(hint uintptr, d time.Duration) { p.responses.Observe(hint, d) }

// ResponseTimes returns the times ObserveResponse counted.
func (p *Pool) ResponseTimes() observe.HistogramSnapshot { return p.responses.Snapshot() }

// InFlight returns how many requests the pool has picked a backend for and
// are not done.
func (p *Pool) InFlight() int64 { return p.inFlight.Load() }

// done takes a request off the count, and closes a pool that Close was
// called on once the count is down to none.
func (p *Pool) done() {
	if p.inFlight.Add(-1) == 0 && p.closing.Load() {
		p.closeClient()
	}
}

// Send sends req to b and calls callback exactly once with the response or
// the reason there is none, and returns the Call that abandons it. It sets
// req.URL's scheme and host to b's address and leaves the rest of the
// request, its Host field included, as it is. See fib's Client.Start for how
// the body is sent and the callback run: the callback must not block. A
// pool whose last hold was given back fails it at once with ErrClosed; Pick
// counted it first, so that Close either sees it under way or it sees Close.
func (p *Pool) Send(b *Backend, req *stdhttp.Request, callback func(*fibhttp.ClientResponse, error)) fibhttp.Call {
	if p.client == nil {
		callback(nil, ErrNoEngine)
		return fibhttp.Call{}
	}
	if p.closing.Load() {
		callback(nil, ErrClosed)
		return fibhttp.Call{}
	}
	req.URL.Scheme = p.scheme
	req.URL.Host = b.Address
	return p.client.Start(req, callback)
}

// Do is Pick, Send and Done for a caller that is done with the response
// when its callback returns, as one that reads a body that arrived whole is.
// An error counts as a failure of the backend, but for ErrBusy, ErrClosed
// and a cancelled request.
func (p *Pool) Do(req *stdhttp.Request, callback func(*fibhttp.ClientResponse, error)) {
	b := p.Pick(req)
	p.Send(b, req, func(resp *fibhttp.ClientResponse, err error) {
		callback(resp, err)
		p.Done(b, err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrBusy) && !errors.Is(err, ErrClosed))
	})
}

// Acquire takes another hold on p, for a graph that shares it with the one
// that made it, as a reload does with an upstream it leaves unchanged: its
// connections, and its backends' health, carry over. Each hold is given
// back with Close.
func (p *Pool) Acquire() { p.users.Add(1) }

// Close gives back a hold on the pool, as a graph's cleanup does, and the
// last one closes it once the requests in flight are done: a request sent
// before then still finishes, its response and body arriving later. Requests
// sent after it fail at once with ErrClosed, even while others are still in
// flight, and the idle connections are closed once those are done. Probes
// stop at once. Closing more often
// than the pool is held does nothing more.
func (p *Pool) Close() {
	if p.users.Add(-1) > 0 {
		return // another graph still uses it
	}
	if p.prober != nil {
		p.prober.stop()
	}
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
