package auth

import (
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/upstream"
)

func TestKeySetLookup(t *testing.T) {
	s := newKeySet([]*key{{id: "a"}, {}})
	if keys, known := s.lookup("a"); len(keys) != 2 || !known {
		t.Errorf("%d keys, known %v", len(keys), known)
	}
	if keys, known := s.lookup("b"); len(keys) != 1 || known {
		t.Errorf("an unknown kid: %d keys, known %v", len(keys), known)
	}
}

func TestParsePublicKeys(t *testing.T) {
	k := testKeys()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "k"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.p256.PublicKey, k.p256)
	if err != nil {
		t.Fatal(err)
	}
	x, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	xder, err := x509.MarshalPKIXPublicKey(x.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	block := func(typ string, b []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b}) }
	good := append(block("RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&k.rsa.PublicKey)), block("CERTIFICATE", der)...)
	good = append(good, block("PRIVATE KEY", []byte("skipped"))...)
	if keys, err := parsePublicKeys(good); err != nil || len(keys) != 2 {
		t.Errorf("%d keys, %v", len(keys), err)
	}
	for what, data := range map[string][]byte{
		"broken":  block("PUBLIC KEY", []byte("junk")),
		"X25519":  block("PUBLIC KEY", xder),
		"no keys": block("PRIVATE KEY", []byte("skipped")),
	} {
		if _, err := parsePublicKeys(data); err == nil {
			t.Errorf("%s: no error", what)
		}
	}
}

func TestJWKPublic(t *testing.T) {
	k := testKeys()
	ok := func(m map[string]any) *jwk {
		j := &jwk{}
		j.Kty, _ = m["kty"].(string)
		j.Crv, _ = m["crv"].(string)
		j.N, _ = m["n"].(string)
		j.E, _ = m["e"].(string)
		j.X, _ = m["x"].(string)
		j.Y, _ = m["y"].(string)
		return j
	}
	if ok(jwkOf(&k.p384.PublicKey, "")).public() == nil {
		t.Error("P-384")
	}
	even := ok(jwkOf(&k.rsa.PublicKey, ""))
	even.E = base64.RawURLEncoding.EncodeToString([]byte{4})
	other := ok(jwkOf(&k.p256.PublicKey, ""))
	other.Crv = "P-192"
	short := ok(jwkOf(&k.p256.PublicKey, ""))
	short.X = "AA"
	okp := ok(jwkOf(k.ed.Public(), ""))
	okp.Crv = "X25519"
	for what, j := range map[string]*jwk{"an even exponent": even, "another curve": other, "a short point": short, "X25519": okp, "no type": {}} {
		if j.public() != nil {
			t.Errorf("%s: a key", what)
		}
	}
}

func TestJWKSGet(t *testing.T) {
	get := func(u string) error {
		j := &jwks{url: u, client: &stdhttp.Client{Timeout: 5 * time.Second}, ctx: context.Background()}
		_, err := j.get()
		return err
	}
	if err := get("http://[::1"); err == nil {
		t.Error("a bad URL")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	l.Close()
	if err := get("http://" + closed + "/"); err == nil {
		t.Error("nothing listening")
	}
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		switch r.URL.Path {
		case "/500":
			w.WriteHeader(500)
		case "/large":
			_, _ = w.Write(make([]byte, maxJWKSBytes+10))
		case "/cut":
			conn, buf, _ := w.(stdhttp.Hijacker).Hijack()
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nabc")
			_ = buf.Flush()
			conn.Close()
		}
	}))
	defer s.Close()
	for _, p := range []string{"/500", "/large", "/cut"} {
		if err := get(s.URL + p); err == nil {
			t.Errorf("%s: no error", p)
		}
	}
}

// TestJWKSClosedWaiting checks a set closed while it waits out minRefetch,
// and one with as many waiters as it takes.
func TestJWKSClosedWaiting(t *testing.T) {
	defer func(d time.Duration) { minRefetch = d }(minRefetch)
	minRefetch = time.Minute
	s := newJWKSServer(t)
	s.serve(jwkOf(testKeys().ed.Public(), "ed"))
	j := newJWKS(s.URL, time.Hour, nil)
	deadline := time.Now().Add(5 * time.Second)
	for j.keys.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("no first fetch")
		}
		time.Sleep(5 * time.Millisecond)
	}
	j.wait(j.keys.Load(), func() {})
	time.Sleep(50 * time.Millisecond) // for it to wait out minRefetch
	j.close()
	select {
	case <-j.done:
	case <-time.After(5 * time.Second):
		t.Fatal("not stopped")
	}

	w := &jwks{wake: make(chan struct{}, 1), waiters: make([]func(), maxKeyWaiters)}
	if w.wait(nil, func() {}) {
		t.Error("past maxKeyWaiters")
	}
}

