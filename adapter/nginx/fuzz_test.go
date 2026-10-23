package nginx

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// FuzzConvert checks that no config, however broken, makes the converter
// panic, and that what it converts is JSON, with warnings that say their
// line.
func FuzzConvert(f *testing.F) {
	for _, name := range []string{"proxy.conf", "static.conf", "full.conf"} {
		if data, err := os.ReadFile("testdata/" + name); err == nil {
			f.Add(string(data))
		}
	}
	f.Add("http { server { listen 80; location ~ ( { return 200 \"x\"; } } }")
	f.Add("a 'b\\'c' \"d\" ${e};")
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
