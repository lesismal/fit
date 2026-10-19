// Package static is the static handler: it serves the files under a root
// directory.
//
// Small files are kept in memory and leave without being read again while
// they stay the same on disk: each request stats the file, one system call,
// and reads it again when its size or modification time moved. A file larger
// than max_file_bytes, or one that does not fit in what is left of
// max_cache_bytes, is read from disk each time.
//
// Responses carry Content-Type (from the extension, or sniffed from the
// first bytes as net/http does), Last-Modified, an ETag made of the
// modification time and the size, as nginx makes it, and Accept-Ranges.
// Conditional requests (If-None-Match, If-Modified-Since, If-Match,
// If-Unmodified-Since) and range requests, If-Range included, are answered as
// net/http.ServeContent answers them.
//
// A request path is taken as a path under the root once strip_prefix is
// removed from it. A path with a ".." segment, a NUL or a backslash is
// refused with 400, so a request cannot name a file outside the root by its
// path; symbolic links under the root are followed, wherever they lead. A
// directory is served by its first index file, and a directory asked for
// without a trailing slash is redirected to the path with one, as nginx and
// Caddy do. Only GET and HEAD are served; other methods get 405.
package static

import (
	"bytes"
	"errors"
	"io/fs"
	"mime"
	stdhttp "net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".static", New: func() any { return new(Static) }})
}

// The defaults for the cache limits.
const (
	DefaultMaxFileBytes  = 1 << 20
	DefaultMaxCacheBytes = 64 << 20
)

// Static serves files under a root directory.
type Static struct {
	Root          string   `json:"root" fit:"required" doc:"The directory files are served from."`
	Index         []string `json:"index,omitempty" doc:"The files that stand for a directory, tried in order; [\"index.html\"] when left out, and none for []."`
	StripPrefix   string   `json:"strip_prefix,omitempty" doc:"A prefix removed from the request path before it is looked up under the root; a request whose path lacks it gets 404."`
	MaxFileBytes  int64    `json:"max_file_bytes,omitempty" min:"0" doc:"The largest file kept in memory, in bytes; 1 MiB when left out."`
	MaxCacheBytes int64    `json:"max_cache_bytes,omitempty" min:"0" doc:"How many bytes of files are kept in memory at most; 64 MiB when left out."`

	root  string
	index []string
	cache cache
}

// Provision checks the root and the index names.
func (s *Static) Provision(ctx *module.Context) error {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return ctx.Errorf("root", "%v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return ctx.Errorf("root", "%v", err)
	}
	if !info.IsDir() {
		return ctx.Errorf("root", "%s is not a directory", root)
	}
	s.root = root
	s.index = s.Index
	if s.Index == nil {
		s.index = []string{"index.html"}
	}
	for _, name := range s.index {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			return ctx.Errorf("index", "%q is not a file name", name)
		}
	}
	if s.StripPrefix != "" && !strings.HasPrefix(s.StripPrefix, "/") {
		return ctx.Errorf("strip_prefix", "%q does not start with /", s.StripPrefix)
	}
	s.cache.maxFile = s.MaxFileBytes
	if s.cache.maxFile == 0 {
		s.cache.maxFile = DefaultMaxFileBytes
	}
	s.cache.maxBytes = s.MaxCacheBytes
	if s.cache.maxBytes == 0 {
		s.cache.maxBytes = DefaultMaxCacheBytes
	}
	s.cache.entries = make(map[string]*entry)
	return nil
}

// ServeHTTP serves the file the request's path names.
func (s *Static) ServeHTTP(c *fibhttp.Context) {
	r := c.Request
	if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead {
		respondError(c, stdhttp.StatusMethodNotAllowed, "Allow", "GET, HEAD")
		return
	}
	urlPath := r.URL.Path
	name, ok := strings.CutPrefix(urlPath, s.StripPrefix)
	if !ok {
		respondError(c, stdhttp.StatusNotFound)
		return
	}
	if !safePath(name) {
		respondError(c, stdhttp.StatusBadRequest)
		return
	}
	dir := name == "" || strings.HasSuffix(name, "/")
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	full := filepath.Join(s.root, filepath.FromSlash(name))
	info, err := os.Stat(full)
	if err != nil {
		s.cache.forget(full)
		respondError(c, statusOf(err))
		return
	}
	if info.IsDir() {
		if !dir {
			redirectToDir(c, urlPath)
			return
		}
		for _, index := range s.index {
			indexFull := filepath.Join(full, index)
			if info, err = os.Stat(indexFull); err == nil && info.Mode().IsRegular() {
				s.serveFile(c, path.Join(name, index), indexFull, info)
				return
			}
		}
		respondError(c, stdhttp.StatusNotFound)
		return
	}
	if dir || !info.Mode().IsRegular() {
		// A file asked for as a directory, or a device, a pipe or a socket.
		respondError(c, stdhttp.StatusNotFound)
		return
	}
	s.serveFile(c, name, full, info)
}

