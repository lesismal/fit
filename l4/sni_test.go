package l4_test

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/tlstest"

	_ "github.com/lesismal/fit/handler/proxy"
)

// tlsBackend is a TLS server, with a certificate for localhost, that tells
// each client its own name and then echoes.
func tlsBackend(t *testing.T, name string) string {
	t.Helper()
	server, _, err := tlstest.Configs()
	if err != nil {
		t.Fatal(err)
	}
	return backend(t, func(c net.Conn) {
		tc := tls.Server(c, server)
		_ = tc.SetDeadline(time.Now().Add(10 * time.Second))
		if tc.Handshake() != nil {
			return
		}
		_, _ = tc.Write([]byte(name + ":" + tc.ConnectionState().ServerName))
		_, _ = io.Copy(tc, tc)
	})
}

// sniConfig has one tcp listener shared by three services: a.example.com's,
// *.w.example.com's and the fallback, an echo server in the clear.
func sniConfig(t *testing.T, fallback bool) string {
	services := `
    {"name": "a", "listeners": ["tcp"], "upstream": "a", "server_names": ["a.example.com"]},
    {"name": "w", "listeners": ["tcp"], "upstream": "w", "server_names": ["*.w.example.com"]}`
	if fallback {
		services += `,
    {"name": "f", "listeners": ["tcp"], "upstream": "f"}`
	}
	return fmt.Sprintf(`{
  "listeners": [{"name": "tcp", "address": "127.0.0.1:0", "protocols": ["tcp"]}],
  "upstreams": [{"name": "a", "addresses": [%q]}, {"name": "w", "addresses": [%q]}, {"name": "f", "addresses": [%q]}],
  "services": [%s]
}`, tlsBackend(t, "a"), tlsBackend(t, "w"), backend(t, echo), services)
}

// TestSNIRouting sends TLS connections to the service their ClientHello's
// name picks, without terminating them: the backend's own certificate
// completes the handshake, and the backend sees the name. A connection that
// is not TLS goes to the fallback, or is closed when there is none.
func TestSNIRouting(t *testing.T) {
	f := startFit(t, sniConfig(t, true))
	_, client, err := tlstest.Configs()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"a.example.com":   "a:a.example.com",
		"x.w.example.com": "w:x.w.example.com",
	} {
		cfg := client.Clone()
		cfg.ServerName = name
		cfg.InsecureSkipVerify = true // the certificate is localhost's
		c := tls.Client(f.dial(0), cfg)
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, len(want))
		if _, err := io.ReadFull(c, got); err != nil || string(got) != want {
			t.Fatalf("%s: read %q, %v", name, got, err)
		}
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(c, got[:4]); err != nil || string(got[:4]) != "ping" {
			t.Fatalf("%s: echoed %q, %v", name, got[:4], err)
		}
	}
	// Not TLS: the fallback's echo, the first bytes included.
	c := f.dial(0)
	_, _ = c.Write([]byte("hello"))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "hello" {
		t.Fatalf("the fallback: read %q, %v", got, err)
	}
	// A name no service has, and no fallback: closed.
	f = startFit(t, sniConfig(t, false))
	cfg := client.Clone()
	cfg.ServerName = "b.example.com"
	tc := tls.Client(f.dial(0), cfg)
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err == nil || isTimeout(err) {
		t.Fatalf("a name no service takes: %v", err)
	}
}

// TestSNIHalfHello holds a client that has sent half a ClientHello, for at
// most OpeningTimeout: no service has taken it yet.
func TestSNIHalfHello(t *testing.T) {
	f := startFit(t, sniConfig(t, true))
	c := f.dial(0)
	_, _ = c.Write([]byte{0x16, 3, 1, 0x02, 0x00, 1, 0, 1})
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := c.Read(make([]byte, 1)); !isTimeout(err) {
		t.Fatalf("half a hello was answered: %d, %v", n, err)
	}
	if n := f.conns(); n != 0 {
		t.Fatalf("%d connections were proxied", n)
	}
}

// headerBackend reports, on each connection, the first line or v2 header it
// is sent, then echoes.
func headerBackend(t *testing.T, got chan<- string) string {
	return backend(t, func(c net.Conn) {
		r := bufio.NewReader(c)
		head, _ := r.Peek(16)
		if len(head) == 16 && strings.HasPrefix(string(head), "\r\n\r\n\x00\r\nQUIT\n") {
			body := make([]byte, 16+int(binary.BigEndian.Uint16(head[14:])))
			_, _ = io.ReadFull(r, body)
			got <- fmt.Sprintf("v2 %x", body[12:])
		} else {
			line, _ := r.ReadString('\n')
			got <- line
		}
		_, _ = io.Copy(c, r)
	})
}

