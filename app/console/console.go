// Package console serves a role's web pages, on the listeners' fib engine:
// the workbench's files, and the JSON API behind logins that work as
// admin.console's do, through package login. The llm and tunnel roles each
// start one where their console member says, and admin.console starts one
// for every role of the process.
//
// A console's API is its own routes, under /api/, and its modules', each
// under /api/m/<module>/: a module is a role's API, which the workbench's
// pages call there whichever console serves them, so that one console may
// show every role that runs beside it.
//
// Requests are served on fib's workers, and nothing here blocks one: a
// login's password is checked on a small bounded pool of its own, and an
// API handler's blocking work done on the console's bounded pool, with the request retained until it answers
// (rules 1 and 4). Bodies are JSON, held whole up to MaxBodyBytes (rule 7),
// but for those Options.StreamBody picks, which stream (rule 5).
package console

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"mime"
	"net"
	stdhttp "net/http"
	"net/netip"
	"path"
	"slices"
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
	// LoginWorkers is how many logins are checked at once, and LoginQueue
	// how many more wait, on a pool of their own: logins, which anyone may
	// send, never take the workers of logged-in users' tasks. LoginWorkers
	// stays below login.Checks, so that a logged-in user's password check
	// always finds a slot.
	LoginWorkers = 1
	LoginQueue   = 16
)

// How long a console keeps a connection. One that has not sent a request
// with a valid session (a login that succeeds counts) is closed
// UnauthIdleTimeout after it was opened, whatever it sends meanwhile, as the
// Admin API closes an unauthenticated one, so that clients without a login
// cannot hold the console's few connections for long. One that has is kept
// for IdleTimeout between requests, until it logs out or makes an API
// request without a valid session, its own having ended; it is then closed
// UnauthIdleTimeout later unless a valid session comes first.
const (
	IdleTimeout       = 2 * time.Minute
	UnauthIdleTimeout = 5 * time.Second
)

// idleTimeout and unauthIdleTimeout are IdleTimeout and UnauthIdleTimeout,
// which tests shorten; Start reads them.
var idleTimeout, unauthIdleTimeout = IdleTimeout, UnauthIdleTimeout

// MaxLoginBytes bounds a login's body, whatever a console's MaxBody.
const MaxLoginBytes = 4 << 10

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
	// Cookie names the session's cookie, as fit_tunnel_hub; the console's
	// port is added to it, as fit_tunnel_hub_7100.
	Cookie string
	// Pages are the role's files: index.html, and what it loads, served
	// as they are, from memory.
	Pages fs.FS
	// Log says who logged in, who failed to, and what each user changed.
	Log *logs.Logger
	// Logf, when Log is nil, is told what Log would be.
	Logf func(format string, args ...any)
	// Routes adds the role's API to the console, under /api/.
	Routes func(api *API)
	// Modules, when not nil, returns the modules the console serves,
	// each under /api/m/<ID>/. It is called for each request to a module,
	// and for GET /api/workbench, on a fib worker: it must not block.
	Modules func() []*Module
	// MaxBody bounds a request's body; MaxBodyBytes when 0.
	MaxBody int64
	// Endpoints are paths outside /api/, as /mcp, each served by its
	// handler for every method, before the files and without a session or
	// a CSRF token: the handler checks the request's own credentials, and
	// calls Request.Authenticated with what it found, so that the
	// connection is kept as a logged-in one is, or not. A request from
	// another web page's Origin is refused before it, as every request is.
	Endpoints map[string]Handler
	// PasswordRoutes are the endpoints, as "POST /api/m/admin/account/password",
	// that a session whose password is temporary (login.Session.MustChange)
	// may call beside GET /api/session, GET /api/workbench and the logout;
	// every other answers 403 until the password changes.
	PasswordRoutes []string
	// Sources, when not nil, returns origins beyond the console's own, as
	// "http://192.168.1.5:9401", whose images and media the pages may load
	// and fetch: the media routes a preview plays from. When it returns
	// any, a response's Content-Security-Policy adds them to img-src and
	// connect-src, and adds media-src 'self' blob: and them. It is called
	// for every response, on a fib worker: it must not block, and what it
	// returns must be origins alone, which the policy holds as they are.
	Sources func(r *stdhttp.Request) []string
	// StreamBody, when not nil, picks the requests whose bodies stream to
	// their handler as they arrive, rather than being held whole up to
	// MaxBody: it is called on a fib worker with the request as its header
	// made it, and must not block. Such a handler takes the body with
	// Context.OnBody, bounds it with Context.LimitBody, and may lift the
	// console's read timeout for it with Context.SetReadDeadline. A request
	// that is not let in is answered at once, and its connection closed
	// rather than its body read.
	StreamBody func(r *stdhttp.Request) bool
}

