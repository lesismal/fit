package cluster

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/lesismal/fit/internal/managed"
)

// The sync states of a member, and of each part its templates own.
const (
	// SyncInSync is a part the member holds as its template renders it,
	// and a member whose every part is so.
	SyncInSync = "in_sync"
	// SyncOutOfSync is a part the member does not hold as rendered, which
	// nobody changed on the member: the template changed, or the part is
	// not there yet. A rollout (I146) brings it in sync.
	SyncOutOfSync = "out_of_sync"
	// SyncConflict is a part someone changed on the member: since the
	// manager last saw it in sync, or before the template owned it.
	SyncConflict = "conflict"
	// SyncError is a member that could not be read, or whose templates do
	// not render for it.
	SyncError = "error"
	// SyncUnmanaged is a member no template is attached to.
	SyncUnmanaged = "unmanaged"
	// SyncPending is a member being read.
	SyncPending = "pending"
)

// Why a part is out of sync or in conflict.
const (
	// ReasonMissing: the member has no such part yet.
	ReasonMissing = "missing"
	// ReasonTemplateChanged: the member holds what the manager last saw in
	// sync, and the template now renders something else.
	ReasonTemplateChanged = "template_changed"
	// ReasonChangedHere: the member's part is not what the manager last saw
	// in sync, nor what the template renders.
	ReasonChangedHere = "changed_here"
	// ReasonRemovedHere: the member no longer has a part the manager saw
	// in sync.
	ReasonRemovedHere = "removed_here"
	// ReasonOwnValue: the member has the part, other than rendered, and the
	// manager never saw it in sync: the member's own value, from before the
	// template owned it.
	ReasonOwnValue = "own_value"
)

// renderInput is what renders a member's parts: who it is, the variables of
// the groups it is in, and every template.
type renderInput struct {
	name, id, hostname string
	labels             map[string]string
	// groups are the groups the member is in, by name, with their
	// variables.
	groups    map[string]map[string]string
	templates []Template
}

// renderInputLocked returns the render input of the member name, as the
// document has it now, and its node's ID and host as the last poll read
// them; false when there is no such member. m.mu is held.
func (m *Manager) renderInputLocked(name string) (*renderInput, bool) {
	i := index(&m.doc, name)
	if i < 0 {
		return nil, false
	}
	mem := &m.doc.Members[i]
	in := &renderInput{name: mem.Name, labels: maps.Clone(mem.Labels), groups: map[string]map[string]string{}}
	for j := range m.groups {
		if c := &m.groups[j]; c.has(mem) {
			in.groups[c.g.Name] = maps.Clone(c.g.Variables)
		}
	}
	for _, t := range m.doc.Templates {
		in.templates = append(in.templates, t.clone())
	}
	if n := m.nodes[name]; n != nil {
		n.mu.Lock()
		if n.state.Node != nil {
			in.id, in.hostname = n.state.Node.ID, n.state.Node.Hostname
		}
		n.mu.Unlock()
	}
	return in, true
}