// TestProxyProtocolToUpstream sends the upstream a PROXY header of each
// version, naming the client and the address it connected to, before what
// the client sends.
func TestProxyProtocolToUpstream(t *testing.T) {
	got := make(chan string, 1)
	up := headerBackend(t, got)
	for _, v := range []string{"v1", "v2"} {
		f := startFit(t, tcpConfig(`, "proxy_protocol": "`+v+`"`, up))
		c := f.dial(0)
		_, _ = c.Write([]byte("hi"))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
			t.Fatal(err)
		}
		client, fit := c.LocalAddr().(*net.TCPAddr), c.RemoteAddr().(*net.TCPAddr)
		want := fmt.Sprintf("PROXY TCP4 127.0.0.1 127.0.0.1 %d %d\r\n", client.Port, fit.Port)
		if v == "v2" {
			want = fmt.Sprintf("v2 2111000c7f0000017f000001%04x%04x", client.Port, fit.Port)
		}
		if h := <-got; h != want {
			t.Fatalf("%s: the upstream was sent %q, want %q", v, h, want)
		}
	}
}

// TestProxyProtocolFromClient takes a tcp connection's PROXY header from a
// trusted address and passes its client on, refuses a trusted connection
// without one, and serves one from elsewhere as it is.
func TestProxyProtocolFromClient(t *testing.T) {
	got := make(chan string, 1)
	up := headerBackend(t, got)
	doc := func(trusted string) string {
		return strings.Replace(tcpConfig(`, "proxy_protocol": "v1"`, up), `"protocols": ["tcp"]}`,
			`"protocols": ["tcp"], "proxy_protocol": {"trusted": ["`+trusted+`"]}}`, 1)
	}
	f := startFit(t, doc("127.0.0.0/8"))
	for _, header := range []string{
		"PROXY TCP4 203.0.113.9 198.51.100.1 5555 443\r\n",
		"\r\n\r\n\x00\r\nQUIT\n\x21\x11\x00\x0c\xcb\x00\x71\x09\xc6\x33\x64\x01\x15\xb3\x01\xbb",
	} {
		c := f.dial(0)
		// The header and the data after it in one write, and the header in
		// two writes.
		_, _ = c.Write([]byte(header[:7]))
		time.Sleep(20 * time.Millisecond)
		_, _ = c.Write([]byte(header[7:] + "hi"))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
			t.Fatal(err)
		}
		if h := <-got; h != "PROXY TCP4 203.0.113.9 198.51.100.1 5555 443\r\n" {
			t.Fatalf("the upstream was sent %q", h)
		}
	}
	c := f.dial(0)
	_, _ = c.Write([]byte("hi there, no header\r\n"))
	expectClosed(t, c, 5*time.Second)

	f = startFit(t, doc("10.0.0.0/8"))
	c = f.dial(0)
	_, _ = c.Write([]byte("hi"))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	if h := <-got; !strings.HasPrefix(h, "PROXY TCP4 127.0.0.1 127.0.0.1 ") {
		t.Fatalf("an untrusted client's own address: the upstream was sent %q", h)
	}
}

// TestProxyProtocolHTTP takes PROXY headers on a listener of HTTP: the
// client they name is the request's, as the upstream's X-Forwarded-For
// shows, and an untrusted client's own address is.
func TestProxyProtocolHTTP(t *testing.T) {
	app := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_, _ = io.WriteString(w, r.Header.Get("X-Forwarded-For"))
	}))
	t.Cleanup(app.Close)
	doc := func(trusted string) string {
		return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "proxy_protocol": {"trusted": ["` + trusted + `"]}}],
  "upstreams": [{"name": "app", "addresses": ["` + strings.TrimPrefix(app.URL, "http://") + `"]}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`
	}
	get := func(f *fit, header string) string {
		c := f.dial(0)
		_, _ = c.Write([]byte(header + "GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := stdhttp.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	f := startFit(t, doc("127.0.0.1"))
	if got := get(f, "PROXY TCP4 203.0.113.9 198.51.100.1 5555 80\r\n"); got != "203.0.113.9" {
		t.Fatalf("v1: X-Forwarded-For %q", got)
	}
	if got := get(f, "\r\n\r\n\x00\r\nQUIT\n\x21\x21\x00\x24"+
		"\x20\x01\x0d\xb8\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x09"+
		"\x20\x01\x0d\xb8\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x15\xb3\x00\x50"); got != "2001:db8::9" {
		t.Fatalf("v2: X-Forwarded-For %q", got)
	}
	f = startFit(t, doc("10.0.0.0/8"))
	if got := get(f, ""); got != "127.0.0.1" {
		t.Fatalf("untrusted: X-Forwarded-For %q", got)
	}
}
