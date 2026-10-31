package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/wire"
	"github.com/lesismal/fit/config"
)

// The bounds of the agent API's requests.
const (
	maxHeader = 64 << 10
	// inlineMax is the largest body sent in the stream's request itself,
	// as agenthub's nodes and hubs expect; a larger one streams after it,
	// in frames of frameSize, which needs fit's on the far side.
	inlineMax = 1 << 20
	frameSize = 64 << 10
	// stallTimeout bounds each wait for an agent to take what was written.
	stallTimeout = 60 * time.Second
)

// pathAPI maps an agent path (after /v1/) to its API format.
var pathAPI = func() map[string]string {
	out := map[string]string{}
	for api, paths := range model.ProxyPaths {
		for _, p := range paths {
			out[p] = api
		}
	}
	return out
}()

// apiServer is the agent API, a binding of its own on the listeners'
// engine.
type apiServer struct {
	c        *Client
	cfg      *config.AgentClientAPI
	engine   *fib.Engine
	index    int
	addr     net.Addr
	secure   bool
	limit    *connLimit
	inflight atomic.Int64
}

func startAPI(c *Client, cfg *config.AgentClientAPI) (*apiServer, error) {
	s := &apiServer{c: c, cfg: cfg, engine: c.H.Engine, index: -1}
	var h fib.Handler = fibhttp.NewHandlerWithConfig(fibhttp.Config{
		MaxHeaderBytes:    maxHeader,
		MaxBodyBytes:      cfg.Body(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}, fibhttp.HandlerFunc(s.serve))
	if cfg.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("api: %w", err)
		}
		s.secure = true
		h = fibtls.NewServer(fibhttp.ConfigureTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}), h)
	}
	s.limit = newConnLimit(h, cfg.Conns())
	i, err := s.engine.AddBinding(fib.Binding{Addr: cfg.Address, Handler: s.limit})
	if err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}
	s.index = i
	if addrs, err := s.engine.BindingAddrs(i); err == nil && len(addrs) > 0 {
		s.addr = addrs[0]
	}
	return s, nil
}

func (s *apiServer) url() string {
	if s.addr == nil {
		return ""
	}
	if s.secure {
		return "https://" + s.addr.String()
	}
	return "http://" + s.addr.String()
}

func (s *apiServer) stop() {
	if s.index >= 0 {
		_ = s.engine.RemoveBinding(s.index)
	}
	s.limit.closeAll()
}

// ---- Errors in the API's own shape ----

// errorAPI picks the error shape of a request: Anthropic for its paths or
// when the agent speaks Anthropic.
func errorAPI(r *stdhttp.Request, api string) string {
	if api != "" {
		return api
	}
	if r.Header.Get("anthropic-version") != "" || r.Header.Get("x-api-key") != "" {
		return model.APIAnthropic
	}
	return model.APIOpenAI
}

func writeError(c *fibhttp.Context, api string, status int, msg string) {
	writeErrorCode(c, api, status, "", msg)
}

// writeErrorCode writes an error the agent's SDK understands, with an
// explicit OpenAI error code (empty: derived from status).
func writeErrorCode(c *fibhttp.Context, api string, status int, oaCode, msg string) {
	var body any
	if api == model.APIAnthropic {
		typ := map[int]string{
			stdhttp.StatusBadRequest: "invalid_request_error", stdhttp.StatusUnauthorized: "authentication_error",
			stdhttp.StatusForbidden: "permission_error", stdhttp.StatusNotFound: "not_found_error",
			stdhttp.StatusRequestEntityTooLarge: "request_too_large", stdhttp.StatusTooManyRequests: "rate_limit_error",
			stdhttp.StatusServiceUnavailable: "overloaded_error",
		}[status]
		if typ == "" {
			typ = "api_error"
		}
		body = map[string]any{"type": "error", "error": map[string]string{"type": typ, "message": msg}}
	} else {
		typ, code := "server_error", ""
		switch status {
		case stdhttp.StatusBadRequest, stdhttp.StatusRequestEntityTooLarge:
			typ = "invalid_request_error"
		case stdhttp.StatusUnauthorized:
			typ, code = "invalid_request_error", "invalid_api_key"
		case stdhttp.StatusNotFound:
			typ, code = "invalid_request_error", "model_not_found"
		case stdhttp.StatusTooManyRequests:
			typ, code = "rate_limit_exceeded", "rate_limit_exceeded"
		}
		if oaCode != "" {
			code = oaCode
		}
		e := map[string]any{"message": msg, "type": typ, "code": nil}
		if code != "" {
			e["code"] = code
		}
		body = map[string]any{"error": e}
	}
	b, _ := json.Marshal(body)
	_ = c.Respond(status, "application/json", b)
}

