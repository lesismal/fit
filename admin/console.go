package admin

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/app/workbench"
	"github.com/lesismal/fit/assistant"
	"github.com/lesismal/fit/assistant/mcp"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/schema"
)

// SourceConsole is a config the console applied.
const SourceConsole = "console"

// The console's cookie and headers.
const (
	sessionCookie = "fit_console"
	csrfHeader    = console.CSRFHeader
)

// errNoConsoleStore is why a password cannot change: without a store, a
// change would be lost at the next restart.
const errNoConsoleStore = "the console users are not saved: fit has no admin.store; passwords change only with a store"

// AdminModule is the ID of the config's own module of the workbench.
const AdminModule = "admin"

// Console serves the workbench at admin.console: the config's own module,
// from which the config is read and changed through the Admin API's
// endpoints, called in process, and the module of every role running beside
// it. It is a console of package app/console, on the listeners' engine; the
// Admin API's handlers run on the console's pool, never on a fib worker. See
// docs/decisions/027-console-and-store.md.
type Console struct {
	c       *console.Console
	api     http.Handler
	schema  []byte
	logf    func(format string, args ...any)
	module  *console.Module
	logins  *login.Logins
	configs *Configs // whose store keeps the console users
	// ai is the assistant, served as the module ai, when admin.assistant
	// is set; nil otherwise.
	ai *assistant.Assistant
	// mcp is the assistant's MCP server, at /mcp, which answers only while
	// the assistant's settings have mcp; nil without the assistant.
	mcp *mcp.Server
	// origins is what the pages' Content-Security-Policy adds for the
	// media routes a preview plays from (media.go), read again while
	// reading is set.
	origins atomic.Pointer[mediaOrigins]
	reading atomic.Bool
	// inner is the Admin API's server the console calls in process.
	inner *Server
}

// StartConsole serves the console c describes on engine, for configs, whose
// Admin API a says how large a config may be. modules, when not nil,
// returns the modules of the roles running beside it, which the workbench
// shows after the config's own. report, when not nil, is called after each
// config the console applies; logf, when not nil, says who logged in, who
// failed to, and what each user changed.
func StartConsole(engine *fib.Engine, c *config.Console, a *config.Admin, configs *Configs, modules func() []*console.Module, report func(Result), logf func(format string, args ...any)) (*Console, error) {
	return StartConsoleWith(engine, c, a, configs, modules, report, logf, ConsoleExtras{})
}

// ConsoleExtras are what a console of a manager of other fits (cluster)
// adds: the check of each request beside the modules' own, and the bound
// on the assistant's levels.
type ConsoleExtras struct {
	// Gate is app/console's Options.Gate.
	Gate func(user, method, path, query string) string
	// AssistantCap, when not nil, is the highest level of the
	// assistant's a user has, whatever admin.assistant gives
	// (assistant's Options.LevelCap).
	AssistantCap func(user string) string
	// Cluster, when not nil, is the manager the assistant's cluster tools
	// run on (assistant's Options.Cluster, I153).
	Cluster assistant.Cluster
}

