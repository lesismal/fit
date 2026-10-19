// Package schema generates the JSON Schema of fit's native config.
//
// The schema is built from the same Go types and struct tags the config
// decoder reads, and from the modules registered, so it cannot drift from
// what fit accepts. fit.schema.json holds the schema for the modules in
// package modules/standard; a test fails when it is out of date, and
// `go test ./schema -update` rewrites it.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// ID is the schema's $id.
const ID = "https://github.com/lesismal/fit/schema/fit.schema.json"

type object = map[string]any

// Generate returns the schema, as indented JSON, for the modules registered
// in the binary.
func Generate() ([]byte, error) {
	g := &generator{defs: object{}}
	root := g.structSchema(reflect.TypeFor[config.Config]())
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["$id"] = ID
	root["title"] = "fit config"
	root["$defs"] = g.defs

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type generator struct {
	defs object
}

var (
	valueType     = reflect.TypeFor[config.Value]()
	moduleRefType = reflect.TypeFor[config.ModuleRef]()
)

func ref(name string) object { return object{"$ref": "#/$defs/" + name} }

// typeSchema returns the schema of a value of type t; f is the struct field
// it is, or is an element of, whose tags constrain it.
func (g *generator) typeSchema(t reflect.Type, f *config.Field) object {
	switch {
	case t == valueType:
		return object{}
	case t == moduleRefType:
		if f == nil || f.Module == "" {
			panic(fmt.Sprintf("schema: a ModuleRef needs a module tag naming its namespace (%v)", f))
		}
		g.defineModuleRef(f.Module)
		return ref(f.Module)
	}
	switch t.Kind() {
	case reflect.Pointer:
		return g.typeSchema(t.Elem(), f)
	case reflect.Struct:
		if t.Name() == "" {
			return g.structSchema(t)
		}
		name := t.String()
		if _, ok := g.defs[name]; !ok {
			g.defs[name] = object{} // a placeholder, in case the type refers to itself
			g.defs[name] = g.structSchema(t)
		}
		return ref(name)
	case reflect.Slice:
		return object{"type": "array", "items": g.typeSchema(t.Elem(), f)}
	case reflect.Map:
		return object{"type": "object", "additionalProperties": g.typeSchema(t.Elem(), nil)}
	case reflect.String:
		s := object{"type": "string"}
		if f != nil && len(f.Enum) > 0 {
			s["enum"] = f.Enum
		}
		return s
	case reflect.Bool:
		return object{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return bounded(object{"type": "integer"}, f)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		s := bounded(object{"type": "integer"}, f)
		if _, ok := s["minimum"]; !ok {
			s["minimum"] = 0
		}
		return s
	case reflect.Float32, reflect.Float64:
		return bounded(object{"type": "number"}, f)
	default:
		panic(fmt.Sprintf("schema: unsupported type %s", t))
	}
}

func bounded(s object, f *config.Field) object {
	if f != nil && f.Min != nil {
		s["minimum"] = *f.Min
	}
	if f != nil && f.Max != nil {
		s["maximum"] = *f.Max
	}
	return s
}

func (g *generator) structSchema(t reflect.Type) object {
	props := object{}
	required := []string{}
	for _, f := range config.Fields(t) {
		if f.Self {
			continue
		}
		s := g.typeSchema(f.Type, &f)
		if f.Doc != "" {
			if _, isRef := s["$ref"]; isRef {
				// Keywords beside $ref apply as well, so the description can
				// sit next to it.
				s = object{"$ref": s["$ref"], "description": f.Doc}
			} else {
				s["description"] = f.Doc
			}
		}
		props[f.Name] = s
		if f.Required {
			required = append(required, f.Name)
		}
	}
	s := object{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// defineModuleRef defines the schema of a module ref in namespace ns: the
// module's name is one of those registered there, and its params follow
// that module's schema.
func (g *generator) defineModuleRef(ns string) {
	if _, ok := g.defs[ns]; ok {
		return
	}
	names := []string{}
	cases := []any{}
	for _, info := range module.Namespace(ns) {
		names = append(names, info.Name())
		g.defs[info.ID] = g.structSchema(reflect.TypeOf(info.New()).Elem())
		cases = append(cases, object{
			"if":   object{"properties": object{"module": object{"const": info.Name()}}},
			"then": object{"properties": object{"params": ref(info.ID)}},
		})
	}
	s := g.structSchema(moduleRefType)
	s["properties"].(object)["module"].(object)["enum"] = names
	if len(cases) > 0 {
		s["allOf"] = cases
	}
	g.defs[ns] = s
}
