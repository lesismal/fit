package filepool

import (
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withProcs sets GOMAXPROCS to n for the test.
func withProcs(t *testing.T, n int) {
	t.Helper()
	old := goruntime.GOMAXPROCS(n)
	t.Cleanup(func() { goruntime.GOMAXPROCS(old) })
}

// waitFor fails t unless ok turns true within a few seconds.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("no %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestKeepsCPUs checks that a pool keeps as many workers as GOMAXPROCS, or
// its bound when that is fewer, and runs jobs on them.
func TestKeepsCPUs(t *testing.T) {
	withProcs(t, 2)
	if n := New(16, 8, time.Second).Workers(); n != 2 {
		t.Fatalf("%d workers kept, want 2", n)
	}
	p := New(1, 8, time.Second)
	if n := p.Workers(); n != 1 {
		t.Fatalf("%d workers kept, want 1", n)
	}
	var wg sync.WaitGroup
	var ran atomic.Int32
	for range 5 {
		wg.Add(1)
		if !p.Submit(func() { ran.Add(1); wg.Done() }, nil) {
			t.Fatal("refused")
		}
	}
	wg.Wait()
	if ran.Load() != 5 || p.Workers() != 1 {
		t.Fatalf("%d ran, %d workers", ran.Load(), p.Workers())
	}
}

// TestGrowsForBlocked checks that workers held past BlockAfter get company,
// with or without a Submit after they block, up to the bound, and that the
// workers added leave once no job waits.
func TestGrowsForBlocked(t *testing.T) {
	withProcs(t, 2)
	p := New(5, 64, 10*time.Second)
	release := make(chan struct{})
	var running atomic.Int32
	block := func() { running.Add(1); <-release; running.Add(-1) }
	// Both kept workers block, and two jobs wait behind them, all queued
	// at once: no Submit comes after the workers have blocked for
	// BlockAfter, so the pool's own look again adds workers.
	for range 4 {
		if !p.Submit(block, nil) {
			t.Fatal("refused")
		}
	}
	waitFor(t, "workers added for blocked ones", func() bool { return running.Load() == 4 })
	if n := p.Workers(); n != 4 {
		t.Fatalf("%d workers, want 4", n)
	}
	// More blocked jobs reach the bound, 5, and wait past it.
	for range 3 {
		p.Submit(block, nil)
	}
	time.Sleep(5 * BlockAfter)
	if n, r := p.Workers(), running.Load(); n != 5 || r != 5 {
		t.Fatalf("%d workers, %d running, want the bound, 5", n, r)
	}
	close(release)
	waitFor(t, "the added workers gone", func() bool { return running.Load() == 0 && p.Workers() == 2 })
}

// TestNoGrowthWhileRunning checks that jobs that keep the workers busy
// without blocking for BlockAfter add none.
func TestNoGrowthWhileRunning(t *testing.T) {
	withProcs(t, 2)
	p := New(8, 4096, 10*time.Second)
	var wg sync.WaitGroup
	for range 2000 {
		wg.Add(1)
		p.Submit(func() { wg.Done() }, nil)
	}
	wg.Wait()
	if n := p.Workers(); n != 2 {
		t.Fatalf("%d workers after short jobs, want 2", n)
	}
}

// TestExpired checks that a job taken up past the timeout runs expire in
// place of run, and that a pool of no workers and no queue takes nothing.
func TestExpired(t *testing.T) {
	withProcs(t, 1)
	p := New(1, 8, 10*time.Millisecond)
	release := make(chan struct{})
	p.Submit(func() { <-release }, nil)
	expired := make(chan bool, 2)
	p.Submit(func() { expired <- false }, func() { expired <- true })
	// Without expire, a job past the timeout is dropped.
	p.Submit(func() { expired <- false }, nil)
	time.Sleep(30 * time.Millisecond)
	close(release)
	if !<-expired {
		t.Fatal("a job past the timeout ran")
	}
	time.Sleep(10 * time.Millisecond)
	if len(expired) != 0 {
		t.Fatal("a job past the timeout without expire ran")
	}
	if p.Timeout() != 10*time.Millisecond {
		t.Fatalf("timeout %v", p.Timeout())
	}
	if New(0, 0, time.Second).Submit(func() {}, nil) {
		t.Fatal("a pool of no workers took a job")
	}
	full := New(1, 1, time.Second)
	hold := make(chan struct{})
	defer close(hold)
	full.Submit(func() { <-hold }, nil)
	waitFor(t, "the job taken up", func() bool { return len(full.jobs) == 0 })
	full.Submit(func() {}, nil)
	if full.Submit(func() {}, nil) {
		t.Fatal("a full queue took a job")
	}
}

// TestHook checks that the hook runs before each job.
func TestHook(t *testing.T) {
	p := New(1, 8, time.Second)
	var hooked atomic.Int32
	p.Hook = func() { hooked.Add(1) }
	done := make(chan struct{})
	p.Submit(func() { close(done) }, nil)
	<-done
	if hooked.Load() != 1 {
		t.Fatalf("hook ran %d times", hooked.Load())
	}
}
