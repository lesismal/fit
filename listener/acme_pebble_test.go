package listener

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/pebbletest"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/tlsmgr"
)

// TestACMEHTTPChallenge obtains a certificate from Pebble, which checks the
// HTTP-01 challenge on a listener in the clear: the dispatcher answers it
// from memory, and the TLS listener then serves the certificate.
func TestACMEHTTPChallenge(t *testing.T) {
	port := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: port})
	c, err := config.Load(fmt.Appendf(nil, `{
  "acme": {"directory": %q, "ca_file": %q, "storage": %q, "timeout": "20s", "challenges": ["http-01"]},
  "listeners": [
    {"name": "http", "address": "127.0.0.1:%d"},
    {"name": "https", "address": "127.0.0.1:0", "tls": {"acme": ["www.fit.test"]}}
  ],
  "routes": [{"handler": {"module": "respond", "params": {"body": "hello"}}}]
}`, p.Directory, p.CAFile, t.TempDir(), port))
	if err != nil {
		t.Fatal(err)
	}
	acme, err := tlsmgr.NewACME(c.ACME, nil, logs.Func(t.Logf))
	if err != nil {
		t.Fatal(err)
	}
	defer acme.Close()
	g, err := runtime.Compile(c, runtime.Env{ACME: acme})
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	defer rt.Close()
	s, err := Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var addr string
	for _, a := range s.Addrs() {
		if a.Name == "https" {
			addr = a.Addr.String()
		}
	}
	client := &stdhttp.Client{Timeout: 5 * time.Second, Transport: &stdhttp.Transport{
		TLSClientConfig: &tls.Config{RootCAs: p.Roots(t)},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := client.Get("https://www.fit.test/")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "hello" {
				t.Errorf("%d %q", resp.StatusCode, body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no certificate within 30s: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
