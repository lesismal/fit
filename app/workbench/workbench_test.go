package workbench

import (
	"io/fs"
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
	if err := fs.WalkDir(p, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path == "index.html" {
			return err
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
