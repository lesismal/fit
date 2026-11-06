package cluster

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
)

// How the alerts are evaluated: a goroutine of its own, every alertTick and
// whenever a drift read ends or the rules change, reads the members' states
// as the poller left them, their sync states, and the rollouts that failed,
// under the manager's locks and never waiting on a member. A rule's
// condition that holds starts an alert pending, from when it began (a
// member's state's since, a failure's time) or from when it was first seen;
// once it has lasted the rule's time the alert fires, and is sent. A
// condition that cannot be judged (a member not read yet since the manager
// started) keeps its alert as it is. A firing alert not silenced is sent
// again every repeat interval, and once its condition is gone it resolves,
// and is sent resolved when it was sent firing. Then the members whose
// configs changed since their sync states were taken are read against
// their templates, without telling them anything.

// alertTick is how often the evaluator runs; tests lower it.
var alertTick = time.Second

// driftMaxAge is how old a managed member's sync state may grow before it
// is read again, though its config's ETag did not change.
var driftMaxAge = 10 * time.Minute

// pokeAlerts has the evaluator run now.
func (m *Manager) pokeAlerts() {
	select {
	case m.al.wake <- struct{}{}:
	default:
	}
}

// runAlerts evaluates the alerts until the manager stops. A goroutine of
// its own.
func (m *Manager) runAlerts() {
	defer close(m.al.done)
	t := time.NewTicker(alertTick)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			m.saveAlerts()
			return
		case <-t.C:
		case <-m.al.wake:
		}
		m.evaluateAlerts()
		m.checkDrift()
		m.saveAlerts()
	}
}

// stopAlerts waits for the evaluator to end, at most wait, and closes the
// webhooks' connections, the notifications waiting dropped.
func (m *Manager) stopAlerts(wait time.Duration) {
	select {
	case <-m.al.done:
	case <-time.After(wait):
	}
	m.al.mu.Lock()
	senders := slices.Collect(maps.Values(m.al.senders))
	m.al.senders = map[string]*sender{}
	m.al.mu.Unlock()
	for _, s := range senders {
		s.close()
	}
}

// failedRollout is a rollout that failed, as the rule rollout_failed sees
// it.
type failedRollout struct {
	id, what, reason string
	ended            time.Time
	nodes            []string
}

// evalInput is what the rules are evaluated on.
type evalInput struct {
	states   []State
	syncs    map[string]MemberSync
	rollouts []failedRollout
}

// alertInput reads what the rules are evaluated on.
func (m *Manager) alertInput() *evalInput {
	in := &evalInput{states: m.States(), syncs: map[string]MemberSync{}}
	for _, s := range m.SyncStates() {
		in.syncs[s.Node] = s
	}
	m.mu.Lock()
	for _, r := range m.ro.list {
		if r.State != RolloutFailed {
			continue
		}
		f := failedRollout{id: r.ID, what: describeRollout(r, len(r.Members)), reason: r.Reason, ended: r.Ended}
		for _, x := range r.Members {
			f.nodes = append(f.nodes, x.Node)
		}
		in.rollouts = append(in.rollouts, f)
	}
	m.mu.Unlock()
	return in
}

// evaluateAlerts evaluates the rules now and sends what is due.
func (m *Manager) evaluateAlerts() {
	m.al.evalMu.Lock()
	defer m.al.evalMu.Unlock()
	in := m.alertInput()
	m.deliver(m.evaluate(m.now().UTC(), in))
}

// finding is a rule's condition that holds: on a member, or on a rollout.
type finding struct {
	node, subject, summary string
	// since is when the condition began, when known.
	since time.Time
}

// reloadTrack follows a member's reloads: the counts its last poll read,
// and whether the latest change was a failure.
type reloadTrack struct {
	polled            time.Time
	succeeded, failed uint64
	failing           bool
	failedAt          time.Time
}

