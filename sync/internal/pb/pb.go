// Package pb appends protocol buffers' wire format by hand, for the sinks
// that send it (loki's push, otlp_logs' export) without a generated
// package: varints, fixed numbers, length-delimited bytes and nested
// messages, and a reader of the few answers they parse.
package pb

import (
	"encoding/binary"
	"errors"
	"math"
)

// The wire types.
const (
	Varint  = 0
	Fixed64 = 1
	Bytes   = 2
	Fixed32 = 5
)

// AppendVarint appends v as a varint.
func AppendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// AppendTag appends a field's tag.
func AppendTag(b []byte, field, wire int) []byte {
	return AppendVarint(b, uint64(field)<<3|uint64(wire))
}

// AppendUint appends a varint field, nothing when v is 0.
func AppendUint(b []byte, field int, v uint64) []byte {
	if v == 0 {
		return b
	}
	return AppendVarint(AppendTag(b, field, Varint), v)
}

// AppendInt appends an int64 field (a varint of its two's complement),
// nothing when v is 0.
func AppendInt(b []byte, field int, v int64) []byte { return AppendUint(b, field, uint64(v)) }

// AppendBool appends a bool field, nothing when v is false.
func AppendBool(b []byte, field int, v bool) []byte {
	if !v {
		return b
	}
	return append(AppendTag(b, field, Varint), 1)
}

// AppendFixed64 appends a fixed64 field, nothing when v is 0.
func AppendFixed64(b []byte, field int, v uint64) []byte {
	if v == 0 {
		return b
	}
	return binary.LittleEndian.AppendUint64(AppendTag(b, field, Fixed64), v)
}

// AppendDouble appends a double field, nothing when v is 0.
func AppendDouble(b []byte, field int, v float64) []byte {
	return AppendFixed64(b, field, math.Float64bits(v))
}

// AppendString appends a string field, nothing when s is empty.
func AppendString(b []byte, field int, s string) []byte {
	if s == "" {
		return b
	}
	return append(AppendVarint(AppendTag(b, field, Bytes), uint64(len(s))), s...)
}

// AppendBytes appends a bytes field, even when p is empty (as a oneof's
// member is).
func AppendBytes(b []byte, field int, p []byte) []byte {
	return append(AppendVarint(AppendTag(b, field, Bytes), uint64(len(p))), p...)
}

// AppendMessage appends a nested message field, its body what fn appends:
// a byte is kept for the length, and the body moved up when its length
// takes more. An empty message is kept, as a oneof's member is.
func AppendMessage(b []byte, field int, fn func([]byte) []byte) []byte {
	b = AppendTag(b, field, Bytes)
	at := len(b)
	b = fn(append(b, 0))
	n := len(b) - at - 1
	if n < 0x80 {
		b[at] = byte(n)
		return b
	}
	var l [binary.MaxVarintLen64]byte
	k := len(AppendVarint(l[:0], uint64(n)))
	b = append(b, l[:k-1]...)
	copy(b[at+k:], b[at+1:at+1+n])
	copy(b[at:], AppendVarint(l[:0], uint64(n)))
	return b
}

// ErrMalformed is a message that does not parse.
var ErrMalformed = errors.New("pb: a malformed message")

// Field is a field read: its number and wire type, and its varint (or
// fixed) value or its bytes.
type Field struct {
	Num, Wire int
	Value     uint64
	Bytes     []byte
}

// Next reads the field at the start of b and returns it and the rest.
func Next(b []byte) (Field, []byte, error) {
	tag, n := binary.Uvarint(b)
	if n <= 0 {
		return Field{}, nil, ErrMalformed
	}
	b = b[n:]
	f := Field{Num: int(tag >> 3), Wire: int(tag & 7)}
	switch f.Wire {
	case Varint:
		v, n := binary.Uvarint(b)
		if n <= 0 {
			return Field{}, nil, ErrMalformed
		}
		f.Value, b = v, b[n:]
	case Fixed64:
		if len(b) < 8 {
			return Field{}, nil, ErrMalformed
		}
		f.Value, b = binary.LittleEndian.Uint64(b), b[8:]
	case Fixed32:
		if len(b) < 4 {
			return Field{}, nil, ErrMalformed
		}
		f.Value, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
	case Bytes:
		l, n := binary.Uvarint(b)
		if n <= 0 || l > uint64(len(b)-n) {
			return Field{}, nil, ErrMalformed
		}
		f.Bytes, b = b[n:n+int(l)], b[n+int(l):]
	default:
		return Field{}, nil, ErrMalformed
	}
	return f, b, nil
}
