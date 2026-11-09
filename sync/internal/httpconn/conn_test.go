package httpconn

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

func init() {
	BackoffFirst, BackoffMost = time.Millisecond, 5*time.Millisecond
}

// newConn makes a Conn of p, failing the test on an error of its params.
func newConn(t *testing.T, p Params) *Conn {
	t.Helper()
	c, errs := New(&module.Context{Params: config.Value{}}, p)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	t.Cleanup(c.Close)
	return c
}

func TestParamsErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	_ = os.WriteFile(bad, []byte("nothing"), 0o600)
	t.Setenv("HTTPCONN_EMPTY", "")
	for _, tc := range []struct {
		p    Params
		want string
	}{
		{Params{}, "no URLs"},
		{Params{URLs: []string{"ftp://x"}}, "not an http or https URL"},
		{Params{URLs: []string{"http://x/path"}}, "with no path"},
		{Params{URLs: []string{"http://x/p?q=1"}, AllowPath: true}, "with no query"},
		{Params{URLs: []string{"http://x"}, Username: "a:b"}, "no ':'"},
		{Params{URLs: []string{"http://x"}, Username: "u", BearerTokenEnv: "T"}, "not both"},
		{Params{URLs: []string{"http://x"}, BearerTokenEnv: "1bad"}, "not an environment variable's name"},
		{Params{URLs: []string{"http://x"}, BearerTokenEnv: "HTTPCONN_EMPTY"}, "not set"},
		{Params{URLs: []string{"http://x"}, PasswordEnv: "P"}, "needs username"},
		{Params{URLs: []string{"http://x"}, Username: "u", PasswordEnv: "HTTPCONN_EMPTY"}, "not set"},
		{Params{URLs: []string{"http://x"}, Header: map[string]string{"a b": "x"}}, "not a header field"},
		{Params{URLs: []string{"http://x"}, Header: map[string]string{"Authorization": "x"}}, "the connector's to set"},
		{Params{URLs: []string{"http://x"}, CAFile: filepath.Join(dir, "none")}, "no such file"},
		{Params{URLs: []string{"http://x"}, CAFile: bad}, "holds no PEM certificate"},
		{Params{URLs: []string{"http://x"}, CertFile: bad}, "go together"},
		{Params{URLs: []string{"http://x"}, CertFile: bad, KeyFile: bad}, "failed to find"},
	} {
		_, errs := New(&module.Context{Params: config.Value{}}, tc.p)
		if !strings.Contains(errs.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.p, errs, tc.want)
		}
	}
}

// TestDoAuthAndFailover sends requests through a URL that refuses
// connections and one that answers 503 to a server that answers: the next
// in turn is tried, and the one that answered is used first after.
func TestDoAuthAndFailover(t *testing.T) {
	var hits atomic.Int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]string{"auth": r.Header.Get("Authorization"), "x": r.Header.Get("X-Fixed"),
			"path": r.URL.Path, "query": r.URL.RawQuery, "body": string(b), "h": r.Header.Get("X-Req")})
	}))
	defer good.Close()
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer busy.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + ln.Addr().String()
	ln.Close()
	t.Setenv("HTTPCONN_PW", "secret")
	c := newConn(t, Params{URLs: []string{dead, busy.URL, good.URL + "/base/"}, Username: "u", PasswordEnv: "HTTPCONN_PW",
		Header: map[string]string{"X-Fixed": "1"}, AllowPath: true, Timeout: config.Duration(5 * time.Second)})
	ctx := context.Background()
	h := http.Header{}
	h.Set("X-Req", "r")
	resp, err := c.Do(ctx, &Request{Method: http.MethodPost, Path: "/p", Query: "a=1", Header: h, Body: []byte("hi")})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	_ = json.Unmarshal(resp.Body, &got)
	if got["auth"] != "Basic dTpzZWNyZXQ=" || got["x"] != "1" || got["path"] != "/base/p" || got["query"] != "a=1" || got["body"] != "hi" || got["h"] != "r" {
		t.Fatalf("%v", got)
	}
	if !strings.HasPrefix(good.URL, "http://"+resp.Host) {
		t.Fatalf("answered by %s", resp.Host)
	}
	if _, err := c.Do(ctx, &Request{Method: http.MethodGet, Path: "/again"}); err != nil || hits.Load() != 2 {
		t.Fatalf("%v, %d hits", err, hits.Load())
	}

	// Every URL down: the last status, or the last error.
	c2 := newConn(t, Params{URLs: []string{busy.URL}})
	if resp, err := c2.Do(ctx, &Request{Method: http.MethodGet}); err != nil || resp.Status != 503 {
		t.Fatalf("%v %v", resp, err)
	}
	c3 := newConn(t, Params{URLs: []string{dead}, BearerTokenEnv: "HTTPCONN_PW"})
	if _, err := c3.Do(ctx, &Request{Method: http.MethodGet}); err == nil {
		t.Fatal("a dead server answered")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Do(cctx, &Request{Method: http.MethodGet}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

// TestDoRedirects follows a frontend's redirect to a backend, with the
// credentials and the body, as far as the request allows.
func TestDoRedirects(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, r.Header.Get("Authorization")+" "+string(b)+" "+r.URL.Path)
	}))
	defer be.Close()
	var loc atomic.Value
	loc.Store(be.URL + "/api/load")
	fe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", loc.Load().(string))
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer fe.Close()
	c := newConn(t, Params{URLs: []string{fe.URL}, Username: "root"})
	ctx := context.Background()
	resp, err := c.Do(ctx, &Request{Method: http.MethodPut, Path: "/api/load", Body: []byte("rows"), Redirects: MaxRedirects})
	if err != nil || resp.Status != 200 || string(resp.Body) != "Basic cm9vdDo= rows /api/load" {
		t.Fatalf("%v %v %s", resp, err, resp.Body)
	}
	if resp, err := c.Do(ctx, &Request{Method: http.MethodPut, Body: []byte("rows")}); err != nil || resp.Status != http.StatusTemporaryRedirect {
		t.Fatalf("%v %v", resp, err)
	}
	loc.Store(fe.URL) // a loop: the last redirect is the answer
	if resp, err := c.Do(ctx, &Request{Method: http.MethodPut, Body: []byte("rows"), Redirects: 10}); err != nil || resp.Status != http.StatusTemporaryRedirect {
		t.Fatalf("%v %v", resp, err)
	}
	loc.Store("ftp://elsewhere")
	if _, err := c.Do(ctx, &Request{Method: http.MethodPut, Body: []byte("rows"), Redirects: 1}); err == nil || !strings.Contains(err.Error(), "not an http") {
		t.Fatalf("%v", err)
	}
}

