package vod

import (
	"container/list"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/medialib"
)

// What a cache entry holds.
const (
	whatPlan uint8 = iota + 1
	whatList       // a media playlist, rendered
	whatInit
	whatSegment
	whatVTT
)

// fileID is a file as the catalog has it: a file changed since is another.
type fileID struct {
	root, path string
	size, mod  int64
}

func idOf(f *medialib.File) fileID {
	return fileID{root: f.Root, path: f.Path, size: f.Size, mod: f.ModTime.UnixNano()}
}

// ckey names what the cache keeps of a file.
type ckey struct {
	file fileID
	what uint8
	c    container
	n    int32
}

// The errors of the cache.
var (
	// errFull is what is to be built finding no room: what is being built
	// holds the rest.
	errFull = errors.New("vod: the cache is full of what is being built")
	// errWaiters is one request too many waiting for one thing.
	errWaiters = errors.New("vod: too many requests wait for the same segment")
)

// maxWaiters is the most requests that may wait for one entry being built.
const maxWaiters = 1024

// entryBytes is what an entry is reckoned to take beside its value.
const entryBytes = 160

// entry is what the cache keeps of a key, or is building for it.
type entry struct {
	key     ckey
	val     any
	bytes   int64
	waiters []func(any, error)
	elem    *list.Element // in the LRU once built
}

// cache keeps what the handler builds from a library's files, plans,
// playlists, initialization sections, segments and subtitles, bounded by
// max_cache_bytes: what is being built holds the bytes it is reckoned to
// take from when it starts, and the least recently used are dropped for
// it. A key is built once for every request that waits for it.
type cache struct {
	mu   sync.Mutex
	max  int64
	used int64
	m    map[ckey]*entry
	lru  list.List
	// cat is the catalog last seen; a new one drops what it lacks. It is
	// read on every request, without the lock.
	cat atomic.Pointer[medialib.Catalog]
}

func newCache(max int64) *cache { return &cache{max: max, m: map[ckey]*entry{}} }

// peek returns what the cache keeps of k, if it does.
func (c *cache) peek(k ckey) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.m[k]; e != nil && e.elem != nil {
		c.lru.MoveToFront(e.elem)
		return e.val, true
	}
	return nil, false
}

// get calls fn with what the cache keeps of k, or with what build makes of
// it, or with the error: at once, on the caller's goroutine, when it keeps
// k or cannot build it (errFull, errWaiters), and otherwise once it is
// built, wherever the job is done. build is called for a k neither kept nor
// being built, without the cache's lock; its job holds the memory the work
// takes once it starts (job.hold), and is done once: with what it made and
// the bytes it takes, or with an error, which is given to those waiting and
// not kept.
func (c *cache) get(k ckey, build func(j job), fn func(any, error)) {
	c.mu.Lock()
	if e := c.m[k]; e != nil {
		if e.elem != nil {
			c.lru.MoveToFront(e.elem)
			c.mu.Unlock()
			fn(e.val, nil)
			return
		}
		if len(e.waiters) >= maxWaiters {
			c.mu.Unlock()
			fn(nil, errWaiters)
			return
		}
		e.waiters = append(e.waiters, fn)
		c.mu.Unlock()
		return
	}
	if !c.room(entryBytes) {
		c.mu.Unlock()
		fn(nil, errFull)
		return
	}
	e := &entry{key: k, bytes: entryBytes, waiters: []func(any, error){fn}}
	c.m[k] = e
	c.used += entryBytes
	c.mu.Unlock()
	build(job{c: c, e: e})
}

// job is an entry being built, as get hands it to build.
type job struct {
	c *cache
	e *entry
}

// hold takes n bytes more for the entry while it is built, the least
// recently used dropped for them, and reports whether there was room: the
// work holds the memory it takes from when it starts, on the file pool, so
// that requests queued for the pool hold none, and the bound holds for
// what is being built as for what is kept. An entry dropped meanwhile
// holds nothing, its work about to find its file changed.
func (j job) hold(n int64) bool {
	c := j.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[j.e.key] != j.e {
		return true
	}
	if !c.room(n) {
		return false
	}
	c.used += n
	j.e.bytes += n
	return true
}

// done ends the job with val, which takes bytes, or with err.
func (j job) done(val any, bytes int64, err error) { j.c.done(j.e, val, bytes, err) }

// room drops the least recently used until need fits, and reports whether
// it does; it drops nothing for what could never fit. c.mu is held.
func (c *cache) room(need int64) bool {
	if need > c.max {
		return false
	}
	for c.used+need > c.max && c.lru.Len() > 0 {
		c.evict(c.lru.Back().Value.(*entry))
	}
	return c.used+need <= c.max
}

// done ends the building of e.
func (c *cache) done(e *entry, val any, bytes int64, err error) {
	c.mu.Lock()
	waiters := e.waiters
	e.waiters = nil
	switch {
	case c.m[e.key] != e:
		// Dropped while it was built: those waiting get it, and it is not
		// kept.
	case err != nil:
		c.forget(e)
	default:
		bytes += entryBytes
		c.used -= e.bytes
		e.val, e.bytes = val, bytes
		if c.room(bytes) {
			c.used += bytes
			e.elem = c.lru.PushFront(e)
		} else {
			delete(c.m, e.key)
		}
	}
	c.mu.Unlock()
	for _, fn := range waiters {
		fn(val, err)
	}
}

// put keeps val for k, of bytes, unless k is kept already or there is no
// room for it.
func (c *cache) put(k ckey, val any, bytes int64) {
	bytes += entryBytes
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[k] != nil || !c.room(bytes) {
		return
	}
	e := &entry{key: k, val: val, bytes: bytes}
	e.elem = c.lru.PushFront(e)
	c.m[k] = e
	c.used += bytes
}

// evict drops e, built. c.mu is held.
func (c *cache) evict(e *entry) {
	delete(c.m, e.key)
	c.lru.Remove(e.elem)
	c.used -= e.bytes
}

// forget drops e, built or being built. c.mu is held.
func (c *cache) forget(e *entry) {
	if e.elem != nil {
		c.evict(e)
		return
	}
	delete(c.m, e.key)
	c.used -= e.bytes
}

// drop drops what the cache keeps, or is building, of file f.
func (c *cache) drop(f fileID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if k.file == f {
			c.forget(e)
		}
	}
}

// changed reports whether cat is not the catalog c last saw, and makes it
// the one it has seen.
func (c *cache) changed(cat *medialib.Catalog) bool {
	old := c.cat.Load()
	return old != cat && c.cat.CompareAndSwap(old, cat)
}

// keep drops what the cache keeps of the files cat does not have as they
// were.
func (c *cache) keep(cat *medialib.Catalog) {
	have := map[fileID]bool{}
	for _, it := range cat.Items {
		for _, list := range [][]*medialib.Rendition{it.Renditions, it.Audio} {
			for _, r := range list {
				have[idOf(&r.File)] = true
			}
		}
		for _, s := range it.Subtitles {
			have[idOf(&s.File)] = true
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if !have[k.file] {
			c.forget(e)
		}
	}
}

// CacheStats is what a handler's cache holds.
type CacheStats struct {
	Entries int
	Bytes   int64
	Max     int64
}

func (c *cache) stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{Entries: len(c.m), Bytes: c.used, Max: c.max}
}

// blob is a response the cache keeps whole: a playlist, a segment, an
// initialization section or a subtitle, with its header.
type blob struct {
	data    []byte
	header  map[string][]string
	modTime time.Time
}
