// Package forward is the forward_proxy handler: an HTTP forward proxy for
// CONNECT, over HTTP/1.1, HTTP/2 and HTTP/3, which opens a tunnel to the
// host and port a request names, for the clients and to the destinations
// its rules allow.
package forward

import (
	stdhttp "net/http"
	"net/netip"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/l4"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".forward_proxy", New: func() any { return new(ForwardProxy) }, Doc: "A forward proxy for HTTP CONNECT, with access rules for clients and destinations."})
}

// ForwardProxy serves CONNECT requests, as config.ForwardProxy's rules
// allow. A request that is not a CONNECT, an extended CONNECT among them,
// is answered 405.
type ForwardProxy struct {
	Clients        []string        `json:"clients" fit:"required" doc:"The addresses and CIDR ranges of the clients that may use the proxy, as \"10.0.0.0/8\"; others are answered 403."`
	Allow          []string        `json:"allow,omitempty" doc:"The destinations the proxy connects to: host names, \"*.example.com\" for every name below example.com, IP addresses and CIDR ranges, which also hold the address a host name resolves to. Every destination deny does not hold when left out; others are answered 403, or 502 for a host name whose address decides."`
	Deny           []string        `json:"deny,omitempty" doc:"Destinations refused, written as in allow, whatever allow holds."`
	Ports          []int           `json:"ports,omitempty" doc:"The destination ports allowed; any when left out."`
	ConnectTimeout config.Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long resolving and connecting to a destination may take; past it the request is answered 504. 10s when left out."`
	IdleTimeout    config.Duration `json:"idle_timeout,omitempty" default:"\"5m\"" doc:"How long a tunnel may carry nothing either way before it and its connection to the destination are closed. 5m when left out."`

	proxy *fibhttp.ConnectProxy
}

// errRefused answers a request the rules refuse, 403.
var errRefused = l4.ErrRefused

// Provision checks the rules and builds the proxy.
func (f *ForwardProxy) Provision(ctx *module.Context) error {
	c := &config.ForwardProxy{Clients: f.Clients, Allow: f.Allow, Deny: f.Deny, Ports: f.Ports,
		ConnectTimeout: f.ConnectTimeout, IdleTimeout: f.IdleTimeout, At: ctx.Params}
	if errs := c.Check(); len(errs) > 0 {
		return errs
	}
	rules := l4.NewRules(c)
	f.proxy = fibhttp.NewConnectProxy(fibhttp.ConnectProxyConfig{
		Allow: func(r *stdhttp.Request) error {
			client, err := netip.ParseAddrPort(r.RemoteAddr)
			if err != nil || !rules.Client(client.Addr()) || !rules.Allows(r.URL.Host) {
				return errRefused
			}
			return nil
		},
		// Dial refuses a host name whose address the rules refuse, which is
		// answered 502.
		Dial:        rules.Dial,
		DialTimeout: rules.ConnectTimeout(),
		IdleTimeout: rules.IdleTimeout(),
	})
	return nil
}

// ServeHTTP serves a CONNECT. It returns while the destination is dialed,
// and the dial's callback answers the request.
func (f *ForwardProxy) ServeHTTP(c *fibhttp.Context) { f.proxy.ServeHTTP(c) }
