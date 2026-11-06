package cluster

import (
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/internal/managed"
)

// capManaged is the capability of GET, PUT and DELETE /node/managed.
const capManaged = "node.managed"

// MaxSyncs is how many reads of members' sync states the pages run at once;
// one more is answered 429.
const MaxSyncs = 4

// syncWait bounds how long a page's request waits for the members it reads:
// those not read by then are answered pending, and read on.
var syncWait = 30 * time.Second

// refreshDelay is how long after a change of the templates, the groups or
// the members the manager tells the members what they own, so that a few
// changes in a row are told once.
var refreshDelay = 500 * time.Millisecond

// tplState is the members' sync states the manager keeps, and its telling
// them what they own.
type tplState struct {
	mu sync.Mutex
	// last is each member's latest sync state, without the parts' values.
	last map[string]MemberSync
	// owned are the members told they own something, whom a change may
	// have to tell they no longer do.
	owned map[string]bool
	// syncs are the page's reads under way; refreshing is whether the
	// refresh runs, dirty whether a change came since it started.
	syncs      int
	refreshing bool
	dirty      bool
	wg         sync.WaitGroup
}

// hostname is the manager's host's name, for the records it gives members.
var hostname = sync.OnceValue(func() string { h, _ := os.Hostname(); return h })

// self is the record that names this manager.
func (m *Manager) self() managed.Record {
	return managed.Record{Manager: m.o.NodeID, ManagerHost: hostname()}
}

// syncOut is a member's sync read: its state, with the parts' values, its
// config and its desired config (nil when it could not be read), and the
// record it was told.
type syncOut struct {
	sync    MemberSync
	config  *jv
	desired *jv
	record  managed.Record
}

// syncMember reads the member name's /node, /config and /node/managed,
// compares its config with what its templates render, and, when push, tells
// it what the manager owns there when that changed; done is called once,
// from a fib worker or a timer, and must not block.
func (m *Manager) syncMember(name string, push bool, done func(*syncOut)) {
	var once sync.Once
	end := func(o *syncOut) {
		once.Do(func() {
			m.keepSync(o, push)
			done(o)
		})
	}
	fail := func(prob *Problem) {
		if prob.At.IsZero() {
			prob.At = m.now().UTC()
		}
		end(&syncOut{sync: MemberSync{Node: name, State: SyncError, Error: prob, Checked: m.now().UTC(), Templates: []string{}, Parts: []PartSync{}}})
	}
	var info NodeInfo
	m.call(name, "GET", "/node", nil, nil, &info, func(_ *Reply, prob *Problem) {
		if prob != nil {
			fail(prob)
			return
		}
		m.call(name, "GET", "/config", nil, nil, nil, func(rc *Reply, prob *Problem) {
			if prob != nil {
				fail(prob)
				return
			}
			cfg, err := parseJV(rc.Body)
			if err != nil || cfg.kind != 'o' {
				fail(&Problem{Kind: KindBadAnswer, Status: rc.Status, Message: "the member's /config is not a JSON object"})
				return
			}
			etag := rc.Header.Get("ETag")
			if !slices.Contains(info.Capabilities, capManaged) {
				m.finishSync(name, &info, cfg, etag, nil, "", push, end)
				return
			}
			var rec managed.Record
			m.call(name, "GET", "/node/managed", nil, nil, &rec, func(rm *Reply, prob *Problem) {
				if prob != nil {
					fail(prob)
					return
				}
				rec.Normalize()
				m.finishSync(name, &info, cfg, etag, &rec, rm.Header.Get("ETag"), push, end)
			})
		})
	})
}

