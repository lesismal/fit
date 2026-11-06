package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
)

// errNoConversation is a propose tool called outside a conversation.
var errNoConversation = errors.New("the proposal has no conversation to be kept in")

// toolPropose returns the Run of the propose tool name: the step is
// checked with the proposal's others on the running config and, unless the
// step itself is wrong, added to the conversation's pending proposal, made
// when there is none. A proposal whose whole does not check is kept, with
// its errors, for the next step to mend; it cannot be applied.
func toolPropose(name string) func(c *Call) (string, error) {
	return func(c *Call) (string, error) {
		a := c.a
		if len(c.Args) > MaxProposalBytes {
			return "", fmt.Errorf("the step's arguments are larger than %d bytes: change parts of large members with ops", MaxProposalBytes)
		}
		a.chats.mu.Lock()
		var p *Proposal
		conv := a.userLocked(c.User).conversation(c.Conversation)
		if conv != nil {
			p = conv.Proposal.clone()
		}
		a.chats.mu.Unlock()
		if conv == nil {
			return "", errNoConversation
		}
		now := nowMs()
		if p == nil {
			p = &Proposal{ID: newID(), Created: now}
		}
		switch {
		case len(p.Steps) >= MaxProposalSteps:
			return "", fmt.Errorf("the proposal has its %d steps: let the person apply or discard it first", MaxProposalSteps)
		case p.bytes()+len(c.Args) > MaxProposalBytes:
			return "", fmt.Errorf("the proposal's steps would pass %d bytes: let the person apply or discard it first", MaxProposalBytes)
		}
		var head struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(c.Args, &head)
		p.Steps = append(p.Steps, ProposalStep{Tool: name, Summary: strings.TrimSpace(head.Summary), Args: c.Args, At: now})
		red := a.redactor(c.Settings)
		b, err := a.build(c.Context, red, p.Steps)
		if err != nil {
			return "", err
		}
		if e := b.stepErrs[len(p.Steps)-1]; e != "" {
			return "", errors.New(e + " (the step was not added to the proposal)")
		}
		rebased := p.BaseETag != "" && p.BaseETag != b.cur.etag
		p.BaseETag, p.BaseVersion, p.Updated, p.Error = b.cur.etag, b.cur.version, now, ""
		p.Revision++
		p.Valid, p.Problems = b.valid, len(b.errors)+len(slices.DeleteFunc(slices.Clone(b.stepErrs), func(e string) bool { return e == "" }))

		a.chats.mu.Lock()
		if c.expired != nil && c.expired.Load() {
			a.chats.mu.Unlock()
			return "", errors.New("the step took too long and was not added")
		}
		conv.Proposal, conv.ProposalNote = p, ""
		a.chats.mu.Unlock()
		a.saveChats(c.User)
		if c.proposed != nil {
			c.proposed(p.clone(), b.changed)
		}
		if c.Emit != nil {
			c.Emit(Event{Type: EventProposal, ID: p.ID, Revision: p.Revision, Name: name, Text: red.text(p.Steps[len(p.Steps)-1].Summary)})
		}
		out := map[string]any{"proposal": p.ID, "revision": p.Revision, "steps": len(p.Steps), "base_version": p.BaseVersion,
			"kind": b.kind, "changed": b.changed, "valid": b.valid,
			"note": "The person reviews the proposal in the panel and applies it, or not; you cannot apply it. Add a step to mend an error, or say what the proposal does."}
		if len(b.errors) > 0 {
			out["errors"] = b.errors
		}
		if errs := stepErrors(b.stepErrs); len(errs) > 0 {
			out["step_errors"] = errs
		}
		if rebased {
			out["rebased"] = fmt.Sprintf("the config changed while the proposal was made: its steps are checked on version %d now", b.cur.version)
		}
		if len(b.purges) > 0 {
			out["purges"] = b.purges
		}
		if len(b.media) > 0 {
			out["media"] = a.modelMedia(c.Settings, b.media)
		}
		if b.rollback > 0 {
			out["rollback"] = b.rollback
		}
		if d, err := a.diff(c.Context, b, "json"); err == nil && d != "" {
			out["diff"] = d
		}
		return jsonText(out), nil
	}
}

