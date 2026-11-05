package logs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/filepool"
)

// The tests here check what a file sink does when something goes wrong:
// a write it has no room for, a closed sink, a file that will not close or
// be written, a rotated name it cannot remove or compress over.

// testClock is a time a test moves by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// engineLog makes the process's log one whose warnings the test reads, and
// returns them.
func engineLog(t *testing.T) *syncBuffer {
	t.Helper()
	r, _, stderr := newRoot(t, nil)
	SetDefault(r)
	t.Cleanup(func() { SetDefault(nil) })
	return stderr
}

// manualRotate rotates by hand only and keeps every rotated file.
func manualRotate(compress bool) *config.Rotate {
	return &config.Rotate{MaxBytes: intp(0), MaxAge: durp(0), MaxTotalBytes: intp(0), Compress: boolp(compress)}
}

func utc() *time.Location { return time.UTC }

// TestFileOpenNoRotate checks a path opened with no rotate is one sink for
// every logger that names none, refuses one that names a rotate, and that a
// rotation of it now leaves the file as it is.
func TestFileOpenNoRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	f, err := fs.Open(path, nil, utc)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	g, err := fs.Open(path, nil, utc)
	if err != nil || g != f {
		t.Fatalf("second open: %p, %v; want the same sink %p", g, err, f)
	}
	defer fs.Release(g)
	if _, err := fs.Open(path, manualRotate(false), utc); err == nil {
		t.Error("a rotate for a file open with none was accepted")
	}
	if _, err := f.Write([]byte("line\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	if err := fs.Rotate(path); err != nil {
		t.Errorf("rotate: %v", err)
	}
	if names := filesIn(t, dir); len(names) != 1 || readFile(t, dir, "fit.log") != "line\n" {
		t.Errorf("files %v after a rotation of a file with no rotate", names)
	}
	if err := fs.Release(nil); err != nil {
		t.Errorf("Release(nil) = %v", err)
	}
}

// TestFilesNotOpen checks Rotate and Cleanup of a path no logger writes say
// so.
func TestFilesNotOpen(t *testing.T) {
	fs := NewFiles()
	path := filepath.Join(t.TempDir(), "none.log")
	if err := fs.Rotate(path); err == nil || !strings.Contains(err.Error(), "no log file is open") {
		t.Errorf("Rotate: %v", err)
	}
	if removed, err := fs.Cleanup(path, true); err == nil || removed != nil || !strings.Contains(err.Error(), "no log file is open") {
		t.Errorf("Cleanup: %v, %v", removed, err)
	}
}

// TestFileClosedRefuses checks a sink released by its last logger refuses
// writes and control operations, and closes once.
func TestFileClosedRefuses(t *testing.T) {
	fs := NewFiles()
	fs.every = time.Hour
	f, err := fs.Open(filepath.Join(t.TempDir(), "fit.log"), manualRotate(false), utc)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Release(f); err != nil {
		t.Fatal(err)
	}
	if n, err := f.Write([]byte("late\n")); n != 0 || !errors.Is(err, ErrClosed) {
		t.Errorf("Write = %d, %v; want ErrClosed", n, err)
	}
	if err := f.rotateNow(); !errors.Is(err, ErrClosed) {
		t.Errorf("rotate now = %v; want ErrClosed", err)
	}
	if removed, err := f.cleanupNow(false); removed != nil || !errors.Is(err, ErrClosed) {
		t.Errorf("cleanup now = %v, %v; want ErrClosed", removed, err)
	}
	if err := f.close(); err != nil {
		t.Errorf("a second close = %v", err)
	}
}

