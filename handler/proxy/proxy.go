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
	"context"
	"io"
	"net"
	stdhttp "net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/upstream"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".proxy", New: func() any { return new(Proxy) }})
}

// Proxy forwards requests to an upstream.
type Proxy struct {
	Upstream       string   `json:"upstream" fit:"required" doc:"The name of the upstream requests go to."`
	TrustedProxies []string `json:"trusted_proxies,omitempty" doc:"The addresses (\"10.0.0.1\") and networks (\"10.0.0.0/8\") of proxies in front of this one, whose X-Forwarded-* and Forwarded fields are believed. None when left out."`
	HostHeader     string   `json:"host_header,omitempty" doc:"The Host header sent upstream; the client's when left out."`

	pool    *upstream.Pool
	trusted []netip.Prefix
	stall   time.Duration
	// graph is the config the proxy belongs to, held by each exchange until
	// it is over; drains are the contexts its exchanges' contexts derive
	// from, which Drain cancels. They are spread over several, so that
	// exchanges starting at once do not all wait on one context's lock.
	graph  module.Holder
	drains [drainShards]drainCtx
}

const drainShards = 32

type drainCtx struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
}

// Drain cuts off the exchanges still open once the proxy's config has been
// swapped out for the drain timeout: one whose response has not begun gets
// 503, and one under way is cut short as an upstream that broke off would
// be, without counting against the backend.
func (p *Proxy) Drain() {
	for i := range p.drains {
		p.drains[i].cancel(errDrained)
	}
}

// Cleanup lets go of the drain contexts.
func (p *Proxy) Cleanup() {
	for i := range p.drains {
		p.drains[i].cancel(nil)
	}
}

// Provision finds the upstream and parses the trusted proxies.
func (p *Proxy) Provision(ctx *module.Context) error {
	pool, ok := ctx.Upstream(p.Upstream)
	if !ok {
		return ctx.Errorf("upstream", "there is no upstream named %q", p.Upstream)
	}
	p.pool = pool
	p.stall = ctx.WriteStallTimeout()
	p.graph = ctx.Graph()
	for i := range p.drains {
		p.drains[i].ctx, p.drains[i].cancel = context.WithCancelCause(context.Background())
	}
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

// outgoing is the request sent upstream for the one c serves, or nil for one
// with a header field whose name is not a token, as "X-A : 1" makes it: RFC
// 9112 asks a server to refuse whitespace before the colon, which fib v1.3.0
// lets through, and an upstream could read such a field differently.
func (p *Proxy) outgoing(c *fibhttp.Context) *stdhttp.Request {
	r := c.Request
	header := make(stdhttp.Header, len(r.Header)+4)
	for key, values := range r.Header {
		if !validToken(key) {
			return nil
		}
		header[key] = values
	}
	removeHopByHop(header)
	// The body's length is the request's own, and the client answers 100
	// Continue itself, when the body is asked for.
	delete(header, "Content-Length")
	delete(header, "Expect")
	if _, ok := header["User-Agent"]; !ok {
		// Present and empty: no client adds its own, and none is sent.
		header["User-Agent"] = []string{}
	}
	p.setForwarded(c, header)

	host := r.Host
	if p.HostHeader != "" {
		host = p.HostHeader
	}
	out := &stdhttp.Request{
		Method:     r.Method,
		URL:        &url.URL{Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery},
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     header,
		Host:       host,
	}
	if body := c.Body(); len(body) > 0 {
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.ContentLength = int64(len(body))
		out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	return out
}

// setForwarded sets the fields that tell the upstream about the client.
func (p *Proxy) setForwarded(c *fibhttp.Context, header stdhttp.Header) {
	r := c.Request
	clientIP, _ := clientAddr(r.RemoteAddr)
	trusted := p.isTrusted(clientIP)
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	ip := ""
	if clientIP.IsValid() {
		ip = clientIP.String()
	}

	if prior := header["X-Forwarded-For"]; trusted && len(prior) > 0 && ip != "" {
		header["X-Forwarded-For"] = []string{strings.Join(prior, ", ") + ", " + ip}
	} else if ip != "" {
		header["X-Forwarded-For"] = []string{ip}
	} else if !trusted {
		delete(header, "X-Forwarded-For")
	}
	if !trusted || len(header["X-Forwarded-Proto"]) == 0 {
		header["X-Forwarded-Proto"] = []string{proto}
	}
	if !trusted || len(header["X-Forwarded-Host"]) == 0 {
		header["X-Forwarded-Host"] = []string{r.Host}
	}

	element := forwardedElement(clientIP, r.Host, proto)
	if prior := header["Forwarded"]; trusted && len(prior) > 0 {
		header["Forwarded"] = []string{strings.Join(prior, ", ") + ", " + element}
	} else {
		header["Forwarded"] = []string{element}
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

// clientAddr is the IP address of a request's RemoteAddr, as "ip:port".
func clientAddr(remote string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap().WithZone(""), true
}

// forwardedElement is a Forwarded element (RFC 7239) for the client.
func forwardedElement(ip netip.Addr, host, proto string) string {
	var b strings.Builder
	b.WriteString("for=")
	switch {
	case !ip.IsValid():
		b.WriteString("unknown")
	case ip.Is6():
		b.WriteString(`"[` + ip.String() + `]"`)
	default:
		b.WriteString(ip.String())
	}
	if host != "" {
		b.WriteString(";host=")
		b.WriteString(forwardedValue(host))
	}
	b.WriteString(";proto=")
	b.WriteString(proto)
	return b.String()
}

// forwardedValue is v as a Forwarded parameter value: a token as it is,
// anything else quoted.
func forwardedValue(v string) string {
	for i := range len(v) {
		ch := v[i]
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", ch) >= 0) {
			return strconv.Quote(v)
		}
	}
	return v
}

// validToken reports whether s is an HTTP token, as a field name must be.
func validToken(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0 {
			continue
		}
		return false
	}
	return true
}

// hopByHop are the fields that concern one connection only, RFC 9110
// section 7.6.1, with Keep-Alive and Proxy-Connection, which older clients
// send.
var hopByHop = []string{
	"Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// removeHopByHop deletes the hop-by-hop fields from h, and the fields its
// Connection field names.
func removeHopByHop(h stdhttp.Header) {
	for _, value := range h["Connection"] {
		for name := range strings.SplitSeq(value, ",") {
			if name = textproto.TrimString(name); name != "" {
				delete(h, textproto.CanonicalMIMEHeaderKey(name))
			}
		}
	}
	for _, name := range hopByHop {
		delete(h, name)
	}
}
