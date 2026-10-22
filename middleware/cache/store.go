package cache

import (
	"fmt"
	"hash/maphash"
	stdhttp "net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// entry is a stored response. It does not change once stored: a response
// freshened by a 304 is stored as a new entry in its place.
type entry struct {
	status int
	header stdhttp.Header
	body   []byte

	// varyNames are the request fields the response varies on, and
	// varyValues their values in the request it answered.
	varyNames, varyValues []string

	// responseTime is when the response arrived, initialAge its age then
	// (RFC 9111 section 4.2.3), and lifetime how long it is fresh for.
	responseTime time.Time
	initialAge   time.Duration
	lifetime     time.Duration
	// noStale is set for a response that must not be served stale:
	// must-revalidate, proxy-revalidate or s-maxage.
	noStale bool
	// swr and sie are how long past its lifetime it may be answered while
	// it is fetched again, and when fetching it again fails (RFC 5861).
	swr, sie time.Duration
	// tags are what it can be purged by.
	tags []string

	etag, lastModified string
	size               int64
}

// age returns the entry's current age.
func (e *entry) age(now time.Time) time.Duration {
	return e.initialAge + max(now.Sub(e.responseTime), 0)
}

// staleFor reports whether the entry, stale at now, is so by no more than
// window.
func (e *entry) staleFor(now time.Time, window time.Duration) bool {
	return window > 0 && e.age(now)-e.lifetime <= window
}

// matches reports whether the entry answers a request with header h: the
// fields it varies on have the values they had.
func (e *entry) matches(h stdhttp.Header) bool {
	for i, name := range e.varyNames {
		if varyValue(h, name) != e.varyValues[i] {
			return false
		}
	}
	return true
}

// sameVariant reports whether e and o answer the same requests.
func (e *entry) sameVariant(o *entry) bool {
	if len(e.varyNames) != len(o.varyNames) {
		return false
	}
	for i := range e.varyNames {
		if e.varyNames[i] != o.varyNames[i] || e.varyValues[i] != o.varyValues[i] {
			return false
		}
	}
	return true
}

// entryOverhead is what an entry costs beyond its body, header and key,
// counted against the store's size.
const entryOverhead = 256

// sizeOf returns what e costs in the store under key.
func sizeOf(key string, e *entry) int64 {
	n := int64(len(key) + len(e.body) + entryOverhead)
	for k, vs := range e.header {
		n += int64(len(k))
		for _, v := range vs {
			n += int64(len(v)) + 16
		}
	}
	for i := range e.varyValues {
		n += int64(len(e.varyNames[i]) + len(e.varyValues[i]))
	}
	for _, t := range e.tags {
		n += int64(len(t)) + 16
	}
	return n
}

// maxVariants bounds the variants kept for one key; past it the oldest
// goes.
const maxVariants = 8

// shards is how many parts a store is split into, each with its lock, its
// share of the size and its own least recently used order.
const shards = 16

// store is the memory tier: responses by key, each key with its variants,
// bounded in size, the least recently used key evicted first.
type store struct {
	name    string
	seed    maphash.Seed
	maxSize atomic.Int64
	// capturing is what responses being stored hold so far, bounded by a
	// quarter of maxSize: past it, a response goes without being kept.
	capturing atomic.Int64
	shards    [shards]shard
	// disk is the zone's disk tier, or nil.
	disk atomic.Pointer[disk]
	// tags index the keys of the responses, in memory and on disk, by tag.
	tags tagIndex
	// background counts the fetches under way that no request waits for.
	background atomic.Int64

	refs int // under zones.mu
}

type shard struct {
	mu    sync.Mutex
	tags  *tagIndex
	items map[string]*item
	// fetches are the fetches under way, by key, that requests for the
	// same key wait for rather than fetch again.
	fetches map[string]*fetch
	// lru is the sentinel of a ring of items, the most recently used next
	// to it.
	lru  item
	size int64
}

// item is a key's variants.
type item struct {
	key        string
	variants   []*entry
	size       int64
	prev, next *item
}

func newStore(name string, maxSize int64) *store {
	s := &store{name: name, seed: maphash.MakeSeed(), tags: tagIndex{m: map[string]map[string]int{}}}
	s.maxSize.Store(maxSize)
	for i := range s.shards {
		sh := &s.shards[i]
		sh.items = map[string]*item{}
		sh.fetches = map[string]*fetch{}
		sh.tags = &s.tags
		sh.lru.prev, sh.lru.next = &sh.lru, &sh.lru
	}
	return s
}

func (s *store) shard(key string) *shard {
	return &s.shards[maphash.String(s.seed, key)%shards]
}

// shardMax is a shard's share of the store's size.
func (s *store) shardMax() int64 { return s.maxSize.Load() / shards }

// get returns the entry under key that answers a request with header h, or
// nil, and whether anything is stored under key; it marks the key used.
func (s *store) get(key string, h stdhttp.Header) (*entry, bool) {
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	it := sh.items[key]
	if it == nil {
		return nil, false
	}
	sh.touch(it)
	for i := len(it.variants) - 1; i >= 0; i-- {
		if e := it.variants[i]; e.matches(h) {
			return e, true
		}
	}
	return nil, true
}

// put stores e under key, in place of the variant it answers for, and
// evicts the least recently used keys past the shard's size.
func (s *store) put(key string, e *entry) {
	e.size = sizeOf(key, e)
	limit := s.shardMax()
	if e.size > limit {
		return
	}
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	it := sh.items[key]
	if it == nil {
		it = &item{key: key}
		sh.items[key] = it
		sh.link(it)
	} else {
		sh.touch(it)
	}
	replaced := false
	for i, v := range it.variants {
		if v.sameVariant(e) {
			it.variants = append(it.variants[:i], it.variants[i+1:]...)
			sh.grow(it, -v.size)
			sh.tags.remove(key, v.tags)
			replaced = true
			break
		}
	}
	if !replaced && len(it.variants) == maxVariants {
		sh.grow(it, -it.variants[0].size)
		sh.tags.remove(key, it.variants[0].tags)
		it.variants = append(it.variants[:0], it.variants[1:]...)
	}
	it.variants = append(it.variants, e)
	sh.grow(it, e.size)
	sh.tags.add(key, e.tags)
	for sh.size > limit && sh.lru.prev != &sh.lru {
		sh.drop(sh.lru.prev)
	}
}

// keep stores e under key in memory, and on disk when the zone has one.
func (s *store) keep(key string, e *entry) {
	s.put(key, e)
	if d := s.disk.Load(); d != nil {
		d.write(key, e)
	}
}

// replace stores e in place of old, if old is still stored under key, and
// on disk: a response freshened by a 304 does not bring back one removed
// meanwhile.
func (s *store) replace(key string, old, e *entry) {
	e.size = sizeOf(key, e)
	sh := s.shard(key)
	sh.mu.Lock()
	it := sh.items[key]
	replaced := false
	if it != nil {
		for i, v := range it.variants {
			if v == old {
				it.variants[i] = e
				sh.grow(it, e.size-old.size)
				sh.tags.remove(key, old.tags)
				sh.tags.add(key, e.tags)
				replaced = true
				break
			}
		}
	}
	sh.mu.Unlock()
	if d := s.disk.Load(); replaced && d != nil {
		d.write(key, e)
	}
}

// remove drops every variant under key, in memory and on disk, and
// reports whether there was any.
func (s *store) remove(key string) bool {
	sh := s.shard(key)
	sh.mu.Lock()
	it := sh.items[key]
	if it != nil {
		sh.drop(it)
	}
	sh.mu.Unlock()
	found := it != nil
	if d := s.disk.Load(); d != nil && d.remove(key) {
		found = true
	}
	return found
}

// clear drops everything the store keeps, in memory and on disk, and
// returns how many keys it held.
func (s *store) clear() int {
	n := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		n += len(sh.items)
		for sh.lru.next != &sh.lru {
			sh.drop(sh.lru.next)
		}
		sh.mu.Unlock()
	}
	if d := s.disk.Load(); d != nil {
		n = max(n, d.clear())
	}
	return n
}

