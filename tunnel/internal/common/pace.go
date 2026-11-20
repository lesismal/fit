package common

import (
	"sync"

	"github.com/lesismal/fib"
)

// Watermark is how much a connection a relay sends to may have queued for
// its socket before the relay stops reading the other side. A WebSocket of
// a relay has it as its PendingWriteLimit.
const Watermark = 256 << 10

// Reads is a connection whose reads a Gate holds: a *fib.Connection or a
// *websocket.Connection.
type Reads interface {
	HoldReads(hold bool)
	SetWriteBackpressure(on bool)
}

// Output is the side of a relay a Gate's connection sends to: OnWritable
// reports whether its backlog is past its watermark, and then runs fn once
// it has drained, as websocket.Connection.OnWritable does. A
// *websocket.Connection is one, its watermark its PendingWriteLimit; Out
// makes one of a *fib.Connection.
type Output interface {
	OnWritable(fn func(error)) bool
}

// fibOutput is the Output of a fib connection, past Watermark.
type fibOutput fib.Connection

func (o *fibOutput) OnWritable(fn func(error)) bool {
	return (*fib.Connection)(o).NotifyOutput(Watermark, fn)
}

// Out is c as an Output: its backlog past Watermark.
func Out(c *fib.Connection) Output { return (*fibOutput)(c) }

// Gate holds the reads of one side of a relay while the other side's
// backlog is past its watermark, and lets them go once it has drained (rule
// 8). Both sides of a relay have write backpressure off, and each a Gate of
// its own.
type Gate struct {
	c Reads

	mu      sync.Mutex
	waiting bool
}

// NewGate returns the gate of c's reads, and turns off c's own write
// backpressure, which the gate of the other side stands in for.
func NewGate(c Reads) *Gate {
	c.SetWriteBackpressure(false)
	return &Gate{c: c}
}

// Pace is called after each send to to of what was read from the gate's
// connection: it holds the gate's reads while to's backlog is past its
// watermark.
func (g *Gate) Pace(to Output) {
	g.mu.Lock()
	if g.waiting {
		g.mu.Unlock()
		return
	}
	g.waiting = true
	g.mu.Unlock()
	if to.OnWritable(g.release) {
		g.mu.Lock()
		// release may have run already, the backlog drained.
		if g.waiting {
			g.c.HoldReads(true)
		}
		g.mu.Unlock()
		return
	}
	g.mu.Lock()
	g.waiting = false
	g.mu.Unlock()
}

// Hold holds the gate's reads, as while what was read waits for the other
// side to open; Release lets them go.
func (g *Gate) Hold() { g.c.HoldReads(true) }

// Release lets the gate's reads go.
func (g *Gate) Release() { g.release(nil) }

func (g *Gate) release(error) {
	g.mu.Lock()
	g.waiting = false
	g.c.HoldReads(false)
	g.mu.Unlock()
}