// statusFor maps a mesh error code to the HTTP status for the agent.
func statusFor(code int) int {
	switch code {
	case model.CodeBadRequest:
		return stdhttp.StatusBadRequest
	case model.CodeNotFound:
		return stdhttp.StatusNotFound
	case model.CodeRateLimited:
		return stdhttp.StatusTooManyRequests
	case model.CodeUnavailable:
		return stdhttp.StatusServiceUnavailable
	default:
		return stdhttp.StatusBadGateway
	}
}

// ---- Models list ----

// modelObject is one entry of GET /v1/models, carrying the fields of both
// OpenAI's and Anthropic's model lists.
type modelObject struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

func (s *apiServer) modelObjects() []modelObject {
	var out []modelObject
	seen := map[string]bool{}
	for _, m := range s.c.cat.list() {
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		out = append(out, modelObject{ID: m.Name, Object: "model", OwnedBy: "agenthub", Type: "model", DisplayName: m.Name,
			CreatedAt: "1970-01-01T00:00:00Z"})
	}
	return out
}

// serve answers every request, on a fib worker, without waiting.
func (s *apiServer) serve(c *fibhttp.Context) {
	c.Header().Set("Cache-Control", "no-store")
	r := c.Request
	switch r.Method {
	case stdhttp.MethodPost:
		s.serveProxy(c)
	case stdhttp.MethodGet:
		s.serveGet(c)
	default:
		writeErrorCode(c, errorAPI(r, ""), stdhttp.StatusNotFound, "unknown_url", "unknown endpoint "+r.Method+" "+r.URL.Path)
	}
}

func (s *apiServer) serveGet(c *fibhttp.Context) {
	r := c.Request
	eapi := errorAPI(r, "")
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/")
	if !ok || rest != "models" && !strings.HasPrefix(rest, "models/") {
		writeErrorCode(c, eapi, stdhttp.StatusNotFound, "unknown_url", "unknown endpoint GET "+r.URL.Path)
		return
	}
	if !s.c.keys.auth(r.Header) {
		writeError(c, eapi, stdhttp.StatusUnauthorized, "invalid or missing api key")
		return
	}
	list := s.modelObjects()
	var body any
	if id := strings.TrimPrefix(rest, "models/"); id != rest {
		for _, m := range list {
			if m.ID == id {
				body = m
			}
		}
		if body == nil {
			writeError(c, eapi, stdhttp.StatusNotFound, fmt.Sprintf("model %q not found", id))
			return
		}
	} else {
		if list == nil {
			list = []modelObject{}
		}
		resp := map[string]any{"object": "list", "data": list, "has_more": false, "first_id": nil, "last_id": nil}
		if n := len(list); n > 0 {
			resp["first_id"], resp["last_id"] = list[0].ID, list[n-1].ID
		}
		body = resp
	}
	b, _ := json.Marshal(body)
	_ = c.Respond(stdhttp.StatusOK, "application/json", b)
}

