package config

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The decoder fills Go values from parsed JSON by these struct tags:
//
//	json:"name"        the member's name; options after a comma are ignored,
//	                   and "-" leaves the field out
//	fit:"required"     the member must be present
//	fit:"self"         on a Value field tagged json:"-": the struct's own
//	                   object, for later checks to report positions in it
//	enum:"a,b,c"       a string, or each string of a slice, must be one of
//	                   these
//	min:"n" max:"n"    a number must lie within these bounds
//	doc:"..."          what the field is, for the JSON Schema
//	module:"ns"        on a ModuleRef: the namespace its module comes from
//
// Names are matched exactly: unlike encoding/json, a member whose case
// differs from the field's name is an unknown field, not the same one.

// Field describes how one struct field is decoded. The schema package reads
// the same descriptions, so that the schema and the decoder cannot disagree.
type Field struct {
	Index    int
	Name     string
	Type     reflect.Type
	Required bool
	Self     bool
	Enum     []string
	Min, Max *float64
	Doc      string
	Module   string
}

var fieldCache sync.Map // reflect.Type → []Field

// Fields returns how the fields of struct type t are decoded, in the order
// they are declared. It panics on a tag it cannot read, which is a mistake
// in the type rather than in a config.
func Fields(t reflect.Type) []Field {
	if fs, ok := fieldCache.Load(t); ok {
		return fs.([]Field)
	}
	var fs []Field
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		f := Field{Index: i, Type: sf.Type, Doc: sf.Tag.Get("doc"), Module: sf.Tag.Get("module")}
		name, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		for opt := range strings.SplitSeq(sf.Tag.Get("fit"), ",") {
			switch opt {
			case "":
			case "required":
				f.Required = true
			case "self":
				f.Self = true
			default:
				panic(fmt.Sprintf("config: %s.%s: unknown fit tag option %q", t, sf.Name, opt))
			}
		}
		if f.Self {
			if name != "-" || sf.Type != valueType {
				panic(fmt.Sprintf("config: %s.%s: fit:\"self\" needs a Value field tagged json:\"-\"", t, sf.Name))
			}
			fs = append(fs, f)
			continue
		}
		if name == "-" {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		f.Name = name
		if enum := sf.Tag.Get("enum"); enum != "" {
			f.Enum = strings.Split(enum, ",")
		}
		f.Min = parseBound(t, sf, "min")
		f.Max = parseBound(t, sf, "max")
		fs = append(fs, f)
	}
	fieldCache.Store(t, fs)
	return fs
}

func parseBound(t reflect.Type, sf reflect.StructField, tag string) *float64 {
	s, ok := sf.Tag.Lookup(tag)
	if !ok {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		panic(fmt.Sprintf("config: %s.%s: bad %s tag %q", t, sf.Name, tag, s))
	}
	return &v
}

var valueType = reflect.TypeFor[Value]()

type decoder struct {
	errs Errors
}

func (d *decoder) errorf(n *node, off int, path, format string, args ...any) {
	line, col := n.pos(off)
	d.errs = append(d.errs, &Error{Path: path, Line: line, Col: col, Msg: fmt.Sprintf(format, args...)})
}

func (d *decoder) decodeInto(n *node, path string, dst any) {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		panic(fmt.Sprintf("config: decoding into %T, which is not a non-nil pointer", dst))
	}
	d.decode(n, path, rv.Elem(), nil)
}

