package schema

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

var templateDirs = map[string]string{"json": "../templates/json/conf.d", "yaml": "../templates/yaml/conf.d", "fit": "../templates/konf/conf.d"}

// TestTemplateDirUpToDate fails when the directory templates fall behind
// the code, as TestTemplatesUpToDate does for the one-file ones.
func TestTemplateDirUpToDate(t *testing.T) {
	for format, dir := range templateDirs {
		files, err := TemplateDir(format)
		if err != nil {
			t.Fatal(err)
		}
		if *update {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
		for name, got := range files {
			path := filepath.Join(dir, name)
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s is out of date; run: go test ./schema -update", path)
			}
		}
		// No file the template no longer has.
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if rel, _ := filepath.Rel(dir, path); files[filepath.ToSlash(rel)] == nil {
					t.Errorf("%s is not in the template", path)
				}
			}
			return nil
		})
	}
	if _, err := TemplateDir("toml"); err == nil {
		t.Error("a directory template in toml")
	}
}

// TestTemplateDirLoads loads each directory template as fit run -c loads a
// directory, with its handlers' params decoded by their modules.
func TestTemplateDirLoads(t *testing.T) {
	for format, dir := range templateDirs {
		srcs, err := adapter.ReadSources([]string{dir})
		if err != nil {
			t.Fatal(err)
		}
		set, err := adapter.AdaptSet(srcs)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		c, err := config.Load(set.JSON)
		if err != nil {
			t.Fatalf("%s: %v", format, set.MapErrors(err))
		}
		var hosts []string
		for _, r := range c.Routes {
			hosts = append(hosts, r.Match.Host...)
			info, _ := module.Lookup(module.Handlers + "." + r.Handler.Module)
			if err := r.Handler.Params.Decode(info.New()); err != nil {
				t.Errorf("%s: %v", format, err)
			}
		}
		if len(c.Listeners) != 1 || len(c.Upstreams) != 1 || !slices.Contains(hosts, "example.com") || !slices.Contains(hosts, "api.example.com") {
			t.Errorf("%s: listeners %d, upstreams %d, hosts %v", format, len(c.Listeners), len(c.Upstreams), hosts)
		}
	}
}

// TestTemplateDirFields checks every field of the config is in the
// directory template, and every one but the lists in one file only.
func TestTemplateDirFields(t *testing.T) {
	in := map[string]int{}
	for _, f := range dirFiles {
		for _, name := range f.fields {
			in[name]++
		}
	}
	for _, f := range config.Fields(reflect.TypeFor[config.Config]()) {
		if f.Self {
			continue
		}
		switch n := in[f.Name]; {
		case n == 0:
			t.Errorf("%s is in no file of the directory template", f.Name)
		case n > 1 && !slices.Contains([]string{"listeners", "routes", "upstreams", "services", "providers"}, f.Name):
			t.Errorf("%s is in %d files of the directory template", f.Name, n)
		}
		delete(in, f.Name)
	}
	for name := range in {
		t.Errorf("the directory template names %s, which the config has not", name)
	}
}
