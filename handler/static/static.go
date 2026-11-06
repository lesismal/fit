// Package static is the static handler: it serves the files under a root
// directory.
//
// No file operation runs on a fib worker. What the handler learns of a
// request path, the file it names, its index file or its compressed twins,
// and the contents of small files, is kept in memory and trusted for
// check_interval, 5s by default, or until a watch of the directories under
// the root, on Linux, reports a change to it, within moments of the change:
// a file written, replaced or removed, a directory on its way renamed, or a
// symbolic link on the way to the root pointed elsewhere. A request it
// answers from what it knows costs no system call. A file's contents are
// served from memory while the file's stamp, its size, modification and
// status change times and inode, is what it was when they were read, which
// tells apart a file replaced by one of the same size and modification time.
// A check_interval of 0 looks at the disk for every request. What it keeps
// of request paths is bounded, to 16384 paths and 16 MiB, and a path longer
// than 4096 bytes is not kept. Anything else, a path not seen lately, a file
// to read, open or list, goes to a pool of goroutines of the package's own
// for the disk, bounded in size and queue, with a timeout, and is answered
// from there. A file larger
// than max_file_bytes, or one that does not fit in what is left of
// max_cache_bytes, is sent from disk each time, through fib's SendFile:
// sendfile(2) on a plain HTTP/1 connection, and pieces read on fib's own
// file pool elsewhere.
//
// Responses carry Content-Type (from the extension, or sniffed from the
// first bytes as net/http does), Last-Modified, an ETag made of the
// modification time and the size, as nginx makes it, and Accept-Ranges.
// Conditional requests (If-None-Match, If-Modified-Since, If-Match,
// If-Unmodified-Since) and range requests, If-Range included, are answered as
// net/http.ServeContent answers them, but for a request for several ranges of
// a file sent from disk, which is answered with the whole file.
//
// A request path is taken as a path under the root once strip_prefix is
// removed from it. A path with a ".." segment, a NUL or a backslash is
// refused with 400, so a request cannot name a file outside the root by its
// path; symbolic links under the root are followed, wherever they lead. A
// path with a segment that starts with a dot, a dotfile such as .git/config,
// .env or .htpasswd, or a file in a dot directory, is answered 404, as a file
// not there is, but for /.well-known/ under the root, unless dotfiles is
// allow; the same holds for try_files candidates and index files, and
// listings leave dotfiles out whatever dotfiles says. A
// directory is served by its first index file, and a directory asked for
// without a trailing slash is redirected to the path with one, as nginx and
// Caddy do. Only GET and HEAD are served; other methods get 405.
//
// On a root whose file system folds case, as Windows' and macOS' do by
// default, which is found once when the handler is provisioned, a path not
// written as the names on disk are, or a file whose extension a fastcgi
// handler runs as a script's, in any case, is answered 404, so that /x.PHP,
// which a \.php$ route misses, never serves x.php's source. Both are looked
// at with the disk, on the file pool, and kept with what the handler knows
// of the path; a root that does not fold case costs nothing more.
package static

import (
	"errors"
	"io/fs"
	"mime"
	stdhttp "net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib/filewatch"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/dotfile"
	"github.com/lesismal/fit/internal/filepool"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".static", New: func() any { return new(Static) }, Doc: "Serves files from a directory, with directory listings, precompressed variants and small files kept in memory."})
}

// The defaults for the cache and listing limits, and for check_interval.
const (
	DefaultMaxFileBytes  = 1 << 20
	DefaultMaxCacheBytes = 64 << 20
	DefaultBrowseMax     = 10000
	DefaultCheckInterval = 5 * time.Second
)

