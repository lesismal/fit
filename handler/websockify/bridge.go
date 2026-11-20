package websockify

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net"
	stdhttp "net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/bufferpool"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/upstream"
)

// A bridge's states, under its mu.
const (
	// dialing: the target is being dialed, and the request is held.
	dialing = iota
	// open: the WebSocket is open, joined to the target's connection.
	open
	// done: the request was answered without a bridge, or given up.
	done
)

// bridge is a WebSocket and the connection to its target.
type bridge struct {
	w    *Websockify
	addr string // the target, "" for an upstream's
	// c is the request, held while dialing.
	c       *fibhttp.Context
	backend *upstream.Backend
	attempt int
	failed  bool // the last dial failed, which counts against the backend

	up atomic.Pointer[fib.Connection]
	// ws is the WebSocket once it is open and what the target sent before
	// has gone to it.
	ws  atomic.Pointer[websocket.Connection]
	b64 bool

	// mu orders the dial's outcome, the switch, the target's close and a
	// cancel: what the target sent before the WebSocket opened waits in
	// pending, at most one read since its reads are held until then.
	mu          sync.Mutex
	state       int
	gone        bool // given up while dialing
	cause       error
	targetEnded bool
	pending     []byte

	// carry is the base64 of the client's that did not make whole groups of
	// four, for the next frame; only the client's reads touch it.
	carry  [3]byte
	ncarry uint8

	toTarget, toClient direction
	// last is when data last went either way, in Unix nanoseconds.
	last    atomic.Int64
	timer   *time.Timer
	closing atomic.Bool // the idle timeout closed the WebSocket once
	ended   atomic.Int32
	once    sync.Once
}

// direction is one way through a bridge, and whether the side it reads from
// is held for the other's backlog.
type direction struct {
	mu   sync.Mutex
	held atomic.Bool
	// lift is made once, under mu, the first time it is needed.
	lift func(error)
}

// dial connects to the target: the address, or an address of the upstream.
func (b *bridge) dial() {
	w := b.w
	var err error
	if w.pool != nil {
		b.attempt++
		b.backend = w.pool.Pick(b.c.Request)
		err = w.pool.Dial(w.engine, "tcp", b.backend.Address, w.pool.ConnectTimeout(), (*target)(b), b.dialed)
	} else {
		err = w.engine.DialWithHandler("tcp", b.addr, w.connect, (*target)(b), b.dialed)
	}
	if err != nil {
		b.dialed(nil, err)
	}
}

// dialed hears how the dial went, after the target side's OnOpen when it
// opened. A failed one is tried on the upstream's next address, up to
// maxAttempts, while the client is still there; otherwise the request is
// answered 502, or 504 for a timeout.
func (b *bridge) dialed(up *fib.Connection, err error) {
	w := b.w
	b.mu.Lock()
	gone := b.gone
	retry := !gone && err != nil && w.pool != nil && b.attempt < w.tries
	if gone || err != nil && !retry {
		b.state = done
	}
	b.mu.Unlock()
	switch {
	case gone:
		if up != nil {
			_ = up.Close()
		}
		b.c.Release()
		b.finish()
	case retry:
		w.pool.Done(b.backend, true)
		b.dial()
	case err != nil:
		b.failed = true
		w.st.failed.Add(1)
		status := dialStatus(err)
		_ = b.c.Respond(status, "text/plain; charset=utf-8", statusBody(status))
		b.c.Release()
		b.finish()
	default:
		b.upgrade(up)
	}
}

// dialStatus is the status of a request whose target could not be reached.
func dialStatus(err error) int {
	var netErr net.Error
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &netErr) && netErr.Timeout() {
		return stdhttp.StatusGatewayTimeout
	}
	return stdhttp.StatusBadGateway
}

