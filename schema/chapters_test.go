package schema

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/lesismal/fit/adapter/fitconf"
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
// module, and each .fit example, which must say the same as the JSON.
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
		if c.json != "" {
			// The .fit example, the chapter's own or made from its JSON,
			// is the same config as the JSON.
			f, err := c.fitExample()
			if err != nil {
				t.Error(err)
				return nil
			}
			r, err := fitconf.Adapt([]byte(f))
			if err != nil {
				t.Errorf("%s: its .fit example: %v\n%s", name, err, f)
				return nil
			}
			want := c.json
			if c.route {
				want = `{"routes": [` + c.json + `]}`
			}
			if !sameJSON(t, r.JSON, []byte(want)) {
				t.Errorf("%s: its .fit example is not its JSON:\n%s\nis\n%s", name, f, r.JSON)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// sameJSON says a and b hold the same JSON value, whatever the order of
// their members.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	std, err := config.Standardize(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(std, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}
