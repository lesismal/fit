// Package vod is the vod handler: it serves the items of a media library
// (package medialib) as video on demand over HLS, cut from the files as they
// are stored, without transcoding. See docs/decisions/032-media-library.md
// and docs/media-library.md.
//
// An item's URLs are its ID's path after the handler's prefix, as
// /vod/films/films/bbb/index.m3u8: a multivariant playlist of its
// renditions, with its audio files and subtitles as groups; a media
// playlist of each, of fragmented MP4 segments, or MPEG-TS for older
// players; the segments, cut at keyframes near segment_duration; and the
// files themselves, with Range. names.go lists them. With encrypt, the
// segments are encrypted with AES-128 under the item's key, derived from
// the library's secret (medialib.Keys), as they are built on the pool, and
// the key, aes.key, is served to whoever auth lets play the item.
//
// Nothing here blocks a fib worker. A playlist is cut from the sample
// tables the library reads on its file pool, a segment's samples are read
// and muxed there too, and a request that waits for either is retained and
// answered from the pool, within timeout. What is built is kept in a cache
// bounded by max_cache_bytes, shared by the handlers of the same params
// across reloads; a segment is built once for every request that waits for
// it. A file found changed or gone when it is read is answered 404, and
// what the cache and the library keep of it is dropped, until a scan lists
// it again; a new catalog drops what it no longer lists.
package vod

import (
	"errors"
	"fmt"
	"hash/fnv"
	stdhttp "net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/hls"
	"github.com/lesismal/fib/media/mp4"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".vod", New: func() any { return new(VOD) },
		Doc: "Serves a media library's items as video on demand over HLS, fMP4 or MPEG-TS segments cut from the files as they are stored, and the files themselves with Range."})
}

// The defaults and bounds of the params.
const (
	DefaultSegmentDuration = 4 * time.Second
	minSegmentDuration     = time.Second
	maxSegmentDuration     = 60 * time.Second
	DefaultMaxCacheBytes   = 256 << 20
	DefaultMaxSegmentBytes = 32 << 20
	minMaxSegmentBytes     = 64 << 10
	maxMaxSegmentBytes     = 1 << 30
	DefaultTimeout         = 30 * time.Second
)

// VOD is the vod handler.
type VOD struct {
	Library         string          `json:"library" fit:"required" doc:"The media_library whose items are served."`
	Prefix          string          `json:"prefix,omitempty" doc:"The part of the path before an item's ID: with /vod/films/, /vod/films/films/bbb/index.m3u8 is the playlist of the item films/bbb. /vod/<library>/ when left out."`
	Auth            string          `json:"auth,omitempty" doc:"The media_auth policy every request, playlist, segment, subtitle and file, must satisfy, play on the stream vod/<library>/<id>: a bearer token, Basic, the token query parameter or the fit_media_token cookie. A playlist fetched with a token in its query names its playlists, segments and subtitles with the token too, so that a player that sends no header (Safari, ffmpeg) plays on. With the policy's url_signing, a signed URL of the item plays too, and its playlists hand what they name a grant of their own. Everyone may play when left out."`
	Container       string          `json:"container,omitempty" enum:"fmp4,ts" default:"\"fmp4\"" doc:"The segments of index.m3u8: fmp4, fragmented MP4 for H.264, H.265, AV1, AAC and Opus, or ts, MPEG-TS for H.264, H.265, AAC and Opus, for older players. Either is played by its own name too, index.fmp4.m3u8 and index.ts.m3u8. fmp4 when left out."`
	SegmentDuration config.Duration `json:"segment_duration,omitempty" default:"\"4s\"" doc:"About how long segments are: a file with video is cut at the keyframe nearest this after a segment's start, so set it to the keyframe interval the files were made with, or a multiple of it. 1s to 60s. 4s when left out."`
	MaxCacheBytes   int64           `json:"max_cache_bytes,omitempty" min:"0" default:"268435456" doc:"The most memory the handler keeps of what it builds from the files, for the next request: how each is cut into segments, its playlists, its segments and subtitles, the least recently used dropped; and of what it is building. 256 MiB when left out or 0."`
	MaxSegmentBytes int64           `json:"max_segment_bytes,omitempty" min:"0" default:"33554432" doc:"The largest segment: one that would be larger is cut at an earlier keyframe, or without one when there is none. 64 KiB to 1 GiB, and at most a quarter of max_cache_bytes. 32 MiB when left out or 0."`
	CacheControl    string          `json:"cache_control,omitempty" doc:"Sent as Cache-Control with every playlist, segment, subtitle and file, so that a CDN keeps them, as \"public, max-age=86400\": they change only when their file does, which their ETag and Last-Modified say. None when left out."`
	CORS            string          `json:"cors,omitempty" doc:"Sent as Access-Control-Allow-Origin, as \"*\", for players on other origins. None when left out."`
	Timeout         config.Duration `json:"timeout,omitempty" default:"\"30s\"" doc:"How long a request may wait for what it needs to be read and built on the library's file pool; past it the answer is 503. 30s when left out."`
	Encrypt         bool            `json:"encrypt,omitempty" doc:"Encrypts the segments with AES-128 (METHOD=AES-128), fMP4 and MPEG-TS alike, each item under a key of its own derived from the library's secret, which the media playlists name as aes.key in the item's directory and the handler serves only to a request auth lets play that item: segments copied, as a CDN keeps them, do not play without it. The files themselves are then not served; initialization sections, subtitles and posters stay clear. Needs auth and the library's secret or secret_file. Off when left out."`

	engine    *fib.Engine
	prefix    string
	auth      *mediaauth.Policy
	container container
	timeout   time.Duration
	stall     time.Duration
	graph     module.Holder
	lib       atomic.Pointer[medialib.Library]
	st        *store
	// keys are the library's keys of encrypted HLS, with encrypt; nil
	// without.
	keys *medialib.Keys
}