// stats returns how many keys the store holds, and its size.
func (s *store) stats() (keys int, size int64) {
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		keys += len(sh.items)
		size += sh.size
		sh.mu.Unlock()
	}
	return keys, size
}

func (sh *shard) link(it *item) {
	it.prev, it.next = &sh.lru, sh.lru.next
	it.next.prev = it
	sh.lru.next = it
}

func (sh *shard) unlink(it *item) {
	it.prev.next, it.next.prev = it.next, it.prev
	it.prev, it.next = nil, nil
}

func (sh *shard) touch(it *item) {
	if sh.lru.next != it {
		sh.unlink(it)
		sh.link(it)
	}
}

func (sh *shard) grow(it *item, n int64) {
	it.size += n
	sh.size += n
}

func (sh *shard) drop(it *item) {
	sh.unlink(it)
	delete(sh.items, it.key)
	sh.size -= it.size
	for _, v := range it.variants {
		sh.tags.remove(it.key, v.tags)
	}
}

// tagIndex is the keys of a zone's responses by tag, each key counted once
// for each response, in memory or on disk, that carries the tag.
type tagIndex struct {
	mu sync.Mutex
	m  map[string]map[string]int
}

func (t *tagIndex) add(key string, tags []string) {
	if len(tags) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tag := range tags {
		keys := t.m[tag]
		if keys == nil {
			keys = map[string]int{}
			t.m[tag] = keys
		}
		keys[key]++
	}
}