// upgrade accepts the WebSocket, once the target is open, and joins the
// two.
func (b *bridge) upgrade(up *fib.Connection) {
	w, c := b.w, b.c
	s := shardOf(w, c.Request)
	s.mu.Lock()
	if s.byReq == nil {
		s.byReq = map[*stdhttp.Request]*bridge{}
	}
	s.byReq[c.Request] = b
	s.mu.Unlock()
	ws, err := w.ws.Upgrade(c, nil)
	if err != nil {
		// Upgrade answered the request, 400 or 403.
		s.mu.Lock()
		delete(s.byReq, c.Request)
		s.mu.Unlock()
		b.mu.Lock()
		b.state = done
		b.mu.Unlock()
		_ = up.Close()
		w.st.refused.Add(1)
		c.Release()
		b.finish()
		return
	}
	b.last.Store(time.Now().UnixNano())
	w.st.bridged.Add(1)
	w.st.open.Add(1)
	b.mu.Lock()
	if len(b.pending) > 0 {
		b.send(ws, b.pending)
		b.pending = nil
	}
	b.ws.Store(ws)
	// Made before the bridge is open, which is before it can finish and
	// stop it.
	b.timer = time.AfterFunc(math.MaxInt64, b.checkIdle)
	b.timer.Reset(w.idle)
	b.state = open
	ended, gone, cause := b.targetEnded, b.gone, b.cause
	b.mu.Unlock()
	c.Release()
	if ended {
		// The target's end, which targetClosed left to this.
		b.sideEnded()
	}
	switch {
	case gone:
		// Given up while it switched.
		b.abandon(cause)
	case ended:
		_ = ws.Close(websocket.CloseNormal, "")
	default:
		up.HoldReads(false)
	}
}

// abandon gives up the dial for cause, or closes the bridge at once once it
// is open.
func (b *bridge) abandon(cause error) {
	b.mu.Lock()
	state := b.state
	if state == dialing {
		// dialed, or upgrade once it has switched, will see it.
		b.gone, b.cause = true, cause
	}
	b.mu.Unlock()
	if state != open {
		return
	}
	b.ws.Load().Abort(cause)
	if up := b.up.Load(); up != nil {
		_ = up.Close()
	}
}

// checkIdle closes a bridge that carried nothing for the idle timeout, and
// looks again when it last carried something otherwise: the first time
// with a close frame, 1001, and if the client has not taken it by the next,
// at once.
func (b *bridge) checkIdle() {
	since := time.Since(time.Unix(0, b.last.Load()))
	if since < b.w.idle {
		b.timer.Reset(b.w.idle - since)
		return
	}
	if b.closing.Swap(true) {
		b.abandon(errIdle)
		return
	}
	_ = b.ws.Load().Close(websocket.CloseGoingAway, "idle timeout")
	if up := b.up.Load(); up != nil {
		_ = up.Close()
	}
	b.timer.Reset(b.w.idle)
}

// fromClient passes a frame the client sent to the target, and holds the
// client's reads while the target has more than the watermark unsent.
func (b *bridge) fromClient(ws *websocket.Connection, fin bool, payload []byte) {
	up := b.up.Load()
	b.last.Store(time.Now().UnixNano())
	if b.b64 {
		data, err := b.decode(payload, fin)
		if err != nil {
			_ = ws.Close(websocket.CloseInvalidPayload, "invalid base64")
			return
		}
		if data != nil {
			_ = up.Send(data)
			b.w.st.toTarget.Add(uint64(len(data)))
			bufferpool.Put(data)
		}
	} else {
		_ = up.Send(payload)
		b.w.st.toTarget.Add(uint64(len(payload)))
	}
	d := &b.toTarget
	if d.held.Load() {
		return
	}
	d.mu.Lock()
	// Held under the lock, so that a lift that comes before HoldReads(true)
	// has returned cannot be undone by it.
	if d.lift == nil {
		d.lift = func(error) { b.lift(d, ws.HoldReads) }
	}
	if !d.held.Load() && up.NotifyOutput(watermark, d.lift) {
		d.held.Store(true)
		ws.HoldReads(true)
	}
	d.mu.Unlock()
}

// lift lets the side d holds read again, once the other's backlog has
// drained or it is closing.
func (b *bridge) lift(d *direction, hold func(bool)) {
	d.mu.Lock()
	d.held.Store(false)
	hold(false)
	d.mu.Unlock()
}

