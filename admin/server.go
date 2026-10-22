package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
)

// The Admin API's timeouts: for a request's header, its whole body, writing
// its response, and an idle connection; variables for tests to shorten.
var (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = time.Minute
	writeTimeout      = time.Minute
	idleTimeout       = time.Minute
)

const maxHeaderBytes = 64 << 10

// VersionHeader carries the version of the config a response is about.
const VersionHeader = "Fit-Config-Version"

// Server serves the Admin API:
//
//	GET    /config[/<pointer>]  the running config, as it was written, or
//	                            the value a JSON pointer names in it
//	PUT    /config[/<pointer>]  set that value to the body; "-" appends
//	PATCH  /config[/<pointer>]  merge the body into it (RFC 7396)
//	DELETE /config/<pointer>    remove it
//	POST   /load                replace the running config with the body
//	POST   /rollback/{n}        apply version n again, as a new version
//	GET    /versions            the configs kept, oldest first
//	GET    /versions/{n}        the config applied as version n
//	GET    /config/effective    the config as it runs, with what providers
//	                            provided merged in, and where each part
//	                            came from
//	GET    /status              what fit is doing now: listeners,
//	                            connections, upstreams and their backends'
//	                            health, certificates
//	GET    /dashboard/          read-only web pages of the GET endpoints,
//	                            unless admin.disable_dashboard
//
// Every config carries an ETag, a hash of it as written; a change sent with
// If-Match is applied only if the running config is still that one, and is
// otherwise answered 412. A change applies a whole new config, so an edit
// that leaves the config invalid is refused, with its errors.
//
// /config is the base config, as the file or the API wrote it; what
// providers provide is merged into it, and only GET /config/effective
// shows that.
type Server struct {
	configs  *Configs
	report   func(Result)
	token    []byte // nil when no token is asked for
	loopback bool   // a TCP address without a token: the Host field must name a loopback host
	maxBody  int64

	ln   net.Listener
	srv  *http.Server
	done chan struct{} // closed when Serve returns
}

// Start listens where a says and serves the Admin API for configs. report,
// when not nil, is called after each config the API applies.
func Start(a *config.Admin, configs *Configs, report func(Result)) (*Server, error) {
	s := &Server{configs: configs, report: report, maxBody: a.ConfigBytes(), done: make(chan struct{})}
	if a.TokenFile != "" {
		data, err := os.ReadFile(a.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("admin: token_file: %w", err)
		}
		if s.token = []byte(strings.TrimSpace(string(data))); len(s.token) == 0 {
			return nil, fmt.Errorf("admin: token_file %s is empty", a.TokenFile)
		}
	}
	network, address := a.Network()
	if network == "unix" {
		if err := removeStaleSocket(address); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen(network, address)
	if err != nil {
		return nil, fmt.Errorf("admin: %w", err)
	}
	if network == "unix" {
		// Only fit's own user may connect.
		if err := os.Chmod(address, 0o600); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("admin: %w", err)
		}
	}
	s.loopback = network == "tcp" && s.token == nil
	s.ln = newLimitListener(ln, a.Conns())

	mux := http.NewServeMux()
	for _, p := range []string{"/config", "/config/{path...}"} {
		mux.HandleFunc("GET "+p, s.getConfig)
		mux.HandleFunc("PUT "+p, s.putConfig)
		mux.HandleFunc("PATCH "+p, s.patchConfig)
		mux.HandleFunc("DELETE "+p, s.deleteConfig)
	}
	mux.HandleFunc("GET /config/effective", s.effective)
	mux.HandleFunc("POST /load", s.load)
	mux.HandleFunc("POST /rollback/{n}", s.rollback)
	mux.HandleFunc("GET /versions", s.versions)
	mux.HandleFunc("GET /versions/{n}", s.version)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /status", s.status)
	if !a.DisableDashboard {
		mux.Handle("GET /dashboard/", dashboardHandler())
		mux.Handle("GET /dashboard", http.RedirectHandler("/dashboard/", http.StatusMovedPermanently))
	}
	s.srv = &http.Server{
		Handler:           s.guard(mux),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		defer close(s.done)
		_ = s.srv.Serve(s.ln)
	}()
	return s, nil
}

// removeStaleSocket removes a socket left at path by a process that is gone,
// and fails if a process still listens there.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode().Type() != os.ModeSocket {
		return nil // nothing there, or something listening would fail on it anyway
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return fmt.Errorf("admin: %s is in use", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("admin: %w", err)
	}
	return nil
}

