package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	stdhttp "net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/schema"
)

// The node switcher's pass-through (I143). The workbench, with a member
// chosen in its header, draws its own pages, from the manager's bundle,
// against that member: each call a page makes to /api/m/<module>/<path> goes
// to /api/n/<member>/m/<module>/<path> on the manager's console instead, and
// the manager passes it on to the member as it reaches it (its console with
// the session it keeps, its Admin API with its token, or its link), and its
// answer back. The browser never holds the member's credentials: they are
// the manager's, added on the way, and the browser's own (the manager's
// session cookie and CSRF token) stop at the manager, whose console checks
// them as it checks every request. Only the request headers a page needs go
// on, and only the answer headers it reads come back, so that a member can
// set no cookie and no header on the manager's origin.
//
// What a page changes on a member is a write: a request that is not a GET
// or a HEAD, but for the text conversions (POST /config/render and
// /config/parse) and dry runs (?dry_run=1), which change nothing. A write is
// refused while the member's config schema is not the manager's own, as
// read by the latest poll (its pages are then read-only), and while the
// manager's members cannot be written; otherwise it is recorded in the
// manager's audit log, with its user, its method and path and how the
// member answered.

// PassPrefix is where the manager's console passes calls on to a member:
// /api/n/<member>/workbench, /api/n/<member>/node and
// /api/n/<member>/m/<module>/<path>.
const PassPrefix = "/api/n/"

// The bounds of the calls passed on.
const (
	// MaxPassCalls is how many calls passed on are under way or waiting at
	// once, to every member; one more is answered 429.
	MaxPassCalls = 64
	// MaxPassPerNode is how many go to one member at once; the others wait
	// their turn, at most MaxPassWaiting of them, each for the call
	// timeout at most.
	MaxPassPerNode = 3
	MaxPassWaiting = 16
	// MaxPassAudit is how many writes may wait for their audit entries to
	// be written to the store; one more is answered 429 before it is
	// sent.
	MaxPassAudit = 64
)

// ActionAPI is a write passed on to a member through the node switcher.
const ActionAPI = "node.api"

// CapSchema is the capability of a member's GET /schema.
const CapSchema = "schema"

// SchemaFromHeader marks a schema the manager answered for a member that
// serves none: its own, which its pages show read-only.
const SchemaFromHeader = "Fit-Schema-From"

// passHeaders are the request headers passed on: what the pages send. The
// member's credentials are the manager's.
var passHeaders = []string{"Content-Type", "If-Match", "If-None-Match", "Upload-Offset", "Upload-Length", "Fit-Store-Password"}

// answerHeaders are the answer headers passed back: what the pages read.
// Content-Type and Content-Disposition are checked apart.
var answerHeaders = []string{"Etag", "Fit-Config-Version", "Fit-Dry-Run", "Retry-After", "Allow", "Upload-Offset", "Upload-Length"}

// answerTypes are the media types an answer keeps; any other is answered as
// application/octet-stream, which no browser renders on the manager's
// origin.
var answerTypes = []string{"application/json", "application/schema+json", "text/plain", "application/octet-stream", "application/yaml", "text/yaml"}

// notPassed are the console modules never opened through the switcher: the
// member's own management (cluster), its assistant (ai), and the LLM roles,
// whose pages read through POST RPCs and streams.
func notPassed(module string) bool {
	return module == ModuleID || module == "ai" || strings.HasPrefix(module, "llm.")
}

// ownSchema is the manager's config schema and its digest, as GET /node
// answers it.
var ownSchema = sync.OnceValues(func() ([]byte, string) {
	data, err := schema.Generate()
	if err != nil {
		return nil, ""
	}
	sum := sha256.Sum256(data)
	return data, "sha256:" + hex.EncodeToString(sum[:])
})

// passHub is the calls passed on: those under way and waiting, by node, and
// the audit entries of writes waiting to be written.
type passHub struct {
	mu     sync.Mutex
	total  int
	queues map[*node]*passQueue
	// audit is the entries waiting to be written, held is how many writes
	// are under way or waiting for theirs, writing is a writer running.
	audit   []AuditEntry
	held    int
	writing bool
	// digest, when set, is the manager's schema digest in place of its
	// own: tests'.
	digest string
}

