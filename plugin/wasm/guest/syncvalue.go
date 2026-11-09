//go:build wasip1

package guest

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"time"
	"unsafe"
)

// Value is a record's value, a field's or a key's, in the form its record
// came in: the binary form, whose first byte is a tag from 0x10 to 0x1F,
// or canonical JSON (see docs/decisions/036-sync.md). The value helpers
// read either and make values in the form the plugin took, and a Value
// written as canonical JSON by hand may go in either: fit reads it. A
// Value is not changed once made; empty is none, as null is.
type Value []byte

// The binary form's tags: 0x10 plus the kind, and a value in canonical
// JSON.
const (
	tagNull byte = 0x10 + iota
	tagBool
	tagInt
	tagUint
	tagFloat
	tagDecimal
	tagString
	tagBytes
	tagTime
	tagDate
	tagJSON
	tagArray
	tagMap
	tagCanonical byte = 0x1F
)

var errNotMap = errors.New("not a map value")

// binaryValue reports whether v is in the binary form.
func binaryValue(v []byte) bool { return len(v) > 0 && v[0] >= tagNull && v[0] <= tagCanonical }

// IsNull reports whether v is null, or none.
func (v Value) IsNull() bool {
	if binaryValue(v) {
		return v[0] == tagNull
	}
	t := trimSpace(v)
	return len(t) == 0 || string(t) == "null"
}

// canonical returns v as JSON: v itself, or what a binary value's tag
// 0x1F holds, and whether it is JSON.
func (v Value) canonical() ([]byte, bool) {
	if !binaryValue(v) {
		return v, true
	}
	if v[0] == tagCanonical {
		r := reader{b: v, i: 1}
		doc := r.bytes()
		return doc, r.err == nil
	}
	return nil, false
}

// AppendJSON appends v's canonical JSON to b; null for none, or a binary
// value cut short.
func (v Value) AppendJSON(b []byte) []byte {
	if doc, ok := v.canonical(); ok {
		if len(trimSpace(doc)) == 0 {
			return append(b, "null"...)
		}
		return append(b, doc...)
	}
	r := reader{b: v}
	out := r.appendJSON(b, 0)
	if r.err != nil {
		return append(b, "null"...)
	}
	return out
}

// MarshalJSON returns v's canonical JSON.
func (v Value) MarshalJSON() ([]byte, error) { return v.AppendJSON(nil), nil }

// UnmarshalJSON keeps the JSON text data as v.
func (v *Value) UnmarshalJSON(data []byte) error {
	*v = append((*v)[:0], data...)
	return nil
}

// appendBinary appends v to b in the binary form: as it is, or, for JSON,
// under the tag 0x1F.
func (v Value) appendBinary(b []byte) []byte {
	if binaryValue(v) {
		return append(b, v...)
	}
	if v.IsNull() {
		return append(b, tagNull)
	}
	return appendBytes(append(b, tagCanonical), trimSpace(v))
}

