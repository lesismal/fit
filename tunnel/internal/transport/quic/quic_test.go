package quic

import (
	"bytes"
	"crypto/tls"
	"errors"
	"math/rand/v2"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// sink is a transport.Handler that keeps what arrives, and echoes it when
// echo is set.
type sink struct {
	c    transport.Conn
	echo bool

	mu     sync.Mutex
	got    []byte
	closed chan struct{}
	err    error
	once   sync.Once
}

func newSink(c transport.Conn, echo bool) *sink {
	return &sink{c: c, echo: echo, closed: make(chan struct{})}
}

func (s *sink) OnData(data []byte) {
	if s.echo {
		_ = s.c.Send(append([]byte(nil), data...))
		return
	}
	s.mu.Lock()
	s.got = append(s.got, data...)
	s.mu.Unlock()
}

func (s *sink) OnClose(err error) {
	s.once.Do(func() { s.err = err; close(s.closed) })
}

func (s *sink) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

type fixture struct {
	t    *testing.T
	l    transport.Listener
	pin  string
	mu   sync.Mutex
	srv  []*sink
	echo bool
}

func newFixture(t *testing.T, echo bool) *fixture {
	certPEM, keyPEM, err := NewSelfSigned("localhost")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	e := tunneltest.Engine(t)
	f := &fixture{t: t, pin: Fingerprint(cert), echo: echo}
	f.l, err = transport.Listen(e, "quic://127.0.0.1:0", transport.ListenOptions{Cert: &cert}, func(c transport.Conn) transport.Handler {
		s := newSink(c, f.echo)
		f.mu.Lock()
		f.srv = append(f.srv, s)
		f.mu.Unlock()
		return s
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.l.Close() })
	return f
}

func (f *fixture) url() string {
	hostport, _ := ParseURL(f.l.URL())
	return DialURL(hostport, f.pin, "", false)
}

func (f *fixture) dial(t *testing.T) (transport.Conn, *sink) {
	t.Helper()
	var s *sink
	c, err := transport.Dial(tunneltest.Engine(t), f.url(), 5*time.Second, func(c transport.Conn) transport.Handler {
		s = newSink(c, false)
		return s
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func payload(n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func TestEcho(t *testing.T) {
	f := newFixture(t, true)
	c, s := f.dial(t)
	if err := c.Send([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the echo", func() bool { return s.len() == 5 })
	if string(s.got) != "hello" {
		t.Fatalf("got %q", s.got)
	}
}

// A large transfer arrives whole and in order, both ways.
func TestLargeTransfer(t *testing.T) {
	f := newFixture(t, true)
	c, s := f.dial(t)
	want := payload(8<<20, 7)
	for b := want; len(b) > 0; {
		n := min(len(b), 64<<10)
		if err := c.Send(append([]byte(nil), b[:n]...)); err != nil {
			t.Fatal(err)
		}
		b = b[n:]
	}
	waitFor(t, "the echo", func() bool { return s.len() >= len(want) })
	if !bytes.Equal(s.got, want) {
		t.Fatal("the echo differs")
	}
}

func TestManyConns(t *testing.T) {
	f := newFixture(t, true)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			c, s := f.dial(t)
			want := payload(64<<10, uint64(i))
			_ = c.Send(append([]byte(nil), want...))
			deadline := time.Now().Add(10 * time.Second)
			for s.len() < len(want) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if !bytes.Equal(s.got, want) {
				t.Errorf("conn %d: the echo differs", i)
			}
		})
	}
	wg.Wait()
}

// A link the server closes closes at the client, and one the client
// closes at the server.
func TestClose(t *testing.T) {
	f := newFixture(t, false)
	c, s := f.dial(t)
	_ = c.Send([]byte("x"))
	waitFor(t, "the server's link", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.srv) == 1 && f.srv[0].len() == 1 })
	f.mu.Lock()
	srv := f.srv[0]
	f.mu.Unlock()
	_ = srv.c.Close()
	select {
	case <-s.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the client's link did not close")
	}

	c, _ = f.dial(t)
	_ = c.Send([]byte("y"))
	waitFor(t, "the second link", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.srv) == 2 && f.srv[1].len() == 1 })
	_ = c.Close()
	f.mu.Lock()
	srv = f.srv[1]
	f.mu.Unlock()
	select {
	case <-srv.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the server's link did not close")
	}
}

// Closing the listener closes the links it accepted.
func TestListenerClose(t *testing.T) {
	f := newFixture(t, true)
	_, s := f.dial(t)
	_ = f.l.Close()
	select {
	case <-s.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the link outlived its listener")
	}
}

func TestPinMismatch(t *testing.T) {
	f := newFixture(t, true)
	hostport, _ := ParseURL(f.l.URL())
	other := DialURL(hostport, "00"+f.pin[2:], "", false)
	if f.pin[:2] == "00" {
		other = DialURL(hostport, "11"+f.pin[2:], "", false)
	}
	_, err := transport.Dial(tunneltest.Engine(t), other, 5*time.Second, func(c transport.Conn) transport.Handler { return newSink(c, false) })
	if err == nil {
		t.Fatal("a certificate that does not match the pin was taken")
	}
}

// A dial to a port nothing listens on fails within its timeout.
func TestDialTimeout(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close() // takes the datagrams, answers none
	start := time.Now()
	_, err = transport.Dial(tunneltest.Engine(t), DialURL(pc.LocalAddr().String(), "", "", true), 300*time.Millisecond, func(c transport.Conn) transport.Handler { return newSink(c, false) })
	if err == nil {
		t.Fatal("the dial worked")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the dial took %v", d)
	}
	if !errors.Is(err, ErrDialTimeout) {
		t.Logf("err %v", err)
	}
}

func TestParseURL(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"quic://example.com:4433", "example.com:4433", true},
		{"quic://[::1]:4433/", "[::1]:4433", true},
		{"quic://example.com:4433?pin=ab", "example.com:4433", true},
		{"127.0.0.1:4433", "127.0.0.1:4433", true},
		{"ws://example.com:80", "", false},
		{"quic://example.com", "", false},
		{"quic://example.com:4433/path", "", false},
		{"example.com", "", false},
	} {
		got, err := ParseURL(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseURL(%q) = %q, %v", tc.in, got, err)
		}
	}
}
