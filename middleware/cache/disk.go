package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/internal/filepool"
)

// Disk is where a zone keeps responses on disk, beside those in memory.
type Disk struct {
	Path       string `json:"path" fit:"required" doc:"The directory the zone's responses are kept in, made if it is missing. It is the zone's alone: no other zone may name it. What it holds is found again when fit starts."`
	MaxBytes   int64  `json:"max_bytes,omitempty" renamed:"max_size" min:"0" default:"1073741824" doc:"The most bytes the zone keeps on disk; past it the least recently used responses go. 1 GiB when left out or 0."`
	MaxEntries int    `json:"max_entries,omitempty" min:"0" default:"100000" doc:"The most responses the zone keeps on disk, which bounds the memory their index takes, a few hundred bytes each. 100000 when left out or 0."`
}

// Defaults of Disk's params.
const (
	DefaultDiskMaxBytes   = 1 << 30
	DefaultDiskMaxEntries = 100000
)

// The disk pool's bounds.
const (
	// diskTimeout bounds how long a request waits for a response read from
	// disk; past it the request goes on as a miss.
	diskTimeout = 5 * time.Second
	// diskQueue bounds the reads and writes waiting for the pool.
	diskQueue = 4096
	// maxPendingWrites bounds the bytes of responses waiting to be written.
	maxPendingWrites = 64 << 20
	// maxMeta bounds a file's fields and the rest of what it records.
	maxMeta = 1 << 20
)

// diskPool returns the pool every disk tier reads and writes its files on,
// apart from the static handler's: 4 workers a CPU, at least 16. Tests
// replace it.
var diskPool = sync.OnceValue(func() *filepool.Pool {
	return filepool.New(max(16, 4*goruntime.GOMAXPROCS(0)), diskQueue, diskTimeout)
})

// disk is a zone's disk tier: one file a response, and an index of them in
// memory, so that a request learns whether the disk has its response
// without touching the disk. Every file operation runs on the pool.
type disk struct {
	dir  string
	pool *filepool.Pool
	// tags is the zone's tag index, which the disk's entries are in too.
	tags *tagIndex

	maxSize    atomic.Int64
	maxEntries atomic.Int64
	// pending is what responses waiting to be written hold.
	pending atomic.Int64
	// seq orders writes and removals, so that a write queued before its key
	// was removed does not bring the response back.
	seq atomic.Uint64
	// ready is set once the index has been read back from the directory.
	ready chan struct{}

	mu    sync.Mutex
	items map[string]*diskItem
	lru   diskItem
	size  int64
	count int
	// trash are files whose entries are gone, to be removed on the pool.
	trash    []string
	draining bool
	// cleared is the seq of the last clear: a write queued before it is not
	// kept.
	cleared uint64
}

// diskItem is a key's variants on disk. removed, when set, is the seq of
// the key's removal: a write queued before it is not kept.
type diskItem struct {
	key        string
	variants   []*diskEntry
	removed    uint64
	prev, next *diskItem
}

// diskEntry is a response on disk: what deciding to serve it takes, in
// meta, whose header and body stay on disk, and its file.
type diskEntry struct {
	meta *entry
	file string
	size int64
}

func newDisk(cfg *Disk, pool *filepool.Pool, tags *tagIndex) (*disk, error) {
	dir := filepath.Clean(cfg.Path)
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o700); err != nil {
		return nil, err
	}
	d := &disk{dir: dir, pool: pool, tags: tags, items: map[string]*diskItem{}, ready: make(chan struct{})}
	d.lru.prev, d.lru.next = &d.lru, &d.lru
	d.setLimits(cfg)
	// Reading the index back is one goroutine's work, once, and requests do
	// not wait for it: until it is done they find less on disk.
	go d.rebuild()
	return d, nil
}

func (d *disk) setLimits(cfg *Disk) {
	d.maxSize.Store(cmpOr(cfg.MaxBytes, DefaultDiskMaxBytes))
	d.maxEntries.Store(int64(cmpOr(cfg.MaxEntries, DefaultDiskMaxEntries)))
}

func cmpOr[T int | int64](v, def T) T {
	if v > 0 {
		return v
	}
	return def
}