// finishSync compares the member's config with its templates, and tells
// it what it owns when push and that changed; old is the record it keeps,
// nil for a fit that keeps none.
func (m *Manager) finishSync(name string, info *NodeInfo, cfg *jv, etag string, old *managed.Record, oldTag string, push bool, end func(*syncOut)) {
	m.mu.Lock()
	in, ok := m.renderInputLocked(name)
	m.mu.Unlock()
	now := m.now().UTC()
	if !ok {
		end(&syncOut{sync: MemberSync{Node: name, State: SyncError, Checked: now, Templates: []string{}, Parts: []PartSync{},
			Error: &Problem{Kind: KindUnreachable, Message: errNotFound(name).Error(), At: now}}})
		return
	}
	in.id, in.hostname = info.ID, info.Hostname
	base := old
	if base == nil {
		base = &managed.Record{}
	}
	res := compare(in, cfg, base, m.self(), now)
	out := &syncOut{sync: res.sync, config: cfg, desired: res.desired, record: res.record}
	out.sync.ConfigETag = etag
	if kind, why := m.compatible(info.SchemaDigest); kind == ReadOnlyVersion {
		out.sync.Note = why
	}
	switch {
	case old == nil:
		if len(res.record.Paths) > 0 {
			out.sync.Managed = "unsupported"
		}
		end(out)
		return
	case !push:
		end(out)
		return
	case len(old.Paths) == 0 && len(res.record.Paths) == 0 || old.Equal(&res.record):
		out.sync.Managed = "kept"
		end(out)
		return
	}
	rec := res.record
	rec.Updated = now
	m.tell(name, &rec, oldTag, true, func(state, why string) {
		out.sync.Managed, out.sync.ManagedError = state, why
		end(out)
	})
}

// tell puts rec, what the manager owns on the member name, with If-Match
// on tag; when another read of the member told it first (412), it reads the
// record again, and puts it once more unless it is rec already. done is
// told "told", "kept" or "failed" with why.
func (m *Manager) tell(name string, rec *managed.Record, tag string, again bool, done func(state, why string)) {
	body, _ := json.Marshal(rec) // strings and a time
	var h stdhttp.Header
	if tag != "" {
		h = stdhttp.Header{"If-Match": {tag}}
	}
	m.call(name, "PUT", "/node/managed", h, body, nil, func(_ *Reply, prob *Problem) {
		switch {
		case prob == nil:
			m.logf("cluster: told the member %q it is managed in %d parts", name, len(rec.Paths))
			done("told", "")
		case prob.Status != stdhttp.StatusPreconditionFailed || !again:
			done("failed", prob.Message)
		default:
			var now managed.Record
			m.call(name, "GET", "/node/managed", nil, nil, &now, func(r *Reply, prob *Problem) {
				now.Normalize()
				switch {
				case prob != nil:
					done("failed", prob.Message)
				case now.Equal(rec):
					done("kept", "")
				default:
					m.tell(name, rec, r.Header.Get("ETag"), false, done)
				}
			})
		}
	})
}

// keepSync keeps a member's sync state, without the parts' values.
func (m *Manager) keepSync(o *syncOut, push bool) {
	m.tpl.mu.Lock()
	defer m.tpl.mu.Unlock()
	if m.tpl.last == nil {
		m.tpl.last, m.tpl.owned = map[string]MemberSync{}, map[string]bool{}
	}
	m.tpl.last[o.sync.Node] = o.sync.summary()
	if push && (o.sync.Managed == "told" || o.sync.Managed == "kept") {
		m.tpl.owned[o.sync.Node] = len(o.record.Paths) > 0
	}
}

// runSync reads the members names, at most max_concurrency at once, each
// as syncMember with push; each is called with each one's read, from any
// goroutine. It returns once every one is read, or the manager stops. It
// blocks: run it on a goroutine of its own.
func (m *Manager) runSync(names []string, push bool, each func(*syncOut)) {
	sem := make(chan struct{}, m.o.Settings.Concurrency())
	var wg sync.WaitGroup
	for _, name := range names {
		select {
		case sem <- struct{}{}:
		case <-m.stop:
			wg.Wait()
			return
		}
		wg.Add(1)
		m.syncMember(name, push, func(o *syncOut) {
			if each != nil {
				each(o)
			}
			<-sem
			wg.Done()
		})
	}
	wg.Wait()
}

// kickManaged has the manager tell the members what they own, a little
// after a change of the templates, the groups or the members.
func (m *Manager) kickManaged() {
	m.tpl.mu.Lock()
	defer m.tpl.mu.Unlock()
	m.tpl.dirty = true
	if m.tpl.refreshing {
		return
	}
	m.tpl.refreshing = true
	m.tpl.wg.Add(1)
	go m.refreshManaged()
}

