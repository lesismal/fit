package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// What the workbench's assistant reaches of a manager (I153): the reads and
// plans of its cluster tools, and its read tools' member argument, through
// AssistantAPI. Every method acts as the user who asks, whose roles it
// checks as the module's endpoints do (Manager.Allow, and the Manager's own
// methods), so that the assistant never does more than the user may; the
// assistant's level is checked on its side, and the narrower of the two
// wins. Nothing answered holds a member's credential: the states hold none,
// a member's errors are redacted of its own as Manager.Call redacts them,
// and secrets in templates, rollouts and members' answers are masked or
// left to the assistant's redaction. A propose tool's plan is a rollout
// plan (PlanRollout) or a checked operation (CheckOp) that a person starts
// on the Cluster pages with their own confirm: nothing here applies
// anything. Reads across members are bounded: max_concurrency calls at
// once, each by the calls' timeout, all of them by the caller's context; a
// member not answered by then is said to be so.

// The bounds of what the assistant reads.
const (
	// assistantRollouts and assistantOps bound the rollouts and the
	// operations a read lists.
	assistantRollouts = 20
	assistantOps      = 10
	// assistantChangeBytes bounds a change's values, each side, in a plan
	// as the assistant reads it; assistantPlanMembers the members whose
	// changes it lists.
	assistantChangeBytes = 2048
	assistantPlanMembers = 64
	// assistantCerts bounds the certificates of the inventory, and
	// assistantSeries the points of each series cluster_metrics gives.
	assistantCerts  = 500
	assistantSeries = 12
)

// PlanSource marks a rollout planned by the assistant, Rollout.Source.
const PlanSource = "assistant"

// AssistantAPI is the manager as the assistant's cluster tools reach it.
type AssistantAPI struct{ m *Manager }

// Assistant returns the manager as the assistant reaches it.
func (m *Manager) Assistant() *AssistantAPI { return &AssistantAPI{m: m} }

// memberRow is a member's state as cluster_members lists it.
type memberRow struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Groups      []string          `json:"groups"`
	Status      string            `json:"status"`
	Since       time.Time         `json:"since,omitzero"`
	Error       *problemOut       `json:"error,omitempty"`
	LatencyMs   int64             `json:"latency_ms,omitempty"`
	Seen        time.Time         `json:"seen,omitzero"`
	Failures    int               `json:"failures,omitempty"`
	FitVersion  string            `json:"fit_version,omitempty"`
	FibVersion  string            `json:"fib_version,omitempty"`
	Uptime      int64             `json:"uptime_seconds,omitempty"`
	Roles       []string          `json:"roles,omitempty"`
	Config      *ConfigInfo       `json:"config,omitempty"`
	Drift       *Drift            `json:"consistency_drift,omitempty"`
	Templates   *syncRow          `json:"templates,omitempty"`
	Certs       *certsRow         `json:"certificates,omitempty"`
	Connections *Connections      `json:"connections,omitempty"`
	Reloads     *Reloads          `json:"reloads,omitempty"`
	Upstreams   *Upstreams        `json:"upstreams,omitempty"`
	NewPassword bool              `json:"must_change_password,omitempty"`
	Note        string            `json:"note,omitempty"`
	Alerts      []string          `json:"alerts_firing,omitempty"`
}

// problemOut is a member's problem, as the assistant reads it.
type problemOut struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	Status  int       `json:"status,omitempty"`
	At      time.Time `json:"at,omitzero"`
}

func problemOf(p *Problem) *problemOut {
	if p == nil {
		return nil
	}
	return &problemOut{Kind: p.Kind, Message: p.Message, Status: p.Status, At: p.At}
}

// syncRow is a member's sync with its templates, as last read.
type syncRow struct {
	State     string     `json:"state"`
	Templates []string   `json:"templates,omitempty"`
	Counts    SyncCounts `json:"counts"`
	Checked   time.Time  `json:"checked,omitzero"`
}

// certsRow is a member's certificates, as its last poll read them.
type certsRow struct {
	Count         int       `json:"count"`
	NearestExpiry time.Time `json:"nearest_expiry,omitzero"`
	DaysLeft      *int      `json:"days_left,omitempty"`
	NearestNames  []string  `json:"nearest_names,omitempty"`
}

// daysLeft returns the whole days from now to t.
func daysLeft(now, t time.Time) *int {
	d := int(t.Sub(now).Hours() / 24)
	return &d
}

