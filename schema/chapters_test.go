package schema

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/lesismal/fit/adapter/caddyfile"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// routeConfig wraps a module chapter's route in a config with the
// upstreams the examples name.
const routeConfig = `{
  "listeners": [{"name": "web", "address": ":8080"}],
  "upstreams": [
    {"name": "app", "addresses": ["127.0.0.1:9000"]},
    {"name": "app-canary", "addresses": ["127.0.0.1:9001"]},
    {"name": "app-backup", "addresses": ["127.0.0.1:9002"]},
    {"name": "auth", "addresses": ["127.0.0.1:9091"]}
  ],
  "routes": [%s]
}`

// TestChapterExamples loads every example of the reference's chapters:
// each JSON config, each module's route with its params decoded by the
// module, and each Caddyfile converted.
func TestChapterExamples(t *testing.T) {
	err := fs.WalkDir(doc, "doc", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := strings.TrimPrefix(path, "doc/")
		c, err := readChapter(name)
		if err != nil {
			t.Error(err)
			return nil
		}
		if c.json != "" {
			src := c.json
			if c.route {
				src = fmt.Sprintf(routeConfig, c.json)
			}
			cfg, err := config.Load([]byte(src))
			if err != nil {
				t.Errorf("%s: its JSON: %v", name, err)
				return nil
			}
			for _, r := range cfg.Routes {
				refs := []struct {
					ns  string
					ref config.ModuleRef
				}{{module.Handlers, r.Handler}}
				for _, m := range r.Middleware {
					refs = append(refs, struct {
						ns  string
						ref config.ModuleRef
					}{module.Middlewares, m})
				}
				for _, x := range refs {
					info, ok := module.Lookup(x.ns + "." + x.ref.Module)
					if !ok {
						t.Errorf("%s: no module %s", name, x.ref.Module)
						continue
					}
					if err := x.ref.Params.Decode(info.New()); err != nil {
						t.Errorf("%s: %s's params: %v", name, x.ref.Module, err)
					}
				}
			}
		}
		if c.caddyfile != "" {
			r, err := caddyfile.Convert([]byte(c.caddyfile))
			if err != nil {
				t.Errorf("%s: its Caddyfile: %v", name, err)
				return nil
			}
			if len(r.Warnings) > 0 {
				t.Errorf("%s: its Caddyfile converts with warnings: %v", name, r.Warnings)
			}
			if _, err := config.Load(r.JSON); err != nil {
				t.Errorf("%s: its Caddyfile: %v\n%s", name, r.MapErrors(err), r.JSON)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
