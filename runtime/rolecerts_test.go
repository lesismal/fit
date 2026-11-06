package runtime

import (
	"path/filepath"
	"testing"

	"github.com/lesismal/fit/internal/certtest"
	_ "github.com/lesismal/fit/llm/hub"
	_ "github.com/lesismal/fit/tunnel/hub"
)

// The certificate files the tunnel hub's listeners and an LLM listener read
// are watched as a listener's are, so that -watch-certs applies the config
// again when they are rotated; a file that cannot be read is not.
func TestRoleCertificatesWatched(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "role.test")
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "none.pem")
	doc := `{"tunnel": {"hub": {"password": "pw", "listeners": [
			{"protocol": "tls", "address": "127.0.0.1:0", "cert_file": "` + cert.CertFile + `", "key_file": "` + cert.KeyFile + `"},
			{"protocol": "wss", "address": "127.0.0.1:0", "cert_file": "` + missing + `", "key_file": "` + missing + `"}]}},
		"llm": {"hub": {"listen": {"address": "127.0.0.1:0", "password": "pw", "cert_file": "` + cert.CertFile + `", "key_file": "` + cert.KeyFile + `"}}}}`
	g := mustCompile(t, doc)
	defer g.Release()
	for _, f := range []string{cert.CertFile, cert.KeyFile} {
		if _, ok := g.CertFiles[f]; !ok {
			t.Fatalf("%s is not watched: %v", f, g.CertFiles)
		}
	}
	if _, ok := g.CertFiles[missing]; ok {
		t.Fatal("a missing file is watched")
	}
}
