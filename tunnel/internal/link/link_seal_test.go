package link

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/tunnel/internal/bodycrypt"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// tap is one direction of a wire an attacker sits on: it keeps a copy of
// every frame sent, and hands the frames to the peer in order, through
// filter when set, which returns what goes on instead.
type tap struct {
	mu     sync.Mutex
	sent   [][]byte
	filter func(b []byte) [][]byte
	ch     chan []byte
	done   bool
	closed atomic.Int32
}

func newTap(t *testing.T) *tap {
	w := &tap{ch: make(chan []byte, 1024)}
	t.Cleanup(func() {
		w.mu.Lock()
		w.done = true
		close(w.ch)
		w.mu.Unlock()
	})
	return w
}

// run delivers what goes on the wire to peer, in order.
func (w *tap) run(peer *Link) {
	go func() {
		for b := range w.ch {
			peer.OnData(b)
		}
	}()
}

func (w *tap) Send(b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return net.ErrClosed
	}
	w.sent = append(w.sent, append([]byte(nil), b...))
	out := [][]byte{b}
	if w.filter != nil {
		out = w.filter(b)
	}
	for _, b := range out {
		w.ch <- b
	}
	return nil
}

// inject puts b on the wire, as the attacker.
func (w *tap) inject(b []byte) { w.ch <- append([]byte(nil), b...) }

// frame is the i-th frame sent.
func (w *tap) frame(i int) []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sent[i]
}

func (w *tap) Close() error         { w.closed.Add(1); return nil }
func (w *tap) CloseAfterSend()      {}
func (w *tap) LocalAddr() net.Addr  { return nil }
func (w *tap) RemoteAddr() net.Addr { return nil }

// tapped is a and b, joined by taps: ab carries what a sends, ba what b
// does, with password on both sides.
func tapped(t *testing.T, ra, rb *Router, password string) (a, b *Link, ab, ba *tap) {
	t.Helper()
	ab, ba = newTap(t), newTap(t)
	a = New(ab, ra, coder(t, password))
	b = New(ba, rb, coder(t, password))
	ab.run(b)
	ba.run(a)
	return a, b, ab, ba
}

// counting serves tunnel data and registrations, counting each.
func counting() (*Router, *atomic.Int32, *atomic.Int32) {
	var data, regs atomic.Int32
	r := NewRouter()
	r.HandleNotify(proto.MethodTunnelData, func(*Link, []byte) { data.Add(1) }, true)
	r.HandleCall(proto.MethodRegister, func(*Link, []byte) (any, error) { regs.Add(1); return "ok", nil }, true)
	return r, &data, &regs
}

// failed waits for l to fail, with an error that says want.
func failed(t *testing.T, l *Link, w *tap, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for w.closed.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the link did not close")
		}
		time.Sleep(time.Millisecond)
	}
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err %v, want %q", err, want)
	}
}

