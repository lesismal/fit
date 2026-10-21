package auth

import (
	"context"
	"errors"
	"net"
	stdhttp "net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/upstream"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".forward_auth", New: func() any { return new(ForwardAuth) }})
}

// DefaultForwardAuthTimeout is how long forward_auth waits for the auth
// service's answer when its timeout is left out.
const DefaultForwardAuthTimeout = 10 * time.Second

// ForwardAuth asks an auth service whether a request may go on: it sends a
// GET to the service, an upstream of the config's, with the request's
// fields and where it was going in X-Forwarded-Method, -Proto, -Host, -Uri
// and -For. A 2xx answer lets the request through, with the fields
// copy_headers names taken from the answer; any other is the client's
// answer, its status, fields and body, as a login redirect or a 401 would
// be. The request waits for the answer retained, through fib's
// asynchronous client, so it holds no worker.
type ForwardAuth struct {
	Upstream       string          `json:"upstream" fit:"required" doc:"The name of the upstream the auth service is."`
	URI            string          `json:"uri,omitempty" default:"\"/\"" doc:"The path, and query, of the request sent to the auth service; / when left out."`
	RequestHeaders []string        `json:"request_headers,omitempty" doc:"The request's fields sent to the auth service; all but the hop-by-hop ones and those of the body when left out."`
	CopyHeaders    []string        `json:"copy_headers,omitempty" doc:"Fields copied from a 2xx answer onto the request, for the handler and the upstream a proxy sends it to. Any of these the client sent are removed, so that it cannot pass them off as the auth service's."`
	Timeout        config.Duration `json:"timeout,omitempty" default:"\"10s\"" doc:"How long to wait for the auth service's answer, after which the request is answered 504 Gateway Timeout; 10s when left out. A service that cannot be reached is answered 502 Bad Gateway."`

	pool           *upstream.Pool
	url            url.URL
	requestHeaders []string // canonical
	copyHeaders    []string // canonical
	timeout        time.Duration
	graph          module.Holder

	mu   sync.Mutex
	open map[*authCall]struct{}
}

// Provision finds the upstream and checks the fields.
func (fa *ForwardAuth) Provision(ctx *module.Context) error {
	pool, ok := ctx.Upstream(fa.Upstream)
	if !ok {
		return ctx.Errorf("upstream", "there is no upstream named %q", fa.Upstream)
	}
	fa.pool = pool
	if fa.URI == "" {
		fa.URI = "/"
	}
	u, err := url.ParseRequestURI(fa.URI)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(fa.URI, "/") {
		return ctx.Errorf("uri", "%q is not a path, with a query or not", fa.URI)
	}
	fa.url = url.URL{Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}
	for i, list := range [][]string{fa.RequestHeaders, fa.CopyHeaders} {
		field := [...]string{"request_headers", "copy_headers"}[i]
		for _, name := range list {
			if !validName(name) {
				return ctx.Errorf(field, "%q is not a field name", name)
			}
			canonical := stdhttp.CanonicalHeaderKey(name)
			if i == 0 {
				fa.requestHeaders = append(fa.requestHeaders, canonical)
			} else {
				fa.copyHeaders = append(fa.copyHeaders, canonical)
			}
		}
	}
	fa.timeout = fa.Timeout.Std()
	if fa.timeout <= 0 {
		fa.timeout = DefaultForwardAuthTimeout
	}
	fa.graph = ctx.Graph()
	fa.open = map[*authCall]struct{}{}
	return nil
}

var (
	errAuthTimeout = errors.New("auth: the auth service did not answer in time")
	errDrained     = errors.New("auth: the config was drained")
)

// Drain ends the checks still waiting once the config has been swapped out
// for the drain timeout: their requests are answered 503.
func (fa *ForwardAuth) Drain() {
	fa.mu.Lock()
	open := make([]*authCall, 0, len(fa.open))
	for x := range fa.open {
		open = append(open, x)
	}
	fa.mu.Unlock()
	for _, x := range open {
		x.cancel(errDrained)
	}
}

// Wrap returns next behind the check.
func (fa *ForwardAuth) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		x := &authCall{fa: fa, c: c, next: next}
		req := fa.request(c, &x.url)
		// The request waits retained, holding its config, until the answer
		// has been dealt with, next included.
		c.Retain()
		fa.graph.Retain()
		fa.mu.Lock()
		fa.open[x] = struct{}{}
		fa.mu.Unlock()
		x.backend = fa.pool.Pick(req)
		c.OnCancel(x.cancel)
		x.timer = time.AfterFunc(fa.timeout, func() { x.cancel(errAuthTimeout) })
		call := fa.pool.Send(x.backend, req, x.answer)
		x.mu.Lock()
		x.call, x.sent = call, true
		err := x.cancelled
		x.mu.Unlock()
		if err != nil {
			call.Cancel(err)
		}
	})
}