// MaxStreamedBody bounds a body StreamBody picks, whatever its handler's
// LimitBody: 1 TiB.
const MaxStreamedBody = 1 << 40

// Module is an API a console serves under /api/m/<ID>/: a role's, made once
// for the role and kept, since its routes are added on its first request.
type Module struct {
	// ID names the module, as the role's member of the config is named:
	// llm.hub, tunnel.proxy, or admin for the config's own.
	ID string
	// Routes adds the module's endpoints, as Options.Routes does: an
	// endpoint the module adds as /api/x is served at /api/m/<ID>/x.
	Routes func(api *API)

	once  sync.Once
	table routeTable
}

// routes returns the module's endpoints, adding them on the first call.
func (m *Module) routes() *routeTable {
	m.once.Do(func() {
		m.table.exact = map[string]Handler{}
		if m.Routes != nil {
			m.Routes(&API{t: &m.table})
		}
	})
	return &m.table
}

// routeTable is a set of endpoints: exact ones by "METHOD /api/path", then
// prefixes, the longest first.
type routeTable struct {
	exact    map[string]Handler
	prefixes []prefixRoute
}

type prefixRoute struct {
	method, prefix string
	h              Handler
}

func (t *routeTable) lookup(method, p string) Handler {
	if h := t.exact[method+" "+p]; h != nil {
		return h
	}
	for _, pr := range t.prefixes {
		if (pr.method == "" || pr.method == method) && strings.HasPrefix(p, pr.prefix) {
			return pr.h
		}
	}
	return nil
}

// Console is a role's pages, served on an engine of fit's.
type Console struct {
	engine  *fib.Engine
	index   int
	addr    net.Addr
	secure  bool
	cookie  string
	logins  *login.Logins
	infof   func(format string, args ...any)
	warnf   func(format string, args ...any)
	files   map[string]file
	routes  routeTable
	modules func() []*Module
	pwOnly  []string           // Options.PasswordRoutes
	ends    map[string]Handler // Options.Endpoints
	// sources is Options.Sources.
	sources func(r *stdhttp.Request) []string
	tasks   chan func()
	checks  chan func() // the login checks' queue, on LoginWorkers of their own
	limit   *limitHandler
	stop    chan struct{}
	workers sync.WaitGroup
	closing sync.Once
	// idle and unauthIdle are how long a connection is kept between
	// requests once it has sent a valid session, and before then.
	idle, unauthIdle time.Duration
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
	// Path is the request's path as its endpoint was added: under /api/,
	// without a module's /api/m/<ID> before it.
	Path string
	c    *Console
}

// Handler answers an API request, on a fib worker: it must not block. Work
// that blocks goes to Request.Go.
type Handler func(r *Request)

// API is where a role adds its endpoints.
type API struct{ t *routeTable }

// Handle adds the endpoint method path, as "POST" "/api/listeners", which
// only logged-in users reach, with the session's CSRF token on anything but
// a GET or a HEAD.
func (a *API) Handle(method, path string, h Handler) {
	a.t.exact[method+" "+path] = h
}

// HandlePrefix adds an endpoint for every path under prefix, as "/api/x/",
// of method, or of any method when method is "", that no endpoint Handle
// added matches; the longest prefix wins.
func (a *API) HandlePrefix(method, prefix string, h Handler) {
	a.t.prefixes = append(a.t.prefixes, prefixRoute{method: method, prefix: prefix, h: h})
	slices.SortStableFunc(a.t.prefixes, func(x, y prefixRoute) int { return len(y.prefix) - len(x.prefix) })
}