// Static serves files under a root directory.
type Static struct {
	Root          string   `json:"root" fit:"required" doc:"The directory files are served from. On a file system that folds case, as Windows' and macOS' do by default, a request whose path is not written as the names on disk are, or for a file whose extension a fastcgi handler's split_path names, in any case, is answered 404, so that a route matching names, as \\.php$, is not passed by."`
	Index         []string `json:"index,omitempty" default:"[\"index.html\"]" doc:"The files that stand for a directory, tried in order; [\"index.html\"] when left out, and none for []."`
	StripPrefix   string   `json:"strip_prefix,omitempty" doc:"A prefix removed from the request path before it is looked up under the root; a request whose path lacks it gets 404."`
	MaxFileBytes  int64    `json:"max_file_bytes,omitempty" min:"0" default:"1048576" doc:"The largest file kept in memory, in bytes; 1 MiB when left out."`
	MaxCacheBytes int64    `json:"max_cache_bytes,omitempty" min:"0" default:"67108864" doc:"How many bytes of files are kept in memory at most; 64 MiB when left out."`
	Precompressed []string `json:"precompressed,omitempty" enum:"br,zstd,gzip" doc:"Encodings whose compressed twins, name.br, name.zst and name.gz, are served in place of a file to a client that accepts them: the one it prefers by Accept-Encoding, and among equals the first listed. None when left out."`
	TryFiles      []string `json:"try_files,omitempty" doc:"Paths tried in turn for a request, as nginx's try_files: {path} stands for the request's path, and one ending in / for a directory, served by its index file. The first that exists is served, and when none does the answer is 404; [\"{path}\", \"{path}/\", \"/index.html\"] serves a single-page app. A directory asked for without its slash is not redirected then."`
	Browse        bool     `json:"browse,omitempty" doc:"List a directory that has no index file, rather than answering 404. Files whose names start with a dot are left out."`
	BrowseMax     int      `json:"browse_max_entries,omitempty" min:"0" default:"10000" doc:"The most entries a listing reads and shows; a directory with more is listed in part, and says so. 10000 when left out or 0."`
	Dotfiles      string   `json:"dotfiles,omitempty" enum:"deny,allow" default:"\"deny\"" doc:"Whether files and directories whose names start with a dot, as .git, .env and .htpasswd do, are served: deny answers a request for one, or for anything under a dot directory, 404, as for a file not there, but /.well-known/ under the root, for ACME's HTTP-01 challenge and security.txt; allow serves them as other files. Listings leave them out either way. On Windows deny also hides a path with a segment shaped as an 8.3 short name, as GIT~1 for .git, which NTFS may give a dotfile. deny when left out."`

	CheckInterval *config.Duration `json:"check_interval,omitempty" default:"\"5s\"" doc:"How long what the handler learned of a request path, the file it names and its validators, is trusted before the disk is looked at again. On Linux a watch of the directories under the root reports a change within moments, a file written, replaced or removed, a directory renamed, or a symbolic link on the way to the root pointed elsewhere, and this bounds what it cannot see, as a write another machine makes on a network file system; elsewhere a change shows within it. A file is told by its size, modification and status change times and inode, so one replaced by another of the same size and modification time is seen too. 0 looks at the disk for every request, with no watch, so that a response always carries the file as it is on disk then. 5s when left out."`

	root         string
	index        []string
	hideDotfiles bool
	// hideShortNames is dotfiles deny on Windows, where a dotfile is also
	// reachable by its 8.3 short name (.git as GIT~1): paths with a segment
	// of that shape are hidden too; see dotfile.HasShortName.
	hideShortNames bool
	// refuseAliases is set on Windows, which opens a file by names not its
	// own, x.php::$DATA, x.php. or "x.php ", and a device for NUL or
	// CON.txt: paths with such a segment are refused 400, whatever dotfiles
	// says; see dotfile.Alias.
	refuseAliases bool
	// foldCase says root's file system folds case (dotfile.FoldsCase), as
	// Windows' and macOS' do by default: a request whose path is not
	// written as the names on disk are, or for a file whose extension a
	// fastcgi handler runs as a script's, in any case, is answered 404, so
	// that /x.PHP, which a \.php$ route misses, does not serve x.php's
	// source. It is looked at when the disk is, not on a fib worker.
	foldCase      bool
	precompressed []encoding
	cache         cache
	stall         time.Duration
	pool          *filepool.Pool
	// watch reports changes under the root to the cache; nil where the
	// system has none, or with a check_interval of 0.
	watch *filewatch.Watcher
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
	s.hideDotfiles = s.Dotfiles != dotfile.Allow
	s.hideShortNames = s.hideDotfiles && dotfile.ShortNames
	s.refuseAliases = dotfile.Aliases
	s.foldCase = dotfile.FoldsCase(root)
	for _, name := range s.index {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			return ctx.Errorf("index", "%q is not a file name", name)
		}
		if s.refuseAliases && dotfile.Alias(name) {
			return ctx.Errorf("index", "%q is not a file name on Windows, which opens another file or a device for it", name)
		}
		if s.hideDotfiles && strings.HasPrefix(name, ".") {
			return ctx.Errorf("index", "%q starts with a dot, which dotfiles deny hides; set dotfiles to allow to serve it", name)
		}
	}
	for _, name := range s.Precompressed {
		s.precompressed = append(s.precompressed, encodings[name])
	}
	for i, t := range s.TryFiles {
		if !strings.HasPrefix(t, "/") && !strings.HasPrefix(t, "{path}") {
			return ctx.Params.Field("try_files").Index(i).Errorf("%q starts with neither / nor {path}", t)
		}
		if s.refuseAliases && dotfile.Alias(strings.ReplaceAll(t, "{path}", "/x")) {
			return ctx.Params.Field("try_files").Index(i).Errorf("%q names no file on Windows, which opens another file or a device for it", t)
		}
		if s.check(strings.ReplaceAll(t, "{path}", "/x")) == stdhttp.StatusNotFound {
			return ctx.Params.Field("try_files").Index(i).Errorf("%q names a path with a segment starting with a dot, or on Windows one shaped as an 8.3 short name, which dotfiles deny hides; set dotfiles to allow to serve it", t)
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
	s.cache.lookups = make(map[string]*lookup)
	s.cache.maxLookups = DefaultMaxLookups
	s.cache.maxLookupBytes = DefaultMaxLookupBytes
	s.cache.index = make(map[string]map[string]struct{})
	s.cache.check = DefaultCheckInterval
	if s.CheckInterval != nil {
		s.cache.check = s.CheckInterval.Std()
	}
	s.stall = ctx.WriteStallTimeout()
	s.pool = defaultPool()
	if s.cache.check > 0 {
		// Where the system offers no watch, the check interval alone
		// bounds how long a change goes unseen.
		if w, err := filewatch.New(root, s.cache.event); err == nil {
			s.watch = w
		}
	}
	return nil
}

// Cleanup stops the watch of the root.
func (s *Static) Cleanup() {
	_ = s.watch.Close()
}

// ServeHTTP serves the file the request's path names: at once from what
// the cache knows of it, or else from the file pool.
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
	if status := s.check(name); status != 0 {
		respondError(c, status)
		return
	}
	if s.serve(c, name, urlPath, nil) {
		return
	}
	s.queue(c, name, urlPath)
}