// trackReloads follows the members' reloads from their polls. al.mu is
// held.
func (al *alertsState) trackReloads(states []State) {
	names := map[string]bool{}
	for i := range states {
		st := &states[i]
		names[st.Name] = true
		if st.Reloads == nil || st.Status != StatusUp {
			continue
		}
		t := al.reloads[st.Name]
		if t == nil {
			// The first count seen is where following starts: a
			// failure before it is not this manager's to tell.
			al.reloads[st.Name] = &reloadTrack{polled: st.Polled, succeeded: st.Reloads.Succeeded, failed: st.Reloads.Failed}
			continue
		}
		if !st.Polled.After(t.polled) {
			continue
		}
		t.polled = st.Polled
		switch r := st.Reloads; {
		case r.Failed < t.failed || r.Succeeded < t.succeeded:
			// The member restarted: it counts from nothing again.
			t.failing = false
		case r.Failed > t.failed:
			t.failing, t.failedAt = true, st.Polled
		case r.Succeeded > t.succeeded:
			t.failing = false
		}
		t.succeeded, t.failed = st.Reloads.Succeeded, st.Reloads.Failed
	}
	for n := range al.reloads {
		if !names[n] {
			delete(al.reloads, n)
		}
	}
}

// inScope reports whether r watches the member of groups.
func inScope(r *AlertRule, groups []string) bool {
	return r.Group == "" || slices.Contains(groups, r.Group)
}

// findings returns where r's condition holds now, and the members it
// cannot be judged on, whose alerts are kept as they are. al.mu is held.
func (al *alertsState) findings(r *AlertRule, in *evalInput, now time.Time) ([]finding, map[string]bool) {
	var out []finding
	unknown := map[string]bool{}
	groupsOf := map[string][]string{}
	for i := range in.states {
		st := &in.states[i]
		groupsOf[st.Name] = st.Groups
		if r.Kind == AlertRolloutFailed || !inScope(r, st.Groups) {
			continue
		}
		if st.Status == StatusUnknown {
			unknown[st.Name] = true
			continue
		}
		if f, ok := al.memberFinding(r, st, in, now); ok {
			f.node = st.Name
			out = append(out, f)
		}
	}
	if r.Kind == AlertRolloutFailed {
		for _, ro := range in.rollouts {
			if r.Group != "" && !slices.ContainsFunc(ro.nodes, func(n string) bool { return slices.Contains(groupsOf[n], r.Group) }) {
				continue
			}
			if ra := r.resolveAfterDur(); ra > 0 && now.Sub(ro.ended) >= ra {
				continue
			}
			out = append(out, finding{subject: ro.id, since: ro.ended,
				summary: clip("the rollout "+ro.id+" ("+ro.what+") failed: "+ro.reason, maxAlertText)})
		}
	}
	return out, unknown
}

// memberFinding returns r's condition on the member st, when it holds.
// al.mu is held.
func (al *alertsState) memberFinding(r *AlertRule, st *State, in *evalInput, now time.Time) (finding, bool) {
	switch r.Kind {
	case AlertMemberDown:
		if st.Status != StatusDown && st.Status != StatusError {
			return finding{}, false
		}
		s := st.Status + " since " + st.Since.UTC().Format(time.RFC3339)
		if st.Error != nil {
			s += ": " + st.Error.Kind + ": " + st.Error.Message
		}
		return finding{since: st.Since, summary: clip(s, maxAlertText)}, true
	case AlertDrift:
		var parts []string
		if s, ok := in.syncs[st.Name]; ok && r.Source != DriftConsistency {
			switch {
			case s.State == SyncOutOfSync || s.State == SyncConflict:
				parts = append(parts, fmt.Sprintf("out of sync with its templates: %d parts out of sync, %d in conflict", s.Counts.OutOfSync, s.Counts.Conflict))
			case s.State == SyncError && len(s.Errors) > 0:
				parts = append(parts, "its templates do not render for it: "+s.Errors[0])
			}
		}
		if d := st.Drift; d != nil && d.Count > 0 && r.Source != DriftTemplates {
			parts = append(parts, fmt.Sprintf("its consistency check finds %d differences", d.Count))
		}
		if len(parts) == 0 {
			return finding{}, false
		}
		return finding{summary: clip(strings.Join(parts, "; "), maxAlertText)}, true
	case AlertCertExpiry:
		c := st.Certificates
		if c == nil || c.NearestExpiry.IsZero() {
			return finding{}, false
		}
		left := c.NearestExpiry.Sub(now)
		if left >= time.Duration(r.Days)*24*time.Hour {
			return finding{}, false
		}
		what := "the certificate of " + strings.Join(c.NearestNames, ", ")
		if c.NearestListener != "" {
			what += " (listener " + c.NearestListener + ")"
		}
		when := c.NearestExpiry.UTC().Format(time.RFC3339)
		if left <= 0 {
			return finding{summary: clip(what+" expired on "+when, maxAlertText)}, true
		}
		return finding{summary: clip(fmt.Sprintf("%s expires in %d days, on %s", what, int(left.Hours()/24), when), maxAlertText)}, true
	case AlertBackendsDown:
		u := st.Upstreams
		if u == nil || u.BackendsDown < r.Threshold {
			return finding{}, false
		}
		return finding{summary: fmt.Sprintf("%d of its %d upstream backends are down", u.BackendsDown, u.Backends)}, true
	default: // AlertReloadFailed
		t := al.reloads[st.Name]
		if t == nil || !t.failing || r.ResolveAfterMs > 0 && now.Sub(t.failedAt) >= r.resolveAfterDur() {
			return finding{}, false
		}
		return finding{since: t.failedAt, summary: fmt.Sprintf("a reload failed, seen at %s: %d failed and %d succeeded since it started",
			t.failedAt.UTC().Format(time.RFC3339), t.failed, t.succeeded)}, true
	}
}

