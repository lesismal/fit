package media

import (
	"bytes"
	"errors"
	"mime"
	"net"
	stdhttp "net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/webrtc/ice"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/upstream"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".webrtc_proxy", New: func() any { return new(ProxyHandler) },
		Doc: "Proxies WHIP and WHEP to the media servers of an upstream, and their peers' media through webrtc.proxy's one UDP port."})
}

// The defaults of ProxyHandler.
const (
	DefaultProxyTimeout = 10 * time.Second
)

// ProxyHandler is the webrtc_proxy handler: it sends a WHIP or WHEP offer
// to a media server of its upstream, and answers with the media server's
// answer, its candidates replaced by webrtc.proxy's port and its Location
// by a URL of fit's, which DELETE, PATCH and the rest follow to the same
// media server.
type ProxyHandler struct {
	Upstream    string            `json:"upstream" fit:"required" doc:"The upstream of the media servers, picked by its load balancing for each offer: fit's own whip and whep, or any WHIP or WHEP server. Its health checks and timeouts apply."`
	MediaAddrs  map[string]string `json:"media_addrs,omitempty" doc:"The address fit relays a media server's peers to, by the media server's address in the upstream, as {\"10.0.0.2:8080\": \"10.0.0.2:8189\"}, or an IP alone, which takes the port of its answer's candidate. When left out, the answer's first host candidate, or server-reflexive one, IPv4 first."`
	Timeout     config.Duration   `json:"timeout,omitempty" default:"\"10s\"" doc:"The most a request may take, from its last byte to the media server's answer read whole; past it, it is answered 504. 10s when left out."`
	MaxSDPBytes int               `json:"max_sdp_bytes,omitempty" min:"0" default:"32768" doc:"The largest request and answer passed on, an offer and an answer among them; a larger request is answered 413, and a larger answer 502. 32 KiB when left out or 0."`

	pool    *upstream.Pool
	engine  *fib.Engine
	media   map[string]netip.AddrPort
	timeout time.Duration
	limit   int
}

// Provision finds the upstream and parses media_addrs.
func (h *ProxyHandler) Provision(ctx *module.Context) error {
	pool, ok := ctx.Upstream(h.Upstream)
	if !ok {
		return ctx.Errorf("upstream", "there is no upstream named %q", h.Upstream)
	}
	h.pool, h.engine = pool, ctx.Engine()
	h.media = map[string]netip.AddrPort{}
	for k, v := range h.MediaAddrs {
		a, err := netip.ParseAddrPort(v)
		if err != nil {
			ip, err2 := netip.ParseAddr(v)
			if err2 != nil || ip.IsUnspecified() {
				return ctx.Params.Field("media_addrs").Field(k).Errorf("%q is not an IP address, or an IP and a port", v)
			}
			a = netip.AddrPortFrom(ip, 0)
		}
		h.media[k] = netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
	}
	h.timeout = h.Timeout.Std()
	if h.timeout <= 0 {
		h.timeout = DefaultProxyTimeout
	}
	h.limit = h.MaxSDPBytes
	if h.limit <= 0 {
		h.limit = config.DefaultMediaMaxSDPBytes
	}
	return nil
}

// ServeHTTP proxies an endpoint's POST and OPTIONS to a media server the
// upstream picks, and a session's requests, its URL or a URL below it, to
// the one that answered it.
func (h *ProxyHandler) ServeHTTP(c *fibhttp.Context) {
	p := LookupProxy(h.engine)
	if p == nil {
		respond(c, stdhttp.StatusServiceUnavailable, "webrtc.proxy is not running", nil)
		return
	}
	path := c.Request.URL.Path
	if s, base, rest := sessionIn(p, path); s != nil {
		h.session(c, p, s, base, rest)
		return
	}
	switch c.Request.Method {
	case stdhttp.MethodPost:
		h.offer(c, p, strings.TrimSuffix(path, "/"))
	case stdhttp.MethodOptions:
		h.forward(c, h.pool.Pick(c.Request), c.Request.URL.Path, nil, h.passOn)
	default:
		if sessionID.MatchString(path[strings.LastIndexByte(path, '/')+1:]) {
			respond(c, stdhttp.StatusNotFound, "no such session", nil)
			return
		}
		respond(c, stdhttp.StatusMethodNotAllowed, "the endpoint takes POST", stdhttp.Header{"Allow": {"POST, OPTIONS"}})
	}
}

