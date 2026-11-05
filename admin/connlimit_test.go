package admin

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
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
	c, peer := net.Pipe()
	ac := &addrConn{Conn: c, peer: peer, remote: a, closed: make(chan struct{})}
	l.made <- ac
	return ac, nil
}

type addrConn struct {
	net.Conn
	peer   net.Conn // the client's end
	remote net.Addr
	once   sync.Once
	closed chan struct{}
}

func (c *addrConn) RemoteAddr() net.Addr { return c.remote }
func (c *addrConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// isClosed reports whether c has been closed.
func (c *addrConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// TestLimitListenerPeerShare: a client other than a loopback one, an IPv6
// one by its /64, is given at most a quarter of the Admin API's
// connections; one past it is closed as it is accepted. Loopback clients
// and Unix sockets are not held to a share.
func TestLimitListenerPeerShare(t *testing.T) {
	from, made := make(chan net.Addr, 64), make(chan *addrConn, 64)
	l := newLimitListener(&fakeListener{from: from, made: made}, login.NewSlots(8), time.Minute)
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
	// Once one goes, its client is accepted again.
	held[0].Close()
	held[0].Close()
	if c := accept(tcp("203.0.113.9:4")); c.RemoteAddr().String() != "203.0.113.9:4" {
		t.Errorf("accepted %s", c.RemoteAddr())
	}
	// Loopback and Unix clients are held to no share.
	accept(tcp("127.0.0.1:1"))
	accept(&net.UnixAddr{Name: "@", Net: "unix"})
}

// TestLimitListenerEvicts: past the share of max_conns unauthenticated
// connections may hold, a new one closes the oldest unauthenticated one;
// authenticated ones are kept, and a new one is refused only when every
// connection has authenticated.
func TestLimitListenerEvicts(t *testing.T) {
	from, made := make(chan net.Addr, 64), make(chan *addrConn, 64)
	l := newLimitListener(&fakeListener{from: from, made: made}, login.NewSlots(4), time.Minute)
	loop := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	accept := func() (*limitConn, *addrConn) {
		from <- loop
		c, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		return c.(*limitConn), <-made
	}
	// 3 of 4 for unauthenticated ones.
	c1, a1 := accept()
	_, a2 := accept()
	_, a3 := accept()
	_, a4 := accept()
	if !a1.isClosed() || a2.isClosed() || a3.isClosed() || a4.isClosed() {
		t.Fatal("not the oldest unauthenticated connection was closed")
	}
	c1.Close()
	t.Cleanup(func() {
		for _, a := range []*addrConn{a2, a3, a4} {
			a.Close()
		}
	})
	// Authenticated, a connection is kept.
	c5, a5 := accept() // closes a2
	c5.authenticate()
	c5.authenticate()
	c6, a6 := accept() // takes the fourth slot
	c6.authenticate()
	if !a2.isClosed() || a5.isClosed() || a6.isClosed() {
		t.Fatal("eviction")
	}
	accept() // closes a3
	accept() // closes a4
	if !a3.isClosed() || !a4.isClosed() || a5.isClosed() || a6.isClosed() {
		t.Fatal("an authenticated connection was closed")
	}
	// A closed connection is not authenticated.
	c1.authenticate()
	if c1.authenticated() {
		t.Error("a closed connection authenticated")
	}
}

// TestLimitConnClosedAtBound: a connection that has not authenticated is
// closed unauth after its accept, whatever it is doing, a write the client
// does not read included, and no write deadline lies past then; one that
// has is kept, its deadlines as net/http sets them.
func TestLimitConnClosedAtBound(t *testing.T) {
	from, made := make(chan net.Addr, 4), make(chan *addrConn, 4)
	l := newLimitListener(&fakeListener{from: from, made: made}, login.NewSlots(4), 200*time.Millisecond)
	loop := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	from <- loop
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	a := <-made
	// A write deadline net/http would set a minute ahead.
	if err := c.SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	_, err = c.Write(make([]byte, 1<<20)) // net.Pipe: blocks until read
	if err == nil {
		t.Fatal("a write nobody reads returned")
	}
	if held := time.Since(begin); held > 20*time.Second {
		t.Fatalf("the write was held %v", held)
	}
	select {
	case <-a.closed:
	case <-time.After(20 * time.Second):
		t.Fatal("the unauthenticated connection was not closed at its bound")
	}

	from <- loop
	c2, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	a2 := <-made
	t.Cleanup(func() { c2.Close() })
	c2.(*limitConn).authenticate()
	time.Sleep(400 * time.Millisecond)
	if a2.isClosed() {
		t.Fatal("an authenticated connection was closed at the bound")
	}
	// Its deadlines are net/http's: a write past the bound waits.
	if err := c2.SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 3)
		_, _ = io.ReadFull(a2.peer, buf)
	}()
	if _, err := c2.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	<-done
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
		_, _ = io.Copy(io.Discard, resp.Body)
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

// TestUnauthenticatedBoundFromAccept: a client without the token cannot keep
// a connection past unauthTimeout from its accept by asking again before
// each wait runs out, neither with requests answered 401, which close the
// connection at once, nor with the token-less dashboard pages; while one
// that sends the token within that time is kept past it, as long as
// idleTimeout.
func TestUnauthenticatedBoundFromAccept(t *testing.T) {
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
	// get sends GET path with header lines extra, and returns its status,
	// or 0 once the server has closed the connection.
	get := func(c net.Conn, br *bufio.Reader, path, extra string) int {
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: localhost\r\n%s\r\n", path, extra); err != nil {
			return 0
		}
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			return 0
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("401", func(t *testing.T) {
		c, br := dial()
		if s := get(c, br, "/status", ""); s != http.StatusUnauthorized {
			t.Fatalf("without the token: %d", s)
		}
		time.Sleep(unauthTimeout / 3)
		if s := get(c, br, "/status", ""); s != 0 {
			t.Fatalf("the connection was kept after a 401: %d", s)
		}
	})
	t.Run("dashboard", func(t *testing.T) {
		c, br := dial()
		begin := time.Now()
		for time.Since(begin) < 10*unauthTimeout {
			s := get(c, br, "/dashboard/", "")
			if s == 0 {
				if held := time.Since(begin); held > 3*unauthTimeout {
					t.Fatalf("closed only after %v", held)
				}
				return
			}
			if s != http.StatusOK {
				t.Fatalf("GET /dashboard/: %d", s)
			}
			time.Sleep(unauthTimeout / 3)
		}
		t.Fatalf("a connection without the token was kept %v, asking every %v", time.Since(begin), unauthTimeout/3)
	})
	t.Run("authenticated late", func(t *testing.T) {
		c, br := dial()
		time.Sleep(unauthTimeout / 3)
		if s := get(c, br, "/status", "Authorization: Bearer s3cret\r\n"); s != http.StatusOK {
			t.Fatalf("with the token: %d", s)
		}
		time.Sleep(2 * unauthTimeout)
		// The dashboard's files on an authenticated connection keep it.
		for range 2 {
			if s := get(c, br, "/dashboard/", ""); s != http.StatusOK {
				t.Fatalf("an authenticated connection was not kept past the bound: %d", s)
			}
		}
	})
}

// TestUnauthenticatedPipelining: a client without the token that sends
// many dashboard requests at once, and reads their answers slowly, gets one
// answer, with Connection: close, and its connection is closed within the
// bound, not held for as long as answering them all would take.
func TestUnauthenticatedPipelining(t *testing.T) {
	old := unauthTimeout
	unauthTimeout = 300 * time.Millisecond
	t.Cleanup(func() { unauthTimeout = old })
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := start(t, doc("v1"), &config.Admin{TokenFile: file})
	c, err := net.Dial("tcp", f.admin.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var reqs []byte
	for range 70 {
		reqs = append(reqs, "GET /dashboard/ HTTP/1.1\r\nHost: localhost\r\n\r\n"...)
	}
	if _, err := c.Write(reqs); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !resp.Close {
		t.Fatalf("%d, Connection: %q", resp.StatusCode, resp.Header.Get("Connection"))
	}
	// Read a byte at a time, slowly: nothing more comes, and the connection
	// is closed.
	for {
		time.Sleep(10 * time.Millisecond)
		if _, err := br.ReadByte(); err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatal("the connection is kept")
			}
			break
		}
		t.Fatal("more than one answer")
	}
	if held := time.Since(begin); held > 15*time.Second {
		t.Fatalf("held %v", held)
	}
}

