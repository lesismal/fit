package cluster

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/app/console"
)

// The operations a manager runs on many members at once, OpRequest.Op.
const (
	// OpReload applies each member's running config again (POST /reload),
	// loading again what it loads from files.
	OpReload = "reload"
	// OpRollback applies again each member's previous config: the latest
	// version it keeps whose ETag is not the running one's (POST
	// /rollback/{n}, with If-Match).
	OpRollback = "rollback"
	// OpCheck runs each member's consistency check (GET /config/check).
	OpCheck = "check"
	// OpPurge purges each member's cache zones by key, prefix, tag or all
	// (POST /cache/purge).
	OpPurge = "purge"
	// OpTest reads each member's GET /node, as the manager reaches it.
	OpTest = "test"
)

// The kinds of Problem only operations report.
const (
	// KindRefused is an operation the member answered with an error: a
	// config it did not apply, a zone it has not, a version it keeps not.
	KindRefused = "refused"
	// KindNoEarlier is a rollback with no config to roll back to: the
	// member keeps none before the running one that differs from it.
	KindNoEarlier = "no_earlier_config"
)

// Ops are the operations, in the order the pages offer them.
var Ops = []string{OpReload, OpRollback, OpCheck, OpPurge, OpTest}

// The bounds of operations.
const (
	// MaxRunningOps is how many operations run at once; one more is
	// answered 429.
	MaxRunningOps = 4
	// MaxKeptOps is how many operations the manager keeps, with each
	// member's outcome, the oldest finished dropped first; they are kept
	// in memory, and in the audit log.
	MaxKeptOps = 32
	// MaxPurgeItems bounds each list of a purge, and MaxPurgeItemBytes
	// each item.
	MaxPurgeItems     = 64
	MaxPurgeItemBytes = 2048
	// MaxAuditBytes bounds the audit log's size, about as the store keeps
	// it: past it the oldest entries go, as past MaxAudit entries.
	MaxAuditBytes = 2 << 20
	// maxAuditError bounds a member's error in the audit log.
	maxAuditError = 256
)

// The statuses of an operation's member, Outcome.Status.
const (
	OutcomePending = "pending"
	OutcomeRunning = "running"
	OutcomeDone    = "done"
	OutcomeFailed  = "failed"
)

// The capabilities of a member's GET /node an operation needs, as package
// admin names them.
const (
	capReload      = "config.reload"
	capVersions    = "config.versions"
	capCache       = "cache"
	capCachePrefix = "cache.prefix"
)

// PurgeParams are what a purge removes from each member's cache: in the
// zone Zone, or every zone when it is empty, the responses to Keys (the
// absolute URLs requests name, which are the cache's keys), to URLs that
// start with any of Prefixes, tagged with any of Tags, or, with All,
// everything.
type PurgeParams struct {
	Zone     string   `json:"zone,omitempty"`
	Keys     []string `json:"keys,omitempty"`
	Prefixes []string `json:"prefixes,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	All      bool     `json:"all,omitempty"`
}

// OpRequest is an operation as POST /api/ops takes it: the operation, and
// the members it runs on: the group's, Members (a selection, which must be
// in the group when Group is given too), or both.
type OpRequest struct {
	Op      string       `json:"op"`
	Group   string       `json:"group,omitempty"`
	Members []string     `json:"members,omitempty"`
	Purge   *PurgeParams `json:"purge,omitempty"`
}

// Outcome is how an operation went on one member: pending, running, done
// or failed with its Error; Detail says what it did, and Result holds what
// the member answered of it.
type Outcome struct {
	Node    string    `json:"node"`
	Status  string    `json:"status"`
	Detail  string    `json:"detail,omitempty"`
	Error   *Problem  `json:"error,omitempty"`
	Result  *OpResult `json:"result,omitempty"`
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	TookMs  int64     `json:"took_ms,omitempty"`
}

// OpResult is what a member answered an operation: the version a reload or
// a rollback applied (and, for a rollback, the version it took the config
// from), the differences a check found, how many keys a purge removed, and
// who answered a test.
type OpResult struct {
	Version uint64 `json:"version,omitempty"`
	From    uint64 `json:"from,omitempty"`
	// Previous is the version the member ran before a reload or a
	// rollback, as last read (I148).
	Previous   uint64 `json:"previous,omitempty"`
	Drift      *Drift `json:"drift,omitempty"`
	Purged     *int   `json:"purged,omitempty"`
	LatencyMs  int64  `json:"latency_ms,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	FitVersion string `json:"fit_version,omitempty"`
}

