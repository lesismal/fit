package cluster

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// slowPolls is a manager that polls each member once, then hourly, so
// that the polls do not take a fake's one-off answers.
var slowPolls = config.Cluster{MaxNodes: 16, MaxConcurrency: 4, PollInterval: config.Duration(time.Hour)}

// TestRolloutRestart: a manager restarted between batches shows the
// rollout paused where it stopped, its members still held, and resumes it;
// one restarted while a member's apply was under way finds by the member's
// ETag that it was applied, and gates it.
func TestRolloutRestart(t *testing.T) {
	k := newRoFixture(t, 2)
	r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
	r = k.start(r.ID, fast(Strategy{Mode: StrategyRolling, BatchSize: 1, PauseMs: 3600_000}))
	r = k.waitRollout(r.ID, "between batches", func(r Rollout) bool { return r.Batch == 1 && !r.NextAt.IsZero() })
	k.m.Close()
	k.start2()
	r, _ = k.m.RolloutByID(r.ID)
	if r.State != RolloutPaused || r.Batch != 1 || !strings.Contains(r.Reason, "stopped") || memberOf(t, r, "m1").Status != MemberDone {
		t.Fatalf("%s %s %d %s", r.State, r.Reason, r.Batch, statuses(r))
	}
	if _, err := k.m.StartOp("op", &OpRequest{Op: OpReload, Members: []string{"m2"}}); err == nil {
		t.Error("an operation ran on a member of a paused rollout")
	}
	if _, err := k.m.SteerRollout("bob", r.ID, SteerResume); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "done", ended)
	if r.State != RolloutDone || r.Counts[MemberDone] != 2 {
		t.Fatalf("%s %s %s %+v", r.State, r.Reason, statuses(r), memberOf(t, r, "m2").Error)
	}

	// Stopped in the middle of an apply.
	k.template(`"v2"`)
	k.ms["m1"].set(func(f *roMember) { f.putDelay = 300 * time.Millisecond })
	r = k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api", Nodes: []string{"m1"}})
	r = k.start(r.ID, fast(Strategy{Mode: StrategyAll}))
	k.waitMember(r.ID, "m1", MemberApplying)
	k.m.Close()
	<-k.ms["m1"].applied
	k.ms["m1"].set(func(f *roMember) { f.putDelay = 0 })
	k.start2()
	r, _ = k.m.RolloutByID(r.ID)
	if r.State != RolloutPaused || memberOf(t, r, "m1").Status != MemberApplying {
		t.Fatalf("%s %s", r.State, statuses(r))
	}
	if _, err := k.m.SteerRollout("bob", r.ID, SteerResume); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "done", ended)
	if x := memberOf(t, r, "m1"); r.State != RolloutDone || x.Status != MemberDone || x.Applied != 3 {
		t.Fatalf("%s %+v", r.State, x)
	}

	// What a restart makes of a plan and of a rollback under way.
	var doc document
	_ = json.Unmarshal(k.store.Section(Section), &doc)
	doc.Rollouts = append(doc.Rollouts, Rollout{ID: "p1", State: RolloutPlanning, Members: []RolloutMember{{Node: "m1", Status: MemberPlanning}}},
		Rollout{ID: "r1", State: RolloutRunning, Members: []RolloutMember{{Node: "m1", Status: MemberDone}}},
		Rollout{ID: "b1", State: RolloutRollingBack, Members: []RolloutMember{{Node: "m2", Status: MemberDone, Applied: 3}}})
	if err := k.store.SetSection(Section, doc.marshal()); err != nil {
		t.Fatal(err)
	}
	k.m.Close()
	k.start2()
	if p, _ := k.m.RolloutByID("p1"); p.State != RolloutExpired {
		t.Errorf("%+v", p)
	}
	if b, _ := k.m.RolloutByID("b1"); b.State != RolloutFailed || !strings.Contains(b.Reason, "roll it back again") {
		t.Errorf("%+v", b)
	}
	if x, _ := k.m.RolloutByID("r1"); x.State != RolloutPaused || !strings.Contains(x.Reason, "restarted") {
		t.Errorf("%+v", x)
	}
}

