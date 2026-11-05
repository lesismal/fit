package listener

import (
	"bufio"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/fcgi"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/fastcgi"
	_ "github.com/lesismal/fit/handler/proxy"
	_ "github.com/lesismal/fit/middleware/auth"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

func TestCanonicalize(t *testing.T) {
	for _, tt := range []struct {
		target string
		// path and raw are URL.Path and URL.RawPath after, and uri
		// RequestURI; refused when path is "".
		path, raw, uri string
	}{
		// Left as they are.
		{"/", "/", "", "/"},
		{"/a/b", "/a/b", "", "/a/b"},
		{"/a/b/", "/a/b/", "", "/a/b/"},
		{"/a/b?x=/../y", "/a/b", "", "/a/b?x=/../y"},
		{"/.well-known/acme-challenge/t", "/.well-known/acme-challenge/t", "", "/.well-known/acme-challenge/t"},
		{"/a/.b/..c/...", "/a/.b/..c/...", "", "/a/.b/..c/..."},
		{"/a/%2e%2ex", "/a/..x", "/a/%2e%2ex", "/a/%2e%2ex"},
		{"/group%2Fproject/x", "/group/project/x", "/group%2Fproject/x", "/group%2Fproject/x"},
		{"/a%5Cb", `/a\b`, "", "/a%5Cb"},
		{"/a;b/..;/c", "/a;b/..;/c", "", "/a;b/..;/c"},
		{"/ADMIN/x", "/ADMIN/x", "", "/ADMIN/x"},
		// Dot-segments and empty segments removed.
		{"/x/../admin/secret", "/admin/secret", "", "/admin/secret"},
		{"/x/%2e%2e/admin/secret", "/admin/secret", "", "/admin/secret"},
		{"/x/%2E./admin/secret?q=1", "/admin/secret", "", "/admin/secret?q=1"},
		{"/./admin", "/admin", "", "/admin"},
		{"//admin/secret", "/admin/secret", "", "/admin/secret"},
		{"/admin//secret///", "/admin/secret/", "", "/admin/secret/"},
		{"/a/b/..", "/a/", "", "/a/"},
		{"/a/b/.", "/a/b/", "", "/a/b/"},
		{"/a/..", "/", "", "/"},
		{"/a/b/../../c", "/c", "", "/c"},
		{"/a/../b%20c/%2F/../d", "/b c/d", "/b%20c/d", "/b%20c/d"},
		{"/x/../a%2Fb", "/a/b", "/a%2Fb", "/a%2Fb"},
		{"/x/..?", "/", "", "/?"},
		// Refused.
		{"/..", "", "", ""},
		{"/../admin", "", "", ""},
		{"/a/../../admin", "", "", ""},
		{"/%2e%2e/admin", "", "", ""},
		{"/x%2F..%2Fadmin", "", "", ""},
		{"/x%2F%2Fadmin", "", "", ""},
		{"/%2Fadmin", "", "", ""},
		{"/x%5C..%5Cadmin", "", "", ""},
		{`/x\..\admin`, "", "", ""},
		{`/\evil.com`, "", "", ""},
	} {
		u, err := url.ParseRequestURI(tt.target)
		if err != nil {
			t.Fatalf("%q: %v", tt.target, err)
		}
		r := &stdhttp.Request{URL: u, RequestURI: tt.target}
		ok := canonicalize(r)
		if tt.path == "" {
			if ok {
				t.Errorf("%q: canonicalized to %q, want refused", tt.target, u.Path)
			}
			continue
		}
		if !ok || u.Path != tt.path || u.RawPath != tt.raw || r.RequestURI != tt.uri {
			t.Errorf("%q: ok %v, path %q raw %q uri %q; want path %q raw %q uri %q",
				tt.target, ok, u.Path, u.RawPath, r.RequestURI, tt.path, tt.raw, tt.uri)
		}
	}

	// The absolute form keeps its scheme and host.
	u, _ := url.ParseRequestURI("http://example.com/x/../y")
	r := &stdhttp.Request{URL: u, RequestURI: "http://example.com/x/../y"}
	if !canonicalize(r) || r.RequestURI != "http://example.com/y" {
		t.Errorf("absolute form: %q", r.RequestURI)
	}
	// Paths not to canonicalize.
	for _, u := range []*url.URL{{Path: "*"}, {Host: "example.com:443"}, {Path: ""}} {
		if !canonicalize(&stdhttp.Request{URL: u}) {
			t.Errorf("%+v refused", u)
		}
	}
}

// TestCanonicalizeAllocs checks that a path with nothing to remove costs no
// allocation: what every request pays.
func TestCanonicalizeAllocs(t *testing.T) {
	r := &stdhttp.Request{URL: &url.URL{Path: "/api/v1/users/12345/profile.json", RawQuery: "a=1"},
		RequestURI: "/api/v1/users/12345/profile.json?a=1"}
	if n := testing.AllocsPerRun(100, func() {
		if !canonicalize(r) {
			t.Fatal("refused")
		}
	}); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}

// TestPathConfusionRoutes sends requests whose paths are spelled to miss a
// route behind basic_auth and reach the catch-all route, which proxies to
// an upstream or runs a FastCGI script, and checks that each gets the
// protected route, or 400, and that what passes on is the canonical path.
func TestPathConfusionRoutes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	be := &stdhttp.Server{Handler: stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_, _ = fmt.Fprintf(w, "uri=%s", r.RequestURI)
	})}
	go func() { _ = be.Serve(ln) }()
	t.Cleanup(func() { _ = be.Close() })

	fl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = fcgi.Serve(fl, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			env := fcgi.ProcessEnv(r)
			_, _ = fmt.Fprintf(w, "script=%s uri=%s", env["SCRIPT_FILENAME"], r.URL.RequestURI())
		}))
	}()
	t.Cleanup(func() { _ = fl.Close() })

	// The hash is of no password anyone sends: every request to /admin/
	// gets 401.
	auth := `[{"module": "basic_auth", "params": {"users": [{"username": "a",
	  "password_hash": "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"}]}}]`
	doc := `{
 "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1"]}],
 "upstreams": [{"name": "app", "addresses": ["` + ln.Addr().String() + `"]}],
 "routes": [
  {"match": {"path": ["/admin/*"]}, "middleware": ` + auth + `, "handler": {"module": "proxy", "params": {"upstream": "app"}}},
  {"match": {"path": ["/php/admin/*"]}, "middleware": ` + auth + `,
   "handler": {"module": "fastcgi", "params": {"address": "` + fl.Addr().String() + `", "root": "/srv"}}},
  {"match": {"path": ["/php/*"]}, "handler": {"module": "fastcgi", "params": {"address": "` + fl.Addr().String() + `", "root": "/srv"}}},
  {"handler": {"module": "proxy", "params": {"upstream": "app"}}}
 ]}`
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); rt.Close(); stop() })
	addr := s.Addrs()[0].Addr.String()

	send := func(target string) (int, string) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = io.WriteString(conn, "GET "+target+" HTTP/1.1\r\nHost: a\r\nConnection: close\r\n\r\n")
		resp, err := stdhttp.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, tt := range []struct {
		target string
		status int
		body   string
	}{
		{"/admin/secret", 401, ""},
		{"/x/../admin/secret", 401, ""},
		{"/x/%2e%2e/admin/secret", 401, ""},
		{"/x/.%2E/admin/secret", 401, ""},
		{"//admin/secret", 401, ""},
		{"/./admin/secret", 401, ""},
		{"/x%2F..%2Fadmin/secret", 400, ""},
		{"/%2Fadmin/secret", 400, ""},
		{"/x%5C..%5Cadmin/secret", 400, ""},
		{"/../admin/secret", 400, ""},
		{"/php/admin/panel.php", 401, ""},
		{"/php/x/../admin/panel.php", 401, ""},
		{"/php/x/%2e%2e/admin/panel.php", 401, ""},
		// What passes on is canonical.
		{"/a/./b/../c//d?q=/../x", 200, "uri=/a/c/d?q=/../x"},
		{"/a/x/%2e%2e/b%2Fc", 200, "uri=/a/b%2Fc"},
		{"/php/a/../index.php", 200, "script=/srv/php/index.php uri=/php/index.php"},
	} {
		status, body := send(tt.target)
		if status != tt.status || tt.body != "" && body != tt.body {
			t.Errorf("%s: %d %q, want %d %q", tt.target, status, body, tt.status, tt.body)
		}
		if status == 200 && strings.Contains(body, "admin") {
			t.Errorf("%s: reached %q", tt.target, body)
		}
	}
}
