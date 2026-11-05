package cache

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	_ "github.com/lesismal/fit/middleware/auth"
)

// TestStorableBehindAuth checks that a request an auth middleware let in
// counts as one with Authorization, though the middleware took the field
// out: its response is kept only when public, s-maxage or must-revalidate
// says a shared cache may keep it (RFC 9111 section 3.5), and one user's is
// never another's.
func TestStorableBehindAuth(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	base := serve(t, `[{"module": "basic_auth", "params": {"users": [{"username": "alice", "password_hash": "`+string(hash)+`"}]}},
  {"module": "cache", "params": {"zone": "test"}}]`)
	c := clients()["http/1.1"]
	auth := []string{"Authorization", "Basic YWxpY2U6cHc="} // alice:pw
	for _, tt := range []struct {
		path string
		kept bool
	}{
		{"/fresh", false},
		{"/heuristic", false},
		{"/public", true},
		{"/must", true},
		{"/s-maxage", true},
	} {
		resetHits()
		first := get(t, c, "GET", base+tt.path, auth...)
		second := get(t, c, "GET", base+tt.path, auth...)
		if first.status != 200 {
			t.Fatalf("%s: %d", tt.path, first.status)
		}
		path, _, _ := strings.Cut(tt.path, "?")
		if kept := hitsOf(path) == 1; kept != tt.kept {
			t.Errorf("%s: kept %v (%d hits; %q then %q)", tt.path, kept, hitsOf(path), first.cacheStatus(), second.cacheStatus())
		}
	}
}
