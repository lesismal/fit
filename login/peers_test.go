package login

import (
	"net/netip"
	"testing"
)

func TestPeers(t *testing.T) {
	p := NewPeers(8)
	if p.Share() != 2 || NewPeers(1).Share() != 1 || NewPeers(0).Share() != 1 {
		t.Fatalf("shares %d %d %d", p.Share(), NewPeers(1).Share(), NewPeers(0).Share())
	}
	a := netip.MustParseAddr("203.0.113.9")
	if !p.Open(a) || !p.Open(netip.MustParseAddr("::ffff:203.0.113.9")) || p.Open(a) {
		t.Error("an IPv4 client's share")
	}
	if !p.Open(netip.MustParseAddr("2001:db8::1")) || !p.Open(netip.MustParseAddr("2001:db8::2%eth0")) || p.Open(netip.MustParseAddr("2001:db8::ffff")) {
		t.Error("an IPv6 /64's share")
	}
	if !p.Open(netip.MustParseAddr("2001:db8:0:1::1")) {
		t.Error("another /64 is refused")
	}
	for range 10 {
		if !p.Open(netip.MustParseAddr("127.0.0.1")) || !p.Open(netip.MustParseAddr("::1")) || !p.Open(netip.Addr{}) {
			t.Fatal("a loopback client or a Unix socket is held to a share")
		}
	}
	p.Close(netip.MustParseAddr("127.0.0.1"))
	p.Close(netip.Addr{})
	p.Close(a)
	if !p.Open(a) || p.Open(a) {
		t.Error("a client after one of its connections closed")
	}
	p.Close(a)
	p.Close(a)
	p.Close(netip.MustParseAddr("2001:db8::1"))
	p.Close(netip.MustParseAddr("2001:db8::2"))
	p.Close(netip.MustParseAddr("2001:db8:0:1::1"))
	if len(p.n) != 0 {
		t.Errorf("%d clients left counted", len(p.n))
	}
}