// start2 starts the fixture's manager again, on its store.
func (k *roFixture) start2() {
	k.t.Helper()
	k.fixture.start()
}

// TestRolloutCopy copies parts of one member's config to others.
func TestRolloutCopy(t *testing.T) {
	k := newRoFixture(t, 3)
	k.ms["m1"].edit(`{"log":{"level":"debug"},"listeners":[{"name":"web","address":"127.0.0.1:0"}],"routes":[{"name":"own","handler":{"module":"respond","params":{"body":"from-m1"}}}]}`)
	r := k.plan(RolloutRequest{Kind: RolloutCopy, From: "m1", Paths: []string{"log", "routes[own]"}, Group: "edge"})
	if len(r.Members) != 2 || r.Counts[MemberReady] != 2 {
		t.Fatalf("%s", statuses(r))
	}
	x := memberOf(t, r, "m2")
	if len(x.Changes) != 2 || x.Changes[0].Path != "routes[own]" || x.Changes[0].State != CompareDiffers || x.Changes[1].State != "added" {
		t.Fatalf("%+v", x.Changes)
	}
	r = k.start(r.ID, fast(Strategy{Mode: StrategyAll}))
	r = k.waitRollout(r.ID, "done", ended)
	if r.State != RolloutDone || !strings.Contains(k.ms["m3"].config(), "from-m1") || !strings.Contains(k.ms["m3"].config(), "debug") {
		t.Fatalf("%s %s", r.State, k.ms["m3"].config())
	}
	// A part the source has not, and a source that cannot be read.
	r = k.plan(RolloutRequest{Kind: RolloutCopy, From: "m1", Paths: []string{"access_log"}, Nodes: []string{"m2"}})
	if x := memberOf(t, r, "m2"); x.Status != MemberError || !strings.Contains(x.Error.Message, "access_log") {
		t.Fatalf("%+v", x)
	}
	k.ms["m1"].set(func(f *roMember) { f.configStatus = 500 })
	r = k.plan(RolloutRequest{Kind: RolloutCopy, From: "m1", Paths: []string{"log"}, Nodes: []string{"m2", "m3"}})
	if x := memberOf(t, r, "m3"); x.Status != MemberError || !strings.Contains(x.Error.Message, "could not be read") {
		t.Fatalf("%+v", x)
	}
	for _, req := range []RolloutRequest{
		{Kind: RolloutCopy, Paths: []string{"log"}, Nodes: []string{"m2"}},
		{Kind: RolloutCopy, From: "nobody", Paths: []string{"log"}, Nodes: []string{"m2"}},
		{Kind: RolloutCopy, From: "m1", Nodes: []string{"m2"}},
		{Kind: RolloutCopy, From: "m1", Paths: make([]string, MaxTemplateParts+1), Nodes: []string{"m2"}},
		{Kind: RolloutCopy, From: "m1", Paths: []string{"admin"}, Nodes: []string{"m2"}},
		{Kind: RolloutCopy, From: "m1", Paths: []string{"log"}},
		{Kind: RolloutCopy, From: "m1", Paths: []string{"log"}, Group: "none"},
		{Kind: RolloutCopy, From: "m1", Paths: []string{"log"}, Nodes: []string{"m1"}},
		{Kind: RolloutCopy, From: "m1", Paths: []string{"log"}, Nodes: []string{"nobody"}},
		{Kind: RolloutTemplate},
		{Kind: RolloutTemplate, Template: "none"},
		{Kind: RolloutTemplate, Template: "api", Nodes: []string{"nobody"}},
		{Kind: RolloutSync, Group: "none"},
		{Kind: RolloutSync, Nodes: []string{"nobody"}},
		{Kind: "x"},
		{Kind: RolloutSync, Description: strings.Repeat("d", 600)},
	} {
		if _, err := k.m.PlanRollout("ann", &req); err == nil {
			t.Errorf("%+v planned", req)
		}
	}
	// A template's rollout on some of its members, a sync of a group.
	k.f2()
	r = k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api", Nodes: []string{"m2", "m2"}})
	if len(r.Members) != 1 {
		t.Fatalf("%s", statuses(r))
	}
	r = k.plan(RolloutRequest{Kind: RolloutSync, Group: "edge"})
	if len(r.Members) != 3 {
		t.Fatalf("%s", statuses(r))
	}
	if _, err := k.m.PlanRollout("ann", &RolloutRequest{Kind: RolloutTemplate, Template: "api", Nodes: []string{"m9"}}); err == nil {
		t.Error("a member of no group planned")
	}
}