func trimSpace(b []byte) []byte {
	i := skipSpace(b, 0)
	j := len(b)
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\n' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}

func appendBytes(b, s []byte) []byte {
	return append(binary.AppendUvarint(b, uint64(len(s))), s...)
}

func appendStr(b []byte, s string) []byte {
	return append(binary.AppendUvarint(b, uint64(len(s))), s...)
}

// reader reads the binary form; the first error sticks, and reads after it
// give zero values.
type reader struct {
	b   []byte
	i   int
	err error
}

func (r *reader) fail() {
	if r.err == nil {
		r.err = errSyntax
	}
	r.i = len(r.b)
}

func (r *reader) readByte() byte {
	if r.i >= len(r.b) {
		r.fail()
		return 0
	}
	c := r.b[r.i]
	r.i++
	return c
}

// uvarint reads a uvarint; one of a byte, a string's length most often,
// without a call.
func (r *reader) uvarint() uint64 {
	if r.i < len(r.b) && r.b[r.i] < 0x80 {
		r.i++
		return uint64(r.b[r.i-1])
	}
	return r.uvarintLong()
}

func (r *reader) uvarintLong() uint64 {
	if r.i > len(r.b) {
		r.fail()
		return 0
	}
	u, n := binary.Uvarint(r.b[r.i:])
	if n <= 0 {
		r.fail()
		return 0
	}
	r.i += n
	return u
}

func (r *reader) varint() int64 {
	u := r.uvarint()
	return int64(u>>1) ^ -int64(u&1)
}

func (r *reader) bytes() []byte {
	n := r.uvarint()
	if n > uint64(len(r.b)-r.i) {
		r.fail()
		return nil
	}
	s := r.b[r.i : r.i+int(n) : r.i+int(n)]
	r.i += int(n)
	return s
}

// str returns a string sharing the reader's bytes, which a batch's input
// never changes.
func (r *reader) str() string {
	b := r.bytes()
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// skip passes the value at the reader and returns it.
func (r *reader) skip() Value {
	start := r.i
	r.skipValue()
	return Value(r.b[start:r.i:r.i])
}

func (r *reader) skipValue() {
	switch r.readByte() {
	case tagNull:
	case tagBool:
		r.readByte()
	case tagInt, tagUint:
		r.uvarint()
	case tagFloat:
		if r.i += 8; r.i > len(r.b) {
			r.fail()
		}
	case tagDecimal, tagString, tagBytes, tagJSON, tagCanonical:
		r.bytes()
	case tagTime:
		r.uvarint()
		r.uvarint()
		r.uvarint()
	case tagDate:
		r.uvarint()
		r.readByte()
		r.readByte()
	case tagArray, tagMap:
		r.size()
	default:
		r.fail()
	}
}

// size passes over an array or a map, after its tag.
func (r *reader) size() {
	end := r.into()
	r.i = max(r.i, end)
}

// into reads an array's or a map's length, after its tag, and returns
// where it ends.
func (r *reader) into() int {
	if r.i+4 > len(r.b) {
		r.fail()
		return r.i
	}
	n := int(binary.LittleEndian.Uint32(r.b[r.i:]))
	r.i += 4
	if n > len(r.b)-r.i {
		r.fail()
		return r.i
	}
	return r.i + n
}

// appendJSON appends the binary value at the reader as canonical JSON.
func (r *reader) appendJSON(b []byte, depth int) []byte {
	if depth > 1000 {
		r.fail()
		return b
	}
	switch tag := r.readByte(); tag {
	case tagNull:
		return append(b, "null"...)
	case tagBool:
		return strconv.AppendBool(b, r.readByte() == 1)
	case tagInt:
		return append(strconv.AppendInt(append(b, `{"int":`...), r.varint(), 10), '}')
	case tagUint:
		return append(strconv.AppendUint(append(b, `{"uint":`...), r.uvarint(), 10), '}')
	case tagFloat:
		if r.i+8 > len(r.b) {
			r.fail()
			return b
		}
		f := math.Float64frombits(binary.LittleEndian.Uint64(r.b[r.i:]))
		r.i += 8
		b = append(b, `{"float":`...)
		switch {
		case math.IsNaN(f):
			b = append(b, `"NaN"`...)
		case math.IsInf(f, 1):
			b = append(b, `"+Inf"`...)
		case math.IsInf(f, -1):
			b = append(b, `"-Inf"`...)
		default:
			b = strconv.AppendFloat(b, f, 'g', -1, 64)
		}
		return append(b, '}')
	case tagDecimal:
		return append(appendQuote(append(b, `{"decimal":`...), r.str()), '}')
	case tagString:
		return appendQuote(b, r.str())
	case tagBytes:
		b = append(b, `{"bytes":"`...)
		b = base64.StdEncoding.AppendEncode(b, r.bytes())
		return append(b, `"}`...)
	case tagJSON:
		return append(append(append(b, `{"json":`...), r.bytes()...), '}')
	case tagCanonical:
		return append(b, r.bytes()...)
	case tagTime:
		sec, nsec, off := r.varint(), r.uvarint(), r.varint()
		t := time.Unix(sec, int64(nsec)).In(time.FixedZone("", int(off)))
		if off == 0 {
			t = t.UTC()
		}
		b = append(b, `{"time":"`...)
		b = t.AppendFormat(b, time.RFC3339Nano)
		return append(b, `"}`...)
	case tagDate:
		y := r.varint()
		m, d := r.readByte(), r.readByte()
		b = append(b, `{"date":"`...)
		b = time.Date(int(y), time.Month(m), int(d), 0, 0, 0, 0, time.UTC).AppendFormat(b, "2006-01-02")
		return append(b, `"}`...)
	case tagArray:
		r.into()
		b = append(b, '[')
		for i, n := 0, r.uvarint(); uint64(i) < n && r.err == nil; i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = r.appendJSON(b, depth+1)
		}
		return append(b, ']')
	case tagMap:
		r.into()
		b = append(b, `{"map":{`...)
		for i, n := 0, r.uvarint(); uint64(i) < n && r.err == nil; i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(appendQuote(b, r.str()), ':')
			b = r.appendJSON(b, depth+1)
		}
		return append(b, "}}"...)
	}
	r.fail()
	return b
}