// stepErrors returns the steps' errors by their number, from 1.
func stepErrors(errs []string) map[string]string {
	out := map[string]string{}
	for i, e := range errs {
		if e != "" {
			out[strconv.Itoa(i+1)] = e
		}
	}
	return out
}

// stepView is a proposal's step as the panel shows it.
type stepView struct {
	ProposalStep
	Error string `json:"error,omitempty"`
}

// ProposalView is a proposal as the panel shows it: checked on the running
// config, with the diff of the members it changes in each format.
type ProposalView struct {
	ID           string            `json:"id"`
	User         string            `json:"user"`
	Conversation string            `json:"conversation"`
	Revision     int               `json:"revision"`
	Created      int64             `json:"created"`
	Updated      int64             `json:"updated"`
	BaseVersion  uint64            `json:"base_version"`
	BaseETag     string            `json:"base_etag"`
	Version      uint64            `json:"version"`
	ETag         string            `json:"etag"`
	Stale        bool              `json:"stale"`
	Kind         string            `json:"kind,omitempty"`
	Steps        []stepView        `json:"steps"`
	Changed      []string          `json:"changed"`
	Diff         map[string]string `json:"diff"`
	Valid        bool              `json:"valid"`
	Errors       []validationError `json:"errors,omitempty"`
	Fixes        []FixRef          `json:"fixes,omitempty"`
	Rollback     uint64            `json:"rollback,omitempty"`
	Purges       []PurgeStep       `json:"purges,omitempty"`
	// Media are the media steps, checked on what runs now, with what uses
	// the items they change; MediaTag is a digest of that, which an apply
	// names to confirm what it was shown.
	Media    []MediaAction `json:"media,omitempty"`
	MediaTag string        `json:"media_tag,omitempty"`
	// Unconverted is what the steps say they did not carry over from a
	// config moved from another server.
	Unconverted []string `json:"unconverted,omitempty"`
	Error       string   `json:"error,omitempty"`
	// Level is the viewer's, and CanApply whether that level applies.
	Level    string `json:"level"`
	CanApply bool   `json:"can_apply"`
}

// viewProposal returns p, of user's conversation conv, as viewer sees it.
func (a *Assistant) viewProposal(ctx context.Context, user, conv string, p *Proposal, viewer string) (*ProposalView, *built, error) {
	b, err := a.build(ctx, a.redactor(a.Settings()), p.Steps)
	if err != nil {
		return nil, nil, err
	}
	return a.viewFrom(ctx, user, conv, p, viewer, b), b, nil
}

// viewFrom returns p as viewer sees it, b being p checked on the running
// config. A diff that cannot be rendered says why in its place.
func (a *Assistant) viewFrom(ctx context.Context, user, conv string, p *Proposal, viewer string, b *built) *ProposalView {
	level, red := a.Settings().Level(viewer), b.red
	v := &ProposalView{ID: p.ID, User: user, Conversation: conv, Revision: p.Revision, Created: p.Created, Updated: p.Updated,
		BaseVersion: p.BaseVersion, BaseETag: p.BaseETag, Version: b.cur.version, ETag: b.cur.etag, Stale: b.kind != "" && p.BaseETag != b.cur.etag,
		Kind: b.kind, Changed: b.changed, Diff: map[string]string{}, Valid: b.valid, Errors: b.errors, Fixes: b.fixes, Rollback: b.rollback,
		Purges: b.purges, Media: b.media, MediaTag: b.mediaTag, Error: p.Error, Level: level, CanApply: level == config.AssistantApply}
	if v.Changed == nil {
		v.Changed = []string{}
	}
	for i, st := range p.Steps {
		st.Args = json.RawMessage(red.text(string(st.Args)))
		st.Summary = red.text(st.Summary)
		v.Steps = append(v.Steps, stepView{ProposalStep: st, Error: b.stepErrs[i]})
	}
	for _, it := range unconverted(p.Steps) {
		v.Unconverted = append(v.Unconverted, red.text(it))
	}
	for _, f := range diffFormats {
		d, err := a.diff(ctx, b, f)
		if err != nil {
			d = "(the diff could not be rendered in " + f + ": " + err.Error() + ")"
		}
		v.Diff[f] = d
	}
	return v
}

