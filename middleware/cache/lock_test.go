package cache

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// many sends n requests for url at once, and returns their replies.
func many(t *testing.T, n int, url string, fields ...string) []reply {
	t.Helper()
	replies := make([]reply, n)
	var wg sync.WaitGroup
	for i := range n {
		c := clients()[[]string{"http/1.1", "h2c"}[i%2]]
		wg.Go(func() { replies[i] = get(t, c, "GET", url, fields...) })
	}
	wg.Wait()
	return replies
}

// TestStampede checks the cache lock: many clients asking at once for a
// response not kept make one fetch, and all get its answer.
func TestStampede(t *testing.T) {
	base := serve(t, cacheOnly)
	replies := many(t, 100, base+"/late")
	if n := hitsOf("/late"); n != 1 {
		t.Errorf("%d fetches for 100 clients", n)
	}
	for i, r := range replies {
		if r.status != 200 || r.body != "/late 1" {
			t.Fatalf("%d: %d %q %q", i, r.status, r.body, r.cacheStatus())
		}
	}
}

// TestLockNotKept checks that requests waiting for a fetch whose response
// is not kept go on to fetch for themselves, as soon as that is known.
func TestLockNotKept(t *testing.T) {
	base := serve(t, cacheOnly)
	started := time.Now()
	replies := many(t, 20, base+"/late-private")
	for i, r := range replies {
		if r.status != 200 || !strings.HasPrefix(r.body, "/late-private ") {
			t.Fatalf("%d: %d %q", i, r.status, r.body)
		}
	}
	// Each waiter goes on as the first response's header is settled: two
	// rounds of the origin's 200ms, not 20, nor lock_timeout's 5s.
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("took %v", took)
	}
	if n := hitsOf("/late-private"); n != 20 {
		t.Errorf("%d fetches", n)
	}
}

// TestLockTimeout checks that a request waits for another's fetch no longer
// than lock_timeout.
func TestLockTimeout(t *testing.T) {
	base := serve(t, `[{"module": "cache", "params": {"zone": "test", "lock_timeout": "100ms"}}]`)
	started := time.Now()
	replies := many(t, 10, base+"/late?delay=1s")
	for i, r := range replies {
		if r.status != 200 {
			t.Fatalf("%d: %d", i, r.status)
		}
	}
	if n := hitsOf("/late"); n != 10 {
		t.Errorf("%d fetches past lock_timeout", n)
	}
	if took := time.Since(started); took > 1800*time.Millisecond {
		t.Errorf("took %v", took)
	}
	if r := get(t, clients()["h2c"], "GET", base+"/late?delay=1s"); !strings.HasPrefix(r.cacheStatus(), "fit; hit") {
		t.Errorf("then: %q", r.cacheStatus())
	}
}

// TestDisableLock checks that every request fetches for itself without the
// lock.
func TestDisableLock(t *testing.T) {
	base := serve(t, `[{"module": "cache", "params": {"zone": "test", "disable_lock": true}}]`)
	many(t, 10, base+"/late")
	if n := hitsOf("/late"); n != 10 {
		t.Errorf("%d fetches", n)
	}
}

// TestStaleWhileRevalidate checks that a stale response its
// stale-while-revalidate allows is answered at once, and fetched again in
// the background, once, with its validators.
func TestStaleWhileRevalidate(t *testing.T) {
	start := time.Now()
	at(t, start, 0)
	base := serve(t, cacheOnly)
	c := clients()["h2c"]
	get(t, c, "GET", base+"/swr")
	at(t, start, 30*time.Second)
	// The first is answered stale, and the fetch it starts, a 304 here, may
	// freshen the response before the others come.
	stale := 0
	for _, r := range many(t, 20, base+"/swr") {
		switch {
		case r.body != "/swr 1":
			t.Fatalf("%q", r.body)
		case strings.HasSuffix(r.cacheStatus(), "detail=stale-while-revalidate"):
			stale++
		case !freshHit(r):
			t.Fatalf("%q", r.cacheStatus())
		}
	}
	if stale == 0 {
		t.Error("nothing answered stale")
	}
	waitFor(t, "the background fetch", func() bool {
		r := get(t, c, "GET", base+"/swr")
		return strings.HasPrefix(r.cacheStatus(), "fit; hit; ttl=") && !strings.Contains(r.cacheStatus(), "detail")
	})
	if n := hitsOf("/swr"); n != 2 {
		t.Errorf("%d fetches", n)
	}
	// Past the window, it is fetched before it is answered.
	at(t, start, 200*time.Second)
	if r := get(t, c, "GET", base+"/swr"); !freshHit(r) || hitsOf("/swr") != 3 {
		t.Errorf("past the window: %q, %d fetches", r.cacheStatus(), hitsOf("/swr"))
	}
}

