package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/bindcheck"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// The bounds of the agent API's requests.
const (
	maxHeader = 64 << 10
	// inlineMax is the largest body sent in the stream's request itself;
	// a larger one streams after it, in frames of frameSize.
	inlineMax = 1 << 20
	frameSize = 64 << 10
	// stallTimeout bounds each wait for an agent to take what was written.
	stallTimeout = 60 * time.Second
)

// endpoint is the call an agent's request makes: the protocol it speaks,
// the call (model.ProxyPaths) and, for Gemini, the model its path names.
type endpoint struct {
	api, path, model string
}

// agentPaths maps the paths agents POST to, but Gemini's, to their calls:
// /v1/... for OpenAI and Anthropic, /api/... for Ollama, /v2/... for
// Cohere, each where the vendor's SDKs call them once their base URL is the
// proxy's.
var agentPaths = func() map[string]endpoint {
	out := map[string]endpoint{}
	for api, prefix := range map[string]string{model.APIOpenAI: "/v1/", model.APIAnthropic: "/v1/", model.APIOllama: "/", model.APICohere: "/v2/"} {
		for _, p := range model.ProxyPaths[api] {
			out[prefix+p] = endpoint{api: api, path: p}
		}
	}
	return out
}()

// geminiPrefixes are where Gemini's SDKs call a model,
// {prefix}{model}:{method}: under /v1beta by default, /v1 when set so.
var geminiPrefixes = []string{"/v1beta/models/", "/v1/models/"}

// agentEndpoint returns the call of a POST to path; ok is false for a path
// that is none, whose endpoint may still name the protocol, for the shape
// of the error.
func agentEndpoint(path string) (e endpoint, ok bool) {
	if e, ok := agentPaths[path]; ok {
		return e, true
	}
	for _, pre := range geminiPrefixes {
		rest, found := strings.CutPrefix(path, pre)
		if !found {
			continue
		}
		// The method follows the last colon: a model's name may hold one.
		i := strings.LastIndexByte(rest, ':')
		if i <= 0 || !slices.Contains(model.ProxyPaths[model.APIGemini], rest[i+1:]) {
			return endpoint{api: model.APIGemini}, false
		}
		return endpoint{api: model.APIGemini, path: rest[i+1:], model: rest[:i]}, true
	}
	return endpoint{}, false
}

// apiServer is the agent API, a binding of its own on the listeners'
// engine.
type apiServer struct {
	c        *Proxy
	cfg      *config.LLMProxyAPI
	engine   *fib.Engine
	index    int
	addr     net.Addr
	secure   bool
	limit    *connLimit
	inflight atomic.Int64
}

func startAPI(c *Proxy, cfg *config.LLMProxyAPI) (*apiServer, error) {
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
	if err := bindcheck.Free("tcp", cfg.Address); err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}
	i, err := s.engine.AddBinding(fib.Binding{Addr: cfg.Address, Handler: s.limit, Filter: c.Allows})
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

// errorAPI picks the error shape of a request: its protocol's, or for a
// path that is none, the one its prefix or headers suggest.
func errorAPI(r *stdhttp.Request, api string) string {
	if api != "" {
		return api
	}
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/v1beta/") || r.Header.Get("x-goog-api-key") != "":
		return model.APIGemini
	case strings.HasPrefix(p, "/api/"):
		return model.APIOllama
	case strings.HasPrefix(p, "/v2/"):
		return model.APICohere
	case r.Header.Get("anthropic-version") != "" || r.Header.Get("x-api-key") != "":
		return model.APIAnthropic
	}
	return model.APIOpenAI
}

func writeError(c *fibhttp.Context, api string, status int, msg string) {
	writeErrorCode(c, api, status, "", msg)
}

