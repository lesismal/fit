package l4_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/tlstest"

	"github.com/lesismal/fit/internal/certtest"
)

// TestTLSTerminatedWithoutNames serves a listener of HTTPS and tcp whose
// service has no server_names: TLS connections are served HTTPS by the
// listener, and the rest go to the service.
func TestTLSTerminatedWithoutNames(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	f := startFit(t, fmt.Sprintf(`{
  "listeners": [{"name": "one", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "tcp"],
    "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}],
  "upstreams": [{"name": "f", "addresses": [%q]}],
  "services": [{"name": "f", "listeners": ["one"], "upstream": "f"}],
  "routes": [{"listeners": ["one"], "handler": {"module": "respond", "params": {"body": "site"}}}]
}`, cert.CertFile, cert.KeyFile, backend(t, echo)))
	tc := tls.Client(f.dial(0), &tls.Config{ServerName: "a.example", RootCAs: certtest.Pool(cert)})
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = tc.Write([]byte("GET / HTTP/1.1\r\nHost: a.example\r\n\r\n"))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "site" {
		t.Fatalf("HTTPS: %q", body)
	}
	c := f.dial(0)
	_, _ = c.Write([]byte("hello"))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "hello" {
		t.Fatalf("the service: read %q, %v", got, err)
	}
}

// TestProxyHeaderThenHello takes a PROXY header on a listener that routes by
// SNI, and the ClientHello only after it, in a write of its own.
func TestProxyHeaderThenHello(t *testing.T) {
	doc := strings.Replace(sniConfig(t, true), `"protocols": ["tcp"]}`,
		`"protocols": ["tcp"], "proxy_protocol": {"trusted": ["127.0.0.0/8"]}}`, 1)
	f := startFit(t, doc)
	_, client, err := tlstest.Configs()
	if err != nil {
		t.Fatal(err)
	}
	c := f.dial(0)
	_, _ = c.Write([]byte("PROXY TCP4 203.0.113.9 198.51.100.1 5555 443\r\n"))
	time.Sleep(20 * time.Millisecond)
	cfg := client.Clone()
	cfg.ServerName = "a.example.com"
	cfg.InsecureSkipVerify = true // the certificate is localhost's
	tc := tls.Client(c, cfg)
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	const want = "a:a.example.com"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(tc, got); err != nil || string(got) != want {
		t.Fatalf("read %q, %v", got, err)
	}
}

// TestProxyProtocolHTTPEdges takes PROXY headers on a listener of HTTP in
// two writes, closes one that sends a bad header, and, on shutdown, closes
// both a connection whose header has not come and an idle one.
func TestProxyProtocolHTTPEdges(t *testing.T) {
	f := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "proxy_protocol": {"trusted": ["127.0.0.1"]}}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]
}`)
	request := func(c net.Conn) {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := stdhttp.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "ok" {
			t.Fatalf("body %q", body)
		}
	}
	// The header in two writes.
	idle := f.dial(0)
	_, _ = idle.Write([]byte("PROXY TCP4 203.0.113.9 "))
	time.Sleep(20 * time.Millisecond)
	_, _ = idle.Write([]byte("198.51.100.1 5555 80\r\nGET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	request(idle)

	// A request, then a half-close: the connection ends.
	half := f.dial(0)
	_, _ = half.Write([]byte("PROXY TCP4 203.0.113.9 198.51.100.1 5555 80\r\nGET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	request(half)
	_ = half.CloseWrite()
	expectClosed(t, half, 5*time.Second)

	// A half-close before the header comes.
	early := f.dial(0)
	_, _ = early.Write([]byte("PROXY "))
	_ = early.CloseWrite()

	bad := f.dial(0)
	_, _ = bad.Write([]byte("PROXY BAD HEADER\r\n"))
	expectClosed(t, bad, 5*time.Second)

	waiting := f.dial(0)
	_, _ = waiting.Write([]byte("PROXY "))
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	expectClosed(t, waiting, 5*time.Second)
	expectClosed(t, idle, 5*time.Second)
}