// Op is an operation on many members, and each one's outcome.
type Op struct {
	ID       string       `json:"id"`
	Op       string       `json:"op"`
	Group    string       `json:"group,omitempty"`
	User     string       `json:"user"`
	Purge    *PurgeParams `json:"purge,omitempty"`
	Started  time.Time    `json:"started"`
	Ended    time.Time    `json:"ended,omitzero"`
	Finished bool         `json:"finished"`
	Counts   OpCounts     `json:"counts"`
	Outcomes []Outcome    `json:"outcomes"`
}

// OpCounts counts an operation's members by status.
type OpCounts struct {
	Pending int `json:"pending"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
}

// opsState is the operations the manager keeps, under m.mu.
type opsState struct {
	list    []*Op             // oldest first
	busy    map[string]string // a member's name → the ID of the operation running on it
	running int
	wg      sync.WaitGroup
}

// errBusy is an operation refused because too many run: 429.
type errBusy string

func (e errBusy) Error() string { return string(e) }

// check checks p, a purge's params.
func (p *PurgeParams) check() error {
	if p == nil {
		return fieldErr("purge", "a purge names keys, prefixes, tags, or all")
	}
	if len(p.Zone) > MaxNameBytes*2 || !printable(p.Zone) {
		return fieldErr("purge.zone", "the zone is a name of at most %d bytes", MaxNameBytes*2)
	}
	lists := []struct {
		field string
		v     []string
	}{{"purge.keys", p.Keys}, {"purge.prefixes", p.Prefixes}, {"purge.tags", p.Tags}}
	n := 0
	for _, l := range lists {
		if len(l.v) > MaxPurgeItems {
			return fieldErr(l.field, "%d items, more than %d", len(l.v), MaxPurgeItems)
		}
		for _, s := range l.v {
			if s == "" || len(s) > MaxPurgeItemBytes || !printable(s) {
				return fieldErr(l.field, "an item is 1 to %d printable characters", MaxPurgeItemBytes)
			}
			if l.field != "purge.tags" && !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
				return fieldErr(l.field, "%q is not an absolute http or https URL, as the cache's keys are", s)
			}
		}
		n += len(l.v)
	}
	if n == 0 && !p.All {
		return fieldErr("purge", "a purge names keys, prefixes, tags, or all")
	}
	if n > 0 && p.All {
		return fieldErr("purge.all", "all purges everything: name no keys, prefixes or tags with it")
	}
	return nil
}

// newOpID returns an operation's ID: 12 random hex digits.
func newOpID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// StartOp starts the operation req as user, and returns it as it starts:
// every member pending. It runs on a goroutine of its own, at most
// max_concurrency members at once, each with the calls' timeouts; the
// operation and each member's outcome go to the audit log once it ends.
func (m *Manager) StartOp(user string, req *OpRequest) (Op, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names, err := m.checkOpLocked(user, req)
	if err != nil {
		return Op{}, err
	}
	op := &Op{ID: newOpID(), Op: req.Op, Group: req.Group, User: user, Purge: req.Purge, Started: m.now().UTC(),
		Outcomes: make([]Outcome, len(names))}
	for i, name := range names {
		op.Outcomes[i] = Outcome{Node: name, Status: OutcomePending}
		m.ops.busy[name] = op.ID
	}
	op.Counts.Pending = len(names)
	m.ops.running++
	m.ops.list = append(m.ops.list, op)
	m.trimOpsLocked()
	m.ops.wg.Add(1)
	out := op.copy()
	go m.runOp(op)
	m.logf("cluster: %q started the operation %s %s on %s", user, op.ID, op.Op, strings.Join(names, ", "))
	return out, nil
}

// checkOpLocked checks the operation req as user would start it now, and
// returns the names of the members it runs on, sorted: what StartOp
// starts, and what the assistant's cluster_op_propose (I153) proposes.
// m.mu is held.
func (m *Manager) checkOpLocked(user string, req *OpRequest) ([]string, error) {
	if !slices.Contains(Ops, req.Op) {
		return nil, fieldErr("op", "%q is not an operation: %s", req.Op, strings.Join(Ops, ", "))
	}
	if req.Op == OpPurge {
		if err := req.Purge.check(); err != nil {
			return nil, err
		}
	} else if req.Purge != nil {
		return nil, fieldErr("purge", "only a purge takes purge")
	}
	switch {
	case m.closed:
		return nil, errClosed
	case m.readOnly != "":
		// An operation is recorded in the audit log, which a read-only
		// document cannot keep.
		return nil, errReadOnly(m.readOnly)
	}
	names, err := m.targetsLocked(req)
	if err != nil {
		return nil, err
	}
	if err := m.allowLocked(user, Need{Role: RoleOperator, Nodes: names}); err != nil {
		return nil, err
	}
	if m.ops.busy == nil {
		m.ops.busy = map[string]string{}
	}
	var taken []string
	for _, name := range names {
		if id := m.ops.busy[name]; id != "" {
			taken = append(taken, name+" ("+id+")")
		}
	}
	if len(taken) > 0 {
		return nil, errConflict("an operation runs on " + strings.Join(taken, ", ") + " already: wait for it to end")
	}
	if m.ops.running >= MaxRunningOps {
		return nil, errBusy(fmt.Sprintf("%d operations run already; try again once one ends", MaxRunningOps))
	}
	return names, nil
}

// targetsLocked returns the names of the members req runs on, sorted.
// m.mu is held.
func (m *Manager) targetsLocked(req *OpRequest) ([]string, error) {
	var inGroup []string
	if req.Group != "" {
		i := groupIndex(&m.doc, req.Group)
		if i < 0 {
			return nil, errNoGroup(req.Group)
		}
		inGroup = m.viewLocked(&m.doc.Groups[i]).Nodes
	}
	if len(req.Members) == 0 {
		if req.Group == "" {
			return nil, fieldErr("members", "an operation runs on a group, members, or members of a group: name them")
		}
		if len(inGroup) == 0 {
			return nil, fieldErr("group", "the group %q has no members now", req.Group)
		}
		return inGroup, nil
	}
	var names []string
	seen := map[string]bool{}
	for _, name := range req.Members {
		if seen[name] {
			continue
		}
		seen[name] = true
		if index(&m.doc, name) < 0 {
			return nil, errNotFound(name)
		}
		if req.Group != "" && !slices.Contains(inGroup, name) {
			return nil, fieldErr("members", "%q is not in the group %q", name, req.Group)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// trimOpsLocked drops the oldest finished operations past MaxKeptOps.
// m.mu is held.
func (m *Manager) trimOpsLocked() {
	for len(m.ops.list) > MaxKeptOps {
		i := slices.IndexFunc(m.ops.list, func(o *Op) bool { return o.Finished })
		if i < 0 {
			return // all run: MaxRunningOps < MaxKeptOps keeps this short
		}
		m.ops.list = slices.Delete(m.ops.list, i, i+1)
	}
}

// copy returns a copy of op that shares nothing that changes with it.
func (op *Op) copy() Op {
	out := *op
	out.Outcomes = slices.Clone(op.Outcomes)
	return out
}

// Ops returns the operations kept, the latest first.
func (m *Manager) Ops() []Op {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Op, 0, len(m.ops.list))
	for i := len(m.ops.list) - 1; i >= 0; i-- {
		out = append(out, m.ops.list[i].copy())
	}
	return out
}

// OpByID returns the operation id, while it is kept.
func (m *Manager) OpByID(id string) (Op, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range m.ops.list {
		if op.ID == id {
			return op.copy(), true
		}
	}
	return Op{}, false
}

// set records o as the outcome of op's i-th member. m.mu is not held.
func (m *Manager) set(op *Op, i int, o Outcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op.Outcomes[i] = o
	c := OpCounts{}
	for _, x := range op.Outcomes {
		switch x.Status {
		case OutcomePending:
			c.Pending++
		case OutcomeRunning:
			c.Running++
		case OutcomeDone:
			c.Done++
		default:
			c.Failed++
		}
	}
	op.Counts = c
}

// runOp runs op on its members, at most max_concurrency at once, then
// records it in the audit log. It is a goroutine of its own: it waits only
// for its members' calls, which go out on fib's asynchronous client.
func (m *Manager) runOp(op *Op) {
	defer m.ops.wg.Done()
	sem := make(chan struct{}, m.o.Settings.Concurrency())
	var wg sync.WaitGroup
	stopped := false
	for i := range op.Outcomes {
		name := op.Outcomes[i].Node
		if !stopped {
			// A call started as the manager stops fails at once: its
			// member's connections are closed.
			select {
			case sem <- struct{}{}:
			case <-m.stop:
				stopped = true
			}
		}
		if stopped {
			m.set(op, i, Outcome{Node: name, Status: OutcomeFailed, Error: &Problem{Kind: KindUnreachable,
				Message: "the manager stopped before the operation reached the member", At: m.now().UTC()}})
			continue
		}
		start := m.now()
		m.set(op, i, Outcome{Node: name, Status: OutcomeRunning, Started: start.UTC()})
		wg.Add(1)
		m.runOne(op, name, func(res *OpResult, detail string, prob *Problem) {
			end := m.now()
			o := Outcome{Node: name, Status: OutcomeDone, Detail: detail, Result: res, Started: start.UTC(), Ended: end.UTC(),
				TookMs: end.Sub(start).Milliseconds()}
			if prob != nil {
				if prob.At.IsZero() {
					prob.At = end.UTC()
				}
				o.Status, o.Error, o.Result = OutcomeFailed, prob, nil
			}
			m.set(op, i, o)
			<-sem
			wg.Done()
		})
	}
	wg.Wait()
	m.mu.Lock()
	op.Ended, op.Finished = m.now().UTC(), true
	for _, o := range op.Outcomes {
		if m.ops.busy[o.Node] == op.ID {
			delete(m.ops.busy, o.Node)
		}
	}
	m.ops.running--
	m.trimOpsLocked()
	// What the operation changed is read by the next poll, at once.
	for _, o := range op.Outcomes {
		if n := m.nodes[o.Node]; n != nil {
			n.next.Store(0)
		}
	}
	m.poke()
	entry := op.audit()
	user, counts := op.User, op.Counts
	m.mu.Unlock()
	m.logf("cluster: the operation %s %s ended: %d done, %d failed", op.ID, op.Op, counts.Done, counts.Failed)
	if err := m.change(user, func(*document) (AuditEntry, error) { return entry, nil }); err != nil {
		m.logf("cluster: the operation %s is not in the audit log: %v", op.ID, err)
	}
}

// audit returns op's audit entry. m.mu is held.
func (op *Op) audit() AuditEntry {
	e := AuditEntry{Action: "op." + op.Op, Group: op.Group, Op: op.ID,
		Detail: fmt.Sprintf("%d done, %d failed", op.Counts.Done, op.Counts.Failed)}
	if p := op.Purge; p != nil {
		e.Detail += "; " + p.describe()
	}
	for _, o := range op.Outcomes {
		a := AuditOutcome{Node: o.Node, Status: o.Status, Detail: o.Detail}
		if o.Error != nil {
			a.Error = clip(o.Error.Kind+": "+o.Error.Message, maxAuditError)
		}
		if res := o.Result; res != nil && (op.Op == OpReload || op.Op == OpRollback) {
			a.Before, a.After = res.Previous, res.Version
		}
		e.Outcomes = append(e.Outcomes, a)
	}
	e.Outcome = opOutcome(e.Outcomes, func(s string) bool { return s == OutcomeDone })
	return e
}

// describe says what a purge removes.
func (p *PurgeParams) describe() string {
	zone := "every zone"
	if p.Zone != "" {
		zone = "zone " + p.Zone
	}
	if p.All {
		return "all of " + zone
	}
	var parts []string
	for _, l := range []struct {
		name string
		v    []string
	}{{"keys", p.Keys}, {"prefixes", p.Prefixes}, {"tags", p.Tags}} {
		if len(l.v) > 0 {
			parts = append(parts, l.name+" "+strings.Join(l.v, " "))
		}
	}
	return clip(zone+": "+strings.Join(parts, "; "), 512)
}

// clip bounds s to n bytes, on a rune's boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// auditSize is about what e takes in the store.
func auditSize(e *AuditEntry) int {
	n := 96 + len(e.User) + len(e.Action) + len(e.Node) + len(e.Group) + len(e.Detail) + len(e.Op) + len(e.Rollout) + len(e.Template) +
		len(e.Outcome) + len(e.Diff) + len(e.Digest) + len(e.Before) + len(e.After)
	for _, o := range e.Outcomes {
		n += 96 + len(o.Node) + len(o.Status) + len(o.Detail) + len(o.Error) + len(o.Digest)
	}
	for _, x := range e.Nodes {
		n += 3 + len(x)
	}
	return n
}

// trimAudit drops the oldest entries past MaxAudit, and past MaxAuditBytes.
func trimAudit(a []AuditEntry) []AuditEntry {
	if n := len(a) - MaxAudit; n > 0 {
		a = slices.Delete(a, 0, n)
	}
	size := 0
	for i := len(a) - 1; i >= 0; i-- {
		if size += auditSize(&a[i]); size > MaxAuditBytes && i < len(a)-1 {
			return slices.Delete(a, 0, i+1)
		}
	}
	return a
}

// runOne runs op on the member name, and calls done once with what it
// answered, or why it failed.
func (m *Manager) runOne(op *Op, name string, done func(*OpResult, string, *Problem)) {
	need := map[string][]string{OpReload: {capReload}, OpRollback: {capVersions}, OpCheck: {capCheck}, OpPurge: {capCache}}[op.Op]
	if op.Op == OpPurge && len(op.Purge.Prefixes) > 0 {
		need = append(need, capCachePrefix)
	}
	if op.Op == OpTest {
		m.opTest(name, done)
		return
	}
	m.capabilities(name, func(caps []string, prob *Problem) {
		if prob != nil {
			done(nil, "", prob)
			return
		}
		for _, c := range need {
			if !slices.Contains(caps, c) {
				done(nil, "", &Problem{Kind: KindUnsupported, Message: "the member does not list " + c +
					" in its capabilities: it is a fit older than this operation"})
				return
			}
		}
		switch op.Op {
		case OpReload:
			m.opReload(name, done)
		case OpRollback:
			m.opRollback(name, done)
		case OpCheck:
			m.opCheck(name, done)
		default:
			m.opPurge(name, op.Purge, done)
		}
	})
}

// call is Call, its answer read as JSON into v when it is 2xx, or turned
// into a Problem; what names the call in a Problem's message.
func (m *Manager) call(name, method, path string, header stdhttp.Header, body []byte, v any, done func(*Reply, *Problem)) {
	err := m.Call(name, method, path, header, body, func(r *Reply, prob *Problem) {
		if prob != nil {
			done(nil, prob)
			return
		}
		if r.Status < 200 || r.Status > 299 {
			prob := replyProblem(r, method+" "+path)
			m.mu.Lock()
			n := m.nodes[name]
			m.mu.Unlock()
			if n != nil {
				prob.Message = n.redact(prob.Message)
			}
			done(nil, prob)
			return
		}
		if v != nil {
			if err := json.Unmarshal(r.Body, v); err != nil {
				done(nil, &Problem{Kind: KindBadAnswer, Status: r.Status, Message: "the member's " + path + " is not the JSON expected: " + err.Error()})
				return
			}
		}
		done(r, nil)
	})
	if err != nil {
		done(nil, &Problem{Kind: KindUnreachable, Message: err.Error()})
	}
}

// replyProblem is the Problem of a member's answer that is not 2xx: its
// error, and the first of its errors in a config refused.
func replyProblem(r *Reply, what string) *Problem {
	var b struct {
		Error  string `json:"error"`
		Errors []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(r.Body, &b)
	msg := b.Error
	if msg == "" {
		msg = stdhttp.StatusText(r.Status)
	}
	if len(b.Errors) > 0 {
		e := b.Errors[0]
		msg += ": " + strings.TrimPrefix(e.Path+": ", ": ") + e.Message
		if len(b.Errors) > 1 {
			msg += fmt.Sprintf(" (and %d more)", len(b.Errors)-1)
		}
	}
	p := &Problem{Kind: KindRefused, Status: r.Status, Message: clip(what+": "+msg, maxMessage)}
	switch r.Status {
	case stdhttp.StatusUnauthorized:
		p.Kind = KindAuth
	case stdhttp.StatusForbidden:
		p.Kind = KindForbidden
	}
	return p
}

// capabilities calls then with the capabilities the member lists: those its
// last poll read, or, when no poll read it yet, its GET /node's.
func (m *Manager) capabilities(name string, then func([]string, *Problem)) {
	m.mu.Lock()
	n := m.nodes[name]
	m.mu.Unlock()
	if n != nil {
		n.mu.Lock()
		var caps []string
		if n.state.Node != nil {
			caps = n.state.Node.Capabilities
		}
		n.mu.Unlock()
		if caps != nil {
			then(caps, nil)
			return
		}
	}
	var info NodeInfo
	m.call(name, "GET", "/node", nil, nil, &info, func(_ *Reply, prob *Problem) {
		if prob != nil {
			then(nil, prob)
			return
		}
		then(nonNil(info.Capabilities), nil)
	})
}

// opTest reads the member's GET /node.
func (m *Manager) opTest(name string, done func(*OpResult, string, *Problem)) {
	var info NodeInfo
	start := m.now()
	m.call(name, "GET", "/node", nil, nil, &info, func(_ *Reply, prob *Problem) {
		if prob != nil {
			done(nil, "", prob)
			return
		}
		res := &OpResult{LatencyMs: m.now().Sub(start).Milliseconds(), NodeID: info.ID, FitVersion: info.FitVersion}
		done(res, fmt.Sprintf("reached in %d ms: fit %s", res.LatencyMs, info.FitVersion), nil)
	})
}

// applied is what a member answers a config applied.
type applied struct {
	Version  uint64   `json:"version"`
	Warnings []string `json:"warnings"`
}

func appliedDetail(what string, a *applied) string {
	s := what + " as version " + strconv.FormatUint(a.Version, 10)
	if len(a.Warnings) > 0 {
		s += "; " + clip(strings.Join(a.Warnings, "; "), 256)
	}
	return s
}

// opReload applies the member's running config again.
func (m *Manager) opReload(name string, done func(*OpResult, string, *Problem)) {
	var a applied
	before := m.polledVersion(name)
	m.call(name, "POST", "/reload", nil, nil, &a, func(_ *Reply, prob *Problem) {
		if prob != nil {
			done(nil, "", prob)
			return
		}
		done(&OpResult{Version: a.Version, Previous: before}, appliedDetail("reloaded", &a), nil)
	})
}

// polledVersion is the config version the member name ran when last
// polled, 0 when not known.
func (m *Manager) polledVersion(name string) uint64 {
	m.mu.Lock()
	n := m.nodes[name]
	m.mu.Unlock()
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if c := n.state.Config; c != nil {
		return c.Version
	}
	return 0
}

// versionsAnswer is a member's GET /versions.
type versionsAnswer struct {
	Current  uint64 `json:"current"`
	Versions []struct {
		Version uint64 `json:"version"`
		ETag    string `json:"etag"`
	} `json:"versions"`
}

// previous returns the version to roll back to, the latest before the
// running one whose ETag is not the running one's, and the running one's
// ETag; 0 when there is none.
func (v *versionsAnswer) previous() (uint64, string) {
	etag := ""
	for _, x := range v.Versions {
		if x.Version == v.Current {
			etag = x.ETag
		}
	}
	for i := len(v.Versions) - 1; i >= 0; i-- {
		if x := v.Versions[i]; x.Version < v.Current && x.ETag != etag {
			return x.Version, etag
		}
	}
	return 0, etag
}

// opRollback applies the member's previous config again, if the running
// one is still the one it read.
func (m *Manager) opRollback(name string, done func(*OpResult, string, *Problem)) {
	var vs versionsAnswer
	m.call(name, "GET", "/versions", nil, nil, &vs, func(_ *Reply, prob *Problem) {
		if prob != nil {
			done(nil, "", prob)
			return
		}
		to, etag := vs.previous()
		if to == 0 {
			done(nil, "", &Problem{Kind: KindNoEarlier, Message: "the member keeps no earlier config than the running one (version " +
				strconv.FormatUint(vs.Current, 10) + ") to roll back to"})
			return
		}
		h := stdhttp.Header{}
		if etag != "" {
			h.Set("If-Match", etag)
		}
		var a applied
		m.call(name, "POST", "/rollback/"+strconv.FormatUint(to, 10), h, nil, &a, func(_ *Reply, prob *Problem) {
			if prob != nil {
				done(nil, "", prob)
				return
			}
			done(&OpResult{Version: a.Version, From: to, Previous: vs.Current}, appliedDetail("rolled back to version "+strconv.FormatUint(to, 10)+",", &a), nil)
		})
	})
}

// opCheck runs the member's consistency check.
func (m *Manager) opCheck(name string, done func(*OpResult, string, *Problem)) {
	var ck checkAnswer
	m.call(name, "GET", "/config/check", nil, nil, &ck, func(_ *Reply, prob *Problem) {
		if prob != nil {
			done(nil, "", prob)
			return
		}
		d := &Drift{Count: ck.Summary.Drift, Info: ck.Summary.Info, CheckedAt: ck.CheckedAt}
		detail := "in sync"
		if d.Count > 0 {
			detail = fmt.Sprintf("%d differences", d.Count)
		}
		done(&OpResult{Drift: d}, detail, nil)
	})
}

// opPurge purges the member's cache.
func (m *Manager) opPurge(name string, p *PurgeParams, done func(*OpResult, string, *Problem)) {
	body, _ := json.Marshal(map[string]any{"zone": p.Zone, "urls": nonNil(p.Keys), "prefixes": nonNil(p.Prefixes), "tags": nonNil(p.Tags), "all": p.All})
	var a struct {
		Purged int `json:"purged"`
	}
	m.call(name, "POST", "/cache/purge", nil, body, &a, func(_ *Reply, prob *Problem) {
		if prob != nil {
			done(nil, "", prob)
			return
		}
		n := a.Purged
		done(&OpResult{Purged: &n}, fmt.Sprintf("purged %d keys", n), nil)
	})
}

// waitOps waits for the operations under way to end, at most wait.
func (m *Manager) waitOps(wait time.Duration) {
	ended := make(chan struct{})
	go func() { m.ops.wg.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(wait):
	}
}

// opsBody is GET /api/ops's answer.
type opsBody struct {
	Ops []Op `json:"ops"`
}

func (m *Manager) opRoutes(api *router) {
	api.Handle("GET", "/api/ops", func(r *console.Request) { _ = r.JSON(200, opsBody{Ops: m.Ops()}) })
	api.Checked("POST", "/api/ops", m.startOp)
	api.HandlePrefix("GET", "/api/ops/", func(r *console.Request) {
		id := strings.TrimPrefix(r.Path, "/api/ops/")
		op, ok := m.OpByID(id)
		if !ok {
			_ = console.Error(r.Context, 404, "no operation "+strconv.Quote(id)+" is kept")
			return
		}
		_ = r.JSON(200, op)
	})
}

// startOp answers POST /api/ops: 202 with the operation started, which GET
// /api/ops/<id> follows.
func (m *Manager) startOp(r *console.Request) {
	var req OpRequest
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("an operation is %d bytes at most", MaxBodyBytes))
		return
	}
	if !r.Decode(&req) {
		return
	}
	op, err := m.StartOp(r.User, &req)
	if err == errClosed {
		_ = console.Error(r.Context, 503, err.Error())
		return
	}
	m.answer(r, 202, op, err)
}
