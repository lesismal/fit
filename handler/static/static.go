// Package static is the static handler: it serves the files under a root
// directory.
//
// No file operation runs on a fib worker. What the handler learns of a
// request path, the file it names, its index file or its compressed twins,
// and the contents of small files, is kept in memory and trusted for
// as long as a watch of the directories under the root, on Linux, reports
// no change to it, which it does within moments of the change: a file
// written, replaced or removed, a directory on its way renamed, or a
// symbolic link on the way to the root pointed elsewhere. What no watch
// vouches for, elsewhere than Linux or for a file that is a symbolic link,
// is trusted for check_interval, 5s by default. A request it answers from
// what it knows costs no system call. A file's contents are
// served from memory while the file's stamp, its size, modification and
// status change times and inode, is what it was when they were read, which
// tells apart a file replaced by one of the same size and modification time.
// A check_interval of 0 looks at the disk for every request, and keeps
// nothing of request paths. What it keeps of them is bounded, to 16384 paths
// and 16 MiB, and a path longer than 4096 bytes is not kept; they are split
// in shards by a hash of the path, each with its own lock and share of the
// bounds, and once a shard is full a path is kept only the second time it is
// asked for within a while, so that paths asked for once, as of a set of
// files far larger than the cache asked for at random, do not push out
// others and wait on each other to. A fib worker never waits for the cache's
// locks: a request whose lock is taken goes to the file pool. Anything else, a path not seen lately, a file
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
// a file larger than 16 KiB sent from disk, which is answered with the whole
// file.
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
	"io"
	"io/fs"
	"mime"
	stdhttp "net/http"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bufferpool"
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
	Compress      []string `json:"compress,omitempty" enum:"zstd,br,gzip" doc:"Encodings a file kept in memory is also kept compressed with, for a client that accepts one: the one it prefers by Accept-Encoding, and among equals the first listed. Each copy is made once, on the file pool, at a level that makes it small (gzip 9, brotli 9, zstd's best), the first time a client asks for it, and kept until the file changes; until then the file goes as it is, for a compress middleware to compress. A file smaller than 1400 bytes, of a type that does not compress, as images and archives, or that compresses by less than a twentieth is not; nor is a ranged request answered from a copy. The copies count in max_cache_bytes. A precompressed twin on disk goes ahead of a copy. None when left out."`
	TryFiles      []string `json:"try_files,omitempty" doc:"Paths tried in turn for a request, as nginx's try_files: {path} stands for the request's path, and one ending in / for a directory, served by its index file. The first that exists is served, and when none does the answer is 404; [\"{path}\", \"{path}/\", \"/index.html\"] serves a single-page app. A directory asked for without its slash is not redirected then."`
	Browse        bool     `json:"browse,omitempty" doc:"List a directory that has no index file, rather than answering 404. Files whose names start with a dot are left out."`
	BrowseMax     int      `json:"browse_max_entries,omitempty" min:"0" default:"10000" doc:"The most entries a listing reads and shows; a directory with more is listed in part, and says so. 10000 when left out or 0."`
	Dotfiles      string   `json:"dotfiles,omitempty" enum:"deny,allow" default:"\"deny\"" doc:"Whether files and directories whose names start with a dot, as .git, .env and .htpasswd do, are served: deny answers a request for one, or for anything under a dot directory, 404, as for a file not there, but /.well-known/ under the root, for ACME's HTTP-01 challenge and security.txt; allow serves them as other files. Listings leave them out either way. On Windows deny also hides a path with a segment shaped as an 8.3 short name, as GIT~1 for .git, which NTFS may give a dotfile. deny when left out."`

	DiskIO string `json:"disk_io,omitempty" enum:"auto,pool,inline" default:"\"auto\"" doc:"Where a request that needs the disk looks at it, for a file the cache does not hold: auto on fib's worker that serves it, as nginx does in its event loop, while no more requests than a quarter of the process's CPUs do so at once, and on the file pool past that; pool always on the file pool, as fit did before, which keeps a disk that hangs off fib's workers altogether; inline always on fib's worker. A request on fib's worker waits for the disk however long it takes, where one on the pool is answered 503 past the pool's timeout: with auto a disk that hangs holds no more of fib's workers than that quarter, and the requests past them get the pool's 503. On a disk that answers from memory, as tmpfs or a page cache that holds the files does, the hand-over to the pool costs a few percent of a busy server's rate (4% at 20 KiB files), which auto wins half of, and inline all, at the cost of a longer tail at saturation (its 99th percentile half as long again). auto when left out."`

	DefaultType string `json:"default_type,omitempty" doc:"The Content-Type of a file whose extension gives none, as nginx's default_type, such as application/octet-stream; when left out, such a file's type is sniffed from its first 512 bytes, as net/http's is, which costs a read of a file not in memory."`

	CheckInterval *config.Duration `json:"check_interval,omitempty" default:"\"5s\"" doc:"How long what the handler learned of a request path, the file it names and its validators, is trusted before the disk is looked at again, where no watch reports its changes. On Linux a watch of the directories under the root reports a change within moments, a file written, replaced or removed, a directory renamed, or a symbolic link on the way to the root pointed elsewhere, and what it watches is trusted until it does; this interval is for other systems, and for a file that is a symbolic link, whose target's changes the watch does not see. A file is told by its size, modification and status change times and inode, so one replaced by another of the same size and modification time is seen too. 0 looks at the disk for every request, with no watch, so that a response always carries the file as it is on disk then, also on a network file system, whose changes from other machines no watch sees. 5s when left out."`

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
	// compress are the encodings files kept in memory are compressed with.
	compress []encoding
	// vary says responses vary by Accept-Encoding: the handler serves
	// twins or compressed copies.
	vary  bool
	cache cache
	stall time.Duration
	pool  *filepool.Pool
	// waiting holds the requests queued for the pool, for its timeout.
	waiting *waiting
	// diskIO is disk_io's value, auto when left out; inlining counts the
	// requests served on fib's workers disk and all, which auto keeps to
	// inlineMax, a quarter of the CPUs. The padding keeps the counter, which every such
	// request changes twice, off the fields read beside it.
	diskIO    string
	inlineMax int32
	_         [64]byte
	inlining  atomic.Int32
	_         [60]byte
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
	if s.DefaultType != "" {
		if _, _, err := mime.ParseMediaType(s.DefaultType); err != nil {
			return ctx.Errorf("default_type", "%q is not a media type: %v", s.DefaultType, err)
		}
	}
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
	for i, name := range s.Compress {
		if slices.Index(s.Compress, name) < i {
			return ctx.Params.Field("compress").Index(i).Errorf("%q is listed twice", name)
		}
		s.compress = append(s.compress, encodings[name])
	}
	s.vary = len(s.precompressed) > 0 || len(s.compress) > 0
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
	s.cache.packs = len(s.compress)
	s.cache.entries = make(map[string]*entry)
	s.cache.initLookups(lookupShards(), DefaultMaxLookups, DefaultMaxLookupBytes)
	s.cache.check = DefaultCheckInterval
	if s.CheckInterval != nil {
		s.cache.check = s.CheckInterval.Std()
	}
	s.stall = ctx.WriteStallTimeout()
	s.pool = defaultPool()
	s.waiting = newWaiting()
	s.diskIO = s.DiskIO
	if s.diskIO == "" {
		s.diskIO = diskAuto
	}
	// A quarter of the CPUs: at 20 KiB on 8, a bound of 2 served 2.5% more
	// than the pool alone at saturation with the 99th percentile 5% up, and
	// 5% more at nine tenths of it with the percentile level; 8, the CPUs,
	// served 5% more at saturation, its percentile 45% up, Ps never idle
	// enough for the runtime to look at fib's parked event loops.
	s.inlineMax = int32(max(1, goruntime.GOMAXPROCS(0)/4))
	if s.cache.check > 0 {
		// Where the system offers no watch, the check interval alone
		// bounds how long a change goes unseen.
		if w, err := filewatch.New(root, s.cache.event); err == nil {
			s.watch = w
			s.cache.watching.Store(true)
		}
	}
	return nil
}