// refreshManaged tells the members a template is attached to, and those
// told before, what they own now, until no change came since it started.
// A goroutine of its own.
func (m *Manager) refreshManaged() {
	defer m.tpl.wg.Done()
	for {
		timer := time.NewTimer(refreshDelay)
		select {
		case <-m.stop:
			timer.Stop()
			m.tpl.mu.Lock()
			m.tpl.refreshing = false
			m.tpl.mu.Unlock()
			return
		case <-timer.C:
		}
		m.tpl.mu.Lock()
		if !m.tpl.dirty {
			m.tpl.refreshing = false
			m.tpl.mu.Unlock()
			return
		}
		m.tpl.dirty = false
		told := map[string]bool{}
		for n, owns := range m.tpl.owned {
			if owns {
				told[n] = true
			}
		}
		m.tpl.mu.Unlock()
		m.runSync(m.managedNames(told), true, nil)
	}
}

// managedNames returns the members a template is attached to, and those of
// also that are members still, sorted.
func (m *Manager) managedNames(also map[string]bool) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for i := range m.doc.Members {
		name := m.doc.Members[i].Name
		in, _ := m.renderInputLocked(name)
		if also[name] || len(in.attached()) > 0 {
			out = append(out, name)
		}
	}
	return out
}

// waitSyncs waits for the reads of members' sync states to end, at most
// wait.
func (m *Manager) waitSyncs(wait time.Duration) {
	ended := make(chan struct{})
	go func() { m.tpl.wg.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(wait):
	}
}

