package admin

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
)

// testUsers is the console user ada, whose password is pw.
func testUsers() []config.ConsoleUser {
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	return []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}
}

// dryAnswer is a dry run's answer.
type dryAnswer struct {
	DryRun    bool   `json:"dry_run"`
	Version   uint64 `json:"version"`
	ETag      string `json:"etag"`
	NewETag   string `json:"new_etag"`
	Unchanged bool   `json:"unchanged"`
	Added     []struct {
		Name, Address string
	} `json:"listeners_added"`
	Removed []struct {
		Name, Address string
	} `json:"listeners_removed"`
	Warnings []string `json:"warnings"`
}

// unchanged fails t unless f still runs version, served answers body, and
// nothing was applied, kept or counted since the reloads were zeroed.
func (f *fixture) unchanged(t *testing.T, what string, v *Version, body string, saved *atomic.Int32) {
	t.Helper()
	cur := f.configs.Current()
	if cur != v || len(f.configs.Versions()) != 1 {
		t.Errorf("%s: the running config changed: version %d, %d versions", what, cur.Number, len(f.configs.Versions()))
	}
	if tried, failed := f.configs.Reloads(); tried != 0 || failed != 0 {
		t.Errorf("%s: reloads counted: %d, %d failed", what, tried, failed)
	}
	f.configs.reloads.Store(0)
	f.configs.failures.Store(0)
	if saved.Load() != 0 {
		t.Errorf("%s: the store was written", what)
	}
	if got := f.served(t); got != body {
		t.Errorf("%s: served %q", what, got)
	}
}