// Cleanup stops the watch of the root; what it vouched for is trusted for
// check_interval from then on.
func (s *Static) Cleanup() {
	s.cache.watching.Store(false)
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
	if s.inlineBegin() {
		s.inline(c, name, urlPath)
		s.inlining.Add(-1)
		return
	}
	if s.serve(c, name, urlPath, nil) {
		return
	}
	s.queue(c, name, urlPath)
}

// inlineBegin reports whether the request is served here, on fib's worker,
// disk and all (see disk_io): always with inline, never with pool, and with
// auto while fewer than inlineMax requests are; inlining then counts it,
// until the caller takes it off.
func (s *Static) inlineBegin() bool {
	switch s.diskIO {
	case diskPool:
		return false
	case diskInline:
		s.inlining.Add(1)
		return true
	}
	if s.inlining.Add(1) <= s.inlineMax {
		return true
	}
	s.inlining.Add(-1)
	return false
}

// inline serves the request here as the file pool's work would, from the
// cache or the disk, however long the disk takes: a response fib sends once
// the handler returns, as it sends a retained one, could not go out sooner
// as the pool's 503 anyway. What bounds a disk that hangs is auto's bound:
// it holds no more of fib's workers than that, and the requests past it go
// to the pool, whose timeout answers them.
func (s *Static) inline(c *fibhttp.Context, name, urlPath string) {
	c.Retain()
	q := &queued{}
	q.state.Store(queuedClaimed)
	if inlineHook != nil {
		inlineHook()
	}
	s.serve(c, name, urlPath, q)
	if !q.sending {
		c.Release()
	}
}

