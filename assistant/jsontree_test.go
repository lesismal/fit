package assistant

import (
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