// Members answers cluster_members: every member's state as the poller
// last read it, filtered by group, label selector and status, with the
// counts by status and the alerts firing on each. Any role reads it.
func (a *AssistantAPI) Members(user, group, selector, status string) ([]byte, error) {
	m := a.m
	if err := m.Allow(user, Need{Role: RoleViewer}); err != nil {
		return nil, err
	}
	var sel Selector
	if selector != "" {
		var err error
		if sel, err = ParseSelector(selector); err != nil {
			return nil, fieldErr("selector", "%v", err)
		}
	}
	if status != "" && !slices.Contains([]string{StatusUp, StatusDown, StatusError, StatusUnknown}, status) {
		return nil, fieldErr("status", "%q is not a status: up, down, error or unknown", status)
	}
	if group != "" {
		m.mu.Lock()
		i := groupIndex(&m.doc, group)
		m.mu.Unlock()
		if i < 0 {
			return nil, errNoGroup(group)
		}
	}
	syncs := map[string]MemberSync{}
	for _, s := range m.SyncStates() {
		syncs[s.Node] = s
	}
	firing := map[string][]string{}
	for _, al := range m.Alerts().Active {
		if al.Node != "" {
			firing[al.Node] = append(firing[al.Node], al.Rule+" ("+al.Severity+")")
		}
	}
	now := m.now()
	counts := map[string]int{}
	rows := []memberRow{}
	all := m.States()
	for _, st := range all {
		counts[st.Status]++
		switch {
		case group != "" && !slices.Contains(st.Groups, group),
			selector != "" && !sel.Matches(st.Labels),
			status != "" && st.Status != status:
			continue
		}
		row := memberRow{Name: st.Name, Labels: st.Labels, Groups: st.Groups, Status: st.Status, Since: st.Since, Error: problemOf(st.Error),
			LatencyMs: st.LatencyMs, Seen: st.Seen, Failures: st.Failures, Config: st.Config, Drift: st.Drift, Connections: st.Connections,
			Reloads: st.Reloads, Upstreams: st.Upstreams, NewPassword: st.MustChangePassword, Alerts: firing[st.Name]}
		if n := st.Node; n != nil {
			row.FitVersion, row.FibVersion, row.Uptime, row.Roles = n.FitVersion, n.FibVersion, n.UptimeSeconds, n.Roles
			if kind, why := m.compatible(n.SchemaDigest); kind == ReadOnlyVersion {
				row.Note = why
			}
		}
		if s, ok := syncs[st.Name]; ok {
			row.Templates = &syncRow{State: s.State, Templates: s.Templates, Counts: s.Counts, Checked: s.Checked}
		}
		if c := st.Certificates; c != nil {
			row.Certs = &certsRow{Count: c.Count, NearestExpiry: c.NearestExpiry, NearestNames: c.NearestNames}
			if !c.NearestExpiry.IsZero() {
				row.Certs.DaysLeft = daysLeft(now, c.NearestExpiry)
			}
		}
		rows = append(rows, row)
	}
	return json.Marshal(map[string]any{"members": rows, "listed": len(rows), "total": len(all), "by_status": counts,
		"alerts": m.AlertSummary(), "read_at": now.UTC()})
}

