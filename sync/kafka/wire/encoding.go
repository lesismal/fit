package kafkawire

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// errShort is a response or a batch that ends before its fields do.
var errShort = errors.New("kafka: message too short")

// encoder appends Kafka's primitive types to a buffer, big-endian, with
// strings and bytes behind an int16 or int32 length (the non-flexible
// versions this package speaks) and varints in record batches.
type encoder struct {
	b []byte
}

func (e *encoder) int8(v int8)   { e.b = append(e.b, byte(v)) }
func (e *encoder) int16(v int16) { e.b = binary.BigEndian.AppendUint16(e.b, uint16(v)) }
func (e *encoder) int32(v int32) { e.b = binary.BigEndian.AppendUint32(e.b, uint32(v)) }
func (e *encoder) int64(v int64) { e.b = binary.BigEndian.AppendUint64(e.b, uint64(v)) }
func (e *encoder) bool(v bool) {
	if v {
		e.b = append(e.b, 1)
	} else {
		e.b = append(e.b, 0)
	}
}

func (e *encoder) string(s string) {
	e.int16(int16(len(s)))
	e.b = append(e.b, s...)
}

// nullableString writes -1 for a nil pointer.
func (e *encoder) nullableString(s *string) {
	if s == nil {
		e.int16(-1)
		return
	}
	e.string(*s)
}

// bytes writes an int32 length, -1 for nil.
func (e *encoder) bytes(b []byte) {
	if b == nil {
		e.int32(-1)
		return
	}
	e.int32(int32(len(b)))
	e.b = append(e.b, b...)
}

func (e *encoder) varint(v int64) { e.b = binary.AppendVarint(e.b, v) }

// varbytes writes a varint length, -1 for nil, as records do.
func (e *encoder) varbytes(b []byte) {
	if b == nil {
		e.varint(-1)
		return
	}
	e.varint(int64(len(b)))
	e.b = append(e.b, b...)
}

// decoder reads what encoder writes. The first error sticks: every later
// read returns zero values, so a message is decoded field by field and
// checked once at its end.
type decoder struct {
	b   []byte
	off int
	err error
}

func (d *decoder) fail(err error) {
	if d.err == nil {
		d.err = err
	}
}

func (d *decoder) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || len(d.b)-d.off < n {
		d.fail(errShort)
		return nil
	}
	b := d.b[d.off : d.off+n]
	d.off += n
	return b
}

func (d *decoder) remaining() int { return len(d.b) - d.off }

func (d *decoder) int8() int8 {
	b := d.take(1)
	if b == nil {
		return 0
	}
	return int8(b[0])
}

func (d *decoder) int16() int16 {
	b := d.take(2)
	if b == nil {
		return 0
	}
	return int16(binary.BigEndian.Uint16(b))
}

func (d *decoder) int32() int32 {
	b := d.take(4)
	if b == nil {
		return 0
	}
	return int32(binary.BigEndian.Uint32(b))
}

func (d *decoder) int64() int64 {
	b := d.take(8)
	if b == nil {
		return 0
	}
	return int64(binary.BigEndian.Uint64(b))
}

func (d *decoder) bool() bool { return d.int8() != 0 }

func (d *decoder) string() string {
	n := d.int16()
	if n < 0 {
		return ""
	}
	return string(d.take(int(n)))
}

func (d *decoder) nullableString() *string {
	n := d.int16()
	if n < 0 {
		return nil
	}
	s := string(d.take(int(n)))
	return &s
}

// bytes returns a slice of the message itself, nil for -1.
func (d *decoder) bytes() []byte {
	n := d.int32()
	if n < 0 {
		return nil
	}
	return d.take(int(n))
}

// arrayLen reads an array's int32 count, bounded by what is left: no
// element is smaller than a byte, so a count past that is a corrupt
// message, not a reason to allocate.
func (d *decoder) arrayLen() int {
	n := d.int32()
	if n < 0 {
		return 0
	}
	if int(n) > d.remaining() {
		d.fail(fmt.Errorf("kafka: array of %d elements in %d bytes", n, d.remaining()))
		return 0
	}
	return int(n)
}

func (d *decoder) varint() int64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Varint(d.b[d.off:])
	if n <= 0 {
		d.fail(errShort)
		return 0
	}
	d.off += n
	return v
}

// varbytes returns a slice of the message, nil for -1.
func (d *decoder) varbytes() []byte {
	n := d.varint()
	if n < 0 {
		return nil
	}
	if n > int64(d.remaining()) {
		d.fail(errShort)
		return nil
	}
	return d.take(int(n))
}
