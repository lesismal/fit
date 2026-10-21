// Package proxy is the proxy handler: it forwards each request to an
// upstream and writes the upstream's response back.
//
// The request goes out with its method, path, query, headers and body; the
// response comes back with its status, headers and body. Hop-by-hop headers
// (Connection and the fields it names, Keep-Alive, Proxy-Connection,
// Proxy-Authenticate, Proxy-Authorization, TE, Trailer, Transfer-Encoding and
// Upgrade) are dropped in both directions, as RFC 9110 asks of a proxy. The
// Host header is the client's, as Caddy and Traefik send it, unless
// host_header says otherwise.
//
// The proxy tells the upstream who the client is with X-Forwarded-For,
// X-Forwarded-Proto, X-Forwarded-Host and Forwarded (RFC 7239). What a
// client sends in those fields is believed only when the client's address is
// in trusted_proxies: then the proxy adds the client to X-Forwarded-For and
// Forwarded and keeps X-Forwarded-Proto and X-Forwarded-Host; otherwise it
// replaces all four with what it sees itself. No proxy is trusted by
// default, so a client cannot pass itself off as another.
//
// Bodies up to 64KB travel whole; larger ones stream in both directions,
// each side paced by the other, and trailers follow them both ways. How a
// request fails is in exchange.go.
package proxy

import (
	"bytes"
	"io"
	"net"
	stdhttp "net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/upstream"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".proxy", New: func() any { return new(Proxy) }})
}

// Proxy forwards requests to an upstream.
type Proxy struct {
	Upstream          string          `json:"upstream,omitempty" doc:"The name of the upstream requests go to; or upstreams."`
	Upstreams         []Split         `json:"upstreams,omitempty" doc:"Upstreams requests are spread over at random by weight, as a canary release does, in place of upstream."`
	Failover          string          `json:"failover,omitempty" doc:"An upstream that takes the requests when none of the chosen upstream's backends is in rotation, for failing or for its health checks, and one of its own is."`
	Mirror            *Mirror         `json:"mirror,omitempty" doc:"Send a copy of a share of the requests whose bodies arrive whole (64KB at most) to another upstream, and drop its answers. At most 256 copies are under way at once; past that, requests are not copied. The requests themselves do not wait for their copies."`
	TrustedProxies    []string        `json:"trusted_proxies,omitempty" doc:"The addresses (\"10.0.0.1\") and networks (\"10.0.0.0/8\") of proxies in front of this one, whose X-Forwarded-* and Forwarded fields are believed. None when left out."`
	HostHeader        string          `json:"host_header,omitempty" doc:"The Host header sent upstream; the client's when left out."`
	Retry             *Retry          `json:"retry,omitempty" doc:"Send a request that failed again, to the backend the upstream picks next: one whose connection could not be made, whatever its method, and, for GET, HEAD, OPTIONS, TRACE, PUT and DELETE, one that failed before its response began or was answered with a status of statuses. A request whose body streams (past 64KB, or still arriving) is never sent again. No retries when left out."`
	TunnelIdleTimeout config.Duration `json:"tunnel_idle_timeout,omitempty" default:"\"10m\"" doc:"How long a tunnel, as a WebSocket the proxy has joined to its upstream is, may carry nothing either way before it is closed; 10m when left out."`
	Timeout           config.Duration `json:"timeout,omitempty" doc:"The most a request may take, from when the proxy takes it to the end of its response, retries included: one past it before its response began is answered 504, and one whose response is under way is cut short. None when left out."`

	pool     *upstream.Pool
	split    []weighted
	failover *upstream.Pool
	mirror   *mirror
	retry    *retryPolicy
	// tunnelIdle bounds a tunnel's silence; relays are the requests that
	// asked to switch protocols, their handshakes and tunnels, which Drain
	// closes.
	tunnelIdle time.Duration
	relays     [openShards]relaySet
	timeout    time.Duration
	trusted    []netip.Prefix
	stall      time.Duration
	// graph is the config the proxy belongs to, held by each exchange until
	// it is over; open are the exchanges under way, which Drain cuts off.
	// They are spread over several sets, so that exchanges starting at once
	// do not all wait on one lock.
	graph module.Holder
	open  [openShards]openSet
}

const openShards = 32

// openSet is a shard of a proxy's open exchanges.
type openSet struct {
	mu        sync.Mutex
	exchanges map[*exchange]struct{}
	_         [40]byte // a shard to a cache line
}

