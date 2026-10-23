package nginx

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestConversions converts configs that show the directives' other cases:
// each warning they should give is given, and what they convert is checked.
func TestConversions(t *testing.T) {
	server := func(body string) string { return "http {\n server {\n" + body + "\n }\n}\n" }
	for _, tt := range []struct {
		name, src string
		// noLoad is for a config whose result fit is not meant to load.
		noLoad bool
		warn   []string
		check  func(t *testing.T, out outConfig)
	}{
		{name: "a route name of no letters", src: server(`listen 80; server_name "*"; return 204;`), noLoad: true,
			check: func(t *testing.T, out outConfig) {
				if out.Routes[0].Name != "route" {
					t.Errorf("%q", out.Routes[0].Name)
				}
			}},
		{name: "a long route name", src: server("listen 80; server_name " + strings.Repeat("a", 70) + ".test; location /x { return 204; }"),
			check: func(t *testing.T, out outConfig) {
				if len(out.Routes[0].Name) != 60 {
					t.Errorf("%q", out.Routes[0].Name)
				}
			}},
		{name: "a server's return before its locations", src: server("listen 80; return 204; location /a { return 200; }"),
			warn: []string{"location /a: skipped: the server's return answers every request first"},
			check: func(t *testing.T, out outConfig) {
				if len(out.Routes) != 1 || out.Routes[0].Match != nil || out.Routes[0].Handler.Params["status"] != 204.0 {
					t.Errorf("%+v", out.Routes)
				}
			}},
		{name: "returns that do not convert", src: "http {\n server { listen 80; return 999; }\n server { listen 81; location /a { return 1 2 3; } location /b { return; } location /c { return 700 x; } location / { return 204; } }\n}\n",
			warn: []string{"line 2: return: skipped: a status out of range", "return takes a status and a text or URL"},
			check: func(t *testing.T, out outConfig) {
				if len(out.Routes) != 1 {
					t.Errorf("%+v", out.Routes)
				}
			}},
		{name: "a server with no listen converted", src: "http {\n server { listen unix:/s; return 204; }\n server { listen 80; return 204; }\n}\n",
			warn: []string{"line 2: server: skipped: none of its listen directives converted"},
			check: func(t *testing.T, out outConfig) {
				if len(out.Listeners) != 1 || len(out.Routes) != 1 {
					t.Errorf("%+v %+v", out.Listeners, out.Routes)
				}
			}},
		{name: "server directives", src: server("listen 80; listen 81 proxy_protocol; listen 82 fastopen=3 default_server; listen; http2 on; ssl on; server_name a.* b.test; add_header X-A $host; add_header X-B b; gzip on; gzip_types text/plain; ssl_protocols TLSv1.3; location /a { return 204; }"),
			warn: []string{"PROXY protocol is not converted", "listen 82 fastopen=3: skipped: not converted", "listen: skipped: no address",
				"ssl: skipped: write ssl on listen", "server_name a.*: skipped: a wildcard at the end", "add_header: skipped: its value has variables",
				"gzip_types: skipped: fit's compress middleware", "ssl_protocols: skipped"},
			check: func(t *testing.T, out outConfig) {
				if !slices.Equal(out.Listeners[0].Protocols, []string{"http/1.1", "h2c"}) || len(out.Listeners) != 3 {
					t.Errorf("%+v", out.Listeners)
				}
				var mods []string
				for _, m := range out.Routes[0].Middleware {
					mods = append(mods, m.Module)
				}
				if !slices.Equal(mods, []string{"headers", "compress"}) || !slices.Equal(out.Routes[0].Match.Host, []string{"b.test"}) {
					t.Errorf("%v %+v", mods, out.Routes[0].Match)
				}
			}},
		{name: "listeners with and without ssl", src: "http {\n server { listen 8443 ssl; listen [::]:8080; ssl_certificate c; ssl_certificate_key k; return 204; }\n server { listen 8443; return 204; }\n server { listen 8443 ssl; listen 8080 ssl; ssl_certificate c2; ssl_certificate_key k2; return 204; }\n}\n",
			warn: []string{"line 3: listen 8443: skipped: another server listens there with ssl", "line 4: listen 8080: skipped: another server listens there without ssl"},
			check: func(t *testing.T, out outConfig) {
				if len(out.Listeners) != 2 || len(out.Listeners[0].TLS.Certificates) != 2 || out.Listeners[1].Address != ":8080" {
					t.Errorf("%+v", out.Listeners)
				}
			}},
		{name: "locations", src: server("listen 80; location {} location a b c {} location @named {} location ~~ /x {} location ~ ( {} location ~* ^/i { return 204; } location /p { location /p/q { return 204; } return 200; }"),
			warn: []string{"location: skipped: a location needs a path", "named locations", "an unknown location modifier ~~", "Go's regular expressions do not take it"},
			check: func(t *testing.T, out outConfig) {
				var paths []string
				for _, r := range out.Routes {
					if r.Match != nil {
						paths = append(paths, strings.Join(append(r.Match.Path, r.Match.PathRegexp...), ","))
					}
				}
				if !slices.Equal(paths, []string{"(?i)^/i", "/p*", "/p/q*"}) {
					t.Errorf("%v", paths)
				}
			}},
		{name: "static files", src: server("listen 80; location /a { try_files $uri =404; } location ~ ^/b { alias /srv/b/; } location ^~ /c/ { alias /srv/c/; index index.php; try_files $uri $uri/ =500; } location /d { root /srv; try_files =404 $uri; } location /e { root /srv; try_files @x; } location /f { root /srv; try_files $foo; } location /g { root /srv; try_files =404; }"),
			warn: []string{"location /a: try_files with no root: answered 404", "alias in a location of a regular expression is not converted",
				"try_files: the last =500 answers 404 in fit", "try_files: skipped: nothing to try", "try_files: skipped: named locations",
				"try_files: skipped: it has variables other than $uri"},
			check: func(t *testing.T, out outConfig) {
				for _, r := range out.Routes {
					if r.Match != nil && r.Match.Path[0] == "/c/*" {
						p := r.Handler.Params
						if p["root"] != "/srv/c" || p["strip_prefix"] != "/c" || p["index"].([]any)[0] != "index.php" || len(p["try_files"].([]any)) != 2 {
							t.Errorf("%+v", p)
						}
						return
					}
				}
				t.Errorf("no /c/: %+v", out.Routes)
			}},
		{name: "rewrites", src: server("listen 80; location / { rewrite /x; rewrite ( /y; rewrite ^/a https://$foo redirect; rewrite ^/b /c; rewrite ^/old https://x.test/new permanent; return 204; }"),
			warn: []string{"rewrite: skipped: rewrite takes a regular expression", "Go's regular expressions do not take \"(\"", "its replacement has variables fit has no placeholder for"},
			check: func(t *testing.T, out outConfig) {
				mw := out.Routes[0].Middleware
				if len(mw) != 2 || mw[0].Params["regexp"] != "^(?:/b).*$" || mw[1].Module != "redirect" || mw[1].Params["status"] != 301.0 {
					t.Errorf("%+v", mw)
				}
			}},
		{name: "proxies", src: "http {\n upstream up { server 10.0.0.1:81; }\n server { listen 80;\n location /a { proxy_set_header X; proxy_set_header X-Env $foo; proxy_pass; }\n location /b { proxy_pass unix:/sock; }\n location ~ ^/c { proxy_pass http://up/x/; }\n location /d { proxy_pass http://h.test:81; proxy_connect_timeout 5s; proxy_read_timeout 1m; proxy_set_header Connection upgrade; proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; proxy_set_header Host $host; }\n location /e { proxy_pass http://h.test:81/; }\n }\n}\n",
			warn: []string{"proxy_set_header: skipped: proxy_set_header takes a field and a value", "proxy_set_header: skipped: its value has variables",
				"proxy_pass: skipped: proxy_pass takes one URL", "proxy_pass: skipped: fit proxies to http:// and https:// upstreams",
				"proxy_pass http://up/x/: the URI is not converted in a location of ~"},
			check: func(t *testing.T, out outConfig) {
				if len(out.Upstreams) != 2 || out.Upstreams[1].Name != "proxy_h_test_81" || out.Upstreams[1].ConnectTimeout != "5s" || out.Upstreams[1].ResponseIdleTimeout != "1m" {
					t.Errorf("%+v", out.Upstreams)
				}
				for _, r := range out.Routes {
					if r.Match != nil && len(r.Match.Path) > 0 && r.Match.Path[0] == "/e*" {
						if len(r.Middleware) != 1 || r.Middleware[0].Params["strip_prefix"].([]any)[0] != "/e" {
							t.Errorf("%+v", r.Middleware)
						}
					}
				}
			}},
		{name: "upstreams", src: "http {\n upstream { server x; }\n upstream a { server; server x:1 max_conns=5 weight=2 slow_start=1; server y:1; random two; keepalive x; keepalive_timeout 5s; zone z 64k; }\n upstream b { hash; hash $remote_addr; server x; }\n upstream c { hash $cookie_sid; server x; }\n upstream d { hash $foo; server x; }\n upstream e { server x backup; server y backup; }\n upstream f { server unix:/s; }\n upstream g { random; server x; }\n upstream h { server x weight=3; server y; max_fails 1; }\n server { listen 80; location /a { proxy_pass http://a; } location /b { proxy_pass http://b; } location /c { proxy_pass http://c; } location /d { proxy_pass http://d; } location /e { proxy_pass http://e; } location /g { proxy_pass http://g; } location /h { proxy_pass http://h; } }\n}\n",
			warn: []string{"upstream: skipped: an upstream needs a name", "server: skipped: no address", "server x:1 slow_start=1: skipped: not converted",
				"keepalive: skipped: keepalive takes a number", "keepalive_timeout: skipped: fit's upstream idle_timeout", "zone: skipped: not converted",
				"hash: skipped: hash takes a key", "hash $foo: skipped: fit hashes on", "upstream f: skipped: no server converted", "max_fails: skipped: not converted"},
			check: func(t *testing.T, out outConfig) {
				ups := map[string]*outUpstream{}
				for _, u := range out.Upstreams {
					ups[u.Name] = u
				}
				if a := ups["a"]; a.MaxConns != 5 || a.LoadBalancing != "p2c" || !slices.Equal(a.Weights, []int{2, 1}) {
					t.Errorf("a: %+v", a)
				}
				if ups["b"].LoadBalancing != "ip_hash" || ups["c"].LoadBalancing != "cookie_hash" || ups["c"].HashKey != "sid" ||
					ups["d"].LoadBalancing != "" || ups["g"].LoadBalancing != "random" || ups["h"].LoadBalancing != "weighted_round_robin" {
					t.Errorf("%+v %+v %+v %+v %+v", ups["b"], ups["c"], ups["d"], ups["g"], ups["h"])
				}
				if e := ups["e"]; !slices.Equal(e.Addresses, []string{"x:80", "y:80"}) || ups["e_backup"] != nil || ups["f"] != nil {
					t.Errorf("e: %+v", e)
				}
			}},
		{name: "limit_req zones", src: "http {\n limit_req_zone $http_x_key zone=h:1m rate=5r/s;\n limit_req_zone $server_name zone=s:1m rate=1r/s;\n limit_req_zone $foo zone=f:1m rate=1r/s;\n limit_req_zone $remote_addr zone=bad:1m rate=fast;\n limit_req_zone $remote_addr rate=1r/s;\n server { listen 80; location / { limit_req zone=h burst=5 nodelay; limit_req zone=s delay=3 nodelay; return 204; } }\n}\n",
			warn: []string{"limit_req_zone $foo: skipped: fit counts requests by", "limit_req_zone: skipped: a rate fit cannot read", "limit_req_zone: skipped: a zone needs a name and a rate"},
			check: func(t *testing.T, out outConfig) {
				mw := out.Routes[0].Middleware
				if len(mw) != 2 || mw[0].Params["key"] != "header:x-key" || mw[0].Params["burst"] != 5.0 || mw[1].Params["key"] != "route" || mw[1].Params["rate"] != 1.0 {
					t.Errorf("%+v", mw)
				}
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Convert([]byte(tt.src))
			if err != nil {
				t.Fatalf("%v\n%s", err, tt.src)
			}
			if !tt.noLoad {
				if _, err := config.Load(r.JSON); err != nil {
					t.Fatalf("fit does not load it: %v\n%s", r.MapErrors(err), r.JSON)
				}
			}
			ws := strings.Join(r.Warnings, "\n")
			for _, w := range tt.warn {
				if !strings.Contains(ws, w) {
					t.Errorf("no warning %q in:\n%s", w, ws)
				}
			}
			var out outConfig
			if err := json.Unmarshal(r.JSON, &out); err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, out)
			}
		})
	}
}

// TestPosition checks that the routes, upstreams and listeners are placed
// at their lines, and nothing else is.
func TestPosition(t *testing.T) {
	r, err := Convert([]byte("http {\n upstream u { server x; }\n server {\n  listen 80;\n  location / { proxy_pass http://u; }\n }\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"listeners[0]": 4, "upstreams[0]": 2, "routes[0]": 5} {
		if line, col, ok := r.Position(path); !ok || line != want || col != 1 {
			t.Errorf("%s: %d:%d %v, want line %d", path, line, col, ok, want)
		}
	}
	if _, _, ok := r.Position("routes[1]"); ok {
		t.Error("routes[1] has a place")
	}
}

func TestParseMore(t *testing.T) {
	ds, err := parse([]byte(`a "x\ny\tz\q";`))
	if err != nil {
		t.Fatal(err)
	}
	if got := ds[0].args[0]; got != "x\ny\tz\\q" {
		t.Errorf("%q", got)
	}
	if _, err := parse([]byte(strings.Repeat("a;", maxDirectives+1))); err == nil || !strings.Contains(err.Error(), "directives") {
		t.Errorf("too many directives: %v", err)
	}
}
