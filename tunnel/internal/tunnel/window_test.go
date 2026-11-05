package tunnel

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// closeLink records the reasons an end closes with.
type closeLink struct {
	mu      sync.Mutex
	reasons []string
}

func (l *closeLink) SendTunnel(m proto.Method, _ uint64, payload []byte) error {
	if m == proto.MethodTunnelClose {
		l.mu.Lock()
		l.reasons = append(l.reasons, string(payload))
		l.mu.Unlock()
	}
	return nil
}

func (l *closeLink) reason() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.reasons, "; ")
}

// An acknowledgement of nothing, or of more than the end sent and has not
// had acknowledged, closes the tunnel: it would let the end send without
// bound. One that keeps to what was sent gives the window back.
func TestAckBounds(t *testing.T) {
	for _, n := range []int64{0, -1, 101, proto.Window, 1 << 62} {
		tb, l := NewTable(nil), &closeLink{}
		e := New(tb, l, 1, true)
		_ = tb.Add(e)
		e.local(make([]byte, 100))
		e.acked(n)
		if tb.Get(1) != nil || !strings.Contains(l.reason(), errBadAck) {
			t.Fatalf("ack of %d: open %v, reason %q", n, tb.Get(1) != nil, l.reason())
		}
	}
	tb, l := NewTable(nil), &closeLink{}
	e := New(tb, l, 1, true)
	_ = tb.Add(e)
	e.local(make([]byte, 100))
	e.acked(60)
	e.acked(40)
	if tb.Get(1) == nil || e.window != proto.Window {
		t.Fatalf("open %v, window %d: %s", tb.Get(1) != nil, e.window, l.reason())
	}
	e.acked(1)
	if tb.Get(1) != nil {
		t.Fatal("an ack past what was sent")
	}
}

// Data for an end that has no connection is not dropped in silence: the
// tunnel closes.
func TestRemoteNoConn(t *testing.T) {
	tb, l := NewTable(nil), &closeLink{}
	e := New(tb, l, 1, true)
	_ = tb.Add(e)
	e.remote([]byte("lost"))
	if tb.Get(1) != nil || l.reason() == "" {
		t.Fatalf("open %v, reason %q", tb.Get(1) != nil, l.reason())
	}
}

// endOn is an end of a tunnel on a TCP connection a client opened, whose
// reads the client never makes.
func endOn(t *testing.T) (*Table, *closeLink, *Endpoint, net.Conn) {
	t.Helper()
	e := tunneltest.Engine(t)
	tb, l := NewTable(nil), &closeLink{}
	ends := make(chan *Endpoint, 1)
	h := &Handler{Open: func(*fib.Connection) *Endpoint {
		end := New(tb, l, 1, true)
		_ = tb.Add(end)
		return end
	}, Opened: func(end *Endpoint) { ends <- end }}
	i, err := e.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: h})
	if err != nil {
		t.Fatal(err)
	}
	addrs, _ := e.BindingAddrs(i)
	c, err := net.Dial("tcp", addrs[0].String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	select {
	case end := <-ends:
		return tb, l, end, c
	case <-time.After(10 * time.Second):
		t.Fatal("the end did not open")
	}
	return nil, nil, nil, nil
}

// An other end that sends past its window, to a connection that does not
// read, has its tunnel closed rather than its bytes queued without bound:
// once the connection's queue is long, the end stops acknowledging, and
// what arrives past MaxInFlight more is the other end ignoring its window.
func TestRemotePastWindow(t *testing.T) {
	tb, l, e, _ := endOn(t)
	chunk := make([]byte, proto.MaxChunk)
	// The socket's buffers take some first, then the connection's queue
	// up to ackLimit, then MaxInFlight.
	for sent := 0; sent <= 64<<20 && tb.Get(1) != nil; sent += len(chunk) {
		e.remote(chunk)
	}
	if tb.Get(1) != nil || !strings.Contains(l.reason(), errOverWindow) {
		t.Fatalf("open %v, reason %q", tb.Get(1) != nil, l.reason())
	}
}

// What the connection cannot take closes the tunnel: the stream cannot go
// on with a hole in it.
func TestRemoteSendFails(t *testing.T) {
	tb, l, e, _ := endOn(t)
	_ = e.Conn().Close()
	e.remote([]byte("after"))
	if tb.Get(1) != nil || l.reason() == "" {
		t.Fatalf("open %v, reason %q", tb.Get(1) != nil, l.reason())
	}
}

// ackingLink is a link whose other end acknowledges each data frame before
// SendTunnel returns, as the goroutine that reads a real link can while
// the end is still sending.
type ackingLink struct {
	closeLink
	e *Endpoint
}

func (l *ackingLink) SendTunnel(m proto.Method, id uint64, payload []byte) error {
	if m == proto.MethodTunnelData {
		l.e.acked(int64(len(payload)))
	}
	return l.closeLink.SendTunnel(m, id, payload)
}

// An acknowledgement that comes back while the end is still sending what
// it acknowledges is of what was sent: the tunnel stays open, and the
// window is whole again once everything is acknowledged.
func TestAckBeforeSendReturns(t *testing.T) {
	tb, l := NewTable(nil), &ackingLink{}
	e := New(tb, l, 1, true)
	l.e = e
	_ = tb.Add(e)
	e.local(make([]byte, 3*proto.MaxChunk+1))
	if tb.Get(1) == nil || e.window != proto.Window {
		t.Fatalf("open %v, window %d: %s", tb.Get(1) != nil, e.window, l.reason())
	}
}
