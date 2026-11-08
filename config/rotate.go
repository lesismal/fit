package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	// The zone database is embedded, so that a zone like "Asia/Shanghai"
	// works in a container that has no zoneinfo of its own.
	_ "time/tzdata"
)

// Rotate says how a log file is rotated and how its rotated files are
// removed. It is the settings of a Log's or a Logger's file output: the
// object being there is what turns rotation on, and a field left out takes
// its default. A file with no Rotate is written as one file for as long as
// fit runs, as it was before rotation existed.
type Rotate struct {
	MaxBytes      *int64    `json:"max_bytes,omitempty" default:"1073741824" min:"0" doc:"How large a file may grow before it is rotated: a write that would take it past this starts a new file. 1 GiB when left out; 0 does not rotate by size."`
	Interval      Duration  `json:"interval,omitempty" doc:"How long a file is written before it is rotated, at the next period boundary of the log's time_zone: 24h turns at midnight, 1h on the hour; a fraction of a second, or a period over 24h that is not a whole number of days, is refused. Off when left out."`
	MaxAge        *Duration `json:"max_age,omitempty" default:"\"720h\"" doc:"How long a rotated file is kept after it was last written: 720h (30 days) when left out; 0 keeps it until a count or size limit removes it."`
	MaxFiles      *int      `json:"max_files,omitempty" min:"0" doc:"The most rotated files kept, newest first; 0 or left out has no count limit."`
	MaxTotalBytes *int64    `json:"max_total_bytes,omitempty" default:"1073741824" min:"0" doc:"The most the rotated files may total, the oldest removed until they fit. 1 GiB when left out; 0 does not limit the total."`
	Compress      *bool     `json:"compress,omitempty" default:"true" doc:"Gzip a file when it is rotated. On when left out."`

	At Value `json:"-" fit:"self"`
}

// The defaults of Rotate's fields, when the field is left out.
const (
	DefaultRotateMaxBytes      int64 = 1 << 30
	DefaultRotateMaxAge              = Duration(720 * time.Hour)
	DefaultRotateMaxTotalBytes int64 = 1 << 30
)

// MaxBytesOrDefault returns the size a file is rotated at: max_bytes, or
// 1 GiB when left out; 0 does not rotate by size.
func (r *Rotate) MaxBytesOrDefault() int64 {
	if r == nil || r.MaxBytes == nil {
		return DefaultRotateMaxBytes
	}
	return *r.MaxBytes
}

// IntervalOrDefault returns how long a file is written before it is
// rotated, or 0 for no rotation by time.
func (r *Rotate) IntervalOrDefault() time.Duration {
	if r == nil {
		return 0
	}
	return r.Interval.Std()
}

// MaxAgeOrDefault returns how long a rotated file is kept: max_age, or 720h
// when left out; 0 keeps it until a count or size limit removes it.
func (r *Rotate) MaxAgeOrDefault() time.Duration {
	if r == nil || r.MaxAge == nil {
		return DefaultRotateMaxAge.Std()
	}
	return r.MaxAge.Std()
}

// MaxFilesOrDefault returns the most rotated files kept: max_files, or 0
// when left out or 0, which has no count limit.
func (r *Rotate) MaxFilesOrDefault() int {
	if r == nil || r.MaxFiles == nil {
		return 0
	}
	return *r.MaxFiles
}

// MaxTotalBytesOrDefault returns the most the rotated files may total:
// max_total_bytes, or 1 GiB when left out; 0 does not limit the total.
func (r *Rotate) MaxTotalBytesOrDefault() int64 {
	if r == nil || r.MaxTotalBytes == nil {
		return DefaultRotateMaxTotalBytes
	}
	return *r.MaxTotalBytes
}

// CompressOrDefault reports whether a rotated file is gzipped: compress, or
// true when left out.
func (r *Rotate) CompressOrDefault() bool {
	if r == nil || r.Compress == nil {
		return true
	}
	return *r.Compress
}

// Equal reports whether r and o rotate and keep files alike; two outputs of
// one path need settings that are equal.
func (r *Rotate) Equal(o *Rotate) bool {
	if r == nil || o == nil {
		return r == o
	}
	return r.MaxBytesOrDefault() == o.MaxBytesOrDefault() &&
		r.IntervalOrDefault() == o.IntervalOrDefault() &&
		r.MaxAgeOrDefault() == o.MaxAgeOrDefault() &&
		r.MaxFilesOrDefault() == o.MaxFilesOrDefault() &&
		r.MaxTotalBytesOrDefault() == o.MaxTotalBytesOrDefault() &&
		r.CompressOrDefault() == o.CompressOrDefault()
}

// The values of a log's time_zone, besides an IANA zone name.
const (
	TimeZoneLocal = "local"
	TimeZoneUTC   = "utc"
)

// LoadTimeZone returns the zone a config's time_zone names: local or "" for
// the local zone, utc for UTC, or an IANA zone name such as Asia/Shanghai.
func LoadTimeZone(name string) (*time.Location, error) {
	trimmed := strings.TrimSpace(name)
	switch strings.ToLower(trimmed) {
	case "", TimeZoneLocal:
		return time.Local, nil
	case TimeZoneUTC:
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%q is not a time zone: local, utc or an IANA name such as Asia/Shanghai", name)
	}
	return loc, nil
}

// TimeZoneName returns the name a log writes its times in: time_zone, or
// local when left out.
func TimeZoneName(name string) string {
	if strings.TrimSpace(name) == "" {
		return TimeZoneLocal
	}
	return strings.TrimSpace(name)
}

// checkTimeZone reports what is wrong with a time_zone, or "" when it is
// one LoadTimeZone takes.
func checkTimeZone(name string) string {
	if _, err := LoadTimeZone(name); err != nil {
		return err.Error()
	}
	return ""
}

// fileOutput reports whether out names a file, rather than stdout, stderr or
// nothing, which the log splits by level.
func fileOutput(out string) bool {
	return out != "" && out != "stdout" && out != "stderr"
}

// IsFileOutput reports whether out names a file that can be rotated and
// kept, rather than stdout, stderr or nothing.
func IsFileOutput(out string) bool { return fileOutput(out) }

// outputPath returns the cleaned path of a file output, the key two outputs
// sharing one file are compared by.
func outputPath(out string) string { return filepath.Clean(out) }

// checkRotate reports what is wrong with r's rotation settings.
func checkRotate(r *Rotate, at Value, add func(Value, string, ...any)) {
	if r == nil {
		return
	}
	if d := r.Interval.Std(); d > 0 {
		switch {
		case d < time.Second:
			add(at.Field("interval"), "%s is too short: a file rotates every second or more", d)
		case d > 24*time.Hour && d%(24*time.Hour) != 0:
			add(at.Field("interval"), "%s is over 24h but not a whole number of days", d)
		}
	}
}

// CheckRotate reports what is wrong with r's rotation settings, at at, as
// a module that writes a rotated file of its own checks its rotate.
func CheckRotate(r *Rotate, at Value) error {
	var errs Errors
	checkRotate(r, at, func(v Value, format string, args ...any) { errs = append(errs, v.Errorf(format, args...)) })
	if len(errs) == 0 {
		return nil
	}
	return errs
}