// f2 adds the member m9, in no group, and discards the plans kept.
func (k *roFixture) f2() {
	k.t.Helper()
	k.ms["m9"] = newRoMember(k.t, roBase(""))
	k.add(tokenInput("m9", k.ms["m9"].srv.URL))
	for _, r := range k.m.Rollouts() {
		if r.State == RolloutPlanned {
			if _, err := k.m.SteerRollout("ann", r.ID, SteerDiscard); err != nil {
				k.t.Fatal(err)
			}
		}
	}
}

// TestRolloutPlanErrors: each member the plan cannot apply is said why, and
// left out; the bounds of plans.
func TestRolloutPlanErrors(t *testing.T) {
	k := newRoFixtureWith(t, 7, slowPolls, func(name string, f *roMember) {
		if name == "m1" {
			f.caps = []string{"node", "config.if_match"}
		}
	})
	k.ms["m2"].edit(roBase(`,"bad-config"`))
	k.ms["m3"].srv.Close()
	k.ms["m5"].set(func(f *roMember) { f.noNewETag = true })
	k.ms["m6"].set(func(f *roMember) { f.dryStatus = 412 })
	k.m.mu.Lock()
	k.m.ops.busy = map[string]string{"m4": "op x"}
	k.m.mu.Unlock()
	if _, err := k.m.AddGroup("tester", &GroupInput{Name: "seven", Members: []string{"m7"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.m.AddTemplate("tester", &TemplateInput{Name: "bad", Groups: []string{"seven"}, Config: json.RawMessage(`{"log":{"level":"${node.labels.nope}"}}`)}); err != nil {
		t.Fatal(err)
	}
	r := k.plan(RolloutRequest{Kind: RolloutSync})
	want := map[string]string{"m1": KindUnsupported, "m2": KindRefused, "m3": KindUnreachable, "m4": KindBusy, "m5": KindBadAnswer, "m6": KindRefused, "m7": KindRefused}
	for name, kind := range want {
		x := memberOf(t, r, name)
		if x.Status != MemberError || x.Error == nil || x.Error.Kind != kind {
			t.Errorf("%s: %+v %+v", name, x, x.Error)
		}
	}
	if x := memberOf(t, r, "m2"); len(x.Errors) != 2 || x.Errors[0].Path != "/routes/1/handler" {
		t.Errorf("%+v", x.Errors)
	}
	if _, err := k.m.StartRollout("bob", r.ID, &StartRequest{Strategy: Strategy{Mode: StrategyAll}}); err == nil {
		t.Error("a rollout of no member ready started", statuses(r))
	}
	k.m.mu.Lock()
	k.m.ops.busy = nil
	k.m.mu.Unlock()

	// At most MaxActiveRollouts planned or under way.
	for range MaxActiveRollouts - 1 {
		k.plan(RolloutRequest{Kind: RolloutSync, Nodes: []string{"m4"}})
	}
	_, err := k.m.PlanRollout("ann", &RolloutRequest{Kind: RolloutSync, Nodes: []string{"m4"}})
	if !errors.As(err, new(errBusy)) {
		t.Fatalf("%v", err)
	}
	// A plan not started within PlanTTL expires; discarded, it is gone.
	old := PlanTTL
	PlanTTL = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	list := k.m.Rollouts()
	PlanTTL = old
	for _, x := range list {
		if x.State != RolloutExpired {
			t.Errorf("%s %s", x.ID, x.State)
		}
	}
	if _, err := k.m.StartRollout("bob", list[0].ID, &StartRequest{Strategy: Strategy{Mode: StrategyAll}}); err == nil {
		t.Error("an expired plan started")
	}
	for _, x := range list {
		if _, err := k.m.SteerRollout("ann", x.ID, SteerDiscard); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(k.m.Rollouts()); n != 0 {
		t.Errorf("%d kept", n)
	}

	// Past the bytes a rollout keeps.
	old2 := rolloutBytes
	rolloutBytes = 10
	t.Cleanup(func() { rolloutBytes = old2 })
	r = k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api", Nodes: []string{"m4"}})
	if x := memberOf(t, r, "m4"); x.Status != MemberError || x.Error.Kind != KindTooLarge {
		t.Errorf("%+v", x)
	}
}

// TestRolloutStartErrors: what a start refuses.
func TestRolloutStartErrors(t *testing.T) {
	k := newRoFixture(t, 3)
	r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
	for _, c := range []struct {
		req StartRequest
		why string
	}{
		{StartRequest{Strategy: Strategy{Mode: "x"}}, "strategy"},
		{StartRequest{Strategy: Strategy{Mode: StrategyAll}, Exclude: []string{"nobody"}}, "not a member"},
		{StartRequest{Strategy: Strategy{Mode: StrategyAll}, Exclude: []string{"m1", "m2", "m3"}}, "no member"},
	} {
		if _, err := k.m.StartRollout("bob", r.ID, &c.req); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%+v: %v", c.req, err)
		}
	}
	if _, err := k.m.StartRollout("bob", "none", &StartRequest{}); !errors.As(err, new(errNoRollout)) {
		t.Error(err)
	}
	// A member in another rollout's hands.
	k.m.mu.Lock()
	k.m.ops.busy = map[string]string{"m2": "rollout y"}
	k.m.mu.Unlock()
	if _, err := k.m.StartRollout("bob", r.ID, &StartRequest{Strategy: Strategy{Mode: StrategyAll}}); err == nil || !strings.Contains(err.Error(), "m2 (rollout y)") {
		t.Error(err)
	}
	k.m.mu.Lock()
	k.m.ops.busy = nil
	k.m.mu.Unlock()
	r = k.start(r.ID, fast(Strategy{Mode: StrategyAll}), "m2")
	if memberOf(t, r, "m2").Status != MemberExcluded {
		t.Fatalf("%s", statuses(r))
	}
	if _, err := k.m.StartRollout("bob", r.ID, &StartRequest{Strategy: Strategy{Mode: StrategyAll}}); err == nil {
		t.Error("started twice")
	}
	r = k.waitRollout(r.ID, "done", ended)
	if r.State != RolloutDone || k.ms["m2"].version() != 1 {
		t.Fatalf("%s %s", r.State, statuses(r))
	}
	for _, what := range []string{SteerPause, SteerResume, SteerAbort, SteerDiscard, "x"} {
		if _, err := k.m.SteerRollout("bob", r.ID, what); err == nil {
			t.Errorf("%s on a done rollout", what)
		}
	}
	if _, err := k.m.SteerRollout("bob", "none", SteerPause); err == nil {
		t.Error("steered no rollout")
	}
	// Read-only, and stopped.
	k.m.mu.Lock()
	k.m.readOnly = "newer"
	k.m.mu.Unlock()
	if _, err := k.m.PlanRollout("ann", &RolloutRequest{Kind: RolloutSync}); !errors.As(err, new(errReadOnly)) {
		t.Error(err)
	}
	if _, err := k.m.SteerRollout("ann", r.ID, SteerRollback); !errors.As(err, new(errReadOnly)) {
		t.Error(err)
	}
	k.m.mu.Lock()
	k.m.readOnly = ""
	k.m.mu.Unlock()
	k.m.Close()
	if _, err := k.m.PlanRollout("ann", &RolloutRequest{Kind: RolloutSync}); err != errClosed {
		t.Error(err)
	}
	if _, err := k.m.SteerRollout("ann", r.ID, SteerRollback); err != errClosed {
		t.Error(err)
	}
}
