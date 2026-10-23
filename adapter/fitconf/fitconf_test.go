package fitconf

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/modules/standard"
)

// same says a and b hold the same JSON value, whatever the order of their
// members.
func same(t *testing.T, a, b []byte) bool {
	t.Helper()
	norm := func(data []byte) string {
		std, err := config.Standardize(data)
		if err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		var v any
		dec := json.NewDecoder(bytes.NewReader(std))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		out, _ := json.Marshal(v)
		return string(out)
	}
	return norm(a) == norm(b)
}

func TestConvert(t *testing.T) {
	t.Setenv("FIT_TEST_PORT", "9090")
	for _, c := range []struct{ name, fit, json string }{
		{"scalars and lists", `
schema_version 1
listener web {
	address :{$FIT_TEST_PORT}
	protocols http/1.1
	protocols h2c
}
listener other {
	address {$FIT_TEST_UNSET:127.0.0.1:1}
}`, `{"schema_version": 1, "listeners": [
			{"name": "web", "address": ":9090", "protocols": ["http/1.1", "h2c"]},
			{"name": "other", "address": "127.0.0.1:1"}]}`},
		{"plural and name in the block", `
listeners {
	name web; address :8080
}`, `{"listeners": [{"name": "web", "address": ":8080"}]}`},
		{"objects, booleans, empty blocks", `
upstream app {
	addresses 127.0.0.1:9000
	sticky_cookie {
		secure
		max_age 24h
	}
}
admin {}
auto_https {
	disable false
}`, `{"upstreams": [{"name": "app", "addresses": ["127.0.0.1:9000"], "sticky_cookie": {"secure": true, "max_age": "24h"}}],
			"admin": {}, "auto_https": {"disable": false}}`},
		{"maps, quoting and comments", `
route { # a comment
	match {
		header {
			X-Env prod staging
			"X Odd" "a b"
		}
		query { debug }
		expr ` + "`" + `Host("example.com") && Method("GET")` + "`" + `
	}
	handler respond {
		headers {
			Cache-Control no-store
		}
		body "a \"quoted\" line\n# not a comment"
		status 201
	}
}`, `{"routes": [{"match": {"header": {"X-Env": ["prod", "staging"], "X Odd": ["a b"]}, "query": {"debug": []},
			"expr": "Host(\"example.com\") && Method(\"GET\")"},
			"handler": {"module": "respond", "params": {"headers": {"Cache-Control": "no-store"}, "body": "a \"quoted\" line\n# not a comment", "status": 201}}}]}`},
		{"modules", `
route {
	middleware compress
	middleware headers { response { delete Server } }
	handler proxy {
		upstream app
		upstreams {
			upstream canary
			weight 5
		}
		retry { attempts 2; statuses 502 503 }
	}
}`, `{"routes": [{"middleware": [{"module": "compress"}, {"module": "headers", "params": {"response": {"delete": ["Server"]}}}],
			"handler": {"module": "proxy", "params": {"upstream": "app", "upstreams": [{"upstream": "canary", "weight": 5}],
			"retry": {"attempts": 2, "statuses": [502, 503]}}}}]}`},
		{"a value of the wrong type is kept for the check", `
limits {
	max_conns "100"
	max_body_bytes lots
}`, `{"limits": {"max_conns": "100", "max_body_bytes": "lots"}}`},
		{"an unknown field is kept for the check", `
bogus {
	a 1
	b x y
}`, `{"bogus": {"a": 1, "b": ["x", "y"]}}`},
		{"a site", `
site example.com www.example.com {
	listeners web
	match { method GET }
	middleware compress
	route /api/* /v2/* {
		name api
		middleware headers { response { delete Server } }
		handler proxy { upstream app }
	}
	route {
		priority 5
		match { method POST }
		handler respond
	}
}
route last {
	handler respond
}`, `{"routes": [
			{"match": {"host": ["example.com", "www.example.com"], "path": ["/api/*", "/v2/*"], "method": ["GET"]},
			 "listeners": ["web"], "name": "api",
			 "middleware": [{"module": "compress"}, {"module": "headers", "params": {"response": {"delete": ["Server"]}}}],
			 "handler": {"module": "proxy", "params": {"upstream": "app"}}},
			{"match": {"host": ["example.com", "www.example.com"], "method": ["POST"]},
			 "listeners": ["web"], "priority": 5, "middleware": [{"module": "compress"}], "handler": {"module": "respond"}},
			{"name": "last", "handler": {"module": "respond"}}]}`},
	} {
		r, err := Adapt([]byte(c.fit))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !same(t, r.JSON, []byte(c.json)) {
			t.Errorf("%s:\n got %s\nwant %s", c.name, r.JSON, c.json)
		}
	}
}

