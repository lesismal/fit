package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdhttp "net/http"
	"os"
	"strconv"
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
	if c.RequestBody() == nil && len(r.Trailer) == 0 {
		p.pool.Send(x.backend, out, x.onResponse)
		return
	}
	// A body still arriving, or one that came with trailers, which only a
	// body sent chunked carries: it goes through a BodyWriter.
	body := fibhttp.NewBodyWriter(0)
	out.Body, out.ContentLength = body, r.ContentLength
	if len(r.Trailer) > 0 {
		// The names now, for the header; the values once the body is done.
		out.Trailer = make(stdhttp.Header, len(r.Trailer))
		for name := range r.Trailer {
			out.Trailer[name] = nil
		}
		out.ContentLength = -1
	}
	endBody := func() {
		// A trailer goes only with a chunked body, which is what a body of
		// unknown length is sent as; on HTTP/2 its names may first be known
		// here, once it has arrived.
		if len(r.Trailer) > 0 && out.ContentLength < 0 {
			if out.Trailer == nil {
				out.Trailer = make(stdhttp.Header, len(r.Trailer))
			}
			for name, values := range r.Trailer {
				out.Trailer[name] = values
			}
		}
		_ = body.Close()
	}
	if c.RequestBody() == nil {
		// Whole, but with trailers.
		_, _ = body.Write(c.Body())
		p.pool.Send(x.backend, out, x.onResponse)
		endBody()
		return
	}
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
			endBody()
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
	done    atomic.Bool
}

// release gives the client's request back, once, and lets go of the
// upstream's.
func (x *exchange) release() {
	if x.done.CompareAndSwap(false, true) {
		if t := x.timer.Load(); t != nil {
			t.Stop()
		}
		x.cancel(nil)
		x.pool.Done(x.backend)
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
