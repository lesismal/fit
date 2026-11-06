package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// planOf reads a plan as the assistant gets it.
func planOf(t *testing.T) func(data []byte, err error) planOut {
	return func(data []byte, err error) planOut {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var p planOut
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		return p
	}
}

// TestAssistantPatch: a change asked for a group becomes a rollout plan,
// each member's diff and dry run taken and nothing applied, with the
// strategy suggested; it runs only once a person starts it.
func TestAssistantPatch(t *testing.T) {
	k := newRoFixture(t, 3)
	a := k.m.Assistant()
	p := planOf(t)(a.PlanRollout(t.Context(), "ann", []byte(`{"kind":"patch","group":"edge","description":"debug logs",
		"config":{"log":{"level":"debug"}},"strategy":{"mode":"canary","canary":1,"batch_size":1,"gate":{"wait_ms":1}}}`)))
	if p.State != RolloutPlanned || p.Kind != RolloutPatch || p.Source != PlanSource || p.Suggested == nil || p.Suggested.Canary != 1 ||
		p.Suggested.OnFailure != OnFailureBatch || p.Link != "#/cluster/rollout/"+p.ID || !strings.HasPrefix(p.Note, "planned: nothing was applied") ||
		string(p.Config) != `{"log":{"level":"debug"}}` || p.PlannedBy != "ann" || len(p.Members) != 3 {
		t.Fatalf("%+v", p)
	}
	for _, x := range p.Members {
		if x.Status != MemberReady || x.NewETag == "" || len(x.Changes) != 1 || x.Changes[0].Path != "log" || x.Changes[0].State != "added" ||
			string(x.Changes[0].Want) != `{"level":"debug"}` {
			t.Fatalf("%+v", x)
		}
	}
	for name, mem := range k.ms {
		mem.mu.Lock()
		puts, dry := mem.puts, mem.dryRuns
		mem.mu.Unlock()
		if puts != 0 || dry != 1 || strings.Contains(mem.config(), "debug") {
			t.Fatalf("%s: %d puts, %d dry runs", name, puts, dry)
		}
	}
	// The audit log says what it plans; the page reads the suggestion.
	if e := k.m.AuditWhere(AuditFilter{Action: ActionRolloutPlan}); len(e) != 1 || !strings.Contains(e[0].Detail, "a patch of log") {
		t.Fatalf("%+v", e)
	}
	k.start(p.ID, fast(Strategy{Mode: StrategyAll}))
	k.waitRollout(p.ID, "done", ended)
	for name, mem := range k.ms {
		if !strings.Contains(mem.config(), `"debug"`) {
			t.Fatalf("%s: %s", name, mem.config())
		}
	}
	// A patch's secrets are each member's own: one that has none there
	// is not ready.
	k.ms["m1"].edit(`{"log":{"level":"info","x_token":"own-secret-1"}}`)
	k.ms["m2"].edit(`{"log":{"level":"info","x_token":"own-secret-2"}}`)
	p = planOf(t)(a.PlanRollout(t.Context(), "ann", []byte(`{"kind":"patch","nodes":["m1","m2","m3"],"config":{"log":{"level":"warn","x_token":"[secret]"}}}`)))
	if strings.Contains(string(mustJSON(p)), "own-secret") || memberOf(t, p.Rollout, "m3").Status != MemberError ||
		!strings.Contains(memberOf(t, p.Rollout, "m3").Error.Message, "no secret is kept there") {
		t.Fatalf("%s", mustJSON(p))
	}
	k.start(p.ID, fast(Strategy{Mode: StrategyAll}))
	k.waitRollout(p.ID, "done", ended)
	if c := k.ms["m2"].config(); !strings.Contains(c, `"own-secret-2"`) || !strings.Contains(c, `"warn"`) {
		t.Fatal(c)
	}
	if r, _ := k.m.RolloutByID(p.ID); r.Config != nil {
		t.Fatalf("a finished rollout keeps its config: %s", r.Config)
	}
	// What a patch takes.
	for _, c := range []struct{ req, err string }{
		{`{"kind":"patch","group":"edge"}`, "a patch carries the parts"},
		{`{"kind":"patch","config":{"log":{}}}`, "a patch names the members"},
		{`{"kind":"patch","group":"edge","config":{"log":{"file":"${node.name}.log"}}}`, "takes no variables"},
		{`{"kind":"patch","group":"edge","config":{"admin":{}}}`, "admin"},
		{`{"kind":"patch","group":"edge","config":[1]}`, "JSON object"},
		{`{"kind":"copy","from":"m1","paths":["log"],"group":"edge","config":{"log":{}}}`, "only a patch or a template's draft"},
		{`{"kind":"patch","group":"edge","config":{"log":"` + strings.Repeat("x", MaxRolloutConfigBytes) + `"}}`, "bytes at most"},
		{`{"kind":"patch","group":"edge","config":{"log":{}},"strategy":{"mode":"fast"}}`, "not a strategy"},
		{`{"kind":"patch","group":"edge","config":{"log":{}},"extra":1}`, "not what a plan takes"},
		{`{"kind":"nope","group":"edge"}`, "patch"},
	} {
		if _, err := a.PlanRollout(t.Context(), "ann", []byte(c.req)); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: %v", c.req, err)
		}
	}
}