// TestStaleIfError checks that a stale response stale-if-error allows is
// answered when fetching it again fails, and not past the window.
func TestStaleIfError(t *testing.T) {
	start := time.Now()
	at(t, start, 0)
	base := serve(t, `[{"module": "cache", "params": {"zone": "test", "stale_if_error": "30s"}}]`)
	c := clients()["http/1.1"]
	get(t, c, "GET", base+"/sie")
	get(t, c, "GET", base+"/sie-param")
	failing.Store(true)
	at(t, start, 20*time.Second)
	for _, p := range []string{"/sie", "/sie-param"} {
		r := get(t, c, "GET", base+p)
		if r.status != 200 || r.body != p+" 1" || !strings.HasSuffix(r.cacheStatus(), "detail=stale-if-error") {
			t.Errorf("%s at 20s: %d %q %q", p, r.status, r.body, r.cacheStatus())
		}
	}
	at(t, start, 50*time.Second) // past the param's 30s, within the response's 60s
	if r := get(t, c, "GET", base+"/sie"); r.status != 200 || !strings.HasSuffix(r.cacheStatus(), "detail=stale-if-error") {
		t.Errorf("/sie at 50s: %d %q", r.status, r.cacheStatus())
	}
	if r := get(t, c, "GET", base+"/sie-param"); r.status != 500 {
		t.Errorf("/sie-param at 50s: %d %q", r.status, r.cacheStatus())
	}
	failing.Store(false)
	if r := get(t, c, "GET", base+"/sie"); r.status != 200 || !freshHit(r) {
		t.Errorf("/sie once the origin is back: %d %q", r.status, r.cacheStatus())
	}
}

// TestPurge checks purges by URL, by tag and of everything, in memory and on
// disk.
func TestPurge(t *testing.T) {
	dir := t.TempDir()
	base := serve(t, diskParams(dir, ""))
	c := clients()["h2c"]
	host := strings.TrimPrefix(base, "http://")
	paths := []string{"/fresh", "/s-maxage", "/tagged?a", "/tagged?b", "/expires"}
	for _, p := range paths {
		get(t, c, "GET", base+p)
	}
	d := diskOf(t, "disk")
	waitFor(t, "files written", func() bool { n, _ := d.stats(); return n == len(paths) })
	kept := func(p string) bool {
		r := get(t, c, "GET", base+p, "Cache-Control", "only-if-cached")
		return r.status != 504
	}
	if n, err := Purge("disk", []string{"http://" + strings.ToUpper(host) + "/fresh", "http://" + host + "/nothing"}, nil, false); n != 1 || err != nil {
		t.Errorf("purge by URL: %d %v", n, err)
	}
	if kept("/fresh") || !kept("/s-maxage") {
		t.Error("purge by URL")
	}
	if n, err := Purge("", nil, []string{"t1"}, false); n != 1 || err != nil {
		t.Errorf("purge by t1: %d %v", n, err)
	}
	if kept("/tagged?a") || !kept("/tagged?b") {
		t.Error("purge by tag")
	}
	if n, err := Purge("disk", nil, []string{"t3", "t2"}, false); n != 1 || err != nil {
		t.Errorf("purge by t3, t2: %d %v", n, err)
	}
	if n, _ := d.stats(); n != 2 {
		t.Errorf("%d responses on disk after the purges, want 2", n)
	}
	if n, err := Purge("disk", nil, nil, true); n != 2 || err != nil {
		t.Errorf("purge all: %d %v", n, err)
	}
	for _, p := range paths {
		if kept(p) {
			t.Errorf("%s kept after purging all", p)
		}
	}
	if n, _ := d.stats(); n != 0 {
		t.Errorf("%d responses on disk", n)
	}
	if _, err := Purge("nothing", nil, nil, true); err == nil {
		t.Error("an unknown zone")
	}
	if _, err := Purge("", []string{"/relative"}, nil, false); err == nil {
		t.Error("a relative URL")
	}
	z := Zones()
	if len(z) != 1 || z[0].Zone != "disk" || z[0].DiskPath == "" {
		t.Errorf("zones: %+v", z)
	}
}

func TestTagsOf(t *testing.T) {
	h := map[string][]string{"Cache-Tag": {"a, b,, a", strings.Repeat("x", 200)}, "Surrogate-Key": {"c  d b"}}
	if got := fmt.Sprint(tagsOf(h)); got != "[a b c d]" {
		t.Errorf("%s", got)
	}
	var many []string
	for i := range 40 {
		many = append(many, fmt.Sprint(i))
	}
	if n := len(tagsOf(map[string][]string{"Cache-Tag": {strings.Join(many, ",")}})); n != maxTags {
		t.Errorf("%d tags", n)
	}
}