// passQueue is a node's calls: how many run, and those waiting.
type passQueue struct {
	running int
	waiting []*passWait
}

type passWait struct {
	at    time.Time
	run   func()
	stale func()
}

// errPassBusy is a call refused for the bounds.
type errPassBusy string

func (e errPassBusy) Error() string { return string(e) }

// passRoutes adds the pass-through to the module's routes: the console
// serves it at PassPrefix, Module.Prefixes says.
func (m *Manager) passRoutes(api *console.API) { api.HandlePrefix("", PassPrefix, m.passOn) }

// ownDigest is the manager's schema digest.
func (m *Manager) ownDigest() string {
	m.pass.mu.Lock()
	d := m.pass.digest
	m.pass.mu.Unlock()
	if d != "" {
		return d
	}
	_, d = ownSchema()
	return d
}

// Why a member's pages are read-only, passMember.ReasonKind.
const (
	// ReadOnlyNotRead: the member has not been read yet.
	ReadOnlyNotRead = "not_read"
	// ReadOnlyVersion: its config schema is not the manager's.
	ReadOnlyVersion = "version"
	// ReadOnlyManager: the manager's members cannot be written, so a
	// write could not be recorded.
	ReadOnlyManager = "manager"
)

// compatible returns why the pages may not write to a member whose schema
// digest is digest, its kind and its message, or "" when they may.
func (m *Manager) compatible(digest string) (string, string) {
	own := m.ownDigest()
	switch {
	case digest == "":
		return ReadOnlyNotRead, "the member has not been read yet, so its version is not known: its pages are read-only until it is"
	case digest != own:
		return ReadOnlyVersion, fmt.Sprintf("the member's config schema (%s) is not this manager's (%s): it runs another version of fit, whose pages this manager's do not know, so they are read-only",
			shortDigest(digest), shortDigest(own))
	}
	return "", ""
}

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19] + "…"
	}
	return d
}

// passRoute is where a call to /api/n/<member>/<sub> goes.
type passRoute struct {
	// what is "workbench", "node", "admin" (the Admin API's path) or
	// "module" (a role's console module, at its console path).
	what   string
	path   string // the Admin API's path, or the console's, escaped
	module string
	shown  string // the path as the audit log shows it, without its query
}

// routeOf returns where sub, escaped, goes, or the status and message of
// why it goes nowhere.
func routeOf(sub string) (passRoute, int, string) {
	for _, seg := range strings.Split(sub, "/") {
		s, err := url.PathUnescape(seg)
		if err != nil || s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\\x00") {
			return passRoute{}, 404, "no such path"
		}
	}
	switch {
	case sub == "workbench":
		return passRoute{what: "workbench"}, 0, ""
	case sub == "node":
		return passRoute{what: "node", path: "/node", module: "admin", shown: "/node"}, 0, ""
	}
	rest, ok := strings.CutPrefix(sub, "m/")
	if !ok {
		return passRoute{}, 404, "no such path: a member's API is passed on at m/<module>/<path>"
	}
	mod, p, _ := strings.Cut(rest, "/")
	switch {
	case p == "":
		return passRoute{}, 404, "no such path"
	case mod == "admin":
		head, _, _ := strings.Cut(p, "/")
		switch head {
		case "users", "account":
			return passRoute{}, 403, "a member's console users and passwords are changed on the member, not through the manager"
		case "debug", "dashboard":
			return passRoute{}, 404, "no such path"
		}
		return passRoute{what: "admin", path: "/" + p, module: mod, shown: "/" + p}, 0, ""
	case notPassed(mod):
		return passRoute{}, 403, "the member's module " + strconv.Quote(mod) + " is not opened through the manager: open the member's own console for it"
	}
	return passRoute{what: "module", path: "/api/m/" + mod + "/" + p, module: mod, shown: "/api/m/" + mod + "/" + p}, 0, ""
}

// writes reports whether a call changes the member: anything but a GET or a
// HEAD, the text conversions and a dry run.
func writes(method string, rt passRoute, query string) bool {
	if method == stdhttp.MethodGet || method == stdhttp.MethodHead {
		return false
	}
	if rt.what == "admin" && method == stdhttp.MethodPost && (rt.path == "/config/render" || rt.path == "/config/parse") {
		return false
	}
	if rt.what == "admin" {
		q, _ := url.ParseQuery(query)
		if d := q.Get("dry_run"); d == "1" || d == "true" {
			return false
		}
	}
	return true
}

