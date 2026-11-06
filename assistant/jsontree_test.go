package assistant

import (
	"fmt"
	"strings"
	"testing"
)

func mustTree(t *testing.T, s string) any {
	t.Helper()
	v, err := decodeTree([]byte(s))
	if err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return v
}

// TestTree: a tree keeps its members' order, and its pointers' operations
// are RFC 6902's: add inserts, replace and remove need what they name.
func TestTree(t *testing.T) {
	src := `{"b":1,"a":[1,2,{"x":"<&>","y":null,"z":true}],"c":{"d":"e~/f"},"n":1.50}`
	v := mustTree(t, src)
	if got := string(encodeTree(v)); got != src {
		t.Errorf("%s", got)
	}
	for _, bad := range []string{`{`, `{"a":`, `{"a":1} x`, `[` + strings.Repeat("[", maxTreeDepth+1)} {
		if _, err := decodeTree([]byte(bad)); err == nil {
			t.Errorf("%s decoded", bad)
		}
	}
	if !equalTree(mustTree(t, `{"a":1,"b":[1]}`), mustTree(t, `{"b":[1],"a":1}`)) || equalTree(mustTree(t, `{"a":1}`), mustTree(t, `{"b":1}`)) ||
		equalTree(mustTree(t, `{"a":1}`), mustTree(t, `{"a":1,"b":2}`)) || equalTree(mustTree(t, `[1]`), mustTree(t, `[2]`)) ||
		equalTree(mustTree(t, `[1]`), mustTree(t, `{}`)) || equalTree(mustTree(t, `{}`), mustTree(t, `[]`)) || equalTree(mustTree(t, `[1]`), mustTree(t, `[1,2]`)) {
		t.Error("equalTree")
	}
	tokens, _ := parsePointer("/c/d~1x~0y")
	if len(tokens) != 2 || tokens[1] != "d/x~y" || pointerOf(tokens) != "/c/d~1x~0y" {
		t.Errorf("%q", tokens)
	}
	if tk, err := parsePointer(""); tk != nil || err != nil {
		t.Error("the whole value")
	}

	for _, x := range []struct {
		op, path, value, want string
	}{
		{opAdd, "/a/0", `0`, `"a":[0,1,2,`},
		{opAdd, "/a/3", `9`, `,9]`},
		{opAdd, "/a/-", `9`, `,9]`},
		{opReplace, "/a/1", `7`, `"a":[1,7,`},
		{opRemove, "/a/0", ``, `"a":[2,`},
		{opAdd, "/new", `"x"`, `"new":"x"`},
		{opReplace, "/b", `2`, `{"b":2,`},
		{opRemove, "/c", ``, `}],"n":1.50}`},
		{opRemove, "/a/2/y", ``, `{"x":"<&>","z":true}`},
	} {
		var val any
		if x.value != "" {
			val = mustTree(t, x.value)
		}
		tokens, _ := parsePointer(x.path)
		got, err := patchTree(cloneTree(v), x.op, tokens, val, false)
		if err != nil || !strings.Contains(string(encodeTree(got)), x.want) {
			t.Errorf("%s %s: %v %s", x.op, x.path, err, encodeTree(got))
		}
	}
	for _, x := range []struct{ op, path string }{
		{opReplace, "/nope"}, {opRemove, "/nope"}, {opAdd, "/a/9"}, {opAdd, "/a/01"}, {opReplace, "/a/3"}, {opReplace, "/a/-"},
		{opAdd, "/b/x"}, {opAdd, "/x/y/z"}, {opAdd, ""},
	} {
		tokens, _ := parsePointer(x.path)
		if _, err := patchTree(cloneTree(v), x.op, tokens, 1, false); err == nil {
			t.Errorf("%s %s applied", x.op, x.path)
		}
	}
	// With create, an add makes the objects, and the array, it goes through;
	// not through a value that is neither.
	got, err := patchTree(cloneTree(v), opAdd, []string{"p", "q", "-"}, mustTree(t, "1"), true)
	if err != nil || !strings.Contains(string(encodeTree(got)), `"p":{"q":[1]}`) {
		t.Errorf("%v %s", err, encodeTree(got))
	}
	if _, err := patchTree(cloneTree(v), opAdd, []string{"b", "q", "r"}, 1, true); err == nil {
		t.Error("made a path through a number")
	}
	// An array at the top, and one in it.
	arr := mustTree(t, `[1,2]`)
	if got, err := patchTree(arr, opRemove, []string{"0"}, nil, false); err != nil || string(encodeTree(got)) != `[2]` {
		t.Errorf("%v %s", err, encodeTree(got))
	}
	nested := mustTree(t, `[[1],[2]]`)
	if got, err := patchTree(nested, opAdd, []string{"1", "-"}, mustTree(t, "3"), false); err != nil || string(encodeTree(got)) != `[[1],[2,3]]` {
		t.Errorf("%v %s", err, encodeTree(got))
	}
	if _, ok := lookupTree(v, []string{"b", "x"}); ok {
		t.Error("a member of a number")
	}
	if _, ok := lookupTree(v, []string{"a", "x"}); ok {
		t.Error("an array's member")
	}
	o := newObject()
	o.del("none")
}

// TestUnifiedDiff: the edits found are the fewest, shown in hunks with
// three lines of context, those near each other in one; past the edits it
// looks for, the middle is shown removed and added whole.
func TestUnifiedDiff(t *testing.T) {
	if unifiedDiff("a", "b", "x\n", "x\n") != "" {
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
	d := unifiedDiff("v1", "proposed", a, b)
	want := "--- v1\n+++ proposed\n@@ -1,8 +1,8 @@\n line 0\n line 1\n-line 2\n+changed 2\n line 3\n-line 4\n+changed 4\n line 5\n line 6\n line 7\n" +
		"@@ -23,7 +23,7 @@\n line 22\n line 23\n line 24\n-line 25\n+changed 25\n line 26\n line 27\n line 28\n"
	if d != want {
		t.Errorf("%s", d)
	}
	// From nothing, and to nothing.
	if d := unifiedDiff("a", "b", "", "x\ny\n"); !strings.Contains(d, "@@ -0,0 +1,2 @@\n+x\n+y\n") {
		t.Errorf("%s", d)
	}
	if d := unifiedDiff("a", "b", "x\n", ""); !strings.Contains(d, "@@ -1,1 +0,0 @@\n-x\n") {
		t.Errorf("%s", d)
	}
	// Insertions and removals between kept lines.
	if d := unifiedDiff("a", "b", "a\nb\nc\nd\n", "a\nx\nc\ny\nz\nd\n"); !strings.Contains(d, "-b\n+x\n c\n+y\n+z\n d\n") {
		t.Errorf("%s", d)
	}
	// Too many edits: the middle whole.
	old := maxDiffEdits
	defer func() { maxDiffEdits = old }()
	maxDiffEdits = 2
	if d := unifiedDiff("a", "b", "k\na\nb\nc\nk\n", "k\nx\ny\nz\nk\n"); !strings.Contains(d, "-a\n-b\n-c\n+x\n+y\n+z\n") {
		t.Errorf("%s", d)
	}
}
