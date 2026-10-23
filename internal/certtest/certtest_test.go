package certtest

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"os"
	"path/filepath"
	"testing"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	c, err := Write(dir, "example.com", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Leaf.Verify(x509.VerifyOptions{DNSName: "127.0.0.1", Roots: Pool(c)}); err != nil {
		t.Fatal(err)
	}
}

func TestCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := NewCA("test CA")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ca.Issue(dir, "http://ocsp.test", "a.test", "::1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Leaf.Verify(x509.VerifyOptions{DNSName: "a.test", Roots: ca.Pool()}); err != nil {
		t.Fatal(err)
	}
	if len(c.Leaf.OCSPServer) != 1 || len(c.Leaf.IPAddresses) != 1 {
		t.Fatalf("OCSP %q, IPs %v", c.Leaf.OCSPServer, c.Leaf.IPAddresses)
	}
	cl, err := ca.IssueClient(dir, pkix.Name{CommonName: "client"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cl.Leaf.DNSNames) != 0 || cl.Leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("a client certificate: %v %v", cl.Leaf.DNSNames, cl.Leaf.ExtKeyUsage)
	}
	p := filepath.Join(dir, "ca.pem")
	if err := ca.WriteCert(p); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(p); err != nil || !x509.NewCertPool().AppendCertsFromPEM(data) {
		t.Fatalf("WriteCert: %v", err)
	}
}

// TestWriteErrors writes where the certificate's file, then its key's, cannot
// be written.
func TestWriteErrors(t *testing.T) {
	ca, err := NewCA("test CA")
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "none")
	keyDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(keyDir, "a.test.key"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{missing, keyDir} {
		if _, err := Write(dir, "a.test"); err == nil {
			t.Errorf("Write in %s", dir)
		}
		if _, err := ca.Issue(dir, "", "a.test"); err == nil {
			t.Errorf("Issue in %s", dir)
		}
	}
}
