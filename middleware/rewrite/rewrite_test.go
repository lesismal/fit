package rewrite

import (
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

func load(t *testing.T, name, params string) (module.Middleware, error) {
	t.Helper()
	v, err := config.ParseValue([]byte(`{"module": "`+name+`", "params": `+params+`}`), "middleware")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		return nil, err
	}
	m, err := module.Load(module.Middlewares, ref, nil)
	if err != nil {
		return nil, err
	}
	return m.(module.Middleware), nil
}

// echo answers with the path and the escaped path it was given.
var echo = fibhttp.HandlerFunc(func(c *fibhttp.Context) {
	_ = c.Respond(200, "text/plain", []byte(c.Request.URL.Path+" "+c.Request.URL.EscapedPath()))
})

func TestRewrite(t *testing.T) {
	for _, tt := range []struct {
		name, params string
		cases        [][2]string // target, "path escaped-path"
	}{
		{
			name:   "strip prefix",
			params: `{"strip_prefix": ["/api/v1", "/api"]}`,
			cases: [][2]string{
				{"/api/users", "/users /users"},
				{"/api/v1/users", "/users /users"},
				{"/api", "/ /"},
				{"/api/", "/ /"},
				{"/apix", "/apix /apix"},
				{"/other/api", "/other/api /other/api"},
				{"/api/a%2Fb", "/a/b /a/b"},
			},
		},
		{
			name:   "strip a prefix ending in /",
			params: `{"strip_prefix": ["/static/"]}`,
			cases:  [][2]string{{"/static/css/a.css", "/css/a.css /css/a.css"}, {"/static", "/ /"}},
		},
		{
			name:   "regexp",
			params: `{"regexp": "^/users/([0-9]+)$", "replacement": "/u?id=$1"}`,
			cases:  [][2]string{{"/users/42", "/u?id=42 /u%3Fid=42"}, {"/users/x", "/users/x /users/x"}},
		},
		{
			name:   "regexp, named groups, every match",
			params: `{"regexp": "(?P<word>[a-z]+)-", "replacement": "${word}_"}`,
			cases:  [][2]string{{"/a-b-c", "/a_b_c /a_b_c"}},
		},
		{
			name:   "a replacement that drops the leading /",
			params: `{"regexp": "^/old", "replacement": ""}`,
			cases:  [][2]string{{"/old", "/ /"}, {"/oldx", "/x /x"}},
		},
		{
			name:   "add prefix",
			params: `{"add_prefix": "/v2"}`,
			cases:  [][2]string{{"/users", "/v2/users /v2/users"}, {"/", "/v2/ /v2/"}},
		},
		{
			name:   "all three, in order",
			params: `{"strip_prefix": ["/api"], "regexp": "^/v1/", "replacement": "/", "add_prefix": "/internal"}`,
			cases:  [][2]string{{"/api/v1/users", "/internal/users /internal/users"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, err := load(t, "rewrite", tt.params)
			if err != nil {
				t.Fatal(err)
			}
			h := m.Wrap(echo)
			for _, c := range tt.cases {
				if got := string(fibtest.Serve(h, "GET", c[0]).Response.Body); got != c[1] {
					t.Errorf("%s: got %q, want %q", c[0], got, c[1])
				}
			}
		})
	}
}

func TestRedirect(t *testing.T) {
	for _, tt := range []struct {
		name, params string
		https        bool
		target       string
		status       int
		location     string // "" means passed on to the handler
	}{
		{"every request", `{"to": "https://{host}{uri}"}`, false, "/a/b?x=1", 302, "https://example.com/a/b?x=1"},
		{"permanent", `{"to": "https://{host}{uri}", "status": 308}`, false, "/a", 308, "https://example.com/a"},
		{"placeholders", `{"to": "{scheme}://new.example{path}#{query}"}`, true, "/a%20b?q", 302, "https://new.example/a%20b#q"},
		{"a fixed URL", `{"to": "/elsewhere", "status": 301}`, false, "/x", 301, "/elsewhere"},
		{"regexp matched", `{"to": "/new/$1{query}", "regexp": "^/old/(.*)$"}`, false, "/old/page?x", 302, "/new/pagex"},
		{"regexp not matched", `{"to": "/new/$1", "regexp": "^/old/(.*)$"}`, false, "/other", 200, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, err := load(t, "redirect", tt.params)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", tt.target, nil)
			if tt.https {
				r.TLS = &tls.ConnectionState{}
			}
			res := fibtest.ServeRequest(m.Wrap(echo), r).Response
			if res.StatusCode != tt.status || res.Header.Get("Location") != tt.location {
				t.Errorf("got %d %q, want %d %q", res.StatusCode, res.Header.Get("Location"), tt.status, tt.location)
			}
		})
	}
}

func TestParamErrors(t *testing.T) {
	for _, tt := range []struct {
		name, params, want string
	}{
		{"rewrite", `{}`, "strip_prefix, regexp or add_prefix is needed"},
		{"rewrite", `{"strip_prefix": ["api"]}`, `"api" does not start with '/'`},
		{"rewrite", `{"add_prefix": "/v2/"}`, `"/v2/" does not start with '/', or ends with one`},
		{"rewrite", `{"regexp": "("}`, `"(" is not a valid regular expression`},
		{"rewrite", `{"replacement": "/x"}`, "a replacement needs a regexp"},
		{"redirect", `{}`, "to"},
		{"redirect", `{"to": "/x", "status": 200}`, "200 is not a redirect status"},
		{"redirect", `{"to": "/x\r\nX-Evil: 1"}`, "line break"},
		{"redirect", `{"to": "/x", "regexp": "["}`, "is not a valid regular expression"},
	} {
		_, err := load(t, tt.name, tt.params)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %s: %v; want %q", tt.name, tt.params, err, tt.want)
		}
	}
}

func TestRedirectHeaderIsItsOwn(t *testing.T) {
	m, err := load(t, "redirect", `{"to": "/x"}`)
	if err != nil {
		t.Fatal(err)
	}
	h := m.Wrap(echo)
	a := fibtest.Serve(h, "GET", "/").Response.Header
	a.Set("Location", "/changed")
	if got := fibtest.Serve(h, "GET", "/").Response.Header.Get("Location"); got != "/x" {
		t.Fatalf("a response's header is shared: %q", got)
	}
}
