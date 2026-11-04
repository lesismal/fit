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
// Over HTTP/1, a route whose proxy no middleware wraps passes the request's
// header on as the lines it arrived in, edited as above, and the response's
// as the upstream's lines, rather than building, sorting and writing a map on
// each side; the fields keep their order and case (outgoingFramed and
// responseBlock).
//
// Bodies up to 64KB travel whole; larger ones stream in both directions,
// each side paced by the other, and trailers follow them both ways. On Linux
// an upload of 128KB or more framed by its length, from an HTTP/1.1 client
// without TLS to an HTTP/1.1 upstream without TLS, goes by splice(2), from one
// socket to the other through a pipe, unless splice_uploads turns it off. How
// a request fails is in exchange.go.
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
	"unsafe"

	"github.com/lesismal/fib/bufferpool"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/upstream"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".proxy", New: func() any { return new(Proxy) }, Doc: "Proxies requests to an upstream, or to several by weight, with retries, mirroring and failover; WebSockets and gRPC calls too, streamed both ways."})
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
	SpliceUploads     *bool           `json:"splice_uploads,omitempty" default:"true" doc:"On Linux, move a large upload (128KB or more, framed by its Content-Length) from a client's HTTP/1.1 connection without TLS to an HTTP/1.1 upstream without TLS with splice(2), the kernel passing its pages from one socket to the other, rather than copying it through the proxy. An upload that cannot go so, to an h2, h2c or TLS upstream, or chunked, goes the ordinary way. It costs the proxy about 40% less CPU on such uploads and the upstream about 5% more, which reads the body cold rather than as the proxy just wrote it, and more once the upstream itself is the bottleneck: false suits an upstream whose CPU is scarcer than the proxy's. False copies every upload. True when left out."`

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
	// splice is SpliceUploads, resolved.
	splice bool
	// graph is the config the proxy belongs to, held by each exchange until
	// it is over; open are the exchanges under way, which Drain cuts off.
	// They are spread over several sets, so that exchanges starting at once
	// do not all wait on one lock.
	graph module.Holder
	open  [openShards]openSet
	// unwrapped says no middleware wraps the proxy, so that a request's
	// header is what arrived, and may pass on as it came (outgoingFramed).
	unwrapped bool
}

// Unwrapped is told that no middleware wraps the proxy in its route.
func (p *Proxy) Unwrapped() { p.unwrapped = true }

const openShards = 32

// openSet is a shard of a proxy's open exchanges: a list through the
// exchanges' own prev and next, so that adding and removing one moves
// pointers rather than hashing into a map.
type openSet struct {
	mu   sync.Mutex
	head *exchange
	_    [48]byte // a shard to a cache line
}

func (s *openSet) add(x *exchange) {
	s.mu.Lock()
	x.next = s.head
	if s.head != nil {
		s.head.prev = x
	}
	s.head = x
	s.mu.Unlock()
}

func (s *openSet) remove(x *exchange) {
	s.mu.Lock()
	if x.prev != nil {
		x.prev.next = x.next
	} else {
		s.head = x.next
	}
	if x.next != nil {
		x.next.prev = x.prev
	}
	x.prev, x.next = nil, nil
	s.mu.Unlock()
}

