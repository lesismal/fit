//go:build !windows

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
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

	if body := getObtained(t, p, addrs["https"], "www.fit.test"); body != "hello" {
		t.Errorf("%q", body)
	}

	resp, err := stdhttp.Get(fmt.Sprintf("http://127.0.0.1:%d/.well-known/acme-challenge/none", port))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "hello") {
		t.Errorf("a token with no challenge: %d %q", resp.StatusCode, body)
	}
}

// getObtained fetches https://name/ from addr, trusting Pebble's roots, until
// fit has the certificate, and returns the body; the handshake fails until
// then.
func getObtained(t *testing.T, p *pebbletest.Pebble, addr, name string) string {
	t.Helper()
	tr := &stdhttp.Transport{
		TLSClientConfig: &tls.Config{RootCAs: p.Roots(t)},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	c := &stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := c.Get("https://" + name + "/")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err := resp.TLS.PeerCertificates[0].VerifyHostname(name); err != nil {
				t.Errorf("certificate for %v", resp.TLS.PeerCertificates[0].DNSNames)
			}
			return string(body)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no certificate within 30s: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestACMETLSALPNFromPebble runs fit with only a TLS listener, on the port
// Pebble checks TLS-ALPN-01 challenges on, so that fib's TLS answers the
// challenge, and no listener in the clear.
func TestACMETLSALPNFromPebble(t *testing.T) {
	port := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: pebbletest.FreePort(t), TLSPort: port})
	_, addrs := startFit(t, fmt.Sprintf(`{
  "acme": {"directory": %q, "ca_file": %q, "storage": %q, "timeout": "20s", "challenges": ["tls-alpn-01"]},
  "listeners": [
    {"name": "https", "address": "127.0.0.1:%d", "tls": {"acme": ["alpn.fit.test"]}}
  ],
  "routes": [{"handler": {"module": "respond", "params": {"body": "over alpn"}}}]
}`, p.Directory, p.CAFile, t.TempDir(), port))
	if body := getObtained(t, p, addrs["https"], "alpn.fit.test"); body != "over alpn" {
		t.Errorf("%q", body)
	}
}

// TestOnDemandFromPebble runs fit with a listener that obtains certificates
// during handshakes, as an ask URL allows: a client asking for an allowed
// name gets the page over a certificate obtained then, and one asking for a
// refused name fails its handshake.
func TestOnDemandFromPebble(t *testing.T) {
	port := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: port})
	ask := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Query().Get("domain") != "yes.fit.test" {
			stdhttp.Error(w, "no", stdhttp.StatusForbidden)
		}
	}))
	defer ask.Close()
	_, addrs := startFit(t, fmt.Sprintf(`{
  "acme": {"directory": %q, "ca_file": %q, "storage": %q, "challenges": ["http-01"],
    "on_demand": {"ask": %q, "timeout": "20s"}},
  "listeners": [
    {"name": "http", "address": "127.0.0.1:%d"},
    {"name": "https", "address": "127.0.0.1:0", "tls": {"on_demand": true}}
  ],
  "routes": [{"handler": {"module": "respond", "params": {"body": "on demand"}}}]
}`, p.Directory, p.CAFile, t.TempDir(), ask.URL, port))
	if body := getObtained(t, p, addrs["https"], "yes.fit.test"); body != "on demand" {
		t.Errorf("%q", body)
	}
	conn, err := tls.Dial("tcp", addrs["https"], &tls.Config{RootCAs: p.Roots(t), ServerName: "no.fit.test"})
	if err == nil {
		conn.Close()
		t.Error("a handshake for a refused name succeeded")
	}
}

// TestAutoHTTPSFromPebble runs fit with a config that names only a host,
// and Pebble's address and ports: HTTPS by default serves it over HTTPS with
// a certificate from Pebble, obtained through HTTP-01 on the listener in
// the clear it makes, which redirects every other request to HTTPS.
func TestAutoHTTPSFromPebble(t *testing.T) {
	httpPort, httpsPort := pebbletest.FreePort(t), pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: httpPort, TLSPort: httpsPort})
	_, addrs := startFit(t, fmt.Sprintf(`{
  "acme": {"directory": %q, "ca_file": %q, "storage": %q},
  "auto_https": {"http_port": %d, "https_port": %d},
  "routes": [{"match": {"host": ["auto.fit.test"]}, "handler": {"module": "respond", "params": {"body": "by default"}}}]
}`, p.Directory, p.CAFile, t.TempDir(), httpPort, httpsPort))
	if body := getObtained(t, p, addrs["https"], "auto.fit.test"); body != "by default" {
		t.Errorf("%q", body)
	}
	c := &stdhttp.Client{Timeout: 5 * time.Second, CheckRedirect: func(*stdhttp.Request, []*stdhttp.Request) error {
		return stdhttp.ErrUseLastResponse
	}}
	req, _ := stdhttp.NewRequest("GET", "http://"+addrs["http"]+"/page", nil)
	req.Host = "auto.fit.test"
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if want := fmt.Sprintf("https://auto.fit.test:%d/page", httpsPort); resp.StatusCode != stdhttp.StatusPermanentRedirect ||
		resp.Header.Get("Location") != want {
		t.Errorf("http: %d %q, want %q", resp.StatusCode, resp.Header.Get("Location"), want)
	}
}

