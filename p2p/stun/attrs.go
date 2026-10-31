package stun

import (
	"encoding/binary"
	"net/netip"
)

// AttrType is an attribute's type.
type AttrType uint16

// Attribute types of STUN (RFC 8489), TURN (RFC 8656), ICE (RFC 8445) and
// NAT behaviour discovery (RFC 5780).
const (
	AttrMappedAddress           AttrType = 0x0001
	AttrChangeRequest           AttrType = 0x0003
	AttrUsername                AttrType = 0x0006
	AttrMessageIntegrity        AttrType = 0x0008
	AttrErrorCode               AttrType = 0x0009
	AttrUnknownAttributes       AttrType = 0x000A
	AttrChannelNumber           AttrType = 0x000C
	AttrLifetime                AttrType = 0x000D
	AttrXORPeerAddress          AttrType = 0x0012
	AttrData                    AttrType = 0x0013
	AttrRealm                   AttrType = 0x0014
	AttrNonce                   AttrType = 0x0015
	AttrXORRelayedAddress       AttrType = 0x0016
	AttrRequestedAddressFamily  AttrType = 0x0017
	AttrEvenPort                AttrType = 0x0018
	AttrRequestedTransport      AttrType = 0x0019
	AttrDontFragment            AttrType = 0x001A
	AttrMessageIntegritySHA256  AttrType = 0x001C
	AttrPasswordAlgorithm       AttrType = 0x001D
	AttrUserhash                AttrType = 0x001E
	AttrXORMappedAddress        AttrType = 0x0020
	AttrReservationToken        AttrType = 0x0022
	AttrPriority                AttrType = 0x0024
	AttrUseCandidate            AttrType = 0x0025
	AttrPadding                 AttrType = 0x0026
	AttrResponsePort            AttrType = 0x0027
	AttrAdditionalAddressFamily AttrType = 0x8000
	AttrAddressErrorCode        AttrType = 0x8001
	AttrPasswordAlgorithms      AttrType = 0x8002
	AttrAlternateDomain         AttrType = 0x8003
	AttrICMP                    AttrType = 0x8004
	AttrSoftware                AttrType = 0x8022
	AttrAlternateServer         AttrType = 0x8023
	AttrFingerprint             AttrType = 0x8028
	AttrICEControlled           AttrType = 0x8029
	AttrICEControlling          AttrType = 0x802A
	AttrResponseOrigin          AttrType = 0x802B
	AttrOtherAddress            AttrType = 0x802C
)

// Required reports whether an agent that does not know t must refuse the
// message: types below 0x8000 are comprehension-required.
func (t AttrType) Required() bool { return t < 0x8000 }

// Address families in address attributes.
const (
	familyIPv4 = 0x01
	familyIPv6 = 0x02
)

// AddAddr appends an address attribute of type t, as MAPPED-ADDRESS,
// ALTERNATE-SERVER and OTHER-ADDRESS carry: in the clear.
func (m *Message) AddAddr(t AttrType, a netip.AddrPort) { m.addAddr(t, a, false) }

// AddXORAddr appends an address attribute of type t XORed with the magic
// cookie and the transaction ID, as XOR-MAPPED-ADDRESS, XOR-PEER-ADDRESS and
// XOR-RELAYED-ADDRESS carry it, so that NATs rewriting addresses they find
// in payloads leave it alone.
func (m *Message) AddXORAddr(t AttrType, a netip.AddrPort) { m.addAddr(t, a, true) }

func (m *Message) addAddr(t AttrType, a netip.AddrPort, xor bool) {
	ip := a.Addr().Unmap()
	n, family := 4, byte(familyIPv4)
	if ip.Is6() {
		n, family = 16, familyIPv6
	}
	m.Raw = append(m.grow(t, 4+n), 0, family)
	port := a.Port()
	if xor {
		port ^= magicCookie >> 16
	}
	m.Raw = binary.BigEndian.AppendUint16(m.Raw, port)
	start := len(m.Raw)
	if ip.Is6() {
		b := ip.As16()
		m.Raw = append(m.Raw, b[:]...)
	} else {
		b := ip.As4()
		m.Raw = append(m.Raw, b[:]...)
	}
	if xor {
		m.xor(m.Raw[start:])
	}
	m.pad()
}

// xor XORs an address with the magic cookie followed by the transaction
// ID: the cookie alone for IPv4.
func (m *Message) xor(b []byte) {
	var key [16]byte
	binary.BigEndian.PutUint32(key[:], magicCookie)
	copy(key[4:], m.ID[:])
	for i := range b {
		b[i] ^= key[i]
	}
}

// Addr returns the address in m's attribute of type t, carried in the
// clear.
func (m *Message) Addr(t AttrType) (netip.AddrPort, error) { return m.addr(t, false) }

// XORAddr returns the address in m's attribute of type t, carried XORed.
func (m *Message) XORAddr(t AttrType) (netip.AddrPort, error) { return m.addr(t, true) }

