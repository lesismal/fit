package fitsync

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// Filter is the transform filter.
type Filter struct {
	Namespaces        []string          `json:"namespaces,omitempty" doc:"Globs a record's namespace must match one of, as path.Match takes them: shop.* matches every table of shop. Any namespace when left out."`
	ExcludeNamespaces []string          `json:"exclude_namespaces,omitempty" doc:"Globs a record's namespace must match none of."`
	Ops               []string          `json:"ops,omitempty" enum:"insert,update,delete,ddl,message" doc:"The operations a record must be one of. Any operation when left out."`
	Where             []FilterCondition `json:"where,omitempty" doc:"Conditions on the row's fields, all of which a record must meet. The row is after, or before when after is null, as for a delete."`
	Action            string            `json:"action,omitempty" enum:"keep,drop" default:"\"keep\"" doc:"keep keeps the records that match every condition and drops the others; drop drops those that match and keeps the others. keep when left out."`

	ops uint8
}

// FilterCondition is a condition on a field of the row.
type FilterCondition struct {
	Field   string   `json:"field" fit:"required" doc:"The field's name, at the top level of the row."`
	Equals  *string  `json:"equals,omitempty" doc:"The text the field's value must have: a string as it is, a number in decimal (\"42\", \"1.5\"), a bool as \"true\" or \"false\", a time in RFC 3339, a date as 2006-01-02. A missing or null field never equals."`
	In      []string `json:"in,omitempty" doc:"Texts, as equals takes them, the field's value must have one of."`
	Present *bool    `json:"present,omitempty" doc:"true: the field must be there and not null; false: it must be missing or null."`

	At config.Value `json:"-" fit:"self"`
}

// Provision checks the globs and the conditions.
func (f *Filter) Provision(ctx *module.Context) error {
	e := globs(ctx.Params.Field("namespaces"), f.Namespaces)
	e = append(e, globs(ctx.Params.Field("exclude_namespaces"), f.ExcludeNamespaces)...)
	for _, o := range f.Ops {
		op, _ := parseOp(o)
		f.ops |= 1 << op
	}
	for _, c := range f.Where {
		n := 0
		for _, set := range []bool{c.Equals != nil, c.In != nil, c.Present != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			e = append(e, c.At.Errorf("a condition needs exactly one of equals, in and present"))
		}
	}
	if len(f.Namespaces)+len(f.ExcludeNamespaces)+len(f.Ops)+len(f.Where) == 0 {
		e = append(e, ctx.Params.Errorf("a filter needs namespaces, exclude_namespaces, ops or where"))
	}
	return errs(e)
}

func (f *Filter) matches(r *Record) bool {
	if f.ops != 0 && f.ops&(1<<r.Op) == 0 ||
		len(f.Namespaces) > 0 && !anyGlob(f.Namespaces, r.Namespace) ||
		anyGlob(f.ExcludeNamespaces, r.Namespace) {
		return false
	}
	row := rowOf(r)
	for _, c := range f.Where {
		v, ok := present(row, c.Field)
		switch {
		case c.Present != nil:
			if ok != *c.Present {
				return false
			}
		case !ok:
			return false
		case c.Equals != nil:
			if textOf(v) != *c.Equals {
				return false
			}
		default:
			if !slices.Contains(c.In, textOf(v)) {
				return false
			}
		}
	}
	return true
}

// Apply passes r on, or drops it.
func (f *Filter) Apply(r *Record, emit func(*Record)) error {
	if f.matches(r) == (f.Action != "drop") {
		emit(r)
	}
	return nil
}

// Route is the transform route.
type Route struct {
	Map       map[string]string `json:"map,omitempty" doc:"Namespaces renamed exactly, the old to the new: {\"shop.orders\": \"orders\"}. Tried before rules."`
	Rules     []RouteRule       `json:"rules,omitempty" doc:"Rules tried in order for a namespace map does not name; the first whose match it matches renames it."`
	Unmatched string            `json:"unmatched,omitempty" enum:"keep,drop" default:"\"keep\"" doc:"What becomes of a record whose namespace neither map nor a rule names: keep passes it on as it is, drop drops it. keep when left out."`

	tpls [][]tplPart
}

