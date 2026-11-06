package cluster

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/config"
)

// passFake is a fakeMember that also serves what the node switcher passes
// on: a config with its ETag, a schema, the console's modules and a role's,
// and answers that test what comes back.
type passFake struct {
	*fakeMember
	mu      sync.Mutex
	version int
	config  string
	seen    []*http.Request // the requests of its own paths, as they came
	bodies  []string
}

const passConfig = `{"routes":[{"name":"r"}]}`

func newPassFake(t *testing.T) *passFake {
	t.Helper()
	p := &passFake{fakeMember: &fakeMember{id: "node-pass", sessions: map[string]bool{}}, version: 1, config: passConfig}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serveAll))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *passFake) etag() string { return `"v` + strconv.Itoa(p.version) + `"` }

// own is the Admin API's path of a request to one of the fake's own paths,
// and whether its credentials hold: through the console with a session, or
// with the token.
func (p *passFake) own(r *http.Request) (string, bool, bool) {
	path := r.URL.Path
	rest, viaConsole := strings.CutPrefix(path, "/api/m/admin")
	if !viaConsole && strings.HasPrefix(path, "/api/") && path != "/api/login" && path != "/api/logout" && path != "/api/node" {
		rest, viaConsole = path, true
	}
	switch rest {
	case "/config", "/schema", "/big", "/html", "/echo", "/forbid", "/users", "/api/workbench", "/api/m/tunnel.hub/status", "/api/m/tunnel.hub/listeners":
	default:
		return "", false, false
	}
	if !viaConsole {
		return rest, true, r.Header.Get("Authorization") == "Bearer "+fakeToken
	}
	c, err := r.Cookie("fit_console_2020")
	p.fakeMember.mu.Lock()
	ok := err == nil && p.sessions[c.Value] && !p.reject
	p.fakeMember.mu.Unlock()
	if ok && r.Method != "GET" && r.Header.Get("X-Fit-Csrf") != "csrf-"+c.Value {
		ok = false
	}
	return rest, true, ok
}

func (p *passFake) serveAll(w http.ResponseWriter, r *http.Request) {
	path, mine, ok := p.own(r)
	if !mine {
		p.serve(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, r.Clone(r.Context()))
	p.bodies = append(p.bodies, string(body))
	w.Header().Set("Set-Cookie", "evil=1; Path=/")
	w.Header().Set("X-Evil", "1")
	w.Header().Set("Content-Security-Policy", "default-src *")
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"log in first"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch path {
	case "/config":
		switch {
		case r.Method == "GET" && r.Header.Get("If-None-Match") == p.etag():
			w.WriteHeader(304)
			return
		case r.Method == "GET":
			w.Header().Set("ETag", p.etag())
			w.Header().Set("Fit-Config-Version", strconv.Itoa(p.version))
			_, _ = w.Write([]byte(p.config))
		case r.Header.Get("If-Match") != "" && r.Header.Get("If-Match") != p.etag():
			w.WriteHeader(412)
			_, _ = w.Write([]byte(`{"error":"the config has changed since the ETag given in If-Match"}`))
		case r.URL.Query().Get("dry_run") == "1":
			w.Header().Set("Fit-Dry-Run", "1")
			_, _ = w.Write([]byte(`{"dry_run":true}`))
		case strings.Contains(string(body), "bad"):
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"the config was not applied","errors":[{"path":"routes[0].name","message":"bad"}]}`))
		default:
			p.version++
			p.config = string(body)
			w.Header().Set("ETag", p.etag())
			w.Header().Set("Fit-Config-Version", strconv.Itoa(p.version))
			_, _ = w.Write([]byte(`{"version":` + strconv.Itoa(p.version) + `}`))
		}
	case "/schema":
		w.Header().Set("Content-Type", "application/schema+json")
		_, _ = w.Write([]byte(`{"title":"the member's schema"}`))
	case "/big":
		_, _ = w.Write([]byte(`"` + strings.Repeat("x", 40<<10) + `"`))
	case "/html":
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Disposition", `attachment; filename="x.html"`)
		_, _ = w.Write([]byte(`<script>alert(1)</script>`))
	case "/echo":
		w.Header().Set("Content-Disposition", "inline")
		_, _ = w.Write([]byte(`{}`))
	case "/users":
		_, _ = w.Write([]byte(`{"users":[]}`))
	case "/forbid":
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"no"}`))
	case "/api/workbench":
		_, _ = w.Write([]byte(`{"user":"ops","modules":["admin","ai","cluster","llm.hub","tunnel.hub"]}`))
	case "/api/m/tunnel.hub/status":
		_, _ = w.Write([]byte(`{"name":"hub"}`))
	case "/api/m/tunnel.hub/listeners":
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

// last returns the latest request of the fake's own paths, and its body.
func (p *passFake) last() (*http.Request, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) == 0 {
		return nil, ""
	}
	return p.seen[len(p.seen)-1], p.bodies[len(p.bodies)-1]
}

