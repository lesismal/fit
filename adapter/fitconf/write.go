package fitconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// FromJSON writes native JSON as a .konf file, field for field, with tabs
// for indentation: what Adapt turns back into the same config. Routes are
// written one by one, not gathered into sites.
func FromJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := readJSON(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON: more after the value")
	}
	obj, ok := v.(*ordered)
	if !ok {
		return nil, errors.New("JSON: a config is an object")
	}
	w := &writer{}
	if err := w.object(configType, obj, 0, true); err != nil {
		return nil, err
	}
	return w.b.Bytes(), nil
}

// ordered is a JSON object, its members in order.
type ordered struct {
	keys []string
	vals []any
}

func (o *ordered) get(k string) (any, bool) {
	for i, key := range o.keys {
		if key == k {
			return o.vals[i], true
		}
	}
	return nil, false
}

// readJSON reads a value: an *ordered, a []any, a string, a json.Number,
// a bool or nil.
func readJSON(dec *json.Decoder, depth int) (any, error) {
	if depth > 256 {
		return nil, errors.New("JSON: nested too deep")
	}
	t, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("JSON: %w", err)
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &ordered{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("JSON: %w", err)
				}
				v, err := readJSON(dec, depth+1)
				if err != nil {
					return nil, err
				}
				o.keys = append(o.keys, kt.(string))
				o.vals = append(o.vals, v)
			}
			_, err := dec.Token()
			return o, err
		case '[':
			a := []any{}
			for dec.More() {
				v, err := readJSON(dec, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := dec.Token()
			return a, err
		}
	}
	return t, nil
}

type writer struct {
	b bytes.Buffer
}

func (w *writer) line(depth int, words ...string) {
	w.b.WriteString(strings.Repeat("\t", depth))
	w.b.WriteString(strings.Join(words, " "))
	w.b.WriteByte('\n')
}

// object writes the members of obj, a value of struct type t.
func (w *writer) object(t reflect.Type, obj *ordered, depth int, root bool) error {
	fields := config.Fields(t)
	prevBlock := false // at the top, a blank line goes around blocks
	for i, k := range obj.keys {
		v := obj.vals[i]
		if v == nil {
			continue
		}
		f, ok := lookup(fields, k)
		if !ok {
			if err := w.generic(k, v, depth); err != nil {
				return err
			}
			continue
		}
		ft := deref(f.Type)
		block := ft.Kind() == reflect.Slice && isObject(deref(ft.Elem())) || isObject(ft) || ft == moduleRefType
		if root && w.b.Len() > 0 && (block || prevBlock) {
			w.b.WriteByte('\n')
		}
		prevBlock = block
		if err := w.field(k, ft, &f, v, depth, root); err != nil {
			return err
		}
	}
	return nil
}

// field writes the member k of type t.
func (w *writer) field(k string, t reflect.Type, f *config.Field, v any, depth int, root bool) error {
	switch {
	case t == valueType:
		return w.generic(k, v, depth)
	case t == moduleRefType:
		return w.module(k, f.Module, v, depth)
	case isObject(t):
		o, ok := v.(*ordered)
		if !ok {
			return w.generic(k, v, depth)
		}
		return w.block(k, nil, depth, func() error { return w.object(t, o, depth+1, false) }, len(o.keys) == 0)
	case t.Kind() == reflect.Map:
		o, ok := v.(*ordered)
		if !ok {
			return w.generic(k, v, depth)
		}
		return w.block(k, nil, depth, func() error {
			for i, mk := range o.keys {
				if err := w.mapEntry(mk, deref(t.Elem()), o.vals[i], depth+1); err != nil {
					return err
				}
			}
			return nil
		}, len(o.keys) == 0)
	case t.Kind() == reflect.Slice:
		a, ok := v.([]any)
		if !ok {
			return w.generic(k, v, depth)
		}
		elem := deref(t.Elem())
		switch {
		case elem == moduleRefType:
			for _, e := range a {
				if err := w.module(k, f.Module, e, depth); err != nil {
					return err
				}
			}
			return nil
		case isObject(elem):
			name := k
			if root {
				for s, p := range singular {
					if p == k {
						name = s
					}
				}
			}
			for i, e := range a {
				o, ok := e.(*ordered)
				if !ok {
					return fmt.Errorf("%s[%d]: an object, not %s", k, i, describe(e))
				}
				var args []string
				_, named := lookup(config.Fields(elem), "name")
				if n, ok := o.get("name"); ok && named {
					if s, ok := n.(string); ok {
						args = []string{word(s)}
						o = without(o, "name")
					}
				}
				if root && i > 0 {
					w.b.WriteByte('\n')
				}
				if err := w.block(name, args, depth, func() error { return w.object(elem, o, depth+1, false) }, len(o.keys) == 0); err != nil {
					return err
				}
			}
			return nil
		case elem.Kind() == reflect.Slice || elem.Kind() == reflect.Map || elem == valueType:
			return fmt.Errorf("%s: a list of %s, which a .konf file does not hold", k, elem)
		}
		words, err := scalars(a, elem.Kind() != reflect.String)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		w.line(depth, append([]string{word(k)}, words...)...)
		return nil
	}
	// A string where the field takes another kind is quoted, so that it
	// reads back a string, for the config's check to report.
	s, err := scalarWord(v, t.Kind() != reflect.String)
	if err != nil {
		return fmt.Errorf("%s: %w", k, err)
	}
	w.line(depth, word(k), s)
	return nil
}