// StartConsoleWith is StartConsole with x.
func StartConsoleWith(engine *fib.Engine, c *config.Console, a *config.Admin, configs *Configs, modules func() []*console.Module, report func(Result), logf func(format string, args ...any), x ConsoleExtras) (*Console, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	sch, err := schema.Generate()
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	cs := &Console{schema: sch, logf: logf, configs: configs}
	cs.origins.Store(configs.loadOrigins())
	// The logins exist once the console is started, which may serve /mcp
	// at once.
	var logins atomic.Pointer[login.Logins]
	inner := &Server{configs: configs, report: report, maxBody: a.ConfigBytes(), source: SourceConsole}
	cs.inner = inner
	cs.api = inner.routes()
	if a.Assistant != nil {
		o := assistant.Options{Engine: engine, Settings: a.Assistant, Console: c, Config: configs.CurrentConfig, Logf: logf,
			// The tools read through the Admin API's endpoints, in process,
			// as the workbench's pages do; they change nothing.
			API: cs.api, Schema: sch, SchemaZh: workbench.SchemaZh, Validate: validateConfig, LevelCap: x.AssistantCap, Cluster: x.Cluster}
		if st := configs.Sealer(); st != nil {
			o.Store = st
		}
		if l := configs.env.Log; l != nil {
			o.Logs = l.Recent
		}
		// Settings is set, and the tools are the assistant's own: New
		// cannot fail.
		cs.ai, _ = assistant.New(o)
		inner.audit = cs.auditFile
		mo := mcp.Options{Assistant: cs.ai, HasUser: func(user string) bool { l := logins.Load(); return l != nil && l.HasUser(user) },
			Docs: schema.Reference, URL: cs.URL, Logf: logf}
		if o.Store != nil {
			mo.Store = o.Store
		}
		cs.mcp = mcp.New(mo)
		cs.ai.Extend(cs.mcp.Routes)
	}
	cs.module = &console.Module{ID: AdminModule, Routes: cs.routes}
	var endpoints map[string]console.Handler
	if cs.mcp != nil {
		endpoints = map[string]console.Handler{mcp.Path: cs.mcp.Serve}
	}
	rc := &config.RoleConsole{Address: c.Addr(), MPTCP: c.MPTCP, CertFile: c.CertFile, KeyFile: c.KeyFile, Users: c.Users,
		SessionTimeout: c.SessionTimeout, SessionLifetime: c.SessionLifetime, MaxSessions: c.MaxSessions, MaxConns: c.MaxConns, Access: c.Access, At: c.At}
	cs.c, err = console.Start(engine, console.Options{
		Config: rc, Cookie: sessionCookie, Pages: workbench.Pages(), Logf: logf, Endpoints: endpoints,
		// GET /api/node, beside /api/m/admin/node: who this fit is, where a
		// manager looks first, whatever modules the console serves.
		Routes: func(api *console.API) { api.Handle("GET", "/api/node", cs.proxy) },
		// The routes of the console's own pages, the workbench's, beside
		// the modules': /api/m/admin/schema and the like.
		Modules: func() []*console.Module {
			out := []*console.Module{cs.module}
			if cs.ai != nil {
				out = append(out, cs.ai.Module())
			}
			if modules != nil {
				out = append(out, modules()...)
			}
			return out
		},
		// A config as large as the Admin API takes, with room for the
		// request's own JSON around it, or a store's binary import.
		MaxBody: a.ConfigBytes() + 64<<10,
		// A session whose password is temporary reads who it is and
		// changes it, and does nothing else.
		PasswordRoutes: []string{"GET /api/m/" + AdminModule + "/users", "POST /api/m/" + AdminModule + "/account/password"},
		// The media routes' listeners, which a preview plays from.
		Sources: cs.sources,
		// An upload's pieces stream to the library's file pool.
		StreamBody: uploadPatch,
		Gate:       x.Gate,
		// admin.console.access, then the global access, as the running
		// config has them.
		Access: &configs.consoleAccess,
	})
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	configs.console.Store(true)
	cs.logins = cs.c.Logins()
	logins.Store(cs.logins)
	if user, until := configs.TemporaryPassword(); user != "" {
		cs.logins.SetTemporary(user, until)
	}
	return cs, nil
}

// validateConfig checks a config's JSON as fit validate does: loaded, and
// compiled, which provisions every module and checks their params, without
// binding anything.
func validateConfig(data []byte) error {
	c, err := config.Load(data)
	if err != nil {
		return err
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		return err
	}
	g.Release()
	return nil
}