// batch is what one webhook is sent of an evaluation.
type batch struct {
	firing, resolved []Alert
}

// evaluate evaluates every rule at now on in, fires, resolves and repeats
// the alerts, and returns what each webhook is to be sent, by name.
func (m *Manager) evaluate(now time.Time, in *evalInput) map[string]*batch {
	al := &m.al
	al.mu.Lock()
	defer al.mu.Unlock()
	al.trackReloads(in.states)
	members := map[string]bool{}
	for i := range in.states {
		members[in.states[i].Name] = true
	}
	rules := map[string]*AlertRule{}
	held := map[string]bool{}
	out := map[string]*batch{}
	type found struct {
		r *AlertRule
		f []finding
	}
	var founds []found
	for i := range al.cfg.rules {
		r := &al.cfg.rules[i]
		if r.Disabled {
			continue
		}
		rules[r.Name] = r
		fs, unknown := al.findings(r, in, now)
		for _, f := range fs {
			held[alertKey(r.Name, f.node, f.subject)] = true
		}
		for key, a := range al.all() {
			if a.Rule == r.Name && a.Kind == r.Kind && a.Node != "" && unknown[a.Node] {
				held[key] = true
			}
		}
		founds = append(founds, found{r, fs})
	}
	// What no longer holds goes first, so that its room is the new
	// alerts'.
	for key, a := range al.all() {
		if held[key] {
			continue
		}
		r := rules[a.Rule]
		if a.State == AlertPending {
			delete(al.pending, key)
			continue
		}
		why := ""
		switch {
		case r == nil:
			why = "the rule was removed or turned off"
		case r.Kind != a.Kind:
			why = "the rule changed"
		case a.Node != "" && !members[a.Node]:
			why = "the member is no longer managed"
		}
		al.resolve(a, now, why)
		if a.Sends > 0 {
			// Sent firing, it is sent resolved, so that the receivers
			// close it: by its rule's webhooks, or every one once the
			// rule is gone.
			for _, w := range al.targets(r, true) {
				out[w] = addTo(out[w])
				out[w].resolved = append(out[w].resolved, *a)
			}
		}
	}
	for _, x := range founds {
		for _, f := range x.f {
			al.hold(x.r, alertKey(x.r.Name, f.node, f.subject), &f, now)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(al.active)) {
		a := al.active[key]
		r := rules[a.Rule]
		if al.silence(a, now) != nil {
			continue
		}
		if a.Sends > 0 && (r.RepeatMs == 0 || now.Sub(a.LastSent) < r.repeatDur()) {
			continue
		}
		targets := al.targets(r, false)
		if len(targets) == 0 {
			continue
		}
		a.Sends++
		a.LastSent = now
		al.dirty = true
		for _, w := range targets {
			out[w] = addTo(out[w])
			out[w].firing = append(out[w].firing, *a)
		}
	}
	return out
}

func addTo(b *batch) *batch {
	if b == nil {
		return &batch{}
	}
	return b
}

// all returns the alerts firing and pending, by key. al.mu is held.
func (al *alertsState) all() map[string]*Alert {
	out := make(map[string]*Alert, len(al.active)+len(al.pending))
	maps.Copy(out, al.active)
	maps.Copy(out, al.pending)
	return out
}

// hold has the alert of r at key follow f, a condition that holds: one
// firing takes its summary; one pending, or a new one, fires once its
// condition has lasted r's time. al.mu is held.
func (al *alertsState) hold(r *AlertRule, key string, f *finding, now time.Time) {
	if a := al.active[key]; a != nil && a.Kind == r.Kind {
		a.Summary, a.Severity = f.summary, r.Severity
		return
	}
	if a := al.active[key]; a != nil {
		// Its rule changed kind: the old alert resolves, the new one
		// starts.
		al.resolve(a, now, "the rule changed")
	}
	a := al.pending[key]
	if a == nil || a.Kind != r.Kind {
		if len(al.active)+len(al.pending) >= MaxActiveAlerts {
			if now.Sub(al.full) > time.Hour {
				al.full = now
				al.logf("cluster: %d alerts are firing or pending, the most kept: new ones are not followed until some resolve", MaxActiveAlerts)
			}
			return
		}
		since := f.since
		if since.IsZero() || since.After(now) {
			since = now
		}
		a = &Alert{Rule: r.Name, Kind: r.Kind, Node: f.node, Subject: f.subject, State: AlertPending, Since: since.UTC()}
		al.pending[key] = a
	}
	a.Summary, a.Severity = f.summary, r.Severity
	if now.Sub(a.Since) < r.forDur() {
		return
	}
	delete(al.pending, key)
	a.ID, a.State, a.Fired = newOpID(), AlertFiring, now
	al.active[key] = a
	al.dirty = true
	al.logf("cluster: the alert %s (%s) fired%s: %s", a.ID, a.Rule, onWhat(a), a.Summary)
}

// resolve resolves the firing alert a, for why when its condition is not
// what ended it, and keeps it in the history. al.mu is held.
func (al *alertsState) resolve(a *Alert, now time.Time, why string) {
	delete(al.active, a.key())
	a.State, a.Resolved, a.ResolvedWhy = AlertResolved, now, why
	al.history = append(al.history, *a)
	if n := len(al.history) - MaxAlertHistory; n > 0 {
		al.history = slices.Delete(al.history, 0, n)
	}
	al.dirty = true
	if why == "" {
		why = "its condition is gone"
	}
	al.logf("cluster: the alert %s (%s) resolved%s: %s", a.ID, a.Rule, onWhat(a), why)
}

// onWhat names what a is on: its member, or its rollout.
func onWhat(a *Alert) string {
	if a.Node != "" {
		return " on " + a.Node
	}
	return " on " + a.Subject
}

// logf is the manager's log, set when the manager starts.
func (al *alertsState) logf(format string, args ...any) {
	if al.log != nil {
		al.log(format, args...)
	}
}

// silence returns the silence that holds a at now, or nil. al.mu is held.
func (al *alertsState) silence(a *Alert, now time.Time) *Silence {
	for i := range al.cfg.silences {
		if s := &al.cfg.silences[i]; s.active(now) && s.matches(a) {
			return s
		}
	}
	return nil
}

// targets returns the webhooks r sends to, enabled, by name, every one for
// a rule gone (nil); for a resolved alert, those that send resolved
// alerts. al.mu is held.
func (al *alertsState) targets(r *AlertRule, resolved bool) []string {
	var out []string
	for _, w := range al.cfg.webhooks {
		if w.Disabled || resolved && !w.SendResolved || r != nil && len(r.Webhooks) > 0 && !slices.Contains(r.Webhooks, w.Name) {
			continue
		}
		out = append(out, w.Name)
	}
	return out
}

// deliver hands each webhook what it is to be sent.
func (m *Manager) deliver(out map[string]*batch) {
	for _, name := range slices.Sorted(maps.Keys(out)) {
		m.al.mu.Lock()
		s := m.al.senders[name]
		m.al.mu.Unlock()
		if s == nil {
			continue
		}
		b := out[name]
		if len(b.firing) > 0 {
			s.enqueue(&notice{status: AlertFiring, alerts: b.firing})
		}
		if len(b.resolved) > 0 {
			s.enqueue(&notice{status: AlertResolved, alerts: b.resolved})
		}
	}
}

// templateGroups returns the groups a template is attached to.
func (m *Manager) templateGroups() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for _, t := range m.doc.Templates {
		for _, g := range t.Groups {
			out[g] = true
		}
	}
	return out
}

