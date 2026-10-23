package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStandardize blanks out comments and trailing commas, and nothing in
// strings, keeping every byte's offset.
func TestStandardize(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": 1}`:                            `{"a": 1}`,
		"{\"a\": 1, // one\n\"b\": [1, 2,],}": "{\"a\": 1,       \n\"b\": [1, 2 ] }",
		"/* a\nb */ [1 /* x */, 2]":           "    \n     [1        , 2]",
		`{"u": "http://x/*y*/", "c": ",]"}`:   `{"u": "http://x/*y*/", "c": ",]"}`,
		`["a\"//", "b",  ]`:                   `["a\"//", "b"   ]`,
		"[\r\n  1,\r\n]":                      "[\r\n  1 \r\n]",
	} {
		got, err := Standardize([]byte(in))
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
		if len(got) != len(in) {
			t.Errorf("%q: the length changed", in)
		}
	}
	if _, err := Standardize([]byte("{\n  /* open")); err == nil || err.(*Error).Line != 2 || err.(*Error).Col != 3 {
		t.Errorf("an unended comment: %v", err)
	}
	in := []byte(`[1,]`)
	if _, _ = Standardize(in); string(in) != `[1,]` {
		t.Error("data changed")
	}
}

// TestLoadJSONC loads a config with comments and trailing commas, and
// still reports errors at their lines and columns in it.
func TestLoadJSONC(t *testing.T) {
	src := `// fit's config
{
  /* the listeners */
  "listeners": [
    {"name": "web", "address": ":8080",}, // trailing commas
  ],
  "routes": [
    {"handler": {"module": "respond", "params": {"body": "a // not a comment",},},},
  ],
}
`
	c, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Body string }
	if err := json.Unmarshal(mustJSON(t, c.Routes[0].Handler.Params), &body); err != nil || body.Body != "a // not a comment" {
		t.Errorf("%v %q", err, body.Body)
	}
	_, err = Load([]byte("{\n  // a comment\n  \"listeners\": [{\"name\": \"web\", \"address\": \"nowhere\",},],\n}"))
	if err == nil || !strings.Contains(err.Error(), "3:") {
		t.Errorf("the error's position: %v", err)
	}
	for _, bad := range []string{"[,]", "{,}", `{"a": 1,,}`, "[1] /* x"} {
		if _, err := Load([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func mustJSON(t *testing.T, v Value) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
