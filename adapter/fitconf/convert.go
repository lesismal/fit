// Package fitconf reads and writes fit's own config format, .konf files: the
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
// that every field JSON and YAML can set, a .konf file can too.
package fitconf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

func init() {
	adapter.Register("fit", Adapt, ".konf")
}

var (
	tlsType       = reflect.TypeFor[config.TLS]()
	valueType     = reflect.TypeFor[config.Value]()
	moduleRefType = reflect.TypeFor[config.ModuleRef]()
	configType    = reflect.TypeFor[config.Config]()
)

// singular are the names the config's top-level lists of objects take in a
// .konf file, one directive for each element.
var singular = map[string]string{
	"listener": "listeners", "route": "routes", "upstream": "upstreams", "service": "services", "provider": "providers",
}

// shorthands are the fields an element of a top-level list takes as
// arguments after its name, in order: listener web :8080 http/1.1 h2c is
// listener web { address :8080; protocols http/1.1 h2c }. The last, a list,
// takes every argument left.
var shorthands = map[reflect.Type][]string{
	reflect.TypeFor[config.Listener](): {"address", "protocols"},
	reflect.TypeFor[config.Upstream](): {"addresses"},
}

// Adapt turns a .konf config into native JSON. It cannot include other
// files, nor import them, as it has no file to find them from: AdaptFile
// can.
func Adapt(data []byte) (*adapter.Result, error) {
	return AdaptFile("", data, nil)
}

// AdaptFile turns the .konf config file at path into native JSON, the
// files it includes read with include. The files it imports are not read:
// adapter.Files finds them, and adapter.AdaptSet merges them after it.
func AdaptFile(path string, data []byte, include adapter.Includer) (*adapter.Result, error) {
	r := newReader(path, include)
	ds, err := r.read(path, data, true)
	if err != nil {
		return nil, err
	}
	if ds, err = expandSites(ds); err != nil {
		return nil, err
	}
	c := &converter{pos: map[string]token{}}
	root, err := c.object(configType, ds, "", true)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	root.write(&b)
	pos := c.pos
	res := &adapter.Result{JSON: b.Bytes(), Position: func(p string) (int, int, bool) {
		t, ok := pos[p]
		return t.line, t.col, ok
	}}
	if include != nil {
		res.File = func(p string) string {
			if t := pos[p]; t.file != path {
				return t.file
			}
			return ""
		}
	}
	return res, nil
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
	pos map[string]token // where each path's value was written
}

func (c *converter) at(path string, t token) {
	if _, ok := c.pos[path]; !ok {
		c.pos[path] = token{file: t.file, line: t.line, col: t.col}
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
				return nil, errTok(d.name, "%s is given twice", d.name.text)
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
			return nil, errTok(d.name, "%s is given twice; it takes one value", d.name.text)
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
		short := shorthands[elem]
		if len(d.args) > 1 && short == nil || len(d.args) == 1 && !named {
			what := "no arguments"
			if named {
				what = "one argument, its name,"
			}
			return errTok(d.name, "%s takes %s and a block", d.name.text, what)
		}
		block := d.block
		if len(d.args) > 1 {
			var err error
			if block, err = shorthand(d, d.args[1:], short); err != nil {
				return err
			}
			d = &directive{name: d.name, args: d.args[:1], block: block, hasBlock: true}
		}
		v, err := c.object(elem, block, p, false)
		if err != nil {
			return err
		}
		if len(d.args) == 1 {
			if v.member("name") != nil {
				return errTok(d.args[0], "the name is given twice, as the argument and in the block")
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
			return errTok(d.name, "%s takes a list of values, not a block", d.name.text)
		}
		for _, a := range d.args {
			p := index(path, len(arr.elems))
			c.at(p, a)
			arr.elems = append(arr.elems, scalar(elem, a))
		}
	}
	return nil
}

// shorthand returns the block of d with the fields its arguments after its
// name, args, give, by the shorthand fields: each one argument, but the
// last, a list, which takes those left.
func shorthand(d *directive, args []token, fields []string) ([]*directive, error) {
	var out []*directive
	for i, name := range fields {
		if len(args) == 0 {
			break
		}
		if slices.ContainsFunc(d.block, func(b *directive) bool { return b.name.text == name }) {
			return nil, errTok(args[0], "%s is given twice, as an argument and in the block", name)
		}
		n := 1
		if i == len(fields)-1 {
			n = len(args)
		}
		a := args[0]
		out = append(out, &directive{name: token{text: name, file: a.file, line: a.line, col: a.col}, args: args[:n]})
		args = args[n:]
	}
	if len(args) > 0 {
		return nil, errTok(args[0], "%s takes its name, then %s, then a block", d.name.text, strings.Join(fields, " and "))
	}
	return append(out, d.block...), nil
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
		block := d.block
		switch {
		case t == tlsType && len(d.args) == 2:
			// tls <cert file> <key file> is a certificate, before any
			// the block gives.
			a := d.args
			cert := &directive{name: token{text: "certificates", file: a[0].file, line: a[0].line, col: a[0].col}, hasBlock: true, block: []*directive{
				{name: token{text: "cert_file", file: a[0].file, line: a[0].line, col: a[0].col}, args: a[:1]},
				{name: token{text: "key_file", file: a[1].file, line: a[1].line, col: a[1].col}, args: a[1:2]},
			}}
			block = append([]*directive{cert}, d.block...)
		case t == tlsType && len(d.args) > 0:
			return nil, errTok(d.args[0], "tls takes a certificate's file and its key's, as in tls cert.pem key.pem, or a block, or both")
		case len(d.args) > 0:
			return nil, errTok(d.args[0], "%s takes a block, not arguments", d.name.text)
		}
		return c.object(t, block, path, false)
	case t.Kind() == reflect.Map:
		c.at(path, d.name)
		if len(d.args) > 0 {
			return nil, errTok(d.args[0], "%s takes a block of keys and their values, not arguments", d.name.text)
		}
		return c.mapBlock(deref(t.Elem()), d.block, path)
	}
	if d.hasBlock {
		return nil, errTok(d.name, "%s takes a value, not a block", d.name.text)
	}
	if len(d.args) == 0 {
		if t.Kind() == reflect.Bool {
			c.at(path, d.name)
			return scalarNode("true"), nil
		}
		return nil, errTok(d.name, "%s takes a value", d.name.text)
	}
	if len(d.args) > 1 {
		return nil, errTok(d.args[1], "%s takes one value; quote one that holds spaces", d.name.text)
	}
	c.at(path, d.args[0])
	if a := d.args[0]; !a.quoted && isBytesField(path, t) {
		if n, ok := parseBytes(a.text); ok {
			return scalarNode(strconv.FormatInt(n, 10)), nil
		}
	}
	return scalar(t, d.args[0]), nil
}

