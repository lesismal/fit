package l4

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/upstream"
)

// TestHelloServerNameMalformed reads ClientHello bodies cut short or with
// lengths that do not add up, and server_name lists without a host name.
func TestHelloServerNameMalformed(t *testing.T) {
	head := make([]byte, 2+32) // the version and random
	base := append(append([]byte(nil), head...), 0, 0, 2, 0x13, 0x01, 1, 0)
	exts := func(e ...byte) []byte {
		b := append([]byte(nil), base...)
		b = append(b, byte(len(e)>>8), byte(len(e)))
		return append(b, e...)
	}
	sni := func(list ...byte) []byte {
		body := append([]byte{byte(len(list) >> 8), byte(len(list))}, list...)
		return exts(append([]byte{0, 0, byte(len(body) >> 8), byte(len(body))}, body...)...)
	}
	for _, tc := range []struct {
		name  string
		hello []byte
		want  string
		err   bool
	}{
		{"no cipher suites length", append(append([]byte(nil), head...), 0), "", true},
		{"session id cut short", append(append([]byte(nil), head...), 5, 1), "", true},
		{"no extensions", base, "", false},
		{"one byte of extensions", append(append([]byte(nil), base...), 0), "", true},
		{"extension past the end", exts(0, 0, 0, 16), "", true},
		{"server_name list length wrong", exts(0, 0, 0, 3, 0, 5, 0), "", true},
		{"name past the end", sni(0, 0, 5, 'a'), "", true},
		{"another kind, then a host name", sni(1, 0, 1, 'x', 0, 0, 1, 'A'), "a", false},
		{"no host name", sni(1, 0, 1, 'x'), "", false},
	} {
		got, err := helloServerName(tc.hello)
		if got != tc.want || (err != nil) != tc.err {
			t.Errorf("%s: %q, %v", tc.name, got, err)
		}
	}
}

// TestProxyV2Unknown writes a v2 header for addresses of no one family.
func TestProxyV2Unknown(t *testing.T) {
	got := appendProxy(nil, 2, netip.AddrPort{}, netip.MustParseAddrPort("10.0.0.1:1"))
	want := append(append([]byte(nil), proxyV2Sig...), 0x21, 0x00, 0, 0)
	if string(got) != string(want) {
		t.Fatalf("%x, want %x", got, want)
	}
	if n, src, _, err := parseProxy(got); n != len(got) || src.IsValid() || err != nil {
		t.Fatalf("parsed: %d, %v, %v", n, src, err)
	}
}

func TestServiceName(t *testing.T) {
	pool, err := upstream.New(nil, &config.Upstream{Name: "up", Addresses: []string{"127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := NewService(&config.Service{Name: "svc"}, pool, nil); s.Name() != "svc" {
		t.Fatalf("name %q", s.Name())
	}
}

// TestRulesNamesOnly decides destinations by rules of names alone: an
// allowed name needs no address check, another is refused, as Dial refuses
// it without dialing.
func TestRulesNamesOnly(t *testing.T) {
	r := NewRules(&config.ForwardProxy{Clients: []string{"0.0.0.0/0"}, Allow: []string{"example.com"}})
	for dest, want := range map[string]verdict{"example.com:443": allowed, "other.example:443": refused} {
		if got := r.dest(dest); got != want {
			t.Errorf("dest(%q) = %d, want %d", dest, got, want)
		}
	}
	if err := r.Dial(nil, "tcp", "other.example:443", time.Second, nil, nil); !errors.Is(err, ErrRefused) {
		t.Errorf("Dial of a refused destination: %v", err)
	}
}
