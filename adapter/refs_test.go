package adapter

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// refsx is an adapter whose files name others line by line, as
// "include <pattern>" and "import <pattern>".
func init() {
	Register("refsx", func(data []byte) (*Result, error) { return &Result{JSON: []byte("{}")}, nil }, ".refsx")
	RegisterFiles("refsx", func(data []byte) (imports, includes []string, err error) {
		for _, line := range strings.Split(string(data), "\n") {
			if p, ok := strings.CutPrefix(line, "include "); ok {
				includes = append(includes, p)
			} else if p, ok := strings.CutPrefix(line, "import "); ok {
				imports = append(imports, p)
			}
		}
		return imports, includes, nil
	}, nil)
}

func TestRegisterFilesUnknown(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "not registered") {
			t.Errorf("%v", r)
		}
	}()
	RegisterFiles("nope", nil, nil)
}

func TestRefPathAbsolute(t *testing.T) {
	abs, _ := filepath.Abs(filepath.Join(t.TempDir(), "x", "..", "a.conf"))
	if got := refPath("sites/b.conf", abs); got != filepath.Clean(abs) {
		t.Errorf("refPath = %q, want %q", got, filepath.Clean(abs))
	}
}

func TestReadLimitedErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := readLimited(filepath.Join(dir, "none"), 10); err == nil {
		t.Error("a missing file read")
	}
	if _, err := readLimited(dir, 10); err == nil {
		t.Error("a directory read")
	}
}

// TestFinderErrors checks what the finder of a config's files reports: too
// many files, by those given or those included, an import of a directory
// with no config files, and a file it cannot read, which adapting reports
// instead.
func TestFinderErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a.refsx", "include b.refsx\n")
	write("b.refsx", "")
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	imp := write("imp.refsx", "import empty\n")
	sub := filepath.Join(dir, "sub.refsx")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	full := &finder{files: make([]string, MaxFiles), seen: map[string]bool{}}
	if err := full.add(a, "refsx", false); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("past MaxFiles: %v", err)
	}
	almost := &finder{files: make([]string, MaxFiles-1), seen: map[string]bool{}}
	if err := almost.add(a, "refsx", false); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("past MaxFiles by an include: %v", err)
	}
	f := &finder{seen: map[string]bool{}}
	if err := f.add(imp, "refsx", false); err == nil || !strings.Contains(err.Error(), "import empty") {
		t.Errorf("an import of an empty directory: %v", err)
	}
	f = &finder{seen: map[string]bool{}}
	if err := f.add(sub, "refsx", false); err != nil || len(f.files) != 1 {
		t.Errorf("a file that cannot be read: %v %v", err, f.files)
	}
}

// TestFinderUnreadableDir checks a directory the walk cannot read fails.
func TestFinderUnreadableDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions do not keep this user out")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.conf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o755)
	if _, err := Files([]string{dir}); err == nil {
		t.Error("a directory that cannot be read walked")
	}
}