// routes adds the config's module: the schema, the console's own accounts,
// and every Admin API endpoint under /api/.
func (cs *Console) routes(api *console.API) {
	api.Handle("GET", "/api/schema", func(r *console.Request) {
		_ = console.Write(r.Context, http.StatusOK, "application/schema+json", cs.schema)
	})
	// Exact routes beat the catch-all below: they are the console's own,
	// under the admin module as /api/m/admin/users and the like.
	api.Handle("GET", "/api/users", cs.users)
	api.Handle("POST", "/api/account/password", cs.accountPassword)
	api.Handle("POST", "/api/users/password", cs.userPassword)
	api.HandlePrefix("PATCH", "/api/media/library/", cs.uploadChunk)
	// The BitTorrent client's events stream on the console's connection
	// (btconsole.go), which the proxy below, answering whole responses,
	// cannot.
	api.Handle("GET", "/api/bittorrent/events", cs.btConsoleEvents)
	api.HandlePrefix("", "/api/", cs.proxy)
}

// Users returns the console's users, by name.
func (cs *Console) Users() []string { return cs.logins.Users() }

// AssistantLevel returns user's level of the assistant's as admin.assistant
// gives it, before any bound; "" without the assistant.
func (cs *Console) AssistantLevel(user string) string {
	if cs.ai == nil {
		return ""
	}
	return cs.ai.Settings().Level(user)
}

// Addr returns the address the console listens on.
func (cs *Console) Addr() net.Addr { return cs.c.Addr() }

// URL returns the console's address as a URL.
func (cs *Console) URL() string { return cs.c.URL() }

// Shutdown stops taking connections, closes those open, and waits for the
// requests under way, until ctx ends at most.
func (cs *Console) Shutdown(ctx context.Context) error {
	cs.c.Stop(ctx)
	if cs.ai != nil {
		cs.ai.Close()
	}
	return ctx.Err()
}

// proxy passes a request on to the Admin API's endpoints, on the console's
// pool, and says what a user changed.
func (cs *Console) proxy(r *console.Request) {
	path := strings.TrimPrefix(r.Path, "/api")
	req, err := http.NewRequestWithContext(r.Context.Context(), r.Request.Method, "http://console"+path, bytes.NewReader(r.Context.Body()))
	if err != nil {
		_ = console.Error(r.Context, http.StatusBadRequest, err.Error())
		return
	}
	req.URL.RawQuery = r.Request.URL.RawQuery
	req.Header = r.Request.Header.Clone()
	// Who asks, and the host the browser addressed, for the URLs the media
	// pages are handed (media.go); never what the client sent as them.
	req.Header.Set(ConsoleUserHeader, r.User)
	req.Header.Set(ConsoleHostHeader, r.Request.Host)
	req.RemoteAddr = r.Request.RemoteAddr
	r.Go(func() {
		w := &recorder{header: http.Header{}, status: http.StatusOK}
		cs.api.ServeHTTP(w, req)
		h := r.Context.Header().Clone()
		for k, v := range w.header {
			h[k] = v
		}
		// The change is logged before the answer goes, so that whoever
		// has the answer finds the change in the log.
		dry, _ := isDryRun(req)
		cs.logChange(r.User, req.Method, path, dry, w)
		_ = r.Context.WriteResponse(fibhttp.Response{StatusCode: w.status, Header: h, Body: w.body.Bytes()})
	})
}

// logChange says what a user's request changed; reads say nothing, and a
// dry run says it changed nothing.
func (cs *Console) logChange(user, method, path string, dry bool, w *recorder) {
	if method == http.MethodGet || method == http.MethodHead {
		return
	}
	if dry {
		cs.logf("console: %q: %s %s?dry_run=1: %d, checked and not applied", user, method, path, w.status)
		return
	}
	if w.status < 300 {
		if v := w.header.Get(VersionHeader); v != "" {
			cs.logf("console: %q: %s %s applied config version %s", user, method, path, v)
			return
		}
	}
	cs.logf("console: %q: %s %s: %d", user, method, path, w.status)
}

