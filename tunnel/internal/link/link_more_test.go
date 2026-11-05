package link

import (
	"encoding/binary"
	"errors"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/tunnel/internal/proto"
)

// loopConn is a transport.Conn that hands what is sent to peer, a frame
// at a time, as if the peer read it off the wire; err, when set, fails each
// Send.
type loopConn struct {
	// rx makes the peer's OnData run one call at a time.
	rx     sync.Mutex
	mu     sync.Mutex
	peer   *Link
	err    error
	closed int
	after  int
}

func (c *loopConn) Send(b []byte) error {
	c.mu.Lock()
	peer, err := c.peer, c.err
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if peer != nil {
		go c.feed(b)
	}
	return nil
}

// feed hands b to the peer, as if it arrived.
func (c *loopConn) feed(b []byte) {
	c.rx.Lock()
	defer c.rx.Unlock()
	c.peer.OnData(b)
}

func (c *loopConn) Close() error {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
	return nil
}

func (c *loopConn) CloseAfterSend() {
	c.mu.Lock()
	c.after++
	c.mu.Unlock()
}

func (c *loopConn) LocalAddr() net.Addr  { return nil }
func (c *loopConn) RemoteAddr() net.Addr { return nil }

// rawFrame is a frame of kind, m and id with body, unsealed.
func rawFrame(kind byte, m proto.Method, id uint32, body []byte) []byte {
	b := make([]byte, LengthSize+HeaderSize, LengthSize+HeaderSize+len(body))
	binary.BigEndian.PutUint32(b, uint32(HeaderSize+len(body)))
	b[LengthSize] = kind
	b[LengthSize+1] = byte(m)
	binary.BigEndian.PutUint32(b[LengthSize+2:], id)
	return append(b, body...)
}

func TestLinkAccessors(t *testing.T) {
	c := &loopConn{}
	l := New(c, NewRouter(), nil)
	if l.Conn() != c {
		t.Error("Conn is not the link's connection")
	}
	if a := l.RemoteAddr(); a != "" {
		t.Errorf("RemoteAddr of a connection without one: %q", a)
	}
	l.SetValue(7)
	if v := l.Value(); v != 7 {
		t.Errorf("Value %v", v)
	}
	l.CloseAfterSend()
	if c.after != 1 {
		t.Error("CloseAfterSend did not reach the connection")
	}
	l.SetMaxFrame(2 * MaxFrame)
	if n := l.maxFrame.Load(); n != MaxFrame {
		t.Errorf("SetMaxFrame past MaxFrame: %d", n)
	}
}

func TestEncodeDecode(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{{nil, ""}, {[]byte("ab"), "ab"}, {"cd", "cd"}, {map[string]int{"x": 1}, `{"x":1}`}} {
		b, err := Encode(tc.in)
		if err != nil || string(b) != tc.want {
			t.Errorf("Encode(%v) = %q, %v", tc.in, b, err)
		}
	}
	if _, err := Encode(func() {}); err == nil {
		t.Error("Encode took a func")
	}
	body := []byte("xyz")
	var bs []byte
	if err := Decode(body, &bs); err != nil || string(bs) != "xyz" {
		t.Errorf("Decode to *[]byte: %q, %v", bs, err)
	}
	body[0] = 'q'
	if string(bs) != "xyz" {
		t.Error("Decode to *[]byte did not copy")
	}
	var s string
	if err := Decode([]byte("str"), &s); err != nil || s != "str" {
		t.Errorf("Decode to *string: %q, %v", s, err)
	}
	if err := Decode([]byte("anything"), nil); err != nil {
		t.Errorf("Decode to nil: %v", err)
	}
	m := map[string]int{"keep": 1}
	if err := Decode(nil, &m); err != nil || m["keep"] != 1 {
		t.Errorf("Decode of an empty body: %v, %v", m, err)
	}
	if err := Decode([]byte("{"), &m); err == nil {
		t.Error("Decode took bad JSON")
	}
}