// decode decodes n into rv. f is the struct field rv is, when it is one,
// whose tags constrain the value.
func (d *decoder) decode(n *node, path string, rv reflect.Value, f *Field) {
	t := rv.Type()
	if t == valueType {
		rv.Set(reflect.ValueOf(Value{n: n, path: path}))
		return
	}
	if t.Kind() == reflect.Pointer {
		if n.kind == kindNull {
			rv.SetZero()
			return
		}
		if rv.IsNil() {
			rv.Set(reflect.New(t.Elem()))
		}
		d.decode(n, path, rv.Elem(), f)
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		if !d.expect(n, path, kindObject) {
			return
		}
		d.decodeStruct(n, path, rv)
	case reflect.Slice:
		if !d.expect(n, path, kindArray) {
			return
		}
		s := reflect.MakeSlice(t, len(n.elems), len(n.elems))
		for i, e := range n.elems {
			d.decode(e, joinIndex(path, i), s.Index(i), f)
		}
		rv.Set(s)
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic(fmt.Sprintf("config: unsupported map key type in %s", t))
		}
		if !d.expect(n, path, kindObject) {
			return
		}
		m := reflect.MakeMapWithSize(t, len(n.members))
		for _, mem := range n.members {
			elem := reflect.New(t.Elem()).Elem()
			d.decode(mem.val, joinKey(path, mem.key), elem, nil)
			m.SetMapIndex(reflect.ValueOf(mem.key).Convert(t.Key()), elem)
		}
		rv.Set(m)
	case reflect.String:
		if !d.expect(n, path, kindString) {
			return
		}
		if f != nil && len(f.Enum) > 0 && !slices.Contains(f.Enum, n.s) {
			d.errorf(n, n.off, path, "%q is not one of %s", n.s, quoteList(f.Enum))
			return
		}
		rv.SetString(n.s)
	case reflect.Bool:
		if !d.expect(n, path, kindBool) {
			return
		}
		rv.SetBool(n.b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if !d.expect(n, path, kindNumber) {
			return
		}
		v, err := strconv.ParseInt(n.s, 10, t.Bits())
		if err != nil {
			d.errorf(n, n.off, path, "%s is not an integer in range for %s", n.s, t)
			return
		}
		if d.inBounds(n, path, float64(v), f) {
			rv.SetInt(v)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !d.expect(n, path, kindNumber) {
			return
		}
		v, err := strconv.ParseUint(n.s, 10, t.Bits())
		if err != nil {
			d.errorf(n, n.off, path, "%s is not a non-negative integer in range for %s", n.s, t)
			return
		}
		if d.inBounds(n, path, float64(v), f) {
			rv.SetUint(v)
		}
	case reflect.Float32, reflect.Float64:
		if !d.expect(n, path, kindNumber) {
			return
		}
		v, err := strconv.ParseFloat(n.s, t.Bits())
		if err != nil || math.IsInf(v, 0) {
			d.errorf(n, n.off, path, "%s is out of range for %s", n.s, t)
			return
		}
		if d.inBounds(n, path, v, f) {
			rv.SetFloat(v)
		}
	default:
		panic(fmt.Sprintf("config: unsupported type %s", t))
	}
}

func (d *decoder) decodeStruct(n *node, path string, rv reflect.Value) {
	fields := Fields(rv.Type())
	seen := make([]bool, len(fields))
	for _, mem := range n.members {
		i := slices.IndexFunc(fields, func(f Field) bool { return !f.Self && f.Name == mem.key })
		if i < 0 {
			d.errorf(n, mem.keyOff, joinField(path, mem.key), "unknown field%s", suggest(mem.key, fields))
			continue
		}
		seen[i] = true
		d.decode(mem.val, joinField(path, mem.key), rv.Field(fields[i].Index), &fields[i])
	}
	for i, f := range fields {
		switch {
		case f.Self:
			rv.Field(f.Index).Set(reflect.ValueOf(Value{n: n, path: path}))
		case seen[i]:
		case f.Required:
			d.errorf(n, n.off, joinField(path, f.Name), "missing required field")
		case f.Type == valueType:
			// An absent Value still knows where it would have been.
			rv.Field(f.Index).Set(reflect.ValueOf(Value{path: joinField(path, f.Name), at: n}))
		}
	}
}

func (d *decoder) expect(n *node, path string, k kind) bool {
	if n.kind == k {
		return true
	}
	d.errorf(n, n.off, path, "expected %s, got %s", k, n.kind)
	return false
}

func (d *decoder) inBounds(n *node, path string, v float64, f *Field) bool {
	if f == nil {
		return true
	}
	if f.Min != nil && v < *f.Min {
		d.errorf(n, n.off, path, "%s is less than the minimum %v", n.s, *f.Min)
		return false
	}
	if f.Max != nil && v > *f.Max {
		d.errorf(n, n.off, path, "%s is greater than the maximum %v", n.s, *f.Max)
		return false
	}
	return true
}

// suggest names the field key was probably meant to be, when one differs
// from it only in case.
func suggest(key string, fields []Field) string {
	for _, f := range fields {
		if !f.Self && strings.EqualFold(f.Name, key) {
			return fmt.Sprintf(" (did you mean %q?)", f.Name)
		}
	}
	return ""
}

func quoteList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return strings.Join(quoted, ", ")
}
