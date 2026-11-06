package ws

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// recorder is a transport.Handler that keeps what arrives, and echoes it
// when echo is set.
type recorder struct {
	c      transport.Conn
	echo   bool
	mu     sync.Mutex
	got    []byte
	closed chan error
}

func newRecorder(c transport.Conn, echo bool) *recorder {
	return &recorder{c: c, echo: echo, closed: make(chan error, 1)}
}

func (r *recorder) OnData(data []byte) {
	if r.echo {
		_ = r.c.Send(append([]byte(nil), data...))
		return
	}
	r.mu.Lock()
	r.got = append(r.got, data...)
	r.mu.Unlock()
}

func (r *recorder) OnClose(err error) { r.closed <- err }

func (r *recorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func (r *recorder) bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.got...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitClosed(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the link did not close")
		return nil
	}
}

// listen serves links on a ws:// listener at path, echoing what they get
// when echo is set; it returns the listener and the server's links.
func listen(t *testing.T, path string, echo bool) (transport.Listener, <-chan *recorder) {
	t.Helper()
	srv := make(chan *recorder, 4)
	l, err := transport.Listen(tunneltest.Engine(t), "ws://127.0.0.1:0"+path, transport.ListenOptions{}, func(c transport.Conn) transport.Handler {
		r := newRecorder(c, echo)
		srv <- r
		return r
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, srv
}

// Sends of every size, up to several frames' worth, echo whole and in
// order.
func TestEcho(t *testing.T) {
	l, _ := listen(t, "/link", true)
	if !strings.HasPrefix(l.URL(), "ws://127.0.0.1:") || !strings.HasSuffix(l.URL(), "/link") {
		t.Fatalf("URL %q", l.URL())
	}
	var r *recorder
	c, err := transport.Dial(tunneltest.Engine(t), l.URL(), 5*time.Second, transport.ClientOptions{}, func(c transport.Conn) transport.Handler {
		r = newRecorder(c, false)
		return r
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var want []byte
	for i, n := range []int{1, 125, 126, 0xffff, 0x10000, maxFrame + 3} {
		b := bytes.Repeat([]byte{byte('a' + i)}, n)
		want = append(want, b...)
		if err := c.Send(append([]byte(nil), b...)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the echo", func() bool { return r.len() >= len(want) })
	if !bytes.Equal(r.bytes(), want) {
		t.Fatal("the echo differs")
	}
	c.CloseAfterSend()
	waitClosed(t, r.closed)
	// Nothing is sent after the close.
	if err := c.Send([]byte("x")); err == nil {
		t.Error("a send after the close went out")
	}
}

func TestBadURLs(t *testing.T) {
	e := tunneltest.Engine(t)
	open := func(c transport.Conn) transport.Handler { return newRecorder(c, false) }
	for _, addr := range []string{"wss://127.0.0.1:1/", "ws://%zz"} {
		if _, err := transport.Dial(e, addr, time.Second, transport.ClientOptions{}, open); err == nil {
			t.Errorf("dialed %q", addr)
		}
		if _, err := transport.Listen(e, addr, transport.ListenOptions{}, open); err == nil {
			t.Errorf("listened on %q", addr)
		}
	}
}

// rawDial opens a TCP connection to a ws:// listener's URL.
func rawDial(t *testing.T, l transport.Listener) net.Conn {
	t.Helper()
	hostport := strings.TrimPrefix(l.URL(), "ws://")
	hostport, _, _ = strings.Cut(hostport, "/")
	c, err := net.Dial("tcp", hostport)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	return c
}

const upgrade = "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n"

// The server turns away handshakes it cannot take, with a status.
func TestServerRefuses(t *testing.T) {
	l, _ := listen(t, "/link", false)
	for _, tc := range []struct {
		name, req string
		status    int
	}{
		{"garbage", "NOT HTTP\r\n\r\n", stdhttp.StatusBadRequest},
		{"wrong path", "GET /other HTTP/1.1\r\nHost: x\r\n" + upgrade + "\r\n", stdhttp.StatusNotFound},
		{"not an upgrade", "GET /link HTTP/1.1\r\nHost: x\r\n\r\n", stdhttp.StatusBadRequest},
		{"post", "POST /link HTTP/1.1\r\nHost: x\r\n" + upgrade + "\r\n", stdhttp.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := rawDial(t, l)
			if _, err := c.Write([]byte(tc.req)); err != nil {
				t.Fatal(err)
			}
			rsp, err := stdhttp.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatal(err)
			}
			if rsp.StatusCode != tc.status {
				t.Errorf("status %d", rsp.StatusCode)
			}
		})
	}
}

// A handshake head past maxHandshake closes the connection.
func TestServerHandshakeTooLong(t *testing.T) {
	l, _ := listen(t, "", false)
	c := rawDial(t, l)
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nX: " + strings.Repeat("a", maxHandshake+1)))
	if _, err := io.ReadAll(c); err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("the connection stayed open")
		}
	}
}

// frame is a final client frame of op with payload, masked as a client's
// must be.
func frame(op websocket.Opcode, payload []byte) []byte {
	head := frameHead(op, len(payload), true)
	b := append(head, payload...)
	mask(b[len(head):], head[len(head)-4:])
	return b
}

// readFrame reads a server frame off r.
func readFrame(t *testing.T, r io.Reader) (websocket.Opcode, []byte) {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		t.Fatal(err)
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		_, _ = io.ReadFull(r, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		_, _ = io.ReadFull(r, b[:])
		n = int(binary.BigEndian.Uint64(b[:]))
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		t.Fatal(err)
	}
	return websocket.Opcode(h[0] & 0x0f), p
}

// A client that sends its first frames with its handshake, pings and
// closes is answered as a WebSocket client should be.
func TestServerControlFrames(t *testing.T) {
	l, srv := listen(t, "", false)
	c := rawDial(t, l)
	req := "GET /any HTTP/1.1\r\nHost: x\r\n" + upgrade + "\r\n"
	if _, err := c.Write(append([]byte(req), frame(websocket.Binary, []byte("early"))...)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	rsp, err := stdhttp.ReadResponse(br, nil)
	if err != nil || rsp.StatusCode != stdhttp.StatusSwitchingProtocols || rsp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("handshake: %v, %v", rsp, err)
	}
	r := <-srv
	waitFor(t, "the early frame", func() bool { return string(r.bytes()) == "early" })
	_, _ = c.Write(frame(websocket.Ping, []byte("hi")))
	if op, p := readFrame(t, br); op != websocket.Pong || string(p) != "hi" {
		t.Fatalf("answer to a ping: %v %q", op, p)
	}
	_, _ = c.Write(frame(websocket.Close, []byte{0x03, 0xe8}))
	if op, _ := readFrame(t, br); op != websocket.Close {
		t.Fatalf("answer to a close: %v", op)
	}
	waitClosed(t, r.closed)
}

// A frame that does not parse closes the link.
func TestServerBadFrame(t *testing.T) {
	l, srv := listen(t, "", false)
	c := rawDial(t, l)
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n" + upgrade + "\r\n"))
	br := bufio.NewReader(c)
	if _, err := stdhttp.ReadResponse(br, nil); err != nil {
		t.Fatal(err)
	}
	r := <-srv
	// An unmasked frame from a client breaks the protocol.
	_, _ = c.Write(frameHead(websocket.Binary, 0, false))
	if err := waitClosed(t, r.closed); err == nil || !strings.Contains(err.Error(), "ws:") {
		t.Errorf("err %v", err)
	}
}

// A client that leaves before its handshake is done never reaches the
// wrapped handler.
func TestServerCloseBeforeOpen(t *testing.T) {
	l, srv := listen(t, "", false)
	c := rawDial(t, l)
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\n"))
	_ = c.Close()
	select {
	case <-srv:
		t.Fatal("a link opened without a handshake")
	case <-time.After(100 * time.Millisecond):
	}
}

// Events of a connection the server does not know are dropped.
func TestServerUnknownConn(t *testing.T) {
	s := NewServer(nil, "")
	s.OnData(nil, []byte("x"))
	s.OnClose(nil, nil)
	s.OnPriorityData(nil, nil)
	(&clientConn{}).OnPriorityData(nil, nil)
}

// rawServer answers each WebSocket handshake with what answer makes of the
// client's key, then does what after says with the connection.
func rawServer(t *testing.T, answer func(key string) string, after func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				br := bufio.NewReader(c)
				req, err := stdhttp.ReadRequest(br)
				if err != nil {
					return
				}
				if a := answer(req.Header.Get("Sec-WebSocket-Key")); a != "" {
					if _, err := c.Write([]byte(a)); err != nil {
						return
					}
				}
				if after != nil {
					after(c)
				}
				_, _ = io.Copy(io.Discard, br)
			}()
		}
	}()
	return "ws://" + ln.Addr().String() + "/"
}

