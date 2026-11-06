package cluster

import (
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/internal/managed"
)

// A rollout (I146) is one change applied to many members in stages: a
// template's change, a sync of members out of sync with their templates, or
// parts of one member's config copied to others. It is planned first: each
// member's desired config, its diff and the member's own dry run, the ETag
// of the config it was made from taken. A person then starts it with a
// strategy: every member at once, in batches by size or percentage with a
// pause between them, or a canary of the first members waiting for the
// person to go on. Each member gets its desired config by its own PUT
// /config with If-Match, so a member changed since the plan is skipped,
// never overwritten; after each batch a health gate checks every member of
// it, and a failed gate stops the rollout and rolls the batch back, or all
// of it, each member to the version it ran by its POST /rollback/{n}. It
// can be paused, resumed, aborted and rolled back; it is kept in the store
// with each member's outcome and versions, and a manager restarted in the
// middle shows it paused where it stopped. See rollrun.go for how it runs.

// The kinds of rollouts, RolloutRequest.Kind.
const (
	// RolloutTemplate applies a template's change: its members' desired
	// configs.
	RolloutTemplate = "template"
	// RolloutSync brings members in sync with their templates: their
	// desired configs.
	RolloutSync = "sync"
	// RolloutCopy copies parts of one member's config to others.
	RolloutCopy = "copy"
	// RolloutPatch (rolldraft.go) puts parts given with the request on
	// members.
)

// The states of a rollout, Rollout.State.
const (
	// RolloutPlanning: the members are being read and checked.
	RolloutPlanning = "planning"
	// RolloutPlanned: the plan is ready to be started, until PlanTTL.
	RolloutPlanned = "planned"
	// RolloutRunning: batches are being applied.
	RolloutRunning = "running"
	// RolloutPaused: stopped between batches by a person, or by the
	// manager's restart; resumed by a person.
	RolloutPaused = "paused"
	// RolloutWaiting: the canary passed its gate, and waits for a person
	// to go on.
	RolloutWaiting = "waiting"
	// RolloutRollingBack: members are being rolled back, as a person asked.
	RolloutRollingBack = "rolling_back"
	// RolloutDone: every batch passed its gate.
	RolloutDone = "done"
	// RolloutFailed: a gate failed and the rollout stopped, its batch (or
	// all of it) rolled back; or it could not go on.
	RolloutFailed = "failed"
	// RolloutAborted: a person stopped it; what was applied stays.
	RolloutAborted = "aborted"
	// RolloutRolledBack: a person rolled it back.
	RolloutRolledBack = "rolled_back"
	// RolloutExpired: a plan not started within PlanTTL, or one the
	// manager's restart cut short.
	RolloutExpired = "expired"
)

// The statuses of a rollout's member, RolloutMember.Status.
const (
	// While it is planned.
	MemberPlanning  = "planning"
	MemberReady     = "ready"
	MemberUnchanged = "unchanged"
	MemberError     = "error"
	// Left out when the rollout started.
	MemberExcluded = "excluded"
	// While it runs.
	MemberPending  = "pending"
	MemberApplying = "applying"
	// MemberApplied: its desired config applied, its gate not passed yet.
	MemberApplied = "applied"
	MemberDone    = "done"
	MemberFailed  = "failed"
	// MemberSkipped: its config changed since the plan; it was not
	// overwritten.
	MemberSkipped        = "skipped"
	MemberRolledBack     = "rolled_back"
	MemberRollbackFailed = "rollback_failed"
)

// The strategies of a rollout, Strategy.Mode.
const (
	StrategyAll     = "all"
	StrategyRolling = "rolling"
	StrategyCanary  = "canary"
)

// What a failed gate rolls back, Strategy.OnFailure.
const (
	OnFailureBatch = "batch"
	OnFailureAll   = "all"
)

// The states of a rollout's batch, Batch.State.
const (
	BatchPending    = "pending"
	BatchApplying   = "applying"
	BatchGating     = "gating"
	BatchPassed     = "passed"
	BatchFailed     = "failed"
	BatchRolledBack = "rolled_back"
	BatchAborted    = "aborted"
)

// KindBusy is a member in another rollout, or in an operation.
const KindBusy = "busy"

// The bounds of rollouts.
const (
	// MaxActiveRollouts is how many rollouts are planning, planned,
	// running, paused, waiting or rolling back at once; one more is
	// refused 429.
	MaxActiveRollouts = 4
	// MaxKeptRollouts is how many rollouts the store keeps, the oldest
	// finished dropped first.
	MaxKeptRollouts = 50
	// MaxRolloutBytes bounds what a rollout keeps of its members' desired
	// configs and diffs while it is not finished; a member past it is not
	// planned.
	MaxRolloutBytes = 8 << 20
	// MaxRolloutErrors bounds the errors a member's dry run reports kept.
	MaxRolloutErrors = 20
	// MaxDescriptionLen bounds a rollout's description.
	maxRolloutDescription = 512
	// maxGateReasons bounds the reasons a gate gives.
	maxGateReasons = 8
)

// rolloutBytes is MaxRolloutBytes, which tests lower.
var rolloutBytes = MaxRolloutBytes

