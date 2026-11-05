package admin

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
)

// fakeListener hands out connections from the addresses it is given, and
// tells made of each.
type fakeListener struct {
	net.Listener
	from chan net.Addr
	made chan *addrConn
}

func (l *fakeListener) Accept() (net.Conn, error) {
	a, ok := <-l.from
	if !ok {
		return nil, net.ErrClosed
	}
	c, _ := net.Pipe()
	ac := &addrConn{Conn: c, remote: a, closed: make(chan struct{})}
	l.made <- ac
	return ac, nil
}

type addrConn struct {
	net.Conn
	remote net.Addr
	closed chan struct{}
}

func (c *addrConn) RemoteAddr() net.Addr { return c.remote }
func (c *addrConn) Close() error         { close(c.closed); return c.Conn.Close() }

// TestLimitListenerPeerShare: a client other than a loopback one, an IPv6
// one by its /64, is given at most a quarter of the Admin API's
// connections; one past it is closed as it is accepted. Loopback clients
// and Unix sockets are not held to a share.
func TestLimitListenerPeerShare(t *testing.T) {
	from, made := make(chan net.Addr, 64), make(chan *addrConn, 64)
	l := newLimitListener(&fakeListener{from: from, made: made}, 8, login.NewPeers(8), time.Minute)
	tcp := func(s string) net.Addr { return net.TCPAddrFromAddrPort(netipAddrPort(t, s)) }
	accept := func(a net.Addr) *limitConn {
		from <- a
		c, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		return c.(*limitConn)
	}
	// Two from 203.0.113.9 and two from one /64; the third of each is
	// closed, and the next accepted is the other client's.
	var held []*limitConn
	for _, a := range []string{"203.0.113.9:1", "203.0.113.9:2", "[2001:db8::1]:1", "[2001:db8::2]:1"} {
		held = append(held, accept(tcp(a)))
	}
	from <- tcp("203.0.113.9:3")
	from <- tcp("[2001:db8::3]:1")
	if c := accept(tcp("198.51.100.1:1")); c.RemoteAddr().String() != "198.51.100.1:1" {
		t.Errorf("accepted %s past its share", c.RemoteAddr())
	}
	// Loopback and Unix clients take the rest of the connections.
	accept(tcp("127.0.0.1:1"))
	accept(tcp("127.0.0.1:2"))
	accept(&net.UnixAddr{Name: "@", Net: "unix"})
	if len(l.slots) != 8 {
		t.Fatalf("%d connections counted, want 8", len(l.slots))
	}
	// Past max_conns, even loopback is closed; once one goes, its client is
	// accepted again.
	for len(made) > 0 {
		<-made
	}
	got := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		got <- c
	}()
	from <- tcp("127.0.0.1:3")
	select {
	case <-(<-made).closed:
	case <-time.After(5 * time.Second):
		t.Fatal("a loopback connection past max_conns is open")
	}
	held[0].Close()
	from <- tcp("203.0.113.9:4")
	if c := <-got; c.RemoteAddr().String() != "203.0.113.9:4" {
		t.Errorf("accepted %s", c.RemoteAddr())
	}
}

func netipAddrPort(t *testing.T, s string) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

// TestUnauthenticatedTimeout: with a token, a connection that has made no
// authenticated request is closed after unauthTimeout, whether it sends
// nothing or only requests without the token, while one that has sent the
// token is kept for idleTimeout.
func TestUnauthenticatedTimeout(t *testing.T) {
	old := unauthTimeout
	unauthTimeout = 300 * time.Millisecond
	t.Cleanup(func() { unauthTimeout = old })
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := start(t, doc("v1"), &config.Admin{TokenFile: file})
	addr := f.admin.Addr().String()
	dial := func() (net.Conn, *bufio.Reader) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c, bufio.NewReader(c)
	}
	get := func(c net.Conn, br *bufio.Reader, auth string) int {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(c, "GET /config HTTP/1.1\r\nHost: localhost\r\n%s\r\n", auth)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// closedWithin reports whether the server closes c within d.
	closedWithin := func(c net.Conn, br *bufio.Reader, d time.Duration) bool {
		_ = c.SetReadDeadline(time.Now().Add(d))
		_, err := br.ReadByte()
		var ne net.Error
		return err != nil && !(errors.As(err, &ne) && ne.Timeout())
	}

	silent, sbr := dial()
	if !closedWithin(silent, sbr, 5*time.Second) {
		t.Error("a silent connection is kept")
	}
	anon, abr := dial()
	if s := get(anon, abr, ""); s != 401 {
		t.Fatalf("without the token: %d", s)
	}
	if !closedWithin(anon, abr, 5*time.Second) {
		t.Error("a connection without the token is kept idle")
	}
	authed, br := dial()
	if s := get(authed, br, "Authorization: Bearer s3cret\r\n"); s != 200 {
		t.Fatalf("with the token: %d", s)
	}
	if closedWithin(authed, br, 3*unauthTimeout) {
		t.Error("an authenticated connection was closed before idleTimeout")
	}
	if s := get(authed, br, "Authorization: Bearer s3cret\r\n"); s != 200 {
		t.Errorf("again on the kept connection: %d", s)
	}
}
