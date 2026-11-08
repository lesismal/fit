// Package btstream is the bt_stream handler: it serves a file of a torrent
// of the bittorrent.client role over HTTP, with Range, while the torrent
// downloads, so that a browser or a player plays it as it streams in. See
// docs/decisions/037-bittorrent.md and docs/bittorrent.md.
//
// A file's URL is <prefix><info hash>/<file path> (the path as the torrent
// lists it, its name first), <prefix><info hash>/<file index>, or
// <prefix><info hash>/ for the torrent's largest file; the info hash is the
// role's torrent ID, 40 hex digits. With magnet, a URL whose query carries
// magnet=<a magnet link of that torrent> adds the torrent when the role
// does not have it, marked temporary: such torrents are bounded in number
// (max_temporary) and in the bytes they want (max_temporary_bytes), download
// only the files streamed, and are removed with their data temporary_ttl
// after their last stream ended.
//
// Nothing here blocks a fib worker. The role's API, which reads and writes
// its state files, and fib's Reader, whose reads wait for the pieces they
// need and read the disk, run on a goroutine of the stream's own, at most
// max_streams of them: the handler's bounded pool. A stream's goroutine
// reads a chunk, writes it without waiting (WriteNoWait), and, when the
// client has more than fib's watermark queued, ends, to be started again by
// OnWritable once the client has taken half of it; so a slow client holds
// a slot of max_streams and about two chunks of memory, never a goroutine
// waiting on it. A read waits at most read_timeout for its piece, a write
// at most the config's write stall timeout for the client.
package btstream

import (
	stdhttp "net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bittorrent/metainfo"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".bt_stream", New: func() any { return new(BTStream) },
		Doc: "Serves a file of a torrent of the bittorrent.client role with Range while it downloads, the ranges asked for first, and adds a torrent by magnet link on the fly."})
}

// The defaults and bounds of the params.
const (
	DefaultPrefix            = "/bt/"
	DefaultMaxStreams        = 32
	maxMaxStreams            = 4096
	DefaultReadTimeout       = 60 * time.Second
	DefaultReadahead         = 8 << 20
	minReadahead             = 64 << 10
	maxReadahead             = 256 << 20
	DefaultInfoTimeout       = 60 * time.Second
	DefaultMaxTemporary      = 4
	maxMaxTemporary          = 1000
	DefaultMaxTemporaryBytes = 16 << 30
	DefaultTemporaryTTL      = 10 * time.Minute
	maxTemporaryTTL          = 7 * 24 * time.Hour
	// chunkBytes is what a stream reads and writes at a time: fib's
	// watermark of a response's queued output.
	chunkBytes = 64 << 10
)

// BTStream is the bt_stream handler.
type BTStream struct {
	Prefix            string          `json:"prefix,omitempty" default:"\"/bt/\"" doc:"The part of the path before a torrent's info hash: with /bt/, /bt/<info hash>/<file path> is a file of the torrent by its path as the torrent lists it, its name first, /bt/<info hash>/<n> its file of index n, and /bt/<info hash>/ its largest file. The info hash is the bittorrent.client role's torrent ID, 40 hex digits. /bt/ when left out."`
	Auth              string          `json:"auth,omitempty" doc:"The media_auth policy every request must satisfy, play on the stream bt/<info hash>: a bearer token, Basic, the token query parameter or the fit_media_token cookie, or, with the policy's url_signing, a URL signed for <prefix><info hash>/. Adding a torrent by magnet link takes publish on the same stream as well. Everyone may play when left out; magnet needs it."`
	MaxStreams        int             `json:"max_streams,omitempty" min:"0" default:"32" doc:"The most responses the handler streams at once, each with a goroutine of its own while it reads; past it a request is answered 503. At most 4096. 32 when left out or 0."`
	ReadTimeout       config.Duration `json:"read_timeout,omitempty" default:"\"60s\"" doc:"How long a read waits for the piece it needs to arrive: past it a response not begun is answered 504, and one begun is cut short. 60s when left out."`
	ReadaheadBytes    int64           `json:"readahead_bytes,omitempty" min:"0" default:"8388608" doc:"How far past what a response reads the pieces are fetched before all others, nearest first. 64 KiB to 256 MiB. 8 MiB when left out or 0."`
	Magnet            bool            `json:"magnet,omitempty" doc:"Lets a request add the torrent its path names when the role does not have it, from the magnet link in its magnet query parameter, which must be of that torrent: /bt/<info hash>/?magnet=<link>, escaped. Such torrents are temporary: they download only the files streamed, are bounded by max_temporary and max_temporary_bytes, and are removed with their data temporary_ttl after their last stream ended, and when fit starts. Needs auth, whose publish on bt/<info hash> the request must have. Off when left out."`
	InfoTimeout       config.Duration `json:"info_timeout,omitempty" default:"\"60s\"" doc:"How long a request waits for the info of a torrent added from a magnet link to arrive from peers; past it the answer is 504. 60s when left out."`
	MaxTemporary      int             `json:"max_temporary,omitempty" min:"0" default:"4" doc:"The most temporary torrents, added by magnet link on the fly, the role holds at once; past it adding one is answered 503. At most 1000. 4 when left out or 0."`
	MaxTemporaryBytes int64           `json:"max_temporary_bytes,omitempty" min:"0" default:"17179869184" doc:"The most bytes the temporary torrents may want of the disk together: a torrent whose files streamed would take it past this is answered 507, and removed when it was just added. 16 GiB when left out or 0."`
	TemporaryTTL      config.Duration `json:"temporary_ttl,omitempty" default:"\"10m\"" doc:"How long a temporary torrent stays after its last stream ended, for the next range a player asks: then it is removed with its data. At most 7 days. 10m when left out."`
	CacheControl      string          `json:"cache_control,omitempty" doc:"Sent as Cache-Control with every file. None when left out."`
	CORS              string          `json:"cors,omitempty" doc:"Sent as Access-Control-Allow-Origin, as \"*\", for players on other origins. None when left out."`

	engine       *fib.Engine
	prefix       string
	auth         *mediaauth.Policy
	maxStreams   int64
	readTimeout  time.Duration
	readahead    int64
	infoTimeout  time.Duration
	maxTemp      int
	maxTempBytes int64
	ttl          time.Duration
	stall        time.Duration
	graph        module.Holder

	active atomic.Int64
	// sent counts the body bytes handed to the connections.
	sent atomic.Int64
	// mu guards streams, the responses streaming, which Drain ends.
	mu      sync.Mutex
	streams map[*stream]struct{}
	drained bool
}

