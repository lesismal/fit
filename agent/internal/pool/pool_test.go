package pool

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDo(t *testing.T) {
	p := New(0, 1) // workers below 1 still start one
	defer p.Stop(context.Background())
	v, err := Do(context.Background(), p, func(context.Context) (int, error) { return 7, nil })
	if v != 7 || err != nil {
		t.Fatalf("Do = %d, %v", v, err)
	}
	boom := errors.New("boom")
	if _, err := Do(context.Background(), p, func(context.Context) (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("Do error = %v", err)
	}
}

func TestBusy(t *testing.T) {
	p := New(1, 1)
	defer p.Stop(context.Background())
	release := make(chan struct{})
	started := make(chan struct{})
	if err := p.Go(func() { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := p.Go(func() {}); err != nil { // fills the queue
		t.Fatal(err)
	}
	if err := p.Go(func() {}); !errors.Is(err, ErrBusy) {
		t.Fatalf("Go on a full queue = %v", err)
	}
	if _, err := Do(context.Background(), p, func(context.Context) (int, error) { return 1, nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("Do on a full queue = %v", err)
	}
	close(release)
}

func TestDoContext(t *testing.T) {
	p := New(1, 4)
	defer p.Stop(context.Background())
	release := make(chan struct{})
	started := make(chan struct{})
	_ = p.Go(func() { close(started); <-release })
	<-started

	// ctx ends while the task waits in the queue: Do returns at once, and
	// the task, run later, sees its context ended and skips fn.
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	errc := make(chan error, 1)
	go func() {
		_, err := Do(ctx, p, func(context.Context) (int, error) { ran.Store(true); return 0, nil })
		errc <- err
	}()
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Do after cancel = %v", err)
	}
	close(release)
	// A task queued after it runs only once it was skipped.
	if _, err := Do(context.Background(), p, func(context.Context) (int, error) { return 0, nil }); err != nil {
		t.Fatal(err)
	}
	if ran.Load() {
		t.Fatal("fn ran with its context ended")
	}
}

func TestStop(t *testing.T) {
	p := New(1, 4)
	release := make(chan struct{})
	started := make(chan struct{})
	_ = p.Go(func() { close(started); <-release })
	<-started
	var queued atomic.Bool
	_ = p.Go(func() { queued.Store(true) })

	// A Do waiting when the pool stops gets ErrStopped.
	errc := make(chan error, 1)
	go func() {
		_, err := Do(context.Background(), p, func(context.Context) (int, error) { return 0, nil })
		errc <- err
	}()
	for len(p.tasks) < 2 {
		time.Sleep(time.Millisecond)
	}
	// Stop gives up waiting once its ctx ends, while a task still runs.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	p.Stop(ctx)
	if err := <-errc; err != nil && !errors.Is(err, ErrStopped) {
		t.Fatalf("Do while stopping = %v", err)
	}
	if err := p.Go(func() {}); !errors.Is(err, ErrStopped) {
		t.Fatalf("Go after Stop = %v", err)
	}
	close(release)
	p.Stop(context.Background()) // again: waits for the workers, which run what was queued
	if !queued.Load() {
		t.Fatal("a queued task did not run after Stop")
	}
}
