package static

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/filepool"
)

// TestNoDiskOnHit checks that a path the cache knows, within check_interval,
// is answered without the file pool, which every look at the disk goes
// through, and that it is looked at again once the interval has passed,
// unless a watch vouches for it.
func TestNoDiskOnHit(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "aaa", "dir/index.html": "<p>i</p>", "big.bin": "0123456789abcdefghij"})
	s := mustLoad(t, root, `, "max_file_bytes": 16, "check_interval": "200ms", "disk_io": "pool"`)
	var jobs atomic.Int32
	s.pool = filepool.New(2, 16, time.Second)
	s.pool.Hook = func() { jobs.Add(1) }
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
	if s.watch != nil {
		if jobs.Load() != before {
			t.Fatal("a.txt, under a watch, was looked at again past check_interval")
		}
		// Without the watch, the interval tells.
		s.Cleanup()
		get(s, "GET", "/a.txt", nil)
	}
	if jobs.Load() != before+1 {
		t.Fatal("a.txt was not looked at again past check_interval")
	}
}

// TestSlowDisk checks that a disk that does not answer costs the requests
// that need it a 503 once the pool's timeout passes, and leaves alone those
// the cache answers.
func TestSlowDisk(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "aaa", "b.txt": "bbb"})
	s := mustLoad(t, root, `, "check_interval": "1h", "disk_io": "pool"`)
	s.pool = filepool.New(1, 4, 300*time.Millisecond)
	if got := get(s, "GET", "/a.txt", nil).Response; string(got.Body) != "aaa" {
		t.Fatalf("a.txt: %d %q", got.StatusCode, got.Body)
	}
	stuck := make(chan struct{})
	defer close(stuck)
	s.pool.Hook = func() { <-stuck }
	start := time.Now()
	if got := get(s, "GET", "/b.txt", nil).Response; got.StatusCode != 503 || time.Since(start) > 2*time.Second {
		t.Fatalf("b.txt on a stuck disk: %d after %v", got.StatusCode, time.Since(start))
	}
	start = time.Now()
	if got := get(s, "GET", "/a.txt", nil).Response; string(got.Body) != "aaa" || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("a.txt from the cache with the disk stuck: %d %q after %v", got.StatusCode, got.Body, time.Since(start))
	}
	// The one worker is stuck and the queue fills: past it, 503 at once.
	queued := make(chan int, 4)
	for range 4 {
		go func() { queued <- get(s, "GET", "/b.txt", nil).Response.StatusCode }()
	}
	time.Sleep(50 * time.Millisecond)
	start = time.Now()
	if got := get(s, "GET", "/b.txt", nil).Response; got.StatusCode != 503 || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("b.txt with the queue full: %d after %v", got.StatusCode, time.Since(start))
	}
	// The queued ones, which no worker takes up, the timeout answers.
	for range 4 {
		select {
		case status := <-queued:
			if status != 503 {
				t.Fatalf("b.txt queued behind a stuck disk: %d", status)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("b.txt queued behind a stuck disk was never answered")
		}
	}
	if n := s.waiting.count.Load(); n != 0 {
		t.Fatalf("%d requests still wait", n)
	}
}

// TestWaiting checks the list of requests waiting for the pool: those
// claimed leave it, the sweep answers those past their deadline, and stops
// once none wait.
func TestWaiting(t *testing.T) {
	w := newWaiting()
	var claimed, expired []*queued
	for i := range 100 {
		q := &queued{}
		// No request to answer: expire is called only once the deadline
		// passes, and the state tells.
		w.join(q, nil, time.Hour)
		if i%2 == 0 {
			if !q.claim() {
				t.Fatal("claim")
			}
			claimed = append(claimed, q)
		} else {
			expired = append(expired, q)
		}
	}
	if n := w.count.Load(); n != 50 {
		t.Fatalf("%d wait, want 50", n)
	}
	for _, q := range claimed {
		if q.next != nil || q.prev != nil {
			t.Fatal("a claimed request is still on the list")
		}
	}
	// Past their deadline: the sweep takes them off and expires them,
	// which, with no request, panics past the state's CAS; claim them as
	// the work would first, so that expire finds them taken.
	for _, q := range expired {
		q.deadline = 0
		q.state.Store(queuedClaimed)
	}
	w.sweep()
	if n := w.count.Load(); n != 0 {
		t.Fatalf("%d wait after the sweep", n)
	}
	for _, q := range expired {
		if q.next != nil || q.state.Load() != queuedClaimed {
			t.Fatal("swept wrong")
		}
		q.leave()
	}
	if w.sweeping.Load() {
		t.Fatal("a sweep is scheduled with none waiting")
	}
}

