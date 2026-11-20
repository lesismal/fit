package listener

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"os"
	"strconv"
	"strings"
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
	a, err := certtest.Write(dir, "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	doc := func(c *certtest.Cert, body string) string {
		return fmt.Sprintf(`{"listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "h3"],
    "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}],
  "routes": [{"handler": {"module": "respond", "params": {"body": %q}}}]}`, c.CertFile, c.KeyFile, body)
	}
	_, s := start(t, doc(a, "v1"))
	l := s.Addrs()[0]
	addr := l.Addr.String()
	// A connection open across the reload, on the old certificate.
	kept := tlsClient(addr, false, a)
	get := func(c *stdhttp.Client) (string, error) {
		resp, err := c.Get("https://a.example/")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return string(body), err
	}
	if body, err := get(kept); err != nil || body != "v1" {
		t.Fatalf("before: %q, %v", body, err)
	}

	// The same files, rewritten with a new certificate: the listener keeps
	// its binding and its connections, and new handshakes get the new one.
	a2, err := certtest.Write(dir, "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	added, removed, err := s.Reload(compile(t, doc(a2, "v2")))
	if err != nil || len(added) != 0 || len(removed) != 0 {
		t.Fatalf("reload: %v, added %v, removed %v", err, added, removed)
	}
	if body, err := get(kept); err != nil || body != "v2" {
		t.Errorf("the connection open across the reload: %q, %v", body, err)
	}
	for _, h2 := range []bool{false, true} {
		if body, err := get(tlsClient(addr, h2, a2)); err != nil || body != "v2" {
			t.Errorf("h2=%v, new certificate: %q, %v", h2, body, err)
		}
		if _, err := get(tlsClient(addr, h2, a)); err == nil {
			t.Errorf("h2=%v: the old certificate is still served", h2)
		}
	}
	target := fmt.Sprintf("https://127.0.0.1:%d/", l.UDPAddr.(*net.UDPAddr).Port)
	if _, body, err := h3Get(t, target, "a.example", a2); err != nil || body != "v2" {
		t.Errorf("h3, new certificate: %q, %v", body, err)
	}
	if _, _, err := h3Get(t, target, "a.example", a); err == nil {
		t.Error("h3: the old certificate is still served")
	}
}

// engine.kernel_tls_offload hands each TLS connection's sealing to the
// kernel right after its handshake: the listener's HTTP over TLS still
// answers, over HTTP/1.1 and HTTP/2, and on Linux with the kernel's TLS
// (/proc/net/tls_stat) the open connections count among the kernel's.
func TestKernelTLSOffload(t *testing.T) {
	a, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{false, true} {
		doc := strings.Replace(tlsDoc(t, "", a), `{"listeners"`, fmt.Sprintf(`{"engine": {"kernel_tls_offload": %v}, "listeners"`, on), 1)
		_, s := start(t, doc)
		if s.kernelTLS != on {
			t.Fatalf("kernel_tls_offload %v: the server took %v", on, s.kernelTLS)
		}
		addr := s.Addrs()[0].Addr.String()
		before, haveStat := kernelTLSSends()
		for _, h2 := range []bool{false, true} {
			c := tlsClient(addr, h2, a)
			defer c.CloseIdleConnections()
			for i := 0; i < 3; i++ {
				resp, err := c.Get("https://a.example/")
				if err != nil {
					t.Fatalf("kernel_tls_offload %v, h2=%v: %v", on, h2, err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(body) != "ok" {
					t.Fatalf("kernel_tls_offload %v, h2=%v: %q", on, h2, body)
				}
			}
		}
		// Others on the machine may add to the count, so only its growing
		// is checked.
		if after, _ := kernelTLSSends(); on && haveStat && after <= before {
			t.Errorf("kernel_tls_offload on: the kernel has sealed %d connections, %d before", after, before)
		}
	}
}

// kernelTLSSends is how many sockets the kernel's TLS has sealed since the
// system started, in software or on the network card, and whether the
// system tells. It only grows, so a test that opens one sees it grow
// whatever closes meanwhile; others on the machine may add to it too.
func kernelTLSSends() (int, bool) {
	data, err := os.ReadFile("/proc/net/tls_stat")
	if err != nil {
		return 0, false
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[0] == "TlsTxSw" || f[0] == "TlsTxDevice") {
			v, _ := strconv.Atoi(f[1])
			n += v
		}
	}
	return n, true
}

// A listener's tls.kernel_tls_offload decides for it, whatever
// engine.kernel_tls_offload says, and one that leaves it out follows the
// engine's; each still serves HTTPS, a reload that changes a listener's
// choice binds that listener anew and keeps the others, and on Linux with
// the kernel's TLS a listener that hands its connections over adds them to
// the kernel's count.
func TestKernelTLSOffloadPerListener(t *testing.T) {
	a, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	certs, _ := json.Marshal([]map[string]string{{"cert_file": a.CertFile, "key_file": a.KeyFile}})
	doc := func(engine bool, offload map[string]string) string {
		var ls []string
		for _, name := range []string{"on", "off", "inherit"} {
			field := ""
			if v, ok := offload[name]; ok {
				field = `, "kernel_tls_offload": ` + v
			}
			ls = append(ls, fmt.Sprintf(`{"name": %q, "address": "127.0.0.1:0", "tls": {"certificates": %s%s}}`, name, certs, field))
		}
		return fmt.Sprintf(`{"engine": {"kernel_tls_offload": %v}, "listeners": [%s],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`, engine, strings.Join(ls, ", "))
	}
	bindingOf := func(s *Server, name string) *binding {
		for _, b := range s.bound {
			if b.name == name {
				return b
			}
		}
		t.Fatalf("no listener %s", name)
		return nil
	}
	get := func(s *Server, name string) {
		t.Helper()
		c := tlsClient(addrOf(s, name), false, a)
		defer c.CloseIdleConnections()
		resp, err := c.Get("https://a.example/")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "ok" {
			t.Fatalf("%s: %q", name, body)
		}
	}
	for _, engine := range []bool{false, true} {
		_, s := start(t, doc(engine, map[string]string{"on": "true", "off": "false"}))
		for name, want := range map[string]bool{"on": true, "off": false, "inherit": engine} {
			if got := bindingOf(s, name).kernelTLS; got != want {
				t.Errorf("engine %v, listener %s: kernel TLS %v, want %v", engine, name, got, want)
			}
		}
		for _, name := range []string{"on", "off", "inherit"} {
			get(s, name)
		}
		// The kernel counts the connection it takes over.
		c := tlsClient(addrOf(s, "on"), false, a)
		before, haveStat := kernelTLSSends()
		if resp, err := c.Get("https://a.example/"); err == nil {
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		if after, _ := kernelTLSSends(); haveStat && after <= before {
			t.Errorf("engine %v: listener on's connection is not the kernel's (%d, %d before)", engine, after, before)
		}
		c.CloseIdleConnections()

		// A reload that turns off's on binds off anew, and keeps the rest.
		on, inherit := bindingOf(s, "on"), bindingOf(s, "inherit")
		if _, _, err := s.Reload(compile(t, doc(engine, map[string]string{"on": "true", "off": "true"}))); err != nil {
			t.Fatal(err)
		}
		if bindingOf(s, "on") != on || bindingOf(s, "inherit") != inherit {
			t.Errorf("engine %v: a reload that changed only off bound the others anew", engine)
		}
		if !bindingOf(s, "off").kernelTLS {
			t.Errorf("engine %v: off kept its old choice across the reload", engine)
		}
		get(s, "off")
	}
}
