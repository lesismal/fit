package ws

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// benchWindow bounds what a throughput benchmark has sent and its peer has
// yet to get, as a link's windows bound what it queues; past it the sender
// waits.
const benchWindow = 4 << 20

// counter is a transport.Handler that counts what arrives, echoing it when
// echo is set, and wakes a waiter on every arrival.
type counter struct {
	c    transport.Conn
	echo bool
	got  atomic.Int64
	wake chan struct{}
}

func newCounter(c transport.Conn, echo bool) *counter {
	return &counter{c: c, echo: echo, wake: make(chan struct{}, 1)}
}

func (h *counter) OnData(data []byte) {
	if h.echo {
		_ = h.c.Send(append([]byte(nil), data...))
	}
	h.got.Add(int64(len(data)))
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *counter) OnClose(error) {}

// waitGot waits until h has got at least n bytes.
func (h *counter) waitGot(b *testing.B, n int64) {
	for h.got.Load() < n {
		select {
		case <-h.wake:
		case <-time.After(10 * time.Second):
			b.Fatalf("stalled at %d of %d bytes", h.got.Load(), n)
		}
	}
}

// benchLink opens a ws:// link on one engine over the loopback and returns
// the dialed end and the accepted one, each with its counter; the accepted
// end echoes when echo is set.
func benchLink(b *testing.B, echo bool) (client transport.Conn, ch *counter, server transport.Conn, sh *counter) {
	b.Helper()
	e := tunneltest.Engine(b)
	accepted := make(chan *counter, 1)
	l, err := transport.Listen(e, "ws://127.0.0.1:0/link", transport.ListenOptions{}, func(c transport.Conn) transport.Handler {
		h := newCounter(c, echo)
		accepted <- h
		return h
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = l.Close() })
	c, err := transport.Dial(e, l.URL(), 5*time.Second, transport.ClientOptions{}, func(c transport.Conn) transport.Handler {
		ch = newCounter(c, false)
		return ch
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Close() })
	select {
	case sh = <-accepted:
	case <-time.After(5 * time.Second):
		b.Fatal("the link was not accepted")
	}
	return c, ch, sh.c, sh
}

// benchSend sends b.N sends of size bytes from one end of a link to the
// other, keeping no more than benchWindow in flight. The payload is the same
// buffer each time: neither transport keeps a send's bytes once Send
// returns, and nothing reads them.
func benchSend(b *testing.B, size int, up bool) {
	client, ch, server, sh := benchLink(b, false)
	from, to := client, sh
	if !up {
		from, to = server, ch
	}
	p := make([]byte, size)
	for i := range p {
		p[i] = byte(i)
	}
	window := max(int64(benchWindow), 2*int64(size))
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	var sent int64
	for range b.N {
		if sent-to.got.Load() > window-int64(size) {
			to.waitGot(b, sent+int64(size)-window)
		}
		if err := from.Send(p); err != nil {
			b.Fatal(err)
		}
		sent += int64(size)
	}
	to.waitGot(b, sent)
}

// Up is the dialer sending to the listener (masked frames), down the
// listener to the dialer.
func BenchmarkSend(b *testing.B) {
	for _, dir := range []struct {
		name string
		up   bool
	}{{"up", true}, {"down", false}} {
		for _, s := range []struct {
			name string
			size int
		}{{"1KiB", 1 << 10}, {"16KiB", 16 << 10}, {"256KiB", 256 << 10}} {
			b.Run(dir.name+"/"+s.name, func(b *testing.B) { benchSend(b, s.size, dir.up) })
		}
	}
}

// BenchmarkEcho is a small request the listener echoes, one at a time.
func BenchmarkEcho(b *testing.B) {
	const size = 64
	client, ch, _, _ := benchLink(b, true)
	p := make([]byte, size)
	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	var want int64
	for range b.N {
		if err := client.Send(p); err != nil {
			b.Fatal(err)
		}
		want += size
		ch.waitGot(b, want)
	}
}
