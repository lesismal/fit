package static

import (
	"math/rand/v2"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lesismal/fib/filewatch"
)

// known returns the lookups the cache keeps, by request path, from every
// shard.
func (c *cache) known() map[string]*lookup {
	m := map[string]*lookup{}
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		for name, l := range sh.lookups {
			m[name] = l
		}
		sh.mu.RUnlock()
	}
	return m
}

// lookupTotals returns how many lookups the cache keeps, and the bytes they
// are counted at.
func (c *cache) lookupTotals() (int, int64) {
	var n int
	var size int64
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		n += len(sh.lookups)
		size += sh.lookupBytes
		sh.mu.RUnlock()
	}
	return n, size
}

// checkIndex fails t for a request path a shard's index holds whose lookup
// the shard does not keep, or that is not in its own shard.
func (c *cache) checkIndex(t *testing.T, what string) {
	t.Helper()
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		for dep, names := range sh.index {
			all := []string{names.one}
			if names.more != nil {
				all = all[:0]
				for name := range names.more {
					all = append(all, name)
				}
			}
			for _, name := range all {
				if sh.lookups[name] == nil || c.shard(c.hash(name)) != sh {
					t.Errorf("%s: index %q holds %s, which is not kept", what, dep, name)
				}
			}
		}
		sh.mu.RUnlock()
	}
}

func TestLookupShards(t *testing.T) {
	n := lookupShards()
	if n < 16 || n > 256 || n&(n-1) != 0 {
		t.Fatalf("%d shards", n)
	}
	var c cache
	c.initLookups(64, DefaultMaxLookups, DefaultMaxLookupBytes)
	if c.shards[0].maxLookups != DefaultMaxLookups/64 || c.shards[0].maxBytes != DefaultMaxLookupBytes/64 {
		t.Fatalf("a shard's bounds: %d lookups, %d bytes", c.shards[0].maxLookups, c.shards[0].maxBytes)
	}
	// Paths spread over the shards.
	used := map[*lookupShard]int{}
	for i := range 64 * 64 {
		used[c.shard(c.hash("/f"+strconv.Itoa(i)))]++
	}
	for sh, k := range used {
		if k < 16 || k > 160 {
			t.Errorf("shard %p got %d of %d paths", sh, k, 64*64)
		}
	}
	if len(used) != 64 {
		t.Errorf("%d of 64 shards used", len(used))
	}
}

func TestDepNames(t *testing.T) {
	var d depNames
	d = d.add("a")
	if !d.single || d.one != "a" || d.more != nil {
		t.Fatalf("one: %+v", d)
	}
	if d = d.add("a"); !d.single || d.more != nil {
		t.Fatalf("the same again: %+v", d)
	}
	if _, empty := d.remove("b"); empty {
		t.Fatal("removing another emptied it")
	}
	d = d.add("b")
	if d.single || len(d.more) != 2 {
		t.Fatalf("two: %+v", d)
	}
	d, empty := d.remove("a")
	if empty || len(d.more) != 1 {
		t.Fatalf("one of two removed: %+v", d)
	}
	if _, empty = d.remove("b"); !empty {
		t.Fatal("the last removed left some")
	}
	if _, empty = (depNames{one: "", single: true}).remove(""); !empty {
		t.Fatal("the root's empty name not removed")
	}
	if _, empty = (depNames{}).remove("x"); !empty {
		t.Fatal("none is not empty")
	}
}

// TestDoorkeeper checks that a path is admitted the second time, that the
// bitmap is cleared once a quarter of it is set, and that a path not seen is
// told seen rarely.
func TestDoorkeeper(t *testing.T) {
	var d doorkeeper
	d.init(1024)
	if len(d.words)*64 != 8192 || d.limit != 2048 {
		t.Fatalf("%d bits, limit %d", len(d.words)*64, d.limit)
	}
	h := uint64(0x1234_5678_9abc_def0)
	if d.admit(h) {
		t.Fatal("admitted the first time")
	}
	if !d.admit(h) {
		t.Fatal("not admitted the second time")
	}
	r := rand.New(rand.NewPCG(1, 2))
	var wrong, tries int
	for range 100000 {
		before := d.set.Load()
		if d.admit(r.Uint64()) {
			wrong++
		}
		tries++
		if d.set.Load() < before {
			// Cleared: h is forgotten.
			if d.admit(h) {
				t.Fatal("a path seen before a clear still admitted")
			}
			d.admit(h)
		}
		if d.set.Load() > d.limit {
			t.Fatalf("%d bits set past the limit %d", d.set.Load(), d.limit)
		}
	}
	if rate := float64(wrong) / float64(tries); rate > 1.0/16 {
		t.Fatalf("paths not seen admitted at %.3f", rate)
	}
	var small doorkeeper
	small.init(1)
	if len(small.words) != 1 {
		t.Fatalf("a cache of one lookup has %d words", len(small.words))
	}
}