// Start serves the console o describes on engine, as a binding of its own,
// with its own bound on connections.
func Start(engine *fib.Engine, o Options) (*Console, error) {
	cfg := o.Config
	logins := login.New(login.OptionsOf(cfg.Console()))
	c := &Console{engine: engine, index: -1, cookie: o.Cookie, logins: logins, modules: o.Modules, pwOnly: o.PasswordRoutes, ends: o.Endpoints, sources: o.Sources,
		idle: idleTimeout, unauthIdle: unauthIdleTimeout,
		files: map[string]file{}, routes: routeTable{exact: map[string]Handler{}}, tasks: make(chan func(), Queue), checks: make(chan func(), LoginQueue), stop: make(chan struct{})}
	switch {
	case o.Log != nil:
		c.infof, c.warnf = o.Log.Infof, o.Log.Warnf
	case o.Logf != nil:
		c.infof, c.warnf = o.Logf, o.Logf
	default:
		c.infof = func(string, ...any) {}
		c.warnf = c.infof
	}
	if o.Pages != nil {
		if err := c.loadFiles(o.Pages); err != nil {
			return nil, err
		}
	}
	if o.Routes != nil {
		o.Routes(&API{t: &c.routes})
	}
	maxBody := o.MaxBody
	if maxBody <= 0 {
		maxBody = MaxBodyBytes
	}
	var h fib.Handler = fibhttp.NewHandlerWithConfig(fibhttp.Config{
		MaxHeaderBytes:    MaxHeader,
		MaxBodyBytes:      maxBody,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Every connection starts out unauthenticated, closed c.unauthIdle
		// after it opens (limitHandler); a request with a valid session
		// gives its connection c.idle (see authenticated).
		IdleTimeout:  c.unauthIdle,
		WriteTimeout: 30 * time.Second,
		// Only the bodies StreamBody picks stream; the others are held
		// whole, up to maxBody, as before.
		StreamRequestBody:          o.StreamBody != nil,
		StreamRequestBodyThreshold: math.MaxInt64,
		StreamRequestBodyAtHeader:  o.StreamBody,
		MaxStreamedBodyBytes:       MaxStreamedBody,
	}, fibhttp.HandlerFunc(c.serve))
	if cfg.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("console: %w", err)
		}
		c.secure = true
		h = fibtls.NewServer(fibhttp.ConfigureTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}), h)
	}
	c.limit = newLimitHandler(h, cfg.Console().Conns(), c.unauthIdle)
	// mptcp is the pages member's own, on a role's console and on
	// admin.console alike: the login behind the binding is the same either
	// way, only the sockets the kernel opens differ.
	var err error
	if c.index, err = engine.AddBinding(fib.Binding{Addr: cfg.Address, Handler: c.limit, MultipathTCP: cfg.MPTCP}); err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	if addrs, err := engine.BindingAddrs(c.index); err == nil && len(addrs) > 0 {
		c.addr = addrs[0]
	}
	c.cookie = login.CookieName(o.Cookie, c.addr)
	for range Workers {
		c.workers.Go(func() { c.work(c.tasks) })
	}
	for range LoginWorkers {
		c.workers.Go(func() { c.work(c.checks) })
	}
	return c, nil
}

// Logins returns the console's logins.
func (c *Console) Logins() *login.Logins { return c.logins }

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

// work runs the tasks of queue until the console stops.
func (c *Console) work(queue chan func()) {
	for {
		select {
		case <-c.stop:
			return
		case t := <-queue:
			t()
		}
	}
}

// Go runs fn on the console's pool, off the worker, with the request
// retained until fn returns; fn answers the request. A full pool answers
// 503 at once, and a task still waiting for a worker after TaskTimeout is
// dropped, its request answered 504.
func (r *Request) Go(fn func()) { r.GoThen(fn, nil) }

// GoThen runs fn as Go does, and then, when not nil, once fn has returned
// or, when the pool drops fn, in its place: what a task must undo whatever
// comes of it. then must not block; it may run on a fib worker.
func (r *Request) GoThen(fn, then func()) { r.goOn(r.c.tasks, fn, then) }

// goOn runs fn on the pool that takes queue, as GoThen says.
func (r *Request) goOn(queue chan func(), fn, then func()) {
	if then == nil {
		then = func() {}
	}
	run := r.Context.Async(fn)
	var once atomic.Bool
	task := func() {
		if once.CompareAndSwap(false, true) {
			defer then()
			run()
		}
	}
	select {
	case queue <- task:
	default:
		once.Store(true)
		run = nil
		r.Context.Release()
		then()
		_ = Error(r.Context, stdhttp.StatusServiceUnavailable, "the console is busy; try again")
		return
	}
	_ = time.AfterFunc(TaskTimeout, func() {
		if once.CompareAndSwap(false, true) {
			then()
			_ = Error(r.Context, stdhttp.StatusGatewayTimeout, "the request took too long")
			r.Context.Release()
		}
	})
}