func (m *Message) addr(t AttrType, xor bool) (netip.AddrPort, error) {
	v, ok := m.Get(t)
	if !ok {
		return netip.AddrPort{}, ErrNoAttr
	}
	if len(v) < 4 {
		return netip.AddrPort{}, ErrBadAttr
	}
	port := binary.BigEndian.Uint16(v[2:])
	if xor {
		port ^= magicCookie >> 16
	}
	var b [16]byte
	switch {
	case v[1] == familyIPv4 && len(v) == 8:
		copy(b[:], v[4:])
		if xor {
			m.xor(b[:4])
		}
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[:4])), port), nil
	case v[1] == familyIPv6 && len(v) == 20:
		copy(b[:], v[4:])
		if xor {
			m.xor(b[:])
		}
		return netip.AddrPortFrom(netip.AddrFrom16(b), port), nil
	}
	return netip.AddrPort{}, ErrBadAttr
}

// Error codes of STUN and TURN.
const (
	CodeTryAlternate                 = 300
	CodeBadRequest                   = 400
	CodeUnauthorized                 = 401
	CodeForbidden                    = 403
	CodeMobilityForbidden            = 405
	CodeUnknownAttribute             = 420
	CodeAllocationMismatch           = 437
	CodeStaleNonce                   = 438
	CodeAddressFamilyNotSupported    = 440
	CodeWrongCredentials             = 441
	CodeUnsupportedTransportProtocol = 442
	CodePeerAddressFamilyMismatch    = 443
	CodeAllocationQuotaReached       = 486
	CodeRoleConflict                 = 487
	CodeServerError                  = 500
	CodeInsufficientCapacity         = 508
)

// maxReason is the longest reason phrase ERROR-CODE carries: 763 bytes,
// for 128 characters of UTF-8.
const maxReason = 763

// AddErrorCode appends ERROR-CODE with code, from 300 to 699, and reason,
// cut to 763 bytes.
func (m *Message) AddErrorCode(code int, reason string) {
	if len(reason) > maxReason {
		reason = reason[:maxReason]
	}
	m.Raw = append(m.grow(AttrErrorCode, 4+len(reason)), 0, 0, byte(code/100), byte(code%100))
	m.Raw = append(m.Raw, reason...)
	m.pad()
}

// ErrorCode returns the code and reason phrase of m's ERROR-CODE.
func (m *Message) ErrorCode() (int, []byte, error) {
	v, ok := m.Get(AttrErrorCode)
	if !ok {
		return 0, nil, ErrNoAttr
	}
	if len(v) < 4 || v[2]&7 < 3 || v[2]&7 > 6 || v[3] > 99 {
		return 0, nil, ErrBadAttr
	}
	return int(v[2]&7)*100 + int(v[3]), v[4:], nil
}

// AddUnknownAttributes appends UNKNOWN-ATTRIBUTES listing types.
func (m *Message) AddUnknownAttributes(types []AttrType) {
	m.Raw = m.grow(AttrUnknownAttributes, 2*len(types))
	for _, t := range types {
		m.Raw = binary.BigEndian.AppendUint16(m.Raw, uint16(t))
	}
	m.pad()
}

// UnknownAttributes appends the types m's UNKNOWN-ATTRIBUTES lists to dst
// and returns it.
func (m *Message) UnknownAttributes(dst []AttrType) ([]AttrType, error) {
	v, ok := m.Get(AttrUnknownAttributes)
	if !ok {
		return dst, ErrNoAttr
	}
	if len(v)%2 != 0 {
		return dst, ErrBadAttr
	}
	for i := 0; i < len(v); i += 2 {
		dst = append(dst, AttrType(binary.BigEndian.Uint16(v[i:])))
	}
	return dst, nil
}

// AddUint32 appends an attribute of type t holding v, as PRIORITY and
// LIFETIME do.
func (m *Message) AddUint32(t AttrType, v uint32) {
	m.Raw = binary.BigEndian.AppendUint32(m.grow(t, 4), v)
	m.pad()
}

// Uint32 returns the value of m's attribute of type t, which holds 4 bytes.
func (m *Message) Uint32(t AttrType) (uint32, error) {
	v, ok := m.Get(t)
	if !ok {
		return 0, ErrNoAttr
	}
	if len(v) != 4 {
		return 0, ErrBadAttr
	}
	return binary.BigEndian.Uint32(v), nil
}

// AddUint64 appends an attribute of type t holding v, as ICE-CONTROLLED
// and ICE-CONTROLLING do.
func (m *Message) AddUint64(t AttrType, v uint64) {
	m.Raw = binary.BigEndian.AppendUint64(m.grow(t, 8), v)
	m.pad()
}

// Uint64 returns the value of m's attribute of type t, which holds 8 bytes.
func (m *Message) Uint64(t AttrType) (uint64, error) {
	v, ok := m.Get(t)
	if !ok {
		return 0, ErrNoAttr
	}
	if len(v) != 8 {
		return 0, ErrBadAttr
	}
	return binary.BigEndian.Uint64(v), nil
}
