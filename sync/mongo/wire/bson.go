// Package mongowire is a MongoDB client written for fit's sync: BSON, the
// OP_MSG wire protocol, finding a replica set's primary, SCRAM-SHA-256, TLS
// and cursors. It imports no driver.
//
// It is a blocking client: a Conn is a net.Conn read and written in
// lockstep, one command at a time, by the goroutine that calls it. Sync's
// connectors call it from goroutines of their own, never from a fib worker.
package mongowire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// The BSON types.
const (
	TypeDouble        byte = 0x01
	TypeString        byte = 0x02
	TypeDocument      byte = 0x03
	TypeArray         byte = 0x04
	TypeBinary        byte = 0x05
	TypeUndefined     byte = 0x06
	TypeObjectID      byte = 0x07
	TypeBool          byte = 0x08
	TypeDateTime      byte = 0x09
	TypeNull          byte = 0x0A
	TypeRegex         byte = 0x0B
	TypeDBPointer     byte = 0x0C
	TypeJavaScript    byte = 0x0D
	TypeSymbol        byte = 0x0E
	TypeCodeWithScope byte = 0x0F
	TypeInt32         byte = 0x10
	TypeTimestamp     byte = 0x11
	TypeInt64         byte = 0x12
	TypeDecimal128    byte = 0x13
	TypeMinKey        byte = 0xFF
	TypeMaxKey        byte = 0x7F
)

// maxDepth bounds the nesting of documents and arrays read.
const maxDepth = 200

// ErrMalformed is wrapped by the errors of BSON that cannot be read.
var ErrMalformed = errors.New("mongowire: malformed BSON")

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrMalformed}, args...)...)
}

// Raw is one BSON document as it is on the wire. The documents a reply
// holds share its buffer, which is not reused.
type Raw []byte

// RawValue is one BSON value: its type and its bytes.
type RawValue struct {
	Type byte
	Data []byte
}

// Validate checks that r is one whole, well-formed document, nested no
// deeper than 200 levels.
func (r Raw) Validate() error {
	n, err := docSize(r, 0)
	if err != nil {
		return err
	}
	if n != len(r) {
		return malformed("%d bytes after the document", len(r)-n)
	}
	return nil
}

// docSize checks the document at the start of b and returns its length.
func docSize(b []byte, depth int) (int, error) {
	if depth > maxDepth {
		return 0, malformed("nested deeper than %d", maxDepth)
	}
	if len(b) < 5 {
		return 0, malformed("a document of %d bytes", len(b))
	}
	n := int(int32(binary.LittleEndian.Uint32(b)))
	if n < 5 || n > len(b) {
		return 0, malformed("a document's length %d of %d bytes", n, len(b))
	}
	if b[n-1] != 0 {
		return 0, malformed("a document not ended by 0")
	}
	p := 4
	for p < n-1 {
		t := b[p]
		p++
		k := cstringEnd(b[p : n-1])
		if k < 0 {
			return 0, malformed("a key not ended by 0")
		}
		p += k + 1
		sz, err := valueSize(t, b[p:n-1], depth)
		if err != nil {
			return 0, err
		}
		p += sz
	}
	if p != n-1 {
		return 0, malformed("a document's elements overrun it")
	}
	return n, nil
}

func cstringEnd(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return -1
}

// strSize checks the BSON string at the start of b and returns its length.
func strSize(b []byte) (int, error) {
	if len(b) < 4 {
		return 0, malformed("a string's length cut")
	}
	n := int(int32(binary.LittleEndian.Uint32(b)))
	if n < 1 || n > len(b)-4 {
		return 0, malformed("a string's length %d", n)
	}
	if b[4+n-1] != 0 {
		return 0, malformed("a string not ended by 0")
	}
	return 4 + n, nil
}