// Submit runs fn on the console's pool, as Go does, but neither holds the
// request nor answers it: for the work of a handler that already answers
// from elsewhere, as a stream does, and keeps the request retained itself.
// It reports false, and fn never runs, when the pool's queue is full; fn
// never runs either once the console has stopped, so a caller bounds its
// wait for fn with a timeout of its own.
func (r *Request) Submit(fn func()) bool {
	select {
	case r.c.tasks <- fn:
		return true
	default:
		return false
	}
}

// Authenticated says whether the request of an endpoint (Options.Endpoints)
// came with valid credentials of its own, as a session's would: its
// connection is then kept for the console's idle time between requests,
// and otherwise put back on the rule of connections without a login. Its
// user, when valid, is who made it.
func (r *Request) Authenticated(valid bool, user string) {
	if valid {
		r.User = user
	}
	r.c.authenticated(r.Context, valid)
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
	r.c.infof("console: %q: %s", r.User, fmt.Sprintf(format, args...))
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
	h.Set("Content-Security-Policy", c.policy(ctx.Request))
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
	if hd := c.ends[p]; hd != nil {
		h.Set("Cache-Control", "no-store")
		hd(&Request{Context: ctx, Path: p, c: c})
		return
	}
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
		c.authenticated(ctx, false)
		_ = Error(ctx, stdhttp.StatusUnauthorized, "log in first")
		return
	}
	if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead && !login.CSRFOK(*s, r.Header.Get(CSRFHeader)) {
		_ = Error(ctx, stdhttp.StatusForbidden, "the request lacks the session's "+CSRFHeader+" token")
		return
	}
	c.authenticated(ctx, true)
	if s.MustChange && !c.passwordRoute(r.Method, p) {
		_ = WriteJSON(ctx, stdhttp.StatusForbidden, mustChangeBody{Error: "change the temporary password first", MustChange: true})
		return
	}
	if r.Method == stdhttp.MethodGet {
		switch p {
		case "/api/session":
			_ = WriteJSON(ctx, stdhttp.StatusOK, newSessionBody(s))
			return
		case "/api/workbench":
			_ = WriteJSON(ctx, stdhttp.StatusOK, workbenchBody{User: s.User, Modules: c.moduleIDs()})
			return
		}
	}
	t, rel := &c.routes, p
	if rest, ok := strings.CutPrefix(p, "/api/m/"); ok {
		id, sub, _ := strings.Cut(rest, "/")
		m := c.module(id)
		if m == nil {
			_ = Error(ctx, stdhttp.StatusNotFound, "no module "+strconv.Quote(id)+" runs here")
			return
		}
		t, rel = m.routes(), "/api/"+sub
	}
	hd := t.lookup(r.Method, rel)
	if hd == nil {
		_ = Error(ctx, stdhttp.StatusNotFound, "no such endpoint")
		return
	}
	hd(&Request{Context: ctx, User: s.User, Path: rel, c: c})
}

