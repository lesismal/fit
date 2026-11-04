package admin

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/config"
)

// TestTextRequestErrors: the text endpoints refuse a body of a type their
// format is not sent as, a config that is not one, and a fragment the
// chosen format cannot write, applying nothing.
func TestTextRequestErrors(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	before := f.configs.Current()
	for _, c := range []struct {
		name, method, path, contentType, body string
		status                                int
		want                                  string
	}{
		{"render text", "POST", "/config/render?format=conf", "text/plain", `{}`, 415, "application/json"},
		{"render an unended comment", "POST", "/config/render?format=json", "application/json", `{"a": 1 /* open`, 400, "the body is not a config: "},
		{"render a list", "POST", "/config/render?format=json", "application/json", `[1]`, 400, "a config is a JSON object"},
		{"render a module that is not one", "POST", "/config/render?format=conf", "application/json",
			`{"routes": [{"handler": 5}]}`, 400, "rendering the config: "},
		{"parse without a format", "POST", "/config/parse", "text/plain", `{}`, 400, "conf, yaml or json"},
		{"parse XML", "POST", "/config/parse?format=json", "application/xml", `{}`, 415, "text/plain"},
		{"put XML", "PUT", "/config/text?format=yaml", "application/xml", `{}`, 415, "application/yaml"},
		{"put broken conf", "PUT", "/config/text?format=conf", "text/plain", "listener web {\n", 400, "the text does not parse"},
		{"put a YAML list", "PUT", "/config/text?format=yaml", "text/plain", "- a\n- b\n", 400, "a config is a JSON object"},
	} {
		resp, body := f.send(t, c.method, c.path, c.contentType, c.body, "")
		if resp.StatusCode != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s", c.name, resp.StatusCode, body)
		}
	}
	// Empty names among the members are left out.
	resp, body := f.send(t, "PUT", "/config/text?format=json&members=,routes,,routes", "text/plain",
		`{"routes": [{"handler": {"module": "respond", "params": {"body": "v2"}}}]}`, "")
	if resp.StatusCode != 200 || f.served(t) != "v2" {
		t.Fatalf("members with empty names: %d %s", resp.StatusCode, body)
	}
	if f.configs.Current().Number != before.Number+1 {
		t.Errorf("versions: %d after %d", f.configs.Current().Number, before.Number)
	}
}

// TestTextSaveNotBound: a text whose config loads and compiles but whose
// listener cannot be bound is not applied, and the error is the server's,
// not the config's.
func TestTextSaveNotBound(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	before := f.configs.Current()
	text := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}, {"name": "taken", "address": "` + taken.Addr().String() + `"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "v2"}}}]}`
	resp, body := f.send(t, "PUT", "/config/text?format=json", "text/plain", text, "")
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "the config was not applied; the running config stays: ") {
		t.Fatalf("a listener whose address is taken: %d %s", resp.StatusCode, body)
	}
	if f.configs.Current() != before || f.served(t) != "v1" {
		t.Error("a config whose listener was not bound was applied")
	}
}

// TestWriteTextUpdateErrorRefused: an edit the API refuses is answered with
// the refusal's own status and message.
func TestWriteTextUpdateErrorRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).writeTextUpdateError(rec, &adapter.Result{}, notFound("there is no %s", "route"))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "there is no route") {
		t.Errorf("a refused edit: %d %s", rec.Code, rec.Body.String())
	}
}

// TestSplitPath: a config error's path is split into member names and array
// indexes, quoted names among them; one that is not written so is not.
func TestSplitPath(t *testing.T) {
	steps, ok := splitPath(`providers["a.b"].routes[2].name`)
	want := []pathStep{{name: "providers"}, {name: "a.b"}, {name: "routes"}, {index: 2, array: true}, {name: "name"}}
	if !ok || len(steps) != len(want) {
		t.Fatalf("steps: %+v %v", steps, ok)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d: %+v, want %+v", i, steps[i], want[i])
		}
	}
	for _, path := range []string{`listeners[0`, `providers["a]`, `listeners[x]`} {
		if steps, ok := splitPath(path); ok {
			t.Errorf("%s: %+v", path, steps)
		}
	}
}

// TestValuePosition: the place of a path is where its value is written in
// the text; a path that does not split has none.
func TestValuePosition(t *testing.T) {
	v, err := config.ParseValue([]byte("{\n  \"listeners\": [\n    {\"name\": \"web\"}\n  ]\n}"), "")
	if err != nil {
		t.Fatal(err)
	}
	pos := valuePosition(v)
	if line, col, ok := pos("listeners[0].name"); !ok || line != 3 || col == 0 {
		t.Errorf("listeners[0].name: %d:%d %v", line, col, ok)
	}
	if line, _, ok := pos(""); !ok || line != 1 {
		t.Errorf("the whole text: %d %v", line, ok)
	}
	if _, _, ok := pos("listeners[x]"); ok {
		t.Error("a path that does not split has a place")
	}
	// A value the text leaves out is placed where its parent is written.
	if line, _, ok := pos("listeners[3].name"); !ok || line != 2 {
		t.Errorf("a value left out: line %d %v", line, ok)
	}
}
