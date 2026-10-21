package upstream

import (
	"crypto/tls"
	"crypto/x509/pkix"
	"io"
	"log"
	stdhttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

// TestClientCertificate sends requests and probes to a backend that requires
// a client certificate from its own authority: they reach it with
// tls_cert_file, which it names, and are refused without one. The backend's
// certificate is for example.com, which tls_server_name asks for in place of
// the address.
func TestClientCertificate(t *testing.T) {
	ca, err := certtest.NewCA("upstream CA")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	server, err := ca.Issue(dir, "", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	client, err := ca.IssueClient(t.TempDir(), pkix.Name{CommonName: "fit"})
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(dir, "ca.pem")
	if err := ca.WriteCert(caFile); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(server.CertFile, server.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_, _ = io.WriteString(w, r.Proto+" "+r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca.Pool()}
	// The handshake refused without a certificate is expected.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	common := `{"addresses": ["` + srv.Listener.Addr().String() + `"], "protocol": "h2", "max_fails": 0, "tls_ca_file": "` + caFile +
		`", "tls_server_name": "example.com"`
	withCert := common + `, "tls_cert_file": "` + client.CertFile + `", "tls_key_file": "` + client.KeyFile + `", ` + probeCheck + `}`
	c, err := decode(t, withCert)
	if err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, startEngine(t), c)
	t.Cleanup(p.Close)
	if r := do(t, p, "/"); r.err != nil || r.status != 200 || r.body != "HTTP/2.0 fit" {
		t.Fatalf("with a client certificate: %d %q %v", r.status, r.body, r.err)
	}
	time.Sleep(200 * time.Millisecond)
	if p.Backends()[0].ProbedDown() {
		t.Fatal("the probes, with the same certificate, failed")
	}

	c, err = decode(t, common+`}`)
	if err != nil {
		t.Fatal(err)
	}
	p = mustNew(t, startEngine(t), c)
	t.Cleanup(p.Close)
	if r := do(t, p, "/"); r.err == nil {
		t.Fatalf("without a client certificate: %d %q", r.status, r.body)
	}
}

func TestTLSCertFileErrors(t *testing.T) {
	a, err := certtest.Write(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := certtest.Write(t.TempDir(), "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		cert, key, want string
	}{
		{filepath.Join(t.TempDir(), "missing.pem"), a.KeyFile, "no such file or directory"},
		{a.CertFile, b.KeyFile, "private key does not match public key"},
	} {
		c, err := decode(t, `{"addresses": ["127.0.0.1:1"], "protocol": "h2", "tls_cert_file": "`+tt.cert+`", "tls_key_file": "`+tt.key+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = New(nil, c)
		if err == nil || !strings.Contains(err.Error(), "upstreams[0].tls_cert_file: ") || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s, %s: %v", tt.cert, tt.key, err)
		}
	}
}
