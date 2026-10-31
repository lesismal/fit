// Package stun is STUN (RFC 8489): its messages, parsed without allocating
// and built by appending, and a server that answers Binding requests on a
// fib engine. ICE and TURN, in p2p/ice and p2p/turn, build on it.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

// magicCookie is the fixed value in every message's header (RFC 8489
// section 5), which tells STUN from the protocols sharing its port.
const magicCookie = 0x2112A442

// headerSize is the size of a message's header, and so of the smallest
// message.
const headerSize = 20

// MaxAttrs is the most attributes a message may carry; Parse refuses one
// with more. Real messages carry a dozen at most.
const MaxAttrs = 64

// Errors Parse and the attribute getters return.
var (
	ErrNotSTUN      = errors.New("stun: not a STUN message")
	ErrMalformed    = errors.New("stun: malformed message")
	ErrTooManyAttrs = errors.New("stun: too many attributes")
	ErrNoAttr       = errors.New("stun: attribute not present")
	ErrBadAttr      = errors.New("stun: malformed attribute")
	ErrIntegrity    = errors.New("stun: message integrity check failed")
	ErrFingerprint  = errors.New("stun: fingerprint check failed")
)

// Method is a message's method: what a request asks for.
type Method uint16

// The methods of STUN (RFC 8489) and TURN (RFC 8656).
const (
	MethodBinding          Method = 0x001
	MethodAllocate         Method = 0x003
	MethodRefresh          Method = 0x004
	MethodSend             Method = 0x006
	MethodData             Method = 0x007
	MethodCreatePermission Method = 0x008
	MethodChannelBind      Method = 0x009
)

// Class is a message's class: a request, an indication, or one of the two
// kinds of response.
type Class uint8

// The four classes.
const (
	ClassRequest    Class = 0
	ClassIndication Class = 1
	ClassSuccess    Class = 2
	ClassError      Class = 3
)

// Type is a message's type: its method and class, which the header
// interleaves into 14 bits.
type Type struct {
	Method Method
	Class  Class
}

// value is the type as the header carries it: M11-M7, C1, M6-M4, C0,
// M3-M0.
func (t Type) value() uint16 {
	m, c := uint16(t.Method), uint16(t.Class)
	return m&0x000f | (c&1)<<4 | (m&0x0070)<<1 | (c&2)<<7 | (m&0x0f80)<<2
}

func typeOf(v uint16) Type {
	return Type{
		Method: Method(v&0x000f | (v>>1)&0x0070 | (v>>2)&0x0f80),
		Class:  Class((v>>4)&1 | (v>>7)&2),
	}
}

// TransactionID ties a response to its request.
type TransactionID [12]byte

// NewTransactionID returns a random transaction ID, as RFC 8489 section 5
// asks: one an attacker cannot guess.
func NewTransactionID() TransactionID {
	var id TransactionID
	_, _ = rand.Read(id[:])
	return id
}

// Attr is one attribute of a message: its type, and where its value lies in
// the message's Raw bytes.
type Attr struct {
	Type AttrType
	off  uint16
	n    uint16
}

// Message is a STUN message. Parse fills it from bytes it then refers to;
// Build and the Add methods write it into Raw, which it owns. A Message is
// reused from one message to the next, keeping its buffers, so neither
// parsing nor building allocates once it has grown.
type Message struct {
	Type Type
	ID   TransactionID
	// Raw is the whole message.
	Raw []byte
	// Attrs are the message's attributes in order, as far as they count:
	// past MESSAGE-INTEGRITY only MESSAGE-INTEGRITY-SHA256 and FINGERPRINT
	// do (RFC 8489 section 14.5), and the rest are left out.
	Attrs []Attr
	// buf is the buffer Build writes into, kept apart from Raw, which may
	// be bytes Parse was given.
	buf []byte
}

// IsMessage reports whether b looks like a STUN message: its first two bits
// are zero, as RFC 7983 tells STUN from DTLS, TURN channels and QUIC, and it
// carries the magic cookie. It does not check the rest.
func IsMessage(b []byte) bool {
	return len(b) >= headerSize && b[0]&0xc0 == 0 && binary.BigEndian.Uint32(b[4:]) == magicCookie
}

// MessageLen returns the length of the message b starts with, header
// included, from its header alone, for reading messages out of a stream as
// STUN over TCP needs. It returns 0 while b holds less than a header, and -1
// when b does not start a STUN message.
func MessageLen(b []byte) int {
	if len(b) < headerSize {
		return 0
	}
	if !IsMessage(b) {
		return -1
	}
	n := int(binary.BigEndian.Uint16(b[2:]))
	if n%4 != 0 {
		return -1
	}
	return headerSize + n
}

