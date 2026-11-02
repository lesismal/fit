package mediaauth

import (
	"runtime"
	"sync"
	"time"
)

// The pool's bounds: how many bcrypt checks may wait, and for how long. A
// check past either gets ErrBusy, so that a flood of wrong passwords costs
// at most the pool's share of the CPU, while tokens, plaintext passwords and
// passwords remembered are checked at once meanwhile.
const (
	queueSize = 256
	maxWait   = 5 * time.Second
)

// pool runs bcrypt checks: a quarter of the CPUs, at least one, so that
// fib's workers keep the rest. Its goroutines start with its first check.
type pool struct {
	once sync.Once
	jobs chan job
}

type job struct {
	queued time.Time
	run    func(late bool)
}

var checks = &pool{}

// submit runs fn on the pool, with late set when it waited past maxWait;
// a pool with no room runs it at once, late.
func (p *pool) submit(fn func(late bool)) {
	p.once.Do(func() {
		p.jobs = make(chan job, queueSize)
		for range max(1, runtime.GOMAXPROCS(0)/4) {
			go p.work()
		}
	})
	select {
	case p.jobs <- job{queued: time.Now(), run: fn}:
	default:
		fn(true)
	}
}

func (p *pool) work() {
	for j := range p.jobs {
		j.run(time.Since(j.queued) > maxWait)
	}
}
