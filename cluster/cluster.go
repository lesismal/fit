// Package cluster is the manager of other fits: one fit, with admin.cluster
// set, keeps the address and an administrator's credentials of other fits,
// its members, polls each one's state, and serves them to the workbench as
// the console module cluster, at /api/m/cluster/. See
// docs/decisions/033-cluster-management.md.
//
// The members are kept in the store's section "cluster" (Section), with
// their passwords and tokens, encrypted with the rest of the store and never
// answered, logged or written anywhere else. A member is reached through its
// console, logged in with a user name and password, or its Admin API, with
// its bearer token; its certificate is verified by the system's CAs, a CA
// PEM of its own, or a pinned fingerprint.
//
// The manager is not in the data path, and nothing here blocks a fib
// worker (rule 1): the calls to members go out on fib's asynchronous HTTP
// client, one transport per member, each with its timeout and its bound on
// answers, and the poller, a goroutine of its own, starts at most
// max_concurrency polls at once. What writes the store runs on the
// console's pool.
package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
)

// ModuleID is the console module the manager is served as.
const ModuleID = "cluster"

// MaxTests is how many tests of members run at once; one more is answered
// 429.
const MaxTests = 4

// closeWait bounds how long Close waits for the members' sessions to be
// logged out.
var closeWait = 2 * time.Second

// Store keeps sections beside the config, as admin/store's Store does.
type Store interface {
	Section(name string) []byte
	SetSection(name string, data []byte) error
}

