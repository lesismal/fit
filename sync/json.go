package fitsync

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"time"
)

// The canonical JSON form of a Value says its kind wherever JSON alone would
// not, so that every kind comes back as it went: null, true and false,
// strings and arrays are JSON's own; every other kind is an object of one
// member named after the kind:
//
//	{"int": -5}  {"uint": 5}  {"float": 1.5}  {"float": "NaN"}
//	{"decimal": "12.30"}  {"bytes": "<base64>"}
//	{"time": "2026-11-08T10:00:00.5+08:00"}  {"date": "2026-11-08"}
//	{"json": <the document, compacted>}  {"map": {"name": <value>, ...}}
//
// A record is an object of its parts, those empty left out:
//
//	{"source": "mysql", "namespace": "shop.orders", "op": "update",
//	 "key": ..., "before": ..., "after": ..., "commit_time": "...",
//	 "tx": {"id": "...", "first": true, "last": true}, "position": <JSON>}
//
// Transforms, WASM plugins and dead letters see records in this form.

const dateLayout = "2006-01-02"

// AppendJSON appends v's canonical JSON to b.
func (v Value) AppendJSON(b []byte) ([]byte, error) {
	switch v.kind {
	case KindNull:
		return append(b, "null"...), nil
	case KindBool:
		return strconv.AppendBool(b, v.num != 0), nil
	case KindString:
		return appendString(b, v.str), nil
	case KindInt:
		return append(strconv.AppendInt(append(b, `{"int":`...), int64(v.num), 10), '}'), nil
	case KindUint:
		return append(strconv.AppendUint(append(b, `{"uint":`...), v.num, 10), '}'), nil
	case KindFloat:
		b = append(b, `{"float":`...)
		switch f := math.Float64frombits(v.num); {
		case math.IsNaN(f):
			b = append(b, `"NaN"`...)
		case math.IsInf(f, 1):
			b = append(b, `"+Inf"`...)
		case math.IsInf(f, -1):
			b = append(b, `"-Inf"`...)
		default:
			b = strconv.AppendFloat(b, f, 'g', -1, 64)
		}
		return append(b, '}'), nil
	case KindDecimal:
		return append(appendString(append(b, `{"decimal":`...), v.str), '}'), nil
	case KindBytes:
		b = append(b, `{"bytes":"`...)
		b = base64.StdEncoding.AppendEncode(b, []byte(v.str))
		return append(b, `"}`...), nil
	case KindTime:
		b = append(b, `{"time":"`...)
		b = v.tm.AppendFormat(b, time.RFC3339Nano)
		return append(b, `"}`...), nil
	case KindDate:
		b = append(b, `{"date":"`...)
		b = v.tm.AppendFormat(b, dateLayout)
		return append(b, `"}`...), nil
	case KindJSON:
		b = append(b, `{"json":`...)
		b, err := appendCompact(b, []byte(v.str))
		if err != nil {
			return b, fmt.Errorf("a json value: %w", err)
		}
		return append(b, '}'), nil
	case KindArray:
		b = append(b, '[')
		for i, e := range v.elems {
			if i > 0 {
				b = append(b, ',')
			}
			var err error
			if b, err = e.AppendJSON(b); err != nil {
				return b, err
			}
		}
		return append(b, ']'), nil
	case KindMap:
		b = append(b, `{"map":{`...)
		for i, f := range v.fields {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(appendString(b, f.Name), ':')
			var err error
			if b, err = f.Value.AppendJSON(b); err != nil {
				return b, err
			}
		}
		return append(b, "}}"...), nil
	}
	return b, fmt.Errorf("a value of kind %d", v.kind)
}

// MarshalJSON returns v's canonical JSON.
func (v Value) MarshalJSON() ([]byte, error) { return v.AppendJSON(nil) }

// UnmarshalJSON reads v from its canonical JSON.
func (v *Value) UnmarshalJSON(data []byte) error {
	d := newDecoder(data)
	w, err := d.value()
	if err != nil {
		return err
	}
	if err := d.end(); err != nil {
		return err
	}
	*v = w
	return nil
}

