package logs

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

func intp(v int64) *int64 { return &v }
func intPtr(v int) *int   { return &v }
func durp(v time.Duration) *config.Duration {
	d := config.Duration(v)
	return &d
}
func boolp(v bool) *bool { return &v }

// nameUTC is how a rotated file's name writes the rotation's time.
const nameUTC = "2006-01-02T15-04-05.000"

// filesIn returns the names in dir, sorted.
func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// readFile returns the bytes of dir/name.
func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// waitIdle waits until what was written has been written out.
func waitIdle(t *testing.T, f *File) {
	t.Helper()
	if err := f.runCtl(&control{}); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// TestFileRotateBySize checks a file turns when a write would take it past
// max_bytes, and that each line is in exactly one file.
func TestFileRotateBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	f, err := fs.Open(path, &config.Rotate{
		MaxBytes: intp(50), MaxAge: durp(0), MaxTotalBytes: intp(0), Compress: boolp(false),
	}, func() *time.Location { return time.UTC })
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)

	if _, err := f.Write([]byte(strings.Repeat("a", 30))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(strings.Repeat("b", 30))); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)

	names := filesIn(t, dir)
	if len(names) != 2 {
		t.Fatalf("files %v, want the file and one rotated", names)
	}
	var rotated string
	for _, n := range names {
		if n != "fit.log" {
			rotated = n
		}
	}
	if !strings.HasPrefix(rotated, "fit.log.") {
		t.Fatalf("rotated name %q", rotated)
	}
	if got := readFile(t, dir, rotated); got != strings.Repeat("a", 30) {
		t.Errorf("rotated file %q", got)
	}
	if got := readFile(t, dir, "fit.log"); got != strings.Repeat("b", 30) {
		t.Errorf("current file %q", got)
	}
}

// TestFileRotateByInterval checks a file turns at a period boundary of the
// log's zone, 24h being local midnight.
func TestFileRotateByInterval(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	now := time.Date(2026, 11, 5, 13, 0, 0, 0, loc)
	fs := NewFiles()
	fs.now = func() time.Time { return now }
	fs.every = time.Hour
	f, err := fs.Open(path, &config.Rotate{
		MaxBytes: intp(0), Interval: config.Duration(24 * time.Hour),
		MaxAge: durp(0), MaxTotalBytes: intp(0), Compress: boolp(false),
	}, func() *time.Location { return loc })
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)

	if _, err := f.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	now = now.Add(24 * time.Hour) // the next local midnight
	if _, err := f.Write([]byte("b")); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)

	names := filesIn(t, dir)
	if len(names) != 2 {
		t.Fatalf("files %v, want two", names)
	}
	var rotated string
	for _, n := range names {
		if n != "fit.log" {
			rotated = n
		}
	}
	if got := readFile(t, dir, rotated); got != "a" {
		t.Errorf("rotated file %q", got)
	}
	if got := readFile(t, dir, "fit.log"); got != "b" {
		t.Errorf("current file %q", got)
	}
}