// TestDoRetry sends a request again while the server answers 429 or 5xx,
// as Retry-After asks, and gives up after the attempts.
func TestDoRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
		case 2:
			w.WriteHeader(500)
		case 3:
			w.WriteHeader(408)
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()
	c := newConn(t, Params{URLs: []string{srv.URL}})
	ctx := context.Background()
	resp, err := c.DoRetry(ctx, &Request{Method: http.MethodPost, Body: []byte("x")}, 0)
	if err != nil || string(resp.Body) != "ok" || n.Load() != 4 {
		t.Fatalf("%v %v after %d", resp, err, n.Load())
	}
	n.Store(1) // 500 then 408: two attempts end at the 408
	if resp, err := c.DoRetry(ctx, &Request{Method: http.MethodPost}, 2); err != nil || resp.Status != 408 {
		t.Fatalf("%v %v", resp, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	n.Store(0)
	BackoffFirst = time.Hour
	defer func() { BackoffFirst = time.Millisecond }()
	BackoffMost = time.Hour
	defer func() { BackoffMost = 5 * time.Millisecond }()
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer busy.Close()
	c = newConn(t, Params{URLs: []string{busy.URL}})
	if _, err := c.DoRetry(cctx, &Request{Method: http.MethodGet}, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	cctx, cancel = context.WithCancel(ctx)
	cancel()
	if _, err := c.DoRetry(cctx, &Request{Method: http.MethodGet}, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if !Retryable(503) || Retryable(400) || !Retryable(429) {
		t.Fatal("Retryable")
	}
	e := &StatusError{Status: 400, Body: strings.Repeat("x", 600)}
	if !strings.Contains(e.Error(), "400") || !strings.HasSuffix(e.Error(), "...") {
		t.Fatal(e.Error())
	}
}

// TestTLS reaches a server of a CA of the test's own, with a client
// certificate, and fails without the CA.
func TestTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := selfSigned(t, dir)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tls")
	}))
	defer srv.Close()
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	ca := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(ca, pemCert, 0o600)
	c := newConn(t, Params{URLs: []string{srv.URL}, CAFile: ca, CertFile: certFile, KeyFile: keyFile, ServerName: "example.com"})
	resp, err := c.Do(context.Background(), &Request{Method: http.MethodGet})
	if err != nil || string(resp.Body) != "tls" {
		t.Fatalf("%v %v", resp, err)
	}
	c2 := newConn(t, Params{URLs: []string{srv.URL}})
	if _, err := c2.Do(context.Background(), &Request{Method: http.MethodGet}); err == nil {
		t.Fatal("an unknown CA was taken")
	}
}

// TestEngine uses the module context's engine when it has one, and an
// engine of its own, stopped by Close, when it has none or cannot make it.
func TestEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := newConn(t, Params{URLs: []string{srv.URL}})
	if _, err := c.Do(context.Background(), &Request{Method: http.MethodGet}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c.Close()
	old := newEngine
	defer func() { newEngine = old }()
	newEngine = func(fib.Config, ...fib.Binding) (*fib.Engine, error) { return nil, errors.New("no engine") }
	c2 := newConn(t, Params{URLs: []string{srv.URL}})
	if _, err := c2.Do(context.Background(), &Request{Method: http.MethodGet}); err == nil || err.Error() != "no engine" {
		t.Fatalf("%v", err)
	}
	if c2.timeout() != DefaultTimeout {
		t.Fatal(c2.timeout())
	}
}

func selfSigned(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	cf, kf := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	_ = os.WriteFile(cf, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(kf, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cf, kf
}
