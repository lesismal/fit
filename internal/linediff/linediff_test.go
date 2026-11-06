package linediff

import (
	"fmt"
	"strings"
	"testing"
)

// TestUnifiedDiff: the edits found are the fewest, shown in hunks with
// three lines of context, those near each other in one; past the edits it
// looks for, the middle is shown removed and added whole.
func TestUnifiedDiff(t *testing.T) {
	if Unified("a", "b", "x\n", "x\n") != "" {
		t.Error("the same texts")
	}
	lines := func(n int, f func(i int) string) string {
		var b strings.Builder
		for i := range n {
			b.WriteString(f(i))
			b.WriteByte('\n')
		}
		return b.String()
	}
	a := lines(30, func(i int) string { return fmt.Sprint("line ", i) })
	b := lines(30, func(i int) string {
		switch i {
		case 2:
			return "changed 2"
		case 4:
			return "changed 4"
		case 25:
			return "changed 25"
		}
		return fmt.Sprint("line ", i)
	})
	d := Unified("v1", "proposed", a, b)
	want := "--- v1\n+++ proposed\n@@ -1,8 +1,8 @@\n line 0\n line 1\n-line 2\n+changed 2\n line 3\n-line 4\n+changed 4\n line 5\n line 6\n line 7\n" +
		"@@ -23,7 +23,7 @@\n line 22\n line 23\n line 24\n-line 25\n+changed 25\n line 26\n line 27\n line 28\n"
	if d != want {
		t.Errorf("%s", d)
	}
	// From nothing, and to nothing.
	if d := Unified("a", "b", "", "x\ny\n"); !strings.Contains(d, "@@ -0,0 +1,2 @@\n+x\n+y\n") {
		t.Errorf("%s", d)
	}
	if d := Unified("a", "b", "x\n", ""); !strings.Contains(d, "@@ -1,1 +0,0 @@\n-x\n") {
		t.Errorf("%s", d)
	}
	// Insertions and removals between kept lines.
	if d := Unified("a", "b", "a\nb\nc\nd\n", "a\nx\nc\ny\nz\nd\n"); !strings.Contains(d, "-b\n+x\n c\n+y\n+z\n d\n") {
		t.Errorf("%s", d)
	}
	// Too many edits: the middle whole.
	old := MaxEdits
	defer func() { MaxEdits = old }()
	MaxEdits = 2
	if d := Unified("a", "b", "k\na\nb\nc\nk\n", "k\nx\ny\nz\nk\n"); !strings.Contains(d, "-a\n-b\n-c\n+x\n+y\n+z\n") {
		t.Errorf("%s", d)
	}
}
