package logs

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// The tests here plant names in a log's directory, as a user who may write
// it but not the files elsewhere could, and check that fit neither writes,
// truncates nor reads another file through them.

const secret = "not the log's\n"

// plantTarget writes the file a planted link points at, outside the log's
// directory, and returns its path.
func plantTarget(t *testing.T) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	return target
}

// symlink makes a symbolic link, skipping the test where links cannot be made.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
}

// checkTarget fails when the planted link's target changed.
func checkTarget(t *testing.T, target string) {
	t.Helper()
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != secret {
		t.Errorf("the link's target was written: %q", data)
	}
}

// openSink opens a sink at dir/fit.log that rotates by hand only.
func openSink(t *testing.T, fs *Files, path string, compress bool) *File {
	t.Helper()
	f, err := fs.Open(path, &config.Rotate{
		MaxBytes: intp(0), MaxAge: durp(0), MaxTotalBytes: intp(0), Compress: boolp(compress),
	}, func() *time.Location { return time.UTC })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Release(f) })
	return f
}

// waitCompressed waits until no compression of f is in flight.
func waitCompressed(t *testing.T, f *File) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.compressing)
		f.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a compression still runs after 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestFileLinkAtPathRefused checks a log path that is a symbolic link is not
// opened, so that nothing is appended to the file it points at.
func TestFileLinkAtPathRefused(t *testing.T) {
	target := plantTarget(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	symlink(t, target, path)
	fs := NewFiles()
	fs.every = time.Hour
	if f, err := fs.Open(path, nil, func() *time.Location { return time.UTC }); err == nil {
		fs.Release(f)
		t.Fatal("a log path that is a symbolic link was opened")
	}
	checkTarget(t, target)
}

// TestFileLinkAtPathRotated checks a symbolic link put at the log's path
// while the file is open is neither compressed, which would copy its target
// into a readable gzip, nor written through after the rotation.
func TestFileLinkAtPathRotated(t *testing.T) {
	target := plantTarget(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	f := openSink(t, fs, path, true)
	if _, err := f.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	if err := os.Remove(path); err != nil {
		t.Skipf("an open file cannot be removed here: %v", err)
	}
	symlink(t, target, path)

	if err := fs.Rotate(path); err != nil {
		t.Fatal(err)
	}
	waitCompressed(t, f)
	if _, err := f.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)

	checkTarget(t, target)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("the log path after the rotation: %v, %v", fi, err)
	}
	if got := readFile(t, dir, "fit.log"); got != "after\n" {
		t.Errorf("the new file holds %q", got)
	}
	for _, n := range filesIn(t, dir) {
		if strings.HasSuffix(n, ".gz") {
			t.Errorf("%s was compressed from a link", n)
		}
	}
	if e := f.Info().LastError; !strings.Contains(e, "compress") {
		t.Errorf("last error %q, want the refused compression", e)
	}
}

// TestFileCompressLinkAtRotatedName checks a compression whose rotated name
// now holds a symbolic link, or any file but the one rotated, reads nothing.
func TestFileCompressLinkAtRotatedName(t *testing.T) {
	target := plantTarget(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	f := openSink(t, fs, path, true)
	held, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "fit.log.2026-11-05T04-00-00.000")
	symlink(t, target, name)
	f.compress(name, held)
	other := filepath.Join(dir, "fit.log.2026-11-05T04-00-00.000-1")
	if err := os.WriteFile(other, []byte("another file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.compress(other, held)

	checkTarget(t, target)
	for _, n := range filesIn(t, dir) {
		if strings.Contains(n, ".gz") {
			t.Errorf("%s was written", n)
		}
	}
	if got := readFile(t, dir, filepath.Base(other)); got != "another file\n" {
		t.Errorf("the file not rotated now holds %q", got)
	}
}

// TestFileCompressLinkAtTemp checks a symbolic link planted where the gzip
// is first written is not written through: the compression fails and keeps
// the plain file.
func TestFileCompressLinkAtTemp(t *testing.T) {
	target := plantTarget(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	f := openSink(t, fs, path, true)
	name := filepath.Join(dir, "fit.log.2026-11-05T04-00-00.000")
	if err := os.WriteFile(name, []byte("rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	symlink(t, target, name+".gz.tmp")
	f.compress(name, held)

	checkTarget(t, target)
	if got := readFile(t, dir, filepath.Base(name)); got != "rotated\n" {
		t.Errorf("the plain file holds %q", got)
	}
	if _, err := os.Lstat(name + ".gz"); err == nil {
		t.Error("a gzip was made through the link")
	}
	if e := f.Info().LastError; !strings.Contains(e, "compress") {
		t.Errorf("last error %q, want the refused compression", e)
	}
}

// TestFileBackupNameSkipsTemp checks a rotation does not take a name whose
// gzip's temporary file is already there, so that its compression is not
// refused.
func TestFileBackupNameSkipsTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	now := time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
	fs := NewFiles()
	fs.now = func() time.Time { return now }
	fs.every = time.Hour
	f := openSink(t, fs, path, true)
	stamp := now.Format(nameUTC)
	if err := os.WriteFile(path+"."+stamp+".gz.tmp", nil, 0o600); err != nil {
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
	gz := path + "." + stamp + "-1.gz"
	file, err := os.Open(gz)
	if err != nil {
		t.Fatalf("%v; files %v", err, filesIn(t, dir))
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := io.ReadAll(zr); err != nil || string(data) != "line\n" {
		t.Errorf("gzip holds %q, %v", data, err)
	}
}

// TestFileRotateLongName checks a rotation whose name the file system cannot
// look up fails, and returns, rather than trying names for ever, and that the
// file keeps being written.
func TestFileRotateLongName(t *testing.T) {
	dir := t.TempDir()
	// 244 bytes is a valid name; with the rotation's 24 bytes after it, it
	// is past the 255 bytes a name may have.
	path := filepath.Join(dir, strings.Repeat("a", 240)+".log")
	fs := NewFiles()
	fs.every = time.Hour
	f, err := fs.Open(path, &config.Rotate{
		MaxBytes: intp(10), MaxAge: durp(0), MaxTotalBytes: intp(0), Compress: boolp(false),
	}, func() *time.Location { return time.UTC })
	if err != nil {
		t.Skipf("a 244-byte name cannot be made here: %v", err)
	}
	defer fs.Release(f)
	if _, err := f.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	// Past max_bytes: the size rotation fails and the line is written on.
	if _, err := f.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- fs.Rotate(path) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a rotation to a name too long succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the rotation did not return in 10s")
	}
	waitIdle(t, f)
	if got := readFile(t, dir, filepath.Base(path)); got != "0123456789abc" {
		t.Errorf("the file holds %q", got)
	}
	if f.Info().LastError == "" {
		t.Error("no error was recorded")
	}
}

// TestFileRotateNamesTaken checks a rotation gives up after maxBackupNames
// names taken, with an error.
func TestFileRotateNamesTaken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	now := time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
	fs := NewFiles()
	fs.now = func() time.Time { return now }
	fs.every = time.Hour
	f := openSink(t, fs, path, false)
	stamp := path + "." + now.Format(nameUTC)
	for i := 0; i < maxBackupNames; i++ {
		name := stamp
		if i > 0 {
			name += "-" + itoa(i)
		}
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Write([]byte("line\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	err := fs.Rotate(path)
	if err == nil || !strings.Contains(err.Error(), "taken") {
		t.Fatalf("rotate: %v, want the names taken", err)
	}
	if got := readFile(t, dir, "fit.log"); got != "line\n" {
		t.Errorf("the file holds %q", got)
	}
}