// SyncStates returns every member's latest sync state, without the parts'
// values, by name; a member not read yet has none.
func (m *Manager) SyncStates() []MemberSync {
	m.mu.Lock()
	names := make([]string, 0, len(m.doc.Members))
	for i := range m.doc.Members {
		names = append(names, m.doc.Members[i].Name)
	}
	m.mu.Unlock()
	m.tpl.mu.Lock()
	defer m.tpl.mu.Unlock()
	out := []MemberSync{}
	for _, n := range names {
		if s, ok := m.tpl.last[n]; ok {
			out = append(out, s.summary())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// Desired is a member's desired config: its own config with the parts its
// templates own taken from them, secrets and all, and the ETag of the
// config it was made from, which a rollout's If-Match takes (I146).
type Desired struct {
	Node   string
	Config []byte
	ETag   string
	Sync   MemberSync
}

// Desired reads the member name's config and renders its desired config,
// telling the member nothing; done is called once, from a fib worker or a
// timer, and must not block. A member there is none of is the error.
func (m *Manager) Desired(name string, done func(*Desired, *Problem)) error {
	m.mu.Lock()
	_, ok := m.nodes[name]
	m.mu.Unlock()
	if !ok {
		return errNotFound(name)
	}
	m.syncMember(name, false, func(o *syncOut) {
		if o.desired == nil {
			done(nil, o.sync.Error)
			return
		}
		done(&Desired{Node: name, Config: o.desired.pretty(), ETag: o.sync.ConfigETag, Sync: o.sync}, nil)
	})
	return nil
}

// ---- The API ----

func (m *Manager) templateRoutes(api *router) {
	// Templates are changed by a cluster-wide admin, as Handle has it;
	// the reads that POST check the members they read.
	api.Handle("GET", "/api/templates", m.listTemplates)
	api.Handle("POST", "/api/templates", m.addTemplate)
	api.HandlePrefix("", "/api/templates/", m.templateRoute)
	api.Handle("GET", "/api/sync", func(r *console.Request) { _ = r.JSON(200, map[string]any{"nodes": m.SyncStates()}) })
	api.Checked("POST", "/api/sync", m.syncRoute)
	api.CheckedPrefix("GET", "/api/desired/", m.desiredRoute)
	api.Checked("POST", "/api/compare", m.compareRoute)
	api.Checked("POST", "/api/import", m.importRoute)
	api.Checked("POST", "/api/text/render", m.renderText)
	api.Checked("POST", "/api/text/parse", m.parseText)
}

// templatesBody is GET /api/templates's answer.
type templatesBody struct {
	Version      string         `json:"version"`
	MaxTemplates int            `json:"max_templates"`
	ReadOnly     string         `json:"read_only,omitempty"`
	Templates    []TemplateView `json:"templates"`
}

func (m *Manager) listTemplates(r *console.Request) {
	m.mu.Lock()
	ro := m.readOnly
	m.mu.Unlock()
	tag := m.etag()
	r.Header().Set("ETag", tag)
	_ = r.JSON(200, templatesBody{Version: strings.Trim(tag, `"`), MaxTemplates: MaxTemplates, ReadOnly: ro, Templates: m.Templates()})
}

// decodeTemplate decodes a template from the request's body, bounded.
func decodeTemplate(r *console.Request, in *TemplateInput) bool {
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a template is %d bytes at most", MaxBodyBytes))
		return false
	}
	return r.Decode(in)
}

func (m *Manager) addTemplate(r *console.Request) {
	var in TemplateInput
	if !decodeTemplate(r, &in) || !m.ifMatch(r) {
		return
	}
	user := r.User
	if in.From == nil {
		r.Go(func() {
			v, err := m.AddTemplate(user, &in)
			m.answer(r, 201, v, err)
		})
		return
	}
	// A template made from a member's config: its parts read now, with
	// their secrets, which never reach the page.
	from := in.From
	m.mu.Lock()
	_, ok := m.nodes[from.Node]
	m.mu.Unlock()
	if !ok {
		_ = console.Error(r.Context, 404, errNotFound(from.Node).Error())
		return
	}
	r.Context.Retain()
	m.fetchConfig(from.Node, func(cfg *jv, _ string, prob *Problem) {
		if prob != nil {
			defer r.Context.Release()
			_ = r.JSON(502, readFailure{Error: prob.Message, Problem: prob})
			return
		}
		frag, err := fragmentOf(cfg, from.Paths)
		if err != nil {
			defer r.Context.Release()
			m.answer(r, 0, nil, err)
			return
		}
		in.Config, in.From = frag.bytes(), nil
		r.GoThen(func() {
			v, err := m.AddTemplate(user, &in)
			m.answer(r, 201, v, err)
		}, r.Context.Release)
	})
}

// templateRoute answers /api/templates/<name>.
func (m *Manager) templateRoute(r *console.Request) {
	name, sub, _ := strings.Cut(strings.TrimPrefix(r.Path, "/api/templates/"), "/")
	if sub != "" {
		_ = console.Error(r.Context, 404, "no such endpoint")
		return
	}
	switch r.Request.Method {
	case "GET":
		v, ok := m.TemplateByName(name)
		if !ok {
			_ = console.Error(r.Context, 404, errNoTemplate(name).Error())
			return
		}
		r.Header().Set("ETag", m.etag())
		_ = r.JSON(200, v)
	case "PUT":
		var in TemplateInput
		if !decodeTemplate(r, &in) || !m.ifMatch(r) {
			return
		}
		if in.From != nil {
			_ = console.Error(r.Context, 400, "a template is made from a member's config when it is added")
			return
		}
		user := r.User
		r.Go(func() {
			v, err := m.UpdateTemplate(user, name, &in)
			m.answer(r, 200, v, err)
		})
	case "DELETE":
		if !m.ifMatch(r) {
			return
		}
		user := r.User
		r.Go(func() {
			err := m.RemoveTemplate(user, name)
			m.answer(r, 200, map[string]string{"removed": name}, err)
		})
	default:
		r.Header().Set("Allow", "GET, PUT, DELETE")
		_ = console.Error(r.Context, 405, "the method is not allowed")
	}
}

// syncRequest is POST /api/sync's body: the members to read, those named,
// or a template's members, or every member.
type syncRequest struct {
	Nodes    []string `json:"nodes"`
	Template string   `json:"template"`
}

// syncBody is POST /api/sync's answer: each member's sync state, with its
// parts' values, and the members still being read.
type syncBody struct {
	Nodes   []MemberSync `json:"nodes"`
	Pending []string     `json:"pending"`
}

// syncRoute answers POST /api/sync: the members read now, each one's
// config compared with its templates, and each told what it owns.
func (m *Manager) syncRoute(r *console.Request) {
	var req syncRequest
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a request is %d bytes at most", MaxBodyBytes))
		return
	}
	if !r.Decode(&req) {
		return
	}
	names, err := m.syncTargets(&req)
	if err != nil {
		m.answer(r, 0, nil, err)
		return
	}
	if !m.allow(r, Need{Role: RoleViewer, Nodes: names}) {
		return
	}
	m.tpl.mu.Lock()
	if m.tpl.syncs >= MaxSyncs {
		m.tpl.mu.Unlock()
		_ = console.Error(r.Context, 429, "other reads of members are under way; try again")
		return
	}
	m.tpl.syncs++
	m.tpl.wg.Add(1)
	m.tpl.mu.Unlock()
	r.Context.Retain()
	var mu sync.Mutex
	got := map[string]MemberSync{}
	ended := make(chan struct{})
	go func() {
		defer m.tpl.wg.Done()
		go func() {
			m.runSync(names, true, func(o *syncOut) {
				mu.Lock()
				got[o.sync.Node] = o.sync
				mu.Unlock()
			})
			close(ended)
		}()
		timer := time.NewTimer(syncWait)
		select {
		case <-ended:
		case <-timer.C:
		case <-m.stop:
		}
		timer.Stop()
		m.tpl.mu.Lock()
		m.tpl.syncs--
		m.tpl.mu.Unlock()
		body := syncBody{Nodes: []MemberSync{}, Pending: []string{}}
		mu.Lock()
		for _, n := range names {
			if s, ok := got[n]; ok {
				body.Nodes = append(body.Nodes, s)
			} else {
				body.Pending = append(body.Pending, n)
			}
		}
		mu.Unlock()
		defer r.Context.Release()
		_ = r.JSON(200, body)
	}()
}

// syncTargets returns the members req names.
func (m *Manager) syncTargets(req *syncRequest) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case len(req.Nodes) > 0:
		if len(req.Nodes) > m.maxNodes {
			return nil, fieldErr("nodes", "%d members, more than %d", len(req.Nodes), m.maxNodes)
		}
		var out []string
		for _, n := range req.Nodes {
			if index(&m.doc, n) < 0 {
				return nil, errNotFound(n)
			}
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
		return out, nil
	case req.Template != "":
		i := templateIndex(&m.doc, req.Template)
		if i < 0 {
			return nil, errNoTemplate(req.Template)
		}
		return templateViewIn(&m.doc, &m.doc.Templates[i]).Nodes, nil
	}
	out := make([]string, 0, len(m.doc.Members))
	for i := range m.doc.Members {
		out = append(out, m.doc.Members[i].Name)
	}
	sort.Strings(out)
	return out, nil
}

