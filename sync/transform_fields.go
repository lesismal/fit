package fitsync

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// Fields is the transform fields.
type Fields struct {
	Unnest []string                `json:"unnest,omitempty" doc:"Maps whose fields are lifted up to the row's top level, the map itself removed: unnest meta turns {\"meta\": {\"a\": 1}} into {\"a\": 1}. A lifted field whose name the row already has fails the record; a null or missing map is left out."`
	Nest   []FieldsNest            `json:"nest,omitempty" doc:"Fields moved into a nested map, after unnest."`
	Rename map[string]string       `json:"rename,omitempty" doc:"Fields renamed, the old name to the new, after nest; a new name the row already has fails the record. The key's fields are renamed too, so the key names the sink's columns."`
	Drop   []string                `json:"drop,omitempty" doc:"Fields removed, after rename, by their new names. Not with keep."`
	Keep   []string                `json:"keep,omitempty" doc:"The only fields kept, after rename, by their new names; the others are removed. Not with drop."`
	Set    map[string]config.Value `json:"set,omitempty" doc:"Fields set to a constant, last, added at the end of the row when it has none of that name: a value in the canonical JSON form, \"eu\" for a string, {\"int\": 5} for an int, true, null."`

	set   []Field
	drops map[string]bool
}

// FieldsNest moves fields into a nested map.
type FieldsNest struct {
	Into   string   `json:"into" fit:"required" doc:"The nested map's name: made at the end of the row when the row has none, added to when it is a map."`
	Fields []string `json:"fields" fit:"required" doc:"The fields moved, those the row has, in the row's order."`

	At config.Value `json:"-" fit:"self"`
}

// Provision parses set, and checks the names.
func (f *Fields) Provision(ctx *module.Context) error {
	var e config.Errors
	if len(f.Drop) > 0 && len(f.Keep) > 0 {
		e = append(e, ctx.Params.Errorf("set drop or keep, not both"))
	}
	seen := map[string]string{}
	for from, to := range f.Rename {
		if to == "" {
			e = append(e, ctx.Params.Field("rename").Field(from).Errorf("an empty name"))
		} else if o, ok := seen[to]; ok {
			e = append(e, ctx.Params.Field("rename").Field(from).Errorf("%s and %s are both renamed to %s", min(o, from), max(o, from), to))
		}
		seen[to] = from
	}
	for _, n := range f.Nest {
		if n.Into == "" || slices.Contains(n.Fields, n.Into) {
			e = append(e, n.At.Field("into").Errorf("%q cannot be moved into itself, nor be empty", n.Into))
		}
	}
	names := make([]string, 0, len(f.Set))
	for name := range f.Set {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw, _ := f.Set[name].MarshalJSON()
		var v Value
		if err := v.UnmarshalJSON(raw); err != nil {
			e = append(e, f.Set[name].Errorf("not a value in the canonical JSON form: %v", err))
		}
		f.set = append(f.set, Field{name, v})
	}
	f.drops = map[string]bool{}
	for _, n := range f.Drop {
		f.drops[n] = true
	}
	if len(f.Unnest)+len(f.Nest)+len(f.Rename)+len(f.Drop)+len(f.Keep)+len(f.Set) == 0 {
		e = append(e, ctx.Params.Errorf("fields needs unnest, nest, rename, drop, keep or set"))
	}
	return errs(e)
}

// removes reports whether the row has no field name once f is done.
func (f *Fields) removes(name string) bool {
	if _, ok := f.Rename[name]; ok && !slices.Contains(mapValues(f.Rename), name) {
		return true
	}
	for _, n := range f.Nest {
		if slices.Contains(n.Fields, name) {
			return true
		}
	}
	_, set := f.Set[name]
	return !set && (f.drops[name] || len(f.Keep) > 0 && !slices.Contains(f.Keep, name) || slices.Contains(f.Unnest, name))
}

func mapValues(m map[string]string) []string {
	vs := make([]string, 0, len(m))
	for _, v := range m {
		vs = append(vs, v)
	}
	return vs
}

func indexField(fs []Field, name string) int {
	return slices.IndexFunc(fs, func(f Field) bool { return f.Name == name })
}