// PlanTTL is how long a plan may wait to be started: past it, the members'
// configs it holds are dropped and it is expired.
var PlanTTL = time.Hour

// The defaults and bounds of a strategy's times, in milliseconds.
const (
	DefaultGateWaitMs    = 5000
	DefaultGateWindowMs  = 30000
	DefaultMinRequests   = 20
	maxGateWaitMs        = 10 * 60 * 1000
	maxGateWindowMs      = 30 * 60 * 1000
	maxPauseMs           = 24 * 60 * 60 * 1000
	maxGateMinRequests   = 1_000_000_000
	defaultOnFailureMode = OnFailureBatch
)

// RolloutRequest plans a rollout, as POST /api/rollouts takes it.
//
// For RolloutTemplate, Template names the template; its members are those
// of its groups, or Nodes of them. For RolloutSync, Nodes names the
// members, Group a group's, or every member when both are empty. For
// RolloutCopy, From names the member whose parts Paths ("log",
// "routes[api]") are copied, to Nodes, or the members of Group but From.
// For RolloutPatch, Config holds the parts put on Nodes, or the members of
// Group; for RolloutTemplate, Config, when set, is a draft of the
// template's config, saved as the template when the rollout starts
// (rolldraft.go).
type RolloutRequest struct {
	Kind        string          `json:"kind"`
	Template    string          `json:"template,omitempty"`
	Group       string          `json:"group,omitempty"`
	Nodes       []string        `json:"nodes,omitempty"`
	From        string          `json:"from,omitempty"`
	Paths       []string        `json:"paths,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
	Description string          `json:"description,omitempty"`

	// source and suggested are the assistant's (I153): who planned it,
	// and the strategy it suggests, which the Rollouts page fills its
	// form with.
	source    string
	suggested *Strategy
}

// Strategy is how a rollout applies its members: StrategyAll, one batch;
// StrategyRolling, batches of BatchSize members, or of BatchPercent of them,
// with PauseMs between batches; StrategyCanary, a first batch of Canary
// members, after whose gate the rollout waits for a person to go on, then
// the rest in batches as rolling has them (one batch when neither size is
// given). OnFailure is what a failed gate rolls back: its batch, or all
// members applied.
type Strategy struct {
	Mode         string `json:"mode"`
	BatchSize    int    `json:"batch_size,omitempty"`
	BatchPercent int    `json:"batch_percent,omitempty"`
	PauseMs      int64  `json:"pause_ms,omitempty"`
	Canary       int    `json:"canary,omitempty"`
	OnFailure    string `json:"on_failure,omitempty"`
	Gate         Gate   `json:"gate"`
}

// Gate is the health gate after each batch, checked on each of its members
// WaitMs after the batch is applied: the member reachable, running the
// config the rollout applied (its reload succeeded), with no more
// differences in its consistency check than before, and, when Max5xxRatio
// is set, its share of 5xx answers over the WindowMs after the batch at
// most Max5xxRatio, judged once at least MinRequests were answered in it.
type Gate struct {
	WaitMs      int64   `json:"wait_ms"`
	Max5xxRatio float64 `json:"max_5xx_ratio,omitempty"`
	WindowMs    int64   `json:"window_ms,omitempty"`
	MinRequests int     `json:"min_requests,omitempty"`
}

// StartRequest starts a planned rollout, as POST /api/rollouts/<id>/start
// takes it: its strategy, and members of the plan left out.
type StartRequest struct {
	Strategy Strategy `json:"strategy"`
	Exclude  []string `json:"exclude,omitempty"`
}

// Batch is a batch of a rollout: its members, in their order, and how it
// went.
type Batch struct {
	Index   int       `json:"index"`
	Nodes   []string  `json:"nodes"`
	Canary  bool      `json:"canary,omitempty"`
	State   string    `json:"state"`
	Started time.Time `json:"started,omitzero"`
	GateAt  time.Time `json:"gate_at,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
}

