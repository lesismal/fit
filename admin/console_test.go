package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
)

// testEngine returns a running fib engine, stopped when t ends.
func testEngine(t *testing.T) *fib.Engine {
	t.Helper()
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = engine.Run(); close(done) }()
	t.Cleanup(func() { engine.Stop(); <-done; _ = engine.Close() })
	return engine
}

// consoleFixture is a console in front of a fixture's Configs, with a
// clock a test moves.
type consoleFixture struct {
	*fixture
	console *Console
	url     string

	mu   sync.Mutex
	now  time.Time
	logs []string
}

func startConsole(t *testing.T, c *config.Console) *consoleFixture {
	t.Helper()
	f := &consoleFixture{fixture: start(t, doc("one"), &config.Admin{}), now: time.Now()}
	if c.Address == "" {
		c.Address = "127.0.0.1:0"
	}
	if c.Users == nil {
		hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
		c.Users = []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}
	}
	var err error
	f.console, err = StartConsole(testEngine(t), c, &config.Admin{}, f.configs, nil, nil, func(format string, args ...any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.logs = append(f.logs, strings.TrimSpace(fmtSprintf(format, args...)))
	})
	if err != nil {
		t.Fatal(err)
	}
	f.console.logins.SetClock(func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.now
	})
	t.Cleanup(func() { _ = f.console.Shutdown(context.Background()) })
	f.url = strings.TrimSuffix(f.console.URL(), "/")
	return f
}

func (f *consoleFixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *consoleFixture) logged(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.logs {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// logLines is a copy of the logs so far.
func (f *consoleFixture) logLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.logs)
}

// browser is a client with cookies, as a browser on the console's pages is.
type browser struct {
	t    *testing.T
	f    *consoleFixture
	c    *http.Client
	csrf string
}

func (f *consoleFixture) browser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, f: f, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (b *browser) do(method, path, body string, header ...string) (int, string, http.Header) {
	b.t.Helper()
	req, _ := http.NewRequest(method, b.f.url+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.csrf != "" {
		req.Header.Set(csrfHeader, b.csrf)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header
}

func (b *browser) login(user, password string) int {
	b.t.Helper()
	code, body, _ := b.do("POST", "/api/login", `{"username": "`+user+`", "password": "`+password+`"}`)
	if code == http.StatusOK {
		var s struct{ CSRF string }
		_ = json.Unmarshal([]byte(body), &s)
		b.csrf = s.CSRF
	}
	return code
}

func TestConsoleLoginAndEdit(t *testing.T) {
	f := startConsole(t, &config.Console{})
	b := f.browser(t)

	// The pages are served to anyone; the API to a session only.
	if code, body, h := b.do("GET", "/", ""); code != http.StatusOK || !strings.Contains(body, "<!doctype html>") ||
		!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("the page: %d %q %v", code, body, h)
	}
	for _, path := range []string{"/api/session", "/api/m/admin/schema", "/api/m/admin/config"} {
		if code, _, _ := b.do("GET", path, ""); code != http.StatusUnauthorized {
			t.Errorf("GET %s without a login: %d", path, code)
		}
	}
	if code := b.login("ada", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("a wrong password: %d", code)
	}
	if code := b.login("nobody", "pw"); code != http.StatusUnauthorized {
		t.Fatalf("an unknown user: %d", code)
	}
	if code := b.login("ada", "pw"); code != http.StatusOK || b.csrf == "" {
		t.Fatalf("login: %d", code)
	}
	if !f.logged(`"ada" logged in`) || !f.logged(`a failed login as "nobody"`) {
		t.Errorf("logs: %q", f.logLines())
	}

	// Reads.
	code, body, h := b.do("GET", "/api/m/admin/config", "")
	if code != http.StatusOK || !strings.Contains(body, `"one"`) || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("config: %d %s %v", code, body, h)
	}
	etag := h.Get("ETag")
	if code, body, _ := b.do("GET", "/api/m/admin/schema", ""); code != http.StatusOK || !strings.Contains(body, `"$defs"`) || !strings.Contains(body, `"console"`) {
		t.Errorf("schema: %d %.200s", code, body)
	}

	// A change without the CSRF token is refused; with it, applied.
	csrf := b.csrf
	b.csrf = ""
	if code, _, _ := b.do("PUT", "/api/m/admin/config", doc("two")); code != http.StatusForbidden {
		t.Errorf("without the CSRF token: %d", code)
	}
	b.csrf = csrf
	if code, body, _ := b.do("PUT", "/api/m/admin/config", doc("two"), "If-Match", etag); code != http.StatusOK {
		t.Fatalf("put: %d %s", code, body)
	}
	if got := f.served(t); got != "two" {
		t.Errorf("served: %q", got)
	}
	if v := f.configs.Current(); v.Source != SourceConsole {
		t.Errorf("source: %q", v.Source)
	}
	if !f.logged(`"ada": PUT /config applied config version`) {
		t.Errorf("logs: %q", f.logLines())
	}
	// A stale ETag, and a config that does not load.
	if code, _, _ := b.do("PUT", "/api/m/admin/config", doc("three"), "If-Match", etag); code != http.StatusPreconditionFailed {
		t.Errorf("a stale ETag: %d", code)
	}
	if code, body, _ := b.do("PUT", "/api/m/admin/config", `{"listeners": []}`); code != http.StatusBadRequest || !strings.Contains(body, "at least one listener") {
		t.Errorf("a bad config: %d %s", code, body)
	}

	// Another page's request is refused, even with the cookie.
	if code, _, _ := b.do("GET", "/api/m/admin/config", "", "Origin", "http://evil.example"); code != http.StatusForbidden {
		t.Errorf("another origin: %d", code)
	}
	if code, _, _ := b.do("GET", "/api/m/admin/config", "", "Origin", f.url); code != http.StatusOK {
		t.Errorf("the console's own origin: %d", code)
	}

	// Logging out ends the session.
	if code, _, _ := b.do("POST", "/api/logout", ""); code != http.StatusNoContent {
		t.Errorf("logout: %d", code)
	}
	if code, _, _ := b.do("GET", "/api/session", ""); code != http.StatusUnauthorized {
		t.Errorf("after logout: %d", code)
	}
}