func (p *passFake) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}

// matchDigest has the manager take the fakes' schema as its own, so that
// their pages may be written.
func (f *fixture) matchDigest(d string) {
	f.m.pass.mu.Lock()
	f.m.pass.digest = d
	f.m.pass.mu.Unlock()
}

// audits returns the audit log's entries of the switcher, once n of them
// are written.
func (f *fixture) audits(n int) []AuditEntry {
	f.t.Helper()
	var out []AuditEntry
	waitFor(f.t, strconv.Itoa(n)+" audit entries of the switcher", func() bool {
		out = out[:0]
		for _, e := range f.m.Audit() {
			if e.Action == ActionAPI {
				out = append(out, e)
			}
		}
		return len(out) >= n
	})
	return out
}

// TestPassThrough opens members' pages through the manager, by console and
// by token: reads, writes with If-Match, dry runs and the text conversions,
// their answers' headers, and the audit of every write with its user.
func TestPassThrough(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	f.matchDigest("sha256:00")
	s := f.serve()
	pc, pt := newPassFake(t), newPassFake(t)
	f.add(consoleInput("c", pc.srv.URL))
	f.add(tokenInput("k", pt.srv.URL))
	f.waitUp("c")
	f.waitUp("k")
	writes := func() int { f.store.mu.Lock(); defer f.store.mu.Unlock(); return f.store.writes }
	before := writes()
	members := func() string { _, _, h := s.do("GET", "/api/m/cluster/nodes", ""); return h.Get("ETag") }
	tag := members()

	for _, x := range []struct {
		name string
		p    *passFake
	}{{"c", pc}, {"k", pt}} {
		base := "/api/n/" + x.name + "/m/admin"
		code, body, h := s.do("GET", base+"/config", "", "X-Custom", "1", "Cookie", "fit_mgr=stolen")
		if code != 200 || body != passConfig || h.Get("ETag") != `"v1"` || h.Get("Fit-Config-Version") != "1" || h.Get("Content-Type") != "application/json" {
			t.Fatalf("%s: %d %s %v", x.name, code, body, h)
		}
		if h.Get("Set-Cookie") != "" || h.Get("X-Evil") != "" || h.Get("Content-Security-Policy") == "default-src *" || h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: the member's headers came through: %v", x.name, h)
		}
		r, _ := x.p.last()
		if r.Header.Get("X-Custom") != "" || strings.Contains(r.Header.Get("Cookie"), "fit_mgr") || r.Header.Get("User-Agent") != "fit-cluster-manager" {
			t.Errorf("%s: the browser's headers reached the member: %v", x.name, r.Header)
		}
		if code, _, _ := s.do("GET", base+"/config", "", "If-None-Match", `"v1"`); code != 304 {
			t.Errorf("%s: %d", x.name, code)
		}

		// A write with the ETag read, then one with it stale.
		if code, body, h := s.do("PUT", base+"/config", `{"routes":[]}`, "If-Match", `"v1"`); code != 200 || h.Get("Fit-Config-Version") != "2" || h.Get("ETag") != `"v2"` {
			t.Fatalf("%s: %d %s", x.name, code, body)
		}
		if r, b := x.p.last(); r.Header.Get("If-Match") != `"v1"` || b != `{"routes":[]}` || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: %v %s", x.name, r.Header, b)
		}
		if code, body, _ := s.do("PUT", base+"/config", `{"routes":[]}`, "If-Match", `"v1"`); code != 412 || !strings.Contains(body, "changed since") {
			t.Errorf("%s: %d %s", x.name, code, body)
		}
		// A save's errors come as the member answered them, to link to
		// their fields.
		if code, body, _ := s.do("PUT", base+"/config", `{"bad":1}`); code != 400 || !strings.Contains(body, `"path":"routes[0].name"`) {
			t.Errorf("%s: %d %s", x.name, code, body)
		}
		// A dry run and a conversion change nothing, and are not
		// audited; the query reaches the member.
		if code, body, h := s.do("PUT", base+"/config?dry_run=1", `{"routes":[]}`); code != 200 || h.Get("Fit-Dry-Run") != "1" {
			t.Errorf("%s: %d %s", x.name, code, body)
		}
		if r, _ := x.p.last(); r.URL.RawQuery != "dry_run=1" {
			t.Errorf("%s: %q", x.name, r.URL.RawQuery)
		}
	}
	got := f.audits(6)
	if len(got) != 6 {
		t.Fatalf("%+v", got)
	}
	for i, want := range []string{"PUT /config: 200, config version 2", "PUT /config: 412", "PUT /config: 400"} {
		for j, name := range []string{"c", "k"} {
			e := got[j*3+i]
			if e.User != "admin" || e.Node != name || e.Detail != want || e.At.IsZero() {
				t.Errorf("%d: %+v", j*3+i, e)
			}
		}
	}
	if writes() == before {
		t.Error("the audit log was not written to the store")
	}
	if members() != tag {
		t.Error("a write through the switcher changed the members' ETag")
	}
	if !strings.Contains(f.log.String(), `"admin", through the node switcher, on the member "c": PUT /config: 200, config version 2`) {
		t.Errorf("%s", f.log)
	}

	// The node, the schema: the console's from the member, the token's
	// from the manager when the member serves none, as the pages are
	// then read-only anyway.
	if code, body, _ := s.do("GET", "/api/n/c/node", ""); code != 200 || !strings.Contains(body, `"id":"node-pass"`) {
		t.Errorf("%d %s", code, body)
	}
	if code, body, h := s.do("GET", "/api/n/c/m/admin/schema", ""); code != 200 || body != `{"title":"the member's schema"}` || h.Get("Content-Type") != "application/schema+json" {
		t.Errorf("%d %s %v", code, body, h)
	}
	n := pt.count()
	if code, body, h := s.do("GET", "/api/n/k/m/admin/schema", ""); code != 200 || !strings.Contains(body, `"$defs"`) || h.Get(SchemaFromHeader) != "manager" || pt.count() != n {
		t.Errorf("%d %.80s %v", code, body, h)
	}
	pt.set(func(f *fakeMember) { f.caps = []string{"node", CapSchema} })
	f.waitState("k", "to serve its schema", func(s State) bool { return s.Node != nil && len(s.Node.Capabilities) == 2 })
	if code, body, _ := s.do("GET", "/api/n/k/m/admin/schema", ""); code != 200 || body != `{"title":"the member's schema"}` {
		t.Errorf("%d %.80s", code, body)
	}

	// What comes back: a type a browser would render is not, an
	// attachment stays one, other dispositions go.
	if code, body, h := s.do("GET", "/api/n/c/m/admin/html", ""); code != 200 || h.Get("Content-Type") != "application/octet-stream" ||
		h.Get("Content-Disposition") != `attachment; filename="x.html"` || body != `<script>alert(1)</script>` {
		t.Errorf("%d %s %v", code, body, h)
	}
	if _, _, h := s.do("GET", "/api/n/c/m/admin/echo", ""); h.Get("Content-Disposition") != "" {
		t.Errorf("%v", h)
	}

	// A role's module through the member's console; not through a token.
	if code, body, _ := s.do("GET", "/api/n/c/m/tunnel.hub/status", ""); code != 200 || body != `{"name":"hub"}` {
		t.Errorf("%d %s", code, body)
	}
	if code, body, _ := s.do("POST", "/api/n/c/m/tunnel.hub/listeners", `{}`); code != 200 {
		t.Errorf("%d %s", code, body)
	}
	if e := f.audits(7)[6]; e.Detail != "POST /api/m/tunnel.hub/listeners: 200" {
		t.Errorf("%+v", e)
	}
	if code, body, _ := s.do("GET", "/api/n/k/m/tunnel.hub/status", ""); code != 404 || !strings.Contains(body, "through its console") {
		t.Errorf("%d %s", code, body)
	}

	// No credential anywhere.
	for _, l := range []string{f.log.String(), s.logs.String(), js(f.m.Audit())} {
		if strings.Contains(l, fakePassword) || strings.Contains(l, fakeToken) {
			t.Errorf("a credential in %s", l)
		}
	}
}

