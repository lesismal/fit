package cache

import (
	"hash/maphash"
	stdhttp "net/http"
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

	etag, lastModified string
	size               int64
}

// age returns the entry's current age.
func (e *entry) age(now time.Time) time.Duration {
	return e.initialAge + max(now.Sub(e.responseTime), 0)
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

	refs int // under zones.mu
}

type shard struct {
	mu    sync.Mutex
	items map[string]*item
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
	s := &store{name: name, seed: maphash.MakeSeed()}
	s.maxSize.Store(maxSize)
	for i := range s.shards {
		sh := &s.shards[i]
		sh.items = map[string]*item{}
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
			replaced = true
			break
		}
	}
	if !replaced && len(it.variants) == maxVariants {
		sh.grow(it, -it.variants[0].size)
		it.variants = append(it.variants[:0], it.variants[1:]...)
	}
	it.variants = append(it.variants, e)
	sh.grow(it, e.size)
	for sh.size > limit && sh.lru.prev != &sh.lru {
		sh.drop(sh.lru.prev)
	}
}

// replace stores e in place of old, if old is still stored under key: a
// response freshened by a 304 does not bring back one removed meanwhile.
func (s *store) replace(key string, old, e *entry) {
	e.size = sizeOf(key, e)
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	it := sh.items[key]
	if it == nil {
		return
	}
	for i, v := range it.variants {
		if v == old {
			it.variants[i] = e
			sh.grow(it, e.size-old.size)
			return
		}
	}
}

// remove drops every variant under key.
func (s *store) remove(key string) {
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if it := sh.items[key]; it != nil {
		sh.drop(it)
	}
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
}

// zones are the stores in use, by name, so that a config reloaded keeps
// what its cache holds: the new config's caches take the store before the
// old config's give it back.
var zones = struct {
	sync.Mutex
	m map[string]*store
}{m: map[string]*store{}}

// acquire returns the store named name, sized maxSize, made if it is not in
// use.
func acquire(name string, maxSize int64) *store {
	zones.Lock()
	defer zones.Unlock()
	s := zones.m[name]
	if s == nil {
		s = newStore(name, maxSize)
		zones.m[name] = s
	}
	s.maxSize.Store(maxSize)
	s.refs++
	return s
}

// release gives back a hold acquire took; the store goes once none is left.
func (s *store) release() {
	zones.Lock()
	defer zones.Unlock()
	if s.refs--; s.refs == 0 && zones.m[s.name] == s {
		delete(zones.m, s.name)
	}
}
