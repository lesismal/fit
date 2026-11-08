package binlog

import (
	"encoding/binary"
	"fmt"
)

// MySQL's serialization (libs/mysql/serialization), which MySQL 8.3's
// tagged GTID event and 8.0.26's heartbeat v2 are written in: the
// format's version, then a message: its size (from the version on) and
// the ID of its last field a reader must know, then fields, each its ID
// and its value; a field left out is not written. Integers are variable-length: the count of
// the first byte's trailing one bits, plus one, is the integer's length in
// bytes, little-endian, the value after those bits; nine bytes are a 0xff
// and eight bytes of value. A signed integer has its sign in the lowest
// bit of the value (zigzag). A string is its length and its bytes; a
// fixed array of bytes is each byte as an integer.

// message reads the fields of a message.
type message struct {
	b    []byte
	id   uint64
	err  error
	last uint64
}

// readVarlen reads an unsigned variable-length integer from b, and how
// many bytes it took; 0 when b is too short.
func readVarlen(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	n := 1
	for c := b[0]; c&1 != 0 && n < 9; c >>= 1 {
		n++
	}
	if len(b) < n {
		return 0, 0
	}
	if n == 9 {
		return binary.LittleEndian.Uint64(b[1:]), 9
	}
	var x [8]byte
	copy(x[:], b[:n])
	return binary.LittleEndian.Uint64(x[:]) >> n, n
}

func readMessage(b []byte) (*message, error) {
	m := &message{}
	_, v := readVarlen(b)
	size, n := readVarlen(b[v:])
	if v == 0 || n == 0 || size < uint64(v+n) || size > uint64(len(b)) {
		return nil, malformed("message size")
	}
	b = b[v+n : size]
	last, k := readVarlen(b)
	if k == 0 {
		return nil, malformed("message header")
	}
	m.b, m.last = b[k:], last
	return m, nil
}

// next reads the next field's ID; false at the end or after an error.
func (m *message) next() bool {
	if m.err != nil || len(m.b) == 0 {
		return false
	}
	m.id = m.varlen()
	return m.err == nil
}

func (m *message) varlen() uint64 {
	v, n := readVarlen(m.b)
	if n == 0 {
		m.fail()
		return 0
	}
	m.b = m.b[n:]
	return v
}

func (m *message) fail() {
	if m.err == nil {
		m.err = malformed("message field %d", m.id)
	}
	m.b = nil
}

func (m *message) uint() uint64 { return m.varlen() }

func (m *message) int() int64 {
	v := m.varlen()
	return int64(v>>1) ^ -int64(v&1)
}

// bytes reads a fixed array of bytes, each an integer, into b.
func (m *message) bytes(b []byte) {
	for i := range b {
		b[i] = byte(m.varlen())
	}
}

func (m *message) fixed(n int) []byte {
	if len(m.b) < n {
		m.fail()
		return nil
	}
	v := m.b[:n]
	m.b = m.b[n:]
	return v
}

func (m *message) str() []byte {
	n := m.varlen()
	if n > uint64(len(m.b)) {
		m.fail()
		return nil
	}
	return m.fixed(int(n))
}

// skip is a field this package does not know, which a reader may skip
// only past the last one it must know; its size is not written, so the
// rest of the message is skipped.
func (m *message) skip() {
	if m.id <= m.last {
		m.err = fmt.Errorf("%w: unknown field %d of a message", ErrUnsupported, m.id)
	}
	m.b = nil
}
