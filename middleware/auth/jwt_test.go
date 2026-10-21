package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
)

// testKeys are one key of each kind, made once: RSA keys take a while.
var testKeys = sync.OnceValue(func() *keyring {
	k := &keyring{secret: []byte("0123456789abcdef0123456789abcdef")}
	var err error
	if k.rsa, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
	if k.rsa2, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
	if k.p256, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		panic(err)
	}
	if k.p384, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader); err != nil {
		panic(err)
	}
	if k.p521, err = ecdsa.GenerateKey(elliptic.P521(), rand.Reader); err != nil {
		panic(err)
	}
	if _, k.ed, err = ed25519.GenerateKey(rand.Reader); err != nil {
		panic(err)
	}
	return k
})

type keyring struct {
	rsa, rsa2        *rsa.PrivateKey
	p256, p384, p521 *ecdsa.PrivateKey
	ed               ed25519.PrivateKey
	secret           []byte
}

// signer returns the private key alg signs with in k.
func (k *keyring) signer(alg string) any {
	switch alg[:2] {
	case "RS", "PS":
		return k.rsa
	case "HS":
		return k.secret
	case "Ed":
		return k.ed
	}
	return map[string]*ecdsa.PrivateKey{"ES256": k.p256, "ES384": k.p384, "ES512": k.p521}[alg]
}

// sign makes a token of claims, its header alg and, if not empty, kid,
// signed with priv by alg's way.
func sign(t testing.TB, alg, kid string, priv any, claims map[string]any) string {
	t.Helper()
	h := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		h["kid"] = kid
	}
	return signRaw(t, alg, priv, h, claims)
}

func signRaw(t testing.TB, alg string, priv any, h, claims map[string]any) string {
	t.Helper()
	enc := base64.RawURLEncoding
	hj, _ := json.Marshal(h)
	cj, _ := json.Marshal(claims)
	signed := enc.EncodeToString(hj) + "." + enc.EncodeToString(cj)
	a, ok := algorithms[alg]
	var sig []byte
	var digest []byte
	if ok && a.kind != 'H' && a.kind != 'O' {
		hh := a.hash.New()
		hh.Write([]byte(signed))
		digest = hh.Sum(nil)
	}
	var err error
	switch {
	case !ok:
		sig = nil // none, or another the middleware does not know
	case a.kind == 'R':
		sig, err = rsa.SignPKCS1v15(rand.Reader, priv.(*rsa.PrivateKey), a.hash, digest)
	case a.kind == 'P':
		sig, err = rsa.SignPSS(rand.Reader, priv.(*rsa.PrivateKey), a.hash, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	case a.kind == 'E':
		var r, s *big.Int
		if r, s, err = ecdsa.Sign(rand.Reader, priv.(*ecdsa.PrivateKey), digest); err == nil {
			sig = make([]byte, 2*a.size)
			r.FillBytes(sig[:a.size])
			s.FillBytes(sig[a.size:])
		}
	case a.kind == 'O':
		sig = ed25519.Sign(priv.(ed25519.PrivateKey), []byte(signed))
	case a.kind == 'H':
		m := hmac.New(a.hash.New, priv.([]byte))
		m.Write([]byte(signed))
		sig = m.Sum(nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + enc.EncodeToString(sig)
}

// keyFiles writes k's public keys as PEM files, and its secret, in a
// directory of t's.
func keyFiles(t testing.TB, k *keyring) (pubs []string, secret string) {
	t.Helper()
	dir := t.TempDir()
	for i, pub := range []any{&k.rsa.PublicKey, &k.p256.PublicKey, &k.p384.PublicKey, &k.p521.PublicKey, k.ed.Public()} {
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, fmt.Sprint("pub", i, ".pem"))
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
			t.Fatal(err)
		}
		pubs = append(pubs, p)
	}
	secret = filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, k.secret, 0o600); err != nil {
		t.Fatal(err)
	}
	return pubs, secret
}

func jsonList(list []string) string {
	b, _ := json.Marshal(list)
	return string(b)
}

// claimsEcho answers with the request's claims and user.
var claimsEcho = fibhttp.HandlerFunc(func(c *fibhttp.Context) {
	b, _ := json.Marshal(ClaimsOf(c))
	_ = c.Respond(200, "text/plain", []byte("user="+UserOf(c)+" claims="+string(b)+" x-sub="+c.Request.Header.Get("X-Sub")+" x-roles="+c.Request.Header.Get("X-Roles")))
})

func bearer(token string) *stdhttp.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func valid() map[string]any {
	return map[string]any{"sub": "alice", "exp": time.Now().Add(time.Hour).Unix()}
}

