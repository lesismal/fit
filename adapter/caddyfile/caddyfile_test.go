package caddyfile

import (
	"encoding/json"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	_ "github.com/lesismal/fit/modules/standard"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// convert converts src, and fails the test unless fit loads the result.
func convert(t *testing.T, src string) (outConfig, []string) {
	t.Helper()
	r, err := Convert([]byte(src))
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	if _, err := config.Load(r.JSON); err != nil {
		t.Fatalf("fit does not load it: %v\n%s", r.MapErrors(err), r.JSON)
	}
	var out outConfig
	if err := json.Unmarshal(r.JSON, &out); err != nil {
		t.Fatal(err)
	}
	return out, r.Warnings
}

func warnedLines(ws []string) []int {
	var lines []int
	for _, w := range ws {
		lines = append(lines, warningLine(w))
	}
	return lines
}

func modules(r *outRoute) []string {
	var mods []string
	for _, m := range r.Middleware {
		mods = append(mods, m.Module)
	}
	return mods
}

// TestSamples converts the sample Caddyfiles: each converts to a config fit
// loads, and each directive left out is reported at its line.
func TestSamples(t *testing.T) {
	for _, tt := range []struct {
		file   string
		routes int
		warned []int
	}{
		{"site.caddyfile", 2, nil},
		{"full.caddyfile", 8, []int{7, 27, 35, 44}},
	} {
		data, err := os.ReadFile(filepath.Join("testdata", tt.file))
		if err != nil {
			t.Fatal(err)
		}
		out, ws := convert(t, string(data))
		if len(out.Routes) != tt.routes {
			t.Errorf("%s: %d routes, want %d", tt.file, len(out.Routes), tt.routes)
		}
		if got := warnedLines(ws); !slices.Equal(got, tt.warned) {
			t.Errorf("%s: warnings at %v, want %v:\n%s", tt.file, got, tt.warned, strings.Join(ws, "\n"))
		}
	}
}

// TestSiteSample checks the migration guide's site: HTTPS by default, so no
// listeners, and handle_path's prefix stripped before the proxy.
func TestSiteSample(t *testing.T) {
	data, _ := os.ReadFile("testdata/site.caddyfile")
	out, _ := convert(t, string(data))
	if len(out.Listeners) != 0 || out.Admin == nil {
		t.Errorf("listeners %+v, admin %+v", out.Listeners, out.Admin)
	}
	api, files := out.Routes[0], out.Routes[1]
	if !slices.Equal(modules(api), []string{"compress", "rewrite"}) || api.Handler.Module != "proxy" || api.Match.Path[0] != "/api/*" {
		t.Errorf("/api/*: %+v", api)
	}
	if files.Handler.Params["root"] != "/srv/www" || !slices.Equal(files.Match.Host, []string{"example.com"}) || files.Match.Path != nil {
		t.Errorf("files: %+v", files)
	}
	u := out.Upstreams[0]
	if u.LoadBalancing != "least_conn" || u.HealthCheck.Path != "/healthz" || len(u.Addresses) != 2 {
		t.Errorf("upstream: %+v", u)
	}
}

func TestFullSample(t *testing.T) {
	data, _ := os.ReadFile("testdata/full.caddyfile")
	out, _ := convert(t, string(data))
	https := out.Listeners[0]
	if https.Address != ":443" || !slices.Equal(https.Protocols, []string{"http/1.1", "h2", "h3"}) || len(https.TLS.Certificates) != 1 {
		t.Errorf("443: %+v", https)
	}
	if l := out.Listeners[1]; l.Address != ":8080" || l.TLS != nil {
		t.Errorf("8080: %+v", l)
	}
	api := out.Routes[0]
	if !slices.Equal(api.Match.Method, []string{"GET", "POST"}) || api.Handler.Params["host_header"] != "backend.internal:8443" ||
		!slices.Equal(modules(api), []string{"headers", "compress", "headers"}) {
		t.Errorf("api: %v %+v", modules(api), api)
	}
	if out.Upstreams[0].Protocol != "h2" || out.Upstreams[0].ConnectTimeout != "5s" {
		t.Errorf("%+v", out.Upstreams[0])
	}
	old := out.Routes[1]
	if old.Match.Path[0] != "/old" || old.Middleware[1].Params["to"] != "/new" {
		t.Errorf("/old: %+v", old)
	}
	admin := out.Routes[2]
	if admin.Match.Path[0] != "/admin/*" || !slices.Contains(modules(admin), "basic_auth") {
		t.Errorf("/admin/*: %+v", admin)
	}
	redirect := out.Routes[len(out.Routes)-1]
	if redirect.Listeners[0] != "http_80" || !slices.Equal(redirect.Match.Host, []string{"example.com", "www.example.com"}) {
		t.Errorf("the redirect: %+v", redirect)
	}
	if out.AccessLog.Output != "/var/log/fit/access.log" || out.ACME.Email != "ops@example.com" || out.Admin.Address != "localhost:2019" {
		t.Errorf("%+v %+v %+v", out.AccessLog, out.ACME, out.Admin)
	}
}

func TestLex(t *testing.T) {
	t.Setenv("FIT_TEST_PORT", "9000")
	toks, err := lex("# a comment line\na \"quoted # not a comment \\\"x\\\"\" `raw \"q\"` b#c # trailing comment\n" +
		"d {$FIT_TEST_PORT} {$FIT_TEST_MISSING:dflt} \\\n  continued\n" +
		"e <<END\n    one\n      two\n    END\nf\n")
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, tk := range toks {
		texts = append(texts, tk.text)
	}
	want := []string{"a", `quoted # not a comment "x"`, `raw "q"`, "b#c", "d", "9000", "dflt", "continued", "e", "one\n  two", "f"}
	if !slices.Equal(texts, want) {
		t.Fatalf("%q\nwant %q", texts, want)
	}
	if toks[0].line != 2 || !toks[0].nl || toks[1].nl || toks[7].nl || toks[7].line != 4 || toks[10].line != 9 || !toks[10].nl {
		t.Errorf("lines: %+v", toks)
	}
	for src, msg := range map[string]string{
		"a \"open":     "does not end",
		"a `open":      "does not end",
		"a <<X\nb\n":   "heredoc",
		"a {\n b\n":    "does not end",
		"a {\n}\n}\n":  "unexpected",
		"a {\n}\nb\n":  "no braces",
		"{\n}\n{\n}\n": "global options come first",
	} {
		if _, _, err := parse([]byte(src)); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v, want %q", src, err, msg)
		}
	}
}