// Provision checks the params and takes the shared store of its params.
func (v *VOD) Provision(ctx *module.Context) error {
	if !validName(v.Library) {
		return ctx.Errorf("library", "%q is not a library's name", v.Library)
	}
	if ctx.MediaLibrary(v.Library) == nil {
		return ctx.Errorf("library", "there is no media_library named %q", v.Library)
	}
	v.prefix = v.Prefix
	if v.prefix == "" {
		v.prefix = "/vod/" + v.Library + "/"
	}
	if !strings.HasPrefix(v.prefix, "/") {
		return ctx.Errorf("prefix", "%q does not start with /", v.prefix)
	}
	if !strings.HasSuffix(v.prefix, "/") {
		v.prefix += "/"
	}
	p, err := mediaauth.Provision(ctx, "auth", v.Auth)
	if err != nil {
		return err
	}
	v.auth = p
	if v.Encrypt {
		if p == nil {
			return ctx.Errorf("encrypt", "encrypt needs auth: the key is served only to a request its policy lets play the item")
		}
		keys, err := medialib.KeysOf(ctx.MediaLibrary(v.Library))
		if err != nil {
			return ctx.Errorf("library", "media_library %q's %s", v.Library, err)
		}
		if keys == nil {
			return ctx.Errorf("encrypt", "encrypt needs a key: set media_library %q's secret or secret_file", v.Library)
		}
		v.keys = keys
	}
	v.container = fmp4C
	if v.Container == "ts" {
		v.container = tsC
	}
	d := v.SegmentDuration.Std()
	if d == 0 {
		d = DefaultSegmentDuration
	}
	if d < minSegmentDuration || d > maxSegmentDuration {
		return ctx.Errorf("segment_duration", "%s is not between %s and %s", d, minSegmentDuration, maxSegmentDuration)
	}
	cacheBytes := v.MaxCacheBytes
	if cacheBytes <= 0 {
		cacheBytes = DefaultMaxCacheBytes
	}
	segBytes := v.MaxSegmentBytes
	if segBytes <= 0 {
		segBytes = min(DefaultMaxSegmentBytes, cacheBytes/4)
	}
	if segBytes < minMaxSegmentBytes || segBytes > maxMaxSegmentBytes || segBytes > cacheBytes/4 {
		return ctx.Errorf("max_segment_bytes", "%d is not between 64 KiB and the smaller of 1 GiB and a quarter of max_cache_bytes (%d)", segBytes, cacheBytes/4)
	}
	if strings.ContainsAny(v.CacheControl+v.CORS, "\r\n") {
		return ctx.Errorf("", "cache_control and cors may not hold a line break")
	}
	v.timeout = v.Timeout.Std()
	if v.timeout <= 0 {
		v.timeout = DefaultTimeout
	}
	v.engine, v.stall, v.graph = ctx.Engine(), ctx.WriteStallTimeout(), ctx.Graph()
	v.st = takeStore(storeKey{engine: v.engine, library: v.Library, segDur: d, segBytes: segBytes, cacheBytes: cacheBytes,
		cacheControl: v.CacheControl, cors: v.CORS, keyID: v.keys.ID()}, v.keys)
	return nil
}

