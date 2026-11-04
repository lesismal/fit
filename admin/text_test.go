package admin

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// textFragment is a config's parts the text endpoints render, parse and
// save: listeners, routes, upstreams, and a system member.
const textFragment = `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"name": "r1", "handler": {"module": "respond", "params": {"body": "v1"}}}],
  "upstreams": [{"name": "app", "addresses": ["127.0.0.1:9000"]}],
  "log": {"level": "debug"}}`

// checkSameJSON fails the test unless want and got are the same JSON value, so
// that an answer is compared by what it says and not by its layout.
func checkSameJSON(t *testing.T, what, want, got string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: the wanted JSON: %v", what, err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("%s: the answer %q: %v", what, got, err)
	}
	if !reflect.DeepEqual(w, g) {
		t.Errorf("%s:\n%s\nwant:\n%s", what, got, want)
	}
}

// textAnswer decodes an Admin API error answer.
func textAnswer(t *testing.T, body string) errorBody {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("the answer %q is not JSON: %v", body, err)
	}
	return e
}

// currentConfig returns the running config, as GET /config gives it.
func currentConfig(t *testing.T, f *fixture) map[string]json.RawMessage {
	t.Helper()
	_, body := f.get(t, "/config")
	var c map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("GET /config: %v", err)
	}
	return c
}

// TestTextRenderParse: a fragment rendered in each text format parses back
// to the same fragment; an empty one too.
func TestTextRenderParse(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	for _, format := range []string{"conf", "yaml", "json"} {
		resp, text := f.send(t, "POST", "/config/render?format="+format, "application/json", textFragment, "")
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" || text == "" {
			t.Fatalf("render as %s: %d %v %q", format, resp.StatusCode, resp.Header, text)
		}
		// Parse it back with the media type its format is usually sent as.
		ct := "text/plain"
		switch format {
		case "yaml":
			ct = "application/yaml"
		case "json":
			ct = "application/json"
		}
		resp, out := f.send(t, "POST", "/config/parse?format="+format, ct, text, "")
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("parse %s: %d %v %s", format, resp.StatusCode, resp.Header, out)
		}
		checkSameJSON(t, "the fragment after "+format, textFragment, out)
	}
	// An empty fragment renders and parses back as an empty object.
	for _, format := range []string{"conf", "yaml", "json"} {
		resp, text := f.send(t, "POST", "/config/render?format="+format, "application/json", `{}`, "")
		if resp.StatusCode != 200 {
			t.Fatalf("render {} as %s: %d %s", format, resp.StatusCode, text)
		}
		if format != "conf" && strings.TrimSpace(text) != "{}" {
			t.Errorf("render {} as %s: %q", format, text)
		}
		resp, out := f.send(t, "POST", "/config/parse?format="+format, "text/plain", text, "")
		if resp.StatusCode != 200 || strings.TrimSpace(out) != "{}" {
			t.Fatalf("parse the empty %s: %d %s", format, resp.StatusCode, out)
		}
	}
	// A body that is not a JSON object, or not JSON, is refused, and so is
	// a format that is not one of the three.
	for _, tt := range []struct{ query, body, want string }{
		{"", textFragment, "conf, yaml or json"},
		{"format=toml", textFragment, "conf, yaml or json"},
		{"format=json", `[1]`, "a config is a JSON object"},
		{"format=conf", `{`, "not JSON"},
	} {
		resp, out := f.send(t, "POST", "/config/render?"+tt.query, "application/json", tt.body, "")
		if resp.StatusCode != 400 || !strings.Contains(out, tt.want) {
			t.Errorf("render %q %q: %d %s", tt.query, tt.body, resp.StatusCode, out)
		}
	}
}

