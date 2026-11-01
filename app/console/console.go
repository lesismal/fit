// Package console serves a role's web pages, on the listeners' fib engine:
// the role's files, and its JSON API behind logins that work as
// admin.console's do, through package login. The agent's and the tunnel's
// roles each start one where their console member says.
//
// Requests are served on fib's workers, and nothing here blocks one: a
// password is checked, and an API handler's blocking work done, on the
// console's own bounded pool, with the request retained until it answers
// (rules 1 and 4). Bodies are JSON, held whole up to MaxBodyBytes (rule 7).
package console

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net"
	stdhttp "net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
	"github.com/lesismal/fit/logs"
)

// The limits of a console's requests and of its pool.
const (
	MaxBodyBytes = 1 << 20
	MaxHeader    = 16 << 10
	// Workers is how many blocking tasks a console runs at once, and Queue
	// how many more wait; a task past them is answered 503.
	Workers = 4
	Queue   = 64
	// TaskTimeout bounds what a task may hold its request for: the
	// request is answered 504 past it, whatever the task does after.
	TaskTimeout = 30 * time.Second
)

// CSRFHeader carries a session's CSRF token on every request that is not a
// GET or a HEAD.
const CSRFHeader = "X-Fit-Csrf"

// csp keeps the pages to their own files and origin.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Options say what a console serves.
type Options struct {
	// Config is the role's console member.
	Config *config.RoleConsole
	// Cookie names the session's cookie, as fit_tunnel_hub.
	Cookie string
	// Pages are the role's files: index.html, and what it loads, served
	// as they are, from memory.
	Pages fs.FS
	// Log says who logged in, who failed to, and what each user changed.
	Log *logs.Logger
	// Routes adds the role's API to the console, under /api/.
	Routes func(api *API)
}

// Console is a role's pages, served on an engine of fit's.
type Console struct {
	engine  *fib.Engine
	index   int
	addr    net.Addr
	secure  bool
	cookie  string
	logins  *login.Logins
	log     *logs.Logger
	files   map[string]file
	routes  map[string]Handler // by "METHOD /api/path"
	tasks   chan func()
	limit   *limitHandler
	stop    chan struct{}
	workers sync.WaitGroup
	closing sync.Once
}

type file struct {
	data        []byte
	contentType string
}

// Request is an API request of a logged-in user.
type Request struct {
	*fibhttp.Context
	// User is who made it.
	User string
	c    *Console
}

// Handler answers an API request, on a fib worker: it must not block. Work
// that blocks goes to Request.Go.
type Handler func(r *Request)

// API is where a role adds its endpoints.
type API struct{ c *Console }

// Handle adds the endpoint method path, as "POST" "/api/listeners", which
// only logged-in users reach, with the session's CSRF token on anything but
// a GET or a HEAD.
func (a *API) Handle(method, path string, h Handler) {
	a.c.routes[method+" "+path] = h
}

// Start serves the console o describes on engine, as a binding of its own,
// with its own bound on connections.
func Start(engine *fib.Engine, o Options) (*Console, error) {
	cfg := o.Config
	logins, err := login.New(login.OptionsOf(cfg.Console()))
	if err != nil {
		return nil, err
	}
	c := &Console{engine: engine, index: -1, cookie: o.Cookie, logins: logins, log: o.Log,
		files: map[string]file{}, routes: map[string]Handler{}, tasks: make(chan func(), Queue), stop: make(chan struct{})}
	if o.Pages != nil {
		if err := c.loadFiles(o.Pages); err != nil {
			return nil, err
		}
	}
	if o.Routes != nil {
		o.Routes(&API{c})
	}
	var h fib.Handler = fibhttp.NewHandlerWithConfig(fibhttp.Config{
		MaxHeaderBytes:    MaxHeader,
		MaxBodyBytes:      MaxBodyBytes,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		WriteTimeout:      30 * time.Second,
	}, fibhttp.HandlerFunc(c.serve))
	if cfg.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("console: %w", err)
		}
		c.secure = true
		h = fibtls.NewServer(fibhttp.ConfigureTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}), h)
	}
	c.limit = newLimitHandler(h, cfg.Console().Conns())
	if c.index, err = engine.AddBinding(fib.Binding{Addr: cfg.Address, Handler: c.limit}); err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	if addrs, err := engine.BindingAddrs(c.index); err == nil && len(addrs) > 0 {
		c.addr = addrs[0]
	}
	for range Workers {
		c.workers.Go(c.work)
	}
	return c, nil
}

// Addr returns where the console listens.
func (c *Console) Addr() net.Addr { return c.addr }

// URL returns the console's address as a URL.
func (c *Console) URL() string {
	scheme := "http"
	if c.secure {
		scheme = "https"
	}
	if c.addr == nil {
		return ""
	}
	return scheme + "://" + c.addr.String() + "/"
}

