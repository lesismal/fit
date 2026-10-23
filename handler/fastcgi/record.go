package fastcgi

import (
	"encoding/binary"
	"errors"
)

// FastCGI 1.0's record types, roles and flags, as fit uses them.
const (
	fcgiVersion = 1

	typeBeginRequest = 1
	typeAbortRequest = 2
	typeEndRequest   = 3
	typeParams       = 4
	typeStdin        = 5
	typeStdout       = 6
	typeStderr       = 7

	roleResponder = 1
	flagKeepConn  = 1

	// requestID is the one id fit uses: a connection carries one request
	// at a time, as PHP-FPM serves them.
	requestID = 1

	headerLen     = 8
	maxContentLen = 65535
)

// appendRecord appends records of type t carrying content, split as one
// record takes at most 65535 bytes; an empty content makes one empty record,
// which ends a stream.
func appendRecord(b []byte, t byte, content []byte) []byte {
	for {
		n := min(len(content), maxContentLen)
		pad := (8 - n%8) % 8
		b = append(b, fcgiVersion, t, 0, requestID, byte(n>>8), byte(n), byte(pad), 0)
		b = append(b, content[:n]...)
		b = append(b, make([]byte, pad)...)
		content = content[n:]
		if len(content) == 0 {
			return b
		}
	}
}

// beginRequest is the record that starts a request as a responder, the
// connection kept for the next.
func appendBeginRequest(b []byte) []byte {
	return appendRecord(b, typeBeginRequest, []byte{0, roleResponder, flagKeepConn, 0, 0, 0, 0, 0})
}

// appendParam appends a name-value pair as FastCGI encodes them: each
// length in one byte below 128, in four with the high bit set past it.
func appendParam(b []byte, name, value string) []byte {
	for _, n := range []int{len(name), len(value)} {
		if n < 128 {
			b = append(b, byte(n))
		} else {
			b = binary.BigEndian.AppendUint32(b, uint32(n)|1<<31)
		}
	}
	return append(append(b, name...), value...)
}

// record is one record read from a connection.
type record struct {
	t       byte
	id      uint16
	content []byte
}

// errProtocol is what a connection that breaks FastCGI's framing fails with.
var errProtocol = errors.New("fastcgi: the application broke the protocol")

// reader reads records out of what a connection delivers, a piece at a
// time, keeping at most one record's worth between pieces.
type reader struct {
	buf []byte
}

// feed adds data and calls fn with each whole record in it; the content
// fn gets is valid only during the call.
func (r *reader) feed(data []byte, fn func(record) error) error {
	if len(r.buf) > 0 {
		r.buf = append(r.buf, data...)
		data = r.buf
	}
	for len(data) >= headerLen {
		if data[0] != fcgiVersion {
			return errProtocol
		}
		n := int(binary.BigEndian.Uint16(data[4:]))
		total := headerLen + n + int(data[6])
		if len(data) < total {
			break
		}
		if err := fn(record{t: data[1], id: binary.BigEndian.Uint16(data[2:]), content: data[headerLen : headerLen+n]}); err != nil {
			return err
		}
		data = data[total:]
	}
	// What is left is part of a record: kept, in a buffer of the reader's
	// own, since data is the connection's.
	r.buf = append(r.buf[:0], data...)
	return nil
}
