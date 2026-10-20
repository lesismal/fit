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
				`1:1: listeners: missing required field`,
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
			want: []string{`1:73: listeners[0].protocols[1]: "spdy" is not one of "http/1.1", "h2c", "h2", "h3"`},
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
			want: []string{`listeners: at least one listener is needed`},
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
  {"name": "r", "match": {"host": ["", "a..b", "*.1.2.3.4", "[::1]", "*.ok.com"], "path": ["x", "/a*b", "/ok/*"]}, "handler": {"module": ""}}
]}`,
			want: []string{
				`2:38: routes[0].listeners[1]: there is no listener named "api"`,
				`3:12: routes[1].name: another route is already named "r"`,
				`3:36: routes[1].match.host[0]: the host is empty`,
				`3:40: routes[1].match.host[1]: "a..b" is not a valid host name`,
				`3:48: routes[1].match.host[2]: "*.1.2.3.4" puts a wildcard on an IP address`,
				`3:92: routes[1].match.path[0]: "x" does not start with '/'`,
				`3:97: routes[1].match.path[1]: "/a*b" has a '*' other than at its end`,
				`3:138: routes[1].handler.module: the module name is empty`,
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
  {"name": "u", "addresses": ["a:1"], "tls_server_name": "x", "tls_ca_file": "ca.pem"}
]}`,
			want: []string{
				`2:58: upstreams[0].tls_server_name: tls_server_name needs protocol h2`,
				`2:78: upstreams[0].tls_ca_file: tls_ca_file needs protocol h2`,
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