// Templates answers cluster_templates: the templates, their parts,
// variables and groups, and each member's sync as last read; with name,
// that template whole, its secrets masked, and each of its members' parts.
// Any role reads it.
func (a *AssistantAPI) Templates(user, name string) ([]byte, error) {
	m := a.m
	if err := m.Allow(user, Need{Role: RoleViewer}); err != nil {
		return nil, err
	}
	syncs := map[string]MemberSync{}
	for _, s := range m.SyncStates() {
		syncs[s.Node] = s
	}
	type tplRow struct {
		Name          string         `json:"name"`
		Description   string         `json:"description,omitempty"`
		Groups        []string       `json:"groups"`
		Order         int            `json:"order"`
		Parts         []string       `json:"parts"`
		Variables     []string       `json:"variables"`
		Warnings      []string       `json:"warnings,omitempty"`
		MissingGroups []string       `json:"missing_groups,omitempty"`
		Members       []string       `json:"members"`
		Sync          map[string]int `json:"members_by_sync"`
	}
	row := func(v *TemplateView) tplRow {
		r := tplRow{Name: v.Name, Description: v.Description, Groups: v.Groups, Order: v.Order, Parts: []string{}, Variables: v.Variables,
			Warnings: v.Warnings, MissingGroups: v.MissingGroups, Members: v.Nodes, Sync: map[string]int{}}
		for _, p := range v.Parts {
			r.Parts = append(r.Parts, p.Path)
		}
		for _, n := range v.Nodes {
			state := "not_read"
			if s, ok := syncs[n]; ok {
				state = s.State
			}
			r.Sync[state]++
		}
		return r
	}
	if name == "" {
		list := []tplRow{}
		for _, v := range m.Templates() {
			list = append(list, row(&v))
		}
		return json.Marshal(map[string]any{"templates": list,
			"note": "sync states are as the manager last read each member; a rollout reads them again when it is planned"})
	}
	v, ok := m.TemplateByName(name)
	if !ok {
		return nil, errNoTemplate(name)
	}
	type memberSync struct {
		Node    string      `json:"node"`
		State   string      `json:"state"`
		Checked time.Time   `json:"checked,omitzero"`
		Error   *problemOut `json:"error,omitempty"`
		Errors  []string    `json:"errors,omitempty"`
		Parts   []PartSync  `json:"parts,omitempty"`
		Note    string      `json:"note,omitempty"`
	}
	members := []memberSync{}
	for _, n := range v.Nodes {
		s, ok := syncs[n]
		if !ok {
			members = append(members, memberSync{Node: n, State: "not_read"})
			continue
		}
		ms := memberSync{Node: n, State: s.State, Checked: s.Checked, Error: problemOf(s.Error), Errors: s.Errors, Note: s.Note}
		for _, p := range s.Parts {
			if p.Template == name {
				ms.Parts = append(ms.Parts, p)
			}
		}
		members = append(members, ms)
	}
	return json.Marshal(map[string]any{"template": row(&v), "config": v.Config, "members": members})
}

// Rollouts answers cluster_rollouts: the latest rollouts, batch by batch,
// with each member's result, and the latest operations; with id, that
// rollout whole, with its changes' values while it keeps them, cut. Any
// role reads it.
func (a *AssistantAPI) Rollouts(user, id string) ([]byte, error) {
	m := a.m
	if err := m.Allow(user, Need{Role: RoleViewer}); err != nil {
		return nil, err
	}
	if id != "" {
		r, ok := m.RolloutByID(id)
		if !ok {
			return nil, errNoRollout(id)
		}
		return json.Marshal(planView(&r, true))
	}
	list := m.Rollouts()
	more := max(len(list)-assistantRollouts, 0)
	list = list[:len(list)-more]
	ops := m.Ops()
	ops = ops[:min(len(ops), assistantOps)]
	out := map[string]any{"rollouts": list, "operations": ops}
	if more > 0 {
		out["note"] = fmt.Sprintf("%d older rollouts are not listed", more)
	}
	return json.Marshal(out)
}

// planOut is a rollout as the assistant reads it: its members' changes cut,
// and where a person starts it.
type planOut struct {
	Rollout
	Link string `json:"link"`
	Note string `json:"note,omitempty"`
}

// planView returns r as the assistant reads it; full keeps its changes'
// values, cut to assistantChangeBytes each, for assistantPlanMembers
// members.
func planView(r *Rollout, full bool) planOut {
	out := planOut{Rollout: *r, Link: "#/cluster/rollout/" + url.PathEscape(r.ID)}
	for i := range out.Members {
		x := &out.Members[i]
		if !full || i >= assistantPlanMembers {
			x.Changes = nil
			continue
		}
		for j := range x.Changes {
			c := &x.Changes[j]
			c.Have, c.Want = cutJSON(c.Have), cutJSON(c.Want)
		}
	}
	if full && len(out.Members) > assistantPlanMembers {
		out.Note = fmt.Sprintf("the changes of the first %d members are listed; the rollout's page shows every one", assistantPlanMembers)
	}
	return out
}

// cutJSON returns v, or, past assistantChangeBytes, a string saying how
// large it is and how it starts.
func cutJSON(v json.RawMessage) json.RawMessage {
	if len(v) <= assistantChangeBytes {
		return v
	}
	s, _ := json.Marshal(fmt.Sprintf("(%d bytes, cut) %s…", len(v), clip(string(v), assistantChangeBytes-64)))
	return s
}

// errPlanWait is a plan not ready within the caller's wait.
const planWaitNote = "still planning when the tool stopped waiting: the Rollouts page shows each member as it is read"