// serve answers the request for name, whose URL path is urlPath, or try_files
// for it. Without q, on a fib worker, it answers only from what the cache
// knows and holds, and reports false, answering nothing, when it would have
// to look at the disk. With q, on the file pool, it looks, and claims the
// request from q before it answers.
func (s *Static) serve(c *fibhttp.Context, name, urlPath string, q *queued) bool {
	if s.TryFiles == nil {
		l := s.find(name, q != nil)
		if l == nil {
			return false
		}
		switch {
		case l.redirect:
			if q.claim() {
				redirectToDir(c, urlPath)
			}
		case l.status != 0:
			if q.claim() {
				respondError(c, l.status)
			}
		default:
			return s.answer(c, l, urlPath, q)
		}
		return true
	}
	requested := "/" + strings.TrimPrefix(name, "/")
	for _, t := range s.TryFiles {
		candidate := strings.ReplaceAll(t, "{path}", requested)
		if s.check(candidate) != 0 {
			continue
		}
		l := s.find(candidate, q != nil)
		switch {
		case l == nil:
			return false
		case l.redirect, l.status != 0:
			continue
		}
		return s.answer(c, l, path.Clean(candidate)+"/", q)
	}
	if q.claim() {
		respondError(c, stdhttp.StatusNotFound)
	}
	return true
}

// find returns what the cache knows of name, a request path within the
// root, when it was learned within check_interval; or, with disk, what a look
// at the disk finds, which the cache then keeps. Without disk it returns nil
// for a name the cache does not know.
func (s *Static) find(name string, disk bool) *lookup {
	if l := s.cache.lookup(name); l != nil {
		return l
	}
	if !disk {
		return nil
	}
	// name may point into the request's head, which the cache would keep
	// whole for as long as it keeps the name.
	name = strings.Clone(name)
	l := s.resolve(name)
	s.cache.keep(name, l)
	return l
}

// answer serves the file or the listing l stands for, as serve does.
func (s *Static) answer(c *fibhttp.Context, l *lookup, listPath string, q *queued) bool {
	if l.listing {
		if q == nil {
			return false
		}
		s.list(c, listPath, l.full, q)
		return true
	}
	v, full, info, stamp := s.variant(c.Request, l)
	e := s.cache.current(full, stamp)
	if e == nil && q != nil && s.cache.fits(full, info.Size()) {
		e = s.cache.load(full, info, stamp, v)
	}
	switch {
	case e != nil:
		if q.claim() {
			serveMemory(c, v.name, e)
		}
	case q == nil:
		return false
	default:
		s.sendFromDisk(c, v, full, q)
	}
	return true
}

// queued is a request on its way through the file pool, which either the
// pool's work or the pool's timeout answers, whichever claims it first.
type queued struct {
	state atomic.Int32
	timer *time.Timer
	// sending says SendFile took the request over, and gives it back.
	sending bool
}

const (
	queuedWaiting int32 = iota
	queuedClaimed
	queuedDone
)

