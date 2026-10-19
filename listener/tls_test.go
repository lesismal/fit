package listener

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

// tlsClient reaches addr, whatever the URL's host, trusting certs, speaking
// HTTP/2 when h2 is set and HTTP/1.1 otherwise.
func tlsClient(addr string, h2 bool, certs ...*certtest.Cert) *stdhttp.Client {
	tr := &stdhttp.Transport{
		TLSClientConfig: &tls.Config{RootCAs: certtest.Pool(certs...)},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		Protocols: new(stdhttp.Protocols),
	}
	if h2 {
		tr.Protocols.SetHTTP2(true)
	} else {
		tr.Protocols.SetHTTP1(true)
	}
	return &stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}
}

func tlsDoc(t *testing.T, protocols string, certs ...*certtest.Cert) string {
	t.Helper()
	var list []map[string]string
	for _, c := range certs {
		list = append(list, map[string]string{"cert_file": c.CertFile, "key_file": c.KeyFile})
	}
	tlsJSON, _ := json.Marshal(map[string]any{"certificates": list})
	return fmt.Sprintf(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"%s, "tls": %s}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`, protocols, tlsJSON)
}

func TestTLSByName(t *testing.T) {
	dir := t.TempDir()
	a, err := certtest.Write(dir, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	b, err := certtest.Write(dir, "b.example", "*.b.example")
	if err != nil {
		t.Fatal(err)
	}
	_, s := start(t, tlsDoc(t, "", a, b))
	addr := s.Addrs()[0].Addr.String()
	for _, tt := range []struct {
		host, want string
	}{{"a.example", "a.example"}, {"b.example", "b.example"}, {"www.b.example", "b.example"}} {
		for _, h2 := range []bool{false, true} {
			resp, err := tlsClient(addr, h2, a, b).Get("https://" + tt.host + "/")
			if err != nil {
				t.Fatalf("%s h2=%v: %v", tt.host, h2, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			wantProto := map[bool]string{false: "HTTP/1.1", true: "HTTP/2.0"}[h2]
			if string(body) != "ok" || resp.Proto != wantProto || resp.TLS.PeerCertificates[0].Subject.CommonName != tt.want ||
				resp.TLS.Version < tls.VersionTLS12 {
				t.Errorf("%s h2=%v: %s %q, certificate %s", tt.host, h2, resp.Proto, body, resp.TLS.PeerCertificates[0].Subject.CommonName)
			}
		}
	}
	// A client asking for no name, or one no certificate has, gets the first.
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, ServerName: "other.example"})
	if err != nil {
		t.Fatal(err)
	}
	if cn := conn.ConnectionState().PeerCertificates[0].Subject.CommonName; cn != "a.example" {
		t.Errorf("an unknown name got %s", cn)
	}
	conn.Close()
	// TLS 1.1 is refused.
	if c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11}); err == nil {
		c.Close()
		t.Error("TLS 1.1 was accepted")
	}
}

func TestTLSProtocols(t *testing.T) {
	a, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		protocols string
		h1, h2    bool
	}{
		{`, "protocols": ["http/1.1"]`, true, false},
		{`, "protocols": ["h2"]`, false, true},
	} {
		_, s := start(t, tlsDoc(t, tt.protocols, a))
		addr := s.Addrs()[0].Addr.String()
		for _, h2 := range []bool{false, true} {
			resp, err := tlsClient(addr, h2, a).Get("https://a.example/")
			ok := err == nil
			if err == nil {
				resp.Body.Close()
			}
			if want := map[bool]bool{false: tt.h1, true: tt.h2}[h2]; ok != want {
				t.Errorf("%s, h2=%v: served %v, want %v (%v)", tt.protocols, h2, ok, want, err)
			}
		}
	}
}

func TestTLSReloadNewCertificate(t *testing.T) {
	dir := t.TempDir()
	a, err := certtest.Write(dir, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	doc := func(c *certtest.Cert) string {
		return fmt.Sprintf(`{"listeners": [{"name": "web", "address": %q, "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`, addr, c.CertFile, c.KeyFile)
	}
	_, s := start(t, doc(a))
	// The same files, rewritten with a new certificate: the listener is bound
	// anew, and new connections get it.
	a2, err := certtest.Write(dir, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	added, _, err := s.Reload(compile(t, doc(a2)))
	if err != nil || len(added) != 1 {
		t.Fatalf("reload: %v, added %v", err, added)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := tlsClient(addr, false, a2).Get("https://a.example/")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the new certificate is not served: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
