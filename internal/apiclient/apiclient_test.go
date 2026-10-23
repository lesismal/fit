package apiclient

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

func TestClient(t *testing.T) {
	dir := t.TempDir()
	ca, err := certtest.Write(dir, "localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	notPEM := filepath.Join(dir, "not.pem")
	if err := os.WriteFile(notPEM, []byte("nothing"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.pem")
	for _, tt := range []struct {
		files TLSFiles
		want  string
	}{
		{TLSFiles{CA: missing}, "missing.pem"},
		{TLSFiles{CA: notPEM}, "not.pem: no PEM certificates"},
		{TLSFiles{Cert: ca.CertFile, Key: missing}, "missing.pem"},
	} {
		if _, err := Client(tt.files, time.Second, 1); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%+v: %v, want %q", tt.files, err, tt.want)
		}
	}

	// A client with no files has no TLS config; one with them has its CA
	// and certificate.
	plain, err := Client(TLSFiles{}, time.Second, 1)
	if err != nil || plain.Transport.(*http.Transport).TLSClientConfig != nil {
		t.Fatalf("no files: %v", err)
	}
	c, err := Client(TLSFiles{CA: ca.CertFile, Cert: ca.CertFile, Key: ca.KeyFile}, 5*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	tc := c.Transport.(*http.Transport).TLSClientConfig
	if tc == nil || tc.RootCAs == nil || len(tc.Certificates) != 1 {
		t.Fatalf("TLS config %+v", tc)
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("cut") }
func (failingBody) Close() error             { return nil }

func TestReadBody(t *testing.T) {
	resp := func(body string) *http.Response {
		return &http.Response{Status: "503 Service Unavailable", Body: io.NopCloser(strings.NewReader(body))}
	}
	if data, err := ReadBody(resp("12345"), 5, "list"); err != nil || string(data) != "12345" {
		t.Errorf("at the bound: %q %v", data, err)
	}
	if _, err := ReadBody(resp("123456"), 5, "list"); err == nil || err.Error() != "list: the answer is larger than 5 bytes" {
		t.Errorf("past the bound: %v", err)
	}
	if _, err := ReadBody(&http.Response{Body: failingBody{}}, 5, "list"); err == nil || err.Error() != "list: cut" {
		t.Errorf("cut: %v", err)
	}
	if err := StatusError(resp(" no leader \n"), "list"); err.Error() != "list: 503 Service Unavailable: no leader" {
		t.Errorf("status: %v", err)
	}
}
