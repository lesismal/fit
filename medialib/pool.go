package medialib

import (
	"sync"
	"sync/atomic"
	"time"
)

// Pool runs a library's file work on goroutines of its own, so that a slow
// disk holds up those and never a fib worker: probing files, reading sample
// tables and subtitles, and reading the bytes of the files served. It is
// bounded twice: by its workers, which are all the disk is asked to do at
// once, and by its queue, past which a job is refused rather than kept.
//
// The reads go through the pool, behind the library's methods, so that a
// library kept elsewhere than on a local disk would change the pool's jobs
// and not their callers.
type Pool struct {
	mu      sync.RWMutex // read-held to queue, held to close
	closed  bool
	jobs    chan job
	timeout time.Duration
	workers int
	wg      sync.WaitGroup

	busy             atomic.Int64
	refused, expired atomic.Uint64

	hook atomic.Pointer[func()] // run before each job, as a slow disk would; for tests
}

// job is a piece of file work. run does it; expire is called instead for a
// job that waited past the pool's timeout to be taken up.
type job struct {
	run    func()
	expire func()
	queued time.Time
}

// NewPool starts a pool of workers goroutines, which take jobs from a queue
// of queue, each within timeout of being queued. Close ends them.
func NewPool(workers, queue int, timeout time.Duration) *Pool {
	p := &Pool{jobs: make(chan job, max(queue, 1)), timeout: timeout, workers: max(workers, 1)}
	p.wg.Add(p.workers)
	for range p.workers {
		go p.work()
	}
	return p
}

func (p *Pool) work() {
	defer p.wg.Done()
	for j := range p.jobs {
		if time.Since(j.queued) > p.timeout {
			p.expired.Add(1)
			if j.expire != nil {
				j.expire()
			}
			continue
		}
		p.busy.Add(1)
		if h := p.hook.Load(); h != nil {
			(*h)()
		}
		j.run()
		p.busy.Add(-1)
	}
}

// Submit queues run, and reports false, queuing nothing, when the queue is
// full or the pool is closed. It never waits. expire, if not nil, runs in
// place of run for a job that waited past the pool's timeout. A job queued
// runs, or expires, even when the pool is closed after it is queued.
func (p *Pool) Submit(run, expire func()) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		p.refused.Add(1)
		return false
	}
	select {
	case p.jobs <- job{run: run, expire: expire, queued: time.Now()}:
		return true
	default:
		p.refused.Add(1)
		return false
	}
}

// Close refuses jobs from now on; the goroutines end once those queued have
// run. It does not wait for them: Wait does.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.jobs)
	}
}

// Wait waits until a closed pool's goroutines have ended.
func (p *Pool) Wait() { p.wg.Wait() }

// PoolStats is what a pool is doing.
type PoolStats struct {
	// Workers and Queue are its bounds.
	Workers int `json:"workers"`
	Queue   int `json:"queue"`
	// Queued is the jobs waiting, and Busy those running.
	Queued int `json:"queued"`
	Busy   int `json:"busy"`
	// Refused counts the jobs refused, its queue full or the pool closed,
	// and Expired those that waited past its timeout.
	Refused uint64 `json:"refused"`
	Expired uint64 `json:"expired"`
}

// Stats returns what p is doing.
func (p *Pool) Stats() PoolStats {
	return PoolStats{Workers: p.workers, Queue: cap(p.jobs), Queued: len(p.jobs), Busy: int(p.busy.Load()),
		Refused: p.refused.Load(), Expired: p.expired.Load()}
}
