package yaml

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/lesismal/fit/config"
)

// same reports whether two JSON documents hold the same value, numbers
// compared as they are written.
func same(t *testing.T, a, b []byte) bool {
	t.Helper()
	decode := func(data []byte) any {
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		return v
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

// TestRoundTrip writes every config the repository has in JSON as YAML and
// adapts it back: the same JSON comes out, and fit loads it as it loads the
// JSON. The bench's config is in fit's own format, fit.conf, and the
// template's JSON has comments, so neither is here.
func TestRoundTrip(t *testing.T) {
	var files []string
	for _, glob := range []string{"../../examples/*.json", "../../examples/*/fit.json", "../../deploy/*/fit.json"} {
		m, _ := filepath.Glob(glob)
		files = append(files, m...)
	}
	if len(files) < 4 {
		t.Fatalf("found %d configs", len(files))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		y, err := FromJSON(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		r, err := Adapt(y)
		if err != nil {
			t.Fatalf("%s: %v\n%s", f, err, y)
		}
		if !same(t, data, r.JSON) {
			t.Errorf("%s: the round trip changed it:\n%s\n%s", f, y, r.JSON)
		}
		// And YAML to JSON to YAML is stable.
		if y2, err := FromJSON(r.JSON); err != nil || !bytes.Equal(y, y2) {
			t.Errorf("%s: YAML not stable: %v\n%s\n%s", f, err, y, y2)
		}
	}
}

// TestExamples checks that the YAML examples say what their JSON twins do.
func TestExamples(t *testing.T) {
	for _, name := range []string{"hello", "proxy"} {
		j, err := os.ReadFile("../../examples/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		y, err := os.ReadFile("../../examples/" + name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		r, err := Adapt(y)
		if err != nil {
			t.Fatal(err)
		}
		if !same(t, j, r.JSON) {
			t.Errorf("%s.yaml says something else than %s.json:\n%s", name, name, r.JSON)
		}
	}
}

func TestYAML(t *testing.T) {
	for _, tt := range []struct{ yaml, json string }{
		{"a: 1\nb: 1.5\nc: true\nd: null\ne: ~\nf: hello\ng: '1'\nh: \"true\"\n",
			`{"a":1,"b":1.5,"c":true,"d":null,"e":null,"f":"hello","g":"1","h":"true"}`},
		// YAML 1.2: yes and on are strings.
		{"a: yes\nb: on\nc: 30s\nd: 0x1F\ne: 1e3\n", `{"a":"yes","b":"on","c":"30s","d":31,"e":1000}`},
		{"list:\n  - a\n  - {b: 2}\n  - [1, 2]\n", `{"list":["a",{"b":2},[1,2]]}`},
		// Anchors, aliases and merge keys.
		{"base: &b {x: 1, y: 2}\nuse: *b\nmerged:\n  <<: *b\n  y: 3\n", `{"base":{"x":1,"y":2},"use":{"x":1,"y":2},"merged":{"y":3,"x":1}}`},
		{"a: &a {p: 1}\nb: &b {q: 2}\nc:\n  <<: [*a, *b]\n  r: 3\n", `{"a":{"p":1},"b":{"q":2},"c":{"r":3,"p":1,"q":2}}`},
		{"200: ok\n", `{"200":"ok"}`},
		{"text: |\n  line one\n  line two\n", `{"text":"line one\nline two\n"}`},
	} {
		r, err := Adapt([]byte(tt.yaml))
		if err != nil {
			t.Errorf("%q: %v", tt.yaml, err)
			continue
		}
		if !same(t, r.JSON, []byte(tt.json)) {
			t.Errorf("%q: %s, want %s", tt.yaml, r.JSON, tt.json)
		}
	}
}

func TestEmpty(t *testing.T) {
	for _, src := range []string{"", "\n", "# a: 1\n# b: 2\n"} {
		r, err := Adapt([]byte(src))
		if err != nil || string(r.JSON) != "{}" {
			t.Errorf("%q: %v %s, want {}", src, err, r.JSON)
		}
	}
}

func TestErrors(t *testing.T) {
	bomb := "a: &a [x, x, x, x, x, x, x, x, x, x]\n"
	prev := "a"
	for _, n := range []string{"b", "c", "d", "e", "f", "g", "h"} {
		bomb += n + ": &" + n + " [*" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + "]\n"
		prev = n
	}
	for yaml, want := range map[string]string{
		"a: 1\n---\nb: 2\n": "more than one document",
		"[a]: 1\n":          "a key must be a string",
		"a: 1\na: 2\n":      `"a" is a key twice`,
		"a: .inf\n":         "not a number JSON can hold",
		"a: !custom x\n":    "the tag !custom",
		"a: [1\n":           "did not find expected",
		"<<: 1\n":           "a merge key",
		bomb:                "expands to too much",
	} {
		if _, err := Adapt([]byte(yaml)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", yaml, err, want)
		}
	}
}

// TestPositions checks that the config's errors are moved to where the YAML
// wrote what they are about.
func TestPositions(t *testing.T) {
	y := `listeners:
  - name: web
    address: ":8080"
    protocols: [http/1.1, h3]
routes:
  - handler:
      module: respond
      params:
        status: 9999
`
	r, err := Adapt([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(r.JSON)
	if err == nil {
		t.Fatal("no error")
	}
	got := r.MapErrors(err).Error()
	for _, want := range []string{"4:27: listeners[0].protocols[1]"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s, want %s", got, want)
		}
	}
	if line, col, ok := r.Position(`routes[0].handler.params["status"]`); !ok || line != 9 || col != 17 {
		t.Errorf("a map key's path: %d:%d %v", line, col, ok)
	}
}

// TestMoreErrors checks the errors of scalars, merge keys and depth that
// TestErrors does not.
func TestMoreErrors(t *testing.T) {
	for yaml, want := range map[string]string{
		"a: !!bool maybe\n":                                 "line 1, column 4",
		"a: !!int 99999999999999999999999\n":                "is not a number JSON can hold",
		"a: !!int x\n":                                      "is not a number JSON can hold",
		"a: !!float x\n":                                    "line 1, column 4",
		"a:\n  <<: [1]\n":                                   "a merge key (<<) needs a mapping",
		"a:\n  <<: {b: 1, b: 2}\n":                          `"b" is a key twice`,
		strings.Repeat("[", 300) + strings.Repeat("]", 300): "nested too deep",
	} {
		if _, err := Adapt([]byte(yaml)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.40q: %v, want %q", yaml, err, want)
		}
	}
	// Nodes a parsed document does not hold.
	for _, tt := range []struct {
		n    *yamlv3.Node
		want string
	}{
		{&yamlv3.Node{Kind: yamlv3.DocumentNode}, "a document inside a document"},
		{&yamlv3.Node{Kind: 0}, "a node of unknown kind"},
	} {
		c := &converter{pos: map[string][2]int{}}
		if err := c.value(tt.n, "", 0); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v, want %q", err, tt.want)
		}
	}
	c := &converter{pos: map[string][2]int{}}
	if _, err := c.members(&yamlv3.Node{Kind: yamlv3.MappingNode}, maxDepth+1); err == nil || !strings.Contains(err.Error(), "nested too deep") {
		t.Errorf("members too deep: %v", err)
	}
}

func TestFromJSONErrors(t *testing.T) {
	for _, src := range []string{"", "{", "1 2", `{1: 2}`, `{"a": }`, `[1, }`, `]`} {
		if _, err := FromJSON([]byte(src)); err == nil || !strings.HasPrefix(err.Error(), "JSON: ") {
			t.Errorf("%q: %v", src, err)
		}
	}
	// A string <<, value or key, which plain could be read as a merge key,
	// comes back a string.
	y, err := FromJSON([]byte(`{"k": "<<", "<<": "x", "s": "a\nb", "n": null, "b": false, "f": 1.5}`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Adapt(y)
	if err != nil {
		t.Fatalf("%v\n%s", err, y)
	}
	if !same(t, r.JSON, []byte(`{"k": "<<", "<<": "x", "s": "a\nb", "n": null, "b": false, "f": 1.5}`)) {
		t.Errorf("%s\n%s", y, r.JSON)
	}
}