// fileName returns the file of key's variant e, relative to the directory.
func fileName(key string, e *entry) string {
	h := sha256.New()
	h.Write([]byte(key))
	for i, name := range e.varyNames {
		h.Write([]byte{0})
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(e.varyValues[i]))
	}
	sum := hex.EncodeToString(h.Sum(nil)[:20])
	return filepath.Join(sum[:2], sum)
}

// get returns key's entry on disk that answers a request with header h, or
// nil, and marks the key used.
func (d *disk) get(key string, h stdhttp.Header) *diskEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	it := d.items[key]
	if it == nil {
		return nil
	}
	d.touch(it)
	for i := len(it.variants) - 1; i >= 0; i-- {
		if de := it.variants[i]; de.meta.matches(h) {
			return de
		}
	}
	return nil
}

// write queues e, key's response, to be written to disk. A response finds
// no room when the pool's queue is full or too much waits to be written:
// it is kept in memory alone.
func (d *disk) write(key string, e *entry) {
	n := int64(len(e.body))
	if d.pending.Add(n) > maxPendingWrites {
		d.pending.Add(-n)
		return
	}
	seq := d.seq.Add(1)
	done := func() { d.pending.Add(-n) }
	if !d.pool.Submit(func() {
		defer done()
		de, err := d.writeFile(key, e)
		if err == nil {
			d.add(key, de, seq)
		}
		d.drain()
	}, done) {
		done()
	}
}

// remove drops key's responses, and reports whether it had any: their
// files go on the pool, and a write of key queued before now is not kept.
func (d *disk) remove(key string) bool {
	seq := d.seq.Add(1)
	d.mu.Lock()
	it := d.items[key]
	if it == nil {
		it = &diskItem{key: key}
		d.items[key] = it
		d.link(it)
	} else {
		d.touch(it)
	}
	found := len(it.variants) > 0
	for _, de := range it.variants {
		d.forget(key, de)
	}
	it.variants = nil
	it.removed = seq
	d.evict()
	d.mu.Unlock()
	d.drainLater()
	return found
}

// clear drops every response on disk, and returns how many keys had one: a
// write queued before now is not kept.
func (d *disk) clear() int {
	seq := d.seq.Add(1)
	d.mu.Lock()
	n := 0
	for d.lru.next != &d.lru {
		it := d.lru.next
		if len(it.variants) > 0 {
			n++
		}
		for _, de := range it.variants {
			d.forget(it.key, de)
		}
		d.unlink(it)
		delete(d.items, it.key)
	}
	d.cleared = seq
	d.mu.Unlock()
	d.drainLater()
	return n
}

// add puts de in the index under key, in place of the variant it answers
// for, unless key was removed after the write was queued, at seq.
func (d *disk) add(key string, de *diskEntry, seq uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	it := d.items[key]
	if it == nil {
		it = &diskItem{key: key}
		d.items[key] = it
		d.link(it)
	} else {
		d.touch(it)
	}
	if seq < it.removed || seq != 0 && seq < d.cleared {
		d.trash = append(d.trash, de.file)
		return
	}
	for i, v := range it.variants {
		if v.meta.sameVariant(de.meta) {
			if v.meta.responseTime.After(de.meta.responseTime) {
				// A newer response is there already: de, read back from
				// the directory, was its file before it.
				return
			}
			d.size -= v.size
			d.count--
			d.tags.remove(key, v.meta.tags)
			it.variants = append(it.variants[:i], it.variants[i+1:]...)
			if v.file != de.file {
				d.trash = append(d.trash, v.file)
			}
			break
		}
	}
	if len(it.variants) == maxVariants {
		d.forget(key, it.variants[0])
		it.variants = append(it.variants[:0], it.variants[1:]...)
	}
	it.variants = append(it.variants, de)
	d.size += de.size
	d.count++
	d.tags.add(key, de.meta.tags)
	d.evict()
}

// drop removes de, key's entry, which could not be read back.
func (d *disk) drop(key string, de *diskEntry) {
	d.mu.Lock()
	if it := d.items[key]; it != nil {
		for i, v := range it.variants {
			if v == de {
				it.variants = append(it.variants[:i], it.variants[i+1:]...)
				d.forget(key, de)
				break
			}
		}
	}
	d.mu.Unlock()
	d.drainLater()
}