// Stop stops taking connections, closes those open, and waits for the
// tasks under way, until ctx ends at most.
func (c *Console) Stop(ctx context.Context) {
	c.closing.Do(func() {
		if c.index >= 0 {
			_ = c.engine.RemoveBinding(c.index)
		}
		c.limit.closeAll()
		close(c.stop)
	})
	done := make(chan struct{})
	go func() {
		c.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// work runs tasks until the console stops.
func (c *Console) work() {
	for {
		select {
		case <-c.stop:
			return
		case t := <-c.tasks:
			t()
		}
	}
}

// Go runs fn on the console's pool, off the worker, with the request
// retained until fn returns; fn answers the request. A full pool answers
// 503 at once, and a task still waiting for a worker after TaskTimeout is
// dropped, its request answered 504.
func (r *Request) Go(fn func()) {
	run := r.Context.Async(fn)
	var once atomic.Bool
	task := func() {
		if once.CompareAndSwap(false, true) {
			run()
		}
	}
	select {
	case r.c.tasks <- task:
	default:
		once.Store(true)
		run = nil
		r.Context.Release()
		_ = Error(r.Context, stdhttp.StatusServiceUnavailable, "the console is busy; try again")
		return
	}
	_ = time.AfterFunc(TaskTimeout, func() {
		if once.CompareAndSwap(false, true) {
			_ = Error(r.Context, stdhttp.StatusGatewayTimeout, "the request took too long")
			r.Context.Release()
		}
	})
}

// Decode decodes the request's JSON body into v, answering 400 or 415 and
// returning false when it cannot.
func (r *Request) Decode(v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Request.Header.Get("Content-Type")); mt != "application/json" {
		_ = Error(r.Context, stdhttp.StatusUnsupportedMediaType, "the body must be sent as application/json")
		return false
	}
	if err := json.Unmarshal(r.Context.Body(), v); err != nil {
		_ = Error(r.Context, stdhttp.StatusBadRequest, "the body is not the JSON expected: "+err.Error())
		return false
	}
	return true
}

// Logf logs a message of the user's, as "console: %q: ...".
func (r *Request) Logf(format string, args ...any) {
	if r.c.log != nil {
		r.c.log.Infof("console: %q: %s", r.User, fmt.Sprintf(format, args...))
	}
}

// Error answers status with {"error": msg}.
func Error(c *fibhttp.Context, status int, msg string) error {
	return WriteJSON(c, status, map[string]string{"error": msg})
}

// WriteJSON answers status with v as JSON, and the headers c.Header() was
// given: the console's own, and any the handler added.
func WriteJSON(c *fibhttp.Context, status int, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return Write(c, status, "application/json", data)
}

// Write answers status with body, of contentType, and the headers
// c.Header() was given.
func Write(c *fibhttp.Context, status int, contentType string, body []byte) error {
	h := c.Header().Clone()
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return c.WriteResponse(fibhttp.Response{StatusCode: status, Header: h, Body: body})
}

// JSON answers status with v as JSON, with the console's headers.
func (r *Request) JSON(status int, v any) error { return WriteJSON(r.Context, status, v) }

// serve answers every request.
func (c *Console) serve(ctx *fibhttp.Context) {
	h := ctx.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	if c.secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	r := ctx.Request
	if o := r.Header.Get("Origin"); o != "" && o != c.origin(r) {
		_ = Error(ctx, stdhttp.StatusForbidden, "requests from other web pages are refused")
		return
	}
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/") {
		h.Set("Cache-Control", "no-cache")
		c.serveFile(ctx, p)
		return
	}
	h.Set("Cache-Control", "no-store")
	switch r.Method + " " + p {
	case "POST /api/login":
		c.login(ctx)
		return
	case "POST /api/logout":
		c.logout(ctx)
		return
	}
	s := c.lookup(r)
	if s == nil {
		_ = Error(ctx, stdhttp.StatusUnauthorized, "log in first")
		return
	}
	if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead && !login.CSRFOK(*s, r.Header.Get(CSRFHeader)) {
		_ = Error(ctx, stdhttp.StatusForbidden, "the request lacks the session's "+CSRFHeader+" token")
		return
	}
	if r.Method == stdhttp.MethodGet && p == "/api/session" {
		_ = WriteJSON(ctx, stdhttp.StatusOK, sessionBody{User: s.User, CSRF: s.CSRF})
		return
	}
	hd := c.routes[r.Method+" "+p]
	if hd == nil {
		_ = Error(ctx, stdhttp.StatusNotFound, "no such endpoint")
		return
	}
	hd(&Request{Context: ctx, User: s.User, c: c})
}

