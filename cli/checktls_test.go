package cli

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/certtest"
)

// startTLSCheckFake starts a fake Admin API over TLS with cert, and returns
// it and the host and port it listens on.
func startTLSCheckFake(t *testing.T, cert *certtest.Cert) (*checkFake, string) {
	t.Helper()
	f := &checkFake{checks: []*checkResult{checkAnswer(1)}}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	pair, err := tls.LoadX509KeyPair(cert.CertFile, cert.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return f, u.Host
}

// TestCheckTLS reaches an Admin API served over TLS, as admin.cert_file
// makes it: the certificate verified with admin.cert_file itself, or with
// -cacert, and for -servername when the address's host is not a name the
// certificate covers.
func TestCheckTLS(t *testing.T) {
	dir := t.TempDir()
	cert, err := certtest.Write(dir, "localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	f, addr := startTLSCheckFake(t, cert)
	cfg := writeCheckBoot(t, `{"address": `+js(addr)+`, "cert_file": `+js(cert.CertFile)+`, "key_file": `+js(cert.KeyFile)+`}`)
	if code, out, stderr := runFitCheck(t, "", "-c", cfg); code != 0 || !strings.Contains(out, "drift: 0") {
		t.Fatalf("over TLS: %d %q %q", code, out, stderr)
	}
	if gets, _, _, _ := f.sent(); gets != 1 {
		t.Errorf("requests: %d", gets)
	}

	// A certificate for a name alone: the address's IP does not verify;
	// -servername names what does, and -cacert gives the CA when
	// admin.cert_file is not readable here.
	named, err := certtest.Write(t.TempDir(), "admin.fit.example")
	if err != nil {
		t.Fatal(err)
	}
	_, addr = startTLSCheckFake(t, named)
	cfg = writeCheckBoot(t, `{"address": `+js(addr)+`, "cert_file": "/nonexistent/admin.pem", "key_file": "/nonexistent/admin.key"}`)
	if code, _, stderr := runFitCheck(t, "", "-c", cfg, "-cacert", named.CertFile); code != 2 || !strings.Contains(stderr, "certificate") {
		t.Errorf("an address the certificate does not cover: %d %q", code, stderr)
	}
	if code, out, stderr := runFitCheck(t, "", "-c", cfg, "-cacert", named.CertFile, "-servername", "admin.fit.example"); code != 0 || !strings.Contains(out, "drift: 0") {
		t.Errorf("-servername: %d %q %q", code, out, stderr)
	}
	if code, _, stderr := runFitCheck(t, "", "-c", cfg, "-cacert", filepath.Join(dir, "missing.pem")); code != 2 || !strings.Contains(stderr, "TLS") {
		t.Errorf("a missing -cacert: %d %q", code, stderr)
	}
}
