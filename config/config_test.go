package config

import (
	"encoding/json"
	"strings"
	"testing"
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
			want: []string{`1:73: listeners[0].protocols[1]: "spdy" is not one of "http/1.1", "h2c"`},
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErrors(t, tt.doc, tt.want)
		})
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