// Addr returns the address the Admin API listens on.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Shutdown stops taking requests and waits for those under way, until ctx
// ends; then it closes their connections.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	if err != nil {
		_ = s.srv.Close()
	}
	<-s.done
	return err
}

// guard refuses what must not reach the API: a request without the token,
// when one is asked for, but for the dashboard's files, which hold no data
// and ask for the token themselves; one from a web page, which a browser
// marks with Origin, but a GET from the API's own origin, as the
// dashboard's are; and, on a TCP address without a token, one whose Host is
// not a loopback name, as a web page's is after DNS rebinding.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != nil && !isDashboard(r) {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), s.token) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="fit admin"`)
				writeError(w, http.StatusUnauthorized, "a valid bearer token is needed")
				return
			}
		}
		if o := r.Header.Get("Origin"); o != "" && !sameOriginRead(r, o) {
			writeError(w, http.StatusForbidden, "requests from web pages are refused")
			return
		}
		if s.loopback && !loopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "the Host field must name a loopback host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// pointer returns the JSON pointer a /config/{path...} request names; none
// for /config itself, or /config/.
func pointer(r *http.Request) ([]string, error) {
	path := r.PathValue("path")
	if path == "" {
		return nil, nil
	}
	return parsePointer("/" + path)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	tokens, err := pointer(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	v := s.configs.Current()
	w.Header().Set("ETag", v.ETag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, v.ETag) {
		w.Header().Set(VersionHeader, strconv.FormatUint(v.Number, 10))
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if tokens == nil {
		writeConfig(w, v)
		return
	}
	root, err := decodeJSON(v.data)
	if err == nil {
		root, err = lookup(root, tokens)
	}
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	data := encodeJSON(root)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(VersionHeader, strconv.FormatUint(v.Number, 10))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func writeConfig(w http.ResponseWriter, v *Version) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", v.ETag)
	w.Header().Set(VersionHeader, strconv.FormatUint(v.Number, 10))
	w.Header().Set("Content-Length", strconv.Itoa(len(v.data)))
	_, _ = w.Write(v.data)
}

// readBody reads a request's body, which must be one of types and no larger
// than max_config_bytes. It answers the request itself when it cannot.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request, types ...string) ([]byte, bool) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !slices.Contains(types, mt) {
		writeError(w, http.StatusUnsupportedMediaType, "the body must be sent as "+strings.Join(types, " or "))
		return nil, false
	}
	if r.ContentLength > s.maxBody {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than max_config_bytes, %d", s.maxBody))
		return nil, false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than max_config_bytes, %d", s.maxBody))
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "reading the body: "+err.Error())
		return nil, false
	}
	return data, true
}

func (s *Server) load(w http.ResponseWriter, r *http.Request) {
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return
	}
	res, err := s.configs.Update(SourceAdmin, r.Header.Get("If-Match"), func(*Version) ([]byte, error) { return data, nil })
	s.applied(w, res, err)
}

// putConfig sets the value at the pointer to the body: it replaces a member
// or an element, adds a member, or, at "-", appends to an array.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	s.edit(w, r, []string{"application/json"}, func(root any, tokens []string, body any) (any, error) {
		return put(root, tokens, body)
	})
}

// patchConfig merges the body into the value at the pointer, as a JSON merge
// patch (RFC 7396).
func (s *Server) patchConfig(w http.ResponseWriter, r *http.Request) {
	s.edit(w, r, []string{"application/merge-patch+json", "application/json"}, func(root any, tokens []string, body any) (any, error) {
		target, err := lookup(root, tokens)
		if err != nil {
			return nil, err
		}
		return put(root, tokens, mergePatch(target, body))
	})
}

