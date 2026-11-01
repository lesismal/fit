package caddyfile

import (
	"slices"
	"strings"
	"testing"
)

// hasWarning reports whether a warning holds want.
func hasWarning(warns []string, want string) bool {
	return slices.ContainsFunc(warns, func(w string) bool { return strings.Contains(w, want) })
}

// TestLogOptions converts the global log and a site's with what fit takes
// of them and what it skips.
func TestLogOptions(t *testing.T) {
	out, warns := adapt(t, "{\n\tdebug\n\tlog {\n\t\toutput stdout\n\t\tformat yaml\n\t\tlevel\n\t}\n\tlog a b\n}\n"+
		":8080 {\n\tlog {\n\t\tformat yaml\n\t\troll_size 1MB\n\t\tlevel DEBUG\n\t}\n\trespond 200\n}\n")
	if out.Log == nil || out.Log.Output != "stdout" || out.Log.Level != "debug" {
		t.Errorf("log: %s", js(out.Log))
	}
	for _, want := range []string{"format: skipped: fit writes its log as line", "level: skipped: not converted", "log: skipped: not converted",
		"format: skipped: fit writes access log lines", "roll_size: skipped: not converted"} {
		if !hasWarning(warns, want) {
			t.Errorf("no warning %q in %q", want, warns)
		}
	}
	out, warns = adapt(t, "{\n\tlog {\n\t\toutput net localhost:9\n\t}\n}\n:8080 {\n\trespond 200\n}\n")
	if out.Log == nil || out.Log.Output != "stderr" || !hasWarning(warns, "fit writes its log to stdout, stderr or a file") {
		t.Errorf("log: %s, warnings %q", js(out.Log), warns)
	}
}

// TestHeredocAtTheEnd checks a heredoc that ends the file, with no line
// break after its marker.
func TestHeredocAtTheEnd(t *testing.T) {
	toks, err := lex("a <<EOF\n\thi\n\tEOF")
	if err != nil || len(toks) != 2 || toks[1].text != "hi" {
		t.Errorf("%+v %v", toks, err)
	}
}

// TestIntersectSameHosts checks matches of the same hosts intersect in
// them.
func TestIntersectSameHosts(t *testing.T) {
	m, ok := intersect(&outMatch{Host: []string{"a.test", "b.test"}}, &outMatch{Host: []string{"b.test", "a.test"}})
	if !ok || len(m.Host) != 2 || m.Expr != "" {
		t.Errorf("%s %v", js(m), ok)
	}
}
