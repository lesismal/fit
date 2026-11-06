package workbench

import (
	"encoding/json"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The page loads every script and style of the bundle, and nothing the
// bundle lacks.
func TestPages(t *testing.T) {
	p := Pages()
	index, err := fs.ReadFile(p, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	loaded := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllStringSubmatch(string(index), -1) {
		ref := m[1]
		if strings.HasPrefix(ref, "data:") {
			continue
		}
		if _, err := fs.Stat(p, ref); err != nil {
			t.Errorf("index.html loads %s: %v", ref, err)
		}
		loaded[ref] = true
	}
	// The vendored files are loaded by a page when it needs them, as
	// gateway/media.js loads vendor/hls.min.js for a player: each script
	// is named by a script of the bundle, and comes with its license.
	var scripts strings.Builder
	if err := fs.WalkDir(p, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".js") && !strings.HasPrefix(path, "vendor/") {
			b, err := fs.ReadFile(p, path)
			scripts.Write(b)
			return err
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := fs.WalkDir(p, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path == "index.html" {
			return err
		}
		if strings.HasPrefix(path, "vendor/") {
			if js, ok := strings.CutSuffix(path, ".min.js"); ok {
				if !strings.Contains(scripts.String(), "'"+path+"'") {
					t.Errorf("%s is loaded by no script of the bundle", path)
				}
				if _, err := fs.Stat(p, js+".LICENSE.txt"); err != nil {
					t.Errorf("%s comes without its license: %v", path, err)
				}
			}
			return nil
		}
		if !loaded[path] {
			t.Errorf("%s is not loaded by index.html", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The core comes first: the library in the head, then the workbench,
	// then the modules that register with it.
	s := string(index)
	ui, wb := strings.Index(s, `src="core/ui.js"`), strings.Index(s, `src="core/workbench.js"`)
	if ui < 0 || wb < ui {
		t.Fatal("core/ui.js and core/workbench.js are not loaded first")
	}
	for ref := range loaded {
		if strings.HasSuffix(ref, ".js") && !strings.HasPrefix(ref, "core/") && strings.Index(s, `src="`+ref+`"`) < wb {
			t.Errorf("%s is loaded before core/workbench.js", ref)
		}
	}
}

// HLSJS is hls.js, vendored once for every page that plays HLS.
func TestHLSJS(t *testing.T) {
	js, license := HLSJS()
	if len(js) < 100<<10 || !strings.Contains(string(license), "Apache License") {
		t.Fatalf("hls.js: %d bytes, license %q", len(js), license)
	}
}

// gateway/schema-zh.js holds the Chinese of every description of the
// config's schema, schema/fit.schema.json, and nothing else; config.js
// words the renamed fields' own.
func TestSchemaZh(t *testing.T) {
	src, err := fs.ReadFile(Pages(), "gateway/schema-zh.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	start, end := strings.Index(s, "window.FitSchemaZh = "), strings.LastIndex(s, ";")
	if start < 0 || end < start {
		t.Fatal("gateway/schema-zh.js does not set window.FitSchemaZh")
	}
	var zh map[string]string
	if err := json.Unmarshal([]byte(s[start+len("window.FitSchemaZh = "):end]), &zh); err != nil {
		t.Fatalf("gateway/schema-zh.js: %v", err)
	}
	raw, err := os.ReadFile("../../schema/fit.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	docs := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if d, ok := v["description"].(string); ok && v["deprecated"] != true {
				docs[d] = true
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(schema)
	for d := range docs {
		if zh[d] == "" {
			t.Errorf("gateway/schema-zh.js lacks %q", d)
		}
	}
	for d := range zh {
		if !docs[d] {
			t.Errorf("gateway/schema-zh.js holds %q, which the schema no longer has", d)
		}
	}
	if got := SchemaZh(); len(got) != len(zh) || got["A console user's name."] != zh["A console user's name."] {
		t.Errorf("SchemaZh has %d descriptions, the file %d", len(got), len(zh))
	}
}
