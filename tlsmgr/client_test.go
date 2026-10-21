package tlsmgr

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/certtest"
)

func TestLoadClientAuth(t *testing.T) {
	dir := t.TempDir()
	server, err := certtest.Write(dir, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for i := range 2 {
		ca, err := certtest.NewCA(fmt.Sprint("CA ", i))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, filepath.Join(dir, fmt.Sprint("ca", i, ".pem")))
		if err := ca.WriteCert(files[i]); err != nil {
			t.Fatal(err)
		}
	}
	noCert := filepath.Join(dir, "key-only.pem")
	key, _ := os.ReadFile(server.KeyFile)
	if err := os.WriteFile(noCert, key, 0o600); err != nil {
		t.Fatal(err)
	}
	clientAuth := func(caFiles []string, mode string) (*ClientAuth, error) {
		t.Helper()
		l, err := load(t, fmt.Sprintf(`{"name": "web", "address": ":443", "tls": {"certificates": [{"cert_file": %q, "key_file": %q}],
  "client_auth": {"ca_files": %s, "mode": %q}}}`, server.CertFile, server.KeyFile, must(json.Marshal(caFiles)), mode))
		if err != nil {
			t.Fatal(err)
		}
		return LoadClientAuth(l.TLS.ClientAuth)
	}

	both, err := clientAuth(files, "require")
	if err != nil {
		t.Fatal(err)
	}
	if both.Type != tls.RequireAndVerifyClientCert || len(both.Files()) != 2 {
		t.Fatalf("%+v", both)
	}
	optional, err := clientAuth(files, "verify_if_given")
	if err != nil {
		t.Fatal(err)
	}
	one, err := clientAuth(files[:1], "require")
	if err != nil {
		t.Fatal(err)
	}
	again, err := clientAuth(files, "require")
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case optional.Type != tls.VerifyClientCertIfGiven:
		t.Errorf("verify_if_given: %v", optional.Type)
	case both.CAs.Equal(one.CAs) || !both.CAs.Equal(again.CAs):
		t.Error("the pools are not of the files")
	case optional.Sum == both.Sum || one.Sum == both.Sum:
		t.Error("another mode or other authorities have the same sum")
	case again.Sum != both.Sum:
		t.Error("the same files load to another sum")
	}

	for _, tt := range []struct {
		files []string
		err   string
	}{
		{[]string{files[0], filepath.Join(dir, "missing.pem")}, "listeners[0].tls.client_auth.ca_files[1]: open"},
		{[]string{noCert}, "listeners[0].tls.client_auth.ca_files[0]: no PEM certificate found"},
	} {
		if _, err := clientAuth(tt.files, "require"); err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%v: %v, want %q", tt.files, err, tt.err)
		}
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
