package mssqlwire

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// The packet types the client sends and reads.
const (
	pktSQLBatch  = 0x01
	pktRPC       = 0x03
	pktReply     = 0x04
	pktAttention = 0x06
	pktBulk      = 0x07
	pktLogin7    = 0x10
	pktPrelogin  = 0x12
)

const (
	headerLen = 8
	statusEOM = 0x01
	// DefaultPacketSize is the packet size the client asks for; the server
	// may set another, from 512 to 32767 bytes.
	DefaultPacketSize = 4096
	maxPacketSize     = 32767
	minPacketSize     = 512
)

// errShort is a message that ends inside a token.
var errShort = errors.New("mssql: the server's message ends inside a token")

// msgWriter writes a message as packets of the connection's size, those
// before the last as each fills.
type msgWriter struct {
	c *Conn
	// ctx, when set, ends the message between two packets: the connection
	// is closed then, as a message cannot be cut short.
	ctx context.Context
	typ byte
	buf []byte
	id  byte
}

func (c *Conn) newWriter(typ byte) *msgWriter {
	return &msgWriter{c: c, typ: typ, buf: c.wbuf[:headerLen], id: 1}
}

// Write appends p to the message, sending each packet that fills.
func (w *msgWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		room := w.c.packetSize - len(w.buf)
		if room == 0 {
			if err := w.send(false); err != nil {
				return 0, err
			}
			continue
		}
		k := min(room, len(p))
		w.buf = append(w.buf, p[:k]...)
		p = p[k:]
	}
	return n, nil
}

// send sends the packet the buffer holds, the message's last when eom.
func (w *msgWriter) send(eom bool) error {
	if w.ctx != nil && w.ctx.Err() != nil {
		return w.c.fail(fmt.Errorf("mssql: a request cut short, the connection closed: %w", w.ctx.Err()))
	}
	h := w.buf[:headerLen]
	h[0] = w.typ
	h[1] = 0
	if eom {
		h[1] = statusEOM
	}
	binary.BigEndian.PutUint16(h[2:], uint16(len(w.buf)))
	h[4], h[5], h[6], h[7] = 0, 0, w.id, 0
	w.id++
	err := w.c.write(w.buf)
	w.buf = w.buf[:headerLen]
	return err
}

// close sends the message's last packet.
func (w *msgWriter) close() error {
	err := w.send(true)
	w.c.wbuf = w.buf[:0]
	return err
}

// msgReader reads a message's payload across its packets.
type msgReader struct {
	c *Conn
	// buf is what is left of the packet read last; eom says it was the
	// message's last.
	buf     []byte
	eom     bool
	started bool
	pkt     []byte
	scratch []byte
}

// begin starts reading the next message.
func (r *msgReader) begin() {
	r.buf, r.eom, r.started = nil, false, false
}

// packet reads the message's next packet.
func (r *msgReader) packet() error {
	if r.eom {
		return errShort
	}
	if cap(r.pkt) < 65536 {
		r.pkt = make([]byte, 65536)
	}
	h := r.pkt[:headerLen]
	if err := r.c.readFull(h); err != nil {
		return err
	}
	n := int(binary.BigEndian.Uint16(h[2:]))
	if n < headerLen {
		return r.c.fail(fmt.Errorf("mssql: a packet of %d bytes, less than its header", n))
	}
	body := r.pkt[headerLen:n]
	if err := r.c.readFull(body); err != nil {
		return err
	}
	r.buf, r.eom, r.started = body, h[1]&statusEOM != 0, true
	return nil
}

// next returns the next n bytes of the message, valid until the next call.
func (r *msgReader) next(n int) ([]byte, error) {
	if n <= len(r.buf) {
		b := r.buf[:n]
		r.buf = r.buf[n:]
		return b, nil
	}
	r.scratch = append(r.scratch[:0], r.buf...)
	r.buf = nil
	for len(r.scratch) < n {
		if err := r.packet(); err != nil {
			return nil, err
		}
		k := min(n-len(r.scratch), len(r.buf))
		r.scratch = append(r.scratch, r.buf[:k]...)
		r.buf = r.buf[k:]
	}
	return r.scratch, nil
}

// skip passes over n bytes.
func (r *msgReader) skip(n int) error {
	for n > 0 {
		if len(r.buf) == 0 {
			if err := r.packet(); err != nil {
				return err
			}
		}
		k := min(n, len(r.buf))
		r.buf = r.buf[k:]
		n -= k
	}
	return nil
}

func (r *msgReader) u8() (byte, error) {
	b, err := r.next(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *msgReader) u16() (uint16, error) {
	b, err := r.next(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

func (r *msgReader) u32() (uint32, error) {
	b, err := r.next(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (r *msgReader) u64() (uint64, error) {
	b, err := r.next(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

// bVarchar reads a B_VARCHAR: a byte's count of UCS-2 characters.
func (r *msgReader) bVarchar() (string, error) {
	n, err := r.u8()
	if err != nil {
		return "", err
	}
	b, err := r.next(int(n) * 2)
	if err != nil {
		return "", err
	}
	return ucs2String(b), nil
}

// usVarchar reads a US_VARCHAR: a uint16's count of UCS-2 characters.
func (r *msgReader) usVarchar() (string, error) {
	n, err := r.u16()
	if err != nil {
		return "", err
	}
	b, err := r.next(int(n) * 2)
	if err != nil {
		return "", err
	}
	return ucs2String(b), nil
}

// ucs2String decodes UTF-16LE.
func ucs2String(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// appendUCS2 appends s as UTF-16LE.
func appendUCS2(b []byte, s string) []byte {
	for _, r := range s {
		if r >= 0x10000 {
			r1, r2 := utf16.EncodeRune(r)
			b = binary.LittleEndian.AppendUint16(b, uint16(r1))
			b = binary.LittleEndian.AppendUint16(b, uint16(r2))
			continue
		}
		b = binary.LittleEndian.AppendUint16(b, uint16(r))
	}
	return b
}

// ucs2Len is how many UTF-16 code units s takes.
func ucs2Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// appendBVarchar appends a B_VARCHAR, s cut to 255 characters.
func appendBVarchar(b []byte, s string) []byte {
	u := appendUCS2(nil, s)
	if len(u) > 510 {
		u = u[:510]
	}
	b = append(b, byte(len(u)/2))
	return append(b, u...)
}

// appendUSVarchar appends a US_VARCHAR.
func appendUSVarchar(b []byte, s string) []byte {
	u := appendUCS2(nil, s)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(u)/2))
	return append(b, u...)
}

// readFull reads len(p) bytes from the connection, each read bounded by
// the call's deadline.
func (c *Conn) readFull(p []byte) error {
	c.nc.SetReadDeadline(c.readDeadline())
	if _, err := io.ReadFull(c.rd, p); err != nil {
		return c.fail(fmt.Errorf("mssql: reading from the server: %w", err))
	}
	return nil
}

// write sends b, bounded by the write timeout.
func (c *Conn) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.SetWriteDeadline(c.writeDeadline())
	if _, err := c.nc.Write(b); err != nil {
		return c.fail(fmt.Errorf("mssql: writing to the server: %w", err))
	}
	return nil
}
