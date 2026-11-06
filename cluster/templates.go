package cluster

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/managed"
)

// The bounds of what templates hold.
const (
	// MaxTemplates is how many templates the manager keeps.
	MaxTemplates = 128
	// MaxTemplateBytes bounds a template's config, as JSON.
	MaxTemplateBytes = 64 << 10
	// MaxTemplateParts bounds the parts a template owns.
	MaxTemplateParts = 256
	// MaxTemplateGroups bounds the groups a template is attached to.
	MaxTemplateGroups = 32
	// MaxOrder bounds a template's order, either way.
	MaxOrder = 1000
)

// The actions an AuditEntry records of templates.
const (
	ActionTemplateAdd    = "template.add"
	ActionTemplateUpdate = "template.update"
	ActionTemplateRemove = "template.remove"
)

// SecretMask stands for a secret a template keeps, in what the API answers;
// sent back in its place, it keeps the secret kept there.
const SecretMask = "[secret]"

// Template is config the manager owns on the members of the groups it is
// attached to: its Config is a fragment of a fit's config, whose top-level
// members it owns whole, but for the elements of the named lists
// (managed.Lists: listeners, routes, services, upstreams), which it owns
// one by one, by name. A member's desired config is its own with the parts
// its templates own taken from them, the templates applied by Order (then
// by name), a later one winning a part two own. Strings in Config may use
// variables: ${node.name}, ${node.id}, ${node.hostname},
// ${node.labels.<key>} and a group's variables, ${<name>}; $${ is a ${ as
// written.
type Template struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Groups      []string        `json:"groups"`
	Order       int             `json:"order"`
	Config      json.RawMessage `json:"config"`
	Created     time.Time       `json:"created"`
	Updated     time.Time       `json:"updated"`
}

// clone returns a copy of t that shares nothing with it.
func (t Template) clone() Template {
	t.Groups = slices.Clone(t.Groups)
	t.Config = slices.Clone(t.Config)
	return t
}

// TemplateInput is a template as the API takes it, to add or replace one.
// From, in place of Config, makes the template's config from a member's:
// the parts Paths names (as "log" or "routes[api]"), as the member has them
// now, secrets and all.
type TemplateInput struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Groups      []string        `json:"groups"`
	Order       int             `json:"order"`
	Config      json.RawMessage `json:"config"`
	From        *ImportFrom     `json:"from,omitempty"`
}

// ImportFrom is the parts of a member's config a template is made from.
type ImportFrom struct {
	Node  string   `json:"node"`
	Paths []string `json:"paths"`
}

// Part is a part of a config a template owns: a top-level member whole, or
// an element of a named list by its name.
type Part struct {
	Path   string `json:"path"`
	Member string `json:"member"`
	Name   string `json:"name,omitempty"`
}

// TemplateView is what the API answers of a template: the template, its
// secrets masked (SecretMask), with the parts it owns, the variables it
// uses, what it warns of (paths to files, which are each member's own), the
// members in its groups now, and the groups it names that there are none
// of.
type TemplateView struct {
	Template
	Parts         []Part   `json:"parts"`
	Variables     []string `json:"variables"`
	Warnings      []string `json:"warnings"`
	Nodes         []string `json:"nodes"`
	MissingGroups []string `json:"missing_groups"`
}

// topMembers are the config's top-level members, by their JSON names.
var topMembers = sync.OnceValue(func() []string {
	var out []string
	for _, f := range config.Fields(reflect.TypeFor[config.Config]()) {
		if f.Name != "" && f.Name != "-" {
			out = append(out, f.Name)
		}
	}
	return out
})

// parsePart reads a part as the pages write it: "log", or "routes[api]".
func parsePart(s string) (member, name string, err error) {
	s = strings.TrimSpace(s)
	member = s
	if i := strings.IndexByte(s, '['); i >= 0 && strings.HasSuffix(s, "]") {
		member, name = s[:i], s[i+1:len(s)-1]
	}
	if err := checkPart(member, name); err != nil {
		return "", "", err
	}
	return member, name, nil
}

