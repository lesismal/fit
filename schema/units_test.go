package schema

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// kinds holds the kinds of field fit's own config types do not have, or
// not all of; it is never registered, so the generated files stay as they
// are.
type kinds struct {
	Anon struct {
		A int `json:"a" doc:"a"`
	} `json:"anon" doc:"anon"`
	Unsigned uint             `json:"unsigned" doc:"u"`
	Small    uint8            `json:"small" min:"1" doc:"s"`
	Most     int              `json:"most" max:"9" doc:"m"`
	Any      config.Value     `json:"any" doc:"any"`
	Names    []string         `json:"names" doc:"n"`
	Map      map[string]int   `json:"map" doc:"m"`
	Ref      config.ModuleRef `json:"ref" module:"other" doc:"r"`
}

func panics(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", name)
		}
	}()
	f()
}

func TestTypeSchema(t *testing.T) {
	g := &generator{defs: object{}}
	s := g.structSchema(reflect.TypeFor[kinds]())
	props := s["properties"].(object)
	if anon := props["anon"].(object); anon["type"] != "object" {
		t.Errorf("anon: %v", anon)
	}
	if u := props["unsigned"].(object); u["minimum"] != 0 {
		t.Errorf("unsigned: %v", u)
	}
	if u := props["small"].(object); u["minimum"] != 1.0 {
		t.Errorf("small: %v", u)
	}
	// A namespace is defined once.
	g.defineModuleRef(module.Handlers)
	before := len(g.defs)
	g.defineModuleRef(module.Handlers)
	if len(g.defs) != before {
		t.Error("defined twice")
	}
	panics(t, "a ModuleRef without a module tag", func() { g.typeSchema(reflect.TypeFor[config.ModuleRef](), nil) })
	panics(t, "complex", func() { g.typeSchema(reflect.TypeFor[complex64](), nil) })
}

func TestReferenceTypes(t *testing.T) {
	r := &reference{anchors: map[reflect.Type]*section{}, module: "m"}
	s := r.walk(reflect.TypeFor[kinds](), "", "kinds")
	types := map[string]string{}
	for _, row := range s.rows {
		types[row[0]] = row[1]
	}
	for field, want := range map[string]string{
		"`anon`":     "[object](#m-anon)",
		"`unsigned`": "integer, 0 or more",
		"`small`":    "integer, 1 or more",
		"`most`":     "integer, 9 or less",
		"`any`":      "any",
		"`ref`":      "module",
	} {
		if types[field] != want {
			t.Errorf("%s: %q, want %q", field, types[field], want)
		}
	}
	panics(t, "complex", func() { r.typeName(reflect.TypeFor[complex64](), nil, "c") })
	if h := (&section{paths: []string{""}}).heading(); h != "" {
		t.Errorf("heading %q", h)
	}
}

// TestReferenceErrors: a chapter missing, or one for a field with none,
// fails the reference.
func TestReferenceErrors(t *testing.T) {
	saved, savedNS := chapters, namespaces
	defer func() { chapters, namespaces = saved, savedNS }()

	chapters = append(slices.Clone(saved), struct{ field, title, anchor, file string }{"nope", "Nope", "nope", "nope.md"})
	if _, err := Reference(); err == nil || !strings.Contains(err.Error(), "no chapter nope.md") {
		t.Errorf("a chapter file missing: %v", err)
	}
	chapters = saved[:len(saved)-1]
	if _, err := Reference(); err == nil || !strings.Contains(err.Error(), "a chapter for "+saved[len(saved)-1].field) {
		t.Errorf("a field without a chapter: %v", err)
	}
	chapters = saved
	namespaces = append(slices.Clone(savedNS), struct{ ns, title, kind, file string }{"x", "X", "x", "nope.md"})
	if _, err := Reference(); err == nil || !strings.Contains(err.Error(), "no chapter nope.md") {
		t.Errorf("a namespace's chapter missing: %v", err)
	}
}

func TestChapterErrors(t *testing.T) {
	if _, err := readChapter("nope.md"); err == nil {
		t.Error("readChapter of a missing file succeeded")
	}
	var buf bytes.Buffer
	if err := (&chapter{file: "x", json: "{"}).write(&buf); err == nil || !strings.Contains(err.Error(), "as a .conf file") {
		t.Errorf("JSON that is no .conf file: %v", err)
	}
	if err := (&chapter{file: "x", json: "{", fit: "x"}).write(&buf); err == nil || !strings.Contains(err.Error(), "its JSON") {
		t.Errorf("JSON that is no YAML: %v", err)
	}
}

func TestTemplateKinds(t *testing.T) {
	tm := &templater{}
	n := tm.object(reflect.TypeFor[kinds](), "zz", nil)
	scalars := map[string]string{}
	for _, c := range n.children {
		scalars[c.name] = c.scalar
	}
	for field, want := range map[string]string{"any": "{}", "names": "[]", "map": "{}", "most": "0", "unsigned": "0", "small": "1"} {
		if scalars[field] != want {
			t.Errorf("%s: %q, want %q", field, scalars[field], want)
		}
	}
	if got := scalarExample(config.Field{Name: "zz"}, "zz", reflect.TypeFor[string]()); got != `""` {
		t.Errorf("a string: %s", got)
	}
	if got := scalarExample(config.Field{Name: "zz"}, "zz", reflect.TypeFor[struct{}]()); got != "null" {
		t.Errorf("a struct: %s", got)
	}
	if deref(reflect.TypeFor[**int]()) != reflect.TypeFor[int]() {
		t.Error("deref")
	}
	if tm.moduleRef(module.Handlers, "nope", "") != nil {
		t.Error("a module that is not there")
	}
	if got := tm.typeName(reflect.TypeFor[config.Value](), nil); got != "any" {
		t.Errorf("typeName(Value) = %q", got)
	}
	if got := tm.typeName(reflect.TypeFor[complex64](), nil); got != "complex64" {
		t.Errorf("typeName(complex64) = %q", got)
	}
	if got := fitScalar("a", "{"); !slices.Equal(got, []string{"a {"}) {
		t.Errorf("fitScalar of what is not JSON: %q", got)
	}
	if got := fitScalar("a", "{}"); !slices.Equal(got, []string{"a {}"}) {
		t.Errorf("fitScalar of {}: %q", got)
	}
	if got := fitWords(nil); got != nil {
		t.Errorf("fitWords(nil) = %q", got)
	}
}
