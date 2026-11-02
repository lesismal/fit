//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
	"github.com/lesismal/fit/tunnel/internal/wscrypt"
)

// proxyGet gets / through the web proxy on addr, on a connection of its own.
func proxyGet(addr string) (int, string, error) {
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get("http://" + addr + "/")
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), err
}

// closedSoon dials addr and expects the connection closed without a byte.
func closedSoon(t *testing.T, addr string) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if n, err := c.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("%s: read %d, %v", addr, n, err)
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

func setProxies(proxies ...map[string]any) func(common.Member) error {
	return func(m common.Member) error { return m.Set("web_proxies", proxies) }
}

// A web proxy serves the hub's pages, takes a new password and a new
// limit for the connections it accepts from then on, and closes its
// connections when it is removed.
func TestWebProxy(t *testing.T) {
	listen, sock, fwd, proxy, con := freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)
	h := start(t, hubDoc(t, listen, sock, tunneltest.Echo(t, ""), fwd, proxy, con, ""))
	var body string
	waitFor(t, "the page through the proxy", func() bool {
		code, b, err := proxyGet(proxy)
		body = b
		return err == nil && code == 200
	})
	if !strings.Contains(body, "core/workbench.js") {
		t.Fatalf("the page: %s", body)
	}
	url := "ws://" + sock + "/"
	pm := func(password string, conns int) map[string]any {
		return map[string]any{"name": "hub", "listen": proxy, "url": url, "password": password, "max_conns": conns}
	}

	// Another password: the hub opens nothing the proxy sends.
	if err := h.editProxy(setProxies(pm("wrong", 0))); err != nil {
		t.Fatal(err)
	}
	if code, _, err := proxyGet(proxy); err == nil {
		t.Fatalf("a wrong password got %d", code)
	}

	// One connection at most: a second is closed at once.
	if err := h.editProxy(setProxies(pm("sock", 1))); err != nil {
		t.Fatal(err)
	}
	first, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	waitFor(t, "the first connection", func() bool { v := h.c.WebProxies(); return len(v) == 1 && v[0].Conns == 1 })
	closedSoon(t, proxy)

	// Removed, it closes the connection it has.
	if err := h.editProxy(setProxies()); err != nil {
		t.Fatal(err)
	}
	_ = first.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("the connection of a removed proxy: %v", err)
	}
	if v := h.c.WebProxies(); len(v) != 0 {
		t.Fatalf("web proxies %+v", v)
	}

	// A socket nothing listens on: each connection is closed.
	if err := h.editProxy(setProxies(map[string]any{"listen": proxy, "url": "ws://" + freeAddr(t) + "/", "password": "sock"})); err != nil {
		t.Fatal(err)
	}
	closedSoon(t, proxy)
}

// A web proxy whose URL cannot be dialed closes each connection.
func TestWebProxyBadURL(t *testing.T) {
	c := New(&app.Host{ID: ID, Engine: tunneltest.Engine(t)})
	defer c.Stop(context.Background())
	addr := freeAddr(t)
	if err := c.Apply(&config.TunnelProxy{Name: "me", WebProxies: []config.TunnelWebProxy{{Listen: addr, URL: "http://127.0.0.1:1/", Password: "p"}}}); err != nil {
		t.Fatal(err)
	}
	closedSoon(t, addr)
}

type nopHandler struct{}

func (nopHandler) OnOpen(*fib.Connection)                 {}
func (nopHandler) OnData(*fib.Connection, []byte)         {}
func (nopHandler) OnPriorityData(*fib.Connection, []byte) {}
func (nopHandler) OnClose(*fib.Connection, error)         {}

// dialer dials, on e, connections whose peers the test reads.
type dialer struct {
	t       *testing.T
	e       *fib.Engine
	stalled <-chan net.Conn
	addr    string
}

func newDialer(t *testing.T) *dialer {
	stalled, addr := tunneltest.Stall(t)
	return &dialer{t: t, e: tunneltest.Engine(t), stalled: stalled, addr: addr}
}

