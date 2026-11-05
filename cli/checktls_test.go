package cli

import (
	"crypto/tls"
	"crypto/x509/pkix"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/certtest"
)

// tlsFake is a fake Admin API over TLS, with a certificate for 127.0.0.1 a
// CA issues, and, when clients is set, asking for client certificates that
// CA issues.
type tlsFake struct {
	*checkFake
	ca   *certtest.CA
	cert *certtest.Cert
	dir  string
	host string // the fake's host and port
}

func newTLSFake(t *testing.T, clients bool) *tlsFake {
	t.Helper()
	dir := t.TempDir()
	ca, err := certtest.NewCA("admin CA")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.Issue(dir, "", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(cert.CertFile, cert.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	f := &checkFake{token: "s3cret", checks: []*checkResult{checkAnswer(3)}}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	if clients {
		f.srv.TLS.ClientCAs, f.srv.TLS.ClientAuth = ca.Pool(), tls.RequireAndVerifyClientCert
	}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &tlsFake{checkFake: f, ca: ca, cert: cert, dir: dir, host: u.Host}
}

// boot writes a bootstrap config whose admin is the fake, with the token
// and the TLS files certFile and, when not empty, clientCA.
func (f *tlsFake) boot(t *testing.T, address, certFile, clientCA string) string {
	t.Helper()
	token := filepath.Join(f.dir, "token")
	writeFile(t, token, "s3cret\n")
	admin := `{"address": ` + js(address) + `, "token_file": ` + js(token) +
		`, "cert_file": ` + js(certFile) + `, "key_file": ` + js(f.cert.KeyFile)
	if clientCA != "" {
		admin += `, "client_ca_file": ` + js(clientCA)
	}
	return writeCheckBoot(t, admin+`}`)
}

// TestCheckTLS reaches an Admin API over TLS, verifying its certificate with
// admin.cert_file's own, or with -ca, for the name -server-name gives.
func TestCheckTLS(t *testing.T) {
	f := newTLSFake(t, false)
	ok := func(name string, args ...string) {
		t.Helper()
		code, out, stderr := runFitCheck(t, "", args...)
		if code != 0 || stderr != "" || !strings.Contains(out, "fit check: config version 3") {
			t.Errorf("%s: %d %q %q", name, code, out, stderr)
		}
	}
	fails := func(name string, want string, args ...string) {
		t.Helper()
		if code, _, stderr := runFitCheck(t, "", args...); code != 2 || !strings.Contains(stderr, want) {
			t.Errorf("%s: %d %q, want %q", name, code, stderr, want)
		}
	}

	// cert_file is trusted as it is, when fit check can read it.
	ok("cert_file", "-c", f.boot(t, f.host, f.cert.CertFile, ""))

	// Unreadable, as on another host: the system's CAs do not know it,
	// and -ca does.
	elsewhere := f.boot(t, f.host, filepath.Join(f.dir, "elsewhere.pem"), "")
	fails("no CA", "-ca", "-c", elsewhere)
	caFile := filepath.Join(f.dir, "ca.pem")
	if err := f.ca.WriteCert(caFile); err != nil {
		t.Fatal(err)
	}
	ok("-ca", "-c", elsewhere, "-ca", caFile)
	fails("-ca missing", "-ca: ", "-c", elsewhere, "-ca", filepath.Join(f.dir, "missing.pem"))
	fails("-ca not PEM", "no PEM certificate", "-c", elsewhere, "-ca", f.cert.KeyFile)

	// The certificate is for 127.0.0.1, not another name; an unspecified
	// host is verified as localhost.
	fails("-server-name", "certificate", "-c", elsewhere, "-ca", caFile, "-server-name", "admin.example.com")
	_, port, _ := net.SplitHostPort(f.host)
	fails("unspecified", "localhost", "-c", f.boot(t, "0.0.0.0:"+port, f.cert.CertFile, ""))
	ok("unspecified, -server-name", "-c", f.boot(t, "0.0.0.0:"+port, f.cert.CertFile, ""), "-server-name", "127.0.0.1")

	// TLS flags without TLS, and a client certificate without its key.
	plain := newCheckFake(t, "", checkAnswer(1)).boot(t, "")
	fails("plain", "admin sets no cert_file", "-c", plain, "-ca", caFile)
	fails("-cert alone", "-cert and -key go together", "-c", elsewhere, "-ca", caFile, "-cert", f.cert.CertFile)
}

// TestCheckTLSClientCert presents -cert to an Admin API with
// admin.client_ca_file.
func TestCheckTLSClientCert(t *testing.T) {
	f := newTLSFake(t, true)
	caFile := filepath.Join(f.dir, "ca.pem")
	if err := f.ca.WriteCert(caFile); err != nil {
		t.Fatal(err)
	}
	boot := f.boot(t, f.host, f.cert.CertFile, caFile)
	if code, _, stderr := runFitCheck(t, "", "-c", boot); code != 2 || !strings.Contains(stderr, "give it with -cert and -key") {
		t.Errorf("without -cert: %d %q", code, stderr)
	}
	client, err := f.ca.IssueClient(t.TempDir(), pkix.Name{CommonName: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := runFitCheck(t, "", "-c", boot, "-cert", client.CertFile, "-key", client.KeyFile); code != 0 || stderr != "" || !strings.Contains(out, "drift: 0") {
		t.Errorf("with -cert: %d %q %q", code, out, stderr)
	}
	if code, _, stderr := runFitCheck(t, "", "-c", boot, "-cert", client.CertFile, "-key", caFile); code != 2 || !strings.Contains(stderr, "-cert and -key: ") {
		t.Errorf("a key that is not one: %d %q", code, stderr)
	}
}

// TestCheckRunningTLS runs fit with its Admin API over TLS, and fit check
// reaches it with the bootstrap file alone.
func TestCheckRunningTLS(t *testing.T) {
	dir := t.TempDir()
	cert, err := certtest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "token")
	writeFile(t, token, "s3cret\n")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	_ = ln.Close()
	cfg := filepath.Join(dir, "fit.json")
	writeFile(t, cfg, `{"admin": {"address": `+js(address)+`, "token_file": `+js(token)+
		`, "cert_file": `+js(cert.CertFile)+`, "key_file": `+js(cert.KeyFile)+`},
"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"handler": {"module": "respond"}}]}`)
	s := startServe(t, "-c", cfg, "-watch-certs=false")
	s.out.waitFor(t, "fit: admin API on ", 1)

	code, out, stderr := runFitCheck(t, "", "-c", cfg)
	if code != 0 || stderr != "" || !strings.Contains(out, "fit check: config version 1") {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
	// Plain HTTP is not heard.
	if resp, err := http.Get("http://" + address + "/config/check"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Error("plain HTTP was answered")
		}
	}
}
