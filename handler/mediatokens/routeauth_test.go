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
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".test_authenticated", New: func() any { return new(authenticated) }})
}

// authenticated stands for a middleware that authenticates the route's
// callers, letting every request through, so that the tests of what the
// handler does need not authenticate each request.
type authenticated struct{}

func (*authenticated) Provision(*module.Context) error         { return nil }
func (*authenticated) Authenticates() bool                     { return true }
func (*authenticated) Wrap(next module.Handler) module.Handler { return next }

// A route of media_tokens without a middleware that authenticates its
// callers is refused when compiled: anyone could otherwise sign tokens for
// every stream, or rotate the policy's secret, which refuses every token
// signed before. Middleware that does not authenticate, or client_cert
// that lets a request without a certificate through, is not enough.
func TestRouteMustAuthenticate(t *testing.T) {
	file := newSecretFile(t)
	for _, tc := range []struct {
		what, middleware string
	}{
		{"no middleware", ``},
		{"rate_limit only", `"middleware": [{"module": "rate_limit", "params": {"rate": 10}}],`},
		{"an optional client_cert", `"middleware": [{"module": "client_cert", "params": {"optional": true}}],`},
	} {
		doc := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{` + tc.middleware + ` "handler": {"module": "media_tokens", "params": {"auth": "live"}}}],
  "media_auth": [{"name": "live", "secret_file": "` + file + `"}]}`
		c, err := config.Load([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		if g, err := runtime.Compile(c, runtime.Env{}); err == nil || !strings.Contains(err.Error(), "serves only callers its route authenticates") {
			if g != nil {
				g.Release()
			}
			t.Errorf("%s: %v", tc.what, err)
		}
	}
}

// With basic_auth on its route, a caller without the password neither
// issues nor revokes; one with it does.
func TestRouteBasicAuth(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("backend-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	file := newSecretFile(t)
	f := startDoc(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"match": {"path": ["/media-tokens", "/media-tokens/*"]},
    "middleware": [{"module": "basic_auth", "params": {"users": [{"username": "backend", "password_hash": "`+string(hash)+`"}]}}],
    "handler": {"module": "media_tokens", "params": {"auth": "live"}}}],
  "media_auth": [{"name": "live", "secret_file": "`+file+`", "token_ttl": "1h"}]}`)
	f.secretFile = file
	batch := `{"tokens": [{"subject": "x", "publish": ["*"], "play": ["*"]}]}`
	for _, path := range []string{"/media-tokens", "/media-tokens/revoke"} {
		if resp, _ := do(t, "POST", f.base+path, batch, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("POST %s without credentials: %d", path, resp.StatusCode)
		}
	}
	if b, err := os.ReadFile(file); err != nil || strings.TrimRight(string(b), "\r\n") != oldSecret {
		t.Fatalf("the secret was rotated: %q %v", b, err)
	}
	auth := map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("backend:backend-pw"))}
	if resp, body := do(t, "POST", f.base+"/media-tokens", batch, auth); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"token"`) {
		t.Fatalf("an authenticated batch: %d %s", resp.StatusCode, body)
	}
}
