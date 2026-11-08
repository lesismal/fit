// Package cron parses cron schedules of five fields, as crontab(5) writes
// them, and finds the times they name: sync's jobs start by them.
//
// The fields are minute (0-59), hour (0-23), day of the month (1-31), month
// (1-12 or jan-dec) and day of the week (0-7 or sun-sat, 0 and 7 both
// Sunday). Each is *, a value, a range a-b, a list of them split by commas,
// and any of * or a range followed by /step. As in Vixie cron, when both
// days are restricted (neither starts with *), a day matching either runs.
// The descriptors @yearly (@annually), @monthly, @weekly, @daily
// (@midnight) and @hourly stand for their schedules. There is no seconds
// field, nor @reboot or @every.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed schedule.
type Schedule struct {
	minute, hour, dom, month, dow uint64 // bit n set: value n matches
	domStar, dowStar              bool
}

var descriptors = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var (
	monthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	dayNames   = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
)

// Parse parses a schedule of five fields, or a descriptor.
func Parse(spec string) (*Schedule, error) {
	s := strings.TrimSpace(spec)
	if strings.HasPrefix(s, "@") {
		d, ok := descriptors[strings.ToLower(s)]
		if !ok {
			return nil, fmt.Errorf("%q is not a descriptor: @yearly, @annually, @monthly, @weekly, @daily, @midnight or @hourly", s)
		}
		s = d
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return nil, fmt.Errorf("%q has %d fields, not 5: minute, hour, day of the month, month and day of the week", spec, len(f))
	}
	var sc Schedule
	var err error
	parts := []struct {
		name     string
		bits     *uint64
		lo, hi   int
		names    []string
		namesLo  int
		starFlag *bool
	}{
		{"minute", &sc.minute, 0, 59, nil, 0, nil},
		{"hour", &sc.hour, 0, 23, nil, 0, nil},
		{"day of the month", &sc.dom, 1, 31, nil, 0, &sc.domStar},
		{"month", &sc.month, 1, 12, monthNames, 1, nil},
		{"day of the week", &sc.dow, 0, 7, dayNames, 0, &sc.dowStar},
	}
	for i, p := range parts {
		if *p.bits, err = field(f[i], p.lo, p.hi, p.names, p.namesLo); err != nil {
			return nil, fmt.Errorf("%s %q: %w", p.name, f[i], err)
		}
		if p.starFlag != nil {
			*p.starFlag = strings.HasPrefix(f[i], "*")
		}
	}
	if sc.dow&(1<<7) != 0 {
		sc.dow |= 1 // 7 is Sunday too
	}
	return &sc, nil
}

func field(s string, lo, hi int, names []string, namesLo int) (uint64, error) {
	var bits uint64
	for _, item := range strings.Split(s, ",") {
		rng, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 || n > hi-lo+1 {
				return 0, fmt.Errorf("the step %q is not a number from 1 to %d", stepText, hi-lo+1)
			}
			step = n
		}
		var a, b int
		switch {
		case rng == "*":
			a, b = lo, hi
		default:
			first, last, isRange := strings.Cut(rng, "-")
			var err error
			if a, err = value(first, lo, hi, names, namesLo); err != nil {
				return 0, err
			}
			b = a
			if isRange {
				if b, err = value(last, lo, hi, names, namesLo); err != nil {
					return 0, err
				}
				if b < a {
					return 0, fmt.Errorf("the range %q runs backwards", rng)
				}
			} else if hasStep {
				return 0, errors.New("a step follows * or a range")
			}
		}
		for v := a; v <= b; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

func value(s string, lo, hi int, names []string, namesLo int) (int, error) {
	for i, n := range names {
		if strings.EqualFold(s, n) {
			return i + namesLo, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%q is not a value from %d to %d", s, lo, hi)
	}
	return v, nil
}

// maxYears bounds the search for the next time: a schedule such as
// "0 0 30 2 *" names none.
const maxYears = 5

// Next returns the first time after t that the schedule names, in loc's
// wall clock, or the zero time when there is none within five years.
// Times a daylight saving change skips are not run; times it repeats run
// once, at their first.
func (s *Schedule) Next(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	end := day.AddDate(maxYears, 0, 0)
	for ; !day.After(end); day = day.AddDate(0, 0, 1) {
		y, m, d := day.Date()
		if s.month&(1<<uint(m)) == 0 || !s.dayMatches(d, day.Weekday()) {
			continue
		}
		for h := 0; h < 24; h++ {
			if s.hour&(1<<uint(h)) == 0 {
				continue
			}
			for mi := 0; mi < 60; mi++ {
				if s.minute&(1<<uint(mi)) == 0 {
					continue
				}
				c := time.Date(y, m, d, h, mi, 0, 0, loc)
				if c.Hour() != h || c.Minute() != mi || c.Day() != d {
					continue // a wall time the zone skips
				}
				if c.After(t) {
					return c
				}
			}
		}
	}
	return time.Time{}
}

func (s *Schedule) dayMatches(d int, wd time.Weekday) bool {
	dom, dow := s.dom&(1<<uint(d)) != 0, s.dow&(1<<uint(wd)) != 0
	if s.domStar || s.dowStar {
		return dom && dow
	}
	return dom || dow
}
