package ws

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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

// Sends of every size, up to several messages' worth, echo whole and in
// order; both links know their addresses.
func TestEcho(t *testing.T) {
	l, srv := listen(t, "/link", true)
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
	if c.LocalAddr() == nil || !strings.Contains(l.URL(), c.RemoteAddr().String()) {
		t.Errorf("addrs %v %v", c.LocalAddr(), c.RemoteAddr())
	}
	if s := (<-srv).c; s.LocalAddr() == nil || s.LocalAddr().String() != c.RemoteAddr().String() || s.RemoteAddr().String() != c.LocalAddr().String() {
		t.Errorf("the accepted link's addrs %v %v", s.LocalAddr(), s.RemoteAddr())
	}
	var want []byte
	for i, n := range []int{0, 1, 125, 126, 0xffff, 0x10000, maxFrame + 3} {
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
	if err := waitClosed(t, r.closed); err != nil {
		t.Errorf("a plain close: %v", err)
	}
	// Nothing is sent after the close.
	if err := c.Send([]byte("x")); err == nil {
		t.Error("a send after the close went out")
	}
}

// Close ends a link at once; the other side learns it.
func TestAbort(t *testing.T) {
	l, srv := listen(t, "", false)
	var r *recorder
	c, err := transport.Dial(tunneltest.Engine(t), l.URL(), 5*time.Second, transport.ClientOptions{}, func(c transport.Conn) transport.Handler {
		r = newRecorder(c, false)
		return r
	})
	if err != nil {
		t.Fatal(err)
	}
	s := <-srv
	_ = s.c.Close()
	waitClosed(t, s.closed)
	waitClosed(t, r.closed)
	_ = c.Close()
}

// A link the listener's or the dialer's open refuses closes as it opens.
func TestOpenRefuses(t *testing.T) {
	e := tunneltest.Engine(t)
	l, err := transport.Listen(e, "ws://127.0.0.1:0/", transport.ListenOptions{}, func(transport.Conn) transport.Handler { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r := make(chan *recorder, 1)
	c, err := transport.Dial(e, l.URL(), 5*time.Second, transport.ClientOptions{}, func(c transport.Conn) transport.Handler {
		rec := newRecorder(c, false)
		r <- rec
		return rec
	})
	if err == nil {
		waitClosed(t, (<-r).closed)
		_ = c.Close()
	}

	l2, _ := listen(t, "", false)
	if _, err := transport.Dial(e, l2.URL(), 5*time.Second, transport.ClientOptions{}, func(transport.Conn) transport.Handler { return nil }); !errors.Is(err, transport.ErrClosedOpening) {
		t.Errorf("a link the dialer refused: %v", err)
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
	var tr wsTransport
	if _, err := tr.Dial(e, "http://127.0.0.1:1/", time.Second, transport.ClientOptions{}, open); err == nil {
		t.Error("dialed http://")
	}
	if _, err := tr.Listen(e, "http://127.0.0.1:0/", transport.ListenOptions{}, open); err == nil {
		t.Error("listened on http://")
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
		{"wrong path with a query", "GET /other?x=/link HTTP/1.1\r\nHost: x\r\n" + upgrade + "\r\n", stdhttp.StatusNotFound},
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

// A handshake head past MaxHandshake closes the connection.
func TestServerHandshakeTooLong(t *testing.T) {
	l, _ := listen(t, "", false)
	c := rawDial(t, l)
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nX: " + strings.Repeat("a", MaxHandshake+1)))
	if _, err := io.ReadAll(c); err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("the connection stayed open")
		}
	}
}

// A handshake that does not complete in time closes the connection; one
// that does, in pieces, does not.
func TestServerHandshakeTimeout(t *testing.T) {
	opened := make(chan *websocket.Connection, 1)
	cfg := websocket.DefaultConfig()
	const timeout = 200 * time.Millisecond
	cfg.HandshakeTimeout = timeout
	s := NewServer(cfg, websocket.HandlerFuncs{Open: func(c *websocket.Connection, _ *stdhttp.Request) { opened <- c }}, "")
	e := tunneltest.Engine(t)
	l, err := transport.ListenFib(e, "127.0.0.1:0", s, nil, func(hostport string) string { return "ws://" + hostport + "/" })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	slow := rawDial(t, l)
	_, _ = slow.Write([]byte("GET / HTTP/1.1\r\n"))
	start := time.Now()
	if _, err := io.ReadAll(slow); err != nil {
		t.Fatalf("the connection stayed open: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("closed after %v", d)
	}

	c := rawDial(t, l)
	req := "GET / HTTP/1.1\r\nHost: x\r\n" + upgrade + "\r\n"
	for _, piece := range []string{req[:len(req)-3], req[len(req)-3 : len(req)-1], req[len(req)-1:]} {
		_, _ = c.Write([]byte(piece))
		time.Sleep(20 * time.Millisecond)
	}
	if rsp, err := stdhttp.ReadResponse(bufio.NewReader(c), nil); err != nil || rsp.StatusCode != stdhttp.StatusSwitchingProtocols {
		t.Fatalf("%v, %v", rsp, err)
	}
	ws := <-opened
	time.Sleep(2 * timeout)
	if err := ws.WriteMessage(websocket.Binary, []byte("still open")); err != nil {
		t.Fatalf("the WebSocket closed: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, p := readFrame(t, c); string(p) != "still open" {
		t.Fatalf("%q", p)
	}
}

// A client that sends its first frames with its handshake, pings and
// closes is answered as a WebSocket client should be.
func TestServerControlFrames(t *testing.T) {
	l, srv := listen(t, "", false)
	c := rawDial(t, l)
	req := "GET /any HTTP/1.1\r\nHost: x\r\n" + upgrade + "\r\n"
	if _, err := c.Write(append([]byte(req), clientFrame(websocket.Binary, []byte("early"))...)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	rsp, err := stdhttp.ReadResponse(br, nil)
	if err != nil || rsp.StatusCode != stdhttp.StatusSwitchingProtocols || rsp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("handshake: %v, %v", rsp, err)
	}
	r := <-srv
	waitFor(t, "the early frame", func() bool { return string(r.bytes()) == "early" })
	_, _ = c.Write(clientFrame(websocket.Ping, []byte("hi")))
	if op, p := readFrame(t, br); op != websocket.Pong || string(p) != "hi" {
		t.Fatalf("answer to a ping: %v %q", op, p)
	}
	_, _ = c.Write(clientFrame(websocket.Close, []byte{0x03, 0xe8}))
	if op, _ := readFrame(t, br); op != websocket.Close {
		t.Fatalf("answer to a close: %v", op)
	}
	if err := waitClosed(t, r.closed); err != nil {
		t.Errorf("a plain close: %v", err)
	}
}

// A frame that does not parse closes the link, with why.
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
	_, _ = c.Write(serverFrame(websocket.Binary, nil))
	if err := waitClosed(t, r.closed); err == nil || !strings.Contains(err.Error(), "ws: closed with 1002") {
		t.Errorf("err %v", err)
	}
}

// A client that leaves before its handshake is done never reaches the
// link.
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

// Events of a WebSocket the handlers do not know are dropped, and a
// WebSocket without a connection of its own is not readied.
func TestUnknownConn(t *testing.T) {
	l := &links{}
	l.OnMessage(&websocket.Connection{}, websocket.Binary, []byte("x"))
	l.OnClose(&websocket.Connection{}, 1000, "", nil)
	d := &dialed{c: &conn{}}
	d.OnMessage(nil, websocket.Binary, []byte("x"))
	d.OnClose(nil, 1000, "", nil)
	prepareLink(&websocket.Connection{})
}

func TestCloseErr(t *testing.T) {
	cut := errors.New("cut")
	for _, tc := range []struct {
		code uint16
		err  error
		want string
	}{
		{1000, nil, ""},
		{1001, nil, ""},
		{1005, nil, ""},
		{1006, nil, ""},
		{1006, cut, "cut"},
		{1002, cut, "cut"},
		{1009, nil, "ws: closed with 1009 too big"},
	} {
		got := closeErr(tc.code, "too big", tc.err)
		if tc.want == "" && got != nil || tc.want != "" && (got == nil || got.Error() != tc.want) {
			t.Errorf("closeErr(%d, %v) = %v", tc.code, tc.err, got)
		}
	}
}

// rawServer answers each WebSocket handshake with what answer makes of the
// client's key, then does what after says with the connection.
func rawServer(t *testing.T, answer func(key string) string, after func(net.Conn, *bufio.Reader)) string {
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
					after(c, br)
				}
				_, _ = io.Copy(io.Discard, br)
			}()
		}
	}()
	return "ws://" + ln.Addr().String() + "/"
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
		{"wrong key", func(string) string { return switched("other") }, "bad handshake"},
		{"garbage", func(string) string { return "garbage\r\n\r\n" }, "malformed"},
		{"too long", func(string) string {
			return "HTTP/1.1 101 Switching Protocols\r\nX: " + strings.Repeat("a", MaxHandshake+1)
		}, "bad handshake"},
		{"closed", func(string) string { return "" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := rawServer(t, tc.answer, func(c net.Conn, _ *bufio.Reader) {
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
	if !errors.Is(err, os.ErrDeadlineExceeded) {
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
// reach the client as they should; a dial without a timeout takes the
// default.
func TestClientControlFrames(t *testing.T) {
	got := make(chan []byte, 2)
	url := rawServer(t, func(key string) string {
		return switched(key) + string(serverFrame(websocket.Binary, []byte("early")))
	}, func(c net.Conn, br *bufio.Reader) {
		_, _ = c.Write(serverFrame(websocket.Ping, []byte("hi")))
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