// claim reports whether the pool's work may answer the request: true once
// it has it, false when the timeout answered it first. A nil q is a request
// answered on its worker, which nothing else answers.
func (q *queued) claim() bool {
	if q == nil {
		return true
	}
	if q.state.CompareAndSwap(queuedWaiting, queuedClaimed) {
		q.timer.Stop()
		return true
	}
	return q.state.Load() == queuedClaimed
}

// queue serves the request on the file pool, or answers 503 when the pool's
// queue is full, or its timeout passes before the pool's work has answered:
// waiting to be taken up, or with the disk the work waits on hung.
func (s *Static) queue(c *fibhttp.Context, name, urlPath string) {
	c.Retain()
	q := &queued{}
	timeout := func() {
		if q.state.CompareAndSwap(queuedWaiting, queuedDone) {
			respondError(c, stdhttp.StatusServiceUnavailable)
			c.Release()
		}
	}
	q.timer = time.AfterFunc(s.pool.Timeout(), timeout)
	// The pool's own ways out, which come after q.timer is set.
	expire := func() {
		q.timer.Stop()
		timeout()
	}
	run := func() {
		if c.Err() != nil {
			// The client went while the request waited.
			expire()
			return
		}
		// With q, serve looks at the disk and always answers.
		s.serve(c, name, urlPath, q)
		if q.state.Load() == queuedClaimed && !q.sending {
			c.Release()
		}
	}
	if !s.pool.Submit(run, expire) {
		expire()
	}
}

// sendFromDisk serves a file the cache does not keep, as v describes it,
// claiming the request from q once the file is open. A file that cannot be
// opened gets 404. The body goes through SendFile, which neither waits for
// the disk nor for the client; see send.go.
func (s *Static) sendFromDisk(c *fibhttp.Context, v variant, full string, q *queued) {
	f, err := os.Open(full)
	if err != nil {
		if q.claim() {
			respondError(c, statusOf(err))
		}
		return
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if q.claim() {
			respondError(c, stdhttp.StatusNotFound)
		}
		return
	}
	if !q.claim() {
		f.Close()
		return
	}
	h := c.Header()
	copyHeader(h, fileHeader(v, info, nil))
	r := c.Request
	if v.encoding != "" && r.Header.Get("Range") == "" {
		// ServeContent leaves Content-Length out once Content-Encoding is
		// set; a whole file knows its length.
		h.Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	if strings.Contains(r.Header.Get("Range"), ",") {
		// Several ranges would go as a multipart body ServeContent copies
		// through a pipe; the whole file goes instead, as RFC 9110 allows.
		r.Header.Del("Range")
	}
	if send(c, f, v.name, info, s.stall) {
		q.sending = true
	}
}

// check returns the status a request path, name, is refused with, in one
// look at it, or 0 when it may be served: 400 for one that could name a file
// outside the root, or a different file on some systems, a ".." segment, a
// NUL or a backslash, which Windows takes as a separator, or on Windows a
// segment it opens as another file or a device (x.php::$DATA, x.php.,
// NUL); and 404 for a dotfile the handler hides, or on Windows for what may
// be a dotfile's 8.3 short name.
func (s *Static) check(name string) int {
	unsafe, hidden := dotfile.Scan(name)
	switch {
	case unsafe, s.refuseAliases && dotfile.Alias(name):
		return stdhttp.StatusBadRequest
	case hidden && s.hideDotfiles, s.hideShortNames && dotfile.HasShortName(name):
		return stdhttp.StatusNotFound
	}
	return 0
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

// variant is how a file goes out: as the file name names, or as its twin
// compressed with encoding, and with Vary when the handler serves twins.
type variant struct {
	name     string
	encoding string
	vary     bool
}

// fileHeader is what a response carrying the file says of it. data is its
// contents, to sniff its type from when its extension says none, or nil when
// they are not at hand, which leaves Content-Type out. A twin has its
// original's type, from the original's name.
func fileHeader(v variant, info fs.FileInfo, data []byte) stdhttp.Header {
	h := make(stdhttp.Header, 6)
	contentType := mime.TypeByExtension(filepath.Ext(v.name))
	if contentType == "" && data != nil && v.encoding == "" {
		contentType = stdhttp.DetectContentType(data)
	}
	if contentType == "" && v.encoding != "" {
		contentType = "application/octet-stream"
	}
	if v.encoding != "" {
		h["Content-Encoding"] = []string{v.encoding}
	}
	if v.vary {
		h["Vary"] = []string{"Accept-Encoding"}
	}
	if contentType != "" {
		h["Content-Type"] = []string{contentType}
	}
	h["Last-Modified"] = []string{info.ModTime().UTC().Format(stdhttp.TimeFormat)}
	h["Etag"] = []string{etag(info)}
	h["Accept-Ranges"] = []string{"bytes"}
	return h
}
