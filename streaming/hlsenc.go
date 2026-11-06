package streaming

import (
	"container/list"
	"errors"
	stdhttp "net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/media/hls"

	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/streaming/channels"
)

// Encrypted HLS of channels (encrypt, I136). A channel publishes its
// packets clear; the hls handler encrypts the segments fib's Muxer cuts of
// it, AES-128 (METHOD=AES-128) under keys of the channel's library, a key
// every key_rotation segments, and serves those keys to whoever its auth
// lets play the channel:
//
//   - a media playlist gains an EXT-X-KEY wherever the key changes, its URI
//     key-<session>-<epoch>.key in the stream's directory, and names no IV,
//     so that a segment's IV is its media sequence number; an EXT-X-MAP after
//     a key is preceded by METHOD=NONE, so that initialization sections stay
//     clear. Low-Latency HLS's tags are left out: parts are not encrypted.
//   - a segment, seg-<session>-<msn>.<ext>, is copied from the Muxer and
//     encrypted on the library's file pool, once for every request that
//     waits for it, and kept in a cache bounded by the handler's max_bytes.
//   - a key is served for a session the stream's Muxer runs, and an epoch no
//     later than its next segment's, so that a viewer cannot fetch the keys
//     of segments not yet cut; by a handler with auth alone, since without
//     it anyone could take the keys; and a part is not served.
//
// <session> is the Muxer's, which a restart of the stream's segmenting
// changes, so that its keys and IVs never repeat. This is fit's side of
// fib's HLS, which encrypts nothing (fib-api-requests.md, A69): once fib's
// Muxer encrypts, it goes.

// The errors of encrypted segments.
var (
	errEncFull    = errors.New("streaming: the encrypted segments' cache is full of what is being encrypted")
	errEncWaiters = errors.New("streaming: too many requests wait for the same encrypted segment")
)

// maxEncWaiters is the most requests that may wait for one segment being
// encrypted.
const maxEncWaiters = 1024

// encEntryBytes is what an entry is reckoned to take beside its segment.
const encEntryBytes = 128

// encKey names an encrypted segment: of a stream, by its name, under keys
// of an ID and a rotation.
type encKey struct {
	stream, name, keys string
}

type encEntry struct {
	key     encKey
	data    []byte
	bytes   int64
	waiters []func([]byte, error)
	elem    *list.Element // in the LRU once encrypted
}

// encCache keeps the encrypted segments of an hls handler's channels,
// bounded in bytes: what is being encrypted holds its bytes from when it is
// asked for, and the least recently used are dropped for it.
type encCache struct {
	mu   sync.Mutex
	max  int64
	used int64
	m    map[encKey]*encEntry
	lru  list.List
}

func newEncCache(max int64) *encCache { return &encCache{max: max, m: map[encKey]*encEntry{}} }

// get calls fn with the segment k, kept or encrypted by build, which takes
// size bytes: once, for every request that waits for it. build is called
// without the lock and calls done once.
func (c *encCache) get(k encKey, size int64, build func(done func([]byte, error)), fn func([]byte, error)) {
	c.mu.Lock()
	if e := c.m[k]; e != nil {
		if e.elem != nil {
			c.lru.MoveToFront(e.elem)
			c.mu.Unlock()
			fn(e.data, nil)
			return
		}
		if len(e.waiters) >= maxEncWaiters {
			c.mu.Unlock()
			fn(nil, errEncWaiters)
			return
		}
		e.waiters = append(e.waiters, fn)
		c.mu.Unlock()
		return
	}
	need := size + encEntryBytes
	if !c.room(need) {
		c.mu.Unlock()
		fn(nil, errEncFull)
		return
	}
	e := &encEntry{key: k, bytes: need, waiters: []func([]byte, error){fn}}
	c.m[k] = e
	c.used += need
	c.mu.Unlock()
	build(func(data []byte, err error) { c.done(e, data, err) })
}

// room drops the least recently used until need fits, and reports whether
// it does. c.mu is held.
func (c *encCache) room(need int64) bool {
	if need > c.max {
		return false
	}
	for c.used+need > c.max && c.lru.Len() > 0 {
		c.evict(c.lru.Back().Value.(*encEntry))
	}
	return c.used+need <= c.max
}

func (c *encCache) evict(e *encEntry) {
	c.lru.Remove(e.elem)
	delete(c.m, e.key)
	c.used -= e.bytes
}