// TestDiskIO checks disk_io: with inline, and with auto under its bound,
// files read whole and sent by SendFile, HEAD, a directory and a file not
// there are answered as from the pool, and the pool is not used; with pool
// it is; a value not known is refused.
func TestDiskIO(t *testing.T) {
	big := strings.Repeat("0123456789", smallFile/10+100)
	root := tree(t, map[string]string{"small.txt": "small", "big.txt": big, "sub/index.html": "index"})
	for _, mode := range []string{"", "auto", "inline", "pool"} {
		extra := `, "max_file_bytes": 4, "check_interval": "0s"`
		if mode != "" {
			extra += `, "disk_io": "` + mode + `"`
		}
		s := mustLoad(t, root, extra)
		var jobs atomic.Int32
		s.pool = filepool.New(2, 16, time.Second)
		s.pool.Hook = func() { jobs.Add(1) }
		for range 2 {
			for _, tt := range []struct {
				method, target string
				status         int
				body           string
			}{
				{"GET", "/small.txt", 200, "small"},
				{"GET", "/big.txt", 200, big},
				{"HEAD", "/big.txt", 200, ""},
				{"GET", "/sub", 301, ""},
				{"GET", "/sub/", 200, "index"},
				{"GET", "/none", 404, ""},
			} {
				got := get(s, tt.method, tt.target, nil).Response
				if got.StatusCode != tt.status || (tt.body != "" && string(got.Body) != tt.body) {
					t.Fatalf("%q %s %s: %d, %d bytes", mode, tt.method, tt.target, got.StatusCode, len(got.Body))
				}
			}
		}
		if n := jobs.Load(); (n == 0) != (mode != "pool") {
			t.Fatalf("%q: the pool ran %d jobs", mode, n)
		}
		if n := s.inlining.Load(); n != 0 {
			t.Fatalf("%q: %d requests still counted inline", mode, n)
		}
	}
	if _, err := load(t, `{"root": `+quote(root)+`, "disk_io": "sometimes"}`); err == nil {
		t.Error("disk_io \"sometimes\" was taken")
	}
}

// TestInlineSlowDisk checks auto on a disk that hangs: no more requests
// than the bound wait for it on fib's workers, answered once it answers;
// those past the bound go to the pool, whose timeout answers them 503.
func TestInlineSlowDisk(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "aaa"})
	s := mustLoad(t, root, `, "check_interval": "0s"`)
	s.pool = filepool.New(1, 8, 300*time.Millisecond)
	s.inlineMax = 2
	stuck := make(chan struct{})
	var inline atomic.Int32
	inlineHook = func() { inline.Add(1); <-stuck }
	defer func() { inlineHook = nil }()
	poolStuck := make(chan struct{})
	s.pool.Hook = func() { <-poolStuck }
	defer close(poolStuck)
	statuses := make(chan int, 3)
	for range 2 {
		go func() { statuses <- get(s, "GET", "/a.txt", nil).Response.StatusCode }()
	}
	for deadline := time.Now().Add(5 * time.Second); inline.Load() != 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d inline", inline.Load())
		}
	}
	// Past the bound: the pool, stuck too, and its timeout's 503.
	start := time.Now()
	if got := get(s, "GET", "/a.txt", nil).Response; got.StatusCode != 503 || time.Since(start) > 3*time.Second || inline.Load() != 2 {
		t.Fatalf("past the bound: %d after %v, %d inline", got.StatusCode, time.Since(start), inline.Load())
	}
	select {
	case status := <-statuses:
		t.Fatalf("answered %d while the disk hangs", status)
	default:
	}
	close(stuck)
	for range 2 {
		select {
		case status := <-statuses:
			if status != 200 {
				t.Fatalf("once the disk answered: %d", status)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("not answered once the disk answered")
		}
	}
	if n := s.inlining.Load(); n != 0 {
		t.Fatalf("%d still counted inline", n)
	}
}