func (w *writer) mapEntry(k string, t reflect.Type, v any, depth int) error {
	if a, ok := v.([]any); ok && t.Kind() == reflect.Slice && !isObject(deref(t.Elem())) {
		words, err := scalars(a, deref(t.Elem()).Kind() != reflect.String)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		w.line(depth, append([]string{word(k)}, words...)...)
		return nil
	}
	return w.field(k, t, nil, v, depth, false)
}

// module writes a module reference: k, the module's name, and its params.
func (w *writer) module(k, ns string, v any, depth int) error {
	o, ok := v.(*ordered)
	if !ok {
		return fmt.Errorf("%s: a module is an object, not %s", k, describe(v))
	}
	m, _ := o.get("module")
	name, ok := m.(string)
	if !ok {
		return fmt.Errorf("%s: a module needs its name, in module", k)
	}
	for _, key := range o.keys {
		if key != "module" && key != "params" {
			return fmt.Errorf("%s: a module has a module and params, not %s", k, key)
		}
	}
	p, _ := o.get("params")
	if p == nil {
		w.line(depth, word(k), word(name))
		return nil
	}
	params, ok := p.(*ordered)
	if !ok {
		return fmt.Errorf("%s: a module's params are an object, not %s", k, describe(p))
	}
	return w.block(k, []string{word(name)}, depth, func() error {
		if info, found := module.Lookup(ns + "." + name); found {
			return w.object(reflect.TypeOf(info.New()).Elem(), params, depth+1, false)
		}
		for i, pk := range params.keys {
			if err := w.generic(pk, params.vals[i], depth+1); err != nil {
				return err
			}
		}
		return nil
	}, len(params.keys) == 0)
}

// block writes name and args, then a block of what body writes, or {} when
// empty is true.
func (w *writer) block(name string, args []string, depth int, body func() error, empty bool) error {
	words := append([]string{word(name)}, args...)
	if empty {
		w.line(depth, append(words, "{}")...)
		return nil
	}
	w.line(depth, append(words, "{")...)
	if err := body(); err != nil {
		return err
	}
	w.line(depth, "}")
	return nil
}

// generic writes a member of no known type, as Adapt reads one back: an
// object as a block, true with no arguments, and a list of two scalars or
// more as arguments.
func (w *writer) generic(k string, v any, depth int) error {
	switch v := v.(type) {
	case nil:
		w.line(depth, word(k), "null")
		return nil
	case *ordered:
		return w.block(k, nil, depth, func() error {
			for i, mk := range v.keys {
				if err := w.generic(mk, v.vals[i], depth+1); err != nil {
					return err
				}
			}
			return nil
		}, len(v.keys) == 0)
	case []any:
		if len(v) < 2 {
			return fmt.Errorf("%s: a list of fewer than two values reads back as one, in a field of no known type", k)
		}
		words, err := scalars(v, true)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		w.line(depth, append([]string{word(k)}, words...)...)
		return nil
	case bool:
		if v {
			w.line(depth, word(k))
			return nil
		}
	}
	s, err := scalarWord(v, true)
	if err != nil {
		return fmt.Errorf("%s: %w", k, err)
	}
	w.line(depth, word(k), s)
	return nil
}

// scalars writes a list's elements as arguments; typeless says the
// field's type is not known, as for scalarWord.
func scalars(a []any, typeless bool) ([]string, error) {
	words := make([]string, 0, len(a))
	for _, e := range a {
		s, err := scalarWord(e, typeless)
		if err != nil {
			return nil, err
		}
		words = append(words, s)
	}
	return words, nil
}

// scalarWord writes a scalar as an argument; typeless says the field's
// type is not known, or not a string, so that a string that reads as
// another kind is quoted.
func scalarWord(v any, typeless bool) (string, error) {
	switch v := v.(type) {
	case string:
		if typeless && (v == "true" || v == "false" || v == "null" || isNumber(v)) {
			return quote(v), nil
		}
		return word(v), nil
	case json.Number:
		return string(v), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	}
	return "", fmt.Errorf("%s is not a value an argument holds", describe(v))
}

// Word writes s as an argument of a .konf file: as it is when it reads back
// so, else quoted.
func Word(s string) string { return word(s) }

// word writes s as a word: as it is when it reads back so, else quoted.
func word(s string) string {
	if s == "" || s == "{" || s == "}" || s == "{}" || strings.ContainsAny(s, " \t\r\n\"`") ||
		strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") || strings.HasSuffix(s, ";") || strings.Contains(s, "{$") {
		return quote(s)
	}
	return s
}

// quote writes s in double quotes, its { escaped where {$ would expand.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`, "\r", `\r`, "{$", `\{$`)
	return `"` + r.Replace(s) + `"`
}

func without(o *ordered, k string) *ordered {
	out := &ordered{}
	for i, key := range o.keys {
		if key != k {
			out.keys = append(out.keys, key)
			out.vals = append(out.vals, o.vals[i])
		}
	}
	return out
}

func describe(v any) string {
	switch v.(type) {
	case *ordered:
		return "an object"
	case []any:
		return "a list"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	}
	return "null"
}
