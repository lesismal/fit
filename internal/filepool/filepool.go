// Package filepool runs file operations on goroutines of their own, so that
// a slow disk holds up those and never a fib worker: the static handler's
// stat, open, read and list, and the cache's disk tier.
package filepool

import (
	goruntime "runtime"
	"sync/atomic"
	"time"
)

// Pool runs jobs on a set of goroutines. It is bounded twice: by its
// workers, which are all the disk is asked to do at once, and by its queue,
// past which a job is refused rather than kept.
//
// It keeps as many workers as the process has CPUs (GOMAXPROCS), up to its
// bound, and adds one, up to the bound, for each worker held in a job longer
// than BlockAfter, waiting on a slow disk; one it adds leaves once it finds
// no job waiting. More workers than CPUs, all with work, as on a file system
// in memory where nothing waits, would keep every CPU busy with them: then
// Go's runtime looks at its network poller only every 10ms, and fib's event
// loops, parked there, wait that long to be woken, which is what a busy
// server's slowest requests then take.
type Pool struct {
	jobs    chan job
	timeout time.Duration
	// Hook, when set, runs before each job, as a slow disk would; for tests.
	Hook func()

	core int
	// started holds, for each worker's slot, when the job it runs began, in
	// nanoseconds since epoch, or 0 while it waits for one; the first core
	// slots are the workers the pool keeps, the rest those it adds.
	started []atomic.Int64
	// taken says a slot has a worker; live counts them.
	taken []atomic.Bool
	live  atomic.Int32
	epoch time.Time
	// checking says a look again in BlockAfter is set, for jobs waiting
	// while no Submit comes to add a worker for them.
	checking atomic.Bool
}

// BlockAfter is how long a worker runs one job before the pool takes it to
// be waiting on the disk, and adds a worker: far longer than a file
// operation that reads from memory takes, and shorter than one that waits
// for a disk.
const BlockAfter = time.Millisecond

// job is a piece of file work. run does it; expire is called instead for a
// job that waited past the pool's timeout to be taken up.
type job struct {
	run    func()
	expire func()
	queued time.Time
}

// New starts a pool of at most workers goroutines, which take jobs from a
// queue of queue, each within timeout of being queued. The workers it keeps,
// as many as GOMAXPROCS and no more than workers, live as long as the
// process. A pool of no workers and no queue takes nothing.
func New(workers, queue int, timeout time.Duration) *Pool {
	workers = max(workers, 0)
	p := &Pool{
		jobs:    make(chan job, queue),
		timeout: timeout,
		core:    min(workers, goruntime.GOMAXPROCS(0)),
		started: make([]atomic.Int64, workers),
		taken:   make([]atomic.Bool, workers),
		epoch:   time.Now(),
	}
	for i := range p.core {
		p.taken[i].Store(true)
		p.live.Add(1)
		go p.work(i, true)
	}
	return p
}

// Timeout is how long a job may wait to be taken up.
func (p *Pool) Timeout() time.Duration { return p.timeout }

// Workers is how many workers the pool runs now.
func (p *Pool) Workers() int { return int(p.live.Load()) }

// now is the time since epoch in nanoseconds, never 0, which marks a free
// worker.
func (p *Pool) now() int64 { return int64(time.Since(p.epoch)) + 1 }

// work runs jobs in slot: for good when kept, and while jobs wait when
// added.
func (p *Pool) work(slot int, kept bool) {
	for {
		var j job
		if kept {
			j = <-p.jobs
		} else {
			select {
			case j = <-p.jobs:
			default:
				p.live.Add(-1)
				p.taken[slot].Store(false)
				if len(p.jobs) > 0 {
					// Queued as it left.
					p.grow()
				}
				return
			}
		}
		if time.Since(j.queued) > p.timeout {
			if j.expire != nil {
				j.expire()
			}
			continue
		}
		p.started[slot].Store(p.now())
		if p.Hook != nil {
			p.Hook()
		}
		j.run()
		p.started[slot].Store(0)
	}
}

// Submit queues run, and reports false, queuing nothing, when the queue is
// full. It never waits. expire, if not nil, runs in place of run for a job
// that waited past the pool's timeout. With jobs waiting, it adds a worker
// for a worker held in a job past BlockAfter.
func (p *Pool) Submit(run, expire func()) bool {
	select {
	case p.jobs <- job{run: run, expire: expire, queued: time.Now()}:
	default:
		return false
	}
	if len(p.jobs) > 0 && int(p.live.Load()) < len(p.started) {
		p.grow()
	}
	return true
}

// grow adds workers while fewer than core of the live ones are free to run
// jobs, the others held in a job past BlockAfter. With jobs still waiting
// and room for more workers, it looks again in BlockAfter, for a worker
// that blocks while no Submit comes.
func (p *Pool) grow() {
	blocked, cutoff := 0, p.now()-int64(BlockAfter)
	for i := range p.started {
		if t := p.started[i].Load(); t != 0 && t < cutoff {
			blocked++
		}
	}
	for i := p.core; i < len(p.taken) && int(p.live.Load())-blocked < p.core; i++ {
		if p.taken[i].CompareAndSwap(false, true) {
			p.live.Add(1)
			go p.work(i, false)
		}
	}
	if len(p.jobs) > 0 && int(p.live.Load()) < len(p.started) && p.checking.CompareAndSwap(false, true) {
		time.AfterFunc(BlockAfter, func() {
			p.checking.Store(false)
			if len(p.jobs) > 0 && int(p.live.Load()) < len(p.started) {
				p.grow()
			}
		})
	}
}