func TestParse(t *testing.T) {
	blocks, ws, err := parse([]byte(`{
	email a@b.test
}
(snip) {
	header X-Snip {args[0]}
	respond {args[:]}
}
a.test,
  b.test c.test {
	import snip one two
	import missing.conf
	route /x {
		import snip three
	}
}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || len(blocks[0].keys) != 0 || !slices.Equal(blocks[1].keys, []string{"a.test", "b.test", "c.test"}) {
		t.Fatalf("%+v", blocks)
	}
	d := blocks[1].dirs
	if len(d) != 3 || d[0].name != "header" || d[0].args[1] != "one" || d[1].name != "respond" || !slices.Equal(d[1].args, []string{"one two"}) {
		t.Errorf("%+v %+v", d[0], d[1])
	}
	if r := d[2]; r.name != "route" || len(r.block) != 2 || r.block[0].args[1] != "three" {
		t.Errorf("%+v", r)
	}
	if len(ws) != 1 || !strings.Contains(ws[0], "line 11: import missing.conf: skipped") {
		t.Errorf("%v", ws)
	}
	// One site may leave out its braces.
	blocks, _, err = parse([]byte("# comment\nlocalhost:8080\nrespond \"hi\"\n"))
	if err != nil || len(blocks) != 1 || blocks[0].keys[0] != "localhost:8080" || blocks[0].dirs[0].args[0] != "hi" {
		t.Errorf("%v %+v", err, blocks)
	}
}

// TestDirectives converts single sites, each showing a directive's cases.
func TestDirectives(t *testing.T) {
	for _, tt := range []struct {
		name, src string
		check     func(t *testing.T, out outConfig, ws []string)
	}{
		{"addresses", "http://a.test, :8081 {\n respond 204\n}\nlocalhost {\n respond 200\n}\n", func(t *testing.T, out outConfig, ws []string) {
			if len(out.Listeners) != 2 || out.Listeners[0].Address != ":80" || out.Listeners[1].Address != ":8081" {
				t.Errorf("%+v", out.Listeners)
			}
			if len(out.Routes) != 3 || out.Routes[0].Match.Host[0] != "a.test" || out.Routes[1].Match != nil || out.Routes[2].Match.Host[0] != "localhost" {
				t.Errorf("%+v %+v", out.Routes[0], out.Routes[1])
			}
			if len(ws) != 1 || !strings.Contains(ws[0], "internal CA") {
				t.Errorf("%v", ws)
			}
		}},
		{"matchers", `:80 {
	@post {
		method post
		header X-Token *
		query debug=1
		remote_ip private_ranges
	}
	@php path *.php /index
	@notapi not path /api/*
	@cel {path}.startsWith("/x")
	respond @post 201
	respond @php 202
	respond @notapi 203
	respond @cel 204
	respond @undefined 205
}`, func(t *testing.T, out outConfig, ws []string) {
			post, php, notapi := out.Routes[0].Match, out.Routes[1].Match, out.Routes[2].Match
			if post.Method[0] != "POST" || post.Header["X-Token"] == nil || len(post.Header["X-Token"]) != 0 || post.Query["debug"][0] != "1" || len(post.RemoteIP) != 6 {
				t.Errorf("%+v", post)
			}
			if !slices.Equal(php.PathRegexp, []string{`^.*\.php$`, `^/index$`}) {
				t.Errorf("%+v", php)
			}
			if notapi.Expr != `!(PathPrefix("/api/"))` {
				t.Errorf("%+v", notapi)
			}
			if len(out.Routes) != 4 || len(ws) != 2 || !strings.Contains(ws[0], "CEL") || !strings.Contains(ws[1], "no matcher @undefined") {
				t.Errorf("%d routes: %v", len(out.Routes), ws)
			}
		}},
		{"splits", `:80 {
	reverse_proxy /api/* app:8080
	header /a* X-A 1
	basicauth @post {
		bob JDJhJDE0JFpreDE5WExpVzZWWW91TEhSNU5tZk9GVTB6MkdUTm1wa1QvNXFxUjdoeDRJaldKUERoanZH
	}
	@post method POST
	root * /srv
	root /docs/* /srv/docs
	file_server
}`, func(t *testing.T, out outConfig, ws []string) {
			got := map[string]string{}
			for _, r := range out.Routes {
				m := r.Match
				if m == nil {
					m = &outMatch{}
				}
				key := fmt.Sprint(m.Path, m.Method)
				got[key] = r.Handler.Module + fmt.Sprint(modules(r)) + fmt.Sprint(r.Handler.Params["root"])
			}
			want := map[string]string{
				"[/api/*] [POST]":  "proxy[headers basic_auth]<nil>",
				"[/api/*] []":      "proxy[headers]<nil>",
				"[/a*] [POST]":     "static[headers basic_auth]/srv",
				"[/a*] []":         "static[headers]/srv",
				"[/docs/*] [POST]": "static[basic_auth]/srv/docs",
				"[/docs/*] []":     "static[]/srv/docs",
				"[] [POST]":        "static[basic_auth]/srv",
				"[] []":            "static[]/srv",
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s: %q, want %q", k, got[k], v)
				}
			}
			if len(got) != len(want) || len(ws) != 0 {
				t.Errorf("%v\n%v", got, ws)
			}
			if out.Routes[0].Middleware != nil && out.Routes[0].Handler.Module == "static" {
				t.Errorf("%+v", out.Routes[0])
			}
		}},
		{"handle", `:80 {
	encode
	handle /api/* {
		uri strip_prefix /api
		reverse_proxy h2c://grpc:50051 localhost:9001-9002
	}
	handle {
		redir https://x.test{uri} permanent
	}
}`, func(t *testing.T, out outConfig, ws []string) {
			api, rest := out.Routes[0], out.Routes[1]
			if !slices.Equal(modules(api), []string{"compress", "rewrite"}) || out.Upstreams[0].Protocol != "h2c" || len(out.Upstreams[0].Addresses) != 1 {
				t.Errorf("%+v %+v", api, out.Upstreams)
			}
			if rest.Middleware[1].Params["to"] != "https://x.test{uri}" || rest.Middleware[1].Params["status"] != 301.0 {
				t.Errorf("%+v", rest)
			}
			if len(ws) != 1 || !strings.Contains(ws[0], "localhost:9001-9002: skipped: fit reaches an upstream's addresses alike") {
				t.Errorf("%v", ws)
			}
		}},
		{"route keeps order", ":80 {\n route {\n  respond 200\n  header X 1\n  rewrite /new{path}\n }\n}\n", func(t *testing.T, out outConfig, ws []string) {
			if r := out.Routes[0]; !slices.Equal(modules(r), []string{"headers", "rewrite"}) || r.Middleware[1].Params["add_prefix"] != "/new" {
				t.Errorf("%+v", r)
			}
		}},
		{"php", ":80 {\n root * /var/www\n php_fastcgi unix//run/php.sock\n file_server\n}\n:81 {\n php_fastcgi :9000 {\n  root /app\n  env APP_ENV prod\n }\n}\n",
			func(t *testing.T, out outConfig, ws []string) {
				php := out.Routes[0]
				if php.Handler.Params["address"] != "unix//run/php.sock" || php.Handler.Params["root"] != "/var/www" || len(php.Match.PathRegexp) != 2 {
					t.Errorf("%+v", php)
				}
				only := out.Routes[2]
				if only.Handler.Params["address"] != "localhost:9000" || only.Handler.Params["root"] != "/app" || len(only.Handler.Params["try_files"].([]any)) != 3 {
					t.Errorf("%+v", only)
				}
				if len(ws) != 1 || !strings.Contains(ws[0], "index.php") {
					t.Errorf("%v", ws)
				}
			}},
		{"proxy options", `:80 {
	reverse_proxy a:1 b:1 {
		lb_policy weighted_round_robin 3 1
		lb_retries 3
		fail_duration 30s
		max_fails 2
		health_uri /h
		health_interval 1d
		header_up X-Env prod
		header_up -Cookie
		flush_interval -1
		transport http {
			keepalive 90s
			keepalive_idle_conns 8
			tls_insecure_skip_verify
		}
	}
	forward_auth auth:9091 {
		uri /verify
		copy_headers Remote-User Remote-Groups>Groups
	}
}`, func(t *testing.T, out outConfig, ws []string) {
			u := out.Upstreams[1]
			if u.Name != "proxy_a_1" || !slices.Equal(u.Weights, []int{3, 1}) || u.LoadBalancing != "weighted_round_robin" || u.FailTimeout != "30s" || *u.MaxFails != 2 ||
				u.HealthCheck.Interval != "24h" || u.IdleTimeout != "90s" || *u.MaxIdleConns != 8 {
				t.Errorf("%+v", u)
			}
			r := out.Routes[0]
			if !slices.Equal(modules(r), []string{"forward_auth", "headers"}) || r.Handler.Params["retry"].(map[string]any)["attempts"] != 3.0 {
				t.Errorf("%+v", r)
			}
			fa := r.Middleware[0].Params
			if fa["uri"] != "/verify" || fmt.Sprint(fa["copy_headers"]) != "[Remote-User]" || fa["upstream"] != "proxy_auth_9091" {
				t.Errorf("%+v", fa)
			}
			if len(ws) != 3 {
				t.Errorf("%v", ws)
			}
		}},
		{"global", `{
	auto_https disable_redirects
	https_port 8443
	admin off
	on_demand_tls {
		ask http://localhost:5555/check
	}
}
example.com {
	tls {
		on_demand
	}
	request_body {
		max_size 10MB
	}
	respond "hi {path}"
}`, func(t *testing.T, out outConfig, ws []string) {
			if out.Admin != nil || out.AutoHTTPS.HTTPSPort != 8443 || !out.AutoHTTPS.DisableRedirects || out.ACME.OnDemand.Ask == "" {
				t.Errorf("%+v %+v %+v", out.Admin, out.AutoHTTPS, out.ACME)
			}
			if len(out.Listeners) != 1 || out.Listeners[0].Address != ":8443" || !out.Listeners[0].TLS.OnDemand || !slices.Equal(out.Listeners[0].TLS.ACME, []string{"example.com"}) {
				t.Errorf("%+v", out.Listeners[0])
			}
			if mw := out.Routes[0].Middleware[0]; mw.Params["max_bytes"] != 1e7 {
				t.Errorf("%+v", mw)
			}
			if len(ws) != 1 || !strings.Contains(ws[0], "placeholders") {
				t.Errorf("%v", ws)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, ws := convert(t, tt.src)
			tt.check(t, out, ws)
		})
	}
	for _, src := range []string{"{\n email a@b.test\n}\n", "unix//x.sock {\n respond 200\n}\n"} {
		if _, err := Convert([]byte(src)); err == nil {
			t.Errorf("%q: no error", src)
		}
	}
}

func TestIntersect(t *testing.T) {
	for _, tt := range []struct {
		a, b *outMatch
		want string
		ok   bool
	}{
		{&outMatch{Path: []string{"/*"}}, &outMatch{Path: []string{"/api/*"}}, `{"path":["/api/*"]}`, true},
		{&outMatch{Path: []string{"/api/*"}}, &outMatch{Path: []string{"/web/*"}}, "", false},
		{&outMatch{Path: []string{"/a/x"}}, &outMatch{Path: []string{"/a/*"}}, `{"path":["/a/x"]}`, true},
		{&outMatch{Method: []string{"GET"}}, &outMatch{Method: []string{"POST"}}, "", false},
		{&outMatch{Host: []string{"*.a.test"}}, &outMatch{Host: []string{"b.a.test"}}, `{"host":["*.a.test"],"expr":"Host(\"b.a.test\")"}`, true},
		{&outMatch{PathRegexp: []string{"x"}, Expr: `Method("GET")`}, &outMatch{PathRegexp: []string{"y"}}, `{"path_regexp":["x"],"expr":"Method(\"GET\") \u0026\u0026 PathRegexp(\"y\")"}`, true},
		{&outMatch{Header: map[string][]string{"x-a": {}}}, &outMatch{Header: map[string][]string{"X-A": {"1"}}}, `{"header":{"x-a":["1"]}}`, true},
	} {
		got, ok := intersect(tt.a, tt.b)
		if ok != tt.ok {
			t.Errorf("%+v ∩ %+v: %v", tt.a, tt.b, ok)
			continue
		}
		if ok {
			if b, _ := json.Marshal(got); string(b) != tt.want {
				t.Errorf("%+v ∩ %+v: %s, want %s", tt.a, tt.b, b, tt.want)
			}
		}
	}
}

func TestUnits(t *testing.T) {
	for in, want := range map[string]string{"5s": "5s", "1d": "24h", "1.5d2h": "36h2h", "500ms": "500ms"} {
		if got, ok := duration(in); !ok || got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
	if _, ok := duration("soon"); ok {
		t.Error("soon")
	}
	for in, want := range map[string]int64{"10MB": 10e6, "1GiB": 1 << 30, "512k": 512e3, "100": 100, "2KiB": 2048} {
		if got, ok := size(in); !ok || got != want {
			t.Errorf("%s: %d", in, got)
		}
	}
	for in, want := range map[string]string{"app": "[app:80]", "https://a.test": "[a.test:443]", ":9000": "[localhost:9000]", "b:1-3": "[b:1 b:2 b:3]"} {
		if got, _, why := upstreamAddr(in); why != "" || fmt.Sprint(got) != want {
			t.Errorf("%s: %v %s", in, got, why)
		}
	}
}

// TestServe serves a converted Caddyfile: a file from its root, a
// response, a redirect, and a proxy with handle_path's prefix stripped.
func TestServe(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "index.html"), []byte("home"), 0o644)
	backend := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		fmt.Fprintf(w, "backend %s %s", r.URL.Path, r.Header.Get("X-Env"))
	}))
	defer backend.Close()
	src := fmt.Sprintf(`# comments are fine anywhere
:0 {
	bind 127.0.0.1
	root * %s
	respond /health "ok" # a trailing comment
	redir /old /new 301
	handle_path /api/* {
		request_header X-Env test
		reverse_proxy %s
	}
	try_files {path} /index.html
	file_server
}`, root, backend.URL)
	r, err := Convert([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(r.JSON)
	if err != nil {
		t.Fatalf("%v\n%s", err, r.JSON)
	}
	c.Admin = nil
	engine, stopEngine, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer stopEngine()
	g, err := runtime.Compile(c, runtime.Env{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.Close()
		rt.Close()
	}()
	base := "http://" + s.Addrs()[0].Addr.String()
	client := &stdhttp.Client{Timeout: 10 * time.Second, CheckRedirect: func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }}
	for path, want := range map[string]string{
		"/health":      "200 ok",
		"/":            "200 home",
		"/app/deep":    "200 home",
		"/old":         "301 /new",
		"/api/users/1": "200 backend /users/1 test",
	} {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got := fmt.Sprintf("%d %s", resp.StatusCode, body)
		if resp.StatusCode == 301 {
			got = fmt.Sprintf("%d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		if got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}
