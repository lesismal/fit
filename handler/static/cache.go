package static

import (
	"bytes"
	"hash/maphash"
	"io/fs"
	"math/bits"
	stdhttp "net/http"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib/bufferpool"
	"github.com/lesismal/fib/filewatch"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/dotfile"
)

// The bounds of what the cache remembers of request paths: how many, how
// many bytes of paths, and the longest path it remembers. A request path is
// up to max_header_bytes long; a client asking for paths that do not exist
// may fill the cache, but only up to these. A longer path names no file on
// most systems (PATH_MAX is 4096 on Linux) and is looked at on disk each
// time.
const (
	DefaultMaxLookups     = 16384
	DefaultMaxLookupBytes = 16 << 20
	MaxLookupPath         = 4096
)

// lookupOverhead is what a lookup costs beyond the bytes of its paths: the
// map's entry, the lookup and its FileInfo, counted at a round figure.
const lookupOverhead = 256

// cache keeps what the handler learned of request paths, and the contents
// of small files, by path on disk. Both are bounded: lookups by count and by
// bytes, which a client asking for paths that do not exist cannot grow
// past, and contents by bytes.
//
// A lookup a watch vouches for is trusted until the watch reports a change
// to what it depends on: the path it was resolved for, its compressed twins,
// the entries of the directory it named when it named one, and every
// directory on its way from the root. index finds the lookups a change names
// without going through them all. A lookup no watch vouches for, where the
// system has none or for a file that is a symbolic link, is trusted for the
// check interval.
//
// The lookups are split in shards by a hash of the request path, each with
// its own lock, index and share of the bounds, so that the file pool's
// workers keeping lookups of different paths do not wait for each other,
// nor fib's workers reading them. Once a shard is full, a path is kept only
// the second time it is looked up within a while (see doorkeeper), so that
// paths asked for once, as of a set of files far larger than the cache
// asked for at random, do not each take a shard's lock to push out another
// as rarely asked for. The contents of files are under a lock of their own.
type cache struct {
	maxFile  int64
	maxBytes int64
	// packs is how many compressed copies a file kept may have: one for
	// each of the handler's compress encodings.
	packs int

	// check is how long a lookup is trusted; 0 trusts none, and keeps none.
	check time.Duration
	// gen moves on with every change a watch reports, so that a lookup
	// resolved while one arrived is not kept as if it were current.
	gen atomic.Uint64
	// watching says the watch that vouches for watched lookups runs.
	watching atomic.Bool

	// seed hashes request paths to their shard and their doorkeeper bits;
	// shift takes a shard's number from a hash's top bits.
	seed   maphash.Seed
	shards []lookupShard
	shift  uint
	seen   doorkeeper

	// mu guards entries and bytes, the contents of files.
	mu      sync.RWMutex
	entries map[string]*entry
	bytes   int64
}

// lookupShard holds the lookups of the request paths whose hash falls on
// it, with their index, under its own lock, and bounded by its share of
// the cache's bounds.
type lookupShard struct {
	mu          sync.RWMutex
	lookups     map[string]*lookup
	lookupBytes int64
	// index holds, for each dependency a lookup has (see lookup.deps), the
	// request paths whose lookups have it.
	index      map[string]depNames
	maxLookups int
	maxBytes   int64
	// full says the shard holds as many lookups, or as many bytes, as it
	// may, so that keeping one more pushes another out: the doorkeeper then
	// decides whether a path is kept.
	full atomic.Bool
	// The padding keeps a shard's lock off the cache line of the next
	// shard's.
	_ [64]byte
}

// depNames are the request paths whose lookups have a dependency: most
// often one, held without a map of its own.
type depNames struct {
	one    string
	single bool
	more   map[string]struct{}
}

// add returns d with name.
func (d depNames) add(name string) depNames {
	switch {
	case d.more != nil:
		d.more[name] = struct{}{}
	case !d.single:
		return depNames{one: name, single: true}
	case d.one != name:
		return depNames{more: map[string]struct{}{d.one: {}, name: {}}}
	}
	return d
}

