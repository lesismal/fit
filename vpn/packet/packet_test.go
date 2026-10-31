package packet

import (
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"net/netip"
	"testing"
)

// ipv4 builds an IPv4 packet with a header of ihl bytes and a payload of n.
func ipv4(src, dst string, proto byte, ihl, n int) []byte {
	b := make([]byte, ihl+n)
	b[0] = 0x40 | byte(ihl/4)
	binary.BigEndian.PutUint16(b[2:4], uint16(ihl+n))
	b[8] = 64
	b[9] = proto
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(b[12:16], s[:])
	copy(b[16:20], d[:])
	return b
}

// ipv6 builds an IPv6 packet with a payload of n.
func ipv6(src, dst string, next byte, n int) []byte {
	b := make([]byte, 40+n)
	b[0] = 0x60
	binary.BigEndian.PutUint16(b[4:6], uint16(n))
	b[6] = next
	b[7] = 64
	s, d := netip.MustParseAddr(src).As16(), netip.MustParseAddr(dst).As16()
	copy(b[8:24], s[:])
	copy(b[24:40], d[:])
	return b
}

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want Header
	}{
		{"ipv4 tcp", ipv4("100.96.0.1", "100.96.0.2", 6, 20, 40),
			Header{4, 6, 60, netip.MustParseAddr("100.96.0.1"), netip.MustParseAddr("100.96.0.2")}},
		{"ipv4 options", ipv4("10.0.0.1", "10.0.0.2", 17, 60, 8),
			Header{4, 17, 68, netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}},
		{"ipv4 header only", ipv4("10.0.0.1", "10.0.0.2", 1, 20, 0),
			Header{4, 1, 20, netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}},
		{"ipv6 udp", ipv6("fd7a::1", "fd7a::2", 17, 100),
			Header{6, 17, 140, netip.MustParseAddr("fd7a::1"), netip.MustParseAddr("fd7a::2")}},
		{"ipv6 no next header", ipv6("fd7a::1", "fd7a::2", 59, 0),
			Header{6, 59, 40, netip.MustParseAddr("fd7a::1"), netip.MustParseAddr("fd7a::2")}},
		{"trailing bytes", append(ipv4("10.0.0.1", "10.0.0.2", 6, 20, 4), 0, 0, 0),
			Header{4, 6, 24, netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := Parse(c.b)
			if err != nil {
				t.Fatal(err)
			}
			if h != c.want {
				t.Fatalf("got %+v, want %+v", h, c.want)
			}
		})
	}
}

