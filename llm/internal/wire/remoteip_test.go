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

// TestAuthenticatedNil: a session whose TCP connection was not found by its
// address leaves the pending counts alone.
// TestAuthenticatedNil：按地址找不到 TCP 连接的会话不改动待认证计数。
func TestAuthenticatedNil(t *testing.T) {
	ip := netip.MustParseAddr("192.0.2.7")
	c := &fib.Connection{}
	tc := &tcpConns{pending: map[*fib.Connection]netip.Addr{c: ip}, pendingIP: map[netip.Addr]int{ip: 1}}
	tc.authenticated(nil)
	if len(tc.pending) != 1 || tc.pendingIP[ip] != 1 {
		t.Fatalf("authenticated(nil) changed the counts: %v %v", tc.pending, tc.pendingIP)
	}
	tc.authenticated(c)
	if len(tc.pending) != 0 || len(tc.pendingIP) != 0 {
		t.Fatalf("authenticated left the counts: %v %v", tc.pending, tc.pendingIP)
	}
}
