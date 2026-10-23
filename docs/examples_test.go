package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/lesismal/fit/adapter"
	_ "github.com/lesismal/fit/adapter/caddyfile"
	_ "github.com/lesismal/fit/adapter/fitconf"
	_ "github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/modules/standard"
)

var configBlock = regexp.MustCompile("(?s)```(fit|caddyfile|yaml|json)\n(.*?)\n```")

// TestConfigExamples loads every config in the pages on config formats, in
// the getting started guide and in the README, in either language: each
// .fit, YAML and JSON config loads, and each Caddyfile, on the page on
// converting them, converts without a warning and loads.
func TestConfigExamples(t *testing.T) {
	files, _ := filepath.Glob("configuration*.md")
	guide, _ := filepath.Glob("getting-started*.md")
	readme, _ := filepath.Glob("../README*.md")
	files = append(append(files, guide...), readme...)
	for _, f := range []string{"fit", "caddyfile", "yaml", "json"} {
		more, _ := filepath.Glob("reference/" + f + "*.md")
		files = append(files, more...)
	}
	if len(files) < 14 {
		t.Fatalf("only %v", files)
	}
	n := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range configBlock.FindAllStringSubmatch(string(data), -1) {
			n++
			r, err := adapter.Adapt(m[1], []byte(m[2]))
			if err != nil {
				t.Errorf("%s: a %s config: %v\n%s", f, m[1], err, m[2])
				continue
			}
			if len(r.Warnings) > 0 {
				t.Errorf("%s: a %s config converts with warnings: %v", f, m[1], r.Warnings)
			}
			if _, err := config.Load(r.JSON); err != nil {
				t.Errorf("%s: a %s config: %v\n%s", f, m[1], r.MapErrors(err), m[2])
			}
		}
	}
	if n == 0 {
		t.Error("no configs")
	}
}
