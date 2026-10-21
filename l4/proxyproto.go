package l4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"strconv"
	"time"

	fib "github.com/lesismal/fib"
)

// The PROXY protocol (haproxy.org's proxy-protocol.txt) puts a header in
// front of a connection's data that names the client a proxy took it from.

// MaxProxyHeader bounds the PROXY header a listener reads: a v1 line is at
// most 107 bytes, and a v2 header's TLVs are left at what a proxy sends in
// practice.
const MaxProxyHeader = 4096

var (
	proxyV1Prefix = []byte("PROXY ")
	proxyV2Sig    = []byte("\r\n\r\n\x00\r\nQUIT\n")

	errProxyHeader = errors.New("l4: not a PROXY protocol header")
)

// parseProxy reads the PROXY header, of either version, data begins with.
// It returns the header's length and the source and destination it names,
// which are not valid for a header that names none (v1's UNKNOWN, v2's
// LOCAL or an unspecified family); a length of 0 with no error says more
// data is needed.
func parseProxy(data []byte) (n int, src, dst netip.AddrPort, err error) {
	switch {
	case len(data) == 0:
		return 0, src, dst, nil
	case data[0] == proxyV1Prefix[0]:
		return parseProxyV1(data)
	case data[0] == proxyV2Sig[0]:
		return parseProxyV2(data)
	}
	return 0, src, dst, errProxyHeader
}

func parseProxyV1(data []byte) (n int, src, dst netip.AddrPort, err error) {
	const maxLine = 107
	if k := min(len(data), len(proxyV1Prefix)); !bytes.Equal(data[:k], proxyV1Prefix[:k]) {
		return 0, src, dst, errProxyHeader
	}
	end := bytes.Index(data[:min(len(data), maxLine)], []byte("\r\n"))
	if end < 0 {
		if len(data) >= maxLine {
			return 0, src, dst, errProxyHeader
		}
		return 0, src, dst, nil
	}
	fields := bytes.Split(data[len(proxyV1Prefix):end], []byte(" "))
	if string(fields[0]) == "UNKNOWN" {
		return end + 2, src, dst, nil
	}
	if len(fields) != 5 || string(fields[0]) != "TCP4" && string(fields[0]) != "TCP6" {
		return 0, src, dst, errProxyHeader
	}
	addr := func(ip, port []byte) (netip.AddrPort, bool) {
		a, err := netip.ParseAddr(string(ip))
		if err != nil || a.Is4() != (string(fields[0]) == "TCP4") || a.Zone() != "" {
			return netip.AddrPort{}, false
		}
		p, err := strconv.ParseUint(string(port), 10, 16)
		return netip.AddrPortFrom(a, uint16(p)), err == nil
	}
	src, ok1 := addr(fields[1], fields[3])
	dst, ok2 := addr(fields[2], fields[4])
	if !ok1 || !ok2 {
		return 0, netip.AddrPort{}, netip.AddrPort{}, errProxyHeader
	}
	return end + 2, src, dst, nil
}

func parseProxyV2(data []byte) (n int, src, dst netip.AddrPort, err error) {
	if k := min(len(data), len(proxyV2Sig)); !bytes.Equal(data[:k], proxyV2Sig[:k]) {
		return 0, src, dst, errProxyHeader
	}
	if len(data) < 16 {
		return 0, src, dst, nil
	}
	verCmd, family := data[12], data[13]
	size := 16 + int(binary.BigEndian.Uint16(data[14:16]))
	switch {
	case verCmd>>4 != 2 || verCmd&0xf > 1 || size > MaxProxyHeader:
		return 0, src, dst, errProxyHeader
	case len(data) < size:
		return 0, src, dst, nil
	case verCmd&0xf == 0:
		// LOCAL: the proxy's own connection, a health check say.
		return size, src, dst, nil
	}
	body := data[16:size]
	switch family {
	case 0x11, 0x12: // TCP and UDP over IPv4
		if len(body) < 12 {
			return 0, src, dst, errProxyHeader
		}
		src = netip.AddrPortFrom(netip.AddrFrom4([4]byte(body[0:4])), binary.BigEndian.Uint16(body[8:10]))
		dst = netip.AddrPortFrom(netip.AddrFrom4([4]byte(body[4:8])), binary.BigEndian.Uint16(body[10:12]))
	case 0x21, 0x22: // over IPv6
		if len(body) < 36 {
			return 0, src, dst, errProxyHeader
		}
		src = netip.AddrPortFrom(netip.AddrFrom16([16]byte(body[0:16])).Unmap(), binary.BigEndian.Uint16(body[32:34]))
		dst = netip.AddrPortFrom(netip.AddrFrom16([16]byte(body[16:32])).Unmap(), binary.BigEndian.Uint16(body[34:36]))
	}
	// Any other family, unspecified or Unix, names no address fit can use.
	return size, src, dst, nil
}

