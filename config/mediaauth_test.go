package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMediaAuth(t *testing.T) {
	good := `{"listeners": [{"name": "w", "address": ":8080"}], "media_auth": [
		{"name": "live", "secret": "0123456789abcdef", "public": ["play"], "token_ttl": "1h",
		 "tokens": [{"name": "obs", "token": "0123456789abcdef-x", "publish": ["live/*"]},
		            {"token_sha256": "` + strings.Repeat("ab", 32) + `", "play": ["*"]}],
		 "users": [{"name": "alice", "password": "pw", "publish": ["alice-*"], "play": ["*"]},
		           {"name": "bob", "password_hash": "$2a$10$abcdefghijklmnopqrstuuJ5VrFz8Z1G0gq8x0bq4d8bGHT3fYz1m"}]},
		{"name": "open", "public": ["publish", "play"]}]}`
	c, err := Load([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	m := c.MediaAuthPolicy("live")
	if m == nil || m.TTL() != time.Hour || len(m.Tokens[1].HashBytes()) != 32 || c.MediaAuthPolicy("nope") != nil ||
		c.MediaAuthPolicy("open").TTL() != DefaultMediaTokenTTL || m.Tokens[0].HashBytes() != nil {
		t.Fatalf("%+v", m)
	}
	if !ValidMediaPattern("live/*") || ValidMediaPattern("") || ValidMediaPattern("a b") || ValidMediaPattern(strings.Repeat("a", 257)) {
		t.Fatal("ValidMediaPattern")
	}
	// A policy whose secret is a file lets someone in, and cannot also set
	// secret; the file itself is read when the policy is compiled, not here.
	c, err = Load([]byte(`{"listeners": [{"name": "w", "address": ":8080"}], "media_auth": [{"name": "f", "secret_file": "/tmp/fit-media-secret"}]}`))
	if err != nil || c.MediaAuthPolicy("f") == nil || c.MediaAuthPolicy("f").SecretFile != "/tmp/fit-media-secret" {
		t.Fatalf("secret_file: %v %+v", err, c.MediaAuthPolicy("f"))
	}
	doc := func(policy string) string {
		return `{"listeners": [{"name": "w", "address": ":8080"}], "media_auth": [` + policy + `]}`
	}
	for _, bad := range []struct{ doc, want string }{
		{doc(`{"name": "a"}`), "lets no one in"},
		{doc(`{"name": "-a", "public": ["play"]}`), "not a valid name"},
		{doc(`{"name": "a", "public": ["play"]}, {"name": "a", "public": ["play"]}`), "already named"},
		{doc(`{"name": "a", "public": ["watch"]}`), "watch"},
		{doc(`{"name": "a", "public": ["play", "play"]}`), "listed twice"},
		{doc(`{"name": "a", "secret": "short"}`), "at least 16"},
		{doc(`{"name": "a", "secret": "0123456789abcdef", "secret_file": "/tmp/fit-secret"}`), "set secret or secret_file"},
		{doc(`{"name": "a"}`), "lets no one in"},
		{doc(`{"name": "a", "tokens": [{"play": ["*"]}]}`), "set token or token_sha256"},
		{doc(`{"name": "a", "tokens": [{"token": "0123456789abcdef", "token_sha256": "00"}]}`), "set token or token_sha256"},
		{doc(`{"name": "a", "tokens": [{"token": "short"}]}`), "at least 16"},
		{doc(`{"name": "a", "tokens": [{"token_sha256": "abc"}]}`), "64 hex digits"},
		{doc(`{"name": "a", "tokens": [{"name": "x", "token": "0123456789abcdef"}, {"name": "x", "token": "0123456789abcdeg"}]}`), "another token is already named"},
		{doc(`{"name": "a", "tokens": [{"token": "0123456789abcdef", "publish": ["a b"]}]}`), "not a stream pattern"},
		{doc(`{"name": "a", "tokens": [{"token": "0123456789abcdef", "play": [""]}]}`), "not a stream pattern"},
		{doc(`{"name": "a", "users": [{"name": "a:b", "password": "p"}]}`), "not a user name"},
		{doc(`{"name": "a", "users": [{"name": "u", "password": "p"}, {"name": "u", "password": "q"}]}`), "already named"},
		{doc(`{"name": "a", "users": [{"name": "u"}]}`), "set password or password_hash"},
		{doc(`{"name": "a", "users": [{"name": "u", "password_hash": "plain"}]}`), "not a bcrypt hash"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
}

// TestMediaAuthKeys checks a media_auth policy's keyring: its validation,
// and that a policy that lets no one in by tokens or secret can by keys.
func TestMediaAuthKeys(t *testing.T) {
	doc := func(policy string) string {
		return `{"listeners": [{"name": "w", "address": ":8080"}], "media_auth": [` + policy + `]}`
	}
	good := doc(`{"name": "ring", "keys": [
		{"key": "k1", "secret": "0123456789abcdef"},
		{"key": "k2", "secret_file": "/tmp/fit-media-k2"}]}`)
	c, err := Load([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	m := c.MediaAuthPolicy("ring")
	if m == nil || len(m.Keys) != 2 || m.Keys[0].Key != "k1" || m.Keys[1].SecretFile != "/tmp/fit-media-k2" {
		t.Fatalf("keys: %+v", m)
	}
	var many strings.Builder
	for i := range MaxMediaKeys + 1 {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"key": "k%d", "secret": "0123456789abcdef"}`, i)
	}
	for _, bad := range []struct{ doc, want string }{
		{doc(`{"name": "a", "secret": "0123456789abcdef", "keys": [{"key": "k1", "secret": "0123456789abcdef"}]}`), "one of the two"},
		{doc(`{"name": "a", "keys": [{"key": "k1", "secret": "0123456789abcdef"}, {"key": "k1", "secret": "0123456789abcdeg"}]}`), "already named"},
		{doc(`{"name": "a", "keys": [{"key": "a b", "secret": "0123456789abcdef"}]}`), "not a key name"},
		{doc(`{"name": "a", "keys": [{"key": "k1"}]}`), "set secret or secret_file"},
		{doc(`{"name": "a", "keys": [{"key": "k1", "secret": "0123456789abcdef", "secret_file": "/tmp/x"}]}`), "set secret or secret_file"},
		{doc(`{"name": "a", "keys": [{"key": "k1", "secret": "short"}]}`), "at least 16"},
		{doc(`{"name": "a", "keys": [` + many.String() + `]}`), "at most 256 keys"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
}
