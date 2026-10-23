// Package fitconf reads and writes fit's own config format, .conf files: the
// native config written as blocks of directives, as nginx and Caddy write
// theirs, which says what the JSON would, field for field (decision 021).
//
// A directive is a field: its name, then its value. A scalar takes one
// argument, a list of scalars its arguments (on one line or repeated), an
// object a block, a map a block of keys and their values, and a list of
// objects a block for each element, repeated. At the top, the lists of
// objects take their names in the singular, with the element's name as
// the argument: listener web { ... }. A module, a route's handler or one
// of its middleware, is its name and a block of its params: handler proxy
// { upstream app }. A site, site <hosts...> { route <paths...> { ... } },
// writes the routes of its hosts together. Which fields there are, and
// their types, are the config's Go types and each module's params, so
// that every field JSON and YAML can set, a .conf file can too.
package fitconf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

func init() {
	adapter.Register("fit", Adapt, ".conf")
}

var (
	valueType     = reflect.TypeFor[config.Value]()
	moduleRefType = reflect.TypeFor[config.ModuleRef]()
	configType    = reflect.TypeFor[config.Config]()
)

// singular are the names the config's top-level lists of objects take in a
// .conf file, one directive for each element.
var singular = map[string]string{
	"listener": "listeners", "route": "routes", "upstream": "upstreams", "service": "services", "provider": "providers",
}

// Adapt turns a .conf config into native JSON.
func Adapt(data []byte) (*adapter.Result, error) {
	ds, err := parse(data)
	if err != nil {
		return nil, err
	}
	if ds, err = expandSites(ds); err != nil {
		return nil, err
	}
	c := &converter{pos: map[string][2]int{}}
	root, err := c.object(configType, ds, "", true)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	root.write(&b)
	pos := c.pos
	return &adapter.Result{JSON: b.Bytes(), Position: func(path string) (int, int, bool) {
		p, ok := pos[path]
		return p[0], p[1], ok
	}}, nil
}

// jnode is a JSON value being built, an object's members in the order
// they were written.
type jnode struct {
	raw   string // a scalar, as JSON
	keys  []string
	vals  []*jnode
	elems []*jnode
	kind  byte // 'o' object, 'a' array, 's' scalar
}

func scalarNode(raw string) *jnode { return &jnode{kind: 's', raw: raw} }
func stringNode(s string) *jnode {
	b, _ := json.Marshal(s)
	return scalarNode(string(b))
}

func (n *jnode) member(key string) *jnode {
	if i := slices.Index(n.keys, key); i >= 0 {
		return n.vals[i]
	}
	return nil
}

func (n *jnode) set(key string, v *jnode) {
	n.keys = append(n.keys, key)
	n.vals = append(n.vals, v)
}

func (n *jnode) write(b *bytes.Buffer) {
	switch n.kind {
	case 's':
		b.WriteString(n.raw)
	case 'a':
		b.WriteByte('[')
		for i, e := range n.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			e.write(b)
		}
		b.WriteByte(']')
	default:
		b.WriteByte('{')
		for i, k := range n.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			n.vals[i].write(b)
		}
		b.WriteByte('}')
	}
}

// converter turns directives into JSON, and records where each path's
// value was written.
type converter struct {
	pos map[string][2]int
}

func (c *converter) at(path string, t token) {
	if _, ok := c.pos[path]; !ok {
		c.pos[path] = [2]int{t.line, t.col}
	}
}