func TestConsoleLoginRefused(t *testing.T) {
	f := startConsole(t, &config.Console{})
	b := f.browser(t)
	// A form a page could post: not JSON.
	req, _ := http.NewRequest("POST", f.url+"/api/login", strings.NewReader("username=ada&password=pw"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := b.c.Do(req); err != nil || resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("a form: %v %v", resp, err)
	}
	// Five failures lock the address out, the right password too, until
	// the lockout ends.
	for range login.MaxFailures {
		if code := b.login("ada", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("failure: %d", code)
		}
	}
	if code := b.login("ada", "pw"); code != http.StatusTooManyRequests {
		t.Errorf("locked out: %d", code)
	}
	f.advance(login.Lockout + time.Second)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Errorf("after the lockout: %d", code)
	}
}

func TestConsoleSessions(t *testing.T) {
	f := startConsole(t, &config.Console{SessionTimeout: config.Duration(time.Minute), SessionLifetime: config.Duration(3 * time.Minute), MaxSessions: 2})
	b := f.browser(t)
	if b.login("ada", "pw") != http.StatusOK {
		t.Fatal("login")
	}
	// Kept alive by use, up to its lifetime.
	for range 3 {
		f.advance(50 * time.Second)
		if code, _, _ := b.do("GET", "/api/session", ""); code != http.StatusOK {
			t.Fatalf("in use: %d", code)
		}
	}
	f.advance(50 * time.Second)
	if code, _, _ := b.do("GET", "/api/session", ""); code != http.StatusUnauthorized {
		t.Errorf("past its lifetime: %d", code)
	}
	// Idle.
	b.login("ada", "pw")
	f.advance(61 * time.Second)
	if code, _, _ := b.do("GET", "/api/session", ""); code != http.StatusUnauthorized {
		t.Errorf("idle: %d", code)
	}
	// A third login ends the oldest.
	first, second, third := f.browser(t), f.browser(t), f.browser(t)
	first.login("ada", "pw")
	f.advance(time.Second)
	second.login("ada", "pw")
	f.advance(time.Second)
	third.login("ada", "pw")
	for i, br := range []*browser{first, second, third} {
		want := http.StatusOK
		if i == 0 {
			want = http.StatusUnauthorized
		}
		if code, _, _ := br.do("GET", "/api/session", ""); code != want {
			t.Errorf("session %d: %d", i, code)
		}
	}
	// A cookie made up is no session.
	b.c.Jar.SetCookies(mustURL(f.url), []*http.Cookie{{Name: login.CookieName(sessionCookie, f.console.Addr()), Value: "made-up"}})
	if code, _, _ := b.do("GET", "/api/session", ""); code != http.StatusUnauthorized {
		t.Errorf("a made-up cookie: %d", code)
	}
}

func TestConsoleFailureTableBounded(t *testing.T) {
	f := startConsole(t, &config.Console{})
	for i := range login.MaxAddresses + 10 {
		f.console.logins.Failed(fmtSprintf("10.0.%d.%d", i/256, i%256))
	}
	if n := f.console.logins.Addresses(); n > login.MaxAddresses {
		t.Errorf("%d addresses kept", n)
	}
}

func fmtSprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// The console shows the config's module and the roles' beside it, each
// role's API under /api/m/<role>/.
func TestConsoleModules(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	role := &console.Module{ID: "llm.hub", Routes: func(api *console.API) {
		api.Handle("GET", "/api/me", func(r *console.Request) { _ = r.JSON(http.StatusOK, map[string]string{"role": "hub"}) })
	}}
	cs, err := StartConsole(testEngine(t), c, &config.Admin{}, f.configs, func() []*console.Module { return []*console.Module{role} }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Shutdown(context.Background())
	cf := &consoleFixture{fixture: f, console: cs, url: strings.TrimSuffix(cs.URL(), "/")}
	b := cf.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	if code, body, _ := b.do("GET", "/api/workbench", ""); code != http.StatusOK || body != `{"user":"ada","modules":["admin","llm.hub"]}` {
		t.Fatalf("workbench: %d %s", code, body)
	}
	if code, body, _ := b.do("GET", "/api/m/llm.hub/me", ""); code != http.StatusOK || body != `{"role":"hub"}` {
		t.Fatalf("the role's API: %d %s", code, body)
	}
	if code, body, h := b.do("GET", "/api/m/admin/versions", ""); code != http.StatusOK || !strings.Contains(h.Get("Content-Type"), "json") {
		t.Fatalf("versions: %d %s %v", code, body, h)
	}
}
