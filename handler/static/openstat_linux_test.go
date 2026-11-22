package static

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lesismal/fib/filewatch"
)

func TestOpenStat(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(file, make([]byte, 20<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o640|fs.ModeSetgid); err != nil {
		t.Fatal(err)
	}

	info, f, ok, err := openStat(file)
	if !ok || err != nil || f == nil {
		t.Fatalf("regular file: ok %v, err %v, file %v", ok, err, f)
	}
	want, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name() != want.Name() || info.Size() != want.Size() || info.Mode() != want.Mode() ||
		!info.ModTime().Equal(want.ModTime()) || info.IsDir() || filewatch.StampOf(info) != filewatch.StampOf(want) {
		t.Fatalf("regular file: %v %v %v %v, want %v %v %v %v", info.Name(), info.Size(), info.Mode(), info.ModTime(), want.Name(), want.Size(), want.Mode(), want.ModTime())
	}
	head := make([]byte, 4)
	if n, err := f.ReadAt(head, 0); n != 4 || err != nil {
		t.Fatalf("read: %d %v", n, err)
	}
	if !f.stands(20 << 10) {
		t.Fatal("the opened file does not stand")
	}
	f.Close()

	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, f, ok, _ := openStat(link); ok || f != nil {
		t.Fatalf("a symbolic link is not handed back to statLink: ok %v", ok)
	}

	if _, _, ok, err := openStat(filepath.Join(dir, "none")); !ok || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: ok %v, err %v", ok, err)
	}
	if _, _, ok, err := openStat(filepath.Join(file, "x")); !ok || err == nil {
		t.Fatalf("a path through a file: ok %v, err %v", ok, err)
	}

	info, f, ok, err = openStat(dir)
	if !ok || err != nil || f != nil || !info.IsDir() || !info.Mode().IsDir() {
		t.Fatalf("directory: ok %v, err %v, file %v, info %v", ok, err, f, info)
	}

	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		info, f, ok, err = openStat(fifo)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("openStat of a named pipe waits for a writer")
	}
	if !ok || err != nil || f != nil || info.Mode()&fs.ModeNamedPipe == 0 {
		t.Fatalf("named pipe: ok %v, err %v, file %v, mode %v", ok, err, f, info.Mode())
	}
}

// TestServeOpened serves files the lookups of which open them: read whole
// and by SendFile from the descriptor the lookup opened, twice, the second
// time from a lookup the cache keeps, with a link, a named pipe, a
// directory and a file that is not there beside them.
func TestServeOpened(t *testing.T) {
	big := strings.Repeat("0123456789", smallFile/10+100)
	small := big[:4<<10]
	root := tree(t, map[string]string{"small.bin": small, "big.bin": big, "sub/x": "x"})
	if err := os.Symlink("big.bin", filepath.Join(root, "link.bin")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := mustLoad(t, root, `, "max_file_bytes": 4, "default_type": "application/octet-stream"`)
	for round := 0; round < 2; round++ {
		for _, tt := range []struct {
			path   string
			status int
			body   string
		}{
			{"/small.bin", 200, small},
			{"/big.bin", 200, big},
			{"/link.bin", 200, big},
			{"/fifo", 404, ""},
			{"/sub", 301, ""},
			{"/none", 404, ""},
			{"/small.bin/x", 404, ""},
		} {
			got := get(s, "GET", tt.path, nil).Response
			if got.StatusCode != tt.status || (tt.status == 200 && string(got.Body) != tt.body) {
				t.Fatalf("round %d %s: status %d, %d bytes; want %d, %d bytes", round, tt.path, got.StatusCode, len(got.Body), tt.status, len(tt.body))
			}
			if tt.status == 200 && got.Header.Get("Content-Type") != "application/octet-stream" {
				t.Fatalf("%s: Content-Type %q", tt.path, got.Header.Get("Content-Type"))
			}
		}
	}
}