// validName reports whether s is a library's name, as config checks it.
func validName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// Cleanup gives the shared store back.
func (v *VOD) Cleanup() {
	if v.st != nil {
		giveStore(v.st)
		v.st = nil
	}
}

// Cache returns what the handler's cache holds.
func (v *VOD) Cache() CacheStats { return v.st.cache.stats() }

// library returns the library the handler serves, found once and kept
// until a reload removes it.
func (v *VOD) library() *medialib.Library {
	if l := v.lib.Load(); l != nil && !l.Removed() {
		return l
	}
	l := medialib.Lookup(v.engine, v.Library)
	if l != nil {
		v.lib.Store(l)
	}
	return l
}

// ServeHTTP serves what the path names of an item.
func (v *VOD) ServeHTTP(c *fibhttp.Context) {
	r := c.Request
	// Whatever the answer, the access log holds no token or signature.
	mediaauth.Redact(r)
	if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead {
		v.respond(c, stdhttp.StatusMethodNotAllowed, "only GET and HEAD", "Allow", "GET, HEAD")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, v.prefix)
	i := strings.LastIndexByte(rest, '/')
	if !ok || i <= 0 {
		v.respond(c, stdhttp.StatusNotFound, "no item in the path: <prefix><id>/index.m3u8")
		return
	}
	id := rest[:i]
	nm, ok := parseName(rest[i+1:])
	if !ok || !medialib.ValidID(id) {
		v.respond(c, stdhttp.StatusNotFound, "not a name an item has")
		return
	}
	l := v.library()
	if l == nil {
		v.respond(c, stdhttp.StatusServiceUnavailable, "the media library is not loaded", "Retry-After", "1")
		return
	}
	cat := l.Catalog()
	if cat.Scanned.IsZero() {
		v.respond(c, stdhttp.StatusServiceUnavailable, "the media library's first scan has not ended", "Retry-After", "1")
		return
	}
	v.st.watch(l, cat)
	it := cat.Item(id)
	if it == nil || it.Error != "" {
		v.respond(c, stdhttp.StatusNotFound, "no such item")
		return
	}
	if v.auth == nil {
		v.serve(c, l, it, nm, mediaauth.Access{})
		return
	}
	// The item's resource, as a signed URL is signed for: the path up to
	// and with the '/' after its ID.
	resource := r.URL.Path[:len(v.prefix)+i+1]
	v.auth.GuardResource(c, "vod/"+v.Library+"/"+id, resource, v.CORS, func(a mediaauth.Access) { v.serve(c, l, it, nm, a) })
}

// personal returns a playlist's header, h, for the viewer whose access a
// is: with a signed URL or a grant, a playlist that names that grant, or
// sets it as a cookie (setCookie), is the viewer's own, which no shared
// cache may keep. h is not changed; another is returned when it differs.
func personal(h stdhttp.Header, a *mediaauth.Access, setCookie string) stdhttp.Header {
	if !a.URL() {
		return h
	}
	out := make(stdhttp.Header, len(h)+2)
	for k, vals := range h {
		out[k] = vals
	}
	out["Cache-Control"] = []string{"private, no-store"}
	if setCookie != "" {
		out["Set-Cookie"] = []string{setCookie}
	}
	return out
}

