package runtime

import (
	"maps"
	stdhttp "net/http"
	"net/http/httptest"
	"slices"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
)

func TestConnectionFirst(t *testing.T) {
	var seen stdhttp.Header
	h := connectionFirst(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		seen = c.Request.Header.Clone()
		_ = c.Respond(204, "", nil)
	}))
	for _, tt := range []struct {
		name     string
		in, want stdhttp.Header
	}{
		{"none", stdhttp.Header{"X-A": {"1"}}, stdhttp.Header{"X-A": {"1"}}},
		{"options", stdhttp.Header{"Connection": {"keep-alive", "Close"}, "Keep-Alive": {"timeout=5"}},
			stdhttp.Header{"Connection": {"keep-alive", "Close"}, "Keep-Alive": {"timeout=5"}}},
		{"a field", stdhttp.Header{"Connection": {"x-hop"}, "X-Hop": {"1"}, "X-A": {"1"}},
			stdhttp.Header{"X-A": {"1"}}},
		{"an upgrade", stdhttp.Header{"Connection": {"Upgrade, HTTP2-Settings, X-Hop"}, "Upgrade": {"websocket"}, "Http2-Settings": {"x"}, "X-Hop": {"1"}},
			stdhttp.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}},
		{"many", stdhttp.Header{"Connection": {" , te ,x-a", "TRAILER, close,Proxy-Connection,transfer-encoding,keep-alive"}, "X-A": {"1"}, "Te": {"trailers"}},
			stdhttp.Header{"Connection": {"te, TRAILER, close, Proxy-Connection, transfer-encoding, keep-alive"}, "Te": {"trailers"}}},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header = tt.in
		if rec := fibtest.ServeRequest(h, r); rec.Response.StatusCode != 204 {
			t.Fatalf("%s: %d", tt.name, rec.Response.StatusCode)
		}
		if !maps.EqualFunc(seen, tt.want, slices.Equal) {
			t.Errorf("%s: next saw %v, want %v", tt.name, seen, tt.want)
		}
	}

	// The common case, no field named, allocates nothing.
	for _, connection := range [][]string{nil, {"keep-alive"}, {"close"}, {"Upgrade"}} {
		if allocs := testing.AllocsPerRun(100, func() {
			if namesFields(connection) {
				t.Fatal("names a field")
			}
		}); allocs != 0 {
			t.Errorf("%q: %v allocations", connection, allocs)
		}
	}
}
