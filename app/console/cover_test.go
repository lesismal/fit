package console

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
	"github.com/lesismal/fit/logs"
)

// newEngine returns a running engine, stopped when the test ends.
func newEngine(t *testing.T) *fib.Engine {
	t.Helper()
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = engine.Run(); close(done) }()
	t.Cleanup(func() { engine.Stop(); <-done; _ = engine.Close() })
	return engine
}

// options returns the options of a console like startConsole's, with a log,
// and routes that exercise Request's helpers.
func options(t *testing.T, log *logs.Logger, block chan struct{}) Options {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	return Options{
		Config: &config.RoleConsole{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "admin", PasswordHash: string(hash)}}},
		Cookie: "fit_test",
		Log:    log,
		Pages:  fstest.MapFS{"index.html": {Data: []byte("<p>hi</p>")}, "blob": {Data: []byte("x")}},
		Routes: func(api *API) {
			api.Handle("POST", "/api/echo", func(r *Request) {
				var v map[string]any
				if !r.Decode(&v) {
					return
				}
				r.Logf("echoed %d keys", len(v))
				_ = r.JSON(200, v)
			})
			api.Handle("GET", "/api/chan", func(r *Request) {
				if err := r.JSON(200, make(chan int)); err != nil {
					_ = Error(r.Context, 500, err.Error())
				}
			})
			api.Handle("GET", "/api/block", func(r *Request) {
				r.Go(func() {
					<-block
					_ = r.JSON(200, "done")
				})
			})
		},
	}
}

func discardLog(t *testing.T) *logs.Logger {
	t.Helper()
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root.Logger(config.LoggerLLM)
}

func newBrowser(t *testing.T, base string, tr http.RoundTripper) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, c: &http.Client{Jar: jar, Timeout: 10 * time.Second, Transport: tr}, base: base}
}

func (b *browser) login() {
	b.t.Helper()
	code, body := b.do("POST", "/api/login", `{"username":"admin","password":"secret"}`)
	if code != 200 {
		b.t.Fatalf("login: %d %s", code, body)
	}
}