// TestTextParseErrors: a text that does not parse is answered with the line
// and column the config's own errors were found at.
func TestTextParseErrors(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	for _, tt := range []struct{ name, format, body string }{
		{"conf", "conf", "listener web {\n\taddress :8080\n"},
		{"json", "json", `{"listeners": [}`},
		{"empty json", "json", ""},
	} {
		resp, out := f.send(t, "POST", "/config/parse?format="+tt.format, "text/plain", tt.body, "")
		if resp.StatusCode != 400 {
			t.Fatalf("parse a broken %s: %d %s", tt.name, resp.StatusCode, out)
		}
		e := textAnswer(t, out)
		if e.Error != "the text does not parse" || len(e.Errors) != 1 {
			t.Fatalf("parse a broken %s: %q", tt.name, out)
		}
		if p := e.Errors[0]; p.Line != 1 || p.Column == 0 || p.Message == "" {
			t.Errorf("parse a broken %s: %+v", tt.name, p)
		}
	}
	// YAML's syntax errors are not config errors: the message has the place.
	resp, out := f.send(t, "POST", "/config/parse?format=yaml", "text/plain", "listeners:\n  - name: web\n    address: :8080\n   bad", "")
	if resp.StatusCode != 400 || !strings.Contains(out, "the text does not parse: yaml: line") {
		t.Errorf("parse broken yaml: %d %s", resp.StatusCode, out)
	}
	// A JSON text is read as a .json file fit reads is: comments and a
	// trailing comma are accepted.
	resp, out = f.send(t, "POST", "/config/parse?format=json", "text/plain",
		"{\n  \"listeners\": [ // the web listener\n    {\"name\": \"web\", \"address\": \"127.0.0.1:0\"},\n  ],\n}\n", "")
	if resp.StatusCode != 200 || !strings.Contains(out, `"listeners"`) {
		t.Errorf("parse JSON with comments: %d %s", resp.StatusCode, out)
	}
	// A text that is not an object is refused.
	resp, out = f.send(t, "POST", "/config/parse?format=yaml", "text/plain", "- a\n- b\n", "")
	if resp.StatusCode != 400 || !strings.Contains(out, "not a config object") {
		t.Errorf("parse a YAML list: %d %s", resp.StatusCode, out)
	}
}

// TestTextPartSave: PUT /config/text with members replaces or deletes only
// those top-level members, applies through the normal path, and applies
// nothing when it fails.
func TestTextPartSave(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	etag := f.configs.Current().ETag

	// members=listeners replaces the listener only.
	resp, out := f.send(t, "PUT", "/config/text?format=conf&members=listeners", "text/plain",
		"listener web {\n\taddress 127.0.0.1:0\n\tprotocols http/1.1\n}\n", etag)
	if resp.StatusCode != 200 || resp.Header.Get(VersionHeader) != "2" || resp.Header.Get("ETag") == etag {
		t.Fatalf("replace listeners: %d %v %s", resp.StatusCode, resp.Header, out)
	}
	cfg := currentConfig(t, f)
	checkSameJSON(t, "listeners", `[{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1"]}]`, string(cfg["listeners"]))
	checkSameJSON(t, "routes after replacing the listener", `[{"handler": {"module": "respond", "params": {"body": "v1"}}}]`, string(cfg["routes"]))
	if got := f.served(t); got != "v1" {
		t.Fatalf("served %q after replacing the listener", got)
	}

	// members=upstreams adds one, and a text that leaves it out deletes it,
	// while the other members stay.
	resp, out = f.send(t, "PUT", "/config/text?format=yaml&members=upstreams", "text/plain",
		"upstreams:\n  - name: app\n    addresses:\n      - 127.0.0.1:9000\n", "")
	if resp.StatusCode != 200 {
		t.Fatalf("add upstreams: %d %s", resp.StatusCode, out)
	}
	cfg = currentConfig(t, f)
	checkSameJSON(t, "upstreams", `[{"name": "app", "addresses": ["127.0.0.1:9000"]}]`, string(cfg["upstreams"]))
	resp, out = f.send(t, "PUT", "/config/text?format=yaml&members=upstreams", "text/plain", "# no upstreams\n", "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete upstreams: %d %s", resp.StatusCode, out)
	}
	cfg = currentConfig(t, f)
	if _, ok := cfg["upstreams"]; ok {
		t.Errorf("upstreams after deleting them: %s", cfg["upstreams"])
	}
	checkSameJSON(t, "routes after deleting upstreams", `[{"handler": {"module": "respond", "params": {"body": "v1"}}}]`, string(cfg["routes"]))

	// members=routes replaces the routes, with the running ETag.
	resp, out = f.send(t, "PUT", "/config/text?format=json&members=routes", "text/plain",
		`{"routes": [{"name": "r2", "handler": {"module": "respond", "params": {"body": "v2"}}}]}`, f.configs.Current().ETag)
	if resp.StatusCode != 200 || f.served(t) != "v2" {
		t.Fatalf("replace routes: %d %s", resp.StatusCode, out)
	}
	cfg = currentConfig(t, f)
	checkSameJSON(t, "listeners after replacing routes", `[{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1"]}]`, string(cfg["listeners"]))

	// A text that sets a member the page does not edit is refused.
	before := f.configs.Current()
	resp, out = f.send(t, "PUT", "/config/text?format=conf&members=listeners", "text/plain",
		"listener web 127.0.0.1:0\nroute r3 { handler respond { body v9 } }\n", "")
	if resp.StatusCode != 400 || !strings.Contains(textAnswer(t, out).Error, `the text sets "routes", which this page does not edit`) {
		t.Fatalf("a text with a member outside the page: %d %s", resp.StatusCode, out)
	}
	if f.configs.Current() != before || f.served(t) != "v2" {
		t.Fatal("a refused save changed the config")
	}

	// admin is never edited this way.
	resp, out = f.send(t, "PUT", "/config/text?format=json&members=admin,listeners", "text/plain", `{"listeners": []}`, "")
	if resp.StatusCode != 400 || !strings.Contains(out, "admin cannot be edited this way") {
		t.Fatalf("members=admin: %d %s", resp.StatusCode, out)
	}

	// A listener address fit refuses is refused with the line it is on in
	// the text, and nothing is applied.
	resp, out = f.send(t, "PUT", "/config/text?format=conf&members=listeners", "text/plain", "listener web {\n\taddress bogus\n}\n", "")
	if resp.StatusCode != 400 {
		t.Fatalf("an invalid listener: %d %s", resp.StatusCode, out)
	}
	e := textAnswer(t, out)
	if e.Error != "the config was not applied; the running config stays" || len(e.Errors) != 1 {
		t.Fatalf("an invalid listener: %s", out)
	}
	if p := e.Errors[0]; p.Path != "listeners[0].address" || p.Line != 2 || p.Column == 0 {
		t.Errorf("an invalid listener's error: %+v", p)
	}
	if f.configs.Current() != before || f.served(t) != "v2" {
		t.Fatal("a refused save changed the config")
	}

	// A stale If-Match is answered 412, as every other config write is.
	resp, out = f.send(t, "PUT", "/config/text?format=json&members=listeners", "text/plain",
		`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}]}`, `"stale"`)
	if resp.StatusCode != 412 || !strings.Contains(out, "If-Match") {
		t.Fatalf("a stale If-Match: %d %s", resp.StatusCode, out)
	}

	// The format is required, and only the three are known.
	for _, query := range []string{"", "format=toml"} {
		resp, out = f.send(t, "PUT", "/config/text?"+query, "text/plain", `{}`, "")
		if resp.StatusCode != 400 || !strings.Contains(out, "conf, yaml or json") {
			t.Errorf("PUT /config/text?%s: %d %s", query, resp.StatusCode, out)
		}
	}
}