// sessionIn finds a session's ID among the last segments of path: base is
// the session's URL in fit, and rest what follows it.
func sessionIn(p *Proxy, path string) (s *proxySession, base, rest string) {
	end := len(path)
	for range 4 {
		i := strings.LastIndexByte(path[:end], '/')
		if i < 0 {
			return nil, "", ""
		}
		if seg := path[i+1 : end]; sessionID.MatchString(seg) {
			if s = p.Session(seg); s != nil {
				return s, path[:end], path[end:]
			}
		}
		end = i
	}
	return nil, "", ""
}

func (h *ProxyHandler) offer(c *fibhttp.Context, p *Proxy, endpoint string) {
	if mt, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type")); err != nil || mt != "application/sdp" {
		respond(c, stdhttp.StatusUnsupportedMediaType, "the offer must be application/sdp", stdhttp.Header{"Accept-Post": {"application/sdp"}})
		return
	}
	if c.Request.ContentLength > int64(h.limit) {
		respond(c, stdhttp.StatusRequestEntityTooLarge, "the offer is too large", nil)
		return
	}
	if !p.Reserve() {
		respond(c, stdhttp.StatusServiceUnavailable, "too many sessions", stdhttp.Header{"Retry-After": {"1"}})
		return
	}
	b := h.pool.Pick(c.Request)
	h.forward(c, b, c.Request.URL.Path, stripCandidates, func(x *exchange, resp *stdhttp.Response, body []byte, err error) {
		if err != nil || resp.StatusCode != stdhttp.StatusCreated {
			p.Unreserve()
			h.passOn(x, resp, body, err)
			return
		}
		loc := upstreamPath(x.out.URL, resp.Header.Get("Location"))
		if loc == "" {
			p.Unreserve()
			respond(x.c, stdhttp.StatusBadGateway, "the media server's answer has no Location", nil)
			return
		}
		sdp, creds, err := rewriteSDP(body, p.Addrs(), h.media[b.Address])
		if err == nil && (len(creds) == 0 || !creds[0].Media.IsValid()) {
			err = errors.New("the answer has no ice-ufrag, or no candidate fit can relay to")
		}
		id := ""
		if err == nil {
			id, err = p.Add(b.Address, loc, creds)
		} else {
			p.Unreserve()
		}
		if err != nil {
			respond(x.c, stdhttp.StatusBadGateway, "the media server's answer: "+err.Error(), nil)
			h.hangUp(b.Address, loc)
			return
		}
		hdr := responseHeader(resp.Header)
		hdr.Set("Location", endpoint+"/"+id)
		setLinks(hdr, resp.Header["Link"], x.out.URL, loc, endpoint+"/"+id)
		if x.c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusCreated, Header: hdr, Body: sdp}) != nil {
			// The peer left before its answer: its session goes too.
			p.End(id)
			h.hangUp(b.Address, loc)
		}
	})
}