// serveProxy forwards a call. The body is held whole, within max_body_bytes:
// the route depends on the model it names.
func (s *apiServer) serveProxy(c *fibhttp.Context) {
	r := c.Request
	path, _ := strings.CutPrefix(r.URL.Path, "/v1/")
	api, ok := pathAPI[path]
	eapi := errorAPI(r, api)
	if !ok || !strings.HasPrefix(r.URL.Path, "/v1/") {
		writeErrorCode(c, eapi, stdhttp.StatusNotFound, "unknown_url", "unknown endpoint POST "+r.URL.Path)
		return
	}
	if !s.c.keys.auth(r.Header) {
		writeError(c, eapi, stdhttp.StatusUnauthorized, "invalid or missing api key")
		return
	}
	body := c.Body()
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		writeError(c, eapi, stdhttp.StatusBadRequest, "request body is not valid json: "+err.Error())
		return
	}
	if probe.Model == "" {
		writeError(c, eapi, stdhttp.StatusBadRequest, "missing \"model\" in request body")
		return
	}
	req := model.ProxyReq{API: api, Path: path, Model: probe.Model}
	for _, k := range model.ProxyHeaders {
		if v := r.Header.Get(k); v != "" {
			if req.Header == nil {
				req.Header = map[string]string{}
			}
			req.Header[k] = v
		}
	}
	routes, known, other := s.c.cat.routes(req.Model, req.API)
	if len(routes) == 0 {
		switch {
		case !known:
			writeError(c, api, stdhttp.StatusNotFound, fmt.Sprintf("model %q not found", req.Model))
		case other != "":
			writeError(c, api, stdhttp.StatusNotFound, fmt.Sprintf("model %q is served with the %s API, not %s", req.Model, other, req.API))
		default:
			writeError(c, api, stdhttp.StatusServiceUnavailable, fmt.Sprintf("model %q has no ready node now", req.Model))
		}
		return
	}
	if s.inflight.Add(1) > int64(s.cfg.Inflight()) {
		s.inflight.Add(-1)
		writeError(c, api, stdhttp.StatusTooManyRequests, "too many calls in flight; try again")
		return
	}
	x := &call{s: s, c: c, req: req, body: body, routes: routes, end: s.c.stats.begin(req.Model)}
	c.Retain()
	c.OnCancel(func(error) { x.cancel(errAgentGone) })
	x.mu.Lock()
	x.firstByte = time.AfterFunc(s.cfg.FirstByte(), func() { x.cancel(errFirstByte) })
	x.total = time.AfterFunc(s.cfg.Total(), func() { x.cancel(errTotal) })
	x.mu.Unlock()
	x.next()
}

var (
	errAgentGone = errors.New("the agent went away")
	errFirstByte = mesh.Errorf(model.CodeUnavailable, "no response began in time")
	errTotal     = errors.New("the call took longer than the API's timeout")
)

// retryable reports whether a failure before the head frame may be retried
// on the next route: the route cannot serve the model now (CodeUnavailable,
// offline included), or its link broke.
func retryable(err error) bool {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Code == model.CodeUnavailable
	}
	return errors.Is(err, wire.ErrClosed)
}

// call is one agent call, from its request to the end of its response. It
// moves on callbacks: the stream's frames, the agent's room for more, its
// leaving and the timers.
type call struct {
	s      *apiServer
	c      *fibhttp.Context
	req    model.ProxyReq
	body   []byte
	routes []Route
	end    func(bytes int64, failed bool)

	mu        sync.Mutex
	attempt   int
	exclude   []string
	route     Route
	nodeHeld  string // the direct node whose count the call raised
	st        *wire.Stream
	stCancel  context.CancelFunc
	started   bool  // the head was written
	done      bool  // the call is over
	cause     error // why it was cancelled
	lastErr   error
	sent      int // bytes of a streamed body sent
	bytes     int64
	held      int
	waiting   bool
	firstByte *time.Timer
	total     *time.Timer
}

// next tries the next route, or fails the call.
func (x *call) next() {
	x.mu.Lock()
	if x.done {
		x.mu.Unlock()
		return
	}
	if x.attempt >= len(x.routes) || x.attempt >= x.s.c.maxAttempts || x.cause != nil {
		err := x.lastErr
		if x.cause != nil {
			err = x.cause
		}
		if err == nil {
			err = mesh.Errorf(model.CodeUnavailable, "no route to model %s", x.req.Model)
		}
		x.mu.Unlock()
		x.fail(err)
		return
	}
	rt := x.routes[x.attempt]
	x.attempt++
	x.route = rt
	req := x.req
	method := model.MethodHubProxy
	if rt.Kind == RouteHub {
		req.Exclude = x.exclude
	} else {
		method = model.MethodNodeProxy
		x.s.c.cat.addInflight(rt.ID, 1)
		x.nodeHeld = rt.ID
	}
	if len(x.body) <= inlineMax {
		req.Body = x.body
	} else {
		req.BodyStream, req.BodySize = true, int64(len(x.body))
	}
	x.sent = 0
	ctx, cancel := context.WithCancel(context.Background())
	x.stCancel = cancel
	x.mu.Unlock()
	st, err := x.s.c.Mesh.OpenStream(ctx, rt.ID, method, req)
	if err != nil {
		cancel()
		x.retry(rt, err)
		return
	}
	x.mu.Lock()
	x.st = st
	cause := x.cause
	x.mu.Unlock()
	if cause != nil {
		st.Cancel()
	}
	if req.BodyStream {
		x.sendBody(st)
	}
	st.OnData(func(data []byte, err error) { x.frame(st, rt, data, err) })
}

