package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/http3"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/listener"
	_ "github.com/lesismal/fit/middleware/limit"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".test_echo", New: func() any { return new(echoHandler) }})
}

// echoHandler answers with who the request was authenticated as, its
// X-Subject field, and how many bytes its body had, taking a streamed one
// with OnBody; a body that fails is let go unanswered.
type echoHandler struct{}

func (echoHandler) ServeHTTP(c *fibhttp.Context) {
	answer := func(n int) {
		_ = c.Respond(200, "text/plain", fmt.Appendf(nil, "user=%s subject=%s n=%d", UserOf(c), c.Request.Header.Get("X-Subject"), n))
	}
	if c.RequestBody() == nil {
		answer(len(c.Body()))
		return
	}
	c.Retain()
	n := 0
	c.OnBody(func(data []byte, fin bool, err error) {
		n += len(data)
		switch {
		case err != nil:
			c.Release()
		case fin:
			answer(n)
			c.Release()
		}
	})
}

// serve starts a listener for cfg, closed when the test ends, and returns
// its addresses.
func serve(t *testing.T, cfg string) listener.Addr {
	t.Helper()
	_, s := start(t, cfg)
	return s.Addrs()[0]
}

// start starts a server for cfg, closed when the test ends.
func start(t *testing.T, cfg string) (*runtime.Runtime, *listener.Server) {
	t.Helper()
	rt := new(runtime.Runtime)
	rt.Swap(compile(t, cfg))
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
	})
	return rt, s
}

