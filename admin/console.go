package admin

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
	"github.com/lesismal/fit/schema"
)

// SourceConsole is a config the console applied.
const SourceConsole = "console"

// consoleFiles are the console's pages: plain HTML, CSS and JavaScript,
// with nothing fetched from elsewhere.
//
//go:embed consoleui
var consoleFiles embed.FS

// consoleCSP keeps the console's pages to their own files and origin.
const consoleCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// The console's cookie and headers.
const (
	sessionCookie = "fit_console"
	csrfHeader    = "X-Fit-Csrf"
)

// maxLoginBytes bounds a login's body.
const maxLoginBytes = 4 << 10

// Console serves the console: web pages behind a login, from which the
// config is read and changed through the Admin API's endpoints, called in
// process. It is served by net/http on goroutines of its own, as the Admin
// API is; see docs/decisions/027-console-and-store.md.
type Console struct {
	api    http.Handler
	schema []byte
	logins *login.Logins
	secure bool
	logf   func(format string, args ...any)

	ln   net.Listener
	srv  *http.Server
	done chan struct{}
}

// StartConsole listens where c says and serves the console for configs,
// whose Admin API a says how large a config may be. report, when not nil,
// is called after each config the console applies; logf, when not nil,
// says who logged in, who failed to, and what each user changed.
func StartConsole(c *config.Console, a *config.Admin, configs *Configs, report func(Result), logf func(format string, args ...any)) (*Console, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	sch, err := schema.Generate()
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	cs := &Console{schema: sch, logf: logf, done: make(chan struct{})}
	if cs.logins, err = login.New(login.OptionsOf(c)); err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	inner := &Server{configs: configs, report: report, maxBody: a.ConfigBytes(), source: SourceConsole}
	cs.api = http.StripPrefix("/api/admin", inner.routes())

	var tlsConf *tls.Config
	if c.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("console: %w", err)
		}
		tlsConf = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		cs.secure = true
	}
	ln, err := net.Listen("tcp", c.Addr())
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	cs.ln = newLimitListener(ln, c.Conns())
	if tlsConf != nil {
		cs.ln = tls.NewListener(cs.ln, tlsConf)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", cs.login)
	mux.HandleFunc("POST /api/logout", cs.logout)
	mux.Handle("GET /api/session", cs.authed(http.HandlerFunc(cs.session)))
	mux.Handle("GET /api/schema", cs.authed(http.HandlerFunc(cs.serveSchema)))
	mux.Handle("/api/admin/", cs.authed(http.HandlerFunc(cs.proxy)))
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	}))
	sub, _ := fs.Sub(consoleFiles, "consoleui")
	files := http.FileServerFS(sub)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "the pages are only read")
			return
		}
		files.ServeHTTP(w, r)
	}))
	cs.srv = &http.Server{
		Handler:           cs.guard(mux),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		defer close(cs.done)
		_ = cs.srv.Serve(cs.ln)
	}()
	return cs, nil
}

// Addr returns the address the console listens on.
func (cs *Console) Addr() net.Addr { return cs.ln.Addr() }

// URL returns the console's address as a URL.
func (cs *Console) URL() string {
	if cs.secure {
		return "https://" + cs.Addr().String() + "/"
	}
	return "http://" + cs.Addr().String() + "/"
}

// Shutdown stops taking requests and waits for those under way, until ctx
// ends; then it closes their connections.
func (cs *Console) Shutdown(ctx context.Context) error {
	err := cs.srv.Shutdown(ctx)
	if err != nil {
		_ = cs.srv.Close()
	}
	<-cs.done
	return err
}

// guard sets the headers every answer carries, and refuses a request from a
// page of another origin: a browser marks those with Origin.
func (cs *Console) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", consoleCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		if cs.secure {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if o := r.Header.Get("Origin"); o != "" && o != cs.origin(r) {
			writeError(w, http.StatusForbidden, "requests from other web pages are refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// origin returns the console's own origin, as a browser on it sends it.
func (cs *Console) origin(r *http.Request) string {
	if cs.secure {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

type ctxUser struct{}

// authed lets through a request of a live session, which, unless it only
// reads, carries the session's CSRF token.
func (cs *Console) authed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := cs.lookup(r)
		if s == nil {
			writeError(w, http.StatusUnauthorized, "log in first")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !login.CSRFOK(*s, r.Header.Get(csrfHeader)) {
			writeError(w, http.StatusForbidden, "the request lacks the session's "+csrfHeader+" token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser{}, s)))
	})
}

// lookup returns the live session r's cookie names, a copy, or nil.
func (cs *Console) lookup(r *http.Request) *login.Session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	s, ok := cs.logins.Lookup(c.Value)
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

func (cs *Console) login(w http.ResponseWriter, r *http.Request) {
	// Only a script on the console's pages sends JSON without a preflight
	// the console does not answer.
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "the body must be sent as application/json")
		return
	}
	var body loginBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be {\"username\", \"password\"}")
		return
	}
	addr := clientAddr(r)
	if wait := cs.logins.LockedOut(addr); wait > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many failed logins from this address; try again later")
		return
	}
	ok, err := cs.logins.Check(r.Context(), body.Username, body.Password)
	if err != nil {
		return // the client left
	}
	if !ok {
		cs.logins.Failed(addr)
		cs.logf("console: a failed login as %q from %s", body.Username, addr)
		writeError(w, http.StatusUnauthorized, "the user name or password is wrong")
		return
	}
	token, s := cs.logins.Start(body.Username)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: cs.secure, SameSite: http.SameSiteStrictMode})
	cs.logf("console: %q logged in from %s", body.Username, addr)
	writeJSON(w, http.StatusOK, sessionBody{User: s.User, CSRF: s.CSRF})
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (cs *Console) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if s, ok := cs.logins.End(c.Value); ok {
			cs.logf("console: %q logged out", s.User)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: cs.secure, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (cs *Console) session(w http.ResponseWriter, r *http.Request) {
	s := r.Context().Value(ctxUser{}).(*login.Session)
	writeJSON(w, http.StatusOK, sessionBody{User: s.User, CSRF: s.CSRF})
}

func (cs *Console) serveSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/schema+json")
	_, _ = w.Write(cs.schema)
}

// proxy passes a request on to the Admin API's endpoints, and says what a
// user changed.
func (cs *Console) proxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		cs.api.ServeHTTP(w, r)
		return
	}
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	cs.api.ServeHTTP(sw, r)
	s := r.Context().Value(ctxUser{}).(*login.Session)
	if sw.status < 300 {
		if v := sw.Header().Get(VersionHeader); v != "" {
			cs.logf("console: %q: %s %s applied config version %s", s.User, r.Method, strings.TrimPrefix(r.URL.Path, "/api/admin"), v)
			return
		}
	}
	cs.logf("console: %q: %s %s: %d", s.User, r.Method, strings.TrimPrefix(r.URL.Path, "/api/admin"), sw.status)
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(p)
}
