package wasm

import (
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
)

// respModule is proxyModule with the response's callbacks, which go on, with
// bodies for those bodies names instead, and without those whose body is
// nil.
func respModule(bodies map[string][]byte) []byte {
	m := wasmModule{pages: 1}
	for _, f := range []wasmFunc{
		{export: "proxy_abi_version_0_2_1"},
		{export: pwOnContextCreate, params: 2},
		{export: pwOnRequestHeaders, params: 3, results: 1, body: i32(0)},
		{export: pwOnResponseHeaders, params: 3, results: 1, body: i32(0)},
		{export: pwOnResponseBody, params: 3, results: 1, body: i32(0)},
		{export: pwOnResponseTrailers, params: 2, results: 1, body: i32(0)},
		{export: pwOnDone, params: 1, results: 1, body: i32(1)},
		{export: pwOnLog, params: 1},
		{export: pwOnDelete, params: 1},
		{export: pwAllocate, params: 1, results: 1, body: i32(envAlloc)},
	} {
		if body, ok := bodies[f.export]; ok {
			if body == nil {
				continue
			}
			f.body = body
		}
		m.funcs = append(m.funcs, f)
	}
	return m.encode()
}

// respWorker makes a worker with its instance for respModule(bodies), whose
// calls on_error says what to do with.
func respWorker(t *testing.T, bodies map[string][]byte, onError string) *worker {
	t.Helper()
	w := newWorker(t, respModule(bodies))
	m := w.p.m
	m.OnError, m.MaxBodyBytes, m.MaxStreams = onError, 1<<20, 4
	w.jobs = make(chan func(), 4)
	if err := w.instantiate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.close)
	return w
}

// opened is a followed call whose context w keeps open.
func opened(w *worker) *call {
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	cl := &call{m: w.p.m, c: fibhttp.NewStreamContext(nil, r, &fibtest.Recorder{}), w: w, id: 2, gen: w.gen, followed: true, filtered: true}
	w.open++
	return cl
}

// bodyResult is what responseBody handed on.
type bodyResult struct {
	called bool
	out    string
	err    error
}

func (r *bodyResult) done(out []byte, err error) { *r = bodyResult{true, string(out), err} }

// TestResponseNotLive checks the response's callbacks for a context already
// closed, or one on_error left the plugin out of: the plugin is not called,
// and the response fails or goes on as on_error says.
func TestResponseNotLive(t *testing.T) {
	for _, onError := range []string{"fail", "skip"} {
		w := respWorker(t, nil, onError)

		cl := opened(w)
		cl.closed.Store(true)
		var resumed error = errors.New("not resumed")
		w.responseHeaders(cl, 200, stdhttp.Header{}, func(err error) { resumed = err })
		if onError == "fail" && !errors.Is(resumed, errResponseCall) || onError == "skip" && resumed != nil {
			t.Errorf("%s: the header resumed with %v", onError, resumed)
		}

		cl = opened(w)
		cl.closed.Store(true)
		var r bodyResult
		w.responseBody(cl, []byte("ab"), false, nil, r.done)
		if onError == "fail" && !errors.Is(r.err, errResponseCall) || onError == "skip" && (r.err != nil || r.out != "ab") {
			t.Errorf("%s: a closed context's body: %+v", onError, r)
		}

		// One skipped goes on, whatever on_error says now.
		cl = opened(w)
		cl.skip.Store(true)
		cl.buf = []byte("held ")
		w.responseBody(cl, []byte("more"), false, nil, r.done)
		if r.err != nil || r.out != "held more" || !cl.closed.Load() {
			t.Errorf("%s: a skipped context's body: %+v %v", onError, r, cl.closed.Load())
		}
	}
}

// TestResponseCallbacksFail checks the body's and the trailer's callbacks
// that trap, or answer once the response has begun: the response fails, or
// goes on without the plugin, as on_error says.
func TestResponseCallbacksFail(t *testing.T) {
	trailer := stdhttp.Header{"X-T": {"t"}}
	for _, tt := range []struct {
		name   string
		bodies map[string][]byte
		answer bool
		end    bool
	}{
		{"the body's callback traps", map[string][]byte{pwOnResponseBody: trap}, false, false},
		{"the body's callback answers", nil, true, false},
		{"the trailer's callback traps", map[string][]byte{pwOnResponseTrailers: trap}, false, true},
		{"the trailer's callback answers", map[string][]byte{pwOnResponseBody: nil}, true, true},
	} {
		for _, onError := range []string{"fail", "skip"} {
			w := respWorker(t, tt.bodies, onError)
			cl := opened(w)
			if tt.answer {
				cl.status = 418
			}
			var r bodyResult
			w.responseBody(cl, []byte("ab"), tt.end, trailer, r.done)
			switch {
			case onError == "fail" && r.err == nil:
				t.Errorf("%s, fail: %+v", tt.name, r)
			case onError == "skip" && (r.err != nil || r.out != "ab"):
				t.Errorf("%s, skip: %+v", tt.name, r)
			case !cl.closed.Load() && (onError == "fail" || tt.end):
				t.Errorf("%s, %s: the context is still open", tt.name, onError)
			}
		}
	}
}

