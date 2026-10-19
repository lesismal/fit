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
	"net"
	stdhttp "net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
)

// The defaults Config's fields stand for when left out.
const (
	DefaultMaxIdleConns   = 32
	DefaultIdleTimeout    = 60 * time.Second
	DefaultConnectTimeout = 10 * time.Second
)

// Config is an upstream: the addresses requests to it go to, and how the
// connections to them are kept.
type Config struct {
	Addresses      []string        `json:"addresses" fit:"required" doc:"The addresses of the upstream's servers, each a host and a port, as in \"10.0.0.1:8080\" or \"backend:80\"."`
	MaxConns       int             `json:"max_conns,omitempty" min:"0" doc:"The most connections open, or being opened, to one address; a request past it waits for one to be free. No limit when left out."`
	MaxIdleConns   *int            `json:"max_idle_conns,omitempty" min:"0" doc:"How many idle connections to one address are kept for later requests; 32 when left out, and 0 keeps none."`
	IdleTimeout    config.Duration `json:"idle_timeout,omitempty" doc:"How long an idle connection is kept before it is closed; 60s when left out."`
	ConnectTimeout config.Duration `json:"connect_timeout,omitempty" doc:"How long opening a connection may take; 10s when left out."`

	At config.Value `json:"-" fit:"self"`
}

// Check reports what is wrong with c that its types do not catch, with the
// path and position of each mistake.
func (c *Config) Check() error {
	var errs config.Errors
	if len(c.Addresses) == 0 {
		errs = append(errs, c.At.Field("addresses").Errorf("an upstream needs at least one address"))
	}
	seen := make(map[string]bool, len(c.Addresses))
	for i, addr := range c.Addresses {
		at := c.At.Field("addresses").Index(i)
		if msg := checkAddress(addr); msg != "" {
			errs = append(errs, at.Errorf("%s", msg))
			continue
		}
		if seen[addr] {
			errs = append(errs, at.Errorf("%q is listed twice", addr))
		}
		seen[addr] = true
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// checkAddress says what is wrong with addr as an upstream's address, or
// returns "".
func checkAddress(addr string) string {
	if strings.Contains(addr, "://") {
		return addr + " is a URL; write the host and port alone, as in \"10.0.0.1:8080\""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return strconv.Quote(addr) + " is not a host and port, as in \"10.0.0.1:8080\""
	}
	if host == "" {
		return strconv.Quote(addr) + " has no host"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return strconv.Quote(addr) + " has no valid port"
	}
	return ""
}

// Pool sends requests to an upstream's addresses, over connections it keeps.
// It is safe for concurrent use.
type Pool struct {
	client    *fibhttp.Client
	addresses []string
	next      atomic.Uint32
}

// New returns a pool for the upstream c, which Check has passed, dialing
// through engine, which must be running. Close the pool before the engine.
func New(engine *fib.Engine, c *Config) *Pool {
	cc := fibhttp.DefaultClientConfig()
	// A request is bounded by the caller's context; the proxy's timeouts
	// come with I17.
	cc.Timeout = 0
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
	return &Pool{client: fibhttp.NewClient(engine, cc), addresses: c.Addresses}
}

// Addresses returns the upstream's addresses. The caller must not change
// them.
func (p *Pool) Addresses() []string { return p.addresses }

// Do sends req to one of the upstream's addresses, taken in turn, and calls
// callback exactly once with the response or the reason there is none. It
// sets req.URL's scheme and host to the address and leaves the rest of the
// request, its Host field included, as it is. See fib's Client.Do for how the
// body is sent and the callback run: the callback must not block.
func (p *Pool) Do(req *stdhttp.Request, callback func(*fibhttp.ClientResponse, error)) {
	addr := p.addresses[0]
	if n := len(p.addresses); n > 1 {
		addr = p.addresses[int((p.next.Add(1)-1)%uint32(n))]
	}
	req.URL.Scheme = "http"
	req.URL.Host = addr
	p.client.Do(req, callback)
}

// Close stops the pool taking requests and closes its idle connections.
// Requests in flight finish as they would have; those still waiting for a
// connection fail.
func (p *Pool) Close() { p.client.Close() }
