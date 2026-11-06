package cluster

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
)

// serveUsers is a console serving the manager's module, as a manager's
// does, with the manager's gate, its users each logged in apart.
func (f *fixture) serveUsers(names ...string) map[string]*served {
	f.t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	var users []config.ConsoleUser
	for _, n := range names {
		users = append(users, config.ConsoleUser{Username: n, PasswordHash: string(hash)})
	}
	logs := &logBuf{}
	c, err := console.Start(f.engine, console.Options{
		Config:  &config.RoleConsole{Address: "127.0.0.1:0", Users: users, MaxSessions: 64},
		Cookie:  "fit_mgr",
		Pages:   fstest.MapFS{"index.html": {Data: []byte("<p>hi</p>")}},
		Logf:    logs.logf,
		Modules: func() []*console.Module { return []*console.Module{f.m.Module()} },
		Gate:    f.m.ConsoleGate,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Stop(f.t.Context()) })
	f.m.SetConsole(func() []string { return names }, func(user string) string {
		if user == "ada" {
			return config.AssistantApply
		}
		return config.AssistantRead
	})
	out := map[string]*served{}
	for _, n := range names {
		jar, _ := cookiejar.New(nil)
		s := &served{t: f.t, url: strings.TrimSuffix(c.URL(), "/"), c: &http.Client{Jar: jar, Timeout: 30 * time.Second}, logs: logs}
		code, body, _ := s.do("POST", "/api/login", `{"username":"`+n+`","password":"pw"}`)
		if code != 200 {
			f.t.Fatalf("login %s: %d %s", n, code, body)
		}
		var b struct{ CSRF string }
		_ = json.Unmarshal([]byte(body), &b)
		s.csrf = b.CSRF
		out[n] = s
	}
	return out
}

// rolesFixture is a manager with two members, a (env=prod) and b
// (env=dev), a group prod of the first, and users of every role: ada an
// admin of the cluster, gil an admin of prod, opal an operator of prod and
// a viewer of the rest, pat a viewer of prod alone, vic a viewer, and nemo
// with no role.
func rolesFixture(t *testing.T, set config.Cluster) (*fixture, map[string]*served) {
	f := newFixture(t, set)
	a, b := consoleInput("a", downURL(t)), consoleInput("b", downURL(t))
	a.Labels, b.Labels = map[string]string{"env": "prod"}, map[string]string{"env": "dev"}
	f.add(a)
	f.add(b)
	if _, err := f.m.AddGroup("tester", &GroupInput{Name: "prod", Selector: "env=prod"}); err != nil {
		t.Fatal(err)
	}
	us := f.serveUsers("ada", "gil", "opal", "pat", "vic", "nemo")
	if _, err := f.m.SetAccess("ada", &Access{Users: []UserRoles{
		{User: "ada", Role: RoleAdmin},
		{User: "gil", Groups: map[string]string{"prod": RoleAdmin}},
		{User: "opal", Role: RoleViewer, Groups: map[string]string{"prod": RoleOperator}},
		{User: "pat", Groups: map[string]string{"prod": RoleViewer}},
		{User: "vic", Role: RoleViewer},
	}}); err != nil {
		t.Fatal(err)
	}
	return f, us
}

// TestRolesOpen: with no roles set, every console user is a cluster-wide
// admin, as before roles were, and nothing of the manager is gated.
func TestRolesOpen(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	us := f.serveUsers("ada", "bob")
	code, got, _ := us["bob"].do("GET", "/api/m/cluster/me", "")
	if code != 200 || !strings.Contains(got, `"open":true`) || !strings.Contains(got, `"role":"admin"`) || !strings.Contains(got, `"assistant":"apply"`) {
		t.Fatalf("%d %s", code, got)
	}
	if code, got, _ := us["bob"].do("POST", "/api/m/cluster/nodes", js(consoleInput("x", downURL(t)))); code != 201 {
		t.Fatalf("%d %s", code, got)
	}
	if f.m.RolesActive() || f.m.ConsoleGate("bob", "PUT", "/api/m/admin/config", "") != "" || f.m.AssistantCap("bob") != config.AssistantApply {
		t.Error("open")
	}
	// The Users page lists every console user as an admin, and their
	// assistant's levels.
	code, got, h := us["ada"].do("GET", "/api/m/cluster/access", "")
	if code != 200 || h.Get("ETag") == "" || !strings.Contains(got, `"open":true`) ||
		!strings.Contains(got, `{"user":"bob","role":"admin","console":true,"assistant":"read","assistant_cap":"read"}`) {
		t.Fatalf("%d %s", code, got)
	}
	// The first roles must leave an admin.
	if _, err := f.m.SetAccess("ada", &Access{Users: []UserRoles{{User: "bob", Role: RoleViewer}}}); err == nil || !strings.Contains(err.Error(), "no one would be an admin") {
		t.Fatal(err)
	}
}