// proposalOf returns user's conversation id and a copy of its proposal,
// either nil when there is none.
func (a *Assistant) proposalOf(user, id string) (*Conversation, *Proposal) {
	a.chats.mu.Lock()
	defer a.chats.mu.Unlock()
	conv := a.userLocked(user).conversation(id)
	if conv == nil {
		return nil, nil
	}
	return conv, conv.Proposal.clone()
}

// setProposal makes p user's conversation id's proposal, or none with
// nil, and note what the model is told of the last one, unless the
// proposal there is no longer was, the proposal at revision rev: a step
// added meanwhile, or a discard, came first, and what it left stands.
func (a *Assistant) setProposal(user, id, was string, rev int, p *Proposal, note string) {
	a.chats.mu.Lock()
	conv := a.userLocked(user).conversation(id)
	ok := conv != nil && conv.Proposal != nil && conv.Proposal.ID == was && conv.Proposal.Revision == rev
	if ok {
		conv.Proposal = p
		if note != "" {
			conv.ProposalNote = note
		}
		conv.Updated = nowMs()
	}
	a.chats.mu.Unlock()
	if ok {
		a.saveChats(user)
	}
}

// proposalBody is an apply's or a discard's body: the proposal, its
// revision, the config's ETag and the media tag of what uses its files the
// person confirmed, and, for another user's proposal, whose it is.
type proposalBody struct {
	User     string `json:"user"`
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	ETag     string `json:"etag"`
	MediaTag string `json:"media_tag"`
}

// pendingView is a pending proposal as GET /api/proposals lists it.
type pendingView struct {
	User         string      `json:"user"`
	Conversation string      `json:"conversation"`
	Title        string      `json:"title"`
	ID           string      `json:"id"`
	Revision     int         `json:"revision"`
	Updated      int64       `json:"updated"`
	BaseVersion  uint64      `json:"base_version"`
	Valid        bool        `json:"valid"`
	Steps        []AuditStep `json:"steps"`
}

// proposalsHandler answers GET /api/proposals: the pending proposals of
// the user's conversations, and, for a user who applies, every user's,
// as a list to review.
func (a *Assistant) proposalsHandler(r *console.Request) {
	viewer := r.User
	r.Go(func() {
		s := a.Settings()
		users := []string{viewer}
		if s.Level(viewer) == config.AssistantApply && a.o.Console != nil {
			for _, u := range a.o.Console.Users {
				if u.Username != viewer {
					users = append(users, u.Username)
				}
			}
		}
		list := []pendingView{}
		a.chats.mu.Lock()
		for _, user := range users {
			for _, c := range a.userLocked(user).convs {
				if p := c.Proposal; p != nil {
					pv := pendingView{User: user, Conversation: c.ID, Title: c.Title, ID: p.ID, Revision: p.Revision, Updated: p.Updated,
						BaseVersion: p.BaseVersion, Valid: p.Valid, Steps: []AuditStep{}}
					for _, st := range p.Steps {
						pv.Steps = append(pv.Steps, AuditStep{Tool: st.Tool, Summary: st.Summary})
					}
					list = append(list, pv)
				}
			}
		}
		a.chats.mu.Unlock()
		slices.SortFunc(list, func(x, y pendingView) int { return int(y.Updated - x.Updated) })
		_ = r.JSON(200, map[string]any{"proposals": list, "level": s.Level(viewer)})
	})
}

// owner returns whose proposal a request names: the user's own, or, for a
// user who applies, another's; ok false when the user may not.
func (a *Assistant) owner(viewer, named string) (string, bool) {
	if named == "" || named == viewer {
		return viewer, true
	}
	return named, a.Settings().Level(viewer) == config.AssistantApply
}

