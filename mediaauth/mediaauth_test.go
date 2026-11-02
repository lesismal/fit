package mediaauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

const staticToken = "0123456789abcdef-static"

func hashOf(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

// policyConfig is a policy with a static token, its hashed twin, a
// plaintext user, a bcrypt user and a secret.
func policyConfig(t *testing.T) config.MediaAuth {
	sum := sha256.Sum256([]byte("another-token-of-16+"))
	return config.MediaAuth{
		Name: "p",
		Tokens: []config.MediaToken{
			{Name: "cam", Token: staticToken, Publish: []string{"live/*"}, Play: []string{"*"}},
			{TokenSHA256: hex.EncodeToString(sum[:]), Play: []string{"room1-*"}},
		},
		Users: []config.MediaUser{
			{Name: "alice", Password: "alice-pw", Publish: []string{"alice"}, Play: []string{"*"}},
			{Name: "bob", PasswordHash: hashOf(t, "bob-pw"), Play: []string{"live/*"}},
		},
		Secret: "a-secret-of-sixteen-bytes",
	}
}

func newPolicy_(t *testing.T, cfg config.MediaAuth) *Policy {
	t.Helper()
	p, err := New(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMatch(t *testing.T) {
	for _, tt := range []struct {
		pattern, stream string
		want            bool
	}{
		{"*", "", true},
		{"*", "a/b/c", true},
		{"live/*", "live/cam1", true},
		{"live/*", "live/a/b", true},
		{"live/*", "live", false},
		{"live/*", "lives/x", false},
		{"room1-*", "room1-alice", true},
		{"room1-*", "room2-alice", false},
		{"cam", "cam", true},
		{"cam", "cam1", false},
		{"*-cam", "a-b-cam", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXbY", false},
		{"**", "x", true},
		{"a*a*a*a*b", strings.Repeat("a", 60), false},
	} {
		if got := Match(tt.pattern, tt.stream); got != tt.want {
			t.Errorf("Match(%q, %q) = %v", tt.pattern, tt.stream, got)
		}
	}
}

func TestAllow(t *testing.T) {
	p := newPolicy_(t, policyConfig(t))
	for _, tt := range []struct {
		name   string
		a      Action
		stream string
		c      Credentials
		sub    string
		err    error
	}{
		{"none", Play, "x", Credentials{}, "", ErrNoCredentials},
		{"static publish", Publish, "live/cam1", Credentials{Token: staticToken}, "token:cam", nil},
		{"static forbidden", Publish, "other", Credentials{Token: staticToken}, "token:cam", ErrForbidden},
		{"static unknown", Play, "x", Credentials{Token: "nope-nope-nope-nope"}, "", ErrBadCredentials},
		{"hashed token", Play, "room1-x", Credentials{Token: "another-token-of-16+"}, "token", nil},
		{"hashed token forbidden", Publish, "room1-x", Credentials{Token: "another-token-of-16+"}, "token", ErrForbidden},
		{"user", Publish, "alice", Credentials{User: "alice", Password: "alice-pw"}, "alice", nil},
		{"user forbidden", Publish, "bob", Credentials{User: "alice", Password: "alice-pw"}, "alice", ErrForbidden},
		{"user wrong", Publish, "alice", Credentials{User: "alice", Password: "nope"}, "", ErrBadCredentials},
		{"unknown user", Publish, "alice", Credentials{User: "carol", Password: "alice-pw"}, "", ErrPending},
		{"bcrypt user", Play, "live/x", Credentials{User: "bob", Password: "bob-pw"}, "", ErrPending},
		{"token first", Publish, "alice", Credentials{Token: "bad-bad-bad-bad-bad", User: "alice", Password: "alice-pw"}, "", ErrBadCredentials},
	} {
		sub, err := p.Allow(tt.a, tt.stream, tt.c)
		if sub != tt.sub || !errors.Is(err, tt.err) {
			t.Errorf("%s: %q %v, want %q %v", tt.name, sub, err, tt.sub, tt.err)
		}
	}
	var nilp *Policy
	if sub, err := nilp.Allow(Publish, "x", Credentials{}); sub != "" || err != nil || !nilp.Public(Play) {
		t.Errorf("a nil policy: %q %v", sub, err)
	}
	if Status(nil) != 0 || Status(ErrNoCredentials) != 401 || Status(ErrBadCredentials) != 401 ||
		Status(ErrForbidden) != 403 || Status(ErrBusy) != 503 || Status(ErrUnavailable) != 503 {
		t.Error("Status")
	}
	if pw, ok := p.Password("alice"); !ok || pw != "alice-pw" {
		t.Errorf("alice's password: %q %v", pw, ok)
	}
	if _, ok := p.Password("bob"); ok {
		t.Error("bob has only a hash")
	}
	if _, ok := p.Password("carol"); ok {
		t.Error("carol is no user")
	}
	if !p.HasUsers() || p.Name() != "p" || p.TTL() != config.DefaultMediaTokenTTL {
		t.Error("HasUsers, Name or TTL")
	}
}

func TestPublic(t *testing.T) {
	cfg := policyConfig(t)
	cfg.Public = []string{"play"}
	p := newPolicy_(t, cfg)
	if sub, err := p.Allow(Play, "anything", Credentials{}); sub != "" || err != nil {
		t.Errorf("public play: %q %v", sub, err)
	}
	if _, err := p.Allow(Publish, "live/x", Credentials{}); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("publish is not public: %v", err)
	}
	if !p.Public(Play) || p.Public(Publish) {
		t.Error("Public")
	}
}

// Check and Login do a bcrypt check on the pool, remember a password found
// right, and refuse a wrong one and an unknown user alike.
func TestBcrypt(t *testing.T) {
	p := newPolicy_(t, policyConfig(t))
	check := func(c Credentials, a Action, stream string) (string, error) {
		type res struct {
			s string
			e error
		}
		ch := make(chan res, 1)
		p.Check(a, stream, c, func(s string, e error) { ch <- res{s, e} })
		r := <-ch
		return r.s, r.e
	}
	if s, err := check(Credentials{User: "bob", Password: "wrong"}, Play, "live/x"); err != ErrBadCredentials || s != "" {
		t.Fatalf("a wrong password: %q %v", s, err)
	}
	if s, err := check(Credentials{User: "carol", Password: "x"}, Play, "live/x"); err != ErrBadCredentials || s != "" {
		t.Fatalf("an unknown user: %q %v", s, err)
	}
	if s, err := check(Credentials{User: "bob", Password: "bob-pw"}, Publish, "live/x"); err != ErrForbidden || s != "bob" {
		t.Fatalf("bob may not publish: %q %v", s, err)
	}
	if s, err := check(Credentials{User: "bob", Password: "bob-pw"}, Play, "live/x"); err != nil || s != "bob" {
		t.Fatalf("bob: %q %v", s, err)
	}
	// Remembered: at once now.
	if s, err := p.Allow(Play, "live/x", Credentials{User: "bob", Password: "bob-pw"}); err != nil || s != "bob" {
		t.Fatalf("bob remembered: %q %v", s, err)
	}
	if _, err := p.Allow(Play, "live/x", Credentials{User: "bob", Password: "other"}); err != ErrPending {
		t.Fatalf("another password is checked again: %v", err)
	}
	// Check with nothing slow answers before it returns.
	called := false
	p.Check(Play, "x", Credentials{Token: staticToken}, func(string, error) { called = true })
	if !called {
		t.Fatal("Check of a token did not answer at once")
	}

	login := func(user, pw string) (string, Grant, error) {
		type res struct {
			s string
			g Grant
			e error
		}
		ch := make(chan res, 1)
		p.Login(user, pw, func(s string, g Grant, e error) { ch <- res{s, g, e} })
		r := <-ch
		return r.s, r.g, r.e
	}
	if s, g, err := login("alice", "alice-pw"); err != nil || s != "alice" || g.Publish[0] != "alice" {
		t.Fatalf("alice's login: %q %v %v", s, g, err)
	}
	if _, _, err := login("alice", "x"); err != ErrBadCredentials {
		t.Fatalf("alice's wrong login: %v", err)
	}
	if s, g, err := login("bob", "bob-pw"); err != nil || s != "bob" || g.Play[0] != "live/*" {
		t.Fatalf("bob's login: %q %v %v", s, g, err)
	}
	p2 := newPolicy_(t, policyConfig(t))
	if s, _, err := login2(p2, "bob", "bob-pw"); err != nil || s != "bob" {
		t.Fatalf("bob's first login: %q %v", s, err)
	}
	if _, _, err := login2(p2, "nobody", "x"); err != ErrBadCredentials {
		t.Fatalf("an unknown user's login: %v", err)
	}
	var nilp *Policy
	if _, _, err := login2(nilp, "a", "b"); err != ErrUnavailable {
		t.Fatalf("a nil policy's login: %v", err)
	}
}

func login2(p *Policy, user, pw string) (string, Grant, error) {
	var s string
	var g Grant
	var e error
	var wg sync.WaitGroup
	wg.Add(1)
	p.Login(user, pw, func(s1 string, g1 Grant, e1 error) { s, g, e = s1, g1, e1; wg.Done() })
	wg.Wait()
	return s, g, e
}

// A pool with no room answers ErrBusy at once, and a check that waited too
// long is refused as busy too.
func TestPoolBusy(t *testing.T) {
	p := newPolicy_(t, policyConfig(t))
	block := make(chan struct{})
	// Fill the workers and the queue with checks that wait.
	checks.submit(func(bool) {}) // start the pool
	// Every worker held, and the queue full.
	for range cap(checks.jobs) + max(1, runtime.GOMAXPROCS(0)/4) {
		checks.jobs <- job{queued: time.Now(), run: func(bool) { <-block }}
	}
	got := make(chan error, 1)
	p.Check(Play, "live/x", Credentials{User: "bob", Password: "bob-pw"}, func(_ string, err error) { got <- err })
	close(block)
	if err := <-got; err != ErrBusy {
		t.Fatalf("a full pool: %v", err)
	}
	late := make(chan bool, 1)
	j := job{queued: time.Now().Add(-2 * maxWait), run: func(l bool) { late <- l }}
	checks.jobs <- j
	if !<-late {
		t.Fatal("a check queued past maxWait is not late")
	}
}

func TestIssue(t *testing.T) {
	p := newPolicy_(t, policyConfig(t))
	tok, exp, err := p.Issue("pub1", Grant{Publish: []string{"live/*"}}, time.Hour)
	if err != nil || !strings.HasPrefix(tok, TokenPrefix) || time.Until(exp) < 59*time.Minute {
		t.Fatalf("%q %v %v", tok, exp, err)
	}
	if s, err := p.Allow(Publish, "live/a", Credentials{Token: tok}); err != nil || s != "pub1" {
		t.Fatalf("its own token: %q %v", s, err)
	}
	if _, err := p.Allow(Play, "live/a", Credentials{Token: tok}); err != ErrForbidden {
		t.Fatalf("play with a publish token: %v", err)
	}
	// A policy of the same secret and name checks it; another name does not.
	if _, err := newPolicy_(t, policyConfig(t)).Allow(Publish, "live/a", Credentials{Token: tok}); err != nil {
		t.Fatalf("same secret and name: %v", err)
	}
	other := policyConfig(t)
	other.Name = "q"
	if _, err := newPolicy_(t, other).Allow(Publish, "live/a", Credentials{Token: tok}); err != ErrBadCredentials {
		t.Fatalf("another policy: %v", err)
	}
	other = policyConfig(t)
	other.Secret = "another-secret-of-16-bytes"
	if _, err := newPolicy_(t, other).Allow(Publish, "live/a", Credentials{Token: tok}); err != ErrBadCredentials {
		t.Fatalf("another secret: %v", err)
	}
	// Without a secret, the key is the policy's own.
	noSecret := policyConfig(t)
	noSecret.Secret = ""
	a, b := newPolicy_(t, noSecret), newPolicy_(t, noSecret)
	tok2, _, _ := a.Issue("x", Grant{Play: []string{"*"}}, 0)
	if _, err := a.Allow(Play, "s", Credentials{Token: tok2}); err != nil {
		t.Fatalf("a's token on a: %v", err)
	}
	if _, err := b.Allow(Play, "s", Credentials{Token: tok2}); err != ErrBadCredentials {
		t.Fatalf("a's token on b: %v", err)
	}

	// Tampering: the payload, the MAC, the form.
	body := strings.TrimPrefix(tok, TokenPrefix)
	payload, mac, _ := strings.Cut(body, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(payload)
	forged := TokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(raw), "live/*", "*", 1))) + "." + mac
	for _, bad := range []string{
		forged,
		TokenPrefix + payload + "." + mac[:len(mac)-2] + "AA",
		TokenPrefix + payload,
		TokenPrefix + "!!!." + mac,
		TokenPrefix + strings.Repeat("A", MaxTokenBytes),
	} {
		if _, err := p.Allow(Publish, "live/a", Credentials{Token: bad}); err != ErrBadCredentials {
			t.Errorf("tampered %q: %v", bad[:min(len(bad), 40)], err)
		}
	}
	// A valid MAC over what is not claims.
	st := p.state()
	junk := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	if _, err := p.Allow(Play, "x", Credentials{Token: TokenPrefix + junk + "." + sign(st.signKey, junk)}); err != ErrBadCredentials {
		t.Errorf("a signed payload that is not claims: %v", err)
	}

	// Expiry.
	defer func(f func() time.Time) { now = f }(now)
	now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := p.Allow(Publish, "live/a", Credentials{Token: tok}); err != ErrBadCredentials {
		t.Fatalf("an expired token: %v", err)
	}
	now = time.Now

	for _, tt := range []struct {
		subject string
		g       Grant
	}{
		{"", Grant{Play: []string{"*"}}},
		{strings.Repeat("x", 129), Grant{Play: []string{"*"}}},
		{"a\nb", Grant{Play: []string{"*"}}},
		{"x", Grant{Play: []string{"bad pattern"}}},
		{"x", Grant{Play: []string{strings.Repeat("a", 250), strings.Repeat("b", 250), strings.Repeat("c", 250),
			strings.Repeat("d", 250), strings.Repeat("e", 250), strings.Repeat("f", 250), strings.Repeat("g", 250),
			strings.Repeat("h", 250), strings.Repeat("i", 250), strings.Repeat("j", 250), strings.Repeat("k", 250),
			strings.Repeat("l", 250), strings.Repeat("m", 250)}}},
	} {
		if _, _, err := p.Issue(tt.subject, tt.g, time.Hour); err == nil {
			t.Errorf("Issue(%q, %v) is no error", tt.subject, tt.g)
		}
	}
}

func TestActions(t *testing.T) {
	for _, a := range []Action{Publish, Play} {
		if b, ok := ParseAction(a.String()); !ok || b != a {
			t.Errorf("%v", a)
		}
	}
	if _, ok := ParseAction("x"); ok || Action(9).String() != "unknown" {
		t.Error("an unknown action")
	}
	if (Credentials{}).Empty() != true || (Credentials{User: "a"}).Empty() {
		t.Error("Empty")
	}
}

func TestCompileErrors(t *testing.T) {
	for _, cfg := range []config.MediaAuth{
		{Name: "a", Public: []string{"watch"}},
		{Name: "a", Tokens: []config.MediaToken{{Publish: []string{"*"}}}},
		{Name: "a", Tokens: []config.MediaToken{{Token: staticToken, Play: []string{"a b"}}}},
		{Name: "a", Users: []config.MediaUser{{Name: "u", Play: []string{"*"}}}},
		{Name: "a", Users: []config.MediaUser{{Name: "u", Password: "x", Publish: []string{"a?"}}}},
		{Name: "a", Users: []config.MediaUser{{Name: "u", PasswordHash: "$2not-a-hash"}}},
		{Name: "a", Users: []config.MediaUser{{Name: "u", Password: "x"}, {Name: "u", Password: "y"}}},
	} {
		if _, err := New(&cfg); err == nil {
			t.Errorf("%+v compiles", cfg)
		}
	}
}

func TestFromHTTP(t *testing.T) {
	r := httptest.NewRequest("POST", "/whip/x?a=1&token=secret-token&b=2", nil)
	if c := FromHTTP(r); c.Token != "secret-token" {
		t.Fatalf("query: %+v", c)
	}
	if r.RequestURI != "/whip/x?a=1&token=REDACTED&b=2" || r.URL.Query().Get("token") != "secret-token" {
		t.Fatalf("redacted: %q %q", r.RequestURI, r.URL.RawQuery)
	}
	r = httptest.NewRequest("POST", "/whip/x?token=q", nil)
	r.Header.Set("Authorization", "Bearer  hdr ")
	if c := FromHTTP(r); c.Token != "hdr" || r.RequestURI != "/whip/x?token=q" {
		t.Fatalf("bearer first: %+v %q", c, r.RequestURI)
	}
	r = httptest.NewRequest("POST", "/whip/x", nil)
	r.SetBasicAuth("alice", "pw:with:colons")
	if c := FromHTTP(r); c.User != "alice" || c.Password != "pw:with:colons" {
		t.Fatalf("basic: %+v", c)
	}
	r = httptest.NewRequest("POST", "/whip/x", nil)
	r.AddCookie(&stdhttp.Cookie{Name: CookieName, Value: "ck"})
	if c := FromHTTP(r); c.Token != "ck" {
		t.Fatalf("cookie: %+v", c)
	}
	r = httptest.NewRequest("POST", "/whip/x?notoken=1", nil)
	r.Header.Set("Authorization", "Digest x")
	if c := FromHTTP(r); !c.Empty() {
		t.Fatalf("nothing: %+v", c)
	}
	if redact("/x") != "/x" {
		t.Fatal("redact without a query")
	}

	if c := FromQuery("?token=t1&user=u"); c.Token != "t1" {
		t.Fatalf("FromQuery token: %+v", c)
	}
	if c := FromQuery("user=u&pass=p%26"); c.User != "u" || c.Password != "p&" {
		t.Fatalf("FromQuery user: %+v", c)
	}
	if c := FromQuery("x=1"); !c.Empty() {
		t.Fatalf("FromQuery nothing: %+v", c)
	}
}

func TestGuard(t *testing.T) {
	p := newPolicy_(t, policyConfig(t))
	serve := func(p *Policy, a Action, stream string, setup func(*stdhttp.Request)) (*fibtest.Recorder, string, bool) {
		r := httptest.NewRequest("POST", "/whip/"+stream, nil)
		if setup != nil {
			setup(r)
		}
		var subject string
		var called bool
		rec := fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
			p.Guard(c, a, stream, func(s string) {
				subject, called = s, true
				_ = c.Respond(201, "text/plain", []byte("ok"))
			})
		}), r)
		return rec, subject, called
	}
	rec, _, called := serve(p, Publish, "live/x", nil)
	if called || rec.Response.StatusCode != 401 || rec.Response.Header.Get("Www-Authenticate") != `Bearer realm="fit"` {
		t.Fatalf("no credentials: %d %v", rec.Response.StatusCode, rec.Response.Header)
	}
	rec, _, _ = serve(p, Publish, "live/x", func(r *stdhttp.Request) { r.Header.Set("Authorization", "Bearer wrong-wrong-wrong") })
	if rec.Response.StatusCode != 401 || !strings.Contains(rec.Response.Header.Get("Www-Authenticate"), "invalid_token") {
		t.Fatalf("a bad token: %d %v", rec.Response.StatusCode, rec.Response.Header)
	}
	rec, _, _ = serve(p, Publish, "other", func(r *stdhttp.Request) { r.Header.Set("Authorization", "Bearer "+staticToken) })
	if rec.Response.StatusCode != 403 {
		t.Fatalf("forbidden: %d", rec.Response.StatusCode)
	}
	rec, sub, called := serve(p, Publish, "live/x", func(r *stdhttp.Request) { r.Header.Set("Authorization", "Bearer "+staticToken) })
	if rec.Response.StatusCode != 201 || !called || sub != "token:cam" {
		t.Fatalf("allowed: %d %v %q", rec.Response.StatusCode, called, sub)
	}
	// A bcrypt user goes to the pool and on from there.
	rec, sub, called = serve(p, Play, "live/x", func(r *stdhttp.Request) { r.SetBasicAuth("bob", "bob-pw") })
	if rec.Response.StatusCode != 201 || !called || sub != "bob" {
		t.Fatalf("bob: %d %v %q", rec.Response.StatusCode, called, sub)
	}
	rec, _, _ = serve(p, Play, "live/x", func(r *stdhttp.Request) { r.SetBasicAuth("bob", "nope") })
	if rec.Response.StatusCode != 401 {
		t.Fatalf("bob's wrong password: %d", rec.Response.StatusCode)
	}
	rec, sub, called = serve(nil, Play, "x", nil)
	if rec.Response.StatusCode != 201 || !called || sub != "" {
		t.Fatalf("a nil policy: %d", rec.Response.StatusCode)
	}
	Remove(nil)
	removed := &Policy{name: "gone"}
	removed.st.Store(&state{removed: true})
	rec, _, _ = serve(removed, Play, "x", nil)
	if rec.Response.StatusCode != 503 || rec.Response.Header.Get("Retry-After") == "" {
		t.Fatalf("a removed policy: %d", rec.Response.StatusCode)
	}
	if _, _, err := removed.Issue("x", Grant{}, 0); err != ErrUnavailable {
		t.Fatalf("a removed policy issues: %v", err)
	}
	if removed.TTL() != config.DefaultMediaTokenTTL || removed.HasUsers() || removed.Public(Play) {
		t.Fatal("a removed policy's TTL, HasUsers or Public")
	}
	if _, ok := removed.Password("a"); ok {
		t.Fatal("a removed policy's password")
	}
	if _, err := removed.Allow(Play, "x", Credentials{}); err != ErrUnavailable {
		t.Fatal("a removed policy allows")
	}
	busyRec := fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) { WriteError(c, ErrBusy) }), httptest.NewRequest("GET", "/", nil))
	if busyRec.Response.StatusCode != 503 {
		t.Fatalf("busy: %d", busyRec.Response.StatusCode)
	}
}