// remove returns d without name, and whether that leaves none.
func (d depNames) remove(name string) (depNames, bool) {
	if d.more != nil {
		delete(d.more, name)
		return d, len(d.more) == 0
	}
	if d.single && d.one == name {
		return depNames{}, true
	}
	return d, !d.single
}

// lookupShards is how many shards a cache's lookups are split in: twice
// the file pool's workers, which keep lookups, rounded up to a power of
// two, and from 16 to 256.
func lookupShards() int {
	n := 2 * max(16, 4*goruntime.GOMAXPROCS(0))
	return min(max(16, 1<<bits.Len(uint(n-1))), 256)
}

// initLookups splits the lookups in n shards, a power of two, with
// maxLookups and maxBytes shared among them.
func (c *cache) initLookups(n, maxLookups int, maxBytes int64) {
	c.seed = maphash.MakeSeed()
	c.shards = make([]lookupShard, n)
	c.shift = uint(64 - bits.TrailingZeros(uint(n)))
	for i := range c.shards {
		sh := &c.shards[i]
		sh.lookups = make(map[string]*lookup)
		sh.index = make(map[string]depNames)
		sh.maxLookups = max(1, maxLookups/n)
		sh.maxBytes = maxBytes / int64(n)
	}
	c.seen.init(maxLookups)
}

// hash is name's hash, which picks its shard and its doorkeeper bits.
func (c *cache) hash(name string) uint64 {
	return maphash.String(c.seed, name)
}

// shard is the shard of the request path whose hash is h.
func (c *cache) shard(h uint64) *lookupShard {
	return &c.shards[h>>c.shift&uint64(len(c.shards)-1)]
}

// lookup is what a request path named when it was last looked at: a file,
// with its compressed twins; a directory to list; a directory asked for
// without its slash, to redirect; or nothing to serve, and why.
type lookup struct {
	checked time.Time
	// gen is the cache's gen when the lookup began, before the disk was
	// looked at.
	gen uint64
	// watched says a watch reports every change that makes the lookup
	// stale, so that it stands until one does, past the check interval.
	watched bool
	// rel is the request path cleaned, slash-separated under the root, ""
	// for the root; deps are what a watch reports a change to that makes
	// the lookup stale: rel itself, and rel's entries ("rel/", "/" for the
	// root's) when rel is a directory whose index file or listing it found.
	rel  string
	deps []string
	// name and full are the file's slash-separated path under the root and
	// its path on disk, and info and stamp what a stat of it said; for a
	// listing, full is the directory.
	name, full string
	info       fs.FileInfo
	stamp      filewatch.Stamp
	// link says full is a symbolic link, whose target's changes a watch
	// does not see.
	link bool
	// twins are the file's compressed twins, one for each of the handler's
	// encodings in their order, with a nil info for one not on disk.
	twins             []twin
	redirect, listing bool
	status            int
	// size is what keeping it costs, counted when it is kept.
	size int64
}

// twin is a file's compressed twin on disk.
type twin struct {
	full  string
	info  fs.FileInfo
	stamp filewatch.Stamp
	link  bool
}

// entry is a file as last read.
type entry struct {
	data    []byte
	header  stdhttp.Header
	modTime time.Time
	// size is what keeping it costs: its data's bytes, and its compressed
	// copies'.
	size int64
	// stamp is the file's when it was read: its size, times and inode,
	// which tell apart a file replaced by one of the same size and
	// modification time.
	stamp filewatch.Stamp
	// packs are its copies compressed with the handler's compress
	// encodings, in their order, nil for a file not worth compressing: each
	// nil until it is asked for, then packing, and then made or unpacked.
	packs []atomic.Pointer[entry]
}

// lookup returns what the cache knows of name, whose hash is h, if it
// learned it within the check interval. Without wait, as on a fib worker,
// it returns nil rather than wait for the shard's lock, which a file pool
// worker keeping a lookup holds.
func (c *cache) lookup(name string, h uint64, wait bool) *lookup {
	if c.check <= 0 {
		return nil
	}
	sh := c.shard(h)
	if wait {
		sh.mu.RLock()
	} else if !sh.mu.TryRLock() {
		return nil
	}
	l := sh.lookups[name]
	sh.mu.RUnlock()
	if l == nil {
		return nil
	}
	if l.watched && c.watching.Load() {
		return l
	}
	if time.Since(l.checked) >= c.check {
		return nil
	}
	return l
}

