//go:build unix

package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// skipRoot skips a test whose refusals come from file modes, which root
// passes.
func skipRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root is not refused by file modes")
	}
}

// TestFileRotateRenameRefused checks a size rotation whose rename is refused
// keeps writing the file it has and waits rotateRetry before trying again,
// and that one which can neither rename nor reopen the file counts what is
// written then as lost, and a rotation now of it does nothing.
func TestFileRotateRenameRefused(t *testing.T) {
	skipRoot(t)
	engineLog(t)
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fit.log")
	clock := &testClock{t: time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)}
	fs := NewFiles()
	fs.now = clock.now
	fs.every = time.Hour
	f, err := fs.Open(path, &config.Rotate{
		MaxBytes: intp(10), MaxAge: durp(0), MaxTotalBytes: intp(0), Compress: boolp(false),
	}, utc)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	t.Cleanup(func() {
		os.Chmod(dir, 0o755)
		os.Chmod(path, 0o600)
	})
	if _, err := f.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)

	// The directory refuses the rename; the file is still the owner's to
	// write.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	if got := readFile(t, dir, "fit.log"); got != "0123456789abc" {
		t.Fatalf("the file holds %q", got)
	}
	if e := f.Info().LastError; !strings.Contains(e, "rotate "+path) {
		t.Errorf("last error %q, want the rotation refused", e)
	}
	if f.retryAt != clock.now().Add(rotateRetry) {
		t.Errorf("retry at %v, want %v", f.retryAt, clock.now().Add(rotateRetry))
	}

	// Past the retry, with the file no longer writable either: the rename is
	// refused, the file cannot be opened again, and writes are lost.
	clock.set(clock.now().Add(2 * rotateRetry))
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"def", "ghi"} {
		if _, err := f.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	waitIdle(t, f)
	if d := f.Info().Dropped; d != 2 {
		t.Errorf("dropped %d, want 2", d)
	}
	if err := fs.Rotate(path); err != nil {
		t.Errorf("a rotation now with no file open = %v", err)
	}
	if names := filesIn(t, dir); len(names) != 1 {
		t.Errorf("files %v, want only the log", names)
	}
	if got := readFile(t, dir, "fit.log"); got != "0123456789abc" {
		t.Errorf("the file holds %q", got)
	}
}

// TestFileOthersMayWriteWarned checks a log whose directory others may
// write is opened with a warning that says so.
func TestFileOthersMayWriteWarned(t *testing.T) {
	warnings := engineLog(t)
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	fs := NewFiles()
	fs.every = time.Hour
	openSink(t, fs, filepath.Join(dir, "fit.log"), false)
	if w := warnings.String(); !strings.Contains(w, "writable by users other than its owner") || !strings.Contains(w, dir) {
		t.Errorf("warnings %q, want the directory others may write", w)
	}
	if _, ok := othersMayWrite(filepath.Join(dir, "missing")); ok {
		t.Error("a directory that is not there is said to be writable by others")
	}
}

// TestFileCompressRefusesDirectory checks a rotated name that became a
// directory is not compressed.
func TestFileCompressRefusesDirectory(t *testing.T) {
	engineLog(t)
	dir := t.TempDir()
	fs := NewFiles()
	fs.every = time.Hour
	f := openSink(t, fs, filepath.Join(dir, "fit.log"), false)
	name := filepath.Join(dir, "fit.log.2026-11-05T04-00-00.000")
	if err := os.Mkdir(name, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	f.compress(name, held)
	if e := f.Info().LastError; !strings.Contains(e, "not a regular file") {
		t.Errorf("last error %q, want the directory refused", e)
	}
	if names := filesIn(t, dir); len(names) != 2 {
		t.Errorf("files %v, want the log and the directory", names)
	}
}

// TestFileDirectoryGone checks a sink whose file and then directory were
// removed under it lists the current file from what it wrote, and that a
// cleanup now and the periodic sweep say the directory cannot be listed.
func TestFileDirectoryGone(t *testing.T) {
	warnings := engineLog(t)
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	f, err := fs.Open(path, &config.Rotate{MaxBytes: intp(0), MaxAge: durp(time.Hour), Compress: boolp(false)}, utc)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	if _, err := f.Write([]byte("line\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	info := f.Info()
	if len(info.Files) != 1 || !info.Files[0].Current || info.Files[0].Size != 5 {
		t.Errorf("files %+v, want the current file of 5 bytes", info.Files)
	}

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if removed, err := fs.Cleanup(path, false); err == nil || removed != nil {
		t.Errorf("cleanup of a directory that is gone = %v, %v", removed, err)
	}
	info = f.Info()
	if len(info.Files) != 0 || !strings.Contains(info.LastError, "list "+dir) {
		t.Errorf("files %+v, last error %q; want none and the listing failed", info.Files, info.LastError)
	}
	// A sweep says it once. What it reads is not changed but by a reload, so
	// the test may run it.
	said := "open " + dir + ": "
	before := strings.Count(warnings.String(), said)
	f.sweep()
	if n := strings.Count(warnings.String(), said) - before; n != 1 {
		t.Errorf("a sweep said the listing failed %d times, want once: %s", n, warnings.String())
	}
}
