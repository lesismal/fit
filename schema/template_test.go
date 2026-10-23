package schema

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/lesismal/fit/adapter"
	_ "github.com/lesismal/fit/adapter/fitconf"
	_ "github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

var templateFiles = map[string]string{"json": "../templates/json/fit.json", "yaml": "../templates/yaml/fit.yaml", "fit": "../templates/fit/fit.fit"}

// TestTemplatesUpToDate fails when a config type, a struct tag or a
// standard module changed without the templates being regenerated.
func TestTemplatesUpToDate(t *testing.T) {
	for format, file := range templateFiles {
		got, err := Template(format)
		if err != nil {
			t.Fatal(err)
		}
		if *update {
			if err := os.WriteFile(file, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is out of date; run: go test ./schema -update", file)
		}
	}
	if _, err := Template("toml"); err == nil {
		t.Error("a template in toml")
	}
}

// TestTemplatesLoad loads each template as it is, with its handler's params
// decoded by the module, and checks it holds every field of the config and
// of every module's params.
func TestTemplatesLoad(t *testing.T) {
	names := map[string]bool{}
	var collect func(reflect.Type)
	seen := map[reflect.Type]bool{}
	collect = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ == config.DurationType || typ == valueType || seen[typ] {
			return
		}
		seen[typ] = true
		for _, f := range config.Fields(typ) {
			if !f.Self {
				names[f.Name] = true
				collect(f.Type)
			}
		}
	}
	collect(reflect.TypeFor[config.Config]())
	for _, ns := range []string{module.Handlers, module.Middlewares} {
		for _, info := range module.Namespace(ns) {
			collect(reflect.TypeOf(info.New()))
		}
	}
	for format, file := range templateFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		r, err := adapter.Adapt(format, data)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		c, err := config.Load(r.JSON)
		if err != nil {
			t.Fatalf("%s: %v", file, r.MapErrors(err))
		}
		for _, route := range c.Routes {
			info, _ := module.Lookup(module.Handlers + "." + route.Handler.Module)
			if err := route.Handler.Params.Decode(info.New()); err != nil {
				t.Errorf("%s: %v", file, err)
			}
		}
		for name := range names {
			key := fmt.Sprintf("%q:", name)
			switch format {
			case "yaml":
				key = " " + name + ":"
			case "fit":
				// A module is its name and a block of its params, with
				// no module or params fields.
				if name == "module" || name == "params" || fitDirective(string(data), name) || fitDirective(string(data), fitSingular[name]) {
					continue
				}
				t.Errorf("%s has no field %s", file, name)
				continue
			}
			if !strings.Contains(string(data), key) && !strings.HasPrefix(string(data), strings.TrimSpace(key)) && !strings.Contains(string(data), "\n"+strings.TrimSpace(key)) {
				t.Errorf("%s has no field %s", file, name)
			}
		}
	}
}

// fitDirective says a .fit template has a directive name, written out or
// commented out.
func fitDirective(data, name string) bool {
	if name == "" {
		return false
	}
	return regexp.MustCompile(`(?m)^[\t# ]*` + regexp.QuoteMeta(name) + `( |$)`).MatchString(data)
}
