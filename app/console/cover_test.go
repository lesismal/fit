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
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
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
	return root.Logger(config.LoggerAgent)
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
	req.AddCookie(&http.Cookie{Name: "fit_test", Value: "nope"})
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
	l := newLimitHandler(full, 1)
	l.OnShutdown(nil)
	l.OnEngineClose(nil)
	if full.shutdowns != 1 || full.closes != 1 {
		t.Errorf("passed on %d shutdowns, %d closes", full.shutdowns, full.closes)
	}
	// A handler without them is not called.
	l = newLimitHandler(plainHandler{}, 1)
	l.OnShutdown(nil)
	l.OnEngineClose(nil)
}

// TestLimitHandlerCloses binds limit handlers of its own: one that takes no
// connection, and one closed already, and checks each closes what opens.
func TestLimitHandlerCloses(t *testing.T) {
	engine := newEngine(t)
	none := newLimitHandler(plainHandler{}, 0)
	closed := newLimitHandler(plainHandler{}, 8)
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
		if n := l.n.Load(); n != 0 {
			t.Errorf("%s: %d connections counted", name, n)
		}
		_ = engine.RemoveBinding(i)
	}
}