// session proxies a request to session s's URL, base in fit, or a URL
// below it, to the media server that answered it.
func (h *ProxyHandler) session(c *fibhttp.Context, p *Proxy, s *proxySession, base, rest string) {
	b := h.pool.PickBackend(s.Backend)
	if b == nil {
		if c.Request.Method == stdhttp.MethodDelete {
			p.End(s.ID)
		}
		respond(c, stdhttp.StatusBadGateway, "the session's media server is no longer in the upstream", nil)
		return
	}
	target := s.Location + rest
	switch {
	case c.Request.Method == stdhttp.MethodDelete && rest == "":
		h.forward(c, b, target, nil, func(x *exchange, resp *stdhttp.Response, body []byte, err error) {
			p.End(s.ID)
			h.passOn(x, resp, body, err)
		})
	case c.Request.Method == "PATCH" && rest == "":
		h.patch(c, p, s, b, target, base)
	default:
		h.forward(c, b, target, nil, func(x *exchange, resp *stdhttp.Response, body []byte, err error) {
			if err == nil {
				setLinks(resp.Header, resp.Header["Link"], x.out.URL, s.Location, base)
			}
			h.passOn(x, resp, body, err)
		})
	}
}

// patch takes a session's PATCH: trickled candidates are answered 204 here,
// since the media server reaches the peer through fit whatever its
// candidates; an ICE restart, with an ice-ufrag, goes to the media server
// without the candidates, and its answer's new ufrag is routed as the
// first's.
func (h *ProxyHandler) patch(c *fibhttp.Context, p *Proxy, s *proxySession, b *upstream.Backend, target, base string) {
	h.forward(c, b, target, func(body []byte) ([]byte, error) {
		if !bytes.Contains(body, []byte("a=ice-ufrag:")) {
			return nil, errTrickle
		}
		return stripCandidates(body)
	}, func(x *exchange, resp *stdhttp.Response, body []byte, err error) {
		if errors.Is(err, errTrickle) {
			respond(x.c, stdhttp.StatusNoContent, "", nil)
			return
		}
		if err == nil && resp.StatusCode/100 == 2 && len(body) > 0 {
			var creds []Cred
			body, creds, err = rewriteSDP(body, p.Addrs(), h.media[b.Address])
			if err == nil {
				err = p.Restart(s.ID, creds)
			}
			if err != nil {
				respond(x.c, stdhttp.StatusBadGateway, "the media server's answer: "+err.Error(), nil)
				return
			}
		}
		if err == nil {
			setLinks(resp.Header, resp.Header["Link"], x.out.URL, s.Location, base)
		}
		h.passOn(x, resp, body, err)
	})
}

// hangUp DELETEs a session fit will not route to, at the media server that
// made it, and forgets the answer.
func (h *ProxyHandler) hangUp(backend, location string) {
	b := h.pool.PickBackend(backend)
	if b == nil {
		return
	}
	req, _ := stdhttp.NewRequest(stdhttp.MethodDelete, "http://"+backend+location, nil)
	h.pool.Send(b, req, func(resp *fibhttp.ClientResponse, err error) {
		if resp != nil {
			resp.Close()
		}
		h.pool.Done(b, err != nil)
	})
}

