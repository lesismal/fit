package l4

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

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

// TestRulesLocal refuses the proxy's own host and link by default, as
// literals in every spelling that reaches them and as the address a host
// name resolves to, whatever allow and deny say; an allow range within them
// reaches what it holds, a wider one does not, and allow_local lets them
// all through as before.
func TestRulesLocal(t *testing.T) {
	local := []string{
		"0.0.0.0", "0.1.2.3", "[::]", "[::ffff:0.0.0.0]", "127.0.0.1", "127.9.9.9", "[::1]",
		"[::ffff:127.0.0.1]", "[::127.0.0.1]", "169.254.169.254", "[::ffff:169.254.169.254]",
		"[fe80::1]", "[febf::1]",
	}
	public := []string{"192.0.2.1", "10.0.0.1", "1.0.0.0", "[2001:db8::1]", "[::ffff:192.0.2.1]", "[fec0::1]", "[::1:0:0:1]"}
	def := NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"}})
	wide := NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"}, Allow: []string{"0.0.0.0/0", "::/0", "example.com"}})
	optOut := NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"}, AllowLocal: true})
	for _, host := range local {
		dest := host + ":80"
		for name, r := range map[string]*Rules{"default": def, "wide allow": wide} {
			if r.Allows(dest) {
				t.Errorf("%s: %s allowed", name, dest)
			}
			if err := r.Dial(nil, "tcp", dest, time.Second, nil, nil); !errors.Is(err, ErrRefused) {
				t.Errorf("%s: Dial(%s): %v", name, dest, err)
			}
			if a := netip.MustParseAddr(strings.Trim(host, "[]")); r.resolved(resolvedAllowed, a) {
				t.Errorf("%s: a name resolved to %s allowed", name, a)
			}
		}
		if optOut.dest(dest) != allowed {
			t.Errorf("allow_local: %s refused", dest)
		}
	}
	for _, host := range public {
		dest := host + ":80"
		for name, r := range map[string]*Rules{"default": def, "wide allow": wide, "allow_local": optOut} {
			if r.dest(dest) != allowed {
				t.Errorf("%s: %s refused", name, dest)
			}
			if a := netip.MustParseAddr(strings.Trim(host, "[]")); !r.resolved(resolvedAllowed, a) {
				t.Errorf("%s: a name resolved to %s refused", name, a)
			}
		}
	}

	// A host name waits for its address unless allow_local is set and deny
	// has no ranges; an address the connection cannot report is refused.
	for name, r := range map[string]*Rules{"default": def, "wide allow": wide} {
		if v := r.dest("example.com:80"); v != resolvedAllowed {
			t.Errorf("%s: dest(example.com) = %d", name, v)
		}
		if r.resolved(resolvedAllowed, netip.Addr{}) {
			t.Errorf("%s: no address allowed", name)
		}
	}
	if v := optOut.dest("localhost:80"); v != allowed {
		t.Errorf("allow_local: dest(localhost) = %d", v)
	}

	// Ranges within the local ones, by address or IPv4-mapped, reach what
	// they hold and nothing else; deny still wins.
	narrow := NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"},
		Allow: []string{"127.0.0.1", "::ffff:169.254.169.254", "fe80::/64", "192.0.2.0/24", "example.com"},
		Deny:  []string{"fe80::5"}})
	for dest, want := range map[string]verdict{
		"127.0.0.1:80": allowed, "[::ffff:127.0.0.1]:80": allowed, "169.254.169.254:80": allowed,
		"[fe80::1]:80": allowed, "[fe80::5]:80": refused, "127.0.0.2:80": refused, "0.0.0.0:80": refused,
		"[::1]:80": refused, "[fe80:0:0:1::1]:80": refused, "192.0.2.1:80": allowed, "localhost:80": unresolved,
	} {
		if got := narrow.dest(dest); got != want {
			t.Errorf("narrow: dest(%q) = %d, want %d", dest, got, want)
		}
	}
	for addr, want := range map[string]bool{"127.0.0.1": true, "::ffff:127.0.0.1": true, "127.0.0.2": false, "0.0.0.0": false, "::": false} {
		if got := narrow.resolved(resolvedAllowed, netip.MustParseAddr(addr)); got != want {
			t.Errorf("narrow: resolved(%s) = %v", addr, got)
		}
	}
	if def.Key() == optOut.Key() {
		t.Error("allow_local does not change the key")
	}
}
