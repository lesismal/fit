package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const validConfig = `{
  "listeners": [
    {"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]},
    {"name": "admin", "address": "127.0.0.1:0"}
  ],
  "routes": [
    {
      "name": "hello",
      "listeners": ["web"],
      "priority": 10,
      "match": {"host": ["example.com", "*.example.com"], "path": ["/hello", "/api/*"]},
      "handler": {"module": "respond", "params": {"status": 200, "body": "hi"}}
    },
    {"handler": {"module": "respond"}}
  ]
}`

func TestLoadValid(t *testing.T) {
	c, err := Load([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Listeners) != 2 || c.Listeners[0].Protocols[1] != ProtocolH2C {
		t.Fatalf("listeners: %+v", c.Listeners)
	}
	r := c.Routes[0]
	if r.Name != "hello" || r.Priority != 10 || r.Match.Path[1] != "/api/*" || r.Handler.Module != "respond" {
		t.Fatalf("route: %+v", r)
	}
	if got := r.Handler.Params.Path(); got != "routes[0].handler.params" {
		t.Fatalf("params path = %q", got)
	}
	if line, col := r.Handler.Params.Pos(); line != 12 || col != 50 {
		t.Fatalf("params position = %d:%d", line, col)
	}
	absent := c.Routes[1].Handler.Params
	if !absent.IsZero() || absent.Path() != "routes[1].handler.params" {
		t.Fatalf("absent params: zero=%v path=%q", absent.IsZero(), absent.Path())
	}
	if line, _ := absent.Pos(); line != 14 {
		t.Fatalf("absent params line = %d, want the handler's", line)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string // each error's text, in order
	}{
		{
			name: "unknown field",
			doc:  `{"listeners": [{"name": "a", "address": ":1", "port": 2}]}`,
			want: []string{`1:47: listeners[0].port: unknown field`},
		},
		{
			name: "field in the wrong case",
			doc:  `{"Listeners": []}`,
			want: []string{
				`1:2: Listeners: unknown field (did you mean "listeners"?)`,
			},
		},
		{
			name: "wrong type",
			doc:  "{\n  \"listeners\": [{\"name\": 1, \"address\": \":1\"}]\n}",
			want: []string{`2:26: listeners[0].name: expected a string, got a number`},
		},
		{
			name: "missing fields",
			doc:  `{"listeners": [{"name": "a"}], "routes": [{}]}`,
			want: []string{
				`1:16: listeners[0].address: missing required field`,
				`1:43: routes[0].handler: missing required field`,
			},
		},
		{
			name: "integer expected",
			doc:  `{"listeners": [{"name": "a", "address": ":1"}], "routes": [{"priority": 1.5, "handler": {"module": "m"}}]}`,
			want: []string{`1:73: routes[0].priority: 1.5 is not an integer in range for int`},
		},
		{
			name: "enum",
			doc:  `{"listeners": [{"name": "a", "address": ":1", "protocols": ["http/1.1", "spdy"]}]}`,
			want: []string{`1:73: listeners[0].protocols[1]: "spdy" is not one of "http/1.1", "h2c", "h2", "h3", "tcp", "udp", "socks"`},
		},
		{
			name: "null for a list",
			doc:  `{"listeners": null}`,
			want: []string{`1:15: listeners: expected an array, got null`},
		},
		{
			name: "duplicate key",
			doc:  `{"listeners": [], "listeners": []}`,
			want: []string{`1:19: duplicate key "listeners"`},
		},
		{
			name: "syntax error",
			doc:  "{\n  \"listeners\": [}\n}",
			want: []string{`2:17: invalid JSON: invalid character '}' looking for beginning of value`},
		},
		{
			name: "unexpected end",
			doc:  `{"listeners": [`,
			want: []string{`1:16: invalid JSON: unexpected end of input`},
		},
		{
			name: "empty document",
			doc:  ``,
			want: []string{`1:1: invalid JSON: unexpected end of input`},
		},
		{
			name: "trailing data",
			doc:  `{"listeners": []} {}`,
			want: []string{`1:19: unexpected data after the top-level value`},
		},
		{
			name: "not an object",
			doc:  `[]`,
			want: []string{`1:1: expected an object, got an array`},
		},
		{
			name: "too deep",
			doc:  strings.Repeat("[", maxDepth+2),
			want: []string{`1:258: nested more than 256 levels deep`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErrors(t, tt.doc, tt.want)
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{
			name: "no listeners",
			doc:  `{"listeners": []}`,
			want: []string{`listeners: at least one listener is needed, or a route that matches a host name for HTTPS by default`},
		},
		{
			name: "no listeners, and no host names for HTTPS by default",
			doc:  `{"routes": [{"match": {"host": ["localhost", "*.a.example", "10.0.0.1", "intranet"]}, "handler": {"module": "respond"}}]}`,
			want: []string{`listeners: at least one listener is needed, or a route that matches a host name for HTTPS by default`},
		},
		{
			name: "no listeners, HTTPS by default off",
			doc:  `{"auto_https": {"disable": true}, "routes": [{"match": {"host": ["a.example"]}, "handler": {"module": "respond"}}]}`,
			want: []string{`listeners: at least one listener is needed`},
		},
		{
			name: "HTTPS by default on one port",
			doc:  `{"auto_https": {"http_port": 8443, "https_port": 8443}, "routes": [{"match": {"host": ["a.example"]}, "handler": {"module": "respond"}}]}`,
			want: []string{`1:30: auto_https.http_port: the HTTP port is the HTTPS port, 8443`},
		},
		{
			name: "empty tls without host names",
			doc:  `{"listeners": [{"name": "web", "address": ":443", "tls": {}}], "routes": [{"match": {"host": ["localhost"]}, "handler": {"module": "respond"}}]}`,
			want: []string{`1:58: listeners[0].tls: a certificate, a name for acme or on_demand is needed, or a route on this listener that matches a host name for HTTPS by default`},
		},
		{
			name: "a route on a listener HTTPS by default does not make",
			doc:  `{"routes": [{"listeners": ["web"], "match": {"host": ["a.example"]}, "handler": {"module": "respond"}}]}`,
			want: []string{`1:28: routes[0].listeners[0]: there is no listener named "web"`},
		},
		{
			name: "listener names and addresses",
			doc: `{"listeners": [
  {"name": "a", "address": ":80"},
  {"name": "a", "address": ":80"},
  {"name": "-b", "address": "localhost"},
  {"name": "c", "address": ":http"},
  {"name": "d", "address": ":0"},
  {"name": "e", "address": ":0", "protocols": ["h2c", "h2c"]}
]}`,
			want: []string{
				`3:12: listeners[1].name: another listener is already named "a"`,
				`3:28: listeners[1].address: listener "a" already listens on :80`,
				`4:12: listeners[2].name: "-b" is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit`,
				`4:29: listeners[2].address: "localhost" is not a host and port, as in ":8080" or "127.0.0.1:8080"`,
				`5:28: listeners[3].address: "http" is not a port between 0 and 65535`,
				`7:55: listeners[5].protocols[1]: "h2c" is listed twice`,
			},
		},
		{
			name: "routes",
			doc: `{"listeners": [{"name": "web", "address": ":80"}], "routes": [
  {"name": "r", "listeners": ["web", "api"], "handler": {"module": "m"}},
  {"name": "r", "match": {"host": ["", "a..b", "*.1.2.3.4", "[::1]", "*.ok.com", "**.ok.org", "**."], "path": ["x", "/a*b", "/ok/*"]}, "handler": {"module": ""}}
]}`,
			want: []string{
				`2:38: routes[0].listeners[1]: there is no listener named "api"`,
				`3:12: routes[1].name: another route is already named "r"`,
				`3:36: routes[1].match.host[0]: the host is empty`,
				`3:40: routes[1].match.host[1]: "a..b" is not a valid host name`,
				`3:48: routes[1].match.host[2]: "*.1.2.3.4" puts a wildcard on an IP address`,
				`3:95: routes[1].match.host[6]: the host is empty`,
				`3:112: routes[1].match.path[0]: "x" does not start with '/'`,
				`3:117: routes[1].match.path[1]: "/a*b" has a '*' other than at its end`,
				`3:158: routes[1].handler.module: the module name is empty`,
			},
		},
		{
			name: "route matchers",
			doc: `{"listeners": [{"name": "web", "address": ":80"}], "routes": [
  {"match": {"method": ["GET", "BAD METHOD", ""], "header": {"X-Ok": [], "Bad Name": ["v"]}, "query": {"": ["1"], "ok": []}}, "handler": {"module": "respond"}},
  {"match": {"remote_ip": ["10.0.0.0/8", "10.0.0.0/33", "a.example", "::1"], "path_regexp": ["^/ok$", "("]}, "handler": {"module": "respond"}},
  {"match": {"expr": "Host(\"a.example\") && Nope(\"x\")"}, "handler": {"module": "respond"}}
]}`,
			want: []string{
				`2:32: routes[0].match.method[1]: "BAD METHOD" is not a valid method`,
				`2:46: routes[0].match.method[2]: "" is not a valid method`,
				`2:86: routes[0].match.header.Bad Name: "Bad Name" is not a valid header name`,
				`2:108: routes[0].match.query.: the query parameter's name is empty`,
				"3:103: routes[1].match.path_regexp[1]: \"(\" is not a valid regular expression: error parsing regexp: missing closing ): `(`",
				`3:42: routes[1].match.remote_ip[1]: "10.0.0.0/33" is not an IP address or a CIDR prefix`,
				`3:57: routes[1].match.remote_ip[2]: "a.example" is not an IP address or a CIDR prefix`,
				`4:22: routes[2].match.expr: at byte 22: there is no matcher Nope`,
			},
		},
		{
			name: "upstreams",
			doc: `{"listeners": [{"name": "web", "address": ":80"}], "upstreams": [
  {"name": "u", "addresses": ["a:1", "a:1"]},
  {"name": "u", "addresses": []},
  {"name": "-x", "addresses": ["http://a:80", "a", ":80", "a:0", "a:x", "a:70000", "[::1]:8080"], "max_conns": 2, "idle_timeout": "1s"}
]}`,
			want: []string{
				`2:38: upstreams[0].addresses[1]: "a:1" is listed twice`,
				`3:12: upstreams[1].name: another upstream is already named "u"`,
				`3:30: upstreams[1].addresses: an upstream needs at least one address`,
				`4:12: upstreams[2].name: "-x" is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit`,
				`4:32: upstreams[2].addresses[0]: http://a:80 is a URL; write the host and port alone, as in "10.0.0.1:8080"`,
				`4:47: upstreams[2].addresses[1]: "a" is not a host and port, as in "10.0.0.1:8080"`,
				`4:52: upstreams[2].addresses[2]: ":80" has no host`,
				`4:59: upstreams[2].addresses[3]: "a:0" has no valid port`,
				`4:66: upstreams[2].addresses[4]: "a:x" has no valid port`,
				`4:73: upstreams[2].addresses[5]: "a:70000" has no valid port`,
			},
		},
		{
			name: "upstream weights",
			doc: `{"listeners": [{"name": "web", "address": ":80"}], "upstreams": [
  {"name": "u", "addresses": ["a:1", "b:1"], "weights": [1]},
  {"name": "v", "addresses": ["a:1"], "weights": [2], "load_balancing": "round_robin"}
]}`,
			want: []string{
				`2:57: upstreams[0].weights: 1 weights for 2 addresses; give one weight for each address`,
				`3:73: upstreams[1].load_balancing: round_robin takes no weights; use weighted_round_robin or least_conn`,
			},
		},
		{
			name: "upstream tls without h2",
			doc: `{"listeners": [{"name": "web", "address": ":80"}], "upstreams": [
  {"name": "u", "addresses": ["a:1"], "tls_server_name": "x", "tls_ca_file": "ca.pem"},
  {"name": "v", "addresses": ["a:1"], "tls_cert_file": "c.pem", "tls_key_file": "k.pem"},
  {"name": "w", "addresses": ["a:1"], "protocol": "h2", "tls_cert_file": "c.pem"},
  {"name": "x", "addresses": ["a:1"], "protocol": "h2", "tls_key_file": "k.pem"}
]}`,
			want: []string{
				`2:58: upstreams[0].tls_server_name: tls_server_name needs protocol h2`,
				`2:78: upstreams[0].tls_ca_file: tls_ca_file needs protocol h2`,
				`3:56: upstreams[1].tls_cert_file: tls_cert_file needs protocol h2`,
				`4:74: upstreams[2].tls_cert_file: tls_cert_file needs tls_key_file`,
				`5:73: upstreams[3].tls_key_file: tls_key_file needs tls_cert_file`,
			},
		},
		{
			name: "tcp listeners and services",
			doc: `{"listeners": [
  {"name": "raw", "address": ":5432", "protocols": ["tcp"]},
  {"name": "mixed", "address": ":5433", "protocols": ["http/1.1", "tcp"]},
  {"name": "lonely", "address": ":5434", "protocols": ["tcp"]},
  {"name": "secure", "address": ":5435", "protocols": ["tcp"], "tls": {"certificates": [{"cert_file": "c", "key_file": "k"}]}},
  {"name": "web", "address": ":80"}
], "upstreams": [{"name": "db", "addresses": ["a:1"]}],
"routes": [{"listeners": ["raw"], "handler": {"module": "respond"}}],
"services": [
  {"name": "pg", "listeners": ["raw", "web", "nope", "secure", "mixed"], "upstream": "db"},
  {"name": "pg", "listeners": ["raw"], "upstream": "none"},
  {"name": "x", "listeners": [], "upstream": "db"}
]}`,
			want: []string{
				`5:71: listeners[3].tls: tls serves the listener's HTTP, and this one serves none: tcp and udp are proxied as they are, TLS and all`,
				`8:27: routes[0].listeners[0]: "raw" is a tcp listener, which a service serves, not routes: it serves no HTTP`,
				`10:39: services[0].listeners[1]: "web" is not a tcp or udp listener: a service takes the connections of listeners whose protocols include tcp, or are ["udp"]`,
				`10:46: services[0].listeners[2]: there is no listener named "nope"`,
				`11:12: services[1].name: another service is already named "pg"`,
				`11:32: services[1].listeners[0]: listener "raw" is already service "pg"'s: services that share a tcp listener take the connections of their server_names, and only one of them may have none`,
				`11:52: services[1].upstream: there is no upstream named "none"`,
				`12:30: services[2].listeners: a service needs at least one tcp or udp listener`,
				`4:55: listeners[2].protocols: no service takes this tcp listener's connections: list it in a service's listeners`,
			},
		},
		{
			name: "listeners of many protocols, and socks",
			doc: `{"listeners": [
  {"name": "one", "address": ":443", "protocols": ["http/1.1", "h2", "tcp", "socks"], "tls": {"certificates": [{"cert_file": "c", "key_file": "k"}]}, "socks": {"clients": ["10.0.0.0/8"]}},
  {"name": "nosocks", "address": ":1080", "protocols": ["socks"]},
  {"name": "extra", "address": ":1081", "socks": {"clients": [], "allow": ["x..y", "*.ok.example", "10.1.0.0/16", "nope/8"], "deny": ["1.2.3.4"], "ports": [0, 443, 70000]}},
  {"name": "s", "address": ":1082", "protocols": ["socks"], "socks": {"clients": ["bad"]}}
], "upstreams": [{"name": "a", "addresses": ["a:1"]}],
"services": [{"name": "pass", "listeners": ["one"], "upstream": "a", "server_names": ["a.example.com"]}],
"routes": [{"listeners": ["one", "s"], "handler": {"module": "respond"}}]}`,
			want: []string{
				`3:57: listeners[1].protocols[0]: socks needs the listener's socks, which says who may use it`,
				`4:50: listeners[2].socks: socks is for a listener whose protocols include socks`,
				`4:62: listeners[2].socks.clients: at least one address is needed`,
				`4:76: listeners[2].socks.allow[0]: "x..y" is not a host name, an IP address or a CIDR range`,
				`4:115: listeners[2].socks.allow[3]: "nope/8" is not a host name, an IP address or a CIDR range`,
				`4:157: listeners[2].socks.ports[0]: 0 is not a port`,
				`4:165: listeners[2].socks.ports[2]: 70000 is not a port`,
				`5:83: listeners[3].socks.clients[0]: "bad" is not an IP address or CIDR range`,
				`8:34: routes[0].listeners[1]: "s" is a socks listener, not routes: it serves no HTTP`,
			},
		},
		{
			name: "udp listeners",
			doc: `{"listeners": [
  {"name": "dns", "address": ":53", "protocols": ["udp"]},
  {"name": "dnstcp", "address": ":53", "protocols": ["tcp"]},
  {"name": "dns2", "address": ":53", "protocols": ["udp"]},
  {"name": "mixed", "address": ":54", "protocols": ["udp", "tcp"]},
  {"name": "secure", "address": ":55", "protocols": ["udp"], "tls": {"certificates": [{"cert_file": "c", "key_file": "k"}]}},
  {"name": "web", "address": ":443", "protocols": ["h3"], "tls": {"certificates": [{"cert_file": "c", "key_file": "k"}]}},
  {"name": "quic", "address": ":443", "protocols": ["udp"]}
], "upstreams": [{"name": "ns", "addresses": ["a:53"]}],
"services": [{"name": "dns", "listeners": ["dns", "dnstcp", "dns2", "mixed", "secure", "quic"], "upstream": "ns"}]}`,
			want: []string{
				`4:31: listeners[2].address: listener "dns" already listens on :53`,
				`5:53: listeners[3].protocols[0]: a udp listener serves UDP alone: list udp without other protocols`,
				`6:69: listeners[4].tls: tls serves the listener's HTTP, and this one serves none: tcp and udp are proxied as they are, TLS and all`,
				`8:31: listeners[6].address: listener "web" already listens on :443`,
			},
		},
		{
			name: "server names and the PROXY protocol",
			doc: `{"listeners": [
  {"name": "tls", "address": ":443", "protocols": ["tcp"], "proxy_protocol": {"trusted": ["10.0.0.0/8", "nope"]}},
  {"name": "dns", "address": ":53", "protocols": ["udp"], "proxy_protocol": {"trusted": []}},
  {"name": "web", "address": ":80", "proxy_protocol": {"trusted": ["127.0.0.1"]}}
], "upstreams": [{"name": "a", "addresses": ["a:1"]}],
"services": [
  {"name": "s1", "listeners": ["tls"], "upstream": "a", "server_names": ["a.example.com", "*.b.example.com"]},
  {"name": "s2", "listeners": ["tls"], "upstream": "a", "server_names": ["A.example.com", "1.2.3.4", "x..y", "c.example.com", "c.example.com"]},
  {"name": "s3", "listeners": ["tls"], "upstream": "a"},
  {"name": "s4", "listeners": ["tls"], "upstream": "a", "proxy_protocol": "v1"},
  {"name": "s5", "listeners": ["dns"], "upstream": "a", "server_names": ["d.example.com"], "proxy_protocol": "v2"}
]}`,
			want: []string{
				`2:105: listeners[0].proxy_protocol.trusted[1]: "nope" is not an IP address or CIDR range`,
				`3:77: listeners[1].proxy_protocol: a PROXY protocol header comes at the start of a TCP connection; this listener takes UDP alone`,
				`3:89: listeners[1].proxy_protocol.trusted: at least one address is needed`,
				`8:74: services[1].server_names[0]: "A.example.com" is already service "s1"'s on listener "tls"`,
				`8:91: services[1].server_names[1]: "1.2.3.4" is an IP address; a ClientHello names host names`,
				`8:102: services[1].server_names[2]: "x..y" is not a valid host name`,
				`8:127: services[1].server_names[4]: "c.example.com" is listed twice`,
				`10:32: services[3].listeners[0]: listener "tls" is already service "s3"'s: services that share a tcp listener take the connections of their server_names, and only one of them may have none`,
				`11:73: services[4].server_names: server_names pick a service by the TLS ClientHello of a tcp connection; a service with a udp listener takes none`,
				`11:110: services[4].proxy_protocol: the PROXY protocol is sent on tcp connections; a service with a udp listener sends none`,
			},
		},
		{
			name: "health checks",
			doc: `{"listeners": [{"name": "web", "address": ":80"}], "upstreams": [
  {"name": "u", "addresses": ["a:1"], "health_check": {"type": "tcp", "path": "/h", "host": "h", "expect_status": [200]}},
  {"name": "v", "addresses": ["a:1"], "health_check": {"path": "h", "host": "*.h", "timeout": "6s", "jitter": "5s"}},
  {"name": "w", "addresses": ["a:1"], "health_check": {"interval": "100ms"}}
]}`,
			want: []string{
				`2:79: upstreams[0].health_check.path: path is for http health checks; a tcp one only opens a connection`,
				`2:93: upstreams[0].health_check.host: host is for http health checks; a tcp one only opens a connection`,
				`2:115: upstreams[0].health_check.expect_status: expect_status is for http health checks; a tcp one only opens a connection`,
				`3:64: upstreams[1].health_check.path: "h" does not start with '/'`,
				`3:77: upstreams[1].health_check.host: "*.h" is not a host name or IP address`,
				`3:95: upstreams[1].health_check.timeout: the timeout, 6s, is longer than the interval, 5s`,
				`3:111: upstreams[1].health_check.jitter: the jitter, 5s, is not less than the interval, 5s`,
				`4:55: upstreams[2].health_check.timeout: the timeout, 2s, is longer than the interval, 100ms`,
			},
		},
		{
			name: "admin on every interface without a token",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "admin": {"address": ":2019"}}`,
			want: []string{`1:73: admin.address: ":2019" is not a loopback address: the Admin API can change everything fit serves, so listen on localhost, 127.0.0.1, [::1] or a Unix socket, or set token_file`},
		},
		{
			name: "admin on another host without a token",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "admin": {"address": "10.0.0.1:2019"}}`,
			want: []string{`1:73: admin.address: "10.0.0.1:2019" is not a loopback address: the Admin API can change everything fit serves, so listen on localhost, 127.0.0.1, [::1] or a Unix socket, or set token_file`},
		},
		{
			name: "admin addresses",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "admin": {"address": "unix/"}}`,
			want: []string{`1:73: admin.address: "unix/" has no path after "unix/"`},
		},
		{
			name: "admin address without a port",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "admin": {"address": "localhost"}}`,
			want: []string{`1:73: admin.address: "localhost" is not a host and port, as in ":8080" or "127.0.0.1:8080"`},
		},
		{
			name: "client_auth without files, requiring certificates where only tls-alpn-01 obtains them",
			doc:  `{"listeners": [{"name": "web", "address": ":443", "tls": {"acme": ["a.example"], "client_auth": {"ca_files": []}}}], "acme": {"challenges": ["tls-alpn-01"]}}`,
			want: []string{
				`1:110: listeners[0].tls.client_auth.ca_files: at least one file is needed`,
				`1:97: listeners[0].tls.client_auth: a listener that requires client certificates cannot answer TLS-ALPN-01, the only challenge acme.challenges allows; set mode verify_if_given or allow http-01`,
			},
		},
		{
			name: "client_auth's mode",
			doc:  `{"listeners": [{"name": "web", "address": ":443", "tls": {"acme": ["a.example"], "client_auth": {"ca_files": ["ca.pem"], "mode": "maybe"}}}]}`,
			want: []string{`1:130: listeners[0].tls.client_auth.mode: "maybe" is not one of "require", "verify_if_given"`},
		},
		{
			name: "acme challenges",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "acme": {"challenges": ["http-01", "dns-01"]}}`,
			want: []string{`1:87: acme.challenges[1]: "dns-01" is not one of "http-01", "tls-alpn-01"`},
		},
		{
			name: "acme challenges twice",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "acme": {"challenges": ["http-01", "tls-alpn-01", "http-01"]}}`,
			want: []string{`1:102: acme.challenges[2]: "http-01" is listed twice`},
		},
		{
			name: "on_demand without acme.on_demand",
			doc:  `{"listeners": [{"name": "web", "address": ":443", "tls": {"on_demand": true}}]}`,
			want: []string{`1:72: listeners[0].tls.on_demand: on_demand needs acme.on_demand, which says whom to ask whether a name may have a certificate`},
		},
		{
			name: "on_demand's ask",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "acme": {"on_demand": {"ask": "ftp://x"}}}`,
			want: []string{`1:82: acme.on_demand.ask: "ftp://x" is not an http or https URL`},
		},
		{
			name: "metrics without admin",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "metrics": {}}`,
			want: []string{`1:63: metrics: the Admin API serves metrics: set admin too`},
		},
		{
			name: "a provider of no kind",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{}]}`,
			want: []string{"1:66: providers[0]: set exactly one kind of provider: docker, consul, consul_kv, etcd or kubernetes"},
		},
		{
			name: "two providers of one name",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"docker": {}}, {"docker": {"endpoint": "tcp://127.0.0.1:2375"}}]}`,
			want: []string{"1:82: providers[1].name: another provider is already named \"docker\": give one a name"},
		},
		{
			name: "a provider named as a base source",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"name": "file", "docker": {}}]}`,
			want: []string{"1:75: providers[0].name: \"file\" names a source of the base config: pick another name"},
		},
		{
			name: "a provider name",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"name": "Bad", "docker": {}}]}`,
			want: []string{"1:75: providers[0].name: \"Bad\" is not a valid name: use lower-case letters, digits, '_', '.' and '-', starting with a letter or digit, at most 63"},
		},
		{
			name: "a docker endpoint",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"docker": {"endpoint": "http://x"}}]}`,
			want: []string{"1:90: providers[0].docker.endpoint: \"http://x\" is neither unix://<path> nor tcp://<host>:<port>"},
		},
		{
			name: "a docker tcp endpoint",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"docker": {"endpoint": "tcp://nohost"}}]}`,
			want: []string{"1:90: providers[0].docker.endpoint: \"tcp://nohost\" has no host and port"},
		},
		{
			name: "a docker label prefix",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"docker": {"label_prefix": "-x"}}]}`,
			want: []string{"1:94: providers[0].docker.label_prefix: \"-x\" is not a valid prefix: use letters, digits, '_', '.' and '-', starting with a letter or digit"},
		},
		{
			name: "two kinds in one provider",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"docker": {}, "kubernetes": {}}]}`,
			want: []string{"1:66: providers[0]: set exactly one kind of provider: docker, consul, consul_kv, etcd or kubernetes"},
		},
		{
			name: "storage and shared_storage",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "acme": {"storage": "/d", "shared_storage": {"redis": {}, "etcd": {}}}}`,
			want: []string{
				`1:72: acme.storage: storage and shared_storage are two places to keep certificates: set one`,
				`1:96: acme.shared_storage: set exactly one kind of shared storage: redis, etcd or consul`,
			},
		},
		{
			name: "redis storage",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "acme": {"shared_storage": {"redis": {"address": "nohost", "username": "u", "ca_file": "ca.pem"}, "lock_ttl": "5s"}}}`,
			want: []string{
				`1:162: acme.shared_storage.lock_ttl: 5s is too short: at least 10s, which Consul's sessions need, and more than a renewal takes`,
				`1:101: acme.shared_storage.redis.address: "nohost" is not a host and port`,
				`1:123: acme.shared_storage.redis.username: username needs password_file`,
				`1:139: acme.shared_storage.redis.ca_file: ca_file needs tls`,
			},
		},
		{
			name: "etcd storage",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "acme": {"shared_storage": {"etcd": {"endpoints": ["tcp://x:1"], "password_file": "p"}}}}`,
			want: []string{
				`1:103: acme.shared_storage.etcd.endpoints[0]: "tcp://x:1" is neither http://<host>:<port> nor https://<host>:<port>`,
				`1:134: acme.shared_storage.etcd.password_file: password_file needs username`,
			},
		},
		{
			name: "consul storage",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "acme": {"shared_storage": {"consul": {"endpoint": "http://x", "cert_file": "c"}}}}`,
			want: []string{
				`1:103: acme.shared_storage.consul.endpoint: "http://x" is not a host and port alone`,
				`1:128: acme.shared_storage.consul.cert_file: cert_file needs key_file`,
			},
		},
		{
			name: "consul settings",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"consul": {"endpoint": "tcp://x:1", "tag_prefix": "-x", "cert_file": "c.pem"}}]}`,
			want: []string{
				`1:90: providers[0].consul.endpoint: "tcp://x:1" is neither http://<host>:<port> nor https://<host>:<port>`,
				`1:136: providers[0].consul.cert_file: cert_file needs key_file`,
				`1:117: providers[0].consul.tag_prefix: "-x" is not a valid prefix: use letters, digits, '_', '.' and '-', starting with a letter or digit`,
			},
		},
		{
			name: "consul_kv settings",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"consul_kv": {"endpoint": "http://x:1/v1", "key_file": "k.pem"}}]}`,
			want: []string{
				`1:93: providers[0].consul_kv.endpoint: "http://x:1/v1" is not a host and port alone`,
				`1:122: providers[0].consul_kv.key_file: key_file needs cert_file`,
			},
		},
		{
			name: "etcd settings",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"etcd": {"endpoints": ["http://ok:2379", "https://nohost"], "username": "u"}}]}`,
			want: []string{
				`1:108: providers[0].etcd.endpoints[1]: "https://nohost" is not a host and port alone`,
				`1:139: providers[0].etcd.username: username needs password_file`,
			},
		},
		{
			name: "kubernetes settings",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"kubernetes": {"listen_host": "example.com", "controller_name": "fit"}}]}`,
			want: []string{
				`1:97: providers[0].kubernetes.listen_host: "example.com" is not an IP address or localhost`,
				`1:131: providers[0].kubernetes.controller_name: "fit" is not a domain-prefixed path, as example.net/gateway-controller`,
			},
		},
		{
			name: "kubernetes ingress ports",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"kubernetes": {"ingress": {"http_port": 70000}}}]}`,
			want: []string{`1:107: providers[0].kubernetes.ingress.http_port: 70000 is not a port`},
		},
		{
			name: "kubernetes ingress on one port",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"kubernetes": {"ingress": {"https_port": 80}}}]}`,
			want: []string{`1:108: providers[0].kubernetes.ingress.https_port: port 80 serves HTTP already: HTTPS needs a port of its own`},
		},
		{
			name: "kubernetes backends",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "providers": [{"kubernetes": {"backends": "nodes"}}]}`,
			want: []string{`1:94: providers[0].kubernetes.backends: "nodes" is not one of "endpoints", "cluster_ip"`},
		},
		{
			name: "admin limits",
			doc:  `{"listeners": [{"name": "web", "address": ":80"}], "admin": {"history": -1}}`,
			want: []string{`1:73: admin.history: -1 is less than the minimum 0`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErrors(t, tt.doc, tt.want)
		})
	}
}

