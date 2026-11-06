package streaming

import (
	"fmt"
	stdhttp "net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/media/hls"
	"github.com/lesismal/fib/media/httpflv"
	"github.com/lesismal/fib/media/hub"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/streaming/channels"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".httpflv", New: func() any { return new(HTTPFLV) },
		Doc: "Plays the streaming role's streams as HTTP-FLV, and as WebSocket-FLV to a WebSocket upgrade: /live/cam.flv plays live/cam."})
	module.Register(module.Info{ID: module.Handlers + ".hls", New: func() any { return new(HLS) },
		Doc: "Plays the streaming role's streams over HLS, fMP4 or MPEG-TS segments, Low-Latency HLS optional: /live/cam/index.m3u8 plays live/cam."})
	module.Register(module.Info{ID: module.Handlers + ".stream_whip", New: func() any { return new(StreamWHIP) },
		Doc: "Takes WebRTC streams published with WHIP (RFC 9725) into the streaming role's hub, which plays them over every protocol."})
	module.Register(module.Info{ID: module.Handlers + ".stream_whep", New: func() any { return new(StreamWHEP) },
		Doc: "Plays the streaming role's streams to WebRTC viewers with WHEP, whatever they were published over."})
}

// streamPath is what a stream's path may be: segments of letters, digits,
// '.', '_', '~' and '-', joined by '/', none of them "." or "..".
var streamPath = regexp.MustCompile(`^[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*$`)

