package config

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// A bind is an address the config has fit listen on: a listener's, the Admin
// API's, the console's or a role's.
type bind struct {
	// name says whose address it is in an error, as in listener "web" or
	// tunnel.proxy.web_proxies[0].
	name string
	at   Value
	// network is "tcp" or "udp".
	network string
	address string
}

// binds returns every address c has fit listen on, in the order its parts
// are written, so that a clash is reported at the later of the two. Unix
// sockets, and addresses that are not a host and port, are left out: the
// first share no port, and the second are reported where they are checked.
func (c *Config) binds() []bind {
	var out []bind
	add := func(name string, at Value, network, address string) {
		out = append(out, bind{name: name, at: at, network: network, address: address})
	}
	listeners := c.Listeners
	if auto := c.AutoHTTPS; len(listeners) == 0 && !(auto.redirects() && auto.HTTP() == auto.HTTPS()) {
		// The ones HTTPS by default makes, unless it has them on one port,
		// which Validate reports by its own ports.
		listeners, _ = c.Effective()
	}
	for _, l := range listeners {
		at := l.At.Field("address")
		if l.At.Path() == "" && c.AutoHTTPS != nil {
			// One HTTPS by default made: its ports are auto_https's.
			at = c.AutoHTTPS.At
		}
		for _, network := range l.networks() {
			add(fmt.Sprintf("listener %q", l.Name), at, network, l.Address)
		}
	}
	if a := c.Admin; a != nil {
		if network, address := a.Network(); network == "tcp" {
			add("admin", a.At.Field("address"), network, address)
		}
		if con := a.Console; con != nil {
			add("admin.console", con.At.Field("address"), "tcp", con.Addr())
		}
	}
	console := func(name string, rc *RoleConsole) {
		if rc != nil {
			add(name, rc.At.Field("address"), "tcp", rc.Address)
		}
	}
	if l := c.LLM; l != nil {
		if h := l.Hub; h != nil {
			console("llm.hub.console", h.Console)
			if h.Listen != nil {
				add("llm.hub.listen", h.Listen.At.Field("address"), "tcp", h.Listen.Address)
			}
		}
		if n := l.Node; n != nil {
			console("llm.node.console", n.Console)
			if n.Listen != nil {
				add("llm.node.listen", n.Listen.At.Field("address"), "tcp", n.Listen.Address)
			}
		}
		if p := l.Proxy; p != nil {
			console("llm.proxy.console", p.Console)
			if p.Listen != nil {
				add("llm.proxy.listen", p.Listen.At.Field("address"), "tcp", p.Listen.Address)
			}
			if p.API != nil {
				add("llm.proxy.api", p.API.At.Field("address"), "tcp", p.API.Address)
			}
		}
	}
	if t := c.Tunnel; t != nil {
		if h := t.Hub; h != nil {
			console("tunnel.hub.console", h.Console)
			for i, l := range h.Listeners {
				network := "tcp"
				if l.Protocol == TunnelQUIC {
					network = "udp"
				}
				add(fmt.Sprintf("tunnel.hub.listeners[%d]", i), l.At.Field("address"), network, l.Address)
			}
			if cs := h.ConsoleSocket; cs != nil {
				add("tunnel.hub.console_socket", cs.At.Field("address"), "tcp", cs.Address)
			}
		}
		if n := t.Node; n != nil {
			console("tunnel.node.console", n.Console)
		}
		if p := t.Proxy; p != nil {
			console("tunnel.proxy.console", p.Console)
			for i, f := range p.Forwards {
				add(fmt.Sprintf("tunnel.proxy.forwards[%d]", i), f.At.Field("listen"), "tcp", f.Listen)
			}
			for i, w := range p.WebProxies {
				add(fmt.Sprintf("tunnel.proxy.web_proxies[%d]", i), w.At.Field("listen"), "tcp", w.Listen)
			}
		}
	}
	if s := c.Streaming; s != nil {
		if r := s.RTMP; r != nil {
			add("streaming.rtmp", r.At.Field("listen"), "tcp", r.Listen)
		}
		if r := s.RTSP; r != nil {
			add("streaming.rtsp", r.At.Field("listen"), "tcp", r.Listen)
		}
		if r := s.SRT; r != nil {
			add("streaming.srt", r.At.Field("listen"), "udp", r.Listen)
		}
		if w := s.WebRTC; w != nil {
			add("streaming.webrtc", w.At.Field("listen"), "udp", w.Listen)
		}
	}
	if b := c.BitTorrent; b != nil && b.Client != nil {
		bc := b.Client
		console("bittorrent.client.console", bc.Console)
		add("bittorrent.client", bc.At.Field("listen"), "tcp", bc.Listen)
		add("bittorrent.client", bc.At.Field("listen"), "udp", bc.Listen)
	}
	if w := c.WebRTC; w != nil {
		if t := w.TURN; t != nil {
			for i, a := range t.Listen {
				add(fmt.Sprintf("webrtc.turn.listen[%d]", i), t.At.Field("listen").Index(i), "udp", a)
			}
			for i, a := range t.ListenTCP {
				add(fmt.Sprintf("webrtc.turn.listen_tcp[%d]", i), t.At.Field("listen_tcp").Index(i), "tcp", a)
			}
			for i, a := range t.ListenTLS {
				add(fmt.Sprintf("webrtc.turn.listen_tls[%d]", i), t.At.Field("listen_tls").Index(i), "tcp", a)
			}
		}
		if m := w.Media; m != nil {
			add("webrtc.media", m.At.Field("listen"), "udp", m.Listen)
		}
		if p := w.Proxy; p != nil {
			add("webrtc.proxy", p.At.Field("listen"), "udp", p.Listen)
		}
	}
	return out
}