// desiredBody is GET /api/desired/<name>'s answer: the member's sync state,
// its config and its desired config, secrets masked.
type desiredBody struct {
	MemberSync
	Config  json.RawMessage `json:"config,omitempty"`
	Desired json.RawMessage `json:"desired,omitempty"`
}

// desiredRoute answers GET /api/desired/<name>.
func (m *Manager) desiredRoute(r *console.Request) {
	name := strings.TrimPrefix(r.Path, "/api/desired/")
	if !m.allow(r, Need{Role: RoleViewer, Nodes: []string{name}}) {
		return
	}
	m.mu.Lock()
	_, ok := m.nodes[name]
	m.mu.Unlock()
	if !ok {
		_ = console.Error(r.Context, 404, errNotFound(name).Error())
		return
	}
	if !m.takeRead(r) {
		return
	}
	r.Context.Retain()
	m.syncMember(name, true, func(o *syncOut) {
		<-m.reads
		defer r.Context.Release()
		body := desiredBody{MemberSync: o.sync}
		if o.config != nil {
			body.Config, body.Desired = masked(o.config).pretty(), masked(o.desired).pretty()
		}
		_ = r.JSON(200, body)
	})
}

// takeRead takes one of the reads of members, or answers 429.
func (m *Manager) takeRead(r *console.Request) bool {
	select {
	case m.reads <- struct{}{}:
		return true
	default:
		_ = console.Error(r.Context, 429, "other reads of members are under way; try again")
		return false
	}
}

// fetchConfig reads the member name's config; done is called once, from a
// fib worker, a timer or the caller's goroutine (a member there is none of
// now).
func (m *Manager) fetchConfig(name string, done func(cfg *jv, etag string, prob *Problem)) {
	m.call(name, "GET", "/config", nil, nil, nil, func(rc *Reply, prob *Problem) {
		if prob != nil {
			done(nil, "", prob)
			return
		}
		cfg, err := parseJV(rc.Body)
		if err != nil || cfg.kind != 'o' {
			done(nil, "", &Problem{Kind: KindBadAnswer, Status: rc.Status, Message: "the member's /config is not a JSON object", At: m.now().UTC()})
			return
		}
		done(cfg, rc.Header.Get("ETag"), nil)
	})
}