// keep remembers l for name, whose hash is h, forgetting other paths of its
// shard, any of them, while the shard holds as many, or as many bytes, as
// it may. A name longer than MaxLookupPath is not remembered, nor is l when
// a watch reported a change after it began, nor anything with a check
// interval of 0, which trusts nothing kept. Once the shard is full, name is
// remembered only when the doorkeeper saw it lately.
func (c *cache) keep(name string, h uint64, l *lookup) {
	if len(name) > MaxLookupPath || c.check <= 0 {
		return
	}
	sh := c.shard(h)
	if sh.full.Load() && !c.seen.admit(h) {
		return
	}
	l.size = int64(len(name)+len(l.name)+len(l.full)) + lookupOverhead
	for _, t := range l.twins {
		l.size += int64(len(t.full))
	}
	for _, d := range l.deps {
		l.size += int64(len(d)) + indexOverhead
	}
	t := lockWait(&sh.mu, siteKeep)
	defer lockHold(&sh.mu, siteKeep, t)
	if c.gen.Load() != l.gen {
		// Whatever changed may be what l found.
		return
	}
	if old, ok := sh.lookups[name]; ok {
		sh.dropLocked(name, old)
	}
	pushed := false
	for other, ol := range sh.lookups {
		if len(sh.lookups) < sh.maxLookups && sh.lookupBytes+l.size <= sh.maxBytes {
			break
		}
		sh.dropLocked(other, ol)
		pushed = true
	}
	sh.lookups[name] = l
	sh.lookupBytes += l.size
	for _, d := range l.deps {
		sh.index[d] = sh.index[d].add(name)
	}
	sh.full.Store(pushed || len(sh.lookups) >= sh.maxLookups)
}

// indexOverhead is what a dependency costs in the index beyond its bytes,
// counted at a round figure.
const indexOverhead = 64

// dropLocked forgets l, the lookup of name.
func (sh *lookupShard) dropLocked(name string, l *lookup) {
	delete(sh.lookups, name)
	sh.lookupBytes -= l.size
	for _, d := range l.deps {
		if names, ok := sh.index[d]; ok {
			if names, empty := names.remove(name); empty {
				delete(sh.index, d)
			} else {
				sh.index[d] = names
			}
		}
	}
}

// dropDependentsLocked forgets the lookups that depend on dep.
func (sh *lookupShard) dropDependentsLocked(dep string) {
	names, ok := sh.index[dep]
	switch {
	case !ok:
	case names.more == nil:
		if l := sh.lookups[names.one]; l != nil {
			sh.dropLocked(names.one, l)
		}
	default:
		for name := range names.more {
			if l := sh.lookups[name]; l != nil {
				sh.dropLocked(name, l)
			}
		}
	}
}

// entriesOf is the dependency on the entries of dir, a slash-separated
// path under the root, "" or "." for the root.
func entriesOf(dir string) string {
	if dir == "" || dir == "." {
		return "/"
	}
	return dir + "/"
}

// event forgets what the cache learned that ev, a change a watch reports,
// may have made stale: the lookups that depend on the path it names, or
// that go through it, a directory, or every lookup. The contents of files
// are kept: each is checked against its file's stamp when it is served.
// gen moves on before any shard is looked at, so that a lookup that began
// before ev and is kept after its shard was is refused by keep, and one
// kept before is found here.
func (c *cache) event(ev filewatch.Event) {
	c.gen.Add(1)
	for i := range c.shards {
		c.shards[i].event(ev)
	}
}