// Provision checks the params.
func (h *BTStream) Provision(ctx *module.Context) error {
	h.prefix = h.Prefix
	if h.prefix == "" {
		h.prefix = DefaultPrefix
	}
	if !strings.HasPrefix(h.prefix, "/") {
		return ctx.Errorf("prefix", "%q does not start with /", h.prefix)
	}
	if !strings.HasSuffix(h.prefix, "/") {
		h.prefix += "/"
	}
	p, err := mediaauth.Provision(ctx, "auth", h.Auth)
	if err != nil {
		return err
	}
	h.auth = p
	if h.Magnet && p == nil {
		return ctx.Errorf("magnet", "magnet needs auth: adding a torrent downloads from the internet, so it is let only to those with publish on bt/<info hash>")
	}
	h.maxStreams = int64(h.MaxStreams)
	if h.maxStreams <= 0 {
		h.maxStreams = DefaultMaxStreams
	}
	if h.maxStreams > maxMaxStreams {
		return ctx.Errorf("max_streams", "%d is more than %d", h.maxStreams, maxMaxStreams)
	}
	h.readTimeout = h.ReadTimeout.Std()
	if h.readTimeout <= 0 {
		h.readTimeout = DefaultReadTimeout
	}
	h.infoTimeout = h.InfoTimeout.Std()
	if h.infoTimeout <= 0 {
		h.infoTimeout = DefaultInfoTimeout
	}
	h.readahead = h.ReadaheadBytes
	if h.readahead <= 0 {
		h.readahead = DefaultReadahead
	}
	if h.readahead < minReadahead || h.readahead > maxReadahead {
		return ctx.Errorf("readahead_bytes", "%d is not between 64 KiB and 256 MiB", h.readahead)
	}
	h.maxTemp = h.MaxTemporary
	if h.maxTemp <= 0 {
		h.maxTemp = DefaultMaxTemporary
	}
	if h.maxTemp > maxMaxTemporary {
		return ctx.Errorf("max_temporary", "%d is more than %d", h.maxTemp, maxMaxTemporary)
	}
	h.maxTempBytes = h.MaxTemporaryBytes
	if h.maxTempBytes <= 0 {
		h.maxTempBytes = DefaultMaxTemporaryBytes
	}
	h.ttl = h.TemporaryTTL.Std()
	if h.ttl <= 0 {
		h.ttl = DefaultTemporaryTTL
	}
	if h.ttl > maxTemporaryTTL {
		return ctx.Errorf("temporary_ttl", "%s is more than %s", h.ttl, maxTemporaryTTL)
	}
	if strings.ContainsAny(h.CacheControl+h.CORS, "\r\n") {
		return ctx.Errorf("", "cache_control and cors may not hold a line break")
	}
	h.engine, h.stall, h.graph = ctx.Engine(), ctx.WriteStallTimeout(), ctx.Graph()
	h.streams = map[*stream]struct{}{}
	return nil
}

// Drain ends the responses still streaming of a config swapped out.
func (h *BTStream) Drain() {
	h.mu.Lock()
	h.drained = true
	list := make([]*stream, 0, len(h.streams))
	for s := range h.streams {
		list = append(list, s)
	}
	h.mu.Unlock()
	for _, s := range list {
		s.cancel()
	}
}