// checkDrift reads against their templates, without telling them
// anything, the members a template is attached to that are up and were
// polled since they were last read, when their sync state is missing, was
// taken of another config (its ETag), or is older than driftMaxAge; at
// most max_concurrency at once, one at a time a member. It never waits:
// a member it has no room for is read after a later poll.
func (m *Manager) checkDrift() {
	m.al.evalMu.Lock()
	defer m.al.evalMu.Unlock()
	m.startDriftReads()
}

// startDriftReads starts the drift reads checkDrift says; m.al.evalMu is
// held, so that one pass runs at a time.
func (m *Manager) startDriftReads() {
	groups := m.templateGroups()
	states := m.States()
	syncs := map[string]MemberSync{}
	for _, s := range m.SyncStates() {
		syncs[s.Node] = s
	}
	now := m.now()
	al := &m.al
	names := map[string]bool{}
	full := false
	for i := range states {
		st := &states[i]
		names[st.Name] = true
		if full || st.Status != StatusUp || !slices.ContainsFunc(st.Groups, func(g string) bool { return groups[g] }) {
			continue
		}
		al.mu.Lock()
		skip := al.driftBusy[st.Name] || !st.Polled.After(al.driftPoll[st.Name])
		al.mu.Unlock()
		if skip {
			continue
		}
		s, ok := syncs[st.Name]
		changed := st.Config != nil && st.Config.ETag != "" && st.Config.ETag != s.ConfigETag
		if ok && !changed && now.Sub(s.Checked) < driftMaxAge {
			continue
		}
		select {
		case al.driftSem <- struct{}{}:
		default:
			full = true
			continue
		}
		name := st.Name
		al.mu.Lock()
		al.driftBusy[name], al.driftPoll[name] = true, st.Polled
		al.mu.Unlock()
		m.syncMember(name, false, func(*syncOut) {
			al.mu.Lock()
			delete(al.driftBusy, name)
			al.mu.Unlock()
			<-al.driftSem
			m.pokeAlerts()
		})
	}
	al.mu.Lock()
	for n := range al.driftPoll {
		if !names[n] {
			delete(al.driftPoll, n)
		}
	}
	al.mu.Unlock()
}