// TestFileRetention checks files past their age are removed, and nothing the
// sink did not make or the open file itself.
func TestFileRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fs := NewFiles()
	fs.now = func() time.Time { return now }
	fs.every = time.Hour
	f, err := fs.Open(path, &config.Rotate{
		MaxBytes: intp(0), Interval: 0, MaxAge: durp(time.Hour),
		MaxTotalBytes: intp(0), Compress: boolp(false),
	}, func() *time.Location { return time.UTC })
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	waitIdle(t, f) // the sweep at open has run

	old := "fit.log." + now.Add(-2*time.Hour).UTC().Format(nameUTC)
	fresh := "fit.log." + now.Add(-10*time.Minute).UTC().Format(nameUTC)
	for _, n := range []string{old, fresh, "other.log"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for n, mtime := range map[string]time.Time{
		old: now.Add(-2 * time.Hour), fresh: now.Add(-10 * time.Minute), "other.log": now.Add(-48 * time.Hour),
	} {
		if err := os.Chtimes(filepath.Join(dir, n), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := fs.Cleanup(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Name != old {
		t.Fatalf("removed %v, want only %s", removed, old)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Errorf("%s was not removed: %v", old, err)
	}
	for _, n := range []string{fresh, "other.log", "fit.log"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was removed: %v", n, err)
		}
	}
}

// TestFileRetentionCountAndTotal checks the count and total size limits
// remove the oldest files first.
func TestFileRetentionCountAndTotal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fs := NewFiles()
	fs.now = func() time.Time { return now }
	fs.every = time.Hour
	f, err := fs.Open(path, &config.Rotate{
		MaxBytes: intp(0), MaxAge: durp(0), MaxFiles: intPtr(3), MaxTotalBytes: intp(0),
		Compress: boolp(false),
	}, func() *time.Location { return time.UTC })
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	waitIdle(t, f) // the sweep at open has run

	// Four rotated files: only the newest three are kept.
	var names []string
	for i := range 4 {
		n := "fit.log." + now.Add(-time.Duration(4-i)*time.Minute).UTC().Format(nameUTC)
		names = append(names, n)
		if err := os.WriteFile(filepath.Join(dir, n), []byte("abcd"), 0o644); err != nil {
			t.Fatal(err)
		}
		mtime := now.Add(-time.Duration(4-i) * time.Minute)
		if err := os.Chtimes(filepath.Join(dir, n), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := fs.Cleanup(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Name != names[0] {
		t.Fatalf("removed %v, want the oldest %s", removed, names[0])
	}

	// Total size: the newest two of four 4-byte files fit in 10 bytes.
	fs2Dir := t.TempDir()
	path2 := filepath.Join(fs2Dir, "fit.log")
	fs2 := NewFiles()
	fs2.now = fs.now
	fs2.every = time.Hour
	f2, err := fs2.Open(path2, &config.Rotate{
		MaxBytes: intp(0), MaxAge: durp(0), MaxTotalBytes: intp(10), Compress: boolp(false),
	}, func() *time.Location { return time.UTC })
	if err != nil {
		t.Fatal(err)
	}
	defer fs2.Release(f2)
	waitIdle(t, f2) // the sweep at open has run, before the files are there
	for i := range 4 {
		n := "fit.log." + now.Add(-time.Duration(4-i)*time.Minute).UTC().Format(nameUTC)
		if err := os.WriteFile(filepath.Join(fs2Dir, n), []byte("abcd"), 0o644); err != nil {
			t.Fatal(err)
		}
		mtime := now.Add(-time.Duration(4-i) * time.Minute)
		if err := os.Chtimes(filepath.Join(fs2Dir, n), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	removed, err = fs2.Cleanup(path2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %v, want the two oldest", removed)
	}
	if names := filesIn(t, fs2Dir); len(names) != 3 { // current file and two rotated
		t.Errorf("files %v", names)
	}
}

// TestFileCompress checks a rotated file is gzipped, keeps the lines and
// removes the plain file.
func TestFileCompress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	fs.every = time.Hour
	f, err := fs.Open(path, &config.Rotate{
		MaxBytes: intp(0), MaxAge: durp(0), MaxTotalBytes: intp(0), Compress: boolp(true),
	}, func() *time.Location { return time.UTC })
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(f)
	want := "line one\nline two\n"
	if _, err := f.Write([]byte(want)); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, f)
	if err := fs.Rotate(path); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		names := filesIn(t, dir)
		var gz string
		for _, n := range names {
			if strings.HasSuffix(n, ".gz") {
				gz = n
			}
		}
		if gz != "" {
			file, err := os.Open(filepath.Join(dir, gz))
			if err != nil {
				t.Fatal(err)
			}
			zr, err := gzip.NewReader(file)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(zr)
			if err != nil {
				t.Fatal(err)
			}
			file.Close()
			if string(data) != want {
				t.Errorf("gzip holds %q, want %q", data, want)
			}
			for _, n := range names {
				if strings.HasPrefix(n, "fit.log.") && !strings.HasSuffix(n, ".gz") {
					t.Errorf("the plain rotated file %s is still there", n)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no gzip after 5s: %v", names)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFileShared checks a path opened twice is one sink, and that two
// rotation policies for one path are refused.
func TestFileShared(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	fs := NewFiles()
	zone := func() *time.Location { return time.UTC }
	rot := &config.Rotate{MaxBytes: intp(100)}
	a, err := fs.Open(path, rot, zone)
	if err != nil {
		t.Fatal(err)
	}
	b, err := fs.Open(path, &config.Rotate{MaxBytes: intp(100)}, zone)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("one path opened two sinks")
	}
	if err := fs.Release(a); err != nil {
		t.Fatal(err)
	}
	if fs.File(path) == nil {
		t.Error("the sink closed while a logger still holds it")
	}
	if err := fs.Release(b); err != nil {
		t.Fatal(err)
	}
	if fs.File(path) != nil {
		t.Error("the sink is still open after the last logger let go")
	}
	// Two open at once with different settings are refused.
	c, err := fs.Open(path, rot, zone)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Release(c)
	if _, err := fs.Open(path, &config.Rotate{MaxBytes: intp(200)}, zone); err == nil {
		t.Error("a second rotation policy for one open file was accepted")
	}
}

// TestPeriodStart checks a period turns at midnight or the top of the hour
// in its zone.
func TestPeriodStart(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	at := func(loc *time.Location, y int, mo time.Month, d, h, mi int) time.Time {
		return time.Date(y, mo, d, h, mi, 0, 0, loc)
	}
	cases := []struct {
		now  time.Time
		d    time.Duration
		loc  *time.Location
		want time.Time
	}{
		{at(shanghai, 2026, 11, 5, 13, 37), 24 * time.Hour, shanghai, at(shanghai, 2026, 11, 5, 0, 0)},
		{at(shanghai, 2026, 11, 5, 13, 37), time.Hour, shanghai, at(shanghai, 2026, 11, 5, 13, 0)},
		{at(time.UTC, 2026, 11, 5, 13, 37), 24 * time.Hour, time.UTC, at(time.UTC, 2026, 11, 5, 0, 0)},
		{at(time.UTC, 2026, 11, 5, 13, 37), 6 * time.Hour, time.UTC, at(time.UTC, 2026, 11, 5, 12, 0)},
	}
	for _, c := range cases {
		if got := periodStart(c.now, c.d, c.loc); !got.Equal(c.want) {
			t.Errorf("periodStart(%v, %v) = %v, want %v", c.now, c.d, got, c.want)
		}
	}
}