// writeErrorCode writes an error the agent's SDK understands, in the shape
// of api: OpenAI's with an explicit error code (empty: derived from status),
// Anthropic's, Gemini's (Google's RPC status), Ollama's or Cohere's.
func writeErrorCode(c *fibhttp.Context, api string, status int, oaCode, msg string) {
	var body any
	switch api {
	case model.APIAnthropic:
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
	case model.APIGemini:
		st := map[int]string{
			stdhttp.StatusBadRequest: "INVALID_ARGUMENT", stdhttp.StatusUnauthorized: "UNAUTHENTICATED",
			stdhttp.StatusForbidden: "PERMISSION_DENIED", stdhttp.StatusNotFound: "NOT_FOUND",
			stdhttp.StatusRequestEntityTooLarge: "INVALID_ARGUMENT", stdhttp.StatusTooManyRequests: "RESOURCE_EXHAUSTED",
			stdhttp.StatusServiceUnavailable: "UNAVAILABLE", stdhttp.StatusGatewayTimeout: "DEADLINE_EXCEEDED",
			stdhttp.StatusBadGateway: "UNAVAILABLE",
		}[status]
		if st == "" {
			st = "INTERNAL"
		}
		body = map[string]any{"error": map[string]any{"code": status, "message": msg, "status": st}}
	case model.APIOllama:
		body = map[string]string{"error": msg}
	case model.APICohere:
		body = map[string]string{"message": msg}
	default:
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

// ---- Models lists ----

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

// modelNames returns the names of the models the proxy reaches, once
// each, sorted; only those called with protocol api, unless it is "".
func (s *apiServer) modelNames(api string) []string {
	var out []string
	for _, m := range s.c.cat.list() {
		if api != "" && model.Protocol(m.API) != api || slices.Contains(out, m.Name) {
			continue
		}
		out = append(out, m.Name)
	}
	return out
}

func (s *apiServer) modelObjects() []modelObject {
	var out []modelObject
	for _, name := range s.modelNames("") {
		out = append(out, modelObject{ID: name, Object: "model", OwnedBy: "fit", Type: "model", DisplayName: name,
			CreatedAt: "1970-01-01T00:00:00Z"})
	}
	return out
}

// geminiModel is a model as Gemini's models.list and models.get give it.
type geminiModel struct {
	Name                       string   `json:"name"`
	BaseModelID                string   `json:"baseModelId"`
	Version                    string   `json:"version"`
	DisplayName                string   `json:"displayName"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

func newGeminiModel(name string) geminiModel {
	return geminiModel{Name: "models/" + name, BaseModelID: name, Version: "001", DisplayName: name,
		SupportedGenerationMethods: model.ProxyPaths[model.APIGemini]}
}

// ollamaModel is a model as Ollama's /api/tags gives it.
type ollamaModel struct {
	Name       string   `json:"name"`
	Model      string   `json:"model"`
	ModifiedAt string   `json:"modified_at"`
	Size       int64    `json:"size"`
	Digest     string   `json:"digest"`
	Details    struct{} `json:"details"`
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

// authorized checks an agent's key: Authorization: Bearer, x-api-key or
// x-goog-api-key, or for Gemini's paths also the key query parameter, as
// Google's REST examples send it.
func (s *apiServer) authorized(r *stdhttp.Request, api string) bool {
	h := r.Header
	if api == model.APIGemini && requestKey(h) == "" {
		if k := r.URL.Query().Get("key"); k != "" {
			h = stdhttp.Header{"X-Api-Key": {k}}
		}
	}
	return s.c.keys.auth(h)
}

// serveGet answers the model lists: GET /v1/models and /v1/models/{id}
// (OpenAI's and Anthropic's, every model), /v1beta/models and
// /v1beta/models/{model} (Gemini's, its models), /api/tags (Ollama's, its
// models).
func (s *apiServer) serveGet(c *fibhttp.Context) {
	r := c.Request
	p := r.URL.Path
	var api string
	switch {
	case p == "/v1/models" || strings.HasPrefix(p, "/v1/models/"):
		api = model.APIOpenAI
	case p == "/v1beta/models" || strings.HasPrefix(p, "/v1beta/models/"):
		api = model.APIGemini
	case p == "/api/tags":
		api = model.APIOllama
	default:
		writeErrorCode(c, errorAPI(r, ""), stdhttp.StatusNotFound, "unknown_url", "unknown endpoint GET "+p)
		return
	}
	eapi := errorAPI(r, "")
	if api != model.APIOpenAI {
		eapi = api
	}
	if !s.authorized(r, api) {
		writeError(c, eapi, stdhttp.StatusUnauthorized, "invalid or missing api key")
		return
	}
	var body any
	switch api {
	case model.APIGemini:
		names := s.modelNames(model.APIGemini)
		if id, ok := strings.CutPrefix(p, "/v1beta/models/"); ok {
			if !slices.Contains(names, id) {
				writeError(c, eapi, stdhttp.StatusNotFound, fmt.Sprintf("model %q not found", id))
				return
			}
			body = newGeminiModel(id)
			break
		}
		list := make([]geminiModel, 0, len(names))
		for _, n := range names {
			list = append(list, newGeminiModel(n))
		}
		body = map[string]any{"models": list}
	case model.APIOllama:
		names := s.modelNames(model.APIOllama)
		list := make([]ollamaModel, 0, len(names))
		for _, n := range names {
			list = append(list, ollamaModel{Name: n, Model: n, ModifiedAt: "1970-01-01T00:00:00Z"})
		}
		body = map[string]any{"models": list}
	default:
		list := s.modelObjects()
		if id, ok := strings.CutPrefix(p, "/v1/models/"); ok {
			for _, m := range list {
				if m.ID == id {
					body = m
				}
			}
			if body == nil {
				writeError(c, eapi, stdhttp.StatusNotFound, fmt.Sprintf("model %q not found", id))
				return
			}
			break
		}
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

// maxModelName bounds the model name a call gives.
const maxModelName = 256

// callModel reads the model a call names, and the body's media type to
// pass on: Gemini's in the path; a multipart/form-data body's (OpenAI's
// uploads) in its model field; any other body's in its JSON's model.
func callModel(r *stdhttp.Request, e endpoint, body []byte) (name, ctype, msg string) {
	if e.api == model.APIGemini {
		return e.model, "", ""
	}
	ct := r.Header.Get("Content-Type")
	if b := model.FormBoundary(ct); b != "" {
		if e.api != model.APIOpenAI {
			return "", "", "a multipart body is taken only by OpenAI's endpoints"
		}
		start, end, err := model.FormField(body, b, "model")
		if err != nil {
			return "", "", "a bad multipart body: " + err.Error()
		}
		if start == end {
			return "", "", "missing \"model\" in the form"
		}
		return string(body[start:end]), ct, ""
	}
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", "", "request body is not valid json: " + err.Error()
	}
	if probe.Model == "" {
		return "", "", "missing \"model\" in request body"
	}
	return probe.Model, "", ""
}

// serveProxy forwards a call. The body is held whole, within max_body_bytes:
// the route depends on the model it names, and a route that cannot serve
// it gets the body again.
func (s *apiServer) serveProxy(c *fibhttp.Context) {
	r := c.Request
	e, ok := agentEndpoint(r.URL.Path)
	eapi := errorAPI(r, e.api)
	if !ok {
		writeErrorCode(c, eapi, stdhttp.StatusNotFound, "unknown_url", "unknown endpoint POST "+r.URL.Path)
		return
	}
	api := e.api
	if !s.authorized(r, api) {
		writeError(c, api, stdhttp.StatusUnauthorized, "invalid or missing api key")
		return
	}
	body := c.Body()
	name, ctype, msg := callModel(r, e, body)
	if msg == "" && (len(name) > maxModelName || strings.IndexFunc(name, unicode.IsSpace) >= 0) {
		msg = "the model name is up to 256 characters without spaces"
	}
	if msg != "" {
		writeError(c, api, stdhttp.StatusBadRequest, msg)
		return
	}
	req := model.ProxyReq{API: api, Path: e.path, Model: name, ContentType: ctype}
	for _, k := range model.ProxyHeaders {
		if v := r.Header.Get(k); v != "" {
			if req.Header == nil {
				req.Header = map[string]string{}
			}
			req.Header[k] = v
		}
	}
	if keys := model.ProxyQuery[api]; len(keys) > 0 {
		in, out := r.URL.Query(), url.Values{}
		for _, k := range keys {
			if v, ok := in[k]; ok {
				out[k] = v
			}
		}
		req.Query = out.Encode()
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
	rc        *mesh.RouteCall // the route the call is on, counted
	sent      int             // bytes of a streamed body sent
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
	rc := x.s.c.Mesh.Routes().Begin("", rt.ID, x.req.Model)
	rc.AddReq(len(req.Body))
	rc.Attach(st, false)
	x.mu.Lock()
	x.st = st
	x.rc = rc
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
	x.endRouteLocked(true)
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
		h.Set("X-Fit-Node", head.Node)
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
	rc := x.rc
	x.rc = nil
	x.mu.Unlock()
	defer func() {
		if rc != nil {
			rc.End(failed)
		}
	}()
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
	x.endRouteLocked(true)
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

// endRouteLocked ends the count of the call on its route, once. x.mu is
// held.
func (x *call) endRouteLocked(failed bool) {
	if x.rc != nil {
		x.rc.End(failed)
		x.rc = nil
	}
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
