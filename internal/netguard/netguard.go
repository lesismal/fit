// Package netguard tells the destinations fit connects to only when its
// config names them: those where whoever chooses a destination for fit, a
// forward proxy's client or a Kubernetes Service's owner, would reach the
// host fit runs on, or the cloud's metadata service, rather than the
// network beyond it. These are loopback, link-local (169.254.169.254
// among them), unspecified and multicast addresses, the metadata services'
// other addresses, and the host's own addresses, which a connection shows
// by coming from the address it goes to.
//
// The check is made on the address a connection was opened to, once it is
// open and before anything is sent on it, so that a host name is decided by
// the address it resolved to for that connection, not by one looked up
// earlier, which DNS could change in between.
package netguard

import (
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
)

// ranges are the addresses refused unless the config names them.
var ranges = [...]netip.Prefix{
	// "This network": a connect to 0.0.0.0 reaches the host itself.
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("127.0.0.0/8"),
	// Link-local, with the metadata service of most clouds at
	// 169.254.169.254.
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	// Alibaba Cloud's metadata service.
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	// AWS's metadata service over IPv6.
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

// Internal returns the range of refused addresses a is in, and whether it
// is in one. An IPv4 address written in IPv6 is the IPv4 address.
func Internal(a netip.Addr) (netip.Prefix, bool) {
	a = a.Unmap()
	for _, p := range ranges {
		if p.Contains(a) {
			return p, true
		}
	}
	return netip.Prefix{}, false
}

// Refused reports whether a connection to remote, opened from local, goes
// where the config must name to let it: into a range Internal holds, unless
// allow has a prefix inside that range holding remote, as "127.0.0.0/8" or
// "169.254.169.254" is and "0.0.0.0/0" is not; or to the host's own address,
// when remote is local, unless allow has remote alone. local may be the
// zero Addr when it is not known.
func Refused(remote, local netip.Addr, allow []netip.Prefix) bool {
	remote = remote.Unmap()
	var bits int
	if p, ok := Internal(remote); ok {
		bits = p.Bits()
	} else if local.IsValid() && remote == local.Unmap() {
		bits = remote.BitLen()
	} else {
		return false
	}
	for _, p := range allow {
		if p.Bits() >= bits && p.Contains(remote) {
			return false
		}
	}
	return true
}

// LocalAddr returns the address c was opened from, or the zero Addr when
// it is not an IP address.
func LocalAddr(c *fib.Connection) netip.Addr {
	switch a := c.LocalAddr().(type) {
	case *net.TCPAddr:
		return a.AddrPort().Addr().Unmap()
	case *net.UDPAddr:
		return a.AddrPort().Addr().Unmap()
	}
	return netip.Addr{}
}

// DialFunc dials as fib.Engine's DialWithHandler does; nil is that.
type DialFunc = func(engine *fib.Engine, network, addr string, timeout time.Duration,
	handler fib.Handler, done func(*fib.Connection, error)) error

// Dial dials addr through dial, or engine.DialWithHandler when dial is nil,
// and lets handler and done have the connection only when ok says so of
// the addresses it was opened to and from: otherwise it is closed as soon
// as it is open, before anything is sent on it, handler hears nothing of
// it, and done is told refusal.
func Dial(dial DialFunc, engine *fib.Engine, network, addr string, timeout time.Duration,
	handler fib.Handler, done func(*fib.Connection, error), ok func(remote, local netip.Addr) bool, refusal error) error {
	if dial == nil {
		dial = (*fib.Engine).DialWithHandler
	}
	h := &checked{Handler: handler, ok: ok}
	return dial(engine, network, addr, timeout, h, func(c *fib.Connection, err error) {
		if err == nil && h.refused.Load() {
			c, err = nil, refusal
		}
		done(c, err)
	})
}

// checked is the handler of a connection ok still decides. Once ok lets the
// connection through, it hands the connection to the handler it stood in
// for, so that every later callback, OnHangup, OnShutdown and OnDatagrams
// among them, goes straight there.
type checked struct {
	fib.Handler
	ok      func(remote, local netip.Addr) bool
	refused atomic.Bool
}

func (h *checked) OnOpen(c *fib.Connection) {
	if !h.ok(c.RemoteAddrPort().Addr().Unmap(), LocalAddr(c)) {
		h.refused.Store(true)
		_ = c.Close()
		return
	}
	// OnOpen runs on the event loop before any round of the connection's,
	// so nothing reads the handler while it changes.
	c.SetHandler(h.Handler)
	h.Handler.OnOpen(c)
}

// OnData, OnPriorityData and OnClose come only for a connection refused,
// whose handler hears nothing of it.
func (h *checked) OnData(*fib.Connection, []byte) {}

func (h *checked) OnPriorityData(*fib.Connection, []byte) {}

func (h *checked) OnClose(*fib.Connection, error) {}