// deleteConfig removes the member or element at the pointer.
func (s *Server) deleteConfig(w http.ResponseWriter, r *http.Request) {
	tokens, err := pointer(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	res, err := s.configs.Update(SourceAdmin, r.Header.Get("If-Match"), func(cur *Version) ([]byte, error) {
		root, err := decodeJSON(cur.data)
		if err == nil {
			root, err = remove(root, tokens)
		}
		if err != nil {
			return nil, err
		}
		return encodeJSON(root), nil
	})
	s.applied(w, res, err)
}

// edit applies change, given the running config's tree, the request's
// pointer and its body, and applies the config it gives.
func (s *Server) edit(w http.ResponseWriter, r *http.Request, types []string, change func(root any, tokens []string, body any) (any, error)) {
	tokens, err := pointer(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	data, ok := s.readBody(w, r, types...)
	if !ok {
		return
	}
	body, err := decodeJSON(data)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	res, err := s.configs.Update(SourceAdmin, r.Header.Get("If-Match"), func(cur *Version) ([]byte, error) {
		if tokens == nil && r.Method == http.MethodPut {
			return data, nil // a whole config, kept as written
		}
		root, err := decodeJSON(cur.data)
		if err == nil {
			root, err = change(root, tokens, body)
		}
		if err != nil {
			return nil, err
		}
		return encodeJSON(root), nil
	})
	s.applied(w, res, err)
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseUint(r.PathValue("n"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "versions are numbers")
		return
	}
	res, err := s.configs.Rollback(n, r.Header.Get("If-Match"))
	s.applied(w, res, err)
}

// applied answers a request that applied a config, or tried to.
func (s *Server) applied(w http.ResponseWriter, res Result, err error) {
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	if s.report != nil {
		s.report(res)
	}
	w.Header().Set("ETag", res.ETag)
	w.Header().Set(VersionHeader, strconv.FormatUint(res.Version, 10))
	writeJSON(w, http.StatusOK, loaded{Version: res.Version, ETag: res.ETag, Added: listeners(res.Added), Removed: listeners(res.Removed), Warnings: res.Warnings})
}

func writeUpdateError(w http.ResponseWriter, err error) {
	var errs config.Errors
	var re *RequestError
	switch {
	case errors.Is(err, ErrChanged):
		writeError(w, http.StatusPreconditionFailed, strings.TrimPrefix(err.Error(), "admin: "))
	case errors.As(err, &re):
		writeError(w, re.Status, re.Msg)
	case errors.As(err, &errs):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "the config was not applied; the running config stays", Errors: problems(errs)})
	default:
		writeError(w, http.StatusInternalServerError, "the config was not applied; the running config stays: "+err.Error())
	}
}

func (s *Server) effective(w http.ResponseWriter, r *http.Request) {
	e := s.configs.Effective()
	w.Header().Set(VersionHeader, strconv.FormatUint(e.Version, 10))
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) versions(w http.ResponseWriter, r *http.Request) {
	vs := s.configs.Versions()
	writeJSON(w, http.StatusOK, struct {
		Current  uint64     `json:"current"`
		Versions []*Version `json:"versions"`
	}{vs[len(vs)-1].Number, vs})
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseUint(r.PathValue("n"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "versions are numbers")
		return
	}
	v, ok := s.configs.Version(n)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("version %d is not kept", n))
		return
	}
	writeConfig(w, v)
}

// loaded is POST /load's answer.
type loaded struct {
	Version  uint64         `json:"version"`
	ETag     string         `json:"etag"`
	Added    []listenerJSON `json:"listeners_added,omitempty"`
	Removed  []listenerJSON `json:"listeners_removed,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
}

type listenerJSON struct {
	Name      string   `json:"name"`
	Address   string   `json:"address"`
	Protocols []string `json:"protocols"`
}

func listeners(as []listener.Addr) []listenerJSON {
	ls := make([]listenerJSON, len(as))
	for i, a := range as {
		ls[i] = listenerJSON{Name: a.Name, Address: a.Addr.String(), Protocols: a.Protocols}
	}
	return ls
}

type errorBody struct {
	Error  string    `json:"error"`
	Errors []problem `json:"errors,omitempty"`
}

// problem is a config error, as the API reports it.
type problem struct {
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
}

func problems(errs config.Errors) []problem {
	ps := make([]problem, len(errs))
	for i, e := range errs {
		ps[i] = problem{Path: e.Path, Line: e.Line, Column: e.Col, Message: e.Msg}
	}
	return ps
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		status, data = http.StatusInternalServerError, []byte(`{"error":"encoding the answer failed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)+1))
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

// limitListener closes connections accepted past its limit at once, rather
// than letting them queue.
type limitListener struct {
	net.Listener
	slots chan struct{}
}

func newLimitListener(ln net.Listener, n int) *limitListener {
	return &limitListener{Listener: ln, slots: make(chan struct{}, n)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitConn{Conn: c, release: func() { <-l.slots }}, nil
		default:
			_ = c.Close()
		}
	}
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