// valueSize checks the value of type t at the start of b and returns its
// length.
func valueSize(t byte, b []byte, depth int) (int, error) {
	fixed := 0
	switch t {
	case TypeDouble, TypeDateTime, TypeInt64, TypeTimestamp:
		fixed = 8
	case TypeDecimal128:
		fixed = 16
	case TypeObjectID:
		fixed = 12
	case TypeInt32:
		fixed = 4
	case TypeBool:
		if len(b) < 1 || b[0] > 1 {
			return 0, malformed("a bool")
		}
		return 1, nil
	case TypeNull, TypeUndefined, TypeMinKey, TypeMaxKey:
		return 0, nil
	case TypeString, TypeJavaScript, TypeSymbol:
		return strSize(b)
	case TypeDocument, TypeArray:
		return docSize(b, depth+1)
	case TypeBinary:
		if len(b) < 5 {
			return 0, malformed("a binary's length cut")
		}
		n := int(int32(binary.LittleEndian.Uint32(b)))
		if n < 0 || n > len(b)-5 {
			return 0, malformed("a binary's length %d", n)
		}
		if b[4] == 2 {
			if n < 4 || int(int32(binary.LittleEndian.Uint32(b[5:]))) != n-4 {
				return 0, malformed("an old binary's inner length")
			}
		}
		return 5 + n, nil
	case TypeRegex:
		i := cstringEnd(b)
		if i < 0 {
			return 0, malformed("a regex's pattern not ended by 0")
		}
		j := cstringEnd(b[i+1:])
		if j < 0 {
			return 0, malformed("a regex's options not ended by 0")
		}
		return i + 1 + j + 1, nil
	case TypeDBPointer:
		n, err := strSize(b)
		if err != nil {
			return 0, err
		}
		if len(b) < n+12 {
			return 0, malformed("a DBPointer cut")
		}
		return n + 12, nil
	case TypeCodeWithScope:
		if len(b) < 4 {
			return 0, malformed("a code with scope cut")
		}
		n := int(int32(binary.LittleEndian.Uint32(b)))
		if n < 4+5+5 || n > len(b) {
			return 0, malformed("a code with scope's length %d", n)
		}
		s, err := strSize(b[4:n])
		if err != nil {
			return 0, err
		}
		d, err := docSize(b[4+s:n], depth+1)
		if err != nil {
			return 0, err
		}
		if 4+s+d != n {
			return 0, malformed("a code with scope's parts")
		}
		return n, nil
	default:
		return 0, malformed("type 0x%02x", t)
	}
	if len(b) < fixed {
		return 0, malformed("a value of type 0x%02x cut", t)
	}
	return fixed, nil
}

// Each calls fn with each element of r in order, until fn returns an error,
// which Each returns. It returns an error for a malformed document.
func (r Raw) Each(fn func(key string, v RawValue) error) error {
	if len(r) < 5 {
		return malformed("a document of %d bytes", len(r))
	}
	n := int(int32(binary.LittleEndian.Uint32(r)))
	if n < 5 || n > len(r) || r[n-1] != 0 {
		return malformed("a document's length %d of %d bytes", n, len(r))
	}
	p := 4
	for p < n-1 {
		t := r[p]
		p++
		k := cstringEnd(r[p : n-1])
		if k < 0 {
			return malformed("a key not ended by 0")
		}
		key := string(r[p : p+k])
		p += k + 1
		sz, err := valueSize(t, r[p:n-1], 0)
		if err != nil {
			return err
		}
		if err := fn(key, RawValue{Type: t, Data: r[p : p+sz]}); err != nil {
			return err
		}
		p += sz
	}
	return nil
}

var errStop = errors.New("stop")

// Lookup returns the value of r's first element named key, and whether
// there is one.
func (r Raw) Lookup(key string) (RawValue, bool) {
	var out RawValue
	found := false
	_ = r.Each(func(k string, v RawValue) error {
		if k == key {
			out, found = v, true
			return errStop
		}
		return nil
	})
	return out, found
}

// Keys returns r's keys in order; nil for a malformed document.
func (r Raw) Keys() []string {
	var keys []string
	if r.Each(func(k string, _ RawValue) error { keys = append(keys, k); return nil }) != nil {
		return nil
	}
	return keys
}

// Document returns a document's or an array's bytes; nil for another type.
func (v RawValue) Document() Raw {
	if v.Type != TypeDocument && v.Type != TypeArray {
		return nil
	}
	return Raw(v.Data)
}

// Array returns an array's elements in order; nil for another type.
func (v RawValue) Array() []RawValue {
	if v.Type != TypeArray {
		return nil
	}
	var out []RawValue
	_ = Raw(v.Data).Each(func(_ string, e RawValue) error { out = append(out, e); return nil })
	return out
}

// StringValue returns a string's, a symbol's or JavaScript's text, and
// whether v is one.
func (v RawValue) StringValue() (string, bool) {
	switch v.Type {
	case TypeString, TypeSymbol, TypeJavaScript:
		if len(v.Data) >= 5 {
			return string(v.Data[4 : len(v.Data)-1]), true
		}
	}
	return "", false
}

