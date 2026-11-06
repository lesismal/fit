package cluster

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestMemberDownFiresAndResolves: a member down fires once it has been down
// for the rule's time, counted from its state's since; it is sent once,
// again at the repeat interval, and resolved once the member is up; a
// member not read since the manager started keeps its alert as it is.
func TestMemberDownFiresAndResolves(t *testing.T) {
	clock := newClock()
	m, _, log := bareManager(t, clock.now)
	sink := newHookSink(t)
	setRules(m, []AlertRule{{Name: "down", Kind: AlertMemberDown, ForMs: 60_000, RepeatMs: 10 * 60_000}}, sink.hook("ops", FormatJSON))
	t0 := clock.now()

	eval(m, t0, &evalInput{states: []State{down("a", t0), up("b", t0)}})
	if a := alertOf(m, "down", "a", ""); a == nil || a.State != AlertPending || !a.Since.Equal(t0) {
		t.Fatalf("pending: %+v", a)
	}
	eval(m, clock.add(59*time.Second), &evalInput{states: []State{down("a", t0), up("b", t0)}})
	if a := alertOf(m, "down", "a", ""); a.State != AlertPending {
		t.Fatalf("fired before its time: %+v", a)
	}
	if got := sink.requests(); len(got) != 0 {
		t.Fatalf("sent while pending: %d", len(got))
	}
	t1 := clock.add(time.Second)
	eval(m, t1, &evalInput{states: []State{down("a", t0), up("b", t0)}})
	a := alertOf(m, "down", "a", "")
	if a.State != AlertFiring || !a.Fired.Equal(t1) || a.ID == "" || a.Severity != SeverityCritical || !strings.Contains(a.Summary, "unreachable") {
		t.Fatalf("not fired: %+v", a)
	}
	got := sink.wait(t, 1)
	p := got[0].payload(t)
	if p.Status != AlertFiring || len(p.Alerts) != 1 || p.Alerts[0].Node != "a" || p.Manager != "mgr-1" || !strings.Contains(p.Text, "[FIRING]") {
		t.Fatalf("payload: %s", got[0].Body)
	}
	if got[0].Header.Get(HeaderSignature) != "" {
		t.Fatal("signed without a secret")
	}
	// Not again before the repeat interval; again at it.
	eval(m, clock.add(5*time.Minute), &evalInput{states: []State{down("a", t0)}, syncs: nil})
	if a := alertOf(m, "down", "a", ""); a.Sends != 1 {
		t.Fatalf("sent again early: %+v", a)
	}
	eval(m, clock.add(5*time.Minute), &evalInput{states: []State{down("a", t0)}})
	sink.wait(t, 2)
	if a := alertOf(m, "down", "a", ""); a.Sends != 2 {
		t.Fatalf("not sent again: %+v", a)
	}
	// Not read since a restart: kept.
	eval(m, clock.add(time.Minute), &evalInput{states: []State{{Name: "a", Status: StatusUnknown}}})
	if a := alertOf(m, "down", "a", ""); a == nil || a.State != AlertFiring {
		t.Fatalf("an unknown member's alert went: %+v", a)
	}
	// Up: resolved, and sent resolved.
	eval(m, clock.add(time.Minute), &evalInput{states: []State{up("a", clock.now())}})
	if a := alertOf(m, "down", "a", ""); a != nil {
		t.Fatalf("not resolved: %+v", a)
	}
	h := history(m)
	if len(h) != 1 || h[0].State != AlertResolved || h[0].ResolvedWhy != "" {
		t.Fatalf("history: %+v", h)
	}
	got = sink.wait(t, 3)
	if p := got[2].payload(t); p.Status != AlertResolved || p.Alerts[0].State != AlertResolved {
		t.Fatalf("resolved payload: %s", got[2].Body)
	}
	if !strings.Contains(log.String(), "fired on a") || !strings.Contains(log.String(), "resolved on a") {
		t.Fatal(log.String())
	}
	// A condition gone before its time leaves nothing.
	eval(m, clock.now(), &evalInput{states: []State{down("b", clock.now())}})
	eval(m, clock.add(time.Second), &evalInput{states: []State{up("b", clock.now())}})
	if a := alertOf(m, "down", "b", ""); a != nil || len(history(m)) != 1 {
		t.Fatalf("a pending alert went to the history: %+v", a)
	}
	if st := waitSent(t, m, "ops", 3); st.Sent != 3 || st.Failed != 0 {
		t.Fatalf("status: %+v", st)
	}
}