// TestResponseBodyPaused checks a plugin that pauses the body holds it, and
// sees it with more the next time.
func TestResponseBodyPaused(t *testing.T) {
	w := respWorker(t, map[string][]byte{pwOnResponseBody: i32(pwActionPause)}, "fail")
	cl := opened(w)
	var r bodyResult
	w.responseBody(cl, []byte("ab"), false, nil, r.done)
	if !r.called || r.err != nil || r.out != "" || string(cl.buf) != "ab" || cl.inBody {
		t.Fatalf("paused: %+v %q", r, cl.buf)
	}
	// At the end a pause goes on: the plugin has all of it.
	w.responseBody(cl, []byte("cd"), true, nil, r.done)
	if r.err != nil || r.out != "abcd" || !cl.closed.Load() {
		t.Errorf("at the end: %+v", r)
	}
}

// TestResponseBigBuffer checks a buffer grown past maxKeptBody is not kept
// for the next part.
func TestResponseBigBuffer(t *testing.T) {
	w := respWorker(t, nil, "fail")
	cl := opened(w)
	var r bodyResult
	w.responseBody(cl, make([]byte, maxKeptBody+1), false, nil, r.done)
	if r.err != nil || len(r.out) != maxKeptBody+1 || cap(cl.buf) != 0 {
		t.Errorf("%v %d, kept %d", r.err, len(r.out), cap(cl.buf))
	}
}

// TestCloseContextTraps checks a context whose proxy_on_done traps is not
// logged or deleted, and still closes.
func TestCloseContextTraps(t *testing.T) {
	w := respWorker(t, map[string][]byte{pwOnDone: trap, pwOnLog: trap}, "fail")
	cl := opened(w)
	w.closeContext(cl)
	if !cl.closed.Load() || w.open != 0 {
		t.Errorf("closed %v, open %d", cl.closed.Load(), w.open)
	}
}

// TestResponseQueueGone checks the response's callbacks that cannot be
// queued, the pool closed or the queue full: the response fails, or goes on
// without the plugin, as on_error says, and the config is let go.
func TestResponseQueueGone(t *testing.T) {
	// A failed response is never written: no need to wait long for it.
	defer func(d time.Duration) { fibtest.Timeout = d }(fibtest.Timeout)
	fibtest.Timeout = time.Second
	for _, onError := range []string{"fail", "skip"} {
		w := respWorker(t, nil, onError)
		w.jobs = make(chan func()) // full: nobody takes from it
		if w.later(func() {}) {
			t.Fatal("queued on a full queue")
		}
		w.dead = true

		cl := opened(w)
		cl.buf = []byte("held ")
		var r bodyResult
		(*bodyFilter)(cl).FilterAsync([]byte("more"), false, nil, r.done)
		if onError == "fail" && !errors.Is(r.err, errResponseCall) || onError == "skip" && (r.err != nil || r.out != "held more") || !cl.closed.Load() {
			t.Errorf("%s: the body: %+v", onError, r)
		}

		cl = opened(w)
		(*bodyFilter)(cl).Abort(errors.New("gone"))
		if !cl.closed.Load() {
			t.Errorf("%s: abort left the context open", onError)
		}

		// The header's callback goes on, or fails, at once.
		cl = opened(w)
		rec := fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
			cl.c = c
			w.follow(cl)
			_ = c.Respond(200, "text/plain", []byte("ok"))
		}), httptest.NewRequest("GET", "http://example.com/", nil))
		if onError == "skip" && (!rec.Written || string(rec.Response.Body) != "ok") || onError == "fail" && rec.Written && rec.Response.StatusCode == 200 && string(rec.Response.Body) == "ok" {
			t.Errorf("%s: the header: %v %d %q", onError, rec.Written, rec.Response.StatusCode, rec.Response.Body)
		}
		if !cl.closed.Load() {
			t.Errorf("%s: the header left the context open", onError)
		}
	}
}

// TestResponseCancelQueueGone checks a request whose client leaves once the
// pool has closed: its context is let go at once.
func TestResponseCancelQueueGone(t *testing.T) {
	w := respWorker(t, nil, "fail")
	w.dead = true
	cl := opened(w)
	sr := &fibhttp.StreamRequest{Request: *httptest.NewRequest("GET", "http://example.com/", nil)}
	c := sr.Context(nil, &fibtest.Recorder{}, nil)
	fibhttp.Serve(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		c.Retain()
		cl.c = c
		w.follow(cl)
	}), c)
	sr.Cancel(errors.New("gone"))
	deadline := time.Now().Add(10 * time.Second)
	for !cl.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the context was not let go")
		}
		time.Sleep(time.Millisecond)
	}
	c.Release()
}

// TestCloseRunsQueued checks that the pool, closing, runs the response's
// calls still queued, which find their instance gone.
func TestCloseRunsQueued(t *testing.T) {
	ran := false
	w := &worker{jobs: make(chan func(), 1)}
	w.jobs <- func() { ran = true }
	p := &pool{m: &WASM{}, stop: make(chan struct{}), workers: []*worker{w}}
	w.p = p
	p.close()
	if !ran || !w.dead {
		t.Errorf("ran %v, dead %v", ran, w.dead)
	}
	if w.later(func() {}) {
		t.Error("queued once the pool closed")
	}
}

// TestResponseAbort checks a response that ended before its body did has
// its context closed on its instance's goroutine.
func TestResponseAbort(t *testing.T) {
	w := respWorker(t, nil, "fail")
	cl := opened(w)
	(*bodyFilter)(cl).Abort(errors.New("cut"))
	if cl.closed.Load() {
		t.Fatal("closed away from the instance's goroutine")
	}
	(<-w.jobs)()
	if !cl.closed.Load() || w.open != 0 {
		t.Errorf("closed %v, open %d", cl.closed.Load(), w.open)
	}
}