func (c *Console) origin(r *stdhttp.Request) string {
	if c.secure {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

func (c *Console) lookup(r *stdhttp.Request) *login.Session {
	ck, err := r.Cookie(c.cookie)
	if err != nil {
		return nil
	}
	s, ok := c.logins.Lookup(ck.Value)
	if !ok {
		return nil
	}
	return &s
}

type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionBody struct {
	User string `json:"user"`
	CSRF string `json:"csrf"`
}

func (c *Console) login(ctx *fibhttp.Context) {
	if mt, _, _ := mime.ParseMediaType(ctx.Request.Header.Get("Content-Type")); mt != "application/json" {
		_ = Error(ctx, stdhttp.StatusUnsupportedMediaType, "the body must be sent as application/json")
		return
	}
	var body loginBody
	if json.Unmarshal(ctx.Body(), &body) != nil {
		_ = Error(ctx, stdhttp.StatusBadRequest, "the body must be {\"username\", \"password\"}")
		return
	}
	addr := clientAddr(ctx.Request)
	if wait := c.logins.LockedOut(addr); wait > 0 {
		ctx.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		_ = Error(ctx, stdhttp.StatusTooManyRequests, "too many failed logins from this address; try again later")
		return
	}
	// bcrypt takes its time on the pool, not on the worker.
	(&Request{Context: ctx, c: c}).Go(func() {
		ok, err := c.logins.Check(ctx.Context(), body.Username, body.Password)
		if err != nil {
			return // the client left
		}
		if !ok {
			c.logins.Failed(addr)
			if c.log != nil {
				c.log.Warnf("console: a failed login as %q from %s", body.Username, addr)
			}
			_ = Error(ctx, stdhttp.StatusUnauthorized, "the user name or password is wrong")
			return
		}
		token, s := c.logins.Start(body.Username)
		ctx.Header().Add("Set-Cookie", (&stdhttp.Cookie{Name: c.cookie, Value: token, Path: "/", HttpOnly: true, Secure: c.secure, SameSite: stdhttp.SameSiteStrictMode}).String())
		if c.log != nil {
			c.log.Infof("console: %q logged in from %s", body.Username, addr)
		}
		_ = WriteJSON(ctx, stdhttp.StatusOK, sessionBody{User: s.User, CSRF: s.CSRF})
	})
}

func (c *Console) logout(ctx *fibhttp.Context) {
	if ck, err := ctx.Request.Cookie(c.cookie); err == nil {
		if s, ok := c.logins.End(ck.Value); ok && c.log != nil {
			c.log.Infof("console: %q logged out", s.User)
		}
	}
	ctx.Header().Add("Set-Cookie", (&stdhttp.Cookie{Name: c.cookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: c.secure, SameSite: stdhttp.SameSiteStrictMode}).String())
	_ = Write(ctx, stdhttp.StatusNoContent, "", nil)
}

func clientAddr(r *stdhttp.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loadFiles reads every file of pages into memory, so that serving one
// reads nothing from a disk.
func (c *Console) loadFiles(pages fs.FS) error {
	return fs.WalkDir(pages, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(pages, p)
		if err != nil {
			return err
		}
		ct := mime.TypeByExtension(path.Ext(p))
		if ct == "" {
			ct = "application/octet-stream"
		}
		c.files["/"+p] = file{data: data, contentType: ct}
		return nil
	})
}

func (c *Console) serveFile(ctx *fibhttp.Context, p string) {
	r := ctx.Request
	if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead {
		ctx.Header().Set("Allow", "GET, HEAD")
		_ = Error(ctx, stdhttp.StatusMethodNotAllowed, "the pages are only read")
		return
	}
	if strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	f, ok := c.files[p]
	if !ok {
		_ = Write(ctx, stdhttp.StatusNotFound, "text/plain; charset=utf-8", []byte("404 page not found\n"))
		return
	}
	_ = Write(ctx, stdhttp.StatusOK, f.contentType, f.data)
}

// limitHandler closes the connections past max as they open, and closes
// every one it has open when the console stops.
type limitHandler struct {
	fib.Handler
	max int64
	n   atomic.Int64

	mu     sync.Mutex
	conns  map[*fib.Connection]struct{}
	closed bool
}

func newLimitHandler(h fib.Handler, max int) *limitHandler {
	return &limitHandler{Handler: h, max: int64(max), conns: map[*fib.Connection]struct{}{}}
}

func (l *limitHandler) OnOpen(c *fib.Connection) {
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

func (l *limitHandler) OnClose(c *fib.Connection, err error) {
	l.mu.Lock()
	_, ok := l.conns[c]
	delete(l.conns, c)
	l.mu.Unlock()
	if ok {
		l.n.Add(-1)
		l.Handler.OnClose(c, err)
	}
}

func (l *limitHandler) OnShutdown(c *fib.Connection) {
	if h, ok := l.Handler.(fib.ShutdownHandler); ok {
		h.OnShutdown(c)
	}
}

func (l *limitHandler) OnEngineClose(c *fib.Connection) {
	if h, ok := l.Handler.(fib.EngineCloseHandler); ok {
		h.OnEngineClose(c)
	}
}

func (l *limitHandler) closeAll() {
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
