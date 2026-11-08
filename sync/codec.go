package fitsync

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lesismal/fit/module"
)

// The built-in codecs: line, json and csv, for the sources and sinks that
// read and write lines.
func init() {
	module.Register(module.Info{ID: Codecs + ".line", New: func() any { return new(LineCodec) },
		Doc: "A line is a message record whose after holds the line as a string, in the field field names (line). Encoded, a record is that field's text, or its canonical JSON when it has none."})
	module.Register(module.Info{ID: Codecs + ".json", New: func() any { return new(JSONCodec) },
		Doc: "A line is a JSON object: with form plain, a message record whose after holds its members, numbers as ints, or floats when they have a fraction or exponent; with form record, a record in its canonical JSON. Encoded, a record is the same, its after (or before, when after is null) as plain JSON or the whole record in its canonical form."})
	module.Register(module.Info{ID: Codecs + ".csv", New: func() any { return new(CSVCodec) },
		Doc: "A line is a row of comma-separated values, quoted as RFC 4180 has them: a message record whose after holds the values as strings, named by columns. Encoded, a record is its after's (or before's) fields, those columns names or all of them, as text."})
}

// TextOf returns v as text, as a line or a template writes it: strings,
// decimals, bytes and JSON as they are held, numbers in decimal, times in
// RFC 3339 with nanoseconds, dates as 2006-01-02, arrays and maps as their
// canonical JSON, and null as "".
func TextOf(v Value) string { return textOf(v) }

// LineCodec is the codec line.
type LineCodec struct {
	Field string `json:"field,omitempty" default:"\"line\"" doc:"The field of after that holds the line. line when left out."`
}

func (c *LineCodec) field() string {
	if c.Field == "" {
		return "line"
	}
	return c.Field
}

// Decode makes a message record of the line.
func (c *LineCodec) Decode(data []byte, emit func(*Record)) error {
	emit(&Record{Op: OpMessage, After: Map(Field{c.field(), String(string(data))})})
	return nil
}

// Encode appends each record's field, or its canonical JSON, and a newline.
func (c *LineCodec) Encode(b []byte, recs []*Record) ([]byte, error) {
	for _, r := range recs {
		if v, ok := rowOf(r).Get(c.field()); ok {
			b = append(b, textOf(v)...)
		} else {
			var err error
			if b, err = r.AppendJSON(b); err != nil {
				return b, err
			}
		}
		b = append(b, '\n')
	}
	return b, nil
}

// JSONCodec is the codec json.
type JSONCodec struct {
	Form string `json:"form,omitempty" enum:"plain,record" default:"\"plain\"" doc:"plain reads and writes a row as a plain JSON object; record reads and writes whole records in their canonical JSON, which keeps every type, the operation, key and namespace. plain when left out."`
}

// Decode parses a JSON object.
func (c *JSONCodec) Decode(data []byte, emit func(*Record)) error {
	if c.Form == "record" {
		r, err := ParseRecord(data)
		if err != nil {
			return err
		}
		r.Position, r.seq, r.made = nil, 0, false
		emit(r)
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := plainValue(d)
	if err != nil {
		return err
	}
	if v.kind != KindMap {
		return errors.New("the line is not a JSON object")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("the line holds more than one JSON value")
	}
	emit(&Record{Op: OpMessage, After: v})
	return nil
}

// ParsePlain reads one plain JSON value, as a search index's document, by the
// json codec's rules: objects keep their members' order, a number without a
// fraction or exponent is an int, or a uint past int64, any other a float, or
// a decimal past float64.
func ParsePlain(data []byte) (Value, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := plainValue(d)
	if err == nil {
		if _, end := d.Token(); end != io.EOF {
			err = errors.New("more than one JSON value")
		}
	}
	return v, err
}

// plainValue reads a plain JSON value, keeping objects' members in order.
func plainValue(d *json.Decoder) (Value, error) {
	tok, err := d.Token()
	if err != nil {
		return Value{}, err
	}
	switch t := tok.(type) {
	case nil:
		return Null(), nil
	case bool:
		return Bool(t), nil
	case string:
		return String(t), nil
	case json.Number:
		s := string(t)
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return Int(i), nil
			}
			if u, err := strconv.ParseUint(s, 10, 64); err == nil {
				return Uint(u), nil
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return Decimal(s), nil
		}
		return Float(f), nil
	case json.Delim:
		if t == '[' {
			var elems []Value
			for d.More() {
				e, err := plainValue(d)
				if err != nil {
					return Value{}, err
				}
				elems = append(elems, e)
			}
			_, err := d.Token()
			return Array(elems...), err
		}
		var fields []Field
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return Value{}, err
			}
			v, err := plainValue(d)
			if err != nil {
				return Value{}, err
			}
			fields = append(fields, Field{k.(string), v})
		}
		_, err := d.Token()
		return Value{kind: KindMap, fields: fields}, err
	}
	return Value{}, fmt.Errorf("unexpected %v", tok)
}

