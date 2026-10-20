//go:build !windows

package e2e

import (
	"context"
	"crypto/tls"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

// TestCertificateFilesWatched renews a listener's certificate files under a
// running fit, as a tool outside fit does: the new certificate is served
// without a reload by hand, and a certificate written before its key, which
// do not match, is reported and leaves the old one served.
func TestCertificateFilesWatched(t *testing.T) {
	dir := t.TempDir()
	old, err := certtest.Write(dir, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	f, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "tls": {"certificates": [
    {"cert_file": "`+old.CertFile+`", "key_file": "`+old.KeyFile+`"}]}}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]
}`, "-watch-interval", "50ms")
	serial := func() *big.Int {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d := tls.Dialer{Config: &tls.Config{ServerName: "a.example", RootCAs: certtest.Pool(old, renewed)}}
		c, err := d.DialContext(ctx, "tcp", addrs["web"])
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.(*tls.Conn).ConnectionState().PeerCertificates[0].SerialNumber
	}
	copyFile := func(from, to string) {
		t.Helper()
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		// Written to a file beside it and renamed over it, as renewal tools
		// do, so that a look never finds it half written.
		tmp := to + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, to); err != nil {
			t.Fatal(err)
		}
	}
	if got := serial(); got.Cmp(old.Leaf.SerialNumber) != 0 {
		t.Fatalf("serving %s, want the first certificate's %s", got, old.Leaf.SerialNumber)
	}

	// The certificate alone: it does not match the key, so the reload fails
	// and the old pair stays.
	copyFile(renewed.CertFile, old.CertFile)
	f.waitLine(t, "fit: certificate files changed; reloading")
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(f.stderr.String(), "private key does not match public key") {
		if time.Now().After(deadline) {
			t.Fatalf("the mismatched pair was not reported: %q", f.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := serial(); got.Cmp(old.Leaf.SerialNumber) != 0 {
		t.Fatalf("after a mismatched pair, serving %s, want the old %s", got, old.Leaf.SerialNumber)
	}

	// Then the key: the renewed pair is served.
	copyFile(renewed.KeyFile, old.KeyFile)
	f.waitLine(t, "fit: certificate files changed; reloading")
	f.waitLine(t, "fit: reloaded certificate files")
	if got := serial(); got.Cmp(renewed.Leaf.SerialNumber) != 0 {
		t.Fatalf("after the renewal, serving %s, want %s", got, renewed.Leaf.SerialNumber)
	}

	if n := strings.Count(f.stderr.String(), "certificate files:"); n != 1 {
		t.Errorf("%d errors reported, want 1: %q", n, f.stderr.String())
	}
}
