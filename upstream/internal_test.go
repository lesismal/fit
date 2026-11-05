package upstream

import (
	"errors"
	"net"
	stdhttp "net/http"
	"sync/atomic"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
)

// TestDenyInternal refuses, with deny_internal, a backend on loopback, by
// address and by a name that resolves there, as the upstream of an
// ExternalName Service pointed at the Admin API would be: the request
// fails with ErrInternal and never reaches it, and so does a tcp probe's
// connection. Without it, the same backend is served.
func TestDenyInternal(t *testing.T) {
	var reached atomic.Int64
	b := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		reached.Add(1)
		_, _ = w.Write([]byte("admin"))
	})
	_, port, _ := net.SplitHostPort(b.addr)
	for _, addr := range []string{b.addr, "localhost:" + port} {
		p := newPool(t, `{"addresses": ["`+addr+`"], "deny_internal": true, "max_fails": 0}`)
		if got := do(t, p, "/load"); !errors.Is(got.err, ErrInternal) {
			t.Errorf("%s: %+v, want ErrInternal", addr, got)
		}
		done := make(chan error, 1)
		if err := p.Dial(startEngine(t), "tcp", addr, time.Second, discard{}, func(c *fib.Connection, err error) {
			done <- err
		}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrInternal) {
			t.Errorf("%s: a dial got %v, want ErrInternal", addr, err)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("%d requests reached the backend", n)
	}
	p := newPool(t, `{"addresses": [%s]}`, b)
	if got := do(t, p, "/"); got.err != nil || got.body != "admin" {
		t.Fatalf("without deny_internal: %+v", got)
	}
}

// TestDenyInternalProbe checks an http health probe of an upstream with
// deny_internal dials as its requests do: one on loopback never reaches the
// backend, and the backend is probed down.
func TestDenyInternalProbe(t *testing.T) {
	h := newHealthy(t, "h")
	p := newPool(t, `{"addresses": [%s], "deny_internal": true, `+probeCheck+`}`, h.backend)
	waitDown(t, p.Backends()[0], true)
	if n := h.probes.Load(); n != 0 {
		t.Errorf("%d probes reached the backend", n)
	}
}
