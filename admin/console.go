package admin

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/app/workbench"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
	"github.com/lesismal/fit/schema"
)

// SourceConsole is a config the console applied.
const SourceConsole = "console"

// The console's cookie and headers.
const (
	sessionCookie = "fit_console"
	csrfHeader    = console.CSRFHeader
)

// AdminModule is the ID of the config's own module of the workbench.
const AdminModule = "admin"

// Console serves the workbench at admin.console: the config's own module,
// from which the config is read and changed through the Admin API's
// endpoints, called in process, and the module of every role running beside
// it. It is a console of package app/console, on the listeners' engine; the
// Admin API's handlers run on the console's pool, never on a fib worker. See
// docs/decisions/027-console-and-store.md.
type Console struct {
	c      *console.Console
	api    http.Handler
	schema []byte
	logf   func(format string, args ...any)
	module *console.Module
	logins *login.Logins
}

// StartConsole serves the console c describes on engine, for configs, whose
// Admin API a says how large a config may be. modules, when not nil,
// returns the modules of the roles running beside it, which the workbench
// shows after the config's own. report, when not nil, is called after each
// config the console applies; logf, when not nil, says who logged in, who
// failed to, and what each user changed.
func StartConsole(engine *fib.Engine, c *config.Console, a *config.Admin, configs *Configs, modules func() []*console.Module, report func(Result), logf func(format string, args ...any)) (*Console, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	sch, err := schema.Generate()
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	cs := &Console{schema: sch, logf: logf}
	inner := &Server{configs: configs, report: report, maxBody: a.ConfigBytes(), source: SourceConsole}
	cs.api = inner.routes()
	cs.module = &console.Module{ID: AdminModule, Routes: cs.routes}
	rc := &config.RoleConsole{Address: c.Addr(), CertFile: c.CertFile, KeyFile: c.KeyFile, Users: c.Users,
		SessionTimeout: c.SessionTimeout, SessionLifetime: c.SessionLifetime, MaxSessions: c.MaxSessions, MaxConns: c.MaxConns, At: c.At}
	cs.c, err = console.Start(engine, console.Options{
		Config: rc, Cookie: sessionCookie, Pages: workbench.Pages(), Logf: logf,
		// The routes of the console's own pages, the workbench's, beside
		// the modules': /api/m/admin/schema and the like.
		Modules: func() []*console.Module {
			out := []*console.Module{cs.module}
			if modules != nil {
				out = append(out, modules()...)
			}
			return out
		},
		// A config as large as the Admin API takes, with room for the
		// request's own JSON around it.
		MaxBody: a.ConfigBytes() + 64<<10,
	})
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	cs.logins = cs.c.Logins()
	return cs, nil
}

// routes adds the config's module: the schema, and every Admin API
// endpoint under /api/.
func (cs *Console) routes(api *console.API) {
	api.Handle("GET", "/api/schema", func(r *console.Request) {
		_ = console.Write(r.Context, http.StatusOK, "application/schema+json", cs.schema)
	})
	api.HandlePrefix("", "/api/", cs.proxy)
}

// Addr returns the address the console listens on.
func (cs *Console) Addr() net.Addr { return cs.c.Addr() }

// URL returns the console's address as a URL.
func (cs *Console) URL() string { return cs.c.URL() }

// Shutdown stops taking connections, closes those open, and waits for the
// requests under way, until ctx ends at most.
func (cs *Console) Shutdown(ctx context.Context) error {
	cs.c.Stop(ctx)
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
		cs.logChange(r.User, req.Method, path, w)
		_ = r.Context.WriteResponse(fibhttp.Response{StatusCode: w.status, Header: h, Body: w.body.Bytes()})
	})
}

// logChange says what a user's request changed; reads say nothing.
func (cs *Console) logChange(user, method, path string, w *recorder) {
	if method == http.MethodGet || method == http.MethodHead {
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
