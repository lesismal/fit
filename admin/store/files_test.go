package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileErrors checks the stores whose files cannot be read or written:
// a directory, one too large, one gone, one of an unknown key derivation,
// and a directory where the file goes.
func TestFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Open(dir, "secret"); err == nil {
		t.Error("a directory opened")
	}
	if _, err := Create(dir, ""); err == nil {
		t.Error("created without a password")
	}

	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, int64(MaxBytes+headerLen+100)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(big, "secret"); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a file too large: %v", err)
	}

	path := filepath.Join(dir, "fit.store")
	s, _, err := Open(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("reading a store never written: %v", err)
	}
	if err := s.Write([]byte("listeners: []")); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("writing something that is not JSON: %v", err)
	}
	// The users are sealed beside the config, within the same bound.
	tooBig := []byte(`"` + strings.Repeat("a", MaxBytes) + `"`)
	if err := s.Write(tooBig); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("writing too much: %v", err)
	}
	file := s.seal(version, []byte(`{}`))
	file[len(magic)+1] = 2
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(path, "secret"); err == nil || !strings.Contains(err.Error(), "method 2") {
		t.Errorf("an unknown key derivation: %v", err)
	}
	// The store remembers its config, so that users may be kept beside it.
	if err := s.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	// The file's place is a directory: the temporary file is not renamed
	// over it, and goes.
	taken := filepath.Join(dir, "taken")
	if err := os.MkdirAll(filepath.Join(taken, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	s.path = taken
	if err := s.Write([]byte(`{}`)); err == nil {
		t.Error("written over a directory")
	}
	if err := s.SetUsers([]User{{Username: "ada"}}); err == nil {
		t.Error("users written over a directory")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".taken.*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	s.path = filepath.Join(dir, "none", "fit.store")
	if err := s.Write([]byte(`{}`)); err == nil {
		t.Error("written into a directory that is not there")
	}
}