// serve serves nm of item it to the request a let in.
func (v *VOD) serve(c *fibhttp.Context, l *medialib.Library, it *medialib.Item, nm name, a mediaauth.Access) {
	switch nm.kind {
	case kindMaster:
		con := nm.c
		if con == 0 {
			con = v.container
		}
		query, cookie := a.PassOn(it.Duration)
		b := renderMaster(it, con, query)
		if b == nil {
			v.respond(c, stdhttp.StatusNotFound, "no rendition of the item is carried in "+con.String())
			return
		}
		serveBlob(c, &blob{data: b, header: personal(v.st.header(hls.PlaylistContentType, "", it.ModTime), &a, cookie), modTime: it.ModTime})
	case kindMedia, kindInit, kindSegment:
		r := v.rendition(it, nm)
		if r == nil {
			v.respond(c, stdhttp.StatusNotFound, "no such rendition")
			return
		}
		switch nm.kind {
		case kindMedia:
			acc := a // escapes with the playlist's callbacks, here alone
			v.serveMedia(c, l, r, nm, &acc)
		case kindInit:
			v.serveInit(c, r, nm)
		default:
			v.serveSegment(c, l, it.ID, r, nm)
		}
	case kindSubtitles, kindVTT:
		if nm.i >= len(it.Subtitles) || it.Subtitles[nm.i].Error != "" {
			v.respond(c, stdhttp.StatusNotFound, "no such subtitle")
			return
		}
		if nm.kind == kindSubtitles {
			query, cookie := a.PassOn(it.Duration)
			serveBlob(c, &blob{data: renderSubtitles(nm.i, it.Duration, query),
				header: personal(v.st.header(hls.PlaylistContentType, "", it.Subtitles[nm.i].ModTime), &a, cookie), modTime: it.Subtitles[nm.i].ModTime})
			return
		}
		v.serveVTT(c, l, it.Subtitles[nm.i])
	case kindKey:
		if v.keys == nil {
			v.respond(c, stdhttp.StatusNotFound, "the item is not encrypted")
			return
		}
		v.serveKey(c, it)
	case kindFile:
		if v.keys != nil {
			v.respond(c, stdhttp.StatusNotFound, "the files of an encrypted item are not served")
			return
		}
		list := it.Renditions
		if nm.audio {
			list = it.Audio
		}
		if nm.i >= len(list) || list[nm.i].Error != "" || strings.ToLower(strings.TrimPrefix(path.Ext(list[nm.i].Path), ".")) != nm.ext {
			v.respond(c, stdhttp.StatusNotFound, "no such file")
			return
		}
		v.serveFile(c, l, &list[nm.i].File, fileType(nm.ext))
	case kindPoster:
		if it.Poster == nil || strings.ToLower(strings.TrimPrefix(path.Ext(it.Poster.Path), ".")) != nm.ext {
			v.respond(c, stdhttp.StatusNotFound, "no such poster")
			return
		}
		v.serveFile(c, l, it.Poster, fileType(nm.ext))
	}
}

// rendition returns the rendition or audio file nm names, nil when there is
// none to serve.
func (v *VOD) rendition(it *medialib.Item, nm name) *medialib.Rendition {
	list := it.Renditions
	if nm.audio {
		list = it.Audio
	}
	if nm.i >= len(list) || list[nm.i].Error != "" || list[nm.i].MP4() == nil {
		return nil
	}
	return list[nm.i]
}

// fileType is the Content-Type of a file of extension ext.
func fileType(ext string) string {
	switch ext {
	case "mp4", "m4v":
		return "video/mp4"
	case "m4a":
		return "audio/mp4"
	case "mov":
		return "video/quicktime"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	}
	return "application/octet-stream"
}

// respond answers with status and a plain text message, and the header
// fields extra names in pairs of name and value.
func (v *VOD) respond(c *fibhttp.Context, status int, msg string, extra ...string) {
	h := stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}}
	if v.CORS != "" {
		h["Access-Control-Allow-Origin"] = []string{v.CORS}
	}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = []string{extra[i+1]}
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: h, Body: []byte(msg + "\n")})
}

// fail answers a request whose work failed with err: 404 for a file not as
// the catalog has it, whose cache entries are dropped, or one not served;
// 503 for a busy pool or cache; 500 otherwise.
func (v *VOD) fail(c *fibhttp.Context, f *medialib.File, err error) {
	switch {
	case errors.Is(err, medialib.ErrChanged):
		if f != nil {
			v.st.cache.drop(idOf(f))
		}
		v.respond(c, stdhttp.StatusNotFound, "the file changed since the library was scanned")
	case errors.Is(err, medialib.ErrRemoved), errors.Is(err, medialib.ErrNotServed), errors.Is(err, medialib.ErrOutside):
		v.respond(c, stdhttp.StatusNotFound, "the file is not served")
	case errors.Is(err, medialib.ErrBusy), errors.Is(err, errFull), errors.Is(err, errWaiters):
		v.respond(c, stdhttp.StatusServiceUnavailable, "the media library is busy", "Retry-After", "1")
	default:
		v.respond(c, stdhttp.StatusInternalServerError, err.Error())
	}
}