// AppendJSON appends r's canonical JSON to b.
func (r *Record) AppendJSON(b []byte) ([]byte, error) {
	b = append(b, '{')
	sep := func() {
		if b[len(b)-1] != '{' {
			b = append(b, ',')
		}
	}
	str := func(name, s string) {
		if s != "" {
			sep()
			b = appendString(append(appendString(b, name), ':'), s)
		}
	}
	str("source", r.Source)
	str("namespace", r.Namespace)
	if r.Op != 0 {
		str("op", r.Op.String())
	}
	for _, f := range []struct {
		name string
		v    Value
	}{{"key", r.Key}, {"before", r.Before}, {"after", r.After}} {
		if f.v.IsNull() {
			continue
		}
		sep()
		b = append(appendString(b, f.name), ':')
		var err error
		if b, err = f.v.AppendJSON(b); err != nil {
			return b, fmt.Errorf("%s: %w", f.name, err)
		}
	}
	if !r.CommitTime.IsZero() {
		str("commit_time", r.CommitTime.Format(time.RFC3339Nano))
	}
	if r.Tx != (Tx{}) {
		sep()
		b = append(b, `"tx":{`...)
		if r.Tx.ID != "" {
			b = appendString(append(b, `"id":`...), r.Tx.ID)
		}
		if r.Tx.First {
			sep()
			b = append(b, `"first":true`...)
		}
		if r.Tx.Last {
			sep()
			b = append(b, `"last":true`...)
		}
		b = append(b, '}')
	}
	if len(r.Position) > 0 {
		sep()
		b = append(b, `"position":`...)
		var err error
		if b, err = appendCompact(b, r.Position); err != nil {
			return b, fmt.Errorf("position: %w", err)
		}
	}
	return append(b, '}'), nil
}

// MarshalJSON returns r's canonical JSON.
func (r *Record) MarshalJSON() ([]byte, error) { return r.AppendJSON(nil) }

// UnmarshalJSON reads r from its canonical JSON.
func (r *Record) UnmarshalJSON(data []byte) error {
	d := newDecoder(data)
	if err := d.delim('{'); err != nil {
		return err
	}
	var out Record
	for d.dec.More() {
		name, err := d.key()
		if err != nil {
			return err
		}
		switch name {
		case "source", "namespace", "op", "commit_time":
			s, err := d.str()
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			switch name {
			case "source":
				out.Source = s
			case "namespace":
				out.Namespace = s
			case "op":
				var ok bool
				if out.Op, ok = parseOp(s); !ok {
					return fmt.Errorf("op: %q is not an operation", s)
				}
			case "commit_time":
				if out.CommitTime, err = time.Parse(time.RFC3339Nano, s); err != nil {
					return fmt.Errorf("commit_time: %w", err)
				}
			}
		case "key", "before", "after":
			v, err := d.value()
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			switch name {
			case "key":
				out.Key = v
			case "before":
				out.Before = v
			default:
				out.After = v
			}
		case "tx":
			var tx struct {
				ID          string `json:"id"`
				First, Last bool
			}
			if err := d.dec.Decode(&tx); err != nil {
				return fmt.Errorf("tx: %w", err)
			}
			out.Tx = Tx{ID: tx.ID, First: tx.First, Last: tx.Last}
		case "position":
			var raw json.RawMessage
			if err := d.dec.Decode(&raw); err != nil {
				return fmt.Errorf("position: %w", err)
			}
			out.Position = Position(raw)
		default:
			return fmt.Errorf("a record has no %q", name)
		}
	}
	if err := d.delim('}'); err != nil {
		return err
	}
	if err := d.end(); err != nil {
		return err
	}
	*r = out
	return nil
}

func appendString(b []byte, s string) []byte {
	q, _ := json.Marshal(s) // a string always marshals
	return append(b, q...)
}

func appendCompact(b, doc []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, doc); err != nil {
		return b, err
	}
	return append(b, buf.Bytes()...), nil
}

// decoder reads canonical JSON token by token, which keeps a map's order
// and a number's digits.
type decoder struct{ dec *json.Decoder }

func newDecoder(data []byte) *decoder {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return &decoder{dec: d}
}

func (d *decoder) end() error {
	if _, err := d.dec.Token(); err != io.EOF {
		return errors.New("more after the value")
	}
	return nil
}

