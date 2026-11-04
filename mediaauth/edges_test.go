package mediaauth

import (
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/observe"
)

// slowly runs p.slow and waits for its answer.
func slowly(p *Policy, name, pw string) (*user, error) {
	var u *user
	var err error
	var wg sync.WaitGroup
	wg.Add(1)
	p.slow(name, pw, func(u1 *user, e1 error) { u, err = u1, e1; wg.Done() })
	wg.Wait()
	return u, err
}

// A nil policy and a removed one log no one in and know no password; a
// bcrypt check that finds the policy removed is unavailable.
func TestLoginUnavailable(t *testing.T) {
	removed := &Policy{name: "gone"}
	removed.st.Store(&state{removed: true})
	for _, p := range []*Policy{nil, removed} {
		if _, _, err := login2(p, "alice", "alice-pw"); err != ErrUnavailable {
			t.Fatalf("%v logs in: %v", p, err)
		}
		if _, ok := p.Password("alice"); ok {
			t.Fatalf("%v knows a password", p)
		}
	}
	if _, err := slowly(removed, "bob", "bob-pw"); err != ErrUnavailable {
		t.Fatalf("a check of a removed policy: %v", err)
	}
}

// A bcrypt check of a user whose password is plaintext by then, after a
// reload, checks it as plaintext.
func TestSlowPlaintext(t *testing.T) {
	p := newPolicy_(t, policyConfig(t))
	if u, err := slowly(p, "alice", "alice-pw"); err != nil || u == nil || u.name != "alice" {
		t.Fatalf("alice's right password: %v %v", u, err)
	}
	if _, err := slowly(p, "alice", "wrong"); err != ErrBadCredentials {
		t.Fatalf("alice's wrong password: %v", err)
	}
	if _, err := slowly(p, "nobody", "x"); err != ErrBadCredentials {
		t.Fatalf("an unknown user: %v", err)
	}
}

// A token signed right whose payload is not base64, or not the claims, is
// refused.
func TestTokenPayload(t *testing.T) {
	p := newPolicy_(t, policyConfig(t))
	key := p.state().signKey
	for _, payload := range []string{"not*base64", base64.RawURLEncoding.EncodeToString([]byte(`{"sub": ""}`))} {
		tok := TokenPrefix + payload + "." + sign(key, payload)
		if _, err := p.Allow(Play, "live/a", Credentials{Token: tok}); err != ErrBadCredentials {
			t.Fatalf("%s: %v", payload, err)
		}
	}
}

// A keyring that does not compile: keys beside a secret, a key with both a
// secret and a secret_file, a key's inline secret too short, and a key file
// that is not there.
func TestKeyringCompileErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, tc := range []struct {
		what string
		cfg  config.MediaAuth
		want string
	}{
		{"keys beside secret", config.MediaAuth{Name: "k", Secret: "a-secret-of-sixteen-bytes",
			Keys: []config.MediaKey{{Key: "k1", Secret: "0123456789abcdef-key"}}}, "set keys, or secret or secret_file"},
		{"keys beside secret_file", config.MediaAuth{Name: "k", SecretFile: missing,
			Keys: []config.MediaKey{{Key: "k1", Secret: "0123456789abcdef-key"}}}, "set keys, or secret or secret_file"},
		{"a key with both", config.MediaAuth{Name: "k",
			Keys: []config.MediaKey{{Key: "k1", Secret: "0123456789abcdef-key", SecretFile: missing}}}, "keys.k1: set secret or secret_file"},
		{"a short key", config.MediaAuth{Name: "k",
			Keys: []config.MediaKey{{Key: "k1", Secret: "short"}}}, "keys.k1: the secret is 5 bytes"},
		{"a missing key file", config.MediaAuth{Name: "k",
			Keys: []config.MediaKey{{Key: "k1", SecretFile: missing}}}, "keys.k1: "},
	} {
		if _, err := New(&tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.what, err, tc.want)
		}
	}
}

