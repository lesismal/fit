package syncs3

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

// TestSinkKeyAndCSV: each of a key's date placeholders is the first
// record's commit time in UTC, text after the last placeholder is kept, and
// the csv codec names the objects .csv and writes the rows' values.
func TestSinkKeyAndCSV(t *testing.T) {
	f := newFake(t)
	s := loadSink(t, f.params(`"key": "{namespace}/{yyyy}/{mm}/{dd}/{hh}/{position}{ext}.part", "codec": {"module": "csv", "params": {"columns": ["line"]}}`))
	r := line(7)
	r.CommitTime = r.CommitTime.Add(5*24*time.Hour + 13*time.Hour) // 2026-11-13 13:00:07 UTC
	if err := s.Write(context.Background(), []*fitsync.Record{r}); err != nil {
		t.Fatal(err)
	}
	keys := f.keys()
	if len(keys) != 1 || !regexp.MustCompile(`^app/web/2026/11/13/13/[0-9a-f]{20}\.csv\.part$`).MatchString(keys[0]) {
		t.Fatalf("%v", keys)
	}
	if body := string(f.object(keys[0])); strings.TrimSpace(body) != "line 7" {
		t.Fatalf("%q", body)
	}
	if _, err := parseKey("{position}/a}b"); err == nil {
		t.Error("a } after the last placeholder was taken")
	}
}
