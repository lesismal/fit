package cluster

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/app/console"
)

// The bounds of what groups hold.
const (
	// MaxGroups is how many groups the manager keeps.
	MaxGroups = 256
	// MaxSelectorBytes bounds a group's selector, which has MaxLabels
	// terms at most.
	MaxSelectorBytes = 1024
	// MaxDescriptionBytes bounds a group's description.
	MaxDescriptionBytes = 256
	// MaxVariables bounds a group's variables, MaxVariableName their
	// names and MaxVariableValue their values.
	MaxVariables     = 64
	MaxVariableName  = 64
	MaxVariableValue = 4096
)

// The actions an AuditEntry records of groups.
const (
	ActionGroupAdd    = "group.add"
	ActionGroupUpdate = "group.update"
	ActionGroupRemove = "group.remove"
)

// Group is a set of members, a resource group: those whose labels its
// Selector matches, and those Members names, whichever labels they have. A
// member is in any number of groups. Variables are the group's values for
// the templates attached to it (I145); they are not secrets, and are shown
// as they are.
type Group struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Selector    string            `json:"selector,omitempty"`
	Members     []string          `json:"members,omitempty"`
	Variables   map[string]string `json:"variables,omitempty"`
	Created     time.Time         `json:"created"`
	Updated     time.Time         `json:"updated"`
}

// clone returns a copy of g that shares nothing with it.
func (g Group) clone() Group {
	g.Members = slices.Clone(g.Members)
	g.Variables = maps.Clone(g.Variables)
	return g
}

// GroupInput is a group as the API takes it, to add or replace one.
type GroupInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Selector    string            `json:"selector"`
	Members     []string          `json:"members"`
	Variables   map[string]string `json:"variables"`
}

// GroupView is what the API answers of a group: the group, and the members
// in it now, by name, with the names Members lists that no member has.
type GroupView struct {
	Group
	Nodes   []string `json:"nodes"`
	Missing []string `json:"missing"`
}

// term is one requirement of a selector: the label key there (exists), not
// there (absent), or there with the value (=) or not with it (!=), which a
// member without the label meets.
type term struct {
	key, op, value string
}

// The operators of a selector's terms.
const (
	opExists = "exists"
	opAbsent = "!"
	opEqual  = "="
	opNot    = "!="
)

// Selector is a label selector, Kubernetes' equality-based ones: terms
// separated by commas, each key=value (or key==value), key!=value, key (the
// label is there) or !key (it is not), every one of which a member's labels
// must meet. The empty selector selects no member: a group of members by
// name alone.
type Selector []term

var varPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseSelector reads a label selector.
func ParseSelector(s string) (Selector, error) {
	if len(s) > MaxSelectorBytes {
		return nil, fmt.Errorf("the selector is more than %d bytes", MaxSelectorBytes)
	}
	var sel Selector
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			if strings.TrimSpace(s) != "" {
				return nil, fmt.Errorf("%q has an empty term: separate terms by single commas", s)
			}
			continue
		}
		var t term
		switch {
		case strings.HasPrefix(part, "!") && !strings.Contains(part, "="):
			t = term{key: strings.TrimSpace(part[1:]), op: opAbsent}
		case strings.Contains(part, "!="):
			k, v, _ := strings.Cut(part, "!=")
			t = term{key: strings.TrimSpace(k), op: opNot, value: strings.TrimSpace(v)}
		case strings.Contains(part, "="):
			k, v, _ := strings.Cut(part, "=")
			t = term{key: strings.TrimSpace(k), op: opEqual, value: strings.TrimSpace(strings.TrimPrefix(v, "="))}
		default:
			t = term{key: part, op: opExists}
		}
		if len(t.key) > MaxLabelKey || !labelPattern.MatchString(t.key) {
			return nil, fmt.Errorf("%q: %q is not a label key: 1 to %d letters, digits, '_', '.', '/' and '-', starting and ending with a letter or digit", part, t.key, MaxLabelKey)
		}
		if len(t.value) > MaxLabelValue || t.value != "" && !labelPattern.MatchString(t.value) {
			return nil, fmt.Errorf("%q: %q is not a label value: 0 to %d letters, digits, '_', '.', '/' and '-', starting and ending with a letter or digit", part, t.value, MaxLabelValue)
		}
		sel = append(sel, t)
	}
	if len(sel) > MaxLabels {
		return nil, fmt.Errorf("the selector has %d terms, more than %d", len(sel), MaxLabels)
	}
	return sel, nil
}