// validStream reports whether p is a stream's path, of at most 256 bytes.
func validStream(p string) bool {
	if len(p) > 256 || !streamPath.MatchString(p) {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// base is what the handlers share: the engine the role is found by, the
// prefix of their paths and their policy.
type base struct {
	engine *fib.Engine
	prefix string
	auth   *mediaauth.Policy
}

func (b *base) provision(ctx *module.Context, prefix, auth, def string) error {
	if prefix == "" {
		prefix = def
	}
	if !strings.HasPrefix(prefix, "/") {
		return ctx.Errorf("prefix", "%q does not start with /", prefix)
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	p, err := mediaauth.Provision(ctx, "auth", auth)
	if err != nil {
		return err
	}
	b.engine, b.prefix, b.auth = ctx.Engine(), prefix, p
	return nil
}

// role returns the running role, or answers 503 and nil.
func (b *base) role(c *fibhttp.Context) *Server {
	s := Lookup(b.engine)
	if s == nil || s.Hub() == nil {
		respond(c, stdhttp.StatusServiceUnavailable, "the streaming role is not running", nil)
		return nil
	}
	return s
}

// respond answers with a status and a plain text message.
func respond(c *fibhttp.Context, status int, msg string, hdr stdhttp.Header) {
	if hdr == nil {
		hdr = stdhttp.Header{}
	}
	var body []byte
	if msg != "" && status != stdhttp.StatusNoContent {
		hdr.Set("Content-Type", "text/plain; charset=utf-8")
		body = []byte(msg + "\n")
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: hdr, Body: body})
}

// HTTPFLV is the httpflv handler.
type HTTPFLV struct {
	Prefix string          `json:"prefix,omitempty" doc:"The part of the path before the stream's: with /flv/, /flv/live/cam.flv plays live/cam. / when left out: /live/cam.flv plays live/cam, the stream rtmp://host/live/cam publishes."`
	Auth   string          `json:"auth,omitempty" doc:"The media_auth policy a player must satisfy, play on the stream's path: a bearer token, Basic, the token query parameter (/live/cam.flv?token=T) or the fit_media_token cookie. 401 with a Bearer challenge without credentials, 403 when they do not allow it. With the policy's url_signing, a signed URL of the stream plays too. Everyone may play when left out, as the route's middleware allow."`
	Wait   config.Duration `json:"wait,omitempty" doc:"How long a player may wait for a stream with no publisher yet, or for the next one after its publisher leaves. 404 at once for a stream without a publisher when left out."`
	CORS   string          `json:"cors,omitempty" doc:"Sent as Access-Control-Allow-Origin, which a web player on another origin (flv.js, mpegts.js) needs, as \"*\". None when left out."`

	b   base
	cur atomic.Pointer[flvHandler]
}

// flvHandler is fib's HTTP-FLV handler of one hub.
type flvHandler struct {
	hub *hub.Hub
	h   *httpflv.Handler
}

// Provision checks the prefix and finds the policy.
func (f *HTTPFLV) Provision(ctx *module.Context) error {
	return f.b.provision(ctx, f.Prefix, f.Auth, "/")
}

// ServeHTTP plays the stream the path names.
func (f *HTTPFLV) ServeHTTP(c *fibhttp.Context) {
	// Whatever the answer, the access log holds no token or signature.
	mediaauth.Redact(c.Request)
	stream, ok := f.stream(c.Request.URL.Path)
	if !ok {
		respond(c, stdhttp.StatusNotFound, "no stream in the path: /<stream>.flv", nil)
		return
	}
	s := f.b.role(c)
	if s == nil {
		return
	}
	hb := s.Hub()
	// The stream's resource, as a signed URL is signed for: /live/cam/ for
	// /live/cam.flv, as mediaauth.ResourceOf has it.
	resource := strings.TrimSuffix(c.Request.URL.Path, ".flv") + "/"
	f.b.auth.GuardResource(c, stream, resource, f.CORS, func(mediaauth.Access) { f.handler(hb).ServeHTTP(c) })
}

func (f *HTTPFLV) stream(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, f.b.prefix)
	if !ok {
		return "", false
	}
	stream, ok := strings.CutSuffix(rest, ".flv")
	return stream, ok && validStream(stream)
}

// handler returns fib's handler of hb, made anew when the role's hub is
// another.
func (f *HTTPFLV) handler(hb *hub.Hub) *httpflv.Handler {
	if cur := f.cur.Load(); cur != nil && cur.hub == hb {
		return cur.h
	}
	next := &flvHandler{hub: hb, h: httpflv.NewHandler(httpflv.Config{
		Hub:  hb,
		Path: f.path,
		CORS: f.CORS,
		Wait: f.Wait.Std(),
	})}
	f.cur.Store(next)
	return next.h
}

func (f *HTTPFLV) path(r *stdhttp.Request) (string, string, bool) {
	stream, ok := f.stream(r.URL.Path)
	return stream, "", ok
}

// HLS is the hls handler.
type HLS struct {
	Prefix         string          `json:"prefix,omitempty" doc:"The part of the path before the stream's: with /hls/, /hls/live/cam/index.m3u8 is the playlist of live/cam. / when left out."`
	Auth           string          `json:"auth,omitempty" doc:"The media_auth policy every request, playlist and segment, must satisfy, play on the stream's path: a bearer token, Basic, the token query parameter or the fit_media_token cookie. A playlist fetched with a token in its query names its playlists, segments and parts with the token too, so that a player that sends no header (Safari, ffmpeg) plays on. With the policy's url_signing, a signed URL of the stream plays too, and its playlists hand what they name a grant of their own. Everyone may play when left out."`
	Format         string          `json:"format,omitempty" enum:"fmp4,ts" doc:"The segment format of a playlist's URL without a format query parameter: fmp4, fragmented MP4 for H.264, H.265, AV1, AAC and Opus, or ts, MPEG-TS for H.264, H.265, AAC and Opus. The other is played with ?format=ts or ?format=fmp4. fmp4 when left out."`
	TargetDuration config.Duration `json:"target_duration,omitempty" default:"\"2s\"" doc:"How long segments are: a stream with video is cut at the first key frame after it. Set it to the key frame interval. 2s when left out."`
	PartDuration   config.Duration `json:"part_duration,omitempty" doc:"Turns on Low-Latency HLS with parts this long, 200ms to 1s being usual: blocking playlist reloads, EXT-X-PART and preload hints. Off when left out."`
	PlaylistSize   int             `json:"playlist_size,omitempty" min:"0" max:"100" default:"6" doc:"How many segments a playlist lists. 6 when left out or 0."`
	MaxBytes       int64           `json:"max_bytes,omitempty" min:"0" default:"33554432" doc:"The bytes of segments one stream and format holds; the oldest go first. 32 MiB when left out or 0."`
	PlayWait       config.Duration `json:"play_wait,omitempty" doc:"How long a playlist request may wait for a stream with no publisher yet. 404 at once when left out."`
	IdleTimeout    config.Duration `json:"idle_timeout,omitempty" default:"\"30s\"" doc:"How long a stream's segmenting runs without a request before it stops. 30s when left out."`
	CORS           string          `json:"cors,omitempty" doc:"Sent as Access-Control-Allow-Origin, as \"*\", for players on other origins. None when left out."`

	b      base
	shared *hlsShared
}

// The defaults of HLS.
const (
	defaultHLSMaxBytes = 32 << 20
	defaultHLSIdle     = 30 * time.Second
)

// hlsShared is fib's HLS handler of one config of the hls handler, shared
// by the configs that have it, so that a reload keeps the segments and
// their numbering: the handler of the same params in the next config takes
// it over.
type hlsShared struct {
	key  hlsKey
	cfg  hls.HandlerConfig
	refs int // under hlsMu

	cur atomic.Pointer[hlsHandler]
	mu  sync.Mutex

	// enc keeps the segments of encrypted channels, encrypted, within
	// max_bytes; libs are the libraries whose keys encrypt them.
	enc  *encCache
	libs encLibs
}

type hlsHandler struct {
	hub *hub.Hub
	h   *hls.Handler
}

type hlsKey struct {
	engine *fib.Engine
	params string
}

var (
	hlsMu   sync.Mutex
	hlsByID = map[hlsKey]*hlsShared{}
)

// Provision checks the params, and takes the shared handler of its params.
func (h *HLS) Provision(ctx *module.Context) error {
	if err := h.b.provision(ctx, h.Prefix, h.Auth, "/"); err != nil {
		return err
	}
	if d := h.PartDuration.Std(); d != 0 && (d < 100*time.Millisecond || d > 5*time.Second) {
		return ctx.Errorf("part_duration", "%s is not between 100ms and 5s", d)
	}
	format, other := hls.FMP4, hls.MPEGTS
	if h.Format == "ts" {
		format, other = hls.MPEGTS, hls.FMP4
	}
	maxBytes := h.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultHLSMaxBytes
	}
	idle := h.IdleTimeout.Std()
	if idle <= 0 {
		idle = defaultHLSIdle
	}
	cfg := hls.HandlerConfig{
		Muxer: hls.Config{Format: format, TargetDuration: h.TargetDuration.Std(), PlaylistSize: h.PlaylistSize,
			MaxBytes: maxBytes, PartDuration: h.PartDuration.Std()},
		Formats:     []hls.Format{other},
		Prefix:      h.b.prefix,
		IdleTimeout: idle,
		PlayWait:    h.PlayWait.Std(),
		AllowOrigin: h.CORS,
	}
	key := hlsKey{engine: h.b.engine, params: fmt.Sprintf("%q %s %s %s %d %d %s %s %q", h.b.prefix, format.Name,
		h.TargetDuration, h.PartDuration, h.PlaylistSize, maxBytes, h.PlayWait, idle, h.CORS)}
	hlsMu.Lock()
	defer hlsMu.Unlock()
	sh := hlsByID[key]
	if sh == nil {
		sh = &hlsShared{key: key, cfg: cfg, enc: newEncCache(maxBytes)}
		hlsByID[key] = sh
	}
	sh.refs++
	h.shared = sh
	return nil
}

