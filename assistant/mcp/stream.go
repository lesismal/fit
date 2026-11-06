package mcp

import (
	"sync"
	"time"

	"github.com/lesismal/fit/app/console"
)

// stream is the SSE answer of a request: comments while it waits, then the
// response as its one event. It writes without waiting: a client that does
// not take what it is sent has stallTimeout to, or the stream is cut, and
// broken is told.
type stream struct {
	r       *console.Request
	broken  func()
	mu      sync.Mutex
	ended   bool
	waiting bool
	ping    *time.Timer
}

// streamOpened, when not nil, is told of every stream as it opens: the
// tests of slow clients shrink the connection's socket buffer with it, so
// that what a stream queues is fib's to bound, not the kernel's, on every
// OS. nil outside tests.
var streamOpened func(r *console.Request)

// openStream starts the SSE answer of r, which the caller has retained.
func openStream(r *console.Request, broken func()) *stream {
	if streamOpened != nil {
		streamOpened(r)
	}
	st := &stream{r: r, broken: broken}
	ctx := r.Context
	_ = ctx.SetWriteDeadline(time.Time{})
	h := ctx.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	ctx.WriteHeader(200)
	st.mu.Lock()
	st.writeLocked([]byte(": waiting for the tool\n\n"))
	ctx.Flush()
	st.ping = time.AfterFunc(pingInterval, st.pingNow)
	st.mu.Unlock()
	return st
}

func (st *stream) pingNow() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ended {
		return
	}
	st.writeLocked([]byte(": ping\n\n"))
	st.r.Context.Flush()
	st.ping.Reset(pingInterval)
}

// writeLocked writes b without waiting; st.mu is held.
func (st *stream) writeLocked(b []byte) {
	ctx := st.r.Context
	above, err := ctx.WriteNoWait(b)
	if err != nil {
		go st.broken()
		return
	}
	if above && !st.waiting {
		st.waiting = true
		_ = ctx.SetWriteDeadline(time.Now().Add(stallTimeout))
		if !ctx.OnWritable(st.writable) {
			st.waiting = false
			_ = ctx.SetWriteDeadline(time.Time{})
		}
	}
}

func (st *stream) writable(err error) {
	if err != nil {
		go st.broken()
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.waiting = false
	if !st.ended {
		_ = st.r.Context.SetWriteDeadline(time.Time{})
	}
}

// finish writes data, a JSON-RPC response, as the stream's last event, or
// nothing when it is nil; the caller then releases the request, which ends
// the stream.
func (st *stream) finish(data []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ended {
		return
	}
	st.ended = true
	if st.ping != nil {
		st.ping.Stop()
	}
	if data != nil {
		st.writeLocked(append(append([]byte("event: message\ndata: "), data...), '\n', '\n'))
		st.r.Context.Flush()
	}
}