// policy returns the Content-Security-Policy of the response to r: csp,
// or, with origins from Options.Sources, csp with them added to img-src and
// connect-src, and media-src for them, the console's own and blob:, which
// a player's MediaSource plays from.
func (c *Console) policy(r *stdhttp.Request) string {
	if c.sources == nil {
		return csp
	}
	origins := c.sources(r)
	if len(origins) == 0 {
		return csp
	}
	more := strings.Join(origins, " ")
	return "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: " + more + "; connect-src 'self' " + more +
		"; media-src 'self' blob: " + more + "; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

// workbenchBody is what GET /api/workbench answers: who is logged in, and
// the IDs of the modules served, for the pages' menu.
type workbenchBody struct {
	User    string   `json:"user"`
	Modules []string `json:"modules"`
}

func (c *Console) moduleIDs() []string {
	ids := []string{}
	if c.modules != nil {
		for _, m := range c.modules() {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

func (c *Console) module(id string) *Module {
	if c.modules == nil {
		return nil
	}
	for _, m := range c.modules() {
		if m.ID == id {
			return m
		}
	}
	return nil
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
	// MustChange is a session whose password is temporary, and Expires when
	// that password stops working.
	MustChange bool   `json:"must_change_password,omitempty"`
	Expires    string `json:"password_expires,omitempty"`
}

func newSessionBody(s *login.Session) sessionBody {
	b := sessionBody{User: s.User, CSRF: s.CSRF, MustChange: s.MustChange}
	if s.MustChange {
		b.Expires = s.Expires.UTC().Format(time.RFC3339)
	}
	return b
}

// mustChangeBody is the answer to a request a session with a temporary
// password may not make, and passwordExpiredBody a login with such a
// password past its time.
type mustChangeBody struct {
	Error      string `json:"error"`
	MustChange bool   `json:"must_change_password"`
}

type passwordExpiredBody struct {
	Error   string `json:"error"`
	Expired bool   `json:"password_expired"`
}

// passwordRoute reports whether a session with a temporary password may
// call method and path.
func (c *Console) passwordRoute(method, p string) bool {
	if method == stdhttp.MethodGet && (p == "/api/session" || p == "/api/workbench") {
		return true
	}
	return slices.Contains(c.pwOnly, method+" "+p)
}

func (c *Console) login(ctx *fibhttp.Context) {
	if mt, _, _ := mime.ParseMediaType(ctx.Request.Header.Get("Content-Type")); mt != "application/json" {
		_ = Error(ctx, stdhttp.StatusUnsupportedMediaType, "the body must be sent as application/json")
		return
	}
	if len(ctx.Body()) > MaxLoginBytes {
		_ = Error(ctx, stdhttp.StatusRequestEntityTooLarge, "a login's body is 4 KiB at most")
		return
	}
	var body loginBody
	if json.Unmarshal(ctx.Body(), &body) != nil {
		_ = Error(ctx, stdhttp.StatusBadRequest, "the body must be {\"username\", \"password\"}")
		return
	}
	// The attempt is admitted, one from an address at a time, before it is
	// queued, so that every failure counts before the next guess is checked;
	// and not while the user's checks are slowed.
	addr := clientAddr(ctx.Request)
	t, wait, busy, slowed := c.logins.AdmitUser(addr, body.Username)
	if t == nil {
		refuseLogin(ctx, wait, busy, slowed)
		return
	}
	// bcrypt takes its time on the login pool, not on the worker, nor on
	// the workers of logged-in users' tasks.
	(&Request{Context: ctx, c: c}).goOn(c.checks, func() {
		if wait := t.LockedOut(); wait > 0 {
			refuseLogin(ctx, wait, false, false)
			return
		}
		ok, err := c.logins.Check(ctx.Context(), body.Username, body.Password)
		if err != nil {
			return // the client left
		}
		if ok {
			t.Passed()
		} else {
			t.Done(true)
		}
		if !ok {
			c.warnf("console: a failed login as %q from %s", body.Username, addr)
			_ = Error(ctx, stdhttp.StatusUnauthorized, "the user name or password is wrong")
			return
		}
		if c.logins.Expired(body.Username) {
			c.warnf("console: a login as %q from %s with its temporary password, which has expired", body.Username, addr)
			_ = WriteJSON(ctx, stdhttp.StatusUnauthorized, passwordExpiredBody{Error: "the temporary password of " + strconv.Quote(body.Username) + " has expired without being changed; restart fit with -admin to set a new one in time", Expired: true})
			return
		}
		token, s := c.logins.Start(body.Username)
		c.authenticated(ctx, true)
		ctx.Header().Add("Set-Cookie", (&stdhttp.Cookie{Name: c.cookie, Value: token, Path: "/", HttpOnly: true, Secure: c.secure, SameSite: stdhttp.SameSiteStrictMode}).String())
		c.infof("console: %q logged in from %s", body.Username, addr)
		_ = WriteJSON(ctx, stdhttp.StatusOK, newSessionBody(&s))
	}, func() { t.Done(false) }) // a check that did not run counts nothing
}

// refuseLogin answers 429 for a login that may not be checked for wait,
// since its address is locked out, or, when busy, another login from it is
// being checked, or, when slowed, its user has failed too often in a row.
func refuseLogin(ctx *fibhttp.Context, wait time.Duration, busy, slowed bool) {
	msg := "too many failed logins from this address; try again later"
	switch {
	case busy:
		msg = "another login from this address is being checked; try again"
	case slowed:
		msg = "too many failed logins as this user; try again later"
	}
	Refuse(ctx, wait, msg)
}

// RetryBody is a 429's answer: why, and the seconds to wait before trying
// again, as Retry-After says too.
type RetryBody struct {
	Error      string `json:"error"`
	RetryAfter int    `json:"retry_after"`
}

// Refuse answers 429 with msg, telling the client to try again after wait,
// in Retry-After and in the body's retry_after: its whole seconds and one
// more, so that a client that waits that long finds the wait over.
func Refuse(ctx *fibhttp.Context, wait time.Duration, msg string) {
	secs := int(wait.Seconds()) + 1
	ctx.Header().Set("Retry-After", strconv.Itoa(secs))
	_ = WriteJSON(ctx, stdhttp.StatusTooManyRequests, RetryBody{Error: msg, RetryAfter: secs})
}

func (c *Console) logout(ctx *fibhttp.Context) {
	if ck, err := ctx.Request.Cookie(c.cookie); err == nil {
		if s, ok := c.logins.End(ck.Value); ok {
			c.infof("console: %q logged out", s.User)
		}
	}
	ctx.Header().Add("Set-Cookie", (&stdhttp.Cookie{Name: c.cookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: c.secure, SameSite: stdhttp.SameSiteStrictMode}).String())
	// Logged out, the connection is an unauthenticated one again.
	c.authenticated(ctx, false)
	_ = Write(ctx, stdhttp.StatusNoContent, "", nil)
}

// authenticated records whether the request ctx carries came with a valid
// session. One that did keeps its connection for the console's usual idle
// time from now on. One that did not, on a connection a session had kept,
// whose session has ended since, puts the connection back on the rule for
// unauthenticated ones: kept unauthIdle between requests, and closed
// unauthIdle from now unless a request with a valid session comes first.
func (c *Console) authenticated(ctx *fibhttp.Context, valid bool) {
	cc := c.limit.conn(ctx.Conn)
	if valid {
		_ = ctx.SetIdleTimeout(c.idle)
		if cc != nil {
			// No longer closed unauthIdle after it opened, nor for a newer
			// connection.
			cc.slot.Authenticate()
		}
		return
	}
	if cc != nil && cc.slot.Unauthenticate() {
		_ = ctx.SetIdleTimeout(c.unauthIdle)
		cc.expire(c.unauthIdle)
	}
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

// limitHandler counts the connections it opens (login.Slots): past max, or
// past the share of it unauthenticated connections may hold, a new one
// closes an unauthenticated one and takes its place, and is closed as it
// opens only when every connection has authenticated, or when its client
// already holds its share (login.Peers). It closes one that has not been
// authenticated unauth after it opened, when unauth is above 0, and every
// one it has open when the console stops.
type limitHandler struct {
	fib.Handler
	slots  *login.Slots
	peer   func(*fib.Connection) netip.Addr // the client's address; tests replace it
	unauth time.Duration

	mu     sync.Mutex
	conns  map[*fib.Connection]*consoleConn
	closed bool
}

// consoleConn is what a limitHandler keeps of one of its connections.
type consoleConn struct {
	// slot is the connection's count; it is authenticated while the
	// connection's last request with a session, or its login, had a valid
	// one (Console.authenticated).
	slot *login.Slot
	// timer closes the connection unless it is authenticated by then; nil
	// when the limitHandler's unauth is 0.
	timer *time.Timer
}

// expire closes the connection d from now unless it is authenticated by
// then.
func (cc *consoleConn) expire(d time.Duration) {
	if cc.timer != nil {
		cc.timer.Reset(d)
	}
}

func newLimitHandler(h fib.Handler, max int, unauth time.Duration) *limitHandler {
	return &limitHandler{Handler: h, slots: login.NewSlots(max), conns: map[*fib.Connection]*consoleConn{}, unauth: unauth,
		peer: func(c *fib.Connection) netip.Addr { return c.RemoteAddrPort().Addr() }}
}

// conn returns what l keeps of c, or nil if c is not one of its open
// connections.
func (l *limitHandler) conn(c *fib.Connection) *consoleConn {
	if c == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.conns[c]
}

// open reports how many connections l holds open.
func (l *limitHandler) open() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.conns)
}

func (l *limitHandler) OnOpen(c *fib.Connection) {
	// Not under l.mu: the connection closed for this one may be closed at
	// once.
	slot := l.slots.Open(l.peer(c), func() { _ = c.Close() })
	if slot == nil {
		_ = c.Close()
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		slot.Release()
		_ = c.Close()
		return
	}
	cc := &consoleConn{slot: slot}
	if l.unauth > 0 {
		cc.timer = time.AfterFunc(l.unauth, func() {
			if !slot.Authenticated() {
				_ = c.Close()
			}
		})
	}
	l.conns[c] = cc
	l.mu.Unlock()
	l.Handler.OnOpen(c)
}

func (l *limitHandler) OnClose(c *fib.Connection, err error) {
	l.mu.Lock()
	cc, ok := l.conns[c]
	delete(l.conns, c)
	l.mu.Unlock()
	if ok {
		if cc.timer != nil {
			cc.timer.Stop()
		}
		cc.slot.Release()
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