// waiter is a request retained while what it needs is read and built: the
// work, its timeout and the client leaving race to answer it, and the first
// claims it.
type waiter struct {
	v     *VOD
	c     *fibhttp.Context
	state atomic.Bool
	// timer is set once started, which its own callback may precede.
	timer atomic.Pointer[time.Timer]
}

// wait retains c, and the config, until the waiter returned answers it, or
// timeout does with 503.
func (v *VOD) wait(c *fibhttp.Context) *waiter {
	w := &waiter{v: v, c: c}
	c.Retain()
	v.graph.Retain()
	t := time.AfterFunc(v.timeout, func() {
		w.answer(func() {
			v.respond(c, stdhttp.StatusServiceUnavailable, "timed out waiting for the media library", "Retry-After", "1")
		})
	})
	w.timer.Store(t)
	c.OnCancel(func(error) { w.answer(func() {}) })
	return w
}

// answer runs fn to answer the request, unless it was answered already,
// and lets it go.
func (w *waiter) answer(fn func()) {
	if !w.claim() {
		return
	}
	fn()
	w.release()
}

// claim reports whether the caller may answer the request, once.
func (w *waiter) claim() bool {
	if !w.state.CompareAndSwap(false, true) {
		return false
	}
	if t := w.timer.Load(); t != nil {
		t.Stop()
	}
	return true
}

func (w *waiter) release() {
	w.c.Release()
	w.v.graph.Release()
}

// blob answers with b, or the error.
func (w *waiter) blob(f *medialib.File, b *blob, err error) {
	w.answer(func() {
		if err != nil {
			w.v.fail(w.c, f, err)
			return
		}
		serveBlob(w.c, b)
	})
}

// serveMedia serves the media playlist of r.
func (v *VOD) serveMedia(c *fibhttp.Context, l *medialib.Library, r *medialib.Rendition, nm name, a *mediaauth.Access) {
	if _, ok := tracksOf(r, nm.c); !ok {
		v.respond(c, stdhttp.StatusNotFound, "the file has no track "+nm.c.String()+" carries")
		return
	}
	// A playlist whose URIs carry a grant or a token is rendered for its
	// request; one that carries none is the one the cache keeps, sent with
	// the viewer's own header when it sets a grant's cookie.
	query, cookie := a.PassOn(r.Duration)
	own := func(b *blob) *blob {
		if !a.URL() {
			return b
		}
		return &blob{data: b.data, header: personal(b.header, a, cookie), modTime: b.modTime}
	}
	lk := ckey{file: idOf(&r.File), what: whatList, c: nm.c}
	if query == "" {
		if b, ok := v.st.cache.peek(lk); ok {
			serveBlob(c, own(b.(*blob)))
			return
		}
	}
	render := func(p *plan) *blob {
		return &blob{data: renderMedia(p, nm.audio, nm.i, nm.c, query, v.keys != nil), header: v.st.header(hls.PlaylistContentType, "", r.ModTime), modTime: r.ModTime}
	}
	if p, ok := v.st.cache.peek(ckey{file: idOf(&r.File), what: whatPlan}); ok {
		b := render(p.(*plan))
		if query == "" {
			v.st.cache.put(lk, b, int64(len(b.data)))
		}
		serveBlob(c, own(b))
		return
	}
	w := v.wait(c)
	v.st.plan(l, r, func(p *plan, err error) {
		if err != nil {
			w.blob(&r.File, nil, err)
			return
		}
		b := render(p)
		if query == "" {
			v.st.cache.put(lk, b, int64(len(b.data)))
		}
		w.blob(&r.File, own(b), nil)
	})
}

// tracksOf returns the tracks of r's playlist as container c carries them,
// false when it carries none.
func tracksOf(r *medialib.Rendition, c container) ([]*av.Track, bool) {
	m := r.MP4()
	vi, ai := pickTracks(m)
	var out []*av.Track
	if vi >= 0 {
		if !carried(c, m.Tracks[vi].Track.Codec) {
			return nil, false
		}
		out = append(out, m.Tracks[vi].Track)
	}
	if ai >= 0 {
		out = append(out, m.Tracks[ai].Track)
	}
	return out, len(out) > 0
}

// serveInit serves the fMP4 initialization section of r.
func (v *VOD) serveInit(c *fibhttp.Context, r *medialib.Rendition, nm name) {
	in, err := v.st.init(r)
	if err != nil {
		v.fail(c, &r.File, err)
		return
	}
	serveBlob(c, in)
}