func TestAdmin(t *testing.T) {
	for _, tt := range []struct {
		admin            string
		network, address string
	}{
		{`{}`, "tcp", DefaultAdminAddress},
		{`{"address": "localhost:2019"}`, "tcp", "localhost:2019"},
		{`{"address": "[::1]:0"}`, "tcp", "[::1]:0"},
		{`{"address": "127.0.0.2:9"}`, "tcp", "127.0.0.2:9"},
		{`{"address": ":2019", "token_file": "token"}`, "tcp", ":2019"},
		{`{"address": "unix//run/fit/admin.sock"}`, "unix", "/run/fit/admin.sock"},
	} {
		c, err := Load([]byte(`{"listeners": [{"name": "web", "address": ":80"}], "admin": ` + tt.admin + `}`))
		if err != nil {
			t.Fatalf("%s: %v", tt.admin, err)
		}
		if network, address := c.Admin.Network(); network != tt.network || address != tt.address {
			t.Errorf("%s: Network() = %s %s, want %s %s", tt.admin, network, address, tt.network, tt.address)
		}
	}
	a := &Admin{}
	if a.HistoryLen() != DefaultAdminHistory || a.ConfigBytes() != DefaultAdminMaxConfigBytes || a.Conns() != DefaultAdminMaxConns {
		t.Errorf("defaults: %d %d %d", a.HistoryLen(), a.ConfigBytes(), a.Conns())
	}
	a = &Admin{History: 3, MaxConfigBytes: 100, MaxConns: 2}
	if a.HistoryLen() != 3 || a.ConfigBytes() != 100 || a.Conns() != 2 {
		t.Errorf("set: %d %d %d", a.HistoryLen(), a.ConfigBytes(), a.Conns())
	}
}