// TestSilencesHold: a silenced alert fires but is not sent, through its
// repeat intervals; sent once the silence ends; and sent resolved, as it
// was sent firing, though silenced again.
func TestSilencesHold(t *testing.T) {
	clock := newClock()
	m, _, _ := bareManager(t, clock.now)
	sink := newHookSink(t)
	setRules(m, []AlertRule{{Name: "down", Kind: AlertMemberDown, RepeatMs: 60_000}}, sink.hook("ops", FormatSlack))
	t0 := clock.now()
	m.al.mu.Lock()
	m.al.cfg.silences = []Silence{{ID: "s1", Node: "a", Starts: t0, Ends: t0.Add(10 * time.Minute)},
		{ID: "s2", Rule: "other", Starts: t0, Ends: t0.Add(time.Hour)}}
	m.al.mu.Unlock()
	for i := 0; i < 5; i++ {
		eval(m, clock.add(time.Minute), &evalInput{states: []State{down("a", t0)}})
	}
	if a := alertOf(m, "down", "a", ""); a.State != AlertFiring || a.Sends != 0 {
		t.Fatalf("sent while silenced: %+v", a)
	}
	v := m.Alerts()
	if len(v.Active) != 1 || v.Active[0].SilencedBy != "s1" || v.Counts.Silenced != 1 || v.Counts.Firing != 0 {
		t.Fatalf("view: %+v", v)
	}
	if c := m.AlertSummary(); c.Silenced != 1 || c.Firing != 0 {
		t.Fatalf("summary: %+v", c)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(sink.requests()); n != 0 {
		t.Fatalf("%d sent while silenced", n)
	}
	// The silence ends: sent now.
	eval(m, clock.add(6*time.Minute), &evalInput{states: []State{down("a", t0)}})
	got := sink.wait(t, 1)
	if !strings.Contains(string(got[0].Body), `"text":"[FIRING] fit cluster on`) {
		t.Fatalf("slack body: %s", got[0].Body)
	}
	// Silenced again, it resolves: sent resolved.
	m.al.mu.Lock()
	m.al.cfg.silences = append(m.al.cfg.silences, Silence{ID: "s3", Rule: "down", Starts: clock.now(), Ends: clock.now().Add(time.Hour)})
	m.al.mu.Unlock()
	eval(m, clock.add(time.Minute), &evalInput{states: []State{up("a", clock.now())}})
	got = sink.wait(t, 2)
	if !strings.Contains(string(got[1].Body), "RESOLVED") {
		t.Fatalf("resolved body: %s", got[1].Body)
	}
}

// TestDriftRule: templates out of sync or in conflict, templates that do
// not render, and a consistency check's differences, as the rule's source
// says.
func TestDriftRule(t *testing.T) {
	clock := newClock()
	m, _, _ := bareManager(t, clock.now)
	setRules(m, []AlertRule{{Name: "drift", Kind: AlertDrift}, {Name: "tpl", Kind: AlertDrift, Source: DriftTemplates},
		{Name: "chk", Kind: AlertDrift, Source: DriftConsistency}})
	now := clock.now()
	a, b, c, d := up("a", now), up("b", now), up("c", now), up("d", now)
	b.Drift = &Drift{Count: 2}
	d.Drift = &Drift{Count: 0}
	in := &evalInput{states: []State{a, b, c, d}, syncs: map[string]MemberSync{
		"a": {Node: "a", State: SyncOutOfSync, Counts: SyncCounts{OutOfSync: 2, Conflict: 1}},
		"c": {Node: "c", State: SyncError, Errors: []string{"the variable x is not set"}},
		"d": {Node: "d", State: SyncInSync},
	}}
	eval(m, now, in)
	want := map[string]string{"drift|a": "2 parts out of sync, 1 in conflict", "drift|b": "finds 2 differences", "drift|c": "do not render",
		"tpl|a": "out of sync", "tpl|c": "do not render", "chk|b": "2 differences"}
	m.al.mu.Lock()
	n := len(m.al.active)
	for k, s := range want {
		rule, node, _ := strings.Cut(k, "|")
		x := m.al.active[alertKey(rule, node, "")]
		if x == nil || !strings.Contains(x.Summary, s) {
			t.Errorf("%s: %+v", k, x)
		}
	}
	m.al.mu.Unlock()
	if n != len(want) {
		t.Fatalf("%d alerts, want %d", n, len(want))
	}
	// A conflict too, and both sources at once.
	in.syncs["b"] = MemberSync{Node: "b", State: SyncConflict, Counts: SyncCounts{Conflict: 1}}
	eval(m, clock.add(time.Second), in)
	if x := alertOf(m, "drift", "b", ""); !strings.Contains(x.Summary, "1 in conflict; its consistency check") {
		t.Fatal(x.Summary)
	}
}

// TestCertBackendsReloads: a certificate expiring within the rule's days or
// expired; upstream backends down past the threshold; a reload that failed,
// from the counts the polls read, until one succeeds, the member restarts,
// or the rule's time to resolve passes.
func TestCertBackendsReloads(t *testing.T) {
	clock := newClock()
	m, _, _ := bareManager(t, clock.now)
	setRules(m, []AlertRule{{Name: "cert", Kind: AlertCertExpiry, Days: 10}, {Name: "be", Kind: AlertBackendsDown, Threshold: 2},
		{Name: "rl", Kind: AlertReloadFailed, ResolveAfterMs: 3_600_000}})
	now := clock.now()
	st := func(name string, polled time.Time, fn func(*State)) State {
		s := up(name, polled)
		fn(&s)
		return s
	}
	certs := func(days int) func(*State) {
		return func(s *State) {
			s.Certificates = &Certificates{Count: 1, NearestExpiry: now.Add(time.Duration(days) * 24 * time.Hour), NearestNames: []string{"x.example"}, NearestListener: "web"}
		}
	}
	in := &evalInput{states: []State{
		st("soon", now, certs(5)), st("late", now, certs(30)), st("gone", now, certs(-1)), st("none", now, func(s *State) { s.Certificates = &Certificates{} }),
		st("be1", now, func(s *State) { s.Upstreams = &Upstreams{Backends: 4, BackendsDown: 1} }),
		st("be2", now, func(s *State) { s.Upstreams = &Upstreams{Backends: 4, BackendsDown: 2} }),
		st("rl", now, func(s *State) { s.Reloads = &Reloads{Succeeded: 3, Failed: 1} }),
	}}
	eval(m, now, in)
	if a := alertOf(m, "cert", "soon", ""); a == nil || !strings.Contains(a.Summary, "expires in 5 days") || !strings.Contains(a.Summary, "listener web") {
		t.Fatalf("soon: %+v", a)
	}
	if a := alertOf(m, "cert", "gone", ""); a == nil || !strings.Contains(a.Summary, "expired on") {
		t.Fatalf("gone: %+v", a)
	}
	if alertOf(m, "cert", "late", "") != nil || alertOf(m, "cert", "none", "") != nil || alertOf(m, "be", "be1", "") != nil {
		t.Fatal("fired without its condition")
	}
	if a := alertOf(m, "be", "be2", ""); a == nil || a.Summary != "2 of its 4 upstream backends are down" {
		t.Fatalf("be2: %+v", a)
	}
	if alertOf(m, "rl", "rl", "") != nil {
		t.Fatal("a failure before the manager watched fired")
	}
	reloads := func(at time.Time, ok, failed uint64, status string) {
		s := st("rl", at, func(s *State) { s.Reloads = &Reloads{Succeeded: ok, Failed: failed} })
		s.Status = status
		in.states[6] = s
		eval(m, at, in)
	}
	// The same poll again, and a poll of a member down, change nothing.
	reloads(now, 3, 2, StatusUp)
	reloads(clock.add(time.Second), 3, 2, StatusDown)
	if alertOf(m, "rl", "rl", "") != nil {
		t.Fatal("fired on a poll not read")
	}
	t1 := clock.add(time.Second)
	reloads(t1, 3, 2, StatusUp)
	if a := alertOf(m, "rl", "rl", ""); a == nil || a.State != AlertFiring || !a.Since.Equal(t1) {
		t.Fatalf("reload failed: %+v", a)
	}
	reloads(clock.add(time.Second), 4, 2, StatusUp)
	if alertOf(m, "rl", "rl", "") != nil {
		t.Fatal("not resolved by a reload that succeeded")
	}
	reloads(clock.add(time.Second), 4, 3, StatusUp)
	reloads(clock.add(time.Second), 0, 0, StatusUp) // restarted
	if alertOf(m, "rl", "rl", "") != nil {
		t.Fatal("not resolved by a restart")
	}
	reloads(clock.add(time.Second), 0, 1, StatusUp)
	if alertOf(m, "rl", "rl", "") == nil {
		t.Fatal("not fired again")
	}
	reloads(clock.add(time.Hour), 0, 1, StatusUp)
	if alertOf(m, "rl", "rl", "") != nil {
		t.Fatal("not resolved after its time")
	}
	// A member gone is no longer followed.
	in.states = in.states[:6]
	eval(m, clock.add(time.Second), in)
	m.al.mu.Lock()
	_, kept := m.al.reloads["rl"]
	m.al.mu.Unlock()
	if kept {
		t.Fatal("a member gone is still followed")
	}
}

// TestRolloutFailedRule: a failed rollout fires at once, on its ID, within
// the rule's group; and resolves once rolled back or past its time.
func TestRolloutFailedRule(t *testing.T) {
	clock := newClock()
	m, _, _ := bareManager(t, clock.now)
	setRules(m, []AlertRule{{Name: "ro", Kind: AlertRolloutFailed, ResolveAfterMs: 3_600_000}, {Name: "ro-eu", Kind: AlertRolloutFailed, Group: "eu"}})
	now := clock.now()
	in := &evalInput{states: []State{up("a", now, "eu"), up("b", now)}, rollouts: []failedRollout{
		{id: "r1", what: "a sync", reason: "the gate failed", ended: now.Add(-time.Minute), nodes: []string{"a"}},
		{id: "r2", what: "a copy", reason: "no", ended: now.Add(-2 * time.Hour), nodes: []string{"b"}},
	}}
	eval(m, now, in)
	a := alertOf(m, "ro", "", "r1")
	if a == nil || a.State != AlertFiring || !strings.Contains(a.Summary, "the gate failed") || !a.Since.Equal(now.Add(-time.Minute)) {
		t.Fatalf("r1: %+v", a)
	}
	if alertOf(m, "ro", "", "r2") != nil || alertOf(m, "ro-eu", "", "r1") == nil || alertOf(m, "ro-eu", "", "r2") != nil {
		t.Fatal("scope or time")
	}
	// Rolled back: no longer failed.
	in.rollouts = in.rollouts[1:]
	eval(m, clock.add(time.Second), in)
	if alertOf(m, "ro", "", "r1") != nil || alertOf(m, "ro-eu", "", "r1") != nil {
		t.Fatal("not resolved")
	}
	h := history(m)
	if len(h) != 2 || h[0].Subject != "r1" {
		t.Fatalf("history: %+v", h)
	}
	// Members moving between groups move the rollouts with them.
	in.states = []State{up("a", now), up("b", now, "eu")}
	in.rollouts = append(in.rollouts, failedRollout{id: "r3", ended: clock.now(), nodes: []string{"a"}})
	eval(m, clock.add(time.Second), in)
	if alertOf(m, "ro-eu", "", "r3") != nil || alertOf(m, "ro-eu", "", "r2") == nil || alertOf(m, "ro", "", "r3") == nil {
		t.Fatal("group scope")
	}
}

// TestRuleChanges: a rule removed, turned off or changed in kind resolves
// its alerts with why, unsent; a member removed resolves its alerts, sent;
// a group scope; webhooks by name, off, and without resolved alerts.
func TestRuleChanges(t *testing.T) {
	clock := newClock()
	m, _, _ := bareManager(t, clock.now)
	sink := newHookSink(t)
	h1, h2, h3 := sink.hook("h1", FormatJSON), sink.hook("h2", FormatJSON), sink.hook("h3", FormatJSON)
	h2.SendResolved = false
	h3.Disabled = true
	rules := []AlertRule{{Name: "x", Kind: AlertMemberDown, Webhooks: []string{"h2"}}, {Name: "y", Kind: AlertMemberDown, Group: "eu"},
		{Name: "z", Kind: AlertMemberDown}}
	setRules(m, rules, h1, h2, h3)
	now := clock.now()
	in := &evalInput{states: []State{down("a", now, "eu"), down("b", now)}}
	eval(m, now, in)
	if alertOf(m, "y", "b", "") != nil || alertOf(m, "y", "a", "") == nil {
		t.Fatal("group scope")
	}
	got := sink.wait(t, 2)
	paths := map[string]int{}
	for _, r := range got {
		paths[r.Path] += len(r.payload(t).Alerts)
	}
	if paths["/hook/h1"] != 3 || paths["/hook/h2"] != 5 || paths["/hook/h3"] != 0 {
		t.Fatalf("targets: %v", paths)
	}
	// x off, y removed, z of another kind: each resolves, and is sent
	// resolved, as it was sent firing, to every webhook that takes them
	// once its rule is gone.
	rules[0].Disabled = true
	rules[2].Kind, rules[2].Severity = AlertDrift, ""
	setRules(m, []AlertRule{rules[0], rules[2]}, h1, h2, h3)
	in.states = []State{down("a", now, "eu")}
	eval(m, clock.add(time.Second), in)
	whys := map[string]string{}
	for _, a := range history(m) {
		whys[a.Rule+"|"+a.Node] = a.ResolvedWhy
	}
	if whys["x|a"] != "the rule was removed or turned off" || whys["y|a"] != "the rule was removed or turned off" || whys["z|a"] != "the rule changed" {
		t.Fatalf("whys: %v", whys)
	}
	if whys["z|b"] != "the rule changed" {
		t.Fatalf("whys: %v", whys)
	}
	got = sink.wait(t, 3)
	if p := got[2].payload(t); got[2].Path != "/hook/h1" || p.Status != AlertResolved || len(p.Alerts) != 5 {
		t.Fatalf("%s %s", got[2].Path, got[2].Body)
	}
	// A member removed: sent resolved, to the webhooks that take them.
	setRules(m, []AlertRule{{Name: "w", Kind: AlertMemberDown}}, h1, h2, h3)
	eval(m, clock.add(time.Second), &evalInput{states: []State{down("c", now)}})
	sink.wait(t, 5)
	eval(m, clock.add(time.Second), &evalInput{})
	got = sink.wait(t, 6)
	time.Sleep(50 * time.Millisecond)
	if n := len(sink.requests()); n != 6 {
		t.Fatalf("%d requests", n)
	}
	last := got[len(got)-1]
	if p := last.payload(t); last.Path != "/hook/h1" || p.Status != AlertResolved || p.Alerts[0].ResolvedWhy != "the member is no longer managed" {
		t.Fatalf("%s %s", last.Path, last.Body)
	}
	// A rule whose kind changes while its alert fires on the same member.
	setRules(m, []AlertRule{{Name: "w", Kind: AlertMemberDown}})
	eval(m, clock.add(time.Second), &evalInput{states: []State{down("d", now)}})
	setRules(m, []AlertRule{{Name: "w", Kind: AlertBackendsDown}})
	s := down("d", now)
	s.Status, s.Upstreams = StatusUp, &Upstreams{Backends: 1, BackendsDown: 1}
	eval(m, clock.add(time.Second), &evalInput{states: []State{s}})
	if a := alertOf(m, "w", "d", ""); a == nil || a.Kind != AlertBackendsDown {
		t.Fatalf("%+v", a)
	}
}

// TestAlertBounds: at most MaxActiveAlerts are followed, which is logged
// once; the history keeps MaxAlertHistory; views sort by severity.
func TestAlertBounds(t *testing.T) {
	clock := newClock()
	m, _, log := bareManager(t, clock.now)
	setRules(m, []AlertRule{{Name: "down", Kind: AlertMemberDown, Severity: SeverityInfo}, {Name: "be", Kind: AlertBackendsDown}})
	now := clock.now()
	var states []State
	for i := 0; i < MaxActiveAlerts+10; i++ {
		states = append(states, down(fmt.Sprintf("m%04d", i), now))
	}
	eval(m, now, &evalInput{states: states})
	eval(m, clock.add(time.Second), &evalInput{states: states})
	if v := m.Alerts(); len(v.Active) != MaxActiveAlerts || v.Counts.Info != MaxActiveAlerts {
		t.Fatalf("%d active, %+v", len(v.Active), v.Counts)
	}
	if n := strings.Count(log.String(), "the most kept"); n != 1 {
		t.Fatalf("logged %d times", n)
	}
	for i := range states {
		states[i] = up(states[i].Name, clock.now())
	}
	states[0].Upstreams = &Upstreams{Backends: 2, BackendsDown: 2}
	eval(m, clock.add(time.Second), &evalInput{states: states})
	v := m.Alerts()
	if len(v.History) != MaxAlertHistory || len(v.Active) != 1 || v.Counts.Warning != 1 {
		t.Fatalf("%d history, %d active", len(v.History), len(v.Active))
	}
	if !v.History[0].Resolved.Equal(clock.now()) {
		t.Fatal("history not the latest first")
	}
	// Severities sort the worst first, then the oldest.
	setRules(m, []AlertRule{{Name: "a", Kind: AlertMemberDown, Severity: SeverityInfo, ForMs: 60_000}, {Name: "b", Kind: AlertMemberDown, ForMs: 60_000}})
	eval(m, clock.add(time.Second), &evalInput{states: []State{down("x", clock.now()), down("y", clock.now().Add(-time.Second))}})
	p := m.Alerts().Pending
	if len(p) != 4 || p[0].Rule != "b" || p[0].Node != "y" || p[3].Rule != "a" {
		t.Fatalf("%+v", p)
	}
}