// done ends the encryption of e: kept, or forgotten on an error, and its
// waiters answered.
func (c *encCache) done(e *encEntry, data []byte, err error) {
	c.mu.Lock()
	waiters := e.waiters
	e.waiters = nil
	if err != nil {
		delete(c.m, e.key)
		c.used -= e.bytes
	} else {
		c.used += int64(cap(data)) + encEntryBytes - e.bytes
		e.data, e.bytes = data, int64(cap(data))+encEntryBytes
		e.elem = c.lru.PushFront(e)
		// What was reckoned is what it takes, give or take its padding.
		c.room(0)
	}
	c.mu.Unlock()
	for _, fn := range waiters {
		fn(data, err)
	}
}

// bytes returns the bytes the cache holds, for tests.
func (c *encCache) bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// fileName is a name of fib's Muxer, parsed: kind (init, seg, part or key,
// the last fit's), its session and number, and its extension.
type fileName struct {
	kind, session, ext string
	n                  uint64
}

// parseFile parses a name of fib's Muxer, or of a key, as
// seg-<session>-<msn>.m4s: false when it is neither.
func parseFile(s string) (fileName, bool) {
	kind, rest, ok := strings.Cut(s, "-")
	if !ok {
		return fileName{}, false
	}
	switch kind {
	case "init", "seg", "part", "key":
	default:
		return fileName{}, false
	}
	dot := strings.LastIndexByte(rest, '.')
	if dot < 0 {
		return fileName{}, false
	}
	f := fileName{kind: kind, ext: rest[dot:]}
	rest = rest[:dot]
	session, num, ok := strings.Cut(rest, "-")
	if !ok || session == "" {
		return fileName{}, false
	}
	if kind == "part" {
		num, _, _ = strings.Cut(num, "-")
	}
	n, err := strconv.ParseUint(num, 10, 63)
	if err != nil {
		return fileName{}, false
	}
	f.session, f.n = session, n
	return f, true
}

// keyName is the name of key epoch of session.
func keyName(session string, epoch uint64) string {
	return "key-" + session + "-" + strconv.FormatUint(epoch, 10) + ".key"
}

// withKeys returns playlist, a media playlist of fib's Muxer of a stream
// encrypted a key every rotation segments, with the EXT-X-KEY tags that name
// them and without Low-Latency HLS's tags; a multivariant playlist, or one
// that names no segment, as it is.
func withKeys(playlist []byte, rotation int) []byte {
	if rotation <= 0 || !strings.Contains(string(playlist), "#EXTINF") {
		return playlist
	}
	lines := strings.Split(string(playlist), "\n")
	out := make([]string, 0, len(lines)+8)
	// The segment line after each #EXTINF names its msn and session.
	keyed, epoch := false, uint64(0)
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "#EXT-X-PART:"), strings.HasPrefix(line, "#EXT-X-PRELOAD-HINT:"),
			strings.HasPrefix(line, "#EXT-X-PART-INF:"), strings.HasPrefix(line, "#EXT-X-SERVER-CONTROL:"):
			continue
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			if keyed {
				// A key before it would encrypt the init section too.
				out = append(out, "#EXT-X-KEY:METHOD=NONE")
				keyed = false
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			f, ok := segmentAfter(lines[i+1:])
			if ok {
				e := f.n / uint64(rotation)
				if !keyed || e != epoch {
					out = append(out, `#EXT-X-KEY:METHOD=AES-128,URI="`+keyName(f.session, e)+`"`)
					keyed, epoch = true, e
				}
			}
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

// segmentAfter returns the segment the first line of lines that is not a
// tag names.
func segmentAfter(lines []string) (fileName, bool) {
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f, ok := parseFile(l)
		return f, ok && f.kind == "seg"
	}
	return fileName{}, false
}

// encLibs are the libraries an hls handler's channels are encrypted with,
// found once and kept until a reload removes them.
type encLibs struct{ m sync.Map } // name -> *medialib.Library

func (h *HLS) library(name string) *medialib.Library {
	if v, ok := h.shared.libs.m.Load(name); ok {
		if l := v.(*medialib.Library); !l.Removed() {
			return l
		}
	}
	l := medialib.Lookup(h.b.engine, name)
	if l != nil {
		h.shared.libs.m.Store(name, l)
	}
	return l
}