func field(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func index(path string, i int) string { return fmt.Sprintf("%s[%d]", path, i) }
func key(path, k string) string       { return path + "[" + strconv.Quote(k) + "]" }

// lookup returns the field of fields a directive names, by its name or an
// old one.
func lookup(fields []config.Field, name string) (config.Field, bool) {
	for _, f := range fields {
		if !f.Self && (f.Name == name || slices.Contains(f.Renamed, name)) {
			return f, true
		}
	}
	return config.Field{}, false
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// isObject says values of t are written as a block of fields.
func isObject(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t != config.DurationType && t != valueType && t != moduleRefType
}

// object converts the directives of a block into the members of struct
// type t, at path; root says it is the whole config. A directive no field
// takes is converted as it is written, for the config's check to name.
func (c *converter) object(t reflect.Type, ds []*directive, path string, root bool) (*jnode, error) {
	fields := config.Fields(t)
	obj := &jnode{kind: 'o'}
	for _, d := range ds {
		name := d.name.text
		if root {
			if plural, ok := singular[name]; ok {
				name = plural
			}
		}
		p := field(path, name)
		f, ok := lookup(fields, name)
		if !ok {
			if obj.member(name) != nil {
				return nil, errAt(d.name.line, d.name.col, "%s is given twice", d.name.text)
			}
			c.at(p, d.name)
			v, err := c.generic(d, p, 0)
			if err != nil {
				return nil, err
			}
			obj.set(name, v)
			continue
		}
		ft := deref(f.Type)
		if ft.Kind() == reflect.Slice && ft.Elem() != valueType {
			arr := obj.member(name)
			if arr == nil {
				arr = &jnode{kind: 'a'}
				obj.set(name, arr)
				c.at(p, d.name)
			}
			if err := c.appendTo(arr, ft, &f, d, p); err != nil {
				return nil, err
			}
			continue
		}
		if obj.member(name) != nil {
			return nil, errAt(d.name.line, d.name.col, "%s is given twice; it takes one value", d.name.text)
		}
		v, err := c.value(ft, &f, d, p)
		if err != nil {
			return nil, err
		}
		obj.set(name, v)
	}
	return obj, nil
}

// appendTo adds what directive d gives a list of type t to arr.
func (c *converter) appendTo(arr *jnode, t reflect.Type, f *config.Field, d *directive, path string) error {
	elem := deref(t.Elem())
	switch {
	case elem == moduleRefType:
		p := index(path, len(arr.elems))
		v, err := c.module(f.Module, d, p)
		if err != nil {
			return err
		}
		arr.elems = append(arr.elems, v)
	case isObject(elem):
		p := index(path, len(arr.elems))
		c.at(p, d.name)
		fields := config.Fields(elem)
		_, named := lookup(fields, "name")
		if len(d.args) > 1 || len(d.args) == 1 && !named {
			what := "no arguments"
			if named {
				what = "one argument, its name,"
			}
			return errAt(d.name.line, d.name.col, "%s takes %s and a block", d.name.text, what)
		}
		v, err := c.object(elem, d.block, p, false)
		if err != nil {
			return err
		}
		if len(d.args) == 1 {
			if v.member("name") != nil {
				return errAt(d.args[0].line, d.args[0].col, "the name is given twice, as the argument and in the block")
			}
			c.at(field(p, "name"), d.args[0])
			v.keys = append([]string{"name"}, v.keys...)
			v.vals = append([]*jnode{stringNode(d.args[0].text)}, v.vals...)
		}
		arr.elems = append(arr.elems, v)
	case elem.Kind() == reflect.Slice || elem.Kind() == reflect.Map || elem == valueType:
		p := index(path, len(arr.elems))
		v, err := c.generic(d, p, 0)
		if err != nil {
			return err
		}
		arr.elems = append(arr.elems, v)
	default:
		if d.hasBlock {
			return errAt(d.name.line, d.name.col, "%s takes a list of values, not a block", d.name.text)
		}
		for _, a := range d.args {
			p := index(path, len(arr.elems))
			c.at(p, a)
			arr.elems = append(arr.elems, scalar(elem, a))
		}
	}
	return nil
}

// value converts directive d into a value of type t, which is not a list.
func (c *converter) value(t reflect.Type, f *config.Field, d *directive, path string) (*jnode, error) {
	switch {
	case t == valueType:
		c.at(path, d.name)
		return c.generic(d, path, 0)
	case t == moduleRefType:
		return c.module(f.Module, d, path)
	case isObject(t):
		c.at(path, d.name)
		if len(d.args) > 0 {
			return nil, errAt(d.args[0].line, d.args[0].col, "%s takes a block, not arguments", d.name.text)
		}
		return c.object(t, d.block, path, false)
	case t.Kind() == reflect.Map:
		c.at(path, d.name)
		if len(d.args) > 0 {
			return nil, errAt(d.args[0].line, d.args[0].col, "%s takes a block of keys and their values, not arguments", d.name.text)
		}
		return c.mapBlock(deref(t.Elem()), d.block, path)
	}
	if d.hasBlock {
		return nil, errAt(d.name.line, d.name.col, "%s takes a value, not a block", d.name.text)
	}
	if len(d.args) == 0 {
		if t.Kind() == reflect.Bool {
			c.at(path, d.name)
			return scalarNode("true"), nil
		}
		return nil, errAt(d.name.line, d.name.col, "%s takes a value", d.name.text)
	}
	if len(d.args) > 1 {
		return nil, errAt(d.args[1].line, d.args[1].col, "%s takes one value; quote one that holds spaces", d.name.text)
	}
	c.at(path, d.args[0])
	return scalar(t, d.args[0]), nil
}

// mapBlock converts a block of keys and their values into an object
// whose values are of type t.
func (c *converter) mapBlock(t reflect.Type, ds []*directive, path string) (*jnode, error) {
	obj := &jnode{kind: 'o'}
	for _, d := range ds {
		k := d.name.text
		if obj.member(k) != nil {
			return nil, errAt(d.name.line, d.name.col, "%q is a key twice", k)
		}
		p := key(path, k)
		var v *jnode
		var err error
		switch {
		case t.Kind() == reflect.Slice && !isObject(deref(t.Elem())) && t.Elem() != valueType:
			if d.hasBlock {
				return nil, errAt(d.name.line, d.name.col, "%q takes a list of values, not a block", k)
			}
			c.at(p, d.name)
			v = &jnode{kind: 'a'}
			for _, a := range d.args {
				c.at(index(p, len(v.elems)), a)
				v.elems = append(v.elems, scalar(deref(t.Elem()), a))
			}
		default:
			v, err = c.value(t, nil, d, p)
		}
		if err != nil {
			return nil, err
		}
		c.at(p, d.name)
		obj.set(k, v)
	}
	return obj, nil
}

// module converts a module reference: the module's name as the argument,
// then its params, in a block, as the module's params type has them.
func (c *converter) module(ns string, d *directive, path string) (*jnode, error) {
	c.at(path, d.name)
	if len(d.args) != 1 {
		var names []string
		for _, info := range module.Namespace(ns) {
			names = append(names, info.Name())
		}
		return nil, errAt(d.name.line, d.name.col, "%s takes the name of a module, one of %s, then its params in a block",
			d.name.text, strings.Join(names, ", "))
	}
	name := d.args[0]
	c.at(field(path, "module"), name)
	ref := &jnode{kind: 'o'}
	ref.set("module", stringNode(name.text))
	if !d.hasBlock {
		return ref, nil
	}
	p := field(path, "params")
	c.at(p, d.name)
	var params *jnode
	var err error
	if info, ok := module.Lookup(ns + "." + name.text); ok {
		params, err = c.object(reflect.TypeOf(info.New()).Elem(), d.block, p, false)
	} else {
		// The config's check says there is no such module.
		params, err = c.generic(&directive{name: d.name, block: d.block, hasBlock: true}, p, 0)
	}
	if err != nil {
		return nil, err
	}
	ref.set("params", params)
	return ref, nil
}

// generic converts a directive of no known type: a block is an object of
// its directives, no arguments true, one argument a scalar and several a
// list of them.
func (c *converter) generic(d *directive, path string, depth int) (*jnode, error) {
	if depth > maxDepth {
		return nil, errAt(d.name.line, d.name.col, "nested too deep")
	}
	switch {
	case d.hasBlock && len(d.args) > 0:
		return nil, errAt(d.args[0].line, d.args[0].col, "%s takes either arguments or a block", d.name.text)
	case d.hasBlock:
		obj := &jnode{kind: 'o'}
		for _, e := range d.block {
			if obj.member(e.name.text) != nil {
				return nil, errAt(e.name.line, e.name.col, "%s is given twice", e.name.text)
			}
			p := field(path, e.name.text)
			c.at(p, e.name)
			c.at(key(path, e.name.text), e.name)
			v, err := c.generic(e, p, depth+1)
			if err != nil {
				return nil, err
			}
			obj.set(e.name.text, v)
		}
		return obj, nil
	case len(d.args) == 0:
		return scalarNode("true"), nil
	case len(d.args) == 1:
		return infer(d.args[0]), nil
	}
	arr := &jnode{kind: 'a'}
	for _, a := range d.args {
		c.at(index(path, len(arr.elems)), a)
		arr.elems = append(arr.elems, infer(a))
	}
	return arr, nil
}

// scalar converts an argument into a value of scalar type t. One that is
// not what t takes is kept as a string, for the config's check to report
// at its place.
func scalar(t reflect.Type, a token) *jnode {
	if !a.quoted {
		switch t.Kind() {
		case reflect.Bool:
			if a.text == "true" || a.text == "false" {
				return scalarNode(a.text)
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			if isNumber(a.text) {
				return scalarNode(a.text)
			}
		}
	}
	return stringNode(a.text)
}

// infer converts an argument of no known type: a number, true, false or
// null as JSON has them, unless quoted; anything else a string.
func infer(a token) *jnode {
	if !a.quoted && (a.text == "true" || a.text == "false" || a.text == "null" || isNumber(a.text)) {
		return scalarNode(a.text)
	}
	return stringNode(a.text)
}

func isNumber(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return false
	}
	_, ok := v.(float64)
	return ok
}