// retry moves to the next route after a failure before the head, when it
// may, or fails the call.
func (x *call) retry(rt Route, err error) {
	x.mu.Lock()
	x.unholdLocked()
	if rt.Kind == RouteNode {
		x.exclude = append(x.exclude, rt.ID)
	}
	x.lastErr = err
	cause := x.cause
	x.mu.Unlock()
	if cause == nil && retryable(err) {
		x.s.c.Log.Infof("route %s %s unavailable for %s, trying the next one: %v", rt.Kind, rt.ID, x.req.Model, err)
		x.next()
		return
	}
	if cause != nil {
		err = cause
	}
	x.fail(err)
}

// sendBody sends the body in frames as the stream's credit allows, then
// half-closes the stream.
func (x *call) sendBody(st *wire.Stream) {
	for {
		x.mu.Lock()
		off := x.sent
		x.mu.Unlock()
		if off >= len(x.body) {
			_ = st.CloseSend()
			return
		}
		end := min(off+frameSize, len(x.body))
		ok, err := st.TrySend(x.body[off:end])
		if err != nil {
			return // the stream's end says why
		}
		if !ok {
			if !st.OnCredit(func() { x.sendBody(st) }) {
				continue
			}
			return
		}
		x.mu.Lock()
		x.sent = end
		x.mu.Unlock()
	}
}

// frame takes a frame of the stream rt answers on.
func (x *call) frame(st *wire.Stream, rt Route, data []byte, err error) {
	x.mu.Lock()
	if x.done || x.st != st {
		x.mu.Unlock()
		if data != nil {
			st.Release(len(data))
		}
		return
	}
	started := x.started
	x.mu.Unlock()
	if !started {
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = mesh.Errorf(model.CodeInternal, "%s ended before its head frame", rt.ID)
			}
			x.retry(rt, err)
			return
		}
		var head model.ProxyHead
		if json.Unmarshal(data, &head) != nil || head.Status < 100 || head.Status > 999 {
			st.Cancel()
			x.retry(rt, mesh.Errorf(model.CodeInternal, "bad head frame from %s", rt.ID))
			return
		}
		st.Release(len(data))
		x.begin(head)
		return
	}
	if err != nil {
		x.finish(st, err)
		return
	}
	x.write(st, data)
}

// begin writes the response's status and header.
func (x *call) begin(head model.ProxyHead) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.started = true
	x.firstByte.Stop()
	h := x.c.Header()
	ct := head.ContentType
	if ct == "" {
		ct = "application/json"
	}
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	if head.Node != "" {
		h.Set("X-Agenthub-Node", head.Node)
	}
	x.c.WriteHeader(head.Status)
	_ = x.c.FlushError()
	if head.Status >= 400 {
		x.lastErr = fmt.Errorf("upstream status %d", head.Status)
	}
}

// write writes a piece of the body; while the agent is behind, the frame
// stays unreleased, so that the far side waits for credit.
func (x *call) write(st *wire.Stream, data []byte) {
	x.mu.Lock()
	above, err := x.c.WriteNoWait(data)
	if err == nil {
		err = x.c.FlushError()
	}
	if err != nil {
		x.mu.Unlock()
		st.Release(len(data))
		x.cancel(errAgentGone)
		return
	}
	defer x.mu.Unlock()
	x.bytes += int64(len(data))
	if !above && !x.waiting {
		st.Release(len(data))
		return
	}
	x.held += len(data)
	if x.waiting {
		return
	}
	x.waiting = true
	_ = x.c.SetWriteDeadline(time.Now().Add(stallTimeout))
	if !x.c.OnWritable(func(err error) { x.writable(st, err) }) {
		x.releaseLocked(st)
	}
}

