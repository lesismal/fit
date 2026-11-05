package console

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/login"
)

// hostDo sends a request to base with host as its Host field and origin, when
// not empty, as its Origin, and returns the answer's status.
func hostDo(t *testing.T, cl *http.Client, method, base, path, host, origin, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestConsoleDNSRebinding is the attack on a loopback console: a web page
// whose name an attacker made resolve to 127.0.0.1 posts a login, its Origin
// matching its Host as the browser sends them. The console refuses every
// request whose Host is not a loopback name, or one of its config's hosts.
func TestConsoleDNSRebinding(t *testing.T) {
	engine := newEngine(t)
	o := options(t, nil, nil)
	o.Config.Hosts = []string{"Console.Example"}
	c, err := Start(engine, o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	base := strings.TrimSuffix(c.URL(), "/")
	_, port, _ := net.SplitHostPort(c.Addr().String())
	cl := &http.Client{Timeout: 10 * time.Second}
	login := `{"username":"admin","password":"secret"}`

	evil := "rebind.attacker.example:" + port
	if code := hostDo(t, cl, "POST", base, "/api/login", evil, "http://"+evil, login); code != http.StatusForbidden {
		t.Errorf("a login from a rebound name: %d", code)
	}
	if code := hostDo(t, cl, "GET", base, "/", evil, "", ""); code != http.StatusForbidden {
		t.Errorf("a page for a rebound name: %d", code)
	}
	// HTTP/1.0 without a Host field at all.
	conn, err := net.DialTimeout("tcp", c.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
	line := make([]byte, 12)
	if _, err := io.ReadFull(conn, line); err != nil || string(line) != "HTTP/1.0 403" && string(line) != "HTTP/1.1 403" {
		t.Errorf("no Host: %q %v", line, err)
	}
	conn.Close()
	for _, host := range []string{"127.0.0.1:" + port, "localhost:" + port, "LOCALHOST", "[::1]:" + port, "console.example:" + port, "console.example."} {
		if code := hostDo(t, cl, "GET", base, "/", host, "", ""); code != http.StatusOK {
			t.Errorf("Host %q: %d", host, code)
		}
	}
	host := "localhost:" + port
	if code := hostDo(t, cl, "POST", base, "/api/login", host, "http://"+host, login); code != http.StatusOK {
		t.Errorf("a login at localhost: %d", code)
	}
}

// TestConsoleTLSHosts: over TLS, a Host may also name what the certificate
// covers, and nothing else.
func TestConsoleTLSHosts(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "fit.example", "*.ops.example", "10.1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	o := options(t, nil, nil)
	o.Config.CertFile, o.Config.KeyFile = cert.CertFile, cert.KeyFile
	c, err := Start(newEngine(t), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: certtest.Pool(cert), ServerName: "fit.example"}}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Timeout: 10 * time.Second, Transport: tr}
	base := strings.TrimSuffix(c.URL(), "/")
	for host, want := range map[string]int{
		"fit.example":           http.StatusOK,
		"fit.example:443":       http.StatusOK,
		"console.ops.example":   http.StatusOK,
		"10.1.2.3:8443":         http.StatusOK,
		"127.0.0.1":             http.StatusOK,
		"other.example":         http.StatusForbidden,
		"a.b.ops.example":       http.StatusForbidden,
		"10.1.2.4":              http.StatusForbidden,
		"fit.example.attacker.": http.StatusForbidden,
	} {
		if code := hostDo(t, cl, "GET", base, "/", host, "", ""); code != want {
			t.Errorf("Host %q: %d, want %d", host, code, want)
		}
	}
	if !strings.HasPrefix(c.Cookie(), "__Host-fit_test_") {
		t.Errorf("the cookie over TLS is %q", c.Cookie())
	}
}

// TestConsoleConcurrentBadLogins: 30 wrong logins at once from one address
// check no more passwords than the address may fail before it is locked
// out; the rest are refused at once, and the right password afterwards too.
func TestConsoleConcurrentBadLogins(t *testing.T) {
	engine := newEngine(t)
	c, err := Start(engine, options(t, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	base := strings.TrimSuffix(c.URL(), "/")
	cl := &http.Client{Timeout: 10 * time.Second}
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 30 {
		wg.Go(func() {
			<-start
			resp, err := cl.Post(base+"/api/login", "application/json", strings.NewReader(`{"username":"admin","password":"wrong"}`))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			mu.Lock()
			codes[resp.StatusCode]++
			mu.Unlock()
		})
	}
	close(start)
	wg.Wait()
	if codes[http.StatusUnauthorized] > login.MaxFailures || codes[http.StatusUnauthorized]+codes[http.StatusTooManyRequests] != 30 {
		t.Errorf("answers %v: more than %d checked", codes, login.MaxFailures)
	}
	t.Logf("answers: %v", codes)
	resp, err := cl.Post(base+"/api/login", "application/json", strings.NewReader(`{"username":"admin","password":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if codes[http.StatusUnauthorized] == login.MaxFailures && resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("the right password after %d failures: %d", login.MaxFailures, resp.StatusCode)
	}
}

// TestConsoleLoginDropped: a login the pool has no room for is answered 503
// and its reservation is let go, so that it counts as no failure.
func TestConsoleLoginDropped(t *testing.T) {
	block := make(chan struct{})
	c, err := Start(newEngine(t), options(t, nil, block))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(block); c.Stop(context.Background()) }()
	started := make(chan struct{})
	for range Workers {
		c.tasks <- func() { started <- struct{}{}; <-block }
	}
	for range Workers {
		<-started
	}
	for range Queue {
		c.tasks <- func() {}
	}
	b := newBrowser(t, strings.TrimSuffix(c.URL(), "/"), nil)
	for range login.MaxFailures + 1 {
		if code, _ := b.do("POST", "/api/login", `{"username":"admin","password":"wrong"}`); code != http.StatusServiceUnavailable {
			t.Fatalf("a login with a full pool: %d", code)
		}
	}
	if n := c.logins.Addresses(); n != 0 {
		t.Errorf("%d addresses remembered for logins never checked", n)
	}
}

// TestConsoleSessionHost: a session's cookie works only on requests naming
// the Host it logged in at: a browser sends 127.0.0.1's cookies to every
// port of it, and another name of the console is another site.
func TestConsoleSessionHost(t *testing.T) {
	c, err := Start(newEngine(t), options(t, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	b := newBrowser(t, strings.TrimSuffix(c.URL(), "/"), nil)
	b.login()
	if code, _ := b.do("GET", "/api/session", ""); code != http.StatusOK {
		t.Fatalf("the session at its Host: %d", code)
	}
	u := strings.TrimSuffix(c.URL(), "/")
	pu, _ := url.Parse(u)
	cks := b.c.Jar.Cookies(pu)
	if len(cks) != 1 {
		t.Fatalf("cookies %v", cks)
	}
	_, port, _ := net.SplitHostPort(c.Addr().String())
	req, _ := http.NewRequest("GET", u+"/api/session", nil)
	req.Host = "localhost:" + port
	req.AddCookie(cks[0])
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the session's cookie at another Host: %d", resp.StatusCode)
	}
}