// TestJWTTooManyWaiting checks a token of an unknown kid when as many
// requests wait for a fetch as may: it is refused.
func TestJWTTooManyWaiting(t *testing.T) {
	k := testKeys()
	s := newJWKSServer(t)
	s.serve(jwkOf(k.ed.Public(), "ed"))
	jw := mustLoad[*JWT](t, "jwt", `{"jwks_url": "`+s.URL+`"}`)
	defer jw.Cleanup()
	h := jw.Wrap(claimsEcho)
	expectStatus(t, h, bearer(sign(t, "EdDSA", "ed", k.ed, valid())), 200, "ed")
	jw.jwks.mu.Lock()
	jw.jwks.waiters = make([]func(), maxKeyWaiters)
	for i := range jw.jwks.waiters {
		jw.jwks.waiters[i] = func() {}
	}
	jw.jwks.mu.Unlock()
	expectStatus(t, h, bearer(sign(t, "EdDSA", "other", k.ed, valid())), 401, "an unknown kid, too many waiting")
}

func TestJWS(t *testing.T) {
	k := testKeys()
	if (&key{alg: "RS256", pub: &k.rsa.PublicKey}).fits("RS384", algorithms["RS384"]) {
		t.Error("a key of another alg")
	}
	if (&key{pub: "a string"}).fits("RS256", algorithms["RS256"]) {
		t.Error("a key of no kind")
	}
	if (&key{pub: &k.p256.PublicKey}).verify(algorithms["ES256"], []byte("x"), []byte("short")) {
		t.Error("a signature of the wrong length")
	}
	if (&key{}).verify(algorithm{kind: 'Z'}, nil, nil) {
		t.Error("no algorithm")
	}
	enc := base64.RawURLEncoding
	h := enc.EncodeToString([]byte(`{"alg":"HS256"}`))
	for what, s := range map[string]string{
		"header":  "!!.e30.AA",
		"payload": h + ".!!.AA",
		"sig":     h + ".e30.!!",
	} {
		if _, err := parseToken(s); err == nil {
			t.Errorf("bad base64 in its %s", what)
		}
	}
	for _, payload := range []string{"null", "{} {}", "[]"} {
		if _, err := (&token{payload: []byte(payload)}).claims(); err == nil {
			t.Errorf("%s: claims", payload)
		}
	}
}

// signPayload is an HS256 token of payload as it is.
func signPayload(secret []byte, payload string) string {
	enc := base64.RawURLEncoding
	signed := enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + enc.EncodeToString([]byte(payload))
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(signed))
	return signed + "." + enc.EncodeToString(m.Sum(nil))
}

func TestJWTMore(t *testing.T) {
	k := testKeys()
	_, secret := keyFiles(t, k)
	h := mustLoad[*JWT](t, "jwt", `{"hmac_secret_file": "`+secret+`"}`).Wrap(claimsEcho)
	expectStatus(t, h, bearer(signPayload(k.secret, "[1]")), 401, "claims no object")

	large := filepath.Join(t.TempDir(), "large.pem")
	if err := os.WriteFile(large, make([]byte, 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ params, err string }{
		{`{"hmac_secret_file": "` + secret + `", "claims_headers": {"X-Sub": ""}}`, "names no claim"},
		{`{"public_key_files": ["` + large + `"]}`, "larger than 1 MiB"},
	} {
		if _, err := load(t, "jwt", tt.params); err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: %v, want %q", tt.params, err, tt.err)
		}
	}
	if ClaimsOf(fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "/", nil), &fibtest.Recorder{})) != nil {
		t.Error("claims of a request no jwt let in")
	}
}

// TestForwardAuthRequest checks the request sent to the auth service: the
// fields request_headers names alone, or all but the hop-by-hop ones and
// those Connection names, and the scheme of a request over TLS.
func TestForwardAuthRequest(t *testing.T) {
	r := httptest.NewRequest("GET", "https://example.com/p?q=1", nil)
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Authorization", "Bearer x")
	r.Header.Set("Connection", "X-Drop, keep-alive")
	r.Header.Set("X-Drop", "1")
	r.Header.Set("X-Keep", "1")
	c := fibhttp.NewStreamContext(nil, r, &fibtest.Recorder{})
	var u url.URL
	req := (&ForwardAuth{}).request(c, &u)
	if req.Header.Get("X-Forwarded-Proto") != "https" || req.Header.Get("X-Drop") != "" || req.Header.Get("Connection") != "" || req.Header.Get("X-Keep") != "1" {
		t.Errorf("all fields: %v", req.Header)
	}
	req = (&ForwardAuth{requestHeaders: []string{"Authorization", "X-Missing"}}).request(c, &u)
	if req.Header.Get("Authorization") != "Bearer x" || req.Header.Get("X-Keep") != "" || len(req.Header["X-Missing"]) != 0 {
		t.Errorf("request_headers: %v", req.Header)
	}
}