// Cleanup gives the shared handler back, closing it when no config has it.
func (h *HLS) Cleanup() {
	sh := h.shared
	if sh == nil {
		return
	}
	h.shared = nil
	hlsMu.Lock()
	sh.refs--
	last := sh.refs == 0
	if last {
		delete(hlsByID, sh.key)
	}
	hlsMu.Unlock()
	if last {
		sh.mu.Lock()
		cur := sh.cur.Swap(nil)
		sh.mu.Unlock()
		if cur != nil {
			cur.h.Close()
		}
	}
}

// handler returns fib's handler of hb, made anew, and the old one closed,
// when the role's hub is another.
func (sh *hlsShared) handler(hb *hub.Hub) *hls.Handler {
	if cur := sh.cur.Load(); cur != nil && cur.hub == hb {
		return cur.h
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	cur := sh.cur.Load()
	if cur != nil && cur.hub == hb {
		return cur.h
	}
	cfg := sh.cfg
	cfg.Hub = hb
	next := &hlsHandler{hub: hb, h: hls.NewHandler(cfg)}
	sh.cur.Store(next)
	if cur != nil {
		cur.h.Close()
	}
	return next.h
}

// ServeHTTP serves a playlist or a segment of the stream the path names.
func (h *HLS) ServeHTTP(c *fibhttp.Context) {
	// Whatever the answer, the access log holds no token or signature.
	mediaauth.Redact(c.Request)
	rest, ok := strings.CutPrefix(c.Request.URL.Path, h.b.prefix)
	i := strings.LastIndexByte(rest, '/')
	if !ok || i <= 0 || !validStream(rest[:i]) {
		respond(c, stdhttp.StatusNotFound, "no stream in the path: /<stream>/index.m3u8", nil)
		return
	}
	stream, name := rest[:i], rest[i+1:]
	s := h.b.role(c)
	if s == nil || h.shared == nil {
		return
	}
	hb := s.Hub()
	// The stream's resource, as a signed URL is signed for: the path up to
	// the playlist's or segment's name.
	resource := c.Request.URL.Path[:len(h.b.prefix)+i+1]
	h.b.auth.GuardResource(c, stream, resource, h.CORS, func(a mediaauth.Access) {
		fh := h.shared.handler(hb)
		// A channel that encrypts its HLS has its keys and segments served
		// here, and its playlists name the keys.
		enc, encrypted := channels.Lookup(h.b.engine).Encryption(stream)
		if encrypted && h.serveEncrypted(c, fh, stream, name, enc) {
			return
		}
		if strings.HasSuffix(name, ".m3u8") {
			// A live stream's grant lives play_grace past its URL.
			if query, cookie := a.PassOn(0); query != "" || cookie != "" || encrypted {
				c.OnResponse(func(r *fibhttp.Response) {
					if r.StatusCode != stdhttp.StatusOK {
						return
					}
					if encrypted {
						r.Body = withKeys(r.Body, enc.Rotation)
					}
					if query != "" {
						r.Body = withQuery(r.Body, query)
					}
					if cookie != "" {
						if r.Header == nil {
							r.Header = stdhttp.Header{}
						}
						r.Header.Add("Set-Cookie", cookie)
					}
				})
			}
		}
		fh.ServeHTTP(c)
	})
}

// uriAttr is a URI attribute of a playlist's tag.
var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

// withQuery returns playlist with query, a token or a grant, added to the
// query of every URI it names: its lines that are not tags, and the URI
// attributes of its tags. A URI that has the query's first parameter is
// left as it is.
func withQuery(playlist []byte, query string) []byte {
	first, _, _ := strings.Cut(query, "=")
	add := func(uri string) string {
		if uri == "" || strings.Contains(uri, first+"=") {
			return uri
		}
		sep := "?"
		if strings.Contains(uri, "?") {
			sep = "&"
		}
		return uri + sep + query
	}
	lines := strings.Split(string(playlist), "\n")
	for i, line := range lines {
		l := strings.TrimRight(line, "\r")
		switch {
		case l == "":
		case strings.HasPrefix(l, "#"):
			lines[i] = uriAttr.ReplaceAllStringFunc(line, func(m string) string {
				return `URI="` + add(m[len(`URI="`):len(m)-1]) + `"`
			})
		default:
			lines[i] = add(l) + line[len(l):]
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// StreamWHIP is the stream_whip handler.
type StreamWHIP struct {
	Stream string `json:"stream,omitempty" doc:"The stream's path. When left out, it is the path after prefix a POST is sent to, as live/cam in /whip/live/cam. A session's URL is the path of its POST followed by its ID, which the route must match too."`
	Prefix string `json:"prefix,omitempty" doc:"The part of the path before the stream's. /whip/ when left out."`
	Auth   string `json:"auth,omitempty" doc:"The media_auth policy a POST must satisfy, publish on the stream's path: a bearer token, Basic, the token query parameter or the fit_media_token cookie. 401 with a Bearer challenge without credentials, 403 when they do not allow it. A session's URL takes PATCH and DELETE without credentials, its random ID being the capability (RFC 9725). Everyone may publish when left out, as the route's middleware allow."`

	h rtcHandler
}

// StreamWHEP is the stream_whep handler.
type StreamWHEP struct {
	Stream string `json:"stream,omitempty" doc:"The stream's path. When left out, it is the path after prefix a POST is sent to, as live/cam in /whep/live/cam. A session's URL is the path of its POST followed by its ID, which the route must match too."`
	Prefix string `json:"prefix,omitempty" doc:"The part of the path before the stream's. /whep/ when left out."`
	Auth   string `json:"auth,omitempty" doc:"The media_auth policy a POST must satisfy, play on the stream's path, as stream_whip's. A session's URL takes PATCH and DELETE without credentials. Everyone may play when left out."`

	h rtcHandler
}

// Provision checks the params and finds the policy.
func (w *StreamWHIP) Provision(ctx *module.Context) error {
	return w.h.provision(ctx, w.Stream, w.Prefix, w.Auth, rtcWHIP, mediaauth.Publish)
}

// ServeHTTP serves the endpoint and its sessions' URLs.
func (w *StreamWHIP) ServeHTTP(c *fibhttp.Context) { w.h.ServeHTTP(c) }

// Provision checks the params and finds the policy.
func (w *StreamWHEP) Provision(ctx *module.Context) error {
	return w.h.provision(ctx, w.Stream, w.Prefix, w.Auth, rtcWHEP, mediaauth.Play)
}

// ServeHTTP serves the endpoint and its sessions' URLs.
func (w *StreamWHEP) ServeHTTP(c *fibhttp.Context) { w.h.ServeHTTP(c) }

// rtcHandler is what stream_whip and stream_whep share.
type rtcHandler struct {
	base
	stream   string
	action   mediaauth.Action
	endpoint string // the path prefix of fib's server the POSTs go to
}

func (r *rtcHandler) provision(ctx *module.Context, stream, prefix, auth, def string, a mediaauth.Action) error {
	if stream != "" && !validStream(stream) {
		return ctx.Errorf("stream", "%q is not a stream's path: segments of letters, digits, '.', '_', '~' and '-' joined by /", stream)
	}
	if err := r.base.provision(ctx, prefix, auth, def); err != nil {
		return err
	}
	r.stream, r.action, r.endpoint = stream, a, def
	return nil
}

// sessionID is what a session's ID is: 32 hex digits.
var sessionID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ServeHTTP passes a POST of an offer to fib's server once its credentials
// are checked, and a session's PATCH, DELETE and OPTIONS without, mapping
// the route's paths to the server's and its session URLs back.
func (r *rtcHandler) ServeHTTP(c *fibhttp.Context) {
	p := c.Request.URL.Path
	rest, ok := strings.CutPrefix(p, r.prefix)
	if !ok && p+"/" == r.prefix {
		rest, ok = "", true
	}
	if !ok {
		respond(c, stdhttp.StatusNotFound, "not found", nil)
		return
	}
	s := r.role(c)
	if s == nil {
		return
	}
	srv := s.RTC()
	if srv == nil {
		respond(c, stdhttp.StatusServiceUnavailable, "streaming.webrtc is not set", nil)
		return
	}
	rest = strings.Trim(rest, "/")
	method := c.Request.Method
	if method != stdhttp.MethodPost {
		id := rest[strings.LastIndexByte(rest, '/')+1:]
		if sessionID.MatchString(id) && (method != stdhttp.MethodOptions || rest != r.stream) {
			c.Request.URL.Path = rtcResource + id
			srv.ServeHTTP(c)
			return
		}
	}
	stream := r.stream
	if stream == "" {
		stream = rest
	}
	if !validStream(stream) {
		respond(c, stdhttp.StatusNotFound, "no stream in the path", nil)
		return
	}
	if method != stdhttp.MethodPost {
		c.Request.URL.Path = r.endpoint + stream
		srv.ServeHTTP(c)
		return
	}
	base := strings.TrimSuffix(p, "/")
	r.auth.Guard(c, r.action, stream, func(string) {
		c.OnHeader(func(_ int, h stdhttp.Header) {
			if loc := h.Get("Location"); strings.HasPrefix(loc, rtcResource) {
				h.Set("Location", base+"/"+loc[len(rtcResource):])
			}
		})
		c.Request.URL.Path = r.endpoint + stream
		srv.ServeHTTP(c)
	})
}