// TestPassWorkbench answers the modules a member's pages open, and the
// member as they show it: writable, read-only for another version, or for a
// manager that cannot record writes; a member that cannot be read.
func TestPassWorkbench(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	pc, pt := newPassFake(t), newPassFake(t)
	f.add(consoleInput("c", pc.srv.URL))
	f.add(tokenInput("k", pt.srv.URL))
	f.waitUp("c")
	f.waitUp("k")
	wb := func(name string) (int, passWorkbenchBody, string) {
		t.Helper()
		code, body, _ := s.do("GET", "/api/n/"+name+"/workbench", "")
		var b passWorkbenchBody
		_ = json.Unmarshal([]byte(body), &b)
		return code, b, body
	}

	// Another version: read-only, and its writes refused before they
	// reach it; reads, dry runs and conversions still go.
	code, b, body := wb("c")
	if code != 200 || strings.Join(b.Modules, ",") != "admin,tunnel.hub" || b.User != "admin" || !b.Member.ReadOnly ||
		b.Member.ReasonKind != ReadOnlyVersion || b.Member.SchemaDigest != "sha256:00" || b.Member.ID != "node-pass" || b.Member.Access != AccessConsole {
		t.Fatalf("%d %s", code, body)
	}
	if _, own := ownSchema(); b.Member.ManagerSchemaDigest != own || own == "" {
		t.Errorf("%s", body)
	}
	n := pc.count()
	if code, body, _ := s.do("PUT", "/api/n/c/m/admin/config", `{}`); code != 409 || !strings.Contains(body, "another version of fit") || pc.count() != n {
		t.Errorf("%d %s", code, body)
	}
	if code, _, _ := s.do("PUT", "/api/n/c/m/admin/config?dry_run=true", `{}`); code != 200 {
		t.Errorf("%d", code)
	}
	if code, _, _ := s.do("POST", "/api/n/c/m/admin/config/render", `{}`); code != 404 {
		// The fake serves no render: it was passed on, not refused.
		t.Errorf("%d", code)
	}
	if code, _, _ := s.do("GET", "/api/n/c/m/admin/config", ""); code != 200 {
		t.Errorf("%d", code)
	}

	// The same version: writable.
	f.matchDigest("sha256:00")
	if code, b, body := wb("k"); code != 200 || strings.Join(b.Modules, ",") != "admin" || b.Member.ReadOnly || b.Member.Reason != "" {
		t.Fatalf("%d %s", code, body)
	}

	// A manager that cannot write its members cannot record a write.
	f.m.mu.Lock()
	f.m.readOnly = "a newer fit wrote them"
	f.m.mu.Unlock()
	if code, b, body := wb("k"); code != 200 || !b.Member.ReadOnly || b.Member.ReasonKind != ReadOnlyManager {
		t.Errorf("%d %s", code, body)
	}
	if code, body, _ := s.do("PUT", "/api/n/k/m/admin/config", `{}`); code != 409 || !strings.Contains(body, "could not be recorded") {
		t.Errorf("%d %s", code, body)
	}
	if err := f.m.appendAudit([]AuditEntry{{Action: ActionAPI}}); !errors.As(err, new(errReadOnly)) {
		t.Errorf("%v", err)
	}
	f.m.mu.Lock()
	f.m.readOnly = ""
	f.m.mu.Unlock()

	// Not read yet.
	if kind, why := f.m.compatible(""); kind != ReadOnlyNotRead || why == "" {
		t.Errorf("%s %s", kind, why)
	}

	// The member's workbench refused, or not JSON; its node refused or
	// not JSON; the member down.
	pc.set(func(f *fakeMember) { f.reject = true })
	if code, _, body := wb("c"); code != 502 || !strings.Contains(body, `"kind":"auth_failed"`) {
		t.Errorf("%d %s", code, body)
	}
	pc.set(func(f *fakeMember) { f.reject = false; f.badJSON = true })
	if code, _, body := wb("c"); code != 502 || !strings.Contains(body, `"kind":"bad_answer"`) {
		t.Errorf("%d %s", code, body)
	}
	pc.set(func(f *fakeMember) { f.badJSON = false })
	if code, _, body := wb("c"); code != 200 {
		t.Errorf("%d %s", code, body)
	}
	pt.set(func(f *fakeMember) { f.nodeStatus = 500 })
	if code, _, body := wb("k"); code != 502 || strings.Contains(body, fakeToken) {
		t.Errorf("%d %s", code, body)
	}
	pt.srv.Close()
	if code, _, body := wb("k"); code != 502 || !strings.Contains(body, `"kind":"unreachable"`) {
		t.Errorf("%d %s", code, body)
	}
	if code, _, _ := s.do("POST", "/api/n/k/workbench", "{}"); code != 405 {
		t.Errorf("%d", code)
	}
}

