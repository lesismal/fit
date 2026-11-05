package admin

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
)

// tlsAdmin is an Admin API over TLS, with a token, and what its tests use.
type tlsAdmin struct {
	*fixture
	dir   string
	ca    *certtest.CA
	cfg   *config.Admin
	roots *x509.CertPool
}

// startTLS starts an Admin API over TLS with a certificate for 127.0.0.1 a
// CA issues, the token s3cret and, when mTLS is set, a client CA.
func startTLS(t *testing.T, mTLS bool) *tlsAdmin {
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
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &config.Admin{TokenFile: token, CertFile: cert.CertFile, KeyFile: cert.KeyFile}
	if mTLS {
		a.ClientCAFile = filepath.Join(dir, "clients.pem")
		if err := ca.WriteCert(a.ClientCAFile); err != nil {
			t.Fatal(err)
		}
	}
	f := start(t, doc("v1"), a)
	f.api = "https://" + f.admin.Addr().String()
	ta := &tlsAdmin{fixture: f, dir: dir, ca: ca, cfg: a, roots: ca.Pool()}
	f.client = ta.client(nil)
	return ta
}

// client returns a client that trusts the CA and presents certs.
func (a *tlsAdmin) client(certs []tls.Certificate) *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: a.roots, Certificates: certs},
		ForceAttemptHTTP2: true,
	}}
}

// get makes GET /config with the token through c.
func (a *tlsAdmin) get(c *http.Client, token string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", a.api+"/config", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return resp, err
}

// TestTLS serves the Admin API over TLS: the token is asked for as without
// it, the protocol is HTTP/1.1 alone, and neither plain HTTP nor TLS before
// 1.2 is heard.
func TestTLS(t *testing.T) {
	a := startTLS(t, false)
	resp, err := a.get(a.client(nil), "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.TLS == nil || resp.ProtoMajor != 1 {
		t.Fatalf("with the token: %d, TLS %v, %s", resp.StatusCode, resp.TLS != nil, resp.Proto)
	}
	if resp, err := a.get(a.client(nil), ""); err != nil || resp.StatusCode != 401 {
		t.Fatalf("without the token: %v %v", resp, err)
	}

	// Plain HTTP gets net/http's 400, not the API.
	plain := "http://" + a.admin.Addr().String() + "/config"
	if resp, err := http.Get(plain); err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 || strings.Contains(string(body), "listeners") {
			t.Errorf("plain HTTP: %d %q", resp.StatusCode, body)
		}
	}

	// TLS 1.1, and a certificate no CA the client trusts signed.
	old := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: a.roots, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}}}
	if _, err := a.get(old, "s3cret"); err == nil {
		t.Error("TLS 1.1 was heard")
	}
	other := &http.Client{Timeout: 10 * time.Second}
	if _, err := a.get(other, "s3cret"); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("an untrusted certificate: %v", err)
	}

	// The dashboard's reads from its own origin, which is https.
	for _, tt := range []struct {
		origin string
		status int
	}{{"https://" + a.admin.Addr().String(), 200}, {"http://" + a.admin.Addr().String(), 403}} {
		req, _ := http.NewRequest("GET", a.api+"/config", nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		req.Header.Set("Origin", tt.origin)
		if resp, _ := a.do(t, req); resp.StatusCode != tt.status {
			t.Errorf("Origin %s: %d", tt.origin, resp.StatusCode)
		}
	}
}

// TestTLSClientCA asks every client for a certificate the client CA signed.
func TestTLSClientCA(t *testing.T) {
	a := startTLS(t, true)
	if _, err := a.get(a.client(nil), "s3cret"); err == nil {
		t.Fatal("a client without a certificate was heard")
	}
	stranger, err := certtest.NewCA("another CA")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		ca     *certtest.CA
		status int
	}{{stranger, 0}, {a.ca, 200}} {
		cc, err := tt.ca.IssueClient(t.TempDir(), pkix.Name{CommonName: "ops"})
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.LoadX509KeyPair(cc.CertFile, cc.KeyFile)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := a.get(a.client([]tls.Certificate{pair}), "s3cret")
		switch {
		case tt.status == 0 && err == nil:
			t.Errorf("%s's client certificate was taken", tt.ca.Cert.Subject.CommonName)
		case tt.status != 0 && (err != nil || resp.StatusCode != tt.status):
			t.Errorf("%s's client certificate: %v %v", tt.ca.Cert.Subject.CommonName, resp, err)
		}
	}
}

// TestTLSReload serves a certificate written over the files once a
// handshake sees them changed, and keeps the running one while the files do
// not load.
func TestTLSReload(t *testing.T) {
	defer func(d time.Duration) { certCheckEvery = d }(certCheckEvery)
	certCheckEvery = 0
	a := startTLS(t, false)
	serial := func() string {
		t.Helper()
		// A new connection each time, so that each is a handshake.
		c := a.client(nil)
		defer c.CloseIdleConnections()
		resp, err := a.get(c, "s3cret")
		if err != nil {
			t.Fatal(err)
		}
		return resp.TLS.PeerCertificates[0].SerialNumber.String()
	}
	first := serial()

	renewed, err := a.ca.Issue(t.TempDir(), "", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	copyFile(t, renewed.CertFile, a.cfg.CertFile)
	copyFile(t, renewed.KeyFile, a.cfg.KeyFile)
	// A file system with coarse modification times may need the size to
	// tell; the two certificates' serials differ, so the files may not.
	touch(t, a.cfg.CertFile, a.cfg.KeyFile)
	if got := serial(); got != renewed.Leaf.SerialNumber.String() || got == first {
		t.Fatalf("after the renewal: %s, want %s", got, renewed.Leaf.SerialNumber)
	}

	// A key that does not load: the renewed certificate stays, and the
	// warning says why.
	var mu sync.Mutex
	var warned []string
	a.cfg.CertFile, a.cfg.KeyFile = renewed.CertFile, renewed.KeyFile
	files, err := loadCertFiles(a.cfg, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		warned = append(warned, fmt.Sprintf(format, args...))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(renewed.KeyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, renewed.KeyFile)
	c := files.current()
	if len(c.Certificates) != 1 || c.Certificates[0].Leaf.SerialNumber.Cmp(renewed.Leaf.SerialNumber) != 0 {
		t.Error("the running certificate did not stay")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(warned) != 1 || !strings.Contains(warned[0], "key_file") || !strings.Contains(warned[0], "the running certificate stays") {
		t.Errorf("warnings: %q", warned)
	}
}

// TestTLSStartErrors fails to start on certificate files that do not load.
func TestTLSStartErrors(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	dir := t.TempDir()
	cert, err := certtest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("nothing"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		a    config.Admin
		want string
	}{
		{config.Admin{CertFile: filepath.Join(dir, "missing.pem"), KeyFile: cert.KeyFile}, "cert_file and key_file"},
		{config.Admin{CertFile: cert.CertFile, KeyFile: cert.KeyFile, ClientCAFile: filepath.Join(dir, "missing.pem")}, "client_ca_file"},
		{config.Admin{CertFile: cert.CertFile, KeyFile: cert.KeyFile, ClientCAFile: bad}, "no PEM certificate"},
	} {
		tt.a.Address = "127.0.0.1:0"
		if _, err := Start(&tt.a, f.configs, nil, nil); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%+v: %v, want %q", tt.a, err, tt.want)
		}
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// touch moves the files' modification times on, past what a coarse clock
// might leave as it was.
func touch(t *testing.T, paths ...string) {
	t.Helper()
	at := time.Now().Add(time.Duration(len(paths)) * time.Hour)
	for _, p := range paths {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}
