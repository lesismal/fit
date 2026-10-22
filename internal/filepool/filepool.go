// Package filepool runs file operations on goroutines of their own, so that
// a slow disk holds up those and never a fib worker: the static handler's
// stat, open, read and list, and the cache's disk tier.
package filepool

import "time"

// Pool runs jobs on a fixed set of goroutines. It is bounded twice: by its
// workers, which are all the disk is asked to do at once, and by its queue,
// past which a job is refused rather than kept.
type Pool struct {
	jobs    chan job
	timeout time.Duration
	// Hook, when set, runs before each job, as a slow disk would; for tests.
	Hook func()
}

// job is a piece of file work. run does it; expire is called instead for a
// job that waited past the pool's timeout to be taken up.
type job struct {
	run    func()
	expire func()
	queued time.Time
}

// New starts a pool of workers goroutines, which take jobs from a queue of
// queue, each within timeout of being queued. Its goroutines live as long
// as the process.
func New(workers, queue int, timeout time.Duration) *Pool {
	p := &Pool{jobs: make(chan job, queue), timeout: timeout}
	for range workers {
		go p.work()
	}
	return p
}

// Timeout is how long a job may wait to be taken up.
func (p *Pool) Timeout() time.Duration { return p.timeout }

func (p *Pool) work() {
	for j := range p.jobs {
		if time.Since(j.queued) > p.timeout {
			if j.expire != nil {
				j.expire()
			}
			continue
		}
		if p.Hook != nil {
			p.Hook()
		}
		j.run()
	}
}

// Submit queues run, and reports false, queuing nothing, when the queue is
// full. It never waits. expire, if not nil, runs in place of run for a job
// that waited past the pool's timeout.
func (p *Pool) Submit(run, expire func()) bool {
	select {
	case p.jobs <- job{run: run, expire: expire, queued: time.Now()}:
		return true
	default:
		return false
	}
}