// What cannot be sent fails the call, the notify or the frame, without
// waiting.
func TestLinkSendErrors(t *testing.T) {
	c := &loopConn{err: errors.New("down")}
	l := New(c, NewRouter(), nil)
	if err := l.Call(proto.MethodPing, func() {}, nil, time.Second); err == nil {
		t.Error("a call whose request does not encode went out")
	}
	if err := l.Notify(proto.MethodPing, func() {}); err == nil {
		t.Error("a notify whose body does not encode went out")
	}
	if err := l.Call(proto.MethodPing, nil, nil, time.Second); err == nil || err.Error() != "down" || !errors.Is(err, ErrClosed) || errors.Unwrap(err) != c.err {
		t.Errorf("a call the connection refused: %v, want down, as ErrClosed, wrapping the connection's error", err)
	}
	l.mu.Lock()
	n := len(l.pending)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("%d calls left pending", n)
	}
	// A call too long for a frame is that, not a link gone.
	if err := New(&loopConn{}, NewRouter(), nil).Call(proto.MethodPing, make([]byte, MaxFrame), nil, time.Second); err == nil ||
		!strings.Contains(err.Error(), "longer than") || errors.Is(err, ErrClosed) {
		t.Errorf("a call past MaxFrame: %v", err)
	}
	if err := l.SendTunnel(proto.MethodTunnelData, 1, make([]byte, MaxFrame)); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("a frame past MaxFrame: %v", err)
	}
	if err := l.Notify(proto.MethodPing, "x"); err == nil {
		t.Error("a notify the connection refused went out")
	}
}

// A call on a closed link fails at once, with why it closed.
func TestLinkCallClosed(t *testing.T) {
	l := New(&loopConn{}, NewRouter(), nil)
	l.OnClose(nil)
	if err := l.Call(proto.MethodPing, nil, nil, time.Second); !errors.Is(err, ErrClosed) || err != ErrClosed {
		t.Errorf("call on a link closed plainly: %v", err)
	}
	// A second OnClose changes nothing.
	l.OnClose(errors.New("later"))
	if l.Err() != nil {
		t.Errorf("err %v", l.Err())
	}

	l = New(&loopConn{}, NewRouter(), nil)
	l.OnClose(errors.New("cut"))
	if err := l.Call(proto.MethodPing, nil, nil, time.Second); !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), "cut") {
		t.Errorf("call on a link closed for an error: %v", err)
	}
}

// Call IDs skip 0 as the sequence wraps.
func TestLinkCallIDWraps(t *testing.T) {
	ca, cb := &loopConn{}, &loopConn{}
	r := NewRouter()
	r.HandleCall(proto.MethodPing, func(*Link, []byte) (any, error) { return "pong", nil }, true)
	a, b := New(ca, NewRouter(), nil), New(cb, r, nil)
	ca.peer, cb.peer = b, a
	a.seq = math.MaxUint32
	var s string
	if err := a.Call(proto.MethodPing, nil, &s, 5*time.Second); err != nil || s != "pong" {
		t.Fatalf("call: %q, %v", s, err)
	}
	if a.seq != 1 {
		t.Errorf("the call after the last ID took %d", a.seq)
	}
}

// The guard fails calls and drops notifies it refuses.
func TestLinkGuard(t *testing.T) {
	ca, cb := &loopConn{}, &loopConn{}
	r := NewRouter()
	served := make(chan struct{}, 1)
	r.HandleCall(proto.MethodPing, func(*Link, []byte) (any, error) { return nil, nil }, true)
	r.HandleNotify(proto.MethodKick, func(*Link, []byte) { served <- struct{}{} }, true)
	r.Guard = func(_ *Link, m proto.Method) error { return errors.New("not yet") }
	a, b := New(ca, NewRouter(), nil), New(cb, r, nil)
	ca.peer, cb.peer = b, a
	if err := a.Call(proto.MethodPing, nil, nil, 5*time.Second); err == nil || err.Error() != "not yet" {
		t.Errorf("guarded call: %v", err)
	}
	ca.feed(rawFrame(kindNotify, proto.MethodKick, 0, nil))
	select {
	case <-served:
		t.Error("a guarded notify was served")
	default:
	}
}