// proposalHandler answers GET /api/proposals/{conversation} (?user= for
// another user's), and POST /api/proposals/{conversation}/apply and
// /discard.
func (a *Assistant) proposalHandler(r *console.Request) {
	rest := strings.TrimPrefix(r.Path, "/api/proposals/")
	conv, action, _ := strings.Cut(rest, "/")
	method := r.Request.Method
	switch {
	case method == "GET" && action == "":
		user, ok := a.owner(r.User, r.Request.URL.Query().Get("user"))
		if !ok {
			_ = console.Error(r.Context, 403, "only a user whose level is apply reads another user's proposal")
			return
		}
		viewer := r.User
		r.Go(func() {
			c, p := a.proposalOf(user, conv)
			if c == nil {
				_ = console.Error(r.Context, 404, "no conversation "+conv)
				return
			}
			if p == nil {
				_ = r.JSON(200, map[string]any{"proposal": nil})
				return
			}
			v, _, err := a.viewProposal(context.Background(), user, conv, p, viewer)
			if err != nil {
				_ = console.Error(r.Context, 502, err.Error())
				return
			}
			_ = r.JSON(200, map[string]any{"proposal": v})
		})
	case method == "POST" && (action == "apply" || action == "discard"):
		var body proposalBody
		if !r.Decode(&body) {
			return
		}
		user, ok := a.owner(r.User, body.User)
		if !ok {
			_ = console.Error(r.Context, 403, "only a user whose level is apply acts on another user's proposal")
			return
		}
		viewer := r.User
		r.Go(func() {
			if action == "apply" {
				a.apply(r, viewer, user, conv, body)
			} else {
				a.discard(r, viewer, user, conv, body)
			}
		})
	default:
		_ = console.Error(r.Context, 405, "GET a proposal, or POST its apply or discard")
	}
}

// pending returns user's conversation conv and its proposal id, answering
// r and returning nil when there is none, or the conversation is
// answering.
func (a *Assistant) pending(r *console.Request, user, conv, id string) *Proposal {
	a.chats.mu.Lock()
	c := a.userLocked(user).conversation(conv)
	var p *Proposal
	busy := false
	if c != nil {
		p, busy = c.Proposal.clone(), c.busy != nil
	}
	a.chats.mu.Unlock()
	switch {
	case c == nil:
		_ = console.Error(r.Context, 404, "no conversation "+conv)
	case p == nil || p.ID != id:
		_ = console.Error(r.Context, 409, "the conversation has no proposal "+id+": read it again")
	case busy:
		_ = console.Error(r.Context, 409, "the conversation is answering, and may change its proposal: wait for the answer, or stop it")
	default:
		return p
	}
	return nil
}

