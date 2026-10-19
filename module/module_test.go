package module

import (
	"errors"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

type plain struct {
	Size int `json:"size" fit:"required" min:"1"`
}

type checked struct {
	Name    string `json:"name"`
	cleaned bool
}

func (c *checked) Provision(ctx *Context) error {
	switch c.Name {
	case "plain-error":
		return errors.New("something failed")
	case "field-error":
		return ctx.Errorf("name", "is reserved")
	}
	return nil
}

func (c *checked) Cleanup() { c.cleaned = true }

func init() {
	Register(Info{ID: "test.mods.plain", New: func() any { return new(plain) }})
	Register(Info{ID: "test.mods.checked", New: func() any { return new(checked) }})
	Register(Info{ID: "test.mods.nested.deeper", New: func() any { return new(plain) }})
}

func TestRegisterPanics(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want string
	}{
		{"malformed", Info{ID: "Handlers.x", New: func() any { return nil }}, `malformed ID "Handlers.x"`},
		{"no namespace", Info{ID: "respond", New: func() any { return nil }}, `malformed ID "respond"`},
		{"no New", Info{ID: "test.mods.nonew"}, "test.mods.nonew has no New"},
		{"twice", Info{ID: "test.mods.plain", New: func() any { return nil }}, "test.mods.plain is registered twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(r.(string), tt.want) {
					t.Fatalf("panic = %v, want %q", r, tt.want)
				}
			}()
			Register(tt.info)
		})
	}
}

func TestLookupAndNamespace(t *testing.T) {
	info, ok := Lookup("test.mods.plain")
	if !ok || info.Namespace() != "test.mods" || info.Name() != "plain" {
		t.Fatalf("Lookup = %+v, %v", info, ok)
	}
	var ids []string
	for _, info := range Namespace("test.mods") {
		ids = append(ids, info.ID)
	}
	if strings.Join(ids, " ") != "test.mods.checked test.mods.plain" {
		t.Fatalf("Namespace = %v", ids)
	}
}

func loadRef(t *testing.T, doc string) config.ModuleRef {
	t.Helper()
	v, err := config.ParseValue([]byte(doc), "handler")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestLoad(t *testing.T) {
	m, err := Load("test.mods", loadRef(t, `{"module": "plain", "params": {"size": 3}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.(*plain).Size != 3 {
		t.Fatalf("loaded %+v", m)
	}
	m, err = Load("test.mods", loadRef(t, `{"module": "checked"}`))
	if err != nil {
		t.Fatal(err)
	}
	Cleanup(m)
	Cleanup(&plain{}) // has no Cleanup
	if !m.(*checked).cleaned {
		t.Fatal("Cleanup was not called")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		doc  string
		want string
	}{
		{`{"module": "nope"}`, `1:12: handler.module: there is no module "nope" in test.mods (it has "checked", "plain")`},
		{`{"module": "plain"}`, `1:1: handler.params.size: missing required field`},
		{`{"module": "plain", "params": {"size": 0}}`, `1:40: handler.params.size: 0 is less than the minimum 1`},
		{`{"module": "checked", "params": {"name": "plain-error"}}`, `1:33: handler.params: something failed`},
		{`{"module": "checked", "params": {"name": "field-error"}}`, `1:42: handler.params.name: is reserved`},
	}
	for _, tt := range tests {
		_, err := Load("test.mods", loadRef(t, tt.doc))
		if err == nil || err.Error() != tt.want {
			t.Errorf("Load(%s) error = %v, want %s", tt.doc, err, tt.want)
		}
	}
	if _, err := Load("test.empty", loadRef(t, `{"module": "x"}`)); err == nil || err.Error() != `1:12: handler.module: there is no module "x" in test.empty` {
		t.Errorf("empty namespace: %v", err)
	}
}