// checkBinds reports every two of c's addresses on one transport that
// overlap, at the later of the two. fib's engines listen with SO_REUSEPORT,
// so that their pollers share an address's connections; two bindings on one
// address would both listen without an error, and the kernel would split the
// connections between two servers. Each part of the config is checked here,
// across roles, rather than each role checking its own.
func (c *Config) checkBinds(add func(Value, string, ...any)) {
	type seen struct {
		bind
		host string
		port uint64
	}
	var kept []seen
	for _, b := range c.binds() {
		host, port, ok := bindKey(b.address)
		if !ok || port == 0 {
			// Port 0 picks a free port, so two of them do not clash.
			continue
		}
		for _, k := range kept {
			if k.network != b.network || k.port != port || !hostsOverlap(k.host, host) {
				continue
			}
			add(b.at, "%s listens on %s %s already%s; two servers on one address would share its connections, so give one of them another port",
				k.name, strings.ToUpper(b.network), k.address, overlapNote(k.address, k.host, b.address, host))
			break
		}
		kept = append(kept, seen{b, host, port})
	}
}

// anyHost is the host of an address on every address: an empty host,
// 0.0.0.0 or ::.
const anyHost = "*"

// bindKey returns the host and port address binds, the host normalized:
// anyHost for every address, 127.0.0.1 for localhost, an IP address in its
// shortest form, and a host name in lower case. ok is false for an address
// that is not a host and port.
func bindKey(address string) (host string, port uint64, ok bool) {
	h, p, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, false
	}
	if port, err = strconv.ParseUint(p, 10, 16); err != nil {
		return "", 0, false
	}
	if h == "" {
		return anyHost, port, true
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		ip = ip.Unmap()
		if ip.IsUnspecified() {
			return anyHost, port, true
		}
		return ip.String(), port, true
	}
	if h = strings.ToLower(h); h == "localhost" {
		// As fib, and net.Listen, resolve it to listen on.
		return "127.0.0.1", port, true
	}
	return h, port, true
}

// hostsOverlap reports whether two normalized hosts take some of the same
// connections: the same host, or one that is every address.
func hostsOverlap(a, b string) bool {
	return a == b || a == anyHost || b == anyHost
}

// overlapNote says how two addresses that are written differently are one:
// the same address written two ways, or every address and one of them.
func overlapNote(first, firstHost, second, secondHost string) string {
	switch {
	case first == second:
		return ""
	case firstHost == secondHost:
		return ", the same address as " + second
	case firstHost == anyHost:
		return ", every address, " + second + " among them"
	default:
		return ", which " + second + ", every address, takes in"
	}
}
