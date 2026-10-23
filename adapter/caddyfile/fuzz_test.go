package caddyfile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// FuzzConvert checks that no Caddyfile, however broken, makes the converter
// panic, and that what it converts is JSON, with warnings that say their
// line.
func FuzzConvert(f *testing.F) {
	for _, name := range []string{"site.caddyfile", "full.caddyfile"} {
		if data, err := os.ReadFile("testdata/" + name); err == nil {
			f.Add(string(data))
		}
	}
	f.Add("(s) {\n import s\n}\n:80 {\n import s\n}\n")
	f.Add(":80 {\n @m not {\n  path /a*\n }\n header @m +X {args[0]}\n respond <<EOF\n  hi\n  EOF\n}\n")
	f.Fuzz(func(t *testing.T, src string) {
		r, err := Convert([]byte(src))
		if err != nil {
			return
		}
		if !json.Valid(r.JSON) {
			t.Fatalf("not JSON: %s", r.JSON)
		}
		for _, w := range r.Warnings {
			if !strings.HasPrefix(w, "line ") {
				t.Fatalf("a warning with no line: %q", w)
			}
		}
		_, _ = config.Load(r.JSON)
	})
}