// passOn answers /api/n/<member>/...: the call passed on to the member.
func (m *Manager) passOn(r *console.Request) {
	escaped := r.Request.URL.EscapedPath()
	rest, ok := strings.CutPrefix(escaped, PassPrefix)
	if !ok {
		// The module's own path, /api/m/cluster/n/...: only PassPrefix
		// is the pass-through's.
		_ = console.Error(r.Context, 404, "no such endpoint")
		return
	}
	name, sub, _ := strings.Cut(rest, "/")
	rt, status, msg := routeOf(sub)
	if status != 0 {
		_ = console.Error(r.Context, status, msg)
		return
	}
	method := r.Request.Method
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
	default:
		r.Header().Set("Allow", "GET, HEAD, POST, PUT, PATCH, DELETE")
		_ = console.Error(r.Context, 405, "the method is not allowed")
		return
	}
	if rt.what == "workbench" && method != "GET" {
		r.Header().Set("Allow", "GET")
		_ = console.Error(r.Context, 405, "the method is not allowed")
		return
	}
	if strings.Contains(r.Request.Header.Get("Accept"), "text/event-stream") {
		_ = console.Error(r.Context, 501, "streams are not passed on to members: open the member's own console for this page")
		return
	}
	data := r.Context.Body()
	if int64(len(data)) > m.lim.maxBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a request passed on to a member is %d bytes at most (max_response_bytes)", m.lim.maxBytes))
		return
	}
	m.mu.Lock()
	n, ro := m.nodes[name], m.readOnly
	m.mu.Unlock()
	if n == nil {
		_ = console.Error(r.Context, 404, errNotFound(name).Error())
		return
	}
	if rt.what == "module" && n.spec.Access != AccessConsole {
		_ = console.Error(r.Context, 404, "the member's role pages are reached through its console: the manager reaches it by "+n.spec.Access)
		return
	}
	n.mu.Lock()
	var digest string
	var caps []string
	if nd := n.state.Node; nd != nil {
		digest, caps = nd.SchemaDigest, nd.Capabilities
	}
	n.mu.Unlock()
	// A member that serves no schema, older than the switcher, is shown
	// with the manager's, and its pages are read-only.
	if rt.what == "admin" && rt.path == "/schema" && method == "GET" && n.spec.Access != AccessConsole && caps != nil && !slices.Contains(caps, CapSchema) {
		data, _ := ownSchema()
		r.Header().Set(SchemaFromHeader, "manager")
		_ = console.Write(r.Context, 200, "application/schema+json", data)
		return
	}
	write := writes(method, rt, r.Request.URL.RawQuery)
	if write {
		if ro != "" {
			_ = console.Error(r.Context, 409, "the manager's members cannot be written ("+ro+"), so a change through it could not be recorded")
			return
		}
		if kind, why := m.compatible(digest); kind != "" {
			_ = console.Error(r.Context, 409, why)
			return
		}
	}
	var body []byte
	if len(data) > 0 || (method != "GET" && method != "HEAD") {
		body = bytes.Clone(data)
	}
	header := stdhttp.Header{}
	for _, k := range passHeaders {
		if v := r.Request.Header.Values(k); len(v) > 0 {
			header[k] = slices.Clone(v)
		}
	}
	path := rt.path
	switch {
	case rt.what == "node":
		path = n.p.node
	case rt.what == "admin" && n.spec.Access == AccessConsole:
		path = "/api/m/admin" + path
	}
	if q := r.Request.URL.RawQuery; q != "" && rt.what != "workbench" && rt.what != "node" {
		path += "?" + q
	}
	if write && !m.holdAudit() {
		_ = console.Error(r.Context, 429, "other changes to members are still being recorded; try again")
		return
	}
	user := r.User
	r.Context.Retain()
	run := func(release func()) {
		if rt.what == "workbench" {
			m.passWorkbench(r, n, user, release)
			return
		}
		n.call(method, path, header, body, func(rep *reply, s *session, prob *Problem) {
			defer release()
			defer r.Context.Release()
			if prob == nil {
				prob = n.passProblem(rep, s)
			}
			if write {
				m.audited(user, n.spec.Name, method, rt.shown, rep, prob)
			}
			if prob != nil {
				if prob.At.IsZero() {
					prob.At = m.now().UTC()
				}
				prob.Message = n.redact(prob.Message)
				_ = r.JSON(502, readFailure{Error: prob.Message, Problem: prob})
				return
			}
			passAnswer(r, rep)
		})
	}
	stale := func() {
		defer r.Context.Release()
		if write {
			m.unholdAudit()
		}
		_ = console.Error(r.Context, 503, "the member is busy with other calls from the manager; try again")
	}
	if err := m.enqueue(n, run, stale); err != nil {
		if write {
			m.unholdAudit()
		}
		_ = console.Error(r.Context, 429, err.Error())
		r.Context.Release()
	}
}

