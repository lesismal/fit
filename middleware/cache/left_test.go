package cache

import (
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/internal/filepool"
)

// endedRecorder is a Recorder told when its request has ended, every
// Retain of it released.
type endedRecorder struct {
	fibtest.Recorder
	ended chan struct{}
}

func (r *endedRecorder) RequestEnded() { close(r.ended) }

// leftContext returns the Context of a request for target whose client has left,
// which the test holds retained until it calls release.
func leftContext(t *testing.T, target string) (c *fibhttp.Context, rec *endedRecorder, release func()) {
	t.Helper()
	sr := &fibhttp.StreamRequest{Request: *httptest.NewRequest("GET", target, nil)}
	rec = &endedRecorder{ended: make(chan struct{})}
	c = sr.Context(nil, rec, nil)
	fibhttp.Serve(fibhttp.HandlerFunc(func(c *fibhttp.Context) { c.Retain() }), c)
	sr.Cancel(errors.New("gone"))
	waitFor(t, "the request cancelled", func() bool { return c.Err() != nil })
	return c, rec, c.Release
}

// TestDiskReadClientGone checks a response only the disk holds, asked for
// by a client that has left by the time the disk pool takes the read up:
// nothing is read or answered.
func TestDiskReadClientGone(t *testing.T) {
	m, d := diskCache(t, "diskgone", t.TempDir())
	var n int
	h := m.Wrap(answering(stdhttp.Header{"Cache-Control": {"max-age=60"}}, "x", &n))
	fibtest.Serve(h, "GET", "http://example.com/x")
	waitFor(t, "the file written", func() bool { c, _ := d.stats(); return c == 1 })
	memoryDrop(m, "http://example.com/x")
	c, rec, release := leftContext(t, "http://example.com/x")
	h.ServeHTTP(c)
	release()
	select {
	case <-rec.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the request did not end")
	}
	if rec.Written || n != 1 {
		t.Errorf("answered %v, %d fetches", rec.Written, n)
	}
}

// TestDrainExpired checks trash whose removal waited past the pool's
// timeout: it is removed all the same, in place of the job.
func TestDrainExpired(t *testing.T) {
	d, err := newDisk(&Disk{Path: t.TempDir()}, filepool.New(2, 64, time.Second), &tagIndex{m: map[string]map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, d)
	stuck := make(chan struct{})
	pool := filepool.New(1, 4, 10*time.Millisecond)
	pool.Hook = func() { <-stuck }
	pool.Submit(func() {}, nil)
	d.pool = pool
	d.mu.Lock()
	d.trash = append(d.trash, "aa/missing")
	d.mu.Unlock()
	d.drainLater()
	time.Sleep(30 * time.Millisecond) // past the pool's timeout
	close(stuck)
	waitFor(t, "the trash drained", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.draining && len(d.trash) == 0
	})
}

// TestRevalidateUnderWay checks a revalidation asked for while a fetch of
// the key is under way: none more is made.
func TestRevalidateUnderWay(t *testing.T) {
	m := provisioned(t, &Cache{Zone: "underway"})
	f, first := m.store.lock("k", clock(), time.Minute)
	if !first {
		t.Fatal("not first")
	}
	var n int
	m.revalidate("k", &entry{}, httptest.NewRequest("GET", "http://example.com/k", nil), answering(nil, "", &n))
	m.store.finish(f, fetchResult{status: 200})
	if n != 0 || m.store.background.Load() != 0 {
		t.Errorf("%d fetches, %d in the background", n, m.store.background.Load())
	}
}
