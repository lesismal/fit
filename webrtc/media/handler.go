package media

import (
	"mime"
	stdhttp "net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".whip", New: func() any { return new(WHIP) },
		Doc: "Takes WebRTC streams published with WHIP (RFC 9725) into fit's media server, webrtc.media, by name."})
	module.Register(module.Info{ID: module.Handlers + ".whep", New: func() any { return new(WHEP) },
		Doc: "Plays the streams of fit's media server, webrtc.media, to viewers with WHEP (draft-ietf-wish-whep), by name."})
}

// WHIP is the whip handler: a POST of an offer publishes a stream, and a
// DELETE of the session's URL ends it.
type WHIP struct {
	Stream string `json:"stream,omitempty" doc:"The stream's name. When left out, the name is the last segment of the path a POST is sent to, as cam1 in /whip/cam1 on a route of /whip/*: one route takes every stream. A session's URL is the path of its POST followed by its ID, which the route must match too, as /live/* beside /live."`

	h handler
}

// WHEP is the whep handler: a POST of an offer plays a stream, and a DELETE
// of the session's URL ends the viewer's session.
type WHEP struct {
	Stream string `json:"stream,omitempty" doc:"The stream's name. When left out, the name is the last segment of the path a POST is sent to, as cam1 in /whep/cam1 on a route of /whep/*: one route plays every stream. A session's URL is the path of its POST followed by its ID, which the route must match too, as /watch/* beside /watch."`

	h handler
}

// Provision checks the stream's name.
func (w *WHIP) Provision(ctx *module.Context) error { return w.h.provision(ctx, Publisher, w.Stream) }

// ServeHTTP serves the endpoint and its sessions' URLs.
func (w *WHIP) ServeHTTP(c *fibhttp.Context) { w.h.ServeHTTP(c) }

// Provision checks the stream's name.
func (w *WHEP) Provision(ctx *module.Context) error { return w.h.provision(ctx, Viewer, w.Stream) }

// ServeHTTP serves the endpoint and its sessions' URLs.
func (w *WHEP) ServeHTTP(c *fibhttp.Context) { w.h.ServeHTTP(c) }

// handler is what WHIP and WHEP share: the endpoint and its sessions' URLs.
type handler struct {
	Stream string
	kind   Kind
	engine *fib.Engine
}

// streamName is what a stream's name may be: what a path segment holds
// without escapes.
var streamName = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

// provision checks the stream's name and finds the engine the media server
// is registered under.
func (h *handler) provision(ctx *module.Context, kind Kind, stream string) error {
	if stream != "" && !streamName.MatchString(stream) {
		return ctx.Errorf("stream", "%q is not a name of 1 to 128 letters, digits, '.', '_', '~' and '-'", stream)
	}
	h.Stream, h.kind, h.engine = stream, kind, ctx.Engine()
	return nil
}

