package l4

import (
	"crypto/tls"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
)

// clientHello is the first flight of a crypto/tls client asking for name.
func clientHello(t *testing.T, name string) []byte {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		_ = tls.Client(a, &tls.Config{ServerName: name, InsecureSkipVerify: true}).Handshake()
	}()
	buf := make([]byte, 64<<10)
	n, err := b.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

// splitRecords puts the handshake a single record carries into records of
// at most size bytes each.
func splitRecords(record []byte, size int) []byte {
	var out []byte
	for body := record[5:]; len(body) > 0; {
		n := min(size, len(body))
		out = append(out, 0x16, 3, 1, byte(n>>8), byte(n))
		out = append(out, body[:n]...)
		body = body[n:]
	}
	return out
}

func TestReadSNI(t *testing.T) {
	hello := clientHello(t, "Example.COM")
	for _, tc := range []struct {
		name string
		data []byte
		want string
		done bool
		err  bool
	}{
		{"whole", hello, "example.com", true, false},
		{"in records of 100 bytes", splitRecords(hello, 100), "example.com", true, false},
		{"cut short", hello[:len(hello)-1], "", false, false},
		{"its header alone", hello[:3], "", false, false},
		{"no name", clientHello(t, ""), "", true, false},
		{"not TLS", []byte("GET / HTTP/1.1\r\n\r\n"), "", false, true},
	} {
		name, done, err := readSNI(tc.data)
		if name != tc.want || done != tc.done || (err != nil) != tc.err {
			t.Errorf("%s: %q, %v, %v; want %q, %v, error %v", tc.name, name, done, err, tc.want, tc.done, tc.err)
		}
	}
	// No prefix of a hello is taken for a whole one, nor fails.
	for i := range hello {
		if name, done, err := readSNI(hello[:i]); done || err != nil {
			t.Fatalf("the first %d bytes: %q, %v, %v", i, name, done, err)
		}
	}
}

func FuzzReadSNI(f *testing.F) {
	f.Add([]byte{0x16, 3, 1, 0, 4, 1, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) { _, _, _ = readSNI(data) })
}

func TestProxyRoundTrip(t *testing.T) {
	for _, tc := range []struct{ src, dst string }{
		{"203.0.113.9:5555", "198.51.100.1:443"},
		{"[2001:db8::1]:5555", "[2001:db8::2]:443"},
	} {
		src, dst := netip.MustParseAddrPort(tc.src), netip.MustParseAddrPort(tc.dst)
		for _, v := range []int{1, 2} {
			header := appendProxy(nil, v, src, dst)
			data := append(header, "rest"...)
			n, gotSrc, gotDst, err := parseProxy(data)
			if err != nil || n != len(header) || gotSrc != src || gotDst != dst {
				t.Errorf("v%d %s: %d, %v, %v, %v; want %d", v, tc.src, n, gotSrc, gotDst, err, len(header))
			}
			for i := range header {
				if n, _, _, err := parseProxy(header[:i]); n != 0 || err != nil {
					t.Errorf("v%d %s: the first %d bytes: %d, %v", v, tc.src, i, n, err)
				}
			}
		}
	}
	if got := string(appendProxy(nil, 1, netip.MustParseAddrPort("[::ffff:10.0.0.1]:1"), netip.MustParseAddrPort("10.0.0.2:2"))); got != "PROXY TCP4 10.0.0.1 10.0.0.2 1 2\r\n" {
		t.Errorf("v1 of a mapped address: %q", got)
	}
	if got := string(appendProxy(nil, 1, netip.MustParseAddrPort("10.0.0.1:1"), netip.MustParseAddrPort("[::1]:2"))); got != "PROXY UNKNOWN\r\n" {
		t.Errorf("v1 of mixed families: %q", got)
	}
}

func TestParseProxy(t *testing.T) {
	local := append(append([]byte(nil), proxyV2Sig...), 0x20, 0x00, 0, 0)
	unix := append(append([]byte(nil), proxyV2Sig...), 0x21, 0x31, 0, 216)
	unix = append(unix, make([]byte, 216)...)
	tlvs := appendProxy(nil, 2, netip.MustParseAddrPort("10.0.0.1:1"), netip.MustParseAddrPort("10.0.0.2:2"))
	binary.BigEndian.PutUint16(tlvs[14:], 12+7)
	tlvs = append(tlvs, 0x02, 0, 4, 'h', 'o', 's', 't')
	for _, tc := range []struct {
		name string
		data string
		n    int
		src  string
		err  bool
	}{
		{"v1 unknown", "PROXY UNKNOWN 1 2 3 4\r\nGET", 23, "", false},
		{"v1 TCP6", "PROXY TCP6 ::1 ::2 1 2\r\n", 24, "[::1]:1", false},
		{"v1 family mismatch", "PROXY TCP4 ::1 ::2 1 2\r\n", 0, "", true},
		{"v1 bad port", "PROXY TCP4 1.2.3.4 1.2.3.5 70000 2\r\n", 0, "", true},
		{"v1 too long", "PROXY TCP4 " + string(make([]byte, 120)), 0, "", true},
		{"v1 misspelt", "PROXI ", 0, "", true},
		{"v2 local", string(local), 16, "", false},
		{"v2 unix", string(unix), 16 + 216, "", false},
		{"v2 with a TLV", string(tlvs), 16 + 12 + 7, "10.0.0.1:1", false},
		{"v2 version 3", string(proxyV2Sig) + "\x31\x11\x00\x0c", 0, "", true},
		{"v2 too short for IPv4", string(proxyV2Sig) + "\x21\x11\x00\x04abcd", 0, "", true},
		{"neither", "GET / HTTP/1.1\r\n", 0, "", true},
	} {
		n, src, _, err := parseProxy([]byte(tc.data))
		got := ""
		if src.IsValid() {
			got = src.String()
		}
		if n != tc.n || got != tc.src || (err != nil) != tc.err {
			t.Errorf("%s: %d, %q, %v; want %d, %q, error %v", tc.name, n, got, err, tc.n, tc.src, tc.err)
		}
	}
}

func FuzzParseProxy(f *testing.F) {
	f.Add([]byte("PROXY TCP4 1.2.3.4 5.6.7.8 1 2\r\n"))
	f.Add(appendProxy(nil, 2, netip.MustParseAddrPort("10.0.0.1:1"), netip.MustParseAddrPort("10.0.0.2:2")))
	f.Fuzz(func(t *testing.T, data []byte) {
		n, _, _, err := parseProxy(data)
		if n < 0 || n > len(data) || err != nil && n != 0 {
			t.Fatalf("%d, %v for %d bytes", n, err, len(data))
		}
	})
}

func TestEntryPick(t *testing.T) {
	a, w, f := &Service{name: "a"}, &Service{name: "w"}, &Service{name: "f"}
	e := NewEntry(nil)
	e.Add(a, []string{"A.example.com"})
	e.Add(w, []string{"*.w.example.com"})
	e.Add(f, nil)
	for name, want := range map[string]*Service{
		"a.example.com": a, "x.w.example.com": w, "w.example.com": f, "y.x.w.example.com": f, "": f, "b.example.com": f,
	} {
		if got := e.pick(name); got != want {
			t.Errorf("pick(%q) = %s, want %s", name, got.name, want.name)
		}
	}
}