// inlineHook, when set, runs before a request served inline looks at the
// disk, as a slow disk would; for tests.
var inlineHook func()

// The values of disk_io.
const (
	diskAuto   = "auto"
	diskPool   = "pool"
	diskInline = "inline"
)

// serve answers the request for name, whose URL path is urlPath, or try_files
// for it. Without q, on a fib worker, it answers only from what the cache
// knows and holds, and reports false, answering nothing, when it would have
// to look at the disk. With q, on the file pool, it looks, and claims the
// request from q before it answers.
func (s *Static) serve(c *fibhttp.Context, name, urlPath string, q *queued) bool {
	if s.TryFiles == nil {
		var o *opened
		if q != nil {
			o = &opened{}
			defer o.close()
		}
		l := s.find(name, q != nil, o)
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
			return s.answer(c, l, urlPath, q, o)
		}
		return true
	}
	requested := "/" + strings.TrimPrefix(name, "/")
	for _, t := range s.TryFiles {
		candidate := strings.ReplaceAll(t, "{path}", requested)
		if s.check(candidate) != 0 {
			continue
		}
		l := s.find(candidate, q != nil, nil)
		switch {
		case l == nil:
			return false
		case l.redirect, l.status != 0:
			continue
		}
		return s.answer(c, l, path.Clean(candidate)+"/", q, nil)
	}
	if q.claim() {
		respondError(c, stdhttp.StatusNotFound)
	}
	return true
}

// find returns what the cache knows of name, a request path within the
// root, when it was learned within check_interval; or, with disk, what a look
// at the disk finds, which the cache then keeps. Without disk it returns nil
// for a name the cache does not know, or cannot tell at once. A file the
// look at the disk opens is left in o (see resolve).
func (s *Static) find(name string, disk bool, o *opened) *lookup {
	h := s.cache.hash(name)
	// Without disk, on a fib worker, the cache's lock is not waited for.
	if l := s.cache.lookup(name, h, disk); l != nil {
		return l
	}
	if !disk {
		return nil
	}
	// name may point into the request's head, which the cache would keep
	// whole for as long as it keeps the name.
	name = strings.Clone(name)
	l := s.resolve(name, o)
	s.cache.keep(name, h, l)
	return l
}