// Options are what a Manager needs.
type Options struct {
	// Engine is the running engine whose connections the calls to
	// members take.
	Engine *fib.Engine
	// Settings is admin.cluster.
	Settings *config.Cluster
	// Store keeps the members.
	Store Store
	// Logf says what users changed and how members' states changed; never
	// a credential.
	Logf func(format string, args ...any)
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// Manager is the manager of a fit's members.
type Manager struct {
	o        Options
	interval time.Duration
	lim      limits
	maxNodes int
	module   *console.Module
	sem      chan struct{} // the polls under way
	tests    chan struct{} // the tests under way
	reads    chan struct{} // the reads of members under way
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	close    sync.Once

	// wmu orders the writes of the document, which run on the console's
	// pool; mu guards what they change, which requests read on workers.
	wmu      sync.Mutex
	mu       sync.Mutex
	doc      document
	readOnly string // why the document cannot be written, or ""
	rev      uint64
	nodes    map[string]*node
	closed   bool

	// link is the members that link out to the manager: the listener
	// they link to, and their links.
	link linkHub
	// pass is the calls the node switcher passes on to members.
	pass passHub
}

// New returns the manager o.Settings turns on, with the members the store
// keeps, and starts polling them. A store's section the manager cannot
// read, or one a newer fit wrote, leaves the manager read-only, which Logf
// says, rather than written over.
func New(o Options) (*Manager, error) {
	switch {
	case o.Settings == nil:
		return nil, errors.New("cluster: admin.cluster is not set")
	case o.Store == nil:
		return nil, errors.New("cluster: admin.cluster needs admin.store")
	case o.Engine == nil:
		return nil, errors.New("cluster: no engine")
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := o.Settings
	m := &Manager{o: o, interval: s.Interval(), lim: limits{timeout: s.CallTimeout(), maxBytes: s.ResponseBytes()},
		maxNodes: s.Nodes(), sem: make(chan struct{}, s.Concurrency()), tests: make(chan struct{}, MaxTests),
		reads: make(chan struct{}, MaxReads), wake: make(chan struct{}, 1), stop: make(chan struct{}),
		done: make(chan struct{}), nodes: map[string]*node{}}
	doc, newer, err := readDocument(o.Store.Section(Section))
	switch {
	case err != nil:
		m.readOnly = err.Error() + "; it is left as it is, and no member can be added until it is fixed"
		o.Logf("cluster: %s", m.readOnly)
	case newer:
		m.readOnly = fmt.Sprintf("the store's members were written by a newer fit (document version %d; this fit writes %d): they are shown, and changed only by that fit", doc.Version, DocVersion)
		o.Logf("cluster: %s", m.readOnly)
	}
	m.doc = doc
	for _, mem := range doc.Members {
		if err := mem.check(); err != nil {
			o.Logf("cluster: the member %q kept in the store is not polled: %v", mem.Name, err)
			continue
		}
		m.nodes[mem.Name] = newNode(m, mem.clone())
	}
	m.module = &console.Module{ID: ModuleID, Routes: m.routes, Prefixes: []string{PassPrefix}}
	if err := m.startLinks(); err != nil {
		for _, n := range m.nodes {
			n.t.close()
		}
		return nil, err
	}
	go m.run()
	return m, nil
}

// Module returns the console module the manager is served as.
func (m *Manager) Module() *console.Module { return m.module }

func (m *Manager) now() time.Time { return m.o.Now() }

func (m *Manager) logf(format string, args ...any) { m.o.Logf(format, args...) }

// Close stops the poller, logs the members' consoles out, waiting for that
// a little, and closes their connections. Call it before the engine's.
func (m *Manager) Close() {
	m.close.Do(func() {
		close(m.stop)
		<-m.done
		m.mu.Lock()
		m.closed = true
		nodes := slices.Collect(maps.Values(m.nodes))
		m.nodes = map[string]*node{}
		m.mu.Unlock()
		m.stopLinks()
		var wg sync.WaitGroup
		for _, n := range nodes {
			wg.Add(1)
			n.shutdown(wg.Done)
		}
		ended := make(chan struct{})
		go func() { wg.Wait(); close(ended) }()
		select {
		case <-ended:
		case <-time.After(closeWait):
			for _, n := range nodes {
				n.t.close()
			}
		}
	})
}

// run polls the members, each every interval, at most cap(m.sem) at once,
// until the manager stops.
func (m *Manager) run() {
	defer close(m.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-timer.C:
		case <-m.wake:
		}
		wait, ok := m.pollDue()
		if !ok {
			return
		}
		timer.Reset(wait)
	}
}

// pollDue starts a poll of each member due, and returns how long until the
// next one is; false once the manager stops.
func (m *Manager) pollDue() (time.Duration, bool) {
	m.mu.Lock()
	nodes := slices.Collect(maps.Values(m.nodes))
	m.mu.Unlock()
	wait := m.interval
	for _, n := range nodes {
		now := m.now()
		if d := time.Duration(n.next.Load() - now.UnixNano()); d > 0 {
			wait = min(wait, d)
			continue
		}
		if n.closed.Load() || !n.busy.CompareAndSwap(false, true) {
			continue
		}
		select {
		case m.sem <- struct{}{}:
		case <-m.stop:
			n.busy.Store(false)
			return 0, false
		}
		n.next.Store(m.now().Add(m.interval).UnixNano())
		n.poll(func() {
			n.busy.Store(false)
			<-m.sem
			if m.now().UnixNano() >= n.next.Load() {
				m.poke()
			}
		})
	}
	return wait, true
}

// poke has the poller look for members due now.
func (m *Manager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// sync makes the nodes those of the document's members: a member whose
// link is unchanged keeps its node, its session and its state; one changed
// or removed has its node shut down. m.mu must be held.
func (m *Manager) sync() {
	keep := map[string]*node{}
	for _, mem := range m.doc.Members {
		if n := m.nodes[mem.Name]; n != nil && sameLink(&n.spec, &mem) {
			// Its spec stays as the poller reads it: only the labels
			// differ, which the state takes from the document.
			keep[mem.Name] = n
			delete(m.nodes, mem.Name)
			continue
		}
		keep[mem.Name] = newNode(m, mem.clone())
	}
	for _, n := range m.nodes {
		n.shutdown(func() {})
	}
	m.nodes = keep
	m.syncLinks()
	m.poke()
}

// Members returns the members as the API shows them, by name.
func (m *Manager) Members() []MemberView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MemberView, 0, len(m.doc.Members))
	for i := range m.doc.Members {
		out = append(out, m.doc.Members[i].View())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// States returns every member's state, by name.
func (m *Manager) States() []State {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]State, 0, len(m.doc.Members))
	for i := range m.doc.Members {
		out = append(out, m.stateLocked(&m.doc.Members[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// stateLocked returns mem's state; m.mu is held.
func (m *Manager) stateLocked(mem *Member) State {
	if n := m.nodes[mem.Name]; n != nil {
		st := n.snapshot(mem)
		if mem.Access == AccessLink {
			st.Link = m.linkStateOf(mem)
		}
		return st
	}
	// A member kept in the store that does not check is never polled.
	v := mem.View()
	return State{Name: mem.Name, Labels: v.Labels, URL: mem.URL, Access: mem.Access, Status: StatusError,
		Error: &Problem{Kind: KindBadAnswer, Message: "the member kept in the store is not valid: edit it"}}
}

// errReadOnly is a write refused: the manager's document cannot be
// written.
type errReadOnly string

func (e errReadOnly) Error() string { return string(e) }

// change runs fn on a copy of the document, and writes it to the store with
// the audit entry fn returns, then uses it. It writes a file: on the
// console's pool.
func (m *Manager) change(user string, fn func(d *document) (AuditEntry, error)) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	m.mu.Lock()
	if m.readOnly != "" {
		m.mu.Unlock()
		return errReadOnly(m.readOnly)
	}
	d := m.doc
	d.Members = slices.Clone(d.Members)
	for i := range d.Members {
		d.Members[i] = d.Members[i].clone()
	}
	d.Audit = slices.Clone(d.Audit)
	m.mu.Unlock()
	entry, err := fn(&d)
	if err != nil {
		return err
	}
	entry.At, entry.User = m.now().UTC(), user
	d.Audit = append(d.Audit, entry)
	if n := len(d.Audit) - MaxAudit; n > 0 {
		d.Audit = slices.Delete(d.Audit, 0, n)
	}
	d.Version = DocVersion
	if err := m.o.Store.SetSection(Section, d.marshal()); err != nil {
		return fmt.Errorf("saving the members: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.doc = d
	m.rev++
	if !m.closed {
		m.sync()
	}
	return nil
}

// errNotFound is a member there is none of.
type errNotFound string

func (e errNotFound) Error() string { return "no member is named " + strconv.Quote(string(e)) }

// errConflict is a change refused for what the document already holds.
type errConflict string

func (e errConflict) Error() string { return string(e) }

func index(d *document, name string) int {
	return slices.IndexFunc(d.Members, func(m Member) bool { return m.Name == name })
}

// Add adds the member in, as user.
func (m *Manager) Add(user string, in *Input) (MemberView, error) {
	var out MemberView
	err := m.change(user, func(d *document) (AuditEntry, error) {
		mem, err := in.member(nil, m.now().UTC())
		if err != nil {
			return AuditEntry{}, err
		}
		if index(d, mem.Name) >= 0 {
			return AuditEntry{}, errConflict("a member is already named " + strconv.Quote(mem.Name))
		}
		if len(d.Members) >= m.maxNodes {
			return AuditEntry{}, errConflict(fmt.Sprintf("the manager keeps %d members at most (max_nodes)", m.maxNodes))
		}
		d.Members = append(d.Members, mem)
		out = mem.View()
		return AuditEntry{Action: ActionAdd, Node: mem.Name, Detail: describe(&mem)}, nil
	})
	if err == nil {
		m.logf("cluster: %q added the member %q: %s", user, out.Name, describeView(&out))
	}
	return out, err
}

// Update replaces the member name with in, which may rename it, as user: a
// password or token in leaves empty keeps the one kept.
func (m *Manager) Update(user, name string, in *Input) (MemberView, error) {
	var out MemberView
	err := m.change(user, func(d *document) (AuditEntry, error) {
		i := index(d, name)
		if i < 0 {
			return AuditEntry{}, errNotFound(name)
		}
		if in.Name == "" {
			in.Name = name
		}
		mem, err := in.member(&d.Members[i], m.now().UTC())
		if err != nil {
			return AuditEntry{}, err
		}
		if mem.Name != name && index(d, mem.Name) >= 0 {
			return AuditEntry{}, errConflict("a member is already named " + strconv.Quote(mem.Name))
		}
		changed := changes(&d.Members[i], &mem)
		d.Members[i] = mem
		out = mem.View()
		return AuditEntry{Action: ActionUpdate, Node: name, Detail: changed}, nil
	})
	if err == nil {
		m.logf("cluster: %q changed the member %q: %s", user, name, describeView(&out))
	}
	return out, err
}

// Remove removes the member name, as user.
func (m *Manager) Remove(user, name string) error {
	err := m.change(user, func(d *document) (AuditEntry, error) {
		i := index(d, name)
		if i < 0 {
			return AuditEntry{}, errNotFound(name)
		}
		d.Members = slices.Delete(d.Members, i, i+1)
		return AuditEntry{Action: ActionRemove, Node: name}, nil
	})
	if err == nil {
		m.logf("cluster: %q removed the member %q", user, name)
	}
	return err
}

// Audit returns the audit log, oldest first.
func (m *Manager) Audit() []AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AuditEntry{}, m.doc.Audit...)
}

// describe says what a member is, without its credentials.
func describe(m *Member) string {
	v := m.View()
	return describeView(&v)
}

func describeView(v *MemberView) string {
	if v.Access == AccessLink {
		return "linking out to the manager, by enrollment"
	}
	s := v.URL + " by " + v.Access + ", certificate by " + v.TLS.Verified()
	if v.Access == AccessConsole {
		s += ", as " + strconv.Quote(v.Username)
	}
	return s
}

// changes names the fields that changed from a to b; credentials by name
// only.
func changes(a, b *Member) string {
	var out []string
	add := func(changed bool, name string) {
		if changed {
			out = append(out, name)
		}
	}
	add(a.Name != b.Name, "name")
	add(!maps.Equal(a.Labels, b.Labels), "labels")
	add(a.URL != b.URL, "url")
	add(a.Access != b.Access, "access")
	add(a.Username != b.Username, "username")
	add(a.Password != b.Password, "password")
	add(a.Token != b.Token, "token")
	add(a.TLS.CA != b.TLS.CA, "tls.ca")
	add(a.TLS.Fingerprint != b.TLS.Fingerprint, "tls.fingerprint")
	add(a.credentialID() != b.credentialID(), "link.credential")
	if len(out) == 0 {
		return "nothing"
	}
	return strings.Join(out, ", ")
}

// marshalMembers is the members' list as GET /api/nodes answers it.
type membersBody struct {
	Version  string       `json:"version"`
	MaxNodes int          `json:"max_nodes"`
	ReadOnly string       `json:"read_only,omitempty"`
	Nodes    []MemberView `json:"nodes"`
}

// stateBody is GET /api/state's answer.
type stateBody struct {
	IntervalMs int64   `json:"interval_ms"`
	TimeoutMs  int64   `json:"timeout_ms"`
	ReadOnly   string  `json:"read_only,omitempty"`
	Nodes      []State `json:"nodes"`
}

// nodeBody is GET /api/nodes/<name>'s answer: the member and its state.
type nodeBody struct {
	MemberView
	State State `json:"state"`
}

func (m *Manager) etag() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strconv.Quote(strconv.FormatUint(m.rev, 10))
}

func (m *Manager) routes(api *console.API) {
	api.Handle("GET", "/api/nodes", m.list)
	api.Handle("POST", "/api/nodes", m.add)
	api.HandlePrefix("", "/api/nodes/", m.nodeRoute)
	api.Handle("POST", "/api/test", m.testDraft)
	api.Handle("GET", "/api/state", m.state)
	api.Handle("GET", "/api/audit", func(r *console.Request) { _ = r.JSON(200, map[string]any{"entries": m.Audit()}) })
	m.linkRoutes(api)
	m.passRoutes(api)
}

func (m *Manager) list(r *console.Request) {
	m.mu.Lock()
	ro, maxNodes := m.readOnly, m.maxNodes
	m.mu.Unlock()
	tag := m.etag()
	r.Header().Set("ETag", tag)
	_ = r.JSON(200, membersBody{Version: strings.Trim(tag, `"`), MaxNodes: maxNodes, ReadOnly: ro, Nodes: m.Members()})
}

func (m *Manager) state(r *console.Request) {
	m.mu.Lock()
	ro := m.readOnly
	m.mu.Unlock()
	_ = r.JSON(200, stateBody{IntervalMs: m.interval.Milliseconds(), TimeoutMs: m.lim.timeout.Milliseconds(), ReadOnly: ro, Nodes: m.States()})
}

// decode decodes a member from the request's body, bounded.
func decode(r *console.Request, in *Input) bool {
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a member is %d bytes at most", MaxBodyBytes))
		return false
	}
	return r.Decode(in)
}

// ifMatch reports whether the request's If-Match, when it sends one, is the
// members' ETag; it answers 412 when not.
func (m *Manager) ifMatch(r *console.Request) bool {
	want := r.Request.Header.Get("If-Match")
	if want == "" || want == "*" || want == m.etag() {
		return true
	}
	_ = console.Error(r.Context, 412, "the members changed since the page read them; read them again")
	return false
}

// answer answers a change: its result, or its error at its status.
func (m *Manager) answer(r *console.Request, status int, v any, err error) {
	var fe *FieldError
	var nf errNotFound
	var cf errConflict
	var ro errReadOnly
	switch {
	case err == nil:
		r.Header().Set("ETag", m.etag())
		_ = r.JSON(status, v)
	case errors.As(err, &fe):
		_ = r.JSON(400, fe)
	case errors.As(err, &nf):
		_ = console.Error(r.Context, 404, err.Error())
	case errors.As(err, &cf), errors.As(err, &ro):
		_ = console.Error(r.Context, 409, err.Error())
	default:
		_ = console.Error(r.Context, 500, err.Error())
	}
}

func (m *Manager) add(r *console.Request) {
	var in Input
	if !decode(r, &in) || !m.ifMatch(r) {
		return
	}
	user := r.User
	r.Go(func() {
		v, err := m.Add(user, &in)
		m.answer(r, 201, v, err)
	})
}

// nodeRoute answers /api/nodes/<name>, /api/nodes/<name>/test and
// /api/nodes/<name>/read/<what>.
func (m *Manager) nodeRoute(r *console.Request) {
	rest := strings.TrimPrefix(r.Path, "/api/nodes/")
	name, sub, _ := strings.Cut(rest, "/")
	method := r.Request.Method
	switch {
	case sub == "test" && method == "POST":
		m.testSaved(r, name)
	case sub == "enroll" && method == "POST":
		m.enrollAgain(r, name)
	case strings.HasPrefix(sub, "read/"):
		m.read(r, name, strings.TrimPrefix(sub, "read/"))
	case sub != "":
		_ = console.Error(r.Context, 404, "no such endpoint")
	case method == "GET":
		m.mu.Lock()
		i := index(&m.doc, name)
		var body nodeBody
		if i >= 0 {
			body = nodeBody{MemberView: m.doc.Members[i].View(), State: m.stateLocked(&m.doc.Members[i])}
		}
		m.mu.Unlock()
		if i < 0 {
			_ = console.Error(r.Context, 404, errNotFound(name).Error())
			return
		}
		_ = r.JSON(200, body)
	case method == "PUT":
		var in Input
		if !decode(r, &in) || !m.ifMatch(r) {
			return
		}
		user := r.User
		r.Go(func() {
			v, err := m.Update(user, name, &in)
			m.answer(r, 200, v, err)
		})
	case method == "DELETE":
		if !m.ifMatch(r) {
			return
		}
		user := r.User
		r.Go(func() {
			err := m.Remove(user, name)
			m.answer(r, 200, map[string]string{"removed": name}, err)
		})
	default:
		r.Header().Set("Allow", "GET, PUT, DELETE")
		_ = console.Error(r.Context, 405, "the method is not allowed")
	}
}

// unmarshalInput reads a member's draft, an Input, from data; empty data is
// none.
func unmarshalInput(data []byte) (*Input, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var in Input
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, err
	}
	return &in, nil
}