// usersBody is GET /api/users's answer: who is logged in, whether a store
// keeps the passwords, and every user who may log in.
type usersBody struct {
	User  string   `json:"user"`
	Store bool     `json:"store"`
	Users []string `json:"users"`
}

// changedBody is a password change's answer: whose password changed.
type changedBody struct {
	Changed  bool   `json:"changed"`
	User     string `json:"user,omitempty"`
	Username string `json:"username,omitempty"`
}

// users answers GET /api/users, with the users who may log in.
func (cs *Console) users(r *console.Request) {
	_, stored := cs.configs.ConsoleUsers()
	_ = r.JSON(http.StatusOK, usersBody{User: r.User, Store: stored, Users: cs.logins.Users()})
}

// passwordBody is a user's change of its own password.
type passwordBody struct {
	CurrentPassword string `json:"current_password"`
	Password        string `json:"password"`
}

// accountPassword answers POST /api/account/password: a user changes its own
// password, giving the current one. The change is hashed and saved to the
// store on the console's pool, since bcrypt and disk writes block, and the
// user's other sessions are ended.
func (cs *Console) accountPassword(r *console.Request) {
	var body passwordBody
	if !r.Decode(&body) {
		return
	}
	if body.CurrentPassword == "" {
		_ = console.Error(r.Context, http.StatusBadRequest, "the body must hold the current password and the new password")
		return
	}
	if msg := passwordProblem(body.Password); msg != "" {
		_ = console.Error(r.Context, http.StatusBadRequest, msg)
		return
	}
	if _, temp := cs.logins.Temporary(r.User); temp {
		if cs.logins.Expired(r.User) {
			_ = console.Error(r.Context, http.StatusForbidden, "the temporary password has expired without being changed; restart fit with -admin to set a new one in time")
			return
		}
		if body.Password == body.CurrentPassword {
			_ = console.Error(r.Context, http.StatusBadRequest, "the new password must not be the temporary one")
			return
		}
	}
	addr, ok := cs.passwordReady(r)
	if !ok {
		return
	}
	// The check is admitted as a login's is: one from an address at a
	// time, each failure counted before the next is checked, against the
	// address and the user.
	t, wait, busy, slowed := cs.logins.AdmitUser(addr, r.User)
	if t == nil {
		refuseChecks(r, wait, busy, slowed)
		return
	}
	user, token := r.User, cs.sessionToken(r)
	r.GoThen(func() {
		if wait := t.LockedOut(); wait > 0 {
			refuseChecks(r, wait, false, false)
			return
		}
		ok, err := cs.logins.Check(r.Context.Context(), user, body.CurrentPassword)
		if err != nil {
			return // the client left
		}
		if ok {
			t.Passed()
		} else {
			t.Done(true)
		}
		if !ok {
			cs.failedPassword(r, addr)
			return
		}
		// passwordProblem kept it to bcrypt's 72 bytes: hashing it cannot
		// fail.
		hash, _ := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err := cs.savePassword(user, string(hash)); err != nil {
			_ = console.Error(r.Context, http.StatusInternalServerError, "saving the password: "+err.Error())
			return
		}
		cs.logins.SetPassword(user, hash)
		cs.logins.EndUserSessions(user, token)
		cs.logf("console: %q changed the password of %q", user, user)
		_ = console.WriteJSON(r.Context, http.StatusOK, changedBody{Changed: true, User: user})
	}, func() { t.Done(false) }) // a check that did not run counts nothing
}

