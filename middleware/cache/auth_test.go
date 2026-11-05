package cache

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"
	"golang.org/x/crypto/bcrypt"

	_ "github.com/lesismal/fit/middleware/auth"
	"github.com/lesismal/fit/observe"
)

// TestAuthenticatedNotShared checks that a response to a request an auth
// middleware let in is kept for others only when it says it may be, as one
// to a request with Authorization is (RFC 9111 section 3.5), though
// basic_auth removed the Authorization before the cache saw it, or the
// handler authenticated the request after the cache sent it on.
func TestAuthenticatedNotShared(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	base := serve(t, `[{"module": "basic_auth", "params": {"users": [
    {"username": "alice", "password_hash": "`+string(hash)+`"},
    {"username": "bob", "password_hash": "`+string(hash)+`"}]}},
  {"module": "cache", "params": {"zone": "test"}}]`)
	c := clients()["http/1.1"]
	alice, bob := []string{"Authorization", "Basic YWxpY2U6cHc="}, []string{"Authorization", "Basic Ym9iOnB3"}
	for _, tt := range []struct {
		path string
		kept bool
	}{
		{"/fresh", false},
		{"/public", true},
		{"/must", true},
		{"/s-maxage", true},
	} {
		resetHits()
		first := get(t, c, "GET", base+tt.path, alice...)
		second := get(t, c, "GET", base+tt.path, bob...)
		if first.status != 200 || second.status != 200 {
			t.Fatalf("%s: %d then %d", tt.path, first.status, second.status)
		}
		if kept := hitsOf(tt.path) == 1; kept != tt.kept {
			t.Errorf("%s: kept %v (%q then %q)", tt.path, kept, first.cacheStatus(), second.cacheStatus())
		}
	}

	// A handler that authenticates the request itself, after the cache
	// sent it on, as media_auth's checks do.
	base = serve(t, cacheOnly)
	fn := func(c *fibhttp.Context) {
		observe.Authenticate(c).User = "alice"
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: stdhttp.Header{"Cache-Control": {"max-age=60"}}, Body: []byte("alice's")})
	}
	answer.Store(&fn)
	get(t, c, "GET", base+"/custom")
	if second := get(t, c, "GET", base+"/custom"); hitsOf("/custom") != 2 {
		t.Errorf("kept: %q", second.cacheStatus())
	}
}

// TestRefetchAuthorized checks that the cache's own fetch for a request
// that was authenticated keeps the response only as it would have for the
// request: its Authorization may be gone, removed by the auth middleware.
func TestRefetchAuthorized(t *testing.T) {
	m := provisioned(t, &Cache{Zone: "refetch-auth"})
	origin := answering(stdhttp.Header{"Cache-Control": {"max-age=60"}}, "per-user", nil)
	for _, authorized := range []bool{true, false} {
		const key = "http://example.com/r"
		m.store.remove(key)
		f, first := m.store.lock(key, clock(), time.Minute)
		if !first {
			t.Fatal("a fetch under way")
		}
		r := httptest.NewRequest("GET", key, nil)
		m.refetch(f, r, authorized, &entry{status: 200, header: stdhttp.Header{}}, origin, false)
		if e, _ := m.store.get(key, nil); (e != nil) == authorized {
			t.Errorf("authorized %v: kept %v", authorized, e != nil)
		}
	}
}
