// Package webui is what the pages of the LLM feature's roles share: the shared
// frontend (ui.css, ui.js, peers.js, jobs.js), served under /static/ beside
// each role's own index.html and app.js, and the API that runs the role's
// mesh methods for its pages, on app/console behind its logins:
//
//   - GET /api/me: the user, the role's identity and, for a proxy, its API's
//     URL;
//   - POST /api/rpc<method>: a method, answered with its result as JSON, or
//     {"error", "code"} with the HTTP status of the code;
//   - POST /api/stream<method>: a stream method, answered with Server-Sent
//     Events: a data event per frame, "end" with the result, "error" with
//     {code, message}.
//
// A method runs on the role's pool, never on a fib worker, with the request
// retained until it answers; a stream's events are written with WriteNoWait
// and its frames released back to the stream only as the browser takes them,
// so that a slow browser holds back its own stream and nothing piles up.
package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	stdhttp "net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/pool"
	"github.com/lesismal/fit/llm/internal/wire"
)

//go:embed static
var staticFS embed.FS

// Pages returns a role's pages: its own files at the root, and the shared
// ones under static/.
func Pages(role fs.FS) fs.FS {
	shared, _ := fs.Sub(staticFS, "static")
	return pages{role: role, shared: shared}
}

type pages struct{ role, shared fs.FS }

func (p pages) Open(name string) (fs.File, error) {
	if name == "static" {
		return p.shared.Open(".")
	}
	if rest, ok := strings.CutPrefix(name, "static/"); ok {
		return p.shared.Open(rest)
	}
	return p.role.Open(name)
}

func (p pages) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == "static" {
		return fs.ReadDir(p.shared, ".")
	}
	if rest, ok := strings.CutPrefix(name, "static/"); ok {
		return fs.ReadDir(p.shared, rest)
	}
	entries, err := fs.ReadDir(p.role, name)
	if err == nil && name == "." {
		if d, err := fs.Stat(p.shared, "."); err == nil {
			entries = append(entries, fs.FileInfoToDirEntry(staticDir{d}))
		}
	}
	return entries, err
}

type staticDir struct{ fs.FileInfo }

func (staticDir) Name() string { return "static" }

// The bounds of the pages' API.
const (
	// MaxStreams is how many streams a role's pages follow at once.
	MaxStreams = 16
	// StallTimeout bounds each wait for a browser to take a stream's events.
	StallTimeout = 30 * time.Second
)

// Options are what Register serves.
type Options struct {
	Router *mesh.Router
	// Pool runs the methods.
	Pool *pool.Pool
	// Me adds to GET /api/me's answer, besides "user".
	Me func() map[string]any
}

var local = model.Identity{Role: model.RoleLocal, ID: "console"}

// Register adds the API to a role's console. Call it from console.Options.Routes,
// once the router has every method.
func Register(api *console.API, o Options) {
	api.Handle("GET", "/api/me", func(r *console.Request) {
		body := map[string]any{}
		if o.Me != nil {
			body = o.Me()
		}
		body["user"] = r.User
		_ = r.JSON(stdhttp.StatusOK, body)
	})
	for _, rt := range o.Router.Routes() {
		method := rt.Method
		api.Handle("POST", "/api/rpc"+method, func(r *console.Request) { rpc(r, o, method) })
	}
	var streams atomic.Int32
	for _, rt := range o.Router.StreamRoutes() {
		method := rt.Method
		api.Handle("POST", "/api/stream"+method, func(r *console.Request) { stream(r, o, method, &streams) })
	}
}

// StatusOf maps a business code to an HTTP status.
func StatusOf(code int) int {
	switch code {
	case model.CodeOK:
		return stdhttp.StatusOK
	case model.CodeBadRequest:
		return stdhttp.StatusBadRequest
	case model.CodeUnauthorized:
		return stdhttp.StatusUnauthorized
	case model.CodeForbidden:
		return stdhttp.StatusForbidden
	case model.CodeNotFound:
		return stdhttp.StatusNotFound
	case model.CodeConflict:
		return stdhttp.StatusConflict
	case model.CodeRateLimited:
		return stdhttp.StatusTooManyRequests
	case model.CodeUnavailable:
		return stdhttp.StatusServiceUnavailable
	default:
		return stdhttp.StatusInternalServerError
	}
}

type errorBody struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

// Fail answers err as {"error", "code"}.
func Fail(c *fibhttp.Context, err error) {
	code, msg := mesh.ErrCode(err)
	status := StatusOf(code)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		status, msg = stdhttp.StatusGatewayTimeout, "timeout"
	case errors.Is(err, pool.ErrBusy):
		status, code = stdhttp.StatusServiceUnavailable, model.CodeUnavailable
	}
	_ = console.WriteJSON(c, status, errorBody{Error: msg, Code: code})
}

// body returns the request's JSON, or nil for none.
func body(r *console.Request) (json.RawMessage, bool) {
	b := r.Context.Body()
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, true
	}
	if !json.Valid(b) {
		_ = console.WriteJSON(r.Context, stdhttp.StatusBadRequest, errorBody{Error: "the body is not JSON", Code: model.CodeBadRequest})
		return nil, false
	}
	return json.RawMessage(append([]byte(nil), b...)), true
}