// TestFileWriteBounds checks an empty write is no write, one past the
// queue's bytes is refused and counted, and one past a pooled buffer is
// written whole.
func TestFileWriteBounds(t *testing.T) {
	dir := t.TempDir()
	fs := NewFiles()
	fs.every = time.Hour
	f := openSink(t, fs, filepath.Join(dir, "fit.log"), false)
	if n, err := f.Write(nil); n != 0 || err != nil {
		t.Errorf("Write(nil) = %d, %v", n, err)
	}
	if n, err := f.Write(make([]byte, QueueBytes+1)); n != 0 || !errors.Is(err, ErrQueueFull) {
		t.Errorf("a write past QueueBytes = %d, %v; want ErrQueueFull", n, err)
	}
	big := strings.Repeat("x", 100<<10)
	if n, err := f.Write([]byte(big)); n != len(big) || err != nil {
		t.Fatalf("a %d-byte write = %d, %v", len(big), n, err)
	}
	waitIdle(t, f)
	if got := readFile(t, dir, "fit.log"); got != big {
		t.Errorf("the file holds %d bytes, want %d", len(got), len(big))
	}
	if d := f.Info().Dropped; d != 1 {
		t.Errorf("dropped %d, want 1", d)
	}
}

// TestFileWriteQueueMessages checks a write the queue has no slot for is
// refused, counted, and leaves the queued bytes as they were.
func TestFileWriteQueueMessages(t *testing.T) {
	f := &File{q: make(chan []byte, 1)}
	if _, err := f.Write([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if n, err := f.Write([]byte("two")); n != 0 || !errors.Is(err, ErrQueueFull) {
		t.Errorf("a write to a full queue = %d, %v; want ErrQueueFull", n, err)
	}
	if q, d := f.queued.Load(), f.dropped.Load(); q != 3 || d != 1 {
		t.Errorf("queued %d, dropped %d; want 3 and 1", q, d)
	}
}

// TestFileDrainClosedQueue checks a drain takes what is queued and stops at
// a closed queue, and that a buffer with no file to go to is counted lost.
func TestFileDrainClosedQueue(t *testing.T) {
	f := &File{q: make(chan []byte, 1)}
	f.q <- []byte("x")
	f.queued.Store(1)
	close(f.q)
	f.drain()
	if q, d := f.queued.Load(), f.dropped.Load(); q != 0 || d != 1 {
		t.Errorf("queued %d, dropped %d; want 0 and 1", q, d)
	}
}

// TestFileRotateEmpty checks a rotation of an empty file makes no rotated
// file and starts the interval's period again.
func TestFileRotateEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	clock := &testClock{t: time.Date(2026, 11, 5, 10, 30, 0, 0, time.UTC)}
	fs := NewFiles()
	fs.now = clock.now
	fs.every = time.Hour
	rot := manualRotate(false)
	rot.Interval = config.Duration(time.Hour)
	f, err := fs.Open(path, rot, utc)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	clock.set(time.Date(2026, 11, 5, 12, 10, 0, 0, time.UTC))
	if err := fs.Rotate(path); err != nil {
		t.Fatal(err)
	}
	if names := filesIn(t, dir); len(names) != 1 {
		t.Errorf("files %v after an empty file was rotated", names)
	}
	// The writer is idle once Rotate returns.
	if want := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC); !f.periodStart.Equal(want) {
		t.Errorf("the period starts at %v, want %v", f.periodStart, want)
	}
}