func expectStatus(t *testing.T, h fibhttp.Handler, r *stdhttp.Request, code int, what string) *fibtest.Recorder {
	t.Helper()
	rec := fibtest.ServeRequest(h, r)
	if rec.Response.StatusCode != code {
		t.Errorf("%s: %d %q, want %d", what, rec.Response.StatusCode, rec.Response.Body, code)
	}
	return rec
}

func TestJWTAlgorithms(t *testing.T) {
	k := testKeys()
	pubs, secret := keyFiles(t, k)
	h := mustLoad[*JWT](t, "jwt", `{"public_key_files": `+jsonList(pubs)+`, "hmac_secret_file": "`+secret+`"}`).Wrap(claimsEcho)
	for name := range algorithms {
		expectStatus(t, h, bearer(sign(t, name, "", k.signer(name), valid())), 200, name)
	}

	// A key of the wrong size or kind, another key, none, and the RSA
	// public key taken for an HMAC secret.
	pemRSA, _ := os.ReadFile(pubs[0])
	for what, token := range map[string]string{
		"ES384 by a P-256 key":        sign(t, "ES384", "", k.p256, valid()),
		"RS256 by another key":        sign(t, "RS256", "", k.rsa2, valid()),
		"none":                        sign(t, "none", "", nil, valid()),
		"HS256 by the RSA public key": sign(t, "HS256", "", pemRSA, valid()),
	} {
		rec := expectStatus(t, h, bearer(token), 401, what)
		if got := rec.Response.Header.Get("Www-Authenticate"); got != `Bearer realm="fit", error="invalid_token"` {
			t.Errorf("%s: %q", what, got)
		}
	}

	only := mustLoad[*JWT](t, "jwt", `{"public_key_files": `+jsonList(pubs)+`, "algorithms": ["ES256"]}`).Wrap(claimsEcho)
	expectStatus(t, only, bearer(sign(t, "ES256", "", k.p256, valid())), 200, "allowed")
	expectStatus(t, only, bearer(sign(t, "RS256", "", k.rsa, valid())), 401, "not allowed")
}

func TestJWTClaims(t *testing.T) {
	k := testKeys()
	_, secret := keyFiles(t, k)
	jw := mustLoad[*JWT](t, "jwt", `{"hmac_secret_file": "`+secret+`", "issuer": "https://issuer.example",
  "audience": ["api", "web"], "leeway": "30s"}`)
	h := jw.Wrap(claimsEcho)
	now := time.Now().Unix()
	base := func(change map[string]any) map[string]any {
		c := map[string]any{"sub": "alice", "iss": "https://issuer.example", "aud": "api", "exp": now + 60}
		for name, v := range change {
			if v == nil {
				delete(c, name)
			} else {
				c[name] = v
			}
		}
		return c
	}
	for _, tt := range []struct {
		what   string
		claims map[string]any
		code   int
	}{
		{"valid", base(nil), 200},
		{"expired within the leeway", base(map[string]any{"exp": now - 20}), 200},
		{"expired", base(map[string]any{"exp": now - 40}), 401},
		{"no exp", base(map[string]any{"exp": nil}), 401},
		{"exp a string", base(map[string]any{"exp": fmt.Sprint(now + 60)}), 401},
		{"fractional exp", base(map[string]any{"exp": float64(now) + 60.5}), 200},
		{"nbf within the leeway", base(map[string]any{"nbf": now + 20}), 200},
		{"not yet", base(map[string]any{"nbf": now + 40}), 401},
		{"another issuer", base(map[string]any{"iss": "https://evil.example"}), 401},
		{"no issuer", base(map[string]any{"iss": nil}), 401},
		{"audiences", base(map[string]any{"aud": []any{"other", "web"}}), 200},
		{"another audience", base(map[string]any{"aud": []any{"other"}}), 401},
		{"no audience", base(map[string]any{"aud": nil}), 401},
	} {
		expectStatus(t, h, bearer(sign(t, "HS256", "", k.secret, tt.claims)), tt.code, tt.what)
	}

	good := sign(t, "HS256", "", k.secret, base(nil))
	parts := strings.Split(good, ".")
	enc := base64.RawURLEncoding
	evil, _ := json.Marshal(base(map[string]any{"sub": "root"}))
	for what, token := range map[string]string{
		"tampered":   parts[0] + "." + enc.EncodeToString(evil) + "." + parts[2],
		"two parts":  parts[0] + "." + parts[1],
		"four parts": good + ".x",
		"bad base64": parts[0] + ".!!." + parts[2],
		"crit":       signRaw(t, "HS256", k.secret, map[string]any{"alg": "HS256", "crit": []string{"exp"}}, base(nil)),
		"not JSON":   enc.EncodeToString([]byte("{")) + "." + parts[1] + "." + parts[2],
		"too long":   strings.Repeat("a", maxTokenBytes) + "." + parts[1] + "." + parts[2],
	} {
		expectStatus(t, h, bearer(token), 401, what)
	}

	// The clock is the middleware's: a token is let in until exp.
	jw.now = func() time.Time { return time.Unix(now+89, 0) }
	expectStatus(t, h, bearer(good), 200, "before exp and the leeway")
	jw.now = func() time.Time { return time.Unix(now+91, 0) }
	expectStatus(t, h, bearer(good), 401, "after exp and the leeway")
}

