package wire

import (
	"net"
	"net/netip"
	"testing"

	"github.com/lesismal/fib"
)

// strAddr is a net.Addr that is only its string.
type strAddr string

func (a strAddr) Network() string { return "test" }
func (a strAddr) String() string  { return string(a) }

// TestRemoteIP: the pre-auth limits count a connection by its IP, read from
// a TCP address or from any address that prints as ip:port, a v4-mapped one
// as v4; an address that is neither counts under the zero Addr.
// TestRemoteIP：认证前的限额按 IP 计数，取自 TCP 地址或任何形如 ip:port 的地址，
// v4 映射地址按 v4 计；两者都不是的地址按零值 Addr 计。
func TestRemoteIP(t *testing.T) {
	v4 := netip.MustParseAddr("192.0.2.7")
	for _, tc := range []struct {
		name string
		a    net.Addr
		want netip.Addr
	}{
		{"tcp", &net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 80}, v4},
		{"udp", &net.UDPAddr{IP: net.ParseIP("192.0.2.7"), Port: 80}, v4},
		{"v4-mapped", strAddr("[::ffff:192.0.2.7]:80"), v4},
		{"v6", strAddr("[2001:db8::1]:443"), netip.MustParseAddr("2001:db8::1")},
		{"unix", &net.UnixAddr{Name: "/run/fit.sock", Net: "unix"}, netip.Addr{}},
		{"nil", nil, netip.Addr{}},
	} {
		if got := remoteIP(tc.a); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestAuthenticated: a connection not held leaves the pending counts alone;
// one held is taken off, its IP's count with it.
// TestAuthenticated：未记录的连接不改动待认证计数；记录中的连接被移出，其 IP 的计数随之减少。
func TestAuthenticated(t *testing.T) {
	ip := netip.MustParseAddr("192.0.2.7")
	c, other := &fib.Connection{}, &fib.Connection{}
	tc := &tcpConns{
		pending:   map[*fib.Connection]netip.Addr{c: ip, other: ip},
		pendingIP: map[netip.Addr]int{ip: 2},
	}
	tc.authenticated(&fib.Connection{})
	tc.authenticated(nil)
	if len(tc.pending) != 2 || tc.pendingIP[ip] != 2 {
		t.Fatalf("a connection not held changed the counts: %v %v", tc.pending, tc.pendingIP)
	}
	tc.authenticated(c)
	if len(tc.pending) != 1 || tc.pendingIP[ip] != 1 {
		t.Fatalf("authenticated(c): %v %v", tc.pending, tc.pendingIP)
	}
	tc.authenticated(other)
	if len(tc.pending) != 0 || len(tc.pendingIP) != 0 {
		t.Fatalf("authenticated left the counts: %v %v", tc.pending, tc.pendingIP)
	}
}

// TestHoldNoAddress: a connection whose address cannot be read counts under
// the zero Addr.
// TestHoldNoAddress：读不到地址的连接按零值 Addr 计数。
func TestHoldNoAddress(t *testing.T) {
	tc := &tcpConns{maxPending: 2, maxPerIP: 1, pending: map[*fib.Connection]netip.Addr{}, pendingIP: map[netip.Addr]int{}}
	if !tc.hold(&fib.Connection{}) || tc.pendingIP[netip.Addr{}] != 1 {
		t.Fatal("a connection with no address was not held")
	}
	if tc.hold(&fib.Connection{}) {
		t.Fatal("a second connection with no address passed the per-IP limit")
	}
}