// passProblem returns what an answer of the member means when it is no
// answer to pass on: 401, the credentials refused (logged in again once
// already), and 403 asking for a temporary password to be changed. A page
// would take either for the manager's own session.
func (n *node) passProblem(r *reply, s *session) *Problem {
	var body struct {
		Error      string `json:"error"`
		MustChange bool   `json:"must_change_password"`
	}
	switch r.status {
	case stdhttp.StatusUnauthorized:
		_ = json.Unmarshal(r.body, &body)
		return &Problem{Kind: KindAuth, Status: r.status, Message: "the member refused the manager's credentials: " + body.Error}
	case stdhttp.StatusForbidden:
		if json.Unmarshal(r.body, &body) == nil && body.MustChange {
			n.dropSession(s)
			return &Problem{Kind: KindPasswordChange, Status: r.status,
				Message: "the member's console asks for the user's temporary password to be changed; change it on the member, then on the Nodes page"}
		}
	}
	return nil
}

// passAnswer answers r with the member's answer, its headers but those the
// pages read left out, and its body as it came.
func passAnswer(r *console.Request, rep *reply) {
	h := r.Context.Header()
	for _, k := range answerHeaders {
		if v := rep.header.Values(k); len(v) > 0 {
			h[stdhttp.CanonicalHeaderKey(k)] = slices.Clone(v)
		}
	}
	if d := rep.header.Get("Content-Disposition"); strings.HasPrefix(d, "attachment") && len(d) <= 512 {
		h.Set("Content-Disposition", d)
	}
	ct := rep.header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); err != nil || !slices.Contains(answerTypes, mt) {
		ct = "application/octet-stream"
		if len(rep.body) == 0 {
			ct = ""
		}
	}
	_ = console.Write(r.Context, rep.status, ct, rep.body)
}