// serveFile serves the regular file at full, whose slash-separated name
// under the root is name, as info describes it.
func (s *Static) serveFile(c *fibhttp.Context, name, full string, info fs.FileInfo) {
	r := c.Request
	e := s.cache.get(full, info)
	if e == nil {
		served := s.serveFromDisk(c, name, full)
		if !served {
			respondError(c, stdhttp.StatusNotFound)
		}
		return
	}
	if !hasConditions(r) {
		// The header is shared by every response the file goes out in; fib
		// does not change a header it is handed.
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: e.header, Body: e.data})
		return
	}
	copyHeader(c.Header(), e.header)
	stdhttp.ServeContent(c, r, name, e.modTime, bytes.NewReader(e.data))
}

// serveFromDisk serves a file the cache does not keep. It reports false when
// the file cannot be opened.
func (s *Static) serveFromDisk(c *fibhttp.Context, name, full string) bool {
	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	h := c.Header()
	copyHeader(h, fileHeader(name, info, nil))
	if h.Get("Content-Type") == "" {
		// ServeContent sniffs the first bytes for a name with no known type.
		delete(h, "Content-Type")
	}
	stdhttp.ServeContent(c, c.Request, name, info.ModTime(), f)
	return true
}

// safePath reports whether name, a request path, is free of what could name a
// file outside the root, or a different file on some systems: a ".."
// segment, a NUL, or a backslash, which Windows takes as a separator.
func safePath(name string) bool {
	if strings.ContainsAny(name, "\x00\\") {
		return false
	}
	for segment := range strings.SplitSeq(name, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// redirectToDir redirects a request for a directory to its path with a
// trailing slash, keeping the query.
func redirectToDir(c *fibhttp.Context, urlPath string) {
	target := urlPath + "/"
	if q := c.Request.URL.RawQuery; q != "" {
		target += "?" + q
	}
	if strings.HasPrefix(target, "//") {
		// A path that would read as a host in the Location header.
		target = "/" + strings.TrimLeft(target, "/")
	}
	respondError(c, stdhttp.StatusMovedPermanently, "Location", target)
}

// statusOf maps an error from os.Stat to a status: 404 for a file that is
// not there, 403 for one that may not be read.
func statusOf(err error) int {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return stdhttp.StatusForbidden
	default:
		return stdhttp.StatusNotFound
	}
}

// respondError answers with status and a short text body, and with the
// header fields extra names in pairs of name and value.
func respondError(c *fibhttp.Context, status int, extra ...string) {
	body := []byte(strconv.Itoa(status) + " " + stdhttp.StatusText(status) + "\n")
	if len(extra) == 0 {
		_ = c.Respond(status, "text/plain; charset=utf-8", body)
		return
	}
	h := stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = []string{extra[i+1]}
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: h, Body: body})
}

func copyHeader(dst, src stdhttp.Header) {
	for key, values := range src {
		dst[key] = values
	}
}

// hasConditions reports whether r asks for anything but the whole file as it
// is: a range, or a condition on its validators.
func hasConditions(r *stdhttp.Request) bool {
	for _, key := range [...]string{"Range", "If-Modified-Since", "If-None-Match", "If-Match", "If-Unmodified-Since", "If-Range"} {
		if _, ok := r.Header[key]; ok {
			return true
		}
	}
	return false
}

// etag is a file's entity tag: its modification time and size in hex, as
// nginx makes it, but with the time in nanoseconds, so that two writes within
// one second that keep the size tell apart where the clock can.
func etag(info fs.FileInfo) string {
	return `"` + strconv.FormatInt(info.ModTime().UnixNano(), 16) + "-" + strconv.FormatInt(info.Size(), 16) + `"`
}

// fileHeader is what a response carrying the file says of it. data is its
// contents, to sniff its type from when its extension says none, or nil when
// they are not at hand, which leaves Content-Type out.
func fileHeader(name string, info fs.FileInfo, data []byte) stdhttp.Header {
	h := make(stdhttp.Header, 4)
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" && data != nil {
		contentType = stdhttp.DetectContentType(data)
	}
	if contentType != "" {
		h["Content-Type"] = []string{contentType}
	}
	h["Last-Modified"] = []string{info.ModTime().UTC().Format(stdhttp.TimeFormat)}
	h["Etag"] = []string{etag(info)}
	h["Accept-Ranges"] = []string{"bytes"}
	return h
}

// cache keeps the contents of small files, by path on disk.
type cache struct {
	maxFile  int64
	maxBytes int64

	mu      sync.RWMutex
	entries map[string]*entry
	bytes   int64
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

// get returns the cached file at full, which info describes, reading it when
// the cache holds nothing current. It returns nil for a file it does not
// keep, too large or past the cache's limit, or one it cannot read.
func (c *cache) get(full string, info fs.FileInfo) *entry {
	if info.Size() > c.maxFile {
		return nil
	}
	c.mu.RLock()
	e := c.entries[full]
	c.mu.RUnlock()
	if e != nil && e.current(info) {
		return e
	}
	data, err := os.ReadFile(full)
	if err != nil || int64(len(data)) != info.Size() {
		// Unreadable, or changed while it was read: served from disk this
		// time, and read again next time.
		c.forget(full)
		return nil
	}
	e = &entry{data: data, header: fileHeader(full, info, data), modTime: info.ModTime(), size: info.Size()}
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
		return nil
	}
	c.entries[full] = e
	c.bytes += e.size - freed
	return e
}

// forget drops what the cache holds of the file at full.
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
