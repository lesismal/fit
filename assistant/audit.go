package assistant

import (
	"encoding/json"
	"slices"
	"strconv"
	"sync"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/assistant/model"
)

// auditSection is the store's section the audit log is kept in.
const auditSection = "assistant.audit"

// The kinds of an audit entry.
const (
	// AuditChat is an answer of the assistant's that called tools.
	AuditChat = "chat"
	// AuditApply is an apply of a proposal, or a try at one.
	AuditApply = "apply"
	// AuditDiscard is a proposal discarded.
	AuditDiscard = "discard"
	// AuditMedia is a change of a media library's files from the
	// workbench (I139): an upload, a rename, a move, a delete or a
	// sidecar written, its call the change and its arguments.
	AuditMedia = "media"
)

// The outcomes of an apply, beside a chat's, which are its reasons
// (ReasonAnswer and the rest).
const (
	OutcomeApplied   = "applied"   // the config and the purges were applied
	OutcomePartial   = "partial"   // the config was applied, a purge failed
	OutcomeFailed    = "failed"    // the Admin API refused the change: nothing was applied
	OutcomeStale     = "stale"     // the config changed since: offered again on the new one
	OutcomeRefused   = "refused"   // the user's level, or a proposal that does not check
	OutcomeDiscarded = "discarded" // discarded, or deleted with its conversation
)

// The sources of an entry: the panel's; I130's MCP server adds its own.
const SourcePanel = "panel"

// SourceWorkbench is the source of what a user changed on the workbench's
// own pages, outside the assistant: a media library's files (I139).
const SourceWorkbench = "workbench"

// The bounds of an entry, so that the log's bound in entries bounds its
// bytes too.
const (
	maxAuditCalls  = 32
	maxAuditArgs   = 512
	maxAuditDetail = 1024
	maxAuditSteps  = MaxProposalSteps
	maxAuditNames  = 64
)