// TestAssistantDraft: a template's draft is planned on its members without
// saving the template; starting the rollout saves it, as the person who
// starts it, then applies it; a template changed since refuses the start.
func TestAssistantDraft(t *testing.T) {
	k := newRoFixture(t, 2)
	a := k.m.Assistant()
	draft := `{"kind":"template","template":"api","config":{"routes":[{"name":"api","match":{"path":["/api"]},"handler":{"module":"respond","params":{"body":"v2"}}}]}}`
	p := planOf(t)(a.PlanRollout(t.Context(), "ann", []byte(draft)))
	if p.State != RolloutPlanned || p.TemplateBase == "" || len(p.Members) != 2 {
		t.Fatalf("%s", mustJSON(p))
	}
	for _, x := range p.Members {
		if x.Status != MemberReady || len(x.Changes) != 1 || x.Changes[0].Path != "routes[api]" || !strings.Contains(string(x.Changes[0].Want), `"v2"`) {
			t.Fatalf("%+v", x)
		}
	}
	if v, _ := k.m.TemplateByName("api"); strings.Contains(string(v.Config), "v2") {
		t.Fatal("the plan saved the template")
	}
	// A strategy that does not check saves nothing.
	if _, err := k.m.StartRollout("bob", p.ID, &StartRequest{Strategy: Strategy{Mode: "nope"}}); err == nil {
		t.Fatal("started")
	}
	if v, _ := k.m.TemplateByName("api"); strings.Contains(string(v.Config), "v2") {
		t.Fatal("a refused start saved the template")
	}
	k.start(p.ID, fast(Strategy{Mode: StrategyAll}))
	k.waitRollout(p.ID, "done", ended)
	if v, _ := k.m.TemplateByName("api"); !strings.Contains(string(v.Config), "v2") {
		t.Fatalf("%s", v.Config)
	}
	for name, mem := range k.ms {
		if !strings.Contains(mem.config(), `"v2"`) {
			t.Fatalf("%s: %s", name, mem.config())
		}
	}
	e := k.m.AuditWhere(AuditFilter{Action: ActionTemplateUpdate})
	if len(e) == 0 || e[0].User != "bob" || e[0].Rollout != p.ID || !strings.Contains(e[0].Diff, `"v2"`) {
		t.Fatalf("%+v", e)
	}
	// A template changed between the plan and the start.
	p = planOf(t)(a.PlanRollout(t.Context(), "ann", []byte(strings.Replace(draft, "v2", "v3", 1))))
	k.template(`"by hand"`)
	if _, err := k.m.StartRollout("bob", p.ID, &StartRequest{Strategy: fast(Strategy{Mode: StrategyAll})}); err == nil || !strings.Contains(err.Error(), "changed since the rollout was planned") {
		t.Fatal(err)
	}
	// A draft saved by a start that went no further is not saved again.
	_, _ = k.m.SteerRollout("bob", p.ID, SteerDiscard)
	p = planOf(t)(a.PlanRollout(t.Context(), "ann", []byte(strings.Replace(draft, "v2", "v4", 1))))
	k.m.mu.Lock()
	k.m.ops.busy = map[string]string{"m1": "op x", "m2": "op x"}
	k.m.mu.Unlock()
	if _, err := k.m.StartRollout("bob", p.ID, &StartRequest{Strategy: fast(Strategy{Mode: StrategyAll})}); err == nil || !strings.Contains(err.Error(), "runs on") {
		t.Fatal(err, statuses(p.Rollout))
	}
	k.m.mu.Lock()
	k.m.ops.busy = map[string]string{}
	k.m.mu.Unlock()
	if _, err := k.m.StartRollout("bob", p.ID, &StartRequest{Strategy: fast(Strategy{Mode: StrategyAll})}); err != nil {
		t.Fatal(err)
	}
	k.waitRollout(p.ID, "done", ended)
	if v, _ := k.m.TemplateByName("api"); !strings.Contains(string(v.Config), "v4") {
		t.Fatalf("%s", v.Config)
	}
	// The template removed since.
	p = planOf(t)(a.PlanRollout(t.Context(), "ann", []byte(strings.Replace(draft, "v2", "v5", 1))))
	k.waitRollout(p.ID, "planned", func(r Rollout) bool { return r.State == RolloutPlanned })
	if err := k.m.RemoveTemplate("tester", "api"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.m.StartRollout("bob", p.ID, &StartRequest{Strategy: fast(Strategy{Mode: StrategyAll})}); err == nil || !strings.Contains(err.Error(), "was removed") {
		t.Fatal(err)
	}
	if _, err := a.PlanRollout(t.Context(), "ann", []byte(draft)); err == nil {
		t.Fatal("a draft of no template")
	}
}

// TestAssistantDraftSecrets: a draft's [secret] keeps the template's, which
// the answers mask and the members get.
func TestAssistantDraftSecrets(t *testing.T) {
	k := newRoFixture(t, 1)
	if _, err := k.m.AddTemplate("tester", &TemplateInput{Name: "auth", Groups: []string{"edge"},
		Config: json.RawMessage(`{"log":{"level":"info","api_key":"tpl-secret-123"}}`)}); err != nil {
		t.Fatal(err)
	}
	a := k.m.Assistant()
	p := planOf(t)(a.PlanRollout(t.Context(), "ann", []byte(`{"kind":"template","template":"auth","config":{"log":{"level":"debug","api_key":"[secret]"}}}`)))
	if s := string(mustJSON(p)); strings.Contains(s, "tpl-secret-123") || !strings.Contains(s, `"api_key":"[secret]"`) {
		t.Fatal(s)
	}
	k.start(p.ID, fast(Strategy{Mode: StrategyAll}))
	k.waitRollout(p.ID, "done", ended)
	if c := k.ms["m1"].config(); !strings.Contains(c, "tpl-secret-123") || !strings.Contains(c, `"debug"`) {
		t.Fatal(c)
	}
	if _, err := a.PlanRollout(t.Context(), "ann", []byte(`{"kind":"template","template":"auth","config":{"log":{"other_secret":"[secret]"}}}`)); err == nil ||
		!strings.Contains(err.Error(), "no secret is kept there") {
		t.Fatal(err)
	}
}

// TestAssistantRoles: the manager's roles bound what the assistant plans
// for a user: an operator may not draft a template it may not edit, nor
// patch; it may roll out the templates there are and propose operations
// on its members; a viewer neither.
func TestAssistantRoles(t *testing.T) {
	k := newRoFixture(t, 2)
	if _, err := k.m.SetAccess("ann", &Access{Users: []UserRoles{{User: "ann", Role: RoleAdmin}, {User: "opal", Groups: map[string]string{"edge": RoleOperator}},
		{User: "vic", Role: RoleViewer}}}); err != nil {
		t.Fatal(err)
	}
	a := k.m.Assistant()
	draft := `{"kind":"template","template":"api","config":{"routes":[{"name":"api","handler":{"module":"respond"}}]}}`
	for _, req := range []string{draft, `{"kind":"patch","group":"edge","config":{"log":{}}}`} {
		if _, err := a.PlanRollout(t.Context(), "opal", []byte(req)); !IsForbidden(err) {
			t.Fatalf("%s: %v", req, err)
		}
	}
	p := planOf(t)(a.PlanRollout(t.Context(), "opal", []byte(`{"kind":"template","template":"api"}`)))
	if p.State != RolloutPlanned {
		t.Fatalf("%+v", p)
	}
	if _, err := a.CheckOp("opal", []byte(`{"op":"reload","group":"edge"}`)); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func() error{
		func() error { _, err := a.CheckOp("vic", []byte(`{"op":"reload","group":"edge"}`)); return err },
		func() error {
			_, err := a.PlanRollout(t.Context(), "vic", []byte(`{"kind":"sync","group":"edge"}`))
			return err
		},
		func() error { _, err := a.FixDrift(t.Context(), "vic", []string{"m1"}); return err },
		func() error { _, err := a.Members("nemo", "", "", ""); return err },
		func() error { _, err := a.Templates("nemo", ""); return err },
		func() error { _, err := a.Rollouts("nemo", ""); return err },
		func() error { _, err := a.Metrics(t.Context(), "nemo", "", nil); return err },
		func() error {
			_, _, _, _, err := a.MemberCall(t.Context(), "nemo", "m1", "GET", "/status", nil)
			return err
		},
	} {
		if err := f(); !IsForbidden(err) {
			t.Errorf("%v", err)
		}
	}
	// An operator may not start an admin's draft: starting saves the
	// template.
	p = planOf(t)(a.PlanRollout(t.Context(), "ann", []byte(draft)))
	if _, err := k.m.StartRollout("opal", p.ID, &StartRequest{Strategy: fast(Strategy{Mode: StrategyAll})}); !IsForbidden(err) {
		t.Fatal(err)
	}
	if v, _ := k.m.TemplateByName("api"); strings.Contains(string(v.Config), `"module":"respond"}`) {
		t.Fatal("saved")
	}
}

// TestAssistantReadsBounded: a read across members answers within its
// context when a member is slow and another down, each said so, the
// others read.
func TestAssistantReadsBounded(t *testing.T) {
	k := newRoFixture(t, 3)
	k.ms["m2"].set(func(f *roMember) { f.statusDelay = 3 * time.Second })
	k.ms["m3"].srv.Close()
	a := k.m.Assistant()
	for _, read := range []func(ctx context.Context) ([]byte, error){
		func(ctx context.Context) ([]byte, error) { return a.Metrics(ctx, "ann", "edge", nil) },
		func(ctx context.Context) ([]byte, error) {
			return a.Certs(ctx, "ann", "", []string{"m1", "m2", "m3", "m1"}, 0)
		},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
		start := time.Now()
		data, err := read(ctx)
		cancel()
		if took := time.Since(start); err != nil || took > 2500*time.Millisecond {
			t.Fatalf("%v after %s", err, took)
		}
		s := string(data)
		if !strings.Contains(s, `"node":"m2"`) || !strings.Contains(s, `"kind":"timeout"`) || !strings.Contains(s, `"kind":"unreachable"`) {
			t.Fatal(s)
		}
	}
	data, _ := a.Metrics(t.Context(), "ann", "", []string{"m1"})
	var m struct {
		Members []struct {
			Node     string
			Requests *uint64
			Error    any
		}
		ByGroup map[string]struct{ Members, Read int } `json:"by_group"`
	}
	if json.Unmarshal(data, &m) != nil || len(m.Members) != 1 || m.Members[0].Requests == nil || m.Members[0].Error != nil ||
		m.ByGroup["edge"].Read != 1 || m.ByGroup["(all)"].Members != 1 {
		t.Fatalf("%s", data)
	}
	for _, c := range []struct {
		group string
		nodes []string
		err   string
	}{{"nope", nil, "no group"}, {"", []string{"x"}, "no member"}} {
		if _, err := a.Metrics(t.Context(), "ann", c.group, c.nodes); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Error(err)
		}
	}
	if _, err := a.Certs(t.Context(), "ann", "", nil, -1); err == nil {
		t.Error("negative days")
	}
	if _, err := a.Certs(t.Context(), "ann", "nope", nil, 0); err == nil {
		t.Error("no group")
	}
}

