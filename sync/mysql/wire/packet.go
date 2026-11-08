package mysqlwire

import (
	"encoding/binary"
	"fmt"
	"io"
)

// maxPayload is the largest payload of one packet on the wire; a longer
// one is sent as packets of this size followed by a shorter one, which may
// be empty.
const maxPayload = 1<<24 - 1

// keepBuffer is the largest reassembly or write buffer a connection keeps
// between packets; a larger one, grown for a large row or statement, is
// let go once it is done with.
const keepBuffer = 1 << 20

// readPacket reads one logical packet, reassembling the packets of a long
// one. The bytes are valid until the next read.
func (c *Conn) readPacket() ([]byte, error) {
	if cap(c.rbuf) > keepBuffer {
		c.rbuf = nil
	}
	c.rbuf = c.rbuf[:0]
	for {
		var h [4]byte
		if _, err := io.ReadFull(c.br, h[:]); err != nil {
			return nil, err
		}
		n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
		if h[3] != c.seq {
			return nil, fmt.Errorf("%w: packet %d, expected %d", ErrMalformed, h[3], c.seq)
		}
		c.seq++
		if len(c.rbuf)+n > c.maxPacket {
			return nil, fmt.Errorf("%w: a packet of %d bytes or more, past MaxPacketSize %d",
				ErrPacketTooLarge, len(c.rbuf)+n, c.maxPacket)
		}
		// A whole packet that fits the reader's buffer is handed out from
		// it, without a copy.
		if len(c.rbuf) == 0 && n < maxPayload && n <= c.br.Size() {
			b, err := c.br.Peek(n)
			if err != nil {
				return nil, noEOF(err)
			}
			_, _ = c.br.Discard(n)
			return b, nil
		}
		at := len(c.rbuf)
		c.rbuf = grow(c.rbuf, n)
		if _, err := io.ReadFull(c.br, c.rbuf[at:at+n]); err != nil {
			return nil, noEOF(err)
		}
		if n < maxPayload {
			return c.rbuf, nil
		}
	}
}

// noEOF turns an end of the stream inside a packet into an unexpected one.
func noEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// grow extends b by n bytes.
func grow(b []byte, n int) []byte {
	if len(b)+n <= cap(b) {
		return b[:len(b)+n]
	}
	nb := make([]byte, len(b)+n, 2*cap(b)+n)
	copy(nb, b)
	return nb
}

// startPacket starts a packet in the write buffer: four bytes for the
// header, filled in by writePacket.
func (c *Conn) startPacket() []byte {
	if cap(c.wbuf) > keepBuffer {
		c.wbuf = nil
	}
	return append(c.wbuf[:0], 0, 0, 0, 0)
}

// startCommand starts a command: a new sequence and the command's byte.
func (c *Conn) startCommand(cmd byte) []byte {
	c.seq = 0
	return append(c.startPacket(), cmd)
}

// writePacket writes b, made by startPacket, as one logical packet, split
// at maxPayload.
func (c *Conn) writePacket(b []byte) error {
	c.wbuf = b
	payload := len(b) - 4
	if payload < maxPayload {
		putHeader(b, payload, c.seq)
		c.seq++
		_, err := c.nc.Write(b)
		return err
	}
	data := b[4:]
	for {
		n := min(len(data), maxPayload)
		var h [4]byte
		putHeader(h[:], n, c.seq)
		c.seq++
		if _, err := c.nc.Write(h[:]); err != nil {
			return err
		}
		if _, err := c.nc.Write(data[:n]); err != nil {
			return err
		}
		data = data[n:]
		if n < maxPayload {
			return nil
		}
	}
}

func putHeader(h []byte, n int, seq uint8) {
	h[0], h[1], h[2], h[3] = byte(n), byte(n>>8), byte(n>>16), seq
}

// appendLenEnc appends n as a length-encoded integer.
func appendLenEnc(b []byte, n uint64) []byte {
	switch {
	case n < 251:
		return append(b, byte(n))
	case n < 1<<16:
		return append(b, 0xfc, byte(n), byte(n>>8))
	case n < 1<<24:
		return append(b, 0xfd, byte(n), byte(n>>8), byte(n>>16))
	default:
		b = append(b, 0xfe)
		return binary.LittleEndian.AppendUint64(b, n)
	}
}

// appendLenEncBytes appends s with its length before it.
func appendLenEncBytes[T string | []byte](b []byte, s T) []byte {
	return append(appendLenEnc(b, uint64(len(s))), s...)
}

// rd reads the fields of a packet; a read past its end marks it bad and
// returns zeros.
type rd struct {
	b   []byte
	bad bool
}

func (r *rd) take(n int) []byte {
	if n < 0 || n > len(r.b) {
		r.bad = true
		r.b = nil
		return nil
	}
	v := r.b[:n:n]
	r.b = r.b[n:]
	return v
}

func (r *rd) u8() byte {
	if b := r.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (r *rd) u16() uint16 {
	if b := r.take(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}
	return 0
}

func (r *rd) u24() uint32 {
	if b := r.take(3); b != nil {
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
	}
	return 0
}

func (r *rd) u32() uint32 {
	if b := r.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (r *rd) u64() uint64 {
	if b := r.take(8); b != nil {
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}

// lenEnc reads a length-encoded integer; null is its NULL marker, 0xfb.
func (r *rd) lenEnc() (n uint64, null bool) {
	switch c := r.u8(); c {
	case 0xfb:
		return 0, true
	case 0xfc:
		return uint64(r.u16()), false
	case 0xfd:
		return uint64(r.u24()), false
	case 0xfe:
		return r.u64(), false
	case 0xff:
		r.bad = true
		return 0, false
	default:
		return uint64(c), false
	}
}

// lenEncBytes reads a length-encoded string; nil for NULL.
func (r *rd) lenEncBytes() []byte {
	n, null := r.lenEnc()
	if null || r.bad {
		return nil
	}
	if n > uint64(len(r.b)) {
		r.bad = true
		return nil
	}
	v := r.take(int(n))
	if v == nil {
		v = []byte{}
	}
	return v
}

// nul reads a string ended by a NUL, or by the packet's end.
func (r *rd) nul() []byte {
	for i, c := range r.b {
		if c == 0 {
			v := r.b[:i:i]
			r.b = r.b[i+1:]
			return v
		}
	}
	v := r.b
	r.b = nil
	return v
}

func (r *rd) rest() []byte {
	v := r.b
	r.b = nil
	return v
}