// RouteRule renames the namespaces a glob matches.
type RouteRule struct {
	Match string `json:"match" fit:"required" doc:"A glob the namespace must match, as path.Match takes it: shop.*."`
	To    string `json:"to" fit:"required" doc:"The new namespace, a template: {namespace} is the old one, {first} its part before the first '.', {rest} its part after the first '.', {last} its part after the last '.'. shop.orders to {first}-{rest} is shop-orders; to {last} it is orders."`

	At config.Value `json:"-" fit:"self"`
}

// tplPart is a piece of a template: text, or a placeholder when part > 0.
type tplPart struct {
	text string
	part int
}

var placeholders = []string{"", "{namespace}", "{first}", "{rest}", "{last}"}

func parseTemplate(s string) ([]tplPart, error) {
	var parts []tplPart
	for s != "" {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			parts = append(parts, tplPart{text: s})
			break
		}
		if i > 0 {
			parts = append(parts, tplPart{text: s[:i]})
		}
		j := strings.IndexByte(s[i:], '}')
		p := -1
		if j > 0 {
			p = slices.Index(placeholders, s[i:i+j+1])
		}
		if p <= 0 {
			return nil, fmt.Errorf("%q has a '{' that starts none of {namespace}, {first}, {rest} and {last}", s)
		}
		parts = append(parts, tplPart{part: p})
		s = s[i+j+1:]
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("an empty template")
	}
	return parts, nil
}

func render(parts []tplPart, ns string) string {
	if len(parts) == 1 && parts[0].part == 1 {
		return ns
	}
	first, rest := ns, ""
	if i := strings.IndexByte(ns, '.'); i >= 0 {
		first, rest = ns[:i], ns[i+1:]
	}
	last := ns[strings.LastIndexByte(ns, '.')+1:]
	var b strings.Builder
	for _, p := range parts {
		switch p.part {
		case 0:
			b.WriteString(p.text)
		case 1:
			b.WriteString(ns)
		case 2:
			b.WriteString(first)
		case 3:
			b.WriteString(rest)
		default:
			b.WriteString(last)
		}
	}
	return b.String()
}

// Provision checks the rules.
func (rt *Route) Provision(ctx *module.Context) error {
	var e config.Errors
	for name, to := range rt.Map {
		if to == "" {
			e = append(e, ctx.Params.Field("map").Field(name).Errorf("an empty namespace"))
		}
	}
	for i, r := range rt.Rules {
		e = append(e, globs(r.At.Field("match"), []string{r.Match})...)
		t, err := parseTemplate(r.To)
		if err != nil {
			e = append(e, ctx.Params.Field("rules").Index(i).Field("to").Errorf("%v", err))
		}
		rt.tpls = append(rt.tpls, t)
	}
	if len(rt.Map)+len(rt.Rules) == 0 {
		e = append(e, ctx.Params.Errorf("a route needs map or rules"))
	}
	return errs(e)
}

// Apply renames r's namespace.
func (rt *Route) Apply(r *Record, emit func(*Record)) error {
	ns, ok := rt.Map[r.Namespace]
	for i := 0; !ok && i < len(rt.Rules); i++ {
		if m, _ := path.Match(rt.Rules[i].Match, r.Namespace); m {
			ns, ok = render(rt.tpls[i], r.Namespace), true
			if ns == "" {
				return fmt.Errorf("rule %d makes %q an empty namespace", i, r.Namespace)
			}
		}
	}
	switch {
	case !ok && rt.Unmatched == "drop":
	case !ok || ns == r.Namespace:
		emit(r)
	default:
		o := r.Own()
		o.Namespace = ns
		emit(o)
	}
	return nil
}