// event is cache.event for one shard.
func (sh *lookupShard) event(ev filewatch.Event) {
	t := lockWait(&sh.mu, siteEvent)
	defer lockHold(&sh.mu, siteEvent, t)
	defer func() { sh.full.Store(len(sh.lookups) >= sh.maxLookups) }()
	switch {
	case ev.All:
		clear(sh.lookups)
		clear(sh.index)
		sh.lookupBytes = 0
		return
	case ev.Dir:
		// What was found under it, or not found there, was found in a
		// directory not there any more.
		prefix := ev.Name + "/"
		for name, l := range sh.lookups {
			if l.rel == ev.Name || strings.HasPrefix(l.rel, prefix) {
				sh.dropLocked(name, l)
			}
		}
	default:
		sh.dropDependentsLocked(ev.Name)
		for _, enc := range encodings {
			if original, ok := strings.CutSuffix(ev.Name, enc.suffix); ok {
				// A twin, which its original's lookup found or not.
				sh.dropDependentsLocked(original)
			}
		}
	}
	sh.dropDependentsLocked(entriesOf(path.Dir(ev.Name)))
}

// doorkeeper remembers, roughly, which request paths were looked up lately:
// two bits of a bitmap for each, set from its hash, which a path seen
// before finds both set. Once a quarter of the bits are set, it forgets
// them all and starts again, so that a path is told seen with a chance of
// one in sixteen at most when it was not, and it remembers about as many
// paths as the cache keeps lookups. It takes no lock: two
// lookups at once may lose a bit, or a count, which only makes a path kept
// a time later, or the bitmap cleared a little early or late.
type doorkeeper struct {
	words []atomic.Uint64
	mask  uint64
	// set counts the bits set since the bitmap was last cleared, and limit
	// is how many clear it.
	set      atomic.Int64
	limit    int64
	clearing atomic.Bool
}

// doorkeeperBits is how many bits the doorkeeper has for each lookup the
// cache may keep.
const doorkeeperBits = 8

// init sizes the bitmap for a cache of n lookups.
func (d *doorkeeper) init(n int) {
	size := max(1<<bits.Len(uint(n*doorkeeperBits-1)), 64)
	d.words = make([]atomic.Uint64, size/64)
	d.mask = uint64(size - 1)
	d.limit = int64(size / 4)
}

// admit reports whether the path whose hash is h was seen since the bitmap
// was last cleared, and remembers it.
func (d *doorkeeper) admit(h uint64) bool {
	i, j := h&d.mask, h>>32&d.mask
	wi, bi := &d.words[i/64], uint64(1)<<(i%64)
	wj, bj := &d.words[j/64], uint64(1)<<(j%64)
	if wi.Load()&bi != 0 && wj.Load()&bj != 0 {
		return true
	}
	var added int64
	if wi.Or(bi)&bi == 0 {
		added++
	}
	if wj.Or(bj)&bj == 0 {
		added++
	}
	if d.set.Add(added) >= d.limit && d.clearing.CompareAndSwap(false, true) {
		for k := range d.words {
			d.words[k].Store(0)
		}
		d.set.Store(0)
		d.clearing.Store(false)
	}
	return false
}

// current returns the contents of the file at full, if the cache holds them
// as read from the file stamp stands for. Without wait, as on a fib worker,
// it returns nil rather than wait for the lock, which the file pool holds
// to keep a file it read.
func (c *cache) current(full string, stamp filewatch.Stamp, wait bool) *entry {
	if wait {
		c.mu.RLock()
	} else if !c.mu.TryRLock() {
		return nil
	}
	e := c.entries[full]
	c.mu.RUnlock()
	if e != nil && e.stamp == stamp {
		return e
	}
	return nil
}

