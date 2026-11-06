package cluster

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
)

// The manager's console users have roles (I148): what each may do on the
// cluster, cluster-wide and per group.
//
//   - viewer reads: the members, their states, groups, templates, rollouts,
//     operations and the audit log, and a member's own pages through the
//     node switcher, read-only;
//   - operator also runs operations (reload, rollback, check, purge, test)
//     and rollouts of the templates there are (a template's change, a sync);
//   - admin also changes members and their credentials, writes through the
//     node switcher and copies parts of one member's config to others, and,
//     cluster-wide, changes groups, templates and the roles themselves.
//
// A user's role on a member is the highest of its cluster-wide role and its
// roles on the groups the member is in; on a group, the higher of its
// cluster-wide role and its role there. The roles are kept in the manager's
// document (Access), changed on the Users and audit page by a cluster-wide
// admin, and take effect at once; admin.cluster.admins names console users
// who are cluster-wide admins whatever the document says, changed only at
// a restart, as all of admin is: the way back in when no admin is left.
// Until roles are set (no Access, no admins), every console user is a
// cluster-wide admin, as before roles were.
//
// Every endpoint of the module goes through one check, Manager.Allow: the
// router (router below) lets in a read (GET, HEAD) of a user with a role
// anywhere and anything else of a cluster-wide admin, unless the endpoint
// was added as checked (router.Checked), whose handler, or the Manager's
// method it calls, says what it needs with a Need. See docs/cluster.md.

// The roles, lowest first.
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

// Roles lists the roles, lowest first.
var Roles = []string{RoleViewer, RoleOperator, RoleAdmin}

// roleRank orders the roles: none 0, viewer 1, operator 2, admin 3.
func roleRank(r string) int { return slices.Index(Roles, r) + 1 }

// higher returns the higher of two roles.
func higher(a, b string) string {
	if roleRank(b) > roleRank(a) {
		return b
	}
	return a
}

// The bounds of the roles kept.
const (
	// MaxAccessUsers is how many users the roles name.
	MaxAccessUsers = 256
	// MaxUserGroups is how many groups a user has roles on.
	MaxUserGroups = 64
)

// The actions of the audit log that change roles.
const ActionAccessUpdate = "access.update"

// Access is the users' roles, as the document keeps them: each user's
// cluster-wide role and roles per group; a console user not named has
// DefaultRole, none when "".
type Access struct {
	Users       []UserRoles `json:"users"`
	DefaultRole string      `json:"default_role,omitempty"`
}

// UserRoles is a user's roles: Role cluster-wide ("" for none), and Groups
// by group name.
type UserRoles struct {
	User   string            `json:"user"`
	Role   string            `json:"role,omitempty"`
	Groups map[string]string `json:"groups,omitempty"`
}

// clone returns a copy of a that shares nothing with it.
func (a *Access) clone() *Access {
	if a == nil {
		return nil
	}
	out := &Access{DefaultRole: a.DefaultRole, Users: slices.Clone(a.Users)}
	for i := range out.Users {
		out.Users[i].Groups = maps.Clone(out.Users[i].Groups)
	}
	return out
}

// user returns user's entry, or nil.
func (a *Access) user(user string) *UserRoles {
	if a == nil {
		return nil
	}
	for i := range a.Users {
		if a.Users[i].User == user {
			return &a.Users[i]
		}
	}
	return nil
}

// check checks a as roles the manager keeps: groups are names of d's.
func (a *Access) check(d *document) error {
	if a.DefaultRole != "" && roleRank(a.DefaultRole) == 0 {
		return fieldErr("default_role", "%q is not a role: %s, or none", a.DefaultRole, strings.Join(Roles, ", "))
	}
	if len(a.Users) > MaxAccessUsers {
		return fieldErr("users", "%d users, more than %d", len(a.Users), MaxAccessUsers)
	}
	seen := map[string]bool{}
	for i, u := range a.Users {
		at := "users[" + strconv.Itoa(i) + "]"
		switch {
		case u.User == "" || len(u.User) > MaxUsernameBytes || !printable(u.User):
			return fieldErr(at+".user", "a user's name is 1 to %d printable characters", MaxUsernameBytes)
		case seen[u.User]:
			return fieldErr(at+".user", "%q is named twice", u.User)
		case u.Role != "" && roleRank(u.Role) == 0:
			return fieldErr(at+".role", "%q is not a role: %s, or none", u.Role, strings.Join(Roles, ", "))
		case len(u.Groups) > MaxUserGroups:
			return fieldErr(at+".groups", "roles on %d groups, more than %d", len(u.Groups), MaxUserGroups)
		}
		seen[u.User] = true
		for _, g := range slices.Sorted(maps.Keys(u.Groups)) {
			if roleRank(u.Groups[g]) == 0 {
				return fieldErr(at+".groups", "%q on the group %q is not a role: %s", u.Groups[g], g, strings.Join(Roles, ", "))
			}
			if groupIndex(d, g) < 0 {
				return fieldErr(at+".groups", "no group is named %q", g)
			}
		}
	}
	return nil
}

