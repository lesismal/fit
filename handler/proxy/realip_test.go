package proxy

import (
	stdhttp "net/http"
	"strings"
	"testing"
)

// spoofed are forwarding fields a client may send that fit does not set.
var spoofed = map[string]string{
	"X-Real-Ip":          "203.0.113.9",
	"X-Forwarded-Port":   "443",
	"X-Forwarded-Prefix": "/admin",
	"X-Forwarded-Ssl":    "on",
	"X-Forwarded-Server": "evil.example",
	"X-Forwarded-Scheme": "https",
}

// TestRealIPUntrusted checks that a client not in trusted_proxies gets
// X-Real-IP set to its own address and its other forwarding fields dropped,
// on a route with no middleware, whose header passes on as its lines over
// HTTP/1 and as a map over h2c, and on one with middleware, whose own
// fields are kept.
func TestRealIPUntrusted(t *testing.T) {
	addr := echoBackend(t, "http/1.1")
	bare := startFit(t, config1(`{"upstream": "app"}`), addr)
	wrapped := startFit(t, strings.Replace(config1(`{"upstream": "app"}`), `"routes": [{`,
		`"routes": [{"middleware": [{"module": "headers", "params": {"request": {"set": {"X-Forwarded-Prefix": "/app"}}}}], `, 1), addr)
	for _, base := range []string{bare, wrapped} {
		for _, h2c := range []bool{false, true} {
			req, _ := stdhttp.NewRequest("GET", base+"/", nil)
			for k, v := range spoofed {
				req.Header.Set(k, v)
			}
			// In any case, over HTTP/1.
			req.Header["x-real-ip"] = []string{"198.51.100.7"}
			_, e := send(t, client(h2c), req)
			if got := strings.Join(e.Header["X-Real-Ip"], " | "); got != "127.0.0.1" {
				t.Errorf("wrapped=%v h2c=%v: X-Real-IP %q, want 127.0.0.1", base == wrapped, h2c, got)
			}
			for name := range spoofed {
				want := ""
				switch {
				case name == "X-Real-Ip":
					continue
				case name == "X-Forwarded-Prefix" && base == wrapped:
					want = "/app"
				}
				if got := strings.Join(e.Header[name], " | "); got != want {
					t.Errorf("wrapped=%v h2c=%v: %s %q, want %q", base == wrapped, h2c, name, got, want)
				}
			}
		}
	}
}

// TestRealIPTrusted checks that a trusted proxy's forwarding fields go on,
// and that X-Real-IP is set to its address when it sends none.
func TestRealIPTrusted(t *testing.T) {
	base := startFit(t, config1(`{"upstream": "app", "trusted_proxies": ["127.0.0.1"]}`), echoBackend(t, "http/1.1"))
	for _, h2c := range []bool{false, true} {
		req, _ := stdhttp.NewRequest("GET", base+"/", nil)
		for k, v := range spoofed {
			req.Header.Set(k, v)
		}
		_, e := send(t, client(h2c), req)
		for name, want := range spoofed {
			if got := strings.Join(e.Header[name], " | "); got != want {
				t.Errorf("h2c=%v: %s %q, want %q", h2c, name, got, want)
			}
		}
		req, _ = stdhttp.NewRequest("GET", base+"/", nil)
		if _, e := send(t, client(h2c), req); e.Header.Get("X-Real-Ip") != "127.0.0.1" {
			t.Errorf("h2c=%v: X-Real-IP %q, want 127.0.0.1", h2c, e.Header.Get("X-Real-Ip"))
		}
	}
}
