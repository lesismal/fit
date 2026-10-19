//go:build !windows

package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/pebbletest"
)

// TestACMEFromPebble runs fit with a listener whose certificate it obtains
// from Pebble, answering the HTTP-01 challenge on its listener in the clear,
// and checks that a client asking for the name gets it, and that the
// challenge path with no challenge under way goes to the routes.
func TestACMEFromPebble(t *testing.T) {
	port := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: port})
	_, addrs := startFit(t, fmt.Sprintf(`{
  "acme": {"directory": %q, "ca_file": %q, "storage": %q, "timeout": "20s"},
  "listeners": [
    {"name": "http", "address": "127.0.0.1:%d"},
    {"name": "https", "address": "127.0.0.1:0", "tls": {"acme": ["www.fit.test"]}}
  ],
  "routes": [{"handler": {"module": "respond", "params": {"body": "hello"}}}]
}`, p.Directory, p.CAFile, t.TempDir(), port))

	tr := &stdhttp.Transport{
		TLSClientConfig: &tls.Config{RootCAs: p.Roots(t)},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addrs["https"])
		},
	}
	c := &stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}
	var resp *stdhttp.Response
	var err error
	deadline := time.Now().Add(30 * time.Second)
	for {
		// Until fit has the certificate, the handshake fails.
		if resp, err = c.Get("https://www.fit.test/"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no certificate within 30s: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello" || resp.TLS.PeerCertificates[0].VerifyHostname("www.fit.test") != nil {
		t.Errorf("%q, certificate for %v", body, resp.TLS.PeerCertificates[0].DNSNames)
	}

	resp, err = stdhttp.Get(fmt.Sprintf("http://127.0.0.1:%d/.well-known/acme-challenge/none", port))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "hello") {
		t.Errorf("a token with no challenge: %d %q", resp.StatusCode, body)
	}
}