// TestAssistantNoCredentials: no member's password, token or session
// reaches anything the assistant reads, when its login fails with them in
// the answer, or when it answers.
func TestAssistantNoCredentials(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	good, bad := newFakeMember(t, true), newFakeMember(t, false)
	in := consoleInput("good", good.srv.URL)
	in.TLS.CA = good.caPEM()
	f.add(in)
	f.add(consoleInput("bad", bad.srv.URL))
	f.add(tokenInput("tok", downURL(t)))
	bad.set(func(f *fakeMember) { f.loginStatus = 500 })
	f.waitUp("good")
	f.waitState("bad", "to fail", func(s State) bool { return s.Error != nil })
	a := f.m.Assistant()
	var outs []string
	keep := func(data []byte, err error) {
		outs = append(outs, string(data))
		if err != nil {
			outs = append(outs, err.Error())
		}
	}
	keep(a.Members("admin", "", "", ""))
	keep(a.Metrics(t.Context(), "admin", "", nil))
	keep(a.Certs(t.Context(), "admin", "", nil, 0))
	keep(a.Rollouts("admin", ""))
	keep(a.Templates("admin", ""))
	keep(a.CheckOp("admin", []byte(`{"op":"test","members":["good","bad","tok"]}`)))
	keep(a.PlanRollout(t.Context(), "admin", []byte(`{"kind":"sync","nodes":["good","bad","tok"]}`)))
	for _, n := range []string{"good", "bad", "tok"} {
		_, _, body, _, err := a.MemberCall(t.Context(), "admin", n, "GET", "/status", nil)
		keep(body, err)
	}
	all := strings.Join(outs, "\n")
	for _, s := range []string{fakePassword, fakeToken, "fit_console_2020", "csrf-s"} {
		if strings.Contains(all, s) {
			t.Errorf("an answer carries %q", s)
		}
	}
	if !strings.Contains(all, `"name":"bad"`) || !strings.Contains(all, "the login broke with") {
		t.Fatal(all)
	}
}