// ConfigPart is a part of a member's config, as Import from a node and
// Compare show it: a top-level member, or an element of a named list by its
// name, or by its index when it has none. Ownable says whether a template
// may own it, and Why when not. Value has its secrets masked.
type ConfigPart struct {
	Path    string          `json:"path"`
	Member  string          `json:"member"`
	Name    string          `json:"name,omitempty"`
	Ownable bool            `json:"ownable"`
	Why     string          `json:"why,omitempty"`
	Secret  bool            `json:"secret,omitempty"`
	Value   json.RawMessage `json:"value"`

	v *jv
}

// partsOf returns cfg's parts, in their order.
func partsOf(cfg *jv) []ConfigPart {
	out := []ConfigPart{}
	for i, k := range cfg.keys {
		v := cfg.vals[i]
		if !managed.IsList(k) || v.kind != 'a' {
			p := ConfigPart{Path: k, Member: k, Ownable: true, v: v}
			if err := checkPart(k, ""); err != nil {
				p.Ownable, p.Why = false, err.Error()
			}
			out = append(out, p)
			continue
		}
		for j, it := range v.items {
			name := ""
			if it.kind == 'o' {
				name = it.name()
			}
			p := ConfigPart{Path: managed.Display(k, name), Member: k, Name: name, Ownable: name != "", v: it}
			if name == "" {
				p.Path = k + "[#" + strconv.Itoa(j) + "]"
				p.Why = "it has no name: a template owns the elements of " + k + " by name"
			}
			out = append(out, p)
		}
	}
	for i := range out {
		mv := masked(out[i].v)
		out[i].Value = mv.bytes()
		out[i].Secret = managed.Hash(out[i].Value) != managed.Hash(out[i].v.bytes())
	}
	return out
}

// fragmentOf returns the parts paths names of cfg, as a template's config.
func fragmentOf(cfg *jv, paths []string) (*jv, error) {
	if len(paths) == 0 {
		return nil, fieldErr("from.paths", "pick the parts of the member's config the template is made of")
	}
	if len(paths) > MaxTemplateParts {
		return nil, fieldErr("from.paths", "%d parts, more than %d", len(paths), MaxTemplateParts)
	}
	root := &jv{kind: 'o'}
	for _, s := range paths {
		member, name, err := parsePart(s)
		if err != nil {
			return nil, fieldErr("from.paths", "%q: %v", s, err)
		}
		v := lookupJV(cfg, member, name)
		if v == nil {
			return nil, fieldErr("from.paths", "the member's config has no %s", managed.Display(member, name))
		}
		if name == "" {
			root.set(member, v.clone())
			continue
		}
		list := root.get(member)
		if list == nil {
			list = &jv{kind: 'a', items: []*jv{}}
			root.set(member, list)
		}
		if lookupJV(root, member, name) == nil {
			list.items = append(list.items, v.clone())
		}
	}
	return root, nil
}

// importRequest is POST /api/import's body.
type importRequest struct {
	Node string `json:"node"`
}

// importBody is POST /api/import's answer: the member's config's parts,
// which a template may be made from.
type importBody struct {
	Node       string       `json:"node"`
	ConfigETag string       `json:"config_etag"`
	Parts      []ConfigPart `json:"parts"`
}

// importRoute answers POST /api/import: the parts of a member's config,
// their secrets masked, for a template made from it.
func (m *Manager) importRoute(r *console.Request) {
	var req importRequest
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a request is %d bytes at most", MaxBodyBytes))
		return
	}
	if !r.Decode(&req) {
		return
	}
	m.readConfigs(r, []string{req.Node}, func(cfgs []*jv, etags []string) any {
		return importBody{Node: req.Node, ConfigETag: etags[0], Parts: partsOf(cfgs[0])}
	})
}

