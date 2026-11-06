package certpin

import (
	"bytes"
	"crypto/tls"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Keep makes a certificate once, writes it 0600, and gives the same one back
// after; a file that holds no certificate is an error and stays as it is.
func TestKeep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.tunnel.hub.pem")
	c, k, made, err := Keep(path, "test", "localhost", "hub")
	if err != nil || !made {
		t.Fatalf("made %v, %v", made, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	c2, k2, made, err := Keep(path, "test", "other")
	if err != nil || made || !bytes.Equal(c, c2) || !bytes.Equal(k, k2) {
		t.Fatalf("kept %v %v", made, err)
	}
	a, _ := tls.X509KeyPair(c, k)
	b, _ := tls.X509KeyPair(c2, k2)
	if Fingerprint(a) == "" || Fingerprint(a) != Fingerprint(b) {
		t.Fatal("the fingerprint changed")
	}
	// No temporary file is left beside it.
	if ents, _ := os.ReadDir(filepath.Dir(path)); len(ents) != 1 {
		t.Fatalf("files %v", ents)
	}

	bad := filepath.Join(t.TempDir(), "bad.pem")
	_ = os.WriteFile(bad, c, 0o600) // a certificate without its key
	if _, _, _, err := Keep(bad, "test"); err == nil || !strings.Contains(err.Error(), "does not hold a certificate and its key") {
		t.Fatalf("a file without a key: %v", err)
	}
	if data, _ := os.ReadFile(bad); !bytes.Equal(data, c) {
		t.Fatal("the bad file was changed")
	}
	// A directory that does not exist cannot be written: the certificate
	// made comes back with the error.
	if c, k, made, err := Keep(filepath.Join(t.TempDir(), "none", "x.pem"), "test"); err == nil || !made || c == nil || k == nil {
		t.Fatalf("kept in a directory that does not exist: %v %v", made, err)
	}
	// A path that is a directory cannot be read.
	if _, _, _, err := Keep(t.TempDir(), "test"); err == nil {
		t.Fatal("kept in a directory")
	}
}
