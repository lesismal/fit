package mediatokens

import (
	"os"
	"path/filepath"
	"sync"
)

// pool is the revoke pool: one worker and a queue of one, so that a revoke
// already waiting makes the next one 503 at once, and a secret file's write
// never blocks a fib worker. It is bounded twice, by the one write under way
// and by the one waiting; a submit past either is refused rather than kept.
// Its worker starts with the module and ends with Cleanup.
type pool struct {
	jobs chan func()
	// mu orders submits with close, and guards hook.
	mu     sync.Mutex
	closed bool
	// hook, when set by a test, runs before each job, as a slow disk would.
	hook func()
}

// newPool starts the pool's worker.
func newPool() *pool {
	p := &pool{jobs: make(chan func(), 1)}
	go p.work()
	return p
}

// work runs the pool's jobs until the pool is closed and its queue is empty.
func (p *pool) work() {
	for job := range p.jobs {
		p.mu.Lock()
		hook := p.hook
		p.mu.Unlock()
		if hook != nil {
			hook()
		}
		job()
	}
}

// submit queues job, and reports false, queuing nothing, when the queue is
// full or the pool has been closed. It never waits.
func (p *pool) submit(job func()) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	select {
	case p.jobs <- job:
		return true
	default:
		return false
	}
}

// close stops the pool: no more jobs are taken, and its worker ends once
// what it holds and what waits for it are done. A write under way finishes on
// its own; close does not wait for it.
func (p *pool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	close(p.jobs)
}

// setHook makes hook run before each job, for tests; it is called before the
// first request of a case, and may be set again between cases.
func (p *pool) setHook(hook func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hook = hook
}

// writeSecret writes secret to path atomically: a temporary file in the same
// directory, mode 0600, written, closed and renamed over path, so that a
// reader sees either the old secret or the new one, never a part of one. A
// process restart reads the new one, so a revocation outlives it.
func writeSecret(path, secret string) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteString(secret); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