// forget drops de, key's entry, from the counts and the tags, and trashes
// its file. d.mu is held.
func (d *disk) forget(key string, de *diskEntry) {
	d.size -= de.size
	d.count--
	d.tags.remove(key, de.meta.tags)
	d.trash = append(d.trash, de.file)
}

// evict drops the least recently used keys past the disk's bounds. A key
// counts as an entry while it is in the index, as a removed one is. d.mu is
// held.
func (d *disk) evict() {
	maxSize, maxEntries := d.maxSize.Load(), d.maxEntries.Load()
	for (d.size > maxSize || int64(len(d.items)) > maxEntries) && d.lru.prev != &d.lru {
		it := d.lru.prev
		for _, de := range it.variants {
			d.forget(it.key, de)
		}
		d.unlink(it)
		delete(d.items, it.key)
	}
}

// drainLater has the trash emptied on the pool, unless that is under way;
// if the pool is full, the next job empties it.
func (d *disk) drainLater() {
	d.mu.Lock()
	if d.draining || len(d.trash) == 0 {
		d.mu.Unlock()
		return
	}
	d.draining = true
	d.mu.Unlock()
	if !d.pool.Submit(func() { d.drain() }, func() { d.drain() }) {
		d.mu.Lock()
		d.draining = false
		d.mu.Unlock()
	}
}

// drain removes the trashed files. It runs on the pool.
func (d *disk) drain() {
	for {
		d.mu.Lock()
		files := d.trash
		d.trash = nil
		if len(files) == 0 {
			d.draining = false
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()
		for _, f := range files {
			_ = os.Remove(filepath.Join(d.dir, f))
		}
	}
}

func (d *disk) link(it *diskItem) {
	it.prev, it.next = &d.lru, d.lru.next
	it.next.prev = it
	d.lru.next = it
}

func (d *disk) unlink(it *diskItem) {
	it.prev.next, it.next.prev = it.next, it.prev
	it.prev, it.next = nil, nil
}

func (d *disk) touch(it *diskItem) {
	if d.lru.next != it {
		d.unlink(it)
		d.link(it)
	}
}

// stats returns how many responses the disk holds, and their size.
func (d *disk) stats() (count int, size int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count, d.size
}

// A file is a header of 12 bytes, the magic, a version and the length of
// the metadata that follows, then the metadata, as JSON, then the body.
const (
	fileMagic   = "FITC"
	fileVersion = 1
	fileHead    = 12
)

// fileMeta is what a file records besides the body.
type fileMeta struct {
	Key          string         `json:"key"`
	Status       int            `json:"status"`
	Header       stdhttp.Header `json:"header"`
	VaryNames    []string       `json:"vary_names,omitempty"`
	VaryValues   []string       `json:"vary_values,omitempty"`
	ResponseTime int64          `json:"response_time"`
	InitialAge   time.Duration  `json:"initial_age"`
	Lifetime     time.Duration  `json:"lifetime"`
	NoStale      bool           `json:"no_stale,omitempty"`
	SWR          time.Duration  `json:"stale_while_revalidate,omitempty"`
	SIE          time.Duration  `json:"stale_if_error,omitempty"`
	BodyLen      int64          `json:"body_len"`
}

// writeFile writes key's response e to its file, through a file in tmp
// renamed into place, so that a file is whole or not there. It runs on the
// pool.
func (d *disk) writeFile(key string, e *entry) (*diskEntry, error) {
	meta, err := json.Marshal(fileMeta{
		Key: key, Status: e.status, Header: e.header, VaryNames: e.varyNames, VaryValues: e.varyValues,
		ResponseTime: e.responseTime.UnixNano(), InitialAge: e.initialAge, Lifetime: e.lifetime,
		NoStale: e.noStale, SWR: e.swr, SIE: e.sie, BodyLen: int64(len(e.body)),
	})
	if err != nil {
		return nil, err
	}
	if len(meta) > maxMeta {
		return nil, errors.New("cache: the response's fields are too large to keep on disk")
	}
	name := fileName(key, e)
	full := filepath.Join(d.dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Join(d.dir, "tmp"), "w")
	if err != nil {
		return nil, err
	}
	head := make([]byte, fileHead, fileHead+len(meta))
	copy(head, fileMagic)
	head[4] = fileVersion
	binary.BigEndian.PutUint32(head[8:], uint32(len(meta)))
	_, err = tmp.Write(append(head, meta...))
	if err == nil {
		_, err = tmp.Write(e.body)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), full)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	m := *e
	m.header, m.body = nil, nil
	return &diskEntry{meta: &m, file: name, size: int64(fileHead+len(meta)) + int64(len(e.body))}, nil
}

