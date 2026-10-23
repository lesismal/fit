package tlsmgr

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFileStorageLockNameTooLong: a lock whose file cannot be created for
// another reason than being held fails at once rather than waiting.
func TestFileStorageLockNameTooLong(t *testing.T) {
	s := &FileStorage{Root: t.TempDir()}
	err := s.Lock(context.Background(), strings.Repeat("a", 300))
	if err == nil || errors.Is(err, fs.ErrExist) {
		t.Errorf("a lock name too long for a file: %v", err)
	}
}

// TestFileStorageStoreUnwritable: a value whose directory takes no new
// files is not stored, and nothing is left there.
func TestFileStorageStoreUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory modes are tested on Unix")
	}
	if os.Geteuid() == 0 {
		t.Skip("root may write what a directory's mode forbids")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "a")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	s := &FileStorage{Root: root}
	if err := s.Store(context.Background(), "a/b", []byte("v")); err == nil {
		t.Error("a value was stored in a read-only directory")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left in the directory: %v", entries)
	}
}