// appendProxy appends a PROXY header of version 1 or 2 for a connection from
// src to dst, which names neither when they are not both valid IP
// addresses of one family.
func appendProxy(b []byte, version int, src, dst netip.AddrPort) []byte {
	src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
	dst = netip.AddrPortFrom(dst.Addr().Unmap(), dst.Port())
	known := src.IsValid() && dst.IsValid() && src.Addr().Is4() == dst.Addr().Is4()
	if version == 1 {
		if !known {
			return append(b, "PROXY UNKNOWN\r\n"...)
		}
		family := "TCP6 "
		if src.Addr().Is4() {
			family = "TCP4 "
		}
		b = append(b, "PROXY "...)
		b = append(b, family...)
		b = src.Addr().AppendTo(b)
		b = append(b, ' ')
		b = dst.Addr().AppendTo(b)
		b = append(b, ' ')
		b = strconv.AppendUint(b, uint64(src.Port()), 10)
		b = append(b, ' ')
		b = strconv.AppendUint(b, uint64(dst.Port()), 10)
		return append(b, "\r\n"...)
	}
	b = append(b, proxyV2Sig...)
	switch {
	case !known:
		// PROXY with an unspecified family: the upstream keeps the
		// connection's own addresses.
		return append(b, 0x21, 0x00, 0, 0)
	case src.Addr().Is4():
		b = append(b, 0x21, 0x11, 0, 12)
		b = append(b, src.Addr().AsSlice()...)
		b = append(b, dst.Addr().AsSlice()...)
	default:
		b = append(b, 0x21, 0x21, 0, 36)
		b = append(b, src.Addr().AsSlice()...)
		b = append(b, dst.Addr().AsSlice()...)
	}
	b = binary.BigEndian.AppendUint16(b, src.Port())
	return binary.BigEndian.AppendUint16(b, dst.Port())
}

// Trusted are the addresses a listener takes PROXY headers from.
type Trusted []netip.Prefix

// Contains reports whether addr is one of t's.
func (t Trusted) Contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range t {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// AcceptProxy returns a handler that reads the PROXY header of each
// connection from a trusted address, and makes the client it names the
// connection's peer, before h takes the connection; a connection from
// elsewhere goes to h as it is. The header must come within OpeningTimeout
// and MaxProxyHeader bytes, or the connection is closed. It is for a
// listener of HTTP, whose header comes before its TLS.
func AcceptProxy(trusted Trusted, h fib.Handler) fib.Handler {
	return &proxyHandler{trusted: trusted, h: h}
}

type proxyHandler struct {
	trusted Trusted
	h       fib.Handler
}

// proxyRead is a connection whose PROXY header is still being read.
type proxyRead struct{ buf []byte }

func (p *proxyHandler) OnOpen(c *fib.Connection) {
	if !p.trusted.Contains(c.RemoteAddrPort().Addr()) {
		p.h.OnOpen(c)
		return
	}
	c.SetAttachment(&proxyRead{})
	_ = c.SetReadDeadline(time.Now().Add(OpeningTimeout))
}

func (p *proxyHandler) OnData(c *fib.Connection, data []byte) {
	r, ok := c.Attachment().(*proxyRead)
	if !ok {
		p.h.OnData(c, data)
		return
	}
	r.buf = append(r.buf, data...)
	n, src, _, err := parseProxy(r.buf)
	switch {
	case err != nil || n == 0 && len(r.buf) >= MaxProxyHeader:
		_ = c.Close()
		return
	case n == 0:
		return
	}
	if src.IsValid() {
		c.SetRemoteAddrPort(src)
	}
	rest := r.buf[n:]
	c.SetAttachment(nil)
	_ = c.SetReadDeadline(time.Time{})
	p.h.OnOpen(c)
	if len(rest) > 0 {
		p.h.OnData(c, rest)
	}
}

func (p *proxyHandler) OnPriorityData(c *fib.Connection, data []byte) {
	if _, ok := c.Attachment().(*proxyRead); !ok {
		p.h.OnPriorityData(c, data)
	}
}

func (p *proxyHandler) OnClose(c *fib.Connection, err error) {
	if _, ok := c.Attachment().(*proxyRead); !ok {
		p.h.OnClose(c, err)
	}
}

func (p *proxyHandler) OnHangup(c *fib.Connection) {
	if h, ok := p.h.(fib.HangupHandler); ok {
		if _, reading := c.Attachment().(*proxyRead); !reading {
			h.OnHangup(c)
		}
	}
}

// OnShutdown closes a connection whose header has not come yet, which h
// has not heard of.
func (p *proxyHandler) OnShutdown(c *fib.Connection) {
	if _, reading := c.Attachment().(*proxyRead); reading {
		_ = c.Close()
		return
	}
	if h, ok := p.h.(fib.ShutdownHandler); ok {
		h.OnShutdown(c)
	}
}
