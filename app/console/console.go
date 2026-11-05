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

// How long a console keeps a connection that waits for its next request.
// One that has not sent a request with a valid session (a login that
// succeeds counts) is kept for UnauthIdleTimeout, as the Admin API keeps an
// unauthenticated one, so that clients without a login cannot hold the
// console's few connections for long; one that has is kept for IdleTimeout,
// until it logs out.
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
	// PasswordRoutes are the endpoints, as "POST /api/m/admin/account/password",
	// that a session whose password is temporary (login.Session.MustChange)
	// may call beside GET /api/session, GET /api/workbench and the logout;
	// every other answers 403 until the password changes.
	PasswordRoutes []string
}

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
	pwOnly  []string // Options.PasswordRoutes
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
	logins, err := login.New(login.OptionsOf(cfg.Console()))
	if err != nil {
		return nil, err
	}
	c := &Console{engine: engine, index: -1, cookie: o.Cookie, logins: logins, modules: o.Modules, pwOnly: o.PasswordRoutes,
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
		// Every connection starts out unauthenticated; a request with a
		// valid session gives its connection c.idle (see serve).
		IdleTimeout:  c.unauthIdle,
		WriteTimeout: 30 * time.Second,
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
	// mptcp is the pages member's own, on a role's console and on
	// admin.console alike: the login behind the binding is the same either
	// way, only the sockets the kernel opens differ.
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
	// A valid session: the connection is kept between requests for the
	// console's usual idle time from now on.
	_ = ctx.SetIdleTimeout(c.idle)
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
	// queued, so that every failure counts before the next guess is checked.
	addr := clientAddr(ctx.Request)
	t, wait, busy := c.logins.Admit(addr)
	if t == nil {
		refuseLogin(ctx, wait, busy)
		return
	}
	// bcrypt takes its time on the login pool, not on the worker, nor on
	// the workers of logged-in users' tasks.
	(&Request{Context: ctx, c: c}).goOn(c.checks, func() {
		if wait := t.LockedOut(); wait > 0 {
			refuseLogin(ctx, wait, false)
			return
		}
		ok, err := c.logins.Check(ctx.Context(), body.Username, body.Password)
		if err != nil {
			return // the client left
		}
		t.Done(!ok)
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
		_ = ctx.SetIdleTimeout(c.idle)
		ctx.Header().Add("Set-Cookie", (&stdhttp.Cookie{Name: c.cookie, Value: token, Path: "/", HttpOnly: true, Secure: c.secure, SameSite: stdhttp.SameSiteStrictMode}).String())
		c.infof("console: %q logged in from %s", body.Username, addr)
		_ = WriteJSON(ctx, stdhttp.StatusOK, newSessionBody(&s))
	}, func() { t.Done(false) }) // a check that did not run counts nothing
}

// refuseLogin answers 429 for a login that may not be checked for wait,
// since its address is locked out or, when busy, another login from it is
// being checked.
func refuseLogin(ctx *fibhttp.Context, wait time.Duration, busy bool) {
	ctx.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	msg := "too many failed logins from this address; try again later"
	if busy {
		msg = "another login from this address is being checked; try again"
	}
	_ = Error(ctx, stdhttp.StatusTooManyRequests, msg)
}

func (c *Console) logout(ctx *fibhttp.Context) {
	if ck, err := ctx.Request.Cookie(c.cookie); err == nil {
		if s, ok := c.logins.End(ck.Value); ok {
			c.infof("console: %q logged out", s.User)
		}
	}
	ctx.Header().Add("Set-Cookie", (&stdhttp.Cookie{Name: c.cookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: c.secure, SameSite: stdhttp.SameSiteStrictMode}).String())
	// Logged out, the connection is an unauthenticated one again.
	_ = ctx.SetIdleTimeout(c.unauthIdle)
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

// limitHandler closes the connections past max as they open, and those of
// a client past its share of them (login.Peers), and closes every one it
// has open when the console stops.
type limitHandler struct {
	fib.Handler
	max   int64
	n     atomic.Int64
	peers *login.Peers
	peer  func(*fib.Connection) netip.Addr // the client's address; tests replace it

	mu     sync.Mutex
	conns  map[*fib.Connection]netip.Addr // to the client's address
	closed bool
}

func newLimitHandler(h fib.Handler, max int) *limitHandler {
	return &limitHandler{Handler: h, max: int64(max), peers: login.NewPeers(max), conns: map[*fib.Connection]netip.Addr{},
		peer: func(c *fib.Connection) netip.Addr { return c.RemoteAddrPort().Addr() }}
}

func (l *limitHandler) OnOpen(c *fib.Connection) {
	if l.n.Add(1) > l.max {
		l.n.Add(-1)
		_ = c.Close()
		return
	}
	ip := l.peer(c)
	if !l.peers.Open(ip) {
		l.n.Add(-1)
		_ = c.Close()
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		l.peers.Close(ip)
		l.n.Add(-1)
		_ = c.Close()
		return
	}
	l.conns[c] = ip
	l.mu.Unlock()
	l.Handler.OnOpen(c)
}

func (l *limitHandler) OnClose(c *fib.Connection, err error) {
	l.mu.Lock()
	ip, ok := l.conns[c]
	delete(l.conns, c)
	l.mu.Unlock()
	if ok {
		l.peers.Close(ip)
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