// renameGroupInAccess has the roles on the group old on name; a group
// removed (name "") takes its roles with it, so that a group made later
// under its name gives no one a role.
func renameGroupInAccess(d *document, old, name string) {
	if d.Access == nil {
		return
	}
	a := d.Access.clone()
	for i := range a.Users {
		r, ok := a.Users[i].Groups[old]
		if !ok {
			continue
		}
		delete(a.Users[i].Groups, old)
		if name != "" {
			a.Users[i].Groups[name] = higher(a.Users[i].Groups[name], r)
		}
	}
	d.Access = a
}

// roleSet is a user's roles, as the checks read them.
type roleSet struct {
	// cluster is the cluster-wide role, groups the roles per group.
	cluster string
	groups  map[string]string
	// open: no roles are set, and every console user is a cluster-wide
	// admin.
	open bool
	// fixed: admin.cluster.admins names the user.
	fixed bool
}

// rolesIn returns user's roles under d's Access and admins, the config's.
func rolesIn(d *document, admins []string, user string) roleSet {
	if user == AdminAPIUser {
		// The Admin API's token holder (adminapi.go); a console user of
		// the name is refused before its roles are read (gate).
		return roleSet{cluster: RoleAdmin, fixed: true}
	}
	if d.Access == nil && len(admins) == 0 {
		return roleSet{cluster: RoleAdmin, open: true}
	}
	if slices.Contains(admins, user) {
		return roleSet{cluster: RoleAdmin, fixed: true}
	}
	if u := d.Access.user(user); u != nil {
		return roleSet{cluster: u.Role, groups: u.Groups}
	}
	if d.Access != nil {
		return roleSet{cluster: d.Access.DefaultRole}
	}
	return roleSet{}
}

// on returns the role on a member in the groups named.
func (rs roleSet) on(groups []string) string {
	r := rs.cluster
	for _, g := range groups {
		r = higher(r, rs.groups[g])
	}
	return r
}

// onGroup returns the role on the group g.
func (rs roleSet) onGroup(g string) string { return higher(rs.cluster, rs.groups[g]) }

// top returns the highest role anywhere.
func (rs roleSet) top() string {
	r := rs.cluster
	for _, gr := range rs.groups {
		r = higher(r, gr)
	}
	return r
}

// groupsIn returns the names of d's groups mem is in, its selectors parsed
// now: for a document being changed, whose groups m.groups are not yet.
func groupsIn(d *document, mem *Member) []string {
	var out []string
	for i := range d.Groups {
		g := &d.Groups[i]
		sel, err := ParseSelector(g.Selector)
		if err != nil {
			continue
		}
		if (&compiled{g: g, sel: sel}).has(mem) {
			out = append(out, g.Name)
		}
	}
	return out
}

// Need is what an action needs of a user's roles: Role on each of Nodes
// (members, by name) and of Groups; Role cluster-wide when Cluster; with
// none of them, Role anywhere: cluster-wide or on any group.
type Need struct {
	Role    string
	Nodes   []string
	Groups  []string
	Cluster bool
}