// TestTextWholeConfig: without members the text is the whole config, which
// is validated before it is applied: it loads, and its modules compile.
func TestTextWholeConfig(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	before := f.configs.Current()

	// A whole config with an invalid part is refused, at its line in the
	// text, and the running config stays.
	resp, out := f.send(t, "PUT", "/config/text?format=conf", "text/plain",
		"listener web {\n\taddress bogus\n}\n\nroute r1 { handler respond { body v1 } }\n", "")
	if resp.StatusCode != 400 {
		t.Fatalf("an invalid whole config: %d %s", resp.StatusCode, out)
	}
	e := textAnswer(t, out)
	if len(e.Errors) != 1 || e.Errors[0].Path != "listeners[0].address" || e.Errors[0].Line != 2 {
		t.Fatalf("an invalid whole config: %s", out)
	}
	if f.configs.Current() != before || f.served(t) != "v1" {
		t.Fatal("a refused whole config changed the running one")
	}

	// A valid one applies, by the normal path, and is kept as its JSON.
	resp, out = f.send(t, "PUT", "/config/text?format=conf", "text/plain",
		"listener web 127.0.0.1:0\n\nroute r1 {\n\thandler respond {\n\t\tbody v3\n\t}\n}\n", "")
	if resp.StatusCode != 200 || f.served(t) != "v3" || resp.Header.Get(VersionHeader) != "2" {
		t.Fatalf("a whole config: %d %v %s", resp.StatusCode, resp.Header, out)
	}
	_, got := f.get(t, "/config")
	checkSameJSON(t, "the whole config", `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"name": "r1", "handler": {"module": "respond", "params": {"body": "v3"}}}]}`, got)

	// Compiling the config checks the modules' params too: an unknown one
	// is an error at its line in the text.
	resp, out = f.send(t, "PUT", "/config/text?format=json", "text/plain",
		`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"bogus": 1}}}]}`, f.configs.Current().ETag)
	if resp.StatusCode != 400 {
		t.Fatalf("an unknown module param: %d %s", resp.StatusCode, out)
	}
	e = textAnswer(t, out)
	if len(e.Errors) != 1 || e.Errors[0].Path != "routes[0].handler.params.bogus" || e.Errors[0].Line == 0 {
		t.Fatalf("an unknown module param: %s", out)
	}
	if f.served(t) != "v3" {
		t.Fatal("a refused whole config changed the running one")
	}
}