var opNames = [...]string{"", "insert", "update", "delete", "ddl", "message"}

func opByte(op string) byte {
	for i, n := range opNames {
		if n == op {
			return byte(i)
		}
	}
	return 0xFF // fit refuses it
}

// Flags of a record in the binary form.
const (
	txFirst = 1
	txLast  = 2
)

// parseBatchBinary reads a batch in the binary form: its strings and
// values share b.
func parseBatchBinary(b []byte) ([]Record, error) {
	r := reader{b: b}
	n := r.uvarint()
	if n > uint64(len(b)/10) {
		return nil, errSyntax
	}
	recs := records(int(n))
	for i := range recs {
		rec := &recs[i]
		op, flags := r.readByte(), r.readByte()
		if int(op) < len(opNames) {
			rec.Op = opNames[op]
		} else {
			r.fail()
		}
		rec.Source, rec.Namespace = r.str(), r.str()
		rec.Key, rec.Before, rec.After = none(r.skip()), none(r.skip()), none(r.skip())
		rec.CommitTime = r.str()
		if id := r.str(); id != "" || flags != 0 {
			rec.Tx = &Tx{ID: id, First: flags&txFirst != 0, Last: flags&txLast != 0}
		}
		if p := r.bytes(); len(p) > 0 {
			rec.Position = p
		}
	}
	if r.err == nil && r.i != len(b) {
		r.fail()
	}
	return recs, r.err
}

// records returns n records, zero, in recBuf.
func records(n int) []Record {
	clear(recBuf[:cap(recBuf)])
	if cap(recBuf) < n {
		recBuf = make([]Record, n)
	}
	return recBuf[:n]
}

// none is v, or nil for null.
func none(v Value) Value {
	if len(v) == 1 && v[0] == tagNull {
		return nil
	}
	return v
}

// appendBinary appends r in the binary form.
func (r *Record) appendBinary(b []byte) []byte {
	var flags byte
	var id string
	if r.Tx != nil {
		id = r.Tx.ID
		if r.Tx.First {
			flags |= txFirst
		}
		if r.Tx.Last {
			flags |= txLast
		}
	}
	b = append(b, opByte(r.Op), flags)
	b = appendStr(appendStr(b, r.Source), r.Namespace)
	b = r.After.appendBinary(r.Before.appendBinary(r.Key.appendBinary(b)))
	b = appendStr(appendStr(b, r.CommitTime), id)
	return appendBytes(b, trimSpace(r.Position))
}

// Field is a field of a Row.
type Field struct {
	Name  string
	Value Value
}

// Row is a map value's fields, in order.
type Row []Field

// ParseRow reads v, a map value, or null or none for a nil Row. The
// fields' values are slices of v.
func ParseRow(v Value) (Row, error) {
	if v.IsNull() {
		return nil, nil
	}
	if doc, ok := v.canonical(); ok {
		return parseRowJSON(doc)
	}
	if v[0] != tagMap {
		return nil, errNotMap
	}
	r := reader{b: v, i: 1}
	r.into()
	n := r.uvarint()
	if n > uint64(len(v)/2) {
		return nil, errNotMap
	}
	row := make(Row, n)
	for i := range row {
		row[i].Name = string(r.bytes())
		row[i].Value = r.skip()
	}
	if r.err != nil || r.i != len(v) {
		return nil, errNotMap
	}
	return row, nil
}

func parseRowJSON(v []byte) (Row, error) {
	var inner []byte
	if err := members(v, func(name string, val []byte) error {
		if name == "map" {
			inner = val
		}
		return nil
	}); err != nil || inner == nil {
		return nil, errNotMap
	}
	var row Row
	err := members(inner, func(name string, val []byte) error {
		row = append(row, Field{Name: name, Value: val})
		return nil
	})
	return row, err
}