// decode decodes what the frame payload carries of a base64 message, and
// keeps what does not make a whole group of four for the next frame. The
// result is borrowed from the bufferpool. A message whose base64 does not
// end at a group of four is invalid.
func (b *bridge) decode(payload []byte, fin bool) ([]byte, error) {
	n := int(b.ncarry)
	total := n + len(payload)
	whole := total &^ 3
	if fin && whole != total {
		return nil, base64.CorruptInputError(total)
	}
	src := payload[:max(whole-n, 0)]
	if n > 0 && whole > 0 {
		joined := bufferpool.Get(whole)
		copy(joined, b.carry[:n])
		copy(joined[n:], payload[:whole-n])
		src = joined[:whole]
		defer bufferpool.Put(joined)
	}
	if whole == 0 {
		// Too little for a group: it all waits for the next frame.
		b.ncarry = uint8(copy(b.carry[n:], payload)) + b.ncarry
		return nil, nil
	}
	rest := payload[whole-n:]
	b.ncarry = uint8(copy(b.carry[:], rest))
	dst := bufferpool.Get(base64.StdEncoding.DecodedLen(whole))
	m, err := base64.StdEncoding.Decode(dst, src)
	if err != nil {
		bufferpool.Put(dst)
		return nil, err
	}
	return dst[:m], nil
}

// send sends data the target sent to the client, as a binary message or a
// text one of base64, and reports whether the client then has more than
// the watermark unsent.
func (b *bridge) send(ws *websocket.Connection, data []byte) bool {
	var above bool
	var err error
	if b.b64 {
		enc := bufferpool.Get(base64.StdEncoding.EncodedLen(len(data)))
		base64.StdEncoding.Encode(enc, data)
		above, err = ws.WriteMessageNoWait(websocket.Text, enc)
		bufferpool.Put(enc)
	} else {
		above, err = ws.WriteMessageNoWait(websocket.Binary, data)
	}
	b.w.st.toClient.Add(uint64(len(data)))
	return err == nil && above
}

// fromTarget passes what the target sent to the client, or keeps it until
// the WebSocket is open, and holds the target's reads while the client has
// more than the watermark unsent.
func (b *bridge) fromTarget(up *fib.Connection, data []byte) {
	ws := b.ws.Load()
	if ws == nil {
		b.mu.Lock()
		if ws = b.ws.Load(); ws == nil {
			b.pending = append(b.pending, data...)
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()
	}
	b.last.Store(time.Now().UnixNano())
	if !b.send(ws, data) {
		return
	}
	d := &b.toClient
	d.mu.Lock()
	if d.lift == nil {
		d.lift = func(error) { b.lift(d, up.HoldReads) }
	}
	if !d.held.Load() && ws.OnWritable(d.lift) {
		d.held.Store(true)
		up.HoldReads(true)
	}
	d.mu.Unlock()
}

// clientClosed ends the target's side once what the client sent has gone.
func (b *bridge) clientClosed() {
	if up := b.up.Load(); up != nil {
		up.CloseAfterSend()
	}
	b.sideEnded()
}

// targetClosed closes the WebSocket once what the target sent has gone, or
// leaves that to upgrade when it is not open yet.
func (b *bridge) targetClosed() {
	b.mu.Lock()
	b.targetEnded = true
	state := b.state
	b.mu.Unlock()
	if state != open {
		return
	}
	ws := b.ws.Load()
	// The client's reads may be held for the target's backlog, which will
	// never drain now; its NotifyOutput lifts them as the target closes.
	_ = ws.Close(websocket.CloseNormal, "")
	b.sideEnded()
}

// sideEnded counts a side's end; once both are over, the bridge lets go of
// what it holds.
func (b *bridge) sideEnded() {
	if b.ended.Add(1) == 2 {
		b.w.st.open.Add(-1)
		b.finish()
	}
}

// finish lets go of what the bridge holds, once: its place among the
// handler's bridges and its connection limit, the upstream's backend and
// the config.
func (b *bridge) finish() {
	b.once.Do(func() {
		if b.timer != nil {
			b.timer.Stop()
		}
		b.remove()
		w := b.w
		if w.pool != nil {
			w.pool.Done(b.backend, b.failed)
		}
		w.conns.Add(-1)
		w.graph.Release()
	})
}

// target is the target connection's handler.
type target bridge

// OnOpen holds the target's reads until the WebSocket is open: a server
// that speaks first, as VNC does, has its greeting sent once there is
// somewhere to send it.
func (t *target) OnOpen(up *fib.Connection) {
	b := (*bridge)(t)
	up.SetWriteBackpressure(false)
	up.HoldReads(true)
	b.up.Store(up)
}

func (t *target) OnData(up *fib.Connection, data []byte) { (*bridge)(t).fromTarget(up, data) }

func (t *target) OnPriorityData(*fib.Connection, []byte) {}

func (t *target) OnClose(*fib.Connection, error) { (*bridge)(t).targetClosed() }
