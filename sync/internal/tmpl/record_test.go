package tmpl

import (
	"strconv"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

func TestRecordRender(t *testing.T) {
	r := &fitsync.Record{Source: "file", Namespace: "/var/log/a.log", Op: fitsync.OpMessage,
		Key: fitsync.Int(7), CommitTime: time.Unix(1, 5).UTC(),
		After: fitsync.Map(fitsync.Field{Name: "level", Value: fitsync.String("info")}, fitsync.Field{Name: "n", Value: fitsync.Int(3)})}
	for tmpl, want := range map[string]string{
		"plain":                 "plain",
		"":                      "",
		"{level}":               "info",
		"{{x}} {level}-{n}":     "{x}} info-3",
		"{$namespace}|{$op}":    "/var/log/a.log|message",
		"{$source}:{$key}":      "file:7",
		"{$commit_time}":        "1970-01-01T00:00:01.000000005Z",
		"{missing}!":            "!",
		"a{level}b{$namespace}": "ainfob/var/log/a.log",
	} {
		tp, err := ParseRecord(tmpl)
		if err != nil {
			t.Fatalf("%s: %v", tmpl, err)
		}
		if got := tp.Render(r); got != want {
			t.Errorf("%s: %q, want %q", tmpl, got, want)
		}
	}
	tp, _ := ParseRecord("{$commit_time}")
	if got := tp.Render(&fitsync.Record{}); got != "" {
		t.Errorf("no commit time: %q", got)
	}
	del := &fitsync.Record{Op: fitsync.OpDelete, Before: fitsync.Map(fitsync.Field{Name: "level", Value: fitsync.String("old")})}
	if tp, _ := ParseRecord("{level}"); tp.Render(del) != "old" {
		t.Error("before")
	}
	for _, bad := range []string{"{", "{}", "{$nothing}"} {
		if _, err := ParseRecord(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestTimeOf(t *testing.T) {
	at := time.Date(2026, 11, 10, 1, 2, 3, 0, time.UTC)
	for _, tc := range []struct {
		v    fitsync.Value
		want time.Time
		ok   bool
	}{
		{fitsync.Time(at), at, true},
		{fitsync.Date(2026, 11, 10), time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), true},
		{fitsync.Int(at.Unix()), at, true},
		{fitsync.Int(at.UnixMilli()), at, true},
		{fitsync.Uint(uint64(at.UnixMicro())), at, true},
		{fitsync.Int(at.UnixNano()), at, true},
		{fitsync.Float(float64(at.Unix())), at, true},
		{fitsync.String(at.Format(time.RFC3339)), at, true},
		{fitsync.String(strconv.FormatInt(at.Unix(), 10)), at, true},
		{fitsync.String(strconv.FormatInt(at.Unix(), 10) + ".5"), at.Add(500 * time.Millisecond), true},
		{fitsync.Decimal(strconv.FormatInt(at.Unix(), 10)), at, true},
		{fitsync.String("not a time"), time.Time{}, false},
		{fitsync.Int(-1), time.Time{}, false},
		{fitsync.Bool(true), time.Time{}, false},
	} {
		got, ok := TimeOf(tc.v)
		if ok != tc.ok || ok && !got.Equal(tc.want) {
			t.Errorf("%v: %v %v, want %v %v", tc.v, got, ok, tc.want, tc.ok)
		}
	}
}
