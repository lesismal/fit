package packet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sort"
)

// MaxRoutes is the most routes a table may hold; NewTable refuses more.
// Every node has one or two, and a subnet router a few more.
const MaxRoutes = 1 << 16

// Errors NewTable returns.
var (
	ErrTooManyRoutes = errors.New("packet: too many routes")
	ErrBadRoute      = errors.New("packet: invalid route")
	ErrDuplicate     = errors.New("packet: duplicate route")
)

// Route says that the addresses in Prefix belong to Node: packets to them go
// to that node, and packets from that node may come from them.
type Route struct {
	Prefix netip.Prefix
	Node   uint32
}

// Table is the overlay's route table. It is built once and never changed,
// so lookups take no lock; a new netmap builds a new table.
type Table struct {
	// v4 and v6 hold one level for each prefix length in use, longest
	// first, so the first level that knows an address is its longest match.
	v4  []level4
	v6  []level6
	len int
}

type level4 struct {
	mask  uint32
	nodes map[uint32]uint32
}

type level6 struct {
	mask  [16]byte
	nodes map[[16]byte]uint32
}

// NewTable builds a table from routes. Each prefix is masked to its length;
// a prefix that is not valid or is an IPv4-mapped IPv6 one is refused, as
// is a prefix given twice, even for the same node.
func NewTable(routes []Route) (*Table, error) {
	if len(routes) > MaxRoutes {
		return nil, ErrTooManyRoutes
	}
	v4 := map[int]map[uint32]uint32{}
	v6 := map[int]map[[16]byte]uint32{}
	for _, r := range routes {
		p := r.Prefix
		if !p.IsValid() || p.Addr().Is4In6() {
			return nil, fmt.Errorf("%w: %v", ErrBadRoute, p)
		}
		p = p.Masked()
		if p.Addr().Is4() {
			key := binary.BigEndian.Uint32(p.Addr().AsSlice())
			level := v4[p.Bits()]
			if level == nil {
				level = map[uint32]uint32{}
				v4[p.Bits()] = level
			}
			if _, ok := level[key]; ok {
				return nil, fmt.Errorf("%w: %v", ErrDuplicate, p)
			}
			level[key] = r.Node
			continue
		}
		key := p.Addr().As16()
		level := v6[p.Bits()]
		if level == nil {
			level = map[[16]byte]uint32{}
			v6[p.Bits()] = level
		}
		if _, ok := level[key]; ok {
			return nil, fmt.Errorf("%w: %v", ErrDuplicate, p)
		}
		level[key] = r.Node
	}
	t := &Table{len: len(routes)}
	for _, bits := range longestFirst(v4) {
		t.v4 = append(t.v4, level4{mask: mask4(bits), nodes: v4[bits]})
	}
	for _, bits := range longestFirst(v6) {
		t.v6 = append(t.v6, level6{mask: mask16(bits), nodes: v6[bits]})
	}
	return t, nil
}

// Len is the number of routes in the table.
func (t *Table) Len() int { return t.len }

// Lookup returns the node whose longest prefix holds addr. An IPv4-mapped
// IPv6 address is looked up as the IPv4 address it maps.
func (t *Table) Lookup(addr netip.Addr) (node uint32, ok bool) {
	addr = addr.Unmap()
	if addr.Is4() {
		a4 := addr.As4()
		key := binary.BigEndian.Uint32(a4[:])
		for i := range t.v4 {
			if node, ok = t.v4[i].nodes[key&t.v4[i].mask]; ok {
				return node, true
			}
		}
		return 0, false
	}
	if !addr.Is6() {
		return 0, false
	}
	a := addr.As16()
	for i := range t.v6 {
		var key [16]byte
		for j := range key {
			key[j] = a[j] & t.v6[i].mask[j]
		}
		if node, ok = t.v6[i].nodes[key]; ok {
			return node, true
		}
	}
	return 0, false
}

// Allows reports whether node may send a packet from src: whether src's
// longest prefix is node's. A packet from a peer that fails it is dropped,
// so that no node can speak for another's addresses.
func (t *Table) Allows(node uint32, src netip.Addr) bool {
	n, ok := t.Lookup(src)
	return ok && n == node
}

func longestFirst[M ~map[int]V, V any](m M) []int {
	bits := make([]int, 0, len(m))
	for b := range m {
		bits = append(bits, b)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(bits)))
	return bits
}

func mask4(bits int) uint32 {
	if bits == 0 {
		return 0
	}
	return ^uint32(0) << (32 - bits)
}

func mask16(bits int) (m [16]byte) {
	for i := range m {
		switch {
		case bits >= 8:
			m[i] = 0xff
			bits -= 8
		case bits > 0:
			m[i] = ^byte(0) << (8 - bits)
			bits = 0
		}
	}
	return m
}