// readConfigs reads the configs of the members names, at most MaxReads at
// once with the members' pages, and answers what answer makes of them; or
// 502 with the first member's problem.
func (m *Manager) readConfigs(r *console.Request, names []string, answer func(cfgs []*jv, etags []string) any) {
	if !m.allow(r, Need{Role: RoleViewer, Nodes: names}) {
		return
	}
	for _, n := range names {
		m.mu.Lock()
		_, ok := m.nodes[n]
		m.mu.Unlock()
		if !ok {
			_ = console.Error(r.Context, 404, errNotFound(n).Error())
			return
		}
	}
	if !m.takeRead(r) {
		return
	}
	r.Context.Retain()
	var mu sync.Mutex
	cfgs, etags, probs := make([]*jv, len(names)), make([]string, len(names)), make([]*Problem, len(names))
	left := len(names)
	for i, n := range names {
		m.fetchConfig(n, func(cfg *jv, etag string, prob *Problem) {
			mu.Lock()
			cfgs[i], etags[i], probs[i] = cfg, etag, prob
			left--
			last := left == 0
			mu.Unlock()
			if !last {
				return
			}
			<-m.reads
			defer r.Context.Release()
			for _, p := range probs {
				if p != nil {
					_ = r.JSON(502, readFailure{Error: p.Message, Problem: p})
					return
				}
			}
			_ = r.JSON(200, answer(cfgs, etags))
		})
	}
}

// compareRequest is POST /api/compare's body: two members.
type compareRequest struct {
	A string `json:"a"`
	B string `json:"b"`
}

// ComparedPart is a part of two members' configs compared: the same,
// different, or in one of them only; A and B are the values, secrets
// masked, where they are.
type ComparedPart struct {
	Path  string          `json:"path"`
	State string          `json:"state"`
	A     json.RawMessage `json:"a,omitempty"`
	B     json.RawMessage `json:"b,omitempty"`
}

// The states of a ComparedPart.
const (
	CompareSame    = "same"
	CompareDiffers = "differs"
	CompareOnlyA   = "only_a"
	CompareOnlyB   = "only_b"
)

// compareSide is one member of a comparison.
type compareSide struct {
	Node       string          `json:"node"`
	ConfigETag string          `json:"config_etag"`
	Config     json.RawMessage `json:"config"`
}

// compareBody is POST /api/compare's answer.
type compareBody struct {
	A     compareSide    `json:"a"`
	B     compareSide    `json:"b"`
	Parts []ComparedPart `json:"parts"`
}

// compareRoute answers POST /api/compare: two members' configs side by
// side, part by part.
func (m *Manager) compareRoute(r *console.Request) {
	var req compareRequest
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a request is %d bytes at most", MaxBodyBytes))
		return
	}
	if !r.Decode(&req) {
		return
	}
	if req.A == "" || req.B == "" || req.A == req.B {
		_ = r.JSON(400, &FieldError{Field: "b", Msg: "compare two members: name both, each once"})
		return
	}
	m.readConfigs(r, []string{req.A, req.B}, func(cfgs []*jv, etags []string) any {
		return compareBody{A: compareSide{Node: req.A, ConfigETag: etags[0], Config: masked(cfgs[0]).pretty()},
			B: compareSide{Node: req.B, ConfigETag: etags[1], Config: masked(cfgs[1]).pretty()}, Parts: compareParts(cfgs[0], cfgs[1])}
	})
}

// compareParts compares two configs part by part: a's parts in their
// order, then those of b's that a has not.
func compareParts(a, b *jv) []ComparedPart {
	pa, pb := partsOf(a), partsOf(b)
	byPath := map[string]*ConfigPart{}
	for i := range pb {
		byPath[pb[i].Path] = &pb[i]
	}
	out := []ComparedPart{}
	for _, p := range pa {
		q := byPath[p.Path]
		c := ComparedPart{Path: p.Path, A: p.Value}
		switch {
		case q == nil:
			c.State = CompareOnlyA
		case managed.Hash(p.v.bytes()) == managed.Hash(q.v.bytes()):
			c.State = CompareSame
		default:
			c.State, c.B = CompareDiffers, q.Value
		}
		if q != nil {
			delete(byPath, p.Path)
		}
		out = append(out, c)
	}
	for _, q := range pb {
		if byPath[q.Path] != nil {
			out = append(out, ComparedPart{Path: q.Path, State: CompareOnlyB, B: q.Value})
		}
	}
	return out
}
