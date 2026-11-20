package common

import (
	"bytes"
	"io"
	stdhttp "net/http"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/websocket"

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

func held(g *Gate) bool { return g.c.(*fib.Connection).ReadsHeld() }

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
	g.Pace(Out(to))
	if held(g) {
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
	g.Pace(Out(to))
	if !held(g) {
		t.Fatal("reads not held past the watermark")
	}
	// A gate already waiting waits on.
	g.Pace(Out(to))

	go func() { _, _ = io.CopyN(io.Discard, peer, total+1024) }()
	waitFor(t, "the backlog to drain", func() bool { return !held(g) })

	g.Hold()
	if !held(g) {
		t.Fatal("Hold")
	}
	g.Release()
	if held(g) {
		t.Fatal("Release")
	}
}

// wsPair is a WebSocket dialed on e, whose messages go to got, and the
// server's side of it, on an engine of its own; the dialed one has
// Watermark as its PendingWriteLimit, as a relay's has.
func wsPair(t *testing.T, e *fib.Engine, got chan<- []byte) (client, server *websocket.Connection) {
	t.Helper()
	opened := make(chan *websocket.Connection, 1)
	srv := tunneltest.Engine(t)
	i, err := srv.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: websocket.NewHandler(websocket.HandlerFuncs{
		Open: func(c *websocket.Connection, _ *stdhttp.Request) { opened <- c },
	})})
	if err != nil {
		t.Fatal(err)
	}
	addrs, _ := srv.BindingAddrs(i)
	d := websocket.NewDialer(e, websocket.DialerConfig{PendingWriteLimit: Watermark})
	client, _, err = d.Go("ws://"+addrs[0].String()+"/", nil, websocket.HandlerFuncs{
		Message: func(_ *websocket.Connection, _ websocket.Opcode, data []byte) { got <- bytes.Clone(data) },
	}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Abort(nil) })
	return client, <-opened
}

// A gate holds a WebSocket's reads, and holds a connection's reads while
// a WebSocket it sends to has more than its PendingWriteLimit waiting,
// until the WebSocket's peer reads.
func TestGateWebSocket(t *testing.T) {
	e := tunneltest.Engine(t)
	got := make(chan []byte, 1)
	client, server := wsPair(t, e, got)

	g := NewGate(client)
	g.Hold()
	if err := server.WriteMessage(websocket.Binary, []byte("held")); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-got:
		t.Fatalf("a held WebSocket read %q", b)
	case <-time.After(100 * time.Millisecond):
	}
	g.Release()
	select {
	case b := <-got:
		if string(b) != "held" {
			t.Fatalf("%q", b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a released WebSocket read nothing")
	}

	// The server does not read: what the client sends waits.
	server.HoldReads(true)
	from := NewGate(dial(t, e, tunneltest.Echo(t, "")))
	from.Pace(client)
	if held(from) {
		t.Fatal("reads held under the limit")
	}
	chunk := make([]byte, 1<<20)
	for range 64 {
		if err := client.WriteMessage(websocket.Binary, chunk); err != nil {
			t.Fatal(err)
		}
	}
	from.Pace(client)
	if !held(from) {
		t.Fatal("reads not held past the limit")
	}
	server.HoldReads(false)
	waitFor(t, "the backlog to drain", func() bool { return !held(from) })
}