// checkPart checks a part a template may own.
func checkPart(member, name string) error {
	if !slices.Contains(topMembers(), member) {
		return fmt.Errorf("%q is not a top-level member of fit's config", member)
	}
	return managed.CheckPart(member, name)
}

// ---- Variables ----

// nodeVars are a member's own variables, as a template names them.
var nodeVars = []string{"node.name", "node.id", "node.hostname"}

// checkVar checks a variable a template names.
func checkVar(name string) error {
	switch {
	case slices.Contains(nodeVars, name):
		return nil
	case strings.HasPrefix(name, "node.labels."):
		k := strings.TrimPrefix(name, "node.labels.")
		if len(k) > MaxLabelKey || !labelPattern.MatchString(k) {
			return fmt.Errorf("${%s}: %q is not a label key", name, k)
		}
		return nil
	case strings.HasPrefix(name, "node."):
		return fmt.Errorf("${%s}: a member's variables are %s and node.labels.<key>", name, strings.Join(nodeVars, ", "))
	case len(name) > MaxVariableName || !varPattern.MatchString(name):
		return fmt.Errorf("${%s}: a group's variable is named with letters, digits and '_'", name)
	}
	return nil
}

// expand replaces the variables of s with what lookup gives them; lookup is
// nil to check s's variables alone, each named to seen.
func expand(s string, lookup func(name string) (string, error), seen func(name string)) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], "${")
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		j += i
		if j > i && s[j-1] == '$' {
			// $${ is a ${ as written.
			b.WriteString(s[i : j-1])
			b.WriteString("${")
			i = j + 2
			continue
		}
		b.WriteString(s[i:j])
		end := strings.IndexByte(s[j+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("%q has a ${ that does not end with }; write $${ for a ${ as it is", s)
		}
		name := s[j+2 : j+2+end]
		if err := checkVar(name); err != nil {
			return "", err
		}
		if seen != nil {
			seen(name)
		}
		if lookup != nil {
			v, err := lookup(name)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
		}
		i = j + 3 + end
	}
	return b.String(), nil
}

// ---- Secrets ----

// secretName reports whether a member named name holds a secret: a
// password, a key, a token or a hash, but not a file or a variable naming
// one, nor a limit. It is the assistant's rule (assistant/redact.go).
func secretName(name string) bool {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, "_file") || strings.HasSuffix(n, "_env") || strings.HasSuffix(n, "_ttl") ||
		strings.HasPrefix(n, "max_") || strings.HasPrefix(n, "min_"):
		return false
	case strings.Contains(n, "password") || strings.Contains(n, "secret") || strings.Contains(n, "passphrase"):
		return true
	}
	switch n {
	case "passwd", "pass", "token", "tokens", "key", "keys", "apikey", "hash", "credential", "credentials",
		"authorization", "cookie", "signature", "psk", "private_key":
		return true
	}
	for _, suffix := range []string{"_token", "_tokens", "_key", "_keys", "_hash", "_sha256", "_signature"} {
		if strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return false
}

// rateKey is rate_limit's key, a selector rather than a secret: ip, route
// or header:Name.
var rateKey = regexp.MustCompile(`^(ip|route|header:[A-Za-z0-9!#$%&'*+.^_|~-]{1,64})$`)

// secretAt reports whether the string s, under the object members path,
// is a secret: under a member secretName names (but rate_limit's key), or
// a push target's url, which carries its stream key.
func secretAt(path []string, s string) bool {
	if s == "" || s == SecretMask {
		return s == SecretMask
	}
	for i, k := range path {
		if secretName(k) && !(k == "key" && i == len(path)-1 && rateKey.MatchString(s)) {
			return true
		}
	}
	n := len(path)
	return n >= 2 && path[n-1] == "url" && path[n-2] == "push"
}