// TestRolesRefuse: each role is refused what it may not do, by the
// module's endpoints, the node switcher and the console's gate, and let
// through what it may.
func TestRolesRefuse(t *testing.T) {
	f, us := rolesFixture(t, config.Cluster{})
	type tc struct {
		user, method, path, body string
		want                     int
	}
	prodDraft := js(&Input{Name: "c", Labels: map[string]string{"env": "prod"}, URL: downURL(t), Access: AccessConsole, Username: "u", Password: "p"})
	devDraft := js(&Input{Name: "d", Labels: map[string]string{"env": "dev"}, URL: downURL(t), Access: AccessConsole, Username: "u", Password: "p"})
	toDev := js(&Input{Labels: map[string]string{"env": "dev"}, URL: downURL(t), Access: AccessConsole, Username: fakeUser})
	cases := []tc{
		// Reads: any role anywhere; nemo has none.
		{"nemo", "GET", "/nodes", "", 403},
		{"nemo", "GET", "/audit", "", 403},
		{"nemo", "GET", "/me", "", 200},
		{"pat", "GET", "/nodes", "", 200},
		{"pat", "GET", "/state", "", 200},
		{"vic", "GET", "/audit", "", 200},
		{"vic", "GET", "/access", "", 200},
		// A member's own reads: a role on that member.
		{"pat", "GET", "/nodes/b/read/node", "", 403},
		{"pat", "GET", "/desired/b", "", 403},
		{"pat", "POST", "/sync", `{"nodes":["b"]}`, 403},
		{"pat", "POST", "/compare", `{"a":"a","b":"b"}`, 403},
		{"pat", "POST", "/import", `{"node":"b"}`, 403},
		{"pat", "POST", "/select", `{"selector":"env"}`, 200},
		{"pat", "POST", "/text/render?format=json", `{}`, 200},
		// Members: an admin of each, as they are and as they will be.
		{"vic", "POST", "/nodes", prodDraft, 403},
		{"opal", "POST", "/nodes", prodDraft, 403},
		{"gil", "POST", "/nodes", devDraft, 403},
		{"gil", "POST", "/test", devDraft, 403},
		{"opal", "POST", "/test", prodDraft, 403},
		{"gil", "PUT", "/nodes/a", toDev, 403},
		{"gil", "DELETE", "/nodes/b", "", 403},
		{"opal", "DELETE", "/nodes/a", "", 403},
		{"gil", "POST", "/nodes/b/enroll", "", 403},
		{"gil", "POST", "/enroll", `{"name":"e","labels":{"env":"dev"}}`, 403},
		// Tests: of a member as kept, an operator's; of a draft, an admin's.
		{"vic", "POST", "/nodes/a/test", "", 403},
		{"opal", "POST", "/nodes/b/test", "", 403},
		{"opal", "POST", "/nodes/a/test", "", 200},
		{"opal", "POST", "/nodes/a/test", js(consoleInput("a", downURL(t))), 403},
		// Operations: an operator of each member.
		{"vic", "POST", "/ops", `{"op":"check","members":["a"]}`, 403},
		{"opal", "POST", "/ops", `{"op":"check","members":["a","b"]}`, 403},
		{"pat", "POST", "/ops", `{"op":"check","group":"prod"}`, 403},
		// Groups, templates and roles: a cluster-wide admin.
		{"gil", "POST", "/groups", `{"name":"g2","selector":"env=dev"}`, 403},
		{"gil", "PUT", "/groups/prod", `{"selector":"env"}`, 403},
		{"gil", "DELETE", "/groups/prod", "", 403},
		{"gil", "POST", "/templates", `{"name":"t","config":{"log":{}}}`, 403},
		{"opal", "PUT", "/templates/t", `{"config":{}}`, 403},
		{"gil", "PUT", "/access", `{"users":[{"user":"gil","role":"admin"}]}`, 403},
		// Rollouts: a template's or a sync, an operator of each member; a
		// copy, an admin of each.
		{"vic", "POST", "/rollouts", `{"kind":"sync","nodes":["a"]}`, 403},
		{"opal", "POST", "/rollouts", `{"kind":"sync"}`, 403},
		{"opal", "POST", "/rollouts", `{"kind":"copy","from":"b","paths":["log"],"nodes":["a"]}`, 403},
		{"gil", "POST", "/rollouts", `{"kind":"copy","from":"b","paths":["log"],"nodes":["a"]}`, 403},
	}
	for _, c := range cases {
		code, got, _ := us[c.user].do(c.method, "/api/m/cluster"+c.path, c.body)
		if code != c.want {
			t.Errorf("%s %s %s: %d, not %d: %s", c.user, c.method, c.path, code, c.want, got)
		}
		if c.want == 403 && !strings.Contains(got, `"kind":"forbidden"`) && !strings.Contains(got, "may not") {
			t.Errorf("%s %s %s: %s", c.user, c.method, c.path, got)
		}
	}
	// And let through what they may.
	allowed := []tc{
		{"gil", "POST", "/nodes", prodDraft, 201},
		{"gil", "DELETE", "/nodes/c", "", 200},
		{"opal", "POST", "/ops", `{"op":"check","members":["a"]}`, 202},
		{"ada", "POST", "/templates", `{"name":"t","groups":["prod"],"config":{"log":{}}}`, 201},
		{"opal", "POST", "/rollouts", `{"kind":"template","template":"t"}`, 202},
		{"ada", "POST", "/rollouts", `{"kind":"copy","from":"b","paths":["log"],"nodes":["a"]}`, 202},
	}
	for _, c := range allowed {
		code, got, _ := us[c.user].do(c.method, "/api/m/cluster"+c.path, c.body)
		if code != c.want {
			t.Fatalf("%s %s %s: %d, not %d: %s", c.user, c.method, c.path, code, c.want, got)
		}
		if c.path == "/rollouts" {
			var r Rollout
			_ = json.Unmarshal([]byte(got), &r)
			// A viewer neither steers nor discards a rollout; a copy's
			// is an admin's.
			for _, u := range []string{"vic", "pat"} {
				if code, got, _ := us[u].do("POST", "/api/m/cluster/rollouts/"+r.ID+"/pause", ""); code != 403 {
					t.Errorf("%s pauses %s: %d %s", u, r.ID, code, got)
				}
				if code, _, _ := us[u].do("POST", "/api/m/cluster/rollouts/"+r.ID+"/start", `{"strategy":{"mode":"all"}}`); code != 403 {
					t.Errorf("%s starts %s: %d", u, r.ID, code)
				}
				if code, _, _ := us[u].do("DELETE", "/api/m/cluster/rollouts/"+r.ID, ""); code != 403 {
					t.Errorf("%s discards %s: %d", u, r.ID, code)
				}
			}
			if code, _, _ := us["opal"].do("POST", "/api/m/cluster/rollouts/"+r.ID+"/pause", ""); code != map[bool]int{true: 409, false: 403}[r.Kind != RolloutCopy] {
				t.Errorf("opal pauses %s (%s): %d", r.ID, r.Kind, code)
			}
		}
	}

	// The node switcher: a viewer of a member reads it, an admin of it
	// writes; pat is no one's on b.
	for _, c := range []tc{
		{"pat", "GET", "/api/n/b/node", "", 403},
		{"opal", "PUT", "/api/n/a/m/admin/config", `{}`, 403},
		{"vic", "PUT", "/api/n/b/m/admin/config", `{}`, 403},
	} {
		if code, got, _ := us[c.user].do(c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s", c.user, c.method, c.path, code, got)
		}
	}
	// Let through, the member then answers (it is down, or not read yet).
	for _, c := range []tc{{"pat", "GET", "/api/n/a/node", "", 502}, {"gil", "PUT", "/api/n/a/m/admin/config", `{}`, 409}} {
		if code, got, _ := us[c.user].do(c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s", c.user, c.method, c.path, code, got)
		}
	}

	// The console's gate: once roles are set, the manager's own pages are
	// changed by a cluster-wide admin only; reads, the cluster module, the
	// switcher, the assistant, a user's password and dry runs pass.
	for _, c := range []tc{
		{"opal", "PUT", "/api/m/admin/config", `{}`, 403},
		{"gil", "POST", "/api/m/tunnel.hub/x", `{}`, 403},
		{"ada", "PUT", "/api/m/admin/config", `{}`, 404},
		{"opal", "PUT", "/api/m/admin/config?dry_run=1", `{}`, 404},
		{"opal", "POST", "/api/m/admin/account/password", `{}`, 404},
		{"opal", "POST", "/api/m/admin/config/render", `{}`, 404},
		{"opal", "POST", "/api/m/ai/chat", `{}`, 404},
		{"opal", "GET", "/api/m/admin/config", "", 404},
	} {
		if code, got, _ := us[c.user].do(c.method, c.path, c.body); code != c.want {
			t.Errorf("gate: %s %s %s: %d %s", c.user, c.method, c.path, code, got)
		}
	}

	// What each user is told of its roles.
	var me meBody
	_, got, _ := us["opal"].do("GET", "/api/m/cluster/me", "")
	if err := json.Unmarshal([]byte(got), &me); err != nil || me.Open || me.Role != RoleViewer || me.Top != RoleOperator || me.Nodes["a"] != RoleOperator ||
		me.Nodes["b"] != RoleViewer || me.Group["prod"] != RoleOperator || !me.ManagerReadOnly || me.Assistant != config.AssistantPropose {
		t.Errorf("%v %+v", err, me)
	}
	_, got, _ = us["nemo"].do("GET", "/api/m/cluster/me", "")
	me = meBody{}
	if err := json.Unmarshal([]byte(got), &me); err != nil || me.Top != "" || len(me.Nodes) != 0 || me.Assistant != config.AssistantRead {
		t.Errorf("%v %+v", err, me)
	}
	// The assistant never does more than the user may.
	for user, want := range map[string]string{"ada": config.AssistantApply, "opal": config.AssistantPropose, "gil": config.AssistantPropose,
		"vic": config.AssistantRead, "nemo": config.AssistantRead} {
		if got := f.m.AssistantCap(user); got != want {
			t.Errorf("%s: %s", user, got)
		}
	}
	// A member whose pages gil may not write opens read-only for him.
	if err := f.m.Allow("gil", Need{Role: RoleAdmin, Nodes: []string{"b"}}); err == nil || !strings.Contains(err.Error(), "on the members b") {
		t.Error(err)
	}
	if err := f.m.Allow("pat", Need{Role: RoleOperator}); err == nil || !strings.Contains(err.Error(), "on the cluster or one of its groups") {
		t.Error(err)
	}
	if err := f.m.Allow("opal", Need{Role: RoleAdmin, Groups: []string{"prod"}}); err == nil || !strings.Contains(err.Error(), "on the groups prod") {
		t.Error(err)
	}
	if err := f.m.Allow("gil", Need{Role: RoleAdmin, Groups: []string{"prod"}}); err != nil {
		t.Error(err)
	}
}

