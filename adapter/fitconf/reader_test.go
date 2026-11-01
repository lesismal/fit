package fitconf

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lesismal/fit/adapter"
)

// TestReaderBounds reads configs past the reader's bounds, with an
// includer of the test's own: an import with nothing to import, includes
// and uses nested too deep, a file that includes the config's own, and
// defs used until the words are too many.
func TestReaderBounds(t *testing.T) {
	// Each file n includes the next, n+1: a chain without a loop.
	chain := func(_, pattern string) ([]adapter.Source, error) {
		n, _ := strconv.Atoi(pattern)
		return []adapter.Source{{Path: pattern, Data: []byte("include " + strconv.Itoa(n+1) + "\n")}}, nil
	}
	self := func(_, _ string) ([]adapter.Source, error) {
		return []adapter.Source{{Path: "top.conf", Data: nil}}, nil
	}
	var defs strings.Builder
	for i := maxIncludeDepth + 2; i > 0; i-- {
		defs.WriteString("def d" + strconv.Itoa(i) + " { use d" + strconv.Itoa(i+1) + " }\n")
	}
	defs.WriteString("def d" + strconv.Itoa(maxIncludeDepth+3) + " { }\nuse d1\n")
	words := "def big {\n" + strings.Repeat("x 1 2 3 4 5 6 7\n", maxTokens/(8*8)) + "}\n" + strings.Repeat("use big\n", 10)
	for _, c := range []struct {
		name    string
		src     string
		include adapter.Includer
		want    string
	}{
		{"import of nothing", "import\n", chain, "import takes the config files to import"},
		{"includes nested too deep", "include 0\n", chain, "nested more than 16 deep"},
		{"uses nested too deep", defs.String(), chain, "nested more than 16 deep"},
		{"the config's own file", "include top.conf\n", self, "top.conf includes itself"},
		{"too many words used", words, chain, "with the defs and files it uses and includes"},
	} {
		_, err := newReader("top.conf", c.include).read("top.conf", []byte(c.src), true)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// TestHelpersBounds checks the helpers' cases the converter and the
// writer never hand them.
func TestHelpersBounds(t *testing.T) {
	if _, err := compact([]byte("\"open")); err == nil {
		t.Error("compact of a file that does not parse")
	}
	d := &directive{name: token{text: "x"}}
	if _, err := shorthand(d, []token{{text: "a"}}, nil); err == nil || !strings.Contains(err.Error(), "x takes its name") {
		t.Errorf("shorthand of no fields: %v", err)
	}
	if isBytesField("a.max_bytes[0]", reflect.TypeFor[int64]()) {
		t.Error("an element of a list is a bytes field")
	}
	if _, err := (&converter{pos: map[string]token{}}).generic(d, "x", maxDepth+1); err == nil || !strings.Contains(err.Error(), "nested too deep") {
		t.Errorf("generic too deep: %v", err)
	}

	o := func(kv ...any) *ordered {
		r := &ordered{}
		for i := 0; i < len(kv); i += 2 {
			r.keys, r.vals = append(r.keys, kv[i].(string)), append(r.vals, kv[i+1])
		}
		return r
	}
	fields := []string{"name", "hosts"}
	for _, in := range []*ordered{
		o("name", "a", "hosts", "h"),               // the last is not a list
		o("name", []any{"a"}, "hosts", []any{"h"}), // a list before the last
		o("name", "a", "hosts", []any{}),           // an empty list
		o("name", "a", "hosts", []any{"h", 1}),     // a list of not only strings
		o("name", 1),                               // neither a string nor a list
	} {
		// What the shorthand cannot take stays in the block.
		if args, rest := shorthandArgs(in, fields); len(rest.keys) == 0 || len(args) > 1 {
			t.Errorf("%v: %q, left %v", in.vals, args, rest.keys)
		}
	}
	cert := func(kv ...any) []any { return []any{o(kv...)} }
	for _, in := range []*ordered{
		o("certificates", []any{"x"}),
		o("certificates", cert("cert_file", "c", "key_file", "k", "x", 1)),
		o("certificates", cert("cert_file", "c", "key_file", 1)),
	} {
		if args, rest := tlsShorthand(in); args != nil || rest != in {
			t.Errorf("%v: %q", in.vals, args)
		}
	}
	two := append(cert("cert_file", "c", "key_file", "k"), o("cert_file", "c2", "key_file", "k2"))
	args, rest := tlsShorthand(o("certificates", two, "min_version", "1.2"))
	if strings.Join(args, " ") != "c k" || len(rest.keys) != 2 || rest.keys[0] != "certificates" {
		t.Errorf("two certificates: %q %v", args, rest.keys)
	}
}
