package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileSourceSpread checks write spreads few records over the files it
// has records for, leaving the rest unmade, and writes a file's lines past
// its buffer in full.
func TestFileSourceSpread(t *testing.T) {
	dir := t.TempDir()
	s := &fileSource{dir: dir, files: 3, size: 16}
	// 4 records over 3 files are 2 a file: the third has none.
	if err := s.write(context.Background(), 0, 4); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{2, 2} {
		data, err := os.ReadFile(s.path(i))
		if err != nil || bytes.Count(data, []byte("\n")) != want {
			t.Errorf("file %d: %v, %q", i, err, data)
		}
	}
	if _, err := os.Stat(s.path(2)); !os.IsNotExist(err) {
		t.Errorf("file 2: %v", err)
	}

	big := &fileSource{dir: t.TempDir(), files: 1, size: 4096}
	const n = 600 // past a buffer of 1 MiB
	if err := big.write(context.Background(), 0, n); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(big.path(0))
	if err != nil || len(data) != n*4096 || bytes.Count(data, []byte("\n")) != n {
		t.Errorf("%v: %d bytes", err, len(data))
	}
	if !bytes.Equal(data[(n-1)*4096:], line(n, 4096)) {
		t.Error("the last line is not record n's")
	}
}

// TestEmptyDirFailures checks emptyDir fails on a directory it may not
// read, or whose entries it may not remove.
func TestEmptyDirFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads and removes what it likes")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "sub", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if err := emptyDir(locked); err == nil {
		t.Error("an entry it may not remove: no error")
	}
	if _, err := os.Stat(filepath.Join(locked, "sub")); err != nil {
		t.Error(err)
	}

	unreadable := filepath.Join(dir, "unreadable")
	if err := os.Mkdir(unreadable, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
	if err := emptyDir(unreadable); err == nil {
		t.Error("a directory it may not read: no error")
	}
}

// TestSteadyShorterThanASecond checks a steady phase too short to write a
// second's records writes none and measures no lag.
func TestSteadyShorterThanASecond(t *testing.T) {
	m := &meter{o: &options{steadyRate: 100, steadyFor: 500 * time.Millisecond}}
	if lag, err := m.steady(context.Background(), nil, 0, 0); lag != nil || err != nil {
		t.Errorf("%+v %v", lag, err)
	}
}