func waitCount(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for n.Load() != want {
		if time.Now().After(deadline) {
			t.Fatalf("count %d, want %d", n.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// A tunnel frame replayed on its link - a Redis command written to the
// service twice - closes the link, and is not served again.
func TestSealedReplay(t *testing.T) {
	rb, data, _ := counting()
	a, b, ab, ba := tapped(t, NewRouter(), rb, "pw")
	if err := a.SendTunnel(proto.MethodTunnelData, 7, []byte("SET k v\r\n")); err != nil {
		t.Fatal(err)
	}
	waitCount(t, data, 1)
	ab.inject(ab.frame(1))
	failed(t, b, ba, "failed to decrypt")
	if data.Load() != 1 {
		t.Fatalf("served %d times", data.Load())
	}
	_ = a
}

// Frames that arrive in another order than they were sent close the link.
func TestSealedReorder(t *testing.T) {
	rb, data, _ := counting()
	ab, ba := newTap(t), newTap(t)
	var held []byte
	ab.filter = func(b []byte) [][]byte {
		if b[LengthSize] != kindSealed|kindNotify {
			return [][]byte{b}
		}
		if held == nil {
			held = b
			return nil
		}
		return [][]byte{b, held}
	}
	a := New(ab, NewRouter(), coder(t, "pw"))
	b := New(ba, rb, coder(t, "pw"))
	ab.run(b)
	ba.run(a)
	// a sends once b's hello came.
	for range 2 {
		if err := a.SendTunnel(proto.MethodTunnelData, 7, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	failed(t, b, ba, "failed to decrypt")
	if data.Load() != 0 {
		t.Fatalf("served %d", data.Load())
	}
}

// A frame sent back to its sender closes the sender's link; so does a
// hello sent back.
func TestSealedReflect(t *testing.T) {
	t.Run("frame", func(t *testing.T) {
		ra, data, _ := counting()
		a, b, ab, ba := tapped(t, ra, NewRouter(), "pw")
		if err := a.SendTunnel(proto.MethodTunnelData, 7, []byte("x")); err != nil {
			t.Fatal(err)
		}
		for len(sentOf(ab)) < 2 {
			time.Sleep(time.Millisecond)
		}
		ba.inject(ab.frame(1))
		failed(t, a, ab, "failed to decrypt")
		if data.Load() != 0 {
			t.Fatal("a served its own frame")
		}
		_ = b
	})
	t.Run("hello", func(t *testing.T) {
		ab, ba := newTap(t), newTap(t)
		a := New(ab, NewRouter(), coder(t, "pw"))
		ba.run(a)
		ba.inject(ab.frame(0))
		failed(t, a, ab, "this side's own")
	})
}

func sentOf(w *tap) [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sent
}

// A node's sealed registration, replayed with the node's hello on another
// link to the hub, does not register: that link's keys are its own.
func TestSealedRegisterReplay(t *testing.T) {
	rb, _, regs := counting()
	node, _, ab, _ := tapped(t, NewRouter(), rb, "pw")
	if err := node.Call(proto.MethodRegister, "node lab", nil, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	hello, register := ab.frame(0), ab.frame(1)

	rb2, _, regs2 := counting()
	hubSide := newTap(t)
	hub := New(hubSide, rb2, coder(t, "pw"))
	attacker := newTap(t)
	attacker.run(hub)
	attacker.inject(hello)
	attacker.inject(register)
	failed(t, hub, hubSide, "failed to decrypt")
	if regs2.Load() != 0 || regs.Load() != 1 {
		t.Fatalf("registered %d times on the replay's link, %d on the node's", regs2.Load(), regs.Load())
	}
}

// A peer that seals without a hello runs version 1, and one whose hello is
// of another version runs something else: each fails, saying so. A second
// hello, a malformed one, or one to a side without a body password fails
// too.
func TestSealedVersions(t *testing.T) {
	hello := func(body []byte) []byte { return rawFrame(kindSealed|kindHello, 0, 0, body) }
	for _, tc := range []struct {
		name   string
		frames [][]byte
		coder  bool
		want   string
	}{
		{"version 1", [][]byte{rawFrame(kindSealed|kindCall, proto.MethodRegister, 1, make([]byte, 40))}, true, "version 1"},
		{"version 3", [][]byte{hello(append([]byte{3}, bodycrypt.Random()...))}, true, "version 3"},
		{"empty hello", [][]byte{hello(nil)}, true, "malformed"},
		{"short hello", [][]byte{hello([]byte{Version, 1, 2})}, true, "malformed"},
		{"second hello", [][]byte{hello(append([]byte{Version}, bodycrypt.Random()...)), hello(append([]byte{Version}, bodycrypt.Random()...))}, true, "malformed"},
		{"unsealed", [][]byte{rawFrame(kindCall, proto.MethodRegister, 1, nil)}, true, "unencrypted"},
		{"no body password", [][]byte{hello(append([]byte{Version}, bodycrypt.Random()...))}, false, "without a body password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, in := newTap(t), newTap(t)
			var cd *bodycrypt.Coder
			if tc.coder {
				cd = coder(t, "pw")
			}
			l := New(out, NewRouter(), cd)
			in.run(l)
			for _, f := range tc.frames {
				in.inject(f)
			}
			failed(t, l, out, tc.want)
		})
	}
}

// What a side sends before the peer's hello waits for it, up to a bound,
// past which the link fails rather than hold more.
func TestSealedBeforeHello(t *testing.T) {
	out := newTap(t)
	l := New(out, NewRouter(), coder(t, "pw"))
	payload := make([]byte, 1<<20)
	var err error
	for i := 0; i < 8 && err == nil; i++ {
		err = l.SendTunnel(proto.MethodTunnelData, 1, payload)
	}
	if !errors.Is(err, errBacklog) {
		t.Fatalf("err %v", err)
	}
	failed(t, l, out, "before the peer's hello")
	if n := len(sentOf(out)); n != 1 {
		t.Fatalf("%d frames went out before the hello", n)
	}
}

// A connection cut before the peer's hello came ends the calls waiting
// for it, rather than leaving them to time out.
func TestSealedCutMidHandshake(t *testing.T) {
	e := tunneltest.Engine(t)
	ln, err := transport.Listen(e, "127.0.0.1:0", transport.ListenOptions{}, func(transport.Conn) transport.Handler { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var a *Link
	if _, err := transport.Dial(e, ln.URL(), 5*time.Second, func(c transport.Conn) transport.Handler { a = New(c, NewRouter(), coder(t, "pw")); return a }); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := a.Call(proto.MethodRegister, "x", nil, 10*time.Second); !errors.Is(err, ErrClosed) {
		t.Fatalf("call: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

// Both sides send at once, from many goroutines, frames up to the longest:
// each arrives whole, once, in its sender's order.
func TestSealedBothWays(t *testing.T) {
	type got struct {
		mu   sync.Mutex
		next map[uint64]uint32
		n    atomic.Int32
		bad  atomic.Int32
	}
	serve := func(g *got) *Router {
		r := NewRouter()
		r.HandleNotify(proto.MethodTunnelData, func(_ *Link, body []byte) {
			id, p, err := proto.ParseFrame(body)
			if err != nil || len(p) < 4 {
				g.bad.Add(1)
				return
			}
			seq := binary.BigEndian.Uint32(p)
			g.mu.Lock()
			if g.next[id] != seq || !bytes.Equal(p[4:], pattern(len(p)-4, byte(seq))) {
				g.bad.Add(1)
			}
			g.next[id] = seq + 1
			g.mu.Unlock()
			g.n.Add(1)
		}, true)
		return r
	}
	ga, gb := &got{next: map[uint64]uint32{}}, &got{next: map[uint64]uint32{}}
	a, b := pair(t, serve(ga), serve(gb), coder(t, "pw"), coder(t, "pw"))
	const senders, each = 4, 20
	longest := MaxFrame - HeaderSize - 8 - bodycrypt.Overhead
	var wg sync.WaitGroup
	for _, l := range []*Link{a, b} {
		for s := range senders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range each {
					n := 1 + (i*7919)%(64<<10)
					if i == each-1 {
						n = longest - 4
					}
					p := binary.BigEndian.AppendUint32(nil, uint32(i))
					p = append(p, pattern(n, byte(i))...)
					if err := l.SendTunnel(proto.MethodTunnelData, uint64(s), p); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	for _, g := range []*got{ga, gb} {
		waitCount(t, &g.n, senders*each)
		if g.bad.Load() != 0 {
			t.Fatalf("%d frames arrived wrong", g.bad.Load())
		}
	}
	if a.Err() != nil || b.Err() != nil {
		t.Fatalf("errs %v, %v", a.Err(), b.Err())
	}
}

func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return b
}
