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
		{[]string{"-policy", "pages", "-play", "*"}, 1, "has no secret"},
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
}