// TestReconnectingClientsKeepNoAdminOut: clients without the token that
// hold every connection the Admin API keeps for unauthenticated ones keep
// out neither an admin that has authenticated, however fast they reconnect,
// nor one that connects anew while they reconnect as each of theirs runs
// out, as the review's client did every 4.9s.
func TestReconnectingClientsKeepNoAdminOut(t *testing.T) {
	old := unauthTimeout
	unauthTimeout = time.Second
	t.Cleanup(func() { unauthTimeout = old })
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := start(t, doc("v1"), &config.Admin{TokenFile: file, MaxConns: 8})
	addr := f.admin.Addr().String()
	get := func(c net.Conn, br *bufio.Reader) int {
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprintf(c, "GET /status HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer s3cret\r\n\r\n"); err != nil {
			return 0
		}
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			return 0
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	// attack runs 8 clients that each hold a connection until the server
	// closes it, or period has passed since they dialed it, and then dial
	// again once period has passed; 0 dials again at once.
	attack := func(period time.Duration) (stop func()) {
		done := make(chan struct{})
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for {
					begin := time.Now()
					if c, err := net.Dial("tcp", addr); err == nil {
						if period > 0 {
							_ = c.SetReadDeadline(begin.Add(period))
						}
						go func() {
							<-done
							c.Close()
						}()
						_, _ = c.Read(make([]byte, 1))
						c.Close()
					}
					select {
					case <-done:
						return
					case <-time.After(max(time.Millisecond, time.Until(begin.Add(period)))):
					}
				}
			})
		}
		return func() {
			close(done)
			wg.Wait()
		}
	}

	admin, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	abr := bufio.NewReader(admin)
	if s := get(admin, abr); s != http.StatusOK {
		t.Fatalf("the admin's first request: %d", s)
	}
	stop := attack(0)
	time.Sleep(2 * unauthTimeout)
	if s := get(admin, abr); s != http.StatusOK {
		t.Errorf("the authenticated admin's connection, with clients reconnecting at once: %d", s)
	}
	stop()

	stop = attack(unauthTimeout * 98 / 100)
	defer stop()
	time.Sleep(unauthTimeout / 2)
	for i := range 5 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if s := get(c, bufio.NewReader(c)); s != http.StatusOK {
			t.Fatalf("a new admin connection, try %d: %d", i, s)
		}
		c.Close()
		time.Sleep(unauthTimeout / 5)
	}
	if s := get(admin, abr); s != http.StatusOK {
		t.Errorf("the authenticated admin's connection: %d", s)
	}
}