// TestDryRun: ?dry_run=1 checks a config through the whole apply path and
// answers as an apply would, with the same errors, leaving the version, the
// config and the store as they were.
func TestDryRun(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	var saved atomic.Int32
	f.configs.UseStore(nil, func([]byte) error { saved.Add(1); return nil })
	v := f.configs.Current()

	// A whole config that loads: answered with the running version and
	// ETag, the ETag it would have, and the listener it would add.
	two := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}, {"name": "more", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "two"}}}], "access_log": {"output": "stderr"}}`
	resp, body := f.send(t, "PUT", "/config?dry_run=1", "application/json", two, v.ETag)
	var a dryAnswer
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &a) != nil || !a.DryRun || a.Version != v.Number ||
		a.ETag != v.ETag || a.NewETag == "" || a.NewETag == v.ETag || a.Unchanged {
		t.Fatalf("a dry run: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get(DryRunHeader) != "1" || resp.Header.Get("ETag") != v.ETag || resp.Header.Get(VersionHeader) != "1" {
		t.Errorf("a dry run's headers: %v", resp.Header)
	}
	if len(a.Added) != 1 || a.Added[0].Name != "more" || a.Added[0].Address != "127.0.0.1:0" || len(a.Removed) != 0 ||
		len(a.Warnings) != 1 || !strings.Contains(a.Warnings[0], "access_log changed") {
		t.Errorf("a dry run's listeners and warnings: %s", body)
	}
	f.unchanged(t, "PUT", v, "one", &saved)

	// The running config itself.
	_, body = f.send(t, "POST", "/load?dry_run=true", "application/json", doc("one"), "")
	if a = (dryAnswer{}); json.Unmarshal([]byte(body), &a) != nil || !a.Unchanged || a.NewETag != v.ETag {
		t.Errorf("the running config, checked: %s", body)
	}

	// A config that does not load, or does not compile, is refused as an
	// apply refuses it, word for word.
	for _, bad := range []string{
		`{"listeners": []}`,
		`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "nope"}}]}`,
		`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"status": "x"}}}]}`,
		`{"listeners": [`,
	} {
		dry, dryBody := f.send(t, "PUT", "/config?dry_run=1", "application/json", bad, "")
		f.unchanged(t, "a bad config, checked", v, "one", &saved)
		applied, appliedBody := f.send(t, "PUT", "/config", "application/json", bad, "")
		f.configs.reloads.Store(0)
		f.configs.failures.Store(0)
		if dry.StatusCode != http.StatusBadRequest || dry.StatusCode != applied.StatusCode || dryBody != appliedBody {
			t.Errorf("%s: a dry run answers %d %s, an apply %d %s", bad, dry.StatusCode, dryBody, applied.StatusCode, appliedBody)
		}
		if dry.Header.Get(DryRunHeader) != "" {
			t.Errorf("a refused dry run says it passed")
		}
	}
	// A stale ETag: 412, as an apply.
	if resp, body := f.send(t, "PUT", "/config?dry_run=1", "application/json", doc("x"), `"stale"`); resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("a stale ETag: %d %s", resp.StatusCode, body)
	}
	// An address another socket holds: refused as the apply's bind is.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	clash := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}, {"name": "clash", "address": "` + taken.Addr().String() + `"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "two"}}}]}`
	dry, dryBody := f.send(t, "PUT", "/config?dry_run=1", "application/json", clash, "")
	applied, appliedBody := f.send(t, "PUT", "/config", "application/json", clash, "")
	if dry.StatusCode == http.StatusOK || dry.StatusCode != applied.StatusCode || dryBody != appliedBody || !strings.Contains(dryBody, `listener \"clash\"`) {
		t.Errorf("a taken address: a dry run answers %d %s, an apply %d %s", dry.StatusCode, dryBody, applied.StatusCode, appliedBody)
	}
	// A dry_run that is neither yes nor no.
	for _, m := range []string{"PUT /config", "PATCH /config", "DELETE /config/routes/0", "POST /load", "PUT /config/text?format=json"} {
		method, path, _ := strings.Cut(m, " ")
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		if resp, body := f.send(t, method, path+sep+"dry_run=maybe", "application/json", "{}", ""); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "dry_run") {
			t.Errorf("%s with dry_run=maybe: %d %s", m, resp.StatusCode, body)
		}
	}
	saved.Store(0)
	f.configs.reloads.Store(0)
	f.configs.failures.Store(0)
	v = f.configs.Current()
	if v.Number != 1 {
		t.Fatalf("version %d", v.Number)
	}

	// PATCH, DELETE and the text, checked and not applied.
	for _, c := range []struct{ method, path, ct, body string }{
		{"PATCH", "/config/routes/0/handler/params?dry_run=1", "application/merge-patch+json", `{"body": "patched"}`},
		{"PUT", "/config/routes/-?dry_run=1", "application/json", `{"handler": {"module": "respond"}}`},
		{"DELETE", "/config/routes/0?dry_run=1", "", ""},
		{"PUT", "/config/text?format=json&members=routes&dry_run=1", "text/plain", `{"routes": [{"name": "r", "handler": {"module": "respond", "params": {"body": "text"}}}]}`},
		{"PUT", "/config/text?format=conf&dry_run=1", "text/plain", "listener web 127.0.0.1:0\nroute r { handler respond { body conf } }\n"},
	} {
		resp, body := f.send(t, c.method, c.path, c.ct, c.body, v.ETag)
		a = dryAnswer{}
		if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &a) != nil || !a.DryRun || a.ETag != v.ETag || a.Unchanged {
			t.Errorf("%s %s: %d %s", c.method, c.path, resp.StatusCode, body)
		}
		f.unchanged(t, c.method+" "+c.path, v, "one", &saved)
	}
	// The text's errors carry its lines, as an apply's do.
	bad := "listener web 127.0.0.1:0\nroute r { handler nope }\n"
	dry, dryBody = f.send(t, "PUT", "/config/text?format=conf&dry_run=1", "text/plain", bad, "")
	applied, appliedBody = f.send(t, "PUT", "/config/text?format=conf", "text/plain", bad, "")
	if dry.StatusCode != http.StatusBadRequest || dryBody != appliedBody || !strings.Contains(dryBody, `"line":2`) {
		t.Errorf("a bad text: a dry run answers %d %s, an apply %d %s", dry.StatusCode, dryBody, applied.StatusCode, appliedBody)
	}

	// Then the same change applied: the dry run's new ETag is the one it
	// gets.
	_, body = f.send(t, "PUT", "/config?dry_run=1", "application/json", doc("three"), v.ETag)
	a = dryAnswer{}
	_ = json.Unmarshal([]byte(body), &a)
	resp, _ = f.send(t, "PUT", "/config", "application/json", doc("three"), v.ETag)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != a.NewETag || f.served(t) != "three" || saved.Load() != 1 {
		t.Errorf("applied after the dry run: %d %v, want ETag %s", resp.StatusCode, resp.Header, a.NewETag)
	}
}

