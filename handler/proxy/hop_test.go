package proxy

import (
	stdhttp "net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestConnectionNamesMiddlewareFields checks that a client's Connection field
// removes only the fields the client sent: the ones middleware sets reach the
// upstream, as basic_auth's user field and a headers middleware's.
func TestConnectionNamesMiddlewareFields(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	base := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [{"name": "app", "addresses": [%s]}],
  "routes": [{"middleware": [
      {"module": "basic_auth", "params": {"user_header": "X-User", "users": [{"username": "alice", "password_hash": "`+string(hash)+`"}]}},
      {"module": "headers", "params": {"request": {"set": {"X-Tenant": "acme"}}}}],
    "handler": {"module": "proxy", "params": {"upstream": "app"}}}]}`, echoBackend(t, "http/1.1"))
	for _, connection := range []string{"X-User", "X-Tenant, X-User, X-Hop", "keep-alive, x-user,x-tenant"} {
		req, _ := stdhttp.NewRequest("GET", base+"/", nil)
		req.SetBasicAuth("alice", "pw")
		req.Header.Set("Connection", connection)
		req.Header.Set("X-User", "mallory")
		req.Header.Set("X-Hop", "1")
		_, e := send(t, client(false), req)
		if got := e.Header["X-User"]; len(got) != 1 || got[0] != "alice" {
			t.Errorf("Connection: %s: X-User %q, want alice", connection, got)
		}
		if got := e.Header.Get("X-Tenant"); got != "acme" {
			t.Errorf("Connection: %s: X-Tenant %q, want acme", connection, got)
		}
		// What the client named of its own is still dropped.
		if _, ok := e.Header["X-Hop"]; ok == strings.Contains(connection, "X-Hop") {
			t.Errorf("Connection: %s: X-Hop reached the upstream: %v", connection, ok)
		}
	}
}
