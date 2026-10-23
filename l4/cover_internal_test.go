package l4

import (
	"io"
	"net"
	"testing"
	"time"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/upstream"
)

// recorder is a handler that counts the callbacks it is given.
type recorder struct {
	priority, closed, hangups int
}

func (r *recorder) OnOpen(*fib.Connection)                 {}
func (r *recorder) OnData(*fib.Connection, []byte)         {}
func (r *recorder) OnPriorityData(*fib.Connection, []byte) { r.priority++ }
func (r *recorder) OnClose(*fib.Connection, error)         { r.closed++ }
func (r *recorder) OnHangup(*fib.Connection)               { r.hangups++ }

// plain is a handler without OnHangup.
type plain struct{ recorder }

func (p *plain) OnHangup() {}

// TestProxyHandlerCallbacks checks that the callbacks of a connection whose
// PROXY header is still being read stay with the proxy handler, and those
// of one past it go on to its handler.
func TestProxyHandlerCallbacks(t *testing.T) {
	r := &recorder{}
	p := AcceptProxy(nil, r).(*proxyHandler)
	c := &fib.Connection{}
	c.SetAttachment(&proxyRead{})
	p.OnPriorityData(c, nil)
	p.OnClose(c, nil)
	p.OnHangup(c)
	if *r != (recorder{}) {
		t.Fatalf("while reading the header: %+v", *r)
	}
	c.SetAttachment(nil)
	p.OnPriorityData(c, nil)
	p.OnClose(c, nil)
	p.OnHangup(c)
	if *r != (recorder{priority: 1, closed: 1, hangups: 1}) {
		t.Fatalf("past the header: %+v", *r)
	}
	// A handler that cannot hear of hangups is not told.
	AcceptProxy(nil, &plain{}).(*proxyHandler).OnHangup(c)
}

// opener hands each connection it opens to an entry.
type opener struct {
	recorder
	e       *Entry
	release func()
}

func (o *opener) OnOpen(c *fib.Connection) { o.e.Open(c, o.release, nil) }

// TestEntryWithoutService closes a connection an entry has no service for:
// it routes by no names and has no service without them.
func TestEntryWithoutService(t *testing.T) {
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	released := make(chan struct{})
	o := &opener{e: NewEntry(nil), release: func() { close(released) }}
	if err := engine.DialWithHandler("tcp", l.Addr().String(), 5*time.Second, o, func(*fib.Connection, error) {}); err != nil {
		t.Fatal(err)
	}
	peer, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := peer.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("read %d, %v; want the connection closed", n, err)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("not released")
	}
}