func TestHealthCheckDefaults(t *testing.T) {
	h := &HealthCheck{}
	if h.Every() != 5*time.Second || h.Wait() != 2*time.Second || h.Spread() != 500*time.Millisecond || h.FailsNeeded() != 2 || h.PassesNeeded() != 2 {
		t.Errorf("defaults: %v %v %v %d %d", h.Every(), h.Wait(), h.Spread(), h.FailsNeeded(), h.PassesNeeded())
	}
	h = &HealthCheck{Interval: Duration(time.Second), Timeout: Duration(time.Second), Jitter: Duration(time.Millisecond), Fails: 1, Passes: 3}
	if h.Every() != time.Second || h.Wait() != time.Second || h.Spread() != time.Millisecond || h.FailsNeeded() != 1 || h.PassesNeeded() != 3 {
		t.Errorf("set: %v %v %v %d %d", h.Every(), h.Wait(), h.Spread(), h.FailsNeeded(), h.PassesNeeded())
	}
}

func checkErrors(t *testing.T, doc string, want []string) {
	t.Helper()
	_, err := Load([]byte(doc))
	if err == nil {
		t.Fatal("Load succeeded")
	}
	errs, ok := err.(Errors)
	if !ok {
		t.Fatalf("error is %T, not Errors", err)
	}
	got := make([]string, len(errs))
	for i, e := range errs {
		got[i] = e.Error()
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	c, err := Load([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"listeners":[{"name":"web","address":":8080","protocols":["http/1.1","h2c"]},{"name":"admin","address":"127.0.0.1:0"}],` +
		`"routes":[{"name":"hello","listeners":["web"],"priority":10,"match":{"host":["example.com","*.example.com"],"path":["/hello","/api/*"]},` +
		`"handler":{"module":"respond","params":{"status":200,"body":"hi"}}},{"handler":{"module":"respond"}}]}`
	if string(out) != want {
		t.Fatalf("marshalled:\n%s\nwant:\n%s", out, want)
	}
	if _, err := Load(out); err != nil {
		t.Fatalf("reloading: %v", err)
	}
}

func TestMetrics(t *testing.T) {
	c, err := Load([]byte(`{"listeners": [{"name": "web", "address": ":80"}], "admin": {}, "metrics": {}}`))
	if err != nil || c.Metrics == nil {
		t.Fatalf("metrics: %v, %+v", err, c)
	}
	if c, err := Load([]byte(`{"listeners": [{"name": "web", "address": ":80"}], "admin": {}}`)); err != nil || c.Metrics != nil {
		t.Fatalf("no metrics: %v, %+v", err, c)
	}
	_, err = Load([]byte(`{"listeners": [{"name": "web", "address": ":80"}], "admin": {}, "metrics": {"per_host": true}}`))
	if err == nil || !strings.Contains(err.Error(), "1:77: metrics.per_host: unknown field") {
		t.Errorf("an unknown field: %v", err)
	}
}
