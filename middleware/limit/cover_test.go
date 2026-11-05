package limit

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/lesismal/fit/module"
)

// TestPeerAndName checks peer of an address with a port and of a bare one,
// and validName of names with and without characters a token may not hold.
func TestPeerAndName(t *testing.T) {
	for s, want := range map[string]string{
		"[::ffff:192.0.2.1]:80": "192.0.2.1", "192.0.2.1:80": "192.0.2.1", "192.0.2.1": "192.0.2.1",
		"fe80::1%eth0": "fe80::1", "": "invalid IP",
	} {
		if got := peer(s); got.String() != want {
			t.Errorf("peer(%q) = %v, want %s", s, got, want)
		}
	}
	for name, want := range map[string]bool{"": false, "X-Api_Key.1~": true, "x y": false, "a:b": false} {
		if got := validName(name); got != want {
			t.Errorf("validName(%q) = %v", name, got)
		}
	}
}

// TestRateIPv6PrefixUnchecked checks a Rate made in Go rather than from a
// config, which the schema's bounds did not check, refuses an ipv6_prefix out
// of range, and that one that slipped past counts each IPv6 address apart
// rather than all as one.
func TestRateIPv6PrefixUnchecked(t *testing.T) {
	for _, bits := range []int{-1, 129} {
		err := (&Rate{Rate: 1, IPv6Prefix: bits}).Provision(&module.Context{})
		if err == nil || !strings.Contains(err.Error(), "is not between 1 and 128") {
			t.Errorf("ipv6_prefix %d: %v", bits, err)
		}
	}
	rl := &Rate{v6bits: -1}
	ip := netip.MustParseAddr("2001:db8::1")
	if got := rl.client(ip); got != ip {
		t.Errorf("client(%s) with bits -1 = %s", ip, got)
	}
}