func TestParseRefuses(t *testing.T) {
	bad := func(f func(b []byte) []byte, b []byte) []byte { return f(b) }
	cases := []struct {
		name string
		b    []byte
		want error
	}{
		{"empty", nil, ErrShort},
		{"version 5", []byte{0x50, 0, 0, 20}, ErrVersion},
		{"version 0", make([]byte, 40), ErrVersion},
		{"ipv4 short", ipv4("10.0.0.1", "10.0.0.2", 6, 20, 0)[:19], ErrShort},
		{"ipv6 short", ipv6("fd7a::1", "fd7a::2", 6, 0)[:39], ErrShort},
		{"ipv4 ihl too small", bad(func(b []byte) []byte { b[0] = 0x44; return b }, ipv4("10.0.0.1", "10.0.0.2", 6, 20, 8)), ErrMalformed},
		{"ipv4 ihl past the end", bad(func(b []byte) []byte { b[0] = 0x4f; return b }, ipv4("10.0.0.1", "10.0.0.2", 6, 20, 8)), ErrMalformed},
		{"ipv4 total under ihl", bad(func(b []byte) []byte { binary.BigEndian.PutUint16(b[2:4], 19); return b }, ipv4("10.0.0.1", "10.0.0.2", 6, 20, 8)), ErrMalformed},
		{"ipv4 total past the end", ipv4("10.0.0.1", "10.0.0.2", 6, 20, 8)[:27], ErrMalformed},
		{"ipv6 payload past the end", ipv6("fd7a::1", "fd7a::2", 6, 8)[:47], ErrMalformed},
		{"ipv6 jumbogram", bad(func(b []byte) []byte { binary.BigEndian.PutUint16(b[4:6], 0); return b }, ipv6("fd7a::1", "fd7a::2", 0, 8)), ErrMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(c.b); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func routes(t testing.TB, rs ...any) []Route {
	t.Helper()
	var out []Route
	for i := 0; i < len(rs); i += 2 {
		out = append(out, Route{netip.MustParsePrefix(rs[i].(string)), uint32(rs[i+1].(int))})
	}
	return out
}

func TestTableLookup(t *testing.T) {
	tab, err := NewTable(routes(t,
		"100.96.0.1/32", 1,
		"100.96.0.2/32", 2,
		"10.1.0.0/16", 2, // a subnet router
		"10.1.2.0/24", 3, // a narrower one wins
		"0.0.0.0/0", 9, // an exit node
		"fd7a::1/128", 1,
		"fd7a::2/128", 2,
		"fd7a:1:2::/48", 3,
		"fd7a:1:2:3:4:5:6:7/112", 4, // masked to fd7a:1:2:3:4:5:6:0/112
	))
	if err != nil {
		t.Fatal(err)
	}
	if tab.Len() != 9 {
		t.Fatalf("Len %d", tab.Len())
	}
	cases := []struct {
		addr string
		node uint32
		ok   bool
	}{
		{"100.96.0.1", 1, true},
		{"100.96.0.2", 2, true},
		{"100.96.0.3", 9, true},
		{"10.1.9.9", 2, true},
		{"10.1.2.200", 3, true},
		{"::ffff:10.1.2.200", 3, true},
		{"8.8.8.8", 9, true},
		{"fd7a::1", 1, true},
		{"fd7a::3", 0, false},
		{"fd7a:1:2:ffff::1", 3, true},
		{"fd7a:1:3::1", 0, false},
		{"fd7a:1:2:3:4:5:6:ffff", 4, true},
		{"fd7a:1:2:3:4:5:7:0", 3, true},
		{"2001:db8::1", 0, false},
	}
	for _, c := range cases {
		node, ok := tab.Lookup(netip.MustParseAddr(c.addr))
		if node != c.node || ok != c.ok {
			t.Errorf("Lookup(%s) = %d %v, want %d %v", c.addr, node, ok, c.node, c.ok)
		}
	}
	if node, ok := tab.Lookup(netip.Addr{}); ok {
		t.Errorf("the zero address found node %d", node)
	}
}

func TestTableAllows(t *testing.T) {
	tab, err := NewTable(routes(t, "100.96.0.1/32", 1, "100.96.0.2/32", 2, "10.1.0.0/16", 2, "10.1.2.0/24", 3))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		node uint32
		src  string
		want bool
	}{
		{1, "100.96.0.1", true},
		{1, "100.96.0.2", false}, // node 2's address
		{2, "10.1.0.5", true},
		{2, "10.1.2.5", false}, // node 3 holds the longer prefix
		{3, "10.1.2.5", true},
		{1, "192.0.2.1", false}, // nobody's
	} {
		if got := tab.Allows(c.node, netip.MustParseAddr(c.src)); got != c.want {
			t.Errorf("Allows(%d, %s) = %v", c.node, c.src, got)
		}
	}
}

func TestNewTableRefuses(t *testing.T) {
	for _, c := range []struct {
		name   string
		routes []Route
		want   error
	}{
		{"zero prefix", []Route{{}}, ErrBadRoute},
		{"mapped", routes(t, "::ffff:10.0.0.0/104", 1), ErrBadRoute},
		{"bits past the address", []Route{{netip.PrefixFrom(netip.MustParseAddr("10.0.0.0"), 33), 1}}, ErrBadRoute},
		{"duplicate", routes(t, "10.0.0.0/8", 1, "10.0.0.0/8", 1), ErrDuplicate},
		{"duplicate once masked", routes(t, "10.0.0.0/8", 1, "10.9.9.9/8", 2), ErrDuplicate},
		{"duplicate ipv6", routes(t, "fd7a::/64", 1, "fd7a::1/64", 2), ErrDuplicate},
		{"too many", make([]Route, MaxRoutes+1), ErrTooManyRoutes},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewTable(c.routes); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
	if _, err := NewTable(nil); err != nil {
		t.Fatalf("an empty table: %v", err)
	}
}

// TestTableAgainstScan checks lookups in random tables against a scan of
// every route for the longest that holds the address.
func TestTableAgainstScan(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	randAddr := func(v6 bool) netip.Addr {
		if v6 {
			var a [16]byte
			a[0], a[1] = 0xfd, byte(r.IntN(4)) // a small space, so prefixes overlap
			for i := 2; i < 16; i++ {
				a[i] = byte(r.IntN(4))
			}
			return netip.AddrFrom16(a)
		}
		return netip.AddrFrom4([4]byte{10, byte(r.IntN(4)), byte(r.IntN(4)), byte(r.IntN(4))})
	}
	for round := 0; round < 50; round++ {
		seen := map[netip.Prefix]bool{}
		var rs []Route
		for len(rs) < 64 {
			v6 := r.IntN(2) == 0
			max := 32
			if v6 {
				max = 128
			}
			p := netip.PrefixFrom(randAddr(v6), r.IntN(max+1)).Masked()
			if seen[p] {
				continue
			}
			seen[p] = true
			rs = append(rs, Route{p, uint32(len(rs))})
		}
		tab, err := NewTable(rs)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 500; i++ {
			a := randAddr(r.IntN(2) == 0)
			wantNode, wantOK, best := uint32(0), false, -1
			for _, rt := range rs {
				if rt.Prefix.Contains(a) && rt.Prefix.Bits() > best {
					wantNode, wantOK, best = rt.Node, true, rt.Prefix.Bits()
				}
			}
			if node, ok := tab.Lookup(a); node != wantNode || ok != wantOK {
				t.Fatalf("Lookup(%s) = %d %v, the scan says %d %v", a, node, ok, wantNode, wantOK)
			}
		}
	}
}

func TestNoAllocs(t *testing.T) {
	tab, err := NewTable(routes(t, "100.96.0.1/32", 1, "10.1.0.0/16", 2, "0.0.0.0/0", 3, "fd7a::1/128", 1, "fd7a:1::/48", 2))
	if err != nil {
		t.Fatal(err)
	}
	p4 := ipv4("100.96.0.1", "10.1.2.3", 6, 20, 40)
	p6 := ipv6("fd7a::1", "fd7a:1::9", 17, 40)
	if n := testing.AllocsPerRun(100, func() {
		for _, p := range [][]byte{p4, p6} {
			h, err := Parse(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := tab.Lookup(h.Dst); !ok || !tab.Allows(1, h.Src) {
				t.Fatal("not routed")
			}
		}
	}); n != 0 {
		t.Fatalf("%v allocations parsing and routing", n)
	}
}

func BenchmarkRoute(b *testing.B) {
	var rs []Route
	for i := 0; i < 1000; i++ {
		rs = append(rs, Route{netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 96, byte(i >> 8), byte(i)}), 32), uint32(i)})
		rs = append(rs, Route{netip.PrefixFrom(netip.AddrFrom16([16]byte{0xfd, 0x7a, 14: byte(i >> 8), 15: byte(i)}), 128), uint32(i)})
	}
	rs = append(rs, routes(b, "10.0.0.0/8", 1, "10.1.0.0/16", 2, "fd7a:1::/48", 3)...)
	tab, err := NewTable(rs)
	if err != nil {
		b.Fatal(err)
	}
	for _, c := range []struct {
		name string
		p    []byte
	}{
		{"ipv4", ipv4("100.96.0.1", "100.96.3.7", 6, 20, 1200)},
		{"ipv6", ipv6("fd7a::1", "fd7a::307", 6, 1200)},
	} {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				h, err := Parse(c.p)
				if err != nil {
					b.Fatal(err)
				}
				if _, ok := tab.Lookup(h.Dst); !ok {
					b.Fatal("not routed")
				}
			}
		})
	}
}