// masked returns v with its secrets replaced by SecretMask.
func masked(v *jv) *jv {
	c := v.clone()
	_ = c.walkStrings(nil, func(path []string, s string) (string, error) {
		if secretAt(path, s) {
			return SecretMask, nil
		}
		return s, nil
	})
	return c
}

// restoreSecrets puts back, in nv, the secrets old keeps where nv has
// SecretMask: matched by object member, by name in a list of named objects,
// or else by index. A mask with no secret kept in its place is an error.
func restoreSecrets(nv, old *jv, at string) error {
	switch nv.kind {
	case 's':
		if nv.str != SecretMask {
			return nil
		}
		if old == nil || old.kind != 's' || old.str == SecretMask {
			return fmt.Errorf("%s is %s, but no secret is kept there: write the secret itself", at, SecretMask)
		}
		nv.str = old.str
	case 'o':
		for i, k := range nv.keys {
			if err := restoreSecrets(nv.vals[i], old.get(k), at+"."+k); err != nil {
				return err
			}
		}
	case 'a':
		for i, it := range nv.items {
			var o *jv
			switch name := it.name(); {
			case old == nil || old.kind != 'a':
			case name != "":
				for _, x := range old.items {
					if x.name() == name {
						o = x
						break
					}
				}
			case i < len(old.items):
				o = old.items[i]
			}
			if err := restoreSecrets(it, o, at+"["+strconv.Itoa(i)+"]"); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- A template's config ----

// tplConfig is a template's config read: its parts, in their order, with
// their values.
type tplConfig struct {
	root  *jv
	parts []tplPart
}

// tplPart is a part a template owns and its value.
type tplPart struct {
	member, name string
	value        *jv
}

func (p *tplPart) display() string { return managed.Display(p.member, p.name) }

// readTemplateConfig reads and checks a template's config: an object of
// top-level members of fit's config, but admin and schema_version; the
// named lists' elements objects with names, each once; variables as
// checkVar has them. seen is told each variable used.
func readTemplateConfig(data []byte, seen func(string)) (*tplConfig, error) {
	if len(data) > MaxTemplateBytes {
		return nil, fieldErr("config", "a template's config is %d bytes at most", MaxTemplateBytes)
	}
	root, err := parseJV(data)
	if err != nil {
		return nil, fieldErr("config", "the config is not JSON: %v", err)
	}
	if root.kind != 'o' {
		return nil, fieldErr("config", "a template's config is a JSON object of top-level members of fit's config")
	}
	tc := &tplConfig{root: root}
	for i, k := range root.keys {
		v := root.vals[i]
		if err := checkPart(k, ""); err != nil && !managed.IsList(k) {
			return nil, fieldErr("config", "%v", err)
		}
		if !managed.IsList(k) {
			tc.parts = append(tc.parts, tplPart{member: k, value: v})
			continue
		}
		if v.kind != 'a' {
			return nil, fieldErr("config", "%s is a list, whose elements the template owns by name", k)
		}
		for j, it := range v.items {
			name := it.name()
			switch {
			case it.kind != 'o':
				return nil, fieldErr("config", "%s[%d] is not an object", k, j)
			case name == "":
				return nil, fieldErr("config", "%s[%d] has no name: a template owns the elements of %s by name, so each is named", k, j, k)
			case strings.Contains(name, "${"):
				return nil, fieldErr("config", "%s[%d]: its name, %q, is how the template owns it on every member: it takes no variables", k, j, name)
			}
			if err := managed.CheckPart(k, name); err != nil {
				return nil, fieldErr("config", "%s[%d]: %v", k, j, err)
			}
			for _, p := range tc.parts {
				if p.member == k && p.name == name {
					return nil, fieldErr("config", "%s has two elements named %q", k, name)
				}
			}
			tc.parts = append(tc.parts, tplPart{member: k, name: name, value: it})
		}
	}
	if len(tc.parts) > MaxTemplateParts {
		return nil, fieldErr("config", "the template owns %d parts, more than %d", len(tc.parts), MaxTemplateParts)
	}
	err = root.walkStrings(nil, func(path []string, s string) (string, error) {
		_, err := expand(s, nil, seen)
		return s, err
	})
	if err != nil {
		return nil, fieldErr("config", "%v", err)
	}
	return tc, nil
}

// fileWarnings are the warnings of a template's config about paths to
// files: a member reads its own, which the manager cannot see.
func fileWarnings(tc *tplConfig) []string {
	out := []string{}
	for i := range tc.parts {
		p := &tc.parts[i]
		var walk func(v *jv, at string)
		walk = func(v *jv, at string) {
			switch v.kind {
			case 'o':
				for j, k := range v.keys {
					sub := at + "." + k
					if fileKey(k) && v.vals[j].kind != 'r' {
						out = append(out, fmt.Sprintf("%s names a file, which each member reads from its own disk: it must be there on every member", sub))
						continue
					}
					walk(v.vals[j], sub)
				}
			case 'a':
				for j, it := range v.items {
					walk(it, at+"["+strconv.Itoa(j)+"]")
				}
			}
		}
		walk(p.value, p.display())
	}
	return out
}

// fileKey reports whether a member named k holds paths to files or
// directories.
func fileKey(k string) bool {
	return strings.HasSuffix(k, "_file") || strings.HasSuffix(k, "_files") || strings.HasSuffix(k, "_dir")
}

// ---- The templates kept ----

// template returns the template in, checked, stamped now and keeping
// old's creation and secrets (old is nil for a new template).
func (in *TemplateInput) template(old *Template, now time.Time) (Template, error) {
	t := Template{Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), Order: in.Order,
		Groups: []string{}, Created: now, Updated: now}
	if old != nil {
		t.Created = old.Created
	}
	seen := map[string]bool{}
	for _, g := range in.Groups {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		t.Groups = append(t.Groups, g)
	}
	if len(in.Config) == 0 {
		in.Config = json.RawMessage("{}")
	}
	root, err := parseJV(in.Config)
	if err != nil {
		return Template{}, fieldErr("config", "the config is not JSON: %v", err)
	}
	var oldRoot *jv
	if old != nil {
		oldRoot, _ = parseJV(old.Config) // checked when kept
	}
	if err := restoreSecrets(root, oldRoot, "config"); err != nil {
		return Template{}, fieldErr("config", "%v", err)
	}
	t.Config = root.bytes()
	if err := t.check(); err != nil {
		return Template{}, err
	}
	return t, nil
}

// check checks t as a template the manager keeps.
func (t *Template) check() error {
	switch {
	case t.Name == "" || len(t.Name) > MaxNameBytes || !namePattern.MatchString(t.Name):
		return fieldErr("name", "%q is not a name: 1 to %d letters, digits, '_', '.' and '-', starting with a letter or digit", t.Name, MaxNameBytes)
	case len(t.Description) > MaxDescriptionBytes || !printable(t.Description):
		return fieldErr("description", "the description is at most %d bytes of printable characters", MaxDescriptionBytes)
	case len(t.Groups) > MaxTemplateGroups:
		return fieldErr("groups", "attached to %d groups, more than %d", len(t.Groups), MaxTemplateGroups)
	case t.Order < -MaxOrder || t.Order > MaxOrder:
		return fieldErr("order", "the order is from %d to %d", -MaxOrder, MaxOrder)
	}
	for _, g := range t.Groups {
		if len(g) > MaxNameBytes || !namePattern.MatchString(g) {
			return fieldErr("groups", "%q is not a group's name", g)
		}
	}
	_, err := readTemplateConfig(t.Config, nil)
	return err
}

// templateIndex returns the index of the template name in d, or -1.
func templateIndex(d *document, name string) int {
	return slices.IndexFunc(d.Templates, func(t Template) bool { return t.Name == name })
}

// templateViewIn returns what the API shows of t, a template of d.
func templateViewIn(d *document, t *Template) TemplateView {
	v := TemplateView{Template: t.clone(), Parts: []Part{}, Variables: []string{}, Warnings: []string{}, Nodes: []string{}, MissingGroups: []string{}}
	if root, err := parseJV(t.Config); err == nil {
		v.Config = masked(root).bytes()
	}
	vars := map[string]bool{}
	if tc, err := readTemplateConfig(t.Config, func(n string) { vars[n] = true }); err == nil {
		for _, p := range tc.parts {
			v.Parts = append(v.Parts, Part{Path: p.display(), Member: p.member, Name: p.name})
		}
		v.Warnings = fileWarnings(tc)
	} else {
		v.Warnings = append(v.Warnings, err.Error())
	}
	v.Variables = slices.Sorted(maps.Keys(vars))
	nodes := map[string]bool{}
	for _, g := range t.Groups {
		i := groupIndex(d, g)
		if i < 0 {
			v.MissingGroups = append(v.MissingGroups, g)
			continue
		}
		for _, n := range viewIn(d, &d.Groups[i]).Nodes {
			nodes[n] = true
		}
	}
	for n := range nodes {
		v.Nodes = append(v.Nodes, n)
	}
	sort.Strings(v.Nodes)
	return v
}

// Templates returns the templates as the API shows them, by name.
func (m *Manager) Templates() []TemplateView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TemplateView, 0, len(m.doc.Templates))
	for i := range m.doc.Templates {
		out = append(out, templateViewIn(&m.doc, &m.doc.Templates[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TemplateByName returns the template name as the API shows it.
func (m *Manager) TemplateByName(name string) (TemplateView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := templateIndex(&m.doc, name)
	if i < 0 {
		return TemplateView{}, false
	}
	return templateViewIn(&m.doc, &m.doc.Templates[i]), true
}

// AddTemplate adds the template in, as user.
func (m *Manager) AddTemplate(user string, in *TemplateInput) (TemplateView, error) {
	var out TemplateView
	err := m.change(user, func(d *document) (AuditEntry, error) {
		if err := m.allowCluster(d, user); err != nil {
			return AuditEntry{}, err
		}
		t, err := in.template(nil, m.now().UTC())
		if err != nil {
			return AuditEntry{}, err
		}
		if templateIndex(d, t.Name) >= 0 {
			return AuditEntry{}, errConflict("a template is already named " + strconv.Quote(t.Name))
		}
		if len(d.Templates) >= MaxTemplates {
			return AuditEntry{}, errConflict(fmt.Sprintf("the manager keeps %d templates at most", MaxTemplates))
		}
		d.Templates = append(slices.Clone(d.Templates), t)
		out = templateViewIn(d, &t)
		e := AuditEntry{Action: ActionTemplateAdd, Template: t.Name, Detail: describeTemplate(&out), Nodes: out.Nodes}
		e.setDiff("", templateText(d, &t), "none", t.Name)
		return e, nil
	})
	if err == nil {
		m.logf("cluster: %q added the template %q", user, out.Name)
	}
	return out, err
}

// UpdateTemplate replaces the template name with in, which may rename it,
// as user; a secret in sent as SecretMask keeps the one kept.
func (m *Manager) UpdateTemplate(user, name string, in *TemplateInput) (TemplateView, error) {
	var out TemplateView
	err := m.change(user, func(d *document) (AuditEntry, error) {
		if err := m.allowCluster(d, user); err != nil {
			return AuditEntry{}, err
		}
		i := templateIndex(d, name)
		if i < 0 {
			return AuditEntry{}, errNoTemplate(name)
		}
		if strings.TrimSpace(in.Name) == "" {
			in.Name = name
		}
		t, err := in.template(&d.Templates[i], m.now().UTC())
		if err != nil {
			return AuditEntry{}, err
		}
		if t.Name != name && templateIndex(d, t.Name) >= 0 {
			return AuditEntry{}, errConflict("a template is already named " + strconv.Quote(t.Name))
		}
		changed, before := templateChanges(&d.Templates[i], &t), templateText(d, &d.Templates[i])
		d.Templates = slices.Clone(d.Templates)
		d.Templates[i] = t
		out = templateViewIn(d, &t)
		e := AuditEntry{Action: ActionTemplateUpdate, Template: name, Detail: changed, Nodes: out.Nodes}
		e.setDiff(before, templateText(d, &t), name, t.Name)
		return e, nil
	})
	if err == nil {
		m.logf("cluster: %q changed the template %q", user, name)
	}
	return out, err
}

// RemoveTemplate removes the template name, as user. The members keep what
// it gave them; the manager no longer owns it there.
func (m *Manager) RemoveTemplate(user, name string) error {
	err := m.change(user, func(d *document) (AuditEntry, error) {
		if err := m.allowCluster(d, user); err != nil {
			return AuditEntry{}, err
		}
		i := templateIndex(d, name)
		if i < 0 {
			return AuditEntry{}, errNoTemplate(name)
		}
		e := AuditEntry{Action: ActionTemplateRemove, Template: name, Nodes: templateViewIn(d, &d.Templates[i]).Nodes}
		e.setDiff(templateText(d, &d.Templates[i]), "", name, "none")
		d.Templates = slices.Delete(slices.Clone(d.Templates), i, i+1)
		return e, nil
	})
	if err == nil {
		m.logf("cluster: %q removed the template %q", user, name)
	}
	return err
}

// renameGroupInTemplates has the templates attached to the group old
// attached to name; to none, when name is "" (the group removed).
func renameGroupInTemplates(d *document, old, name string) {
	cloned := false
	for i := range d.Templates {
		j := slices.Index(d.Templates[i].Groups, old)
		if j < 0 {
			continue
		}
		if !cloned {
			d.Templates = slices.Clone(d.Templates)
			cloned = true
		}
		t := d.Templates[i].clone()
		if name == "" || slices.Contains(t.Groups, name) {
			t.Groups = slices.Delete(t.Groups, j, j+1)
		} else {
			t.Groups[j] = name
		}
		d.Templates[i] = t
	}
}

// errNoTemplate is a template there is none of.
type errNoTemplate string

func (e errNoTemplate) Error() string { return "no template is named " + strconv.Quote(string(e)) }

// describeTemplate says what a template owns and where.
func describeTemplate(v *TemplateView) string {
	parts := make([]string, len(v.Parts))
	for i, p := range v.Parts {
		parts[i] = p.Path
	}
	s := "owns " + clip(strings.Join(parts, ", "), 512)
	if len(parts) == 0 {
		s = "owns nothing"
	}
	if len(v.Groups) > 0 {
		s += "; attached to " + strings.Join(v.Groups, ", ")
	}
	return s
}

// templateChanges names the fields that changed from a to b; the config by
// the parts that changed, never their values.
func templateChanges(a, b *Template) string {
	var out []string
	add := func(changed bool, name string) {
		if changed {
			out = append(out, name)
		}
	}
	add(a.Name != b.Name, "name")
	add(a.Description != b.Description, "description")
	add(!slices.Equal(a.Groups, b.Groups), "groups")
	add(a.Order != b.Order, "order")
	if changed := partChanges(a.Config, b.Config); changed != "" {
		out = append(out, "config ("+changed+")")
	}
	if len(out) == 0 {
		return "nothing"
	}
	return strings.Join(out, ", ")
}

// partChanges names the parts that differ between two templates' configs.
func partChanges(a, b json.RawMessage) string {
	ta, _ := readTemplateConfig(a, nil)
	tb, _ := readTemplateConfig(b, nil)
	if ta == nil || tb == nil {
		return ""
	}
	val := map[string]string{}
	for _, p := range ta.parts {
		val[p.display()] = managed.Hash(p.value.bytes())
	}
	var out []string
	for _, p := range tb.parts {
		k := p.display()
		if h, ok := val[k]; !ok || h != managed.Hash(p.value.bytes()) {
			out = append(out, k)
		}
		delete(val, k)
	}
	out = append(out, slices.Sorted(maps.Keys(val))...)
	return clip(strings.Join(out, ", "), 512)
}