// fits reports whether a file of size bytes at full would be kept: no
// larger than maxFile, and within what is left of maxBytes. One that would
// not is sent from disk rather than read whole into memory to go once.
func (c *cache) fits(full string, size int64) bool {
	if size > c.maxFile {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var freed int64
	if old := c.entries[full]; old != nil {
		freed = old.size
	}
	return c.bytes-freed+size <= c.maxBytes
}

// load reads the file at full, which info and stamp describe, and keeps its
// contents if they fit. It returns nil for a file it cannot read, or one that
// changed while it was read; and the contents, kept or not, otherwise. It
// runs on the file pool.
func (c *cache) load(full string, info fs.FileInfo, stamp filewatch.Stamp, v variant) *entry {
	data, err := os.ReadFile(full)
	if err != nil || int64(len(data)) != info.Size() {
		// Unreadable, or changed while it was read: served from disk this
		// time, and read again next time.
		c.forget(full)
		return nil
	}
	e := &entry{data: data, header: fileHeader(v, info, data), modTime: info.ModTime(), size: info.Size(), stamp: stamp}
	if v.encoding == "" {
		e.packs = packSlots(c.packs, e.header, data)
	}
	t := lockWait(&c.mu, siteLoad)
	defer lockHold(&c.mu, siteLoad, t)
	old := c.entries[full]
	var freed int64
	if old != nil {
		freed = old.size
	}
	if c.bytes-freed+e.size > c.maxBytes {
		if old != nil {
			delete(c.entries, full)
			c.bytes -= freed
		}
		return e
	}
	c.entries[full] = e
	c.bytes += e.size - freed
	return e
}

// forget drops the contents the cache holds of the file at full.
func (c *cache) forget(full string) {
	c.mu.RLock()
	_, ok := c.entries[full]
	c.mu.RUnlock()
	if !ok {
		return
	}
	c.mu.Lock()
	if e := c.entries[full]; e != nil {
		c.bytes -= e.size
		delete(c.entries, full)
	}
	c.mu.Unlock()
}

// resolve looks at what name, a request path within the root that check
// passed, names on disk: a file; a directory's index file or listing when
// name ends in a slash; or a redirect for a directory without one. It runs on
// the file pool.
func (s *Static) resolve(name string) *lookup {
	l := &lookup{checked: time.Now(), gen: s.cache.gen.Load()}
	dir := name == "" || strings.HasSuffix(name, "/")
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	// A request path written clean, as most are, is cleaned without a copy.
	name = strings.TrimPrefix(path.Clean(name), "/")
	l.rel, l.deps = name, []string{name}
	// Watched before the disk is looked at, so that no change after the
	// look goes unreported.
	l.watched = s.watch.Watch(path.Dir(name))
	full := filepath.Join(s.root, filepath.FromSlash(name))
	info, link, err := statLink(full)
	if err != nil {
		s.cache.forget(full)
		l.status = statusOf(err)
		return l
	}
	if s.foldCase && !onDisk(s.root, name) {
		// Not the names on disk: a request a route that matches names, by
		// a script's extension or a protected prefix, may have missed.
		l.status = stdhttp.StatusNotFound
		return l
	}
	if info.IsDir() {
		if !dir {
			l.redirect = true
			return l
		}
		l.watched = s.watch.Watch(name) && l.watched
		l.deps = append(l.deps, entriesOf(name))
		for _, index := range s.index {
			indexFull := filepath.Join(full, index)
			if info, link, err = statLink(indexFull); err == nil && info.Mode().IsRegular() {
				l.name, l.full, l.info, l.stamp, l.link = path.Join(name, index), indexFull, info, filewatch.StampOf(info), link
				return s.file(l)
			}
		}
		if s.Browse {
			l.full, l.listing = full, true
			return l
		}
		l.status = stdhttp.StatusNotFound
		return l
	}
	if dir || !info.Mode().IsRegular() {
		// A file asked for as a directory, or a device, a pipe or a socket.
		l.status = stdhttp.StatusNotFound
		return l
	}
	l.name, l.full, l.info, l.stamp, l.link = name, full, info, filewatch.StampOf(info), link
	return s.file(l)
}

// onDisk is dotfile.OnDisk, which tests replace to stand for a file system
// that folds case.
var onDisk = dotfile.OnDisk

// file finishes l, a file found to serve: on a root whose file system folds
// case, refused 404 when its extension is one a fastcgi handler runs as a
// script's, in any case, so that a script's source is not served for a
// request a script route missed; otherwise served, with its compressed
// twins.
func (s *Static) file(l *lookup) *lookup {
	if s.foldCase && dotfile.Script(l.name) {
		return &lookup{checked: l.checked, gen: l.gen, watched: l.watched, rel: l.rel, deps: l.deps, status: stdhttp.StatusNotFound}
	}
	s.twins(l)
	if l.watched && l.link {
		// The watch sees the link replaced, not the file it leads to
		// changed.
		l.watched = false
	}
	for _, t := range l.twins {
		if l.watched && t.info != nil && t.link {
			l.watched = false
		}
	}
	return l
}

// statLink stats full as os.Stat does, following a symbolic link, and
// reports whether full is one: with one lstat for a path that is not a
// link, the common case, where os.Stat and a check for a link would make
// two calls, and a stat after it for one that is.
func statLink(full string) (fs.FileInfo, bool, error) {
	info, err := os.Lstat(full)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return info, false, err
	}
	info, err = os.Stat(full)
	return info, true, err
}

