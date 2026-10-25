package wasm

import (
	"errors"
	stdhttp "net/http"
	"slices"
	"time"

	fibhttp "github.com/lesismal/fib/http"
	"github.com/tetratelabs/wazero/api"
)

// A Proxy-Wasm plugin's response callbacks (decision 023). A plugin that
// exports any of them follows the response of each request it lets go on:
// the request's context stays open on the instance that made it, and the
// response's callbacks run on that instance's goroutine, queued before new
// requests, never on the goroutine writing the response. The header is held
// while proxy_on_response_headers runs (fib's OnHeaderAsync), and the body
// goes through proxy_on_response_body as it is written (fib's
// OnBodyFilterAsync), what is written meanwhile held up to 64 KB, past which
// the writer is paced as by a slow client; the body's last call carries the
// trailer, which proxy_on_response_trailers is given when there is one. A
// plugin that pauses the body holds it, up to max_body_bytes, and is called
// again with all it holds as more comes; one that pauses a header or a
// trailer goes on. Once the body has ended, or the request has, the context
// is done, logged and deleted.

const (
	pwOnResponseHeaders  = "proxy_on_response_headers"
	pwOnResponseBody     = "proxy_on_response_body"
	pwOnResponseTrailers = "proxy_on_response_trailers"
)

// The map and buffer types of the response's callbacks.
const (
	pwResponseTrailers = 3
	pwResponseBody     = 1
)

// What a response whose callbacks cannot run fails with, when on_error says
// fail.
var (
	errResponseCall  = errors.New("wasm: the plugin's response callback failed")
	errLocalResponse = errors.New("wasm: the plugin answered once the response had begun")
	errBodyTooLong   = errors.New("wasm: the plugin held more of the body than max_body_bytes")
)

// follows reports whether the plugin follows cl's response: it exports a
// response callback, the config does not leave them out, and the response
// is not a tunnel's, which goes at once.
func (w *worker) follows(cl *call) bool {
	pw := w.pw
	if !pw.response || w.p.m.Response == "none" {
		return false
	}
	r := cl.c.Request
	return r.Method != stdhttp.MethodConnect && len(r.Header["Upgrade"]) == 0
}

// follow registers the hooks that run the response's callbacks of cl, whose
// context w keeps, and holds the config for them until the context closes.
func (w *worker) follow(cl *call) {
	m := w.p.m
	m.graph.Retain()
	c := cl.c
	pw := w.pw
	if m.Response != "headers" && (pw.responseBody != nil || pw.responseTrailers != nil) {
		cl.filtered = true
		// Registered before the header's hook, it runs after it, so that
		// the plugin sees the header as it came, Content-Length and all.
		c.OnBodyFilterAsync(func(int, stdhttp.Header) fibhttp.AsyncBodyFilter { return (*bodyFilter)(cl) })
	}
	c.OnHeaderAsync(func(status int, h stdhttp.Header, resume func(error)) {
		if !w.later(func() { w.responseHeaders(cl, status, h, resume) }) {
			cl.apply(h)
			resume(cl.lost())
			cl.unfollow()
		}
	})
	c.OnCancel(func(error) {
		if !w.later(func() { w.closeContext(cl) }) {
			cl.unfollow()
		}
	})
}

// lost is what goes for a response whose callbacks cannot run, its
// instance gone or its queue full: on_error skip leaves the plugin out of
// the rest of it, and fail fails it.
func (cl *call) lost() error {
	if cl.m.OnError == "skip" {
		cl.skip.Store(true)
		return nil
	}
	return errResponseCall
}

// has reports whether cl's context is open on w's instance, and live that
// the plugin is still to be called for it too.
func (w *worker) has(cl *call) bool {
	return !cl.closed.Load() && cl.gen == w.gen && w.mod != nil && !w.mod.IsClosed()
}

func (w *worker) live(cl *call) bool { return !cl.skip.Load() && w.has(cl) }

// invoke calls fn of the plugin for cl, as serve calls the request's, and
// makes the instance again when the call broke it.
func (w *worker) invoke(cl *call, fn api.Function, params ...uint64) (uint64, error) {
	pw := w.pw
	w.current = cl
	w.started.Store(time.Now().UnixNano())
	copy(pw.stack[:], params)
	err := fn.CallWithStack(w.ctx, pw.stack[:])
	w.started.Store(0)
	w.current = nil
	if err != nil && (w.ctx.Err() != nil || w.mod.IsClosed()) {
		w.close()
		w.mod = nil
		_ = w.instantiate()
	}
	return pw.stack[0], err
}

// responseHeaders runs proxy_on_response_headers for cl, with the response's
// status and its header h, which it may change, and lets the header go.
func (w *worker) responseHeaders(cl *call, status int, h stdhttp.Header, resume func(error)) {
	cl.apply(h)
	cl.ops = cl.ops[:0]
	cl.respStatus = status
	if !w.live(cl) {
		resume(cl.lost())
		w.closeContext(cl)
		return
	}
	end := !bodyAllowed(status) || cl.c.Request.Method == stdhttp.MethodHead
	if pw := w.pw; pw.responseHeaders != nil {
		cl.respHeader = h
		_, err := w.invoke(cl, pw.responseHeaders, uint64(cl.id), uint64(len(h)+1), boolParam(end))
		cl.respHeader = nil
		if err == nil && cl.status != 0 {
			// The response is the upstream's, begun: fib cannot replace it.
			err = errLocalResponse
		}
		if err != nil {
			resume(cl.failed(err))
			w.closeContext(cl)
			return
		}
	}
	if end || !cl.filtered {
		w.closeContext(cl)
	}
	resume(nil)
}