// serveSegment serves segment nm.n of r, of the item whose ID is id.
func (v *VOD) serveSegment(c *fibhttp.Context, l *medialib.Library, id string, r *medialib.Rendition, nm name) {
	if _, ok := tracksOf(r, nm.c); !ok {
		v.respond(c, stdhttp.StatusNotFound, "the file has no track "+nm.c.String()+" carries")
		return
	}
	sk := ckey{file: idOf(&r.File), what: whatSegment, c: nm.c, n: int32(nm.n)}
	if b, ok := v.st.cache.peek(sk); ok {
		serveBlob(c, b.(*blob))
		return
	}
	w := v.wait(c)
	v.st.plan(l, r, func(p *plan, err error) {
		if err != nil {
			w.blob(&r.File, nil, err)
			return
		}
		if nm.n >= len(p.segs) {
			w.answer(func() { v.respond(c, stdhttp.StatusNotFound, "no such segment") })
			return
		}
		v.st.segment(l, id, r, p, nm.n, nm.c, func(b *blob, err error) { w.blob(&r.File, b, err) })
	})
}

// serveVTT serves subtitle s as WebVTT.
func (v *VOD) serveVTT(c *fibhttp.Context, l *medialib.Library, s *medialib.Subtitle) {
	k := ckey{file: idOf(&s.File), what: whatVTT}
	if b, ok := v.st.cache.peek(k); ok {
		serveBlob(c, b.(*blob))
		return
	}
	w := v.wait(c)
	f := s.File
	v.st.cache.get(k, func(j job) {
		// The file read, and its WebVTT: within max_subtitle_bytes.
		if !j.hold(2*f.Size + 1024) {
			j.done(nil, 0, errFull)
			return
		}
		l.Subtitle(s, func(data []byte, err error) {
			if err != nil {
				j.done(nil, 0, err)
				return
			}
			b := &blob{data: withTimestampMap(data), header: v.st.header("text/vtt; charset=utf-8", etag(&f, "vtt"), f.ModTime), modTime: f.ModTime}
			j.done(b, int64(cap(b.data)), nil)
		})
	}, func(b any, err error) {
		bl, _ := b.(*blob)
		w.blob(&f, bl, err)
	})
}

// serveKey serves the AES-128 key of it, to a request auth let in for it,
// which no cache may keep.
func (v *VOD) serveKey(c *fibhttp.Context, it *medialib.Item) {
	key := v.keys.Key(medialib.KeyItem, it.ID, 0)
	h := stdhttp.Header{"Content-Type": {"application/octet-stream"}, "Cache-Control": {"private, no-store"}}
	if v.CORS != "" {
		h["Access-Control-Allow-Origin"] = []string{v.CORS}
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: h, Body: key[:]})
}

// serveFile sends f as it is, with Range, from the library's pool.
func (v *VOD) serveFile(c *fibhttp.Context, l *medialib.Library, f *medialib.File, contentType string) {
	w := v.wait(c)
	file := *f
	l.Open(&file, func(h *medialib.Handle, err error) {
		if !w.claim() {
			if h != nil {
				h.Close()
			}
			return
		}
		if err != nil {
			v.fail(c, &file, err)
			w.release()
			return
		}
		for k, vals := range v.st.header(contentType, etag(&file, ""), time.Time{}) {
			c.Header()[k] = vals
		}
		c.Header().Set("Accept-Ranges", "bytes")
		if strings.Contains(c.Request.Header.Get("Range"), ",") {
			// Several ranges would go as a multipart body ServeContent
			// copies through a pipe; the whole file goes instead, as RFC
			// 9110 allows.
			c.Request.Header.Del("Range")
		}
		if !sendFile(c, h.File(), path.Base(file.Path), file.ModTime, v.stall, func() { h.Close(); w.release() }) {
			h.Close()
			w.release()
		}
	})
}

// etag is the entity tag of what is made of f: its time and size, as the
// static handler's, and what, which tells the parts made of it apart.
func etag(f *medialib.File, what string) string {
	t := `"` + strconv.FormatInt(f.ModTime.UnixNano(), 16) + "-" + strconv.FormatInt(f.Size, 16)
	if what != "" {
		t += "-" + what
	}
	return t + `"`
}

// storeKey is what a store is shared by: the params whose handlers make the
// same segments and headers of the same library.
type storeKey struct {
	engine               *fib.Engine
	library              string
	segDur               time.Duration
	segBytes, cacheBytes int64
	cacheControl, cors   string
	// keyID is the ID of the keys segments are encrypted with, "" for
	// none: a new secret makes a new store, with nothing kept of the old.
	keyID string
}

