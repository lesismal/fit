package proxy

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	stdhttp "net/http"
	"net/textproto"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/upstream"
)

// Why a proxied request was cut short, as the cause of its context.
var (
	errClientGone    = errors.New("proxy: the client went away")
	errHeaderTimeout = errors.New("proxy: the upstream took too long to begin its response")
	errIdleTimeout   = errors.New("proxy: the upstream went silent in the middle of its response")
	errDrained       = errors.New("proxy: the config the request started on was swapped out the drain timeout ago")
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
	x := &exchange{c: c}
	if !p.outgoing(c, x) {
		_ = c.Respond(stdhttp.StatusBadRequest, "text/plain; charset=utf-8", badRequest)
		return
	}
	backend := p.pool.Pick(c.Request)
	if backend == nil {
		// max_pending requests wait for a connection already.
		_ = c.Respond(stdhttp.StatusServiceUnavailable, "text/plain; charset=utf-8", unavailable)
		return
	}
	ctx, cancel := context.WithCancelCause(p.drains[rand.Uint32()%drainShards].ctx)
	out := x.base.WithContext(ctx)
	// The backend is held until the exchange is over, body and all, so that
	// least_conn counts a long download as busy for as long as it lasts;
	// and the config, so that a download that outlasts a reload stays on
	// the config it started on, until the drain timeout.
	p.graph.Retain()
	x.out, x.ctx, x.cancel, x.start, x.timeouts = out, ctx, cancel, time.Now(), p.pool.Timeouts()
	x.stall, x.pool, x.backend, x.setCookie, x.graph = p.stall, p.pool, backend, p.pool.SetCookie(c.Request, backend), p.graph
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
	}
	// A body still arriving goes up as it arrives, through a BodyWriter.
	// One that announced trailers is sent chunked, with their names in its
	// Trailer before it goes and their values, known only once it has
	// ended, handed over as it closes.
	body := fibhttp.NewBodyWriter(0)
	out.Body, out.ContentLength = body, r.ContentLength
	if len(announced) > 0 {
		out.Trailer = make(stdhttp.Header, len(announced))
		for _, name := range announced {
			out.Trailer[name] = nil
		}
		out.ContentLength = -1
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
			if len(announced) > 0 {
				_ = body.CloseWithTrailer(announcedTrailer(r, announced))
			} else {
				_ = body.Close()
			}
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

// announcedTrailer is the trailer r's body ended with, of the fields it
// announced.
func announcedTrailer(r *stdhttp.Request, announced []string) stdhttp.Header {
	trailer := make(stdhttp.Header, len(announced))
	for _, name := range announced {
		if values, ok := r.Trailer[name]; ok {
			trailer[name] = values
		}
	}
	return trailer
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

var (
	badRequest  = []byte("400 Bad Request\n")
	unavailable = []byte("503 Service Unavailable\n")
)

// exchange is one proxied request, seen from the upstream's callbacks, which
// come one at a time.
type exchange struct {
	c *fibhttp.Context
	// base is the request outgoing made, which out is with ctx; url and
	// forwarded are its URL and its forwarding fields' values, kept here so
	// that the exchange is one allocation rather than several.
	base      stdhttp.Request
	url       url.URL
	forwarded [4]string
	out       *stdhttp.Request
	ctx       context.Context
	cancel    context.CancelCauseFunc
	start     time.Time
	timeouts  upstream.Timeouts
	// stall bounds each wait for the client to take what was written.
	stall   time.Duration
	pool    *upstream.Pool
	backend *upstream.Backend
	// setCookie is the sticky cookie the response sets, or "".
	setCookie string
	graph     module.Holder
	// timer is the response header timeout's, then the idle timeout's.
	timer atomic.Pointer[time.Timer]

	// mu orders what the upstream body's callbacks and OnWritable's do to
	// the client's response, since a resumed body and OnWritable's function
	// run on goroutines of fib's.
	mu      sync.Mutex
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
		x.graph.Release()
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
		x.addCookie(header)
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
// reads the upstream connection, or on fib's side pool once a held body
// resumes, and never waits: each piece goes out with WriteNoWait, and once
// more than the response's limit is queued for the client, the upstream body
// is held with HoldBody until OnWritable says the client has taken half of
// it. While it is held, the upstream connection is read no further on
// HTTP/1, and on HTTP/2 and HTTP/3 this stream alone gets no more window, so
// a slow client holds back its own download and nothing else, and neither
// side's body piles up here.
//
// Each wait for the client is bounded by the write stall timeout: the write
// deadline is set when the body is held and lifted when the client catches
// up, so a client that stops reading loses its response (fib closes its
// HTTP/1 connection or resets its HTTP/2 or HTTP/3 stream, and OnWritable's
// function hears of it), and the upstream response is given up.
func (x *exchange) onBody(data []byte, fin bool, err error) {
	x.mu.Lock()
	defer x.mu.Unlock()
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
		above, werr := x.c.WriteNoWait(data)
		if werr == nil && !fin {
			// Held back while the response is short; out at once, as a
			// stream of events needs.
			werr = x.c.FlushError()
		}
		if werr != nil {
			x.giveUp()
			return
		}
		if above && !fin {
			x.hold()
		}
	}
	if fin {
		h := x.c.Header()
		for name, values := range x.resp.Trailer() {
			h[stdhttp.TrailerPrefix+name] = values
		}
		// What Finish queues, the last of the body and the trailers, is
		// bounded by the stall timeout too; the deadline goes with the
		// response.
		_ = x.c.SetWriteDeadline(time.Now().Add(x.stall))
		_ = x.c.Finish()
		x.release()
	}
}

// hold holds the upstream body until the client has room for more, for at
// most the stall timeout. x.mu is held.
func (x *exchange) hold() {
	x.resp.HoldBody(true)
	if err := x.c.SetWriteDeadline(time.Now().Add(x.stall)); err != nil && !errors.Is(err, stdhttp.ErrNotSupported) {
		x.giveUp()
		return
	}
	if !x.c.OnWritable(x.onWritable) {
		// Room already, or nothing more will be written.
		x.resume()
	}
}

// onWritable resumes the upstream body once the client has room, or gives
// the response up when the client went first or the stall timeout passed.
// It runs on a goroutine of fib's.
func (x *exchange) onWritable(err error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.done.Load() {
		return
	}
	if err != nil {
		x.giveUp()
		return
	}
	x.resume()
}

// resume lifts the deadline hold set and lets the upstream body go on. x.mu
// is held.
func (x *exchange) resume() {
	if err := x.c.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, stdhttp.ErrNotSupported) {
		x.giveUp()
		return
	}
	x.resp.HoldBody(false)
}

// giveUp ends an exchange whose client is gone, or stopped reading for the
// stall timeout: the upstream response is closed, which stops its
// callbacks. x.mu is held.
func (x *exchange) giveUp() {
	x.resp.Close()
	x.release()
}

// addCookie adds the sticky cookie, if the response sets one, to h, after
// the upstream's own.
func (x *exchange) addCookie(h stdhttp.Header) {
	if x.setCookie != "" {
		own := h["Set-Cookie"]
		h["Set-Cookie"] = append(own[:len(own):len(own)], x.setCookie)
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
	x.addCookie(h)
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
	case errors.Is(cause, errDrained):
		return stdhttp.StatusServiceUnavailable
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
	// Cut off by a drain, the backend did nothing wrong.
	x.failed = !errors.Is(context.Cause(x.ctx), errDrained)
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

// readBody reads a response body that arrived whole: into a buffer of its
// length when it has one, as it does unless it came chunked, so that it is
// read in one allocation. fib holds the body already; reading it without a
// copy is A11 in fib-api-requests.
func readBody(resp *fibhttp.ClientResponse) ([]byte, error) {
	if resp.Body == nil || resp.Body == stdhttp.NoBody {
		return nil, nil
	}
	if n := resp.ContentLength; n >= 0 && n <= 1<<20 {
		buf := make([]byte, n)
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
	return io.ReadAll(resp.Body)
}