// TestAssistantMemberCall: a member argument reads the member, as user,
// what the read tools read and never what writes; a member of another
// version is noted; a member that does not answer in time is said so.
func TestAssistantMemberCall(t *testing.T) {
	k := newRoFixture(t, 2)
	a := k.m.Assistant()
	k.waitUp("m1")
	status, h, body, note, err := a.MemberCall(t.Context(), "ann", "m1", "GET", "/config", nil)
	if err != nil || status != 200 || h.Get("ETag") == "" || !strings.Contains(string(body), `"own"`) || !strings.Contains(note, "another version of fit") {
		t.Fatalf("%v %d %s %q", err, status, body, note)
	}
	if status, _, _, _, err := a.MemberCall(t.Context(), "ann", "m1", "GET", "/versions/1?x=1", nil); err != nil || status != 404 {
		t.Fatalf("%v %d", err, status)
	}
	if _, _, _, _, err := a.MemberCall(t.Context(), "ann", "m1", "POST", "/config/render", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"PUT", "/config"}, {"GET", "/tokens"}, {"POST", "/reload"}, {"GET", "/logs"}, {"DELETE", "/node/managed"}} {
		if _, _, _, _, err := a.MemberCall(t.Context(), "ann", "m1", c[0], c[1], nil); err == nil || !strings.Contains(err.Error(), "never changed") {
			t.Errorf("%v: %v", c, err)
		}
	}
	if _, _, _, _, err := a.MemberCall(t.Context(), "ann", "nope", "GET", "/status", nil); err == nil {
		t.Error("no member")
	}
	k.ms["m2"].set(func(f *roMember) { f.statusDelay = 2 * time.Second })
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, _, _, _, err := a.MemberCall(ctx, "ann", "m2", "GET", "/status", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	k.ms["m2"].srv.Close()
	if _, _, _, _, err := a.MemberCall(t.Context(), "ann", "m2", "GET", "/config", nil); err == nil || !strings.Contains(err.Error(), `the member "m2"`) {
		t.Fatal(err)
	}
}