// Change is a part of a member's config a rollout changes: "differs", or
// "added" (POST /api/compare's differs and only_b; a rollout removes none),
// with what the member has and what it gets, secrets masked; for a rollout
// of templates, the part's sync state, why, and its template.
type Change struct {
	Path     string          `json:"path"`
	State    string          `json:"state"`
	Sync     string          `json:"sync,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Template string          `json:"template,omitempty"`
	Have     json.RawMessage `json:"have,omitempty"`
	Want     json.RawMessage `json:"want,omitempty"`
}

// ConfigError is an error a member's dry run found in its desired config,
// at its JSON pointer.
type ConfigError struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// GateResult is how a member's gate went: passed or not, and why.
type GateResult struct {
	Passed  bool      `json:"passed"`
	Checked time.Time `json:"checked,omitzero"`
	// Reasons say why it failed; Notes what passed with a remark.
	Reasons     []string `json:"reasons,omitempty"`
	Notes       []string `json:"notes,omitempty"`
	Version     uint64   `json:"version,omitempty"`
	DriftBefore *int     `json:"drift_before,omitempty"`
	DriftAfter  *int     `json:"drift_after,omitempty"`
	// Requests and Errors5xx are what the member answered in the window,
	// and Ratio their share, when the 5xx ratio is gated.
	Requests  *uint64  `json:"requests,omitempty"`
	Errors5xx *uint64  `json:"errors_5xx,omitempty"`
	Ratio     *float64 `json:"ratio,omitempty"`
}

// RolloutMember is a member of a rollout: its plan and how it went.
type RolloutMember struct {
	Node   string `json:"node"`
	Status string `json:"status"`
	// ConfigETag and Version are the config the plan was made from, and
	// the version the member ran then, which a rollback goes back to;
	// NewETag the ETag of the desired config, as the member's dry run
	// said; Digest the desired config's hash.
	ConfigETag string `json:"config_etag,omitempty"`
	Version    uint64 `json:"version,omitempty"`
	NewETag    string `json:"new_etag,omitempty"`
	Digest     string `json:"digest,omitempty"`
	// Applied is the version the rollout applied, and RolledBack the
	// version its rollback made.
	Applied    uint64 `json:"applied,omitempty"`
	RolledBack uint64 `json:"rolled_back,omitempty"`
	// Unsure is a member whose apply was not answered, and whose ETag
	// could not be read after: it may run the rollout's config, and a
	// rollback looks.
	Unsure bool `json:"unsure,omitempty"`
	// Changes are the parts that change, their values kept until the
	// rollout ends; Changed their paths, kept after.
	Changes []Change `json:"changes,omitempty"`
	Changed []string `json:"changed,omitempty"`
	// Errors are its dry run's, Warnings its dry run's and apply's,
	// Note a remark (another version of fit), and Error why it failed.
	Errors   []ConfigError `json:"errors,omitempty"`
	Warnings []string      `json:"warnings,omitempty"`
	Note     string        `json:"note,omitempty"`
	Error    *Problem      `json:"error,omitempty"`
	Gate     *GateResult   `json:"gate,omitempty"`
	// Config is the desired config, secrets and all, kept in the store
	// until the rollout ends, byte for byte (base64), so that the ETag the
	// member gives it is the one its dry run said; never answered.
	Config []byte `json:"config,omitempty"`
}

// RolloutCounts counts a rollout's members by status.
type RolloutCounts map[string]int

// Rollout is a change applied to many members, and how it went.
type Rollout struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Template string   `json:"template,omitempty"`
	Group    string   `json:"group,omitempty"`
	From     string   `json:"from,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	// Config is a patch's parts, or a template's draft (rolldraft.go),
	// its secrets masked in answers, dropped once the rollout ends;
	// TemplateBase the hash of the template's config the draft was
	// planned on.
	Config       json.RawMessage `json:"config,omitempty"`
	TemplateBase string          `json:"template_base,omitempty"`
	Description  string          `json:"description,omitempty"`
	PlannedBy    string          `json:"planned_by"`
	// Source is PlanSource for a plan the assistant made for PlannedBy,
	// and Suggested the strategy it suggests (I153).
	Source    string    `json:"source,omitempty"`
	Suggested *Strategy `json:"suggested,omitempty"`
	StartedBy string    `json:"started_by,omitempty"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	Strategy  *Strategy `json:"strategy,omitempty"`
	Created   time.Time `json:"created"`
	Planned   time.Time `json:"planned,omitzero"`
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
	// Batch is the batch under way, or next; NextAt when it starts,
	// while the rollout pauses between batches.
	Batch   int             `json:"batch"`
	NextAt  time.Time       `json:"next_at,omitzero"`
	Batches []Batch         `json:"batches,omitempty"`
	Members []RolloutMember `json:"members"`
	Counts  RolloutCounts   `json:"counts"`
}

// active reports whether r holds its members: planning, planned or under
// way.
func (r *Rollout) active() bool {
	switch r.State {
	case RolloutPlanning, RolloutPlanned, RolloutRunning, RolloutPaused, RolloutWaiting, RolloutRollingBack:
		return true
	}
	return false
}

// holds reports whether r holds its members against operations and other
// rollouts: started and not ended.
func (r *Rollout) holds() bool {
	switch r.State {
	case RolloutRunning, RolloutPaused, RolloutWaiting, RolloutRollingBack:
		return true
	}
	return false
}

// finished reports whether r ended.
func (r *Rollout) finished() bool { return !r.active() }

// member returns r's member name, or nil.
func (r *Rollout) member(name string) *RolloutMember {
	for i := range r.Members {
		if r.Members[i].Node == name {
			return &r.Members[i]
		}
	}
	return nil
}

// count counts r's members by status.
func (r *Rollout) count() {
	c := RolloutCounts{}
	for _, x := range r.Members {
		c[x.Status]++
	}
	r.Counts = c
}

// copy returns a copy of r that shares nothing that changes with it; full
// keeps the changes' values, and secrets keeps the members' configs.
func (r *Rollout) copy(full, secrets bool) Rollout {
	out := *r
	out.Paths = slices.Clone(r.Paths)
	out.Config = copyConfig(r.Config, secrets)
	if r.Strategy != nil {
		s := *r.Strategy
		out.Strategy = &s
	}
	if r.Suggested != nil {
		s := *r.Suggested
		out.Suggested = &s
	}
	out.Batches = make([]Batch, len(r.Batches))
	for i, b := range r.Batches {
		b.Nodes = slices.Clone(b.Nodes)
		out.Batches[i] = b
	}
	out.Members = make([]RolloutMember, len(r.Members))
	for i, x := range r.Members {
		x.Changed = slices.Clone(x.Changed)
		x.Errors = slices.Clone(x.Errors)
		x.Warnings = slices.Clone(x.Warnings)
		if x.Gate != nil {
			g := *x.Gate
			g.Reasons, g.Notes = slices.Clone(g.Reasons), slices.Clone(g.Notes)
			x.Gate = &g
		}
		if x.Error != nil {
			p := *x.Error
			x.Error = &p
		}
		if full {
			x.Changes = slices.Clone(x.Changes)
		} else {
			x.Changes = nil
		}
		if !secrets {
			x.Config = nil
		}
		out.Members[i] = x
	}
	out.Counts = RolloutCounts{}
	for k, v := range r.Counts {
		out.Counts[k] = v
	}
	return out
}

// shed drops what r keeps of its members' configs and diffs, once it is
// finished: the paths changed stay.
func (r *Rollout) shed() {
	r.Config = nil
	for i := range r.Members {
		x := &r.Members[i]
		x.Config = nil
		for j := range x.Changes {
			x.Changes[j].Have, x.Changes[j].Want = nil, nil
		}
	}
}

// rolloutRun is what a rollout running on its goroutine is told: to pause,
// to abort; wake has it look.
type rolloutRun struct {
	wake  chan struct{}
	pause bool
	abort bool
}

// rolloutsState is the rollouts the manager keeps, under m.mu.
type rolloutsState struct {
	list []*Rollout // oldest first
	// runs are the rollouts whose goroutines run, by ID.
	runs map[string]*rolloutRun
	wg   sync.WaitGroup
}

// errNoRollout is a rollout there is none of.
type errNoRollout string

func (e errNoRollout) Error() string { return "no rollout " + strconv.Quote(string(e)) + " is kept" }

// loadRollouts takes the rollouts the store kept: one under way when the
// manager stopped is paused where it stopped, and holds its members again;
// one being planned is expired.
func (m *Manager) loadRollouts(list []Rollout) {
	now := m.now().UTC()
	for i := range list {
		r := list[i]
		switch r.State {
		case RolloutPlanning:
			r.State, r.Reason, r.Ended = RolloutExpired, "the manager restarted while it was planned: plan it again", now
			r.shed()
		case RolloutRunning:
			r.State, r.Reason, r.NextAt = RolloutPaused, "the manager restarted while it ran: it was paused where it stopped; resume it", time.Time{}
		case RolloutRollingBack:
			r.State, r.Reason = RolloutFailed, "the manager restarted while it rolled back: roll it back again"
			r.Ended = now
		}
		r.count()
		m.ro.list = append(m.ro.list, &r)
		if r.holds() {
			m.holdLocked(&r)
		}
	}
}

// holdLocked marks r's members as held, against operations and other
// rollouts. m.mu is held.
func (m *Manager) holdLocked(r *Rollout) {
	if m.ops.busy == nil {
		m.ops.busy = map[string]string{}
	}
	for _, x := range r.Members {
		if x.Status != MemberExcluded && x.Status != MemberUnchanged && x.Status != MemberError && m.ops.busy[x.Node] == "" {
			m.ops.busy[x.Node] = "rollout " + r.ID
		}
	}
}

// releaseLocked lets r's members go. m.mu is held.
func (m *Manager) releaseLocked(r *Rollout) {
	for name, id := range m.ops.busy {
		if id == "rollout "+r.ID {
			delete(m.ops.busy, name)
		}
	}
}

// storedRollouts returns the rollouts as the store keeps them, configs and
// all. m.mu is held.
func (m *Manager) storedRolloutsLocked() []Rollout {
	out := make([]Rollout, 0, len(m.ro.list))
	for _, r := range m.ro.list {
		out = append(out, r.copy(true, true))
	}
	return out
}

// expireLocked expires the plans not started within PlanTTL, and drops the
// oldest finished rollouts past MaxKeptRollouts. m.mu is held.
func (m *Manager) expireLocked() {
	now := m.now()
	for _, r := range m.ro.list {
		if r.State == RolloutPlanned && now.Sub(r.Planned) > PlanTTL {
			r.State, r.Reason, r.Ended = RolloutExpired, "not started within "+PlanTTL.String()+" of its plan: plan it again", now.UTC()
			r.shed()
		}
	}
	for len(m.ro.list) > MaxKeptRollouts {
		i := slices.IndexFunc(m.ro.list, func(r *Rollout) bool { return r.finished() })
		if i < 0 {
			return // MaxActiveRollouts < MaxKeptRollouts keeps this short
		}
		m.ro.list = slices.Delete(m.ro.list, i, i+1)
	}
}

// rolloutLocked returns the rollout id. m.mu is held.
func (m *Manager) rolloutLocked(id string) *Rollout {
	for _, r := range m.ro.list {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// Rollouts returns the rollouts kept, the latest first, without their
// changes' values.
func (m *Manager) Rollouts() []Rollout {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	out := make([]Rollout, 0, len(m.ro.list))
	for i := len(m.ro.list) - 1; i >= 0; i-- {
		out = append(out, m.ro.list[i].copy(false, false))
	}
	return out
}

// RolloutByID returns the rollout id, with its changes' values while it
// keeps them; never its members' configs.
func (m *Manager) RolloutByID(id string) (Rollout, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	r := m.rolloutLocked(id)
	if r == nil {
		return Rollout{}, false
	}
	return r.copy(true, false), true
}

// saveRollouts writes the document with the rollouts as they are now, and
// entries added to the audit log; the members' ETag stays. It writes a
// file: off a fib worker.
func (m *Manager) saveRollouts(entries ...AuditEntry) {
	if err := m.appendAudit(entries); err != nil {
		m.logf("cluster: the rollouts were not saved: %v", err)
	}
}

// ---- Planning ----

// rolloutTargets returns the members req names, sorted. m.mu is held.
func (m *Manager) rolloutTargetsLocked(req *RolloutRequest) ([]string, error) {
	var names []string
	pick := func(list []string) error {
		seen := map[string]bool{}
		for _, n := range list {
			if seen[n] {
				continue
			}
			seen[n] = true
			if index(&m.doc, n) < 0 {
				return errNotFound(n)
			}
			names = append(names, n)
		}
		return nil
	}
	group := func() ([]string, error) {
		i := groupIndex(&m.doc, req.Group)
		if i < 0 {
			return nil, errNoGroup(req.Group)
		}
		return m.viewLocked(&m.doc.Groups[i]).Nodes, nil
	}
	switch req.Kind {
	case RolloutTemplate:
		i := templateIndex(&m.doc, req.Template)
		if i < 0 {
			if req.Template == "" {
				return nil, fieldErr("template", "a template's rollout names the template")
			}
			return nil, errNoTemplate(req.Template)
		}
		of := templateViewIn(&m.doc, &m.doc.Templates[i]).Nodes
		if len(req.Nodes) == 0 {
			names = of
			break
		}
		if err := pick(req.Nodes); err != nil {
			return nil, err
		}
		for _, n := range names {
			if !slices.Contains(of, n) {
				return nil, fieldErr("nodes", "%q is in none of the groups of the template %q", n, req.Template)
			}
		}
	case RolloutSync:
		switch {
		case len(req.Nodes) > 0:
			if err := pick(req.Nodes); err != nil {
				return nil, err
			}
		case req.Group != "":
			g, err := group()
			if err != nil {
				return nil, err
			}
			names = g
		default:
			for i := range m.doc.Members {
				names = append(names, m.doc.Members[i].Name)
			}
		}
	case RolloutCopy:
		if req.From == "" {
			return nil, fieldErr("from", "a copy names the member whose parts it copies")
		}
		if index(&m.doc, req.From) < 0 {
			return nil, errNotFound(req.From)
		}
		if len(req.Paths) == 0 {
			return nil, fieldErr("paths", "a copy names the parts it copies, as \"log\" or \"routes[api]\"")
		}
		if len(req.Paths) > MaxTemplateParts {
			return nil, fieldErr("paths", "%d parts, more than %d", len(req.Paths), MaxTemplateParts)
		}
		for _, p := range req.Paths {
			if _, _, err := parsePart(p); err != nil {
				return nil, fieldErr("paths", "%q: %v", p, err)
			}
		}
		switch {
		case len(req.Nodes) > 0:
			if err := pick(req.Nodes); err != nil {
				return nil, err
			}
		case req.Group != "":
			g, err := group()
			if err != nil {
				return nil, err
			}
			names = g
		default:
			return nil, fieldErr("nodes", "a copy names the members it copies to, or a group")
		}
		names = slices.DeleteFunc(names, func(n string) bool { return n == req.From })
	case RolloutPatch:
		switch {
		case len(req.Nodes) > 0:
			if err := pick(req.Nodes); err != nil {
				return nil, err
			}
		case req.Group != "":
			g, err := group()
			if err != nil {
				return nil, err
			}
			names = g
		default:
			return nil, fieldErr("nodes", "a patch names the members it changes, or a group")
		}
	default:
		return nil, fieldErr("kind", "%q is not a kind of rollout: %s, %s, %s or %s", req.Kind, RolloutTemplate, RolloutSync, RolloutCopy, RolloutPatch)
	}
	if len(names) == 0 {
		return nil, fieldErr("nodes", "the rollout has no members to plan")
	}
	slices.Sort(names)
	return names, nil
}

// PlanRollout plans the rollout req, as user, and returns it as it starts
// planning: each member read, its desired config made, its diff taken and
// checked by its own dry run, on a goroutine of its own, at most
// max_concurrency members at once. It writes the store: off a fib worker.
func (m *Manager) PlanRollout(user string, req *RolloutRequest) (Rollout, error) {
	req.Description = strings.TrimSpace(req.Description)
	if len(req.Description) > maxRolloutDescription || !printable(req.Description) {
		return Rollout{}, fieldErr("description", "the description is at most %d bytes of printable characters", maxRolloutDescription)
	}
	m.mu.Lock()
	switch {
	case m.closed:
		m.mu.Unlock()
		return Rollout{}, errClosed
	case m.readOnly != "":
		m.mu.Unlock()
		return Rollout{}, errReadOnly(m.readOnly)
	}
	r := &Rollout{}
	names, err := m.rolloutTargetsLocked(req)
	if err == nil {
		err = m.allowRolloutLocked(user, req.Kind, req.From, names)
	}
	if err == nil {
		err = m.rolloutConfigLocked(user, req, r)
	}
	if err != nil {
		m.mu.Unlock()
		return Rollout{}, err
	}
	m.expireLocked()
	if n := m.activeLocked(); n >= MaxActiveRollouts {
		m.mu.Unlock()
		return Rollout{}, errBusy(fmt.Sprintf("%d rollouts are planned or under way already: start, discard or end one first", n))
	}
	*r = Rollout{ID: newOpID(), Kind: req.Kind, Template: req.Template, Group: req.Group, From: req.From, Paths: slices.Clone(req.Paths),
		Config: r.Config, TemplateBase: r.TemplateBase, Description: req.Description, PlannedBy: user, Source: req.source, Suggested: req.suggested,
		State: RolloutPlanning, Created: m.now().UTC()}
	if req.Kind == RolloutTemplate {
		r.Group = ""
	}
	for _, n := range names {
		r.Members = append(r.Members, RolloutMember{Node: n, Status: MemberPlanning})
	}
	r.count()
	m.ro.list = append(m.ro.list, r)
	m.ro.wg.Add(1)
	out := r.copy(true, false)
	m.mu.Unlock()
	m.saveRollouts(AuditEntry{At: m.now().UTC(), User: user, Action: ActionRolloutPlan, Rollout: r.ID, Template: r.Template, Group: r.Group,
		Detail: describeRollout(r, len(names)), Nodes: names, Outcome: AuditOK})
	m.logf("cluster: %q planned the rollout %s (%s) of %s", user, r.ID, r.Kind, strings.Join(names, ", "))
	go m.planRollout(r)
	return out, nil
}

// allowRolloutLocked checks that user may roll out a change of kind to the
// members names: a template's or a sync, an operator of each; a copy of
// parts of the member from, or a patch, which apply config of no template,
// an admin of each, who reads from. m.mu is held.
func (m *Manager) allowRolloutLocked(user, kind, from string, names []string) error {
	if kind != RolloutCopy && kind != RolloutPatch {
		return m.allowLocked(user, Need{Role: RoleOperator, Nodes: names})
	}
	if err := m.allowLocked(user, Need{Role: RoleAdmin, Nodes: names}); err != nil || kind == RolloutPatch {
		return err
	}
	return m.allowLocked(user, Need{Role: RoleViewer, Nodes: []string{from}})
}

// nodes returns the names of r's members.
func (r *Rollout) nodes() []string {
	out := make([]string, len(r.Members))
	for i := range r.Members {
		out[i] = r.Members[i].Node
	}
	return out
}

// activeLocked counts the rollouts that are active. m.mu is held.
func (m *Manager) activeLocked() int {
	n := 0
	for _, r := range m.ro.list {
		if r.active() {
			n++
		}
	}
	return n
}

// describeRollout says what a rollout changes, for the audit log.
func describeRollout(r *Rollout, members int) string {
	var s string
	switch r.Kind {
	case RolloutTemplate:
		s = "the template " + r.Template
		if len(r.Config) > 0 {
			s = "a draft of the template " + r.Template + ", saved when it starts"
		}
	case RolloutSync:
		s = "a sync with the templates"
	case RolloutPatch:
		s = "a patch of " + strings.Join(partNames(r.Config), ", ")
	default:
		s = "a copy of " + strings.Join(r.Paths, ", ") + " from " + r.From
	}
	return clip(fmt.Sprintf("%s, on %d members", s, members), 512)
}

// planRollout plans r's members, then marks it planned. A goroutine of its
// own.
func (m *Manager) planRollout(r *Rollout) {
	defer m.ro.wg.Done()
	var parts []*rendered
	var srcProb *Problem
	if r.Kind == RolloutPatch {
		parts = patchParts(r.Config)
	}
	if r.Kind == RolloutCopy {
		cfg, _, prob := m.fetchConfigSync(r.From)
		if prob != nil {
			srcProb = &Problem{Kind: prob.Kind, Status: prob.Status, At: prob.At,
				Message: clip("the member "+strconv.Quote(r.From)+" the parts are copied from could not be read: "+prob.Message, maxMessage)}
		} else if frag, err := fragmentOf(cfg, r.Paths); err != nil {
			srcProb = &Problem{Kind: KindRefused, Message: clip(err.Error(), maxMessage), At: m.now().UTC()}
		} else {
			parts = fragmentParts(frag)
		}
	}
	m.mu.Lock()
	names := make([]string, len(r.Members))
	for i, x := range r.Members {
		names[i] = x.Node
	}
	m.mu.Unlock()
	sem := make(chan struct{}, m.o.Settings.Concurrency())
	var wg sync.WaitGroup
	var sizeMu sync.Mutex
	size := 0
	for _, name := range names {
		if srcProb != nil {
			p := *srcProb
			m.setMember(r, name, func(x *RolloutMember) { x.Status, x.Error = MemberError, &p })
			continue
		}
		// Each member's calls are bounded by the calls' timeout, and fail
		// at once when the manager stops.
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			pm := m.planMember(r, name, parts)
			if pm.Status == MemberReady {
				sizeMu.Lock()
				n := len(pm.Config)
				for _, c := range pm.Changes {
					n += len(c.Have) + len(c.Want) + len(c.Path) + 64
				}
				if size+n > rolloutBytes {
					pm.Status, pm.Config, pm.Changes = MemberError, nil, nil
					pm.Error = &Problem{Kind: KindTooLarge, At: m.now().UTC(),
						Message: fmt.Sprintf("the rollout keeps %d bytes of its members' configs and diffs at most: plan fewer members at once", rolloutBytes)}
				} else {
					size += n
				}
				sizeMu.Unlock()
			}
			m.setMember(r, name, func(x *RolloutMember) { *x = pm })
		}()
	}
	wg.Wait()
	if m.stopping() {
		return
	}
	m.mu.Lock()
	r.State, r.Planned = RolloutPlanned, m.now().UTC()
	r.count()
	ready, unchanged, errs := r.Counts[MemberReady], r.Counts[MemberUnchanged], r.Counts[MemberError]
	m.mu.Unlock()
	m.saveRollouts()
	m.logf("cluster: the rollout %s is planned: %d members ready, %d unchanged, %d not ready", r.ID, ready, unchanged, errs)
}

// stopping reports whether the manager stops.
func (m *Manager) stopping() bool {
	select {
	case <-m.stop:
		return true
	default:
		return false
	}
}

// setMember changes r's member name with fn, under m.mu, and counts again.
func (m *Manager) setMember(r *Rollout, name string, fn func(*RolloutMember)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if x := r.member(name); x != nil {
		fn(x)
		r.count()
	}
}

// fragmentParts returns the parts of a fragment of a config, as a copy
// renders them: its top-level members whole, and the named lists'
// elements by name.
func fragmentParts(frag *jv) []*rendered {
	var out []*rendered
	for i, k := range frag.keys {
		v := frag.vals[i]
		if !managed.IsList(k) || v.kind != 'a' {
			out = append(out, &rendered{member: k, value: v})
			continue
		}
		for _, it := range v.items {
			out = append(out, &rendered{member: k, name: it.name(), value: it})
		}
	}
	return out
}

// planMember plans the member name of r: its desired config, its diff and
// its dry run. It blocks: the planning goroutine's.
func (m *Manager) planMember(r *Rollout, name string, parts []*rendered) RolloutMember {
	pm := RolloutMember{Node: name, Status: MemberError}
	fail := func(p *Problem) RolloutMember {
		if p.At.IsZero() {
			p.At = m.now().UTC()
		}
		pm.Error = p
		return pm
	}
	m.mu.Lock()
	held := m.ops.busy[name]
	m.mu.Unlock()
	if held != "" {
		return fail(&Problem{Kind: KindBusy, Message: "the member is held by " + held + ": one rollout or operation runs on a member at a time"})
	}
	caps, prob := m.capabilitiesSync(name)
	if prob != nil {
		return fail(prob)
	}
	for _, c := range []string{capIfMatch, capDryRun, capVersions} {
		if !slices.Contains(caps, c) {
			return fail(&Problem{Kind: KindUnsupported, Message: "the member does not list " + c + " in its capabilities: it is a fit older than rollouts"})
		}
	}
	var cfg, want *jv
	var etag string
	var sy *MemberSync
	if r.Kind == RolloutCopy || r.Kind == RolloutPatch {
		var p *Problem
		if cfg, etag, p = m.fetchConfigSync(name); p != nil {
			return fail(p)
		}
		own, err := withOwnSecrets(parts, cfg)
		if err != nil {
			return fail(&Problem{Kind: KindRefused, Message: clip(err.Error(), maxMessage)})
		}
		want = desired(cfg, own)
	} else {
		o := m.planSync(r, name)
		if o.desired == nil {
			return fail(o.sync.Error) // set whenever it could not be read
		}
		if o.sync.State == SyncError {
			return fail(&Problem{Kind: KindRefused, Message: clip("its templates do not render for it: "+strings.Join(o.sync.Errors, "; "), maxMessage)})
		}
		cfg, want, etag, sy = o.config, o.desired, o.sync.ConfigETag, &o.sync
		pm.Note = o.sync.Note
	}
	pm.ConfigETag = etag
	if managed.Hash(cfg.bytes()) == managed.Hash(want.bytes()) {
		pm.Status = MemberUnchanged
		return pm
	}
	pm.Changes = rolloutChanges(cfg, want, sy)
	for _, c := range pm.Changes {
		pm.Changed = append(pm.Changed, c.Path)
	}
	body := want.pretty()
	pm.Digest = managed.Hash(body)
	h := stdhttp.Header{"If-Match": {etag}}
	rep, prob := m.callSync(name, "PUT", "/config?dry_run=1", h, body)
	if prob != nil {
		return fail(prob)
	}
	var a struct {
		Version   uint64   `json:"version"`
		ETag      string   `json:"etag"`
		NewETag   string   `json:"new_etag"`
		Unchanged bool     `json:"unchanged"`
		Warnings  []string `json:"warnings"`
	}
	switch {
	case rep.Status == stdhttp.StatusPreconditionFailed:
		return fail(&Problem{Kind: KindRefused, Status: rep.Status, Message: "the member's config changed while it was planned: plan the rollout again"})
	case rep.Status < 200 || rep.Status > 299:
		p := replyProblem(rep, "its dry run")
		pm.Errors = configErrors(rep.Body)
		return fail(p)
	case json.Unmarshal(rep.Body, &a) != nil || a.NewETag == "":
		return fail(&Problem{Kind: KindBadAnswer, Status: rep.Status, Message: "the member's dry run is not the JSON expected"})
	case a.Unchanged:
		pm.Status, pm.Changes, pm.Changed = MemberUnchanged, nil, nil
		return pm
	}
	pm.Status, pm.Version, pm.NewETag, pm.Config = MemberReady, a.Version, a.NewETag, body
	pm.Warnings = clipList(a.Warnings, MaxRolloutErrors, 256)
	return pm
}

// configErrors reads the errors of a config refused, at most
// MaxRolloutErrors.
func configErrors(body []byte) []ConfigError {
	var b struct {
		Errors []ConfigError `json:"errors"`
	}
	_ = json.Unmarshal(body, &b)
	out := []ConfigError{}
	for _, e := range b.Errors {
		if len(out) == MaxRolloutErrors {
			break
		}
		out = append(out, ConfigError{Path: clip(e.Path, 256), Message: clip(e.Message, 512)})
	}
	return out
}

// clipList bounds a list of strings, and each of them.
func clipList(list []string, n, each int) []string {
	var out []string
	for _, s := range list {
		if len(out) == n {
			break
		}
		out = append(out, clip(s, each))
	}
	return out
}

// rolloutChanges are the parts of have that want changes, secrets masked;
// sy, when given, says each part's sync state.
func rolloutChanges(have, want *jv, sy *MemberSync) []Change {
	var out []Change
	for _, p := range compareParts(have, want) {
		if p.State == CompareSame {
			continue
		}
		// A rollout adds parts or changes them; it never removes one.
		c := Change{Path: p.Path, State: p.State, Have: p.A, Want: p.B}
		if p.State == CompareOnlyB {
			c.State = "added"
		}
		if sy != nil {
			for _, ps := range sy.Parts {
				if ps.Path == p.Path {
					c.Sync, c.Reason, c.Template = ps.State, ps.Reason, ps.Template
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// ---- Calls that wait, for the rollouts' goroutines ----

// callSync is call, waited for: on a rollout's goroutine, never a fib
// worker's. Its answer is whatever its status.
func (m *Manager) callSync(name, method, path string, header stdhttp.Header, body []byte) (*Reply, *Problem) {
	type res struct {
		r *Reply
		p *Problem
	}
	ch := make(chan res, 1)
	err := m.Call(name, method, path, header, body, func(r *Reply, p *Problem) { ch <- res{r, p} })
	if err != nil {
		return nil, &Problem{Kind: KindUnreachable, Message: err.Error(), At: m.now().UTC()}
	}
	x := <-ch
	return x.r, x.p
}

// getSync is a GET waited for, its answer read as JSON into v when 2xx, or
// turned into a Problem.
func (m *Manager) getSync(name, path string, v any) *Problem {
	rep, prob := m.callSync(name, "GET", path, nil, nil)
	if prob != nil {
		return prob
	}
	if rep.Status < 200 || rep.Status > 299 {
		return replyProblem(rep, "GET "+path)
	}
	if err := json.Unmarshal(rep.Body, v); err != nil {
		return &Problem{Kind: KindBadAnswer, Status: rep.Status, Message: "the member's " + path + " is not the JSON expected: " + err.Error(), At: m.now().UTC()}
	}
	return nil
}

// capabilitiesSync is capabilities, waited for.
func (m *Manager) capabilitiesSync(name string) ([]string, *Problem) {
	type res struct {
		c []string
		p *Problem
	}
	ch := make(chan res, 1)
	m.capabilities(name, func(c []string, p *Problem) { ch <- res{c, p} })
	x := <-ch
	return x.c, x.p
}

// fetchConfigSync is fetchConfig, waited for.
func (m *Manager) fetchConfigSync(name string) (*jv, string, *Problem) {
	type res struct {
		c *jv
		e string
		p *Problem
	}
	ch := make(chan res, 1)
	m.fetchConfig(name, func(c *jv, e string, p *Problem) { ch <- res{c, e, p} })
	x := <-ch
	return x.c, x.e, x.p
}

// syncSync is syncMember without telling the member, waited for.
func (m *Manager) syncSync(name string) *syncOut {
	ch := make(chan *syncOut, 1)
	m.syncMember(name, false, func(o *syncOut) { ch <- o })
	return <-ch
}
