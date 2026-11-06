package medialib

import (
	"container/list"
	"errors"
	"sync"

	"github.com/lesismal/fib/media/mp4"
)

// maxTableWaiters is the most calls that may wait for one table being read;
// one more is answered ErrBusy.
const maxTableWaiters = 1024

// tableKey names a sample table: a track of a file as the catalog has it.
type tableKey struct {
	abs   string
	size  int64
	mod   int64 // UnixNano
	track int
}

// table is a sample table kept, or being read.
type table struct {
	key     tableKey
	t       *mp4.Table // nil while it is read
	bytes   int64
	waiters []func(*mp4.Table, error)
	elem    *list.Element // in the LRU once read
}

// tables is a library's sample tables, bounded by max_table_bytes: a table
// is admitted by the bytes mp4's TableBytes says it takes before it is
// read, and the least recently used are dropped for it.
type tables struct {
	mu   sync.Mutex
	max  int64
	used int64
	m    map[tableKey]*table
	lru  list.List // of *table, the most recently used at the front
	// hits counts the calls of Table that found their table kept, and
	// misses those that did not: the table read from the file, a read
	// under way waited for, or the call refused.
	hits, misses uint64
}

func (ts *tables) init(max int64) {
	ts.max, ts.m = max, map[tableKey]*table{}
}

// TableStats is what a library's sample tables take, and how often a table
// asked for was kept (Hits) or not (Misses).
type TableStats struct {
	Tables int    `json:"tables"`
	Bytes  int64  `json:"bytes"`
	Max    int64  `json:"max"`
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
}

// Tables returns what the library's sample tables take.
func (l *Library) Tables() TableStats {
	ts := &l.tables
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return TableStats{Tables: ts.lru.Len(), Bytes: ts.used, Max: ts.max, Hits: ts.hits, Misses: ts.misses}
}

// Table gets the sample table of the track at index track of the
// rendition's MP4 (its Track.Index), and calls fn with it, or with the
// error: at once, on the caller's goroutine, when the library keeps it, or
// when it fails at once (ErrNotServed for a rendition listed with an error,
// ErrTooLarge for a table larger than max_table_bytes, ErrBusy, ErrRemoved);
// and otherwise from the library's pool once it is read, which fn must not
// block. A table is read once, for every call that waits for it, and kept,
// bounded by max_table_bytes, until the file changes or leaves the
// catalog. The table is immutable, and shared.
func (l *Library) Table(r *Rendition, track int, fn func(*mp4.Table, error)) {
	if l.removed.Load() {
		fn(nil, ErrRemoved)
		return
	}
	m := r.MP4()
	if m == nil || track < 0 || track >= len(m.Tracks) {
		fn(nil, ErrNotServed)
		return
	}
	t := m.Tracks[track]
	key := tableKey{abs: r.abs, size: r.Size, mod: r.ModTime.UnixNano(), track: track}
	ts := &l.tables
	ts.mu.Lock()
	if e := ts.m[key]; e != nil {
		if e.t != nil {
			ts.hits++
			ts.lru.MoveToFront(e.elem)
			ts.mu.Unlock()
			fn(e.t, nil)
			return
		}
		ts.misses++
		if len(e.waiters) >= maxTableWaiters {
			ts.mu.Unlock()
			fn(nil, ErrBusy)
			return
		}
		e.waiters = append(e.waiters, fn)
		ts.mu.Unlock()
		return
	}
	ts.misses++
	need := t.TableBytes()
	if need > ts.max {
		ts.mu.Unlock()
		fn(nil, ErrTooLarge)
		return
	}
	for ts.used+need > ts.max && ts.lru.Len() > 0 {
		ts.evict(ts.lru.Back().Value.(*table))
	}
	if ts.used+need > ts.max {
		// Tables being read hold the rest.
		ts.mu.Unlock()
		fn(nil, ErrBusy)
		return
	}
	e := &table{key: key, bytes: need, waiters: []func(*mp4.Table, error){fn}}
	ts.m[key] = e
	ts.used += need
	ts.mu.Unlock()
	file := r.File
	l.async(func() {
		h, err := l.OpenFile(&file)
		var tb *mp4.Table
		if err == nil {
			tb, err = t.ReadTable(h)
			h.Close()
		}
		ts.done(e, tb, err)
	}, func(err error) { ts.done(e, nil, err) })
}

// done ends the read of e, with tb or err, and calls those that wait.
func (ts *tables) done(e *table, tb *mp4.Table, err error) {
	ts.mu.Lock()
	waiters := e.waiters
	e.waiters = nil
	switch {
	case ts.m[e.key] != e:
		// Dropped while it was read: those waiting get it, and it is not
		// kept.
	case err != nil:
		delete(ts.m, e.key)
		ts.used -= e.bytes
	default:
		e.t = tb
		ts.used += tb.Bytes() - e.bytes
		e.bytes = tb.Bytes()
		e.elem = ts.lru.PushFront(e)
	}
	ts.mu.Unlock()
	if errors.Is(err, mp4.ErrSampleData) || errors.Is(err, mp4.ErrMalformed) {
		err = errors.Join(ErrNotServed, err)
	}
	for _, fn := range waiters {
		fn(tb, err)
	}
}

// evict drops e, read. ts.mu is held.
func (ts *tables) evict(e *table) {
	delete(ts.m, e.key)
	ts.lru.Remove(e.elem)
	ts.used -= e.bytes
}

// forget drops e, read or being read. ts.mu is held.
func (ts *tables) forget(e *table) {
	if e.t != nil {
		ts.evict(e)
		return
	}
	delete(ts.m, e.key)
	ts.used -= e.bytes
}

// resize sets max_table_bytes, dropping the least recently used past it.
func (ts *tables) resize(max int64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.max = max
	for ts.used > ts.max && ts.lru.Len() > 0 {
		ts.evict(ts.lru.Back().Value.(*table))
	}
}

// drop drops the tables of the file at abs.
func (ts *tables) drop(abs string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for k, e := range ts.m {
		if k.abs == abs {
			ts.forget(e)
		}
	}
}

// keep drops the tables of the files cat does not have as they were read.
func (ts *tables) keep(cat *Catalog) {
	have := map[tableKey]bool{}
	for _, it := range cat.Items {
		for _, list := range [][]*Rendition{it.Renditions, it.Audio} {
			for _, r := range list {
				have[tableKey{abs: r.abs, size: r.Size, mod: r.ModTime.UnixNano()}] = true
			}
		}
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for k, e := range ts.m {
		if !have[tableKey{abs: k.abs, size: k.size, mod: k.mod}] {
			ts.forget(e)
		}
	}
}

// clear drops every table.
func (ts *tables) clear() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, e := range ts.m {
		ts.forget(e)
	}
}
