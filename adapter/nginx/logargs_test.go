package nginx

import (
	"slices"
	"testing"
)

// TestLogArgs checks error_log and access_log with no arguments, or more
// than error_log takes, are skipped at their lines.
func TestLogArgs(t *testing.T) {
	for _, src := range []string{
		"error_log;\nhttp { server { listen 80; } }",
		"error_log stderr warn x;\nhttp { server { listen 80; } }",
	} {
		out, ws := convert(t, src)
		if out.Log != nil || out.AccessLog != nil || !slices.Equal(warnedLines(ws), []int{1}) {
			t.Errorf("%q: log %+v, access_log %+v, warnings %q", src, out.Log, out.AccessLog, ws)
		}
	}
	out, ws := convert(t, "http {\naccess_log;\nserver { listen 80; } }")
	if out.AccessLog != nil || !slices.Equal(warnedLines(ws), []int{2}) {
		t.Errorf("access_log with no arguments: %+v, warnings %q", out.AccessLog, ws)
	}
}