func TestConsoleRequests(t *testing.T) {
	engine := newEngine(t)
	c, err := Start(engine, options(t, discardLog(t), nil))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	b := newBrowser(t, strings.TrimSuffix(c.URL(), "/"), nil)

	if code, _ := b.do("POST", "/", "{}"); code != 405 {
		t.Errorf("POST to a page: %d", code)
	}
	if code, _ := b.do("GET", "/blob", ""); code != 200 {
		t.Errorf("a file without an extension: %d", code)
	}
	if code, _ := b.do("POST", "/api/login", ""); code != 415 {
		t.Errorf("a login that is not JSON: %d", code)
	}
	if code, _ := b.do("POST", "/api/login", "{"); code != 400 {
		t.Errorf("a login of bad JSON: %d", code)
	}
	if code, _ := b.do("POST", "/api/login", `{"username":"admin","password":"wrong"}`); code != 401 {
		t.Errorf("a wrong password: %d", code)
	}
	// An unknown session's cookie is not logged in.
	req, _ := http.NewRequest("GET", b.base+"/api/session", nil)
	req.AddCookie(&http.Cookie{Name: c.cookie, Value: "nope"})
	if resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req); err != nil || resp.StatusCode != 401 {
		t.Errorf("an unknown session: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}

	b.login()
	code, body := b.do("GET", "/api/session", "")
	if code != 200 || !strings.Contains(body, `"user":"admin"`) {
		t.Fatalf("session: %d %s", code, body)
	}
	var s sessionBody
	_ = json.Unmarshal([]byte(body), &s)
	b.csrf = s.CSRF
	if code, _ := b.do("POST", "/api/echo", ""); code != 415 {
		t.Errorf("an echo that is not JSON: %d", code)
	}
	if code, _ := b.do("POST", "/api/echo", "{"); code != 400 {
		t.Errorf("an echo of bad JSON: %d", code)
	}
	if code, body := b.do("POST", "/api/echo", `{"a":1}`); code != 200 || !strings.Contains(body, `"a":1`) {
		t.Errorf("echo: %d %s", code, body)
	}
	if code, body := b.do("GET", "/api/chan", ""); code != 500 || !strings.Contains(body, "unsupported type") {
		t.Errorf("a value JSON cannot hold: %d %s", code, body)
	}
	if code, _ := b.do("POST", "/api/logout", ""); code != 204 {
		t.Errorf("logout: %d", code)
	}

	// Too many failures from an address lock it out.
	for range login.MaxFailures {
		c.logins.Failed("127.0.0.1")
	}
	req, _ = http.NewRequest("POST", b.base+"/api/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("a locked-out address: %d %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestConsoleBusyAndStopTimeout(t *testing.T) {
	engine := newEngine(t)
	block := make(chan struct{})
	c, err := Start(engine, options(t, nil, block))
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t, strings.TrimSuffix(c.URL(), "/"), nil)
	b.login()

	// Every worker holds a task and the queue is full: the next is 503.
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
	if code, body := b.do("GET", "/api/block", ""); code != 503 || !strings.Contains(body, "busy") {
		t.Errorf("a full pool: %d %s", code, body)
	}

	// Stop gives up waiting for the tasks once ctx ends.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Stop(ctx)
	close(block)
	c.Stop(context.Background())
}

// TestSubmit: Submit runs a task on the pool without holding or answering
// the request, and refuses one past the queue.
func TestSubmit(t *testing.T) {
	c := &Console{tasks: make(chan func(), 1)}
	r := &Request{c: c}
	ran := false
	if !r.Submit(func() { ran = true }) {
		t.Fatal("a queue with room refused a task")
	}
	if r.Submit(func() {}) {
		t.Error("a full queue took a task")
	}
	(<-c.tasks)()
	if !ran {
		t.Error("the task did not run")
	}
}

func TestConsoleTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeCert(t, dir)
	engine := newEngine(t)
	o := options(t, nil, nil)
	o.Config.CertFile, o.Config.KeyFile = certFile, keyFile
	c, err := Start(engine, o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	if !strings.HasPrefix(c.URL(), "https://") {
		t.Fatalf("URL = %s", c.URL())
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer tr.CloseIdleConnections()
	b := newBrowser(t, strings.TrimSuffix(c.URL(), "/"), tr)
	req, _ := http.NewRequest("GET", b.base+"/", nil)
	req.Header.Set("Origin", b.base)
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Strict-Transport-Security") == "" {
		t.Errorf("over TLS: %d, HSTS %q", resp.StatusCode, resp.Header.Get("Strict-Transport-Security"))
	}

	o.Config.KeyFile = filepath.Join(dir, "missing.pem")
	if _, err := Start(engine, o); err == nil || !strings.Contains(err.Error(), "console:") {
		t.Errorf("a missing key file: %v", err)
	}
}

func writeCert(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// failFS lists bad.txt but cannot read it.
type failFS struct{ fstest.MapFS }

func (f failFS) ReadFile(name string) ([]byte, error) {
	if name == "bad.txt" {
		return nil, errors.New("unreadable")
	}
	return f.MapFS.ReadFile(name)
}

func TestStartErrors(t *testing.T) {
	engine := newEngine(t)
	o := options(t, nil, nil)
	o.Pages = failFS{fstest.MapFS{"bad.txt": {Data: []byte("x")}}}
	if _, err := Start(engine, o); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("unreadable pages: %v", err)
	}
	o = options(t, nil, nil)
	o.Config.Address = "256.0.0.1:0"
	if _, err := Start(engine, o); err == nil || !strings.Contains(err.Error(), "console:") {
		t.Errorf("a bad address: %v", err)
	}
}

func TestURLAndClientAddr(t *testing.T) {
	if got := (&Console{}).URL(); got != "" {
		t.Errorf("URL without an address = %q", got)
	}
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8}
	if got := (&Console{secure: true, addr: addr}).URL(); got != "https://127.0.0.1:8/" {
		t.Errorf("URL = %q", got)
	}
	if got := (&Console{addr: addr}).Addr(); got != addr {
		t.Errorf("Addr = %v", got)
	}
	if got := clientAddr(&http.Request{RemoteAddr: "pipe"}); got != "pipe" {
		t.Errorf("clientAddr without a port = %q", got)
	}
}

// plainHandler is a fib.Handler and nothing more.
type plainHandler struct{}

func (plainHandler) OnOpen(*fib.Connection)                 {}
func (plainHandler) OnData(*fib.Connection, []byte)         {}
func (plainHandler) OnPriorityData(*fib.Connection, []byte) {}
func (plainHandler) OnClose(*fib.Connection, error)         {}

// fullHandler also takes shutdowns and the engine's close.
type fullHandler struct {
	plainHandler
	shutdowns, closes int
}

func (h *fullHandler) OnShutdown(*fib.Connection)    { h.shutdowns++ }
func (h *fullHandler) OnEngineClose(*fib.Connection) { h.closes++ }

func TestLimitHandlerPassesOn(t *testing.T) {
	full := &fullHandler{}
	l := newLimitHandler(full, 1, 0)
	l.OnShutdown(nil)
	l.OnEngineClose(nil)
	if full.shutdowns != 1 || full.closes != 1 {
		t.Errorf("passed on %d shutdowns, %d closes", full.shutdowns, full.closes)
	}
	// A handler without them is not called.
	l = newLimitHandler(plainHandler{}, 1, 0)
	l.OnShutdown(nil)
	l.OnEngineClose(nil)
}

// TestLimitHandlerCloses binds limit handlers of its own: one that takes no
// connection, and one closed already, and checks each closes what opens.
func TestLimitHandlerCloses(t *testing.T) {
	engine := newEngine(t)
	none := newLimitHandler(plainHandler{}, 0, 0)
	closed := newLimitHandler(plainHandler{}, 8, 0)
	closed.closeAll()
	for name, l := range map[string]*limitHandler{"past max": none, "closed": closed} {
		i, err := engine.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: l})
		if err != nil {
			t.Fatal(err)
		}
		addrs, err := engine.BindingAddrs(i)
		if err != nil || len(addrs) == 0 {
			t.Fatal(addrs, err)
		}
		conn, err := net.DialTimeout("tcp", addrs[0].String(), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Errorf("%s: the connection is open", name)
		}
		conn.Close()
		if n := l.open(); n != 0 {
			t.Errorf("%s: %d connections counted", name, n)
		}
		_ = engine.RemoveBinding(i)
	}
}