// Encode appends each record as a line of JSON.
func (c *JSONCodec) Encode(b []byte, recs []*Record) ([]byte, error) {
	for _, r := range recs {
		var err error
		if c.Form == "record" {
			b, err = r.AppendJSON(b)
		} else {
			b, err = appendPlain(b, rowOf(r))
		}
		if err != nil {
			return b, err
		}
		b = append(b, '\n')
	}
	return b, nil
}

// CSVCodec is the codec csv.
type CSVCodec struct {
	Columns []string `json:"columns,omitempty" doc:"The columns' names, in order. Read, a row's values are named by them, and those past them c<n>, counting from 1; written, a record's fields of these names are written, a missing one empty. Left out, they are c1, c2 and so on read, and every field of the row written."`
	Comma   string   `json:"comma,omitempty" default:"\",\"" doc:"The separator, one character other than a quote or a line break. A comma when left out."`

	comma rune
}

// Provision checks comma.
func (c *CSVCodec) Provision(ctx *module.Context) error {
	c.comma = ','
	if c.Comma != "" {
		r, n := utf8.DecodeRuneInString(c.Comma)
		if n != len(c.Comma) || r == '"' || r == '\r' || r == '\n' || r == utf8.RuneError {
			return ctx.Errorf("comma", "%q is not one character other than a quote or a line break", c.Comma)
		}
		c.comma = r
	}
	return nil
}

func (c *CSVCodec) sep() rune {
	if c.comma == 0 {
		return ','
	}
	return c.comma
}

// Decode reads one row.
func (c *CSVCodec) Decode(data []byte, emit func(*Record)) error {
	rd := csv.NewReader(bytes.NewReader(data))
	rd.Comma, rd.FieldsPerRecord = c.sep(), -1
	vals, err := rd.Read()
	if err != nil {
		return err
	}
	fields := make([]Field, len(vals))
	for i, v := range vals {
		name := "c" + strconv.Itoa(i+1)
		if i < len(c.Columns) {
			name = c.Columns[i]
		}
		fields[i] = Field{name, String(v)}
	}
	emit(&Record{Op: OpMessage, After: Map(fields...)})
	return nil
}

// Encode appends each record as a row.
func (c *CSVCodec) Encode(b []byte, recs []*Record) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Comma = c.sep()
	var vals []string
	for _, r := range recs {
		row := rowOf(r)
		vals = vals[:0]
		if len(c.Columns) > 0 {
			for _, col := range c.Columns {
				v, _ := row.Get(col)
				vals = append(vals, textOf(v))
			}
		} else {
			for _, f := range row.Fields() {
				vals = append(vals, textOf(f.Value))
			}
		}
		if err := w.Write(vals); err != nil {
			return b, err
		}
	}
	w.Flush()
	return append(b, buf.Bytes()...), w.Error()
}