// Get returns the field name.
func (r Row) Get(name string) (Value, bool) {
	for _, f := range r {
		if f.Name == name {
			return f.Value, true
		}
	}
	return nil, false
}

// Set sets the field name to v, adding it at the end when it is new.
func (r *Row) Set(name string, v Value) {
	for i, f := range *r {
		if f.Name == name {
			(*r)[i].Value = v
			return
		}
	}
	*r = append(*r, Field{name, v})
}

// Delete removes the field name.
func (r *Row) Delete(name string) {
	for i, f := range *r {
		if f.Name == name {
			*r = append((*r)[:i], (*r)[i+1:]...)
			return
		}
	}
}

// Value returns r as a map value, in the form the plugin took.
func (r Row) Value() Value {
	if form == formBinary {
		b := binary.AppendUvarint([]byte{tagMap, 0, 0, 0, 0}, uint64(len(r)))
		for _, f := range r {
			b = f.Value.appendBinary(appendStr(b, f.Name))
		}
		binary.LittleEndian.PutUint32(b[1:], uint32(len(b)-5))
		return b
	}
	b := []byte(`{"map":{`)
	for i, f := range r {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendQuote(b, f.Name)
		b = f.Value.AppendJSON(append(b, ':'))
	}
	return append(b, "}}"...)
}

// Int returns i as a value.
func Int(i int64) Value {
	if form == formBinary {
		return binary.AppendVarint([]byte{tagInt}, i)
	}
	return []byte(`{"int":` + strconv.FormatInt(i, 10) + `}`)
}

// String returns s as a value.
func String(s string) Value {
	if form == formBinary {
		return appendStr([]byte{tagString}, s)
	}
	return appendQuote(nil, s)
}

// Decimal returns the decimal number written as s, as "-12.30", as a
// value.
func Decimal(s string) Value {
	if form == formBinary {
		return appendStr([]byte{tagDecimal}, s)
	}
	return append(appendQuote([]byte(`{"decimal":`), s), '}')
}

// Bool returns b as a value.
func Bool(b bool) Value {
	if form == formBinary {
		if b {
			return Value{tagBool, 1}
		}
		return Value{tagBool, 0}
	}
	return []byte(strconv.FormatBool(b))
}

// Null is the null value.
func Null() Value {
	if form == formBinary {
		return Value{tagNull}
	}
	return []byte("null")
}

// AsInt returns v's integer, an int or a uint's.
func AsInt(v Value) (int64, bool) {
	if doc, ok := v.canonical(); ok {
		return asIntJSON(doc)
	}
	r := reader{b: v, i: 1}
	var n int64
	switch v[0] {
	case tagInt:
		n = r.varint()
	case tagUint:
		n = int64(r.uvarint())
	default:
		return 0, false
	}
	return n, r.err == nil
}

func asIntJSON(v []byte) (int64, bool) {
	var n int64
	ok := false
	_ = members(v, func(name string, val []byte) error {
		var err error
		switch name {
		case "int":
			n, err = strconv.ParseInt(string(val), 10, 64)
		case "uint":
			var u uint64
			u, err = strconv.ParseUint(string(val), 10, 64)
			n = int64(u)
		default:
			return nil
		}
		ok = err == nil
		return nil
	})
	return n, ok
}

// AsString returns v's string.
func AsString(v Value) (string, bool) {
	if doc, ok := v.canonical(); ok {
		i := skipSpace(doc, 0)
		if i >= len(doc) || doc[i] != '"' {
			return "", false
		}
		return unquote(doc[i:]), true
	}
	return binaryText(v, tagString)
}

// AsDecimal returns the text of v's decimal, as "12.30".
func AsDecimal(v Value) (string, bool) {
	if doc, ok := v.canonical(); ok {
		var s string
		found := false
		_ = members(doc, func(name string, val []byte) error {
			if name == "decimal" && len(val) > 0 && val[0] == '"' {
				s, found = unquote(val), true
			}
			return nil
		})
		return s, found
	}
	return binaryText(v, tagDecimal)
}

// binaryText returns the text of a binary value of tag.
func binaryText(v Value, tag byte) (string, bool) {
	if v[0] != tag {
		return "", false
	}
	r := reader{b: v, i: 1}
	b := r.bytes()
	return string(b), r.err == nil
}