// Parse makes m the message b holds, which must be all of b. m refers to b
// from then on, and its Attrs to b's bytes. Parse checks the framing only:
// integrity and fingerprints are checked by CheckIntegrity and
// CheckFingerprint.
func (m *Message) Parse(b []byte) error {
	m.Raw, m.Attrs = nil, m.Attrs[:0]
	if !IsMessage(b) {
		return ErrNotSTUN
	}
	if MessageLen(b) != len(b) {
		return ErrMalformed
	}
	m.Type = typeOf(binary.BigEndian.Uint16(b))
	copy(m.ID[:], b[8:headerSize])
	sawIntegrity := false
	for off := headerSize; off < len(b); {
		if len(b)-off < 4 {
			return ErrMalformed
		}
		t := AttrType(binary.BigEndian.Uint16(b[off:]))
		n := int(binary.BigEndian.Uint16(b[off+2:]))
		end := off + 4 + n
		if end > len(b) {
			return ErrMalformed
		}
		padded := off + 4 + (n+3)&^3
		if padded > len(b) {
			return ErrMalformed
		}
		switch {
		case t == AttrFingerprint:
			if padded != len(b) || n != 4 {
				// FINGERPRINT is the last attribute, always.
				return ErrMalformed
			}
		case sawIntegrity && t != AttrMessageIntegritySHA256:
			off = padded
			continue
		}
		if t == AttrMessageIntegrity || t == AttrMessageIntegritySHA256 {
			sawIntegrity = true
		}
		if len(m.Attrs) == MaxAttrs {
			return ErrTooManyAttrs
		}
		m.Attrs = append(m.Attrs, Attr{Type: t, off: uint16(off + 4), n: uint16(n)})
		off = padded
	}
	m.Raw = b
	return nil
}

// Build starts m as a message of type t with transaction id, with no
// attributes yet, reusing m's buffer.
func (m *Message) Build(t Type, id TransactionID) {
	m.Type, m.ID, m.Attrs = t, id, m.Attrs[:0]
	m.Raw = binary.BigEndian.AppendUint16(m.buf[:0], t.value())
	m.Raw = append(m.Raw, 0, 0)
	m.Raw = binary.BigEndian.AppendUint32(m.Raw, magicCookie)
	m.Raw = append(m.Raw, id[:]...)
	m.buf = m.Raw
}

// Add appends an attribute of type t with value v.
func (m *Message) Add(t AttrType, v []byte) {
	m.Raw = append(m.grow(t, len(v)), v...)
	m.pad()
}

// AddString appends an attribute of type t with value v.
func (m *Message) AddString(t AttrType, v string) {
	m.Raw = append(m.grow(t, len(v)), v...)
	m.pad()
}

// grow appends the header of an attribute of type t with a value n long,
// records it, and returns Raw for the value to be appended to.
func (m *Message) grow(t AttrType, n int) []byte {
	off := len(m.Raw)
	m.Raw = binary.BigEndian.AppendUint16(m.Raw, uint16(t))
	m.Raw = binary.BigEndian.AppendUint16(m.Raw, uint16(n))
	m.Attrs = append(m.Attrs, Attr{Type: t, off: uint16(off + 4), n: uint16(n)})
	return m.Raw
}

// pad pads the last attribute to a multiple of 4 bytes and sets the
// header's length.
func (m *Message) pad() {
	for len(m.Raw)%4 != 0 {
		m.Raw = append(m.Raw, 0)
	}
	m.buf = m.Raw
	m.setLength(len(m.Raw))
}

// setLength sets the header's length to say the message ends at end.
func (m *Message) setLength(end int) {
	binary.BigEndian.PutUint16(m.Raw[2:], uint16(end-headerSize))
}

// Value returns a's value.
func (m *Message) Value(a Attr) []byte { return m.Raw[a.off : int(a.off)+int(a.n)] }

// Get returns the value of the first attribute of type t.
func (m *Message) Get(t AttrType) ([]byte, bool) {
	for _, a := range m.Attrs {
		if a.Type == t {
			return m.Value(a), true
		}
	}
	return nil, false
}

// Has reports whether m has an attribute of type t.
func (m *Message) Has(t AttrType) bool {
	_, ok := m.Get(t)
	return ok
}

// UnknownRequired appends to dst the types of m's comprehension-required
// attributes (0x0000-0x7FFF) that known does not know, and returns it: a
// request with any is answered with a 420 that lists them (RFC 8489 section
// 6.3.1), a response with any is dropped.
func (m *Message) UnknownRequired(dst []AttrType, known func(AttrType) bool) []AttrType {
	for _, a := range m.Attrs {
		if a.Type.Required() && !known(a.Type) {
			dst = append(dst, a.Type)
		}
	}
	return dst
}
