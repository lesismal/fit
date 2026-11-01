package common

import (
	"io"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

func dial(t *testing.T, e *fib.Engine, addr string) *fib.Connection {
	t.Helper()
	got := make(chan *fib.Connection, 1)
	if err := e.Dial("tcp", addr, 5*time.Second, func(c *fib.Connection, err error) {
		if err != nil {
			t.Error(err)
		}
		got <- c
	}); err != nil {
		t.Fatal(err)
	}
	c := <-got
	if c == nil {
		t.FailNow()
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
	}
}

// A gate holds its reads while the other side's backlog is past Watermark,
// and lets them go once it drains.
func TestGatePace(t *testing.T) {
	e := tunneltest.Engine(t)
	stalled, slow := tunneltest.Stall(t)
	to := dial(t, e, slow)
	peer := <-stalled
	g := NewGate(dial(t, e, tunneltest.Echo(t, "")))

	// Under the watermark, nothing is held.
	if err := to.Send(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	g.Pace(to)
	if g.c.ReadsHeld() {
		t.Fatal("reads held under the watermark")
	}

	// Far more than the socket buffers take: the backlog stays past the
	// watermark while the peer does not read.
	const total = 64 << 20
	chunk := make([]byte, 1<<20)
	for range total / len(chunk) {
		if err := to.Send(chunk); err != nil {
			t.Fatal(err)
		}
	}
	g.Pace(to)
	if !g.c.ReadsHeld() {
		t.Fatal("reads not held past the watermark")
	}
	// A gate already waiting waits on.
	g.Pace(to)

	go func() { _, _ = io.CopyN(io.Discard, peer, total+1024) }()
	waitFor(t, "the backlog to drain", func() bool { return !g.c.ReadsHeld() })

	g.Hold()
	if !g.c.ReadsHeld() {
		t.Fatal("Hold")
	}
	g.Release()
	if g.c.ReadsHeld() {
		t.Fatal("Release")
	}
}
