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
		if got := r.resolved(unresolved, netip.MustParseAddr(addr)); got != want {
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
	if open.resolved(resolvedAllowed, netip.MustParseAddr("127.0.0.1")) || !open.resolved(resolvedAllowed, netip.MustParseAddr("10.0.0.1")) {
		t.Error("open: resolved addresses decided wrongly")
	}
	if NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"}}).Key() == open.Key() || (*Rules)(nil).Key() != "" {
		t.Error("keys of different rules agree")
	}
}