// resetBody is a console user's reset of another's password.
type resetBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// userPassword answers POST /api/users/password: a console user sets another
// user's password. Every session of the other user ends; all console users
// are admins, so no role of its own is asked for.
func (cs *Console) userPassword(r *console.Request) {
	var body resetBody
	if !r.Decode(&body) {
		return
	}
	if body.Username == "" {
		_ = console.Error(r.Context, http.StatusBadRequest, "the body must hold the user name and the new password")
		return
	}
	if msg := passwordProblem(body.Password); msg != "" {
		_ = console.Error(r.Context, http.StatusBadRequest, msg)
		return
	}
	if !cs.logins.HasUser(body.Username) {
		_ = console.Error(r.Context, http.StatusBadRequest, fmt.Sprintf("there is no console user named %q", body.Username))
		return
	}
	if _, ok := cs.passwordReady(r); !ok {
		return
	}
	caller, target := r.User, body.Username
	r.Go(func() {
		// passwordProblem kept it to bcrypt's 72 bytes: hashing it cannot
		// fail.
		hash, _ := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err := cs.savePassword(target, string(hash)); err != nil {
			_ = console.Error(r.Context, http.StatusInternalServerError, "saving the password: "+err.Error())
			return
		}
		cs.logins.SetPassword(target, hash)
		cs.logins.EndUserSessions(target, "")
		cs.logf("console: %q set the password of %q", caller, target)
		_ = console.WriteJSON(r.Context, http.StatusOK, changedBody{Changed: true, Username: target})
	})
}

// passwordProblem returns why password cannot be a console password, and ""
// when it can: bcrypt takes 72 bytes at most.
func passwordProblem(password string) string {
	switch {
	case password == "":
		return "the new password is empty"
	case len(password) > 72:
		return "the new password is longer than bcrypt's 72 bytes"
	}
	return ""
}

// passwordReady answers 429 while a lockout holds the request's address, and
// 409 when no store keeps the console users; ok says a change may go on, from
// addr, which the console counts failures by.
func (cs *Console) passwordReady(r *console.Request) (addr string, ok bool) {
	addr = clientHost(r.Context.Request)
	if wait := cs.logins.LockedOut(addr); wait > 0 {
		refuseChecks(r, wait, false, false)
		return addr, false
	}
	if _, stored := cs.configs.ConsoleUsers(); !stored {
		_ = console.Error(r.Context, http.StatusConflict, errNoConsoleStore)
		return addr, false
	}
	return addr, true
}

// failedPassword answers a failed password check from addr, which its
// ticket counted: 403, or 429 once it has locked the address out.
func (cs *Console) failedPassword(r *console.Request, addr string) {
	if wait := cs.logins.LockedOut(addr); wait > 0 {
		refuseChecks(r, wait, false, false)
		return
	}
	_ = console.Error(r.Context, http.StatusForbidden, "the current password is wrong")
}

// refuseChecks answers 429 for checks an address may not make for wait, as
// the console refuses logins from it, or, when busy, while another check
// from it is under way, or, when slowed, while its user's are slowed.
func refuseChecks(r *console.Request, wait time.Duration, busy, slowed bool) {
	msg := "too many failed password checks from this address; try again later"
	switch {
	case busy:
		msg = "another password check from this address is under way; try again"
	case slowed:
		msg = "too many failed password checks of this user; try again later"
	}
	console.Refuse(r.Context, wait, msg)
}

// savePassword writes hash as user's password hash beside the config in the
// store, replacing the one of a user whose password changed already, and
// adding user when it is not there yet; two changes at once do not lose one.
func (cs *Console) savePassword(user, hash string) error {
	return cs.configs.SetConsolePassword(user, hash)
}

// sessionToken returns the raw token of the session r was made with, which
// keeps its session when the user's others end: the console's cookie, named
// as app/console names it.
func (cs *Console) sessionToken(r *console.Request) string {
	ck, err := r.Context.Request.Cookie(login.CookieName(sessionCookie, cs.c.Addr()))
	if err != nil {
		return ""
	}
	return ck.Value
}

// clientHost is the host of r's address, as the console counts failed logins
// by it.
func clientHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// recorder is the Admin API's answer, held whole: the API's answers are
// bounded by its configs and its history.
type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (w *recorder) Header() http.Header { return w.header }

func (w *recorder) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
}

func (w *recorder) Write(p []byte) (int, error) {
	w.wrote = true
	return w.body.Write(p)
}
