package listener

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/tlsmgr"
)

// TestOCSPStapled has a listener serve a certificate from files that names
// an OCSP responder, and checks that clients of TLS 1.3 and 1.2 get its
// response stapled.
func TestOCSPStapled(t *testing.T) {
	ca, err := certtest.NewCA("fit test CA")
	if err != nil {
		t.Fatal(err)
	}
	responder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, err := ocsp.ParseRequest(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		now := time.Now()
		resp, _ := ocsp.CreateResponse(ca.Cert, ca.Cert, ocsp.Response{Status: ocsp.Good, SerialNumber: req.SerialNumber,
			ThisUpdate: now, NextUpdate: now.Add(time.Hour)}, ca.Key)
		_, _ = w.Write(resp)
	}))
	defer responder.Close()
	cert, err := ca.Issue(t.TempDir(), responder.URL, "a.example")
	if err != nil {
		t.Fatal(err)
	}

	o := tlsmgr.NewOCSP(nil)
	defer o.Close()
	c, err := config.Load(fmt.Appendf(nil, `{"listeners": [{"name": "web", "address": "127.0.0.1:0",
    "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`, cert.CertFile, cert.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{OCSP: o})
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.Close()
		rt.Close()
	}()
	addr := s.Addrs()[0].Addr.String()
	for _, version := range []uint16{tls.VersionTLS13, tls.VersionTLS12} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: ca.Pool(), ServerName: "a.example",
				MinVersion: version, MaxVersion: version})
			if err != nil {
				t.Fatal(err)
			}
			staple := conn.ConnectionState().OCSPResponse
			conn.Close()
			if len(staple) > 0 {
				if resp, err := ocsp.ParseResponseForCert(staple, cert.Leaf, ca.Cert); err != nil || resp.Status != ocsp.Good {
					t.Errorf("TLS %x: %v, %+v", version, err, resp)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("TLS %x: no response stapled", version)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
