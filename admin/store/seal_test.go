package store

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSealUnseal: a file sealed with a password opens with it, with the
// users beside the config, and not with another; one sealed with a store's
// key opens with the store's password.
func TestSealUnseal(t *testing.T) {
	users := []User{{Username: "ada", PasswordHash: "$2a$04$ada"}}
	file, err := Seal("export-pw", []byte(`{"routes": []}`), users)
	if err != nil {
		t.Fatal(err)
	}
	data, got, err := Unseal(file, "export-pw")
	if err != nil || string(data) != `{"routes": []}` || !slices.Equal(got, users) {
		t.Fatalf("unsealed: %v %q %v", err, data, got)
	}
	if _, _, err := Unseal(file, "other"); !errors.Is(err, ErrOpen) {
		t.Errorf("another password: %v", err)
	}
	s, _, err := Open(filepath.Join(t.TempDir(), "fit.store"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	file, err = s.Seal([]byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, got, err := s.Unseal(file); err != nil || string(data) != `{}` || len(got) != 0 {
		t.Errorf("a store's own export: %v %q %v", err, data, got)
	}
	if _, _, err := Unseal(file, "secret"); err != nil {
		t.Errorf("a store's export with its password: %v", err)
	}
}

// TestSealUnsealRefused: nothing is sealed or opened without a password, a
// config that is not JSON is not sealed, and a file larger than a store may
// be is not read.
func TestSealUnsealRefused(t *testing.T) {
	if _, err := Seal("", []byte(`{}`), nil); err == nil || !strings.Contains(err.Error(), "password is empty") {
		t.Errorf("seal without a password: %v", err)
	}
	if _, err := Seal("pw", []byte(`listeners: []`), nil); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("seal what is not JSON: %v", err)
	}
	if _, _, err := Unseal([]byte("FITSTORE"), ""); err == nil || !strings.Contains(err.Error(), "password is empty") {
		t.Errorf("unseal without a password: %v", err)
	}
	big := make([]byte, MaxBytes+headerLen+100)
	if _, _, err := Unseal(big, "pw"); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("unseal a file too large: %v", err)
	}
	if _, _, err := Unseal([]byte("not a store at all, but long enough to have a header and a tag"), "pw"); !errors.Is(err, ErrNotStore) {
		t.Errorf("unseal what is not a store: %v", err)
	}
}

// TestPayloadWithoutConfig: a version 2 file whose payload holds no config,
// or is not the payload's JSON, does not open, from a file or as an export.
func TestPayloadWithoutConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, err := Open(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"users": []}`, `[1]`, `not JSON`} {
		file := s.seal(version, []byte(payload))
		if _, _, err := Unseal(file, "secret"); err == nil || !strings.Contains(err.Error(), "holds no config") {
			t.Errorf("an export of %s: %v", payload, err)
		}
		if _, err := s.open(file); err == nil || !strings.Contains(err.Error(), "holds no config") {
			t.Errorf("a store of %s: %v", payload, err)
		}
	}
}

// TestUsersPastTheBound: a config as large as a store holds, with users
// beside it, is more than a store holds, and is not written.
func TestUsersPastTheBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, err := Open(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsers([]User{{Username: "ada", PasswordHash: "$2a$04$ada"}}); err != nil {
		t.Fatal(err)
	}
	full := []byte(`"` + strings.Repeat("a", MaxBytes-2) + `"`)
	if err := s.Write(full); err == nil || !strings.Contains(err.Error(), "the config and its users are larger than") {
		t.Errorf("a full config with users: %v", err)
	}
	if data, err := s.Read(); err != nil || string(data) != `{}` {
		t.Errorf("the store after the refused write: %v %q", err, data)
	}
}
