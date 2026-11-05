//go:build unix

package logs

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestFileHardLinkAtPathRefused checks a log path that is another name of a
// file elsewhere is not opened.
func TestFileHardLinkAtPathRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fit.log")
	if err := os.Link(target, path); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	fs := NewFiles()
	fs.every = time.Hour
	if f, err := fs.Open(path, nil, func() *time.Location { return time.UTC }); err == nil {
		fs.Release(f)
		t.Fatal("a log path with another hard link was opened")
	} else if !strings.Contains(err.Error(), "hard links") {
		t.Errorf("error %v", err)
	}
	checkTarget(t, target)
}

// TestFileFIFOAtPathRefused checks a FIFO at the log path is refused at once,
// rather than holding the open until someone reads it.
func TestFileFIFOAtPathRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.log")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	fs := NewFiles()
	fs.every = time.Hour
	done := make(chan error, 1)
	go func() {
		f, err := fs.Open(path, nil, func() *time.Location { return time.UTC })
		if err == nil {
			fs.Release(f)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was opened as a log")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the open of a FIFO did not return in 10s")
	}
}

// TestFileModes checks a log file is created 0640, that a rotation's new
// file takes the mode of the one it replaces, and its gzip too.
func TestFileModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	f := openSink(t, fs, path, true)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m&^fileMode != 0 || m&0o600 != 0o600 {
		t.Errorf("a new log file is %v, want %v less the umask", m, fileMode)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("line\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	if err := fs.Rotate(path); err != nil {
		t.Fatal(err)
	}
	waitCompressed(t, f)
	for _, n := range filesIn(t, dir) {
		fi, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if m := fi.Mode().Perm(); m != 0o600 {
			t.Errorf("%s is %v, want the rotated file's 0600", n, m)
		}
	}
	if names := filesIn(t, dir); len(names) != 2 || !strings.HasSuffix(names[1], ".gz") {
		t.Errorf("files %v, want the log and a gzip", names)
	}
}
