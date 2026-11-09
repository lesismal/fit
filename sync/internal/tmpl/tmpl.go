// Package tmpl is the templates of sync's sinks' names (a measurement, a
// collection, a subtable): text, {namespace} the record's namespace, and
// {name} the row's field name, {{ being a brace.
package tmpl

import (
	"errors"
	"fmt"
	"strings"

	fitsync "github.com/lesismal/fit/sync"
)

// Part is a piece of a template: text, the namespace, or a field's value.
type Part struct {
	text, field string
	namespace   bool
}

// Parse reads a template of text, {namespace} and {field}s, {{
// being a brace.
func Parse(t string) ([]Part, error) {
	var parts []Part
	var text strings.Builder
	for i := 0; i < len(t); i++ {
		switch {
		case strings.HasPrefix(t[i:], "{{"):
			text.WriteByte('{')
			i++
		case t[i] == '{':
			j := strings.IndexByte(t[i:], '}')
			if j < 0 {
				return nil, fmt.Errorf("a { at %d is not closed", i)
			}
			name := t[i+1 : i+j]
			if name == "" {
				return nil, errors.New("{} names nothing")
			}
			if text.Len() > 0 {
				parts = append(parts, Part{text: text.String()})
				text.Reset()
			}
			parts = append(parts, Part{namespace: name == "namespace", field: name})
			i += j
		default:
			text.WriteByte(t[i])
		}
	}
	if text.Len() > 0 {
		parts = append(parts, Part{text: text.String()})
	}
	return parts, nil
}

// Static returns a template's text when it has no placeholder.
func Static(parts []Part) (string, bool) {
	var b strings.Builder
	for _, p := range parts {
		if p.namespace || p.field != "" {
			return "", false
		}
		b.WriteString(p.text)
	}
	return b.String(), true
}

// Expand returns the template's text for r, whose row is row.
func Expand(parts []Part, r *fitsync.Record, row fitsync.Value) (string, error) {
	var b strings.Builder
	for _, p := range parts {
		switch {
		case p.namespace:
			b.WriteString(r.Namespace)
		case p.field != "":
			v, ok := row.Get(p.field)
			if !ok || v.IsNull() {
				return "", fmt.Errorf("the record has no %s", p.field)
			}
			b.WriteString(fitsync.TextOf(v))
		default:
			b.WriteString(p.text)
		}
	}
	return b.String(), nil
}