// Streams returns how many responses the handler streams now.
func (h *BTStream) Streams() int { return int(h.active.Load()) }

// ServeHTTP serves the file the path names.
func (h *BTStream) ServeHTTP(c *fibhttp.Context) {
	r := c.Request
	// Whatever the answer, the access log holds no token or signature.
	mediaauth.Redact(r)
	if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead {
		h.respond(c, stdhttp.StatusMethodNotAllowed, "only GET and HEAD", "Allow", "GET, HEAD")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, h.prefix)
	id, sel, slash := strings.Cut(rest, "/")
	id = strings.ToLower(id)
	if !ok || !validID(id) {
		h.respond(c, stdhttp.StatusNotFound, "no torrent in the path: <prefix><info hash>/<file path or index>")
		return
	}
	var magnet string
	if strings.Contains(r.URL.RawQuery, "magnet=") {
		magnet = r.URL.Query().Get("magnet")
	}
	if magnet != "" {
		if !h.Magnet {
			h.respond(c, stdhttp.StatusForbidden, "adding a torrent by magnet link is off")
			return
		}
		if len(magnet) > bittorrent.MaxMagnetBytes {
			h.respond(c, stdhttp.StatusBadRequest, "the magnet link is longer than "+strconv.Itoa(bittorrent.MaxMagnetBytes)+" bytes")
			return
		}
		m, err := metainfo.ParseMagnet(magnet)
		if err != nil {
			h.respond(c, stdhttp.StatusBadRequest, "not a magnet link: "+err.Error())
			return
		}
		if magnetID(m) != id {
			h.respond(c, stdhttp.StatusBadRequest, "the magnet link is of another torrent than the path's")
			return
		}
	}
	role := bittorrent.Lookup(h.engine)
	if role == nil {
		h.respond(c, stdhttp.StatusServiceUnavailable, "the bittorrent.client role does not run", "Retry-After", "5")
		return
	}
	req := request{id: id, sel: sel, largest: !slash || sel == "", magnet: magnet}
	if h.auth == nil {
		h.start(c, role, req)
		return
	}
	if magnet != "" {
		req.creds = mediaauth.FromHTTP(r)
	}
	// The torrent's resource, as a signed URL is signed for: the path up
	// to and with the '/' after its info hash.
	resource := r.URL.Path[:len(h.prefix)+len(id)]
	if slash {
		resource += "/"
	}
	h.auth.GuardResource(c, "bt/"+id, resource, h.CORS, func(mediaauth.Access) { h.start(c, role, req) })
}

// request is what a request asks for.
type request struct {
	id, sel string
	// largest asks for the torrent's largest file.
	largest bool
	magnet  string
	// creds are the request's, for publish when it adds its torrent.
	creds mediaauth.Credentials
}

// start takes a slot of max_streams for the request and starts its stream,
// or answers 503.
func (h *BTStream) start(c *fibhttp.Context, role *bittorrent.Role, req request) {
	if h.active.Add(1) > h.maxStreams {
		h.active.Add(-1)
		h.respond(c, stdhttp.StatusServiceUnavailable, "max_streams responses are streaming already", "Retry-After", "1")
		return
	}
	s := newStream(h, c, role, req)
	h.mu.Lock()
	drained := h.drained
	if !drained {
		h.streams[s] = struct{}{}
	}
	h.mu.Unlock()
	if drained {
		h.active.Add(-1)
		h.respond(c, stdhttp.StatusServiceUnavailable, "the config is being replaced", "Retry-After", "1")
		return
	}
	c.Retain()
	h.graph.Retain()
	c.OnCancel(func(error) { s.cancel() })
	// The handler's pool: a goroutine for each stream, max_streams at most.
	go s.open()
}

// done gives back the slot of s.
func (h *BTStream) done(s *stream) {
	h.mu.Lock()
	delete(h.streams, s)
	h.mu.Unlock()
	h.active.Add(-1)
}

// respond answers with status and a plain text message, and the header
// fields extra names in pairs of name and value.
func (h *BTStream) respond(c *fibhttp.Context, status int, msg string, extra ...string) {
	hdr := stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}}
	if h.CORS != "" {
		hdr["Access-Control-Allow-Origin"] = []string{h.CORS}
	}
	for i := 0; i+1 < len(extra); i += 2 {
		hdr[extra[i]] = []string{extra[i+1]}
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: hdr, Body: []byte(msg + "\n")})
}

// validID reports whether id is a torrent ID: 40 lowercase hex digits.
func validID(id string) bool {
	if len(id) != 40 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// magnetID is the torrent ID of a magnet link, as the role makes it: its
// v1 info hash, or its v2 hash truncated.
func magnetID(m *metainfo.Magnet) string {
	if !m.InfoHash.IsZero() {
		return m.InfoHash.String()
	}
	return m.InfoHashV2.Truncate().String()
}