// TestAssistantLists: the members, templates and rollouts as the assistant
// reads them, filtered, and what they refuse.
func TestAssistantLists(t *testing.T) {
	k := newRoFixture(t, 3)
	k.ms["m3"].srv.Close()
	k.waitUp("m1")
	k.waitState("m3", "down", func(s State) bool { return s.Status == StatusDown || s.Status == StatusError })
	waitFor(t, "a drift read", func() bool { return len(k.m.SyncStates()) >= 2 })
	a := k.m.Assistant()
	var ms struct {
		Members []memberRow
		Listed  int
		Total   int
	}
	data, err := a.Members("ann", "edge", "role=edge", "up")
	if err != nil || json.Unmarshal(data, &ms) != nil || ms.Total != 3 || ms.Listed != 2 || ms.Members[0].Name != "m1" ||
		ms.Members[0].FitVersion != "v1" || ms.Members[0].Note == "" || ms.Members[0].Templates == nil {
		t.Fatalf("%v %s", err, data)
	}
	for _, c := range [][3]string{{"nope", "", ""}, {"", "=x", ""}, {"", "", "sideways"}} {
		if _, err := a.Members("ann", c[0], c[1], c[2]); err == nil {
			t.Errorf("%v", c)
		}
	}
	data, err = a.Templates("ann", "")
	if err != nil || !strings.Contains(string(data), `"name":"api"`) || !strings.Contains(string(data), `"parts":["routes[api]"]`) ||
		!strings.Contains(string(data), `"out_of_sync":`) {
		t.Fatalf("%v %s", err, data)
	}
	data, err = a.Templates("ann", "api")
	if err != nil || !strings.Contains(string(data), `"state":"out_of_sync"`) || !strings.Contains(string(data), `"path":"routes[api]"`) {
		t.Fatalf("%v %s", err, data)
	}
	if _, err := a.Templates("ann", "nope"); err == nil {
		t.Error("no template")
	}
	p := planOf(t)(a.PlanRollout(t.Context(), "ann", []byte(`{"kind":"template","template":"api","nodes":["m1","m2"]}`)))
	data, err = a.Rollouts("ann", "")
	if err != nil || !strings.Contains(string(data), p.ID) || !strings.Contains(string(data), `"operations":[]`) {
		t.Fatalf("%v %s", err, data)
	}
	data, err = a.Rollouts("ann", p.ID)
	if err != nil || !strings.Contains(string(data), `"changes":[`) || !strings.Contains(string(data), `"link":"#/cluster/rollout/`+p.ID) {
		t.Fatalf("%v %s", err, data)
	}
	if _, err := a.Rollouts("ann", "nope"); err == nil {
		t.Error("no rollout")
	}
	// Past the bounds: older rollouts are not listed; a change's values
	// are cut; the changes of the members past the first are left out.
	k.m.mu.Lock()
	for range assistantRollouts + 2 {
		k.m.ro.list = append(k.m.ro.list, &Rollout{ID: newOpID(), State: RolloutExpired, Counts: RolloutCounts{}})
	}
	k.m.mu.Unlock()
	if data, _ = a.Rollouts("ann", ""); !strings.Contains(string(data), "older rollouts are not listed") {
		t.Fatalf("%s", data)
	}
	big := json.RawMessage(`"` + strings.Repeat("x", assistantChangeBytes) + `"`)
	if c := cutJSON(big); len(c) > assistantChangeBytes || !strings.Contains(string(c), "bytes, cut") {
		t.Fatalf("%s", c)
	}
	r := Rollout{ID: "x", Members: make([]RolloutMember, assistantPlanMembers+1)}
	for i := range r.Members {
		r.Members[i].Changes = []Change{{Path: "log"}}
	}
	if v := planView(&r, true); v.Members[assistantPlanMembers].Changes != nil || v.Members[0].Changes == nil || !strings.Contains(v.Note, "first 64") {
		t.Fatalf("%+v", v.Note)
	}
	// A plan still planning when the wait ends says so.
	k.ms["m1"].set(func(f *roMember) { f.nodeDelay = time.Second })
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	p = planOf(t)(a.PlanRollout(ctx, "ann", []byte(`{"kind":"sync","nodes":["m1"]}`)))
	if p.State != RolloutPlanning || !strings.Contains(p.Note, "still planning") {
		t.Fatalf("%+v", p)
	}
	k.waitRollout(p.ID, "planned", func(r Rollout) bool { return r.State != RolloutPlanning })
}