// Matches reports whether labels meet every term of s; the empty selector
// matches nothing.
func (s Selector) Matches(labels map[string]string) bool {
	if len(s) == 0 {
		return false
	}
	for _, t := range s {
		v, has := labels[t.key]
		switch t.op {
		case opExists:
			if !has {
				return false
			}
		case opAbsent:
			if has {
				return false
			}
		case opEqual:
			if !has || v != t.value {
				return false
			}
		case opNot:
			if has && v == t.value {
				return false
			}
		}
	}
	return true
}

// String writes s as ParseSelector reads it, its terms in their order.
func (s Selector) String() string {
	parts := make([]string, len(s))
	for i, t := range s {
		switch t.op {
		case opExists:
			parts[i] = t.key
		case opAbsent:
			parts[i] = "!" + t.key
		default:
			parts[i] = t.key + t.op + t.value
		}
	}
	return strings.Join(parts, ", ")
}

// compiled is a group with its selector parsed, as membership is decided.
type compiled struct {
	g   *Group
	sel Selector
}

// has reports whether mem is in the group.
func (c *compiled) has(mem *Member) bool {
	return slices.Contains(c.g.Members, mem.Name) || c.sel.Matches(mem.Labels)
}

// group returns the group in, checked, stamped now and keeping old's
// creation (old is nil for a new group); maxMembers bounds its members by
// name.
func (in *GroupInput) group(old *Group, now time.Time, maxMembers int) (Group, error) {
	g := Group{Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), Created: now, Updated: now}
	if old != nil {
		g.Created = old.Created
	}
	sel, err := ParseSelector(in.Selector)
	if err != nil {
		return Group{}, fieldErr("selector", "%v", err)
	}
	g.Selector = sel.String()
	seen := map[string]bool{}
	for _, name := range in.Members {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		g.Members = append(g.Members, name)
	}
	sort.Strings(g.Members)
	if len(in.Variables) > 0 {
		g.Variables = maps.Clone(in.Variables)
	}
	if err := g.check(maxMembers); err != nil {
		return Group{}, err
	}
	return g, nil
}

// check checks g as a group the manager keeps.
func (g *Group) check(maxMembers int) error {
	switch {
	case g.Name == "" || len(g.Name) > MaxNameBytes || !namePattern.MatchString(g.Name):
		return fieldErr("name", "%q is not a name: 1 to %d letters, digits, '_', '.' and '-', starting with a letter or digit", g.Name, MaxNameBytes)
	case len(g.Description) > MaxDescriptionBytes || !printable(g.Description):
		return fieldErr("description", "the description is at most %d bytes of printable characters", MaxDescriptionBytes)
	case g.Selector == "" && len(g.Members) == 0:
		return fieldErr("selector", "a group is a label selector, members by name, or both: give one")
	case len(g.Members) > maxMembers:
		return fieldErr("members", "%d members by name, more than %d (max_nodes)", len(g.Members), maxMembers)
	case len(g.Variables) > MaxVariables:
		return fieldErr("variables", "%d variables, more than %d", len(g.Variables), MaxVariables)
	}
	if _, err := ParseSelector(g.Selector); err != nil {
		return fieldErr("selector", "%v", err)
	}
	for _, name := range g.Members {
		if len(name) > MaxNameBytes || !namePattern.MatchString(name) {
			return fieldErr("members", "%q is not a member's name", name)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(g.Variables)) {
		v := g.Variables[k]
		if len(k) > MaxVariableName || !varPattern.MatchString(k) {
			return fieldErr("variables", "%q is not a variable's name: 1 to %d letters, digits and '_', not starting with a digit", k, MaxVariableName)
		}
		if len(v) > MaxVariableValue || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return fieldErr("variables", "the value of %q is not at most %d bytes of UTF-8", k, MaxVariableValue)
		}
	}
	return nil
}