func (s *openSet) add(x *exchange) {
	s.mu.Lock()
	if s.exchanges == nil {
		s.exchanges = make(map[*exchange]struct{})
	}
	s.exchanges[x] = struct{}{}
	s.mu.Unlock()
}

func (s *openSet) remove(x *exchange) {
	s.mu.Lock()
	delete(s.exchanges, x)
	s.mu.Unlock()
}

// Drain cuts off the exchanges still open once the proxy's config has been
// swapped out for the drain timeout: one whose response has not begun gets
// 503, and one under way is cut short as an upstream that broke off would
// be, without counting against the backend.
func (p *Proxy) Drain() {
	var open []*exchange
	for i := range p.open {
		s := &p.open[i]
		s.mu.Lock()
		for x := range s.exchanges {
			open = append(open, x)
		}
		s.mu.Unlock()
	}
	for _, x := range open {
		x.cancel(errDrained)
	}
	p.drainRelays()
}

// Provision finds the upstream and parses the trusted proxies.
func (p *Proxy) Provision(ctx *module.Context) error {
	if err := p.provisionSplit(ctx); err != nil {
		return err
	}
	p.stall = ctx.WriteStallTimeout()
	p.graph = ctx.Graph()
	for i, s := range p.TrustedProxies {
		prefix, err := parsePrefix(s)
		if err != nil {
			return ctx.Params.Field("trusted_proxies").Index(i).Errorf("%q is not an IP address or a network, as in \"10.0.0.1\" or \"10.0.0.0/8\"", s)
		}
		p.trusted = append(p.trusted, prefix)
	}
	if strings.ContainsAny(p.HostHeader, " \t\r\n\x00/") {
		return ctx.Errorf("host_header", "%q is not a host", p.HostHeader)
	}
	if r := p.Retry; r != nil {
		for i, status := range r.Statuses {
			if status < 500 || status > 599 {
				return ctx.Params.Field("retry").Field("statuses").Index(i).Errorf("%d is not a 5xx status", status)
			}
		}
		p.retry = newRetryPolicy(r)
	}
	p.timeout = p.Timeout.Std()
	if p.tunnelIdle = p.TunnelIdleTimeout.Std(); p.tunnelIdle <= 0 {
		p.tunnelIdle = DefaultTunnelIdleTimeout
	}
	return nil
}