// rpc runs method on the role's pool, the request retained until it answers.
func rpc(r *console.Request, o Options, method string) {
	raw, ok := body(r)
	if !ok {
		return
	}
	c := r.Context
	ctx := c.Context()
	if rt, ok := o.Router.Lookup(method); ok {
		// The answer may come after the console's write timeout, as a
		// hub's call to a node's method does: its own timeout bounds it.
		_ = c.SetWriteDeadline(time.Now().Add(rt.Timeout + 10*time.Second))
	}
	var refused atomic.Bool
	run := c.Async(func() {
		if refused.Load() {
			return
		}
		data, err := o.Router.Invoke(ctx, local, method, raw)
		if err != nil {
			Fail(c, err)
			return
		}
		if method != model.MethodPeers && method != model.MethodStatus && !strings.HasSuffix(method, "/list") && !strings.HasSuffix(method, "/get") {
			r.Logf("%s", method)
		}
		_ = console.WriteJSON(c, stdhttp.StatusOK, data)
	})
	if err := o.Pool.Go(run); err != nil {
		refused.Store(true)
		Fail(c, err)
		run()
	}
}

// stream opens method and writes its frames as Server-Sent Events.
func stream(r *console.Request, o Options, method string, n *atomic.Int32) {
	raw, ok := body(r)
	if !ok {
		return
	}
	if n.Add(1) > MaxStreams {
		n.Add(-1)
		Fail(r.Context, mesh.Errorf(model.CodeRateLimited, "too many streams open; close one"))
		return
	}
	c := r.Context
	c.Retain()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := o.Router.OpenLocal(ctx, local, method, raw)
	if err != nil {
		cancel()
		n.Add(-1)
		Fail(c, err)
		c.Release()
		return
	}
	s := &sse{c: c, st: st, done: func() { cancel(); n.Add(-1) }}
	c.OnCancel(func(error) { st.Cancel() })
	st.OnData(s.frame)
}

// sse writes one stream's frames to the browser.
type sse struct {
	c    *fibhttp.Context
	st   *wire.Stream
	done func()

	mu      sync.Mutex
	started bool
	ended   bool
	held    int // bytes of frames not released, waiting for the browser
	waiting bool
}

func (s *sse) frame(data []byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		if data != nil {
			s.st.Release(len(data))
		}
		return
	}
	if err != nil {
		s.finish(err)
		return
	}
	n := len(data)
	if n == 0 {
		data = []byte("null")
	}
	s.start()
	above, werr := s.c.WriteNoWait(event("", data))
	if werr == nil {
		werr = s.c.FlushError()
	}
	if werr != nil {
		s.st.Cancel()
		return
	}
	if !above && !s.waiting {
		s.st.Release(n)
		return
	}
	s.held += n
	if s.waiting {
		return
	}
	s.waiting = true
	_ = s.c.SetWriteDeadline(time.Now().Add(StallTimeout))
	if !s.c.OnWritable(s.writable) {
		s.release()
	}
}

func (s *sse) writable(err error) {
	if err != nil {
		// Unlocked: the end it brings comes back through frame.
		s.st.Cancel()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.c.SetWriteDeadline(time.Time{})
	s.release()
}

// release gives the stream back the bytes held. s.mu is held.
func (s *sse) release() {
	s.waiting = false
	if n := s.held; n > 0 {
		s.held = 0
		s.st.Release(n)
	}
}

func (s *sse) start() {
	if s.started {
		return
	}
	s.started = true
	// A stream lasts as long as its method: the console's write timeout,
	// meant for whole responses, is lifted, and each wait for the browser
	// is bounded by StallTimeout instead.
	_ = s.c.SetWriteDeadline(time.Time{})
	h := s.c.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("X-Accel-Buffering", "no")
	s.c.WriteHeader(stdhttp.StatusOK)
}

// finish ends the response as the stream ended. s.mu is held.
func (s *sse) finish(err error) {
	s.ended = true
	defer s.done()
	defer s.c.Release()
	if errors.Is(err, io.EOF) {
		res := s.st.Result()
		if len(strings.TrimSpace(string(res))) == 0 {
			res = []byte("null")
		}
		s.start()
		_, _ = s.c.WriteNoWait(event("end", res))
		_ = s.c.SetWriteDeadline(time.Now().Add(StallTimeout))
		_ = s.c.Finish()
		return
	}
	if !s.started {
		Fail(s.c, err)
		return
	}
	code, msg := mesh.ErrCode(err)
	b, _ := json.Marshal(map[string]any{"code": code, "message": msg})
	_, _ = s.c.WriteNoWait(event("error", b))
	_ = s.c.SetWriteDeadline(time.Now().Add(StallTimeout))
	_ = s.c.Finish()
}

// event is one Server-Sent Event.
func event(name string, data []byte) []byte {
	var b []byte
	if name != "" {
		b = append(b, "event: "+name+"\n"...)
	}
	for _, line := range strings.Split(string(data), "\n") {
		b = append(b, "data: "...)
		b = append(b, strings.TrimSuffix(line, "\r")...)
		b = append(b, '\n')
	}
	return append(b, '\n')
}