// failed is what follows a response callback that failed: on_error skip
// leaves the plugin out of the rest of the response, and fail fails it.
func (cl *call) failed(err error) error {
	if cl.m.OnError == "skip" {
		cl.skip.Store(true)
		return nil
	}
	return err
}

// bodyFilter is a followed request's call, as the filter its response's
// body goes through.
type bodyFilter call

// FilterAsync queues the plugin's call for the next part of the body.
func (f *bodyFilter) FilterAsync(p []byte, end bool, trailer stdhttp.Header, done func([]byte, error)) {
	cl := (*call)(f)
	if cl.skip.Load() {
		done(p, nil)
		return
	}
	if !cl.w.later(func() { cl.w.responseBody(cl, p, end, trailer, done) }) {
		if err := cl.lost(); err != nil {
			done(nil, err)
		} else {
			done(append(cl.buf, p...), nil)
		}
		cl.unfollow()
	}
}

// Abort closes the context of a response that ended before its body did.
func (f *bodyFilter) Abort(error) {
	cl := (*call)(f)
	if !cl.w.later(func() { cl.w.closeContext(cl) }) {
		cl.unfollow()
	}
}

// responseBody runs proxy_on_response_body for cl with the body it holds and
// p, and at the end proxy_on_response_trailers, and hands on what the plugin
// lets go.
func (w *worker) responseBody(cl *call, p []byte, end bool, trailer stdhttp.Header, done func([]byte, error)) {
	m, pw := w.p.m, w.pw
	if !w.live(cl) {
		var err error
		if !cl.skip.Load() {
			err = cl.lost()
		}
		w.closeContext(cl)
		if err != nil {
			done(nil, err)
			return
		}
		w.handOn(cl, append(cl.buf, p...), done)
		return
	}
	if int64(len(cl.buf)+len(p)) > m.MaxBodyBytes {
		w.closeContext(cl)
		if err := cl.failed(errBodyTooLong); err != nil {
			done(nil, err)
			return
		}
		w.handOn(cl, append(cl.buf, p...), done)
		return
	}
	cl.buf = append(cl.buf, p...)
	if pw.responseBody != nil {
		cl.inBody = true
		if end {
			cl.trailer = trailer
		}
		action, err := w.invoke(cl, pw.responseBody, uint64(cl.id), uint64(len(cl.buf)), boolParam(end && len(trailer) == 0))
		if err == nil && cl.status != 0 {
			err = errLocalResponse
		}
		if err != nil {
			cl.inBody, cl.trailer = false, nil
			w.closeContext(cl)
			if err = cl.failed(err); err != nil {
				done(nil, err)
				return
			}
			w.handOn(cl, cl.buf, done)
			return
		}
		if uint32(action) == pwActionPause && !end {
			// The plugin holds the body, and sees it with more next time.
			cl.inBody = false
			done(nil, nil)
			return
		}
	}
	if end && len(trailer) > 0 && pw.responseTrailers != nil {
		cl.trailer = trailer
		_, err := w.invoke(cl, pw.responseTrailers, uint64(cl.id), uint64(len(trailer)))
		if err == nil && cl.status != 0 {
			err = errLocalResponse
		}
		if err != nil {
			if err = cl.failed(err); err != nil {
				cl.inBody, cl.trailer = false, nil
				w.closeContext(cl)
				done(nil, err)
				return
			}
		}
	}
	cl.inBody, cl.trailer = false, nil
	if end {
		w.closeContext(cl)
	}
	w.handOn(cl, cl.buf, done)
}

// maxKeptBody is the most of a buffer a context keeps for the next part of
// its body; one that grew past it, held by the plugin, goes.
const maxKeptBody = 64 << 10

// handOn hands out on, which fib sends or copies before done returns, and
// keeps cl's buffer for the next part.
func (w *worker) handOn(cl *call, out []byte, done func([]byte, error)) {
	done(out, nil)
	if cap(out) > maxKeptBody {
		out = nil
	}
	cl.buf = out[:0]
}

// closeContext closes cl's context, once: done, logged and deleted, on the
// instance that has it, and lets go of the config.
func (w *worker) closeContext(cl *call) {
	has := w.has(cl)
	if !cl.closed.CompareAndSwap(false, true) {
		return
	}
	if has {
		w.open--
		for _, fn := range []api.Function{w.pw.done, w.pw.logFn, w.pw.deleteFn} {
			if _, err := w.invoke(cl, fn, uint64(cl.id)); err != nil {
				break
			}
		}
	}
	cl.m.graph.Release()
}

// unfollow lets go of the config for a context closed away from its
// instance's goroutine: one the pool, closed, can no longer reach.
func (cl *call) unfollow() {
	if cl.closed.CompareAndSwap(false, true) {
		cl.m.graph.Release()
	}
}

// bodyAllowed reports whether a response with status carries a body.
func bodyAllowed(status int) bool {
	return status >= 200 && status != stdhttp.StatusNoContent && status != stdhttp.StatusNotModified
}

func boolParam(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// setBody replaces size bytes of the body cl holds from start with data, as
// proxy_set_buffer_bytes does: start at the end appends, and size 0
// inserts.
func (cl *call) setBody(start, size uint32, data []byte) uint32 {
	n := uint32(len(cl.buf))
	start = min(start, n)
	end := start + min(size, n-start)
	if int64(n-(end-start))+int64(len(data)) > cl.m.MaxBodyBytes {
		return pwBadArgument
	}
	cl.buf = slices.Concat(cl.buf[:start], data, cl.buf[end:])
	return pwOK
}