// passWorkbench answers GET /api/n/<member>/workbench: the member's modules
// the switcher opens, and the member as the pages show it, whose /node is
// read now. It calls release once it has answered.
func (m *Manager) passWorkbench(r *console.Request, n *node, user string, release func()) {
	answer := func(status int, v any) {
		defer release()
		defer r.Context.Release()
		_ = r.JSON(status, v)
	}
	fail := func(prob *Problem) {
		if prob.At.IsZero() {
			prob.At = m.now().UTC()
		}
		prob.Message = n.redact(prob.Message)
		answer(502, readFailure{Error: prob.Message, Problem: prob})
	}
	n.call("GET", n.p.node, nil, nil, func(rep *reply, s *session, prob *Problem) {
		if prob == nil {
			prob = n.answerProblem(rep, n.p.node, s)
		}
		if prob != nil {
			fail(prob)
			return
		}
		var nd NodeInfo
		if err := json.Unmarshal(rep.body, &nd); err != nil {
			fail(&Problem{Kind: KindBadAnswer, Message: "the member's /node is not the JSON expected: " + err.Error()})
			return
		}
		n.mu.Lock()
		polled := ""
		if n.state.Node != nil {
			polled = n.state.Node.SchemaDigest
		}
		n.mu.Unlock()
		if polled != nd.SchemaDigest {
			// The member changed since the poll: read it now, which
			// the writes go by.
			n.next.Store(0)
			m.poke()
		}
		kind, why := m.compatible(nd.SchemaDigest)
		if kind == "" {
			kind, why = m.compatible(polled)
		}
		m.mu.Lock()
		if ro := m.readOnly; ro != "" && kind == "" {
			kind, why = ReadOnlyManager, "the manager's members cannot be written ("+ro+"), so a change through it could not be recorded"
		}
		m.mu.Unlock()
		mem := passMember{Name: n.spec.Name, Access: n.spec.Access, URL: n.spec.URL, ID: nd.ID, Hostname: nd.Hostname,
			FitVersion: nd.FitVersion, SchemaDigest: nd.SchemaDigest, ManagerSchemaDigest: m.ownDigest(), Roles: nonNil(nd.Roles),
			ReadOnly: kind != "", ReasonKind: kind, Reason: why}
		if n.spec.Access != AccessConsole {
			answer(200, passWorkbenchBody{User: user, Modules: []string{"admin"}, Member: mem})
			return
		}
		n.call("GET", "/api/workbench", nil, nil, func(rep *reply, s *session, prob *Problem) {
			if prob == nil {
				prob = n.answerProblem(rep, "/api/workbench", s)
			}
			if prob != nil {
				fail(prob)
				return
			}
			var wb struct {
				Modules []string `json:"modules"`
			}
			if err := json.Unmarshal(rep.body, &wb); err != nil {
				fail(&Problem{Kind: KindBadAnswer, Message: "the member's /api/workbench is not the JSON expected: " + err.Error()})
				return
			}
			mods := []string{}
			for _, id := range wb.Modules {
				if !notPassed(id) && len(mods) < 64 {
					mods = append(mods, id)
				}
			}
			answer(200, passWorkbenchBody{User: user, Modules: mods, Member: mem})
		})
	})
}

// passWorkbenchBody is GET /api/n/<member>/workbench's answer: as the
// console's GET /api/workbench, with the member.
type passWorkbenchBody struct {
	User    string     `json:"user"`
	Modules []string   `json:"modules"`
	Member  passMember `json:"member"`
}

