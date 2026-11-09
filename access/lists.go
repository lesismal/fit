package access

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/lesismal/fit/router"
)

// maxLineBytes bounds a line of a list file.
const maxLineBytes = 16 << 10

// DATBlockLevel is the highest level of an eMule ipfilter.dat line that is
// listed: a line of a higher level is left out, as qBittorrent leaves it
// out, and as eMule does at its default filter level.
const DATBlockLevel = 127

// Range is the addresses from Lo to Hi, both included: both of one family,
// IPv4 addresses never mapped into IPv6, and Lo not past Hi.
type Range struct {
	Lo, Hi netip.Addr
}

// RangeOf returns the addresses of p.
func RangeOf(p netip.Prefix) Range {
	p = p.Masked()
	bits := p.Bits()
	if p.Addr().Is4In6() {
		bits = max(bits-96, 0)
	}
	lo := p.Addr().Unmap()
	b := lo.AsSlice()
	for i := bits; i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	hi, _ := netip.AddrFromSlice(b)
	return Range{lo, hi}
}

// parseLine parses a line of a list file: false when it lists nothing (a
// blank line, a comment, or an ipfilter.dat line of a level above
// DATBlockLevel). It tells the formats apart by the line's content:
//
//	203.0.113.0/24 # a comment      an address or prefix, as before
//	203.0.113.1-203.0.113.9         a range ("first - last" too)
//	001.002.003.004 - 001.002.003.255 , 000 , Some name   ipfilter.dat
//	Some name:1.2.3.4-1.2.3.255     PeerGuardian .p2p
//
// The addresses of a range may have leading zeros in IPv4, as
// ipfilter.dat writes them.
func parseLine(line string) (Range, bool, error) {
	s := strings.TrimSpace(line)
	if s == "" || s[0] == '#' || s[0] == ';' || strings.HasPrefix(s, "//") {
		return Range{}, false, nil
	}
	// An address or a prefix, with a comment after it.
	plain := s
	if i := strings.IndexAny(plain, "#;"); i >= 0 {
		plain = strings.TrimSpace(plain[:i])
	}
	if p, err := router.ParsePrefix(plain); err == nil {
		return RangeOf(p), true, nil
	}
	// A bare range.
	if !strings.ContainsAny(plain, ",") && strings.Contains(plain, "-") {
		if r, err := parseRange(plain); err == nil {
			return r, true, nil
		}
	}
	var datErr error
	if fields := strings.SplitN(s, ",", 3); len(fields) >= 2 && strings.Contains(fields[0], "-") {
		r, err := parseRange(fields[0])
		if err == nil {
			level, lerr := strconv.Atoi(strings.TrimSpace(fields[1]))
			if lerr != nil || level < 0 || level > 255 {
				return Range{}, false, fmt.Errorf("%q: an ipfilter.dat line whose level %q is not 0 to 255", clip(s), strings.TrimSpace(fields[1]))
			}
			return r, level <= DATBlockLevel, nil
		}
		datErr = err
	}
	if i := strings.LastIndexByte(s, ':'); i > 0 {
		if r, err := parseRange(s[i+1:]); err == nil {
			return r, true, nil
		}
	}
	if datErr != nil {
		return Range{}, false, fmt.Errorf("%q: %v", clip(s), datErr)
	}
	return Range{}, false, fmt.Errorf("%q is not an IP address, a CIDR prefix, a range, an ipfilter.dat line or a .p2p line", clip(s))
}

// parseRange parses "first-last", spaces allowed around the dash.
func parseRange(s string) (Range, error) {
	// An IPv6 address has no '-', so the first one splits the range.
	i := strings.IndexByte(s, '-')
	if i < 0 {
		return Range{}, errors.New("no '-' between two addresses")
	}
	lo, err := parseAddr(strings.TrimSpace(s[:i]))
	if err != nil {
		return Range{}, err
	}
	hi, err := parseAddr(strings.TrimSpace(s[i+1:]))
	if err != nil {
		return Range{}, err
	}
	if lo.Is4() != hi.Is4() {
		return Range{}, fmt.Errorf("the range %s-%s mixes IPv4 and IPv6", lo, hi)
	}
	if hi.Less(lo) {
		return Range{}, fmt.Errorf("the range %s-%s ends before it begins", lo, hi)
	}
	return Range{lo, hi}, nil
}

// parseAddr parses an address: IPv4 with leading zeros allowed in each
// part, as ipfilter.dat writes them, or IPv6 without a zone.
func parseAddr(s string) (netip.Addr, error) {
	if strings.Contains(s, ":") {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			return netip.Addr{}, fmt.Errorf("%q is not an IP address", clip(s))
		}
		return a.Unmap(), nil
	}
	var b [4]byte
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return netip.Addr{}, fmt.Errorf("%q is not an IP address", clip(s))
	}
	for i, p := range parts {
		if p == "" || len(p) > 3 {
			return netip.Addr{}, fmt.Errorf("%q is not an IP address", clip(s))
		}
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return netip.Addr{}, fmt.Errorf("%q is not an IP address", clip(s))
			}
			n = n*10 + int(c-'0')
		}
		if n > 255 {
			return netip.Addr{}, fmt.Errorf("%q is not an IP address", clip(s))
		}
		b[i] = byte(n)
	}
	return netip.AddrFrom4(b), nil
}

// clip shortens s for an error.
func clip(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
