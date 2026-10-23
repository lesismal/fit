package upstream

import (
	"errors"
	"io"
	stdhttp "net/http"
	"testing"
	"time"
)

// TestDefaultMaxConns reaches the default of 256 connections to an address
// and max_pending past it: with every request held by the backend, 256
// connections open, the next max_pending requests wait, and the one after
// them is refused at once. The 256 go at once: a request waiting for the
// connection being opened for it does not count against max_pending.
func TestDefaultMaxConns(t *testing.T) {
	release := make(chan struct{})
	b := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		<-release
		_, _ = io.WriteString(w, "ok")
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	p := newPool(t, `{"addresses": [%s], "max_pending": 8}`, b)
	var results []<-chan result
	for range DefaultMaxConns {
		results = append(results, goDo(t, p, "/"))
	}
	deadline := time.Now().Add(10 * time.Second)
	for b.open.Load() < DefaultMaxConns {
		if time.Now().After(deadline) {
			t.Fatalf("%d connections open, want %d", b.open.Load(), DefaultMaxConns)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // every request is on its connection
	for range 8 {
		results = append(results, goDo(t, p, "/"))
	}
	time.Sleep(100 * time.Millisecond)
	if got := <-goDo(t, p, "/"); !errors.Is(got.err, ErrBusy) {
		t.Fatalf("one past max_conns and max_pending: %+v", got)
	}
	time.Sleep(100 * time.Millisecond)
	if n := b.maxOpen.Load(); n != DefaultMaxConns {
		t.Fatalf("%d connections at most, want %d", n, DefaultMaxConns)
	}
	close(release)
	for i, ch := range results {
		select {
		case got := <-ch:
			if got.err != nil || got.body != "ok" {
				t.Fatalf("request %d: %+v", i, got)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("request %d of %d never finished; %d connections open, %d in flight", i, len(results), b.open.Load(), p.inFlight.Load())
		}
	}
	// With the requests done, there is room again.
	if got := do(t, p, "/"); got.err != nil {
		t.Fatal(got.err)
	}
}

func TestResponseTimeout(t *testing.T) {
	b := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	p := newPool(t, `{"addresses": [%s], "response_timeout": "300ms"}`, b)
	start := time.Now()
	got := do(t, p, "/")
	if got.err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("after %v: %+v", time.Since(start), got)
	}
}

// TestSaturated: an HTTP/1.1 pool is saturated once it holds max_conns and
// max_pending requests for each backend, and is not once one is done; an
// HTTP/2 pool, whose connections carry many requests each, never says so.
func TestSaturated(t *testing.T) {
	c, err := decode(t, `{"addresses": ["127.0.0.1:1", "127.0.0.1:2"], "max_conns": 2, "max_pending": 1}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(nil, c)
	if err != nil {
		t.Fatal(err)
	}
	var picked []*Backend
	for range 5 {
		picked = append(picked, p.Pick(nil))
		if p.Saturated() {
			t.Fatalf("saturated with %d requests in flight, want 6", len(picked))
		}
	}
	b := p.Pick(nil)
	if !p.Saturated() {
		t.Fatal("not saturated with 6 requests in flight")
	}
	p.Done(b, false)
	if p.Saturated() {
		t.Fatal("still saturated once one is done")
	}
	for _, b := range picked {
		p.Done(b, false)
	}

	c, err = decode(t, `{"addresses": ["127.0.0.1:1"], "protocol": "h2c", "max_conns": 1, "max_pending": 1}`)
	if err != nil {
		t.Fatal(err)
	}
	if p, err = New(nil, c); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		p.Pick(nil)
	}
	if p.Saturated() {
		t.Fatal("an h2c pool says it is saturated")
	}
}