// TestFileIntervalTurnedOn checks a file open with no interval, given one by
// a reload, starts its period at the next write and turns at the first write
// past it.
func TestFileIntervalTurnedOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	clock := &testClock{t: time.Date(2026, 11, 5, 10, 30, 0, 0, time.UTC)}
	fs := NewFiles()
	fs.now = clock.now
	fs.every = time.Hour
	f, err := fs.Open(path, manualRotate(false), utc)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	rot := manualRotate(false)
	rot.Interval = config.Duration(time.Hour)
	f.SetRotate(rot)
	if _, err := f.Write([]byte("ten\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	if names := filesIn(t, dir); len(names) != 1 {
		t.Fatalf("files %v: the first write after the interval was turned on rotated", names)
	}
	clock.set(time.Date(2026, 11, 5, 11, 5, 0, 0, time.UTC))
	if _, err := f.Write([]byte("eleven\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	rotated := "fit.log." + time.Date(2026, 11, 5, 11, 5, 0, 0, time.UTC).Format(nameUTC)
	if names := filesIn(t, dir); len(names) != 2 || names[1] != rotated {
		t.Fatalf("files %v, want fit.log and %s", names, rotated)
	}
	if got := readFile(t, dir, rotated); got != "ten\n" {
		t.Errorf("rotated file %q", got)
	}
	if got := readFile(t, dir, "fit.log"); got != "eleven\n" {
		t.Errorf("current file %q", got)
	}
}

// TestFileClosedUnderWriter checks a file that fails a write says so, and
// that a rotation of it, whose close and stat fail too, still starts a new
// file, keeping the old one plain rather than compressing what it cannot
// stat.
func TestFileClosedUnderWriter(t *testing.T) {
	warnings := engineLog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	f := openSink(t, fs, path, true)
	if _, err := f.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	_ = f.file.Close()
	if _, err := f.Write([]byte("lost\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	if e := f.Info().LastError; !strings.Contains(e, "write "+path) {
		t.Errorf("last error %q, want the failed write", e)
	}
	if err := fs.Rotate(path); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if e := f.Info().LastError; !strings.Contains(e, "compress ") {
		t.Errorf("last error %q, want the compression refused", e)
	}
	if _, err := f.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	waitCompressed(t, f)
	names := filesIn(t, dir)
	if len(names) != 2 || strings.HasSuffix(names[1], ".gz") {
		t.Fatalf("files %v, want fit.log and a plain rotated file", names)
	}
	if got := readFile(t, dir, names[1]); got != "before\n" {
		t.Errorf("rotated file %q", got)
	}
	if got := readFile(t, dir, "fit.log"); got != "after\n" {
		t.Errorf("current file %q", got)
	}
	for _, want := range []string{"write " + path, "rotate " + path, "compress " + filepath.Join(dir, names[1])} {
		if !strings.Contains(warnings.String(), want) {
			t.Errorf("the engine log does not say %q: %s", want, warnings.String())
		}
	}
}

// TestFileRetentionRemoveFails checks a rotated name past retention that
// cannot be removed is reported removed by the cleanup and said in the log,
// and that the sink goes on.
func TestFileRetentionRemoveFails(t *testing.T) {
	engineLog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	old := filepath.Join(dir, "fit.log.2020-01-01T00-00-00.000")
	if err := os.MkdirAll(filepath.Join(old, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, when, when); err != nil {
		t.Fatal(err)
	}
	fs := NewFiles()
	fs.every = time.Hour
	f, err := fs.Open(path, &config.Rotate{MaxBytes: intp(0), MaxAge: durp(time.Hour), Compress: boolp(false)}, utc)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	removed, err := fs.Cleanup(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Name != filepath.Base(old) {
		t.Errorf("removed %v, want %s", removed, filepath.Base(old))
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("the name that could not be removed: %v", err)
	}
	if e := f.Info().LastError; !strings.Contains(e, "remove "+filepath.Base(old)) {
		t.Errorf("last error %q, want the failed removal", e)
	}
}

// TestFileCompressPoolBusy checks a rotated file the compression pool has
// no room for is kept plain, said, and not left marked as compressing.
func TestFileCompressPoolBusy(t *testing.T) {
	engineLog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	pool := filepool.New(1, 1, time.Minute)
	block, started := make(chan struct{}), make(chan struct{})
	defer close(block)
	pool.Submit(func() { close(started); <-block }, nil)
	<-started
	if !pool.Submit(func() {}, nil) {
		t.Fatal("the pool's queue has no room for one job")
	}
	fs.pool.Store(pool)
	f := openSink(t, fs, path, true)
	if _, err := f.Write([]byte("line\n")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	if err := fs.Rotate(path); err != nil {
		t.Fatal(err)
	}
	if e := f.Info().LastError; !strings.Contains(e, "the file pool is busy") {
		t.Errorf("last error %q, want the pool busy", e)
	}
	names := filesIn(t, dir)
	if len(names) != 2 || strings.HasSuffix(names[1], ".gz") || readFile(t, dir, names[1]) != "line\n" {
		t.Errorf("files %v, want fit.log and the plain rotated file", names)
	}
	if f.isCompressing(names[1]) {
		t.Error("the rotated file is still marked as compressing")
	}
}

// TestFileCompressOverDirectory checks a compression whose gzip name was
// taken by a directory after the rotation keeps the plain file and leaves
// no temporary file.
func TestFileCompressOverDirectory(t *testing.T) {
	engineLog(t)
	dir := t.TempDir()
	fs := NewFiles()
	fs.every = time.Hour
	f := openSink(t, fs, filepath.Join(dir, "fit.log"), false)
	name := filepath.Join(dir, "fit.log.2026-11-05T04-00-00.000")
	if err := os.WriteFile(name, []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(name+".gz", "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.compress(name, held)
	if got := readFile(t, dir, filepath.Base(name)); got != "line\n" {
		t.Errorf("the plain file holds %q", got)
	}
	if _, err := os.Lstat(name + ".gz.tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary gzip is left: %v", err)
	}
	if e := f.Info().LastError; !strings.Contains(e, "compress "+name) {
		t.Errorf("last error %q, want the compression failed", e)
	}
}

// TestFileInfoNewestFirst checks the workbench lists the current file, then
// the rotated ones newest first.
func TestFileInfoNewestFirst(t *testing.T) {
	dir := t.TempDir()
	fs := NewFiles()
	fs.every = time.Hour
	f := openSink(t, fs, filepath.Join(dir, "fit.log"), false)
	older := "fit.log.2026-11-04T04-00-00.000"
	newer := "fit.log.2026-11-05T04-00-00.000"
	for i, n := range []string{newer, older} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-time.Duration(i+1) * time.Hour)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	info := f.Info()
	var got []string
	for _, fi := range info.Files {
		got = append(got, fi.Name)
	}
	if want := []string{"fit.log", newer, older}; strings.Join(got, " ") != strings.Join(want, " ") || !info.Files[0].Current {
		t.Errorf("files %v, want %v with the first current", got, want)
	}
}

// TestFileSweepEvery checks the sink removes a rotated file past its age
// at its sweep, with nothing written.
func TestFileSweepEvery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = 5 * time.Millisecond
	f, err := fs.Open(path, &config.Rotate{MaxBytes: intp(0), MaxAge: durp(time.Hour), Compress: boolp(false)}, utc)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	waitIdle(t, f)
	old := filepath.Join(dir, "fit.log.2020-01-01T00-00-00.000")
	if err := os.WriteFile(old, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, when, when); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Lstat(old); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the old rotated file is still there after 5s: %v", filesIn(t, dir))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestFileHelpers checks the small functions rotation names and periods are
// made with at their edges.
func TestFileHelpers(t *testing.T) {
	f := &File{}
	f.setErr(nil)
	if f.lastErr.Load() != nil {
		t.Error("setErr(nil) recorded an error")
	}
	if got := itoa(0); got != "0" {
		t.Errorf("itoa(0) = %q", got)
	}
	now := time.Date(2026, 11, 5, 13, 37, 0, 0, time.UTC)
	if got, want := periodStart(now, 0, time.UTC), time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("periodStart with no period = %v, want %v", got, want)
	}
	if got, want := periodStart(now, time.Hour, nil), periodStart(now, time.Hour, time.Local); !got.Equal(want) {
		t.Errorf("periodStart with no zone = %v, want the local %v", got, want)
	}
}
