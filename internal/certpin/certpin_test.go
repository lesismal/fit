package certpin

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

func TestSelfSignedAndPin(t *testing.T) {
	c, k, err := SelfSigned("test", "127.0.0.1", "node.test", "")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.IPAddresses) != 1 || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "node.test" || leaf.Subject.Organization[0] != "test" {
		t.Fatalf("leaf %v %v %v", leaf.IPAddresses, leaf.DNSNames, leaf.Subject)
	}
	other, _, err := SelfSigned("test")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(other)
	if block == nil {
		t.Fatal("no PEM")
	}
	if o, _ := x509.ParseCertificate(block.Bytes); o == nil || o.DNSNames[0] != "localhost" {
		t.Fatalf("a certificate without hosts: %v", o)
	}

	fp := Fingerprint(cert)
	if len(fp) != 64 || Fingerprint(tls.Certificate{}) != "" {
		t.Fatalf("fingerprint %q", fp)
	}
	// Colons, case and spaces around do not matter.
	var colons []string
	for i := 0; i < len(fp); i += 2 {
		colons = append(colons, strings.ToUpper(fp[i:i+2]))
	}
	pin, err := Parse(" " + strings.Join(colons, ":") + " ")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "abcd", strings.Repeat("g", 64), strings.Repeat("a", 66)} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) took it", bad)
		}
	}

	conf := &tls.Config{ClientSessionCache: tls.NewLRUClientSessionCache(1)}
	Pinned(conf, pin, "test: server")
	if !conf.InsecureSkipVerify || conf.ClientSessionCache != nil || !conf.SessionTicketsDisabled {
		t.Fatalf("pinned config %+v", conf)
	}
	if err := conf.VerifyPeerCertificate([][]byte{cert.Certificate[0]}, nil); err != nil {
		t.Fatal(err)
	}
	if err := conf.VerifyPeerCertificate([][]byte{block.Bytes}, nil); err == nil || !strings.Contains(err.Error(), "test: server certificate") {
		t.Fatalf("another certificate: %v", err)
	}
	if err := conf.VerifyPeerCertificate(nil, nil); err == nil || !strings.Contains(err.Error(), "sent no certificate") {
		t.Fatalf("no certificate: %v", err)
	}
}
