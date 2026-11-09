package access

import (
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

func TestRangeOf(t *testing.T) {
	for p, want := range map[string]string{
		"10.0.0.0/8":          "10.0.0.0-10.255.255.255",
		"10.1.2.3/32":         "10.1.2.3-10.1.2.3",
		"0.0.0.0/0":           "0.0.0.0-255.255.255.255",
		"10.1.2.3/20":         "10.1.0.0-10.1.15.255",
		"2001:db8::/32":       "2001:db8::-2001:db8:ffff:ffff:ffff:ffff:ffff:ffff",
		"::/0":                "::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
		"2001:db8::1/128":     "2001:db8::1-2001:db8::1",
		"::ffff:10.0.0.0/104": "10.0.0.0-10.255.255.255",
	} {
		r := RangeOf(netip.MustParsePrefix(p))
		if got := r.Lo.String() + "-" + r.Hi.String(); got != want {
			t.Errorf("RangeOf(%s) = %s, want %s", p, got, want)
		}
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		line   string
		want   string // "" for a line that lists nothing
		hasErr bool
	}{
		{"", "", false},
		{"   ", "", false},
		{"# a comment", "", false},
		{"; a comment", "", false},
		{"// a comment", "", false},
		{"203.0.113.7", "203.0.113.7-203.0.113.7", false},
		{"203.0.113.0/24 ; SBL", "203.0.113.0-203.0.113.255", false},
		{"2001:db8::/126 # x", "2001:db8::-2001:db8::3", false},
		{"203.0.113.1-203.0.113.9", "203.0.113.1-203.0.113.9", false},
		{"203.0.113.1 - 203.0.113.9", "203.0.113.1-203.0.113.9", false},
		{"2001:db8::1-2001:db8::ff", "2001:db8::1-2001:db8::ff", false},
		// ipfilter.dat, leading zeros, levels.
		{"001.002.003.004 - 001.002.003.255 , 000 , Some ISP, Inc", "1.2.3.4-1.2.3.255", false},
		{"1.2.3.4 - 1.2.3.5 , 100 , x", "1.2.3.4-1.2.3.5", false},
		{"1.2.3.4 - 1.2.3.5 , 127 , at the bound", "1.2.3.4-1.2.3.5", false},
		{"1.2.3.4 - 1.2.3.5 , 128 , allowed", "", false},
		{"1.2.3.4 - 1.2.3.5 , 255 , allowed", "", false},
		{"1.2.3.4 - 1.2.3.5 , 256 , x", "", true},
		{"1.2.3.4 - 1.2.3.5 , high , x", "", true},
		{"1.2.3.4 - 1.2.3.5,0", "1.2.3.4-1.2.3.5", false},
		// .p2p, names with colons, commas, dashes and '#'.
		{"Bad Corp:1.2.3.4-1.2.3.255", "1.2.3.4-1.2.3.255", false},
		{"Bad: Corp, Inc. #2:5.6.7.8-5.6.7.9", "5.6.7.8-5.6.7.9", false},
		{"Foo-Bar, Ltd:9.9.9.0-9.9.9.255", "9.9.9.0-9.9.9.255", false},
		{"Leading:010.000.000.001-010.000.000.002", "10.0.0.1-10.0.0.2", false},
		// Bad lines.
		{"not-an-ip", "", true},
		{"1.2.3.4-1.2.3", "", true},
		{"1.2.3.9-1.2.3.4", "", true},
		{"1.2.3.4-2001:db8::1", "", true},
		{"1.2.3.256", "", true},
		{"1.2.3.4 - 1.2.3.0 , 0 , backwards", "", true},
		{"name:1.2.3.4", "", true},
		{"1.2.3.0001-1.2.3.5", "", true},
		{"fe80::1%eth0-fe80::2", "", true},
	}
	for _, tt := range tests {
		r, ok, err := parseLine(tt.line)
		if (err != nil) != tt.hasErr {
			t.Errorf("parseLine(%q) error = %v, want error %v", tt.line, err, tt.hasErr)
			continue
		}
		got := ""
		if ok {
			got = r.Lo.String() + "-" + r.Hi.String()
		}
		if got != tt.want {
			t.Errorf("parseLine(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

// The .p2p and .dat formats in deny_files and allow_files, mixed with the
// plain format, through Compile and a Policy, as every module reads them.
func TestP2PAndDATFiles(t *testing.T) {
	dir := t.TempDir()
	p2p := filepath.Join(dir, "level1.p2p")
	writeFile(t, p2p, "# PeerGuardian list\nSome Org:203.0.113.10-203.0.113.20\nOther, Inc:198.51.100.0-198.51.100.255\n\n")
	dat := filepath.Join(dir, "ipfilter.dat")
	writeFile(t, dat, "// eMule\r\n192.000.002.000 - 192.000.002.127 , 000 , Blocked\r\n192.000.002.128 - 192.000.002.255 , 200 , Not blocked\r\n")
	mixed := filepath.Join(dir, "mixed.txt")
	writeFile(t, mixed, "10.0.0.0/8\n2001:db8::1-2001:db8::9\nX:172.16.0.1-172.16.0.1\n")
	l, err := Compile(&config.Access{DenyFiles: []string{p2p, dat, mixed}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := Chain(l)
	for a, want := range map[string]bool{
		"203.0.113.9": true, "203.0.113.10": false, "203.0.113.20": false, "203.0.113.21": true,
		"198.51.100.77": false, "192.0.2.0": false, "192.0.2.127": false, "192.0.2.128": true,
		"10.9.9.9": false, "2001:db8::5": false, "2001:db8::a": true, "172.16.0.1": false, "172.16.0.2": true,
		"::ffff:203.0.113.15": false,
	} {
		if got := p.Allows(netip.MustParseAddr(a)); got != want {
			t.Errorf("Allows(%s) = %v, want %v", a, got, want)
		}
	}

	// An allow list of .p2p ranges serves only them.
	l, err = Compile(&config.Access{AllowFiles: []string{p2p}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := Chain(l); !p.Allows(netip.MustParseAddr("203.0.113.15")) || p.Allows(netip.MustParseAddr("8.8.8.8")) {
		t.Error("the .p2p allow list does not decide")
	}

	// A bad line names its file and line.
	bad := filepath.Join(dir, "bad.dat")
	writeFile(t, bad, "1.2.3.4 - 1.2.3.5 , 0 , ok\n1.2.3.4 - 1.2.3.5 , x , bad level\n")
	if _, err := Compile(&config.Access{DenyFiles: []string{bad}}, nil); err == nil || !strings.Contains(err.Error(), "bad.dat:2") {
		t.Errorf("a bad .dat line: %v", err)
	}
	long := filepath.Join(dir, "long.p2p")
	writeFile(t, long, strings.Repeat("x", maxLineBytes+1)+":1.2.3.4-1.2.3.5\n")
	if _, err := Compile(&config.Access{DenyFiles: []string{long}}, nil); err == nil {
		t.Error("a line past maxLineBytes was read")
	}
}

// Ranges against a linear check: a set made of random ranges holds what
// they hold.
func TestRangesAgainstLinear(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for round := 0; round < 50; round++ {
		var rs []Range
		for i := 0; i < 1+r.IntN(30); i++ {
			a := netip.AddrFrom4([4]byte{10, 0, byte(r.IntN(4)), byte(r.IntN(256))})
			b := netip.AddrFrom4([4]byte{10, 0, byte(r.IntN(4)), byte(r.IntN(256))})
			if b.Less(a) {
				a, b = b, a
			}
			rs = append(rs, Range{a, b})
		}
		s := newSet(append([]Range(nil), rs...))
		for i := 0; i < 2000; i++ {
			a := netip.AddrFrom4([4]byte{10, 0, byte(r.IntN(5)), byte(r.IntN(256))})
			want := false
			for _, rg := range rs {
				want = want || !a.Less(rg.Lo) && !rg.Hi.Less(a)
			}
			if got := s.contains(a); got != want {
				t.Fatalf("round %d: contains(%s) = %v, want %v", round, a, got, want)
			}
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
