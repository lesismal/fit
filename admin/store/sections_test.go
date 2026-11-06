package store

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSections: a section is kept beside the config and the users, makes the
// file version 3, survives every other write and a reopen, and removing the
// last one makes the file version 2 again.
func TestSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, err := Open(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSection("assistant", []byte(`{"a":1}`)); err == nil {
		t.Error("a section before any config was kept")
	}
	if err := s.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	fileVersion := func() byte {
		file, _ := os.ReadFile(path)
		return file[len(magic)]
	}
	if v := fileVersion(); v != version {
		t.Fatalf("a store without sections is version %d", v)
	}
	for _, bad := range []struct {
		name string
		data string
	}{{"", `{}`}, {strings.Repeat("n", MaxSectionName+1), `{}`}, {"x", `{`}} {
		if err := s.SetSection(bad.name, []byte(bad.data)); err == nil {
			t.Errorf("SetSection(%q, %q) was kept", bad.name, bad.data)
		}
	}
	if err := s.SetSection("assistant", []byte(`{"keys":{"p":"sk-1"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSection("other", []byte(`[1]`)); err != nil {
		t.Fatal(err)
	}
	if v := fileVersion(); v != versionSections {
		t.Fatalf("a store with sections is version %d", v)
	}
	users := []User{{Username: "ada", PasswordHash: "$2a$04$ada"}}
	if err := s.SetUsers(users); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte(`{"listeners":[]}`)); err != nil {
		t.Fatal(err)
	}
	got := s.Section("assistant")
	got[0] = 'X' // a copy: the store's own is untouched
	if string(s.Section("assistant")) != `{"keys":{"p":"sk-1"}}` || s.Section("none") != nil {
		t.Errorf("sections: %q, %q", s.Section("assistant"), s.Section("none"))
	}
	again, data, err := Open(path, "secret")
	if err != nil || string(data) != `{"listeners":[]}` || !slices.Equal(again.Users(), users) ||
		string(again.Section("assistant")) != `{"keys":{"p":"sk-1"}}` || string(again.Section("other")) != `[1]` {
		t.Fatalf("reopened: %v %q %v %q", err, data, again.Users(), again.Section("assistant"))
	}

	// An export holds no sections, and is version 2.
	exp, err := again.Seal(data, users)
	if err != nil || exp[len(magic)] != version || strings.Contains(string(exp), "sk-1") {
		t.Fatalf("export: %v, version %d", err, exp[len(magic)])
	}
	if _, u, err := again.Unseal(exp); err != nil || !slices.Equal(u, users) {
		t.Errorf("unseal: %v %v", err, u)
	}

	if err := again.SetSection("assistant", nil); err != nil {
		t.Fatal(err)
	}
	if err := again.SetSection("other", nil); err != nil {
		t.Fatal(err)
	}
	if v := fileVersion(); v != version {
		t.Errorf("a store whose sections went is version %d", v)
	}
	if _, _, err := Open(path, "secret"); err != nil {
		t.Error(err)
	}
}

// TestSectionsOnlyInVersion3: a version 2 payload that happens to hold
// sections, as a hand-made file might, has them ignored.
func TestSectionsOnlyInVersion3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, err := Open(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, s.seal(version, []byte(`{"config":{},"sections":{"a":1}}`)), 0o600); err != nil {
		t.Fatal(err)
	}
	old, _, err := Open(path, "secret")
	if err != nil || old.Section("a") != nil {
		t.Errorf("a version 2 file's sections: %v %q", err, old.Section("a"))
	}
}

// TestSectionPastTheBound: a section that would make the store larger than
// it may be is refused, and the store keeps what it held.
func TestSectionPastTheBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, _ := Open(path, "secret")
	if err := s.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSection("big", []byte(`"`+strings.Repeat("a", MaxBytes)+`"`)); err == nil {
		t.Fatal("a section past the bound was kept")
	}
	if s.Section("big") != nil {
		t.Error("the refused section is remembered")
	}
	if _, data, err := Open(path, "secret"); err != nil || string(data) != `{}` {
		t.Errorf("after the refused section: %v %q", err, data)
	}
}