// waitPlanned waits until the rollout id is planned, or ctx ends, and
// returns it as it is then.
func (a *AssistantAPI) waitPlanned(ctx context.Context, id string) Rollout {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		r, ok := a.m.RolloutByID(id)
		if !ok || r.State != RolloutPlanning {
			return r
		}
		select {
		case <-ctx.Done():
			return r
		case <-tick.C:
		}
	}
}

// ProposeRequest is what rollout_propose asks for: a RolloutRequest, and
// the strategy suggested for it, which the Rollouts page fills its form
// with.
type ProposeRequest struct {
	RolloutRequest
	Strategy *Strategy `json:"strategy,omitempty"`
}

// PlanRollout plans the rollout req, a ProposeRequest's JSON, as user, and
// waits for its plan until ctx ends: each member's diff and the member's
// own dry run. Nothing is applied: a person starts it on the Rollouts page.
func (a *AssistantAPI) PlanRollout(ctx context.Context, user string, req []byte) ([]byte, error) {
	var in ProposeRequest
	dec := json.NewDecoder(bytes.NewReader(req))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("the rollout is not what a plan takes: %v", err)
	}
	if in.Strategy != nil {
		s := *in.Strategy
		n := max(len(in.Nodes), 2)
		if s.Mode == StrategyRolling || s.Mode == StrategyCanary {
			n = max(n, s.BatchSize, s.Canary+1)
		}
		if err := s.check(n); err != nil {
			return nil, err
		}
		in.Strategy = &s
	}
	in.source, in.suggested = PlanSource, in.Strategy
	r, err := a.m.PlanRollout(user, &in.RolloutRequest)
	if err != nil {
		return nil, err
	}
	return a.planned(ctx, r.ID), nil
}

// planned answers the plan id once planned, or as it is when ctx ends.
func (a *AssistantAPI) planned(ctx context.Context, id string) []byte {
	r := a.waitPlanned(ctx, id)
	out := planView(&r, true)
	switch r.State {
	case RolloutPlanning:
		out.Note = strings.TrimSpace(planWaitNote + ". " + out.Note)
	case RolloutPlanned:
		out.Note = strings.TrimSpace("planned: nothing was applied. A person starts it on the Rollouts page, with a strategy and a confirm. " + out.Note)
	}
	return mustJSON(out)
}

// CheckOp checks the operation req, an OpRequest's JSON, as user would
// start it now, without starting it, and answers its members and the link
// of the page where a person starts it with its confirm.
func (a *AssistantAPI) CheckOp(user string, req []byte) ([]byte, error) {
	var in OpRequest
	dec := json.NewDecoder(bytes.NewReader(req))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("the operation is not what POST /api/ops takes: %v", err)
	}
	m := a.m
	m.mu.Lock()
	names, err := m.checkOpLocked(user, &in)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"op": in.Op, "group": in.Group, "members": names, "purge": in.Purge, "link": opLink(&in, names),
		"note": "not started: a person starts it on the operations page, which opens with it filled in, with their own confirm"})
}

// opLink is the page where a person starts the operation req on names: a
// group's, or every member's, with the operation and the members in its
// query, and a purge's params.
func opLink(req *OpRequest, names []string) string {
	base := "#/cluster/ops"
	if req.Group != "" {
		base = "#/cluster/group/" + url.PathEscape(req.Group)
	}
	q := "op=" + url.QueryEscape(req.Op) + "&members=" + url.QueryEscape(strings.Join(names, ","))
	if req.Purge != nil {
		p, _ := json.Marshal(req.Purge) // strings and a bool
		q += "&purge=" + url.QueryEscape(string(p))
	}
	return base + "?" + q
}

