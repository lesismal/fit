package cache

import (
	"context"
	stdhttp "net/http"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

// The bounds on what waits for a fetch.
const (
	// maxWaiters bounds the requests waiting for one fetch; past it a
	// request goes on to fetch for itself.
	maxWaiters = 4096
	// maxBackground bounds the fetches a zone makes that no request waits
	// for, to refresh a response answered stale; past it a stale response
	// is answered without being refreshed, until a later request finds
	// room.
	maxBackground = 256
)

// fetch is a fetch of a key's response under way, which the requests for the
// key that come meanwhile wait for, so that a miss fetches once (a cache
// lock). It is done once the response is kept, or is known not to be.
type fetch struct {
	key     string
	started time.Time

	mu      sync.Mutex
	done    bool
	result  fetchResult
	waiters []*waiter
}

// fetchResult is how a fetch ended: the status its response had, and failed
// when it got none, or one cut short.
type fetchResult struct {
	status int
	failed bool
}

// erred reports whether a fetch that ended so failed, as stale-if-error
// means it: no response, or a 500, 502, 503 or 504.
func (r fetchResult) erred() bool {
	switch r.status {
	case 500, 502, 503, 504:
		return true
	}
	return r.failed
}

// lock returns the fetch under way for key, or starts one, with first set,
// which the caller then makes. A fetch older than abandoned is taken for one
// that will never end, as a handler that never answers would leave, and is
// replaced.
func (s *store) lock(key string, now time.Time, abandoned time.Duration) (f *fetch, first bool) {
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if f := sh.fetches[key]; f != nil && now.Sub(f.started) < abandoned {
		return f, false
	}
	f = &fetch{key: key, started: now}
	sh.fetches[key] = f
	return f, true
}

// finish ends f with r: it is no longer what requests for its key wait for,
// and those that waited are woken, on this goroutine, each to look for its
// response again. Only the first call does anything.
func (s *store) finish(f *fetch, r fetchResult) {
	sh := s.shard(f.key)
	sh.mu.Lock()
	if sh.fetches[f.key] == f {
		delete(sh.fetches, f.key)
	}
	sh.mu.Unlock()
	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		return
	}
	f.done, f.result = true, r
	waiters := f.waiters
	f.waiters = nil
	f.mu.Unlock()
	for _, w := range waiters {
		w.wake(r)
	}
}

// waiter is a request waiting for another's fetch of its response: it is
// retained, with the config, until the fetch ends or lock_timeout passes,
// whichever claims it first.
type waiter struct {
	m     *Cache
	x     *miss
	next  module.Handler
	timer *time.Timer
	// claimed says whether the fetch's end (1) or the timeout (2) answers.
	claimed atomic.Int32
}

// wait has x's request wait for f, or go on if f has as many waiters as it
// may.
func (m *Cache) wait(f *fetch, x *miss, next module.Handler) {
	w := &waiter{m: m, x: x, next: next}
	f.mu.Lock()
	switch {
	case f.done:
		f.mu.Unlock()
		// Its end is known: look again now.
		x.c.Retain()
		m.graph.Retain()
		w.claimed.Store(1)
		w.answer(f.result)
		return
	case len(f.waiters) >= maxWaiters:
		f.mu.Unlock()
		x.forward(next)
		return
	}
	x.c.Retain()
	m.graph.Retain()
	f.waiters = append(f.waiters, w)
	w.timer = time.AfterFunc(m.LockTimeout.Std(), w.timeout)
	f.mu.Unlock()
}

// wake answers the request once the fetch it waited for has ended.
func (w *waiter) wake(r fetchResult) {
	if w.claimed.CompareAndSwap(0, 1) {
		w.timer.Stop()
		w.answer(r)
	}
}

// timeout sends the request on, the fetch it waited for not having ended
// within lock_timeout.
func (w *waiter) timeout() {
	if w.claimed.CompareAndSwap(0, 2) {
		w.x.forward(w.next)
		w.release()
	}
}

// answer answers the request from what the fetch kept, from the stale
// response it had if the fetch failed and stale-if-error allows, or else
// sends it on.
func (w *waiter) answer(r fetchResult) {
	defer w.release()
	x, now := w.x, clock()
	if x.c.Err() != nil {
		return
	}
	if e, _ := w.m.store.get(x.key, x.c.Request.Header); e != nil {
		if hit, _ := usable(e, x.req, now); hit {
			w.m.hit(x.c, e, e.age(now), "")
			return
		}
	}
	if s := x.stale; s != nil && r.erred() && !s.noStale && s.staleFor(now, s.sie) {
		w.m.hit(x.c, s, s.age(now), "stale-if-error")
		return
	}
	x.forward(w.next)
}

