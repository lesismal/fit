//go:build !windows

package e2e

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

func TestTLSTwoHosts(t *testing.T) {
	dir := t.TempDir()
	a, err := certtest.Write(dir, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	b, err := certtest.Write(dir, "b.example")
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_, _ = io.WriteString(w, "proxied "+r.Header.Get("X-Forwarded-Proto"))
	}))
	t.Cleanup(backend.Close)
	_, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "tls": {"certificates": [
    {"cert_file": "`+a.CertFile+`", "key_file": "`+a.KeyFile+`"},
    {"cert_file": "`+b.CertFile+`", "key_file": "`+b.KeyFile+`"}]}}],
  "upstreams": [{"name": "app", "addresses": ["`+backend.Listener.Addr().String()+`"]}],
  "routes": [
    {"match": {"host": ["a.example"]}, "handler": {"module": "respond", "params": {"body": "site a"}}},
    {"match": {"host": ["b.example"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}}}
  ]
}`)
	for _, h2 := range []bool{false, true} {
		tr := &stdhttp.Transport{
			TLSClientConfig: &tls.Config{RootCAs: certtest.Pool(a, b)},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addrs["web"])
			},
			Protocols: new(stdhttp.Protocols),
		}
		if h2 {
			tr.Protocols.SetHTTP2(true)
		} else {
			tr.Protocols.SetHTTP1(true)
		}
		c := &stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}
		for _, tt := range []struct{ host, body string }{{"a.example", "site a"}, {"b.example", "proxied https"}} {
			resp, err := c.Get("https://" + tt.host + "/")
			if err != nil {
				t.Fatalf("h2=%v %s: %v", h2, tt.host, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			wantProto := map[bool]string{false: "HTTP/1.1", true: "HTTP/2.0"}[h2]
			if string(body) != tt.body || resp.Proto != wantProto || resp.TLS.PeerCertificates[0].Subject.CommonName != tt.host {
				t.Errorf("h2=%v %s: %s %q, certificate %s", h2, tt.host, resp.Proto, body, resp.TLS.PeerCertificates[0].Subject.CommonName)
			}
		}
	}
}