func (x *call) writable(st *wire.Stream, err error) {
	if err != nil {
		st.Cancel()
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	_ = x.c.SetWriteDeadline(time.Time{})
	x.releaseLocked(st)
}

func (x *call) releaseLocked(st *wire.Stream) {
	x.waiting = false
	if n := x.held; n > 0 {
		x.held = 0
		st.Release(n)
	}
}

// finish ends a response that began: whole when the stream ended well, cut
// short otherwise, so that the agent can tell.
func (x *call) finish(st *wire.Stream, err error) {
	x.mu.Lock()
	if x.done {
		x.mu.Unlock()
		return
	}
	x.done = true
	n, failed := x.bytes, x.lastErr != nil
	x.stop()
	x.mu.Unlock()
	if errors.Is(err, io.EOF) {
		_ = x.c.SetWriteDeadline(time.Now().Add(stallTimeout))
		_ = x.c.Finish()
	} else {
		failed = true
		if !errors.Is(err, errAgentGone) {
			x.s.c.Log.Warnf("the call to %s broke after its head, %d bytes in: %v", x.req.Model, n, err)
		}
		if x.c.Request.ProtoMajor == 1 {
			// A chunked body without its last chunk: closing the
			// connection is what tells the agent.
			_ = x.c.Conn.Close()
		} else {
			_ = x.c.Finish()
		}
	}
	x.end(n, failed)
	x.s.inflight.Add(-1)
	x.c.Release()
}

// fail answers a call no route served.
func (x *call) fail(err error) {
	x.mu.Lock()
	if x.done {
		x.mu.Unlock()
		return
	}
	x.done = true
	x.stop()
	x.mu.Unlock()
	if !errors.Is(err, errAgentGone) {
		code, msg := mesh.ErrCode(err)
		status := statusFor(code)
		if errors.Is(err, errTotal) {
			status = stdhttp.StatusGatewayTimeout
		}
		writeError(x.c, x.req.API, status, msg)
	}
	x.end(0, true)
	x.s.inflight.Add(-1)
	x.c.Release()
}

// unholdLocked lowers the count of calls on the direct node the call is
// on, once. x.mu is held.
func (x *call) unholdLocked() {
	if x.nodeHeld != "" {
		x.s.c.cat.addInflight(x.nodeHeld, -1)
		x.nodeHeld = ""
	}
}

// stop stops the timers and the stream's context. x.mu is held.
func (x *call) stop() {
	x.unholdLocked()
	x.firstByte.Stop()
	x.total.Stop()
	if x.stCancel != nil {
		x.stCancel()
	}
}

// cancel ends the call for cause: the agent left, or a timer ran out.
func (x *call) cancel(cause error) {
	x.mu.Lock()
	if x.done || x.cause != nil {
		x.mu.Unlock()
		return
	}
	x.cause = cause
	st, started := x.st, x.started
	x.mu.Unlock()
	if st != nil {
		// Its end comes back through frame: before the head, retry sees
		// the cause and fails the call with it.
		st.Cancel()
		if started {
			x.finish(st, cause)
		}
		return
	}
	x.fail(cause)
}

// connLimit closes the connections past max as they open, and closes every
// one it has open when the API stops.
type connLimit struct {
	fib.Handler
	max int64
	n   atomic.Int64

	mu     sync.Mutex
	conns  map[*fib.Connection]struct{}
	closed bool
}

func newConnLimit(h fib.Handler, max int) *connLimit {
	return &connLimit{Handler: h, max: int64(max), conns: map[*fib.Connection]struct{}{}}
}

func (l *connLimit) OnOpen(c *fib.Connection) {
	if l.n.Add(1) > l.max {
		l.n.Add(-1)
		_ = c.Close()
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		l.n.Add(-1)
		_ = c.Close()
		return
	}
	l.conns[c] = struct{}{}
	l.mu.Unlock()
	l.Handler.OnOpen(c)
}

func (l *connLimit) OnClose(c *fib.Connection, err error) {
	l.mu.Lock()
	_, ok := l.conns[c]
	delete(l.conns, c)
	l.mu.Unlock()
	if ok {
		l.n.Add(-1)
		l.Handler.OnClose(c, err)
	}
}

func (l *connLimit) OnShutdown(c *fib.Connection) {
	if h, ok := l.Handler.(fib.ShutdownHandler); ok {
		h.OnShutdown(c)
	}
}

func (l *connLimit) OnEngineClose(c *fib.Connection) {
	if h, ok := l.Handler.(fib.EngineCloseHandler); ok {
		h.OnEngineClose(c)
	}
}

func (l *connLimit) closeAll() {
	l.mu.Lock()
	l.closed = true
	conns := make([]*fib.Connection, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}