// Notifies run on goroutines of their own unless inline, at most MaxAsync
// at once; one of a method not served is dropped.
func TestLinkAsyncNotify(t *testing.T) {
	r := NewRouter()
	release := make(chan struct{})
	started := make(chan []byte, MaxAsync+1)
	r.HandleNotify(proto.MethodKick, func(_ *Link, body []byte) { started <- body; <-release }, false)
	l := New(&loopConn{}, r, nil)
	defer close(release)
	l.OnData(rawFrame(kindNotify, proto.MethodNodes, 0, nil))
	frame := rawFrame(kindNotify, proto.MethodKick, 0, []byte("k"))
	for range MaxAsync {
		l.OnData(frame)
	}
	for range MaxAsync {
		if b := <-started; string(b) != "k" {
			t.Fatalf("body %q", b)
		}
	}
	l.OnData(frame)
	if n := l.async.Load(); n != MaxAsync {
		t.Errorf("%d notifies running", n)
	}
	select {
	case <-started:
		t.Error("a notify past MaxAsync ran")
	default:
	}
}

// A frame of an unknown kind closes the link, and what arrives after is
// dropped.
func TestLinkBadKind(t *testing.T) {
	c := &loopConn{}
	served := 0
	r := NewRouter()
	r.HandleNotify(proto.MethodKick, func(*Link, []byte) { served++ }, true)
	l := New(c, r, nil)
	l.OnData(rawFrame(9, proto.MethodKick, 0, nil))
	if c.closed != 1 || l.Err() == nil || !strings.Contains(l.Err().Error(), "kind 9") {
		t.Fatalf("closed %d, err %v", c.closed, l.Err())
	}
	l.OnData(rawFrame(kindNotify, proto.MethodKick, 0, nil))
	if served != 0 {
		t.Error("a frame after the link failed was served")
	}
	// A failure after the first, or after the link closed, changes nothing.
	l.fail(errors.New("again"))
	l.OnClose(nil)
	l.fail(errors.New("closed"))
	if c.closed != 1 || !strings.Contains(l.Err().Error(), "kind 9") {
		t.Errorf("closed %d, err %v", c.closed, l.Err())
	}
}

// A buffer grown past 1 MiB for a long frame is let go once the frame is
// served.
func TestLinkLongFrameBuffer(t *testing.T) {
	got := 0
	r := NewRouter()
	r.HandleNotify(proto.MethodTunnelData, func(_ *Link, body []byte) { got = len(body) }, true)
	l := New(&loopConn{}, r, nil)
	f := rawFrame(kindNotify, proto.MethodTunnelData, 0, make([]byte, 2<<20))
	l.OnData(f[:1000])
	l.OnData(f[1000:])
	if got != 2<<20 {
		t.Fatalf("got %d bytes", got)
	}
	if l.in != nil {
		t.Errorf("kept a buffer of %d bytes", cap(l.in))
	}
}

// A reply to a call no longer waiting is dropped.
func TestLinkStrayReply(t *testing.T) {
	l := New(&loopConn{}, NewRouter(), nil)
	l.OnData(rawFrame(kindReply, proto.MethodPing, 99, []byte("late")))
	l.OnData(rawFrame(kindError, proto.MethodPing, 98, []byte("late")))
	if l.Err() != nil {
		t.Errorf("err %v", l.Err())
	}
}

// recConn is a transport.Conn that keeps what is sent.
type recConn struct {
	loopConn
	sentMu sync.Mutex
	sent   [][]byte
}