// Two consoles of one role on one host keep their logins apart: a browser
// sends a host's cookies to each of its ports, so each console's cookie
// carries its port.
func TestConsolesShareAHost(t *testing.T) {
	engine := newEngine(t)
	var bs []*browser
	jar, _ := cookiejar.New(nil)
	for range 2 {
		c, err := Start(engine, options(t, discardLog(t), nil))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Stop(context.Background())
		b := newBrowser(t, strings.TrimSuffix(c.URL(), "/"), nil)
		b.c.Jar = jar // one browser
		b.login()
		bs = append(bs, b)
	}
	for i, b := range bs {
		if code, _ := b.do("GET", "/api/session", ""); code != 200 {
			t.Errorf("console %d lost its login: %d", i, code)
		}
	}
}

// A console logs through Logf without a Log, and through nothing without
// either; a login's body is bounded whatever MaxBody says.
func TestConsoleLogfAndLoginBound(t *testing.T) {
	for _, withLogf := range []bool{true, false} {
		o := options(t, nil, nil)
		var mu sync.Mutex
		var lines []string
		if withLogf {
			o.Logf = func(format string, args ...any) {
				mu.Lock()
				lines = append(lines, fmt.Sprintf(format, args...))
				mu.Unlock()
			}
		}
		o.MaxBody = 1 << 20
		c, err := Start(newEngine(t), o)
		if err != nil {
			t.Fatal(err)
		}
		base := strings.TrimSuffix(c.URL(), "/")
		resp, err := http.Post(base+"/api/login", "application/json", strings.NewReader(`{"username":"admin","password":"`+strings.Repeat("x", MaxLoginBytes)+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("a large login: %d", resp.StatusCode)
		}
		resp, err = http.Post(base+"/api/login", "application/json", strings.NewReader(`{"username":"admin","password":"secret"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || c.Logins() == nil {
			t.Errorf("login: %d", resp.StatusCode)
		}
		c.Stop(context.Background())
		mu.Lock()
		if got := strings.Join(lines, "\n"); withLogf != strings.Contains(got, `"admin" logged in`) {
			t.Errorf("Logf %v: %q", withLogf, got)
		}
		mu.Unlock()
	}
}

// TestConsoleLoginRace: wrong passwords sent at once from one address are
// checked one at a time, so no more than login.MaxFailures of them are
// checked before the address is locked out; the rest are answered 429.
func TestConsoleLoginRace(t *testing.T) {
	engine := newEngine(t)
	c, err := Start(engine, options(t, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	base := strings.TrimSuffix(c.URL(), "/")
	const n = 24 // below the console's 32 connections
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			b := newBrowser(t, base, nil)
			req, _ := http.NewRequest("POST", base+"/api/login", strings.NewReader(`{"username":"admin","password":"guess"}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := b.c.Do(req)
			if err != nil {
				codes <- 0
				return
			}
			resp.Body.Close()
			codes <- resp.StatusCode
		})
	}
	wg.Wait()
	close(codes)
	checked := 0
	for code := range codes {
		switch code {
		case 401:
			checked++
		case 429, 503:
		default:
			t.Errorf("a guess answered %d", code)
		}
	}
	if checked > login.MaxFailures {
		t.Errorf("%d guesses checked, want %d at most", checked, login.MaxFailures)
	}
	// Once the guesses are over, the address stays locked out.
	for c.logins.LockedOut("127.0.0.1") <= 0 && checked < login.MaxFailures {
		b := newBrowser(t, base, nil)
		if code, _ := b.do("POST", "/api/login", `{"username":"admin","password":"guess"}`); code == 401 {
			checked++
		}
	}
	if c.logins.LockedOut("127.0.0.1") <= 0 {
		t.Error("not locked out")
	}
	if code, _ := newBrowser(t, base, nil).do("POST", "/api/login", `{"username":"admin","password":"secret"}`); code != 429 {
		t.Errorf("the right password while locked out: %d", code)
	}
}

// TestConsoleLoginQueue: logins waiting for their own pool do not take the
// workers of logged-in users' tasks, and a full login queue answers 503.
func TestConsoleLoginQueue(t *testing.T) {
	engine := newEngine(t)
	block := make(chan struct{})
	c, err := Start(engine, options(t, nil, block))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(block); c.Stop(context.Background()) }()
	b := newBrowser(t, strings.TrimSuffix(c.URL(), "/"), nil)
	b.login()
	code, body := b.do("GET", "/api/session", "")
	var s sessionBody
	_ = json.Unmarshal([]byte(body), &s)
	b.csrf = s.CSRF
	if code != 200 {
		t.Fatalf("session: %d", code)
	}

	// The login pool is full.
	started := make(chan struct{})
	for range LoginWorkers {
		c.checks <- func() { started <- struct{}{}; <-block }
	}
	for range LoginWorkers {
		<-started
	}
	for range LoginQueue {
		c.checks <- func() {}
	}
	if code, body := newBrowser(t, b.base, nil).do("POST", "/api/login", `{"username":"admin","password":"secret"}`); code != 503 || !strings.Contains(body, "busy") {
		t.Errorf("a login with its pool full: %d %s", code, body)
	}
	// The address was released with the dropped check.
	if tk, _, busy := c.logins.Admit("127.0.0.1"); tk == nil || busy {
		t.Error("a dropped login left its address busy")
	} else {
		tk.Done(false)
	}
	// A logged-in user's task still runs.
	if code, body := b.do("POST", "/api/echo", `{"a":1}`); code != 200 {
		t.Errorf("a logged-in user's request with the login pool full: %d %s", code, body)
	}
}