// request is the request sent to the auth service for the one c serves.
func (fa *ForwardAuth) request(c *fibhttp.Context, u *url.URL) *stdhttp.Request {
	r := c.Request
	header := make(stdhttp.Header, len(r.Header)+5)
	if fa.requestHeaders != nil {
		for _, name := range fa.requestHeaders {
			if v, ok := r.Header[name]; ok {
				header[name] = v
			}
		}
	} else {
		for name, v := range r.Header {
			if !isHopByHop(name) && name != "Content-Length" && name != "Content-Type" && name != "Expect" {
				header[name] = v
			}
		}
		for _, name := range r.Header["Connection"] {
			for _, n := range strings.Split(name, ",") {
				delete(header, stdhttp.CanonicalHeaderKey(strings.TrimSpace(n)))
			}
		}
	}
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	clientIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(clientIP); err == nil {
		clientIP = host
	}
	header["X-Forwarded-Method"] = []string{r.Method}
	header["X-Forwarded-Proto"] = []string{proto}
	header["X-Forwarded-Host"] = []string{r.Host}
	header["X-Forwarded-Uri"] = []string{r.URL.RequestURI()}
	header["X-Forwarded-For"] = []string{clientIP}
	*u = fa.url
	return &stdhttp.Request{
		Method:     stdhttp.MethodGet,
		URL:        u,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     header,
		Host:       r.Host,
	}
}

// authCall is a request waiting for the auth service's answer.
type authCall struct {
	fa      *ForwardAuth
	c       *fibhttp.Context
	next    module.Handler
	url     url.URL
	backend *upstream.Backend
	timer   *time.Timer

	mu        sync.Mutex
	call      fibhttp.Call
	sent      bool  // call is set
	cancelled error // why it was cancelled before it was sent
}

// cancel abandons the subrequest; its callback then runs with err.
func (x *authCall) cancel(err error) {
	x.mu.Lock()
	if !x.sent {
		if x.cancelled == nil {
			x.cancelled = err
		}
		x.mu.Unlock()
		return
	}
	call := x.call
	x.mu.Unlock()
	call.Cancel(err)
}

// answer deals with the auth service's answer, once: it runs exactly once,
// with the answer or why there is none.
func (x *authCall) answer(resp *fibhttp.ClientResponse, err error) {
	fa, c := x.fa, x.c
	x.timer.Stop()
	fa.mu.Lock()
	delete(fa.open, x)
	fa.mu.Unlock()
	defer fa.graph.Release()
	defer c.Release()
	failed := err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errDrained) &&
		!errors.Is(err, upstream.ErrBusy) && !errors.Is(err, upstream.ErrClosed)
	fa.pool.Done(x.backend, failed)
	switch {
	case err != nil && c.Err() != nil:
		return // the client has gone
	case errors.Is(err, errAuthTimeout):
		_ = c.Respond(stdhttp.StatusGatewayTimeout, "", nil)
		return
	case errors.Is(err, errDrained):
		_ = c.Respond(stdhttp.StatusServiceUnavailable, "", nil)
		return
	case err != nil:
		_ = c.Respond(stdhttp.StatusBadGateway, "", nil)
		return
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		resp.Close() // a body is not wanted, and one still arriving is given up
		h := c.Request.Header
		for _, name := range fa.copyHeaders {
			delete(h, name)
			if v, ok := resp.Header[name]; ok {
				h[name] = append([]string(nil), v...)
			}
		}
		x.next.ServeHTTP(c)
		return
	}
	// The answer is the client's. A body that arrived whole (up to the
	// pool's 64 KiB threshold) goes with it; a larger one, still arriving,
	// is given up, and the answer goes without.
	header := make(stdhttp.Header, len(resp.Header))
	for name, v := range resp.Header {
		if !isHopByHop(name) && name != "Content-Length" {
			header[name] = v
		}
	}
	body, whole := resp.BodyBytes()
	if !whole {
		resp.Close()
		body = nil
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: resp.StatusCode, Header: header, Body: body})
}

// isHopByHop reports whether the canonical name is of a hop-by-hop field.
func isHopByHop(name string) bool {
	switch name {
	case "Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}