func TestJWTTokenFrom(t *testing.T) {
	k := testKeys()
	_, secret := keyFiles(t, k)
	token := sign(t, "HS256", "", k.secret, valid())
	load := func(from string) fibhttp.Handler {
		return mustLoad[*JWT](t, "jwt", `{"hmac_secret_file": "`+secret+`", "token_from": "`+from+`"}`).Wrap(claimsEcho)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "bearer  "+token)
	expectStatus(t, load("header"), r, 200, "Authorization, in lower case")
	rec := expectStatus(t, load("header"), httptest.NewRequest("GET", "/", nil), 401, "no token")
	if got := rec.Response.Header.Get("Www-Authenticate"); got != `Bearer realm="fit"` {
		t.Errorf("no token: %q", got)
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("alice", token)
	expectStatus(t, load("header"), r, 401, "Basic")

	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Token", token)
	expectStatus(t, load("header:x-token"), r, 200, "a field")
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&stdhttp.Cookie{Name: "session", Value: token})
	expectStatus(t, load("cookie:session"), r, 200, "a cookie")
	expectStatus(t, load("cookie:session"), bearer(token), 401, "no cookie")
	expectStatus(t, load("query:access_token"), httptest.NewRequest("GET", "/?access_token="+token, nil), 200, "a query")
}

func TestJWTClaimsHeaders(t *testing.T) {
	k := testKeys()
	_, secret := keyFiles(t, k)
	h := mustLoad[*JWT](t, "jwt", `{"hmac_secret_file": "`+secret+`", "claims_headers": {"x-sub": "sub", "X-Roles": "roles", "X-Missing": "nope"}}`).Wrap(claimsEcho)
	exp := time.Now().Add(time.Hour).Unix()
	r := bearer(sign(t, "HS256", "", k.secret, map[string]any{"sub": "alice\r\nX-Evil: 1", "roles": []string{"admin", "dev"}, "exp": exp}))
	r.Header.Set("X-Roles", "forged")
	r.Header.Set("X-Missing", "forged")
	rec := expectStatus(t, h, r, 200, "claims")
	want := fmt.Sprintf(`user=alice`+"\r\n"+`X-Evil: 1 claims={"exp":%d,"roles":["admin","dev"],"sub":"alice\r\nX-Evil: 1"} x-sub=alice??X-Evil: 1 x-roles=["admin","dev"]`, exp)
	if string(rec.Response.Body) != want {
		t.Errorf("got  %q\nwant %q", rec.Response.Body, want)
	}

	// A refused request still loses the fields.
	seen := ""
	probe := mustLoad[*JWT](t, "jwt", `{"hmac_secret_file": "`+secret+`", "claims_headers": {"X-Roles": "roles"}}`)
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Roles", "forged")
	expectStatus(t, probe.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {})), r, 401, "no token")
	seen = r.Header.Get("X-Roles")
	if seen != "" {
		t.Errorf("the client's field kept: %q", seen)
	}
}

func TestJWTParams(t *testing.T) {
	k := testKeys()
	pubs, secret := keyFiles(t, k)
	short := filepath.Join(t.TempDir(), "short")
	_ = os.WriteFile(short, []byte("too short"), 0o600)
	for _, tt := range []struct{ params, err string }{
		{`{}`, "a key is needed"},
		{`{"jwks_url": "ftp://x/keys"}`, "not an http or https URL"},
		{`{"public_key_files": ["` + secret + `"]}`, "no PEM public key"},
		{`{"public_key_files": ["/nonexistent"]}`, "public_key_files[0]"},
		{`{"hmac_secret_file": "` + short + `"}`, "at least 32 are needed"},
		{`{"public_key_files": ["` + pubs[0] + `"], "token_from": "body"}`, "is not header"},
		{`{"public_key_files": ["` + pubs[0] + `"], "token_from": "cookie:"}`, "is not header"},
		{`{"public_key_files": ["` + pubs[0] + `"], "claims_headers": {"a b": "sub"}}`, "not a field name"},
		{`{"public_key_files": ["` + pubs[0] + `"], "algorithms": ["none"]}`, `"none" is not one of`},
	} {
		if _, err := load(t, "jwt", tt.params); err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: %v, want %q", tt.params, err, tt.err)
		}
	}
}

