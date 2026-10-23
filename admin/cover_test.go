package admin

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// leafless is a certificate manager whose certificate has no parsed Leaf.
type leafless struct{}

func (leafless) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return nil, errors.New("no certificate")
}

func (leafless) Certificates() []*tls.Certificate { return []*tls.Certificate{{}} }

// TestCertificatesWithoutLeaf: a certificate with no Leaf is left out of
// the status and of the metrics, which say nothing they cannot read.
func TestCertificatesWithoutLeaf(t *testing.T) {
	data := []byte(doc("v1"))
	c, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		t.Fatal(err)
	}
	g.Listeners[0].Certificates = leafless{}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
	})
	cs := NewConfigs(rt, s, runtime.Env{Metrics: observe.NewMetrics()}, c, data, SourceFile, 1)

	if st := cs.Status(); len(st.Certs) != 0 {
		t.Errorf("certificates: %+v", st.Certs)
	}
	if m := string(cs.WriteMetrics(nil)); strings.Contains(m, "fit_tls_certificate_not_after_seconds{") {
		t.Errorf("a certificate without a Leaf is scraped:\n%s", m)
	}
}

// TestWriteJSONFails: an answer that cannot be encoded is a 500 with an
// error of its own.
func TestWriteJSONFails(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, 200, map[string]any{"c": make(chan int)})
	if rec.Code != 500 || rec.Body.String() != "{\"error\":\"encoding the answer failed\"}\n" {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
}

// TestStaleSocketNotRemoved: a stale socket that cannot be removed fails
// Start rather than being listened beside.
func TestStaleSocketNotRemoved(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Unix sockets are tested on Unix")
	}
	if os.Geteuid() == 0 {
		t.Skip("root may remove what a directory's mode forbids")
	}
	dir, err := os.MkdirTemp("", "fit")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "a.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close() // the socket is left behind, with nobody listening
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := removeStaleSocket(path); err == nil || !strings.HasPrefix(err.Error(), "admin: ") {
		t.Errorf("a stale socket in a read-only directory: %v", err)
	}
}
