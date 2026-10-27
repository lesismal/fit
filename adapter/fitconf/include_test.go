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
		"fit.conf": `include listeners.conf
site a.example {
	include shared/site-*.conf
	route /api/* {
		include shared/proxy.inc
	}
}
include routes/*.conf
include none/*.conf
`,
		"listeners.conf":         "listener web { address :8080 }\n",
		"shared/site-a.conf":     "listeners web\n",
		"shared/site-b.conf":     "middleware compress\n",
		"shared/proxy.inc":       "include headers.inc\nhandler proxy { upstream app }\n",
		"shared/headers.inc":     "middleware headers { response { delete Server } }\n",
		"routes/1-hello.conf":    "route hello { handler respond { body hi } }\n",
		"routes/2-notfound.conf": "route not-found {\n\tpriority -1\n\thandler respond { status 404 }\n}\n",
	})
	set, err := loadTree(t, dir, "fit.conf")
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
		"fit.conf":          "import listeners.yaml sites\nroute last { priority -1; handler respond }\n",
		"listeners.yaml":    "listeners:\n  - name: web\n    address: :8080\n",
		"sites/a.conf":      "site a.example {\n\troute { include ../shared/ok.conf }\n}\n",
		"sites/b.json":      `{"routes": [{"name": "b", "handler": {"module": "respond"}}]}`,
		"sites/shared.conf": "handler respond { body shared }\n",
		"sites/c.conf":      "site c.example {\n\troute { include shared.conf }\n}\n",
		"shared/ok.conf":    "handler respond { body ok }\n",
		"shared/other.conf": "this is never read\n",
	})
	files, err := adapter.Files([]string{filepath.Join(dir, "fit.conf")})
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
	if want := []string{"fit.conf", "listeners.yaml", "sites/a.conf", "shared/ok.conf", "sites/b.json", "sites/c.conf", "sites/shared.conf"}; !slices.Equal(rel, want) {
		t.Errorf("files %v, want %v", rel, want)
	}
	set, err := loadTree(t, dir, "fit.conf")
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
		{"missing", map[string]string{"fit.conf": "include nope.conf\n"}, "fit.conf:1:9: include nope.conf: {dir}/nope.conf is not among the config's files read"},
		{"a def is not a file", map[string]string{"fit.conf": "def a { }\ninclude a\n"}, "fit.conf:2:9: include a: {dir}/a is not among the config's files read"},
		{"use of a file", map[string]string{"fit.conf": "use a.inc\n", "a.inc": ""}, "fit.conf:1:5: use a.inc: there is no def a.inc defined before it"},
		{"use before the def", map[string]string{"fit.conf": "use a\ndef a { }\n"}, "fit.conf:1:5: use a: there is no def a defined before it"},
		{"import missing", map[string]string{"fit.conf": "import nope.conf\n"}, "fit.conf: import nope.conf: "},
		{"itself", map[string]string{"fit.conf": "include fit.conf\n"}, "includes itself"},
		{"each other", map[string]string{"fit.conf": "include a.conf\n", "a.conf": "include b.conf\n", "b.conf": "include a.conf\n"},
			"includes itself: " + "{dir}/a.conf includes {dir}/b.conf includes {dir}/a.conf"},
		{"import in a block", map[string]string{"fit.conf": "route { import a.conf }\n", "a.conf": ""}, "fit.conf:1:9: import is given at the top of a config file only"},
		{"import in a file included", map[string]string{"fit.conf": "include a.inc\n", "a.inc": "import b.conf\n", "b.conf": ""}, "a.inc:1:1: import is given at the top of a config file only"},
		{"no files", map[string]string{"fit.conf": "include\n"}, "fit.conf:1:1: include takes the files to include"},
		{"a block", map[string]string{"fit.conf": "include a.conf { }\n", "a.conf": ""}, "fit.conf:1:1: include takes the files to include"},
		{"bad glob", map[string]string{"fit.conf": "include [\n"}, "include ["},
		{"in the file included", map[string]string{"fit.conf": "route a {\n\tinclude a.inc\n}\n", "a.inc": "handler respond\nhandler respond\n"},
			"a.inc:2:1: handler is given twice"},
		{"lexed in the file included", map[string]string{"fit.conf": "include a.inc\n", "a.inc": "\n\"open\n"}, "a.inc:2:1: a quote is not closed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := writeTree(t, c.files)
			_, err := loadTree(t, dir, "fit.conf")
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
		"fit.conf":  "schema_version 1\nlistener web { address :8080 }\nroute a {\n\tinclude m.inc\n\thandler respond\n}\n",
		"m.inc":     "\nmatch { bogus 1 }\n",
		"b.conf":    "include v.inc\n",
		"v.inc":     "\nschema_version 2\n",
		"more.conf": "include ok.inc\n",
		"ok.inc":    "route c { handler respond }\n",
	})
	set, err := loadTree(t, dir, "fit.conf", "more.conf")
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(set.JSON)
	if got, want := set.MapErrors(err).Error(), filepath.Join(dir, "m.inc")+":2:9: routes[0].match.bogus: unknown field"; !strings.Contains(got, want) {
		t.Errorf("no %q in\n%s", want, got)
	}
	// A field two files give, one in a file it includes.
	_, err = loadTree(t, dir, "fit.conf", "b.conf")
	if want := filepath.Join(dir, "v.inc") + ":2:16: schema_version: differs from the one in"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%v, want %q", err, want)
	}
}

// TestIncludeNeedsAFile checks that a config read without its path cannot
// include, and that the words its includes add are bounded.
func TestIncludeNeedsAFile(t *testing.T) {
	if _, err := Adapt([]byte("include a.conf\n")); err == nil || !strings.Contains(err.Error(), "include reads other files") {
		t.Errorf("%v", err)
	}
	if _, err := Adapt([]byte("import a.conf\n")); err == nil || !strings.Contains(err.Error(), "import reads other files") {
		t.Errorf("%v", err)
	}
	// Each file includes the next twice: 2^15 copies of the last, past
	// the bound on words.
	files := map[string]string{"fit.conf": "include f00.inc\n"}
	for i := range 16 {
		next := strings.Repeat("x ", 100) + "\n"
		if i < 15 {
			n := incName(i + 1)
			next = "include " + n + " " + n + "\n"
		}
		files[incName(i)] = next
	}
	dir := writeTree(t, files)
	_, err := loadTree(t, dir, "fit.conf")
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("%v, want the bound", err)
	}
}

func incName(i int) string {
	return "f" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + ".inc"
}

// TestDefFiles checks that a def defined in a file included is used by its
// name after it, and that the files a def includes are found from the file
// that defined it.
func TestDefFiles(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"fit.conf":        "include lib/defs.conf\nupstream app { addresses 10.0.0.1:80; use pool }\nroute a { use secure; handler respond }\n",
		"lib/defs.conf":   "def pool { max_conns 8 }\ndef secure { include headers.inc }\n",
		"lib/headers.inc": "middleware headers { response { delete Server } }\n",
	})
	set, err := loadTree(t, dir, "fit.conf")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"upstreams": [{"name": "app", "addresses": ["10.0.0.1:80"], "max_conns": 8}],
		"routes": [{"name": "a", "middleware": [{"module": "headers", "params": {"response": {"delete": ["Server"]}}}], "handler": {"module": "respond"}}]}`
	if !same(t, set.JSON, []byte(want)) {
		t.Errorf("got\n%s", set.JSON)
	}
}
