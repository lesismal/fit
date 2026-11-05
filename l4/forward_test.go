package l4

import (
	"net/netip"
	"testing"

	"github.com/lesismal/fit/config"
)

func TestRulesDest(t *testing.T) {
	r := NewRules(&config.ForwardProxy{
		Clients: []string{"10.0.0.0/8", "::1"},
		Allow:   []string{"example.com", "*.example.org", "192.0.2.0/24"},
		Deny:    []string{"bad.example.org", "192.0.2.66"},
		Ports:   []int{443, 8443},
	})
	for _, tc := range []struct {
		dest string
		want verdict
	}{
		// deny's range may still hold the address an allowed name has.
		{"example.com:443", resolvedAllowed},
		{"EXAMPLE.com.:443", resolvedAllowed},
		{"example.com:80", refused},
		{"www.example.com:443", unresolved}, // only a range can allow it
		{"a.b.example.org:443", resolvedAllowed},
		{"example.org:443", unresolved},
		{"bad.example.org:443", refused},
		{"192.0.2.1:8443", allowed},
		{"192.0.2.66:443", refused},
		{"198.51.100.1:443", refused},
		{"[fe80::1%eth0]:443", refused},
		{"nohost", refused},
		{"example.com:x", refused},
	} {
		if got := r.dest(tc.dest); got != tc.want {
			t.Errorf("dest(%q) = %d, want %d", tc.dest, got, tc.want)
		}
	}
	for addr, want := range map[string]bool{"192.0.2.9": true, "192.0.2.66": false, "198.51.100.1": false} {
		if got := r.resolved(unresolved, netip.MustParseAddr(addr), netip.Addr{}); got != want {
			t.Errorf("resolved(%s) = %v", addr, got)
		}
	}
	for addr, want := range map[string]bool{"10.1.2.3": true, "::ffff:10.1.2.3": true, "::1": true, "11.0.0.1": false} {
		if got := r.Client(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Client(%s) = %v", addr, got)
		}
	}

	// Without allow, everything deny does not hold; a deny range makes a
	// name wait for its address.
	open := NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"}, Deny: []string{"127.0.0.0/8"}})
	for dest, want := range map[string]verdict{"example.com:1": resolvedAllowed, "127.0.0.1:1": refused, "10.0.0.1:1": allowed} {
		if got := open.dest(dest); got != want {
			t.Errorf("open: dest(%q) = %d, want %d", dest, got, want)
		}
	}
	if open.resolved(resolvedAllowed, netip.MustParseAddr("127.0.0.1"), netip.Addr{}) ||
		!open.resolved(resolvedAllowed, netip.MustParseAddr("10.0.0.1"), netip.Addr{}) {
		t.Error("open: resolved addresses decided wrongly")
	}
	if NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"}}).Key() == open.Key() || (*Rules)(nil).Key() != "" {
		t.Error("keys of different rules agree")
	}
}

// TestRulesInternal refuses, with allow and deny left out, what a client
// would reach the gateway's host or the cloud's metadata service through:
// the Admin API on 127.0.0.1:2019 among them, by address, by a name that
// resolves there and by the host's own address; an allow range inside one
// of those ranges lets it through, and a wider one does not.
func TestRulesInternal(t *testing.T) {
	open := NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"}})
	for _, dest := range []string{"127.0.0.1:2019", "[::1]:2019", "[::ffff:127.0.0.1]:2019", "0.0.0.0:2019", "[::]:2019",
		"169.254.169.254:80", "[fe80::1]:80", "224.0.0.1:80", "[ff02::1]:80", "100.100.100.200:80", "[fd00:ec2::254]:80"} {
		if got := open.dest(dest); got != refused {
			t.Errorf("dest(%q) = %d, want refused", dest, got)
		}
	}
	for _, dest := range []string{"10.0.0.1:443", "192.0.2.1:443", "[2001:db8::1]:443"} {
		if got := open.dest(dest); got != allowed {
			t.Errorf("dest(%q) = %d, want allowed", dest, got)
		}
	}
	self := netip.MustParseAddr("10.0.0.5")
	for _, tc := range []struct {
		remote, local string
		want          bool
	}{
		{"127.0.0.1", "127.0.0.1", false},             // localhost resolved
		{"::ffff:169.254.169.254", "10.0.0.5", false}, // a metadata name resolved
		{"10.0.0.5", "10.0.0.5", false},               // the host's own address
		{"10.0.0.6", "10.0.0.5", true},
		{"192.0.2.1", "", true},
	} {
		local := netip.Addr{}
		if tc.local != "" {
			local = netip.MustParseAddr(tc.local)
		}
		for _, v := range []verdict{allowed, resolvedAllowed} {
			if got := open.resolved(v, netip.MustParseAddr(tc.remote), local); got != tc.want {
				t.Errorf("resolved(%d, %s from %s) = %v, want %v", v, tc.remote, tc.local, got, tc.want)
			}
		}
	}

	// An allow range inside an internal range, or the host's address
	// alone, lets it through; one wider does not.
	optIn := NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"},
		Allow: []string{"0.0.0.0/0", "::/0", "127.0.0.0/8", "169.254.169.254", "10.0.0.5"}})
	for dest, want := range map[string]verdict{"127.0.0.1:2019": allowed, "169.254.169.254:80": allowed,
		"169.254.1.1:80": refused, "[::1]:2019": refused, "0.0.0.0:80": refused, "example.com:443": unresolved} {
		if got := optIn.dest(dest); got != want {
			t.Errorf("opted in: dest(%q) = %d, want %d", dest, got, want)
		}
	}
	if !optIn.resolved(unresolved, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.1")) ||
		!optIn.resolved(allowed, self, self) || optIn.resolved(unresolved, netip.MustParseAddr("::1"), netip.Addr{}) {
		t.Error("opted in: resolved addresses decided wrongly")
	}
	wide := NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"}, Allow: []string{"10.0.0.0/8"}})
	if wide.resolved(unresolved, self, self) || !wide.resolved(unresolved, netip.MustParseAddr("10.0.0.6"), self) {
		t.Error("a wide allow range lets the host's own address through, or refuses another")
	}
}
