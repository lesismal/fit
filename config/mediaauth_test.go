package config

import (
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
