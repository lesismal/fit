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
	"unsafe"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/upstream"
)

// Why a proxied request was cut short, as the cause exchange.cancel records.
var (
	errClientGone   = errors.New("proxy: the client went away")
	errIdleTimeout  = errors.New("proxy: the upstream went silent in the middle of its response")
	errDrained      = errors.New("proxy: the config the request started on was swapped out the drain timeout ago")
	errRouteTimeout = errors.New("proxy: the request took longer than the route's timeout")
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
	if protocol, extended := upgradeProtocol(c.Request); protocol != "" {
		pool := p.pool
		if p.split != nil || p.failover != nil {
			pool = p.pickPool()
		}
		p.serveUpgrade(c, pool, protocol, extended)
		return
	}
	x := &exchange{c: c}
	if !p.outgoing(c, x) {
		_ = c.Respond(stdhttp.StatusBadRequest, "text/plain; charset=utf-8", badRequest)
		return
	}
	timeout, why := p.timeout, errRouteTimeout
	if isGRPC(c.Request) {
		x.grpc = true
		x.base.Header["Te"] = teTrailers
		timeout, why = x.callTimeout(p.timeout)
	}
	pool := p.pool
	if p.split != nil || p.failover != nil {
		pool = p.pickPool()
	}
	if p.mirror != nil {
		p.mirror.copyTo(c, &x.base)
	}
	backend := pool.Pick(c.Request)
	// The request goes without a context of its own: it is abandoned through
	// its Call, which costs nothing, rather than a context fib would watch.
	out := &x.base
	// The backend is held until the exchange is over, body and all, so that
	// least_conn counts a long download as busy for as long as it lasts;
	// and the config, so that a download that outlasts a reload stays on
	// the config it started on, until the drain timeout.
	p.graph.Retain()
	x.out, x.start, x.timeouts = out, time.Now(), pool.Timeouts()
	x.stall, x.pool, x.backend, x.setCookie, x.graph = p.stall, pool, backend, pool.SetCookie(c.Request, backend), p.graph
	x.open = &p.open[rand.Uint32()%openShards]
	x.open.add(x)
	if x.policy = p.retry; x.policy != nil {
		x.policy.budget.deposit()
	}
	if timeout > 0 {
		x.deadline = time.AfterFunc(timeout, func() { x.cancel(why) })
	}
	// The request, its body among it, stays the handler's until the
	// response is written.
	c.Retain()
	c.OnCancel(func(error) { x.cancel(errClientGone) })

	r := c.Request
	announced := trailerNames(r)
	switch {
	case c.RequestBody() == nil && len(announced) == 0:
		x.send(out)
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
		x.send(out)
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
	x.send(out)
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

var badRequest = []byte("400 Bad Request\n")

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
	// traceparent is the value of the field naming the request's span,
	// when it is traced.
	traceparent [1]string
	out         *stdhttp.Request
	start       time.Time
	timeouts    upstream.Timeouts
	// stall bounds each wait for the client to take what was written.
	stall   time.Duration
	pool    *upstream.Pool
	backend *upstream.Backend
	// setCookie is the sticky cookie the response sets, or "".
	setCookie string
	graph     module.Holder
	// policy is the proxy's retries, or nil, and attempt how many times the
	// request has been sent again.
	policy  *retryPolicy
	attempt int
	// deadline is the route's timeout's, or the call's grpc-timeout's, or
	// nil.
	deadline *time.Timer
	// grpc says the request is a gRPC call, which is answered as gRPC
	// answers when it fails (grpc.go).
	grpc bool
	// timer is the response idle timeout's.
	timer atomic.Pointer[time.Timer]
	// open is the proxy's shard of open exchanges this one is in, for a
	// drain to find it, and prev and next its neighbours there, under the
	// shard's lock.
	open       *openSet
	prev, next *exchange

	// call abandons the upstream request, once Send has returned it, and
	// cause is why it was abandoned, or nil; callMu orders the two.
	callMu sync.Mutex
	call   fibhttp.Call
	cause  atomic.Pointer[error]

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
	if x.settle() {
		x.c.Release()
		x.graph.Release()
	}
}

// settle lets go of the upstream's request and tells the pool how the
// backend did, once, and reports whether it did so; release, or fail
// before it answers, gives the client's request back after it.
func (x *exchange) settle() bool {
	if !x.done.CompareAndSwap(false, true) {
		return false
	}
	if t := x.timer.Load(); t != nil {
		t.Stop()
	}
	if x.deadline != nil {
		x.deadline.Stop()
	}
	x.open.remove(x)
	x.pool.Done(x.backend, x.failed)
	return true
}

// send sends the request upstream, keeping its Call for cancel, and
// abandons it at once if cancel came first.
func (x *exchange) send(out *stdhttp.Request) {
	x.callMu.Lock()
	x.call = x.pool.Send(x.backend, out, x.onResponse)
	x.callMu.Unlock()
	if cause := x.cause.Load(); cause != nil {
		x.call.Cancel(*cause)
	}
}

// cancel abandons the upstream request for cause, the first cause given,
// which statusOf and broken tell the outcome by.
func (x *exchange) cancel(cause error) {
	x.cause.CompareAndSwap(nil, &cause)
	x.callMu.Lock()
	call := x.call
	x.callMu.Unlock()
	call.Cancel(cause)
}

// timedOut reports whether the request was cut short by the route's timeout
// or the call's grpc-timeout, which are no failure of the backend's.
func (x *exchange) timedOut() bool {
	cause := x.causeErr()
	return errors.Is(cause, errRouteTimeout) || errors.Is(cause, errCallDeadline)
}

// causeErr is why the request was abandoned, or nil.
func (x *exchange) causeErr() error {
	if cause := x.cause.Load(); cause != nil {
		return *cause
	}
	return nil
}

func (x *exchange) onResponse(resp *fibhttp.ClientResponse, err error) {
	if t := x.timer.Load(); t != nil {
		t.Stop()
	}
	took := time.Since(x.start)
	x.pool.ObserveResponse(uintptr(unsafe.Pointer(x.c)), took)
	if e := observe.EntryOf(x.c); e != nil {
		e.Upstream, e.UpstreamDuration = x.backend.Address, took
	}
	if x.policy != nil && x.retry(resp, err) {
		return
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
	if errors.Is(x.causeErr(), errClientGone) {
		x.release()
		return
	}
	status := x.statusOf(err)
	// 400, 408 and 413 are the client's body failing, not the upstream, and
	// the route's timeout is the route's.
	x.failed = (status == stdhttp.StatusBadGateway || status == stdhttp.StatusGatewayTimeout) && !x.timedOut()
	// The pool hears of the failure before the client does, so that the
	// request the client sends next is not given a backend this one has
	// just taken out of rotation.
	settled := x.settle()
	if x.grpc {
		x.grpcFail(status)
	} else {
		_ = x.c.Respond(status, "text/plain; charset=utf-8", []byte(strconv.Itoa(status)+" "+stdhttp.StatusText(status)+"\n"))
	}
	if settled {
		x.c.Release()
		x.graph.Release()
	}
}

// statusOf is the status for a request that failed with err before its
// response began.
func (x *exchange) statusOf(err error) int {
	cause := x.causeErr()
	var upload *uploadError
	switch {
	case errors.Is(cause, errIdleTimeout), x.timedOut(), errors.Is(err, fibhttp.ErrResponseHeaderTimeout):
		return stdhttp.StatusGatewayTimeout
	case errors.Is(cause, errDrained), errors.Is(err, upstream.ErrBusy), errors.Is(err, upstream.ErrClosed):
		// Swapped out, max_pending requests wait for a connection already,
		// or the upstream's pool is closed.
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
	if errors.Is(x.causeErr(), errClientGone) {
		x.release()
		return
	}
	if !x.started {
		x.fail(err)
		return
	}
	// Cut off by a drain or the route's timeout, the backend did nothing
	// wrong.
	x.failed = !errors.Is(x.causeErr(), errDrained) && !x.timedOut()
	switch {
	case x.resp.ContentLength >= 0:
		// Short of its length: fib closes an HTTP/1 connection and resets
		// an HTTP/2 stream for it.
		_ = x.c.Finish()
	case x.grpc:
		x.grpcBroken()
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

// readBody returns a response body that arrived whole: the bytes fib's
// client holds, with no copy, or, for a body that is not fib's own buffer,
// what reading it gives.
func readBody(resp *fibhttp.ClientResponse) ([]byte, error) {
	if body, ok := resp.BodyBytes(); ok {
		return body, nil
	}
	if resp.Body == nil || resp.Body == stdhttp.NoBody {
		return nil, nil
	}
	return io.ReadAll(resp.Body)
}