// ErrForbidden is an action refused for the user's roles: the role it
// needed, and the members or groups it lacks it on.
type ErrForbidden struct {
	User    string   `json:"user"`
	Role    string   `json:"role"`
	Nodes   []string `json:"nodes,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Cluster bool     `json:"cluster,omitempty"`
}

func (e *ErrForbidden) Error() string {
	var where string
	switch {
	case e.Cluster:
		where = "cluster-wide"
	case len(e.Nodes) > 0:
		where = "on the members " + clip(strings.Join(e.Nodes, ", "), 512)
	case len(e.Groups) > 0:
		where = "on the groups " + clip(strings.Join(e.Groups, ", "), 512)
	default:
		where = "on the cluster or one of its groups"
	}
	return fmt.Sprintf("%q may not do this: it needs the role %s %s, which an admin of the cluster gives on the Users and audit page", e.User, e.Role, where)
}

// Allow returns nil when user's roles meet need, or an *ErrForbidden. It is
// the check every endpoint goes through, and the one later iterations call
// for theirs (I147's alerts, I153's assistant tools).
func (m *Manager) Allow(user string, need Need) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allowLocked(user, need)
}

// allowLocked is Allow; m.mu is held.
func (m *Manager) allowLocked(user string, need Need) error {
	rs := rolesIn(&m.doc, m.admins, user)
	want := roleRank(need.Role)
	refused := &ErrForbidden{User: user, Role: need.Role}
	if need.Cluster && roleRank(rs.cluster) < want {
		refused.Cluster = true
		return refused
	}
	for _, n := range need.Nodes {
		role := rs.cluster
		if i := index(&m.doc, n); i >= 0 {
			role = rs.on(m.groupsOf(&m.doc.Members[i]))
		}
		if roleRank(role) < want {
			refused.Nodes = append(refused.Nodes, n)
		}
	}
	for _, g := range need.Groups {
		if roleRank(rs.onGroup(g)) < want {
			refused.Groups = append(refused.Groups, g)
		}
	}
	if len(refused.Nodes) > 0 || len(refused.Groups) > 0 {
		return refused
	}
	if !need.Cluster && len(need.Nodes) == 0 && len(need.Groups) == 0 && roleRank(rs.top()) < want {
		return refused
	}
	return nil
}

// allowIn checks that user has role on each of mems in d, a document being
// changed, whose groups are taken as d has them.
func (m *Manager) allowIn(d *document, user, role string, mems ...*Member) error {
	rs := rolesIn(d, m.admins, user)
	refused := &ErrForbidden{User: user, Role: role}
	for _, mem := range mems {
		if roleRank(rs.on(groupsIn(d, mem))) < roleRank(role) {
			refused.Nodes = append(refused.Nodes, mem.Name)
		}
	}
	if len(refused.Nodes) > 0 {
		return refused
	}
	return nil
}

// allow answers 403 and returns false when r's user's roles do not meet
// need.
func (m *Manager) allow(r *console.Request, need Need) bool {
	err := m.Allow(r.User, need)
	if err == nil {
		return true
	}
	m.answer(r, 0, nil, err)
	return false
}

// reading reports whether method only reads.
func reading(method string) bool { return method == "GET" || method == "HEAD" }

// router adds the module's endpoints behind the role check. Handle and
// HandlePrefix let in a read (GET, HEAD) of a user with a role anywhere,
// and anything else of a cluster-wide admin only; Checked and
// CheckedPrefix let in a user with a role anywhere, and the handler, or the
// Manager's method it calls, checks what more it needs with Manager.Allow
// (or allow). An endpoint added without a router is not checked at all:
// every one goes through it.
type router struct {
	m   *Manager
	api *console.API
}

// Handle adds an endpoint: a read for any role, anything else for a
// cluster-wide admin.
func (rt *router) Handle(method, path string, h console.Handler) {
	rt.api.Handle(method, path, rt.m.gate(h, false))
}

// HandlePrefix adds endpoints under prefix, checked as Handle checks.
func (rt *router) HandlePrefix(method, prefix string, h console.Handler) {
	rt.api.HandlePrefix(method, prefix, rt.m.gate(h, false))
}

// Checked adds an endpoint that checks what it needs itself, beyond a role
// anywhere.
func (rt *router) Checked(method, path string, h console.Handler) {
	rt.api.Handle(method, path, rt.m.gate(h, true))
}

// CheckedPrefix adds endpoints under prefix, checked as Checked checks.
func (rt *router) CheckedPrefix(method, prefix string, h console.Handler) {
	rt.api.HandlePrefix(method, prefix, rt.m.gate(h, true))
}

// gate returns h behind the role check: a role anywhere, and, unless
// checked, cluster-wide admin for anything but a read.
func (m *Manager) gate(h console.Handler, checked bool) console.Handler {
	return func(r *console.Request) {
		if r.User == AdminAPIUser {
			_ = console.Error(r.Context, 403, strconv.Quote(AdminAPIUser)+" is the name the manager's Admin API acts under: a console user of that name has no role")
			return
		}
		need := Need{Role: RoleViewer}
		if !checked && !reading(r.Request.Method) {
			need = Need{Role: RoleAdmin, Cluster: true}
		}
		if m.allow(r, need) {
			h(r)
		}
	}
}

// RolesActive reports whether roles are set: otherwise every console user
// is a cluster-wide admin.
func (m *Manager) RolesActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !rolesIn(&m.doc, m.admins, "").open
}

// ConsoleGate is the console's check of a request beside the module's own
// (app/console's Options.Gate): once roles are set, the manager's own
// pages and its other modules are changed only by a cluster-wide admin, as
// a change to the manager's config, users or store could otherwise give
// anyone any role. Reads, the module cluster and the switcher (which check
// themselves), the assistant (whose levels AssistantCap bounds), a user's
// own password, the text conversions and dry runs are let through. It
// returns "" for a request let through, or why it is refused.
func (m *Manager) ConsoleGate(user, method, path, query string) string {
	if user == AdminAPIUser && !reading(method) {
		return strconv.Quote(AdminAPIUser) + " is the name the manager's Admin API acts under: a console user of that name changes nothing"
	}
	if reading(method) || strings.HasPrefix(path, "/api/m/"+ModuleID+"/") || strings.HasPrefix(path, PassPrefix) ||
		strings.HasPrefix(path, "/api/m/ai/") {
		return ""
	}
	switch method + " " + path {
	case "POST /api/m/admin/account/password", "POST /api/m/admin/config/render", "POST /api/m/admin/config/parse":
		return ""
	}
	if strings.HasPrefix(path, "/api/m/admin/") && dryRun(query) {
		return ""
	}
	if err := m.Allow(user, Need{Role: RoleAdmin, Cluster: true}); err != nil {
		return fmt.Sprintf("%q may not change this manager: on a manager whose cluster has roles, its own config, users, store and services are changed by a cluster-wide admin only", user)
	}
	return ""
}

// dryRun reports whether a query asks for a dry run.
func dryRun(query string) bool {
	for p := range strings.SplitSeq(query, "&") {
		if p == "dry_run=1" || p == "dry_run=true" {
			return true
		}
	}
	return false
}

// AssistantCap is the highest level the assistant has for user on the
// manager (I126's levels), whatever admin.assistant gives: the assistant
// never does more than the user may. Once roles are set, a cluster-wide
// admin keeps every level; a user who is an operator somewhere at most
// proposes, which a cluster-wide admin applies; anyone else reads. Before
// roles are set, it is apply: no bound.
func (m *Manager) AssistantCap(user string) string {
	if user == AdminAPIUser {
		return capFor(roleSet{})
	}
	m.mu.Lock()
	rs := rolesIn(&m.doc, m.admins, user)
	m.mu.Unlock()
	return capFor(rs)
}

// SetConsole tells the manager who the console's users are, and their
// assistant's levels (nil without the assistant), for the Users page.
func (m *Manager) SetConsole(users func() []string, level func(user string) string) {
	m.mu.Lock()
	m.consoleUsers, m.assistantLevel = users, level
	m.mu.Unlock()
}

// meBody is GET /api/me's answer: the user's roles, cluster-wide, per
// group and on each member, for the pages to show only what it may do.
type meBody struct {
	User string `json:"user"`
	// Open: no roles are set, and every console user is a cluster-wide
	// admin. Fixed: admin.cluster.admins names the user.
	Open  bool              `json:"open"`
	Fixed bool              `json:"fixed,omitempty"`
	Role  string            `json:"role"`
	Top   string            `json:"top"`
	Group map[string]string `json:"groups"`
	Nodes map[string]string `json:"nodes"`
	// ManagerReadOnly: the user may not change the manager's own pages
	// (ConsoleGate).
	ManagerReadOnly bool `json:"manager_read_only"`
	// Assistant is the highest level the assistant has for the user here.
	Assistant string `json:"assistant"`
}

// me returns user's roles as GET /api/me answers them.
func (m *Manager) me(user string) meBody {
	m.mu.Lock()
	rs := rolesIn(&m.doc, m.admins, user)
	b := meBody{User: user, Open: rs.open, Fixed: rs.fixed, Role: rs.cluster, Top: rs.top(), Group: map[string]string{}, Nodes: map[string]string{}}
	for i := range m.doc.Groups {
		if r := rs.onGroup(m.doc.Groups[i].Name); r != "" {
			b.Group[m.doc.Groups[i].Name] = r
		}
	}
	for i := range m.doc.Members {
		if r := rs.on(m.groupsOf(&m.doc.Members[i])); r != "" {
			b.Nodes[m.doc.Members[i].Name] = r
		}
	}
	m.mu.Unlock()
	b.ManagerReadOnly = !rs.open && rs.cluster != RoleAdmin
	b.Assistant = capFor(rs)
	return b
}

// accessUser is a user as GET /api/access answers it: its roles, whether
// the console has the user, admin.cluster.admins naming it, and its
// assistant's level.
type accessUser struct {
	UserRoles
	Console   bool   `json:"console"`
	Fixed     bool   `json:"fixed,omitempty"`
	Assistant string `json:"assistant,omitempty"`
	// AssistantCap is the assistant's level bounded by the roles.
	AssistantCap string `json:"assistant_cap,omitempty"`
}

// accessBody is GET /api/access's answer, and PUT's.
type accessBody struct {
	Version     string       `json:"version"`
	Open        bool         `json:"open"`
	ReadOnly    string       `json:"read_only,omitempty"`
	Admins      []string     `json:"admins"`
	DefaultRole string       `json:"default_role"`
	Users       []accessUser `json:"users"`
	Groups      []string     `json:"groups"`
	Roles       []string     `json:"roles"`
	MaxUsers    int          `json:"max_users"`
}

// accessView returns the roles as the Users page shows them: every user
// the roles name, every console user and every fixed admin.
func (m *Manager) accessView() accessBody {
	m.mu.Lock()
	acc, admins, users, level, ro := m.doc.Access.clone(), slices.Clone(m.admins), m.consoleUsers, m.assistantLevel, m.readOnly
	b := accessBody{Version: strings.Trim(strconv.Quote(strconv.FormatUint(m.rev, 10)), `"`), Open: acc == nil && len(admins) == 0,
		ReadOnly: ro, Admins: admins, Users: []accessUser{}, Groups: []string{}, Roles: Roles, MaxUsers: MaxAccessUsers}
	for i := range m.doc.Groups {
		b.Groups = append(b.Groups, m.doc.Groups[i].Name)
	}
	doc := document{Access: acc}
	m.mu.Unlock()
	sort.Strings(b.Groups)
	var console []string
	if users != nil {
		console = users()
	}
	if acc != nil {
		b.DefaultRole = acc.DefaultRole
	}
	names := map[string]bool{}
	add := func(name string) {
		if names[name] {
			return
		}
		names[name] = true
		u := accessUser{UserRoles: UserRoles{User: name}, Console: slices.Contains(console, name), Fixed: slices.Contains(admins, name)}
		if e := acc.user(name); e != nil {
			u.UserRoles = UserRoles{User: name, Role: e.Role, Groups: maps.Clone(e.Groups)}
		} else if acc != nil {
			u.Role = acc.DefaultRole
		}
		if u.Fixed || b.Open {
			u.Role = RoleAdmin
		}
		if u.Groups == nil {
			u.Groups = map[string]string{}
		}
		if level != nil {
			u.Assistant = level(name)
			rs := rolesIn(&doc, admins, name)
			if rank := slices.Index(config.AssistantLevels, u.Assistant); rank >= 0 {
				capped := capFor(rs)
				if c := slices.Index(config.AssistantLevels, capped); c < rank {
					u.AssistantCap = capped
				} else {
					u.AssistantCap = u.Assistant
				}
			}
		}
		b.Users = append(b.Users, u)
	}
	if acc != nil {
		for _, u := range acc.Users {
			add(u.User)
		}
	}
	for _, n := range console {
		add(n)
	}
	for _, n := range admins {
		add(n)
	}
	sort.Slice(b.Users, func(i, j int) bool { return b.Users[i].User < b.Users[j].User })
	return b
}

