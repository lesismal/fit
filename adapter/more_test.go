package adapter

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// raw is an adapter that gives back what it reads as its JSON, with no
// positions: a stand-in for one whose JSON is broken, or that places
// nothing.
func init() {
	Register("raw", func(data []byte) (*Result, error) { return &Result{JSON: data}, nil }, ".raw")
}

func TestRegisterTwice(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "registered twice") {
			t.Errorf("%v", r)
		}
	}()
	Register("raw", nil)
}

func TestAdaptJSON(t *testing.T) {
	if r, err := Adapt("json", []byte(`{"a": 1 /* one */}`)); err != nil || strings.Contains(string(r.JSON), "/") {
		t.Errorf("%v %q", err, r.JSON)
	}
	_, err := Adapt("", []byte("{/* x"))
	if errs := config.AsErrors(err); len(errs) != 1 || errs[0].Line != 1 || !strings.Contains(err.Error(), "does not end") {
		t.Errorf("%v", err)
	}
	if names := Names(); len(names) == 0 {
		t.Error("no adapters")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.raw")
	if err := os.WriteFile(p, []byte(`{"x": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err := ReadFile(p); err != nil || string(r.JSON) != `{"x": 1}` {
		t.Errorf("%v %v", r, err)
	}
	if _, err := ReadFile(filepath.Join(dir, "missing.json")); !os.IsNotExist(err) {
		t.Errorf("%v", err)
	}
}

func TestResultMapErrors(t *testing.T) {
	e := errors.New("plain")
	var nilResult *Result
	if nilResult.MapErrors(e) != e {
		t.Error("a nil Result changes the error")
	}
	r := &Result{Position: func(path string) (int, int, bool) {
		if path == "a" {
			return 3, 4, true
		}
		return 0, 0, false
	}}
	if r.MapErrors(nil) != nil {
		t.Error("no error became one")
	}
	if err := r.MapErrors(config.Errors{}); len(err.(config.Errors)) != 0 {
		t.Errorf("%v", err)
	}
	got := config.AsErrors(r.MapErrors(config.Errors{{Path: "a.b[1]", Line: 9, Col: 9}, {Path: "z.y", Line: 9, Col: 9}, {Msg: "pathless"}}))
	if got[0].Line != 3 || got[0].Col != 4 || got[1].Line != 0 || got[2].Line != 0 || got[2].Msg != "pathless" {
		t.Errorf("%v", got)
	}
}

func TestFilesAndSources(t *testing.T) {
	if _, err := Files(nil); err == nil || err.Error() != "no config files" {
		t.Errorf("no paths: %v", err)
	}
	if _, err := ReadSources([]string{filepath.Join(t.TempDir(), "missing.conf")}); !os.IsNotExist(err) {
		t.Errorf("a missing file: %v", err)
	}
	// A file past MaxBytes; sparse where the file system can be.
	p := filepath.Join(t.TempDir(), "big.json")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := ReadSources([]string{p}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("a big file: %v", err)
	}
}

func srcs(files ...string) []Source {
	var out []Source
	for i := 0; i+1 < len(files); i += 2 {
		out = append(out, Source{Path: files[i], Data: []byte(files[i+1])})
	}
	return out
}

func TestAdaptSetErrors(t *testing.T) {
	for _, tt := range []struct {
		srcs []Source
		want string
	}{
		{srcs("a.json", `{}`, "b.raw", ``), "EOF"},
		{srcs("a.json", `{}`, "b.raw", `[1]`), "a config is an object"},
		{srcs("a.json", `{}`, "b.raw", `{1: 2}`), "b.raw"},
		{srcs("a.json", `{}`, "b.raw", `{"a": }`), "b.raw"},
		{srcs("a.json", `{"routes": 1}`, "b.json", `{"routes": []}`), `b.json:1:12: routes: also given in a.json`},
		{srcs("a.json", `{"routes": []}`, "b.json", `{"routes": {}}`), `b.json:1:12: routes: also given in a.json`},
		{srcs("a.json", `{}`, "b.json", `{"routes": [1]}`, "c.raw", `{"routes": 2}`), `c.raw: routes: also given in b.json`},
		{srcs("a.json", `{"x": 1}`, "b.json", `{"y": 1}`, "c.json", `{"x": 2}`), `c.json:1:7: x: also given in a.json`},
	} {
		_, err := AdaptSet(tt.srcs)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: %v, want %q", tt.srcs, err, tt.want)
		}
	}
}

func TestSetMapErrors(t *testing.T) {
	set, err := AdaptSet(srcs(
		"a.json", "{\n\"routes\": [{\"x\": 1}],\n\"admin\": {\"address\": 1}\n}",
		"b.raw", `{"routes": [{"y": 2}], "listeners": []}`,
		"c.json", `{"listeners": []}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if set.Name() != "a.json, b.raw, c.json" || set.MapErrors(nil) != nil {
		t.Errorf("%s", set.Name())
	}
	for _, tt := range []struct {
		path, file, local string
		line              int
	}{
		{"admin.address", "a.json", "admin.address", 3},
		{"routes", "a.json", "routes", 2},
		{"routes[0].x", "a.json", "routes[0].x", 2},
		{"routes[1].y", "b.raw", "routes[0].y", 0},
		{"routes[2]", "", "routes[2]", 0},
		{"routes[x]", "", "routes[x]", 0},
		{"routes[", "", "routes[", 0},
		{"listeners", "", "listeners", 0},
		{"unknown", "", "unknown", 0},
	} {
		got := config.AsErrors(set.MapErrors(config.Errors{{Path: tt.path, Line: 7, Col: 7, Msg: "m"}}))[0]
		if got.File != tt.file || got.Path != tt.local || got.Line != tt.line {
			t.Errorf("%s: %s %s %d, want %s %s %d", tt.path, got.File, got.Path, got.Line, tt.file, tt.local, tt.line)
		}
	}

	// One file: its errors are its own.
	one, err := AdaptSet(srcs("a.json", `{"x": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	got := config.AsErrors(one.MapErrors(config.Errors{{Path: "x.y"}}))[0]
	if got.File != "a.json" || got.Path != "x.y" {
		t.Errorf("%+v", got)
	}
}

func TestJSONPosition(t *testing.T) {
	if jsonPosition([]byte("{")) != nil {
		t.Error("a position function for broken JSON")
	}
	pos := jsonPosition([]byte("{\n\"a\": {\"k.1\": [10, {\"b\": 2}]}\n}"))
	for path, want := range map[string]int{
		`a["k.1"][1].b`: 25,
		`a["k.1"]`:      14,
		`a["k.1"][0]`:   15,
	} {
		if line, col, ok := pos(path); !ok || line != 2 || col != want {
			t.Errorf("%s: %d:%d %v, want 2:%d", path, line, col, ok, want)
		}
	}
	for _, path := range []string{`a["k.1`, `a["k.1"][x]`, `a["k.1"][1`, `a.missing`, `a["k.1"][5]`} {
		if _, _, ok := pos(path); ok {
			t.Errorf("%s has a place", path)
		}
	}
}
