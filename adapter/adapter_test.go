package adapter

import "testing"

func TestParent(t *testing.T) {
	for path, want := range map[string]string{
		"a":                     "",
		"a.b":                   "a",
		"a.b[2]":                "a.b",
		"a.b[2].c":              "a.b[2]",
		`a["k"]`:                "a",
		`a["x.y[1]"]`:           "a",
		`a["k"].b`:              `a["k"]`,
		`routes[0].params["h"]`: "routes[0].params",
	} {
		if got := parent(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}

func TestForPath(t *testing.T) {
	Register("test", func(data []byte) (*Result, error) { return &Result{JSON: data}, nil }, ".tst")
	if ForPath("a/b.TST") != "test" || ForPath("fit.json") != "" || ForPath("noext") != "" {
		t.Error(ForPath("a/b.TST"), ForPath("fit.json"))
	}
	if _, err := Adapt("missing", nil); err == nil {
		t.Error("an unknown adapter")
	}
}