// Int returns an int32's or int64's value, or a double's that is a whole
// number, and whether v is one.
func (v RawValue) Int() (int64, bool) {
	switch v.Type {
	case TypeInt32:
		if len(v.Data) == 4 {
			return int64(int32(binary.LittleEndian.Uint32(v.Data))), true
		}
	case TypeInt64:
		if len(v.Data) == 8 {
			return int64(binary.LittleEndian.Uint64(v.Data)), true
		}
	case TypeDouble:
		if len(v.Data) == 8 {
			f := math.Float64frombits(binary.LittleEndian.Uint64(v.Data))
			if f == math.Trunc(f) && f >= math.MinInt64 && f < math.MaxInt64 {
				return int64(f), true
			}
		}
	}
	return 0, false
}

// Float returns a double's value, or an integer's as a float, and whether
// v is one.
func (v RawValue) Float() (float64, bool) {
	if v.Type == TypeDouble && len(v.Data) == 8 {
		return math.Float64frombits(binary.LittleEndian.Uint64(v.Data)), true
	}
	if i, ok := v.Int(); ok {
		return float64(i), true
	}
	return 0, false
}

// Bool returns a bool's value, and whether v is one.
func (v RawValue) Bool() (bool, bool) {
	if v.Type == TypeBool && len(v.Data) == 1 {
		return v.Data[0] == 1, true
	}
	return false, false
}

// Truthy reports whether v is true, a non-zero number, or another type
// that is not null: what MongoDB reads as true in a flag such as ok.
func (v RawValue) Truthy() bool {
	switch v.Type {
	case TypeBool:
		b, _ := v.Bool()
		return b
	case TypeInt32, TypeInt64, TypeDouble:
		f, _ := v.Float()
		return f != 0
	case TypeNull, TypeUndefined, 0:
		return false
	}
	return true
}

// ObjectID returns an ObjectID's value, and whether v is one.
func (v RawValue) ObjectID() (ObjectID, bool) {
	var id ObjectID
	if v.Type != TypeObjectID || len(v.Data) != 12 {
		return id, false
	}
	copy(id[:], v.Data)
	return id, true
}

// Time returns a UTC datetime's value, and whether v is one.
func (v RawValue) Time() (time.Time, bool) {
	if v.Type != TypeDateTime || len(v.Data) != 8 {
		return time.Time{}, false
	}
	return time.UnixMilli(int64(binary.LittleEndian.Uint64(v.Data))).UTC(), true
}

// Timestamp returns a timestamp's value, and whether v is one.
func (v RawValue) Timestamp() (Timestamp, bool) {
	if v.Type != TypeTimestamp || len(v.Data) != 8 {
		return Timestamp{}, false
	}
	return Timestamp{I: binary.LittleEndian.Uint32(v.Data), T: binary.LittleEndian.Uint32(v.Data[4:])}, true
}

// Binary returns a binary's subtype and data, and whether v is one; the
// data of the old subtype 2 is without its inner length.
func (v RawValue) Binary() (Binary, bool) {
	if v.Type != TypeBinary || len(v.Data) < 5 {
		return Binary{}, false
	}
	b := Binary{Subtype: v.Data[4], Data: v.Data[5:]}
	if b.Subtype == 2 && len(b.Data) >= 4 {
		b.Data = b.Data[4:]
	}
	return b, true
}

// ObjectID is a BSON ObjectId.
type ObjectID [12]byte

// String returns id as 24 hexadecimal digits.
func (id ObjectID) String() string {
	const hexd = "0123456789abcdef"
	var b [24]byte
	for i, c := range id {
		b[2*i] = hexd[c>>4]
		b[2*i+1] = hexd[c&15]
	}
	return string(b[:])
}

// ParseObjectID reads 24 hexadecimal digits.
func ParseObjectID(s string) (ObjectID, error) {
	var id ObjectID
	if len(s) != 24 {
		return id, fmt.Errorf("mongowire: an ObjectId of %d characters", len(s))
	}
	for i := 0; i < 12; i++ {
		n, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			return id, fmt.Errorf("mongowire: an ObjectId %q", s)
		}
		id[i] = byte(n)
	}
	return id, nil
}

// Timestamp is a BSON timestamp: seconds and an ordinal within them.
type Timestamp struct {
	T, I uint32
}

// Binary is BSON binary data of a subtype.
type Binary struct {
	Subtype byte
	Data    []byte
}

// Regex is a BSON regular expression.
type Regex struct {
	Pattern, Options string
}

// MinKey and MaxKey are the BSON values below and above every other.
type (
	MinKey struct{}
	MaxKey struct{}
)

// Undefined is BSON's deprecated undefined.
type Undefined struct{}
