package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
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

// Logins: how many fail from one address before it is refused, within what
// window, for how long, and how many addresses are remembered; how many
// passwords are checked at once.
const (
	maxLoginFailures  = 5
	loginWindow       = 15 * time.Minute
	loginLockout      = 15 * time.Minute
	maxLoginAddresses = 4096
	loginChecks       = 2
)

// maxLoginBytes bounds a login's body.
const maxLoginBytes = 4 << 10

// Console serves the console: web pages behind a login, from which the
// config is read and changed through the Admin API's endpoints, called in
// process. It is served by net/http on goroutines of its own, as the Admin
// API is; see docs/decisions/027-console-and-store.md.
type Console struct {
	api      http.Handler
	schema   []byte
	users    map[string][]byte // user name to bcrypt hash
	dummy    []byte            // a hash to check unknown names against
	idle     time.Duration
	lifetime time.Duration
	max      int
	secure   bool
	logf     func(format string, args ...any)
	now      func() time.Time // a variable for tests

	checks chan struct{} // a slot for each password checked at once

	mu       sync.Mutex
	sessions map[[sha256.Size]byte]*session // by the hash of their token
	fails    map[string]*loginFailures      // by client address

	ln   net.Listener
	srv  *http.Server
	done chan struct{}
}

type session struct {
	user          string
	csrf          string
	created, seen time.Time
}

type loginFailures struct {
	n            int
	first, until time.Time
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
	cs := &Console{
		schema: sch, users: map[string][]byte{}, idle: c.Idle(), lifetime: c.Lifetime(), max: c.Sessions(),
		logf: logf, now: time.Now, checks: make(chan struct{}, loginChecks),
		sessions: map[[sha256.Size]byte]*session{}, fails: map[string]*loginFailures{}, done: make(chan struct{}),
	}
	cost := bcrypt.DefaultCost
	for _, u := range c.Users {
		cs.users[u.Username] = []byte(u.PasswordHash)
		if n, err := bcrypt.Cost([]byte(u.PasswordHash)); err == nil {
			cost = n
		}
	}
	// An unknown name costs what a known one does.
	if cs.dummy, err = bcrypt.GenerateFromPassword([]byte(rand.Text()), cost); err != nil {
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
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get(csrfHeader)), []byte(s.csrf)) != 1 {
			writeError(w, http.StatusForbidden, "the request lacks the session's "+csrfHeader+" token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser{}, s)))
	})
}

// lookup returns the live session r's cookie names, a copy, or nil, ending
// one that has lasted too long or been idle too long.
func (cs *Console) lookup(r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	key := sha256.Sum256([]byte(c.Value))
	now := cs.now()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	s := cs.sessions[key]
	if s == nil {
		return nil
	}
	if now.Sub(s.seen) > cs.idle || now.Sub(s.created) > cs.lifetime {
		delete(cs.sessions, key)
		return nil
	}
	s.seen = now
	cp := *s
	return &cp
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
	if wait := cs.lockedOut(addr); wait > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many failed logins from this address; try again later")
		return
	}
	select {
	case cs.checks <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	hash, known := cs.users[body.Username]
	if !known {
		hash = cs.dummy
	}
	err := bcrypt.CompareHashAndPassword(hash, []byte(body.Password))
	<-cs.checks
	if err != nil || !known {
		cs.failed(addr)
		cs.logf("console: a failed login as %q from %s", body.Username, addr)
		writeError(w, http.StatusUnauthorized, "the user name or password is wrong")
		return
	}
	token, s := cs.start(body.Username)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: cs.secure, SameSite: http.SameSiteStrictMode})
	cs.logf("console: %q logged in from %s", body.Username, addr)
	writeJSON(w, http.StatusOK, sessionBody{User: s.user, CSRF: s.csrf})
}

// start starts a session for user and returns its token, ending the oldest
// when there are too many.
func (cs *Console) start(user string) (string, *session) {
	token := rand.Text() + rand.Text() // 52 base32 characters: 260 bits
	csrf := make([]byte, 24)
	_, _ = rand.Read(csrf)
	now := cs.now()
	s := &session{user: user, csrf: base64.RawURLEncoding.EncodeToString(csrf), created: now, seen: now}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for len(cs.sessions) >= cs.max {
		var oldest [sha256.Size]byte
		var at time.Time
		for k, v := range cs.sessions {
			if at.IsZero() || v.created.Before(at) {
				oldest, at = k, v.created
			}
		}
		delete(cs.sessions, oldest)
	}
	cs.sessions[sha256.Sum256([]byte(token))] = s
	return token, s
}

// lockedOut returns how long addr is still refused logins.
func (cs *Console) lockedOut(addr string) time.Duration {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if f := cs.fails[addr]; f != nil {
		return f.until.Sub(cs.now())
	}
	return 0
}

// failed counts a failed login from addr, refusing it logins for a while
// once it has failed too often.
func (cs *Console) failed(addr string) {
	now := cs.now()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	f := cs.fails[addr]
	if f == nil || now.Sub(f.first) > loginWindow && now.After(f.until) {
		if f == nil && len(cs.fails) >= maxLoginAddresses {
			cs.forgetFailures(now)
		}
		f = &loginFailures{first: now}
		cs.fails[addr] = f
	}
	if f.n++; f.n >= maxLoginFailures {
		f.until = now.Add(loginLockout)
	}
}

// forgetFailures makes room in the table of failures: the addresses no
// longer counted or refused go, or else the one that began failing first.
func (cs *Console) forgetFailures(now time.Time) {
	var oldest string
	var at time.Time
	for k, f := range cs.fails {
		if now.Sub(f.first) > loginWindow && now.After(f.until) {
			delete(cs.fails, k)
		} else if at.IsZero() || f.first.Before(at) {
			oldest, at = k, f.first
		}
	}
	if len(cs.fails) >= maxLoginAddresses {
		delete(cs.fails, oldest)
	}
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
		key := sha256.Sum256([]byte(c.Value))
		cs.mu.Lock()
		s := cs.sessions[key]
		delete(cs.sessions, key)
		cs.mu.Unlock()
		if s != nil {
			cs.logf("console: %q logged out", s.user)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: cs.secure, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (cs *Console) session(w http.ResponseWriter, r *http.Request) {
	s := r.Context().Value(ctxUser{}).(*session)
	writeJSON(w, http.StatusOK, sessionBody{User: s.user, CSRF: s.csrf})
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
	s := r.Context().Value(ctxUser{}).(*session)
	if sw.status < 300 {
		if v := sw.Header().Get(VersionHeader); v != "" {
			cs.logf("console: %q: %s %s applied config version %s", s.user, r.Method, strings.TrimPrefix(r.URL.Path, "/api/admin"), v)
			return
		}
	}
	cs.logf("console: %q: %s %s: %d", s.user, r.Method, strings.TrimPrefix(r.URL.Path, "/api/admin"), sw.status)
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