// AuditEntry is what the assistant did for a user: the tools an answer
// called, or a proposal applied or discarded, and how it ended.
type AuditEntry struct {
	// Seq numbers the entries, from 1, across restarts; At is when, in
	// Unix milliseconds.
	Seq  int64  `json:"seq"`
	At   int64  `json:"at"`
	User string `json:"user"`
	// Source is where it came from: panel, or (I130) mcp, with Token the
	// name of the MCP token.
	Source string `json:"source"`
	Token  string `json:"token,omitempty"`
	Kind   string `json:"kind"`
	// Owner is whose conversation a proposal is of, when not User's: a
	// user who applies acts on others' proposals too.
	Owner        string `json:"owner,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	// Skill is the skill the answer ran (I127).
	Skill     string         `json:"skill,omitempty"`
	Calls     []AuditCall    `json:"calls,omitempty"`
	MoreCalls int            `json:"more_calls,omitempty"`
	Proposal  *AuditProposal `json:"proposal,omitempty"`
	// Outcome is how it ended: a chat's reason, or an apply's outcome;
	// Detail why, when it failed.
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
	// Version is the config version an apply made.
	Version uint64 `json:"version,omitempty"`
}

// AuditCall is a tool call: its tool, its arguments (redacted, cut), and
// whether it failed.
type AuditCall struct {
	Tool  string `json:"tool"`
	Args  string `json:"args,omitempty"`
	Error bool   `json:"error,omitempty"`
}

// AuditProposal is a proposal as the log keeps it: its steps' tools and
// summaries, and the members it changes.
type AuditProposal struct {
	ID          string      `json:"id"`
	Revision    int         `json:"revision"`
	BaseVersion uint64      `json:"base_version,omitempty"`
	Steps       []AuditStep `json:"steps,omitempty"`
	Changed     []string    `json:"changed,omitempty"`
}

// AuditStep is a proposal's step as the log keeps it.
type AuditStep struct {
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
}

// auditOf returns p as the log keeps it, with the members changed.
func auditOf(p *Proposal, changed []string) *AuditProposal {
	if p == nil {
		return nil
	}
	ap := &AuditProposal{ID: p.ID, Revision: p.Revision, BaseVersion: p.BaseVersion}
	for i, s := range p.Steps {
		if i == maxAuditSteps {
			break
		}
		ap.Steps = append(ap.Steps, AuditStep{Tool: s.Tool, Summary: clip(s.Summary, maxSummaryBytes)})
	}
	if len(changed) > maxAuditNames {
		changed = changed[:maxAuditNames]
	}
	ap.Changed = slices.Clone(changed)
	return ap
}

// auditLog is the entries kept, oldest first.
type auditLog struct {
	mu      sync.Mutex
	entries []AuditEntry
	seq     int64
	dirty   bool
}

// keptAudit is the store's section of the log.
type keptAudit struct {
	Seq     int64        `json:"seq"`
	Entries []AuditEntry `json:"entries"`
}

// loadAudit reads the log the store keeps.
func (a *Assistant) loadAudit() {
	if a.o.Store == nil {
		return
	}
	sec := a.o.Store.Section(auditSection)
	if sec == nil {
		return
	}
	var k keptAudit
	if err := json.Unmarshal(sec, &k); err != nil {
		a.o.Logf("assistant: the audit log kept in the store does not load, and is started again: %v", err)
		return
	}
	a.audit.entries, a.audit.seq = k.Entries, k.Seq
	for _, e := range k.Entries {
		a.audit.seq = max(a.audit.seq, e.Seq)
	}
	a.boundAudit()
}

// boundAudit drops the oldest entries past the settings' bound; a.audit.mu
// is held, or the log is not shared yet.
func (a *Assistant) boundAudit() {
	if n := len(a.audit.entries) - a.Settings().Limits.AuditEntries(); n > 0 {
		a.audit.entries = slices.Delete(a.audit.entries, 0, n)
	}
}

// Audit adds e to the audit log, bounded, and has the log written to the
// store. Seq is set, and At when 0.
func (a *Assistant) Audit(e AuditEntry) {
	if e.At == 0 {
		e.At = nowMs()
	}
	if e.Source == "" {
		e.Source = SourcePanel
	}
	if len(e.Calls) > maxAuditCalls {
		e.MoreCalls += len(e.Calls) - maxAuditCalls
		e.Calls = e.Calls[:maxAuditCalls]
	}
	// The entry is the log's own: never changed once in it.
	e.Calls = slices.Clone(e.Calls)
	for i := range e.Calls {
		e.Calls[i].Args = clip(e.Calls[i].Args, maxAuditArgs)
	}
	e.Detail = clip(e.Detail, maxAuditDetail)
	a.audit.mu.Lock()
	a.audit.seq++
	e.Seq = a.audit.seq
	a.audit.entries = append(a.audit.entries, e)
	a.boundAudit()
	a.audit.dirty = true
	a.audit.mu.Unlock()
	select {
	case a.chats.wake <- struct{}{}:
	default:
	}
}

// saveAudit writes the log to the store when it changed; the saver's.
func (a *Assistant) saveAudit() {
	if a.o.Store == nil {
		return
	}
	a.audit.mu.Lock()
	if !a.audit.dirty {
		a.audit.mu.Unlock()
		return
	}
	a.audit.dirty = false
	k := keptAudit{Seq: a.audit.seq, Entries: slices.Clone(a.audit.entries)}
	a.audit.mu.Unlock()
	data, _ := json.Marshal(k)
	if err := a.o.Store.SetSection(auditSection, data); err != nil {
		a.o.Logf("assistant: the audit log is kept in memory only: %v", err)
	}
}

// AuditFilter picks entries: by user, conversation and kind when not "",
// before the entry Before when not 0, the latest Limit.
type AuditFilter struct {
	User, Conversation, Kind string
	Before                   int64
	Limit                    int
}

// The bounds of a page of the log.
const (
	defaultAuditPage = 100
	maxAuditPage     = 500
)

// AuditLog returns the entries f picks, the latest first, and how many the
// log keeps.
func (a *Assistant) AuditLog(f AuditFilter) ([]AuditEntry, int) {
	if f.Limit <= 0 {
		f.Limit = defaultAuditPage
	}
	f.Limit = min(f.Limit, maxAuditPage)
	a.audit.mu.Lock()
	defer a.audit.mu.Unlock()
	out := []AuditEntry{}
	for i := len(a.audit.entries) - 1; i >= 0 && len(out) < f.Limit; i-- {
		e := a.audit.entries[i]
		if f.Before > 0 && e.Seq >= f.Before || f.User != "" && e.User != f.User || f.Conversation != "" && e.Conversation != f.Conversation ||
			f.Kind != "" && e.Kind != f.Kind {
			continue
		}
		out = append(out, e)
	}
	return out, len(a.audit.entries)
}

// auditHandler answers GET /api/audit: the log, the latest first, as
// ?user=, ?conversation=, ?kind=, ?before= and ?limit= pick it. Every
// console user may read it, as the console's log of changes.
func (a *Assistant) auditHandler(r *console.Request) {
	q := r.Request.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries, total := a.AuditLog(AuditFilter{User: q.Get("user"), Conversation: q.Get("conversation"), Kind: q.Get("kind"), Before: before, Limit: limit})
	users := []string{}
	if a.o.Console != nil {
		for _, u := range a.o.Console.Users {
			users = append(users, u.Username)
		}
	}
	_ = r.JSON(200, map[string]any{"entries": entries, "total": total, "max": a.Settings().Limits.AuditEntries(),
		"kept": a.o.Store != nil, "users": users})
}

// auditCall returns a tool call as the log keeps it.
func auditCall(red *redactor, tc model.ToolCall, isErr bool) AuditCall {
	return AuditCall{Tool: tc.Name, Args: clip(red.text(string(tc.Arguments)), maxAuditArgs), Error: isErr}
}