// TestAssistantOpsAndDrift: an operation is checked and linked, never
// started; a drift fix plans a sync rollout for members out of sync and a
// reload for those whose consistency check differs.
func TestAssistantOpsAndDrift(t *testing.T) {
	k := newRoFixture(t, 3)
	a := k.m.Assistant()
	data, err := a.CheckOp("ann", []byte(`{"op":"purge","group":"edge","members":["m2","m1"],"purge":{"zone":"z","prefixes":["https://a.example/img/"]}}`))
	var op struct {
		Members []string
		Link    string
	}
	if err != nil || json.Unmarshal(data, &op) != nil || strings.Join(op.Members, ",") != "m1,m2" ||
		op.Link != `#/cluster/group/edge?op=purge&members=m1%2Cm2&purge=%7B%22zone%22%3A%22z%22%2C%22prefixes%22%3A%5B%22https%3A%2F%2Fa.example%2Fimg%2F%22%5D%7D` {
		t.Fatalf("%v %s", err, data)
	}
	if len(k.m.Ops()) != 0 {
		t.Fatal("an operation started")
	}
	for _, req := range []string{`{"op":"dance","group":"edge"}`, `{"op":"reload","members":["x"]}`, `{"op":"reload","group":"edge","purge":{"all":true}}`,
		`{"op":"purge","group":"edge"}`, `{"op":"reload","what":1}`} {
		if _, err := a.CheckOp("ann", []byte(req)); err == nil {
			t.Errorf("%s", req)
		}
	}
	// m1 and m2 are out of sync with the template api (they lack its
	// route); m3 is in sync and its consistency check finds a difference.
	k.ms["m3"].set(func(f *roMember) { f.drift = 1 })
	if _, err := k.m.UpdateTemplate("tester", "api", &TemplateInput{Groups: []string{"edge"}, Config: json.RawMessage(`{"routes":[{"name":"api","match":{"path":["/api"]}}]}`)}); err != nil {
		t.Fatal(err)
	}
	k.ms["m3"].edit(`{"listeners":[{"name":"web","address":"127.0.0.1:0"}],"routes":[{"name":"own","handler":{"module":"respond","params":{"body":"ok"}}},{"name":"api","match":{"path":["/api"]}}]}`)
	waitFor(t, "the drift reads", func() bool {
		n := 0
		for _, d := range k.m.DriftStates() {
			if d.Node == "m3" && d.Templates != nil && d.Templates.State == SyncInSync && d.Consistency != nil && d.Consistency.Count == 1 ||
				d.Node != "m3" && d.Fixable {
				n++
			}
		}
		return n == 3
	})
	k.ms["m2"].set(func(f *roMember) { f.drift = 0 })
	data, err = a.FixDrift(t.Context(), "ann", []string{"m1", "m3", "m1"})
	var fix struct {
		Rollout planOut
		Reload  struct{ Members []string }
		Link    string
	}
	if err != nil || json.Unmarshal(data, &fix) != nil || fix.Rollout.Kind != RolloutSync || len(fix.Rollout.Members) != 1 ||
		fix.Rollout.Members[0].Node != "m1" || strings.Join(fix.Reload.Members, ",") != "m3" || fix.Link != fix.Rollout.Link {
		t.Fatalf("%v %s", err, data)
	}
	data, err = a.FixDrift(t.Context(), "ann", []string{"m3"})
	if err != nil || !strings.Contains(string(data), `"link":"#/cluster/ops?op=reload\u0026members=m3"`) {
		t.Fatalf("%v %s", err, data)
	}
	// A member with nothing to fix.
	k.ms["m3"].set(func(f *roMember) { f.drift = 0 })
	k.ms["m3"].edit(strings.Replace(k.ms["m3"].config(), `"ok"`, `"ok2"`, 1))
	waitFor(t, "the drift to go", func() bool {
		for _, d := range k.m.DriftStates() {
			if d.Node == "m3" {
				return d.Consistency != nil && d.Consistency.Count == 0 && !d.Fixable
			}
		}
		return false
	})
	if data, err = a.FixDrift(t.Context(), "ann", []string{"m3"}); err != nil || string(data) != `{"nothing_to_fix":["m3"]}` {
		t.Fatalf("%v %s", err, data)
	}
	for _, nodes := range [][]string{nil, {"nope"}, make([]string, 17)} {
		if _, err := a.FixDrift(t.Context(), "ann", nodes); err == nil {
			t.Errorf("%v", nodes)
		}
	}
	// A reload a member is busy for refuses the fix.
	k.m.mu.Lock()
	k.m.ops.busy = map[string]string{"m3": "op y"}
	k.m.mu.Unlock()
	k.ms["m3"].set(func(f *roMember) { f.drift = 1 })
	waitFor(t, "the drift again", func() bool {
		for _, d := range k.m.DriftStates() {
			if d.Node == "m3" {
				return d.Consistency != nil && d.Consistency.Count == 1
			}
		}
		return false
	})
	if _, err := a.FixDrift(t.Context(), "ann", []string{"m3"}); err == nil {
		t.Error("busy")
	}
	// A sync the manager refuses (too many plans) refuses the fix.
	k.m.mu.Lock()
	k.m.ops.busy = map[string]string{}
	for range MaxActiveRollouts {
		k.m.ro.list = append(k.m.ro.list, &Rollout{ID: newOpID(), State: RolloutPlanned, Planned: k.m.now(), Counts: RolloutCounts{}})
	}
	k.m.mu.Unlock()
	if _, err := a.FixDrift(t.Context(), "ann", []string{"m1"}); err == nil {
		t.Error("too many plans")
	}
}
