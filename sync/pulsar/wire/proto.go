package pulsarwire

import (
	"encoding/binary"
	"errors"
)

// Pulsar's commands are protocol buffers (PulsarApi.proto); the few fields
// the client sends and reads are encoded here by hand, as decision 036 has
// the protocols be written in fit.

// errProto is a protocol buffer that does not parse.
var errProto = errors.New("pulsar: a malformed protocol buffer")

// The wire types used.
const (
	wireVarint = 0
	wire64     = 1
	wireBytes  = 2
	wire32     = 5
)

// pb appends protocol buffer fields.
type pb []byte

func (b pb) tag(f, wt int) pb { return binary.AppendUvarint(b, uint64(f)<<3|uint64(wt)) }

// uint appends a varint field: uint32, uint64, an enum, or an int32 or
// int64 as its two's complement, as protobuf writes a negative one.
func (b pb) uint(f int, v uint64) pb { return binary.AppendUvarint(b.tag(f, wireVarint), v) }

func (b pb) int(f int, v int64) pb { return b.uint(f, uint64(v)) }

func (b pb) bool(f int, v bool) pb {
	if v {
		return b.uint(f, 1)
	}
	return b.uint(f, 0)
}

func (b pb) bytes(f int, v []byte) pb {
	return append(binary.AppendUvarint(b.tag(f, wireBytes), uint64(len(v))), v...)
}

func (b pb) str(f int, v string) pb {
	return append(binary.AppendUvarint(b.tag(f, wireBytes), uint64(len(v))), v...)
}

// pbReader reads a protocol buffer's fields one after the other.
type pbReader struct {
	b   []byte
	err error
	// wt is the wire type of the field next read.
	wt int
}

// next reads a field's tag, returning its number, or false at the end or on
// an error.
func (r *pbReader) next() (int, bool) {
	if r.err != nil || len(r.b) == 0 {
		return 0, false
	}
	t := r.varint()
	if r.err != nil || t>>3 == 0 || t>>3 > 1<<29 {
		r.fail()
		return 0, false
	}
	r.wt = int(t & 7)
	return int(t >> 3), true
}

func (r *pbReader) fail() {
	r.err = errProto
	r.b = nil
}

func (r *pbReader) varint() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.fail()
		return 0
	}
	r.b = r.b[n:]
	return v
}

// uint reads a varint field's value.
func (r *pbReader) uint() uint64 {
	if r.wt != wireVarint {
		r.fail()
		return 0
	}
	return r.varint()
}

func (r *pbReader) int() int64 { return int64(r.uint()) }

// bytes reads a length-delimited field's value.
func (r *pbReader) bytes() []byte {
	if r.wt != wireBytes {
		r.fail()
		return nil
	}
	n := r.varint()
	if r.err != nil || n > uint64(len(r.b)) {
		r.fail()
		return nil
	}
	v := r.b[:n:n]
	r.b = r.b[n:]
	return v
}

func (r *pbReader) str() string { return string(r.bytes()) }

// skip passes over a field the client does not read.
func (r *pbReader) skip() {
	switch r.wt {
	case wireVarint:
		r.varint()
	case wire64, wire32:
		n := 8
		if r.wt == wire32 {
			n = 4
		}
		if len(r.b) < n {
			r.fail()
			return
		}
		r.b = r.b[n:]
	case wireBytes:
		r.bytes()
	default:
		r.fail()
	}
}
