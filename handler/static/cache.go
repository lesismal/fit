package static

import (
	"bytes"
	"io/fs"
	stdhttp "net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
type cache struct {
	maxFile        int64
	maxBytes       int64
	maxLookups     int
	maxLookupBytes int64
	// check is how long a lookup is trusted; 0 trusts none.
	check time.Duration

	// gen moves on with every change a watch reports, so that a lookup
	// resolved while one arrived is not kept as if it were current.
	gen atomic.Uint64
	// watching says the watch that vouches for watched lookups runs.
	watching atomic.Bool

	mu          sync.RWMutex
	lookups     map[string]*lookup
	lookupBytes int64
	// index holds, for each dependency a lookup has (see lookup.deps), the
	// request paths whose lookups have it.
	index   map[string]map[string]struct{}
	entries map[string]*entry
	bytes   int64
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
}

// entry is a file as last read.
type entry struct {
	data    []byte
	header  stdhttp.Header
	modTime time.Time
	size    int64
	// stamp is the file's when it was read: its size, times and inode,
	// which tell apart a file replaced by one of the same size and
	// modification time.
	stamp filewatch.Stamp
}

// lookup returns what the cache knows of name, if it learned it within the
// check interval.
func (c *cache) lookup(name string) *lookup {
	c.mu.RLock()
	l := c.lookups[name]
	c.mu.RUnlock()
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

// keep remembers l for name, forgetting other paths, any of them, while
// the cache holds as many, or as many bytes, as it may. A name longer than
// MaxLookupPath is not remembered, nor is l when a watch reported a change
// after it began.
func (c *cache) keep(name string, l *lookup) {
	if len(name) > MaxLookupPath {
		return
	}
	l.size = int64(len(name)+len(l.name)+len(l.full)) + lookupOverhead
	for _, t := range l.twins {
		l.size += int64(len(t.full))
	}
	for _, d := range l.deps {
		l.size += int64(len(d)) + indexOverhead
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen.Load() != l.gen {
		// Whatever changed may be what l found.
		return
	}
	if old, ok := c.lookups[name]; ok {
		c.dropLocked(name, old)
	}
	for other, ol := range c.lookups {
		if len(c.lookups) < c.maxLookups && c.lookupBytes+l.size <= c.maxLookupBytes {
			break
		}
		c.dropLocked(other, ol)
	}
	c.lookups[name] = l
	c.lookupBytes += l.size
	for _, d := range l.deps {
		names := c.index[d]
		if names == nil {
			names = make(map[string]struct{})
			c.index[d] = names
		}
		names[name] = struct{}{}
	}
}

// indexOverhead is what a dependency costs in the index beyond its bytes,
// counted at a round figure.
const indexOverhead = 64

// dropLocked forgets l, the lookup of name.
func (c *cache) dropLocked(name string, l *lookup) {
	delete(c.lookups, name)
	c.lookupBytes -= l.size
	for _, d := range l.deps {
		if names := c.index[d]; names != nil {
			delete(names, name)
			if len(names) == 0 {
				delete(c.index, d)
			}
		}
	}
}

// dropDependentsLocked forgets the lookups that depend on dep.
func (c *cache) dropDependentsLocked(dep string) {
	for name := range c.index[dep] {
		if l := c.lookups[name]; l != nil {
			c.dropLocked(name, l)
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
func (c *cache) event(ev filewatch.Event) {
	c.gen.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case ev.All:
		clear(c.lookups)
		clear(c.index)
		c.lookupBytes = 0
		return
	case ev.Dir:
		// What was found under it, or not found there, was found in a
		// directory not there any more.
		prefix := ev.Name + "/"
		for name, l := range c.lookups {
			if l.rel == ev.Name || strings.HasPrefix(l.rel, prefix) {
				c.dropLocked(name, l)
			}
		}
	default:
		c.dropDependentsLocked(ev.Name)
		for _, enc := range encodings {
			if original, ok := strings.CutSuffix(ev.Name, enc.suffix); ok {
				// A twin, which its original's lookup found or not.
				c.dropDependentsLocked(original)
			}
		}
	}
	c.dropDependentsLocked(entriesOf(path.Dir(ev.Name)))
}

// current returns the contents of the file at full, if the cache holds them
// as read from the file stamp stands for.
func (c *cache) current(full string, stamp filewatch.Stamp) *entry {
	c.mu.RLock()
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	l.rel, l.deps = name, []string{name}
	// Watched before the disk is looked at, so that no change after the
	// look goes unreported.
	l.watched = s.watch.Watch(path.Dir(name))
	full := filepath.Join(s.root, filepath.FromSlash(name))
	info, err := os.Stat(full)
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
			if info, err = os.Stat(indexFull); err == nil && info.Mode().IsRegular() {
				l.name, l.full, l.info, l.stamp = path.Join(name, index), indexFull, info, filewatch.StampOf(info)
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
	l.name, l.full, l.info, l.stamp = name, full, info, filewatch.StampOf(info)
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
	if l.watched && isLink(l.full) {
		// The watch sees the link replaced, not the file it leads to
		// changed.
		l.watched = false
	}
	for _, t := range l.twins {
		if l.watched && t.info != nil && isLink(t.full) {
			l.watched = false
		}
	}
	return l
}

// isLink reports whether full is a symbolic link.
func isLink(full string) bool {
	info, err := os.Lstat(full)
	return err == nil && info.Mode()&fs.ModeSymlink != 0
}

// twins looks for the compressed twins of l's file.
func (s *Static) twins(l *lookup) {
	if len(s.precompressed) == 0 {
		return
	}
	l.twins = make([]twin, len(s.precompressed))
	for i, enc := range s.precompressed {
		full := l.full + enc.suffix
		if info, err := os.Stat(full); err == nil && info.Mode().IsRegular() {
			l.twins[i] = twin{full: full, info: info, stamp: filewatch.StampOf(info)}
		}
	}
}

// variant is how l's file goes out to r's client: as it is, or as the
// compressed twin the client prefers among those on disk. It returns the
// variant, and the path, stat and stamp of the file that carries it.
func (s *Static) variant(r *stdhttp.Request, l *lookup) (variant, string, fs.FileInfo, filewatch.Stamp) {
	v := variant{name: l.name, vary: len(s.precompressed) > 0}
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