// answer serves the file or the listing l stands for, as serve does, from
// the file in o when the lookup opened it.
func (s *Static) answer(c *fibhttp.Context, l *lookup, listPath string, q *queued, o *opened) bool {
	if l.listing {
		if q == nil {
			return false
		}
		s.list(c, listPath, l.full, q)
		return true
	}
	v, full, info, stamp := s.variant(c.Request, l)
	e, room := s.cache.currentOrRoom(full, stamp, info.Size(), q != nil)
	if e == nil && q != nil && room {
		e = s.cache.load(full, info, stamp, v)
	}
	switch {
	case e != nil:
		if q.claim() {
			serveMemory(c, v.name, s.compressed(c.Request, full, e, v))
		}
	case q == nil:
		return false
	default:
		s.sendFromDisk(c, v, full, info, q, o)
	}
	return true
}

// opened is the file a lookup opened to stat it (see openStat), which the
// request then reads or sends from, and which serve closes when nothing
// took it.
type opened struct {
	f    sendable
	full string
}

// take returns the file opened at full, which the caller then closes, or
// nil.
func (o *opened) take(full string) sendable {
	if o == nil || o.f == nil || o.full != full {
		return nil
	}
	f := o.f
	o.f = nil
	return f
}

func (o *opened) close() {
	if o != nil && o.f != nil {
		o.f.Close()
		o.f = nil
	}
}