// TestPassWorkbenchBadModules is a console whose /api/workbench is not the
// JSON expected.
func TestPassWorkbenchBadModules(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	p := newPassFake(t)
	var broken sync.Mutex
	status := 200
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		broken.Lock()
		st := status
		broken.Unlock()
		if r.URL.Path == "/api/workbench" {
			w.WriteHeader(st)
			_, _ = w.Write([]byte(`{"modules":`))
			return
		}
		p.serveAll(w, r)
	}))
	t.Cleanup(bad.Close)
	f.add(consoleInput("c", bad.URL))
	f.waitUp("c")
	if code, body, _ := s.do("GET", "/api/n/c/workbench", ""); code != 502 || !strings.Contains(body, "/api/workbench is not the JSON expected") {
		t.Errorf("%d %s", code, body)
	}
	broken.Lock()
	status = 500
	broken.Unlock()
	if code, body, _ := s.do("GET", "/api/n/c/workbench", ""); code != 502 || !strings.Contains(body, `"kind":"http_status"`) {
		t.Errorf("%d %s", code, body)
	}

	// A member that changed since the poll is read again at once, and its
	// pages go by the poll until then.
	f.m.mu.Lock()
	n := f.m.nodes["c"]
	f.m.mu.Unlock()
	n.mu.Lock()
	n.state.Node.SchemaDigest = "sha256:old"
	n.next.Store(n.m.now().Add(time64).UnixNano())
	n.mu.Unlock()
	f.matchDigest("sha256:00")
	broken.Lock()
	status = 200
	broken.Unlock()
	p.mu.Lock()
	p.config = ""
	p.mu.Unlock()
	if code, body, _ := s.do("GET", "/api/n/c/workbench", ""); code != 502 {
		// Its modules still do not parse; what counts is the poll.
		t.Errorf("%d %s", code, body)
	}
	f.waitState("c", "read again", func(s State) bool { return s.Node != nil && s.Node.SchemaDigest == "sha256:00" })
}

