package transport

import (
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// recorder is a Handler that keeps what arrives.
type recorder struct {
	mu     sync.Mutex
	got    []byte
	closed chan error
}

func newRecorder() *recorder { return &recorder{closed: make(chan error, 1)} }

func (r *recorder) OnData(data []byte) {
	r.mu.Lock()
	r.got = append(r.got, data...)
	r.mu.Unlock()
}

func (r *recorder) OnClose(err error) { r.closed <- err }

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.got)
}

func TestScheme(t *testing.T) {
	for _, tc := range []struct {
		addr, scheme string
		url          bool
	}{
		{"127.0.0.1:1", "tcp", false},
		{"tcp://127.0.0.1:1", "tcp", true},
		{"ws://h:1/p", "ws", true},
		{"quic://h:1", "quic", true},
	} {
		if s := Scheme(tc.addr); s != tc.scheme {
			t.Errorf("Scheme(%q) = %q", tc.addr, s)
		}
		if u := IsURL(tc.addr); u != tc.url {
			t.Errorf("IsURL(%q) = %v", tc.addr, u)
		}
	}
	if !slices.Contains(Schemes(), "tcp") {
		t.Errorf("schemes %v", Schemes())
	}
}

func TestUnsupportedScheme(t *testing.T) {
	e := tunneltest.Engine(t)
	if _, err := Dial(e, "nope://h:1", time.Second, nil); err == nil || !strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("dial: %v", err)
	}
	if _, err := Listen(e, "nope://h:1", ListenOptions{}, nil); err == nil || !strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("listen: %v", err)
	}
}

// A TCP link carries bytes both ways, and closes at the other end.
func TestTCP(t *testing.T) {
	e := tunneltest.Engine(t)
	srv := make(chan Conn, 1)
	rs := newRecorder()
	l, err := Listen(e, "tcp://0.0.0.0:0", ListenOptions{}, func(c Conn) Handler { srv <- c; return rs })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !strings.HasPrefix(l.URL(), "127.0.0.1:") {
		t.Errorf("URL of a listener on every address: %q", l.URL())
	}
	rc := newRecorder()
	c, err := Dial(e, "tcp://"+l.URL(), 5*time.Second, func(Conn) Handler { return rc })
	if err != nil {
		t.Fatal(err)
	}
	if c.LocalAddr() == nil || c.RemoteAddr().String() != l.URL() {
		t.Errorf("addrs %v %v", c.LocalAddr(), c.RemoteAddr())
	}
	if s := c.(interface{ String() string }).String(); !strings.HasPrefix(s, "fib ") {
		t.Errorf("String %q", s)
	}
	if err := c.Send([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	sc := <-srv
	waitFor(t, "the server's bytes", func() bool { return rs.String() == "ping" })
	_ = sc.Send([]byte("pong"))
	sc.CloseAfterSend()
	select {
	case <-rc.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the client's link did not close")
	}
	if rc.String() != "pong" {
		t.Errorf("client got %q", rc.String())
	}
	select {
	case <-rs.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the server's link did not close")
	}
}

// A connection open turns away closes, and its dial fails.
func TestOpenRefused(t *testing.T) {
	e := tunneltest.Engine(t)
	l, err := Listen(e, "127.0.0.1:0", ListenOptions{}, func(Conn) Handler { return newRecorder() })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := Dial(e, l.URL(), 5*time.Second, func(Conn) Handler { return nil }); !errors.Is(err, ErrClosedOpening) {
		t.Errorf("dial: %v", err)
	}
}

func TestDialErrors(t *testing.T) {
	e := tunneltest.Engine(t)
	// A port nothing listens on refuses the connection.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	if _, err := Dial(e, addr, 2*time.Second, func(Conn) Handler { return newRecorder() }); err == nil {
		t.Error("a dial to a closed port worked")
	}
	want := errors.New("no dial")
	if _, err := DialFib(func(func(*fib.Connection, error)) error { return want }); err != want {
		t.Errorf("DialFib: %v", err)
	}
}

func TestListenError(t *testing.T) {
	e := tunneltest.Engine(t)
	if _, err := Listen(e, "tcp://256.0.0.1:0", ListenOptions{}, func(Conn) Handler { return nil }); err == nil {
		t.Error("listened on a bad address")
	}
}

func TestDialable(t *testing.T) {
	e := tunneltest.Engine(t)
	if d := Dialable(e, 99); d != "" {
		t.Errorf("Dialable of no binding: %q", d)
	}
	i, err := e.AddBinding(fib.Binding{Network: "udp", Addr: "127.0.0.1:0", Handler: FibHandler(func(Conn) Handler { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	defer e.RemoveBinding(i)
	if d := Dialable(e, i); !strings.HasPrefix(d, "127.0.0.1:") {
		t.Errorf("Dialable of a UDP binding: %q", d)
	}
}

func TestFibHandlerPriorityData(t *testing.T) {
	// Priority data, which links do not use, is dropped.
	h := FibHandler(nil).(fibHandler)
	h.OnPriorityData(nil, nil)
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
