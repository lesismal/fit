package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/certtest"
	_ "github.com/lesismal/fit/webrtc/turn"
)

// webrtc.turn's certificates are loaded when the config compiles, as a
// listener's are: a file that does not load fails it, and those that do are
// watched.
func TestTURNCertificates(t *testing.T) {
	turnDoc := func(cert, key string) string {
		return `{"webrtc": {"turn": {"listen": ["127.0.0.1:0"], "listen_tls": ["127.0.0.1:0"], "realm": "r", "secret": "s",
			"relay_ip": "127.0.0.1", "tls": {"certificates": [{"cert_file": "` + cert + `", "key_file": "` + key + `"}]}}}}`
	}
	cert, err := certtest.Write(t.TempDir(), "turn.test")
	if err != nil {
		t.Fatal(err)
	}
	g := mustCompile(t, turnDoc(cert.CertFile, cert.KeyFile))
	defer g.Release()
	if _, ok := g.CertFiles[cert.KeyFile]; !ok {
		t.Fatalf("the key file is not watched: %v", g.CertFiles)
	}
	missing := filepath.Join(t.TempDir(), "none.pem")
	if _, err := compile(t, turnDoc(missing, cert.KeyFile)); err == nil || !strings.Contains(err.Error(), "webrtc.turn.tls.certificates[0]") {
		t.Fatalf("a missing certificate file: %v", err)
	}
}
