package mediaauth

import (
	"errors"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestPasskeys checks users' passkeys and tokens taken as passkeys: their
// grants, keyrings and rotation, and what is refused.
func TestPasskeys(t *testing.T) {
	cfg := &config.MediaAuth{
		Name:   "bt",
		Secret: "0123456789abcdef0123456789",
		Users: []config.MediaUser{
			{Name: "alice", Password: "alice-password", Play: []string{"bittorrent/*"}},
			{Name: "bob", PasswordHash: "$2a$04$abcdefghijklmnopqrstuuJ8Hn3lsfm2nAHxt0fdXAbMGKHMbJXdC", Play: []string{"bittorrent/aa"}},
		},
		Tokens: []config.MediaToken{{Name: "t", Token: "token-0123456789ab", Play: []string{"bittorrent/*"}}},
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := p.Passkey("alice")
	if err != nil || len(alice) != PasskeyChars {
		t.Fatalf("alice %q %v", alice, err)
	}
	bob, err := p.Passkey("bob")
	if err != nil || bob == alice {
		t.Fatalf("bob %q %v", bob, err)
	}
	if again, _ := p.Passkey("alice"); again != alice {
		t.Fatal("a passkey changed")
	}
	if _, err := p.Passkey("carol"); err == nil {
		t.Fatal("a passkey for no user")
	}
	for _, c := range []struct {
		stream, passkey, subject string
		err                      error
	}{
		{"bittorrent/aa", alice, "alice", nil},
		{"bittorrent/aa", bob, "bob", nil},
		{"bittorrent/bb", bob, "bob", ErrForbidden},
		{"bittorrent/bb", "token-0123456789ab", "token:t", nil},
		{"bittorrent/bb", "", "", ErrNoCredentials},
		{"bittorrent/bb", "nope", "", ErrBadCredentials},
		{"bittorrent/bb", "alice-password", "", ErrBadCredentials},
	} {
		subject, err := p.AllowPasskey(c.stream, c.passkey)
		if subject != c.subject || !errors.Is(err, c.err) {
			t.Errorf("%s with %q: %q %v, want %q %v", c.stream, c.passkey, subject, err, c.subject, c.err)
		}
	}

	// Another secret: other passkeys.
	cfg2 := *cfg
	cfg2.Secret = "another-secret-0123456789"
	p2, err := New(&cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p2.AllowPasskey("bittorrent/aa", alice); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a passkey of another secret: %v", err)
	}

	// A keyring: the default key's passkey is handed out, every key's is
	// taken.
	ring := *cfg
	ring.Secret = ""
	ring.Keys = []config.MediaKey{{Key: "new", Secret: "another-secret-0123456789"}, {Key: "old", Secret: cfg.Secret}}
	p3, err := New(&ring)
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := p3.Passkey("alice"); k == alice {
		t.Fatal("the keyring's passkey is the old key's")
	}
	if _, err := p3.AllowPasskey("bittorrent/aa", alice); err != nil {
		t.Fatalf("the old key's passkey: %v", err)
	}

	// Public play lets anyone through; a nil policy too.
	pub := *cfg
	pub.Public = []string{"play"}
	p4, err := New(&pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p4.AllowPasskey("bittorrent/bb", bob); err != nil {
		t.Fatalf("public: %v", err)
	}
	var none *Policy
	if _, err := none.AllowPasskey("x", ""); err != nil {
		t.Fatalf("nil policy: %v", err)
	}
	removed := &Policy{name: "gone"}
	if _, err := removed.AllowPasskey("x", alice); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a policy not loaded: %v", err)
	}
	if _, err := removed.Passkey("alice"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a passkey of a policy not loaded: %v", err)
	}
}