func groupIndex(d *document, name string) int {
	return slices.IndexFunc(d.Groups, func(g Group) bool { return g.Name == name })
}

// compileGroups sets m.groups from the document's groups; a group that does
// not check is left out, and logged once. m.mu is held.
func (m *Manager) compileGroups(logBad bool) {
	m.groups = m.groups[:0]
	for i := range m.doc.Groups {
		g := &m.doc.Groups[i]
		if err := g.check(max(m.maxNodes, len(g.Members))); err != nil {
			if logBad {
				m.logf("cluster: the group %q kept in the store is left out: %v", g.Name, err)
			}
			continue
		}
		sel, _ := ParseSelector(g.Selector) // checked
		m.groups = append(m.groups, compiled{g: g, sel: sel})
	}
}

// groupsOf returns the names of the groups mem is in, sorted. m.mu is held.
func (m *Manager) groupsOf(mem *Member) []string {
	out := []string{}
	for i := range m.groups {
		if m.groups[i].has(mem) {
			out = append(out, m.groups[i].g.Name)
		}
	}
	sort.Strings(out)
	return out
}

// selectIn returns the members of d sel or names select, sorted, and the
// names that are no member's.
func selectIn(d *document, sel Selector, names []string) (nodes, missing []string) {
	nodes, missing = []string{}, []string{}
	for i := range d.Members {
		mem := &d.Members[i]
		if slices.Contains(names, mem.Name) || sel.Matches(mem.Labels) {
			nodes = append(nodes, mem.Name)
		}
	}
	for _, name := range names {
		if index(d, name) < 0 && !slices.Contains(missing, name) {
			missing = append(missing, name)
		}
	}
	sort.Strings(nodes)
	sort.Strings(missing)
	return nodes, missing
}

// viewIn returns what the API shows of g, a group of d.
func viewIn(d *document, g *Group) GroupView {
	sel, _ := ParseSelector(g.Selector) // checked
	v := GroupView{Group: g.clone()}
	if v.Members == nil {
		v.Members = []string{}
	}
	if v.Variables == nil {
		v.Variables = map[string]string{}
	}
	v.Nodes, v.Missing = selectIn(d, sel, g.Members)
	return v
}

// viewLocked returns what the API shows of g. m.mu is held.
func (m *Manager) viewLocked(g *Group) GroupView { return viewIn(&m.doc, g) }

