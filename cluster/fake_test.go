package cluster

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/pebbletest"
)

// The fake members' credentials.
const (
	fakeUser     = "ops"
	fakePassword = "pa55-w0rd-of-the-member"
	fakeToken    = "tok-0123456789abcdef-member"
)

// fakeMember is a fit as a manager sees it: a console that logs in, and the
// Admin API's GET /node, /status and /config/check behind it, or behind a
// bearer token.
type fakeMember struct {
	srv *httptest.Server
	id  string

	mu         sync.Mutex
	sessions   map[string]bool
	mustChange bool
	rateLimit  int           // seconds a login is refused for, when not 0
	delay      time.Duration // before each answer
	noNode     bool
	badJSON    bool
	statusPad  int // bytes added to /status
	// caps replaces the capabilities /node lists; nodeStatus and
	// loginStatus answer /node and logins with a status of their own;
	// noCSRF logs in without a CSRF token; expired refuses the password as
	// a temporary one expired; reject refuses every session; logoutDelay
	// holds a logout.
	caps        []string
	nodeStatus  int
	loginStatus int
	noCSRF      bool
	expired     bool
	reject      bool
	logoutDelay time.Duration
	noRoles     bool
	logins      int
	failed      int
	logouts     int

	inflight, maxInflight atomic.Int64
	requests              atomic.Int64

	// hist is the configs applied, the latest last, which /reload,
	// /rollback and /versions use; purges are the bodies of /cache/purge;
	// zones the cache zones it has; opFail answers the operations' writes
	// with that status.
	hist   []fakeVersion
	purges []string
	zones  []string
	opFail int
	// bt answers GET /bittorrent/summary when set (I206), and btReads
	// counts the reads of it.
	bt      string
	btReads int
}

// fakeVersion is a config a fake member applied.
type fakeVersion struct {
	n    int
	etag string
}

// current returns the version running and its ETag; f.mu is held.
func (f *fakeMember) current() fakeVersion {
	if len(f.hist) == 0 {
		f.hist = []fakeVersion{{6, `"a"`}, {7, `"abc"`}}
	}
	return f.hist[len(f.hist)-1]
}

func newFakeMember(t *testing.T, tlsOn bool) *fakeMember {
	t.Helper()
	f := &fakeMember{id: "node-" + strconv.Itoa(int(time.Now().UnixNano()%100000)), sessions: map[string]bool{}}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	if tlsOn {
		f.srv.StartTLS()
	} else {
		f.srv.Start()
	}
	t.Cleanup(f.srv.Close)
	return f
}

// caPEM is the PEM of the fake's certificate, which httptest signs itself.
func (f *fakeMember) caPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw}))
}

func (f *fakeMember) set(fn func(f *fakeMember)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeMember) counts() (logins, failed, logouts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins, f.failed, f.logouts
}

// restart forgets every session, as a member restarted does.
func (f *fakeMember) restart() {
	f.set(func(f *fakeMember) { f.sessions = map[string]bool{} })
}

