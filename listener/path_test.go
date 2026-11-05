package listener

import (
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/lesismal/fit/internal/certtest"
)

// pathCases are request targets and the body of the route that should serve
// them: "admin" for the /admin/* route, "public" for the /* one, "" for a
// path refused with 400.
var pathCases = []struct{ target, want string }{
	{"/admin/secret", "admin"},
	{"/./admin/secret", "admin"},
	{"/x/../admin/secret", "admin"},
	{"//admin/secret", "admin"},
	{"/x/%2e%2e/admin/secret", "admin"},
	{"/x/%2E%2E/admin/secret", "admin"},
	{"/x/.%2e/admin/secret", "admin"},
	{"/%2e/admin/secret", "admin"},
	{"/../admin/secret", "admin"},
	{"/admin/./secret", "admin"},
	{"/admin/x/../../public", "public"},
	{"/x/..%2Fadmin/secret", ""},
	{"/%2Fadmin/secret", ""},
	{"/.well-known/x", "public"},
	{"/a%2Fb", "public"},
	{"/", "public"},
}

// TestPathNormalized checks that a route cannot be stepped around with a path
// an upstream would resolve to its own, over every protocol: dot segments,
// encoded dots and repeated slashes are resolved before routing.
func TestPathNormalized(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_, s := start(t, fmt.Sprintf(`{"listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "h3"],
    "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}],
  "routes": [
    {"match": {"path": ["/admin/*"]}, "handler": {"module": "respond", "params": {"body": "admin"}}},
    {"handler": {"module": "respond", "params": {"body": "public"}}}
  ]}`, cert.CertFile, cert.KeyFile))
	a := s.Addrs()[0]
	port := strconv.Itoa(a.Addr.(*net.TCPAddr).Port)
	for _, tc := range pathCases {
		check := func(proto string, resp *stdhttp.Response, body string) {
			t.Helper()
			switch {
			case tc.want == "" && resp.StatusCode != 400:
				t.Errorf("%s %s: %d %q, want 400", proto, tc.target, resp.StatusCode, body)
			case tc.want != "" && (resp.StatusCode != 200 || body != tc.want):
				t.Errorf("%s %s: %d %q, want %q", proto, tc.target, resp.StatusCode, body, tc.want)
			}
		}
		for _, h2 := range []bool{false, true} {
			u, _ := url.Parse("https://a.example" + tc.target)
			resp, err := tlsClient(a.Addr.String(), h2, cert).Do(&stdhttp.Request{Method: "GET", URL: u, Header: stdhttp.Header{}})
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			check(resp.Proto, resp, string(body))
		}
		resp, body, err := h3Get(t, "https://127.0.0.1:"+port+tc.target, "a.example", cert)
		if err != nil {
			t.Fatal(err)
		}
		check("HTTP/3", resp, body)
	}
}

func TestAdmit(t *testing.T) {
	for _, tc := range []struct {
		proto      int
		target     string
		ok         bool
		path, raw  string
		requestURI string
	}{
		{1, "/a/b?q=1", true, "/a/b", "", "/a/b?q=1"},
		{1, "/x/../a/b?q=1", true, "/a/b", "", "/a/b?q=1"},
		{1, "/x/%2e%2e/c%20d?q", true, "/c d", "", "/c%20d?q"},
		{1, "/x/%2e%2e/a%2Fb/../c", false, "", "", ""},
		{1, "/x/%2e%2e/a%2Fb/c?q", true, "/a/b/c", "/a%2Fb/c", "/a%2Fb/c?q"},
		{1, "/x/..%2Fa", false, "", "", ""},
		{2, "/a b", false, "", "", ""},
		{2, "/a\x01b", false, "", "", ""},
		{3, "/a\x7fb", false, "", "", ""},
		{2, "/a/./b", true, "/a/b", "", "/a/b"},
	} {
		u, err := url.ParseRequestURI(tc.target)
		if err != nil {
			// What url refuses fib refuses too; the proto check comes first.
			u = &url.URL{Path: tc.target}
		}
		r := &stdhttp.Request{ProtoMajor: tc.proto, RequestURI: tc.target, URL: u, Header: stdhttp.Header{}}
		if ok := admit(r); ok != tc.ok {
			t.Errorf("admit(%q over HTTP/%d) = %v, want %v", tc.target, tc.proto, ok, tc.ok)
			continue
		}
		if tc.ok && (r.URL.Path != tc.path || r.URL.RawPath != tc.raw || r.RequestURI != tc.requestURI) {
			t.Errorf("admit(%q): path %q raw %q target %q, want %q %q %q", tc.target, r.URL.Path, r.URL.RawPath, r.RequestURI,
				tc.path, tc.raw, tc.requestURI)
		}
	}
	// An absolute-form target, as a forward proxy gets, keeps its form.
	u, _ := url.ParseRequestURI("http://example.com/a/../b")
	r := &stdhttp.Request{ProtoMajor: 1, RequestURI: "http://example.com/a/../b", URL: u, Header: stdhttp.Header{}}
	if !admit(r) || r.URL.Path != "/b" || r.RequestURI != "http://example.com/a/../b" {
		t.Errorf("absolute form: %q %q", r.URL.Path, r.RequestURI)
	}
}

func TestAdmitConnectionNamed(t *testing.T) {
	h := stdhttp.Header{"Connection": {"Upgrade, x-a,  , keep-alive", "TE, HTTP2-Settings"}, "Upgrade": {"websocket"},
		"X-A": {"1"}, "Http2-Settings": {"x"}, "Te": {"trailers"}, "Keep-Alive": {"5"}, "X-B": {"2"}}
	r := &stdhttp.Request{ProtoMajor: 1, RequestURI: "/", URL: &url.URL{Path: "/"}, Header: h}
	if !admit(r) {
		t.Fatal("refused")
	}
	for _, name := range []string{"Connection", "Upgrade", "Te", "Keep-Alive", "X-B"} {
		if _, ok := h[name]; !ok {
			t.Errorf("%s removed", name)
		}
	}
	for _, name := range []string{"X-A", "Http2-Settings"} {
		if _, ok := h[name]; ok {
			t.Errorf("%s kept", name)
		}
	}
	if n := testing.AllocsPerRun(100, func() {
		removeConnectionNamed(stdhttp.Header{}, []string{"keep-alive, Upgrade"})
	}); n > 1 {
		t.Errorf("removeConnectionNamed allocates %v times", n)
	}
}
