package observe

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// TestAccessLogTimeZone checks lines are written in the log's time zone, and
// that a reload changes it in place.
func TestAccessLogTimeZone(t *testing.T) {
	var out syncBuffer
	l := NewAccessLogTo(&out, time.Hour, 0)
	defer l.Close()
	if err := l.SetConfig(&config.AccessLog{Output: "stdout", TimeZone: "utc"}); err != nil {
		t.Fatal(err)
	}
	logN(l, 1)
	if err := l.SetConfig(&config.AccessLog{Output: "stdout", TimeZone: "Asia/Shanghai"}); err != nil {
		t.Fatal(err)
	}
	logN(l, 1)
	l.Close()
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines %q", lines)
	}
	if !strings.Contains(lines[0], `"time":"`) || !strings.Contains(lines[0], `Z"`) {
		t.Errorf("UTC line %q", lines[0])
	}
	if !strings.Contains(lines[1], `+08:00"`) {
		t.Errorf("Shanghai line %q", lines[1])
	}
	if _, err := NewAccessLogFiles(&config.AccessLog{Output: "stdout", TimeZone: "Mars/Olympus"}, nil); err == nil {
		t.Error("an unknown time zone was taken")
	}
}

// TestAccessLogRotate checks the access log's file rotates and keeps its
// lines whole, through the same sink fit's own log writes through.
func TestAccessLogRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	l, err := NewAccessLogFiles(&config.AccessLog{
		Output:        path,
		FlushInterval: config.Duration(5 * time.Millisecond),
		Rotate:        &config.Rotate{MaxBytes: pi64(300), Compress: pbool(false), MaxAge: pdur(0), MaxTotalBytes: pi64(0)},
		TimeZone:      "utc",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/x", nil)
	// Lines arrive a flush apart, as they do from real requests, so that
	// the sink writes them in batches.
	for range 10 {
		l.Log(&Entry{Start: time.Now(), Listener: "web"}, r, 200, 7)
		l.Log(&Entry{Start: time.Now(), Listener: "web"}, r, 200, 7)
		time.Sleep(20 * time.Millisecond)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("no rotation: %v; %s", entries, fileSizes(dir, entries))
	}
	lines := 0
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
			t.Errorf("%s does not end with a whole line: %q", e.Name(), data)
		}
		lines += strings.Count(string(data), "\n")
	}
	if lines != 20 {
		t.Errorf("%d lines across the files, want 20", lines)
	}
}

// TestAccessLogSharedFile checks the access log and fit's own log naming one
// path share one sink, and that each line is whole.
func TestAccessLogSharedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "both.log")
	files := logs.NewFiles()
	rot := &config.Rotate{MaxBytes: pi64(0), Compress: pbool(false), MaxAge: pdur(0), MaxTotalBytes: pi64(0)}
	l, err := NewAccessLogFiles(&config.AccessLog{Output: path, Rotate: rot, TimeZone: "utc"}, files)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/x", nil)
	for range 5 {
		l.Log(&Entry{Start: time.Now(), Listener: "web"}, r, 200, 7)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "\n"); n != 5 {
		t.Errorf("%d lines, want 5: %q", n, data)
	}
}

// fileSizes says each file's size and lines, for a failure's message.
func fileSizes(dir string, entries []os.DirEntry) string {
	var b strings.Builder
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		b.WriteString(e.Name() + "=" + strconv.Itoa(len(data)) + "B/" + strconv.Itoa(strings.Count(string(data), "\n")) + "L ")
	}
	return b.String()
}

func pi64(v int64) *int64 { return &v }
func pdur(v time.Duration) *config.Duration {
	d := config.Duration(v)
	return &d
}
func pbool(v bool) *bool { return &v }
