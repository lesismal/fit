// Package pebbletest runs Pebble, Let's Encrypt's ACME test server, with its
// DNS test server answering 127.0.0.1 for every name, for tests that obtain
// certificates.
//
// The binaries are found in $FIT_PEBBLE_BIN, or on PATH; a test without them
// is skipped, or fails when $FIT_PEBBLE_REQUIRED is set, as in CI's M2 exit
// tests. Install them with:
//
//	go install github.com/letsencrypt/pebble/v2/cmd/pebble@v2.10.1
//	go install github.com/letsencrypt/pebble/v2/cmd/pebble-challtestsrv@v2.10.1
package pebbletest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

// Version is the Pebble the tests are written against.
const Version = "v2.10.1"

// Pebble is a running Pebble.
type Pebble struct {
	// Directory is the URL of its ACME directory, and CAFile the PEM file of
	// the certificate it serves it with.
	Directory string
	CAFile    string
	// HTTPPort and TLSPort are the ports it checks HTTP-01 and TLS-ALPN-01
	// challenges on.
	HTTPPort, TLSPort int

	mgmt   string
	client *http.Client
}

// Options are what a test sets of Pebble.
type Options struct {
	// HTTPPort is the port Pebble checks HTTP-01 challenges on, at 127.0.0.1,
	// and TLSPort the one it checks TLS-ALPN-01 challenges on; a free port
	// when zero.
	HTTPPort, TLSPort int
	// Validity is how long the certificates it issues are valid; 90 days
	// when zero.
	Validity time.Duration
}

// handed holds the ports FreePort has returned.
var handed struct {
	sync.Mutex
	ports map[int]bool
}

// FreePort returns a TCP port on 127.0.0.1 that nothing listens on, and
// that it has not returned before: a port it returned may not be listened
// on yet, as when a process started with it has yet to bind it, and the
// system would hand it out again.
func FreePort(t testing.TB) int {
	t.Helper()
	for range 100 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		handed.Lock()
		if !handed.ports[port] {
			if handed.ports == nil {
				handed.ports = map[int]bool{}
			}
			handed.ports[port] = true
			handed.Unlock()
			return port
		}
		handed.Unlock()
	}
	t.Fatal("no free port that was not returned before")
	return 0
}

// binary returns the path of name, or skips t.
func binary(t testing.TB, name string) string {
	t.Helper()
	if dir := os.Getenv("FIT_PEBBLE_BIN"); dir != "" {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("FIT_PEBBLE_REQUIRED") != "" {
			t.Fatalf("%s not found in $FIT_PEBBLE_BIN or on PATH, and $FIT_PEBBLE_REQUIRED is set", name)
		}
		t.Skipf("%s not found in $FIT_PEBBLE_BIN or on PATH; see package pebbletest", name)
	}
	return p
}

// Start runs Pebble and its DNS server for the length of t.
func Start(t testing.TB, o Options) *Pebble {
	t.Helper()
	pebble, challtestsrv := binary(t, "pebble"), binary(t, "pebble-challtestsrv")
	dir := t.TempDir()

	dns := fmt.Sprintf("127.0.0.1:%d", FreePort(t))
	run(t, challtestsrv, nil, "-defaultIPv4", "127.0.0.1", "-defaultIPv6", "", "-dnsserver", dns,
		"-management", fmt.Sprintf("127.0.0.1:%d", FreePort(t)), "-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", "")

	cert, err := certtest.Write(dir, "127.0.0.1", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	validity := uint64(o.Validity / time.Second)
	if validity == 0 {
		validity = 90 * 24 * 3600
	}
	if o.TLSPort == 0 {
		o.TLSPort = FreePort(t)
	}
	listen, mgmt := fmt.Sprintf("127.0.0.1:%d", FreePort(t)), fmt.Sprintf("127.0.0.1:%d", FreePort(t))
	cfg := map[string]any{"pebble": map[string]any{
		"listenAddress":           listen,
		"managementListenAddress": mgmt,
		"certificate":             cert.CertFile,
		"privateKey":              cert.KeyFile,
		"httpPort":                o.HTTPPort,
		"tlsPort":                 o.TLSPort,
		"ocspResponderURL":        "",
		"retryAfter":              map[string]int{"authz": 1, "order": 1},
		"keyAlgorithm":            "ecdsa",
		"profiles":                map[string]any{"default": map[string]any{"description": "fit's tests", "validityPeriod": validity}},
	}}
	data, _ := json.Marshal(cfg)
	cfgFile := filepath.Join(dir, "pebble.json")
	if err := os.WriteFile(cfgFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// No random sleeps before validating, and no nonces refused at random:
	// the tests are about fit, not about its retries of a bad nonce.
	run(t, pebble, []string{"PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0"}, "-config", cfgFile, "-dnsserver", dns)

	p := &Pebble{Directory: "https://" + listen + "/dir", CAFile: cert.CertFile, HTTPPort: o.HTTPPort, TLSPort: o.TLSPort, mgmt: "https://" + mgmt,
		client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: certtest.Pool(cert)}}}}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := p.client.Get(p.Directory)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pebble did not come up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// run starts a command that lives as long as t.
func run(t testing.TB, name string, env []string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	if testing.Verbose() {
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
}

// SetRenewalInfo has Pebble suggest, for the certificate in certPEM, that it
// be renewed between start and end (ACME Renewal Information).
func (p *Pebble) SetRenewalInfo(t testing.TB, certPEM []byte, start, end time.Time) {
	t.Helper()
	window, _ := json.Marshal(map[string]any{"suggestedWindow": map[string]time.Time{"start": start, "end": end}})
	body, _ := json.Marshal(map[string]string{"Certificate": string(certPEM), "ARIResponse": string(window)})
	resp, err := p.client.Post(p.mgmt+"/set-renewal-info/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("set-renewal-info: %s %s", resp.Status, msg)
	}
}

// Roots returns the roots the certificates Pebble issues chain to.
func (p *Pebble) Roots(t testing.TB) *x509.CertPool {
	t.Helper()
	resp, err := p.client.Get(p.mgmt + "/roots/0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		t.Fatalf("pebble's root is not PEM: %q", data)
	}
	return pool
}