// twins looks for the compressed twins of l's file.
func (s *Static) twins(l *lookup) {
	if len(s.precompressed) == 0 {
		return
	}
	l.twins = make([]twin, len(s.precompressed))
	for i, enc := range s.precompressed {
		full := l.full + enc.suffix
		if info, link, err := statLink(full); err == nil && info.Mode().IsRegular() {
			l.twins[i] = twin{full: full, info: info, stamp: filewatch.StampOf(info), link: link}
		}
	}
}

// variant is how l's file goes out to r's client: as it is, or as the
// compressed twin the client prefers among those on disk. It returns the
// variant, and the path, stat and stamp of the file that carries it.
func (s *Static) variant(r *stdhttp.Request, l *lookup) (variant, string, fs.FileInfo, filewatch.Stamp) {
	v := variant{name: l.name, vary: s.vary, defaultType: s.DefaultType}
	if i := s.preferred(r, l); i >= 0 {
		v.encoding = s.precompressed[i].name
		t := &l.twins[i]
		return v, t.full, t.info, t.stamp
	}
	return v, l.full, l.info, l.stamp
}

// serveMemory answers with a file the cache holds: whole, or, for a
// conditional or ranged request, as ServeContent answers it, gathered and
// written whole, so that nothing waits for the client.
func serveMemory(c *fibhttp.Context, name string, e *entry) {
	r := c.Request
	if !hasConditions(r) {
		// The header is shared by every response the file goes out in; fib
		// does not change a header it is handed.
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: e.header, Body: e.data})
		return
	}
	w := &gathered{header: make(stdhttp.Header, len(e.header)+2)}
	copyHeader(w.header, e.header)
	// ServeContent always sets the status.
	stdhttp.ServeContent(w, r, name, e.modTime, bytes.NewReader(e.data))
	_ = c.WriteResponse(fibhttp.Response{StatusCode: w.status, Header: w.header, Body: w.body.Bytes()})
}

// serveRead answers with data, the whole file v names, which info
// describes, read into a buffer of package bufferpool, as serveMemory
// answers with a file the cache holds, and gives the buffer back. The
// response is written with WriteResponseCopy, whose copy the send path
// gives back to the pool as it is done with it, where WriteResponseOwned
// would leave a body above a small reply's to the collector on HTTP/1; and
// its header as a block of lines built in a pooled buffer, with no map nor
// strings made for it, but for a conditional or ranged request, which
// ServeContent answers.
func serveRead(c *fibhttp.Context, v variant, info fs.FileInfo, data []byte) {
	defer bufferpool.Put(data)
	if hasConditions(c.Request) {
		serveMemory(c, v.name, &entry{data: data, header: fileHeader(v, info, data), modTime: info.ModTime()})
		return
	}
	block := appendFileHeader(bufferpool.Get(headerBlockSize)[:0], v, info, data)
	_ = c.WriteResponseCopy(fibhttp.Response{StatusCode: stdhttp.StatusOK, HeaderBlock: block, Body: data})
	bufferpool.Put(block)
}

// headerBlockSize is room for the lines appendFileHeader writes.
const headerBlockSize = 512

// gathered is a ResponseWriter that keeps what it is given. What
// ServeContent writes of a file in memory is no larger than the file, but
// for a multipart body's few part headers.
type gathered struct {
	header stdhttp.Header
	status int
	body   bytes.Buffer
}

func (g *gathered) Header() stdhttp.Header { return g.header }

func (g *gathered) WriteHeader(status int) {
	if g.status == 0 {
		g.status = status
	}
}

func (g *gathered) Write(p []byte) (int, error) {
	if g.status == 0 {
		g.status = stdhttp.StatusOK
	}
	return g.body.Write(p)
}