func TestErrors(t *testing.T) {
	for _, c := range []struct{ fit, want string }{
		{"listener web {\n\taddress :1\n", "1:14: this { is not closed"},
		{"}\n", "1:1: a } closes no block"},
		{"admin {\n\taddress \"x\n}\n", `2:10: a quote is not closed`},
		{"admin {\n\taddress `x\n}\n", "2:10: a backquote is not closed"},
		{"admin {} x\n", "1:10: a block's } ends its directive"},
		{"{\n}\n", "1:1: a block needs a directive before its {"},
		{"admin {\n\taddress a b\n}\n", "2:12: address takes one value"},
		{"admin {\n\taddress\n}\n", "2:2: address takes a value"},
		{"admin {\n\taddress { x }\n}\n", "2:2: address takes a value, not a block"},
		{"admin x\n", "1:7: admin takes a block, not arguments"},
		{"admin {}\nadmin {}\n", "2:1: admin is given twice"},
		{"listener a b {}\n", "1:1: listener takes one argument, its name, and a block"},
		{"listener a {\n\tname b\n}\n", "1:10: the name is given twice"},
		{"route {\n\thandler\n}\n", "2:2: handler takes the name of a module"},
		{"route {\n\tmatch {\n\t\tpath x { }\n\t}\n}\n", "3:3: path takes a list of values, not a block"},
		{"route {\n\tmatch {\n\t\theader {\n\t\t\tA 1\n\t\t\tA 2\n\t\t}\n\t}\n}\n", `5:4: "A" is a key twice`},
		{"admin \"a\"b\n", "1:10: a quoted word must end"},
		{"admin {$}\n", "1:7: {$} names no environment variable"},
		{"site {\n}\n", "1:1: a site needs its host names"},
		{"site a.example\n", "1:1: a site needs a block with its routes"},
		{"site a.example {\n}\n", "1:1: a site needs a route"},
		{"site a.example {\n\thandler respond\n}\n", "2:2: a site holds listeners, priority, match, middleware and its routes, not handler"},
		{"site a.example {\n\tmatch { host b }\n\troute { handler respond }\n}\n", "2:10: a site's routes take their hosts from the site"},
		{"site a.example {\n\troute {\n\t\tmatch { host b }\n\t\thandler respond\n\t}\n}\n", "3:11: a site's route takes its hosts from the site"},
		{"site a.example {\n\troute /a {\n\t\tmatch { path /b }\n\t\thandler respond\n\t}\n}\n", "3:11: the route's paths are given twice"},
		{"site a.example {\n\troute /a\n}\n", "2:2: a route needs a block with its handler"},
		{"site a.example {\n\tlisteners a\n\tlisteners b\n\troute { handler respond }\n}\n", "3:2: a site's listeners is given twice"},
		{"\xff\n", "not UTF-8"},
	} {
		_, err := Adapt([]byte(c.fit))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.fit, err, c.want)
		}
	}
}

// TestPositions checks that the config's errors point at the .conf file.
func TestPositions(t *testing.T) {
	src := "listener web {\n\taddress nowhere\n}\nsite a.example {\n\troute /x {\n\t\thandler respond { status 999 }\n\t}\n}\nroute {\n\tmatch { bogus 1 }\n\thandler respond\n}\n"
	r, err := Adapt([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(r.JSON)
	if got, want := r.MapErrors(err).Error(), "10:10: routes[1].match.bogus: unknown field"; !strings.Contains(got, want) {
		t.Errorf("no %q in\n%s", want, got)
	}
	// Once it decodes, the checks find the address.
	r, err = Adapt([]byte(strings.Replace(src, "bogus 1", "method GET", 1)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(r.JSON)
	if got, want := r.MapErrors(err).Error(), "2:10: listeners[0].address"; !strings.Contains(got, want) {
		t.Errorf("no %q in\n%s", want, got)
	}
	if line, col, ok := r.Position("routes[0].handler.params.status"); !ok || line != 6 || col != 28 {
		t.Errorf("the status is at %d:%d %v", line, col, ok)
	}
	if line, _, ok := r.Position("routes[0].match.host"); !ok || line != 4 {
		t.Errorf("a site's hosts are at line %d %v", line, ok)
	}
}

// TestRoundTrip writes configs as .conf files and reads them back: the
// templates, the examples and the test configs.
func TestRoundTrip(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.json")
	more, _ := filepath.Glob("../../config/testdata/*.json")
	files = append(append(files, more...), "../../templates/json/fit.json")
	n := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		std, err := config.Standardize(data)
		if err != nil || !json.Valid(std) {
			continue
		}
		if _, err := config.Load(std); err != nil {
			continue // a test config that is meant not to load
		}
		n++
		out, err := FromJSON(std)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		r, err := Adapt(out)
		if err != nil {
			t.Errorf("%s: %v\n%s", f, err, out)
			continue
		}
		if !same(t, r.JSON, std) {
			t.Errorf("%s: the round trip changed it:\n%s\n%s", f, out, r.JSON)
		}
	}
	if n < 3 {
		t.Errorf("only %d configs", n)
	}
}

func TestWord(t *testing.T) {
	for _, s := range []string{"", "a b", "{", "}", "{}", "#x", "x;", ";", "a\"b", "a`b", "{$HOME}", "{$X} `y`", "tab\there", "line\nbreak", `back\slash`, "{path}"} {
		r, err := Adapt([]byte("admin {\n\taddress " + word(s) + "\n}\n"))
		if err != nil {
			t.Errorf("%q as %s: %v", s, word(s), err)
			continue
		}
		var c struct {
			Admin struct{ Address string }
		}
		if err := json.Unmarshal(r.JSON, &c); err != nil || c.Admin.Address != s {
			t.Errorf("%q as %s reads back as %q (%v)", s, word(s), c.Admin.Address, err)
		}
	}
}

// TestRegistered checks that .conf files are adapted by their extension.
func TestRegistered(t *testing.T) {
	if name := adapter.ForPath("conf.d/site.conf"); name != "fit" {
		t.Errorf("a .conf file is adapted by %q", name)
	}
}
