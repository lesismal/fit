package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/internal/linediff"
)

// The audit log (I141, I148): every cluster action, who did it and when,
// on which members, what changed and how it went, kept in the manager's
// document within MaxAudit entries and MaxAuditBytes, and so across
// restarts; read filtered at GET /api/audit, and exported there as JSON
// lines.

// The outcomes of an AuditEntry.
const (
	// AuditOK: done, on every member it acted on.
	AuditOK = "ok"
	// AuditFailed: refused, or failed on every member.
	AuditFailed = "failed"
	// AuditPartial: done on some members, failed on others.
	AuditPartial = "partial"
	// AuditUnknown: sent to a member and not answered, so it may or may
	// not have been applied.
	AuditUnknown = "unknown"
	// AuditNotSent: the member's login failed, and nothing was sent.
	AuditNotSent = "not_sent"
)

// MaxAuditDiff bounds the diff an entry keeps; its digest is of the whole.
const MaxAuditDiff = 16 << 10

// ActionTest is a member tested from the pages, saved or not.
const ActionTest = "node.test"

// digestOf is the SHA-256 of data, as the audit log writes it.
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// setDiff sets e's diff from before to after, texts, and its digest, the
// whole diff's, the diff clipped to MaxAuditDiff.
func (e *AuditEntry) setDiff(before, after, from, to string) {
	e.setDiffText(linediff.Unified(from, to, before, after))
}

// setDiffText sets e's diff to d, and its digest, the whole diff's, the
// diff clipped to MaxAuditDiff.
func (e *AuditEntry) setDiffText(d string) {
	if d == "" {
		return
	}
	e.Digest = digestOf([]byte(d))
	if len(d) > MaxAuditDiff {
		d, e.Clipped = clip(d, MaxAuditDiff), true
	}
	e.Diff = d
}

// pretty is v as indented JSON, a line a value, for a diff.
func pretty(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ") // the API's views: strings, times, maps
	return string(data) + "\n"
}

// prettyJSON is raw JSON indented, or as it is when it is not JSON.
func prettyJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") != nil {
		return string(raw) + "\n"
	}
	return b.String() + "\n"
}

// memberText is a member as the audit log diffs it: what the API shows,
// its credentials only as set or not.
func memberText(m *Member) string {
	v := m.View()
	v.Created, v.Updated = time.Time{}, time.Time{}
	return pretty(v)
}

// groupText and templateText are a group and a template as the audit log
// diffs them, a template's secrets masked.
func groupText(g *Group) string {
	c := g.clone()
	c.Created, c.Updated = time.Time{}, time.Time{}
	return pretty(c)
}

func templateText(d *document, t *Template) string {
	v := templateViewIn(d, t)
	head := pretty(struct {
		Name        string   `json:"name"`
		Description string   `json:"description,omitempty"`
		Groups      []string `json:"groups"`
		Order       int      `json:"order"`
	}{v.Name, v.Description, v.Groups, v.Order})
	return head + "config:\n" + prettyJSON(v.Config)
}

// rolloutDiff is what a rollout changes on the members names, as the audit
// log keeps it: a unified diff of each part on each member, what it has
// and what it gets, secrets masked.
func rolloutDiff(r *Rollout, names []string) string {
	var b strings.Builder
	for _, x := range r.Members {
		if !slices.Contains(names, x.Node) {
			continue
		}
		for _, c := range x.Changes {
			b.WriteString(linediff.Unified(x.Node+" "+c.Path+" (now)", x.Node+" "+c.Path+" (rolled out)", prettyJSON(c.Have), prettyJSON(c.Want)))
		}
	}
	return b.String()
}

// revision is a revision of the document, as the audit log names it.
func revision(n uint64) string { return "r" + strconv.FormatUint(n, 10) }

// opOutcome is the outcome of an entry with outcomes on members: ok when
// every one is done, failed when none is, partial between.
func opOutcome(outs []AuditOutcome, ok func(string) bool) string {
	done := 0
	for _, o := range outs {
		if ok(o.Status) {
			done++
		}
	}
	switch {
	case len(outs) == 0 || done == len(outs):
		return AuditOK
	case done == 0:
		return AuditFailed
	}
	return AuditPartial
}

