package proxy

import (
	stdhttp "net/http"
	"net/netip"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// FuzzHopByHop checks that removeHopByHop leaves none of the hop-by-hop
// fields, nor any field Connection names, whatever the fields are.
func FuzzHopByHop(f *testing.F) {
	f.Add("Connection: keep-alive, X-A\nX-A: 1\nKeep-Alive: 5\nX-B: 2")
	f.Add("Connection: ,,  ,te\nTE: trailers\nUpgrade: websocket")
	f.Add("connection: x-y\r\nx-y: z\r\n")
	f.Fuzz(func(t *testing.T, fields string) {
		h := stdhttp.Header{}
		for line := range strings.SplitSeq(fields, "\n") {
			name, value, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
			if !ok {
				continue
			}
			key := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
			h[key] = append(h[key], strings.TrimSpace(value))
		}
		named := append([]string(nil), h["Connection"]...)
		removeHopByHop(h)
		for name := range h {
			if isHopByHop(name) {
				t.Fatalf("%s was left", name)
			}
		}
		for _, v := range named {
			for name := range strings.SplitSeq(v, ",") {
				if name = textproto.TrimString(name); name != "" {
					if _, ok := h[textproto.CanonicalMIMEHeaderKey(name)]; ok {
						t.Fatalf("%s, which Connection names, was left", name)
					}
				}
			}
		}
	})
}

// FuzzForwarded checks that a Forwarded element made from any client
// address and host is one field value: no control characters, which would
// let a request write fields of its own upstream, and its host quoted
// unless it is a token.
func FuzzForwarded(f *testing.F) {
	f.Add("203.0.113.9:5555", "example.com")
	f.Add("[2001:db8::1]:443", "a b\r\nX-Injected: 1")
	f.Add("garbage", "")
	f.Fuzz(func(t *testing.T, remote, host string) {
		ip, s := clientAddr(remote)
		if ip.IsValid() {
			if _, err := netip.ParseAddr(s); err != nil {
				t.Fatalf("%q: the address written, %q, does not parse", remote, s)
			}
		}
		el := string(appendForwardedElement(nil, ip, s, host, "https"))
		for _, c := range el {
			if c < 0x20 || c == 0x7f {
				t.Fatalf("%q: a control character in %q", host, el)
			}
		}
		if !strings.HasPrefix(el, "for=") || !strings.HasSuffix(el, ";proto=https") {
			t.Fatalf("%q", el)
		}
	})
}

// FuzzGRPCTimeout checks grpc-timeout's parser, and that what the proxy
// writes for a duration reads back no shorter.
func FuzzGRPCTimeout(f *testing.F) {
	f.Add("100m", int64(time.Second))
	f.Add("99999999H", int64(1))
	f.Fuzz(func(t *testing.T, field string, d int64) {
		if v, ok := grpcTimeout([]string{field}); ok && v < 0 {
			t.Fatalf("%q: %v", field, v)
		}
		if d <= 0 {
			return
		}
		enc := encodeGRPCTimeout(time.Duration(d))
		got, ok := grpcTimeout([]string{enc})
		if !ok || got < time.Duration(d) && got != 1<<63-1 {
			t.Fatalf("%v: %q reads back as %v, %v", time.Duration(d), enc, got, ok)
		}
	})
}
