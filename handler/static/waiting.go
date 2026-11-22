package static

import (
	stdhttp "net/http"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

// waiting holds the requests queued for the file pool that its work has not
// claimed yet, for the pool's timeout to answer the ones that wait too long:
// to be taken up, or for a disk that hangs before the work claims them. A
// sweep every eighth of the timeout, scheduled only while requests wait,
// answers those past it with 503, within an eighth of the timeout of it.
// It stands for a timer a request: a time.AfterFunc made and stopped for
// each was 2% of the CPU of a server answering 300,000 requests a second
// from the pool, where joining and leaving a list under a lock shared by an
// eighth of them is a few nanoseconds each way.
type waiting struct {
	// timeout is the pool's, as the last request to join found it.
	timeout atomic.Int64
	epoch   time.Time
	// count is how many requests the shards hold; sweeping says a sweep is
	// scheduled.
	count    atomic.Int64
	sweeping atomic.Bool
	next     atomic.Uint32
	shards   [waitShards]waitShard
}

const waitShards = 8

// waitShard is a list of waiting requests, in the order they joined, which
// is the order of their deadlines, under its own lock.
type waitShard struct {
	mu sync.Mutex
	// head is the list's sentinel: head.next the first, head.prev the last.
	head queued
	// The padding keeps a shard's lock off the cache line of the next
	// shard's.
	_ [64]byte
}

func newWaiting() *waiting {
	w := &waiting{epoch: time.Now()}
	for i := range w.shards {
		h := &w.shards[i].head
		h.next, h.prev = h, h
	}
	return w
}

// now is the time since epoch, monotonic.
func (w *waiting) now() int64 { return int64(time.Since(w.epoch)) }

// interval is how often a sweep runs while requests wait.
func (w *waiting) interval() time.Duration {
	return max(time.Duration(w.timeout.Load())/8, time.Millisecond)
}

// join adds q, a request for c just queued, to wait at most timeout.
func (w *waiting) join(q *queued, c *fibhttp.Context, timeout time.Duration) {
	if time.Duration(w.timeout.Load()) != timeout {
		w.timeout.Store(int64(timeout))
	}
	q.c, q.list, q.deadline = c, w, w.now()+int64(timeout)
	sh := &w.shards[w.next.Add(1)%waitShards]
	q.shard = sh
	sh.mu.Lock()
	last := sh.head.prev
	q.prev, q.next = last, &sh.head
	last.next = q
	sh.head.prev = q
	sh.mu.Unlock()
	w.count.Add(1)
	if w.sweeping.CompareAndSwap(false, true) {
		time.AfterFunc(w.interval(), w.sweep)
	}
}

// leave takes q off its list, when it is on one.
func (q *queued) leave() {
	sh := q.shard
	if sh == nil {
		return
	}
	sh.mu.Lock()
	on := q.next != nil
	if on {
		q.prev.next, q.next.prev = q.next, q.prev
		q.prev, q.next = nil, nil
	}
	sh.mu.Unlock()
	if on {
		q.list.count.Add(-1)
	}
}

// sweep answers the requests past their deadline, and runs again in an
// interval while any wait.
func (w *waiting) sweep() {
	now := w.now()
	var due []*queued
	for i := range w.shards {
		sh := &w.shards[i]
		sh.mu.Lock()
		for q := sh.head.next; q != &sh.head && q.deadline <= now; q = sh.head.next {
			sh.head.next, q.next.prev = q.next, &sh.head
			q.prev, q.next = nil, nil
			due = append(due, q)
		}
		sh.mu.Unlock()
	}
	w.count.Add(-int64(len(due)))
	for _, q := range due {
		q.expire()
	}
	w.sweeping.Store(false)
	// A request that joined while the flag was still up scheduled nothing.
	if w.count.Load() > 0 && w.sweeping.CompareAndSwap(false, true) {
		time.AfterFunc(w.interval(), w.sweep)
	}
}

// expire answers q 503, unless the pool's work claimed it, or it was
// answered so already.
func (q *queued) expire() {
	if q.state.CompareAndSwap(queuedWaiting, queuedDone) {
		respondError(q.c, stdhttp.StatusServiceUnavailable)
		q.c.Release()
	}
}