// TestLetsEncryptStaging obtains a certificate from Let's Encrypt's staging
// CA, with a config that names only a host. It is run by hand, on a host
// that the name resolves to from the internet, and that can listen on ports
// 80 and 443 (as root, or with CAP_NET_BIND_SERVICE):
//
//	FIT_LE_STAGING_NAME=test.example.com go test ./test/e2e -run TestLetsEncryptStaging -v -timeout 10m
//
// FIT_LE_STAGING_EMAIL, if set, is the account's address. Staging
// certificates chain to roots no system trusts, so the test checks the
// issuer's name instead of the chain.
func TestLetsEncryptStaging(t *testing.T) {
	name := os.Getenv("FIT_LE_STAGING_NAME")
	if name == "" {
		t.Skip("FIT_LE_STAGING_NAME is not set; see the test's comment")
	}
	_, addrs := startFit(t, fmt.Sprintf(`{
  "acme": {"directory": "https://acme-staging-v02.api.letsencrypt.org/directory", "email": %q, "storage": %q},
  "routes": [{"match": {"host": [%q]}, "handler": {"module": "respond", "params": {"body": "staging"}}}]
}`, os.Getenv("FIT_LE_STAGING_EMAIL"), t.TempDir(), name))
	deadline := time.Now().Add(5 * time.Minute)
	for {
		conn, err := tls.Dial("tcp", addrs["https"], &tls.Config{ServerName: name, InsecureSkipVerify: true})
		if err == nil {
			leaf := conn.ConnectionState().PeerCertificates[0]
			conn.Close()
			if err := leaf.VerifyHostname(name); err == nil {
				if !strings.Contains(leaf.Issuer.String(), "STAGING") {
					t.Errorf("issued by %s, not Let's Encrypt's staging CA", leaf.Issuer)
				}
				t.Logf("certificate for %v from %s, valid until %s", leaf.DNSNames, leaf.Issuer, leaf.NotAfter)
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no certificate within 5 minutes: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}

// TestACMESharedStorage runs two instances of fit on one shared storage,
// in Redis, etcd and Consul in turn, as on two hosts: Pebble reaches only
// the first, on its HTTP-01 port. The second obtains a name only it
// manages, the first answering the challenge from the storage, and a name
// both manage is obtained once and served by both.
func TestACMESharedStorage(t *testing.T) {
	servers := map[string]func(t *testing.T) string{
		"redis": func(t *testing.T) string {
			return fmt.Sprintf(`{"redis": {"address": %q}}`, startServer(t, "redis:8.2.2", 6379))
		},
		"etcd": func(t *testing.T) string {
			ep := "http://" + startServer(t, etcdImage, 2379, "etcd",
				"--listen-client-urls", "http://0.0.0.0:2379", "--advertise-client-urls", "http://127.0.0.1:2379")
			return fmt.Sprintf(`{"etcd": {"endpoints": [%q]}}`, ep)
		},
		"consul": func(t *testing.T) string {
			ep := "http://" + startServer(t, consulImage, 8500, "agent", "-dev", "-client", "0.0.0.0")
			return fmt.Sprintf(`{"consul": {"endpoint": %q}}`, ep)
		},
	}
	for _, kind := range []string{"redis", "etcd", "consul"} {
		t.Run(kind, func(t *testing.T) {
			storage := servers[kind](t)
			port := pebbletest.FreePort(t)
			p := pebbletest.Start(t, pebbletest.Options{HTTPPort: port})
			acme := fmt.Sprintf(`{"directory": %q, "ca_file": %q, "timeout": "20s", "challenges": ["http-01"], "shared_storage": %s}`, p.Directory, p.CAFile, storage)
			// The store may take a moment to answer once it has started;
			// fit tries again. Each instance has two listeners.
			first, addrs1 := startFitWant(t, fmt.Sprintf(`{
  "acme": %s,
  "listeners": [
    {"name": "http", "address": "127.0.0.1:%d"},
    {"name": "https", "address": "127.0.0.1:0", "tls": {"acme": ["both.fit.test"]}}
  ],
  "routes": [{"handler": {"module": "respond", "params": {"body": "first"}}}]
}`, acme, port), 2)
			second, addrs2 := startFitWant(t, fmt.Sprintf(`{
  "acme": %s,
  "listeners": [
    {"name": "http", "address": "127.0.0.1:0"},
    {"name": "https", "address": "127.0.0.1:0", "tls": {"acme": ["both.fit.test", "b.fit.test"]}}
  ],
  "routes": [{"handler": {"module": "respond", "params": {"body": "second"}}}]
}`, acme), 2)
			for _, f := range []*fit{first, second} {
				go func() {
					for range f.lines {
					}
				}()
			}
			if body := getObtained(t, p, addrs2["https"], "b.fit.test"); body != "second" {
				t.Errorf("b.fit.test: %q", body)
			}
			leaf1 := servedLeaf(t, p, addrs1["https"], "both.fit.test")
			leaf2 := servedLeaf(t, p, addrs2["https"], "both.fit.test")
			if !leaf1.Equal(leaf2) {
				t.Errorf("the instances serve different certificates for both.fit.test: serials %s and %s", leaf1.SerialNumber, leaf2.SerialNumber)
			}
			logs := first.stderr.String() + second.stderr.String()
			if n := strings.Count(logs, "both.fit.test: obtained a certificate"); n != 1 {
				t.Errorf("both.fit.test obtained %d times:\n%s", n, logs)
			}
			if n := strings.Count(logs, "b.fit.test: obtained a certificate"); n != 1 {
				t.Errorf("b.fit.test obtained %d times:\n%s", n, logs)
			}
		})
	}
}

// servedLeaf returns the certificate addr serves for name, once it has
// one Pebble's roots verify.
func servedLeaf(t *testing.T, p *pebbletest.Pebble, addr, name string) *x509.Certificate {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, &tls.Config{RootCAs: p.Roots(t), ServerName: name})
		if err == nil {
			leaf := conn.ConnectionState().PeerCertificates[0]
			conn.Close()
			return leaf
		}
		if time.Now().After(deadline) {
			t.Fatalf("no certificate for %s within 30s: %v", name, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