// time64 puts a node's next poll far off.
const time64 = 1 << 40

// TestPassRefused is what the switcher does not pass on, and the members'
// answers it does not pass back as they are.
func TestPassRefused(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxResponseBytes: 16 << 10})
	f.matchDigest("sha256:00")
	s := f.serve()
	pc, pt := newPassFake(t), newPassFake(t)
	f.add(consoleInput("c", pc.srv.URL))
	f.add(tokenInput("k", pt.srv.URL))
	f.waitUp("c")
	f.waitUp("k")
	for _, x := range []struct {
		method, path string
		code         int
		has          string
	}{
		{"GET", "/api/n/c/m/admin/users", 403, "changed on the member"},
		{"POST", "/api/n/c/m/admin/account/password", 403, "changed on the member"},
		{"GET", "/api/n/c/m/admin/debug/pprof/heap", 404, "no such path"},
		{"GET", "/api/n/c/m/admin/dashboard", 404, "no such path"},
		{"GET", "/api/n/c/m/ai/tools", 403, "not opened through the manager"},
		{"GET", "/api/n/c/m/cluster/state", 403, "not opened through the manager"},
		{"POST", "/api/n/c/m/llm.hub/rpc/peers", 403, "not opened through the manager"},
		{"GET", "/api/n/c/m/admin/%2e%2e/users", 404, "no such path"},
		{"GET", "/api/n/c/m/admin/a//b", 404, "no such path"},
		{"GET", "/api/n/c/m/admin", 404, "no such path"},
		{"GET", "/api/n/c/config", 404, "passed on at m/"},
		{"GET", "/api/n/nobody/m/admin/config", 404, "no member"},
		{"GET", "/api/m/cluster/n/c/node", 404, "no such endpoint"},
		{"OPTIONS", "/api/n/c/m/admin/config", 405, "not allowed"},
	} {
		if code, body, _ := s.do(x.method, x.path, ""); code != x.code || !strings.Contains(body, x.has) {
			t.Errorf("%s %s: %d %s", x.method, x.path, code, body)
		}
	}
	if code, body, _ := s.do("GET", "/api/n/c/m/admin/config", "", "Accept", "text/event-stream"); code != 501 {
		t.Errorf("%d %s", code, body)
	}

	// The body caps: a request larger than max_response_bytes is not
	// sent, an answer larger is not passed back.
	if code, body, _ := s.do("PUT", "/api/n/c/m/admin/config", `"`+strings.Repeat("x", 17<<10)+`"`); code != 413 {
		t.Errorf("%d %s", code, body)
	}
	if code, body, _ := s.do("GET", "/api/n/c/m/admin/big", ""); code != 502 || !strings.Contains(body, `"kind":"too_large"`) {
		t.Errorf("%d %.200s", code, body)
	}

	// A member refusing the manager is no 401 of the manager's own, nor a
	// password change asked of its user.
	pt2 := newPassFake(t)
	f.add(&Input{Name: "wrong", URL: pt2.srv.URL, Access: AccessAdminAPI, Token: "not-the-token-0123456789"})
	if code, body, _ := s.do("GET", "/api/n/wrong/m/admin/config", ""); code != 502 || !strings.Contains(body, `"kind":"auth_failed"`) || strings.Contains(body, "not-the-token") {
		t.Errorf("%d %s", code, body)
	}
	if code, body, _ := s.do("PUT", "/api/n/wrong/m/admin/config", "{}"); code != 409 {
		// Never read: its version is not known.
		t.Errorf("%d %s", code, body)
	}
	// A session the member asks to change its password, then a login it
	// asks the same of.
	s.do("GET", "/api/n/c/m/admin/echo", "")
	pc.set(func(f *fakeMember) { f.mustChange = true })
	for _, path := range []string{"/api/n/c/m/admin/status", "/api/n/c/m/admin/echo"} {
		if code, body, _ := s.do("GET", path, ""); code != 502 || !strings.Contains(body, `"kind":"password_change_required"`) || strings.Contains(body, "must_change_password") {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	// A write then is not sent: the login it needs fails.
	if code, _, _ := s.do("PUT", "/api/n/c/m/admin/config", "{}"); code != 502 {
		t.Errorf("%d", code)
	}
	if e := f.audits(1)[0]; !strings.HasPrefix(e.Detail, "PUT /config: not sent (password_change_required): ") {
		t.Errorf("%+v", e)
	}
	pc.set(func(f *fakeMember) { f.mustChange = false })
	pc.restart()
	f.m.mu.Lock()
	f.m.nodes["c"].resetLogin()
	f.m.mu.Unlock()
	// Another 403 is the member's own answer.
	if code, body, _ := s.do("GET", "/api/n/c/m/admin/forbid", ""); code != 403 || body != `{"error":"no"}` {
		t.Errorf("%d %s", code, body)
	}
	// A write refused by the member's console is audited with its status.
	pc.set(func(f *fakeMember) { f.reject = true })
	if code, body, _ := s.do("PUT", "/api/n/c/m/admin/config", "{}"); code != 502 || !strings.Contains(body, `"kind":"auth_failed"`) {
		t.Errorf("%d %s", code, body)
	}
	if e := f.audits(2)[1]; e.Node != "c" || e.Detail != "PUT /config: 401, auth_failed" {
		t.Errorf("%+v", e)
	}
	pc.set(func(f *fakeMember) { f.reject = false })

	// A write the member never answered is audited as such.
	pt.srv.Close()
	if code, body, _ := s.do("PUT", "/api/n/k/m/admin/config", "{}"); code != 502 || !strings.Contains(body, `"kind":"unreachable"`) {
		t.Errorf("%d %s", code, body)
	}
	if e := f.audits(3)[2]; e.Node != "k" || !strings.Contains(e.Detail, "PUT /config: not answered (unreachable), so it may or may not have been applied") {
		t.Errorf("%+v", e)
	}

	// Too many writes waiting for their audit entries.
	f.m.pass.mu.Lock()
	f.m.pass.held = MaxPassAudit
	f.m.pass.mu.Unlock()
	if code, body, _ := s.do("PUT", "/api/n/c/m/admin/config", "{}"); code != 429 || !strings.Contains(body, "being recorded") {
		t.Errorf("%d %s", code, body)
	}
	f.m.pass.mu.Lock()
	f.m.pass.held = 0
	f.m.pass.mu.Unlock()
}

// TestPassQueue bounds the calls: a member's turns, those waiting, and all
// of them; a call that waited past the timeout is answered busy, not sent.
func TestPassQueue(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	p := newPassFake(t)
	f.add(tokenInput("k", p.srv.URL))
	f.add(tokenInput("j", p.srv.URL))
	f.m.mu.Lock()
	n, other := f.m.nodes["k"], f.m.nodes["j"]
	f.m.mu.Unlock()
	var releases []func()
	ran, stale := 0, 0
	run := func(release func()) { ran++; releases = append(releases, release) }
	for range MaxPassPerNode + MaxPassWaiting {
		if err := f.m.enqueue(n, run, func() { stale++ }); err != nil {
			t.Fatal(err)
		}
	}
	if ran != MaxPassPerNode {
		t.Fatalf("%d run", ran)
	}
	if err := f.m.enqueue(n, run, nil); err == nil || !strings.Contains(err.Error(), "to the member") {
		t.Fatalf("%v", err)
	}
	// The first waiting is fresh, the next two waited too long.
	f.m.pass.mu.Lock()
	q := f.m.pass.queues[n]
	for _, w := range q.waiting[1:3] {
		w.at = w.at.Add(-2 * f.m.lim.timeout)
	}
	f.m.pass.mu.Unlock()
	releases[0]()
	releases[0]() // once
	if ran != MaxPassPerNode+1 || stale != 0 {
		t.Fatalf("%d run, %d stale", ran, stale)
	}
	releases[1]()
	if ran != MaxPassPerNode+2 || stale != 2 {
		t.Fatalf("%d run, %d stale", ran, stale)
	}
	// Every call to every member: one more is refused.
	f.m.pass.mu.Lock()
	total := f.m.pass.total
	f.m.pass.total = MaxPassCalls
	f.m.pass.mu.Unlock()
	if err := f.m.enqueue(other, run, nil); err == nil || !strings.Contains(err.Error(), "to members") {
		t.Fatalf("%v", err)
	}
	f.m.pass.mu.Lock()
	f.m.pass.total = total
	f.m.pass.mu.Unlock()
	// Ending every turn empties the queue.
	for len(releases) > 0 {
		r := releases[0]
		releases = releases[1:]
		r()
	}
	f.m.pass.mu.Lock()
	left, queues := f.m.pass.total, len(f.m.pass.queues)
	f.m.pass.mu.Unlock()
	if left != 0 || queues != 0 {
		t.Fatalf("%d calls, %d queues left", left, queues)
	}

	// Through the console: a call refused for the bounds is answered 429,
	// and a write so refused gives its audit place back; one that waited
	// too long, 503.
	s := f.serve()
	f.matchDigest("sha256:00")
	f.waitUp("k")
	f.m.pass.mu.Lock()
	f.m.pass.total = MaxPassCalls
	f.m.pass.mu.Unlock()
	if code, body, _ := s.do("PUT", "/api/n/k/m/admin/config", "{}"); code != 429 {
		t.Errorf("%d %s", code, body)
	}
	f.m.pass.mu.Lock()
	f.m.pass.total = 0
	held := f.m.pass.held
	f.m.pass.mu.Unlock()
	if held != 0 {
		t.Errorf("%d audit places held", held)
	}
	var hold []func()
	for range MaxPassPerNode {
		_ = f.m.enqueue(n, func(release func()) { hold = append(hold, release) }, nil)
	}
	done := make(chan int, 1)
	go func() { code, _, _ := s.do("PUT", "/api/n/k/m/admin/config", "{}"); done <- code }()
	waitFor(t, "the write to wait its turn", func() bool {
		f.m.pass.mu.Lock()
		defer f.m.pass.mu.Unlock()
		q := f.m.pass.queues[n]
		return q != nil && len(q.waiting) == 1
	})
	f.m.pass.mu.Lock()
	f.m.pass.queues[n].waiting[0].at = f.m.pass.queues[n].waiting[0].at.Add(-2 * f.m.lim.timeout)
	f.m.pass.mu.Unlock()
	hold[0]()
	if code := <-done; code != 503 {
		t.Errorf("%d", code)
	}
	for _, r := range hold[1:] {
		r()
	}
	f.m.pass.mu.Lock()
	held = f.m.pass.held
	f.m.pass.mu.Unlock()
	if held != 0 {
		t.Errorf("%d audit places held", held)
	}
}

// TestPassAuditStore keeps writing the audit entries when the store fails,
// saying so, and gives their places back.
func TestPassAuditStore(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	f.store.mu.Lock()
	f.store.fail = errors.New("disk full")
	f.store.mu.Unlock()
	if !f.m.holdAudit() {
		t.Fatal("no audit place")
	}
	f.m.audited("admin", "k", "PUT", "/config", nil, &Problem{Kind: KindTimeout, Message: "slow"})
	waitFor(t, "the audit writer to give up", func() bool {
		return strings.Contains(f.log.String(), "entries of the audit log were not kept: saving the members: disk full")
	})
	waitFor(t, "the audit place to be given back", func() bool {
		f.m.pass.mu.Lock()
		defer f.m.pass.mu.Unlock()
		return f.m.pass.held == 0 && !f.m.pass.writing
	})
	// Bounded as the rest of the log.
	f.store.mu.Lock()
	f.store.fail = nil
	f.store.mu.Unlock()
	batch := make([]AuditEntry, MaxAudit+5)
	for i := range batch {
		batch[i] = AuditEntry{Action: ActionAPI, Detail: strconv.Itoa(i)}
	}
	if err := f.m.appendAudit(batch); err != nil {
		t.Fatal(err)
	}
	if a := f.m.Audit(); len(a) != MaxAudit || a[0].Detail != "5" {
		t.Fatalf("%d %+v", len(a), a[0])
	}
}

// TestPassLinked opens a linked member's pages over its link.
func TestPassLinked(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	f.matchDigest("sha256:00")
	e := enrollIn(t, f, "nat")
	p := newPassFake(t)
	lm := startLinked(t, f, e.Config, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// As admin.LinkAPI serves the Admin API: no token asked for.
		r.Header.Set("Authorization", "Bearer "+fakeToken)
		p.serveAll(w, r)
	}))
	lm.waitLinked(t, true)
	f.waitUp("nat")
	s := f.serve()
	if code, body, _ := s.do("GET", "/api/n/nat/workbench", ""); code != 200 || !strings.Contains(body, `"modules":["admin"]`) || !strings.Contains(body, `"access":"link"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body, h := s.do("GET", "/api/n/nat/m/admin/config", ""); code != 200 || body != passConfig || h.Get("ETag") != `"v1"` {
		t.Fatalf("%d %s", code, body)
	}
	if code, body, _ := s.do("PUT", "/api/n/nat/m/admin/config?x=1", `{"routes":[]}`, "If-Match", `"v1"`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if r, _ := p.last(); r.URL.RawQuery != "x=1" || r.Header.Get("If-Match") != `"v1"` {
		t.Errorf("%v %v", r.URL, r.Header)
	}
	if e := f.audits(1)[0]; e.Node != "nat" || e.Detail != "PUT /config: 200, config version 2" {
		t.Errorf("%+v", e)
	}
}

// TestPassCapabilities: the manager and the member name the schema's
// capability alike.
func TestPassCapabilities(t *testing.T) {
	if CapSchema != admin.CapSchema {
		t.Fatal(CapSchema)
	}
}

// TestRouteOf refuses a path whose escapes do not decode, which no browser
// sends.
func TestRouteOf(t *testing.T) {
	if _, code, _ := routeOf("m/admin/%zz"); code != 404 {
		t.Fatal(code)
	}
}

// TestTransportBadPath: a path that makes no URL is a call's failure.
func TestTransportBadPath(t *testing.T) {
	tr := newTransport(testEngine(t), &Member{URL: "http://127.0.0.1:1"}, limits{timeout: time.Second, maxBytes: 1 << 20})
	defer tr.close()
	got := make(chan error, 1)
	tr.do("GET", "/%zz", nil, nil, func(_ *reply, err error) { got <- err })
	if err := <-got; err == nil {
		t.Fatal("no error")
	}
}
