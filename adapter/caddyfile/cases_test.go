package caddyfile

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// adapt converts src, failing the test on an error, and returns the
// result, its config and its warnings.
func adapt(t *testing.T, src string) (outConfig, []string) {
	t.Helper()
	r, err := Convert([]byte(src))
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	var out outConfig
	if err := json.Unmarshal(r.JSON, &out); err != nil {
		t.Fatal(err)
	}
	return out, r.Warnings
}

// js writes v as JSON, with & < > as they are.
func js(v any) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// routeAt returns the route whose first path is path.
func routeAt(t *testing.T, out outConfig, path string) *outRoute {
	t.Helper()
	for _, r := range out.Routes {
		if r.Match != nil && len(r.Match.Path) > 0 && r.Match.Path[0] == path {
			return r
		}
	}
	t.Fatalf("no route for %s", path)
	return nil
}

// upstreamNamed returns the upstream named name.
func upstreamNamed(t *testing.T, out outConfig, name string) *outUpstream {
	t.Helper()
	for _, u := range out.Upstreams {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("no upstream %s", name)
	return nil
}

// TestConvertCases converts Caddyfiles that each show a group of
// directives' cases, the converted and the skipped, checking the config
// and that every warning wanted is given.
func TestConvertCases(t *testing.T) {
	for _, tt := range []struct {
		name  string
		src   string
		warns []string
		check func(t *testing.T, out outConfig)
	}{
		{"global options", `{
	email a@b.test c
	acme_ca https://ca.test/dir
	acme_ca_root /ca.pem
	storage file_system /data
	admin
	auto_https disable_certs
	http_port 8080
	https_port nope
	grace_period 5s
	bogus_opt
	on_demand_tls {
		permission http {
			endpoint http://localhost:9/ask
		}
		interval 2m
		burst 5
		rate_limit 1
	}
	servers :443 {
		protocols h1 h2 h2c h3 spdy
		timeouts 5s
	}
}
:8080 {
	respond 200
}
`, []string{"line 2: email: skipped: not converted", "https_port: skipped: not converted", "grace_period: skipped: not converted: fit's defaults",
			"bogus_opt: skipped: not converted", "rate_limit: skipped", "protocols spdy: skipped", "timeouts: skipped: not converted: fit's limits",
			"servers :443: its options apply to every listener"}, func(t *testing.T, out outConfig) {
			a := out.ACME
			if a.Email != "" || a.Directory != "https://ca.test/dir" || a.CAFile != "/ca.pem" || a.Storage != "/data" ||
				a.OnDemand.Ask != "http://localhost:9/ask" || a.OnDemand.Rate != 5 || a.OnDemand.Interval != "2m" {
				t.Errorf("%+v %+v", a, a.OnDemand)
			}
			if out.Admin == nil || out.Admin.Address != "" || out.AutoHTTPS.HTTPPort != 8080 {
				t.Errorf("%+v %+v", out.Admin, out.AutoHTTPS)
			}
			if l := out.Listeners; len(l) != 1 || l[0].Address != ":8080" || l[0].TLS != nil || !slices.Equal(l[0].Protocols, []string{"http/1.1", "h2c"}) {
				t.Errorf("%+v", l[0])
			}
		}},
		{"global options off", `{
	storage file_system {
		root /var/certs
	}
	admin :2019 {
		origins x
	}
	auto_https ignore_loaded_certs
	auto_https off
	auto_https sideways
	on_demand_tls {
		interval soon
	}
}
example.com {
	tls /c.pem /k.pem
	respond 200
}
`, []string{"admin: its block is not converted", "auto_https: skipped: not converted", "on_demand_tls: skipped: on-demand TLS needs an ask URL"},
			func(t *testing.T, out outConfig) {
				if out.ACME.Storage != "/var/certs" || out.Admin.Address != ":2019" || !out.AutoHTTPS.Disable {
					t.Errorf("%+v %+v %+v", out.ACME, out.Admin, out.AutoHTTPS)
				}
				if len(out.Listeners) != 1 || out.Listeners[0].Address != ":443" || out.Listeners[0].TLS.ACME != nil || len(out.Routes) != 1 {
					t.Errorf("%+v %+v", out.Listeners, out.Routes)
				}
			}},
		{"global options skipped", "{\n storage redis\n admin a b\n}\n:8080 {\n respond 200\n}\n",
			[]string{"storage: skipped: fit keeps certificates", "admin: skipped: not converted"}, nil},
		{"protocols with none for HTTP", "{\n servers {\n  protocols h2 h3\n }\n}\n:8080 {\n respond 200\n}\n", nil, func(t *testing.T, out outConfig) {
			if !slices.Equal(out.Listeners[0].Protocols, []string{"http/1.1"}) {
				t.Errorf("%+v", out.Listeners[0])
			}
		}},
		{"tls", `{
	email ops@example.com
}
a.example.com {
	tls other@example.com
	respond 200
}
b.example.com {
	tls bogus
	tls {
		client_auth {
			mode request
			trusted_ca_cert_file /ca1.pem
			trust_pool file /ca2.pem
			verify_depth 3
		}
		protocols tls1.3
		issuer acme {
			email ops@example.com
			dir https://acme.test/dir
			eab x y
		}
		ca https://ca.example/dir
		ca
		dns cloudflare
		wildcard
	}
	respond 200
}
c.example.com {
	tls /c.pem /k.pem {
		client_auth {
			trust_pool file {
				pem_file /ca3.pem
			}
		}
	}
	respond 200
}
d.example.com {
	tls {
		client_auth {
			mode require_and_verify
		}
		issuer internal
	}
	respond 200
}
e.example.com {
	tls {
		client_auth {
			mode verify_if_given
			trusted_ca_cert_file /ca1.pem
			trusted_ca_cert_file /ca2.pem
		}
	}
	respond 200
}
`, []string{"tls other@example.com: fit has one ACME email, ops@example.com", "tls: skipped: tls takes a certificate file",
			"client_auth mode request: fit requires", "verify_depth: skipped: not converted", "protocols: skipped: fit picks TLS",
			"eab: skipped: not converted", "ca: skipped: not converted", "dns: skipped: fit answers", "wildcard: skipped: not converted",
			"client_auth: skipped: client_auth needs CA files", "issuer internal: fit has no internal CA",
			"tls client_auth: :443 already verifies clients with other CAs"}, func(t *testing.T, out outConfig) {
			if out.ACME.Email != "ops@example.com" || out.ACME.Directory != "https://ca.example/dir" {
				t.Errorf("%+v", out.ACME)
			}
			l := out.Listeners[0]
			if l.Address != ":443" || len(l.TLS.Certificates) != 1 || !slices.Equal(l.TLS.ACME, []string{"a.example.com", "b.example.com", "e.example.com"}) ||
				!slices.Equal(l.TLS.ClientAuth.CAFiles, []string{"/ca1.pem", "/ca2.pem"}) || l.TLS.ClientAuth.Mode != "verify_if_given" {
				t.Errorf("%+v %+v", l.TLS, l.TLS.ClientAuth)
			}
			redirect := out.Routes[len(out.Routes)-1]
			if redirect.Middleware[0].Module != "redirect" || !slices.Equal(redirect.Match.Host, []string{"a.example.com", "b.example.com", "c.example.com", "e.example.com"}) {
				t.Errorf("%+v", redirect)
			}
		}},
		{"tls internal and on demand", "a.example.com {\n tls internal\n respond 200\n}\nb.example.com {\n tls {\n  on_demand\n }\n respond 200\n}\n",
			[]string{"tls internal: fit has no internal CA", "tls on_demand: skipped: it needs the global on_demand_tls"}, nil},
		{"client auth alike", `c.example.com {
	tls /c.pem /k.pem {
		client_auth {
			trusted_ca_cert_file /ca.pem
		}
	}
	respond 200
}
d.example.com {
	tls /d.pem /k.pem {
		client_auth {
			trusted_ca_cert_file /ca.pem
		}
	}
	respond 200
}
`, nil, func(t *testing.T, out outConfig) {
			if ca := out.Listeners[0].TLS.ClientAuth; ca.Mode != "require" || len(out.Listeners[0].TLS.Certificates) != 2 {
				t.Errorf("%+v", out.Listeners[0].TLS)
			}
		}},
		{"log", `:8080 {
	log {
		output stderr
		format console
		level INFO
	}
	respond 200
}
:8081 {
	log {
		output file /var/log/a.log {
			roll_size 10MB
		}
		format json
	}
	respond 200
}
:8082 {
	log {
		output net localhost:9
	}
	respond 200
}
:8083 {
	log {
		output discard
	}
	respond 200
}
`, []string{"format: skipped: fit writes access log lines as JSON", "output file: rolling is not converted",
			"line 10: log: fit has one access log, written to stderr", "output: skipped: fit writes to stdout, stderr or a file"},
			func(t *testing.T, out outConfig) {
				if out.AccessLog.Output != "stderr" {
					t.Errorf("%+v", out.AccessLog)
				}
			}},
		{"log level", `:8080 {
	log {
		output stdout
		level WARN
	}
	respond 200
}
:8081 {
	log {
		output stdout
		level FATAL
	}
	respond 200
}
:8082 {
	log {
		level LOUD
	}
	respond 200
}
`, []string{"line 11: level FATAL is fit's error, its highest", "line 9: log: fit has one access log, written to stdout",
			"line 17: level: skipped: not a level"},
			func(t *testing.T, out outConfig) {
				if *out.AccessLog != (outAccessLog{Output: "stdout", Level: "warn"}) {
					t.Errorf("%+v", out.AccessLog)
				}
			}},
		{"global log", `{
	debug
	log {
		output file /var/log/caddy.log {
			roll_keep 5
		}
		format json
		level INFO
		exclude http.log.access
		sampling
	}
	log access {
		include http.log.access.web
		output stdout
		level ERROR
	}
	log other {
		output stdout
	}
}
:8080 {
	respond 200
}
`, []string{"line 4: output file: rolling is not converted", "line 9: exclude: skipped: fit's log.levels",
			"line 10: sampling: skipped: not converted", "line 17: log: skipped: fit has one log"},
			func(t *testing.T, out outConfig) {
				if *out.Log != (outLog{Output: "/var/log/caddy.log", Format: "json"}) {
					t.Errorf("log %+v", out.Log)
				}
				if *out.AccessLog != (outAccessLog{Output: "stdout", Level: "error"}) {
					t.Errorf("access_log %+v", out.AccessLog)
				}
			}},
		{"global debug", `{
	debug
	log {
		format console
		output discard
		include http
	}
}
:8080 {
	respond 200
}
`, []string{"line 5: output: skipped: fit writes its log to stdout, stderr or a file", "line 6: include: skipped: fit's log takes every logger's"},
			func(t *testing.T, out outConfig) {
				if *out.Log != (outLog{Output: "stderr", Level: "debug"}) {
					t.Errorf("log %+v", out.Log)
				}
			}},
		{"addresses", `ftp://a.test {
	respond 200
}
unix//x.sock, a.test/x*y, a.test:http, *.*.a.test, a*b.test, {host}.test {
	respond 200
}
http://*:8090/api/*, https://localhost:8443 {
	tls /c.pem /k.pem
	respond 200
}
`, []string{"site address ftp://a.test: skipped: fit serves http://", "site address unix//x.sock: skipped: not converted",
			"a.test/x*y: skipped: fit takes a path", "a.test:http: skipped: not a port", "*.*.a.test: skipped: fit takes names",
			"a*b.test: skipped: fit takes names", "{host}.test: skipped: not converted", "site ftp://a.test: skipped: none of its addresses converted"},
			func(t *testing.T, out outConfig) {
				if len(out.Listeners) != 2 || out.Listeners[0].Address != ":8090" || out.Listeners[1].TLS == nil {
					t.Errorf("%+v", out.Listeners)
				}
				if r := out.Routes[0]; r.Match.Host != nil || r.Match.Path[0] != "/api/*" {
					t.Errorf("%+v", r.Match)
				}
			}},
		{"listener taken without TLS", "http://a.test:9000 {\n respond 200\n}\nhttps://b.test:9000 {\n tls /c.pem /k.pem\n respond 200\n}\n",
			[]string{"site address https://b.test:9000: skipped: another site serves :9000 without TLS"}, func(t *testing.T, out outConfig) {
				if len(out.Routes) != 1 {
					t.Errorf("%+v", out.Routes)
				}
			}},
		{"addresses alike on two listeners", "a.test:8080, a.test:8081 {\n respond 200\n}\n", nil, func(t *testing.T, out outConfig) {
			if len(out.Routes) != 1 || len(out.Routes[0].Listeners) != 2 {
				t.Errorf("%+v", out.Routes)
			}
		}},
		{"addresses with different paths", "a.test/x, a.test/y {\n respond 200\n}\n", []string{"its addresses have different paths"}, func(t *testing.T, out outConfig) {
			if out.Routes[0].Match.Path != nil {
				t.Errorf("%+v", out.Routes[0].Match)
			}
		}},
		{"redirects on another HTTPS port", "{\n https_port 8443\n}\nb.example.com {\n tls /c /k\n respond 200\n}\n", nil, func(t *testing.T, out outConfig) {
			r := out.Routes[len(out.Routes)-1]
			if r.Middleware[0].Params["to"] != "https://{host}:8443{uri}" || out.Listeners[1].Address != ":80" {
				t.Errorf("%+v %+v", r, out.Listeners)
			}
		}},
		{"redirects with TLS on the HTTP port", "{\n https_port 8443\n}\nhttps://a.example.com:80 {\n tls /c /k\n respond 200\n}\nb.example.com {\n tls /c /k\n respond 200\n}\n",
			[]string{"redirects to HTTPS: skipped: :80 serves TLS"}, nil},
		{"modifiers", `:8080 {
	root /a b c
	root * {vars.root}
	redir
	redir /x https://{foo}/
	redir /y /z temporary
	redir /h /z html
	redir /b /z 200
	redir /p /z 307
	rewrite /r a b
	rewrite /q /new?x=1
	rewrite /rel rel
	rewrite /same {path}
	rewrite /ph /x/{query}
	rewrite /lit /a$b
	uri /u1 only
	uri /u2 strip_prefix api/
	uri /u3 strip_suffix .html
	uri /u4 replace a b 1
	uri /u5 path_regexp ( x
	uri /u6 path_regexp ^/a /b
	uri /u7 bogus a
	header @nope X 1
	header /hd -X 1
	header /hd2 X
	header /hd3 X a b
	header /hd4 X {path}
	header /hd5 +X a
	header /hd6 ?X a
	header /hd7 >X a
	header /hd8 {
		defer
		X-B b
	}
	header /hd9 {
		defer
	}
	request_body /rb {
		max_size lots
	}
	respond 200
}
`, []string{"root: skipped: root takes a path", "root: skipped: a root with placeholders", "redir: skipped: redir takes a URL",
			"redir: skipped: its URL has placeholders", "redir html: written as a 302", "redir: skipped: a status fit's redirect does not take",
			"rewrite: skipped: rewrite takes a URI", "rewrite: skipped: a rewrite of the query", "rewrite: skipped: fit rewrites to a path starting with /",
			"rewrite: skipped: a URI with placeholders other than {path}", "uri: skipped: uri takes an operation",
			"uri replace: its limit is not converted", "uri: skipped: Go's regular expressions do not take", "uri: skipped: uri bogus is not converted",
			"header: skipped: no matcher @nope is defined", "header -X: skipped: a deletion takes no value", "header X: skipped: a field with no value",
			"header X: skipped: replacing within a value", "header X: skipped: its value has placeholders", "header ?X: written as set",
			"header: skipped: no field it changes converted", "request_body: skipped: fit limits a request body's size"},
			func(t *testing.T, out outConfig) {
				for path, want := range map[string]string{
					"/y":   `{"status":302,"to":"/z"}`,
					"/p":   `{"status":307,"to":"/z"}`,
					"/lit": `{"regexp":"^.*$","replacement":"/a$$b"}`,
					"/u2":  `{"strip_prefix":["/api"]}`,
					"/u3":  `{"regexp":"\\.html$","replacement":""}`,
					"/u4":  `{"regexp":"a","replacement":"b"}`,
					"/u6":  `{"regexp":"^/a","replacement":"/b"}`,
					"/hd5": `{"response":{"add":{"X":["a"]}}}`,
					"/hd6": `{"response":{"set":{"X":"a"}}}`,
					"/hd7": `{"response":{"set":{"X":"a"}}}`,
					"/hd8": `{"response":{"set":{"X-B":"b"}}}`,
				} {
					r := routeAt(t, out, path)
					if len(r.Middleware) != 1 {
						t.Errorf("%s: %+v", path, r.Middleware)
						continue
					}
					if b := js(r.Middleware[0].Params); b != want {
						t.Errorf("%s: %s, want %s", path, b, want)
					}
				}
			}},
		{"try_files", `:8080 {
	root * /srv
	try_files /t1 =404 {path}
	try_files /t2 {path}?q
	try_files /t3 index.html =500 {
		policy first_exist
	}
	try_files /t4 =404
	file_server
}
`, []string{"try_files: skipped: =status is not converted but last", "try_files: skipped: a file with a query",
			"try_files: the last =500 answers 404 in fit", "policy: skipped: fit tries files in order", "try_files: skipped: nothing to try"},
			func(t *testing.T, out outConfig) {
				if r := routeAt(t, out, "/t3"); fmt.Sprint(r.Handler.Params["try_files"]) != "[/index.html]" || r.Handler.Params["root"] != "/srv" {
					t.Errorf("%+v", r.Handler)
				}
			}},
		{"inner root and try_files stand", `:8080 {
	root * /outer
	try_files {path} /outer.html
	handle /in/* {
		root * /inner
		try_files {path} /inner.html
		file_server
	}
	file_server
}
`, nil, func(t *testing.T, out outConfig) {
			in := routeAt(t, out, "/in/*")
			if in.Handler.Params["root"] != "/inner" || fmt.Sprint(in.Handler.Params["try_files"]) != "[{path} /inner.html]" {
				t.Errorf("%+v", in.Handler)
			}
			last := out.Routes[len(out.Routes)-1]
			if last.Handler.Params["root"] != "/outer" || fmt.Sprint(last.Handler.Params["try_files"]) != "[{path} /outer.html]" {
				t.Errorf("%+v", last.Handler)
			}
		}},
		{"middleware directives", `:8080 {
	encode /e1 deflate
	encode /e2 {
		zstd 3
		gzip
		minimum_length 256
		match {
			header Content-Type text/*
		}
		bogus
	}
	encode /e3 {
		minimum_length -1
	}
	basicauth /b1 sha256 {
		bob x
	}
	basic_auth /b2 bcrypt Realm {
		bob $2a$14$abc
		carol a b
		dave notahash
	}
	basic_auth /b3 {
		eve notahash
	}
	forward_auth /f1 {
		to auth:9091
		uri /v/{path}
		header_up X-A 1
		trusted_proxies private_ranges
		copy_headers {
			X-User
			X-A>X-B
		}
		bogus
	}
	forward_auth /f2
	respond 200
}
`, []string{"encode deflate: skipped: fit compresses with", "encode: skipped: none of its encodings converted", "encode zstd 3: the level is not converted",
			"minimum_length: skipped: minimum_length takes a number", "match: skipped: fit compresses what compresses well", "bogus: skipped: not converted",
			"basicauth: skipped: fit checks bcrypt hashes", "carol: skipped: a user takes a name", "dave: skipped: not a bcrypt hash",
			"basic_auth: skipped: no user converted", "uri: skipped: a URI with placeholders", "header_up: skipped: fit sends the request's fields",
			"trusted_proxies: skipped: not converted", "copy_headers X-A>X-B: skipped: renaming", "forward_auth: skipped: no upstream address"},
			func(t *testing.T, out outConfig) {
				if b := js(routeAt(t, out, "/e2").Middleware[0].Params); b != `{"encodings":["zstd","gzip"],"min_bytes":256}` {
					t.Errorf("/e2: %s", b)
				}
				if b := js(routeAt(t, out, "/b2").Middleware[0].Params); b != `{"realm":"Realm","users":[{"password_hash":"$2a$14$abc","username":"bob"}]}` {
					t.Errorf("/b2: %s", b)
				}
				if b := js(routeAt(t, out, "/f1").Middleware[0].Params); b != `{"copy_headers":["X-User"],"upstream":"proxy_auth_9091"}` {
					t.Errorf("/f1: %s", b)
				}
			}},
		{"handlers", `:8080 {
	root /f2* /srv
	error /e1 "boom" 404
	error /e2
	abort /a
	respond /r1 "body"
	respond /r2 "body" x
	respond /r3 a b c
	respond /r4 999
	respond /r5 200 {
		body hi
		close
		bogus
	}
	file_server /f1 browse extra {
		root a b
		browse tmpl.html
		index index.htm
		precompressed br deflate
		hide .git
		bogus
	}
	file_server /f2 {
		precompressed
	}
	file_server /f3 {
		root /f3
	}
	php_fastcgi /p1
	php_fastcgi /p2 app a2 {
		root /app
		split .php
		index off
		try_files {path}
		env A 1
		env B 2
		dial_timeout 3s
		read_timeout 1m
		resolve_root_symlink
		bogus
	}
	php_fastcgi /p3 app:9001 {
		root /app
		index main.php
	}
	php_fastcgi /p4 unix//run/php.sock
	respond 200
}
`, []string{"error: answered 404 with no body", "error: answered 500 with no body", "abort: skipped: fit answers every request",
			"respond: skipped: respond takes a body and a status", "respond: skipped: a status out of range", "close: skipped: fit keeps the connection",
			"file_server extra: skipped: not converted", "root: skipped: root takes a path", "browse: its template and options are not converted",
			"precompressed deflate: skipped", "hide: skipped: fit hides no files", "php_fastcgi: skipped: php_fastcgi needs the application's address",
			"php_fastcgi: fit sends requests to the first address, app", "resolve_root_symlink: skipped: not converted",
			"php_fastcgi: skipped: it needs a root", "file_server with no root"},
			func(t *testing.T, out outConfig) {
				for path, want := range map[string]string{
					"/e1": `{"module":"respond","params":{"status":404}}`,
					"/r1": `{"module":"respond","params":{"body":"body"}}`,
					"/r5": `{"module":"respond","params":{"body":"hi","status":200}}`,
					"/f1": `{"module":"static","params":{"browse":true,"index":["index.htm"],"precompressed":["br"],"root":"."}}`,
					"/f2": `{"module":"static","params":{"precompressed":["br","zstd","gzip"],"root":"/srv"}}`,
					"/f3": `{"module":"static","params":{"root":"/f3"}}`,
					"/p2": `{"module":"fastcgi","params":{"address":"app:9000","connect_timeout":"3s","env":{"A":"1","B":"2"},"response_idle_timeout":"1m","root":"/app","split_path":[".php"],"try_files":["{path}"]}}`,
					"/p3": `{"module":"fastcgi","params":{"address":"app:9001","index":"main.php","root":"/app","try_files":["{path}","{path}/main.php","/main.php"]}}`,
				} {
					if b := js(routeAt(t, out, path).Handler); b != want {
						t.Errorf("%s: %s, want %s", path, b, want)
					}
				}
			}},
		{"fastcgi takes try_files of its block", ":8081 {\n root * /app\n try_files {path} /index.php\n php_fastcgi /p app {\n  index off\n }\n}\n", nil,
			func(t *testing.T, out outConfig) {
				if r := routeAt(t, out, "/p"); fmt.Sprint(r.Handler.Params["try_files"]) != "[{path} /index.php]" || r.Handler.Params["root"] != "/app" {
					t.Errorf("%+v", r.Handler)
				}
			}},
		{"reverse_proxy", `:8080 {
	reverse_proxy /r1
	reverse_proxy /r2 {foo}:80 ftp://x unix//s app/path app:0 app:1-200 ::1
	reverse_proxy /r3 a:1 {
		header_up Host {host}
		header_up X-Forwarded-Proto https
		header_up X-Real-IP {remote_host}
		header_up X-Add 1
		header_up
		header_down X-Down 1
		trusted_proxies private_ranges 1.2.3.0/24
		lb_retries x
		lb_try_duration 5s
		stream_timeout 1h
		buffer_requests
		health_port 8081
		health_timeout soon
		health_status 2xx
		health_status 5xx
		health_passes 2
		health_fails x
		health_headers {
			Host h.test
			X-Other 1
		}
		unhealthy_status 500
		lb_policy
		bogus
	}
	reverse_proxy /r4 a:1 b:1 {
		header_up Host {upstream_hostport}
		lb_try_duration 5s
		stream_timeout forever
		health_status 204
		health_fails 3
		lb_policy cookie sticky secret
		transport fastcgi
	}
	reverse_proxy /r5 a:1 {
		header_up Host {upstream_hostport}
		lb_policy header X-Key
	}
	reverse_proxy /r6 a:1 {
		header_up Host {vars.h}
		lb_policy header
	}
	reverse_proxy /r7 https://a:1 {
		transport http {
			tls
			tls_server_name s.test
			tls_trusted_ca_certs /ca.pem
			tls_trust_pool file /ca2.pem
			tls_trust_pool inline x
			tls_client_auth /c.pem /k.pem
			dial_timeout soon
			response_header_timeout 5s
			read_timeout 30s
			keepalive off
			keepalive_idle_conns_per_host 4
			max_conns_per_host 50
			compression off
		}
	}
	reverse_proxy /r8 a:1 {
		transport http {
			versions h2c 2
		}
		lb_policy random_choose 2
	}
	reverse_proxy /r9 a:1 {
		lb_policy client_ip_hash
		transport http {
			versions 1.1 2
			keepalive off
		}
	}
	reverse_proxy /r10 a:1 b:1 {
		lb_policy weighted_round_robin 1 x
	}
	reverse_proxy /r11 a:1 b:1 {
		lb_policy weighted_round_robin 1
	}
	reverse_proxy /r12 a:1 {
		lb_policy first
	}
	reverse_proxy /r13 {
		to a:1
		header_up Host example.org
		lb_policy cookie
	}
	reverse_proxy /r14 a:1 {
		lb_policy bogus
	}
	reverse_proxy /r15 c:1
	reverse_proxy /r16 c:1
	respond 200
}
`, []string{"reverse_proxy: skipped: no upstream address", "reverse_proxy {foo}:80: skipped: placeholders", "reverse_proxy ftp://x: skipped: fit reaches upstreams over http://",
			"reverse_proxy unix//s: skipped: fit reaches upstreams over TCP", "reverse_proxy app/path: skipped: an upstream address has no path",
			"reverse_proxy app:0: skipped: not a port", "reverse_proxy app:1-200: skipped: not a port", "reverse_proxy: skipped: none of its addresses converted",
			"header_up X-Forwarded-Proto: skipped: fit's proxy sets", "header_up X-Real-IP: skipped", "lb_retries: skipped: not converted",
			"lb_try_duration: written as fit's retry", "buffer_requests: skipped: fit streams bodies", "health_timeout: skipped: it takes a duration",
			"health_status 5xx: skipped", "health_fails: skipped: it takes a number", "X-Other: skipped: fit's probes send a Host",
			"unhealthy_status: skipped: not converted: fit counts", "lb_policy: skipped: lb_policy names a policy", "bogus: skipped: not converted",
			"header_up Host {upstream_hostport}: skipped: fit sends one Host to every address", "stream_timeout: skipped: not converted",
			"lb_policy cookie: its secret is not converted", "transport: skipped: fit's proxy speaks HTTP", "header_up Host {vars.h}: skipped: not converted",
			"lb_policy: skipped: lb_policy header takes a field", "tls_trust_pool: skipped: fit takes one CA file", "dial_timeout: skipped: it takes a duration",
			"compression: skipped: not converted", "versions 2: fit speaks HTTP/2 to TLS upstreams", "lb_policy: skipped: weights are numbers from 1",
			"lb_policy: skipped: it needs a weight for each address", "lb_policy first: written as round_robin", "lb_policy: skipped: fit balances by"},
			func(t *testing.T, out outConfig) {
				r3 := routeAt(t, out, "/r3")
				if b := js(r3.Handler.Params); b != `{"retry":{},"trusted_proxies":["192.168.0.0/16","172.16.0.0/12","10.0.0.0/8","127.0.0.1/8","fd00::/8","::1","1.2.3.0/24"],"tunnel_idle_timeout":"1h","upstream":"proxy_a_1"}` {
					t.Errorf("/r3: %s", b)
				}
				if b := js(r3.Middleware[0].Params); b != `{"request":{"set":{"X-Add":"1"}},"response":{"set":{"X-Down":"1"}}}` {
					t.Errorf("/r3: %s", b)
				}
				if h := upstreamNamed(t, out, "proxy_a_1").HealthCheck; h.Path != "/" || h.Port != 8081 || h.Passes != 2 || h.Host != "h.test" || h.ExpectStatus != nil {
					t.Errorf("%+v", h)
				}
				if r := routeAt(t, out, "/r5"); r.Handler.Params["host_header"] != "a:1" {
					t.Errorf("/r5: %+v", r.Handler)
				}
				byRoute := func(path string) *outUpstream {
					return upstreamNamed(t, out, routeAt(t, out, path).Handler.Params["upstream"].(string))
				}
				if u := byRoute("/r4"); u.StickyCookie.Name != "sticky" || u.HealthCheck.ExpectStatus[0] != 204 {
					t.Errorf("/r4: %+v", u)
				}
				if u := byRoute("/r5"); u.LoadBalancing != "header_hash" || u.HashKey != "X-Key" {
					t.Errorf("/r5: %+v", u)
				}
				if u := byRoute("/r7"); u.Protocol != "h2" || u.TLSServerName != "s.test" || u.TLSCAFile != "/ca2.pem" || u.TLSCertFile != "/c.pem" || u.TLSKeyFile != "/k.pem" ||
					u.ResponseHeaderTimeout != "5s" || u.ResponseIdleTimeout != "30s" || *u.MaxIdleConns != 4 || u.MaxConns != 50 || u.ConnectTimeout != "" {
					t.Errorf("/r7: %+v", u)
				}
				if u := byRoute("/r8"); u.Protocol != "h2c" || u.LoadBalancing != "p2c" {
					t.Errorf("/r8: %+v", u)
				}
				if u := byRoute("/r9"); u.LoadBalancing != "ip_hash" || *u.MaxIdleConns != 0 {
					t.Errorf("/r9: %+v", u)
				}
				if r := routeAt(t, out, "/r13"); r.Handler.Params["host_header"] != "example.org" {
					t.Errorf("/r13: %+v", r.Handler)
				}
				if u := byRoute("/r4"); u.HealthCheck.Fails != 3 {
					t.Errorf("/r4: %+v", u.HealthCheck)
				}
				if u := byRoute("/r13"); u.StickyCookie.Name != "lb" {
					t.Errorf("/r13: %+v", u)
				}
				if a, b := routeAt(t, out, "/r15").Handler.Params["upstream"], routeAt(t, out, "/r16").Handler.Params["upstream"]; a != "proxy_c_1" || b != a {
					t.Errorf("%v %v", a, b)
				}
			}},
		{"matchers", `:8080 {
	@empty
	@cel2 expression {path} == "/"
	@p1 path
	@p2 path /a%20
	@p3 path abc
	@pr path_regexp name ^/x
	@pr1 path_regexp ^/y
	@pr3 path_regexp a b c
	@prbad path_regexp (
	@h host
	@hbad host a*b.test
	@hok host *.Example.com x.test
	@m method
	@hd header
	@hdneg header !X-No
	@hdbad header X-A a*
	@hdmulti {
		header X-A 1
		header X-A 2
	}
	@q query
	@qbad query =x
	@qok query a=* b c=1 c=2
	@ip remote_ip forwarded 1.2.3.4
	@ip2 client_ip
	@ip3 client_ip 10.0.0.0/8
	@not1 not
	@not2 not path abc
	@not3 {
		not {
		}
	}
	@notblock {
		not {
			method GET
			path /a /b*
		}
	}
	@notline not {
		method GET
	}
	@file file
	@paths path /exact /pre* /mid*dle
	@multi {
		path_regexp ^/a
		path /x*y
		not method GET
	}
	respond @p1 200
	respond @pr 201
	respond @pr1 202
	respond @hok 203
	respond @hdneg 204
	respond @hdmulti 205
	respond @qok 206
	respond @ip3 207
	respond @notblock 208
	respond @notline 211
	respond @paths 209
	respond @multi 210
}
`, []string{"@empty: it has no matchers", "@cel2: CEL expressions are not converted", "@p1: path with no paths", "@p2: a path with escapes",
			"@p3: path abc starts with neither", "@pr3: path_regexp takes a name", "@prbad: Go's regular expressions", "@h: host with no names",
			"@hbad: host a*b.test is not converted", "@m: method with no methods", "@hd: header with no field", "@hdbad: a header value with wildcards",
			"@q: query with no parameters", "@qbad: query =x is not converted", "@ip: remote_ip forwarded", "@ip2: client_ip with no ranges",
			"@not1: not with no matcher", "@not2: path abc starts with neither", "@not3: not with no matcher", "@file: the file matcher is not converted"},
			func(t *testing.T, out outConfig) {
				want := []string{
					`{"path_regexp":["^/x"]}`,
					`{"path_regexp":["^/y"]}`,
					`{"host":["*.example.com","x.test"]}`,
					`{"expr":"!Header(\"X-No\")"}`,
					`{"header":{"X-A":["1","2"]}}`,
					`{"query":{"a":[],"b":[],"c":["1","2"]}}`,
					`{"remote_ip":["10.0.0.0/8"]}`,
					`{"expr":"(!((Path(\"/a\") || PathPrefix(\"/b\")) && Method(\"GET\")))"}`,
					`{"expr":"!(Method(\"GET\"))"}`,
					`{"path_regexp":["^/exact$","^/pre.*$","^/mid.*dle$"]}`,
					`{"path_regexp":["^/a"],"expr":"PathRegexp(\"^/x.*y$\") && !(Method(\"GET\"))"}`,
				}
				if len(out.Routes) != len(want)+1 {
					t.Fatalf("%d routes", len(out.Routes))
				}
				for i, w := range want {
					if b := js(out.Routes[i].Match); b != w {
						t.Errorf("%d: %s, want %s", i, b, w)
					}
				}
			}},
		{"scopes", `:8080 {
	handle /in/* {
		tls internal
		bind 127.0.0.1
		log
		respond /out 200
		metrics
	}
	handle /h extra {
		respond 200
	}
	handle_path {
		respond 200
	}
	handle_errors {
		respond 500
	}
	"import" x
	encode_gzip
	gzip
	abort /ab
	handle /empty {
		respond
	}
	header /hx X 1
}
`, []string{"tls: skipped: it belongs in the site's block", "bind: skipped: it belongs", "log: skipped: it belongs",
			"respond: skipped: its matcher matches nothing", "metrics: fit serves them on the Admin API", "handle: its arguments are not converted",
			"handle_path: skipped: handle_path takes one path", "handle_errors: skipped: error pages", "import: skipped: not converted",
			"encode_gzip: skipped: write encode", "gzip: skipped: write encode", "abort: skipped"}, func(t *testing.T, out outConfig) {
			if out.Metrics == nil {
				t.Error("no metrics")
			}
			if r := routeAt(t, out, "/empty"); r.Handler.Module != "respond" || r.Handler.Params != nil {
				t.Errorf("%+v", r)
			}
			if r := routeAt(t, out, "/hx"); r.Handler.Module != "respond" || r.Handler.Params != nil || r.Middleware[0].Module != "headers" {
				t.Errorf("%+v", r)
			}
		}},
		{"snippet arguments and quoted lines", "(s) {\n respond {args[3]}ok\n}\n:80 {\n import s a\n header /q X \"a\nb\"\n}\n", nil, func(t *testing.T, out outConfig) {
			if out.Routes[len(out.Routes)-1].Handler.Params["body"] != "ok" {
				t.Errorf("%+v", out.Routes)
			}
			if b := js(routeAt(t, out, "/q").Middleware[0].Params); b != `{"response":{"set":{"X":"a\nb"}}}` {
				t.Errorf("%s", b)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, ws := adapt(t, tt.src)
			all := strings.Join(ws, "\n")
			for _, w := range tt.warns {
				if !strings.Contains(all, w) {
					t.Errorf("no warning %q in:\n%s", w, all)
				}
			}
			if tt.check != nil {
				tt.check(t, out)
			}
		})
	}
}

// TestConvertErrors converts Caddyfiles that do not convert.
func TestConvertErrors(t *testing.T) {
	deepSnippet := "(deep) {\n" + strings.Repeat("a {\n", 60) + strings.Repeat("}\n", 60) + "}\n" +
		strings.Repeat("b {\n", 5) + "import deep\n" + strings.Repeat("}\n", 5)
	for _, tt := range []struct {
		name, src, err string
	}{
		{"too many tokens", strings.Repeat("a ", maxTokens+1), "more than 200000 tokens"},
		{"snippet twice", "(s) {\n}\n(s) {\n}\n:80 {\n}\n", "snippet s is defined twice"},
		{"nested too deep", strings.Repeat("a {\n", maxNesting+1) + strings.Repeat("}\n", maxNesting+1), "nested too deep"},
		{"nested too deep by an import", deepSnippet, "blocks nested too deep"},
		{"import of nothing", ":80 {\n import\n}\n", "import names no snippet"},
		{"too many imports", "(s) {\n header X 1\n}\n:80 {\n" + strings.Repeat(" import s\n", maxImports+1) + "}\n", "more than 1000 imports"},
		{"too many tokens imported", "(s) {\n" + strings.Repeat("x ", 1000) + "\n}\n:80 {\n" + strings.Repeat(" import s\n", 201) + "}\n", "once snippets are imported"},
		{"a block in the global options", "{\n {\n }\n}\n", "a block with no directive before it"},
		{"a block in a site", "a {\n {\n }\n}\n", "a block with no directive before it"},
		{"a block in a directive", ":80 {\n route {\n  {\n  }\n }\n}\n", "a block with no directive before it"},
		// A brace given as a snippet's argument opens a block that does not end.
		{"a brace by a snippet's argument", "(s) {\n route {args[0]}\n}\n:80 {\n import s \"{\"\n}\n", "a block that does not end"},
		{"no site converted", ":80 {\n php_fastcgi :9000\n}\n", "none of the Caddyfile's sites converted"},
		{"no site", "ftp://a.test {\n respond 200\n}\n", "no site to convert"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Convert([]byte(tt.src)); err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("%v, want %q", err, tt.err)
			}
		})
	}
}