// passOn writes the media server's response as it came, or the status for
// the error that kept it from coming.
func (h *ProxyHandler) passOn(x *exchange, resp *stdhttp.Response, body []byte, err error) {
	if err != nil {
		var se statusError
		switch {
		case errors.As(err, &se):
			respond(x.c, se.code, se.msg, nil)
		case errors.Is(err, errProxyTimeout) || isTimeout(err):
			respond(x.c, stdhttp.StatusGatewayTimeout, "the media server did not answer in time", nil)
		default:
			respond(x.c, stdhttp.StatusBadGateway, "the media server could not be reached", nil)
		}
		return
	}
	_ = x.c.WriteResponse(fibhttp.Response{StatusCode: resp.StatusCode, Header: responseHeader(resp.Header), Body: body})
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// hopByHop are the fields a proxy does not pass on (RFC 9110 section 7.6.1),
// and Content-Length, which is set for the body sent.
var hopByHop = []string{"Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length"}

func responseHeader(in stdhttp.Header) stdhttp.Header {
	out := in.Clone()
	for _, k := range hopByHop {
		out.Del(k)
	}
	out.Del("Location")
	out.Del("Link")
	return out
}

// upstreamPath returns the path, and query, of the session's URL loc,
// resolved against the request that made it, or "".
func upstreamPath(req *url.URL, loc string) string {
	if loc == "" {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	u = req.ResolveReference(u)
	return u.RequestURI()
}

// linkTarget is a Link field value's URI and the rest of it.
var linkTarget = regexp.MustCompile(`^\s*<([^>]*)>(.*)$`)

// setLinks sets hdr's Link fields from the media server's: an ICE server's
// as it is, one to a URL below the session's, loc, as the same URL below
// its URL in fit, base; the rest, which fit would not proxy, are dropped.
func setLinks(hdr stdhttp.Header, links []string, req *url.URL, loc, base string) {
	var out []string
	for _, field := range links {
		for _, l := range splitLinks(field) {
			m := linkTarget.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			if strings.Contains(m[2], "ice-server") {
				out = append(out, strings.TrimSpace(l))
				continue
			}
			u, err := url.Parse(m[1])
			if err != nil || u.Host != "" && u.Host != req.Host {
				continue
			}
			if p := req.ResolveReference(u).RequestURI(); strings.HasPrefix(p, loc) {
				out = append(out, "<"+base+p[len(loc):]+">"+m[2])
			}
		}
	}
	hdr.Del("Link")
	if len(out) > 0 {
		hdr["Link"] = out
	}
}

// splitLinks splits a Link field into its links, at commas outside <> and
// quotes.
func splitLinks(field string) []string {
	var out []string
	inURI, inQuote, start := false, false, 0
	for i := 0; i < len(field); i++ {
		switch c := field[i]; {
		case c == '<' && !inQuote:
			inURI = true
		case c == '>' && !inQuote:
			inURI = false
		case c == '"' && !inURI:
			inQuote = !inQuote
		case c == ',' && !inURI && !inQuote:
			out = append(out, field[start:i])
			start = i + 1
		}
	}
	return append(out, field[start:])
}

// statusError is a request fit answers itself, with code.
type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return e.msg }

var (
	errProxyTimeout = errors.New("the media server did not answer in time")
	errProxyClient  = errors.New("the client left")
)

// exchange is a request passed to a media server, its body and its
// response's each gathered whole, up to the handler's max_sdp_bytes.
type exchange struct {
	h       *ProxyHandler
	c       *fibhttp.Context
	backend *upstream.Backend
	out     *stdhttp.Request
	edit    func([]byte) ([]byte, error)
	finish  func(x *exchange, resp *stdhttp.Response, body []byte, err error)

	mu       sync.Mutex
	buf      []byte
	sent     bool
	ended    bool
	retained bool
	call     fibhttp.Call
	timer    *time.Timer
	doneOnce sync.Once
}

// forward sends the request c serves to b, at path, its body edited by
// edit when not nil, and hands finish the response or the error, edit's
// among them. b was picked for it.
func (h *ProxyHandler) forward(c *fibhttp.Context, b *upstream.Backend, path string, edit func([]byte) ([]byte, error),
	finish func(x *exchange, resp *stdhttp.Response, body []byte, err error)) {
	u, err := url.Parse(path)
	if err != nil {
		u = &url.URL{Path: path}
	}
	if u.RawQuery == "" {
		u.RawQuery = c.Request.URL.RawQuery
	}
	out := &stdhttp.Request{Method: c.Request.Method, URL: u, Host: c.Request.Host, Header: c.Request.Header.Clone()}
	for _, k := range hopByHop {
		out.Header.Del(k)
	}
	if ip, _, err := net.SplitHostPort(c.Request.RemoteAddr); err == nil {
		out.Header.Set("X-Forwarded-For", ip)
	}
	x := &exchange{h: h, c: c, backend: b, out: out, edit: edit, finish: finish}
	c.OnBody(x.body)
}

// errTrickle ends a PATCH that only trickles before it is sent: it is
// answered 204 here.
var errTrickle = errors.New("trickle")

func (x *exchange) body(data []byte, fin bool, err error) {
	x.mu.Lock()
	if x.ended {
		x.mu.Unlock()
		return
	}
	if err != nil {
		x.mu.Unlock()
		x.end(nil, nil, errProxyClient)
		return
	}
	if len(x.buf)+len(data) > x.h.limit {
		x.mu.Unlock()
		x.end(nil, nil, statusError{stdhttp.StatusRequestEntityTooLarge, "the request is too large"})
		return
	}
	x.buf = append(x.buf, data...)
	if !fin {
		x.mu.Unlock()
		return
	}
	body := x.buf
	x.buf = nil
	x.mu.Unlock()
	if x.edit != nil {
		if body, err = x.edit(body); err != nil {
			x.end(nil, nil, err)
			return
		}
	}
	x.out.Body, x.out.ContentLength = fibhttp.NewBufferedBody(body), int64(len(body))
	x.c.Retain()
	x.mu.Lock()
	x.retained, x.sent = true, true
	x.mu.Unlock()
	x.c.OnCancel(func(error) { x.end(nil, nil, errProxyClient) })
	x.timer = time.AfterFunc(x.h.timeout, func() { x.end(nil, nil, errProxyTimeout) })
	call := x.h.pool.Send(x.backend, x.out, x.response)
	x.mu.Lock()
	x.call = call
	ended := x.ended
	x.mu.Unlock()
	if ended {
		call.Cancel(errProxyTimeout)
	}
}

// response gathers the media server's response whole.
func (x *exchange) response(resp *fibhttp.ClientResponse, err error) {
	if err != nil {
		x.upDone(!errors.Is(err, errProxyClient))
		x.end(nil, nil, err)
		return
	}
	var buf []byte
	over := false
	resp.OnBody(func(data []byte, fin bool, err error) {
		switch {
		case over:
		case err != nil:
			x.upDone(true)
			x.end(nil, nil, err)
		case len(buf)+len(data) > x.h.limit:
			over = true
			x.upDone(false)
			resp.Close()
			x.end(nil, nil, statusError{stdhttp.StatusBadGateway, "the media server's response is too large"})
		default:
			buf = append(buf, data...)
			if fin {
				x.upDone(false)
				x.end(resp.Response, buf, nil)
			}
		}
	})
}

func (x *exchange) upDone(failed bool) {
	x.doneOnce.Do(func() { x.h.pool.Done(x.backend, failed) })
}

// end finishes the exchange once: with the response, or with why there is
// none, which cancels the request.
func (x *exchange) end(resp *stdhttp.Response, body []byte, err error) {
	x.mu.Lock()
	if x.ended {
		x.mu.Unlock()
		return
	}
	x.ended = true
	sent, retained, call := x.sent, x.retained, x.call
	x.mu.Unlock()
	if x.timer != nil {
		x.timer.Stop()
	}
	if !sent {
		x.upDone(false)
	} else if err != nil && resp == nil {
		call.Cancel(err)
	}
	if !errors.Is(err, errProxyClient) {
		x.finish(x, resp, body, err)
	} else {
		x.finish(x, nil, nil, err)
	}
	if retained {
		x.c.Release()
	}
}

// stripCandidates drops a description's candidates, and its
// end-of-candidates, so that the media server learns its peer's address
// from the checks fit relays, and never sends to the peer past fit.
func stripCandidates(sdp []byte) ([]byte, error) {
	var out []byte
	for _, l := range bytes.SplitAfter(sdp, []byte("\n")) {
		t := bytes.TrimRight(l, "\r\n")
		if bytes.HasPrefix(t, []byte("a=candidate:")) || bytes.Equal(t, []byte("a=end-of-candidates")) {
			continue
		}
		out = append(out, l...)
	}
	return out, nil
}

// rewriteSDP replaces the candidates of an answer, or of a trickle-ice
// fragment, with fit's addrs as host candidates, and returns its ICE
// credentials, each with the media server's address fit relays to: over,
// or with a port of 0 over's IP and its best candidate's port, or its best
// candidate, a host one before a server-reflexive one and IPv4 first,
// among the UDP candidates of the RTP component.
func rewriteSDP(sdp []byte, addrs []netip.AddrPort, over netip.AddrPort) ([]byte, []Cred, error) {
	if len(addrs) == 0 {
		return nil, nil, errors.New("webrtc.proxy has no address")
	}
	lines := strings.Split(strings.TrimRight(string(sdp), "\r\n"), "\n")
	var best ice.Candidate
	rank := 99
	var creds []Cred
	ufrag, pwd := "", ""
	flush := func() {
		if ufrag != "" && pwd != "" && !containsUfrag(creds, ufrag) {
			creds = append(creds, Cred{Ufrag: ufrag, Pwd: pwd})
		}
	}
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		lines[i] = l
		switch {
		case strings.HasPrefix(l, "m="):
			flush()
		case strings.HasPrefix(l, "a=ice-ufrag:"):
			ufrag = l[len("a=ice-ufrag:"):]
		case strings.HasPrefix(l, "a=ice-pwd:"):
			pwd = l[len("a=ice-pwd:"):]
		case strings.HasPrefix(l, "a=candidate:"):
			c, err := ice.ParseCandidate(l)
			// The relay sends UDP to the RTP component: a TCP candidate's
			// port, or RTCP's, is not where the media server reads it.
			if err != nil || c.Transport != "udp" || c.Component != 1 || !c.Address.IsValid() || c.Address.IsLinkLocalUnicast() {
				continue
			}
			r := 0
			switch c.Type {
			case ice.Host:
			case ice.ServerReflexive:
				r = 2
			default:
				continue
			}
			if !c.Address.Unmap().Is4() {
				r++
			}
			if r < rank {
				best, rank = c, r
			}
		}
	}
	flush()
	media := netip.AddrPort{}
	switch {
	case over.Port() != 0:
		media = over
	case over.Addr().IsValid() && rank < 99:
		media = netip.AddrPortFrom(over.Addr(), best.Port)
	case rank < 99:
		media = netip.AddrPortFrom(best.Address.Unmap(), best.Port)
	}
	for i := range creds {
		creds[i].Media = media
	}
	var cands []string
	for i, a := range addrs {
		c := ice.Candidate{Foundation: strconv.Itoa(i + 1), Component: 1, Transport: "udp",
			Priority: ice.Priority(ice.Host, uint16(65535-i), 1), Address: a.Addr(), Port: a.Port(), Type: ice.Host}
		cands = append(cands, "a="+c.String())
	}
	cands = append(cands, "a=end-of-candidates")
	var out []string
	media0 := -1 // the index in out of the media section being written, or -1
	closeSection := func() {
		if media0 < 0 {
			return
		}
		if f := strings.Fields(out[media0]); len(f) > 1 && f[1] != "0" {
			out = append(out, cands...)
		}
	}
	sections := 0
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "a=candidate:"), l == "a=end-of-candidates":
			continue
		case strings.HasPrefix(l, "m="):
			closeSection()
			media0 = len(out)
			sections++
		case strings.HasPrefix(l, "c=IN "):
			ip := addrs[0].Addr()
			family := "IP4"
			if !ip.Is4() {
				family = "IP6"
			}
			l = "c=IN " + family + " " + ip.String()
		}
		out = append(out, l)
	}
	closeSection()
	if sections == 0 {
		out = append(out, cands...)
	}
	return []byte(strings.Join(out, "\r\n") + "\r\n"), creds, nil
}

func containsUfrag(creds []Cred, u string) bool {
	for _, c := range creds {
		if c.Ufrag == u {
			return true
		}
	}
	return false
}
