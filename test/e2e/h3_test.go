//go:build !windows

package e2e

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	stdhttp "net/http"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/http3"

	"github.com/lesismal/fit/internal/certtest"
)

func TestHTTP3SameRoute(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	f, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "h3"],
    "tls": {"certificates": [{"cert_file": "`+cert.CertFile+`", "key_file": "`+cert.KeyFile+`"}]}}],
  "routes": [{"match": {"path": ["/hello"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}`)
	addr := addrs["web"]
	_, port, _ := net.SplitHostPort(addr)

	// Over h2: the route, and Alt-Svc pointing at HTTP/3 on the same port.
	tr := &stdhttp.Transport{
		TLSClientConfig: &tls.Config{RootCAs: certtest.Pool(cert)},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		Protocols: new(stdhttp.Protocols),
	}
	tr.Protocols.SetHTTP2(true)
	resp, err := (&stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}).Get("https://a.example/hello")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.ProtoMajor != 2 || string(body) != "hello" || resp.Header.Get("Alt-Svc") != `h3=":`+port+`"; ma=86400` {
		t.Fatalf("over h2: %s %q, Alt-Svc %q", resp.Proto, body, resp.Header.Get("Alt-Svc"))
	}

	// Over h3, where Alt-Svc said.
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run() }()
	defer func() { engine.Stop(); <-done; _ = engine.Close() }()
	cfg := http3.DefaultClientConfig()
	cfg.TLSConfig = &tls.Config{RootCAs: certtest.Pool(cert), ServerName: "a.example"}
	cfg.Timeout = 5 * time.Second
	h3 := http3.NewClient(engine, cfg)
	defer h3.Close()
	req, _ := stdhttp.NewRequest("GET", "https://"+addr+"/hello", nil)
	resp, err = h3.Go(req).Wait()
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	if resp.ProtoMajor != 3 || resp.StatusCode != 200 || string(body) != "hello" {
		t.Fatalf("over h3: %s %d %q", resp.Proto, resp.StatusCode, body)
	}
	if code, out := f.stop(t); code != 0 {
		t.Fatalf("stop: exit %d, %q", code, out)
	}
}
