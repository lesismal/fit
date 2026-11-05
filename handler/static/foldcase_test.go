package static

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lesismal/fit/internal/dotfile"
)

// TestFoldCase checks that, on a root whose file system folds case, a
// request for a file with a script's extension, as a fastcgi handler's
// split_path names it, is answered 404 in any case, so that /x.PHP, which a
// \.php$ route misses, never serves x.php's source; and that a request
// whose path is not written as the names on disk are is answered 404 too,
// through every way the handler finds a file. On a root that does not fold
// case, nothing changes.
func TestFoldCase(t *testing.T) {
	dotfile.AddScripts([]string{".php"})
	root := tree(t, map[string]string{
		"x.php":            "<?php secret",
		"page.html":        "page",
		"Dir/a.txt":        "a",
		"php/index.php":    "<?php index",
		"spa/index.html":   "spa",
		"listed/item.txt":  "i",
		"precomp.js":       "js",
		"precomp.js.gz":    "gz",
		"shortname/x.html": "x",
	})
	folds := mustLoad(t, root, `, "index": ["index.php", "index.html"], "browse": true, "precompressed": ["gzip"]`)
	plain := mustLoad(t, root, `, "index": ["index.php", "index.html"], "browse": true`)
	if want := dotfile.FoldsCase(root); folds.foldCase != want {
		t.Fatalf("foldCase %v, want %v", folds.foldCase, want)
	}
	folds.foldCase, plain.foldCase = true, false
	// A root that folds case, as the test's file system may not: its
	// names on disk are the tree's.
	for _, target := range []string{"/x.php", "/x.PHP", "/x.Php", "/php/", "/DIR/a.txt", "/dir/a.txt", "/Dir/A.TXT", "/PAGE.html", "/Listed/", "/SPA/"} {
		if got := get(folds, "GET", target, nil).Response; got.StatusCode != 404 {
			t.Errorf("folding GET %s: %d %q, want 404", target, got.StatusCode, got.Body)
		}
	}
	for target, want := range map[string]string{"/page.html": "page", "/Dir/a.txt": "a", "/spa/": "spa"} {
		if got := get(folds, "GET", target, nil).Response; got.StatusCode != 200 || string(got.Body) != want {
			t.Errorf("folding GET %s: %d %q, want %q", target, got.StatusCode, got.Body, want)
		}
	}
	if got := get(folds, "GET", "/precomp.js", map[string]string{"Accept-Encoding": "gzip"}).Response; string(got.Body) != "gz" {
		t.Errorf("twin: %d %q", got.StatusCode, got.Body)
	}
	if got := get(folds, "GET", "/listed/", nil).Response; got.StatusCode != 200 {
		t.Errorf("listing: %d", got.StatusCode)
	}
	// try_files candidates are found the same way.
	spa := mustLoad(t, root, `, "try_files": ["{path}", "/spa/index.html"]`)
	spa.foldCase = true
	for _, target := range []string{"/x.PHP", "/PAGE.HTML"} {
		if got := get(spa, "GET", target, nil).Response; got.StatusCode != 200 || string(got.Body) != "spa" {
			t.Errorf("try_files GET %s: %d %q, want the fallback", target, got.StatusCode, got.Body)
		}
	}

	// Not folding, the handler serves what the file system opens, as
	// before.
	if got := get(plain, "GET", "/x.php", nil).Response; got.StatusCode != 200 {
		t.Errorf("not folding GET /x.php: %d", got.StatusCode)
	}

	// A name the file system opens for another, as an 8.3 short name on
	// Windows: onDisk stands for it here.
	defer func(f func(string, string) bool) { onDisk = f }(onDisk)
	onDisk = func(root, rel string) bool { return rel != "shortname/x.html" }
	fresh := mustLoad(t, root, "")
	fresh.foldCase = true
	if got := get(fresh, "GET", "/shortname/x.html", nil).Response; got.StatusCode != 404 {
		t.Errorf("a name not on disk: %d", got.StatusCode)
	}
}

// TestFoldCaseOnDisk checks the handler as provisioned, where the test's
// file system folds case, as macOS' and Windows' do by default: /x.PHP
// does not serve x.php.
func TestFoldCaseOnDisk(t *testing.T) {
	dotfile.AddScripts([]string{".php"})
	root := tree(t, map[string]string{"x.php": "<?php secret", "page.html": "page"})
	if _, err := os.Stat(filepath.Join(root, "X.PHP")); err != nil {
		t.Skip("the file system does not fold case")
	}
	s := mustLoad(t, root, "")
	if !s.foldCase {
		t.Fatal("foldCase is not set")
	}
	for _, target := range []string{"/x.php", "/x.PHP", "/X.php", "/PAGE.HTML"} {
		if got := get(s, "GET", target, nil).Response; got.StatusCode != 404 {
			t.Errorf("GET %s: %d %q, want 404", target, got.StatusCode, got.Body)
		}
	}
	if got := get(s, "GET", "/page.html", nil).Response; got.StatusCode != 200 {
		t.Errorf("GET /page.html: %d", got.StatusCode)
	}
}
