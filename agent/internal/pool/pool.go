// Package pool runs the blocking work of the agent's roles on bounded pools
// of their own, never on a fib worker (docs/rules.md, rule 4): commands a
// node runs, its hardware and GPU queries, and the methods the pages call,
// which may wait on a peer or on a config change.
package pool

import (
	"context"
	"errors"
	"sync"
)

// ErrBusy is what a task gets when the pool's queue is full.
var ErrBusy = errors.New("busy: too much work is waiting; try again")

// ErrStopped is what a task gets once the pool has stopped.
var ErrStopped = errors.New("stopped")

// Pool runs tasks on a fixed number of goroutines, with a bounded queue.
type Pool struct {
	tasks   chan func()
	stop    chan struct{}
	once    sync.Once
	workers sync.WaitGroup
}

// New starts a pool of workers goroutines, with room for queue tasks to
// wait.
func New(workers, queue int) *Pool {
	p := &Pool{tasks: make(chan func(), queue), stop: make(chan struct{})}
	for range max(workers, 1) {
		p.workers.Go(p.work)
	}
	return p
}

func (p *Pool) work() {
	for {
		select {
		case <-p.stop:
			// What waits still runs, so that a task holding a request
			// answers it; tasks see their context ended by then.
			for {
				select {
				case t := <-p.tasks:
					t()
				default:
					return
				}
			}
		case t := <-p.tasks:
			t()
		}
	}
}

// Go queues fn, or reports ErrBusy or ErrStopped at once. It never waits.
func (p *Pool) Go(fn func()) error {
	select {
	case <-p.stop:
		return ErrStopped
	default:
	}
	select {
	case p.tasks <- fn:
		return nil
	default:
		return ErrBusy
	}
}

// Do runs fn on the pool and waits for it, or for ctx to end, when it gives
// ctx's error and fn, which is handed ctx, is left to finish on its own.
// Call it only off fib's workers.
func Do[T any](ctx context.Context, p *Pool, fn func(ctx context.Context) (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	var zero T
	if err := p.Go(func() {
		if ctx.Err() != nil {
			ch <- result{err: ctx.Err()}
			return
		}
		v, err := fn(ctx)
		ch <- result{v, err}
	}); err != nil {
		return zero, err
	}
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-p.stop:
		return zero, ErrStopped
	}
}

// Stop stops the pool: it takes no more tasks, runs those queued, and waits
// for them until ctx ends.
func (p *Pool) Stop(ctx context.Context) {
	p.once.Do(func() { close(p.stop) })
	done := make(chan struct{})
	go func() {
		p.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
