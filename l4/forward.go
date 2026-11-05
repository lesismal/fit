package l4

import (
	"encoding/json"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/socks"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/netguard"
	"github.com/lesismal/fit/router"
)

// ErrRefused is how a dial to a destination the rules refuse fails, once
// the address a host name resolved to turns out to be one of those.
var ErrRefused = &socks.ReplyError{Version: socks.Version5, Code: socks.ReplyNotAllowed}

// Rules are who may use a forward proxy, SOCKS or HTTP CONNECT, and where
// it may connect them to, as config.ForwardProxy has them.
type Rules struct {
	clients     Trusted
	allow, deny dests
	ports       []uint16
	connect     time.Duration
	idle        time.Duration
	key         string
}

// dests are destinations: host names, the suffixes of wildcards
// (".example.com") and address ranges.
type dests struct {
	names    map[string]bool
	suffixes []string
	nets     []netip.Prefix
}

func (d *dests) add(s string) {
	if p, err := router.ParsePrefix(s); err == nil {
		d.nets = append(d.nets, p)
		return
	}
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if rest, ok := strings.CutPrefix(s, "*"); ok {
		d.suffixes = append(d.suffixes, rest)
		return
	}
	if d.names == nil {
		d.names = map[string]bool{}
	}
	d.names[s] = true
}

func (d *dests) empty() bool { return d.names == nil && d.suffixes == nil && d.nets == nil }

// name reports whether d holds the host name host, in lower case.
func (d *dests) name(host string) bool {
	if d.names[host] {
		return true
	}
	for _, s := range d.suffixes {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// addr reports whether d holds the address a.
func (d *dests) addr(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range d.nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// NewRules compiles c, which Validate has checked.
func NewRules(c *config.ForwardProxy) *Rules {
	r := &Rules{connect: c.Connect(), idle: c.Idle()}
	for _, s := range c.Clients {
		p, _ := router.ParsePrefix(s)
		r.clients = append(r.clients, p)
	}
	for _, s := range c.Allow {
		r.allow.add(s)
	}
	for _, s := range c.Deny {
		r.deny.add(s)
	}
	for _, p := range c.Ports {
		r.ports = append(r.ports, uint16(p))
	}
	key, _ := json.Marshal(c)
	r.key = string(key)
	return r
}

// Key identifies the rules, for telling whether a reload changed them.
func (r *Rules) Key() string {
	if r == nil {
		return ""
	}
	return r.key
}

// ConnectTimeout is how long a dial of a destination may take.
func (r *Rules) ConnectTimeout() time.Duration { return r.connect }

// IdleTimeout is how long a proxied connection may carry nothing.
func (r *Rules) IdleTimeout() time.Duration { return r.idle }

// Client reports whether a client at addr may use the proxy.
func (r *Rules) Client(addr netip.Addr) bool { return r.clients.Contains(addr) }

// verdict is what the rules say of a destination before it is dialed.
type verdict uint8

const (
	refused verdict = iota
	// allowed lets the dial go on: only whether the address is the host's
	// own is left to decide once it is connected.
	allowed
	// unresolved lets the dial go on, but the address a host name resolves
	// to still decides: a deny range may hold it, or only an allow range
	// may allow it.
	unresolved
	// resolvedAllowed is unresolved for a name allow holds, which only
	// deny's ranges and the default refusal of internal addresses may still
	// refuse.
	resolvedAllowed
)

// Allows reports whether the rules may allow the destination hostport,
// "host:port": Dial still decides a host name by the address it resolves
// to.
func (r *Rules) Allows(hostport string) bool { return r.dest(hostport) != refused }

// dest decides the destination hostport before it is dialed.
func (r *Rules) dest(hostport string) verdict {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return refused
	}
	if r.ports != nil {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || !slices.Contains(r.ports, uint16(n)) {
			return refused
		}
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Zone() != "" || r.deny.addr(a) || !r.allow.empty() && !r.allow.addr(a) ||
			netguard.Refused(a, netip.Addr{}, r.allow.nets) {
			return refused
		}
		return allowed
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if r.deny.name(host) {
		return refused
	}
	if r.allow.empty() || r.allow.name(host) {
		return resolvedAllowed
	}
	if r.allow.nets == nil {
		return refused
	}
	return unresolved
}

// resolved decides the address remote a destination was connected to, from
// local, by the verdict v of its name: deny's ranges, allow's for a name
// allow does not hold, and, whatever v, the default refusal of loopback,
// link-local, unspecified and multicast addresses and of the host's own,
// which only an allow range naming them lifts.
func (r *Rules) resolved(v verdict, remote, local netip.Addr) bool {
	remote = remote.Unmap()
	if v != allowed && (r.deny.addr(remote) || v != resolvedAllowed && !r.allow.addr(remote)) {
		return false
	}
	return !netguard.Refused(remote, local, r.allow.nets)
}

// Dial is a socks.DialFunc that dials only what the rules allow: a
// destination whose address they refuse, a host name's once resolved or
// the host's own, is closed as soon as it is connected, before handler
// hears of it, and done is told ErrRefused.
func (r *Rules) Dial(engine *fib.Engine, network, addr string, timeout time.Duration,
	handler fib.Handler, done func(*fib.Connection, error)) error {
	v := r.dest(addr)
	if v == refused {
		return ErrRefused
	}
	return netguard.Dial(nil, engine, network, addr, timeout, handler, done, func(remote, local netip.Addr) bool {
		return r.resolved(v, remote, local)
	}, ErrRefused)
}

// errCommand refuses the SOCKS commands fit does not serve.
var errCommand = &socks.ReplyError{Version: socks.Version5, Code: socks.ReplyCommandNotSupported}

// NewSOCKS returns the SOCKS server of a listener's socks: CONNECT, without
// authentication, for the clients and to the destinations r allows.
func NewSOCKS(r *Rules) *socks.Server {
	return socks.NewServer(socks.Config{
		Allow: func(req *socks.Request) error {
			switch {
			case req.Command != socks.CommandConnect:
				return errCommand
			case !r.Client(req.Conn.RemoteAddrPort().Addr()), !r.Allows(req.Addr):
				return ErrRefused
			}
			return nil
		},
		Dial:             r.Dial,
		HandshakeTimeout: OpeningTimeout,
		DialTimeout:      r.connect,
		IdleTimeout:      r.idle,
	})
}
