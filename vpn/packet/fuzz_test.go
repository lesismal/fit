package packet

import "testing"

// FuzzParse checks that Parse never panics, and that what it takes is
// consistent with the bytes it was given.
func FuzzParse(f *testing.F) {
	f.Add(ipv4("100.96.0.1", "100.96.0.2", 6, 20, 40))
	f.Add(ipv4("10.0.0.1", "10.0.0.2", 17, 60, 8))
	f.Add(ipv6("fd7a::1", "fd7a::2", 17, 100))
	f.Add(ipv6("fd7a::1", "fd7a::2", 59, 0))
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := Parse(b)
		if err != nil {
			return
		}
		if h.Len > len(b) {
			t.Fatalf("length %d past the %d bytes given", h.Len, len(b))
		}
		switch h.Version {
		case 4:
			if !h.Src.Is4() || !h.Dst.Is4() || h.Len < ipv4HeaderSize {
				t.Fatalf("IPv4 header %+v", h)
			}
		case 6:
			if !h.Src.Is6() || !h.Dst.Is6() || h.Len < ipv6HeaderSize {
				t.Fatalf("IPv6 header %+v", h)
			}
		default:
			t.Fatalf("version %d", h.Version)
		}
		if again, err := Parse(b[:h.Len]); err != nil || again != h {
			t.Fatalf("the packet cut to its length parses as %+v, %v", again, err)
		}
	})
}