// capFor is AssistantCap for the roles rs.
func capFor(rs roleSet) string {
	switch {
	case rs.cluster == RoleAdmin:
		return config.AssistantApply
	case roleRank(rs.top()) >= roleRank(RoleOperator):
		return config.AssistantPropose
	}
	return config.AssistantRead
}

// SetAccess replaces the roles with in, as user, who must be a cluster-wide
// admin; at least one cluster-wide admin must be left, among the users
// named or admin.cluster.admins. Users named with no role and no group
// roles are dropped.
func (m *Manager) SetAccess(user string, in *Access) (accessBody, error) {
	err := m.change(user, func(d *document) (AuditEntry, error) {
		if err := m.allowCluster(d, user); err != nil {
			return AuditEntry{}, err
		}
		a := in.clone()
		if a == nil {
			a = &Access{}
		}
		kept := a.Users[:0]
		for _, u := range a.Users {
			u.User = strings.TrimSpace(u.User)
			for g, r := range u.Groups {
				if r == "" {
					delete(u.Groups, g)
				}
			}
			if len(u.Groups) == 0 {
				u.Groups = nil
			}
			if u.Role == "" && len(u.Groups) == 0 && a.DefaultRole == "" {
				continue
			}
			kept = append(kept, u)
		}
		a.Users = kept
		sort.Slice(a.Users, func(i, j int) bool { return a.Users[i].User < a.Users[j].User })
		if err := a.check(d); err != nil {
			return AuditEntry{}, err
		}
		if len(m.admins) == 0 && !slices.ContainsFunc(a.Users, func(u UserRoles) bool { return u.Role == RoleAdmin }) {
			return AuditEntry{}, fieldErr("users", "no one would be an admin of the cluster: give at least one user the role admin cluster-wide (or name one in admin.cluster.admins)")
		}
		before := d.Access
		d.Access = a
		e := AuditEntry{Action: ActionAccessUpdate, Detail: describeAccessChange(before, a)}
		e.setDiff(accessText(before), accessText(a), "roles before", "roles after")
		return e, nil
	})
	if err != nil {
		return accessBody{}, err
	}
	m.logf("cluster: %q changed the users' roles", user)
	return m.accessView(), nil
}

