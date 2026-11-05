package console

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// idleConn is a raw HTTP/1.1 connection to a console, so that a test knows
// which connection each request goes on.
type idleConn struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dialIdle(t *testing.T, c *Console) *idleConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", c.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &idleConn{t: t, conn: conn, r: bufio.NewReader(conn)}
}

// do sends a request with header lines extra and body, and reads its
// response.
func (ic *idleConn) do(method, path, body string, extra ...string) *http.Response {
	ic.t.Helper()
	var b strings.Builder
	b.WriteString(method + " " + path + " HTTP/1.1\r\nHost: test\r\n")
	for _, h := range extra {
		b.WriteString(h + "\r\n")
	}
	if body != "" {
		b.WriteString("Content-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n")
	}
	b.WriteString("\r\n" + body)
	_ = ic.conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(ic.conn, b.String()); err != nil {
		ic.t.Fatalf("%s %s: %v", method, path, err)
	}
	resp, err := http.ReadResponse(ic.r, &http.Request{Method: method})
	if err != nil {
		ic.t.Fatalf("%s %s: %v", method, path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// closesWithin checks that the console closes the connection, within at
// most from now.
func (ic *idleConn) closesWithin(within time.Duration) {
	ic.t.Helper()
	start := time.Now()
	_ = ic.conn.SetReadDeadline(start.Add(within))
	if _, err := ic.r.ReadByte(); err == nil {
		ic.t.Fatal("the console sent more on an idle connection")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		ic.t.Fatalf("the connection is still open after %v", within)
	}
}

// TestConsoleIdleTimeouts: a connection that has not sent a request with a
// valid session is closed after the short idle timeout, whether it said
// nothing or made requests without one; one that has, by logging in or by
// a request carrying the session, outlives it, request after request, until
// it logs out.
func TestConsoleIdleTimeouts(t *testing.T) {
	const unauth, idle = 300 * time.Millisecond, 5 * time.Second
	defer func(i, u time.Duration) { idleTimeout, unauthIdleTimeout = i, u }(idleTimeout, unauthIdleTimeout)
	idleTimeout, unauthIdleTimeout = idle, unauth
	c, err := Start(newEngine(t), options(t, discardLog(t), nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop(t.Context()) })

	t.Run("silent", func(t *testing.T) {
		dialIdle(t, c).closesWithin(idle / 2)
	})
	t.Run("without a session", func(t *testing.T) {
		ic := dialIdle(t, c)
		if resp := ic.do("GET", "/", ""); resp.StatusCode != 200 {
			t.Fatalf("GET /: %d", resp.StatusCode)
		}
		if resp := ic.do("GET", "/api/session", "", "Cookie: "+c.cookie+"=forged"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a forged session: %d", resp.StatusCode)
		}
		if resp := ic.do("POST", "/api/login", `{"username":"admin","password":"wrong"}`); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a wrong password: %d", resp.StatusCode)
		}
		ic.closesWithin(idle / 2)
	})

	var cookie string
	t.Run("logged in", func(t *testing.T) {
		ic := dialIdle(t, c)
		resp := ic.do("POST", "/api/login", `{"username":"admin","password":"secret"}`)
		if resp.StatusCode != 200 {
			t.Fatalf("login: %d", resp.StatusCode)
		}
		for _, ck := range resp.Cookies() {
			if ck.Name == c.cookie {
				cookie = ck.Name + "=" + ck.Value
			}
		}
		if cookie == "" {
			t.Fatal("no session cookie")
		}
		// Past the short idle timeout: the login keeps the connection.
		time.Sleep(3 * unauth)
		if resp := ic.do("GET", "/", ""); resp.StatusCode != 200 {
			t.Fatalf("GET / after a pause: %d", resp.StatusCode)
		}
	})
	if cookie == "" {
		t.FailNow()
	}
	t.Run("with a session", func(t *testing.T) {
		ic := dialIdle(t, c)
		if resp := ic.do("GET", "/api/session", "", "Cookie: "+cookie); resp.StatusCode != 200 {
			t.Fatalf("GET /api/session: %d", resp.StatusCode)
		}
		// Twice past the short idle timeout, the second time after a
		// request without the session, which does not shorten it again.
		time.Sleep(3 * unauth)
		if resp := ic.do("GET", "/", ""); resp.StatusCode != 200 {
			t.Fatalf("GET / after a pause: %d", resp.StatusCode)
		}
		time.Sleep(3 * unauth)
		if resp := ic.do("POST", "/api/logout", "", "Cookie: "+cookie); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("logout: %d", resp.StatusCode)
		}
		// Logged out, it is an unauthenticated connection again.
		ic.closesWithin(idle / 2)
	})
}

// keepsAsking sends GET path, with header lines extra, every gap until the
// console closes the connection, and returns how long after start that
// was; it fails the test if the connection is still open after limit.
func (ic *idleConn) keepsAsking(start time.Time, gap, limit time.Duration, path string, extra ...string) time.Duration {
	ic.t.Helper()
	var b strings.Builder
	b.WriteString("GET " + path + " HTTP/1.1\r\nHost: test\r\n")
	for _, h := range extra {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("\r\n")
	for time.Since(start) < limit {
		_ = ic.conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(ic.conn, b.String()); err != nil {
			return time.Since(start)
		}
		resp, err := http.ReadResponse(ic.r, &http.Request{Method: "GET"})
		if err != nil {
			return time.Since(start)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		time.Sleep(gap)
	}
	ic.t.Fatalf("the connection is still open %v on, asked every %v", time.Since(start), gap)
	return 0
}

// TestConsoleUnauthBoundFromOpen: a connection without a valid session is
// closed unauthIdleTimeout after it opened however often it asks, so that
// requests just inside each idle wait do not hold it; and one whose
// session has ended since it was kept for it is closed unauthIdleTimeout
// after the request that found the session gone, however often it asks.
func TestConsoleUnauthBoundFromOpen(t *testing.T) {
	const unauth, idle = 300 * time.Millisecond, 5 * time.Second
	defer func(i, u time.Duration) { idleTimeout, unauthIdleTimeout = i, u }(idleTimeout, unauthIdleTimeout)
	idleTimeout, unauthIdleTimeout = idle, unauth
	c, err := Start(newEngine(t), options(t, discardLog(t), nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop(t.Context()) })

	for _, path := range []string{"/", "/api/session"} {
		t.Run("without a session "+path, func(t *testing.T) {
			start := time.Now()
			ic := dialIdle(t, c)
			if held := ic.keepsAsking(start, unauth/3, 10*unauth, path); held > 3*unauth {
				t.Errorf("closed only after %v", held)
			}
		})
	}

	t.Run("session ended", func(t *testing.T) {
		ic := dialIdle(t, c)
		resp := ic.do("POST", "/api/login", `{"username":"admin","password":"secret"}`)
		if resp.StatusCode != 200 {
			t.Fatalf("login: %d", resp.StatusCode)
		}
		var token string
		for _, ck := range resp.Cookies() {
			if ck.Name == c.cookie {
				token = ck.Value
			}
		}
		if token == "" {
			t.Fatal("no session cookie")
		}
		cookie := "Cookie: " + c.cookie + "=" + token
		// Kept past the bound by its session.
		time.Sleep(2 * unauth)
		if resp := ic.do("GET", "/api/session", "", cookie); resp.StatusCode != 200 {
			t.Fatalf("GET /api/session: %d", resp.StatusCode)
		}
		// The session ends elsewhere (another tab's logout, a revocation,
		// its expiry); the next request finds it gone.
		if _, ok := c.logins.End(token); !ok {
			t.Fatal("no session to end")
		}
		start := time.Now()
		if resp := ic.do("GET", "/api/session", "", cookie); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET /api/session with the ended session: %d", resp.StatusCode)
		}
		if held := ic.keepsAsking(start, unauth/3, 10*unauth, "/"); held > 3*unauth {
			t.Errorf("closed only after %v", held)
		}
	})
}
