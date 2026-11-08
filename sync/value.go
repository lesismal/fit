package fitsync

import (
	"math"
	"time"
)

// Kind is the type of a Value.
type Kind uint8

// The kinds of Value.
const (
	KindNull Kind = iota
	KindBool
	KindInt
	KindUint
	KindFloat
	// KindDecimal is an exact decimal number, held as its text, as
	// "-12.3400", so that no precision or scale is lost.
	KindDecimal
	KindString
	KindBytes
	// KindTime is an instant, with the offset it was written with.
	KindTime
	// KindDate is a calendar date, without a time or zone.
	KindDate
	// KindJSON is a JSON document, held as its text, as a database's JSON
	// column is.
	KindJSON
	KindArray
	// KindMap is a map whose fields keep their order, as a row's columns.
	KindMap
)

var kindNames = [...]string{"null", "bool", "int", "uint", "float", "decimal", "string", "bytes", "time", "date", "json", "array", "map"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// Value is a typed value of a record: a column, a field, a key. The zero
// Value is null. A Value is not changed once made; its Bytes, Elems and
// Fields are not to be changed by those that read them.
type Value struct {
	kind   Kind
	num    uint64 // bool, int, uint, float's bits
	str    string // decimal, string, bytes, json
	tm     time.Time
	elems  []Value
	fields []Field
}

// Field is a named Value of a map.
type Field struct {
	Name  string
	Value Value
}

// Null returns the null Value.
func Null() Value { return Value{} }

// Bool returns b as a Value.
func Bool(b bool) Value {
	v := Value{kind: KindBool}
	if b {
		v.num = 1
	}
	return v
}

// Int returns i as a Value.
func Int(i int64) Value { return Value{kind: KindInt, num: uint64(i)} }

// Uint returns u as a Value.
func Uint(u uint64) Value { return Value{kind: KindUint, num: u} }

// Float returns f as a Value.
func Float(f float64) Value { return Value{kind: KindFloat, num: math.Float64bits(f)} }

// Decimal returns the decimal number written as s, as "-12.30", as a Value.
// s is kept as written; the source is trusted to write a number.
func Decimal(s string) Value { return Value{kind: KindDecimal, str: s} }

// String returns s as a Value.
func String(s string) Value { return Value{kind: KindString, str: s} }

// Bytes returns a copy of b as a Value.
func Bytes(b []byte) Value { return Value{kind: KindBytes, str: string(b)} }

// Time returns t as a Value, without its monotonic reading.
func Time(t time.Time) Value { return Value{kind: KindTime, tm: t.Round(0)} }

// Date returns the date of year, month and day as a Value.
func Date(year int, month time.Month, day int) Value {
	return Value{kind: KindDate, tm: time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// JSON returns the JSON document doc as a Value. doc is kept as written;
// the source is trusted to write JSON.
func JSON(doc []byte) Value { return Value{kind: KindJSON, str: string(doc)} }

// Array returns elems as a Value; it keeps elems.
func Array(elems ...Value) Value { return Value{kind: KindArray, elems: elems} }

// Map returns fields as a Value, in their order; it keeps fields.
func Map(fields ...Field) Value { return Value{kind: KindMap, fields: fields} }

// Kind returns v's kind.
func (v Value) Kind() Kind { return v.kind }

// IsNull reports whether v is null.
func (v Value) IsNull() bool { return v.kind == KindNull }

// AsBool returns v's bool; false for another kind.
func (v Value) AsBool() bool { return v.kind == KindBool && v.num != 0 }

// AsInt returns v's int; 0 for another kind.
func (v Value) AsInt() int64 {
	if v.kind != KindInt {
		return 0
	}
	return int64(v.num)
}

// AsUint returns v's uint; 0 for another kind.
func (v Value) AsUint() uint64 {
	if v.kind != KindUint {
		return 0
	}
	return v.num
}

// AsFloat returns v's float; 0 for another kind.
func (v Value) AsFloat() float64 {
	if v.kind != KindFloat {
		return 0
	}
	return math.Float64frombits(v.num)
}

// Text returns the text of a decimal, a string, bytes or JSON; "" for
// another kind.
func (v Value) Text() string {
	switch v.kind {
	case KindDecimal, KindString, KindBytes, KindJSON:
		return v.str
	}
	return ""
}

// AsTime returns v's time, or its date at midnight UTC; the zero time for
// another kind.
func (v Value) AsTime() time.Time {
	if v.kind != KindTime && v.kind != KindDate {
		return time.Time{}
	}
	return v.tm
}

// Elems returns an array's elements; nil for another kind.
func (v Value) Elems() []Value { return v.elems }

// Fields returns a map's fields, in order; nil for another kind.
func (v Value) Fields() []Field { return v.fields }

// Get returns the map field named name, and whether there is one.
func (v Value) Get(name string) (Value, bool) {
	for _, f := range v.fields {
		if f.Name == name {
			return f.Value, true
		}
	}
	return Value{}, false
}

// Equal reports whether v and w are the same value of the same kind; times
// are equal when they are the same instant with the same offset.
func (v Value) Equal(w Value) bool {
	if v.kind != w.kind || v.num != w.num || v.str != w.str || len(v.elems) != len(w.elems) || len(v.fields) != len(w.fields) {
		return false
	}
	if v.kind == KindTime || v.kind == KindDate {
		_, vo := v.tm.Zone()
		_, wo := w.tm.Zone()
		if !v.tm.Equal(w.tm) || vo != wo {
			return false
		}
	}
	for i := range v.elems {
		if !v.elems[i].Equal(w.elems[i]) {
			return false
		}
	}
	for i := range v.fields {
		if v.fields[i].Name != w.fields[i].Name || !v.fields[i].Value.Equal(w.fields[i].Value) {
			return false
		}
	}
	return true
}

// valueOverhead is what a Value is reckoned to take besides its text and
// its elements.
const valueOverhead = 32

// size reckons the memory v takes, for the queue's and the batches' bounds.
func (v Value) size() int {
	n := valueOverhead + len(v.str)
	for _, e := range v.elems {
		n += e.size()
	}
	for _, f := range v.fields {
		n += len(f.Name) + f.Value.size()
	}
	return n
}