func (t *tagIndex) remove(key string, tags []string) {
	if len(tags) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tag := range tags {
		keys := t.m[tag]
		if keys[key]--; keys[key] <= 0 {
			delete(keys, key)
			if len(keys) == 0 {
				delete(t.m, tag)
			}
		}
	}
}

// keys returns the keys of the responses tagged tag.
func (t *tagIndex) keys(tag string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	keys := make([]string, 0, len(t.m[tag]))
	for k := range t.m[tag] {
		keys = append(keys, k)
	}
	return keys
}

// zones are the stores in use, by name, so that a config reloaded keeps
// what its cache holds: the new config's caches take the store before the
// old config's give it back.
var zones = struct {
	sync.Mutex
	m map[string]*store
}{m: map[string]*store{}}

// acquire returns the store named name, sized maxSize, with the disk tier
// cfg describes or none, made if it is not in use. A zone whose disk changed
// directory takes the new one, read back from what it holds; one that names
// the directory of another zone's disk is refused.
func acquire(name string, maxSize int64, cfg *Disk) (*store, error) {
	zones.Lock()
	defer zones.Unlock()
	s := zones.m[name]
	fresh := s == nil
	if fresh {
		s = newStore(name, maxSize)
	}
	switch d := s.disk.Load(); {
	case cfg == nil:
		s.disk.Store(nil)
	case d != nil && d.dir == filepath.Clean(cfg.Path):
		d.setLimits(cfg)
	default:
		dir := filepath.Clean(cfg.Path)
		for other, o := range zones.m {
			if od := o.disk.Load(); other != name && od != nil && od.dir == dir {
				return nil, fmt.Errorf("the directory %s is zone %q's", dir, other)
			}
		}
		nd, err := newDisk(cfg, diskPool(), &s.tags)
		if err != nil {
			return nil, err
		}
		s.disk.Store(nd)
	}
	if fresh {
		zones.m[name] = s
	}
	s.maxSize.Store(maxSize)
	s.refs++
	return s, nil
}

// release gives back a hold acquire took; the store goes once none is left.
func (s *store) release() {
	zones.Lock()
	defer zones.Unlock()
	if s.refs--; s.refs == 0 && zones.m[s.name] == s {
		delete(zones.m, s.name)
	}
}