// allowCluster checks that user is a cluster-wide admin under d's roles.
func (m *Manager) allowCluster(d *document, user string) error {
	if rs := rolesIn(d, m.admins, user); rs.cluster != RoleAdmin {
		return &ErrForbidden{User: user, Role: RoleAdmin, Cluster: true}
	}
	return nil
}

// accessText is a's roles as the audit log diffs them: a line a user.
func accessText(a *Access) string {
	if a == nil {
		return ""
	}
	var b strings.Builder
	if a.DefaultRole != "" {
		fmt.Fprintf(&b, "default role: %s\n", a.DefaultRole)
	}
	for _, u := range a.Users {
		fmt.Fprintf(&b, "%s: %s", u.User, orNone(u.Role))
		for _, g := range slices.Sorted(maps.Keys(u.Groups)) {
			fmt.Fprintf(&b, ", %s on %s", u.Groups[g], g)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func orNone(r string) string {
	if r == "" {
		return "none"
	}
	return r
}

// describeAccessChange names the users whose roles changed.
func describeAccessChange(a, b *Access) string {
	var changed []string
	old := map[string]string{}
	if a != nil {
		for _, u := range a.Users {
			j, _ := json.Marshal(u) // strings
			old[u.User] = string(j)
		}
	}
	for _, u := range b.Users {
		j, _ := json.Marshal(u) // strings
		if old[u.User] != string(j) {
			changed = append(changed, u.User)
		}
		delete(old, u.User)
	}
	changed = append(changed, slices.Sorted(maps.Keys(old))...)
	s := "the roles of " + clip(strings.Join(changed, ", "), 512)
	if len(changed) == 0 {
		s = "no user's roles"
	}
	if a == nil {
		s = "roles set for the first time; " + s
	}
	if (a == nil && b.DefaultRole != "") || (a != nil && a.DefaultRole != b.DefaultRole) {
		s += "; the default role " + orNone(b.DefaultRole)
	}
	return s
}

// accessRoutes adds GET /api/me, GET and PUT /api/access.
func (m *Manager) accessRoutes(api *router) {
	// Every console user reads its own roles, a role or not: the pages
	// learn from it what to show.
	api.api.Handle("GET", "/api/me", func(r *console.Request) { _ = r.JSON(200, m.me(r.User)) })
	api.Handle("GET", "/api/access", func(r *console.Request) {
		b := m.accessView()
		r.Header().Set("ETag", strconv.Quote(b.Version))
		_ = r.JSON(200, b)
	})
	api.Handle("PUT", "/api/access", func(r *console.Request) {
		var in Access
		if !bodyBounded(r) || !r.Decode(&in) || !m.ifMatch(r) {
			return
		}
		user := r.User
		r.Go(func() {
			b, err := m.SetAccess(user, &in)
			m.answer(r, 200, b, err)
		})
	})
}