// dial is a connection and its peer.
func (d *dialer) dial() (*fib.Connection, net.Conn) {
	d.t.Helper()
	got := make(chan *fib.Connection, 1)
	if err := d.e.DialWithHandler("tcp", d.addr, 5*time.Second, nopHandler{}, func(c *fib.Connection, err error) {
		if err != nil {
			d.t.Error(err)
		}
		got <- c
	}); err != nil {
		d.t.Fatal(err)
	}
	c := <-got
	if c == nil {
		d.t.FailNow()
	}
	d.t.Cleanup(func() { _ = c.Close() })
	peer := <-d.stalled
	_ = peer.SetDeadline(time.Now().Add(10 * time.Second))
	return c, peer
}

func readN(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func expectEOF(t *testing.T, c net.Conn) {
	t.Helper()
	if n, err := c.Read(make([]byte, 64)); err == nil || isTimeout(err) {
		t.Fatalf("read %d, %v", n, err)
	}
}

// The handlers of a proxy's two connections, driven one step at a time:
// what the local connection sends before the session is up waits for it,
// and then goes sealed, in order; what the socket sends is opened, and one
// message that does not open ends it.
func TestProxyConnSession(t *testing.T) {
	d := newDialer(t)
	secret, _ := wscrypt.NewSecret("sock")
	p := &webProxy{c: &Proxy{}, conns: map[*fib.Connection]*proxyConn{}}
	p.set.Store(&proxyCfg{cfg: &config.TunnelWebProxy{Listen: "local"}, secret: secret})

	local, localPeer := d.dial()
	// A connection the proxy does not know.
	p.OnData(local, []byte("x"))
	p.OnClose(local, nil)
	p.OnPriorityData(local, nil)

	pc := &proxyConn{p: p, local: local, lgate: common.NewGate(local), secret: secret}
	local.SetAttachment(pc)
	p.OnData(local, []byte("early"))
	if len(pc.pending) != 1 {
		t.Fatalf("pending %q", pc.pending)
	}

	ws, wsPeer := d.dial()
	w := &proxyWS{pc: pc}
	w.OnOpen(ws)
	w.OnPriorityData(ws, nil)
	serverHello := wscrypt.Hello()
	w.OnData(ws, serverHello)
	p.OnData(local, []byte("later"))

	clientHello := readN(t, wsPeer, wscrypt.HelloLen)
	srv, err := wscrypt.NewSession(secret, serverHello, clientHello, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"early", "later"} {
		got, err := srv.Open(readN(t, wsPeer, len(want)+16))
		if err != nil || string(got) != want {
			t.Fatalf("%q, %v", got, err)
		}
	}

	w.OnData(ws, srv.Seal([]byte("reply")))
	if got := readN(t, localPeer, 5); string(got) != "reply" {
		t.Fatalf("%q", got)
	}
	w.OnData(ws, []byte("not sealed"))
	expectEOF(t, wsPeer)
	w.OnClose(ws, nil)
	expectEOF(t, localPeer)
}

// What arrives for a proxy connection closed already is dropped, and a
// hello that is not one ends the socket.
func TestProxyConnClosed(t *testing.T) {
	d := newDialer(t)
	secret, _ := wscrypt.NewSecret("sock")
	p := &webProxy{c: &Proxy{}, conns: map[*fib.Connection]*proxyConn{}}
	p.set.Store(&proxyCfg{cfg: &config.TunnelWebProxy{Listen: "local"}, secret: secret})

	local, _ := d.dial()
	pc := &proxyConn{p: p, local: local, secret: secret, closed: true}
	local.SetAttachment(pc)
	p.OnData(local, []byte("x"))
	if len(pc.pending) != 0 {
		t.Fatal("a closed connection kept data")
	}
	ws, wsPeer := d.dial()
	w := &proxyWS{pc: pc}
	w.OnOpen(ws)
	expectEOF(t, wsPeer)
	w.OnData(ws, wscrypt.Hello())
	if pc.sess != nil {
		t.Fatal("a closed connection's session opened")
	}

	local2, _ := d.dial()
	ws2, ws2Peer := d.dial()
	w2 := &proxyWS{pc: &proxyConn{p: p, local: local2, secret: secret}}
	w2.OnOpen(ws2)
	w2.OnData(ws2, []byte("short"))
	expectEOF(t, ws2Peer)
}
