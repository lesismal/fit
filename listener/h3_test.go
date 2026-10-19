package listener

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strconv"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/http3"

	"github.com/lesismal/fit/internal/certtest"
)

// h3Get fetches target over HTTP/3, trusting cert, with name as the TLS
// server name.
func h3Get(t *testing.T, target, name string, cert *certtest.Cert) (*stdhttp.Response, string, error) {
	t.Helper()
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run() }()
	defer func() {
		engine.Stop()
		<-done
		_ = engine.Close()
	}()
	cfg := http3.DefaultClientConfig()
	cfg.TLSConfig = &tls.Config{RootCAs: certtest.Pool(cert), ServerName: name}
	cfg.Timeout = 5 * time.Second
	c := http3.NewClient(engine, cfg)
	defer c.Close()
	req, _ := stdhttp.NewRequest("GET", target, nil)
	resp, err := c.Go(req).Wait()
	if err != nil {
		return nil, "", err
	}
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body), nil
}

func h3Doc(protocols string, c *certtest.Cert) string {
	return fmt.Sprintf(`{"listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": %s,
    "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}],
  "routes": [{"match": {"path": ["/"]}, "handler": {"module": "respond", "params": {"body": "ok"}}}]}`, protocols, c.CertFile, c.KeyFile)
}

func TestHTTP3(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_, s := start(t, h3Doc(`["http/1.1", "h2", "h3"]`, cert))
	a := s.Addrs()[0]
	port := a.Addr.(*net.TCPAddr).Port
	if a.UDPAddr == nil || a.UDPAddr.(*net.UDPAddr).Port != port {
		t.Fatalf("HTTP/3 on %v, TCP on %v", a.UDPAddr, a.Addr)
	}
	resp, body, err := h3Get(t, "https://127.0.0.1:"+strconv.Itoa(port)+"/", "a.example", cert)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || body != "ok" || resp.ProtoMajor != 3 {
		t.Fatalf("over h3: %d %s %q", resp.StatusCode, resp.Proto, body)
	}
	want := `h3=":` + strconv.Itoa(port) + `"; ma=86400`
	for _, h2 := range []bool{false, true} {
		for _, path := range []string{"/", "/missing"} {
			resp, err := tlsClient(a.Addr.String(), h2, cert).Get("https://a.example" + path)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got := resp.Header.Get("Alt-Svc"); got != want {
				t.Errorf("h2=%v %s: Alt-Svc %q, want %q", h2, path, got, want)
			}
		}
	}
}

func TestHTTP3Only(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_, s := start(t, h3Doc(`["h3"]`, cert))
	a := s.Addrs()[0]
	udp, ok := a.Addr.(*net.UDPAddr)
	if !ok || a.UDPAddr == nil {
		t.Fatalf("addrs %+v", a)
	}
	resp, body, err := h3Get(t, "https://"+udp.String()+"/", "a.example", cert)
	if err != nil || resp.StatusCode != 200 || body != "ok" {
		t.Fatalf("over h3: %v %v %q", err, resp, body)
	}
	if conn, err := net.DialTimeout("tcp", udp.String(), time.Second); err == nil {
		conn.Close()
		t.Error("an h3-only listener takes TCP connections")
	}
}

func TestNoAltSvcWithoutH3(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	_, s := start(t, h3Doc(`["http/1.1", "h2"]`, cert))
	resp, err := tlsClient(s.Addrs()[0].Addr.String(), true, cert).Get("https://a.example/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Alt-Svc"); got != "" || s.Addrs()[0].UDPAddr != nil {
		t.Errorf("Alt-Svc %q without h3", got)
	}
}

func TestReloadDropsHTTP3(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_, s := start(t, h3Doc(`["http/1.1", "h3"]`, cert))
	s.SetDrainTimeout(time.Second)
	udp := s.Addrs()[0].UDPAddr.String()
	if _, _, err := s.Reload(compile(t, h3Doc(`["http/1.1"]`, cert))); err != nil {
		t.Fatal(err)
	}
	// The UDP port is let go once the old binding drains.
	deadline := time.Now().Add(5 * time.Second)
	for {
		pc, err := net.ListenPacket("udp", udp)
		if err == nil {
			pc.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP/3 still holds %s: %v", udp, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAltSvcLeavesSharedHeaders(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	// One respond module, whose response header is shared by every request,
	// behind a listener with h3 and one without.
	_, s := start(t, fmt.Sprintf(`{"listeners": [
    {"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h3"], "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}},
    {"name": "plain", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok", "headers": {"X-A": "1"}}}}]}`, cert.CertFile, cert.KeyFile))
	resp, err := tlsClient(addrOf(s, "web"), false, cert).Get("https://a.example/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Alt-Svc") == "" || resp.Header.Get("X-A") != "1" {
		t.Fatalf("web: %v", resp.Header)
	}
	resp, err = client(false).Get("http://" + addrOf(s, "plain") + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Alt-Svc") != "" || resp.Header.Get("X-A") != "1" {
		t.Fatalf("plain picked up web's Alt-Svc: %v", resp.Header)
	}
}
