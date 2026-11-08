package access

import (
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

func list(t testing.TB, allow, deny []string) *List {
	t.Helper()
	l, err := Compile(&config.Access{Allow: allow, Deny: deny}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestPolicyLayers(t *testing.T) {
	global := list(t, nil, []string{"203.0.113.0/24", "2001:db8:bad::/48"})
	routeAllow := list(t, []string{"203.0.113.7", "10.0.0.0/8"}, nil)
	routeDeny := list(t, nil, []string{"198.51.100.1"})
	whitelist := list(t, []string{"192.0.2.0/24"}, nil)

	tests := []struct {
		name   string
		p      *Policy
		addr   string
		allows bool
	}{
		{"nil policy serves all", nil, "203.0.113.1", true},
		{"global deny", Chain(nil, global), "203.0.113.1", false},
		{"global deny v6", Chain(global), "2001:db8:bad::1", false},
		{"global no say", Chain(global), "198.51.100.1", true},
		{"route allow overrides global deny", Chain(routeAllow, global), "203.0.113.7", true},
		{"route allow list refuses others", Chain(routeAllow, global), "198.51.100.9", false},
		{"route deny overrides global allow", Chain(routeDeny, whitelist), "198.51.100.1", false},
		{"route no say falls to global whitelist", Chain(routeDeny, whitelist), "198.51.100.2", false},
		{"route no say, global whitelist serves", Chain(routeDeny, whitelist), "192.0.2.5", true},
		{"mapped v4 counts as v4", Chain(global), "::ffff:203.0.113.1", false},
		{"zone ignored", Chain(list(t, nil, []string{"fe80::/10"})), "fe80::1%eth0", false},
		{"invalid refused", Chain(global), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a netip.Addr
			if tt.addr != "" {
				a = addr(tt.addr)
			}
			if got := tt.p.Allows(a); got != tt.allows {
				t.Errorf("Allows(%s) = %v, want %v", tt.addr, got, tt.allows)
			}
		})
	}
}

func TestChainEmpty(t *testing.T) {
	if p := Chain(nil, list(t, nil, nil)); p != nil {
		t.Errorf("Chain of nothing = %v, want nil", p)
	}
	if l, err := Compile(nil, nil); l != nil || err != nil {
		t.Errorf("Compile(nil) = %v, %v", l, err)
	}
}

func TestDenyBeatsAllowInOneList(t *testing.T) {
	p := Chain(list(t, []string{"10.0.0.0/8"}, []string{"10.1.0.0/16"}))
	if p.Allows(addr("10.1.2.3")) {
		t.Error("10.1.2.3 allowed")
	}
	if !p.Allows(addr("10.2.2.3")) {
		t.Error("10.2.2.3 refused")
	}
}

func TestEdges(t *testing.T) {
	p := Chain(list(t, nil, []string{"0.0.0.0/0"}))
	for _, a := range []string{"0.0.0.0", "255.255.255.255", "1.2.3.4"} {
		if p.Allows(addr(a)) {
			t.Errorf("%s allowed by 0.0.0.0/0 deny", a)
		}
	}
	if !p.Allows(addr("::1")) {
		t.Error("::1 refused by an IPv4 deny")
	}
	p = Chain(list(t, nil, []string{"::/0"}))
	for _, a := range []string{"::", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "2001:db8::1"} {
		if p.Allows(addr(a)) {
			t.Errorf("%s allowed by ::/0 deny", a)
		}
	}
	p = Chain(list(t, nil, []string{"255.255.255.255", "2001:db8::/64", "2001:db8:0:1::/64", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"}))
	for a, want := range map[string]bool{
		"255.255.255.255":                         false,
		"255.255.255.254":                         true,
		"2001:db8::ffff":                          false,
		"2001:db8:0:1::5":                         false,
		"2001:db8:0:2::":                          true,
		"2001:db7:ffff::":                         true,
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff": false,
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe": true,
	} {
		if got := p.Allows(addr(a)); got != want {
			t.Errorf("Allows(%s) = %v, want %v", a, got, want)
		}
	}
}

// TestSetAgainstLinear checks the merged ranges against a linear scan of
// the prefixes, on random prefixes and addresses.
func TestSetAgainstLinear(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for round := 0; round < 50; round++ {
		var ps []netip.Prefix
		for i := 0; i < 1+r.IntN(40); i++ {
			if r.IntN(2) == 0 {
				var b [4]byte
				for j := range b {
					b[j] = byte(r.IntN(4)) // a small space, so that ranges meet
				}
				ps = append(ps, netip.PrefixFrom(netip.AddrFrom4(b), 16+r.IntN(17)).Masked())
			} else {
				var b [16]byte
				for j := range b {
					b[j] = byte(r.IntN(3))
				}
				ps = append(ps, netip.PrefixFrom(netip.AddrFrom16(b), 100+r.IntN(29)).Masked())
			}
		}
		s := newSet(append([]netip.Prefix(nil), ps...))
		for i := 0; i < 2000; i++ {
			var a netip.Addr
			if r.IntN(2) == 0 {
				var b [4]byte
				for j := range b {
					b[j] = byte(r.IntN(4))
				}
				a = netip.AddrFrom4(b)
			} else {
				var b [16]byte
				for j := range b {
					b[j] = byte(r.IntN(3))
				}
				a = netip.AddrFrom16(b)
			}
			want := false
			for _, p := range ps {
				want = want || p.Contains(a)
			}
			if got := s.contains(a); got != want {
				t.Fatalf("round %d: contains(%s) = %v, want %v; prefixes %v", round, a, got, want, ps)
			}
		}
	}
}

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drop.txt")
	content := "; a list\n203.0.113.0/24 ; SBL1\n\n  198.51.100.7  # one\n2001:db8::/32\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	files := map[string][sha256.Size]byte{}
	l, err := Compile(&config.Access{DenyFiles: []string{path}, Allow: []string{"198.51.100.0/24"}}, files)
	if err != nil {
		t.Fatal(err)
	}
	if files[path] != sha256.Sum256([]byte(content)) {
		t.Error("the file's sum is not recorded")
	}
	p := Chain(l)
	for a, want := range map[string]bool{"203.0.113.9": false, "198.51.100.7": false, "198.51.100.8": true, "2001:db8::1": false, "192.0.2.1": false} {
		if got := p.Allows(addr(a)); got != want {
			t.Errorf("Allows(%s) = %v, want %v", a, got, want)
		}
	}

	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte("10.0.0.0/8\nnot-an-ip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(&config.Access{AllowFiles: []string{bad}}, nil); err == nil || !strings.Contains(err.Error(), "bad.txt:2") {
		t.Errorf("bad file: %v", err)
	}
	if _, err := Compile(&config.Access{AllowFiles: []string{filepath.Join(dir, "missing")}}, nil); err == nil {
		t.Error("missing file: no error")
	}
}

func TestCompileErrors(t *testing.T) {
	_, err := Compile(&config.Access{Allow: []string{"10.0.0.0/33"}, Deny: []string{"x"}}, nil)
	es := config.AsErrors(err)
	if len(es) != 2 {
		t.Fatalf("errors = %v, want 2", err)
	}
}

func TestGate(t *testing.T) {
	var g Gate
	peer := netip.MustParseAddrPort("203.0.113.1:5000")
	if !g.Allows(peer) {
		t.Error("zero gate refuses")
	}
	g.Set(Chain(list(t, nil, []string{"203.0.113.0/24"})))
	if g.Allows(peer) {
		t.Error("gate allows a denied peer")
	}
	g.Set(nil)
	if !g.Allows(peer) {
		t.Error("cleared gate refuses")
	}
	var nilGate *Gate
	if !nilGate.Allows(peer) {
		t.Error("nil gate refuses")
	}
}

func BenchmarkAllows(b *testing.B) {
	for _, n := range []int{10, 1000, 100000} {
		var deny []string
		for i := 0; i < n; i++ {
			deny = append(deny, fmt.Sprintf("%d.%d.%d.0/24", 1+i>>16&0xff, i>>8&0xff, i&0xff))
		}
		p := Chain(list(b, nil, deny))
		a := addr("192.0.2.1")
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if !p.Allows(a) {
					b.Fatal("refused")
				}
			}
		})
	}
}

// Check and Files, what a role's app.Info.Check and CertFiles run on its
// own access.
func TestCheckAndFiles(t *testing.T) {
	if Check(nil) != nil || Files(nil) != nil {
		t.Fatal("a nil access")
	}
	if err := Check(&config.Access{Deny: []string{"10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	a := &config.Access{AllowFiles: []string{filepath.Join(t.TempDir(), "none")}, DenyFiles: []string{"d"}}
	if err := Check(a); err == nil {
		t.Fatal("checked a file that does not exist")
	}
	if f := Files(a); len(f) != 2 || f[0] != a.AllowFiles[0] || f[1] != "d" {
		t.Fatal(f)
	}
	// Files gives a list of its own.
	Files(a)[0] = "x"
	if a.AllowFiles[0] == "x" {
		t.Fatal("Files shares the member's slice")
	}
}
