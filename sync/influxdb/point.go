package syncinflux

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/tmpl"
)

// Point is a record as a time series point: a measurement (a super table
// for TDengine), tags, fields and a time.
type Point struct {
	Measurement string
	// Tags are sorted by key, none with an empty value.
	Tags   []Tag
	Fields []fitsync.Field
	Time   time.Time
}

// Tag is a point's tag.
type Tag struct{ Key, Value string }

// Mapping turns records into points, as a sink's params say.
type Mapping struct {
	measurement []tmpl.Part
	tags        []string
	fields      []string
	isTag       map[string]bool
	timeField   string
}

// NewMapping returns the mapping of a measurement template ({namespace}
// when empty), the tag fields, the fields (every other field of the row
// when none) and the field the time is read from ("" for the commit
// time). The errors name the params.
func NewMapping(measurement string, tags, fields []string, timeField string) (*Mapping, []error) {
	m := &Mapping{tags: tags, fields: fields, timeField: timeField, isTag: map[string]bool{}}
	var errs []error
	if measurement == "" {
		measurement = "{namespace}"
	}
	var err error
	if m.measurement, err = tmpl.Parse(measurement); err != nil {
		errs = append(errs, fmt.Errorf("measurement: %w", err))
	}
	for _, t := range tags {
		if t == "" || m.isTag[t] {
			errs = append(errs, fmt.Errorf("tags: %q is empty or named twice", t))
		}
		m.isTag[t] = true
	}
	for _, f := range fields {
		if f == "" || m.isTag[f] || f == timeField {
			errs = append(errs, fmt.Errorf("fields: %q is empty, or a tag, or the time_field", f))
		}
	}
	slices.Sort(m.tags)
	return m, errs
}

// TagNames returns the tag fields, sorted.
func (m *Mapping) TagNames() []string { return m.tags }

// RowOf returns the row a record's point is made of: after, or before when
// after is null.
func RowOf(r *fitsync.Record) fitsync.Value {
	if r.After.IsNull() {
		return r.Before
	}
	return r.After
}

// Point returns r's point; a record that cannot be one (no row, no field,
// a time that does not read) is an error, for good. now is the time of a
// record with neither a time_field nor a commit time.
func (m *Mapping) Point(r *fitsync.Record, now time.Time) (*Point, error) {
	row := RowOf(r)
	if row.Kind() != fitsync.KindMap {
		return nil, fmt.Errorf("a %s's row is a %s, not a map to make a point of", r.Op, row.Kind())
	}
	p := &Point{}
	var err error
	if p.Measurement, err = tmpl.Expand(m.measurement, r, row); err != nil {
		return nil, fmt.Errorf("measurement: %w", err)
	}
	if p.Measurement == "" {
		return nil, errors.New("the measurement is empty")
	}
	for _, t := range m.tags {
		v, ok := row.Get(t)
		if !ok || v.IsNull() {
			continue
		}
		if s := fitsync.TextOf(v); s != "" {
			p.Tags = append(p.Tags, Tag{t, s})
		}
	}
	if len(m.fields) > 0 {
		for _, f := range m.fields {
			if v, ok := row.Get(f); ok && !v.IsNull() {
				p.Fields = append(p.Fields, fitsync.Field{Name: f, Value: v})
			}
		}
	} else {
		for _, f := range row.Fields() {
			if !m.isTag[f.Name] && f.Name != m.timeField && !f.Value.IsNull() {
				p.Fields = append(p.Fields, f)
			}
		}
	}
	if len(p.Fields) == 0 {
		return nil, errors.New("the record has no field to write")
	}
	p.Time = r.CommitTime
	if m.timeField != "" {
		v, ok := row.Get(m.timeField)
		if !ok || v.IsNull() {
			return nil, fmt.Errorf("the record has no %s", m.timeField)
		}
		if p.Time, err = timeOf(v); err != nil {
			return nil, fmt.Errorf("%s: %w", m.timeField, err)
		}
	}
	if p.Time.IsZero() {
		p.Time = now
	}
	return p, nil
}

// timeOf reads a time_field's value: a time, a date, an int or uint of
// Unix nanoseconds, or a string in RFC 3339.
func timeOf(v fitsync.Value) (time.Time, error) {
	switch v.Kind() {
	case fitsync.KindTime, fitsync.KindDate:
		return v.AsTime(), nil
	case fitsync.KindInt:
		return time.Unix(0, v.AsInt()), nil
	case fitsync.KindUint:
		if v.AsUint() > math.MaxInt64 {
			return time.Time{}, fmt.Errorf("%d nanoseconds is past 2262", v.AsUint())
		}
		return time.Unix(0, int64(v.AsUint())), nil
	case fitsync.KindString:
		return time.Parse(time.RFC3339Nano, v.Text())
	}
	return time.Time{}, fmt.Errorf("a %s is not a time", v.Kind())
}

// Precision is a timestamp's unit.
type Precision string

// The precisions.
const (
	Nanoseconds  Precision = "ns"
	Microseconds Precision = "us"
	Milliseconds Precision = "ms"
	Seconds      Precision = "s"
)