// TestWorkerDoesNotWait checks that a fib worker, which looks only at what
// the cache holds, does not wait for a lock a file pool worker holds, and
// takes the request to the file pool instead.
func TestWorkerDoesNotWait(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "a"})
	s := mustLoad(t, root, "")
	if got := get(s, "GET", "/a.txt", nil); got.Response.StatusCode != 200 {
		t.Fatalf("%d", got.Response.StatusCode)
	}
	if s.find("/a.txt", false, nil) == nil {
		t.Fatal("a lookup not kept")
	}
	sh := s.cache.shard(s.cache.hash("/a.txt"))
	sh.mu.Lock()
	start := time.Now()
	l := s.find("/a.txt", false, nil)
	sh.mu.Unlock()
	if l != nil || time.Since(start) > time.Second {
		t.Fatalf("a worker found %v past a held lock in %v", l, time.Since(start))
	}
	full := filepath.Join(root, "a.txt")
	stamp := fileStamp(t, full)
	if s.cache.current(full, stamp, false) == nil {
		t.Fatal("the file's contents not held")
	}
	s.cache.mu.Lock()
	e := s.cache.current(full, stamp, false)
	s.cache.mu.Unlock()
	if e != nil {
		t.Fatal("a worker read the contents past a held lock")
	}
	// Answered from the file pool while a lock is held.
	sh.mu.Lock()
	done := make(chan int)
	go func() { done <- get(s, "GET", "/a.txt", nil).Response.StatusCode }()
	time.Sleep(20 * time.Millisecond)
	sh.mu.Unlock()
	if code := <-done; code != 200 {
		t.Fatalf("%d", code)
	}
}

// TestKeepsNothingWithoutCheck checks that a check interval of 0, which
// trusts nothing kept, keeps nothing.
func TestKeepsNothingWithoutCheck(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "a"})
	s := mustLoad(t, root, `, "check_interval": "0s"`)
	for range 3 {
		if got := get(s, "GET", "/a.txt", nil); got.Response.StatusCode != 200 {
			t.Fatalf("%d", got.Response.StatusCode)
		}
	}
	if n, _ := s.cache.lookupTotals(); n != 0 {
		t.Fatalf("%d lookups kept", n)
	}
}

// TestEventEmptiesFullShard checks that a shard emptied by a change keeps
// paths again the first time they are looked up.
func TestEventEmptiesFullShard(t *testing.T) {
	var c cache
	c.check = time.Hour
	c.initLookups(1, 2, 1<<20)
	keep := func(name string) {
		c.keep(name, c.hash(name), &lookup{checked: time.Now(), gen: c.gen.Load(), rel: name[1:], deps: []string{name[1:]}})
	}
	keep("/a")
	keep("/b")
	if !c.shards[0].full.Load() {
		t.Fatal("not full")
	}
	c.event(filewatch.Event{Name: "a"})
	if c.shards[0].full.Load() {
		t.Fatal("still full")
	}
	keep("/c")
	if got := c.known(); got["/c"] == nil || got["/a"] != nil || len(got) != 2 {
		t.Fatalf("lookups %v", got)
	}
	c.checkIndex(t, "after an event")
}

// TestSharedDependency checks the index of a dependency several request
// paths have, as /a.txt and //a.txt both resolve to a.txt: one forgotten
// leaves the others, and a change forgets them all.
func TestSharedDependency(t *testing.T) {
	var c cache
	c.check = time.Hour
	c.initLookups(1, 16, 1<<20)
	keep := func(name string) *lookup {
		l := &lookup{checked: time.Now(), gen: c.gen.Load(), rel: "a.txt", deps: []string{"a.txt"}}
		c.keep(name, c.hash(name), l)
		return l
	}
	keep("/a.txt")
	keep("//a.txt")
	keep("/./a.txt")
	sh := &c.shards[0]
	if names := sh.index["a.txt"]; len(names.more) != 3 {
		t.Fatalf("index %+v", names)
	}
	// Kept again: replaced, the others left.
	keep("//a.txt")
	if names := sh.index["a.txt"]; len(names.more) != 3 || len(sh.lookups) != 3 {
		t.Fatalf("index %+v, %d lookups", names, len(sh.lookups))
	}
	c.checkIndex(t, "shared")
	c.event(filewatch.Event{Name: "a.txt"})
	if len(sh.lookups) != 0 || len(sh.index) != 0 {
		t.Fatalf("after a change: %d lookups, index %v", len(sh.lookups), sh.index)
	}
}

// TestLookupExpires checks that a lookup no watch vouches for is trusted
// for the check interval only.
func TestLookupExpires(t *testing.T) {
	var c cache
	c.check = time.Millisecond
	c.initLookups(1, 16, 1<<20)
	h := c.hash("/a")
	c.keep("/a", h, &lookup{checked: time.Now().Add(-time.Second)})
	if c.lookup("/a", h, true) != nil {
		t.Fatal("a lookup older than the check interval trusted")
	}
	c.keep("/b", c.hash("/b"), &lookup{checked: time.Now().Add(time.Hour)})
	if c.lookup("/b", c.hash("/b"), true) == nil {
		t.Fatal("a fresh lookup not trusted")
	}
}
