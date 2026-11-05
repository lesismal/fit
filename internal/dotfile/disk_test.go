package dotfile

import (
	"os"
	"path/filepath"
	"testing"
)

// folds reports whether the file system dir is on opens a file by another
// case of its name, by trying it.
func folds(t *testing.T, dir string) bool {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(dir, "probe"))
	_, err := os.Stat(filepath.Join(dir, "PROBE"))
	return err == nil
}

func TestFoldsCase(t *testing.T) {
	want := folds(t, t.TempDir())
	// By a name in the root.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Index.html"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "123"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FoldsCase(root); got != want {
		t.Errorf("a root with names: %v, want %v", got, want)
	}
	// An empty root: by the names on its path.
	empty := filepath.Join(t.TempDir(), "1", "2")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FoldsCase(empty); got != want {
		t.Errorf("an empty root: %v, want %v", got, want)
	}
	// No name to look at: the system's default.
	if got := FoldsCase(filepath.Join(string(filepath.Separator), "nonexistent-fit-root", "1", "2")); got != foldsByDefault {
		t.Errorf("no names: %v", got)
	}
}

func TestOnDisk(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Sub", "x.php"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]bool{
		"":              true,
		"Sub":           true,
		"Sub/x.php":     true,
		"./Sub//x.php":  true,
		"Sub/":          true,
		"sub/x.php":     false,
		"Sub/x.PHP":     false,
		"Sub/X.php":     false,
		"nope/x.php":    false,
		"Sub/x.php/y":   false,
		"Sub/x.php.bak": false,
	} {
		if got := OnDisk(root, rel); got != want {
			t.Errorf("OnDisk(%q): %v, want %v", rel, got, want)
		}
	}
	if OnDisk(filepath.Join(root, "gone"), "a") {
		t.Error("a root that is not there")
	}
}

func TestScripts(t *testing.T) {
	if Script("x.zz9") {
		t.Fatal("nothing recorded, yet a script")
	}
	AddScripts([]string{".zz9", "", ".zz9"})
	AddScripts([]string{".zz9"})
	AddScripts([]string{".yy9"})
	for name, want := range map[string]bool{
		"/x.zz9": true, "/x.ZZ9": true, "/a/b.Yy9": true, "zz9": false, "/x.zz9.txt": false, "": false,
	} {
		if got := Script(name); got != want {
			t.Errorf("Script(%q): %v, want %v", name, got, want)
		}
	}
	if n := len(*scripts.Load()); n != 2 {
		t.Errorf("%d extensions recorded, want 2", n)
	}
}