// Policies found by engine and name change in place with Apply; those it
// leaves out are refused; Provision makes a new one from the config being
// compiled, and leaves one applied to the role.
func TestRegistry(t *testing.T) {
	key := new(fib.Engine)
	t.Cleanup(func() { Remove(key) })
	cfg := policyConfig(t)
	if err := Apply(key, []config.MediaAuth{cfg}); err != nil {
		t.Fatal(err)
	}
	p := Lookup(key, "p")
	if p == nil || Lookup(key, "q") != nil {
		t.Fatal("Lookup")
	}
	// bob's password, remembered, survives a reload that keeps his hash.
	done := make(chan error, 1)
	p.Check(Play, "live/x", Credentials{User: "bob", Password: "bob-pw"}, func(_ string, err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cfg2 := policyConfig(t)
	cfg2.Users[1].PasswordHash = cfg.Users[1].PasswordHash
	cfg2.Tokens[0].Publish = []string{"other/*"}
	if err := Apply(key, []config.MediaAuth{cfg2}); err != nil {
		t.Fatal(err)
	}
	if Lookup(key, "p") != p {
		t.Fatal("the policy is not the same after a reload")
	}
	if _, err := p.Allow(Publish, "live/x", Credentials{Token: staticToken}); err != ErrForbidden {
		t.Fatalf("the token's grant did not change: %v", err)
	}
	if _, err := p.Allow(Play, "live/x", Credentials{User: "bob", Password: "bob-pw"}); err != nil {
		t.Fatalf("bob's remembered password is forgotten: %v", err)
	}
	// An error changes nothing.
	bad := policyConfig(t)
	bad.Public = []string{"nope"}
	if err := Apply(key, []config.MediaAuth{bad}); err == nil {
		t.Fatal("a bad policy applies")
	}
	if err := Apply(key, []config.MediaAuth{cfg2, cfg2}); err == nil {
		t.Fatal("a policy listed twice applies")
	}
	if _, err := p.Allow(Publish, "other/x", Credentials{Token: staticToken}); err != nil {
		t.Fatalf("after a failed apply: %v", err)
	}
	// Provision: an unknown name, an applied policy left to the role, and
	// a new one made from the config.
	q := cfg2
	q.Name = "q"
	q.Tokens[0].Publish = []string{"q/*"}
	env := &module.Env{Engine: key, MediaAuth: []config.MediaAuth{cfg, q}}
	if _, err := provision(env, "nope"); err == nil || !strings.Contains(err.Error(), `no media_auth policy named "nope"`) {
		t.Fatalf("an unknown policy: %v", err)
	}
	if got, err := provision(env, "p"); err != nil || got != p {
		t.Fatalf("an applied policy: %v", err)
	}
	if _, err := p.Allow(Publish, "other/x", Credentials{Token: staticToken}); err != nil {
		t.Fatalf("Provision changed an applied policy: %v", err)
	}
	pq, err := provision(env, "q")
	if err != nil || pq == nil {
		t.Fatal(err)
	}
	if _, err := pq.Allow(Publish, "q/x", Credentials{Token: staticToken}); err != nil {
		t.Fatalf("a provisioned policy: %v", err)
	}
	if got, err := provision(env, ""); got != nil || err != nil {
		t.Fatal("no name is no policy")
	}
	badEnv := &module.Env{Engine: key, MediaAuth: []config.MediaAuth{bad}}
	bad.Name = "p"
	if _, err := provision(badEnv, "p"); err == nil {
		t.Fatal("a policy that does not compile provisions")
	}
	// Apply leaving p out: p is refused, q is the one.
	if err := Apply(key, []config.MediaAuth{q}); err != nil {
		t.Fatal(err)
	}
	if Lookup(key, "p") != nil || Lookup(key, "q") != pq {
		t.Fatal("after p is left out")
	}
	if _, err := p.Allow(Play, "x", Credentials{Token: staticToken}); err != ErrUnavailable {
		t.Fatalf("a removed policy: %v", err)
	}
	if err := Apply(key, nil); err != nil || Lookup(key, "q") != nil {
		t.Fatal("Apply of none")
	}
}

// provision runs Provision as a module's Provision would, through
// module.Load.
func provision(env *module.Env, name string) (*Policy, error) {
	var got *Policy
	id := "test.mediaauth." + strings.ReplaceAll(name, ".", "_") + "_" + time.Now().Format("150405.000000000")
	module.Register(module.Info{ID: "http.handlers." + strings.ReplaceAll(id, ".", "_"), New: func() any {
		return &probe{name: name, got: &got}
	}})
	params, err := config.Load([]byte(`{"listeners": [{"name": "w", "address": ":0"}], "routes": [{"handler": {"module": "` +
		strings.ReplaceAll(id, ".", "_") + `", "params": {}}}]}`))
	if err != nil {
		return nil, err
	}
	_, err = module.Load("http.handlers", params.Routes[0].Handler, env)
	return got, err
}

type probe struct {
	name string
	got  **Policy
}

func (p *probe) Provision(ctx *module.Context) error {
	pol, err := Provision(ctx, "auth", p.name)
	*p.got = pol
	return err
}

func (p *probe) ServeHTTP(*fibhttp.Context) {}