func (d *decoder) delim(want json.Delim) error {
	t, err := d.dec.Token()
	if err != nil {
		return err
	}
	if t != want {
		return fmt.Errorf("%v where %v was expected", t, want)
	}
	return nil
}

func (d *decoder) key() (string, error) {
	t, err := d.dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := t.(string)
	if !ok {
		return "", fmt.Errorf("%v where a name was expected", t)
	}
	return s, nil
}

func (d *decoder) str() (string, error) {
	t, err := d.dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := t.(string)
	if !ok {
		return "", fmt.Errorf("%v is not a string", t)
	}
	return s, nil
}

func (d *decoder) value() (Value, error) {
	t, err := d.dec.Token()
	if err != nil {
		return Value{}, err
	}
	switch t := t.(type) {
	case nil:
		return Null(), nil
	case bool:
		return Bool(t), nil
	case string:
		return String(t), nil
	case json.Number:
		return Value{}, fmt.Errorf("the number %s does not say its kind: write {\"int\": %s}, {\"uint\": %s} or {\"float\": %s}", t, t, t, t)
	case json.Delim:
		if t == '[' {
			elems := []Value{}
			for d.dec.More() {
				e, err := d.value()
				if err != nil {
					return Value{}, err
				}
				elems = append(elems, e)
			}
			return Array(elems...), d.delim(']')
		}
		if t != '{' {
			return Value{}, fmt.Errorf("%v where a value was expected", t)
		}
	}
	kind, err := d.key()
	if err != nil {
		return Value{}, err
	}
	v, err := d.tagged(kind)
	if err != nil {
		return Value{}, fmt.Errorf("%s: %w", kind, err)
	}
	if d.dec.More() {
		return Value{}, fmt.Errorf("a %s value has more than one member", kind)
	}
	return v, d.delim('}')
}

func (d *decoder) tagged(kind string) (Value, error) {
	if kind == "json" {
		var raw json.RawMessage
		if err := d.dec.Decode(&raw); err != nil {
			return Value{}, err
		}
		return JSON(raw), nil
	}
	if kind == "map" {
		if err := d.delim('{'); err != nil {
			return Value{}, err
		}
		fields := []Field{}
		for d.dec.More() {
			name, err := d.key()
			if err != nil {
				return Value{}, err
			}
			for _, f := range fields {
				if f.Name == name {
					return Value{}, fmt.Errorf("%q is there twice", name)
				}
			}
			v, err := d.value()
			if err != nil {
				return Value{}, fmt.Errorf("%s: %w", name, err)
			}
			fields = append(fields, Field{Name: name, Value: v})
		}
		return Map(fields...), d.delim('}')
	}
	t, err := d.dec.Token()
	if err != nil {
		return Value{}, err
	}
	n, isNum := t.(json.Number)
	s, isStr := t.(string)
	switch {
	case kind == "int" && isNum:
		i, err := strconv.ParseInt(string(n), 10, 64)
		return Int(i), err
	case kind == "uint" && isNum:
		u, err := strconv.ParseUint(string(n), 10, 64)
		return Uint(u), err
	case kind == "float" && isNum:
		f, err := strconv.ParseFloat(string(n), 64)
		return Float(f), err
	case kind == "float" && isStr:
		switch s {
		case "NaN":
			return Float(math.NaN()), nil
		case "+Inf":
			return Float(math.Inf(1)), nil
		case "-Inf":
			return Float(math.Inf(-1)), nil
		}
	case kind == "decimal" && isStr:
		return Decimal(s), nil
	case kind == "bytes" && isStr:
		b, err := base64.StdEncoding.DecodeString(s)
		return Bytes(b), err
	case kind == "time" && isStr:
		tm, err := time.Parse(time.RFC3339Nano, s)
		return Time(tm), err
	case kind == "date" && isStr:
		tm, err := time.Parse(dateLayout, s)
		return Date(tm.Date()), err
	case !slices.Contains(kindNames[:], kind):
		return Value{}, fmt.Errorf("there is no kind %q", kind)
	}
	return Value{}, fmt.Errorf("%v is not a %s", t, kind)
}