// jwkOf is the JWK of pub, with kid.
func jwkOf(pub any, kid string) map[string]any {
	enc := base64.RawURLEncoding
	switch pub := pub.(type) {
	case *rsa.PublicKey:
		return map[string]any{"kty": "RSA", "kid": kid, "n": enc.EncodeToString(pub.N.Bytes()), "e": enc.EncodeToString(big.NewInt(int64(pub.E)).Bytes())}
	case *ecdsa.PublicKey:
		size := (pub.Curve.Params().BitSize + 7) / 8
		point, err := pub.Bytes()
		if err != nil {
			panic(err)
		}
		x, y := point[1:1+size], point[1+size:]
		return map[string]any{"kty": "EC", "kid": kid, "crv": pub.Curve.Params().Name, "x": enc.EncodeToString(x), "y": enc.EncodeToString(y)}
	case ed25519.PublicKey:
		return map[string]any{"kty": "OKP", "kid": kid, "crv": "Ed25519", "x": enc.EncodeToString(pub)}
	}
	panic(pub)
}

func TestParseJWKS(t *testing.T) {
	k := testKeys()
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	offCurve := jwkOf(&k.p256.PublicKey, "off")
	offCurve["y"] = offCurve["x"]
	set := map[string]any{"keys": []any{
		jwkOf(&k.rsa.PublicKey, "rsa"), jwkOf(&k.p256.PublicKey, "p256"), jwkOf(&k.p521.PublicKey, "p521"), jwkOf(k.ed.Public(), "ed"),
		map[string]any{"kty": "RSA", "kid": "enc", "use": "enc", "n": jwkOf(&k.rsa.PublicKey, "")["n"], "e": "AQAB"},
		map[string]any{"kty": "oct", "kid": "secret", "k": "c2VjcmV0"},
		jwkOf(&small.PublicKey, "small"), offCurve,
		map[string]any{"kty": "RSA", "kid": "hs", "alg": "HS256", "n": jwkOf(&k.rsa.PublicKey, "")["n"], "e": "AQAB"},
	}}
	data, _ := json.Marshal(set)
	keys, err := parseJWKS(data)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, k := range keys {
		ids = append(ids, k.id)
	}
	if strings.Join(ids, ",") != "rsa,p256,p521,ed" {
		t.Errorf("keys %v", ids)
	}
	for _, bad := range []string{`{"keys": []}`, `{"keys": [{"kty": "oct", "k": "AA"}]}`, `[`} {
		if _, err := parseJWKS([]byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

// jwksServer serves a JWK set that a test changes, counting the fetches.
type jwksServer struct {
	*httptest.Server
	mu      sync.Mutex
	set     []byte
	delay   time.Duration
	fetches atomic.Int32
}

func newJWKSServer(t *testing.T) *jwksServer {
	s := &jwksServer{}
	s.Server = httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		s.fetches.Add(1)
		s.mu.Lock()
		set, delay := s.set, s.delay
		s.mu.Unlock()
		time.Sleep(delay)
		_, _ = w.Write(set)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) serve(keys ...map[string]any) {
	data, _ := json.Marshal(map[string]any{"keys": keys})
	s.mu.Lock()
	s.set = data
	s.mu.Unlock()
}

// TestJWKSRotation checks that the JWK set is fetched when the config is
// loaded, a request before it waiting for it; that a token naming a new kid
// has it fetched again, and waits for it; and that a key the set dropped no
// longer verifies.
func TestJWKSRotation(t *testing.T) {
	defer func(d time.Duration) { minRefetch = d }(minRefetch)
	minRefetch = 50 * time.Millisecond
	k := testKeys()
	s := newJWKSServer(t)
	s.serve(jwkOf(&k.rsa.PublicKey, "one"))
	s.delay = 200 * time.Millisecond
	jw := mustLoad[*JWT](t, "jwt", `{"jwks_url": "`+s.URL+`"}`)
	defer jw.Cleanup()
	h := jw.Wrap(claimsEcho)

	one := sign(t, "RS256", "one", k.rsa, valid())
	expectStatus(t, h, bearer(one), 200, "before the first fetch")
	s.mu.Lock()
	s.delay = 0
	s.mu.Unlock()
	expectStatus(t, h, bearer(one), 200, "kid one")
	if n := s.fetches.Load(); n != 1 {
		t.Errorf("%d fetches", n)
	}

	// The keys rotate: a token of the new key has the set fetched again.
	s.serve(jwkOf(&k.p256.PublicKey, "two"))
	expectStatus(t, h, bearer(sign(t, "ES256", "two", k.p256, valid())), 200, "kid two, after a rotation")
	if n := s.fetches.Load(); n != 2 {
		t.Errorf("%d fetches", n)
	}
	expectStatus(t, h, bearer(one), 401, "kid one, dropped")
	expectStatus(t, h, bearer(sign(t, "RS256", "two", k.rsa, valid())), 401, "kid two by another key")

	// Unknown kids have it fetched at most every minRefetch.
	before := s.fetches.Load()
	start := time.Now()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			expectStatus(t, h, bearer(sign(t, "RS256", fmt.Sprint("unknown", i), k.rsa, valid())), 401, "an unknown kid")
		}()
	}
	wg.Wait()
	if n, most := s.fetches.Load()-before, int32(time.Since(start)/minRefetch)+2; n > most {
		t.Errorf("%d fetches for unknown kids in %v", n, time.Since(start))
	}
}

func TestJWKSRefresh(t *testing.T) {
	k := testKeys()
	s := newJWKSServer(t)
	s.serve(jwkOf(k.ed.Public(), "ed"))
	jw := mustLoad[*JWT](t, "jwt", `{"jwks_url": "`+s.URL+`", "jwks_refresh": "50ms"}`)
	defer jw.Cleanup()
	h := jw.Wrap(claimsEcho)
	expectStatus(t, h, bearer(sign(t, "EdDSA", "ed", k.ed, valid())), 200, "ed")
	deadline := time.Now().Add(5 * time.Second)
	for s.fetches.Load() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("%d fetches in 5s, refreshing every 50ms", s.fetches.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A set that fails to load keeps the keys there were.
	s.mu.Lock()
	s.set = []byte("{")
	s.mu.Unlock()
	n := s.fetches.Load()
	for s.fetches.Load() < n+2 {
		time.Sleep(10 * time.Millisecond)
	}
	expectStatus(t, h, bearer(sign(t, "EdDSA", "ed", k.ed, valid())), 200, "after a failed fetch")
	if err := jw.jwks.err.Load(); err == nil || !strings.Contains((*err).Error(), "fetching") {
		t.Errorf("the failure: %v", err)
	}
}

func TestJWTCache(t *testing.T) {
	k := testKeys()
	pubs, _ := keyFiles(t, k)
	jw := mustLoad[*JWT](t, "jwt", `{"public_key_files": `+jsonList(pubs)+`}`)
	h := jw.Wrap(claimsEcho)
	token := sign(t, "ES256", "", k.p256, valid())
	sum := sha256.Sum256([]byte(token))
	expectStatus(t, h, bearer(token), 200, "verified")
	if jw.verified.get(sum, jw.keySet()) == nil {
		t.Fatal("not kept")
	}
	expectStatus(t, h, bearer(token), 200, "kept")
	if jw.verified.get(sum, newKeySet()) != nil {
		t.Error("kept for other keys")
	}

	for i := range 20000 {
		jw.verified.put(sha256.Sum256([]byte(fmt.Sprint(i))), jw.keySet(), nil)
	}
	n := 0
	for i := range jw.verified.shards {
		n += len(jw.verified.shards[i].entries)
	}
	if n > cacheShards*cacheShardMax {
		t.Errorf("%d tokens kept", n)
	}
}

// BenchmarkJWT serves a request with a token through jwt, by algorithm,
// with the token verified anew and kept from before.
func BenchmarkJWT(b *testing.B) {
	k := testKeys()
	pubs, secret := keyFiles(b, k)
	jw := mustLoad[*JWT](b, "jwt", `{"public_key_files": `+jsonList(pubs)+`, "hmac_secret_file": "`+secret+`"}`)
	h := jw.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) { _ = c.Respond(200, "", nil) }))
	for _, alg := range []string{"HS256", "EdDSA", "ES256", "RS256"} {
		for _, kept := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/kept=%v", alg, kept), func(b *testing.B) {
				r := bearer(sign(b, alg, "", k.signer(alg), valid()))
				b.ReportAllocs()
				for b.Loop() {
					if !kept {
						for i := range jw.verified.shards {
							jw.verified.shards[i].entries = nil
						}
					}
					fibtest.ServeRequest(h, r)
				}
			})
		}
	}
}