// TestRolesSwitcherReadOnly: a member opens read-only through the switcher
// for a user who is not its admin.
func TestRolesSwitcherReadOnly(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	f.matchDigest("sha256:00")
	fm := newPassFake(t)
	in := consoleInput("a", fm.srv.URL)
	in.Labels = map[string]string{"env": "prod"}
	f.add(in)
	f.waitUp("a")
	us := f.serveUsers("ada", "vic")
	if _, err := f.m.SetAccess("ada", &Access{Users: []UserRoles{{User: "ada", Role: RoleAdmin}, {User: "vic", Role: RoleViewer}}}); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[string]string{"ada": `"read_only":false`, "vic": `"reason_kind":"role"`} {
		code, got, _ := us[user].do("GET", "/api/n/a/workbench", "")
		if code != 200 || !strings.Contains(got, want) {
			t.Errorf("%s: %d %s", user, code, got)
		}
	}
}

// TestAccess: the roles are checked, follow a group's rename, go with its
// removal, are written as document version 3 and kept across a restart,
// and each change is in the audit log with its diff.
func TestAccess(t *testing.T) {
	f := newFixture(t, config.Cluster{Admins: []string{"root"}})
	if _, err := f.m.AddGroup("tester", &GroupInput{Name: "prod", Selector: "env=prod"}); err == nil {
		t.Fatal("admin.cluster.admins names root alone: tester is no admin")
	}
	if !f.m.RolesActive() {
		t.Fatal("admins set roles")
	}
	if _, err := f.m.AddGroup("root", &GroupInput{Name: "prod", Selector: "env=prod"}); err != nil {
		t.Fatal(err)
	}
	us := f.serveUsers("root", "ada")
	for _, c := range []struct{ body, field string }{
		{`{"default_role":"king"}`, "default_role"},
		{`{"users":[{"user":"ada","role":"king"}]}`, "users[0].role"},
		{`{"users":[{"user":"ada","groups":{"prod":"king"}}]}`, "users[0].groups"},
		{`{"users":[{"user":"ada","groups":{"none":"viewer"}}]}`, "users[0].groups"},
		{`{"users":[{"user":"ada","role":"viewer"},{"user":"ada","role":"admin"}]}`, "users[1].user"},
		{`{"users":[{"user":"","role":"viewer"}]}`, "users[0].user"},
	} {
		code, got, _ := us["root"].do("PUT", "/api/m/cluster/access", c.body)
		if code != 400 || !strings.Contains(got, `"field":"`+c.field+`"`) {
			t.Errorf("%s: %d %s", c.body, code, got)
		}
	}
	code, got, h := us["root"].do("GET", "/api/m/cluster/access", "")
	if code != 200 || !strings.Contains(got, `"admins":["root"]`) || !strings.Contains(got, `{"user":"root","role":"admin","console":true,"fixed":true,"assistant":"read","assistant_cap":"read"}`) {
		t.Fatalf("%d %s", code, got)
	}
	// A stale version is refused; root, a fixed admin, may leave no other.
	if code, got, _ := us["root"].do("PUT", "/api/m/cluster/access", `{"users":[]}`, "If-Match", `"999"`); code != 412 {
		t.Errorf("%d %s", code, got)
	}
	code, got, _ = us["root"].do("PUT", "/api/m/cluster/access", `{"default_role":"viewer","users":[{"user":"ada","role":"","groups":{"prod":"operator","gone":""}},{"user":"zed"}]}`, "If-Match", h.Get("ETag"))
	if code != 200 || !strings.Contains(got, `{"user":"ada","groups":{"prod":"operator"},"console":true`) {
		t.Fatalf("%d %s", code, got)
	}
	// zed, named with no role under a default role, is kept: none, not the
	// default.
	if !strings.Contains(got, `{"user":"zed","console":false`) {
		t.Errorf("%s", got)
	}
	if err := f.m.Allow("zed", Need{Role: RoleViewer}); err == nil {
		t.Error("zed has the default role")
	}
	// A rename follows; a removal takes the roles with it.
	if _, err := f.m.UpdateGroup("root", "prod", &GroupInput{Name: "live", Selector: "env=prod"}); err != nil {
		t.Fatal(err)
	}
	if u := f.m.accessView(); !strings.Contains(js(u.Users), `"user":"ada","groups":{"live":"operator"}`) {
		t.Errorf("%s", js(u.Users))
	}
	sec := string(f.store.Section(Section))
	if !strings.Contains(sec, `"version":3`) || !strings.Contains(sec, `"access":{"users":[{"user":"ada","groups":{"live":"operator"}}`) {
		t.Errorf("%s", sec)
	}
	// The change of roles is audited, its diff a line a user.
	es := f.m.AuditWhere(AuditFilter{Action: "access"})
	if len(es) != 1 || es[0].User != "root" || !strings.Contains(es[0].Diff, "+ada: none, operator on prod") || !strings.HasPrefix(es[0].Digest, "sha256:") ||
		!strings.Contains(es[0].Detail, "roles set for the first time") || !strings.Contains(es[0].Detail, "the default role viewer") {
		t.Errorf("%+v", es)
	}
	// Kept across a restart.
	f.m.Close()
	f.start()
	if err := f.m.Allow("ada", Need{Role: RoleViewer}); err != nil {
		t.Error(err)
	}
	if err := f.m.Allow("someone", Need{Role: RoleViewer}); err != nil {
		t.Error("the default role", err)
	}
	if err := f.m.RemoveGroup("root", "live"); err != nil {
		t.Fatal(err)
	}
	if u := f.m.accessView(); strings.Contains(js(u.Users), `"live"`) {
		t.Errorf("%s", js(u.Users))
	}
	// Only a cluster-wide admin changes them.
	if _, err := f.m.SetAccess("ada", &Access{Users: []UserRoles{{User: "ada", Role: RoleAdmin}}}); err == nil {
		t.Error("ada changed the roles")
	}
}

