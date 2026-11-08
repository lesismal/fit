// Package access decides which client addresses fit serves: allow lists
// (whitelists) and deny lists (blacklists) of IP addresses and CIDR
// prefixes, set for the whole config and for each listener, route, service
// and role, where the most specific one that has a say decides.
//
// A List is one place's allow and deny entries, compiled into sorted ranges
// that an address is looked up in by binary search, so that a list of
// thousands of prefixes costs a lookup a few comparisons. A Policy is the
// lists that apply somewhere, the most specific first: a route's, then its
// listener's, then the global one. Each list in turn may decide:
//
//   - an address its deny holds is refused;
//   - else an address its allow holds is served;
//   - else, when its allow has entries, the address is refused, since an
//     allow list serves only what it holds;
//   - else the list has no say, and the next one decides.
//
// An address no list has a say on is served. A more specific list therefore
// overrides a less specific one both ways: a route's allow serves an address
// the global deny refuses, and a route's deny refuses one the global allow
// serves.
//
// List files (allow_files, deny_files) hold addresses and prefixes, ranges,
// eMule ipfilter.dat lines and PeerGuardian .p2p lines, told apart line by
// line (see ReadFile), for every module that has an access.
//
// A Gate holds the Policy a long-lived socket goes by, swapped atomically
// when the config is applied, so that the accept path reads it with one load.
package access

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/router"
)

// MaxFileBytes bounds a list file, so that a mistaken path cannot have fit
// read a huge file into memory: 64 MiB holds millions of entries.
const MaxFileBytes = 64 << 20

// List is one place's allow and deny entries.
type List struct {
	allow, deny set
}

// verdict is what a list says of an address.
type verdict int8

const (
	noSay verdict = iota
	allowed
	denied
)

func (l *List) verdict(a netip.Addr) verdict {
	switch {
	case l.deny.contains(a):
		return denied
	case l.allow.contains(a):
		return allowed
	case !l.allow.empty():
		return denied
	}
	return noSay
}

// Empty reports whether l has no entries, and so never has a say.
func (l *List) Empty() bool { return l == nil || l.allow.empty() && l.deny.empty() }

// Compile compiles a, reading its files; it returns nil for a nil or empty
// a. files, when not nil, is given the SHA-256 of each file read, by path,
// for the config to be applied again when one changes.
func Compile(a *config.Access, files map[string][sha256.Size]byte) (*List, error) {
	if a == nil {
		return nil, nil
	}
	var errs config.Errors
	var allow, deny []Range
	parse := func(entries []string, at config.Value, into *[]Range) {
		for i, s := range entries {
			p, err := router.ParsePrefix(strings.TrimSpace(s))
			if err != nil {
				errs = append(errs, at.Index(i).Errorf("%v", err))
				continue
			}
			*into = append(*into, RangeOf(p))
		}
	}
	read := func(paths []string, at config.Value, into *[]Range) {
		for i, path := range paths {
			ps, sum, err := ReadFile(path)
			if err != nil {
				errs = append(errs, at.Index(i).Errorf("%v", err))
				continue
			}
			if files != nil {
				files[path] = sum
			}
			*into = append(*into, ps...)
		}
	}
	parse(a.Allow, a.At.Field("allow"), &allow)
	parse(a.Deny, a.At.Field("deny"), &deny)
	read(a.AllowFiles, a.At.Field("allow_files"), &allow)
	read(a.DenyFiles, a.At.Field("deny_files"), &deny)
	if len(errs) > 0 {
		return nil, errs
	}
	l := &List{allow: newSet(allow), deny: newSet(deny)}
	if l.Empty() {
		return nil, nil
	}
	return l, nil
}

// Check compiles a and returns why it does not compile, reading its files:
// what a role's app.Info.Check runs on its own access.
func Check(a *config.Access) error {
	_, err := Compile(a, nil)
	return err
}

// Files returns a's list files, allow_files then deny_files: what a role's
// app.Info.CertFiles adds, for the config to be applied again when one
// changes. nil for a nil a.
func Files(a *config.Access) []string {
	if a == nil {
		return nil
	}
	return append(slices.Clone(a.AllowFiles), a.DenyFiles...)
}

// ReadFile reads a list file, at most MaxFileBytes, and returns the
// ranges it lists and the file's SHA-256. Each line is one of (see
// parseLine, which tells them apart by the line's content, so that one file
// may mix them):
//
//   - an IP address or CIDR prefix, with what follows a '#' or ';' left
//     out, as the lists published for firewalls are written;
//   - a range, "first-last" or "first - last";
//   - an eMule ipfilter.dat line, "first - last , level , name": a line
//     whose level is above 127 is not listed, as qBittorrent and eMule
//     (at its default filter level) leave it out;
//   - a PeerGuardian .p2p line, "name:first-last".
//
// Blank lines and lines that begin with '#', ';' or "//" are left out. A
// line that is none of these makes the file an error, naming the line.
func ReadFile(path string) ([]Range, [sha256.Size]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > MaxFileBytes {
		return nil, [sha256.Size]byte{}, fmt.Errorf("%s: larger than %d bytes", path, MaxFileBytes)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(f, MaxFileBytes+1)); err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	if buf.Len() > MaxFileBytes {
		return nil, [sha256.Size]byte{}, fmt.Errorf("%s: larger than %d bytes", path, MaxFileBytes)
	}
	sum := sha256.Sum256(buf.Bytes())
	rs, err := parseList(&buf, path)
	return rs, sum, err
}

