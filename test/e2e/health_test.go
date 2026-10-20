//go:build !windows

package e2e

import (
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestHealthCheckRotation is M2's third exit criterion: an upstream address
// that stops answering its probes leaves rotation within the configured
// window and comes back once it answers again, with no request failing
// meanwhile. The fit binary proxies to two backends with an HTTP health
// check; the second's probe path stops answering (it hangs, so the probe
// times out) while its other requests are still served, so that only the
// active health check can take it out.
func TestHealthCheckRotation(t *testing.T) {
	var hang atomic.Bool
	backend := func(name string, probed func() bool) string {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &stdhttp.Server{Handler: stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if r.URL.Path == "/health" && !probed() {
				<-r.Context().Done() // never answers; the probe times out
				return
			}
			_, _ = io.WriteString(w, name)
		})}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close() })
		return ln.Addr().String()
	}
	a := backend("a", func() bool { return true })
	b := backend("b", func() bool { return !hang.Load() })

	const (
		interval = 200 * time.Millisecond
		timeout  = 100 * time.Millisecond
		fails    = 2
		passes   = 2
	)
	_, addrs := startFit(t, fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "app", "addresses": [%q, %q],
    "health_check": {"path": "/health", "interval": %q, "timeout": %q, "jitter": "1ms", "fails": %d, "passes": %d}}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`, a, b, interval.String(), timeout.String(), fails, passes))
	web := "http://" + addrs["web"] + "/"
	c := &stdhttp.Client{Timeout: 5 * time.Second}
	get := func() string {
		t.Helper()
		resp, err := c.Get(web)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("a request failed: %d %q", resp.StatusCode, body)
		}
		return string(body)
	}
	// until sends requests until done says so of the backend that answered
	// the last, and returns how long that took, at most limit.
	until := func(what string, limit time.Duration, done func(seen string) bool) time.Duration {
		t.Helper()
		start := time.Now()
		for !done(get()) {
			if time.Since(start) > limit {
				t.Fatalf("%s: not within %v", what, limit)
			}
			time.Sleep(5 * time.Millisecond)
		}
		return time.Since(start)
	}

	until("both in rotation", 5*time.Second, func(seen string) bool { return seen == "b" })

	// The window: fails probes, each started an interval apart and failing
	// once its timeout passes, plus the jitter, and a probe interval of
	// slack for when the hang began within one.
	window := fails*interval + timeout + interval
	hang.Store(true)
	var run int
	took := until("out of rotation", 10*window, func(seen string) bool {
		if seen == "b" {
			run = 0
			return false
		}
		run++
		return run == 20
	})
	// The last of the 20 requests to a alone went out after b left; the
	// first may have.
	left := took - 20*5*time.Millisecond
	if left > window+time.Second {
		t.Errorf("left rotation after about %v, past the window of %v", left, window)
	}
	for range 20 {
		if get() == "b" {
			t.Fatal("an address out of rotation was sent a request")
		}
	}

	back := passes*interval + interval
	hang.Store(false)
	returned := until("back in rotation", 10*back, func(seen string) bool { return seen == "b" })
	if returned > back+time.Second {
		t.Errorf("came back after %v, past the window of %v", returned, back)
	}
	t.Logf("left within about %v (window %v), came back after %v (window %v)", left, window, returned, back)
}