// Stamp returns t in the precision's units since the Unix epoch.
func (p Precision) Stamp(t time.Time) int64 {
	switch p {
	case Microseconds:
		return t.UnixMicro()
	case Milliseconds:
		return t.UnixMilli()
	case Seconds:
		return t.Unix()
	}
	return t.UnixNano()
}

// Flavor is a dialect of the line protocol.
type Flavor int

// The flavors: InfluxDB 2 and 3 (uints with u), InfluxDB 1 (no u), and
// TDengine's schemaless writes (i64, u64 and f64).
const (
	Influx Flavor = iota
	Influx1
	TDengine
)

// AppendLine appends p as a line of the line protocol, with its time in
// precision, and a newline.
func AppendLine(b []byte, p *Point, prec Precision, fl Flavor) ([]byte, error) {
	if err := checkName(p.Measurement); err != nil {
		return nil, fmt.Errorf("the measurement %w", err)
	}
	b = appendEscaped(b, p.Measurement, ", ")
	for _, t := range p.Tags {
		if err := errors.Join(checkName(t.Key), checkName(t.Value)); err != nil {
			return nil, fmt.Errorf("the tag %s %w", t.Key, err)
		}
		b = append(b, ',')
		b = appendEscaped(b, t.Key, ",= ")
		b = append(b, '=')
		b = appendEscaped(b, t.Value, ",= ")
	}
	for i, f := range p.Fields {
		if err := checkName(f.Name); err != nil {
			return nil, fmt.Errorf("the field %w", err)
		}
		if i == 0 {
			b = append(b, ' ')
		} else {
			b = append(b, ',')
		}
		b = appendEscaped(b, f.Name, ",= ")
		b = append(b, '=')
		var err error
		if b, err = appendValue(b, f.Value, fl); err != nil {
			return nil, fmt.Errorf("the field %s: %w", f.Name, err)
		}
	}
	b = append(b, ' ')
	b = strconv.AppendInt(b, prec.Stamp(p.Time), 10)
	return append(b, '\n'), nil
}

// checkName refuses what the line protocol cannot hold in a name or a tag:
// a line break, or a trailing backslash, which would escape what follows.
func checkName(s string) error {
	switch {
	case s == "":
		return errors.New("is empty")
	case strings.ContainsAny(s, "\r\n"):
		return fmt.Errorf("%q holds a line break", s)
	case strings.HasSuffix(s, `\`):
		return fmt.Errorf("%q ends in a backslash", s)
	}
	return nil
}

func appendEscaped(b []byte, s, special string) []byte {
	for i := range len(s) {
		if strings.IndexByte(special, s[i]) >= 0 {
			b = append(b, '\\')
		}
		b = append(b, s[i])
	}
	return b
}

// appendValue appends a field's value: ints, uints, floats and bools as
// such, every other kind as a string of its text (a decimal as a float).
func appendValue(b []byte, v fitsync.Value, fl Flavor) ([]byte, error) {
	switch v.Kind() {
	case fitsync.KindBool:
		return strconv.AppendBool(b, v.AsBool()), nil
	case fitsync.KindInt:
		b = strconv.AppendInt(b, v.AsInt(), 10)
		if fl == TDengine {
			return append(b, "i64"...), nil
		}
		return append(b, 'i'), nil
	case fitsync.KindUint:
		switch fl {
		case Influx1:
			if v.AsUint() > math.MaxInt64 {
				return nil, fmt.Errorf("%d does not fit InfluxDB 1's integers", v.AsUint())
			}
			return append(strconv.AppendUint(b, v.AsUint(), 10), 'i'), nil
		case TDengine:
			return append(strconv.AppendUint(b, v.AsUint(), 10), "u64"...), nil
		}
		return append(strconv.AppendUint(b, v.AsUint(), 10), 'u'), nil
	case fitsync.KindFloat, fitsync.KindDecimal:
		f := v.AsFloat()
		if v.Kind() == fitsync.KindDecimal {
			var err error
			if f, err = strconv.ParseFloat(v.Text(), 64); err != nil {
				return nil, fmt.Errorf("the decimal %s: %w", v.Text(), err)
			}
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v is not a number the line protocol takes", f)
		}
		b = strconv.AppendFloat(b, f, 'g', -1, 64)
		if fl == TDengine {
			return append(b, "f64"...), nil
		}
		return b, nil
	}
	s := fitsync.TextOf(v)
	if k := v.Kind(); k == fitsync.KindBytes || k == fitsync.KindArray || k == fitsync.KindMap {
		var err error
		if s, err = plain(v); err != nil {
			return nil, err
		}
	}
	b = append(b, '"')
	for i := range len(s) {
		if s[i] == '"' || s[i] == '\\' {
			b = append(b, '\\')
		}
		b = append(b, s[i])
	}
	return append(b, '"'), nil
}

// plain returns v as plain JSON's text: bytes in base64.
func plain(v fitsync.Value) (string, error) {
	j, err := fitsync.AppendPlain(nil, v)
	if err != nil {
		return "", err
	}
	s, err := strconv.Unquote(string(j))
	if err != nil {
		return string(j), nil
	}
	return s, nil
}
