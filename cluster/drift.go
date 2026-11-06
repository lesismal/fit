package cluster

import (
	"slices"
	"strings"
)

// DriftNode is a member's drift: its sync state against its templates, as
// the latest read took it (without the parts' values: GET /api/desired/
// <name> reads them), and its consistency check, as its latest poll read
// it. Drifted is either; Fixable is a drift a sync rollout fixes, of a
// member out of sync with its templates or in conflict with them.
type DriftNode struct {
	Node        string      `json:"node"`
	Status      string      `json:"status"`
	Groups      []string    `json:"groups"`
	Managed     bool        `json:"managed"`
	Templates   *MemberSync `json:"templates,omitempty"`
	Consistency *Drift      `json:"consistency,omitempty"`
	Drifted     bool        `json:"drifted"`
	Fixable     bool        `json:"fixable"`
}

// DriftStates returns every member's drift, by name.
func (m *Manager) DriftStates() []DriftNode {
	groups := m.templateGroups()
	syncs := map[string]MemberSync{}
	for _, s := range m.SyncStates() {
		syncs[s.Node] = s
	}
	out := []DriftNode{}
	for _, st := range m.States() {
		d := DriftNode{Node: st.Name, Status: st.Status, Groups: st.Groups, Consistency: st.Drift,
			Managed: slices.ContainsFunc(st.Groups, func(g string) bool { return groups[g] })}
		if s, ok := syncs[st.Name]; ok {
			d.Templates = &s
			d.Fixable = s.State == SyncOutOfSync || s.State == SyncConflict
		}
		d.Drifted = d.Fixable || d.Templates != nil && d.Templates.State == SyncError && len(d.Templates.Errors) > 0 ||
			st.Drift != nil && st.Drift.Count > 0
		out = append(out, d)
	}
	return out
}

// FixDrift plans a sync rollout of the members nodes, as user: each
// brought in sync with its templates once a person starts it, as a sync
// rollout from the Rollouts page is. Nothing is applied until then. It
// writes the store: off a fib worker.
func (m *Manager) FixDrift(user string, nodes []string) (Rollout, error) {
	if len(nodes) == 0 {
		return Rollout{}, fieldErr("nodes", "a fix names the members it brings in sync")
	}
	if len(nodes) > m.maxNodes {
		return Rollout{}, fieldErr("nodes", "%d members, more than %d", len(nodes), m.maxNodes)
	}
	desc := "a fix of the drift of " + strings.Join(nodes, ", ")
	return m.PlanRollout(user, &RolloutRequest{Kind: RolloutSync, Nodes: slices.Clone(nodes), Description: clip(desc, maxRolloutDescription-4)})
}