// FixDrift answers drift_fix_propose for the members nodes: those out of
// sync with their templates (or in conflict) in a sync rollout's plan, as
// the Drift page's Fix makes; those whose own consistency check finds
// differences in a reload, checked as an operation; and those with
// nothing to fix said so.
func (a *AssistantAPI) FixDrift(ctx context.Context, user string, nodes []string) ([]byte, error) {
	m := a.m
	if len(nodes) == 0 {
		return nil, fieldErr("nodes", "name the members whose drift to fix")
	}
	if len(nodes) > m.maxNodes {
		return nil, fieldErr("nodes", "%d members, more than %d", len(nodes), m.maxNodes)
	}
	drift := map[string]DriftNode{}
	for _, d := range m.DriftStates() {
		drift[d.Node] = d
	}
	var sync, reload, nothing []string
	for _, n := range nodes {
		d, ok := drift[n]
		switch {
		case !ok:
			return nil, errNotFound(n)
		case slices.Contains(sync, n) || slices.Contains(reload, n) || slices.Contains(nothing, n):
			continue
		}
		fixed := false
		if d.Fixable {
			sync, fixed = append(sync, n), true
		}
		if d.Consistency != nil && d.Consistency.Count > 0 {
			reload, fixed = append(reload, n), true
		}
		if !fixed {
			nothing = append(nothing, n)
		}
	}
	out := map[string]any{}
	if len(nothing) > 0 {
		out["nothing_to_fix"] = nothing
	}
	if len(sync) > 0 {
		r, err := m.FixDrift(user, sync)
		if err != nil {
			return nil, err
		}
		out["rollout"], out["link"] = json.RawMessage(a.planned(ctx, r.ID)), "#/cluster/rollout/"+url.PathEscape(r.ID)
	}
	if len(reload) > 0 {
		op, err := a.CheckOp(user, mustJSON(OpRequest{Op: OpReload, Members: reload}))
		if err != nil {
			return nil, err
		}
		out["reload"] = json.RawMessage(op)
		if out["link"] == nil {
			out["link"] = opLink(&OpRequest{Op: OpReload}, reload)
		}
	}
	return json.Marshal(out)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// statusRead is what the assistant keeps of a member's GET /status.
type statusRead struct {
	Reloads     Reloads        `json:"reloads"`
	Connections Connections    `json:"connections"`
	Requests    *requestCounts `json:"requests"`
	Upstreams   []struct {
		Name     string `json:"name"`
		InFlight int64  `json:"in_flight"`
		Backends []struct {
			Address string `json:"address"`
			Up      bool   `json:"up"`
		} `json:"backends"`
	} `json:"upstreams"`
}

// memberRead is a member's answer of a read across members, or why there
// is none.
type memberRead struct {
	name   string
	status *statusRead
	prob   *Problem
}

// targets returns the members a read names: nodes, the group's, or every
// member; checked to be ones user may read.
func (a *AssistantAPI) targets(user, group string, nodes []string) ([]string, error) {
	m := a.m
	m.mu.Lock()
	var names []string
	switch {
	case len(nodes) > 0:
		for _, n := range nodes {
			if index(&m.doc, n) < 0 {
				m.mu.Unlock()
				return nil, errNotFound(n)
			}
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	case group != "":
		i := groupIndex(&m.doc, group)
		if i < 0 {
			m.mu.Unlock()
			return nil, errNoGroup(group)
		}
		names = m.viewLocked(&m.doc.Groups[i]).Nodes
	default:
		for i := range m.doc.Members {
			names = append(names, m.doc.Members[i].Name)
		}
	}
	m.mu.Unlock()
	sort.Strings(names)
	if err := m.Allow(user, Need{Role: RoleViewer, Nodes: names}); err != nil {
		return nil, err
	}
	return names, nil
}

// readStatus GETs /status from each of names, at most max_concurrency at
// once, each call bounded by the calls' timeout and the whole by ctx: a
// member that has not answered when ctx ends is said not to have, and its
// call is left to end on its own. It blocks: the console pool's.
func (a *AssistantAPI) readStatus(ctx context.Context, names []string) []memberRead {
	m := a.m
	ch := make(chan memberRead, len(names))
	sem := make(chan struct{}, m.o.Settings.Concurrency())
	go func() {
		for _, name := range names {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			var st statusRead
			m.call(name, "GET", "/status", nil, nil, &st, func(_ *Reply, prob *Problem) {
				<-sem
				if prob != nil {
					ch <- memberRead{name: name, prob: prob}
					return
				}
				ch <- memberRead{name: name, status: &st}
			})
		}
	}()
	got := map[string]memberRead{}
	for len(got) < len(names) {
		select {
		case r := <-ch:
			got[r.name] = r
		case <-ctx.Done():
			for _, n := range names {
				if _, ok := got[n]; !ok {
					got[n] = memberRead{name: n, prob: &Problem{Kind: KindTimeout, At: m.now().UTC(),
						Message: "the member did not answer before the read across members ended; its last poll's state is in cluster_members"}}
				}
			}
		}
	}
	out := make([]memberRead, 0, len(names))
	for _, n := range names {
		out = append(out, got[n])
	}
	return out
}

// Metrics answers cluster_metrics: each member's requests by class,
// connections, reloads and upstreams' backends down, read now from its
// GET /status, and their sums by group; totals since each member started.
func (a *AssistantAPI) Metrics(ctx context.Context, user, group string, nodes []string) ([]byte, error) {
	names, err := a.targets(user, group, nodes)
	if err != nil {
		return nil, err
	}
	type row struct {
		Node         string            `json:"node"`
		Groups       []string          `json:"groups"`
		Requests     *uint64           `json:"requests,omitempty"`
		Classes      map[string]uint64 `json:"requests_by_class,omitempty"`
		Open         int64             `json:"connections_open"`
		Refused      uint64            `json:"connections_refused"`
		Reloads      Reloads           `json:"reloads"`
		Backends     int               `json:"backends"`
		BackendsDown int               `json:"backends_down"`
		Error        *problemOut       `json:"error,omitempty"`
	}
	type sum struct {
		Members      int               `json:"members"`
		Read         int               `json:"read"`
		Requests     uint64            `json:"requests"`
		Classes      map[string]uint64 `json:"requests_by_class"`
		Open         int64             `json:"connections_open"`
		BackendsDown int               `json:"backends_down"`
		ReloadsFail  uint64            `json:"reloads_failed"`
	}
	groupsOf := map[string][]string{}
	for _, st := range a.m.States() {
		groupsOf[st.Name] = st.Groups
	}
	rows := []row{}
	sums := map[string]*sum{}
	add := func(g string, r *row) {
		s := sums[g]
		if s == nil {
			s = &sum{Classes: map[string]uint64{}}
			sums[g] = s
		}
		s.Members++
		if r.Error != nil {
			return
		}
		s.Read++
		if r.Requests != nil {
			s.Requests += *r.Requests
		}
		for k, v := range r.Classes {
			s.Classes[k] += v
		}
		s.Open += r.Open
		s.BackendsDown += r.BackendsDown
		s.ReloadsFail += r.Reloads.Failed
	}
	for _, mr := range a.readStatus(ctx, names) {
		r := row{Node: mr.name, Groups: groupsOf[mr.name], Error: problemOf(mr.prob)}
		if st := mr.status; st != nil {
			r.Open, r.Refused, r.Reloads = st.Connections.Open, st.Connections.Refused, st.Reloads
			if st.Requests != nil {
				r.Requests, r.Classes = &st.Requests.Total, st.Requests.Classes
			}
			for _, u := range st.Upstreams {
				for _, b := range u.Backends {
					r.Backends++
					if !b.Up {
						r.BackendsDown++
					}
				}
			}
		}
		rows = append(rows, r)
		add("(all)", &r)
		for _, g := range r.Groups {
			add(g, &r)
		}
	}
	q := MetricsQuery{Nodes: names, Points: assistantSeries}
	if group != "" {
		q.Groups = []string{group}
	}
	series, _ := a.m.Metrics(q) // names and group checked above
	return json.Marshal(map[string]any{"members": rows, "by_group": sums, "read_at": a.m.now().UTC(), "series": series,
		"note": "members and by_group are counters since each member started, read now; series are the manager's per poll (I150), rates a second, oldest first; requests are counted only where the member's config sets metrics"})
}

// Certs answers cluster_certs: the certificate inventory (I150), every
// certificate the members serve as their latest polls read them, the one
// that expires first first, of the members nodes names, the group's, or
// every member; with within, only those that expire within that many days;
// at most assistantCerts. It reads no member: the poller did.
func (a *AssistantAPI) Certs(ctx context.Context, user, group string, nodes []string, within int) ([]byte, error) {
	if within < 0 {
		return nil, fieldErr("within_days", "the days are 0 (every certificate) or more")
	}
	names, err := a.targets(user, group, nodes)
	if err != nil {
		return nil, err
	}
	inv := a.m.Certificates()
	list := []CertEntry{}
	for _, c := range inv.Certificates {
		if slices.Contains(names, c.Node) && (within == 0 || c.DaysLeft <= within) {
			list = append(list, c)
		}
	}
	unread := []CertNode{}
	for _, n := range inv.Nodes {
		if slices.Contains(names, n.Name) && n.Read.IsZero() {
			unread = append(unread, n)
		}
	}
	out := map[string]any{"certificates": list, "read_at": inv.Now, "note": "as each member's latest poll read them"}
	if len(list) > assistantCerts {
		out["certificates"], out["note"] = list[:assistantCerts], fmt.Sprintf("%d certificates; the %d that expire first are listed", len(list), assistantCerts)
	}
	if len(unread) > 0 {
		out["not_read"] = unread
	}
	return json.Marshal(out)
}

// assistantTail bounds the messages a member gives a tail the assistant
// reads, when not told.
const assistantTail = 50

// Logs answers cluster_log_tail: the latest messages of the members' own
// logs (I150's merged tail of their rings), of nodes or the group's, at
// most MaxTailNodes, merged by time, filtered by logger, least level and
// text, limit a member; waited for until ctx ends. Each member's outcome
// is said, one slow or down as its problem.
func (a *AssistantAPI) Logs(ctx context.Context, user, group string, nodes []string, logger, level, contains string, limit int) ([]byte, error) {
	if len(nodes) == 0 && group == "" {
		return nil, fieldErr("nodes", "name the members whose logs to read, or a group, of %d members at most", MaxTailNodes)
	}
	names, err := a.targets(user, group, nodes)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = assistantTail
	}
	type res struct {
		b   TailBody
		err error
	}
	ch := make(chan res, 1)
	a.m.TailLogs(TailQuery{Nodes: names, Logger: logger, Level: level, Contains: contains, Limit: limit}, func(b TailBody, err error) { ch <- res{b, err} })
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		r.b.Cursor = ""
		return json.Marshal(r.b)
	case <-ctx.Done():
		return nil, fmt.Errorf("the members' logs did not come in time: %w", ctx.Err())
	}
}