// TestTooManyRoutes checks the bound on the routes a site makes.
func TestTooManyRoutes(t *testing.T) {
	var b strings.Builder
	b.WriteString(":80 {\n")
	for i := range maxSiteRoutes + 1 {
		fmt.Fprintf(&b, " respond /p%d 200\n", i)
	}
	b.WriteString("}\n:81 {\n respond 200\n}\n")
	out, ws := adapt(t, b.String())
	if len(out.Routes) != 1 || !strings.Contains(strings.Join(ws, "\n"), "it makes more than 2000 routes") {
		t.Errorf("%d routes, %v", len(out.Routes), ws)
	}

	b.Reset()
	b.WriteString(":80 {\n")
	for i := range maxSiteRoutes - 1 {
		fmt.Fprintf(&b, " respond /p%d 200\n", i)
	}
	b.WriteString(" header /zz* X 1\n}\n")
	out, ws = adapt(t, b.String())
	if len(out.Routes) != maxSiteRoutes || !strings.Contains(strings.Join(ws, "\n"), "header: not applied: the site makes too many routes") {
		t.Errorf("%d routes, %v", len(out.Routes), ws)
	}
}

// TestPosition checks where the result says a config path comes from.
func TestPosition(t *testing.T) {
	r, err := Convert([]byte(":80 {\n log\n respond 200\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"listeners[0]": 1, "routes[0]": 3, "routes[0].handler": 3, "access_log": 2, "upstreams[0]": 0} {
		line, _, ok := r.Position(path)
		if ok != (want > 0) || line != want {
			t.Errorf("%s: %d %v, want %d", path, line, ok, want)
		}
	}
}

func TestRouteName(t *testing.T) {
	c := &converter{names: map[string]bool{}}
	long := strings.Repeat("a", 70)
	for _, tt := range [][3]string{
		{"-", "", "route"},
		{"-", "", "route-2"},
		{long, "", long[:60]},
		{"a.test", "/x y/*", "a.test-x_y"},
	} {
		if got := c.routeName(tt[0], tt[1]); got != tt[2] {
			t.Errorf("%q %q: %q, want %q", tt[0], tt[1], got, tt[2])
		}
	}
}

func TestMatchHelpers(t *testing.T) {
	for _, tt := range []struct {
		a, b *outMatch
		want string
		ok   bool
	}{
		{nil, &outMatch{}, `{}`, true},
		{&outMatch{}, &outMatch{Host: []string{"x.test"}}, `{"host":["x.test"]}`, true},
		{&outMatch{Host: []string{"a.test", "b.test"}}, &outMatch{Host: []string{"B.test"}}, `{"host":["b.test"]}`, true},
		{&outMatch{Host: []string{"a.test"}}, &outMatch{Host: []string{"c.test"}}, "", false},
		{&outMatch{Path: []string{"/a*"}}, &outMatch{Path: []string{"/a/b*"}}, `{"path":["/a/b*"]}`, true},
		{&outMatch{PathRegexp: []string{"x"}}, &outMatch{PathRegexp: []string{"x"}}, `{"path_regexp":["x"]}`, true},
		{&outMatch{Method: []string{"GET", "POST"}}, &outMatch{Method: []string{"POST"}}, `{"method":["POST"]}`, true},
		{&outMatch{Header: map[string][]string{"X": {"1"}}}, &outMatch{Header: map[string][]string{"x": {"2"}}}, "", false},
		{&outMatch{Header: map[string][]string{"X": {"1", "2"}}}, &outMatch{Header: map[string][]string{"X": {"2"}}}, `{"header":{"X":["2"]}}`, true},
		{&outMatch{Header: map[string][]string{"X": {"1"}}}, &outMatch{Header: map[string][]string{"X": {}}}, `{"header":{"X":["1"]}}`, true},
		{&outMatch{Query: map[string][]string{"q": {"1"}}}, &outMatch{Query: map[string][]string{"q": {"2"}}}, "", false},
		{&outMatch{}, &outMatch{RemoteIP: []string{"1.0.0.0/8"}}, `{"remote_ip":["1.0.0.0/8"]}`, true},
		{&outMatch{RemoteIP: []string{"1.0.0.0/8"}}, &outMatch{RemoteIP: []string{"2.0.0.0/8"}}, `{"remote_ip":["1.0.0.0/8"],"expr":"RemoteIP(\"2.0.0.0/8\")"}`, true},
		{&outMatch{Expr: `Method("GET") || Method("PUT")`}, &outMatch{Expr: "X"}, `{"expr":"(Method(\"GET\") || Method(\"PUT\")) && X"}`, true},
	} {
		got, ok := intersect(tt.a, tt.b)
		if ok != tt.ok {
			t.Errorf("%+v ∩ %+v: %v", tt.a, tt.b, ok)
			continue
		}
		if ok {
			if b := js(got); b != tt.want {
				t.Errorf("%+v ∩ %+v: %s, want %s", tt.a, tt.b, b, tt.want)
			}
		}
	}

	for _, tt := range []struct {
		m    *outMatch
		want string
	}{
		{nil, ""},
		{&outMatch{Path: []string{"/x"}}, `Path("/x")`},
		{&outMatch{Path: []string{"/y*"}}, `PathPrefix("/y")`},
		{&outMatch{Host: []string{"a"}, Path: []string{"/x", "/y*"}, PathRegexp: []string{"r"}, Method: []string{"GET"},
			Header: map[string][]string{"H": {"1"}}, Query: map[string][]string{"q": {}}, RemoteIP: []string{"1.2.3.4"}, Expr: "E"},
			`Host("a") && (Path("/x") || PathPrefix("/y")) && PathRegexp("r") && Method("GET") && Header("H", "1") && Query("q") && RemoteIP("1.2.3.4") && E`},
	} {
		if got := expr(tt.m); got != tt.want {
			t.Errorf("%+v: %s, want %s", tt.m, got, tt.want)
		}
	}

	if sameSet([]string{"a", "b"}, []string{"a", "c"}) || sameMatch(&outMatch{}, &outMatch{Path: []string{"/"}}) {
		t.Error("sameSet or sameMatch")
	}
	if _, ok := size("99999999999T"); ok {
		t.Error("a size too large")
	}
	if _, ok := size("ten"); ok {
		t.Error("not a size")
	}
}