// TestAccessCheck: the roles' bounds, and how a change of them is told.
func TestAccessCheck(t *testing.T) {
	d := &document{Groups: []Group{{Name: "g"}}}
	many := &Access{}
	for range MaxAccessUsers + 1 {
		many.Users = append(many.Users, UserRoles{User: "u"})
	}
	groups := map[string]string{}
	for i := range MaxUserGroups + 1 {
		groups[strings.Repeat("g", i+1)] = RoleViewer
	}
	for _, c := range []struct {
		a     *Access
		field string
	}{
		{many, "users"},
		{&Access{Users: []UserRoles{{User: strings.Repeat("u", MaxUsernameBytes+1)}}}, "users[0].user"},
		{&Access{Users: []UserRoles{{User: "u", Groups: groups}}}, "users[0].groups"},
	} {
		var fe *FieldError
		if err := c.a.check(d); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: %v", c.field, err)
		}
	}
	a := &Access{Users: []UserRoles{{User: "ada", Role: RoleAdmin}}}
	if s := describeAccessChange(a, a); s != "no user's roles" {
		t.Error(s)
	}
	if s := describeAccessChange(a, &Access{DefaultRole: RoleViewer}); s != "the roles of ada; the default role viewer" {
		t.Error(s)
	}
	if s := accessText(nil); s != "" {
		t.Error(s)
	}
	// Nil roles are none, which leaves no admin.
	f := newFixture(t, config.Cluster{})
	if _, err := f.m.SetAccess("ada", nil); err == nil || !strings.Contains(err.Error(), "no one would be an admin") {
		t.Error(err)
	}
	// A diff of parts as JSON, or as they are when they are not.
	if prettyJSON(nil) != "" || prettyJSON([]byte("x")) != "x\n" || prettyJSON([]byte(`{"a":1}`)) != "{\n  \"a\": 1\n}\n" {
		t.Error("prettyJSON")
	}
	if n := (&AuditEntry{Node: "a", Nodes: []string{"b"}, Outcomes: []AuditOutcome{{Node: "c"}}}).auditNodes(); strings.Join(n, ",") != "a,b,c" {
		t.Error(n)
	}
}

