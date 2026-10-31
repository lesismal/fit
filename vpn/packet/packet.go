// Package packet reads the IP headers of the packets a VPN node moves
// between its TUN device and its peers, and routes them by the overlay's
// route table (decision 026). Neither allocates nor locks, so both can run
// for every packet on a fib worker.
package packet

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

// The sizes of the fixed headers.
const (
	ipv4HeaderSize = 20
	ipv6HeaderSize = 40
)

// Errors Parse returns.
var (
	ErrShort     = errors.New("packet: shorter than its header")
	ErrVersion   = errors.New("packet: not IPv4 or IPv6")
	ErrMalformed = errors.New("packet: malformed header")
)

// Header is what a node needs of a packet's IP header.
type Header struct {
	// Version is 4 or 6.
	Version uint8
	// Protocol is IPv4's protocol or IPv6's first next header: the
	// extension headers are not walked.
	Protocol uint8
	// Len is the packet's length by its header, which may be less than the
	// bytes Parse was given but never more.
	Len int
	// Src and Dst are the source and destination, IPv4 addresses for an
	// IPv4 packet and IPv6 ones for IPv6.
	Src, Dst netip.Addr
}

// Parse reads the header of the IP packet in b. It refuses a packet whose
// header is cut short or inconsistent, or whose length by its header is more
// than b holds: what a TUN device should never hand over. Checksums are not
// checked; the kernel made them, or the peer's QUIC connection carried them.
func Parse(b []byte) (Header, error) {
	if len(b) == 0 {
		return Header{}, ErrShort
	}
	switch b[0] >> 4 {
	case 4:
		return parse4(b)
	case 6:
		return parse6(b)
	}
	return Header{}, ErrVersion
}

func parse4(b []byte) (Header, error) {
	if len(b) < ipv4HeaderSize {
		return Header{}, ErrShort
	}
	ihl := int(b[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(b[2:4]))
	if ihl < ipv4HeaderSize || total < ihl || total > len(b) {
		return Header{}, ErrMalformed
	}
	return Header{
		Version:  4,
		Protocol: b[9],
		Len:      total,
		Src:      netip.AddrFrom4([4]byte(b[12:16])),
		Dst:      netip.AddrFrom4([4]byte(b[16:20])),
	}, nil
}

func parse6(b []byte) (Header, error) {
	if len(b) < ipv6HeaderSize {
		return Header{}, ErrShort
	}
	payload := int(binary.BigEndian.Uint16(b[4:6]))
	// A payload length of zero with more behind the header is a jumbogram
	// (RFC 2675), which no TUN MTU allows.
	if ipv6HeaderSize+payload > len(b) || payload == 0 && len(b) > ipv6HeaderSize {
		return Header{}, ErrMalformed
	}
	return Header{
		Version:  6,
		Protocol: b[6],
		Len:      ipv6HeaderSize + payload,
		Src:      netip.AddrFrom16([16]byte(b[8:24])),
		Dst:      netip.AddrFrom16([16]byte(b[24:40])),
	}, nil
}