// A removed policy rotates, issues and names no key, and a policy with one
// secret has no keyring to rotate or sign with.
func TestKeysUnavailable(t *testing.T) {
	const secret = "0123456789abcdef-rotated"
	removed := &Policy{name: "gone"}
	removed.st.Store(&state{removed: true})
	if err := removed.Rotate(secret); err != ErrUnavailable {
		t.Fatalf("Rotate of a removed policy: %v", err)
	}
	if err := removed.RotateKey("k1", secret); err != ErrUnavailable {
		t.Fatalf("RotateKey of a removed policy: %v", err)
	}
	if _, _, err := removed.IssueKey("k1", "a", Grant{Play: []string{"*"}}, time.Hour); err != ErrUnavailable {
		t.Fatalf("IssueKey of a removed policy: %v", err)
	}
	if keys := removed.Keys(); keys != nil {
		t.Fatalf("a removed policy's keys %v", keys)
	}
	if key := removed.DefaultKey(); key != "" {
		t.Fatalf("a removed policy's default key %q", key)
	}
	if _, ok := removed.KeyFile("k1"); ok {
		t.Fatal("a removed policy has a key file")
	}

	single := newPolicy_(t, policyConfig(t))
	if key := single.DefaultKey(); key != "" {
		t.Fatalf("a single secret's default key %q", key)
	}
	if err := single.RotateKey("k1", "short"); err == nil || !strings.Contains(err.Error(), "the secret is 5 bytes") {
		t.Fatalf("RotateKey with a short secret: %v", err)
	}
	if err := single.RotateKey("k1", secret); err == nil || !strings.Contains(err.Error(), "no keyring") {
		t.Fatalf("RotateKey of a single secret: %v", err)
	}
	if _, _, err := single.IssueKey("k1", "a", Grant{Play: []string{"*"}}, time.Hour); err == nil || !strings.Contains(err.Error(), "no keyring") {
		t.Fatalf("IssueKey of a single secret: %v", err)
	}
}

// RotateKey refuses to give a key the secret another key of the ring has,
// which would make a token verify under both, and leaves the key as it was.
func TestRotateKeySharedSecret(t *testing.T) {
	p := newPolicy_(t, config.MediaAuth{Name: "ring", Keys: []config.MediaKey{
		{Key: "k1", Secret: "0123456789abcdef-k1"},
		{Key: "k2", Secret: "0123456789abcdef-k2"},
	}})
	tok, _, err := p.IssueKey("k1", "a", Grant{Play: []string{"*"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RotateKey("k1", "0123456789abcdef-k2"); err == nil || !strings.Contains(err.Error(), `would share a secret with "k2"`) {
		t.Fatalf("RotateKey to k2's secret: %v", err)
	}
	if _, err := p.Allow(Play, "any", Credentials{Token: tok}); err != nil {
		t.Fatalf("k1's token after a refused rotation: %v", err)
	}
}

// A guarded request that is let through names its subject in the access
// log's entry, when the request has one.
func TestGuardNamesTheUser(t *testing.T) {
	p := newPolicy_(t, policyConfig(t))
	r := httptest.NewRequest("POST", "/whip/live/x", nil)
	r.Header.Set("Authorization", "Bearer "+staticToken)
	e := &observe.Entry{}
	var subject string
	rec := fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		c.SetValue(e)
		p.Guard(c, Publish, "live/x", func(s string) {
			subject = s
			_ = c.Respond(201, "text/plain", []byte("ok"))
		})
	}), r)
	if rec.Response.StatusCode != 201 || subject != "token:cam" || e.User != "token:cam" {
		t.Fatalf("a guarded request: %d %q, the entry's user %q", rec.Response.StatusCode, subject, e.User)
	}
}

// The role takes the config's media_auth member, and nothing else.
func TestRoleMemberType(t *testing.T) {
	if err := (&role{}).Apply("not a list"); err == nil || !strings.Contains(err.Error(), "a member of type string") {
		t.Fatalf("a member of the wrong type: %v", err)
	}
}