func compile(t *testing.T, cfg string) *runtime.Graph {
	t.Helper()
	c, err := config.Load([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// pki is a server certificate, a CA its clients' certificates chain to, a
// client of it, and a client of another CA.
type pki struct {
	server          *certtest.Cert
	caFile          string
	client, unknown tls.Certificate
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	dir := t.TempDir()
	server, err := certtest.Write(dir, "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := &pki{server: server, caFile: filepath.Join(dir, "clients.pem")}
	for i, out := range []*tls.Certificate{&p.client, &p.unknown} {
		ca, err := certtest.NewCA(fmt.Sprintf("CA %d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := ca.WriteCert(p.caFile); err != nil {
				t.Fatal(err)
			}
		}
		c, err := ca.IssueClient(t.TempDir(), pkix.Name{CommonName: "alice", Organization: []string{"Example"}})
		if err != nil {
			t.Fatal(err)
		}
		if *out, err = tls.LoadX509KeyPair(c.CertFile, c.KeyFile); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// get fetches path over HTTP/1.1, h2 or h3 from a, as the client of cert,
// or of none when cert is nil.
func (p *pki) get(t *testing.T, a listener.Addr, proto, path string, cert *tls.Certificate) (int, string, error) {
	t.Helper()
	cfg := &tls.Config{RootCAs: certtest.Pool(p.server), ServerName: "a.example"}
	if cert != nil {
		// Sent whatever authorities the server names, which a client given
		// Certificates would match it to, sending none of another's.
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return cert, nil }
	}
	var res *stdhttp.Response
	var err error
	if proto == "h3" {
		engine, err := fib.New(fib.DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- engine.Run() }()
		defer func() {
			engine.Stop()
			<-done
			_ = engine.Close()
		}()
		hc := http3.DefaultClientConfig()
		hc.TLSConfig = cfg
		hc.Timeout = 5 * time.Second
		c := http3.NewClient(engine, hc)
		defer c.Close()
		req, _ := stdhttp.NewRequest("GET", "https://127.0.0.1:"+strconv.Itoa(a.UDPAddr.(*net.UDPAddr).Port)+path, nil)
		if res, err = c.Go(req).Wait(); err != nil {
			return 0, "", err
		}
	} else {
		tr := &stdhttp.Transport{
			TLSClientConfig: cfg,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, a.Addr.String())
			},
			Protocols: new(stdhttp.Protocols),
		}
		tr.Protocols.SetHTTP1(proto == "http/1.1")
		tr.Protocols.SetHTTP2(proto == "h2")
		defer tr.CloseIdleConnections()
		if res, err = (&stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}).Get("https://a.example" + path); err != nil {
			return 0, "", err
		}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body), nil
}

func mtlsDoc(p *pki, mode string) string {
	return fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "h3"],
    "tls": {"certificates": [{"cert_file": %q, "key_file": %q}],
      "client_auth": {"ca_files": [%q], "mode": %q}}}],
  "routes": [
    {"match": {"path": ["/open"]}, "middleware": [{"module": "client_cert", "params": {"optional": true, "subject_header": "x-subject"}}], "handler": {"module": "test_echo"}},
    {"middleware": [{"module": "client_cert", "params": {"subject_header": "x-subject"}}], "handler": {"module": "test_echo"}}
  ]
}`, p.server.CertFile, p.server.KeyFile, p.caFile, mode)
}

// TestClientAuthRequire checks a listener that requires client
// certificates: its CA's client is let in, with its subject passed on, and
// a client with none, or with another CA's, is refused in the handshake.
func TestClientAuthRequire(t *testing.T) {
	p := newPKI(t)
	a := serve(t, mtlsDoc(p, "require"))
	for _, proto := range []string{"http/1.1", "h2", "h3"} {
		code, body, err := p.get(t, a, proto, "/", &p.client)
		if want := "user=CN=alice,O=Example subject=CN=alice,O=Example n=0"; err != nil || code != 200 || body != want {
			t.Errorf("%s, a client of the CA: %v %d %q, want %q", proto, err, code, body, want)
		}
		for name, cert := range map[string]*tls.Certificate{"no certificate": nil, "another CA's": &p.unknown} {
			// TLS 1.3 clients learn of the refusal on their first read.
			if code, body, err := p.get(t, a, proto, "/open", cert); err == nil {
				t.Errorf("%s, %s: %d %q, want refused", proto, name, code, body)
			}
		}
	}
}

// TestClientAuthVerifyIfGiven checks a listener on which certificates are
// optional: client_cert refuses a request without one where it is required
// and lets it in where it is optional, and a certificate sent must still be
// valid.
func TestClientAuthVerifyIfGiven(t *testing.T) {
	p := newPKI(t)
	a := serve(t, mtlsDoc(p, "verify_if_given"))
	for _, proto := range []string{"http/1.1", "h2", "h3"} {
		for _, tt := range []struct {
			path string
			cert *tls.Certificate
			code int
			body string
		}{
			{"/", &p.client, 200, "user=CN=alice,O=Example subject=CN=alice,O=Example n=0"},
			{"/", nil, 403, "Forbidden\n"},
			{"/open", nil, 200, "user= subject= n=0"},
			{"/open", &p.client, 200, "user=CN=alice,O=Example subject=CN=alice,O=Example n=0"},
		} {
			if code, body, err := p.get(t, a, proto, tt.path, tt.cert); err != nil || code != tt.code || body != tt.body {
				t.Errorf("%s %s, certificate %v: %v %d %q, want %d %q", proto, tt.path, tt.cert != nil, err, code, body, tt.code, tt.body)
			}
		}
		if code, body, err := p.get(t, a, proto, "/open", &p.unknown); err == nil {
			t.Errorf("%s, another CA's: %d %q, want refused", proto, code, body)
		}
	}
}

// TestClientAuthReload checks that a reload that changes how a listener
// verifies clients binds it anew, and one that does not keeps it.
func TestClientAuthReload(t *testing.T) {
	p := newPKI(t)
	_, s := start(t, mtlsDoc(p, "require"))
	reload := func(doc string) []listener.Addr {
		t.Helper()
		added, _, err := s.Reload(compile(t, doc))
		if err != nil {
			t.Fatal(err)
		}
		return added
	}
	if added := reload(mtlsDoc(p, "require")); len(added) != 0 {
		t.Fatalf("the same client_auth bound anew: %+v", added)
	}
	if code, _, err := p.get(t, s.Addrs()[0], "h2", "/open", nil); err == nil {
		t.Fatalf("no certificate before the reload: %d", code)
	}
	if added := reload(mtlsDoc(p, "verify_if_given")); len(added) != 1 {
		t.Fatalf("another mode kept the binding: %+v", added)
	}
	if code, body, err := p.get(t, s.Addrs()[0], "h2", "/open", nil); err != nil || code != 200 {
		t.Fatalf("no certificate after the reload: %v %d %q", err, code, body)
	}
}

// TestBasicOverNetwork sends bodies through basic_auth, and a body_limit
// after it, over HTTP/1.1: the first request of each waits for its check
// on the pool with its body arriving, and the next is let in at once; the
// handler sees each body whole, or the limit refuses it.
func TestBasicOverNetwork(t *testing.T) {
	addr := serve(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"middleware": [
      {"module": "basic_auth", "params": {"users": [{"username": "alice", "password_hash": "`+hash(t, "pw")+`"}]}},
      {"module": "body_limit", "params": {"max_bytes": 1048576}}],
    "handler": {"module": "test_echo"}}]
}`).Addr.String()
	client := &stdhttp.Client{Timeout: 10 * time.Second}
	post := func(user, password string, size int, chunked bool) (int, string) {
		t.Helper()
		var body io.Reader = strings.NewReader(strings.Repeat("x", size))
		if chunked {
			body = io.MultiReader(body) // hides the length: sent chunked
		}
		req, _ := stdhttp.NewRequest("POST", "http://"+addr+"/", body)
		req.SetBasicAuth(user, password)
		res, err := client.Do(req)
		if err != nil {
			return 0, err.Error() // refused before the whole body was sent
		}
		defer res.Body.Close()
		got, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(got)
	}
	if code, _ := post("alice", "nope", 1<<20, true); code != 401 && code != 0 {
		t.Errorf("a wrong password: %d", code)
	}
	for i, tt := range []struct {
		size    int
		chunked bool
	}{{1 << 20, true}, {1 << 20, true}, {100, true}, {1 << 20, false}} {
		if code, body := post("alice", "pw", tt.size, tt.chunked); code != 200 || body != "user=alice subject= n="+strconv.Itoa(tt.size) {
			t.Errorf("%d: %d bytes, chunked %v: %d %q", i, tt.size, tt.chunked, code, body)
		}
	}
	if code, body := post("alice", "pw", 1<<20+1, true); code != 413 && code != 0 {
		t.Errorf("past the limit: %d %q", code, body)
	}
}
