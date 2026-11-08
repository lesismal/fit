package cron

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	from := time.Date(2026, 11, 8, 10, 7, 30, 0, time.UTC) // a Sunday
	for _, c := range []struct {
		spec string
		loc  *time.Location
		from time.Time
		want string
	}{
		{"* * * * *", time.UTC, from, "2026-11-08T10:08:00Z"},
		{"*/15 * * * *", time.UTC, from, "2026-11-08T10:15:00Z"},
		{"0 3 * * *", time.UTC, from, "2026-11-09T03:00:00Z"},
		{"30 2 1 jan *", time.UTC, from, "2027-01-01T02:30:00Z"},
		{"0 0 * * mon-fri", time.UTC, from, "2026-11-09T00:00:00Z"},
		{"0 0 * * 7", time.UTC, from, "2026-11-15T00:00:00Z"},
		{"0 12 13 * 5", time.UTC, from, "2026-11-13T12:00:00Z"}, // the 13th or a Friday: Friday the 13th comes first
		{"0 12 10 * 5", time.UTC, from, "2026-11-10T12:00:00Z"}, // the 10th, a Tuesday
		{"5,10-12 8 * * *", time.UTC, from, "2026-11-09T08:05:00Z"},
		{"@hourly", time.UTC, from, "2026-11-08T11:00:00Z"},
		{"@weekly", time.UTC, from, "2026-11-15T00:00:00Z"},
		{"@yearly", time.UTC, from, "2027-01-01T00:00:00Z"},
		{"0 0 29 2 *", time.UTC, from, "2028-02-29T00:00:00Z"},
		{"0 0 30 2 *", time.UTC, from, "0001-01-01T00:00:00Z"},
		{"0 9 * * *", ny, from, "2026-11-08T14:00:00Z"},
		// 2027-03-14 02:30 does not exist in New York: the day is skipped.
		{"30 2 * * *", ny, time.Date(2027, 3, 13, 12, 0, 0, 0, time.UTC), "2027-03-15T06:30:00Z"},
		// 2026-11-01 01:30 happens twice in New York: once.
		{"30 1 * * *", ny, time.Date(2026, 11, 1, 5, 31, 0, 0, time.UTC), "2026-11-02T06:30:00Z"},
	} {
		s, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		if got := s.Next(c.from, c.loc).UTC().Format(time.RFC3339); got != c.want {
			t.Errorf("%s from %s: %s, want %s", c.spec, c.from, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, spec := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *",
		"* * * * 8", "5-1 * * * *", "*/0 * * * *", "5/2 * * * *", "a * * * *", "@reboot", "@every 1m", "1,,2 * * * *"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("%q parsed", spec)
		}
	}
	if _, err := Parse("0-59/2 0-23/3 1-31/2 JAN-DEC/2 SUN-SAT"); err != nil {
		t.Fatal(err)
	}
}