// applyResult is an apply's answer.
type applyResult struct {
	Outcome  string         `json:"outcome"`
	Version  uint64         `json:"version,omitempty"`
	ETag     string         `json:"etag,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
	Purged   []purgeResult  `json:"purged,omitempty"`
	Media    []mediaResult  `json:"media,omitempty"`
	Proposal *ProposalView  `json:"proposal,omitempty"`
	Error    string         `json:"error,omitempty"`
	Errors   []apiProblem   `json:"errors,omitempty"`
	Audit    *AuditProposal `json:"-"`
}

// purgeResult is a purge's outcome.
type purgeResult struct {
	PurgeStep
	Purged int    `json:"purged"`
	Error  string `json:"error,omitempty"`
}

// apply answers POST /api/proposals/{conversation}/apply, on the pool: the
// proposal the person confirmed, at the revision and on the config they
// saw, is applied as viewer, as the console's pages apply a change, with
// If-Match: its config steps as one change, then its purges. A proposal
// that is stale is moved to the running config and offered again.
func (a *Assistant) apply(r *console.Request, viewer, user, conv string, body proposalBody) {
	entry := AuditEntry{User: viewer, Owner: user, Kind: AuditApply, Conversation: conv}
	if user == viewer {
		entry.Owner = ""
	}
	refuse := func(status int, outcome, msg string, p *Proposal, v *ProposalView) {
		entry.Outcome, entry.Detail = outcome, msg
		if entry.Proposal == nil {
			entry.Proposal = auditOf(p, nil)
		}
		a.Audit(entry)
		_ = r.JSON(status, applyResult{Outcome: outcome, Error: msg, Proposal: v})
	}
	if level := a.Settings().Level(viewer); level != config.AssistantApply {
		p := a.pending(r, user, conv, body.ID)
		if p != nil {
			refuse(403, OutcomeRefused, fmt.Sprintf("%s's level is %s: only a user whose level is apply applies a proposal", viewer, level), p, nil)
		}
		return
	}
	// One apply at a time, so that two confirms of one proposal do not
	// both run it.
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	p := a.pending(r, user, conv, body.ID)
	if p == nil {
		return
	}
	ctx := context.Background() // an apply runs to its end once begun
	v, b, err := a.viewProposal(ctx, user, conv, p, viewer)
	if err != nil {
		_ = console.Error(r.Context, 502, err.Error())
		return
	}
	entry.Proposal = auditOf(p, b.changed)
	switch {
	case body.Revision != p.Revision:
		refuse(409, OutcomeRefused, "the proposal changed since it was confirmed: review it again", p, v)
		return
	case b.kind != "" && (v.Stale || body.ETag != b.cur.etag):
		// Purges alone are not of a config, and are never stale.
		a.rebase(r, user, conv, p, b, viewer, entry, "")
		return
	case body.MediaTag != b.mediaTag:
		a.rebase(r, user, conv, p, b, viewer, entry, "what uses the files the proposal changes is not what was confirmed: it is offered again with what uses them now; review it and apply again")
		return
	case !b.valid:
		refuse(422, OutcomeRefused, "the proposal does not check, so it cannot be applied", p, v)
		return
	}
	res := applyResult{Outcome: OutcomeApplied}
	if b.kind != "" && (b.kind != KindPatch || len(b.changed) > 0) {
		ans, err := a.applyConfig(ctx, b)
		if err != nil {
			res.Outcome, res.Error = OutcomeFailed, err.Error()
			var ae *apiError
			if errors.As(err, &ae) {
				if ae.Status == 412 {
					a.rebase(r, user, conv, p, b, viewer, entry, "")
					return
				}
				res.Error, res.Errors = ae.Msg, ae.Errors
			}
			p.Error = res.Error
			a.setProposal(user, conv, p.ID, p.Revision, p, "")
			res.Proposal = a.viewFrom(ctx, user, conv, p, viewer, b)
			entry.Outcome, entry.Detail = OutcomeFailed, res.Error
			a.Audit(entry)
			a.o.Logf("console: %q: the assistant's proposal %s was not applied: %s", viewer, p.ID, res.Error)
			_ = r.JSON(422, res)
			return
		}
		res.Version, res.ETag, res.Warnings = ans.Version, ans.ETag, ans.Warnings
	}
	for _, m := range b.media {
		mr := a.applyMedia(ctx, viewer, m)
		if mr.Error != "" {
			res.Outcome = OutcomePartial
		}
		res.Media = append(res.Media, mr)
	}
	for _, pg := range b.purges {
		pr := purgeResult{PurgeStep: pg}
		data, _ := json.Marshal(pg)
		out, err := a.apiJSON(ctx, "POST", "/cache/purge", "application/json", data)
		if err != nil {
			pr.Error = err.Error()
			res.Outcome = OutcomePartial
		} else {
			var n struct {
				Purged int `json:"purged"`
			}
			_ = json.Unmarshal(out, &n)
			pr.Purged = n.Purged
		}
		res.Purged = append(res.Purged, pr)
	}
	note := fmt.Sprintf("%s applied the proposal %s (%d steps)", viewer, p.ID, len(p.Steps))
	if res.Version > 0 {
		note += fmt.Sprintf(" as config version %d", res.Version)
	}
	for _, mr := range res.Media {
		if mr.Error != "" {
			note += fmt.Sprintf("; its step %d (%s) failed: %s", mr.Step, mr.Op, mr.Error)
		}
	}
	for _, pr := range res.Purged {
		if pr.Error != "" {
			note += "; a cache purge failed"
			break
		}
	}
	a.setProposal(user, conv, p.ID, p.Revision, nil, note+".")
	entry.Outcome, entry.Version = res.Outcome, res.Version
	for _, mr := range res.Media {
		if mr.Error != "" && entry.Detail == "" {
			entry.Detail = mr.Error
		}
	}
	for _, pr := range res.Purged {
		if pr.Error != "" {
			entry.Detail = pr.Error
		}
	}
	a.Audit(entry)
	a.o.Logf("console: %q: %s", viewer, note)
	_ = r.JSON(200, res)
}

// rebase moves a stale proposal to the running config, b's, and answers
// 412 with it, to be confirmed again, saying why: msg, or the config's
// change when "".
func (a *Assistant) rebase(r *console.Request, user, conv string, p *Proposal, b *built, viewer string, entry AuditEntry, msg string) {
	p.BaseETag, p.BaseVersion, p.Updated = b.cur.etag, b.cur.version, nowMs()
	p.Revision++
	p.Valid = b.valid
	a.setProposal(user, conv, p.ID, p.Revision-1, p, "")
	v := a.viewFrom(context.Background(), user, conv, p, viewer, b)
	if msg == "" {
		msg = fmt.Sprintf("the config changed since the proposal was checked: it is offered again on version %d; review it and apply again", v.Version)
	}
	entry.Outcome, entry.Detail = OutcomeStale, msg
	a.Audit(entry)
	_ = r.JSON(412, applyResult{Outcome: OutcomeStale, Error: msg, Proposal: v})
}

// applied is a config change's answer.
type applied struct {
	Version  uint64   `json:"version"`
	ETag     string   `json:"etag"`
	Warnings []string `json:"warnings"`
}

// applyConfig applies b's config steps as one change, through the
// endpoint of their kind, with If-Match the ETag they were checked on.
func (a *Assistant) applyConfig(ctx context.Context, b *built) (*applied, error) {
	var method, path string
	var body []byte
	switch b.kind {
	case KindPatch:
		// The members changed, set or removed, the rest left as written.
		method, path = "PUT", "/config/text?format=json&members="+url.QueryEscape(strings.Join(b.changed, ","))
		body = encodeTree(subset(b.after, b.changed))
	case KindReconcile:
		method, path = "POST", "/config/reconcile"
		body, _ = json.Marshal(map[string]any{"fixes": b.fixes})
	default: // KindRollback
		method, path = "POST", "/rollback/"+strconv.FormatUint(b.rollback, 10)
	}
	data, err := a.apiDo(ctx, method, path, "application/json", b.cur.etag, body)
	if err != nil {
		return nil, err
	}
	var out applied
	_ = json.Unmarshal(data, &out)
	return &out, nil
}

// discard answers POST /api/proposals/{conversation}/discard: the
// proposal is dropped, and nothing else changes.
func (a *Assistant) discard(r *console.Request, viewer, user, conv string, body proposalBody) {
	if a.Settings().Level(viewer) == config.AssistantOff {
		_ = console.Error(r.Context, 403, "the assistant is off for "+viewer)
		return
	}
	p := a.pending(r, user, conv, body.ID)
	if p == nil {
		return
	}
	a.setProposal(user, conv, p.ID, p.Revision, nil, fmt.Sprintf("%s discarded the proposal %s.", viewer, p.ID))
	e := AuditEntry{User: viewer, Kind: AuditDiscard, Conversation: conv, Proposal: auditOf(p, nil), Outcome: OutcomeDiscarded}
	if user != viewer {
		e.Owner = user
	}
	a.Audit(e)
	_ = r.JSON(200, map[string]any{"discarded": p.ID})
}
