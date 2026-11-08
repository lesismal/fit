package fitsync

import (
	"encoding/base64"
	"fmt"
	"math"
	"path"
	"slices"
	"strconv"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// The built-in transforms: filter, route, fields, key, cast, mask and
// envelope. Each reads a record's fields from its maps' top level, by name,
// and changes a record only through Own, so a chain copies it once.

func init() {
	for _, t := range []struct {
		name, doc string
		new       func() any
	}{
		{"filter", "Keeps, or with action drop drops, the records that match all of its conditions: their namespace (globs), their operation, and their fields' values.", func() any { return new(Filter) }},
		{"route", "Renames records' namespaces, by an exact map and then by glob rules with templates, as tables to indices or topics.", func() any { return new(Route) }},
		{"fields", "Changes the fields of the rows before and after: lifts nested maps' fields up, moves fields into a nested map, renames, drops, keeps only some, and sets constants, in that order. Renames apply to the key's fields too; nothing else touches the key, so a delete keeps it.", func() any { return new(Fields) }},
		{"key", "Builds records' keys from fields of the row: a map of them, a single value, or their text joined. A delete whose row lacks them keeps the key it has.", func() any { return new(Key) }},
		{"cast", "Changes fields' types, in the rows before and after and in the key, by defined conversions; a value that cannot be converted fails the record.", func() any { return new(Cast) }},
		{"mask", "Replaces fields' values in the rows before and after by a keyed HMAC-SHA256 or a SHA-256 of them, in hex, or by null. A hash applies to the key's fields too, so the key stays unique and matches the row.", func() any { return new(Mask) }},
		{"envelope", "Turns inserts, updates and deletes into one document in after, for sinks that write a document: the row with metadata fields, the before and after pair, or Debezium's envelope. The key stays as it is.", func() any { return new(Envelope) }},
	} {
		module.Register(module.Info{ID: Transforms + "." + t.name, New: t.new, Doc: t.doc})
	}
}

// remover is a transform that may leave a field out of the row, which a key
// transform after it must not build the key from.
type remover interface {
	removes(name string) bool
}

// checkChain refuses a key transform built from a field a transform before
// it removes: its records would have no key, and deletes would lose theirs.
func checkChain(refs []config.ModuleRef, ts []Transform) config.Errors {
	var errs config.Errors
	for i, t := range ts {
		k, ok := t.(*Key)
		if !ok {
			continue
		}
		for j := range i {
			r, ok := ts[j].(remover)
			if !ok {
				continue
			}
			for _, f := range k.Fields {
				if r.removes(f) {
					errs = append(errs, refs[i].Params.Field("fields").Errorf("the key's field %q is gone after transform %d (%s), so records, deletes too, would have no key; build the key before it", f, j, refs[j].Module))
				}
			}
		}
	}
	return errs
}

// rowOf is the row a record's fields are read from: after, or before when
// after is null, as for a delete.
func rowOf(r *Record) Value {
	if r.After.IsNull() {
		return r.Before
	}
	return r.After
}

// present returns the field named name of row, unless it is missing or null.
func present(row Value, name string) (Value, bool) {
	v, ok := row.Get(name)
	return v, ok && !v.IsNull()
}

// editFields returns m with edit applied to its fields named in names, and
// whether it has any of them: m itself when it has none, or is not a map.
func editFields(m Value, names map[string]bool, edit func(name string, v Value) (Value, error)) (Value, bool, error) {
	var fs []Field
	for i, f := range m.fields {
		if !names[f.Name] {
			continue
		}
		v, err := edit(f.Name, f.Value)
		if err != nil {
			return Value{}, false, fmt.Errorf("field %s: %w", f.Name, err)
		}
		if fs == nil {
			fs = slices.Clone(m.fields)
		}
		fs[i].Value = v
	}
	if fs == nil {
		return m, false, nil
	}
	return Map(fs...), true, nil
}

// globs checks each of patterns is a glob, as path.Match takes.
func globs(v config.Value, patterns []string) config.Errors {
	var errs config.Errors
	for i, p := range patterns {
		if _, err := path.Match(p, ""); err != nil || p == "" {
			errs = append(errs, v.Index(i).Errorf("%q is not a glob", p))
		}
	}
	return errs
}

func anyGlob(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, s); ok {
			return true
		}
	}
	return false
}

func errs(e config.Errors) error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// textOf returns v as text: a string, decimal, bytes or JSON as it is held,
// a number in decimal, a bool as true or false, a time in RFC 3339 with
// nanoseconds, a date as 2006-01-02, an array or a map as its canonical
// JSON, and null as "".
func textOf(v Value) string {
	switch v.kind {
	case KindDecimal, KindString, KindBytes, KindJSON:
		return v.str
	case KindBool:
		return strconv.FormatBool(v.AsBool())
	case KindInt:
		return strconv.FormatInt(v.AsInt(), 10)
	case KindUint:
		return strconv.FormatUint(v.num, 10)
	case KindFloat:
		return strconv.FormatFloat(v.AsFloat(), 'g', -1, 64)
	case KindTime:
		return v.tm.Format(time.RFC3339Nano)
	case KindDate:
		return v.tm.Format(time.DateOnly)
	case KindArray, KindMap:
		b, _ := v.AppendJSON(nil) // only a float's NaN fails, which is written as text
		return string(b)
	}
	return ""
}

// appendPlain appends v as plain JSON, as a JSON column holds it: numbers
// bare, decimals as numbers, bytes in base64, times and dates as strings,
// maps as objects.
func appendPlain(b []byte, v Value) ([]byte, error) {
	switch v.kind {
	case KindNull:
		return append(b, "null"...), nil
	case KindBool, KindInt, KindUint:
		return append(b, textOf(v)...), nil
	case KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v is not a JSON number", f)
		}
		return strconv.AppendFloat(b, f, 'g', -1, 64), nil
	case KindDecimal:
		return append(b, v.str...), nil
	case KindString:
		return appendString(b, v.str), nil
	case KindBytes:
		return appendString(b, base64.StdEncoding.EncodeToString([]byte(v.str))), nil
	case KindTime, KindDate:
		return appendString(b, textOf(v)), nil
	case KindJSON:
		return appendCompact(b, []byte(v.str))
	case KindArray:
		b = append(b, '[')
		for i, e := range v.elems {
			if i > 0 {
				b = append(b, ',')
			}
			var err error
			if b, err = appendPlain(b, e); err != nil {
				return nil, err
			}
		}
		return append(b, ']'), nil
	case KindMap:
		b = append(b, '{')
		for i, f := range v.fields {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(appendString(b, f.Name), ':')
			var err error
			if b, err = appendPlain(b, f.Value); err != nil {
				return nil, err
			}
		}
		return append(b, '}'), nil
	}
	return nil, fmt.Errorf("a value of kind %s", v.kind)
}