// isBytesField says the field at path, of type t, is a number of bytes: an
// integer whose name ends in bytes or _size.
func isBytesField(path string, t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Int, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint32, reflect.Uint64:
	default:
		return false
	}
	if strings.HasSuffix(path, "]") {
		return false
	}
	name := path[strings.LastIndexByte(path, '.')+1:]
	return strings.HasSuffix(name, "bytes") || strings.HasSuffix(name, "_size")
}

// byteUnits are the units a number of bytes may be written in, as 16MiB or
// 2GB: powers of 1024 and of 1000, the case of the letters not minded.
var byteUnits = []struct {
	name string
	n    int64
}{
	{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
	{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12}, {"b", 1},
}

// parseBytes reads a whole number of bytes in a unit, as 16MiB, or false.
// A number alone is left as it is.
func parseBytes(s string) (int64, bool) {
	lower := strings.ToLower(s)
	for _, u := range byteUnits {
		num, ok := strings.CutSuffix(lower, u.name)
		if !ok || num == "" {
			continue
		}
		v, err := strconv.ParseInt(num, 10, 64)
		if err != nil || v < 0 || v > math.MaxInt64/u.n {
			return 0, false
		}
		return v * u.n, true
	}
	return 0, false
}

// formatBytes writes n in the largest power of 1024 it is a whole number
// of, from KiB up, or as it is.
func formatBytes(n int64) string {
	for _, u := range []struct {
		name string
		n    int64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if n > 0 && n%u.n == 0 {
			return strconv.FormatInt(n/u.n, 10) + u.name
		}
	}
	return strconv.FormatInt(n, 10)
}

// mapBlock converts a block of keys and their values into an object
// whose values are of type t.
func (c *converter) mapBlock(t reflect.Type, ds []*directive, path string) (*jnode, error) {
	obj := &jnode{kind: 'o'}
	for _, d := range ds {
		k := d.name.text
		if obj.member(k) != nil {
			return nil, errTok(d.name, "%q is a key twice", k)
		}
		p := key(path, k)
		var v *jnode
		var err error
		switch {
		case t.Kind() == reflect.Slice && !isObject(deref(t.Elem())) && t.Elem() != valueType:
			if d.hasBlock {
				return nil, errTok(d.name, "%q takes a list of values, not a block", k)
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
		return nil, errTok(d.name, "%s takes the name of a module, one of %s, then its params in a block",
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
		return nil, errTok(d.name, "nested too deep")
	}
	switch {
	case d.hasBlock && len(d.args) > 0:
		return nil, errTok(d.args[0], "%s takes either arguments or a block", d.name.text)
	case d.hasBlock:
		obj := &jnode{kind: 'o'}
		for _, e := range d.block {
			if obj.member(e.name.text) != nil {
				return nil, errTok(e.name, "%s is given twice", e.name.text)
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