// auditNodes are the members e acted on: Node, Nodes and its outcomes'.
func (e *AuditEntry) auditNodes() []string {
	var out []string
	if e.Node != "" {
		out = append(out, e.Node)
	}
	out = append(out, e.Nodes...)
	for _, o := range e.Outcomes {
		out = append(out, o.Node)
	}
	return out
}

// AuditFilter picks entries of the audit log: by user, by a member they
// acted on, by action (whole, as "op.reload", or its kind before the dot,
// as "op"), and by time, since and before until; every field left empty
// picks all.
type AuditFilter struct {
	User   string
	Node   string
	Action string
	Since  time.Time
	Until  time.Time
}

// match reports whether f picks e.
func (f *AuditFilter) match(e *AuditEntry) bool {
	switch {
	case f.User != "" && e.User != f.User:
		return false
	case f.Node != "" && !slices.Contains(e.auditNodes(), f.Node):
		return false
	case f.Action != "" && e.Action != f.Action && !strings.HasPrefix(e.Action, f.Action+"."):
		return false
	case !f.Since.IsZero() && e.At.Before(f.Since):
		return false
	case !f.Until.IsZero() && !e.At.Before(f.Until):
		return false
	}
	return true
}

// AuditWhere returns the entries of the audit log f picks, oldest first.
func (m *Manager) AuditWhere(f AuditFilter) []AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []AuditEntry{}
	for i := range m.doc.Audit {
		if f.match(&m.doc.Audit[i]) {
			out = append(out, m.doc.Audit[i])
		}
	}
	return out
}

// auditBody is GET /api/audit's answer.
type auditBody struct {
	Entries    []AuditEntry `json:"entries"`
	Total      int          `json:"total"`
	MaxEntries int          `json:"max_entries"`
	MaxBytes   int          `json:"max_bytes"`
	Users      []string     `json:"users"`
	Actions    []string     `json:"actions"`
}

// auditRoute answers GET /api/audit: the entries the query picks (user,
// node, action, since, until, the times RFC 3339), oldest first; with
// format=jsonl, as JSON lines, a file to save.
func (m *Manager) auditRoute(r *console.Request) {
	q := r.Request.URL.Query()
	f := AuditFilter{User: q.Get("user"), Node: q.Get("node"), Action: q.Get("action")}
	for _, t := range []struct {
		name string
		to   *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if s := q.Get(t.name); s != "" {
			at, err := time.Parse(time.RFC3339, s)
			if err != nil {
				_ = console.Error(r.Context, 400, t.name+": "+strconv.Quote(s)+" is not an RFC 3339 time, as 2026-11-06T09:00:00Z")
				return
			}
			*t.to = at
		}
	}
	entries := m.AuditWhere(f)
	switch q.Get("format") {
	case "", "json":
	case "jsonl":
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		for i := range entries {
			_ = enc.Encode(&entries[i]) // strings, times and numbers
		}
		r.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="fit-cluster-audit-%s.jsonl"`, m.now().UTC().Format("20060102-150405")))
		_ = console.Write(r.Context, 200, "application/jsonl", b.Bytes())
		return
	default:
		_ = console.Error(r.Context, 400, "format is json or jsonl")
		return
	}
	m.mu.Lock()
	total := len(m.doc.Audit)
	users, actions := map[string]bool{}, map[string]bool{}
	for i := range m.doc.Audit {
		users[m.doc.Audit[i].User] = true
		actions[m.doc.Audit[i].Action] = true
	}
	m.mu.Unlock()
	delete(users, "")
	_ = r.JSON(200, auditBody{Entries: entries, Total: total, MaxEntries: MaxAudit, MaxBytes: MaxAuditBytes,
		Users: sortedKeys(users), Actions: sortedKeys(actions)})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// record adds e to the audit log later, on the writer of the switcher's
// entries: for what happens on a fib worker or a member's answer, which
// must not wait for a file to be written.
func (m *Manager) record(e AuditEntry) {
	h := &m.pass
	h.mu.Lock()
	h.held++
	h.audit = append(h.audit, e)
	start := !h.writing
	h.writing = true
	h.mu.Unlock()
	if start {
		go m.writeAudit()
	}
}
