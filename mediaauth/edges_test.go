package mediaauth

import (
	"encoding/base64"
	"sync"
	"testing"
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
