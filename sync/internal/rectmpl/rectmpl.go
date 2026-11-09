// Package rectmpl is the text templates the log sinks (loki, otlp_logs)
// make labels and attributes with, as the file sink's template: {name} is
// a field of the record's row (after, or before when after is null) as
// text, and {$namespace}, {$op}, {$source}, {$key} and {$commit_time} are
// the record's; {{ is a brace. It also reads a time from a field.
package rectmpl

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

// Template is a parsed template.
type Template struct {
	parts []part
}

type part struct {
	text  string
	field string // a row's field, or $ and the record's part
}

// The record's parts a template may name.
var recordParts = map[string]bool{"$namespace": true, "$op": true, "$source": true, "$key": true, "$commit_time": true}

// Parse parses t.
func Parse(t string) (*Template, error) {
	var parts []part
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
			if name == "" || strings.HasPrefix(name, "$") && !recordParts[name] {
				return nil, fmt.Errorf("{%s} names nothing: a field, or one of $namespace, $op, $source, $key and $commit_time", name)
			}
			if text.Len() > 0 {
				parts = append(parts, part{text: text.String()})
				text.Reset()
			}
			parts = append(parts, part{field: name})
			i += j
		default:
			text.WriteByte(t[i])
		}
	}
	if text.Len() > 0 {
		parts = append(parts, part{text: text.String()})
	}
	return &Template{parts: parts}, nil
}

// Row returns the row a record's fields are read from: after, or before
// when after is null.
func Row(r *fitsync.Record) fitsync.Value {
	if r.After.IsNull() {
		return r.Before
	}
	return r.After
}

// Render returns the template made with r.
func (t *Template) Render(r *fitsync.Record) string {
	if len(t.parts) == 1 && t.parts[0].field == "" {
		return t.parts[0].text
	}
	var b strings.Builder
	for _, p := range t.parts {
		switch p.field {
		case "":
			b.WriteString(p.text)
		case "$namespace":
			b.WriteString(r.Namespace)
		case "$op":
			b.WriteString(r.Op.String())
		case "$source":
			b.WriteString(r.Source)
		case "$key":
			b.WriteString(fitsync.TextOf(r.Key))
		case "$commit_time":
			if !r.CommitTime.IsZero() {
				b.WriteString(r.CommitTime.Format(time.RFC3339Nano))
			}
		default:
			v, _ := Row(r).Get(p.field)
			b.WriteString(fitsync.TextOf(v))
		}
	}
	return b.String()
}

// TimeOf reads a time from a field's value: a time or a date; an integer,
// as Unix seconds, milliseconds, microseconds or nanoseconds by its size;
// or text in RFC 3339 (or a number, as an integer is). false when it holds
// none.
func TimeOf(v fitsync.Value) (time.Time, bool) {
	switch v.Kind() {
	case fitsync.KindTime, fitsync.KindDate:
		return v.AsTime(), true
	case fitsync.KindInt:
		return unix(v.AsInt()), v.AsInt() > 0
	case fitsync.KindUint:
		return unix(int64(min(v.AsUint(), 1<<62))), v.AsUint() > 0
	case fitsync.KindFloat:
		f := v.AsFloat()
		return unix(int64(f)), f > 0 && f < 1<<62
	case fitsync.KindString, fitsync.KindDecimal:
		s := strings.TrimSpace(v.Text())
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t, true
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			return unix(n), true
		}
		// Seconds with a fraction, before 2255.
		if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 && f < 9e9 {
			return time.Unix(0, int64(f*1e9)), true
		}
	}
	return time.Time{}, false
}

// unix reads n as Unix seconds, milliseconds, microseconds or nanoseconds,
// whichever puts it after 1973 and before 5138.
func unix(n int64) time.Time {
	switch {
	case n < 1e11:
		return time.Unix(n, 0)
	case n < 1e14:
		return time.UnixMilli(n)
	case n < 1e17:
		return time.UnixMicro(n)
	}
	return time.Unix(0, n)
}
