package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
)

func TestMediaToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.json")
	doc := `{"listeners": [{"name": "w", "address": ":8080"}], "media_auth": [
		{"name": "live", "secret": "a-secret-of-sixteen-bytes"},
		{"name": "pages", "public": ["play"]}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runFit("media-token", "-c", path, "-policy", "live", "-publish", "live/*,cam1", "-play", "*", "-ttl", "1h", "-subject", "obs")
	if code != 0 || !strings.HasPrefix(stdout, mediaauth.TokenPrefix) || !strings.Contains(stderr, "good until") {
		t.Fatalf("%d %q %q", code, stdout, stderr)
	}
	// The running fit, with the same secret, takes it.
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mediaauth.New(c.MediaAuthPolicy("live"))
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSpace(stdout)
	for _, s := range []string{"live/a", "cam1"} {
		if sub, err := p.Allow(mediaauth.Publish, s, mediaauth.Credentials{Token: tok}); err != nil || sub != "obs" {
			t.Fatalf("publish %s: %q %v", s, sub, err)
		}
	}
	if _, err := p.Allow(mediaauth.Publish, "cam2", mediaauth.Credentials{Token: tok}); err != mediaauth.ErrForbidden {
		t.Fatalf("cam2: %v", err)
	}

	for _, tt := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"-play", "*"}, 2, "-policy is required"},
		{[]string{"-policy", "live"}, 2, "-publish, -play or both"},
		{[]string{"-policy", "live", "-play", "*", "extra"}, 2, "unexpected argument"},
		{[]string{"-policy", "live", "-play", "*", "-ttl", "-1h"}, 2, "negative"},
		{[]string{"-policy", "live", "-play", "*", "-bogus"}, 2, "bogus"},
		{[]string{"-policy", "nope", "-play", "*"}, 1, `no media_auth policy named "nope"`},
		{[]string{"-policy", "pages", "-play", "*"}, 1, "has neither secret, secret_file nor keys"},
		{[]string{"-policy", "live", "-play", "a b"}, 1, "not a stream pattern"},
		{[]string{"-policy", "live", "-play", "*", "-subject", ""}, 1, "not a subject"},
	} {
		code, _, stderr := runFit(append([]string{"media-token", "-c", path}, tt.args...)...)
		if code != tt.code || !strings.Contains(stderr, tt.err) {
			t.Errorf("%v: %d %q, want %d %q", tt.args, code, stderr, tt.code, tt.err)
		}
	}
	if code, _, stderr := runFit("media-token", "-c", filepath.Join(dir, "missing.json"), "-policy", "live", "-play", "*"); code != 1 || stderr == "" {
		t.Errorf("a missing config: %d %q", code, stderr)
	}

	// A policy whose secret is a file signs with it, as a running fit that
	// reads the same file checks.
	secretFile := filepath.Join(dir, "media-secret")
	if err := os.WriteFile(secretFile, []byte("a-secret-file-of-16+\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileDoc := `{"listeners": [{"name": "w", "address": ":8080"}], "media_auth": [
		{"name": "file", "secret_file": "` + secretFile + `"}]}`
	filePath := filepath.Join(dir, "file.json")
	if err := os.WriteFile(filePath, []byte(fileDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runFit("media-token", "-c", filePath, "-policy", "file", "-play", "live/*", "-subject", "obs")
	if code != 0 || !strings.HasPrefix(stdout, mediaauth.TokenPrefix) {
		t.Fatalf("secret_file: %d %q %q", code, stdout, stderr)
	}
	c, err = config.Load([]byte(fileDoc))
	if err != nil {
		t.Fatal(err)
	}
	p, err = mediaauth.New(c.MediaAuthPolicy("file"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Allow(mediaauth.Play, "live/a", mediaauth.Credentials{Token: strings.TrimSpace(stdout)}); err != nil {
		t.Fatalf("the token a secret_file policy signed: %v", err)
	}
}

// TestMediaTokenKeyring signs with a named key of a media_auth keyring, and
// refuses a key where the policy has none or has only one secret.
func TestMediaTokenKeyring(t *testing.T) {
	dir := t.TempDir()
	keys := map[string]string{}
	for _, key := range []string{"k1", "k2"} {
		path := filepath.Join(dir, key)
		if err := os.WriteFile(path, []byte("a-secret-of-"+key+"-16+"), 0o600); err != nil {
			t.Fatal(err)
		}
		keys[key] = path
	}
	doc := `{"listeners": [{"name": "w", "address": ":8080"}], "media_auth": [
		{"name": "live", "keys": [
			{"key": "k1", "secret_file": "` + keys["k1"] + `"},
			{"key": "k2", "secret_file": "` + keys["k2"] + `"}]}]}`
	path := filepath.Join(dir, "fit.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mediaauth.New(c.MediaAuthPolicy("live"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"k1", "k2"} {
		code, stdout, stderr := runFit("media-token", "-c", path, "-policy", "live", "-play", "*", "-key", key)
		if code != 0 || !strings.HasPrefix(stdout, mediaauth.TokenPrefix) {
			t.Fatalf("-key %s: %d %q %q", key, code, stdout, stderr)
		}
		if _, err := p.Allow(mediaauth.Play, "mtg-1", mediaauth.Credentials{Token: strings.TrimSpace(stdout)}); err != nil {
			t.Fatalf("-key %s: the token: %v", key, err)
		}
	}
	if code, _, stderr := runFit("media-token", "-c", path, "-policy", "live", "-play", "*", "-key", "nope"); code != 1 || !strings.Contains(stderr, "no key named") {
		t.Errorf("an unknown key: %d %q", code, stderr)
	}
	single := filepath.Join(dir, "single.json")
	if err := os.WriteFile(single, []byte(`{"listeners": [{"name": "w", "address": ":8080"}], "media_auth": [
		{"name": "live", "secret": "0123456789abcdef-secret"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runFit("media-token", "-c", single, "-policy", "live", "-play", "*", "-key", "k1"); code != 1 || !strings.Contains(stderr, "has no keyring") {
		t.Errorf("a key on a single-key policy: %d %q", code, stderr)
	}
}