// attached returns the templates attached to a group of the member's, in
// the order they apply: by Order, then by name.
func (in *renderInput) attached() []*Template {
	var out []*Template
	for i := range in.templates {
		t := &in.templates[i]
		if slices.ContainsFunc(t.Groups, func(g string) bool { _, ok := in.groups[g]; return ok }) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// rendered is a part a template renders for a member.
type rendered struct {
	member, name, template string
	// overrides are the templates, applied before, whose value for the
	// part this one replaces.
	overrides []string
	value     *jv
}

func (r *rendered) display() string { return managed.Display(r.member, r.name) }

// render renders the parts the member's templates own, a later template's
// replacing an earlier one's, and the names of the templates attached; a
// template that does not render for the member (a variable it lacks) is
// left out, and said in errs.
func (in *renderInput) render() (parts []*rendered, templates, errs []string) {
	templates = []string{}
	byKey := map[string]*rendered{}
	for _, t := range in.attached() {
		templates = append(templates, t.Name)
		vars := map[string]string{}
		for _, g := range t.Groups {
			maps.Copy(vars, in.groups[g])
		}
		lookup := func(name string) (string, error) {
			switch {
			case name == "node.name":
				return in.name, nil
			case name == "node.id" || name == "node.hostname":
				v := in.id
				if name == "node.hostname" {
					v = in.hostname
				}
				if v == "" {
					return "", fmt.Errorf("${%s}: the member was not read yet", name)
				}
				return v, nil
			case len(name) > len("node.labels.") && name[:len("node.labels.")] == "node.labels.":
				k := name[len("node.labels."):]
				v, ok := in.labels[k]
				if !ok {
					return "", fmt.Errorf("${%s}: the member has no label %q", name, k)
				}
				return v, nil
			}
			v, ok := vars[name]
			if !ok {
				return "", fmt.Errorf("${%s}: no group of the member's that the template is attached to has the variable %q", name, name)
			}
			return v, nil
		}
		tc, err := readTemplateConfig(t.Config, nil)
		if err != nil {
			errs = append(errs, fmt.Sprintf("the template %q: %v", t.Name, err))
			continue
		}
		var out []*rendered
		for _, p := range tc.parts {
			v := p.value.clone()
			if err = v.walkStrings(nil, func(_ []string, s string) (string, error) { return expand(s, lookup, nil) }); err != nil {
				break
			}
			out = append(out, &rendered{member: p.member, name: p.name, template: t.Name, value: v})
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("the template %q: %v", t.Name, err))
			continue
		}
		for _, r := range out {
			k := r.display()
			if old := byKey[k]; old != nil {
				r.overrides = append(slices.Clone(old.overrides), old.template)
				*old = *r
				continue
			}
			byKey[k] = r
			parts = append(parts, r)
		}
	}
	return parts, templates, errs
}

// desired returns the member's config cfg with the parts rendered: a
// top-level member set whole, in its place or last; a list's element
// replaced in its place, or added at the list's end.
func desired(cfg *jv, parts []*rendered) *jv {
	root := cfg.clone()
	for _, p := range parts {
		if p.name == "" {
			root.set(p.member, p.value.clone())
			continue
		}
		list := root.get(p.member)
		if list == nil || list.kind != 'a' {
			list = &jv{kind: 'a', items: []*jv{}}
			root.set(p.member, list)
		}
		i := slices.IndexFunc(list.items, func(it *jv) bool { return it.kind == 'o' && it.name() == p.name })
		if i >= 0 {
			list.items[i] = p.value.clone()
		} else {
			list.items = append(list.items, p.value.clone())
		}
	}
	return root
}

// lookupJV returns the part member, name of cfg, or nil.
func lookupJV(cfg *jv, member, name string) *jv {
	v := cfg.get(member)
	if v == nil || name == "" {
		return v
	}
	if v.kind != 'a' {
		return nil
	}
	for _, it := range v.items {
		if it.kind == 'o' && it.name() == name {
			return it
		}
	}
	return nil
}

// PartSync is how a member holds a part its templates own: in sync, out of
// sync or in conflict, and why; Have is the member's value and Want the
// rendered one, their secrets masked, when the answer carries values.
type PartSync struct {
	Path      string          `json:"path"`
	Member    string          `json:"member"`
	Name      string          `json:"name,omitempty"`
	Template  string          `json:"template"`
	Overrides []string        `json:"overrides,omitempty"`
	State     string          `json:"state"`
	Reason    string          `json:"reason,omitempty"`
	Have      json.RawMessage `json:"have,omitempty"`
	Want      json.RawMessage `json:"want,omitempty"`
}

// SyncCounts counts a member's parts by state.
type SyncCounts struct {
	InSync    int `json:"in_sync"`
	OutOfSync int `json:"out_of_sync"`
	Conflict  int `json:"conflict"`
}

// MemberSync is a member's sync state against its templates: in sync, out
// of sync, in conflict, unmanaged, or an error, with each part's.
type MemberSync struct {
	Node  string `json:"node"`
	State string `json:"state"`
	// Error is why the member could not be read; Errors the templates
	// that do not render for it.
	Error   *Problem  `json:"error,omitempty"`
	Errors  []string  `json:"errors,omitempty"`
	Checked time.Time `json:"checked,omitzero"`
	// ConfigETag is the member's config's ETag when it was read: what a
	// rollout's If-Match takes.
	ConfigETag string     `json:"config_etag,omitempty"`
	Templates  []string   `json:"templates"`
	Parts      []PartSync `json:"parts"`
	Counts     SyncCounts `json:"counts"`
	// Note says that the member runs another version of fit, whose config
	// may read the templates otherwise.
	Note string `json:"note,omitempty"`
	// Released are parts the member's record listed that no template owns
	// now.
	Released []string `json:"released,omitempty"`
	// Managed is what came of telling the member what it owns: "told", it
	// was told; "kept", it knew; "unsupported", a fit older than it;
	// "failed", with ManagedError; "" when not told.
	Managed      string `json:"managed,omitempty"`
	ManagedError string `json:"managed_error,omitempty"`
}

// summary returns s without the parts' values, as the manager keeps it.
func (s *MemberSync) summary() MemberSync {
	c := *s
	c.Parts = make([]PartSync, len(s.Parts))
	for i, p := range s.Parts {
		p.Have, p.Want = nil, nil
		c.Parts[i] = p
	}
	c.Templates = slices.Clone(s.Templates)
	c.Errors = slices.Clone(s.Errors)
	c.Released = slices.Clone(s.Released)
	return c
}

// compareResult is a member's config compared with its templates: its sync
// state, the record of what the manager owns there to tell it, and its
// desired config.
type compareResult struct {
	sync    MemberSync
	record  managed.Record
	desired *jv
}

// compare compares the member's config cfg with what its templates render,
// with old, the record the member keeps, for what the manager last saw in
// sync; self names the manager in the new record.
func compare(in *renderInput, cfg *jv, old *managed.Record, self managed.Record, now time.Time) *compareResult {
	parts, templates, errs := in.render()
	res := &compareResult{sync: MemberSync{Node: in.name, Templates: templates, Errors: errs, Parts: []PartSync{}, Checked: now}}
	res.record = self
	res.record.Paths = []managed.Path{}
	base := map[string]string{}
	for _, p := range old.Paths {
		base[p.String()] = p.Hash
	}
	for _, p := range parts {
		k := p.display()
		have := lookupJV(cfg, p.member, p.name)
		want := p.value.bytes()
		hWant := managed.Hash(want)
		ps := PartSync{Path: k, Member: p.member, Name: p.name, Template: p.template, Overrides: p.overrides,
			Want: masked(p.value).bytes()}
		var hHave string
		if have != nil {
			hHave = managed.Hash(have.bytes())
			ps.Have = masked(have).bytes()
		}
		b := base[k]
		switch {
		case have != nil && hHave == hWant:
			ps.State = SyncInSync
			b = hWant
		case have == nil && b == "":
			ps.State, ps.Reason = SyncOutOfSync, ReasonMissing
		case have == nil:
			ps.State, ps.Reason = SyncConflict, ReasonRemovedHere
		case b != "" && hHave == b:
			ps.State, ps.Reason = SyncOutOfSync, ReasonTemplateChanged
		case b == "":
			ps.State, ps.Reason = SyncConflict, ReasonOwnValue
		default:
			ps.State, ps.Reason = SyncConflict, ReasonChangedHere
		}
		switch ps.State {
		case SyncInSync:
			res.sync.Counts.InSync++
		case SyncOutOfSync:
			res.sync.Counts.OutOfSync++
		default:
			res.sync.Counts.Conflict++
		}
		res.sync.Parts = append(res.sync.Parts, ps)
		res.record.Paths = append(res.record.Paths, managed.Path{Member: p.member, Name: p.name, Template: p.template, Hash: b})
		delete(base, k)
	}
	res.sync.Released = slices.Sorted(maps.Keys(base))
	res.record.Normalize()
	res.desired = desired(cfg, parts)
	switch {
	case len(errs) > 0:
		res.sync.State = SyncError
	case len(templates) == 0:
		res.sync.State = SyncUnmanaged
	case res.sync.Counts.Conflict > 0:
		res.sync.State = SyncConflict
	case res.sync.Counts.OutOfSync > 0:
		res.sync.State = SyncOutOfSync
	default:
		res.sync.State = SyncInSync
	}
	return res
}
