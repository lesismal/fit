package nginx

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

// warnedLines returns the lines the warnings are about.
func warnedLines(ws []string) []int {
	var lines []int
	for _, w := range ws {
		lines = append(lines, warningLine(w))
	}
	return lines
}

// TestSamples converts the sample configs: each converts to a config fit
// loads, and each directive left out is reported at its line.
func TestSamples(t *testing.T) {
	for _, tt := range []struct {
		file   string
		routes int
		warned []int
	}{
		{"proxy.conf", 3, []int{25, 36}},
		{"static.conf", 6, nil},
		{"full.conf", 3, []int{1, 2, 3, 4, 6, 11, 12, 13, 14, 15, 16, 17, 28, 33, 47, 51, 55}},
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

func TestProxySample(t *testing.T) {
	data, _ := os.ReadFile("testdata/proxy.conf")
	out, _ := convert(t, string(data))
	b := out.Upstreams[0]
	if b.Name != "backend" || !slices.Equal(b.Addresses, []string{"10.0.0.1:8080", "10.0.0.2:8080"}) || !slices.Equal(b.Weights, []int{3, 1}) ||
		b.LoadBalancing != "least_conn" || *b.MaxIdleConns != 64 || *b.MaxFails != 2 || b.FailTimeout != "30s" ||
		b.ConnectTimeout != "5s" || b.ResponseIdleTimeout != "2m" {
		t.Errorf("backend: %+v", b)
	}
	if out.Upstreams[1].Name != "backend_backup" || out.Upstreams[1].Addresses[0] != "10.0.0.3:8080" {
		t.Errorf("backup: %+v", out.Upstreams[1])
	}
	if l := out.Listeners[1]; l.Address != ":443" || !slices.Equal(l.Protocols, []string{"http/1.1", "h2"}) || len(l.TLS.Certificates) != 1 {
		t.Errorf("443: %+v", l)
	}
	api := out.Routes[1]
	var mods []string
	for _, m := range api.Middleware {
		mods = append(mods, m.Module)
	}
	if !slices.Equal(mods, []string{"rate_limit", "body_limit", "headers", "rewrite", "compress"}) ||
		api.Handler.Params["failover"] != "backend_backup" || api.Match.Path[0] != "/api/*" {
		t.Errorf("/api/: %v %+v", mods, api)
	}
	redirect := out.Routes[0]
	if redirect.Middleware[0].Module != "redirect" || redirect.Middleware[0].Params["to"] != "https://{host}{uri}" {
		t.Errorf("the redirect: %+v", redirect)
	}
}

func TestParse(t *testing.T) {
	ds, err := parse([]byte(`# a comment
a "quoted; with # and \"escapes\"" 'single' plain;
b ${var}x {  c 1; d { e; } }
f "line
two";
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 || ds[0].args[0] != `quoted; with # and "escapes"` || ds[0].args[1] != "single" || ds[0].line != 2 {
		t.Fatalf("%+v", ds[0])
	}
	if ds[1].args[0] != "${var}x" || len(ds[1].block) != 2 || ds[1].block[1].block[0].name != "e" {
		t.Errorf("%+v", ds[1])
	}
	if ds[2].line != 4 || ds[2].args[0] != "line\ntwo" {
		t.Errorf("%+v", ds[2])
	}
	for src, want := range map[string]string{
		"a b":       "does not end",
		"a { b; ":   "does not end",
		"a; }":      "unexpected",
		"a \"open;": "does not end",
		"; a;":      "no directive",
		"a { b }":   "does not end with",
		"http { a ": "does not end",
		strings.Repeat("a {", 100) + strings.Repeat("}", 100): "nested too deep",
	} {
		if _, err := parse([]byte(src)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", src, err, want)
		}
	}
}

// TestDirectives converts single servers, each showing a directive's cases.
func TestDirectives(t *testing.T) {
	server := func(body string) string { return "http {\n server {\n" + body + "\n }\n}\n" }
	for _, tt := range []struct {
		name, src string
		check     func(t *testing.T, out outConfig, ws []string)
	}{
		{"names", server("listen 8080; server_name a.test *.b.test .c.test ~^re$ _;"), func(t *testing.T, out outConfig, ws []string) {
			if h := out.Routes[0].Match.Host; !slices.Equal(h, []string{"a.test", "**.b.test", "c.test", "**.c.test"}) {
				t.Errorf("%v", h)
			}
			if len(ws) != 1 || !strings.Contains(ws[0], "server_name ~^re$") {
				t.Errorf("%v", ws)
			}
		}},
		{"listen", server("listen 127.0.0.1:8080; listen 8443 ssl; listen 8443 quic; listen unix:/tmp/s; ssl_certificate c; ssl_certificate_key k;"), func(t *testing.T, out outConfig, ws []string) {
			if len(out.Listeners) != 2 || out.Listeners[0].Address != "127.0.0.1:8080" || !slices.Equal(out.Listeners[1].Protocols, []string{"http/1.1", "h3"}) {
				t.Errorf("%+v %+v", out.Listeners[0], out.Listeners[1])
			}
			if len(ws) != 1 || !strings.Contains(ws[0], "listen: skipped: fit listens on TCP") {
				t.Errorf("%v", ws)
			}
		}},
		{"ssl without a certificate", server("listen 80; listen 443 ssl;"), func(t *testing.T, out outConfig, ws []string) {
			if len(out.Listeners) != 1 || len(ws) != 1 || !strings.Contains(ws[0], "no ssl_certificate") {
				t.Errorf("%+v %v", out.Listeners, ws)
			}
		}},
		{"returns", server("listen 80; location /a { return 404; } location /b { return 302 /c?x=$args; } location /d { return https://e.test$uri; } location /f { return 200 $host; } location /g { return 301 $foo; }"),
			func(t *testing.T, out outConfig, ws []string) {
				if out.Routes[0].Handler.Params["status"] != 404.0 || out.Routes[1].Middleware[0].Params["to"] != "/c?x={query}" ||
					out.Routes[2].Middleware[0].Params["to"] != "https://e.test{path}" || out.Routes[2].Middleware[0].Params["status"] != 302.0 {
					t.Errorf("%+v", out.Routes)
				}
				if len(ws) != 2 || !strings.Contains(ws[0], "its text has variables") || !strings.Contains(ws[1], "no placeholder") {
					t.Errorf("%v", ws)
				}
			}},
		{"rewrites", server("listen 80; rewrite ^/x$ /y last; location / { rewrite /a /b; rewrite ^/q /r?s=1; root /w; }"), func(t *testing.T, out outConfig, ws []string) {
			mw := out.Routes[0].Middleware
			if len(mw) != 2 || mw[0].Params["regexp"] != "^/x$" || mw[1].Params["regexp"] != "^.*?(?:/a).*$" || mw[1].Params["replacement"] != "/b" {
				t.Errorf("%+v", mw)
			}
			if len(ws) != 2 || !strings.Contains(ws[0], "last") || !strings.Contains(ws[1], "with a query") {
				t.Errorf("%v", ws)
			}
		}},
		{"proxy_pass", server("listen 80; location / { proxy_pass https://api.test; proxy_set_header Host api.test; } location /v { proxy_pass http://$x; }"), func(t *testing.T, out outConfig, ws []string) {
			u := out.Upstreams[0]
			if u.Addresses[0] != "api.test:443" || u.Protocol != "h2" || out.Routes[0].Handler.Params["host_header"] != "api.test" {
				t.Errorf("%+v %+v", u, out.Routes[0])
			}
			if len(ws) != 2 || !strings.Contains(ws[0], "HTTP/2") || !strings.Contains(ws[1], "variables") {
				t.Errorf("%v", ws)
			}
		}},
		{"upstream hashes", "http {\n upstream a { hash $request_uri consistent; server x; }\n upstream b { hash $http_x_user; server y:81 down; server z:82; }\n upstream c { ip_hash; server unix:/s; server w; }\n server { listen 80; location /a { proxy_pass http://a; } location /b { proxy_pass http://b; } location /c { proxy_pass http://c; } }\n}\n",
			func(t *testing.T, out outConfig, ws []string) {
				if out.Upstreams[0].LoadBalancing != "uri_hash" || out.Upstreams[0].Addresses[0] != "x:80" ||
					out.Upstreams[1].LoadBalancing != "header_hash" || out.Upstreams[1].HashKey != "x-user" || len(out.Upstreams[1].Addresses) != 1 ||
					out.Upstreams[2].LoadBalancing != "ip_hash" {
					t.Errorf("%+v", out.Upstreams)
				}
				if len(ws) != 2 || !strings.Contains(ws[0], "down: left out") || !strings.Contains(ws[1], "over TCP") {
					t.Errorf("%v", ws)
				}
			}},
		{"limit_req", "http {\n limit_req_zone $binary_remote_addr zone=z:1m rate=30r/m;\n server { listen 80; limit_req zone=z; location / { return 204; } location /x { limit_req zone=nope; return 204; } }\n}\n",
			func(t *testing.T, out outConfig, ws []string) {
				if mw := out.Routes[0].Middleware; len(mw) != 1 || mw[0].Params["rate"] != 0.5 {
					t.Errorf("%+v", mw)
				}
				if len(ws) != 2 || !strings.Contains(ws[0], "without nodelay") || !strings.Contains(ws[1], "zone was not converted") {
					t.Errorf("%v", ws)
				}
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, ws := convert(t, tt.src)
			tt.check(t, out, ws)
		})
	}
	if _, err := Convert([]byte("events {}")); err == nil {
		t.Error("a config with no server")
	}
}

func TestUnits(t *testing.T) {
	for in, want := range map[string]string{"60": "60s", "2m": "2m", "500ms": "500ms", "1d": "24h", "1h30m": "1h30m", "1w": "168h"} {
		if got, ok := duration([]string{in}); !ok || got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
	if _, ok := duration([]string{"soon"}); ok {
		t.Error("soon")
	}
	for in, want := range map[string]int64{"10m": 10 << 20, "512k": 512 << 10, "1G": 1 << 30, "100": 100} {
		if got, ok := size(in); !ok || got != want {
			t.Errorf("%s: %d", in, got)
		}
	}
}

// TestServe serves a converted config: a file from its root, a return, a
// redirect, and a proxy to a backend with the location's prefix replaced.
func TestServe(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "index.html"), []byte("home"), 0o644)
	backend := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		fmt.Fprintf(w, "backend %s %s", r.URL.Path, r.Header.Get("X-Env"))
	}))
	defer backend.Close()
	src := fmt.Sprintf(`http {
  server {
    listen 127.0.0.1:0;
    root %s;
    location = /health { return 200 "ok"; }
    location /old { return 301 /new; }
    location /api/ {
      proxy_pass http://%s/v1/;
      proxy_set_header X-Env test;
    }
    location / { try_files $uri $uri/ /index.html; }
  }
}`, root, strings.TrimPrefix(backend.URL, "http://"))
	r, err := Convert([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(r.JSON)
	if err != nil {
		t.Fatal(err)
	}
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
		"/old":         "301 ",
		"/api/users/1": "200 backend /v1/users/1 test",
	} {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got := fmt.Sprintf("%d %s", resp.StatusCode, body)
		if resp.StatusCode == 301 {
			got = fmt.Sprintf("%d ", resp.StatusCode)
			if resp.Header.Get("Location") != "/new" {
				t.Errorf("%s: Location %q", path, resp.Header.Get("Location"))
			}
		}
		if got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}
