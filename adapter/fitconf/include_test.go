package fitconf

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lesismal/fit/adapter"
	_ "github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/config"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// loadTree reads and adapts the config paths, under dir, name.
func loadTree(t *testing.T, dir string, paths ...string) (*adapter.Set, error) {
	t.Helper()
	for i, p := range paths {
		paths[i] = filepath.Join(dir, p)
	}
	srcs, err := adapter.ReadSources(paths)
	if err != nil {
		return nil, err
	}
	return adapter.AdaptSet(srcs)
}

// TestInclude checks that include reads a file's directives in its place:
// at the top, in a site and in a route, by a path or a glob, relative to
// the file including it, and nested.
func TestInclude(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"fit.konf": `include listeners.konf
site a.example {
	include snippets/site-*.konf
	route /api/* {
		include snippets/proxy.inc
	}
}
include routes/*.konf
include none/*.konf
`,
		"listeners.konf":         "listener web { address :8080 }\n",
		"snippets/site-a.konf":   "listeners web\n",
		"snippets/site-b.konf":   "middleware compress\n",
		"snippets/proxy.inc":     "include headers.inc\nhandler proxy { upstream app }\n",
		"snippets/headers.inc":   "middleware headers { response { delete Server } }\n",
		"routes/1-hello.konf":    "route hello { handler respond { body hi } }\n",
		"routes/2-notfound.konf": "route not-found {\n\tpriority -1\n\thandler respond { status 404 }\n}\n",
	})
	set, err := loadTree(t, dir, "fit.konf")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"listeners": [{"name": "web", "address": ":8080"}],
		"routes": [
			{"listeners": ["web"], "match": {"host": ["a.example"], "path": ["/api/*"]},
				"middleware": [{"module": "compress"}, {"module": "headers", "params": {"response": {"delete": ["Server"]}}}],
				"handler": {"module": "proxy", "params": {"upstream": "app"}}},
			{"name": "hello", "handler": {"module": "respond", "params": {"body": "hi"}}},
			{"name": "not-found", "priority": -1, "handler": {"module": "respond", "params": {"status": 404}}}],
		"upstreams": [{"name": "app", "addresses": ["127.0.0.1:9000"]}]}`
	if len(set.Files) != 1 {
		t.Errorf("parts %v, want the one file", set.Files)
	}
	// The upstream the proxy names is the test's own, to load the config.
	data := append([]byte(nil), set.JSON...)
	data = append(data[:len(data)-1], []byte(`,"upstreams":[{"name":"app","addresses":["127.0.0.1:9000"]}]}`)...)
	if !same(t, data, []byte(want)) {
		t.Errorf("got\n%s", data)
	}
	if _, err := config.Load(data); err != nil {
		t.Errorf("load: %v", err)
	}
}

// TestImport checks that import merges whole config files, in any format,
// after the file importing them, as -c does, and that a file included is
// read only where it is included, also when a directory imported holds it.
func TestImport(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"fit.konf":            "import listeners.yaml sites\nroute last { priority -1; handler respond }\n",
		"listeners.yaml":      "listeners:\n  - name: web\n    address: :8080\n",
		"sites/a.konf":        "site a.example {\n\troute { include ../snippets/ok.konf }\n}\n",
		"sites/b.json":        `{"routes": [{"name": "b", "handler": {"module": "respond"}}]}`,
		"sites/shared.konf":   "handler respond { body shared }\n",
		"sites/c.konf":        "site c.example {\n\troute { include shared.konf }\n}\n",
		"snippets/ok.konf":    "handler respond { body ok }\n",
		"snippets/other.konf": "this is never read\n",
	})
	files, err := adapter.Files([]string{filepath.Join(dir, "fit.konf")})
	if err != nil {
		t.Fatal(err)
	}
	var rel []string
	for _, f := range files {
		r, _ := filepath.Rel(dir, f)
		rel = append(rel, filepath.ToSlash(r))
	}
	// The files watched: those imported and those included, each after
	// the file naming it.
	if want := []string{"fit.konf", "listeners.yaml", "sites/a.konf", "snippets/ok.konf", "sites/b.json", "sites/c.konf", "sites/shared.konf"}; !slices.Equal(rel, want) {
		t.Errorf("files %v, want %v", rel, want)
	}
	set, err := loadTree(t, dir, "fit.konf")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Files) != 5 {
		t.Errorf("parts %v, want 5: the included ones are not", set.Files)
	}
	want := `{"routes": [
			{"name": "last", "priority": -1, "handler": {"module": "respond"}},
			{"match": {"host": ["a.example"]}, "handler": {"module": "respond", "params": {"body": "ok"}}},
			{"name": "b", "handler": {"module": "respond"}},
			{"match": {"host": ["c.example"]}, "handler": {"module": "respond", "params": {"body": "shared"}}}],
		"listeners": [{"name": "web", "address": ":8080"}]}`
	if !same(t, set.JSON, []byte(want)) {
		t.Errorf("got\n%s", set.JSON)
	}
	if _, err := config.Load(set.JSON); err != nil {
		t.Errorf("load: %v", set.MapErrors(err))
	}
}

// TestIncludeErrors checks that what is wrong with an include or an import
// is said, at its place, and an error in a file included names that file.
func TestIncludeErrors(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"missing", map[string]string{"fit.konf": "include nope.konf\n"}, "fit.konf:1:9: include nope.konf: there is no snippet nope.konf defined before it, nor such a file"},
		{"import missing", map[string]string{"fit.konf": "import nope.konf\n"}, "fit.konf: import nope.konf: "},
		{"itself", map[string]string{"fit.konf": "include fit.konf\n"}, "includes itself"},
		{"each other", map[string]string{"fit.konf": "include a.konf\n", "a.konf": "include b.konf\n", "b.konf": "include a.konf\n"},
			"includes itself: " + "{dir}/a.konf includes {dir}/b.konf includes {dir}/a.konf"},
		{"import in a block", map[string]string{"fit.konf": "route { import a.konf }\n", "a.konf": ""}, "fit.konf:1:9: import is given at the top of a config file only"},
		{"import in a file included", map[string]string{"fit.konf": "include a.inc\n", "a.inc": "import b.konf\n", "b.konf": ""}, "a.inc:1:1: import is given at the top of a config file only"},
		{"no files", map[string]string{"fit.konf": "include\n"}, "fit.konf:1:1: include takes the snippets or the files to include"},
		{"a block", map[string]string{"fit.konf": "include a.konf { }\n", "a.konf": ""}, "fit.konf:1:1: include takes the snippets or the files to include"},
		{"bad glob", map[string]string{"fit.konf": "include [\n"}, "include ["},
		{"in the file included", map[string]string{"fit.konf": "route a {\n\tinclude a.inc\n}\n", "a.inc": "handler respond\nhandler respond\n"},
			"a.inc:2:1: handler is given twice"},
		{"lexed in the file included", map[string]string{"fit.konf": "include a.inc\n", "a.inc": "\n\"open\n"}, "a.inc:2:1: a quote is not closed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := writeTree(t, c.files)
			_, err := loadTree(t, dir, "fit.konf")
			want := strings.ReplaceAll(c.want, "{dir}", dir)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%v, want %q", err, want)
			}
		})
	}
}

// TestIncludePositions checks that the config's errors about a value
// written in a file included point at that file.
func TestIncludePositions(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"fit.konf":  "schema_version 1\nlistener web { address :8080 }\nroute a {\n\tinclude m.inc\n\thandler respond\n}\n",
		"m.inc":     "\nmatch { bogus 1 }\n",
		"b.konf":    "include v.inc\n",
		"v.inc":     "\nschema_version 2\n",
		"more.konf": "include ok.inc\n",
		"ok.inc":    "route c { handler respond }\n",
	})
	set, err := loadTree(t, dir, "fit.konf", "more.konf")
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(set.JSON)
	if got, want := set.MapErrors(err).Error(), filepath.Join(dir, "m.inc")+":2:9: routes[0].match.bogus: unknown field"; !strings.Contains(got, want) {
		t.Errorf("no %q in\n%s", want, got)
	}
	// A field two files give, one in a file it includes.
	_, err = loadTree(t, dir, "fit.konf", "b.konf")
	if want := filepath.Join(dir, "v.inc") + ":2:16: schema_version: differs from the one in"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%v, want %q", err, want)
	}
}

// TestIncludeNeedsAFile checks that a config read without its path cannot
// include, and that the words its includes add are bounded.
func TestIncludeNeedsAFile(t *testing.T) {
	if _, err := Adapt([]byte("include a.konf\n")); err == nil || !strings.Contains(err.Error(), "include reads other files") {
		t.Errorf("%v", err)
	}
	if _, err := Adapt([]byte("import a.konf\n")); err == nil || !strings.Contains(err.Error(), "import reads other files") {
		t.Errorf("%v", err)
	}
	// Each file includes the next twice: 2^15 copies of the last, past
	// the bound on words.
	files := map[string]string{"fit.konf": "include f00.inc\n"}
	for i := range 16 {
		next := strings.Repeat("x ", 100) + "\n"
		if i < 15 {
			n := incName(i + 1)
			next = "include " + n + " " + n + "\n"
		}
		files[incName(i)] = next
	}
	dir := writeTree(t, files)
	_, err := loadTree(t, dir, "fit.konf")
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("%v, want the bound", err)
	}
}

func incName(i int) string {
	return "f" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + ".inc"
}

// TestSnippetFiles checks that a snippet defined in a file included is
// included by its name after it, and that the files a snippet includes are
// found from the file that defined it.
func TestSnippetFiles(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"fit.konf":          "include lib/snippets.konf\nupstream app { addresses 10.0.0.1:80; include pool }\nroute a { include secure; handler respond }\n",
		"lib/snippets.konf": "snippet pool { max_conns 8 }\nsnippet secure { include headers.inc }\n",
		"lib/headers.inc":   "middleware headers { response { delete Server } }\n",
	})
	set, err := loadTree(t, dir, "fit.konf")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"upstreams": [{"name": "app", "addresses": ["10.0.0.1:80"], "max_conns": 8}],
		"routes": [{"name": "a", "middleware": [{"module": "headers", "params": {"response": {"delete": ["Server"]}}}], "handler": {"module": "respond"}}]}`
	if !same(t, set.JSON, []byte(want)) {
		t.Errorf("got\n%s", set.JSON)
	}
}
