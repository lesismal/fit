package proxy

import (
	stdhttp "net/http"
	"net/url"
	"testing"

	"golang.org/x/crypto/bcrypt"

	_ "github.com/lesismal/fit/middleware/auth"
)

// TestPathAuthBypass checks that a path an upstream would resolve to a
// guarded one is guarded too: /admin/* sits behind basic_auth beside a /*
// route to the same upstream, and what the upstream is sent is the path the
// route was picked by.
func TestPathAuthBypass(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	base := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [{"name": "app", "addresses": [%s]}],
  "routes": [
    {"match": {"path": ["/admin/*"]}, "middleware": [{"module": "basic_auth", "params": {"users": [{"username": "alice", "password_hash": "`+string(hash)+`"}]}}],
     "handler": {"module": "proxy", "params": {"upstream": "app"}}},
    {"handler": {"module": "proxy", "params": {"upstream": "app"}}}
  ]}`, echoBackend(t, "http/1.1"))
	for _, h2c := range []bool{false, true} {
		for _, target := range []string{"/admin/secret", "/./admin/secret", "/x/../admin/secret", "//admin/secret",
			"/x/%2e%2e/admin/secret", "/x/%2E%2E/admin/secret", "/admin/./secret"} {
			u, _ := url.Parse(base + target)
			resp, err := client(h2c).Do(&stdhttp.Request{Method: "GET", URL: u, Header: stdhttp.Header{}})
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Errorf("h2c=%v %s: %d, want 401", h2c, target, resp.StatusCode)
			}
		}
		for _, target := range []string{"/x/..%2Fadmin/secret", "/%2Fadmin/secret"} {
			u, _ := url.Parse(base + target)
			resp, err := client(h2c).Do(&stdhttp.Request{Method: "GET", URL: u, Header: stdhttp.Header{}})
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Errorf("h2c=%v %s: %d, want 400", h2c, target, resp.StatusCode)
			}
		}
		// What the upstream gets is the path routed, with other escapes as
		// the client wrote them.
		for target, want := range map[string]string{
			"/x/../public?q=%20":   "/public?q=%20",
			"/a//b/./c":            "/a/b/c",
			"/x/%2e%2e/a%2Fb/%7e/": "/a%2Fb/%7e/",
		} {
			u, _ := url.Parse(base + target)
			req := &stdhttp.Request{Method: "GET", URL: u, Header: stdhttp.Header{}}
			if _, e := send(t, client(h2c), req); e.RequestURI != want {
				t.Errorf("h2c=%v %s: upstream got %q, want %q", h2c, target, e.RequestURI, want)
			}
		}
		u, _ := url.Parse(base + "/admin/x/../secret")
		req := &stdhttp.Request{Method: "GET", URL: u, Header: stdhttp.Header{}}
		req.SetBasicAuth("alice", "pw")
		if _, e := send(t, client(h2c), req); e.RequestURI != "/admin/secret" {
			t.Errorf("h2c=%v: upstream got %q, want /admin/secret", h2c, e.RequestURI)
		}
	}
}
