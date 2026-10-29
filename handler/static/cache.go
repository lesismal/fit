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
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

// DefaultMaxLookups bounds the request paths the cache remembers.
const DefaultMaxLookups = 16384

// cache keeps what the handler learned of request paths, and the contents
// of small files, by path on disk. Both are bounded: lookups by count, which
// a client asking for paths that do not exist cannot grow past, and
// contents by bytes.
type cache struct {
	maxFile    int64
	maxBytes   int64
	maxLookups int
	// check is how long a lookup is trusted; 0 trusts none.
	check time.Duration

	mu      sync.RWMutex
	lookups map[string]*lookup
	entries map[string]*entry
	bytes   int64
}

// lookup is what a request path named when it was last looked at: a file,
// with its compressed twins; a directory to list; a directory asked for
// without its slash, to redirect; or nothing to serve, and why.
type lookup struct {
	checked time.Time
	// name and full are the file's slash-separated path under the root and
	// its path on disk, and info what a stat of it said; for a listing,
	// full is the directory.
	name, full string
	info       fs.FileInfo
	// twins are the file's compressed twins, one for each of the handler's
	// encodings in their order, with a nil info for one not on disk.
	twins             []twin
	redirect, listing bool
	status            int
}

// twin is a file's compressed twin on disk.
type twin struct {
	full string
	info fs.FileInfo
}

// entry is a file as last read.
type entry struct {
	data    []byte
	header  stdhttp.Header
	modTime time.Time
	size    int64
}

// current reports whether e still stands for the file info describes.
func (e *entry) current(info fs.FileInfo) bool {
	return e.size == info.Size() && e.modTime.Equal(info.ModTime())
}

// lookup returns what the cache knows of name, if it learned it within the
// check interval.
func (c *cache) lookup(name string) *lookup {
	c.mu.RLock()
	l := c.lookups[name]
	c.mu.RUnlock()
	if l == nil || time.Since(l.checked) >= c.check {
		return nil
	}
	return l
}

// keep remembers l for name, forgetting another path when the cache holds
// as many as it may, and the contents of a file that is gone.
func (c *cache) keep(name string, l *lookup) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.lookups[name]; !ok && len(c.lookups) >= c.maxLookups {
		for other := range c.lookups {
			delete(c.lookups, other)
			break
		}
	}
	c.lookups[name] = l
}

// current returns the contents of the file at full, if the cache holds them
// as info describes the file.
func (c *cache) current(full string, info fs.FileInfo) *entry {
	c.mu.RLock()
	e := c.entries[full]
	c.mu.RUnlock()
	if e != nil && e.current(info) {
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

// load reads the file at full, which info describes, and keeps its contents
// if they fit. It returns nil for a file it cannot read, or one that changed
// while it was read; and the contents, kept or not, otherwise. It runs on the
// file pool.
func (c *cache) load(full string, info fs.FileInfo, v variant) *entry {
	data, err := os.ReadFile(full)
	if err != nil || int64(len(data)) != info.Size() {
		// Unreadable, or changed while it was read: served from disk this
		// time, and read again next time.
		c.forget(full)
		return nil
	}
	e := &entry{data: data, header: fileHeader(v, info, data), modTime: info.ModTime(), size: info.Size()}
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

// resolve looks at what name, a request path within the root that safePath
// passed, names on disk: a file; a directory's index file or listing when
// name ends in a slash; or a redirect for a directory without one. It runs on
// the file pool.
func (s *Static) resolve(name string) *lookup {
	l := &lookup{checked: time.Now()}
	dir := name == "" || strings.HasSuffix(name, "/")
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	full := filepath.Join(s.root, filepath.FromSlash(name))
	info, err := os.Stat(full)
	if err != nil {
		s.cache.forget(full)
		l.status = statusOf(err)
		return l
	}
	if info.IsDir() {
		if !dir {
			l.redirect = true
			return l
		}
		for _, index := range s.index {
			indexFull := filepath.Join(full, index)
			if info, err = os.Stat(indexFull); err == nil && info.Mode().IsRegular() {
				l.name, l.full, l.info = path.Join(name, index), indexFull, info
				s.twins(l)
				return l
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
	l.name, l.full, l.info = name, full, info
	s.twins(l)
	return l
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
			l.twins[i] = twin{full: full, info: info}
		}
	}
}

// variant is how l's file goes out to r's client: as it is, or as the
// compressed twin the client prefers among those on disk. It returns the
// variant, and the path and stat of the file that carries it.
func (s *Static) variant(r *stdhttp.Request, l *lookup) (variant, string, fs.FileInfo) {
	v := variant{name: l.name, vary: len(s.precompressed) > 0}
	if i := s.preferred(r, l); i >= 0 {
		v.encoding = s.precompressed[i].name
		return v, l.twins[i].full, l.twins[i].info
	}
	return v, l.full, l.info
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
	stdhttp.ServeContent(w, r, name, e.modTime, bytes.NewReader(e.data))
	if w.status == 0 {
		w.status = stdhttp.StatusOK
	}
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