// memberPaths are the Admin API's paths a read tool's member argument
// reaches: what the read tools read, never tokens, logs' files or
// anything that writes.
var memberPaths = regexp.MustCompile(`^/(config|config/effective|config/check|versions|versions/[0-9]+|status|metrics|cache|node|schema|media/(library|auth|stats)(/.*)?|streaming/(channels|push)(/.*)?)$`)

// memberPosts are the POSTs that only convert, which config_get makes.
var memberPosts = []string{"/config/render", "/config/parse"}

// MemberCall calls the Admin API of the member as a read tool's member
// argument does, as user, who needs a role on the member: a GET of what
// the read tools read, or a render or parse of config text. It answers the
// member's status, header and body whatever its status, and a note when
// the member runs another version of fit; or why there is no answer. It
// waits for the answer until ctx ends: the console pool's.
func (a *AssistantAPI) MemberCall(ctx context.Context, user, member, method, path string, body []byte) (int, stdhttp.Header, []byte, string, error) {
	m := a.m
	p, _, _ := strings.Cut(path, "?")
	switch {
	case method == "GET" && memberPaths.MatchString(p):
	case method == "POST" && slices.Contains(memberPosts, p):
	default:
		return 0, nil, nil, "", fmt.Errorf("%s %s is not read on a member: a member is read, never changed, by the assistant's tools", method, p)
	}
	if err := m.Allow(user, Need{Role: RoleViewer, Nodes: []string{member}}); err != nil {
		return 0, nil, nil, "", err
	}
	note := ""
	m.mu.Lock()
	if n := m.nodes[member]; n != nil {
		n.mu.Lock()
		if n.state.Node != nil {
			if kind, why := m.compatible(n.state.Node.SchemaDigest); kind == ReadOnlyVersion {
				note = why
			}
		}
		n.mu.Unlock()
	}
	m.mu.Unlock()
	type res struct {
		r *Reply
		p *Problem
	}
	ch := make(chan res, 1)
	if err := m.Call(member, method, path, nil, body, func(r *Reply, p *Problem) { ch <- res{r, p} }); err != nil {
		return 0, nil, nil, "", err
	}
	select {
	case x := <-ch:
		if x.p != nil {
			return 0, nil, nil, note, fmt.Errorf("the member %q: %s: %s", member, x.p.Kind, x.p.Message)
		}
		return x.r.Status, x.r.Header, x.r.Body, note, nil
	case <-ctx.Done():
		return 0, nil, nil, note, fmt.Errorf("the member %q did not answer in time: %w", member, ctx.Err())
	}
}

// IsForbidden reports whether err is a refusal for the user's roles.
func IsForbidden(err error) bool {
	var fb *ErrForbidden
	return errors.As(err, &fb)
}
