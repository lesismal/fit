package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdhttp "net/http"
	"net/textproto"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/upstream"
)

// Why a proxied request was cut short, as the cause of its context.
var (
	errClientGone    = errors.New("proxy: the client went away")
	errHeaderTimeout = errors.New("proxy: the upstream took too long to begin its response")
	errIdleTimeout   = errors.New("proxy: the upstream went silent in the middle of its response")
)

// brokenTrailer is the Fit-Error trailer's value on an HTTP/2 response whose
// upstream failed in the middle of a body of no declared length.
const brokenTrailer = "upstream failed mid-body"

// uploadError is a request body that failed on its way upstream, which
// statusOf tells apart from the upstream's own failure.
type uploadError struct{ err error }

func (e *uploadError) Error() string { return "request body: " + e.err.Error() }
func (e *uploadError) Unwrap() error { return e.err }

// ServeHTTP forwards the request and answers with the upstream's response,
// later, from the upstream client's callbacks.
//
// A body that arrived with the request goes out as it is. One still arriving
// is pushed upstream piece by piece from Context.OnBody through a
// fibhttp.BodyWriter: when the upstream falls behind, the body is held with
// Context.HoldBody, which stops reading the client, and resumed when the
// writer has room, so nothing waits and memory stays bounded.
//
// The upstream request is cancelled when the client goes away, when the
// upstream takes longer than its response_header_timeout to begin its
// response, and when it is silent for its response_idle_timeout in the
// middle of a body. A request that fails before its response begins gets 504
// for a timeout, the connect's included, and 502 for anything else; a body
// the client failed to send gets 400, 408 or 413, as the reason was. A
// response that fails after it began is cut short so the client can tell:
// its connection is closed on HTTP/1, and on HTTP/2 its stream is reset, or,
// for a body of no declared length, ended with a Fit-Error trailer.
func (p *Proxy) ServeHTTP(c *fibhttp.Context) {
	out := p.outgoing(c)
	if out == nil {
		_ = c.Respond(stdhttp.StatusBadRequest, "text/plain; charset=utf-8", []byte("400 Bad Request\n"))
		return
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	out = out.WithContext(ctx)
	// The backend is held until the exchange is over, body and all, so that
	// least_conn counts a long download as busy for as long as it lasts.
	x := &exchange{c: c, out: out, ctx: ctx, cancel: cancel, start: time.Now(), timeouts: p.pool.Timeouts(),
		pool: p.pool, backend: p.pool.Pick()}
	// The request, its body among it, stays the handler's until the
	// response is written.
	c.Retain()
	c.OnCancel(func(error) { cancel(errClientGone) })
	if t := x.timeouts.ResponseHeader; t > 0 {
		x.timer.Store(time.AfterFunc(t, func() { cancel(errHeaderTimeout) }))
	}

	r := c.Request
	announced := trailerNames(r)
	switch {
	case c.RequestBody() == nil && len(announced) == 0:
		p.pool.Send(x.backend, out, x.onResponse)
		return
	case c.RequestBody() == nil:
		// Whole, with trailers, whose values are all here: they are set
		// before the request goes, and only a body sent chunked carries them.
		out.Trailer = make(stdhttp.Header, len(announced))
		for _, name := range announced {
			out.Trailer[name] = r.Trailer[name]
		}
		body := fibhttp.NewBodyWriter(0)
		_, _ = body.Write(c.Body())
		_ = body.Close()
		out.Body, out.ContentLength = body, -1
		p.pool.Send(x.backend, out, x.onResponse)
		return
	case len(announced) > 0:
		p.forwardWithTrailers(c, x, out, announced)
		return
	}
	// A body still arriving goes up as it arrives, through a BodyWriter.
	body := fibhttp.NewBodyWriter(0)
	out.Body, out.ContentLength = body, r.ContentLength
	p.pool.Send(x.backend, out, x.onResponse)
	c.OnBody(func(data []byte, fin bool, err error) {
		switch {
		case err != nil:
			body.CloseWithError(&uploadError{err})
			return
		case len(data) > 0:
			if _, werr := body.Write(data); werr != nil {
				// The upstream request is over; the rest is not wanted.
				return
			}
		}
		if fin {
			_ = body.Close()
			return
		}
		// Held first, so that a drain that comes before OnDrain returns is
		// not missed.
		c.HoldBody(true)
		if !body.OnDrain(func() { c.HoldBody(false) }) {
			c.HoldBody(false)
		}
	})
}

// forwardWithTrailers forwards a body still arriving that announced
// trailers, whose values are known only once it has ended. fib's client reads
// a request's Trailer when it sends the header, which may be after the body
// has ended, so the values cannot be written into it from OnBody without
// racing that read. Instead the body is a trailerPipe, which fib's client
// reads on a goroutine of its own that it starts once the header has gone,
// and the pipe fills the values in on that goroutine just before it reports
// the end. That costs the upload a goroutine, which only uploads with
// trailers, rare as they are, pay.
func (p *Proxy) forwardWithTrailers(c *fibhttp.Context, x *exchange, out *stdhttp.Request, announced []string) {
	out.Trailer = make(stdhttp.Header, len(announced))
	for _, name := range announced {
		out.Trailer[name] = nil
	}
	pipe := &trailerPipe{c: c, out: out, in: c.Request}
	pipe.cond = sync.NewCond(&pipe.mu)
	out.Body, out.ContentLength = pipe, -1
	p.pool.Send(x.backend, out, x.onResponse)
	c.OnBody(pipe.push)
}

// trailerPipeLimit is how much of a body a trailerPipe holds before it holds
// the client's body back.
const trailerPipeLimit = 256 << 10

// trailerPipe carries a body with trailers from OnBody to the upstream
// client's reading goroutine; see forwardWithTrailers.
type trailerPipe struct {
	c   *fibhttp.Context
	out *stdhttp.Request // the upstream request, whose Trailer Read fills in
	in  *stdhttp.Request // the client's, whose Trailer has the values at the end

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	held   bool
	ended  bool
	err    error
	closed bool
}

// push takes a piece of the body, as OnBody delivers it, and holds the
// client's body while the pipe is full.
func (p *trailerPipe) push(data []byte, fin bool, err error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.buf = append(p.buf, data...)
	p.ended = p.ended || fin
	if err != nil {
		p.err = &uploadError{err}
	}
	hold := !p.held && len(p.buf) >= trailerPipeLimit && !fin
	p.held = p.held || hold
	p.cond.Signal()
	p.mu.Unlock()
	if hold {
		p.c.HoldBody(true)
	}
}

// Read waits for the body, as a body fib's client reads on its own goroutine
// may.
func (p *trailerPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	for len(p.buf) == 0 && !p.ended && p.err == nil && !p.closed {
		p.cond.Wait()
	}
	switch {
	case p.closed:
		p.mu.Unlock()
		return 0, io.ErrClosedPipe
	case len(p.buf) > 0:
		n := copy(b, p.buf)
		p.buf = p.buf[:copy(p.buf, p.buf[n:])]
		release := p.held && len(p.buf) < trailerPipeLimit/2
		p.held = p.held && !release
		p.mu.Unlock()
		if release {
			p.c.HoldBody(false)
		}
		return n, nil
	case p.err != nil:
		err := p.err
		p.mu.Unlock()
		return 0, err
	}
	p.mu.Unlock()
	// The end: the client's trailer is complete, and the upstream client
	// reads the request's once this Read has returned.
	for name, values := range p.in.Trailer {
		p.out.Trailer[name] = values
	}
	return 0, io.EOF
}

// Close ends the pipe, as fib's client does once the request is over.
func (p *trailerPipe) Close() error {
	p.mu.Lock()
	p.closed = true
	release := p.held
	p.held = false
	p.cond.Broadcast()
	p.mu.Unlock()
	if release {
		p.c.HoldBody(false)
	}
	return nil
}

// trailerNames returns the trailer fields r announced: the names in its
// Trailer map, which HTTP/1 fills from the Trailer header, and those in the
// Trailer header itself, which is all HTTP/2 has of them before the body
// ends.
func trailerNames(r *stdhttp.Request) []string {
	var names []string
	for name := range r.Trailer {
		names = append(names, name)
	}
	for _, value := range r.Header["Trailer"] {
		for name := range strings.SplitSeq(value, ",") {
			name = textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
			if name != "" && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

// exchange is one proxied request, seen from the upstream's callbacks, which
// come one at a time.
type exchange struct {
	c        *fibhttp.Context
	out      *stdhttp.Request
	ctx      context.Context
	cancel   context.CancelCauseFunc
	start    time.Time
	timeouts upstream.Timeouts
	pool     *upstream.Pool
	backend  *upstream.Backend
	// timer is the response header timeout's, then the idle timeout's.
	timer atomic.Pointer[time.Timer]

	resp    *fibhttp.ClientResponse
	started bool
	// failed says the upstream failed the request, which counts against the
	// backend's health.
	failed bool
	done   atomic.Bool
}

// release gives the client's request back, once, and lets go of the
// upstream's.
func (x *exchange) release() {
	if x.done.CompareAndSwap(false, true) {
		if t := x.timer.Load(); t != nil {
			t.Stop()
		}
		x.cancel(nil)
		x.pool.Done(x.backend, x.failed)
		x.c.Release()
	}
}

func (x *exchange) onResponse(resp *fibhttp.ClientResponse, err error) {
	if t := x.timer.Load(); t != nil {
		t.Stop()
	}
	if e := observe.EntryOf(x.c.Request); e != nil {
		e.Upstream, e.UpstreamDuration = x.backend.Address, time.Since(x.start)
	}
	if err != nil {
		x.fail(err)
		return
	}
	if resp.BodyComplete() {
		// The whole of it is here: one write, as small responses go.
		body, err := readBody(resp)
		if err != nil {
			x.fail(err)
			return
		}
		header := resp.Header
		removeHopByHop(header)
		_ = x.c.WriteResponse(fibhttp.Response{StatusCode: resp.StatusCode, Header: header, Body: body, Trailer: resp.Trailer()})
		x.release()
		return
	}
	x.resp = resp
	if t := x.timeouts.ResponseIdle; t > 0 {
		x.timer.Store(time.AfterFunc(t, func() { x.cancel(errIdleTimeout) }))
	}
	resp.OnBody(x.onBody)
}

// onBody streams the response body to the client. It runs on the worker that
// reads the upstream connection, on the upstream engine. A client slower than
// the upstream makes Write wait once 64KB are queued for it (HTTP/1), or the
// stream's window is spent (HTTP/2), and while it waits the upstream
// connection is not read: the upstream is held back by TCP or by its own
// stream window, and neither side's body piles up here.
func (x *exchange) onBody(data []byte, fin bool, err error) {
	if x.done.Load() {
		return
	}
	if err != nil {
		x.broken(err)
		return
	}
	if t := x.timer.Load(); t != nil && !fin {
		t.Reset(x.timeouts.ResponseIdle)
	}
	if !x.started {
		x.begin()
	}
	if len(data) > 0 {
		if _, werr := x.c.Write(data); werr != nil {
			// The client is gone; giving the response up stops the
			// upstream's callbacks.
			x.resp.Close()
			x.release()
			return
		}
		if !fin {
			x.c.Flush()
		}
	}
	if fin {
		h := x.c.Header()
		for name, values := range x.resp.Trailer() {
			h[stdhttp.TrailerPrefix+name] = values
		}
		_ = x.c.Finish()
		x.release()
	}
}

// begin sends the response's status and header to the client.
func (x *exchange) begin() {
	x.started = true
	h := x.c.Header()
	announced := x.resp.Header["Trailer"]
	for key, values := range x.resp.Header {
		h[key] = values
	}
	removeHopByHop(h)
	if len(announced) > 0 {
		h["Trailer"] = announced
	}
	delete(h, "Content-Length")
	if x.resp.ContentLength >= 0 {
		h["Content-Length"] = []string{strconv.FormatInt(x.resp.ContentLength, 10)}
	}
	x.c.WriteHeader(x.resp.StatusCode)
}

// fail answers a request whose upstream gave no response, unless the client
// is gone.
func (x *exchange) fail(err error) {
	if errors.Is(context.Cause(x.ctx), errClientGone) {
		x.release()
		return
	}
	status := x.statusOf(err)
	// 400, 408 and 413 are the client's body failing, not the upstream.
	x.failed = status == stdhttp.StatusBadGateway || status == stdhttp.StatusGatewayTimeout
	_ = x.c.Respond(status, "text/plain; charset=utf-8", []byte(strconv.Itoa(status)+" "+stdhttp.StatusText(status)+"\n"))
	x.release()
}

// statusOf is the status for a request that failed with err before its
// response began.
func (x *exchange) statusOf(err error) int {
	cause := context.Cause(x.ctx)
	var upload *uploadError
	switch {
	case errors.Is(cause, errHeaderTimeout), errors.Is(cause, errIdleTimeout):
		return stdhttp.StatusGatewayTimeout
	case errors.As(err, &upload):
		switch {
		case errors.Is(err, fibhttp.ErrBodyTooLarge):
			return stdhttp.StatusRequestEntityTooLarge
		case errors.Is(err, fibhttp.ErrBodyHeldTooLong):
			// The upstream stopped taking the body.
			return stdhttp.StatusGatewayTimeout
		case errors.Is(err, os.ErrDeadlineExceeded):
			// The client took too long to send it.
			return stdhttp.StatusRequestTimeout
		}
		return stdhttp.StatusBadRequest
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		// A connect that timed out.
		return stdhttp.StatusGatewayTimeout
	}
	return stdhttp.StatusBadGateway
}

// broken handles a response that failed after it began: the upstream broke
// off, went silent, or the client went away.
func (x *exchange) broken(err error) {
	if errors.Is(context.Cause(x.ctx), errClientGone) {
		x.release()
		return
	}
	if !x.started {
		x.fail(err)
		return
	}
	x.failed = true
	switch {
	case x.resp.ContentLength >= 0:
		// Short of its length: fib closes an HTTP/1 connection and resets
		// an HTTP/2 stream for it.
		_ = x.c.Finish()
	case x.c.Request.ProtoMajor == 1:
		// A chunked body without its last chunk; closing the connection is
		// what tells the client.
		_ = x.c.Conn.Close()
	default:
		x.c.Header().Set(stdhttp.TrailerPrefix+"Fit-Error", brokenTrailer)
		_ = x.c.Finish()
	}
	x.release()
}

// readBody reads a response body that arrived whole.
func readBody(resp *fibhttp.ClientResponse) ([]byte, error) {
	if resp.Body == nil {
		return nil, nil
	}
	n := resp.ContentLength
	if n < 0 || n > 1<<20 {
		n = 512
	}
	buf := bytes.NewBuffer(make([]byte, 0, n))
	_, err := io.Copy(buf, resp.Body)
	return buf.Bytes(), err
}