// TestDryRunRemovesListener: a dry run lists the listeners an apply would
// take away, and frees nothing.
func TestDryRunRemovesListener(t *testing.T) {
	f := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}, {"name": "old", "address": "127.0.0.1:0", "protocols": ["http/1.1"]}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "one"}}}]}`, &config.Admin{})
	resp, body := f.send(t, "PUT", "/config?dry_run=1", "application/json", doc("one"), "")
	var a dryAnswer
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &a) != nil || len(a.Removed) != 1 || a.Removed[0].Name != "old" || len(a.Added) != 0 {
		t.Fatalf("a dry run that removes a listener: %d %s", resp.StatusCode, body)
	}
	if n := len(f.server.Addrs()); n != 2 {
		t.Errorf("%d listeners after the dry run", n)
	}
}

// TestConsoleProgram: a program, not a browser, logs in to the console, reads
// the config and its ETag, checks a change, and applies it with the CSRF
// token the login answered; the dry run is logged as one.
func TestConsoleProgram(t *testing.T) {
	f := startConsole(t, &config.Console{})
	b := f.browser(t)
	code, body, _ := b.do("POST", "/api/login", `{"username": "ada", "password": "pw"}`)
	var s struct {
		User string `json:"user"`
		CSRF string `json:"csrf"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &s) != nil || s.User != "ada" || s.CSRF == "" {
		t.Fatalf("login: %d %s", code, body)
	}
	if code, body, _ := b.do("GET", "/api/session", ""); code != http.StatusOK || !strings.Contains(body, `"csrf":"`+s.CSRF+`"`) {
		t.Errorf("the session's CSRF token: %d %s", code, body)
	}
	b.csrf = s.CSRF
	code, _, h := b.do("GET", "/api/m/admin/config", "")
	etag := h.Get("ETag")
	if code != http.StatusOK || etag == "" {
		t.Fatalf("read: %d %v", code, h)
	}
	code, body, h = b.do("PUT", "/api/m/admin/config?dry_run=1", doc("two"), "If-Match", etag)
	if code != http.StatusOK || h.Get(DryRunHeader) != "1" || !strings.Contains(body, `"dry_run":true`) || f.served(t) != "one" {
		t.Fatalf("a dry run: %d %s", code, body)
	}
	if !f.logged(`"ada": PUT /config?dry_run=1: 200, checked and not applied`) || f.logged("applied config version 2") {
		t.Errorf("logs: %q", f.logLines())
	}
	if code, body, _ := b.do("PUT", "/api/m/admin/config", doc("two"), "If-Match", etag); code != http.StatusOK || f.served(t) != "two" {
		t.Fatalf("apply: %d %s", code, body)
	}
}

// TestConsoleLoginUserBackoff: a user that failed login.UserFailures times
// in a row, from wherever, is refused a login for a while, with a 429 a
// program can read; the right password works once the wait is over.
func TestConsoleLoginUserBackoff(t *testing.T) {
	f := startConsole(t, &config.Console{})
	l := f.console.logins
	for i := range login.UserFailures {
		tk, _, _, _ := l.AdmitUser("10.0.0."+string(rune('1'+i%5)), "ada")
		if tk == nil {
			t.Fatalf("failure %d not admitted", i+1)
		}
		tk.Done(true)
	}
	b := f.browser(t)
	code, body, h := b.do("POST", "/api/login", `{"username": "ada", "password": "pw"}`)
	var r struct {
		Error      string `json:"error"`
		RetryAfter int    `json:"retry_after"`
	}
	if code != http.StatusTooManyRequests || json.Unmarshal([]byte(body), &r) != nil || !strings.Contains(r.Error, "as this user") ||
		r.RetryAfter < 1 || h.Get("Retry-After") != "3" || r.RetryAfter != 3 {
		t.Fatalf("slowed: %d %s %v", code, body, h)
	}
	f.advance(login.UserBackoff)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("after the wait: %d", code)
	}
	// The right password ended the count: one more failure is not slowed.
	if code := f.browser(t).login("ada", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("a failure after the count ended: %d", code)
	}
	if code := f.browser(t).login("ada", "pw"); code != http.StatusOK {
		t.Errorf("a login after one failure: %d", code)
	}
}