// Drain cuts off the exchanges still open once the proxy's config has been
// swapped out for the drain timeout: one whose response has not begun gets
// 503, and one under way is cut short as an upstream that broke off would
// be, without counting against the backend.
func (p *Proxy) Drain() {
	// Each with the request it serves, read while it is surely open: one that
	// ends meanwhile may serve another by the time it is cut off.
	type openExchange struct {
		x   *exchange
		gen uint32
	}
	var open []openExchange
	for i := range p.open {
		s := &p.open[i]
		s.mu.Lock()
		for x := s.head; x != nil; x = x.next {
			open = append(open, openExchange{x, x.gen.Load()})
		}
		s.mu.Unlock()
	}
	for _, o := range open {
		o.x.cancel(o.gen, errDrained)
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
	p.splice = p.SpliceUploads == nil || *p.SpliceUploads
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
//
// The upstream's header is the client's own map, edited in place, in one pass
// over it, as most requests allow: the request is the proxy's until its
// response is written, and fib read what it needs of the fields
// (Connection) as it parsed them. A few get a copy instead, as their fields
// are read again later: a request with Expect, by fib's HTTP/2 server when
// the body is asked for; one with Trailer, by trailerNames when the body
// ends; and an upgrade, by fib as it switches the client's side.
func (p *Proxy) outgoing(c *fibhttp.Context, x *exchange) bool {
	r := c.Request
	if p.unwrapped && p.mirror == nil && !x.own {
		if block := c.RequestHeaderBlock(); block != "" && p.outgoingFramed(c, x, block) {
			return true
		}
	}
	// The values set in the client's header are the request's, apart from
	// the exchange, which may serve the next request while the client's
	// header is still read.
	x.fields = new(headerFields)
	connection := r.Header["Connection"]
	header := r.Header
	_, expect := r.Header["Expect"]
	_, trailer := r.Header["Trailer"]
	copied := expect || trailer || x.own
	if copied {
		header = make(stdhttp.Header, len(r.Header)+4)
	}
	for key, values := range r.Header {
		// A pseudo-header the server kept, as an extended CONNECT's
		// :protocol, is not sent: the upstream's request has its own
		// framing. The body's length is the request's own, and the client
		// answers 100 Continue itself, when the body is asked for.
		drop := strings.HasPrefix(key, ":") || isHopByHop(key) || key == "Content-Length" || key == "Expect"
		if !drop && !validToken(key) {
			return false
		}
		switch {
		case !copied && drop:
			delete(header, key)
		case copied && !drop:
			header[key] = values
		}
	}
	removeConnectionNamed(header, connection)
	if _, ok := header["User-Agent"]; !ok {
		// Present and empty: no client adds its own, and none is sent.
		header["User-Agent"] = []string{}
	}
	p.setForwarded(c, header, x)
	if e := observe.EntryOf(c); e != nil && e.Trace() != nil {
		// The upstream's parent is fit's span; tracestate goes on as it
		// came, with the rest of the fields.
		x.fields.traceparent[0] = e.Trace().Traceparent()
		header["Traceparent"] = x.fields.traceparent[:]
	}

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
	x.blockResponse = r.ProtoMajor == 1 && r.Method != stdhttp.MethodHead
	if body := c.Body(); len(body) > 0 {
		// fib's client takes a buffered body's bytes as they are, rather
		// than reading them into a copy; they stay put until the exchange
		// is over, past the upstream's callback.
		x.base.Body = fibhttp.NewBufferedBody(body)
		x.base.ContentLength = int64(len(body))
		x.base.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	return true
}

// outgoingFramed fills in x.base as outgoing does, for a request whose header
// passes on as the lines it arrived in, block, rather than through a map:
// x.framed is the upstream's header, those lines but the ones outgoing drops,
// with the forwarding fields after them, which fib's client writes as they
// are. It reports false, having changed nothing that matters, for a request
// it leaves to outgoing: one whose client is a trusted proxy, whose
// forwarding fields are kept, one with Expect or Trailer, whose fields are
// read again (outgoing), and a gRPC call, which gets fields of its own.
//
// The proxy's route has no middleware, so nothing before it can have changed
// the header since it arrived; fib checked every line of it as it parsed it.
func (p *Proxy) outgoingFramed(c *fibhttp.Context, x *exchange, block string) bool {
	r := c.Request
	clientIP, ip := clientAddr(r.RemoteAddr)
	if p.isTrusted(clientIP) || isGRPC(r) {
		return false
	}
	connection := r.Header["Connection"]
	var traceparent string
	if e := observe.EntryOf(c); e != nil && e.Trace() != nil {
		traceparent = e.Trace().Traceparent()
	}
	// The header is built in a buffer of the pool's, which send gives back
	// once fib's client has copied it, rather than one the exchange keeps
	// through the wait for the upstream: with thousands of requests in
	// flight, that wait is where the buffers would pile up.
	dst := bufferpool.Get(len(block) + forwardingRoom + 3*len(r.Host) + len(traceparent))[:0]
	for block != "" {
		end := strings.IndexByte(block, '\n') + 1
		line := block[:end]
		block = block[end:]
		name := line[:strings.IndexByte(line, ':')]
		switch framedRequestField(name) {
		case fieldKeep:
		case fieldDrop:
			continue
		case fieldReadAgain:
			bufferpool.Put(dst)
			return false
		}
		if len(connection) > 0 && connectionNames(connection, name) ||
			traceparent != "" && strings.EqualFold(name, "Traceparent") {
			continue
		}
		dst = append(dst, line...)
	}
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	if ip != "" {
		dst = appendField(dst, "X-Forwarded-For: ", ip)
	}
	dst = appendField(dst, "X-Forwarded-Proto: ", proto)
	dst = appendField(dst, "X-Forwarded-Host: ", r.Host)
	dst = append(dst, "Forwarded: "...)
	dst = appendForwardedElement(dst, clientIP, ip, r.Host, proto)
	dst = append(dst, "\r\n"...)
	if traceparent != "" {
		dst = appendField(dst, "Traceparent: ", traceparent)
	}
	x.framed = dst
	x.blockResponse = r.Method != stdhttp.MethodHead

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
		Host:       host,
	}
	if body := c.Body(); len(body) > 0 {
		x.base.Body = fibhttp.NewBufferedBody(body)
		x.base.ContentLength = int64(len(body))
		x.base.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	return true
}

// forwardingRoom is what outgoingFramed's buffer has room for past the
// client's lines, besides three copies of the host and the traceparent: the
// forwarding fields' names and an address, as most requests need.
const forwardingRoom = 160

// What outgoingFramed does with a field of the client's request.
const (
	fieldKeep = iota
	// fieldDrop is a field outgoing drops, or replaces with one of its own.
	fieldDrop
	// fieldReadAgain is one whose request outgoing serves instead.
	fieldReadAgain
)

// framedRequestField is what outgoingFramed does with the field name names,
// which may be in any case, for a client that is not a trusted proxy.
func framedRequestField(name string) int {
	switch len(name) {
	case 2:
		if strings.EqualFold(name, "Te") {
			return fieldDrop
		}
	case 4:
		if strings.EqualFold(name, "Host") {
			return fieldDrop
		}
	case 6:
		if strings.EqualFold(name, "Expect") {
			return fieldReadAgain
		}
	case 7:
		if strings.EqualFold(name, "Upgrade") {
			return fieldDrop
		}
		if strings.EqualFold(name, "Trailer") {
			return fieldReadAgain
		}
	case 9:
		if strings.EqualFold(name, "Forwarded") {
			return fieldDrop
		}
	case 10:
		if strings.EqualFold(name, "Connection") || strings.EqualFold(name, "Keep-Alive") {
			return fieldDrop
		}
	case 14:
		if strings.EqualFold(name, "Content-Length") {
			return fieldDrop
		}
	case 15:
		if strings.EqualFold(name, "X-Forwarded-For") {
			return fieldDrop
		}
	case 16:
		if strings.EqualFold(name, "Proxy-Connection") || strings.EqualFold(name, "X-Forwarded-Host") {
			return fieldDrop
		}
	case 17:
		if strings.EqualFold(name, "Transfer-Encoding") || strings.EqualFold(name, "X-Forwarded-Proto") {
			return fieldDrop
		}
	case 18:
		if strings.EqualFold(name, "Proxy-Authenticate") {
			return fieldDrop
		}
	case 19:
		if strings.EqualFold(name, "Proxy-Authorization") {
			return fieldDrop
		}
	}
	return fieldKeep
}

// responseBlock is the header the client's response is written with for the
// upstream's, block, as ClientResponse.HeaderBlock has it: its lines but the
// hop-by-hop ones, those its Connection names and Content-Length, which the
// response's own framing replaces, and the sticky cookie after them. It is
// written in a buffer of the pool's, which the caller gives back once the
// response is written.
func (x *exchange) responseBlock(block string) []byte {
	dst := x.headBuffer(block)
	// The Connection field's values, as few as there are, for a second pass
	// when they name a field.
	var connection [2]string
	n := 0
	named := false
	for rest := block; rest != ""; {
		end := strings.IndexByte(rest, '\n') + 1
		line := rest[:end]
		rest = rest[end:]
		colon := strings.IndexByte(line, ':')
		name := line[:colon]
		if isHopByHopFold(name) {
			if len(name) == len("Connection") && strings.EqualFold(name, "Connection") {
				value := textproto.TrimString(line[colon+1:])
				if !onlyConnectionOptions(value) {
					named = true
					if n < len(connection) {
						connection[n] = value
					}
					n++
				}
			}
			continue
		}
		dst = append(dst, line...)
	}
	if named {
		bufferpool.Put(dst)
		if n > len(connection) {
			// More Connection fields than kept: read them all again.
			return x.responseBlockNamed(block, blockHeader(block)["Connection"])
		}
		return x.responseBlockNamed(block, connection[:n])
	}
	return x.appendCookie(dst)
}

// responseBlockNamed is responseBlock for a block whose Connection fields,
// connection, name fields of their own, which are dropped too.
func (x *exchange) responseBlockNamed(block string, connection []string) []byte {
	dst := x.headBuffer(block)
	for rest := block; rest != ""; {
		end := strings.IndexByte(rest, '\n') + 1
		line := rest[:end]
		rest = rest[end:]
		name := line[:strings.IndexByte(line, ':')]
		if isHopByHopFold(name) || connectionNames(connection, name) {
			continue
		}
		dst = append(dst, line...)
	}
	return x.appendCookie(dst)
}

// headBuffer is an empty buffer of the pool's with room for block and the
// sticky cookie.
func (x *exchange) headBuffer(block string) []byte {
	return bufferpool.Get(len(block) + len(x.setCookie) + len("Set-Cookie: \r\n"))[:0]
}

// appendCookie appends the sticky cookie's field to dst, if the response
// sets one, and returns it.
func (x *exchange) appendCookie(dst []byte) []byte {
	if x.setCookie != "" {
		dst = appendField(dst, "Set-Cookie: ", x.setCookie)
	}
	return dst
}

// onlyConnectionOptions reports whether value, a Connection field's, names
// nothing but keep-alive and close, as nearly every one does: Keep-Alive is
// dropped anyway, and there is no Close field.
func onlyConnectionOptions(value string) bool {
	for token := range strings.SplitSeq(value, ",") {
		token = textproto.TrimString(token)
		if token != "" && !strings.EqualFold(token, "keep-alive") && !strings.EqualFold(token, "close") {
			return false
		}
	}
	return true
}

// isHopByHopFold is isHopByHop for a name in any case, and Content-Length
// too, which a response written from a header block is framed by afresh.
func isHopByHopFold(name string) bool {
	switch len(name) {
	case 2:
		return strings.EqualFold(name, "Te")
	case 7:
		return strings.EqualFold(name, "Trailer") || strings.EqualFold(name, "Upgrade")
	case 10:
		return strings.EqualFold(name, "Connection") || strings.EqualFold(name, "Keep-Alive")
	case 14:
		return strings.EqualFold(name, "Content-Length")
	case 16:
		return strings.EqualFold(name, "Proxy-Connection")
	case 17:
		return strings.EqualFold(name, "Transfer-Encoding")
	case 18:
		return strings.EqualFold(name, "Proxy-Authenticate")
	case 19:
		return strings.EqualFold(name, "Proxy-Authorization")
	}
	return false
}

// blockHeader is block, header lines as ClientResponse.HeaderBlock has them,
// as a map, for what reads the upstream's response header by name.
func blockHeader(block string) stdhttp.Header {
	h := make(stdhttp.Header, strings.Count(block, "\n"))
	for block != "" {
		end := strings.IndexByte(block, '\n')
		line := block[:end-1]
		block = block[end+1:]
		name, value, _ := strings.Cut(line, ":")
		key := textproto.CanonicalMIMEHeaderKey(name)
		h[key] = append(h[key], textproto.TrimString(value))
	}
	return h
}

// connectionNames reports whether connection, a Connection field's values,
// names the field name.
func connectionNames(connection []string, name string) bool {
	for _, value := range connection {
		for token := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(textproto.TrimString(token), name) {
				return true
			}
		}
	}
	return false
}

// appendField appends a header line of prefix, the field's name and its
// colon and space, and value.
func appendField(dst []byte, prefix, value string) []byte {
	dst = append(dst, prefix...)
	dst = append(dst, value...)
	return append(dst, "\r\n"...)
}

// setForwarded sets the fields that tell the upstream about the client. The
// fields' values go in x.fields.forwarded, and the Forwarded element is
// written in x.fields.element when it fits, so that they last as long as the
// header.
func (p *Proxy) setForwarded(c *fibhttp.Context, header stdhttp.Header, x *exchange) {
	r := c.Request
	clientIP, ip := clientAddr(r.RemoteAddr)
	trusted := p.isTrusted(clientIP)
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	values := &x.fields.forwarded
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

	// The bytes are x's and are not written again, so the string may be
	// made of them without a copy.
	b := appendForwardedElement(x.fields.element[:0], clientIP, ip, r.Host, proto)
	element := unsafe.String(unsafe.SliceData(b), len(b))
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

// appendForwardedElement appends to dst a Forwarded element (RFC 7239) for
// the client at ip, written as s.
func appendForwardedElement(dst []byte, ip netip.Addr, s, host, proto string) []byte {
	dst = append(dst, "for="...)
	switch {
	case !ip.IsValid():
		dst = append(dst, "unknown"...)
	case ip.Is6():
		dst = append(dst, `"[`...)
		dst = append(dst, s...)
		dst = append(dst, `]"`...)
	default:
		dst = append(dst, s...)
	}
	if host != "" {
		dst = append(dst, ";host="...)
		dst = appendForwardedValue(dst, host)
	}
	dst = append(dst, ";proto="...)
	return append(dst, proto...)
}

// appendForwardedValue appends v as a Forwarded parameter value: a token as
// it is, anything else quoted.
func appendForwardedValue(dst []byte, v string) []byte {
	if validToken(v) {
		return append(dst, v...)
	}
	// Printable ASCII with no quote or backslash, as a host and port are,
	// is quoted as it is, which is what AppendQuote would make of it.
	for i := range len(v) {
		if c := v[i]; c < ' ' || c > '~' || c == '"' || c == '\\' {
			return strconv.AppendQuote(dst, v)
		}
	}
	dst = append(dst, '"')
	dst = append(dst, v...)
	return append(dst, '"')
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
		if strings.IndexByte(value, ',') < 0 {
			// One name, as nearly every Connection field has.
			removeConnectionToken(h, value)
			continue
		}
		for name := range strings.SplitSeq(value, ",") {
			removeConnectionToken(h, name)
		}
	}
}

// removeConnectionToken deletes from h the field one of a Connection field's
// names names. The names nearly every Connection field holds are known
// already in their canonical form.
func removeConnectionToken(h stdhttp.Header, name string) {
	switch name = textproto.TrimString(name); {
	case name == "":
	case strings.EqualFold(name, "keep-alive"):
		delete(h, "Keep-Alive")
	case strings.EqualFold(name, "close"):
		delete(h, "Close")
	default:
		delete(h, textproto.CanonicalMIMEHeaderKey(name))
	}
}