func (c *recConn) Send(b []byte) error {
	c.sentMu.Lock()
	c.sent = append(c.sent, append([]byte(nil), b...))
	c.sentMu.Unlock()
	return nil
}

func (c *recConn) frames() [][]byte {
	c.sentMu.Lock()
	defer c.sentMu.Unlock()
	return append([][]byte(nil), c.sent...)
}

// A checked call's answer is checked before the frames after it are
// served: one the check refuses fails the call, closes the link, and
// nothing after it is served; one it takes lets them be.
func TestLinkCallChecked(t *testing.T) {
	for _, refuse := range []bool{true, false} {
		r := NewRouter()
		served := make(chan string, 4)
		r.HandleNotify(proto.MethodKick, func(_ *Link, body []byte) { served <- string(body) }, true)
		c := &recConn{}
		l := New(c, r, nil)
		var checked []byte
		done := make(chan error, 1)
		go func() {
			var got string
			done <- l.CallChecked(proto.MethodPing, "q", &got, 5*time.Second, func(body []byte) error {
				checked = append([]byte(nil), body...)
				// The frames after the answer wait for the check.
				select {
				case s := <-served:
					t.Errorf("%q served before the check", s)
				default:
				}
				if refuse {
					return errors.New("bad proof")
				}
				return nil
			})
		}()
		waitSent(t, c, 1)
		// The answer and a notify after it, read at once.
		in := append(rawFrame(kindReply, proto.MethodPing, 1, []byte("answer")), rawFrame(kindNotify, proto.MethodKick, 0, []byte("after"))...)
		l.OnData(in)
		err := <-done
		if string(checked) != "answer" {
			t.Errorf("checked %q", checked)
		}
		if refuse {
			if err == nil || err.Error() != "bad proof" {
				t.Errorf("refused: %v", err)
			}
			// What arrives afterwards is not served either.
			l.OnData(rawFrame(kindNotify, proto.MethodKick, 0, []byte("later")))
			select {
			case s := <-served:
				t.Errorf("%q served after a refused answer", s)
			default:
			}
			if c.closed != 1 || !strings.Contains(l.Err().Error(), "bad proof") {
				t.Errorf("closed %d, err %v", c.closed, l.Err())
			}
			continue
		}
		if err != nil {
			t.Errorf("taken: %v", err)
		}
		if s := <-served; s != "after" {
			t.Errorf("served %q", s)
		}
	}
}

// waitSent waits for c to have sent n frames.
func waitSent(t *testing.T, c *recConn, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(c.frames()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("sent %d frames, not %d", len(c.frames()), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// A Then's After runs once its answer is sent, inline or not, and after an
// error answer too.
func TestLinkThen(t *testing.T) {
	for _, inline := range []bool{true, false} {
		r := NewRouter()
		c := &recConn{}
		after := make(chan int, 2)
		r.HandleCall(proto.MethodPing, func(l *Link, body []byte) (any, error) {
			then := &Then{Rsp: "r", After: func() { after <- len(c.frames()) }}
			if string(body) == "fail" {
				return then, errors.New("no")
			}
			return then, nil
		}, inline)
		l := New(c, r, nil)
		l.OnData(rawFrame(kindCall, proto.MethodPing, 1, nil))
		if n := <-after; n != 1 {
			t.Errorf("inline %v: After ran with %d frames sent", inline, n)
		}
		f := c.frames()[0]
		if f[LengthSize] != kindReply || string(f[LengthSize+HeaderSize:]) != "r" {
			t.Errorf("inline %v: answer %q", inline, f)
		}
		l.OnData(rawFrame(kindCall, proto.MethodPing, 2, []byte("fail")))
		if n := <-after; n != 2 {
			t.Errorf("inline %v: After of a failure ran with %d frames sent", inline, n)
		}
		if f := c.frames()[1]; f[LengthSize] != kindError || string(f[LengthSize+HeaderSize:]) != "no" {
			t.Errorf("inline %v: failure %q", inline, f)
		}
	}
}