// TestDryRunDeprecated: a dry run reports the renamed names a config uses,
// as an apply's warnings do.
func TestDryRunDeprecated(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	renamed := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"middleware": [{"module": "cache", "params": {"zone": "dry", "max_size": 1048576}}], "handler": {"module": "respond"}}]}`
	resp, body := f.send(t, "PUT", "/config?dry_run=1", "application/json", renamed, "")
	var a dryAnswer
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &a) != nil || len(a.Warnings) != 1 ||
		!strings.Contains(a.Warnings[0], `deprecated: renamed "max_bytes"`) {
		t.Fatalf("a renamed name, checked: %d %s", resp.StatusCode, body)
	}
}

// TestAccountPasswordUserSlowed: a password change is refused while its
// user's checks are slowed, as a login is.
func TestAccountPasswordUserSlowed(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	for i := range login.UserFailures {
		tk, _, _, _ := f.console.logins.AdmitUser("10.0.9."+string(rune('1'+i%3)), "ada")
		if tk == nil {
			t.Fatalf("failure %d not admitted", i+1)
		}
		tk.Done(true)
	}
	code, body, h := b.do("POST", accountPath, `{"current_password": "pw", "password": "new"}`)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "of this user") || h.Get("Retry-After") == "" {
		t.Errorf("a change while the user is slowed: %d %s %v", code, body, h)
	}
}

// TestPutWholeConfigWithAdmin: a manager's rollout (I146) puts a member's
// whole config back with its changes, as GET /config answered it: on a fit
// without a store that includes admin, which is taken, by a dry run and an
// apply alike, with no warning when it is unchanged; a changed admin is
// applied with the warning that it takes effect at a restart.
func TestPutWholeConfigWithAdmin(t *testing.T) {
	f := start(t, `{"admin": {"address": "127.0.0.1:0", "history": 4}, "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "one"}}}]}`, &config.Admin{})
	resp, body := f.get(t, "/config")
	if resp.StatusCode != 200 || !strings.Contains(body, `"admin"`) {
		t.Fatalf("GET /config: %d %s", resp.StatusCode, body)
	}
	next := strings.Replace(body, `"one"`, `"two"`, 1)
	v := f.configs.Current()
	resp, out := f.send(t, "PUT", "/config?dry_run=1", "application/json", next, v.ETag)
	var a dryAnswer
	if resp.StatusCode != 200 || json.Unmarshal([]byte(out), &a) != nil || !a.DryRun || len(a.Warnings) != 0 {
		t.Fatalf("a dry run of the config with its admin: %d %s", resp.StatusCode, out)
	}
	resp, out = f.send(t, "PUT", "/config", "application/json", next, v.ETag)
	if resp.StatusCode != 200 || strings.Contains(out, "warnings") || f.served(t) != "two" {
		t.Fatalf("the config with its admin: %d %s", resp.StatusCode, out)
	}
	if got := f.configs.Current().ETag; got != a.NewETag {
		t.Errorf("applied as %s, the dry run said %s", got, a.NewETag)
	}
	changed := strings.Replace(next, `"history": 4`, `"history": 5`, 1)
	resp, out = f.send(t, "PUT", "/config", "application/json", changed, f.configs.Current().ETag)
	if resp.StatusCode != 200 || !strings.Contains(out, "admin changed") {
		t.Fatalf("a changed admin: %d %s", resp.StatusCode, out)
	}
}