// store is the cache of the handlers of one storeKey, kept across reloads
// that keep their params.
type store struct {
	key   storeKey
	refs  int // under storesMu
	cache *cache
	// tag tells what the store's params make apart in ETags: segments cut
	// by one segment_duration differ from those of another, and segments
	// encrypted from clear ones and under another key.
	tag string
	// keys encrypt the store's segments, nil for none.
	keys *medialib.Keys
}

var (
	storesMu sync.Mutex
	stores   = map[storeKey]*store{}
)

func takeStore(k storeKey, keys *medialib.Keys) *store {
	storesMu.Lock()
	defer storesMu.Unlock()
	st := stores[k]
	if st == nil {
		h := fnv.New32a()
		fmt.Fprintf(h, "%d %d %d", segmentFormat, k.segDur, k.segBytes)
		if k.keyID != "" {
			fmt.Fprintf(h, " aes-128 %s", k.keyID)
		}
		st = &store{key: k, cache: newCache(k.cacheBytes), tag: strconv.FormatUint(uint64(h.Sum32()), 36), keys: keys}
		stores[k] = st
	}
	st.refs++
	return st
}

func giveStore(st *store) {
	storesMu.Lock()
	defer storesMu.Unlock()
	st.refs--
	if st.refs == 0 {
		delete(stores, st.key)
	}
}

// segmentFormat changes when the bytes of the segments fit makes of a
// file change, so that ETags do.
const segmentFormat = 1

// header returns the header of a response of contentType, with etag and
// mod as Last-Modified when not empty, and the store's Cache-Control and
// CORS. A response hands it to
// fib as it is, which does not change it.
func (st *store) header(contentType, etag string, mod time.Time) stdhttp.Header {
	h := make(stdhttp.Header, 5)
	h["Content-Type"] = []string{contentType}
	if etag != "" {
		h["Etag"] = []string{etag}
	}
	if !mod.IsZero() {
		h["Last-Modified"] = []string{mod.UTC().Format(stdhttp.TimeFormat)}
	}
	if st.key.cacheControl != "" {
		h["Cache-Control"] = []string{st.key.cacheControl}
	}
	if st.key.cors != "" {
		h["Access-Control-Allow-Origin"] = []string{st.key.cors}
	}
	return h
}

// watch drops what the cache keeps of files a new catalog of l does not
// list as they were, on l's pool.
func (st *store) watch(l *medialib.Library, cat *medialib.Catalog) {
	if st.cache.changed(cat) {
		l.Submit(func() { st.cache.keep(cat) }, nil)
	}
}

// init returns the fMP4 initialization section of r, built at once, from
// the catalog, or kept.
func (st *store) init(r *medialib.Rendition) (*blob, error) {
	k := ckey{file: idOf(&r.File), what: whatInit}
	if b, ok := st.cache.peek(k); ok {
		return b.(*blob), nil
	}
	tracks, ok := tracksOf(r, fmp4C)
	if !ok {
		return nil, medialib.ErrNotServed
	}
	in, err := buildInit(tracks)
	if err != nil {
		return nil, err
	}
	b := &blob{data: in.data, header: st.header(contentTypeOf(tracks), etag(&r.File, "init-"+st.tag), r.ModTime), modTime: r.ModTime}
	st.cache.put(k, b, int64(cap(b.data)))
	return b, nil
}

// contentTypeOf is the Content-Type of fMP4 of tracks.
func contentTypeOf(tracks []*av.Track) string {
	for _, t := range tracks {
		if t.Codec.IsVideo() {
			return "video/mp4"
		}
	}
	return "audio/mp4"
}