// AlertCounts counts the alerts firing: those sent (not silenced) by
// severity, and those silenced.
type AlertCounts struct {
	Firing   int `json:"firing"`
	Silenced int `json:"silenced"`
	Pending  int `json:"pending"`
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// AlertsView is GET /api/alerts's answer: the alerts firing, those
// pending, and those resolved, the latest first.
type AlertsView struct {
	ReadOnly string      `json:"read_only,omitempty"`
	Counts   AlertCounts `json:"counts"`
	Active   []Alert     `json:"active"`
	Pending  []Alert     `json:"pending"`
	History  []Alert     `json:"history"`
}

// Alerts returns the alerts as GET /api/alerts answers them.
func (m *Manager) Alerts() AlertsView {
	now := m.now().UTC()
	al := &m.al
	al.mu.Lock()
	defer al.mu.Unlock()
	v := AlertsView{ReadOnly: al.readOnly, Counts: al.countsLocked(now), Active: []Alert{}, Pending: []Alert{}, History: []Alert{}}
	for _, a := range al.active {
		c := *a
		if s := al.silence(a, now); s != nil {
			c.SilencedBy = s.ID
		}
		v.Active = append(v.Active, c)
	}
	for _, a := range al.pending {
		v.Pending = append(v.Pending, *a)
	}
	for i := len(al.history) - 1; i >= 0; i-- {
		v.History = append(v.History, al.history[i])
	}
	order := func(list []Alert) {
		sort.SliceStable(list, func(i, j int) bool {
			si, sj := slices.Index(Severities, list[i].Severity), slices.Index(Severities, list[j].Severity)
			if si != sj {
				return si > sj
			}
			return list[i].Since.Before(list[j].Since)
		})
	}
	order(v.Active)
	order(v.Pending)
	return v
}

// AlertSummary returns the counts of the alerts, for the Cluster menu's
// badge.
func (m *Manager) AlertSummary() AlertCounts {
	now := m.now().UTC()
	m.al.mu.Lock()
	defer m.al.mu.Unlock()
	return m.al.countsLocked(now)
}

func (al *alertsState) countsLocked(now time.Time) AlertCounts {
	c := AlertCounts{Pending: len(al.pending)}
	for _, a := range al.active {
		if al.silence(a, now) != nil {
			c.Silenced++
			continue
		}
		c.Firing++
		switch a.Severity {
		case SeverityCritical:
			c.Critical++
		case SeverityWarning:
			c.Warning++
		default:
			c.Info++
		}
	}
	return c
}
