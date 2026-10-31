package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func init() {
	// Cheap enough for tests; the format records them.
	DefaultParams = Params{Time: 1, MemoryKiB: 64, Threads: 1}
}

func TestWriteAndOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, data, err := Open(path, "secret")
	if err != nil || data != nil {
		t.Fatalf("a new store: %v, %q", err, data)
	}
	want := []byte(`{"listeners":[]}`)
	if err := s.Write(want); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("the store's mode: %v, %v", fi.Mode(), err)
		}
	}
	file, _ := os.ReadFile(path)
	if string(file[:8]) != magic || len(file) != headerLen+len(want)+16 {
		t.Fatalf("the file: %q", file)
	}
	_, got, err := Open(path, "secret")
	if err != nil || string(got) != string(want) {
		t.Fatalf("opened again: %v, %q", err, got)
	}

	// Each write takes a new nonce; the salt stays.
	if err := s.Write(want); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if string(again[saltOffset:saltOffset+saltLen]) != string(file[saltOffset:saltOffset+saltLen]) {
		t.Error("the salt changed")
	}
	if string(again[saltOffset+saltLen:headerLen]) == string(file[saltOffset+saltLen:headerLen]) {
		t.Error("the nonce did not change")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("files left beside the store: %v", entries)
	}
}

func TestWrongPasswordOrChangedByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, _ := Open(path, "secret")
	if err := s.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(path, "wrong"); !errors.Is(err, ErrOpen) {
		t.Errorf("a wrong password: %v", err)
	}
	file, _ := os.ReadFile(path)
	for _, i := range []int{9 + 1 + 4 + 4 + 1, saltOffset, headerLen - 1, headerLen, len(file) - 1} {
		changed := append([]byte(nil), file...)
		changed[i] ^= 1
		os.WriteFile(path, changed, 0o600)
		if _, _, err := Open(path, "secret"); err == nil {
			t.Errorf("byte %d changed: opened", i)
		}
	}
}

func TestNotAStore(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"json":  `{"listeners":[{"name":"web","address":":8080"}],"routes":[]}`,
		"short": "FITSTORE",
	} {
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte(content), 0o600)
		if _, _, err := Open(path, "secret"); !errors.Is(err, ErrNotStore) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestHeaderBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, _ := Open(path, "secret")
	s.params = Params{Time: 1, MemoryKiB: maxMemoryKiB + 1, Threads: 1}
	file := s.seal([]byte(`{}`))
	os.WriteFile(path, file, 0o600)
	if _, _, err := Open(path, "secret"); err == nil || errors.Is(err, ErrOpen) {
		t.Errorf("a header asking for too much memory: %v", err)
	}
	file[len(magic)] = 2
	os.WriteFile(path, file, 0o600)
	if _, _, err := Open(path, "secret"); err == nil {
		t.Error("version 2 opened")
	}
}

func TestReadAfterAnotherWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, _ := Open(path, "secret")
	if err := s.Write([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	// Another process imports a store: a new salt, the same password.
	other, _ := Create(path, "secret")
	if err := other.Write([]byte(`{"b":2}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(); err != nil || string(got) != `{"b":2}` {
		t.Fatalf("read after another store's write: %v, %q", err, got)
	}
	if err := s.Write([]byte(`{"c":3}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := other.Read(); err != nil || string(got) != `{"c":3}` {
		t.Fatalf("the other store reads it: %v, %q", err, got)
	}
}

func TestEmptyPassword(t *testing.T) {
	if _, _, err := Open(filepath.Join(t.TempDir(), "x"), ""); err == nil {
		t.Error("an empty password was taken")
	}
}