// TestForwardAuthDrain checks requests waiting for the auth service when
// their config is drained: 503.
func TestForwardAuthDrain(t *testing.T) {
	a := newAuthService(t)
	cfg := fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "auth", "addresses": [%q]}],
  "routes": [{"middleware": [{"module": "forward_auth", "params": {"upstream": "auth", "request_headers": ["Authorization"], "timeout": "30s"}}],
    "handler": {"module": "test_fields"}}]
}`, a.Listener.Addr().String())
	rt, s := start(t, cfg)
	rt.SetDrainTimeout(100 * time.Millisecond)
	base := "http://" + s.Addrs()[0].Addr.String()
	got := make(chan int, 1)
	go func() {
		ans, err := fetch(t, context.Background(), "GET", base+"/p", "Bearer hang")
		if err != nil {
			got <- 0
			return
		}
		got <- ans.code
	}()
	time.Sleep(300 * time.Millisecond) // for it to wait
	rt.Swap(compile(t, cfg))
	select {
	case code := <-got:
		if code != 503 {
			t.Errorf("drained: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("not drained")
	}
}

// endedRecorder is a Recorder told when its request has ended, every
// Retain of it released.
type endedRecorder struct {
	fibtest.Recorder
	ended chan struct{}
}

func (r *endedRecorder) RequestEnded() { close(r.ended) }

// gone returns the Context of a request for r whose client has left, which
// the test holds retained until it calls release.
func gone(t *testing.T, r *stdhttp.Request) (c *fibhttp.Context, rec *endedRecorder, release func()) {
	t.Helper()
	sr := &fibhttp.StreamRequest{Request: *r}
	rec = &endedRecorder{ended: make(chan struct{})}
	c = sr.Context(nil, rec, nil)
	fibhttp.Serve(fibhttp.HandlerFunc(func(c *fibhttp.Context) { c.Retain() }), c)
	sr.Cancel(errors.New("gone"))
	deadline := time.Now().Add(10 * time.Second)
	for c.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the request was not cancelled")
		}
		time.Sleep(time.Millisecond)
	}
	return c, rec, c.Release
}

// waitEnded waits for rec's request to end, and checks it was not answered.
func waitEnded(t *testing.T, rec *endedRecorder) {
	t.Helper()
	select {
	case <-rec.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the request did not end")
	}
	if rec.Written {
		t.Errorf("answered a client that left: %d", rec.Response.StatusCode)
	}
}

// nopHolder is a config nothing drains.
type nopHolder struct{}

func (nopHolder) Retain()  {}
func (nopHolder) Release() {}

// TestBasicCheckClientGone checks a password check whose client left while
// it waited: nothing is checked or answered.
func TestBasicCheckClientGone(t *testing.T) {
	c, rec, release := gone(t, httptest.NewRequest("GET", "/", nil))
	k := &check{ba: &Basic{graph: nopHolder{}}, c: c, queued: time.Now()}
	k.verify()
	release()
	waitEnded(t, rec)
}

// TestForwardAuthClientGoneFirst checks a request whose client left before
// its subrequest was sent: the subrequest is cancelled as soon as it is.
func TestForwardAuthClientGoneFirst(t *testing.T) {
	pool, err := upstream.New(nil, &config.Upstream{Name: "auth", Addresses: []string{"127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	fa := &ForwardAuth{pool: pool, timeout: time.Minute, graph: nopHolder{}, open: map[*authCall]struct{}{}}
	c, rec, release := gone(t, httptest.NewRequest("GET", "/", nil))
	fa.Wrap(claimsEcho).ServeHTTP(c)
	release()
	waitEnded(t, rec)
}

// TestJWTClientGoneWaiting checks a request that waits for the JWK set to
// be fetched again, for its token's unknown kid, and whose client leaves
// meanwhile: it is not answered once the set arrives.
func TestJWTClientGoneWaiting(t *testing.T) {
	defer func(d time.Duration) { minRefetch = d }(minRefetch)
	minRefetch = time.Millisecond
	k := testKeys()
	s := newJWKSServer(t)
	s.serve(jwkOf(k.ed.Public(), "ed"))
	jw := mustLoad[*JWT](t, "jwt", `{"jwks_url": "`+s.URL+`"}`)
	defer jw.Cleanup()
	h := jw.Wrap(claimsEcho)
	expectStatus(t, h, bearer(sign(t, "EdDSA", "ed", k.ed, valid())), 200, "ed")
	c, rec, release := gone(t, bearer(sign(t, "EdDSA", "other", k.ed, valid())))
	h.ServeHTTP(c)
	release()
	waitEnded(t, rec)
}
