package tlsmgr

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
)

func load(t *testing.T, listener string) (*config.Listener, error) {
	t.Helper()
	c, err := config.Load([]byte(`{"listeners": [` + listener + `]}`))
	if err != nil {
		return nil, err
	}
	return &c.Listeners[0], nil
}

func TestServerConfig(t *testing.T) {
	get := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, nil }
	for _, tt := range []struct {
		protocols []string
		alpn      []string
	}{
		{[]string{"http/1.1", "h2"}, []string{"h2", "http/1.1"}},
		{[]string{"h2"}, []string{"h2"}},
		{[]string{"http/1.1"}, []string{"http/1.1"}},
		{[]string{"http/1.1", "h2", "h3"}, []string{"h2", "http/1.1"}},
	} {
		cfg := ServerConfig(tt.protocols, get)
		if !slices.Equal(cfg.NextProtos, tt.alpn) || cfg.MinVersion != tls.VersionTLS12 || cfg.GetCertificate == nil ||
			len(cfg.Certificates) != 0 || len(cfg.CipherSuites) != len(cipherSuites) {
			t.Errorf("%v: %+v", tt.protocols, cfg)
		}
	}
}

// keyCert makes a self-signed certificate for names in memory, with an
// ECDSA key, or an RSA one with rsaKey set.
func keyCert(t testing.TB, rsaKey bool, names ...string) *tls.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
	}
	var key any
	var pub any
	if rsaKey {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	} else {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// hello is a TLS 1.3 client hello for name that takes the signature schemes
// given, or ECDSA and RSA-PSS ones when none is.
func hello(name string, schemes ...tls.SignatureScheme) *tls.ClientHelloInfo {
	if len(schemes) == 0 {
		schemes = []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256, tls.PSSWithSHA256}
	}
	return &tls.ClientHelloInfo{
		ServerName:        name,
		SupportedVersions: []uint16{tls.VersionTLS13},
		CipherSuites:      []uint16{tls.TLS_AES_128_GCM_SHA256},
		SupportedCurves:   []tls.CurveID{tls.CurveP256, tls.X25519},
		SignatureSchemes:  schemes,
	}
}

func TestStatic(t *testing.T) {
	first := keyCert(t, false, "first.example")
	a := keyCert(t, false, "a.example", "A2.example")
	wild := keyCert(t, false, "*.w.example")
	exact := keyCert(t, false, "x.w.example")
	ecdsaB := keyCert(t, false, "b.example")
	rsaB := keyCert(t, true, "b.example")
	s := NewStatic(first, a, wild, exact, ecdsaB, rsaB)
	for _, tt := range []struct {
		hello *tls.ClientHelloInfo
		want  *tls.Certificate
	}{
		{hello(""), first},
		{hello("a.example"), a},
		{hello("A.Example."), a},
		{hello("a2.example"), a},
		{hello("other.example"), first},
		{hello("y.w.example"), wild},
		{hello("w.example"), first},
		{hello("z.y.w.example"), first},
		// The wildcard came first, and serves the name too.
		{hello("x.w.example"), wild},
		{hello("b.example"), ecdsaB},
		// A client without ECDSA gets the RSA certificate for the name.
		{hello("b.example", tls.PSSWithSHA256), rsaB},
		// One that takes neither gets the first for the name.
		{hello("b.example", tls.Ed25519), ecdsaB},
	} {
		got, err := s.GetCertificate(tt.hello)
		if err != nil || got != tt.want {
			t.Errorf("%q %v: %s, %v; want %s", tt.hello.ServerName, tt.hello.SignatureSchemes, got.Leaf.Subject.CommonName, err,
				tt.want.Leaf.Subject.CommonName)
		}
	}
	if _, err := NewStatic().GetCertificate(hello("a.example")); err == nil {
		t.Error("no certificates, and no error")
	}
	// One certificate serves every name.
	if got, _ := NewStatic(a).GetCertificate(hello("other.example")); got != a {
		t.Error("the only certificate was not served")
	}
}

func TestLoadStatic(t *testing.T) {
	dir := t.TempDir()
	a, err := certtest.Write(dir, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	ip, err := certtest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	certs := `{"certificates": [{"cert_file": "` + a.CertFile + `", "key_file": "` + a.KeyFile + `"}, {"cert_file": "` + ip.CertFile + `", "key_file": "` + ip.KeyFile + `"}]}`
	l, err := load(t, `{"name": "web", "address": ":443", "tls": `+certs+`}`)
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadStatic(l.TLS)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.example": "a.example", "127.0.0.1": "127.0.0.1", "": "a.example"} {
		got, err := s.GetCertificate(hello(name))
		if err != nil || got.Leaf.Subject.CommonName != want {
			t.Errorf("%q: %v, %v", name, got, err)
		}
	}
}

func TestLoadStaticErrors(t *testing.T) {
	dir := t.TempDir()
	a, err := certtest.Write(dir, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	b, err := certtest.Write(dir, "b.example")
	if err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not pem"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		cert, key, want string
	}{
		{filepath.Join(dir, "missing.crt"), a.KeyFile, "no such file or directory"},
		{garbage, a.KeyFile, "failed to find any PEM data"},
		{a.CertFile, b.KeyFile, "private key does not match public key"},
	} {
		l, err := load(t, `{"name": "web", "address": ":443", "tls": {"certificates": [{"cert_file": "`+tt.cert+`", "key_file": "`+tt.key+`"}]}}`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = LoadStatic(l.TLS)
		if err == nil || !strings.Contains(err.Error(), "listeners[0].tls.certificates[0]: ") || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s/%s: %v", filepath.Base(tt.cert), filepath.Base(tt.key), err)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, tt := range []struct {
		listener, want string
	}{
		{`{"name": "web", "address": ":80", "protocols": ["h2"]}`, "h2 needs tls"},
		{`{"name": "web", "address": ":443", "protocols": ["h2c"], "tls": {"certificates": [{"cert_file": "c", "key_file": "k"}]}}`, "use h2"},
		{`{"name": "web", "address": ":443", "tls": {"certificates": []}}`, "listeners[0].tls: a certificate or a name for acme is needed"},
		{`{"name": "web", "address": ":443", "tls": {}}`, "listeners[0].tls: a certificate or a name for acme is needed"},
		{`{"name": "web", "address": ":443", "tls": {"acme": ["*.example.com"]}}`, "acme[0]: \"*.example.com\" is a wildcard"},
		{`{"name": "web", "address": ":443", "tls": {"acme": ["192.0.2.1"]}}`, "acme[0]: \"192.0.2.1\" is an IP address"},
		{`{"name": "web", "address": ":443", "tls": {"acme": ["a.example", "A.EXAMPLE"]}}`, "acme[1]: \"A.EXAMPLE\" is listed twice"},
		{`{"name": "web", "address": ":443", "tls": {"acme": ["bad_name.example"]}}`, "acme[0]: \"bad_name.example\" is not a valid host name"},
	} {
		if _, err := load(t, tt.listener); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", tt.listener, err)
		}
	}
}

func BenchmarkStatic(b *testing.B) {
	var certs []*tls.Certificate
	for i := range 100 {
		certs = append(certs, keyCert(b, false, fmt.Sprintf("host%d.example", i)))
	}
	s := NewStatic(certs...)
	h := hello("host75.example")
	b.ReportAllocs()
	for b.Loop() {
		if c, _ := s.GetCertificate(h); c != certs[75] {
			b.Fatal("wrong certificate")
		}
	}
}