// Groups returns the groups as the API shows them, by name.
func (m *Manager) Groups() []GroupView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]GroupView, 0, len(m.doc.Groups))
	for i := range m.doc.Groups {
		out = append(out, m.viewLocked(&m.doc.Groups[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Select returns the members a selector and names would select now, and
// the names no member has: a group's form previews its members with it.
func (m *Manager) Select(selector string, names []string) (nodes, missing []string, err error) {
	sel, err := ParseSelector(selector)
	if err != nil {
		return nil, nil, fieldErr("selector", "%v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	nodes, missing = selectIn(&m.doc, sel, names)
	return nodes, missing, nil
}

// AddGroup adds the group in, as user.
func (m *Manager) AddGroup(user string, in *GroupInput) (GroupView, error) {
	var out GroupView
	err := m.change(user, func(d *document) (AuditEntry, error) {
		g, err := in.group(nil, m.now().UTC(), m.maxNodes)
		if err != nil {
			return AuditEntry{}, err
		}
		if groupIndex(d, g.Name) >= 0 {
			return AuditEntry{}, errConflict("a group is already named " + strconv.Quote(g.Name))
		}
		if len(d.Groups) >= MaxGroups {
			return AuditEntry{}, errConflict(fmt.Sprintf("the manager keeps %d groups at most", MaxGroups))
		}
		d.Groups = append(d.Groups, g)
		out = viewIn(d, &g)
		return AuditEntry{Action: ActionGroupAdd, Group: g.Name, Detail: describeGroup(&g)}, nil
	})
	if err == nil {
		m.logf("cluster: %q added the group %q", user, out.Name)
	}
	return out, err
}

// UpdateGroup replaces the group name with in, which may rename it, as
// user.
func (m *Manager) UpdateGroup(user, name string, in *GroupInput) (GroupView, error) {
	var out GroupView
	err := m.change(user, func(d *document) (AuditEntry, error) {
		i := groupIndex(d, name)
		if i < 0 {
			return AuditEntry{}, errNoGroup(name)
		}
		if strings.TrimSpace(in.Name) == "" {
			in.Name = name
		}
		g, err := in.group(&d.Groups[i], m.now().UTC(), m.maxNodes)
		if err != nil {
			return AuditEntry{}, err
		}
		if g.Name != name && groupIndex(d, g.Name) >= 0 {
			return AuditEntry{}, errConflict("a group is already named " + strconv.Quote(g.Name))
		}
		changed := groupChanges(&d.Groups[i], &g)
		d.Groups[i] = g
		if g.Name != name {
			renameGroupInTemplates(d, name, g.Name)
		}
		out = viewIn(d, &g)
		return AuditEntry{Action: ActionGroupUpdate, Group: name, Detail: changed}, nil
	})
	if err == nil {
		m.logf("cluster: %q changed the group %q", user, name)
	}
	return out, err
}

// RemoveGroup removes the group name, as user; its members stay.
func (m *Manager) RemoveGroup(user, name string) error {
	err := m.change(user, func(d *document) (AuditEntry, error) {
		i := groupIndex(d, name)
		if i < 0 {
			return AuditEntry{}, errNoGroup(name)
		}
		d.Groups = slices.Delete(d.Groups, i, i+1)
		renameGroupInTemplates(d, name, "")
		return AuditEntry{Action: ActionGroupRemove, Group: name}, nil
	})
	if err == nil {
		m.logf("cluster: %q removed the group %q", user, name)
	}
	return err
}

// renameMember has the groups that list the member old by name list it as
// name; a member removed (name "") is taken out of them.
func renameMember(d *document, old, name string) {
	for i := range d.Groups {
		g := &d.Groups[i]
		j := slices.Index(g.Members, old)
		if j < 0 {
			continue
		}
		g.Members = slices.Delete(slices.Clone(g.Members), j, j+1)
		if name != "" && !slices.Contains(g.Members, name) {
			g.Members = append(g.Members, name)
			sort.Strings(g.Members)
		}
	}
}

// errNoGroup is a group there is none of.
type errNoGroup string

func (e errNoGroup) Error() string { return "no group is named " + strconv.Quote(string(e)) }

// describeGroup says what a group selects.
func describeGroup(g *Group) string {
	var parts []string
	if g.Selector != "" {
		parts = append(parts, "selector "+g.Selector)
	}
	if len(g.Members) > 0 {
		parts = append(parts, "members "+strings.Join(g.Members, ", "))
	}
	if len(g.Variables) > 0 {
		parts = append(parts, "variables "+strings.Join(slices.Sorted(maps.Keys(g.Variables)), ", "))
	}
	return strings.Join(parts, "; ")
}

// groupChanges names the fields that changed from a to b.
func groupChanges(a, b *Group) string {
	var out []string
	add := func(changed bool, name string) {
		if changed {
			out = append(out, name)
		}
	}
	add(a.Name != b.Name, "name")
	add(a.Description != b.Description, "description")
	add(a.Selector != b.Selector, "selector")
	add(!slices.Equal(a.Members, b.Members), "members")
	add(!maps.Equal(a.Variables, b.Variables), "variables")
	if len(out) == 0 {
		return "nothing"
	}
	return strings.Join(out, ", ")
}

// groupsBody is GET /api/groups's answer.
type groupsBody struct {
	Version   string      `json:"version"`
	MaxGroups int         `json:"max_groups"`
	ReadOnly  string      `json:"read_only,omitempty"`
	Groups    []GroupView `json:"groups"`
}

// groupBody is GET /api/groups/<name>'s answer: the group and its members'
// states.
type groupBody struct {
	GroupView
	States []State `json:"states"`
}

// selectBody is POST /api/select's body and answer.
type selectBody struct {
	Selector string   `json:"selector"`
	Members  []string `json:"members"`
}

func (m *Manager) groupRoutes(api *console.API) {
	api.Handle("GET", "/api/groups", m.listGroups)
	api.Handle("POST", "/api/groups", m.addGroup)
	api.HandlePrefix("", "/api/groups/", m.groupRoute)
	api.Handle("POST", "/api/select", m.selectMembers)
}

func (m *Manager) listGroups(r *console.Request) {
	m.mu.Lock()
	ro := m.readOnly
	m.mu.Unlock()
	tag := m.etag()
	r.Header().Set("ETag", tag)
	_ = r.JSON(200, groupsBody{Version: strings.Trim(tag, `"`), MaxGroups: MaxGroups, ReadOnly: ro, Groups: m.Groups()})
}

// decodeGroup decodes a group from the request's body, bounded.
func decodeGroup(r *console.Request, in *GroupInput) bool {
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a group is %d bytes at most", MaxBodyBytes))
		return false
	}
	return r.Decode(in)
}

func (m *Manager) addGroup(r *console.Request) {
	var in GroupInput
	if !decodeGroup(r, &in) || !m.ifMatch(r) {
		return
	}
	user := r.User
	r.Go(func() {
		v, err := m.AddGroup(user, &in)
		m.answer(r, 201, v, err)
	})
}

// groupRoute answers /api/groups/<name>.
func (m *Manager) groupRoute(r *console.Request) {
	name, sub, _ := strings.Cut(strings.TrimPrefix(r.Path, "/api/groups/"), "/")
	if sub != "" {
		_ = console.Error(r.Context, 404, "no such endpoint")
		return
	}
	switch r.Request.Method {
	case "GET":
		m.mu.Lock()
		i := groupIndex(&m.doc, name)
		var body groupBody
		if i >= 0 {
			body.GroupView = m.viewLocked(&m.doc.Groups[i])
			body.States = make([]State, 0, len(body.Nodes))
			for _, n := range body.Nodes {
				body.States = append(body.States, m.stateLocked(&m.doc.Members[index(&m.doc, n)]))
			}
		}
		m.mu.Unlock()
		if i < 0 {
			_ = console.Error(r.Context, 404, errNoGroup(name).Error())
			return
		}
		r.Header().Set("ETag", m.etag())
		_ = r.JSON(200, body)
	case "PUT":
		var in GroupInput
		if !decodeGroup(r, &in) || !m.ifMatch(r) {
			return
		}
		user := r.User
		r.Go(func() {
			v, err := m.UpdateGroup(user, name, &in)
			m.answer(r, 200, v, err)
		})
	case "DELETE":
		if !m.ifMatch(r) {
			return
		}
		user := r.User
		r.Go(func() {
			err := m.RemoveGroup(user, name)
			m.answer(r, 200, map[string]string{"removed": name}, err)
		})
	default:
		r.Header().Set("Allow", "GET, PUT, DELETE")
		_ = console.Error(r.Context, 405, "the method is not allowed")
	}
}

// selectMembers answers POST /api/select: the members a selector and names
// select now, and the names no member has, for a group's form.
func (m *Manager) selectMembers(r *console.Request) {
	var in selectBody
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a selection is %d bytes at most", MaxBodyBytes))
		return
	}
	if !r.Decode(&in) {
		return
	}
	nodes, missing, err := m.Select(in.Selector, in.Members)
	if err != nil {
		m.answer(r, 0, nil, err)
		return
	}
	_ = r.JSON(200, map[string][]string{"nodes": nodes, "missing": missing})
}