// row returns v changed, when it is a map.
func (f *Fields) row(v Value) (Value, error) {
	if v.kind != KindMap {
		return v, nil
	}
	fs := slices.Clone(v.fields)
	for _, name := range f.Unnest {
		i := indexField(fs, name)
		if i < 0 {
			continue
		}
		in := fs[i].Value
		if in.kind != KindMap && !in.IsNull() {
			return Value{}, fmt.Errorf("field %s is a %s, not a map to unnest", name, in.kind)
		}
		fs = slices.Delete(fs, i, i+1)
		for _, g := range in.fields {
			if indexField(fs, g.Name) >= 0 {
				return Value{}, fmt.Errorf("field %s of %s is in the row already", g.Name, name)
			}
		}
		fs = slices.Insert(fs, i, in.fields...)
	}
	for _, n := range f.Nest {
		var moved []Field
		fs = slices.DeleteFunc(fs, func(g Field) bool {
			if slices.Contains(n.Fields, g.Name) {
				moved = append(moved, g)
				return true
			}
			return false
		})
		if len(moved) == 0 {
			continue
		}
		i := indexField(fs, n.Into)
		switch {
		case i < 0:
			fs = append(fs, Field{n.Into, Map(moved...)})
		case fs[i].Value.kind == KindMap:
			for _, g := range moved {
				if indexField(fs[i].Value.fields, g.Name) >= 0 {
					return Value{}, fmt.Errorf("field %s is in %s already", g.Name, n.Into)
				}
			}
			fs[i].Value = Map(append(slices.Clone(fs[i].Value.fields), moved...)...)
		default:
			return Value{}, fmt.Errorf("field %s is a %s, not a map to nest into", n.Into, fs[i].Value.kind)
		}
	}
	if err := f.rename(fs); err != nil {
		return Value{}, err
	}
	if len(f.Drop) > 0 || len(f.Keep) > 0 {
		fs = slices.DeleteFunc(fs, func(g Field) bool {
			return f.drops[g.Name] || len(f.Keep) > 0 && !slices.Contains(f.Keep, g.Name)
		})
	}
	for _, s := range f.set {
		if i := indexField(fs, s.Name); i >= 0 {
			fs[i].Value = s.Value
		} else {
			fs = append(fs, s)
		}
	}
	return Map(fs...), nil
}

func (f *Fields) rename(fs []Field) error {
	if len(f.Rename) == 0 {
		return nil
	}
	for i := range fs {
		if to, ok := f.Rename[fs[i].Name]; ok {
			if j := indexField(fs, to); j >= 0 && j != i {
				if _, moving := f.Rename[to]; !moving {
					return fmt.Errorf("field %s is renamed to %s, which the row has already", fs[i].Name, to)
				}
			}
			fs[i].Name = to
		}
	}
	return nil
}

// Apply changes r's rows, and renames its key's fields.
func (f *Fields) Apply(r *Record, emit func(*Record)) error {
	before, err := f.row(r.Before)
	if err != nil {
		return fmt.Errorf("before: %w", err)
	}
	after, err := f.row(r.After)
	if err != nil {
		return fmt.Errorf("after: %w", err)
	}
	key := r.Key
	if key.kind == KindMap && len(f.Rename) > 0 && slices.ContainsFunc(key.fields, func(g Field) bool { _, ok := f.Rename[g.Name]; return ok }) {
		fs := slices.Clone(key.fields)
		if err := f.rename(fs); err != nil {
			return fmt.Errorf("key: %w", err)
		}
		key = Map(fs...)
	}
	o := r.Own()
	o.Before, o.After, o.Key = before, after, key
	emit(o)
	return nil
}

// Key is the transform key.
type Key struct {
	Fields    []string `json:"fields" fit:"required" doc:"The fields the key is built from, at the top level of the row: after, or before for a delete. A record whose row lacks one, or has it null, fails, except a delete, which keeps the key it has; a key transform after a transform that removes one of them is refused."`
	Format    string   `json:"format,omitempty" enum:"map,value,join" default:"\"map\"" doc:"map makes the key a map of the fields, in the order given; value makes it the one field's value, with a single field; join makes it a string of the fields' texts joined by separator, as a document ID. map when left out."`
	Separator string   `json:"separator,omitempty" default:"\":\"" doc:"What join puts between the fields' texts. : when left out."`
}

// Provision checks the fields and the format.
func (k *Key) Provision(ctx *module.Context) error {
	var e config.Errors
	if len(k.Fields) == 0 {
		e = append(e, ctx.Params.Field("fields").Errorf("no fields"))
	}
	for i, n := range k.Fields {
		if n == "" || slices.Index(k.Fields, n) < i {
			e = append(e, ctx.Params.Field("fields").Index(i).Errorf("%q is empty or named twice", n))
		}
	}
	if k.Format == "value" && len(k.Fields) != 1 {
		e = append(e, ctx.Params.Field("format").Errorf("value takes a single field, not %d", len(k.Fields)))
	}
	if k.Separator == "" {
		k.Separator = ":"
	}
	return errs(e)
}

// Apply sets r's key.
func (k *Key) Apply(r *Record, emit func(*Record)) error {
	if r.Op == OpDDL {
		emit(r)
		return nil
	}
	row := r.After
	if r.Op == OpDelete {
		row = r.Before
	}
	vals := make([]Field, len(k.Fields))
	for i, n := range k.Fields {
		v, ok := present(row, n)
		if !ok {
			if r.Op == OpDelete && !r.Key.IsNull() {
				emit(r)
				return nil
			}
			return fmt.Errorf("the key's field %s is missing or null", n)
		}
		vals[i] = Field{n, v}
	}
	var key Value
	switch k.Format {
	case "value":
		key = vals[0].Value
	case "join":
		texts := make([]string, len(vals))
		for i, f := range vals {
			texts[i] = textOf(f.Value)
		}
		key = String(strings.Join(texts, k.Separator))
	default:
		key = Map(vals...)
	}
	o := r.Own()
	o.Key = key
	emit(o)
	return nil
}
