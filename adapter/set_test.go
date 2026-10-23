package adapter_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lesismal/fit/adapter"
	_ "github.com/lesismal/fit/adapter/fitconf"
	_ "github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/config"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFiles checks which files a directory stands for: those of fit's
// formats, in its subdirectories too, in order, but hidden ones.
func TestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"b.konf": "", "a.json": "", "sites/c.yaml": "", "sites/d.YML": "", "notes.txt": "",
		".hidden.konf": "", ".git/x.konf": "", "Caddyfile": "",
	})
	got, err := adapter.Files([]string{dir, filepath.Join(dir, "b.konf"), filepath.Join(dir, "Caddyfile")})
	if err != nil {
		t.Fatal(err)
	}
	var rel []string
	for _, f := range got {
		r, _ := filepath.Rel(dir, f)
		rel = append(rel, filepath.ToSlash(r))
	}
	if want := []string{"a.json", "b.konf", "sites/c.yaml", "sites/d.YML", "Caddyfile"}; !reflect.DeepEqual(rel, want) {
		t.Errorf("%v, want %v", rel, want)
	}
	empty := filepath.Join(dir, "empty")
	_ = os.Mkdir(empty, 0o755)
	if _, err := adapter.Files([]string{empty}); err == nil || !strings.Contains(err.Error(), "no config files") {
		t.Errorf("an empty directory: %v", err)
	}
	if _, err := adapter.Files([]string{filepath.Join(dir, "missing.konf")}); !os.IsNotExist(err) {
		t.Errorf("a missing file: %v", err)
	}
}

func loadDir(t *testing.T, files map[string]string) (*adapter.Set, error) {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	srcs, err := adapter.ReadSources([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	return adapter.AdaptSet(srcs)
}

// TestMerge checks that the top-level lists of several files are joined in
// the files' order, in whatever format each is written.
func TestMerge(t *testing.T) {
	set, err := loadDir(t, map[string]string{
		"1-base.konf":  "schema_version 1\nlistener web {\n\taddress :8080\n}\nadmin {}\n",
		"2-api.konf":   "schema_version 1\nsite api.example {\n\troute /v1/* { handler respond { body v1 } }\n}\n",
		"3-www.yaml":   "routes:\n  - match: {host: [www.example]}\n    handler: {module: respond}\n",
		"4-more.json":  `{"listeners": [{"name": "other", "address": ":8081"}], /* a comment */ "routes": []}`,
		"5-empty.yaml": "{}\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(set.JSON, &got); err != nil {
		t.Fatalf("%v: %s", err, set.JSON)
	}
	if n := len(got["listeners"].([]any)); n != 2 {
		t.Errorf("%d listeners", n)
	}
	routes := got["routes"].([]any)
	if len(routes) != 2 || !strings.Contains(string(set.JSON), `"api.example"`) || got["schema_version"] != 1.0 {
		t.Errorf("%s", set.JSON)
	}
	if _, err := config.Load(set.JSON); err != nil {
		t.Errorf("%v", set.MapErrors(err))
	}
}

// TestMergeConflicts checks that a field other than a list is given by one
// file only, and that schema_version agrees.
func TestMergeConflicts(t *testing.T) {
	_, err := loadDir(t, map[string]string{"a.konf": "admin {}\n", "b.konf": "\nadmin {\n\taddress :1\n}\n"})
	if err == nil || !strings.Contains(err.Error(), "b.konf:2:1: admin: also given in ") || !strings.Contains(err.Error(), "a.konf") {
		t.Errorf("admin twice: %v", err)
	}
	_, err = loadDir(t, map[string]string{"a.konf": "schema_version 1\n", "b.json": `{"schema_version": 2}`})
	if err == nil || !strings.Contains(err.Error(), "b.json:1:20: schema_version: differs from the one in") {
		t.Errorf("schema_version: %v", err)
	}
	_, err = loadDir(t, map[string]string{"a.konf": "listener a {\n}\n", "b.konf": "listener b {\n}\n\nroute {\n\thandler\n}\n"})
	if err == nil || !strings.Contains(err.Error(), "b.konf:5:2: handler takes the name of a module") {
		t.Errorf("a file that does not adapt: %v", err)
	}
}

// TestMapErrors checks that an error in the merged config points at its
// file, its path there and its place.
func TestMapErrors(t *testing.T) {
	set, err := loadDir(t, map[string]string{
		"a.konf": "listener web {\n\taddress :8080\n}\nroute {\n\thandler respond\n}\n",
		"b.json": "{\n  \"routes\": [\n    {\"handler\": {\"module\": \"respond\"}},\n    {\"handler\": {\"module\": \"respond\", \"params\": {}}, \"bogus\": 1}\n  ]\n}\n",
		"c.yaml": "listeners:\n  - name: other\n    address: nowhere\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(set.JSON)
	if got, want := set.MapErrors(err).Error(), "b.json:4:63: routes[1].bogus: unknown field"; !strings.Contains(got, want) {
		t.Errorf("no %q in\n%s", want, got)
	}

	// Once the config decodes, its checks find the address, in the third
	// file.
	set, err = loadDir(t, map[string]string{
		"a.konf": "listener web {\n\taddress :8080\n}\nroute {\n\thandler respond\n}\n",
		"c.yaml": "listeners:\n  - name: other\n    address: nowhere\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(set.JSON)
	if got, want := set.MapErrors(err).Error(), "c.yaml:3:14: listeners[0].address"; !strings.Contains(got, want) {
		t.Errorf("no %q in\n%s", want, got)
	}
}