func switched(key string) string {
	return "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
}

func dialRaw(t *testing.T, url string, timeout time.Duration) (transport.Conn, *recorder, error) {
	t.Helper()
	var r *recorder
	c, err := transport.Dial(tunneltest.Engine(t), url, timeout, transport.ClientOptions{}, func(c transport.Conn) transport.Handler {
		r = newRecorder(c, false)
		return r
	})
	if err == nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, r, err
}

// A dial fails on an answer that is not a switch to the WebSocket asked
// for.
func TestClientBadAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(string) string
		want   string
	}{
		{"refused", func(string) string { return "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n" }, "403"},
		{"wrong key", func(string) string { return switched("other") }, "bad websocket handshake"},
		{"garbage", func(string) string { return "garbage\r\n\r\n" }, "malformed"},
		{"too long", func(string) string {
			return "HTTP/1.1 101 Switching Protocols\r\nX: " + strings.Repeat("a", maxHandshake+1)
		}, "bad websocket handshake"},
		{"closed", func(string) string { return "" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := rawServer(t, tc.answer, func(c net.Conn) {
				if tc.name == "closed" {
					_ = c.Close()
				}
			})
			_, _, err := dialRaw(t, url, 5*time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %v", err)
			}
		})
	}
}

// A server that never answers the handshake fails the dial at its timeout.
func TestClientHandshakeTimeout(t *testing.T) {
	url := rawServer(t, func(string) string { return "" }, nil)
	start := time.Now()
	_, _, err := dialRaw(t, url, 200*time.Millisecond)
	if !errors.Is(err, errTimeout) {
		t.Errorf("err %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the dial took %v", d)
	}
}

func TestClientDialRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "ws://" + ln.Addr().String() + "/"
	_ = ln.Close()
	if _, _, err := dialRaw(t, url, 2*time.Second); err == nil {
		t.Error("a dial to a closed port worked")
	}
}

// A server's frames that come with its answer, its pings and its close
// reach the client as they should; a dial without a timeout or a port takes
// the defaults.
func TestClientControlFrames(t *testing.T) {
	got := make(chan []byte, 2)
	url := rawServer(t, func(key string) string {
		return switched(key) + string(serverFrame(websocket.Binary, []byte("early")))
	}, func(c net.Conn) {
		_, _ = c.Write(serverFrame(websocket.Ping, []byte("hi")))
		br := bufio.NewReader(c)
		_, p := readClientFrame(br)
		got <- p
		_, _ = c.Write(serverFrame(websocket.Close, []byte{0x03, 0xe8}))
		_, p = readClientFrame(br)
		got <- p
	})
	_, r, err := dialRaw(t, url, 0)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the early frame", func() bool { return string(r.bytes()) == "early" })
	if p := <-got; string(p) != "hi" {
		t.Errorf("pong %q", p)
	}
	if p := <-got; !bytes.Equal(p, []byte{0x03, 0xe8}) {
		t.Errorf("close %x", p)
	}
	waitClosed(t, r.closed)
}

// A ws:// URL without a port takes port 80.
func TestDialerDefaultPort(t *testing.T) {
	if _, err := Dialer(tunneltest.Engine(t), "ws://127.0.0.1/x", time.Second, nil); err != nil {
		t.Fatal(err)
	}
	// Dialer is for ws:// only, and a web proxy's URL may have a query.
	for _, bad := range []string{"wss://127.0.0.1/x", "ws://%zz/x"} {
		if _, err := Dialer(tunneltest.Engine(t), bad, time.Second, nil); err == nil {
			t.Errorf("Dialer(%q)", bad)
		}
	}
	if _, err := Dialer(tunneltest.Engine(t), "ws://127.0.0.1/x?a=b", time.Second, nil); err != nil {
		t.Fatal(err)
	}
}

func serverFrame(op websocket.Opcode, payload []byte) []byte {
	return append(frameHead(op, len(payload), false), payload...)
}

// readClientFrame reads a masked frame and unmasks it.
func readClientFrame(br *bufio.Reader) (websocket.Opcode, []byte) {
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return 0, nil
	}
	n := int(h[1] & 0x7f)
	var key [4]byte
	_, _ = io.ReadFull(br, key[:])
	p := make([]byte, n)
	_, _ = io.ReadFull(br, p)
	mask(p, key[:])
	return websocket.Opcode(h[0] & 0x0f), p
}

var _ fib.Handler = (*Server)(nil)

// capture is a fib.Handler that hands over the connection it opens.
type capture chan *fib.Connection

func (c capture) OnOpen(conn *fib.Connection)          { c <- conn }
func (capture) OnData(*fib.Connection, []byte)         {}
func (capture) OnPriorityData(*fib.Connection, []byte) {}
func (capture) OnClose(*fib.Connection, error)         {}

// A send of two parts goes out as one frame; nothing goes out after a
// close frame, nor on a connection that closed.
func TestFramesSend(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	opened := make(capture, 1)
	if err := tunneltest.Engine(t).DialWithHandler("tcp", ln.Addr().String(), 5*time.Second, opened, func(*fib.Connection, error) {}); err != nil {
		t.Fatal(err)
	}
	peer, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(10 * time.Second))
	f := &frames{c: <-opened}
	if err := f.Send([]byte("ab"), []byte("cd")); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(peer)
	if op, p := readFrame(t, br); op != websocket.Binary || string(p) != "abcd" {
		t.Fatalf("frame %v %q", op, p)
	}
	f.sendControl(websocket.Close, nil)
	f.sendControl(websocket.Ping, nil)
	if op, _ := readFrame(t, br); op != websocket.Close {
		t.Fatalf("frame %v", op)
	}
	if err := f.Send([]byte("x"), nil); !errors.Is(err, net.ErrClosed) {
		t.Errorf("send after a close frame: %v", err)
	}
	f.closed = false
	_ = f.c.Close()
	waitFor(t, "the send to fail", func() bool { return f.Send([]byte("x"), nil) != nil })
}