// parseList parses the lines of a list file named name.
func parseList(r io.Reader, name string) ([]Range, error) {
	var rs []Range
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), maxLineBytes)
	for n := 1; sc.Scan(); n++ {
		rg, ok, err := parseLine(sc.Text())
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", name, n, err)
		}
		if ok {
			rs = append(rs, rg)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	return rs, nil
}

// Policy is the lists that apply somewhere, the most specific first. A nil
// Policy serves every address.
type Policy struct {
	lists []*List
}

// Chain returns the policy of lists, the most specific first; nil lists, and
// empty ones, are left out. It returns nil when none is left.
func Chain(lists ...*List) *Policy {
	var p Policy
	for _, l := range lists {
		if !l.Empty() {
			p.lists = append(p.lists, l)
		}
	}
	if len(p.lists) == 0 {
		return nil
	}
	return &p
}

// Allows reports whether p serves a. An IPv4 address mapped into IPv6
// counts as IPv4; an invalid address is served only when no list has
// entries, which a nil p says.
func (p *Policy) Allows(a netip.Addr) bool {
	if p == nil {
		return true
	}
	if !a.IsValid() {
		return false
	}
	a = a.Unmap().WithZone("")
	for _, l := range p.lists {
		switch l.verdict(a) {
		case allowed:
			return true
		case denied:
			return false
		}
	}
	return true
}

// Gate is the policy a socket goes by, swapped when the config is applied.
// The zero Gate serves everyone.
type Gate struct {
	p atomic.Pointer[Policy]
}

// Set has g go by p from now on; nil serves everyone.
func (g *Gate) Set(p *Policy) { g.p.Store(p) }

// Policy returns the policy g goes by now.
func (g *Gate) Policy() *Policy { return g.p.Load() }

// Allows reports whether g serves peer, as fib's Binding.Filter asks. A nil
// g serves everyone.
func (g *Gate) Allows(peer netip.AddrPort) bool {
	if g == nil {
		return true
	}
	return g.p.Load().Allows(peer.Addr())
}

// set is a set of addresses: sorted, merged ranges of each family.
type set struct {
	v4 []range4
	v6 []range6
}

type range4 struct{ lo, hi uint32 }

type u128 struct{ hi, lo uint64 }

func (a u128) cmp(b u128) int {
	if c := cmp.Compare(a.hi, b.hi); c != 0 {
		return c
	}
	return cmp.Compare(a.lo, b.lo)
}

func (a u128) next() (u128, bool) {
	if a.lo != ^uint64(0) {
		return u128{a.hi, a.lo + 1}, true
	}
	if a.hi != ^uint64(0) {
		return u128{a.hi + 1, 0}, true
	}
	return a, false
}

type range6 struct{ lo, hi u128 }

func (s *set) empty() bool { return len(s.v4) == 0 && len(s.v6) == 0 }

// newSet returns the set of rs, ranges whose ends are of one family and
// in order, as RangeOf and parseLine make them.
func newSet(rs []Range) set {
	var s set
	for _, r := range rs {
		if r.Lo.Is4() {
			s.v4 = append(s.v4, range4{binary.BigEndian.Uint32(r.Lo.AsSlice()), binary.BigEndian.Uint32(r.Hi.AsSlice())})
			continue
		}
		lo, hi := r.Lo.As16(), r.Hi.As16()
		s.v6 = append(s.v6, range6{u128{binary.BigEndian.Uint64(lo[:8]), binary.BigEndian.Uint64(lo[8:])},
			u128{binary.BigEndian.Uint64(hi[:8]), binary.BigEndian.Uint64(hi[8:])}})
	}
	slices.SortFunc(s.v4, func(a, b range4) int { return cmp.Compare(a.lo, b.lo) })
	s.v4 = merge4(s.v4)
	slices.SortFunc(s.v6, func(a, b range6) int { return a.lo.cmp(b.lo) })
	s.v6 = merge6(s.v6)
	return s
}

// merge4 merges sorted ranges that overlap or touch.
func merge4(rs []range4) []range4 {
	out := rs[:0]
	for _, r := range rs {
		if n := len(out); n > 0 && (out[n-1].hi == ^uint32(0) || r.lo <= out[n-1].hi+1) {
			out[n-1].hi = max(out[n-1].hi, r.hi)
			continue
		}
		out = append(out, r)
	}
	return slices.Clip(out)
}

func merge6(rs []range6) []range6 {
	out := rs[:0]
	for _, r := range rs {
		if n := len(out); n > 0 {
			next, ok := out[n-1].hi.next()
			if !ok || r.lo.cmp(next) <= 0 {
				if r.hi.cmp(out[n-1].hi) > 0 {
					out[n-1].hi = r.hi
				}
				continue
			}
		}
		out = append(out, r)
	}
	return slices.Clip(out)
}

func (s *set) contains(a netip.Addr) bool {
	if a.Is4() {
		if len(s.v4) == 0 {
			return false
		}
		b := a.As4()
		v := binary.BigEndian.Uint32(b[:])
		// The first range starting past v; the one before it may hold v.
		i, _ := slices.BinarySearchFunc(s.v4, v, func(r range4, v uint32) int {
			if r.lo <= v {
				return -1
			}
			return 1
		})
		return i > 0 && v <= s.v4[i-1].hi
	}
	if len(s.v6) == 0 {
		return false
	}
	b := a.As16()
	v := u128{binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])}
	i, _ := slices.BinarySearchFunc(s.v6, v, func(r range6, v u128) int {
		if r.lo.cmp(v) <= 0 {
			return -1
		}
		return 1
	})
	return i > 0 && v.cmp(s.v6[i-1].hi) <= 0
}
