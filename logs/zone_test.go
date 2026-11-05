package logs

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestLogTimeZone checks every format writes the log's time zone, and that a
// reload changes it in place.
func TestLogTimeZone(t *testing.T) {
	r, stdout, _ := newRoot(t, &config.Log{Output: "stdout", Format: "text", TimeZone: "utc"})
	r.Logger(config.LoggerServer).Infof("first")
	if err := r.SetConfig(&config.Log{Output: "stdout", Format: "text", TimeZone: "Asia/Shanghai"}); err != nil {
		t.Fatal(err)
	}
	if got := r.TimeZoneName(); got != "Asia/Shanghai" {
		t.Errorf("time zone %q", got)
	}
	r.Logger(config.LoggerServer).Infof("second")
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines %q", lines)
	}
	if !strings.Contains(lines[0], "Z level=INFO msg=first") {
		t.Errorf("utc line %q", lines[0])
	}
	if !strings.Contains(lines[1], "+08:00 level=INFO msg=second") {
		t.Errorf("Shanghai line %q", lines[1])
	}
}

// TestLogTimeZoneInvalid checks an unknown time zone is an error, not a
// surprise at the first line.
func TestLogTimeZoneInvalid(t *testing.T) {
	if _, err := New(&config.Log{Output: "stdout", TimeZone: "Mars/Olympus"}, &syncBuffer{}, &syncBuffer{}); err == nil {
		t.Error("an unknown time zone was taken")
	}
}

// TestLogSetConfigRotate checks a reload changes the rotation of an open
// file in place.
func TestLogSetConfigRotate(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/fit.log"
	r, err := New(&config.Log{Output: path}, &syncBuffer{}, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	f := r.Files().File(path)
	if f == nil {
		t.Fatal("no sink")
	}
	if f.rot.Load() != nil {
		t.Fatal("a file with no rotate has settings")
	}
	if err := r.SetConfig(&config.Log{Output: path, Rotate: &config.Rotate{MaxBytes: pi64(10)}}); err != nil {
		t.Fatal(err)
	}
	rot := f.rot.Load()
	if rot == nil || rot.maxBytes != 10 {
		t.Fatalf("rotation after reload %+v", rot)
	}
}

func pi64(v int64) *int64 { return &v }
