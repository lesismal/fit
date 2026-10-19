package static

import (
	goruntime "runtime"
	"sync"
	"time"
)

// The file pool's defaults.
const (
	// DefaultFileTimeout bounds how long a request waits for the file pool:
	// to be taken up, and then for its file to be found and opened.
	DefaultFileTimeout = 10 * time.Second
	// fileQueue bounds the requests waiting for a worker of the pool.
	fileQueue = 4096
)

// filePool runs the static handlers' file operations, stat, open, read and
// list, on goroutines of its own, so that a slow disk holds up those and
// never a fib worker. It is bounded twice: by its workers, which are all
// the disk is asked to do at once, and by its queue, past which a request is
// refused rather than kept.
type filePool struct {
	jobs    chan fileJob
	timeout time.Duration
	// hook, when set, runs before each job, as a slow disk would; for tests.
	hook func()
}

// fileJob is a request's file work. run does it; expire answers a request
// that waited past the pool's timeout to be taken up.
type fileJob struct {
	run    func()
	expire func()
	queued time.Time
}

// newFilePool starts a pool of workers goroutines, which take jobs from a
// queue of queue, each within timeout of being queued.
func newFilePool(workers, queue int, timeout time.Duration) *filePool {
	p := &filePool{jobs: make(chan fileJob, queue), timeout: timeout}
	for range workers {
		go p.work()
	}
	return p
}

var (
	sharedPoolOnce sync.Once
	sharedPool     *filePool
)

// defaultPool is the pool every static handler shares: 4 workers a CPU, and
// at least 16, which is about what nginx's thread pools and Go's own
// os package make of a disk.
func defaultPool() *filePool {
	sharedPoolOnce.Do(func() {
		sharedPool = newFilePool(max(16, 4*goruntime.GOMAXPROCS(0)), fileQueue, DefaultFileTimeout)
	})
	return sharedPool
}

func (p *filePool) work() {
	for j := range p.jobs {
		if time.Since(j.queued) > p.timeout {
			j.expire()
			continue
		}
		if p.hook != nil {
			p.hook()
		}
		j.run()
	}
}

// submit queues run, and reports false, queuing nothing, when the queue is
// full. It never waits.
func (p *filePool) submit(run, expire func()) bool {
	select {
	case p.jobs <- fileJob{run: run, expire: expire, queued: time.Now()}:
		return true
	default:
		return false
	}
}