// TestLimitHandlerPeerShare: a client other than a loopback one holds at
// most its share of the console's connections, a quarter of them; one past
// it is closed as it opens, and another client is still served.
func TestLimitHandlerPeerShare(t *testing.T) {
	engine := newEngine(t)
	l := newLimitHandler(plainHandler{}, 8, 0)
	var from atomic.Value
	from.Store(netip.MustParseAddr("203.0.113.9"))
	l.peer = func(*fib.Connection) netip.Addr { return from.Load().(netip.Addr) }
	i, err := engine.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: l})
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := engine.BindingAddrs(i)
	if err != nil || len(addrs) == 0 {
		t.Fatal(addrs, err)
	}
	dial := func() net.Conn {
		conn, err := net.DialTimeout("tcp", addrs[0].String(), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	closed := func(conn net.Conn) bool {
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, err := conn.Read(make([]byte, 1))
		var ne net.Error
		return err != nil && !(errors.As(err, &ne) && ne.Timeout())
	}
	share := l.slots.Peers().Share()
	if share != 2 {
		t.Fatalf("share %d, want 2", share)
	}
	var held []net.Conn
	for range share {
		held = append(held, dial())
	}
	for _, c := range held {
		if closed(c) {
			t.Fatal("a connection within the share was closed")
		}
	}
	if !closed(dial()) {
		t.Error("a connection past the client's share is open")
	}
	from.Store(netip.MustParseAddr("2001:db8:0:1::9")) // another client
	if closed(dial()) {
		t.Error("another client's connection was closed")
	}
	held[0].Close()
	from.Store(netip.MustParseAddr("203.0.113.9"))
	deadline := time.Now().Add(5 * time.Second)
	for closed(dial()) {
		if time.Now().After(deadline) {
			t.Fatal("the client is not served once one of its connections went")
		}
	}
}

// TestLimitHandlerEvicts: with its connections taken, a console closes the
// oldest unauthenticated one for a new connection, rather than refuse it;
// an authenticated one is kept, and a new one is refused only when every
// connection has authenticated.
func TestLimitHandlerEvicts(t *testing.T) {
	engine := newEngine(t)
	l := newLimitHandler(plainHandler{}, 4, 0) // 3 for unauthenticated ones
	i, err := engine.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: l})
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := engine.BindingAddrs(i)
	if err != nil || len(addrs) == 0 {
		t.Fatal(addrs, err)
	}
	// dial opens a connection and waits until the console holds want.
	dial := func(want int) net.Conn {
		conn, err := net.DialTimeout("tcp", addrs[0].String(), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		deadline := time.Now().Add(5 * time.Second)
		for l.open() != want {
			if time.Now().After(deadline) {
				t.Fatalf("%d connections open, want %d", l.open(), want)
			}
			time.Sleep(time.Millisecond)
		}
		return conn
	}
	closed := func(conn net.Conn) bool {
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, err := conn.Read(make([]byte, 1))
		var ne net.Error
		return err != nil && !(errors.As(err, &ne) && ne.Timeout())
	}
	authenticateAll := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, cc := range l.conns {
			cc.slot.Authenticate()
		}
	}
	c1 := dial(1)
	authenticateAll()
	c2 := dial(2)
	c3 := dial(3)
	c4 := dial(4)
	// Full, with 3 unauthenticated: the oldest of them, c2, goes.
	conn, err := net.DialTimeout("tcp", addrs[0].String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if !closed(c2) || closed(c1) || closed(c3) || closed(c4) || closed(conn) {
		t.Fatal("not the oldest unauthenticated connection was closed")
	}
	authenticateAll()
	// Every one authenticated: a new one is closed as it opens.
	last, err := net.DialTimeout("tcp", addrs[0].String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	if !closed(last) {
		t.Fatal("a connection past max_conns, all authenticated, is open")
	}
}