// TestRolesAlerts: I147's drift and alerts under the roles. Reading them is
// any role's; a drift's fix and a silence an operator's of what they touch;
// rules, webhooks and a webhook's test a cluster-wide admin's. Their audit
// entries carry the alerts' versions and a diff without secrets.
func TestRolesAlerts(t *testing.T) {
	f, us := rolesFixture(t, config.Cluster{})
	sink := newHookSink(t)
	hook := js(map[string]any{"name": "ops", "url": sink.srv.URL + "/hook?token=sekret-token", "secret": "shh"})
	type tc struct {
		user, method, path, body string
		want                     int
	}
	for _, c := range []tc{
		{"nemo", "GET", "/alerts/config", "", 403},
		{"nemo", "GET", "/drift", "", 403},
		{"pat", "GET", "/alerts", "", 200},
		{"pat", "GET", "/alerts/summary", "", 200},
		{"vic", "GET", "/alerts/config", "", 200},
		{"vic", "GET", "/drift", "", 200},
		{"opal", "POST", "/alerts/rules", `{"name":"down","kind":"member_down"}`, 403},
		{"gil", "POST", "/alerts/webhooks", hook, 403},
		{"ada", "POST", "/alerts/webhooks", hook, 201},
		{"ada", "POST", "/alerts/rules", `{"name":"down","kind":"member_down","webhooks":["ops"]}`, 201},
		{"ada", "POST", "/alerts/rules", `{"name":"prod-down","kind":"member_down","group":"prod"}`, 201},
		{"gil", "POST", "/alerts/webhooks/ops/test", "", 403},
		{"gil", "PUT", "/alerts/rules/down", `{"kind":"member_down"}`, 403},
		{"gil", "DELETE", "/alerts/webhooks/ops", "", 403},
		{"vic", "POST", "/alerts/silences", `{"node":"a","duration_ms":60000}`, 403},
		{"opal", "POST", "/alerts/silences", `{"node":"b","duration_ms":60000}`, 403},
		{"opal", "POST", "/alerts/silences", `{"rule":"down","duration_ms":60000}`, 403},
		{"opal", "POST", "/alerts/silences", `{"rule":"prod-down","duration_ms":60000}`, 201},
		{"opal", "POST", "/drift/fix", `{"nodes":["b"]}`, 403},
		{"opal", "POST", "/drift/fix", `{"nodes":["a"]}`, 202},
	} {
		if code, got, _ := us[c.user].do(c.method, "/api/m/cluster"+c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d, not %d: %s", c.user, c.method, c.path, code, c.want, got)
		}
	}
	// A silence of a member is ended by an operator of it, not a viewer.
	code, got, _ := us["opal"].do("POST", "/api/m/cluster/alerts/silences", `{"node":"a","duration_ms":60000}`)
	var s Silence
	if code != 201 || json.Unmarshal([]byte(got), &s) != nil {
		t.Fatalf("%d %s", code, got)
	}
	if code, _, _ := us["vic"].do("DELETE", "/api/m/cluster/alerts/silences/"+s.ID, ""); code != 403 {
		t.Errorf("vic ended a silence: %d", code)
	}
	if code, got, _ := us["opal"].do("DELETE", "/api/m/cluster/alerts/silences/"+s.ID, ""); code != 200 {
		t.Errorf("%d %s", code, got)
	}
	if err := f.m.TestWebhook("gil", "ops", func(WebhookResult) {}); err == nil {
		t.Error("gil tested a webhook")
	}
	// The Manager's methods refuse as the endpoints do.
	for what, err := range map[string]error{
		"add a rule":       second(f.m.AddAlertRule("gil", &AlertRule{Name: "x", Kind: "member_down"})),
		"change a rule":    second(f.m.UpdateAlertRule("gil", "down", &AlertRule{Kind: "member_down"})),
		"remove a rule":    f.m.RemoveAlertRule("gil", "down"),
		"add a webhook":    second(f.m.AddWebhook("gil", &WebhookInput{Name: "x"})),
		"change a webhook": second(f.m.UpdateWebhook("gil", "ops", &WebhookInput{})),
		"remove a webhook": f.m.RemoveWebhook("gil", "ops"),
	} {
		var fb *ErrForbidden
		if !errors.As(err, &fb) {
			t.Errorf("gil may %s: %v", what, err)
		}
	}
	es := f.m.AuditWhere(AuditFilter{Action: "alert.webhook"})
	if len(es) != 1 || es[0].User != "ada" || es[0].Outcome != AuditOK || es[0].Before != "a0" || es[0].After != "a1" ||
		!strings.Contains(es[0].Diff, `"secret_set": true`) || strings.Contains(es[0].Diff, "sekret-token") || strings.Contains(es[0].Diff, "shh") {
		t.Errorf("%+v", es)
	}
}

// second returns the error of a call's two results.
func second[T any](_ T, err error) error { return err }