// plan gets the plan of r, from the cache or built on l's pool from its
// sample tables, and calls fn with it, or with the error: at once, or from
// the pool.
func (st *store) plan(l *medialib.Library, r *medialib.Rendition, fn func(*plan, error)) {
	k := ckey{file: idOf(&r.File), what: whatPlan}
	vi, ai := pickTracks(r.MP4())
	reserve := int64(planBytes + segBytes*(int(r.Duration/st.key.segDur)+16))
	st.cache.get(k, func(j job) {
		tables(l, r, vi, ai, func(vt, at *mp4.Table, err error) {
			if err != nil {
				j.done(nil, 0, err)
				return
			}
			ok := l.Submit(func() {
				if !j.hold(reserve) {
					j.done(nil, 0, errFull)
					return
				}
				p, err := buildPlan(vt, at, st.key.segDur, st.key.segBytes)
				if err != nil {
					j.done(nil, 0, err)
					return
				}
				p.video, p.audio = vi, ai
				j.done(p, p.memory(), nil)
			}, func() { j.done(nil, 0, medialib.ErrBusy) })
			if !ok {
				j.done(nil, 0, medialib.ErrBusy)
			}
		})
	}, func(p any, err error) {
		pl, _ := p.(*plan)
		fn(pl, err)
	})
}

// tables gets the sample tables of r's tracks vi and ai, either -1 for
// none, and calls fn with them once both are read, or with the first error.
func tables(l *medialib.Library, r *medialib.Rendition, vi, ai int, fn func(vt, at *mp4.Table, err error)) {
	var (
		mu      sync.Mutex
		vt, at  *mp4.Table
		pending = 2
		failed  error
	)
	got := func(which **mp4.Table) func(*mp4.Table, error) {
		return func(t *mp4.Table, err error) {
			mu.Lock()
			if which != nil {
				*which = t
			}
			if err != nil && failed == nil {
				failed = err
			}
			pending--
			last := pending == 0
			mu.Unlock()
			if last {
				fn(vt, at, failed)
			}
		}
	}
	if vi >= 0 {
		l.Table(r, vi, got(&vt))
	} else {
		got(nil)(nil, nil)
	}
	if ai >= 0 {
		l.Table(r, ai, got(&at))
	} else {
		got(nil)(nil, nil)
	}
}

// segment gets segment n of p, r's plan, in container c, from the cache or
// built on l's pool, and calls fn with it, or with the error. id is the ID
// of r's item, whose key encrypts it when the store encrypts.
func (st *store) segment(l *medialib.Library, id string, r *medialib.Rendition, p *plan, n int, c container, fn func(*blob, error)) {
	k := ckey{file: idOf(&r.File), what: whatSegment, c: c, n: int32(n)}
	// The samples are read into one buffer and muxed into another, which
	// encryption pads in place.
	reserve := 2*int64(p.segs[n].bytes) + 64<<10
	file := r.File
	st.cache.get(k, func(j job) {
		tables(l, r, p.video, p.audio, func(vt, at *mp4.Table, err error) {
			if err != nil {
				j.done(nil, 0, err)
				return
			}
			src := &source{vt: vt, at: at}
			m := r.MP4()
			if vt != nil {
				src.vtrack = m.Tracks[p.video].Track
			}
			if at != nil {
				src.atrack = m.Tracks[p.audio].Track
			}
			ok := l.Submit(func() {
				if !j.hold(reserve) {
					j.done(nil, 0, errFull)
					return
				}
				data, err := st.build(l, &file, id, p, n, c, src)
				if err != nil {
					j.done(nil, 0, err)
					return
				}
				ct := hls.MPEGTS.ContentType
				if c == fmp4C {
					ct = contentTypeOf(src.tracks())
				}
				b := &blob{data: data, header: st.header(ct, etag(&file, c.String()+strconv.Itoa(n)+"-"+st.tag), file.ModTime), modTime: file.ModTime}
				j.done(b, int64(cap(data)), nil)
			}, func() { j.done(nil, 0, medialib.ErrBusy) })
			if !ok {
				j.done(nil, 0, medialib.ErrBusy)
			}
		})
	}, func(b any, err error) {
		bl, _ := b.(*blob)
		fn(bl, err)
	})
}

// build reads and muxes segment n of p on the pool, and encrypts it under
// the key of item id when the store encrypts. It blocks.
func (st *store) build(l *medialib.Library, f *medialib.File, id string, p *plan, n int, c container, src *source) ([]byte, error) {
	var in *initSection
	if c == fmp4C {
		var err error
		if in, err = buildInit(src.tracks()); err != nil {
			return nil, err
		}
	}
	h, err := l.OpenFile(f)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	var data []byte
	if c == fmp4C {
		data, err = buildFMP4(h, p, n, src, in)
	} else {
		data, err = buildTS(h, p, n, src)
	}
	if err != nil || st.keys == nil {
		return data, err
	}
	return medialib.Encrypt(st.keys.Key(medialib.KeyItem, id, 0), uint64(n), data), nil
}