// sessionID is what a session's ID is: 32 hex digits.
var sessionID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ServeHTTP answers the endpoint's POST and OPTIONS, and the DELETE of a
// session's URL, the endpoint's path followed by the session's ID. PATCH,
// for trickle ICE and ICE restarts, is answered 405: fit is an ICE lite
// agent whose answer holds all of its candidates, and it learns the peer's
// address from the peer's checks, so trickled candidates add nothing; an
// ICE restart is a new session (RFC 9725 section 4.3.1).
func (h *handler) ServeHTTP(c *fibhttp.Context) {
	path := strings.TrimSuffix(c.Request.URL.Path, "/")
	last := path[strings.LastIndexByte(path, '/')+1:]
	srv := Lookup(h.engine)
	if sessionID.MatchString(last) && (srv == nil || srv.has(h.kind, last)) {
		switch c.Request.Method {
		case stdhttp.MethodDelete:
			if srv == nil || !srv.End(h.kind, last) {
				respond(c, stdhttp.StatusNotFound, "no such session", nil)
				return
			}
			respond(c, stdhttp.StatusOK, "", nil)
		case stdhttp.MethodOptions:
			respond(c, stdhttp.StatusNoContent, "", stdhttp.Header{"Allow": {"DELETE, OPTIONS"}})
		default:
			respond(c, stdhttp.StatusMethodNotAllowed, "a session's URL takes DELETE; PATCH (trickle ICE, ICE restarts) is not supported", stdhttp.Header{"Allow": {"DELETE, OPTIONS"}})
		}
		return
	}
	name := h.Stream
	if name == "" {
		name = last
	}
	switch c.Request.Method {
	case stdhttp.MethodPost:
	case stdhttp.MethodOptions:
		respond(c, stdhttp.StatusNoContent, "", stdhttp.Header{"Allow": {"POST, OPTIONS"}, "Accept-Post": {"application/sdp"}})
		return
	default:
		if sessionID.MatchString(last) {
			respond(c, stdhttp.StatusNotFound, "no such session", nil)
			return
		}
		respond(c, stdhttp.StatusMethodNotAllowed, "the endpoint takes POST", stdhttp.Header{"Allow": {"POST, OPTIONS"}})
		return
	}
	if !streamName.MatchString(name) {
		respond(c, stdhttp.StatusNotFound, "no stream name in the path", nil)
		return
	}
	if mt, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type")); err != nil || mt != "application/sdp" {
		respond(c, stdhttp.StatusUnsupportedMediaType, "the offer must be application/sdp", stdhttp.Header{"Accept-Post": {"application/sdp"}})
		return
	}
	if srv == nil {
		respond(c, stdhttp.StatusServiceUnavailable, "webrtc.media is not running", nil)
		return
	}
	limit := srv.set.Load().sdpBytes
	if c.Request.ContentLength > int64(limit) {
		respond(c, stdhttp.StatusRequestEntityTooLarge, "the offer is too large", nil)
		return
	}
	o := &offer{h: h, srv: srv, c: c, name: name, path: path, limit: limit}
	if n := c.Request.ContentLength; n > 0 {
		o.buf = make([]byte, 0, n)
	}
	c.OnBody(o.body)
}

// offer gathers an offer's body, up to the limit, and answers it once whole.
type offer struct {
	h          *handler
	srv        *Server
	c          *fibhttp.Context
	name, path string
	limit      int

	mu   sync.Mutex
	buf  []byte
	done bool
}

func (o *offer) body(data []byte, fin bool, err error) {
	o.mu.Lock()
	if o.done {
		o.mu.Unlock()
		return
	}
	if err != nil {
		o.done = true
		o.mu.Unlock()
		return
	}
	if len(o.buf)+len(data) > o.limit {
		o.done = true
		o.mu.Unlock()
		respond(o.c, stdhttp.StatusRequestEntityTooLarge, "the offer is too large", nil)
		return
	}
	o.buf = append(o.buf, data...)
	if !fin {
		o.mu.Unlock()
		return
	}
	o.done = true
	sdp := string(o.buf)
	o.mu.Unlock()

	var a *Answer
	var e *Error
	if o.h.kind == Publisher {
		a, e = o.srv.Publish(o.name, sdp)
	} else {
		a, e = o.srv.View(o.name, sdp)
	}
	if e != nil {
		var hdr stdhttp.Header
		if e.RetryAfter > 0 {
			hdr = stdhttp.Header{"Retry-After": {strconv.Itoa(e.RetryAfter)}}
		}
		respond(o.c, e.Status, e.Msg, hdr)
		return
	}
	hdr := stdhttp.Header{
		"Content-Type": {"application/sdp"},
		"Location":     {o.path + "/" + a.ID},
		"Link":         o.srv.Links(a.ID),
	}
	if len(hdr["Link"]) == 0 {
		delete(hdr, "Link")
	}
	if err := o.c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusCreated, Header: hdr, Body: []byte(a.SDP)}); err != nil {
		// The peer left before its answer: its session goes too.
		o.srv.End(o.h.kind, a.ID)
	}
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

// has reports whether the session id of kind runs.
func (s *Server) has(kind Kind, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.sessions[id]
	return x != nil && x.kind == kind
}