func (f *fakeMember) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxInflight.Load()
		if n <= m || f.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	delay, mustChange, rate, noNode, badJSON, pad := f.delay, f.mustChange, f.rateLimit, f.noNode, f.badJSON, f.statusPad
	caps, nodeStatus, loginStatus, noCSRF, expired, reject, logoutDelay, noRoles := f.caps, f.nodeStatus, f.loginStatus, f.noCSRF, f.expired, f.reject, f.logoutDelay, f.noRoles
	f.mu.Unlock()
	if caps == nil {
		caps = []string{"cache", "cache.prefix", "config.check", "config.reload", "config.versions", "console", "node", "status"}
	}
	var roles any = []string{}
	if noRoles {
		roles, caps = nil, nil
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/login":
		body, _ := io.ReadAll(r.Body)
		var b struct{ Username, Password string }
		_ = json.Unmarshal(body, &b)
		if loginStatus != 0 {
			w.WriteHeader(loginStatus)
			_, _ = w.Write([]byte(`{"error":"the login broke with ` + fakePassword + `"}`))
			return
		}
		if rate > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(rate))
			w.WriteHeader(429)
			if rate == 7 {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			fmt.Fprintf(w, `{"error":"too many failed logins from this address; try again later","retry_after":%d}`, rate)
			return
		}
		f.mu.Lock()
		f.logins++
		if b.Username != fakeUser || b.Password != fakePassword || expired {
			f.failed++
			f.mu.Unlock()
			w.WriteHeader(401)
			if expired {
				_, _ = w.Write([]byte(`{"error":"expired","password_expired":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"error":"the user name or password is wrong"}`))
			return
		}
		tok := fmt.Sprintf("s%d", f.logins)
		f.sessions[tok] = true
		f.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "fit_console_2020", Value: tok, Path: "/", HttpOnly: true})
		csrf := "csrf-" + tok
		if noCSRF {
			csrf = ""
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"user": fakeUser, "csrf": csrf, "must_change_password": mustChange})
		return
	case "/api/logout":
		time.Sleep(logoutDelay)
		f.mu.Lock()
		if c, err := r.Cookie("fit_console_2020"); err == nil && r.Header.Get("X-Fit-Csrf") == "csrf-"+c.Value {
			delete(f.sessions, c.Value)
			f.logouts++
		}
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
		return
	}
	path, viaConsole := r.URL.Path, false
	if p, ok := strings.CutPrefix(path, "/api/m/admin"); ok {
		path, viaConsole = p, true
	} else if path == "/api/node" {
		path, viaConsole = "/node", true
	}
	if viaConsole {
		c, err := r.Cookie("fit_console_2020")
		f.mu.Lock()
		ok := err == nil && f.sessions[c.Value] && !reject
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"log in first"}`))
			return
		}
		if mustChange {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"change the temporary password first","must_change_password":true}`))
			return
		}
	} else if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"a valid token is needed"}`))
		return
	}
	if badJSON {
		_, _ = w.Write([]byte(`{"id":`))
		return
	}
	if f.serveOps(w, r, path) {
		return
	}
	if path == "/bittorrent/summary" {
		f.mu.Lock()
		bt := f.bt
		f.btReads++
		f.mu.Unlock()
		if bt == "" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such endpoint"}`))
			return
		}
		_, _ = w.Write([]byte(bt))
		return
	}
	switch path {
	case "/node":
		if noNode {
			w.WriteHeader(404)
			return
		}
		if nodeStatus != 0 {
			w.WriteHeader(nodeStatus)
			_, _ = w.Write([]byte(`{"error":"no; the token is ` + fakeToken + `"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.id, "id_kept": "store", "hostname": "h", "fit_version": "v1.2.3",
			"fib_version": "v1.7.3", "go_version": "go1.27", "os": "linux", "arch": "amd64", "started": "2026-11-06T00:00:00Z",
			"uptime_seconds": 42, "config_version": 7, "config_etag": `"abc"`, "config_applied": "2026-11-06T00:00:01Z",
			"config_source": "console", "roles": roles, "schema_version": 1, "schema_digest": "sha256:00",
			"capabilities": caps})
	case "/status":
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 7, "reloads": map[string]any{"succeeded": 3, "failed": 1},
			"listeners":   []any{map[string]any{"name": "web", "address": "[::]:443", "protocols": []string{"h1", "h2"}}},
			"connections": map[string]any{"open": 5, "refused": 2},
			"upstreams":   []any{map[string]any{"name": "u", "backends": []any{map[string]any{"up": true}, map[string]any{"up": false}}}},
			"certificates": []any{
				map[string]any{"listener": "web", "names": []string{"late.example"}, "not_after": "2030-01-01T00:00:00Z"},
				map[string]any{"listener": "web", "names": []string{"soon.example"}, "not_after": "2027-01-01T00:00:00Z"},
			},
			"pad": strings.Repeat("x", pad)})
	case "/versions":
		f.mu.Lock()
		cur := f.current()
		var vs []any
		for _, v := range f.hist {
			vs = append(vs, map[string]any{"version": v.n, "applied": "2026-11-05T00:00:00Z", "source": "console", "bytes": 10, "etag": v.etag})
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"current": cur.n, "versions": vs})
	case "/config/check":
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 7, "checked_at": "2026-11-06T00:00:02Z", "summary": map[string]any{"drift": 2, "info": 1}})
	default:
		w.WriteHeader(404)
	}
}

// memStore is a store's sections, in memory.
type memStore struct {
	mu       sync.Mutex
	sections map[string][]byte
	fail     error
	writes   int
}

func newMemStore() *memStore { return &memStore{sections: map[string][]byte{}} }

func (s *memStore) Section(name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.sections[name])
}

func (s *memStore) SetSection(name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.writes++
	s.sections[name] = bytes.Clone(data)
	return nil
}

// testEngine returns a running engine, stopped when the test ends.
func testEngine(t *testing.T) *fib.Engine {
	t.Helper()
	e, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = e.Run(); close(done) }()
	t.Cleanup(func() { e.Stop(); <-done; _ = e.Close() })
	return e
}

// logBuf keeps what a manager logs.
type logBuf struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBuf) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// fixture is a manager on a running engine, its store and its log.
type fixture struct {
	t      *testing.T
	engine *fib.Engine
	store  *memStore
	log    *logBuf
	m      *Manager
	set    config.Cluster
}

func newFixture(t *testing.T, set config.Cluster) *fixture {
	t.Helper()
	if set.PollInterval == 0 {
		set.PollInterval = config.Duration(50 * time.Millisecond)
	}
	if set.Timeout == 0 {
		set.Timeout = config.Duration(5 * time.Second)
	}
	f := &fixture{t: t, engine: testEngine(t), store: newMemStore(), log: &logBuf{}, set: set}
	f.start()
	return f
}

func (f *fixture) start() {
	f.t.Helper()
	m, err := New(Options{Engine: f.engine, Settings: &f.set, Store: f.store, Logf: f.log.logf})
	if err != nil {
		f.t.Fatal(err)
	}
	f.m = m
	f.t.Cleanup(m.Close)
}

// waitFor waits for cond, failing the test after a generous while.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("waited too long for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// state returns the member name's state.
func (f *fixture) state(name string) State {
	for _, s := range f.m.States() {
		if s.Name == name {
			return s
		}
	}
	f.t.Fatalf("no member %q", name)
	return State{}
}

// waitState waits for the member name's state to satisfy cond.
func (f *fixture) waitState(name, what string, cond func(State) bool) State {
	f.t.Helper()
	var st State
	waitFor(f.t, name+" "+what, func() bool { st = f.state(name); return cond(st) })
	return st
}

func (f *fixture) waitKind(name, kind string) State {
	f.t.Helper()
	return f.waitState(name, "to fail with "+kind, func(s State) bool { return s.Error != nil && s.Error.Kind == kind })
}

func (f *fixture) waitUp(name string) State {
	f.t.Helper()
	return f.waitState(name, "up", func(s State) bool { return s.Status == StatusUp })
}

// consoleInput is a member reached by its console.
func consoleInput(name, url string) *Input {
	return &Input{Name: name, URL: url, Access: AccessConsole, Username: fakeUser, Password: fakePassword}
}

// tokenInput is a member reached by its Admin API.
func tokenInput(name, url string) *Input {
	return &Input{Name: name, URL: url, Access: AccessAdminAPI, Token: fakeToken}
}

func (f *fixture) add(in *Input) {
	f.t.Helper()
	if _, err := f.m.Add("tester", in); err != nil {
		f.t.Fatal(err)
	}
}

// downURL is an http URL on loopback where nothing listens.
func downURL(t *testing.T) string {
	return "http://127.0.0.1:" + strconv.Itoa(pebbletest.FreePort(t))
}

// served is a console serving the manager's module, and a browser of it.
type served struct {
	t    *testing.T
	url  string
	c    *http.Client
	csrf string
	logs *logBuf
}

func (f *fixture) serve() *served {
	f.t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	logs := &logBuf{}
	c, err := console.Start(f.engine, console.Options{
		Config:  &config.RoleConsole{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "admin", PasswordHash: string(hash)}}},
		Cookie:  "fit_mgr",
		Pages:   fstest.MapFS{"index.html": {Data: []byte("<p>hi</p>")}},
		Logf:    logs.logf,
		Modules: func() []*console.Module { return []*console.Module{f.m.Module()} },
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Stop(f.t.Context()) })
	jar, _ := cookiejar.New(nil)
	s := &served{t: f.t, url: strings.TrimSuffix(c.URL(), "/"), c: &http.Client{Jar: jar, Timeout: 30 * time.Second}, logs: logs}
	code, body, _ := s.do("POST", "/api/login", `{"username":"admin","password":"pw"}`)
	if code != 200 {
		f.t.Fatalf("login: %d %s", code, body)
	}
	var b struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &b)
	s.csrf = b.CSRF
	return s
}

func (s *served) do(method, path, body string, header ...string) (int, string, http.Header) {
	s.t.Helper()
	req, _ := http.NewRequest(method, s.url+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.csrf != "" {
		req.Header.Set(console.CSRFHeader, s.csrf)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := s.c.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header
}

// js marshals v.
func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// newFakeMemberAt starts a plain fake member at addr, as one restarted
// there; nil when the address cannot be had again.
func newFakeMemberAt(t *testing.T, addr string) *fakeMember {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil
	}
	f := &fakeMember{id: "node-back", sessions: map[string]bool{}}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.srv.Listener.Close()
	f.srv.Listener = l
	f.srv.Start()
	t.Cleanup(f.srv.Close)
	return f
}

// serveOps answers the writes operations make: POST /reload, /rollback/{n}
// and /cache/purge; false for another path.
func (f *fakeMember) serveOps(w http.ResponseWriter, r *http.Request, path string) bool {
	if r.Method != "POST" || path != "/reload" && path != "/cache/purge" && !strings.HasPrefix(path, "/rollback/") {
		return false
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.opFail == http.StatusTeapot {
		w.WriteHeader(f.opFail)
		return true
	}
	if f.opFail != 0 {
		w.WriteHeader(f.opFail)
		_, _ = w.Write([]byte(`{"error":"the config was not applied; the running config stays","errors":[{"path":"/routes/0","message":"no"},{"message":"two"}]}`))
		return true
	}
	cur := f.current()
	switch {
	case path == "/reload":
		f.hist = append(f.hist, fakeVersion{cur.n + 1, cur.etag})
		fmt.Fprintf(w, `{"version":%d,"etag":%q}`, cur.n+1, cur.etag)
	case path == "/cache/purge":
		var b struct {
			Zone string
		}
		_ = json.Unmarshal(body, &b)
		if b.Zone != "" && !slices.Contains(f.zones, b.Zone) {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error":"no cache uses the zone \"%s\""}`, b.Zone)
			return true
		}
		f.purges = append(f.purges, string(body))
		_, _ = w.Write([]byte(`{"purged":3}`))
	default:
		n, _ := strconv.Atoi(strings.TrimPrefix(path, "/rollback/"))
		if im := r.Header.Get("If-Match"); im != "" && im != cur.etag {
			w.WriteHeader(412)
			_, _ = w.Write([]byte(`{"error":"the config has changed since the ETag given in If-Match"}`))
			return true
		}
		for _, v := range f.hist {
			if v.n == n {
				f.hist = append(f.hist, fakeVersion{cur.n + 1, v.etag})
				fmt.Fprintf(w, `{"version":%d,"etag":%q,"warnings":["w"]}`, cur.n+1, v.etag)
				return true
			}
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"version is not kept"}`))
	}
	return true
}
