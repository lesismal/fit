package tlsmgr

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	dir := t.TempDir()
	a, err := certtest.Write(dir, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	b, err := certtest.Write(dir, "b.example")
	if err != nil {
		t.Fatal(err)
	}
	certs := `{"certificates": [{"cert_file": "` + a.CertFile + `", "key_file": "` + a.KeyFile + `"}, {"cert_file": "` + b.CertFile + `", "key_file": "` + b.KeyFile + `"}]}`
	l, err := load(t, `{"name": "web", "address": ":443", "tls": `+certs+`}`)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, tt := range []struct {
		protocols []string
		alpn      []string
	}{
		{[]string{"http/1.1", "h2"}, []string{"h2", "http/1.1"}},
		{[]string{"h2"}, []string{"h2"}},
		{[]string{"http/1.1"}, []string{"http/1.1"}},
	} {
		cfg, key, err := ServerConfig(l.TLS, tt.protocols)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.NextProtos, tt.alpn) || cfg.MinVersion != tls.VersionTLS12 || len(cfg.Certificates) != 2 ||
			len(cfg.CipherSuites) != len(cipherSuites) {
			t.Errorf("%v: %+v", tt.protocols, cfg)
		}
		keys[key] = true
	}
	if len(keys) != 3 {
		t.Errorf("fingerprints do not tell the protocols apart: %v", keys)
	}
	// Another certificate in the same files changes the fingerprint.
	_, before, _ := ServerConfig(l.TLS, []string{"h2"})
	if _, err := certtest.Write(dir, "a.example"); err != nil {
		t.Fatal(err)
	}
	if _, after, _ := ServerConfig(l.TLS, []string{"h2"}); after == before {
		t.Error("a new certificate kept the fingerprint")
	}
}

func TestServerConfigErrors(t *testing.T) {
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
		_, _, err = ServerConfig(l.TLS, []string{"h2"})
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
		{`{"name": "web", "address": ":443", "tls": {"certificates": []}}`, "at least one certificate is needed"},
		{`{"name": "web", "address": ":443", "tls": {}}`, "certificates: missing required field"},
	} {
		if _, err := load(t, tt.listener); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", tt.listener, err)
		}
	}
}
