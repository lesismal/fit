package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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
	wantFile := `{"config":{"listeners":[]}}`
	if string(file[:8]) != magic || file[len(magic)] != version || len(file) != headerLen+len(wantFile)+16 {
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
	file := s.seal(version, []byte(`{}`))
	os.WriteFile(path, file, 0o600)
	if _, _, err := Open(path, "secret"); err == nil || errors.Is(err, ErrOpen) {
		t.Errorf("a header asking for too much memory: %v", err)
	}
	file[len(magic)] = version + 1
	os.WriteFile(path, file, 0o600)
	if _, _, err := Open(path, "secret"); err == nil {
		t.Error("an unknown version opened")
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

func TestUsersRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, err := Open(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	cfg := []byte(`{"listeners":[]}`)
	if err := s.Write(cfg); err != nil {
		t.Fatal(err)
	}
	if got := s.Users(); len(got) != 0 {
		t.Fatalf("users of a store that kept none: %v", got)
	}
	users := []User{{Username: "ada", PasswordHash: "$2a$04$ada"}, {Username: "bob", PasswordHash: "$2a$04$bob"}}
	if err := s.SetUsers(users); err != nil {
		t.Fatal(err)
	}
	if got := s.Users(); !slices.Equal(got, users) {
		t.Errorf("remembered: %v", got)
	}
	// A fresh Open returns the same config and users.
	s2, data, err := Open(path, "secret")
	if err != nil || string(data) != string(cfg) {
		t.Fatalf("opened again: %v, %q", err, data)
	}
	if got := s2.Users(); !slices.Equal(got, users) {
		t.Errorf("opened users: %v", got)
	}
	// A Write keeps them.
	next := []byte(`{"listeners":[{"name":"web","address":":8080"}]}`)
	if err := s2.Write(next); err != nil {
		t.Fatal(err)
	}
	s3, data, err := Open(path, "secret")
	if err != nil || string(data) != string(next) {
		t.Fatalf("written again: %v, %q", err, data)
	}
	if got := s3.Users(); !slices.Equal(got, users) {
		t.Errorf("users after a write: %v", got)
	}
	// SetUsers changes them, keeping the config.
	one := []User{{Username: "ada", PasswordHash: "$2a$04$new"}}
	if err := s3.SetUsers(one); err != nil {
		t.Fatal(err)
	}
	s4, data, err := Open(path, "secret")
	if err != nil || string(data) != string(next) {
		t.Fatalf("after SetUsers: %v, %q", err, data)
	}
	if got := s4.Users(); !slices.Equal(got, one) {
		t.Errorf("users after SetUsers: %v", got)
	}
}

// TestSetUsersWithoutConfig: a store keeps users only beside a config.
func TestSetUsersWithoutConfig(t *testing.T) {
	s, _, err := Open(filepath.Join(t.TempDir(), "fit.store"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsers([]User{{Username: "ada"}}); err == nil {
		t.Error("users were kept by a store with no config")
	}
}

// TestOldVersionReadable: a version 1 store, whose payload is the config
// alone, still opens, and its next write is version 2.
func TestOldVersionReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, err := Open(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"listeners":[]}`)
	if err := os.WriteFile(path, s.seal(versionConfig, want), 0o600); err != nil {
		t.Fatal(err)
	}
	old, data, err := Open(path, "secret")
	if err != nil || string(data) != string(want) {
		t.Fatalf("a version 1 store: %v, %q", err, data)
	}
	if got := old.Users(); len(got) != 0 {
		t.Errorf("a version 1 store keeps no users: %v", got)
	}
	users := []User{{Username: "ada", PasswordHash: "$2a$04$ada"}}
	if err := old.SetUsers(users); err != nil {
		t.Fatal(err)
	}
	file, _ := os.ReadFile(path)
	if file[len(magic)] != version {
		t.Errorf("the file's version after a write: %d", file[len(magic)])
	}
	if again, data, err := Open(path, "secret"); err != nil || string(data) != string(want) || !slices.Equal(again.Users(), users) {
		t.Errorf("the version 2 store: %v, %q, %v", err, data, again.Users())
	}
}

// TestWriteNotJSON: the store embeds the config in a JSON envelope, so it
// refuses a config that is not JSON rather than making a file it cannot read.
func TestWriteNotJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, err := Open(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte("listeners: []")); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("a config that is not JSON: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the store was written: %v", err)
	}
}
