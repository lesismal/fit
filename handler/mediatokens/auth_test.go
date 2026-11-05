package mediatokens

import (
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/middleware/auth"
	_ "github.com/lesismal/fit/middleware/limit"
	"github.com/lesismal/fit/runtime"
)

// authConf is a config with media_tokens behind middleware, its params
// params, signed by secretFile.
func authConf(secretFile, middleware, params string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"match": {"path": ["/media-tokens", "/media-tokens/*"]},
    "middleware": [` + middleware + `],
    "handler": {"module": "media_tokens", "params": ` + params + `}}],
  "media_auth": [{"name": "live", "secret_file": "` + secretFile + `", "token_ttl": "1h"}]
}`
}

// The handler has no auth of its own: a route without an auth middleware,
// as one with rate_limit alone, is a config error unless allow_no_auth says
// it is meant, so that no one who reaches it unauthenticated issues tokens
// or revokes them all; behind basic_auth both answer 401 without
// credentials, and the secret is not rotated.
func TestNeedsAuthMiddleware(t *testing.T) {
	file := newSecretFile(t)
	for _, mw := range []string{``, `{"module": "rate_limit", "params": {"rate": 10, "burst": 10}}`} {
		c, err := config.Load([]byte(authConf(file, mw, `{"auth": "live"}`)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.Compile(c, runtime.Env{}); err == nil || !strings.Contains(err.Error(), "needs an auth middleware") {
			t.Fatalf("a route with middleware [%s]: %v", mw, err)
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte("backend-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	f := startDoc(t, authConf(file, `{"module": "basic_auth", "params": {"users": [{"username": "backend", "password_hash": "`+string(hash)+`"}]}}`,
		`{"auth": "live"}`))
	json := map[string]string{"Content-Type": "application/json"}
	if resp, body := do(t, "POST", f.base+"/media-tokens", `{"tokens": [{"subject": "a", "publish": ["*"], "play": ["*"]}]}`, json); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a batch without credentials: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, "POST", f.base+"/media-tokens/revoke", `{"all": true}`, json); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoke without credentials: %d %s", resp.StatusCode, body)
	}
	if b, _ := os.ReadFile(file); strings.TrimSpace(string(b)) != oldSecret {
		t.Fatalf("the secret was rotated: %q", b)
	}
	creds := map[string]string{"Content-Type": "application/json", "Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("backend:backend-pw"))}
	if resp, body := do(t, "POST", f.base+"/media-tokens", `{"tokens": [{"subject": "a", "publish": ["mtg-*"]}]}`, creds); resp.StatusCode != http.StatusOK {
		t.Fatalf("a batch with credentials: %d %s", resp.StatusCode, body)
	}
}
