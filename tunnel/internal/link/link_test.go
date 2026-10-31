package link

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/tunnel/internal/bodycrypt"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// pair is two links joined by a TCP connection of a fib engine's.
func pair(t *testing.T, ra, rb *Router, ca, cb *bodycrypt.Coder) (*Link, *Link) {
	t.Helper()
	e := tunneltest.Engine(t)
	bs := make(chan *Link, 1)
	l, err := transport.Listen(e, "127.0.0.1:0", transport.ListenOptions{}, func(c transport.Conn) transport.Handler {
		b := New(c, rb, cb)
		bs <- b
		return b
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var a *Link
	if _, err := transport.Dial(e, l.URL(), 5*time.Second, func(c transport.Conn) transport.Handler { a = New(c, ra, ca); return a }); err != nil {
		t.Fatal(err)
	}
	b := <-bs
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func coder(t *testing.T, password string) *bodycrypt.Coder {
	t.Helper()
	c, err := bodycrypt.New(password)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func echoRouter(got chan<- []byte) *Router {
	r := NewRouter()
	r.HandleCall(proto.MethodPing, func(_ *Link, body []byte) (any, error) {
		var req proto.PingReq
		if err := Decode(body, &req); err != nil {
			return nil, err
		}
		return &proto.PingRsp{Nodes: map[string]proto.NodeLatency{"x": {RTT: req.RTT}}}, nil
	}, true)
	r.HandleCall(proto.MethodUpdate, func(*Link, []byte) (any, error) { return nil, errors.New("no, thanks") }, false)
	r.HandleNotify(proto.MethodTunnelData, func(_ *Link, body []byte) { got <- append([]byte(nil), body...) }, true)
	return r
}

func TestLink(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ca, cb *bodycrypt.Coder
	}{{"plain", nil, nil}, {"sealed", coder(t, "k"), coder(t, "k")}} {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan []byte, 4)
			a, _ := pair(t, NewRouter(), echoRouter(got), tc.ca, tc.cb)
			var rsp proto.PingRsp
			if err := a.Call(proto.MethodPing, &proto.PingReq{}, &rsp, time.Second); err != nil || rsp.Nodes["x"].RTT.Samples != 0 || len(rsp.Nodes) != 1 {
				t.Fatalf("ping: %+v, %v", rsp, err)
			}
			if err := a.Call(proto.MethodUpdate, nil, nil, time.Second); err == nil || err.Error() != "no, thanks" {
				t.Fatalf("update: %v", err)
			}
			if err := a.Call(proto.MethodNodes, nil, nil, time.Second); err == nil || !strings.Contains(err.Error(), "no method nodes") {
				t.Fatalf("nodes: %v", err)
			}
			payload := bytes.Repeat([]byte("ab"), 40000)
			if err := a.SendTunnel(proto.MethodTunnelData, 42, payload); err != nil {
				t.Fatal(err)
			}
			body := <-got
			id, p, err := proto.ParseFrame(body)
			if err != nil || id != 42 || !bytes.Equal(p, payload) {
				t.Fatalf("tunnel frame: %d, %d bytes, %v", id, len(p), err)
			}
		})
	}
}

// Links whose body passwords differ, or of which one has none, close.
func TestLinkMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ca, cb *bodycrypt.Coder
	}{{"other password", coder(t, "a"), coder(t, "b")}, {"caller has none", nil, coder(t, "b")}, {"callee has none", coder(t, "a"), nil}} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := pair(t, NewRouter(), echoRouter(make(chan []byte, 1)), tc.ca, tc.cb)
			if err := a.Call(proto.MethodPing, &proto.PingReq{}, nil, 2*time.Second); !errors.Is(err, ErrClosed) {
				t.Fatalf("call: %v", err)
			}
			<-b.Done()
		})
	}
}

func TestLinkTimeoutAndClose(t *testing.T) {
	r := NewRouter()
	release := make(chan struct{})
	r.HandleCall(proto.MethodPing, func(*Link, []byte) (any, error) { <-release; return nil, nil }, false)
	a, _ := pair(t, NewRouter(), r, nil, nil)
	defer close(release)
	if err := a.Call(proto.MethodPing, nil, nil, 50*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("call: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Call(proto.MethodPing, nil, nil, 5*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	a.Close()
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("call cut short: %v", err)
	}
}

// Frames that arrive a byte at a time, or several in one read, parse.
func TestLinkSplitFrames(t *testing.T) {
	got := make(chan []byte, 8)
	r := NewRouter()
	r.HandleNotify(proto.MethodTunnelData, func(_ *Link, body []byte) { got <- append([]byte(nil), body...) }, true)
	sink := &sinkConn{}
	l := New(sink, r, nil)
	sender := New(sink, NewRouter(), nil)
	for i := range 3 {
		_ = sender.SendTunnel(proto.MethodTunnelData, uint64(i), []byte("hello"))
	}
	wire := bytes.Join(sink.sent, nil)
	for i := range wire[:len(wire)/2] {
		l.OnData(wire[i : i+1])
	}
	l.OnData(wire[len(wire)/2:])
	for i := range 3 {
		id, p, _ := proto.ParseFrame(<-got)
		if id != uint64(i) || string(p) != "hello" {
			t.Fatalf("frame %d: %d %q", i, id, p)
		}
	}
}

type sinkConn struct{ sent [][]byte }

func (s *sinkConn) Send(b []byte) error  { s.sent = append(s.sent, b); return nil }
func (s *sinkConn) Close() error         { return nil }
func (s *sinkConn) CloseAfterSend()      {}
func (s *sinkConn) LocalAddr() net.Addr  { return nil }
func (s *sinkConn) RemoteAddr() net.Addr { return nil }

// A frame past the link's limit closes it, as an unregistered link's
// longer than a registration would.
func TestLinkMaxFrame(t *testing.T) {
	a, b := pair(t, NewRouter(), echoRouter(make(chan []byte, 1)), nil, nil)
	b.SetMaxFrame(1024)
	if err := a.SendTunnel(proto.MethodTunnelData, 1, make([]byte, 2048)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the link took a frame past its limit")
	}
	if !strings.Contains(b.Err().Error(), "a frame of") {
		t.Errorf("err %v", b.Err())
	}
}

// Calls past MaxAsync of a link at once are answered ErrBusy, rather than
// taking a goroutine each.
func TestLinkMaxAsync(t *testing.T) {
	r := NewRouter()
	release := make(chan struct{})
	started := make(chan struct{}, MaxAsync)
	r.HandleCall(proto.MethodUpdate, func(*Link, []byte) (any, error) { started <- struct{}{}; <-release; return nil, nil }, false)
	a, _ := pair(t, NewRouter(), r, nil, nil)
	defer close(release)
	for range MaxAsync {
		go func() { _ = a.Call(proto.MethodUpdate, nil, nil, 10*time.Second) }()
	}
	for range MaxAsync {
		<-started
	}
	if err := a.Call(proto.MethodUpdate, nil, nil, 5*time.Second); err == nil || !strings.Contains(err.Error(), ErrBusy.Error()) {
		t.Fatalf("call past the limit: %v", err)
	}
}