// read reads de's response back, whole. It runs on the pool.
func (d *disk) read(de *diskEntry) (*entry, error) {
	data, err := os.ReadFile(filepath.Join(d.dir, de.file))
	if err != nil {
		return nil, err
	}
	meta, body, err := parseFile(data)
	if err != nil {
		return nil, err
	}
	e := entryOfMeta(meta)
	e.body = body
	return e, nil
}

// parseFile splits a file into its metadata and its body.
func parseFile(data []byte) (*fileMeta, []byte, error) {
	if len(data) < fileHead || string(data[:4]) != fileMagic || data[4] != fileVersion {
		return nil, nil, errors.New("cache: not a file of the cache's")
	}
	n := int(binary.BigEndian.Uint32(data[8:]))
	if n > maxMeta || fileHead+n > len(data) {
		return nil, nil, errors.New("cache: a file cut short")
	}
	var meta fileMeta
	if err := json.Unmarshal(data[fileHead:fileHead+n], &meta); err != nil {
		return nil, nil, err
	}
	body := data[fileHead+n:]
	if int64(len(body)) != meta.BodyLen || len(meta.VaryNames) != len(meta.VaryValues) {
		return nil, nil, errors.New("cache: a file cut short")
	}
	return &meta, body, nil
}

// readMeta reads a file's metadata alone, for the index.
func readMeta(path string) (*fileMeta, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	head := make([]byte, fileHead)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, 0, err
	}
	if string(head[:4]) != fileMagic || head[4] != fileVersion {
		return nil, 0, errors.New("cache: not a file of the cache's")
	}
	n := int64(binary.BigEndian.Uint32(head[8:]))
	if n > maxMeta {
		return nil, 0, errors.New("cache: metadata too large")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(f, raw); err != nil {
		return nil, 0, err
	}
	var meta fileMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, 0, err
	}
	if info.Size() != fileHead+n+meta.BodyLen || len(meta.VaryNames) != len(meta.VaryValues) {
		return nil, 0, errors.New("cache: a file cut short")
	}
	return &meta, info.Size(), nil
}

// entryOfMeta makes the entry a file records, without its body. Its fields'
// values are clipped, as a stored response's are, so that a hook appending
// to a value copies it.
func entryOfMeta(m *fileMeta) *entry {
	for k, v := range m.Header {
		m.Header[k] = slices.Clip(v)
	}
	return &entry{
		status: m.Status, header: m.Header,
		varyNames: m.VaryNames, varyValues: m.VaryValues,
		responseTime: time.Unix(0, m.ResponseTime), initialAge: m.InitialAge, lifetime: m.Lifetime,
		noStale: m.NoStale, swr: m.SWR, sie: m.SIE, tags: tagsOf(m.Header),
		etag: m.Header.Get("Etag"), lastModified: m.Header.Get("Last-Modified"),
	}
}

// rebuild reads the index back from the directory: every file's metadata,
// the files that are not whole or not the cache's removed, and what tmp
// holds, a write cut short, cleared.
func (d *disk) rebuild() {
	defer close(d.ready)
	tmp := filepath.Join(d.dir, "tmp")
	if entries, err := os.ReadDir(tmp); err == nil {
		for _, e := range entries {
			_ = os.Remove(filepath.Join(tmp, e.Name()))
		}
	}
	subs, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	for _, sub := range subs {
		if !sub.IsDir() || len(sub.Name()) != 2 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(d.dir, sub.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			name := filepath.Join(sub.Name(), f.Name())
			meta, size, err := readMeta(filepath.Join(d.dir, name))
			if err != nil || !strings.HasPrefix(f.Name(), sub.Name()) {
				_ = os.Remove(filepath.Join(d.dir, name))
				continue
			}
			e := entryOfMeta(meta)
			if fileName(meta.Key, e) != name {
				_ = os.Remove(filepath.Join(d.dir, name))
				continue
			}
			e.header = nil
			d.add(meta.Key, &diskEntry{meta: e, file: name, size: size}, 0)
		}
	}
	d.drain()
}