func (w *waiter) release() {
	w.x.c.Release()
	w.m.graph.Release()
}

// refetch fetches key's response again with a request of the cache's own,
// made from the client's request r, revalidating stale with its validators,
// and ends f with how it went. It runs next on this goroutine, which must
// not block, and returns once next has: the response comes later.
func (m *Cache) refetch(f *fetch, r *stdhttp.Request, stale *entry, next module.Handler, background bool) {
	req := r.Clone(context.Background())
	for _, field := range [...]string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "Cache-Control", "Pragma"} {
		delete(req.Header, field)
	}
	if stale.etag != "" {
		req.Header["If-None-Match"] = []string{stale.etag}
	} else if stale.lastModified != "" {
		req.Header["If-Modified-Since"] = []string{stale.lastModified}
	}
	// No connection carries it: as HTTP/2's, a response cut short ends
	// with a trailer rather than by closing one.
	req.Proto, req.ProtoMajor, req.ProtoMinor = "HTTP/2.0", 2, 0
	req.Method, req.Body, req.ContentLength = stdhttp.MethodGet, stdhttp.NoBody, 0
	s := &fetchStream{m: m, f: f, background: background}
	c := fibhttp.NewStreamContext(nil, req, s)
	_, authorized := req.Header["Authorization"]
	x := &miss{m: m, c: c, key: f.key, requestTime: clock(), status: fwdStale, stale: stale,
		storable: true, authorized: authorized, req: directives{maxAge: -1, sMaxAge: -1, maxStale: -1, minFresh: -1}}
	c.OnBodyFilter(x.filter)
	c.OnHeader(x.header)
	m.graph.Retain()
	fibhttp.Serve(next, c)
}

// fetchStream is where the response to a request of the cache's own goes:
// nowhere, once the hooks the cache put on its Context have kept it. It
// ends the fetch once the request has ended, the response kept by then.
type fetchStream struct {
	m          *Cache
	f          *fetch
	background bool
	status     atomic.Int64
	failed     atomic.Bool
}

func (s *fetchStream) WriteResponse(_ *stdhttp.Request, r fibhttp.Response) error {
	s.status.Store(int64(r.StatusCode))
	return nil
}

func (s *fetchStream) WriteInterim(int, stdhttp.Header) error { return nil }

func (s *fetchStream) Push(*stdhttp.Request, string, *stdhttp.PushOptions) error {
	return stdhttp.ErrNotSupported
}

func (s *fetchStream) BeginResponse(_ *stdhttp.Request, status int, _ stdhttp.Header) error {
	s.status.Store(int64(status))
	return nil
}

func (s *fetchStream) WriteBody([]byte, bool) error { return nil }

func (s *fetchStream) WriteBodyNoWait([]byte) (bool, error) { return false, nil }

func (s *fetchStream) NotifyWritable(func(error)) bool { return false }

func (s *fetchStream) EndResponse(_ stdhttp.Header, complete bool) error {
	if !complete {
		s.failed.Store(true)
	}
	return nil
}

func (s *fetchStream) Abandon() { s.failed.Store(true) }

// RequestEnded ends the fetch, once whatever the request's response was has
// been kept or given up.
func (s *fetchStream) RequestEnded() {
	status := int(s.status.Load())
	s.m.store.finish(s.f, fetchResult{status: status, failed: status == 0 || s.failed.Load()})
	if s.background {
		s.m.store.background.Add(-1)
	}
	s.m.graph.Release()
}

// revalidate refreshes e, key's stale response, which r's request was just
// answered with, by a fetch of the cache's own that nothing waits for,
// unless one is under way or the zone makes as many as it may.
func (m *Cache) revalidate(key string, e *entry, r *stdhttp.Request, next module.Handler) {
	f, first := m.store.lock(key, clock(), 2*m.LockTimeout.Std())
	if !first {
		return
	}
	if now, _ := m.store.get(key, r.Header); now != e {
		// A fetch that ended since e was looked up has refreshed it.
		m.store.finish(f, fetchResult{status: 200})
		return
	}
	if m.store.background.Add(1) > maxBackground {
		m.store.background.Add(-1)
		m.store.finish(f, fetchResult{failed: true})
		return
	}
	m.refetch(f, r, e, next, true)
}
