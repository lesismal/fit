package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHeaderStrength refuses a header asking for less than DefaultParams,
// as one made cheap to guess at would, or more than fit takes, as one made
// to be expensive would; neither opens as a wrong password.
func TestHeaderStrength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, _ := Open(path, "secret")
	for _, p := range []Params{
		{Time: DefaultParams.Time, MemoryKiB: DefaultParams.MemoryKiB / 2, Threads: 1},
		{Time: maxTime + 1, MemoryKiB: DefaultParams.MemoryKiB, Threads: 1},
		{Time: DefaultParams.Time, MemoryKiB: 8 * (maxThreads + 1), Threads: maxThreads + 1},
	} {
		s.params = p
		_ = os.WriteFile(path, s.seal(version, []byte(`{}`)), 0o600)
		if _, _, err := Open(path, "secret"); err == nil || errors.Is(err, ErrOpen) || !strings.Contains(err.Error(), "does not take") {
			t.Errorf("%+v: %v", p, err)
		}
	}
}

// TestTamperedHeaderNotKept: a header changed without the password, its
// settings within bounds but not those the file was sealed with, does not
// open, and the store's next Write seals with what it had, not with what the
// changed header asked for.
func TestTamperedHeaderNotKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	s, _, _ := Open(path, "secret")
	if err := s.Write([]byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := parseHeader(path, file)
	tampered := bytes.Clone(file)
	binary.BigEndian.PutUint32(tampered[len(magic)+2:], want.params.Time+1)
	tampered[saltOffset] ^= 0xff
	_ = os.WriteFile(path, tampered, 0o600)
	if _, err := s.Read(); !errors.Is(err, ErrOpen) {
		t.Fatalf("a tampered header: %v", err)
	}
	if err := s.Write([]byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	file, _ = os.ReadFile(path)
	if got, _ := parseHeader(path, file); got.params != want.params || got.salt != want.salt {
		t.Errorf("written with %+v, want %+v", got, want)
	}
	if _, data, err := Open(path, "secret"); err != nil || string(data) != `{"v":2}` {
		t.Errorf("reopened: %s %v", data, err)
	}
}
