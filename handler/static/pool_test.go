package static

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestNoDiskOnHit checks that a path the cache knows, within check_interval,
// is answered without the file pool, which every look at the disk goes
// through, and that it is looked at again once the interval has passed.
func TestNoDiskOnHit(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "aaa", "dir/index.html": "<p>i</p>", "big.bin": "0123456789abcdefghij"})
	s := mustLoad(t, root, `, "max_file_bytes": 16, "check_interval": "200ms"`)
	var jobs atomic.Int32
	s.pool = newFilePool(2, 16, time.Second)
	s.pool.hook = func() { jobs.Add(1) }
	for _, target := range []string{"/a.txt", "/dir/", "/dir", "/missing"} {
		before := jobs.Load()
		first := get(s, "GET", target, nil).Response
		if jobs.Load() != before+1 {
			t.Fatalf("%s: %d jobs the first time", target, jobs.Load()-before)
		}
		for range 3 {
			if got := get(s, "GET", target, nil).Response; got.StatusCode != first.StatusCode || string(got.Body) != string(first.Body) {
				t.Fatalf("%s from the cache: %d %q, first %d %q", target, got.StatusCode, got.Body, first.StatusCode, first.Body)
			}
		}
		if jobs.Load() != before+1 {
			t.Fatalf("%s: %d jobs for four requests", target, jobs.Load()-before)
		}
	}
	// A file too large to keep is sent from disk every time.
	before := jobs.Load()
	for range 2 {
		if got := get(s, "GET", "/big.bin", nil).Response; string(got.Body) != "0123456789abcdefghij" {
			t.Fatalf("big.bin: %d %q", got.StatusCode, got.Body)
		}
	}
	if jobs.Load() != before+2 {
		t.Fatalf("big.bin: %d jobs for two requests", jobs.Load()-before)
	}
	time.Sleep(250 * time.Millisecond)
	before = jobs.Load()
	get(s, "GET", "/a.txt", nil)
	if jobs.Load() != before+1 {
		t.Fatal("a.txt was not looked at again past check_interval")
	}
}

// TestSlowDisk checks that a disk that does not answer costs the requests
// that need it a 503 once the pool's timeout passes, and leaves alone those
// the cache answers.
func TestSlowDisk(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "aaa", "b.txt": "bbb"})
	s := mustLoad(t, root, `, "check_interval": "1h"`)
	s.pool = newFilePool(1, 4, 300*time.Millisecond)
	if got := get(s, "GET", "/a.txt", nil).Response; string(got.Body) != "aaa" {
		t.Fatalf("a.txt: %d %q", got.StatusCode, got.Body)
	}
	stuck := make(chan struct{})
	defer close(stuck)
	s.pool.hook = func() { <-stuck }
	start := time.Now()
	if got := get(s, "GET", "/b.txt", nil).Response; got.StatusCode != 503 || time.Since(start) > 2*time.Second {
		t.Fatalf("b.txt on a stuck disk: %d after %v", got.StatusCode, time.Since(start))
	}
	start = time.Now()
	if got := get(s, "GET", "/a.txt", nil).Response; string(got.Body) != "aaa" || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("a.txt from the cache with the disk stuck: %d %q after %v", got.StatusCode, got.Body, time.Since(start))
	}
	// The one worker is stuck and the queue fills: past it, 503 at once.
	for range 4 {
		go get(s, "GET", "/b.txt", nil)
	}
	time.Sleep(50 * time.Millisecond)
	start = time.Now()
	if got := get(s, "GET", "/b.txt", nil).Response; got.StatusCode != 503 || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("b.txt with the queue full: %d after %v", got.StatusCode, time.Since(start))
	}
}