// passMember is the member the switcher opened, as its pages show it.
type passMember struct {
	Name         string   `json:"name"`
	Access       string   `json:"access"`
	URL          string   `json:"url,omitempty"`
	ID           string   `json:"id"`
	Hostname     string   `json:"hostname"`
	FitVersion   string   `json:"fit_version"`
	SchemaDigest string   `json:"schema_digest"`
	Roles        []string `json:"roles"`
	// ManagerSchemaDigest is the manager's own, which a member's pages
	// need for them to be written.
	ManagerSchemaDigest string `json:"manager_schema_digest"`
	ReadOnly            bool   `json:"read_only"`
	// ReasonKind is why it is read-only: ReadOnlyNotRead,
	// ReadOnlyVersion or ReadOnlyManager; Reason says it.
	ReasonKind string `json:"reason_kind,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// enqueue runs run when n has a turn, at once or after the calls before it,
// and bounds the calls: it returns why when there are too many. run is
// handed the release of its turn, which it calls once. A call that waited
// longer than the call timeout is not run: stale is called in its place.
func (m *Manager) enqueue(n *node, run func(release func()), stale func()) error {
	h := &m.pass
	h.mu.Lock()
	if h.total >= MaxPassCalls {
		h.mu.Unlock()
		return errPassBusy("other calls to members are under way; try again")
	}
	if h.queues == nil {
		h.queues = map[*node]*passQueue{}
	}
	q := h.queues[n]
	if q == nil {
		q = &passQueue{}
		h.queues[n] = q
	}
	if q.running >= MaxPassPerNode && len(q.waiting) >= MaxPassWaiting {
		h.mu.Unlock()
		return errPassBusy("other calls to the member are under way; try again")
	}
	h.total++
	if q.running < MaxPassPerNode {
		q.running++
		h.mu.Unlock()
		run(m.releaser(n))
		return nil
	}
	q.waiting = append(q.waiting, &passWait{at: m.now(), run: func() { run(m.releaser(n)) }, stale: stale})
	h.mu.Unlock()
	return nil
}

// releaser returns the release of a turn of n's: once, it starts the next
// call waiting, if any.
func (m *Manager) releaser(n *node) func() {
	var once sync.Once
	return func() { once.Do(func() { m.next(n) }) }
}

// next ends a turn of n's and gives it to the next call waiting.
func (m *Manager) next(n *node) {
	h := &m.pass
	for {
		h.mu.Lock()
		h.total--
		q := h.queues[n]
		if len(q.waiting) == 0 {
			q.running--
			if q.running == 0 {
				delete(h.queues, n)
			}
			h.mu.Unlock()
			return
		}
		w := q.waiting[0]
		q.waiting = slices.Delete(q.waiting, 0, 1)
		h.mu.Unlock()
		if m.now().Sub(w.at) <= m.lim.timeout {
			w.run()
			return
		}
		// It waited too long: its turn goes on to the next.
		w.stale()
	}
}

// holdAudit takes a place for a write's audit entry, false when
// MaxPassAudit writes wait for theirs.
func (m *Manager) holdAudit() bool {
	h := &m.pass
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held >= MaxPassAudit {
		return false
	}
	h.held++
	return true
}

// unholdAudit gives back the place of a write that was not sent.
func (m *Manager) unholdAudit() {
	h := &m.pass
	h.mu.Lock()
	h.held--
	h.mu.Unlock()
}

// audited records a write passed on to the member name: in the log at once,
// and in the audit log of the store on a goroutine of its own, which writes
// a file.
func (m *Manager) audited(user, name, method, path string, rep *reply, prob *Problem) {
	var outcome string
	switch {
	case prob != nil && rep == nil && (prob.Kind == KindAuth || prob.Kind == KindRateLimited || prob.Kind == KindPasswordChange):
		// The console's login failed: the write was not sent.
		outcome = "not sent (" + prob.Kind + "): " + prob.Message
	case prob != nil && rep == nil:
		outcome = "not answered (" + prob.Kind + "), so it may or may not have been applied: " + prob.Message
	case prob != nil:
		outcome = strconv.Itoa(rep.status) + ", " + prob.Kind
	default:
		outcome = strconv.Itoa(rep.status)
		if v := rep.header.Get("Fit-Config-Version"); v != "" && rep.status < 300 {
			outcome += ", config version " + v
		}
	}
	detail := boundString(method+" "+path+": "+outcome, maxMessage)
	m.logf("cluster: %q, through the node switcher, on the member %q: %s", user, name, detail)
	e := AuditEntry{At: m.now().UTC(), User: user, Action: ActionAPI, Node: name, Detail: detail}
	h := &m.pass
	h.mu.Lock()
	h.audit = append(h.audit, e)
	start := !h.writing
	h.writing = true
	h.mu.Unlock()
	if start {
		go m.writeAudit()
	}
}

// writeAudit writes the audit entries waiting, until there are none.
func (m *Manager) writeAudit() {
	h := &m.pass
	for {
		h.mu.Lock()
		batch := h.audit
		h.audit = nil
		if len(batch) == 0 {
			h.writing = false
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		if err := m.appendAudit(batch); err != nil {
			m.logf("cluster: %d entries of the audit log were not kept: %v", len(batch), err)
		}
		h.mu.Lock()
		h.held -= len(batch)
		h.mu.Unlock()
	}
}

// appendAudit adds entries to the audit log in the store. Unlike change, it
// leaves the members' ETag as it is: a page's edit of the members is not
// refused for a write through the switcher.
func (m *Manager) appendAudit(entries []AuditEntry) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	m.mu.Lock()
	if m.readOnly != "" {
		m.mu.Unlock()
		return errReadOnly(m.readOnly)
	}
	// Every writer holds wmu and replaces the document, never changing
	// its slices in place: this copy is read alone.
	d := m.doc
	m.mu.Unlock()
	d.Audit = append(slices.Clone(d.Audit), entries...)
	if n := len(d.Audit) - MaxAudit; n > 0 {
		d.Audit = slices.Delete(d.Audit, 0, n)
	}
	d.Version = DocVersion
	if err := m.o.Store.SetSection(Section, d.marshal()); err != nil {
		return fmt.Errorf("saving the members: %w", err)
	}
	m.mu.Lock()
	m.doc = d
	m.mu.Unlock()
	return nil
}