// serveEncrypted serves name of stream, a channel whose HLS enc encrypts,
// and reports whether it did: a key, a segment encrypted, or a part
// refused; playlists and init sections are fib's, the playlists given their
// keys by the caller.
func (h *HLS) serveEncrypted(c *fibhttp.Context, fh *hls.Handler, stream, name string, enc channels.Encryption) bool {
	f, ok := parseFile(name)
	if !ok || f.kind == "init" {
		return false
	}
	if f.kind == "part" {
		h.respond(c, stdhttp.StatusNotFound, "the parts of an encrypted stream are not served", "")
		return true
	}
	l := h.library(enc.Library)
	keys := (*medialib.Keys)(nil)
	if l != nil {
		keys = l.Keys()
	}
	if keys == nil {
		h.respond(c, stdhttp.StatusServiceUnavailable, "the channel's media library, and its keys, are not loaded", "1")
		return true
	}
	resource := stream + "/" + f.session
	switch f.kind {
	case "key":
		if h.b.auth == nil {
			// Everyone could take the key: the handler serves none.
			h.respond(c, stdhttp.StatusForbidden, "the keys of an encrypted channel are served only by an hls handler with auth", "")
			return true
		}
		if f.ext != ".key" || !h.keyServed(fh, stream, f, enc.Rotation) {
			h.respond(c, stdhttp.StatusNotFound, "no such key", "")
			return true
		}
		key := keys.Key(medialib.KeyChannel, resource, f.n)
		hdr := stdhttp.Header{"Content-Type": {"application/octet-stream"}, "Cache-Control": {"private, no-store"}}
		if h.CORS != "" {
			hdr["Access-Control-Allow-Origin"] = []string{h.CORS}
		}
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: hdr, Body: key[:]})
		return true
	}
	// A segment: of the Muxer of its session, in whichever format.
	var data []byte
	contentType := ""
	for _, fm := range []hls.Format{hls.FMP4, hls.MPEGTS} {
		if fm.Extension != f.ext {
			continue
		}
		if m := fh.FormatMuxer(stream, fm.Name); m != nil {
			if d, ok := m.File(name); ok {
				data, contentType = d, fm.ContentType
			}
		}
	}
	if data == nil {
		h.respond(c, stdhttp.StatusNotFound, "no such segment", "")
		return true
	}
	key := keys.Key(medialib.KeyChannel, resource, f.n/uint64(enc.Rotation))
	k := encKey{stream: stream, name: name, keys: keys.ID() + "/" + strconv.Itoa(enc.Rotation)}
	var answered atomic.Bool
	c.Retain()
	answer := func(seg []byte, err error) {
		if !answered.CompareAndSwap(false, true) {
			return
		}
		defer c.Release()
		if err != nil {
			h.respond(c, stdhttp.StatusServiceUnavailable, "the media library is busy", "1")
			return
		}
		hdr := stdhttp.Header{"Content-Type": {contentType}, "Cache-Control": {"max-age=3600"}}
		if h.CORS != "" {
			hdr["Access-Control-Allow-Origin"] = []string{h.CORS}
		}
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: hdr, Body: seg})
	}
	c.OnCancel(func(error) {
		if answered.CompareAndSwap(false, true) {
			c.Release()
		}
	})
	h.shared.enc.get(k, int64(len(data))+16, func(done func([]byte, error)) {
		ok := l.Submit(func() {
			// The Muxer's segment is not modified: a copy is encrypted.
			out := make([]byte, len(data), len(data)+16)
			copy(out, data)
			done(medialib.Encrypt(key, f.n, out), nil)
		}, func() { done(nil, medialib.ErrBusy) })
		if !ok {
			done(nil, medialib.ErrBusy)
		}
	}, answer)
	return true
}

// keyServed reports whether f, a key's name, is of a session of the
// stream's Muxer, and an epoch no later than that of the segment it cuts
// next.
func (h *HLS) keyServed(fh *hls.Handler, stream string, f fileName, rotation int) bool {
	for _, fm := range []hls.Format{hls.FMP4, hls.MPEGTS} {
		m := fh.FormatMuxer(stream, fm.Name)
		if m == nil {
			continue
		}
		pl, ok := m.MediaPlaylist(nil)
		if !ok {
			continue
		}
		prefix := "seg-" + f.session + "-"
		i := strings.LastIndex(string(pl), prefix)
		if i < 0 {
			continue
		}
		last, ok := parseFile(strings.TrimRight(strings.SplitN(string(pl[i:]), "\n", 2)[0], "\r"))
		return ok && f.n <= (last.n+1)/uint64(rotation)
	}
	return false
}

// respond answers with status and a plain text message, with the handler's
// CORS, and Retry-After when retry is set.
func (h *HLS) respond(c *fibhttp.Context, status int, msg, retry string) {
	hdr := stdhttp.Header{}
	if h.CORS != "" {
		hdr["Access-Control-Allow-Origin"] = []string{h.CORS}
	}
	if retry != "" {
		hdr["Retry-After"] = []string{retry}
	}
	respond(c, status, msg, hdr)
}
