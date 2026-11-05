package config

import (
	"strings"
	"testing"
	"time"
)

func pitr(v int) *int     { return &v }
func pi64(v int64) *int64 { return &v }
func pdr(v time.Duration) *Duration {
	d := Duration(v)
	return &d
}
func pbl(v bool) *bool { return &v }

// withLog returns validConfig with a log and an access_log member added.
func withLog(t *testing.T, logJSON, accessJSON string) (*Config, error) {
	t.Helper()
	data := strings.TrimSuffix(validConfig, "}") + `,
  "log": ` + logJSON
	if accessJSON != "" {
		data += `,
  "access_log": ` + accessJSON
	}
	data += "\n}"
	return Load([]byte(data))
}

func TestRotateDefaults(t *testing.T) {
	var nilRotate *Rotate
	if got := nilRotate.MaxBytesOrDefault(); got != DefaultRotateMaxBytes {
		t.Errorf("no rotate max_bytes %d", got)
	}
	if got := nilRotate.MaxAgeOrDefault(); got != DefaultRotateMaxAge.Std() {
		t.Errorf("no rotate max_age %v", got)
	}
	if got := nilRotate.MaxTotalBytesOrDefault(); got != DefaultRotateMaxTotalBytes {
		t.Errorf("no rotate max_total_bytes %d", got)
	}
	if !nilRotate.CompressOrDefault() {
		t.Error("no rotate compresses nothing")
	}
	empty := &Rotate{}
	if empty.MaxBytesOrDefault() != DefaultRotateMaxBytes || empty.MaxAgeOrDefault() != DefaultRotateMaxAge.Std() ||
		empty.MaxTotalBytesOrDefault() != DefaultRotateMaxTotalBytes || !empty.CompressOrDefault() ||
		empty.IntervalOrDefault() != 0 || empty.MaxFilesOrDefault() != 0 {
		t.Errorf("empty rotate %+v", empty)
	}
	off := &Rotate{MaxBytes: pi64(0), MaxAge: pdr(0), MaxTotalBytes: pi64(0), Compress: pbl(false), MaxFiles: pitr(3)}
	if off.MaxBytesOrDefault() != 0 || off.MaxAgeOrDefault() != 0 || off.MaxTotalBytesOrDefault() != 0 ||
		off.CompressOrDefault() || off.MaxFilesOrDefault() != 3 {
		t.Errorf("rotate with zeros %+v", off)
	}
	if !nilRotate.Equal(nil) || nilRotate.Equal(empty) {
		t.Error("equal over nil")
	}
	if !empty.Equal(&Rotate{MaxBytes: pi64(DefaultRotateMaxBytes)}) {
		t.Error("defaults are not equal to the defaults")
	}
}

func TestLoadTimeZone(t *testing.T) {
	for _, name := range []string{"", "local", "LOCAL", "utc", "UTC", "Asia/Shanghai"} {
		if _, err := LoadTimeZone(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	if loc, _ := LoadTimeZone(""); loc != time.Local {
		t.Errorf("the empty zone is %v", loc)
	}
	if loc, _ := LoadTimeZone("utc"); loc != time.UTC {
		t.Errorf("utc is %v", loc)
	}
	if _, err := LoadTimeZone("Mars/Olympus"); err == nil {
		t.Error("a zone that does not exist was taken")
	}
}

// TestCheckLogRotate checks the config rules of rotation and time zones.
func TestCheckLogRotate(t *testing.T) {
	cases := []struct {
		name    string
		logJSON string
		want    string
	}{
		{"zone", `{"time_zone": "Mars/Olympus"}`, "time zone"},
		{"short interval", `{"rotate": {"interval": "500ms"}}`, "too short"},
		{"odd interval", `{"rotate": {"interval": "36h"}}`, "whole number of days"},
		{"one file, two rotates", `{"output": "/tmp/fit.log", "rotate": {"max_bytes": 100},
			"loggers": {"engine": {"output": "/tmp/fit.log", "rotate": {"max_bytes": 200}}}}`, "one rotate"},
		{"ok", `{"time_zone": "utc", "rotate": {"max_bytes": 0, "interval": "24h", "compress": false}}`, ""},
	}
	for _, c := range cases {
		_, err := withLog(t, c.logJSON, "")
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
}

// TestCheckLogAccessLogSharing checks the two logs may share a file only
// when its rotation and time zone are the same.
func TestCheckLogAccessLogSharing(t *testing.T) {
	logJSON := `{"output": "/tmp/fit.log", "rotate": {"max_bytes": 100}}`
	if _, err := withLog(t, logJSON, `{"output": "/tmp/fit.log", "rotate": {"max_bytes": 100}}`); err != nil {
		t.Errorf("the same policy: %v", err)
	}
	for _, c := range []struct {
		name   string
		access string
		want   string
	}{
		{"rotate", `{"output": "/tmp/fit.log", "rotate": {"max_bytes": 200}}`, "one rotate"},
		{"zone", `{"output": "/tmp/fit.log", "time_zone": "utc", "rotate": {"max_bytes": 100}}`, "one boundary"},
	} {
		if _, err := withLog(t, logJSON, c.access); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
}

// TestLoadLogRotate checks the settings load and their defaults apply.
func TestLoadLogRotate(t *testing.T) {
	c, err := withLog(t, `{"output": "/tmp/fit.log", "rotate": {}}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Log.TimeZoneName() != TimeZoneLocal {
		t.Errorf("time_zone %q", c.Log.TimeZoneName())
	}
	outs := c.Log.FileOutputs()
	if len(outs) != 1 || outs[0].Path != "/tmp/fit.log" || outs[0].Rotate == nil ||
		outs[0].Rotate.MaxBytesOrDefault() != DefaultRotateMaxBytes {
		t.Fatalf("file outputs %+v", outs)
	}
	if len(outs[0].Loggers) != len(Loggers) {
		t.Errorf("the output's loggers %v", outs[0].Loggers)
	}
}