// parsePrefix parses an address, as a network of one, or a network.
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		prefix, err := netip.ParsePrefix(s)
		return prefix.Masked(), err
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// outgoing fills in x.base, the request sent upstream for the one c serves,
// and reports false for one with a header field whose name is not a token,
// as "X-A : 1" makes it: RFC 9112 asks a server to refuse whitespace before
// the colon, and an upstream could read such a field differently. fib refuses
// it with 400 since v1.4.0; this stays as defence in depth. The request, its
// URL and its forwarding fields' values live in x, so that they cost no
// allocations of their own.
func (p *Proxy) outgoing(c *fibhttp.Context, x *exchange) bool {
	r := c.Request
	header := make(stdhttp.Header, len(r.Header)+4)
	for key, values := range r.Header {
		if strings.HasPrefix(key, ":") {
			// A pseudo-header the server kept, as an extended CONNECT's
			// :protocol; the upstream's request has its own framing.
			continue
		}
		if !validToken(key) {
			return false
		}
		// The body's length is the request's own, and the client answers
		// 100 Continue itself, when the body is asked for.
		if isHopByHop(key) || key == "Content-Length" || key == "Expect" {
			continue
		}
		header[key] = values
	}
	removeConnectionNamed(header, r.Header["Connection"])
	if _, ok := header["User-Agent"]; !ok {
		// Present and empty: no client adds its own, and none is sent.
		header["User-Agent"] = []string{}
	}
	p.setForwarded(c, header, &x.forwarded)

	host := r.Host
	if p.HostHeader != "" {
		host = p.HostHeader
	}
	x.url = url.URL{Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	x.base = stdhttp.Request{
		Method:     r.Method,
		URL:        &x.url,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     header,
		Host:       host,
	}
	if body := c.Body(); len(body) > 0 {
		x.base.Body = io.NopCloser(bytes.NewReader(body))
		x.base.ContentLength = int64(len(body))
		x.base.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	return true
}

// setForwarded sets the fields that tell the upstream about the client. The
// fields' values go in values, which the caller keeps for as long as the
// header.
func (p *Proxy) setForwarded(c *fibhttp.Context, header stdhttp.Header, values *[4]string) {
	r := c.Request
	clientIP, ip := clientAddr(r.RemoteAddr)
	trusted := p.isTrusted(clientIP)
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	field := func(i int, v string) []string {
		values[i] = v
		return values[i : i+1 : i+1]
	}

	if prior := header["X-Forwarded-For"]; trusted && len(prior) > 0 && ip != "" {
		header["X-Forwarded-For"] = field(0, strings.Join(prior, ", ")+", "+ip)
	} else if ip != "" {
		header["X-Forwarded-For"] = field(0, ip)
	} else if !trusted {
		delete(header, "X-Forwarded-For")
	}
	if !trusted || len(header["X-Forwarded-Proto"]) == 0 {
		header["X-Forwarded-Proto"] = field(1, proto)
	}
	if !trusted || len(header["X-Forwarded-Host"]) == 0 {
		header["X-Forwarded-Host"] = field(2, r.Host)
	}

	element := forwardedElement(clientIP, ip, r.Host, proto)
	if prior := header["Forwarded"]; trusted && len(prior) > 0 {
		header["Forwarded"] = field(3, strings.Join(prior, ", ")+", "+element)
	} else {
		header["Forwarded"] = field(3, element)
	}
}

func (p *Proxy) isTrusted(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	for _, prefix := range p.trusted {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// clientAddr is the IP address of a request's RemoteAddr, as "ip:port", and
// that address as a string, or "" when it is not one. An IPv4 address is
// written in its canonical form already, since netip parses no other, so its
// string is a part of RemoteAddr's, without an allocation.
func clientAddr(remote string) (netip.Addr, string) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, ""
	}
	if ip.Is4() {
		return ip, host
	}
	ip = ip.Unmap().WithZone("")
	return ip, ip.String()
}

// forwardedElement is a Forwarded element (RFC 7239) for the client at ip,
// written as s.
func forwardedElement(ip netip.Addr, s, host, proto string) string {
	var b strings.Builder
	b.Grow(len(`for="[]";host="";proto=https`) + len(s) + len(host))
	b.WriteString("for=")
	switch {
	case !ip.IsValid():
		b.WriteString("unknown")
	case ip.Is6():
		b.WriteString(`"[`)
		b.WriteString(s)
		b.WriteString(`]"`)
	default:
		b.WriteString(s)
	}
	if host != "" {
		b.WriteString(";host=")
		writeForwardedValue(&b, host)
	}
	b.WriteString(";proto=")
	b.WriteString(proto)
	return b.String()
}

// writeForwardedValue writes v as a Forwarded parameter value: a token as it
// is, anything else quoted.
func writeForwardedValue(b *strings.Builder, v string) {
	if validToken(v) {
		b.WriteString(v)
		return
	}
	b.WriteString(strconv.Quote(v))
}

// tokenChars are the bytes an HTTP token may hold.
var tokenChars = func() (t [256]bool) {
	for c := range 256 {
		t[c] = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", byte(c)) >= 0
	}
	return t
}()

// validToken reports whether s is an HTTP token, as a field name must be.
func validToken(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if !tokenChars[s[i]] {
			return false
		}
	}
	return true
}

// isHopByHop reports whether name, canonical, is a field that concerns one
// connection only, RFC 9110 section 7.6.1, or Keep-Alive or
// Proxy-Connection, which older clients send.
func isHopByHop(name string) bool {
	switch name {
	case "Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}

// removeHopByHop deletes the hop-by-hop fields from h, and the fields its
// Connection field names. It looks at each field once rather than deleting
// each hop-by-hop name, as few headers have any.
func removeHopByHop(h stdhttp.Header) {
	removeConnectionNamed(h, h["Connection"])
	for name := range h {
		if isHopByHop(name) {
			delete(h, name)
		}
	}
}

// removeConnectionNamed deletes from h the fields that connection, a
// Connection field's values, names.
func removeConnectionNamed(h stdhttp.Header, connection []string) {
	for _, value := range connection {
		for name := range strings.SplitSeq(value, ",") {
			if name = textproto.TrimString(name); name != "" {
				delete(h, textproto.CanonicalMIMEHeaderKey(name))
			}
		}
	}
}
