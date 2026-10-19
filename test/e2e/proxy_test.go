//go:build !windows

package e2e

import (
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyForwards(t *testing.T) {
	backend := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Seen-Host", r.Host)
		w.Header().Set("X-Seen-For", r.Header.Get("X-Forwarded-For"))
		_, _ = io.WriteString(w, r.Method+" "+r.RequestURI+" "+string(body))
	}))
	t.Cleanup(backend.Close)
	f, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [{"name": "app", "addresses": ["`+backend.Listener.Addr().String()+`"]}],
  "routes": [{"match": {"path": ["/api/*"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`)
	for _, h2c := range []bool{false, true} {
		resp, err := client(h2c).Post("http://"+addrs["web"]+"/api/x?q=1", "text/plain", strings.NewReader("ping"))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != "POST /api/x?q=1 ping" || resp.Header.Get("X-Seen-For") != "127.0.0.1" ||
			resp.Header.Get("X-Seen-Host") != addrs["web"] {
			t.Fatalf("h2c=%v: %d %q %v", h2c, resp.StatusCode, body, resp.Header)
		}
	}
	if code, out := f.stop(t); code != 0 {
		t.Fatalf("stop: exit %d, output %q", code, out)
	}
}