// queued is a request on its way through the file pool, which either the
// pool's work or the pool's timeout answers, whichever claims it first.
type queued struct {
	state atomic.Int32
	// sending says SendFile took the request over, and gives it back.
	sending bool
	// c is the request, and the rest its place among the waiting (see
	// waiting), until the pool's work claims it.
	c          *fibhttp.Context
	list       *waiting
	shard      *waitShard
	prev, next *queued
	deadline   int64
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
		q.leave()
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
	s.waiting.join(q, c, s.pool.Timeout())
	// The pool's own ways out.
	expire := func() {
		q.leave()
		q.expire()
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

// smallFile is the size up to which a file the cache does not keep is read
// whole and answered from memory rather than through SendFile: fib's
// SendFile reads a file below its sendfile threshold, 16 KiB, a piece at a
// time anyway, on a goroutine of its own, while reading it whole here takes
// an open, a read and a close, where SendFile's way, as ServeContent drives
// it, takes ten system calls.
const smallFile = 16 << 10

// sendFromDisk serves a file the cache does not keep, as v describes it and
// info, the stat of it the lookup made, sizes it, claiming the request from
// q once the file is open. A file that cannot be opened gets 404. A small
// one is read whole and answered from memory; a larger one goes through
// SendFile, which neither waits for the disk nor for the client; see
// send.go.
func (s *Static) sendFromDisk(c *fibhttp.Context, v variant, full string, info fs.FileInfo, q *queued, o *opened) {
	// stale says the lookup's stat no longer stands for the file.
	stale := false
	if info.Size() <= smallFile {
		var data []byte
		var err error
		if f := o.take(full); f != nil {
			data, err = readFrom(f, full, info.Size())
			f.Close()
		} else {
			data, err = readSmall(full, info.Size())
		}
		switch {
		case err != nil:
			if q.claim() {
				respondError(c, statusOf(err))
			}
			return
		case data != nil:
			if !q.claim() {
				bufferpool.Put(data)
				return
			}
			// The validators are the lookup's stat, as for a file the cache
			// reads; a file that changed size since is sent as below.
			serveRead(c, v, info, data)
			return
		}
		stale = true
	}
	if !stale && !hasConditions(c.Request) && s.sendWhole(c, v, full, info, q, o.take(full)) {
		return
	}
	f, err := openFile(full)
	if err != nil {
		if q.claim() {
			respondError(c, statusOf(err))
		}
		return
	}
	info, err = f.Stat()
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

// readFrom reads the whole of f, the file at full, of size bytes when it
// was looked at, as readSmall reads it, but for the open: one read, a short
// one its end.
func readFrom(f sendable, full string, size int64) ([]byte, error) {
	// One byte more tells a file that grew.
	buf := bufferpool.Get(int(size) + 1)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		bufferpool.Put(buf)
		return nil, &fs.PathError{Op: "read", Path: full, Err: err}
	}
	if int64(n) != size {
		bufferpool.Put(buf)
		return nil, nil
	}
	return buf[:n], nil
}

// sendable is a file opened for SendFile, which done closes.
type sendable interface {
	fib.File
	Close() error
	// stands reports whether the file is still a regular file of size
	// bytes, as the lookup found it.
	stands(size int64) bool
}

// sendWhole sends the whole of the file at full, as v describes it and
// info, the lookup's stat of it, sizes it, with SendFile, for a request with
// no conditions and no range: the most of them, which ServeContent would
// only set the header for and sniff the type of. It opens the file as
// openSendable does, and sets the header itself, as fileHeader makes it but
// for the type of a file whose name gives none, sniffed from its first 512
// bytes; the validators are the lookup's, as for a file read whole, once an
// fstat finds it the regular file of the size the lookup did. Above 16 KiB,
// where files are not read whole, this is what fib's SendFile costs beside
// a read whole: 17 KiB files went at 70% of nginx's rate through
// ServeContent, an os.File and a duplicate of its descriptor. It reports
// false, answering nothing, for a file no longer as the lookup found it,
// which sendFromDisk then serves as before. With f, the file the lookup
// opened and info is the fstat of, it opens and checks nothing.
func (s *Static) sendWhole(c *fibhttp.Context, v variant, full string, info fs.FileInfo, q *queued, f sendable) bool {
	if f == nil {
		var err error
		if f, err = openSendable(full); err != nil {
			if q.claim() {
				respondError(c, statusOf(err))
			}
			return true
		}
		if !f.stands(info.Size()) {
			f.Close()
			return false
		}
	}
	if !q.claim() {
		f.Close()
		return true
	}
	contentType := v.contentType(nil)
	if contentType == "" {
		var head [512]byte
		n, _ := f.ReadAt(head[:], 0)
		contentType = stdhttp.DetectContentType(head[:n])
	}
	h := c.Header()
	if v.encoding != "" {
		h["Content-Encoding"] = []string{v.encoding}
	}
	if v.vary {
		h["Vary"] = varyAcceptEncoding
	}
	// The length and the validators in one string, and the four values in
	// one array: two allocations where there would be seven.
	var buf [96]byte
	b := strconv.AppendInt(buf[:0], info.Size(), 10)
	length := len(b)
	b = appendHTTPTime(b, info.ModTime())
	modified := len(b)
	all := string(appendEtag(b, info))
	values := &[4]string{contentType, all[:length], all[length:modified], all[modified:]}
	h["Content-Type"] = values[0:1:1]
	h["Content-Length"] = values[1:2:2]
	h["Last-Modified"] = values[2:3:3]
	h["Etag"] = values[3:4:4]
	h["Accept-Ranges"] = acceptRangesBytes
	if q != nil {
		q.sending = true
	}
	c.SendFileStall(f, 0, info.Size(), s.stall, func(error) {
		f.Close()
		c.Release()
	})
	return true
}

// The values of header fields sendWhole sets, the same for every response,
// which fib does not change.
var (
	varyAcceptEncoding = []string{"Accept-Encoding"}
	acceptRangesBytes  = []string{"bytes"}
)

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
	return string(appendEtag(make([]byte, 0, 32), info))
}

// appendEtag appends etag's tag to b.
func appendEtag(b []byte, info fs.FileInfo) []byte {
	b = append(b, '"')
	b = strconv.AppendInt(b, info.ModTime().UnixNano(), 16)
	b = append(b, '-')
	b = strconv.AppendInt(b, info.Size(), 16)
	return append(b, '"')
}

// appendFileHeader appends to b, as lines of "Name: value" each ended by
// CRLF, the fields fileHeader makes for v, info and data, without making
// them: for Response.HeaderBlock.
func appendFileHeader(b []byte, v variant, info fs.FileInfo, data []byte) []byte {
	contentType := v.contentType(data)
	if v.encoding != "" {
		b = append(append(append(b, "Content-Encoding: "...), v.encoding...), "\r\n"...)
	}
	if v.vary {
		b = append(b, "Vary: Accept-Encoding\r\n"...)
	}
	if contentType != "" {
		b = append(append(append(b, "Content-Type: "...), contentType...), "\r\n"...)
	}
	b = append(b, "Last-Modified: "...)
	b = appendHTTPTime(b, info.ModTime())
	b = append(b, "\r\nEtag: "...)
	b = appendEtag(b, info)
	return append(b, "\r\nAccept-Ranges: bytes\r\n"...)
}

// variant is how a file goes out: as the file name names, or as its twin
// compressed with encoding, and with Vary when the handler serves twins.
type variant struct {
	name     string
	encoding string
	vary     bool
	// defaultType is the handler's default_type.
	defaultType string
}

// contentType is the type of the file v names: its extension's; else the
// handler's default_type; else, for a file not compressed, the one sniffed
// from data, its first bytes, when they are at hand, or none; and for a
// compressed twin, which has its original's name, application/octet-stream.
func (v variant) contentType(data []byte) string {
	t := mime.TypeByExtension(filepath.Ext(v.name))
	if t == "" {
		t = v.defaultType
	}
	if t == "" && data != nil && v.encoding == "" {
		t = stdhttp.DetectContentType(data)
	}
	if t == "" && v.encoding != "" {
		t = "application/octet-stream"
	}
	return t
}

// fileHeader is what a response carrying the file says of it. data is its
// contents, to sniff its type from when its extension says none, or nil when
// they are not at hand, which leaves Content-Type out. A twin has its
// original's type, from the original's name.
func fileHeader(v variant, info fs.FileInfo, data []byte) stdhttp.Header {
	h := make(stdhttp.Header, 6)
	contentType := v.contentType(data)
	if v.encoding != "" {
		h["Content-Encoding"] = []string{v.encoding}
	}
	if v.vary {
		h["Vary"] = []string{"Accept-Encoding"}
	}
	if contentType != "" {
		h["Content-Type"] = []string{contentType}
	}
	h["Last-Modified"] = []string{string(appendHTTPTime(make([]byte, 0, len(stdhttp.TimeFormat)), info.ModTime()))}
	h["Etag"] = []string{etag(info)}
	h["Accept-Ranges"] = []string{"bytes"}
	return h
}

// appendHTTPTime appends t, in UTC, as http.TimeFormat lays it out, written
// field by field rather than through AppendFormat, which reads the layout
// each time and costs several times as much.
func appendHTTPTime(b []byte, t time.Time) []byte {
	t = t.UTC()
	year, month, day := t.Date()
	if year < 0 || year > 9999 {
		return t.AppendFormat(b, stdhttp.TimeFormat)
	}
	hour, minute, second := t.Clock()
	const days, months = "SunMonTueWedThuFriSat", "JanFebMarAprMayJunJulAugSepOctNovDec"
	wd, mo := int(t.Weekday())*3, (int(month)-1)*3
	b = append(b, days[wd:wd+3]...)
	b = append(b, ',', ' ', byte('0'+day/10), byte('0'+day%10), ' ')
	b = append(b, months[mo:mo+3]...)
	return append(b, ' ',
		byte('0'+year/1000), byte('0'+year/100%10), byte('0'+year/10%10), byte('0'+year%10), ' ',
		byte('0'+hour/10), byte('0'+hour%10), ':', byte('0'+minute/10), byte('0'+minute%10), ':',
		byte('0'+second/10), byte('0'+second%10), ' ', 'G', 'M', 'T')
}
