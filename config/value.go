package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Value is a piece of a config kept as it was written, together with where
// it was: a module's params, which only the module knows how to decode, or
// the position of a part of the config that later checks report problems
// at. The zero Value is an absent one.
type Value struct {
	n    *node
	path string
	// at is where an absent Value would have been: the object or array it
	// is missing from, whose position its errors report.
	at *node
}

// ParseValue parses data, which must hold one JSON value, into a Value whose
// paths start at path.
func ParseValue(data []byte, path string) (Value, error) {
	n, err := parse(data)
	if err != nil {
		err.(*Error).Path = path
		return Value{}, err
	}
	return Value{n: n, path: path}, nil
}

// IsZero reports whether v is absent. It lets encoding/json leave out an
// absent Value tagged omitzero.
func (v Value) IsZero() bool { return v.n == nil }

// Path returns where v is in its document, as in routes[0].handler.params.
func (v Value) Path() string { return v.path }

// Pos returns the 1-based line and column v starts at, or, for an absent v,
// those of the object it is missing from. Both are 0 when there is no
// position, as for a Value built in Go.
func (v Value) Pos() (line, col int) {
	n := v.anchor()
	if n == nil {
		return 0, 0
	}
	return n.pos(n.off)
}

func (v Value) anchor() *node {
	if v.n != nil {
		return v.n
	}
	return v.at
}

// Field returns the member name of the object v holds, or an absent Value
// when there is no such member.
func (v Value) Field(name string) Value {
	path := joinField(v.path, name)
	if v.n != nil && v.n.kind == kindObject {
		if m := v.n.member(name); m != nil {
			return Value{n: m, path: path}
		}
	}
	return Value{path: path, at: v.anchor()}
}

// Index returns element i of the array v holds, or an absent Value when
// there is no such element.
func (v Value) Index(i int) Value {
	path := joinIndex(v.path, i)
	if v.n != nil && v.n.kind == kindArray && i >= 0 && i < len(v.n.elems) {
		return Value{n: v.n.elems[i], path: path}
	}
	return Value{path: path, at: v.anchor()}
}

// Errorf returns an Error about v, at its path and position.
func (v Value) Errorf(format string, args ...any) *Error {
	line, col := v.Pos()
	return &Error{Path: v.path, Line: line, Col: col, Msg: fmt.Sprintf(format, args...)}
}

// Decode decodes v into dst, a non-nil pointer, as Load decodes a whole
// config: unknown fields, values of the wrong type and missing required
// fields are all reported, each with its path. An absent v decodes as an
// empty object.
func (v Value) Decode(dst any) error {
	n := v.n
	if n == nil || n.kind == kindNull {
		n = &node{kind: kindObject, off: -1}
		if at := v.anchor(); at != nil {
			n.src, n.off = at.src, at.off
		}
	}
	var d decoder
	d.decodeInto(n, v.path, dst)
	if len(d.errs) > 0 {
		return d.errs
	}
	return nil
}

// MarshalJSON writes v back out as JSON, with object members in the order
// they were written.
func (v Value) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	writeNode(&buf, v.n)
	return buf.Bytes(), nil
}

func writeNode(buf *bytes.Buffer, n *node) {
	if n == nil {
		buf.WriteString("null")
		return
	}
	switch n.kind {
	case kindNull:
		buf.WriteString("null")
	case kindBool:
		buf.WriteString(strconv.FormatBool(n.b))
	case kindNumber:
		buf.WriteString(n.s)
	case kindString:
		writeString(buf, n.s)
	case kindArray:
		buf.WriteByte('[')
		for i, e := range n.elems {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeNode(buf, e)
		}
		buf.WriteByte(']')
	case kindObject:
		buf.WriteByte('{')
		for i, m := range n.members {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, m.key)
			buf.WriteByte(':')
			writeNode(buf, m.val)
		}
		buf.WriteByte('}')
	}
}

func writeString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)           // a string always encodes
	buf.Truncate(buf.Len() - 1) // Encode ends with a newline
}
