package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
)

// The propose tools. Each adds a step to the conversation's one pending
// proposal; none changes anything. A person with the level apply applies
// the proposal from the panel, with a second confirm (apply.go).
const (
	ToolConfigPropose    = "config_propose"
	ToolReconcilePropose = "reconcile_propose"
	ToolRollbackPropose  = "rollback_propose"
	ToolPurgePropose     = "cache_purge_propose"
)

// The kinds of a proposal's config steps. A proposal's config steps are of
// one kind, applied as one change through its own endpoint: patches merged
// into one PUT /config/text, the fixes of every reconcile step in one POST
// /config/reconcile, or one POST /rollback/{n}. Cache purges go with any,
// after the config.
const (
	KindPatch     = "patch"
	KindReconcile = "reconcile"
	KindRollback  = "rollback"
)

// The bounds of a proposal.
const (
	// MaxProposalSteps bounds a proposal's steps.
	MaxProposalSteps = 32
	// MaxProposalBytes bounds its steps' arguments together, and so one
	// step's: a change to a large member is made with ops.
	MaxProposalBytes = 256 << 10
	// maxSummaryBytes bounds a step's summary.
	maxSummaryBytes = 300
	// maxPurgeItems bounds a purge's URLs, and its tags.
	maxPurgeItems = 100
	// maxDiffBytes bounds each format's diff.
	maxDiffBytes = 256 << 10
)

// diffFormats are the formats a proposal's diff is shown in.
var diffFormats = []string{"conf", "yaml", "json"}

// Proposal is a conversation's pending change: the steps the model
// proposed, on the config whose ETag and version they were checked on.
// It is kept with its conversation until it is applied or discarded.
type Proposal struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
	// Revision counts its changes: a step added, or a move to a newer
	// config. An apply names the revision it confirmed.
	Revision    int            `json:"revision"`
	BaseVersion uint64         `json:"base_version"`
	BaseETag    string         `json:"base_etag"`
	Steps       []ProposalStep `json:"steps"`
	// Valid and Problems are the check of the whole when it last changed,
	// for the prompt; the view checks it again.
	Valid    bool `json:"valid"`
	Problems int  `json:"problems,omitempty"`
	// Error is why the last apply failed, until the next.
	Error string `json:"error,omitempty"`
}

// ProposalStep is a propose tool's call: its tool, its summary and its
// arguments, as the model gave them.
type ProposalStep struct {
	Tool    string          `json:"tool"`
	Summary string          `json:"summary"`
	Args    json.RawMessage `json:"args"`
	At      int64           `json:"at"`
}

func (p *Proposal) clone() *Proposal {
	if p == nil {
		return nil
	}
	q := *p
	q.Steps = slices.Clone(p.Steps)
	return &q
}

func (p *Proposal) bytes() int {
	n := 0
	for _, s := range p.Steps {
		n += len(s.Args) + len(s.Summary) + 64
	}
	return n
}

// stepArgs are the arguments of every propose tool; each reads its own.
type stepArgs struct {
	Summary string `json:"summary"`
	// config_propose
	Members json.RawMessage `json:"members"`
	Ops     []patchOp       `json:"ops"`
	// reconcile_propose
	Fixes []FixRef `json:"fixes"`
	// rollback_propose
	Version uint64 `json:"version"`
	// cache_purge_propose
	Zone string   `json:"zone"`
	URLs []string `json:"urls"`
	Tags []string `json:"tags"`
	All  bool     `json:"all"`
}

// patchOp is an operation of config_propose: add, replace or remove at a
// JSON pointer.
type patchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// FixRef is a Consistency fix chosen: its difference's ID and the action.
type FixRef struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// PurgeStep is a cache purge a proposal makes after the config.
type PurgeStep struct {
	Zone string   `json:"zone,omitempty"`
	URLs []string `json:"urls,omitempty"`
	Tags []string `json:"tags,omitempty"`
	All  bool     `json:"all,omitempty"`
}

const summarySchema = `"summary":{"type":"string","description":"what this step does, in one short sentence the person reads before applying"}`

// proposeTools returns the propose tools.
func proposeTools() []*Tool {
	note := " The step joins the conversation's one pending proposal, which fit checks as a whole and shows the person as a diff; only the person applies it, in the panel, after a second confirm. You cannot apply it."
	return []*Tool{
		{Name: ToolConfigPropose, Group: GroupChange, Level: config.AssistantPropose,
			Description: "Propose a change to fit's config: top-level members set whole (members), or operations at JSON pointers (ops: add, replace or remove; /routes/- appends). Read the config first and change only what was asked; prefer ops for a part of a large member. Values shown as [redacted] may be written back where they were, never elsewhere; secrets and admin cannot be proposed." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"members":{"type":"object","description":"top-level members set to these values; null removes one"},"ops":{"type":"array","items":{"type":"object","properties":{"op":{"type":"string","enum":["add","replace","remove"]},"path":{"type":"string","description":"a JSON pointer, as /routes/0/handle or /upstreams/-"},"value":{"description":"the value of an add or a replace"}},"required":["op","path"]}}},"required":["summary"]}`),
			Run:         toolPropose(ToolConfigPropose)},
		{Name: ToolReconcilePropose, Group: GroupChange, Level: config.AssistantPropose,
			Description: "Propose fixes of the Consistency check's differences, by each difference's id and the action of one of its fixes, as consistency_check lists them. Reconcile steps are applied together, and not with config_propose or rollback_propose steps: propose those in a proposal of their own." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"fixes":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"action":{"type":"string"}},"required":["id","action"]},"minItems":1}},"required":["summary","fixes"]}`),
			Run:         toolPropose(ToolReconcilePropose)},
		{Name: ToolRollbackPropose, Group: GroupChange, Level: config.AssistantPropose,
			Description: "Propose applying a kept config version again, as a new version (versions_diff lists them and what changed). A rollback is the proposal's only config step." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"version":{"type":"integer","description":"the kept version to apply again"}},"required":["summary","version"]}`),
			Run:         toolPropose(ToolRollbackPropose)},
		{Name: ToolPurgePropose, Group: GroupChange, Level: config.AssistantPropose,
			Description: "Propose removing responses the cache keeps, after the proposal's config change: in one zone (every zone when left out), by absolute URL, by tag (Cache-Tag, Surrogate-Key), or all of them." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"zone":{"type":"string"},"urls":{"type":"array","items":{"type":"string"}},"tags":{"type":"array","items":{"type":"string"}},"all":{"type":"boolean"}},"required":["summary"]}`),
			Run:         toolPropose(ToolPurgePropose)},
	}
}

// current is the running base config, as GET /config answers it.
type current struct {
	data    []byte
	etag    string
	version uint64
}

func (a *Assistant) currentConfig(ctx context.Context) (*current, error) {
	ans, err := a.api(ctx, "GET", "/config", "", "", nil)
	if err != nil {
		return nil, err
	}
	if ans.status != 200 {
		return nil, fmt.Errorf("reading the config: the Admin API answered %d", ans.status)
	}
	v, _ := strconv.ParseUint(ans.header.Get("Fit-Config-Version"), 10, 64)
	return &current{data: ans.body, etag: ans.header.Get("ETag"), version: v}, nil
}

// built is a proposal checked on the running config: what its steps make
// of it, each step's error, and the check of the whole.
type built struct {
	cur      *current
	base     *jobject
	after    *jobject
	kind     string
	fixes    []FixRef
	rollback uint64
	purges   []PurgeStep
	stepErrs []string
	changed  []string
	valid    bool
	errors   []validationError

	red    *redactor
	check  []checkDiff     // GET /config/check's, read once
	zones  map[string]bool // GET /cache's, read once
	fixAt  []int           // the step each fix is from
	reread error
}

// checkDiff is a difference of GET /config/check, as far as a fix needs.
type checkDiff struct {
	ID    string     `json:"id"`
	Fixes []checkFix `json:"fixes"`
}

// checkFix is a fix a difference offers, and the patch it makes.
type checkFix struct {
	Action string `json:"action"`
	Patch  *struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"patch"`
}

// build checks steps on the running config. Its error is only the config
// not being read; what is wrong with a step is that step's error.
func (a *Assistant) build(ctx context.Context, red *redactor, steps []ProposalStep) (*built, error) {
	cur, err := a.currentConfig(ctx)
	if err != nil {
		return nil, err
	}
	tree, err := decodeTree(cur.data)
	base, ok := tree.(*jobject)
	if err != nil || !ok {
		return nil, errors.New("the running config is not a JSON object")
	}
	b := &built{cur: cur, base: base, after: cloneTree(base).(*jobject), stepErrs: make([]string, len(steps)), red: red}
	for i, st := range steps {
		if err := b.step(a, ctx, i, st); err != nil {
			b.stepErrs[i] = err.Error()
		}
	}
	if b.kind == KindReconcile {
		b.applyFixes()
	}
	for _, k := range b.after.keys {
		if v, ok := b.base.get(k); !ok || !equalTree(v, b.after.vals[k]) {
			b.changed = append(b.changed, k)
		}
	}
	for _, k := range b.base.keys {
		if _, ok := b.after.get(k); !ok {
			b.changed = append(b.changed, k)
		}
	}
	b.valid = !slices.ContainsFunc(b.stepErrs, func(e string) bool { return e != "" })
	switch {
	case b.kind == KindPatch && len(b.changed) == 0 && len(b.purges) == 0:
		b.valid = false
		b.errors = append(b.errors, validationError{Msg: "the proposal changes nothing"})
	case b.kind != "" && b.valid && len(b.changed) > 0:
		if err := a.validate(encodeTree(b.after)); err != nil {
			b.valid = false
			// An error that is not config.Errors is one, without a path.
			errs := config.AsErrors(err)
			for i, e := range errs {
				if i == 50 {
					b.errors = append(b.errors, validationError{Msg: fmt.Sprintf("and %d more", len(errs)-50)})
					break
				}
				b.errors = append(b.errors, validationError{Path: e.Path, Msg: red.text(e.Msg)})
			}
		}
	}
	return b, nil
}

// setKind makes kind the proposal's config steps' kind, refusing another.
func (b *built) setKind(kind string) error {
	switch {
	case b.kind == "":
		b.kind = kind
	case b.kind != kind:
		return fmt.Errorf("the proposal's config steps are a %s already; a %s cannot join them, as each is applied as one change through its own endpoint: discard the proposal, or propose this after it is applied", b.kind, kind)
	case kind == KindRollback:
		return errors.New("the proposal rolls back to a version already; it holds one rollback")
	}
	return nil
}

// step checks step i and adds what it does to b.
func (b *built) step(a *Assistant, ctx context.Context, i int, st ProposalStep) error {
	var args stepArgs
	if err := json.Unmarshal(st.Args, &args); err != nil {
		return fmt.Errorf("the arguments are not what %s takes: %v", st.Tool, err)
	}
	if s := strings.TrimSpace(args.Summary); s == "" || len(s) > maxSummaryBytes {
		return fmt.Errorf("summary is 1 to %d bytes saying what the step does", maxSummaryBytes)
	}
	switch st.Tool {
	case ToolConfigPropose:
		if err := b.setKind(KindPatch); err != nil {
			return err
		}
		return b.patch(args)
	case ToolReconcilePropose:
		if err := b.setKind(KindReconcile); err != nil {
			return err
		}
		return b.reconcile(a, ctx, i, args)
	case ToolRollbackPropose:
		if err := b.setKind(KindRollback); err != nil {
			return err
		}
		return b.rollbackTo(a, ctx, args.Version)
	case ToolPurgePropose:
		return b.purge(a, ctx, args)
	}
	return fmt.Errorf("%s is not a propose tool", st.Tool)
}

// patch applies config_propose's members and ops to b.after, all or none.
func (b *built) patch(args stepArgs) error {
	if len(args.Members) == 0 && len(args.Ops) == 0 {
		return errors.New("give members, ops or both")
	}
	next := cloneTree(b.after).(*jobject)
	if len(args.Members) > 0 {
		tree, err := decodeTree(args.Members)
		members, ok := tree.(*jobject)
		if err != nil || !ok {
			return errors.New("members is not a JSON object")
		}
		for _, k := range members.keys {
			if err := forbidden([]string{k}); err != nil {
				return err
			}
			v := members.vals[k]
			if v == nil {
				next.del(k)
				continue
			}
			if v, err = b.guard([]string{k}, v); err != nil {
				return err
			}
			next.set(k, v)
		}
	}
	for n, op := range args.Ops {
		tokens, err := parsePointer(op.Path)
		if err != nil {
			return fmt.Errorf("ops[%d]: %v", n, err)
		}
		if len(tokens) == 0 {
			return fmt.Errorf("ops[%d]: the path names the whole config; set top-level members with members", n)
		}
		if err := forbidden(tokens); err != nil {
			return fmt.Errorf("ops[%d]: %v", n, err)
		}
		var v any
		switch op.Op {
		case opAdd, opReplace:
			tree, err := decodeTree(op.Value)
			if len(op.Value) == 0 || err != nil {
				return fmt.Errorf("ops[%d]: %s needs a value", n, op.Op)
			}
			if v, err = b.guard(tokens, tree); err != nil {
				return fmt.Errorf("ops[%d]: %v", n, err)
			}
		case opRemove:
		default:
			return fmt.Errorf("ops[%d]: op is add, replace or remove, not %q", n, op.Op)
		}
		root, err := patchTree(next, op.Op, tokens, v, false)
		if err != nil {
			return fmt.Errorf("ops[%d]: %v", n, err)
		}
		next = root.(*jobject)
	}
	b.after = next
	return nil
}

// forbidden refuses a path into admin: the members no tool reads are in
// it, and it changes only when fit restarts, so no proposal touches it.
func forbidden(tokens []string) error {
	if tokens[0] == "admin" {
		return errors.New("admin is out of the assistant's reach: it holds the console's users, the store and the Admin API's own settings, and changes only when fit restarts; the person changes it in the config file")
	}
	return nil
}

// isRedacted reports whether s holds what redaction put in place of a
// secret.
func isRedacted(s string) bool { return strings.Contains(s, "[redacted") }

// guard returns v, to be written at tokens, with each redacted value put
// back as the config has it at the same place, and refuses a value that is
// redacted with no such secret there, and a secret the config does not
// already have there: the assistant never writes a secret.
func (b *built) guard(tokens []string, v any) (any, error) {
	secret := slices.ContainsFunc(tokens, secretName)
	baseV, _ := lookupTree(b.base, tokens)
	return b.guardAt(tokens, v, baseV, secret)
}

func (b *built) guardAt(tokens []string, v, baseV any, secret bool) (any, error) {
	switch t := v.(type) {
	case *jobject:
		bo, _ := baseV.(*jobject)
		out := newObject()
		for _, k := range t.keys {
			var bv any
			if bo != nil {
				bv = bo.vals[k]
			}
			x, err := b.guardAt(append(tokens, k), t.vals[k], bv, secret || secretName(k))
			if err != nil {
				return nil, err
			}
			out.set(k, x)
		}
		return out, nil
	case []any:
		ba, _ := baseV.([]any)
		out := make([]any, len(t))
		for i, x := range t {
			var bv any
			if i < len(ba) {
				bv = ba[i]
			}
			y, err := b.guardAt(append(tokens, strconv.Itoa(i)), x, bv, secret)
			if err != nil {
				return nil, err
			}
			out[i] = y
		}
		return out, nil
	case string:
		bs, ok := baseV.(string)
		if isRedacted(t) {
			if ok && bs != "" && (t == Redacted || b.red.text(bs) == t) {
				return bs, nil
			}
			return nil, fmt.Errorf("%s is %q, a redacted value the config does not have there: leave that member as it is, and change what is around it with ops", pointerOf(tokens), t)
		}
		if secret && t != "" && t != bs {
			return nil, fmt.Errorf("%s holds a secret, which the assistant never writes: leave it out, or as it is, and the person sets it on the page", pointerOf(tokens))
		}
	}
	return v, nil
}

// reconcile adds reconcile_propose's fixes, checked against the
// differences GET /config/check lists now.
func (b *built) reconcile(a *Assistant, ctx context.Context, i int, args stepArgs) error {
	if len(args.Fixes) == 0 {
		return errors.New("name at least one fix")
	}
	if b.check == nil && b.reread == nil {
		body, err := a.apiJSON(ctx, "GET", "/config/check", "", nil)
		var res struct {
			Differences []checkDiff `json:"differences"`
		}
		if err == nil {
			err = json.Unmarshal(body, &res)
		}
		b.check, b.reread = res.Differences, err
		if b.check == nil {
			b.check = []checkDiff{}
		}
	}
	if b.reread != nil {
		return fmt.Errorf("the Consistency check could not be read: %v", b.reread)
	}
	for _, f := range args.Fixes {
		if slices.ContainsFunc(b.fixes, func(x FixRef) bool { return x.ID == f.ID }) {
			return fmt.Errorf("the difference %q is fixed by the proposal already", f.ID)
		}
		d := slices.IndexFunc(b.check, func(x checkDiff) bool { return x.ID == f.ID })
		if d < 0 {
			return fmt.Errorf("the Consistency check has no difference %q now: check again", f.ID)
		}
		k := slices.IndexFunc(b.check[d].Fixes, func(x checkFix) bool { return x.Action == f.Action })
		if k < 0 {
			return fmt.Errorf("the difference %q offers no %q fix", f.ID, f.Action)
		}
		if p := b.check[d].Fixes[k].Patch; p != nil {
			if tokens, err := parsePointer(p.Path); err == nil && len(tokens) > 0 {
				if err := forbidden(tokens); err != nil {
					return fmt.Errorf("the fix of %q: %v", f.ID, err)
				}
			}
		}
	}
	for _, f := range args.Fixes {
		b.fixes = append(b.fixes, f)
		b.fixAt = append(b.fixAt, i)
	}
	return nil
}

// applyFixes applies the fixes' patches to b.after as POST
// /config/reconcile does: adds and replaces in order, then the removals,
// the deepest and the highest index first. A fix that does not apply is
// its step's error.
func (b *built) applyFixes() {
	type pending struct {
		op     string
		tokens []string
		value  json.RawMessage
		step   int
	}
	var sets, dels []pending
	for n, f := range b.fixes {
		d := b.check[slices.IndexFunc(b.check, func(x checkDiff) bool { return x.ID == f.ID })]
		for _, fx := range d.Fixes {
			if fx.Action != f.Action || fx.Patch == nil {
				continue
			}
			tokens, err := parsePointer(fx.Patch.Path)
			if err != nil || len(tokens) == 0 {
				b.stepErrs[b.fixAt[n]] = fmt.Sprintf("the fix of %q has no path to change", f.ID)
				continue
			}
			p := pending{op: fx.Patch.Op, tokens: tokens, value: fx.Patch.Value, step: b.fixAt[n]}
			if p.op == "delete" {
				dels = append(dels, p)
			} else {
				sets = append(sets, p)
			}
		}
	}
	slices.SortStableFunc(dels, func(x, y pending) int {
		for i := range min(len(x.tokens), len(y.tokens)) {
			if x.tokens[i] == y.tokens[i] {
				continue
			}
			xi, xerr := strconv.Atoi(x.tokens[i])
			yi, yerr := strconv.Atoi(y.tokens[i])
			if xerr == nil && yerr == nil {
				return yi - xi
			}
			return strings.Compare(x.tokens[i], y.tokens[i])
		}
		return len(y.tokens) - len(x.tokens)
	})
	var root any = b.after
	for _, p := range append(sets, dels...) {
		var v any
		op, last := opRemove, p.tokens[len(p.tokens)-1]
		if p.op != "delete" {
			tree, err := decodeTree(p.value)
			if err != nil {
				b.stepErrs[p.step] = fmt.Sprintf("the fix for %s has no value", pointerOf(p.tokens))
				continue
			}
			v, op = tree, opAdd
			// The Admin API sets an array's element in place.
			if _, err := strconv.Atoi(last); err == nil {
				op = opReplace
			}
		}
		// Only an add at the end of an array the config leaves out makes
		// it, as the Admin API does.
		next, err := patchTree(cloneTree(root), op, p.tokens, v, p.op == "add" && last == "-")
		if err != nil {
			b.stepErrs[p.step] = err.Error()
			continue
		}
		root = next
	}
	b.after = root.(*jobject)
}

// rollbackTo makes b.after the config kept as version n.
func (b *built) rollbackTo(a *Assistant, ctx context.Context, n uint64) error {
	if n == 0 {
		return errors.New("version is the kept version to apply again")
	}
	if n == b.cur.version {
		return fmt.Errorf("version %d is the one running", n)
	}
	data, err := a.apiJSON(ctx, "GET", "/versions/"+strconv.FormatUint(n, 10), "", nil)
	if err != nil {
		return err
	}
	tree, err := decodeTree(data)
	v, ok := tree.(*jobject)
	if err != nil || !ok {
		return fmt.Errorf("version %d is not a JSON object", n)
	}
	was, _ := b.base.get("admin")
	now, _ := v.get("admin")
	if !equalTree(was, now) {
		return fmt.Errorf("version %d has another admin, which the assistant may not propose: the person rolls back to it on the Versions page", n)
	}
	b.rollback, b.after = n, v
	return nil
}

// purge adds cache_purge_propose's purge, checked against the zones the
// cache has now.
func (b *built) purge(a *Assistant, ctx context.Context, args stepArgs) error {
	switch {
	case len(args.URLs) == 0 && len(args.Tags) == 0 && !args.All:
		return errors.New("name urls, tags, or all")
	case len(args.URLs) > maxPurgeItems || len(args.Tags) > maxPurgeItems:
		return fmt.Errorf("at most %d urls and %d tags", maxPurgeItems, maxPurgeItems)
	}
	for _, u := range args.URLs {
		p, err := url.Parse(u)
		if err != nil || p.Host == "" || p.Scheme != "http" && p.Scheme != "https" {
			return fmt.Errorf("%q is not an absolute http or https URL", u)
		}
	}
	if args.Zone != "" {
		if b.zones == nil {
			body, err := a.apiJSON(ctx, "GET", "/cache", "", nil)
			if err != nil {
				return fmt.Errorf("the cache's zones could not be read: %v", err)
			}
			var zones []struct {
				Zone string `json:"zone"`
			}
			_ = json.Unmarshal(body, &zones)
			b.zones = map[string]bool{}
			for _, z := range zones {
				b.zones[z.Zone] = true
			}
		}
		if !b.zones[args.Zone] {
			return fmt.Errorf("no cache uses the zone %q", args.Zone)
		}
	}
	b.purges = append(b.purges, PurgeStep{Zone: args.Zone, URLs: args.URLs, Tags: args.Tags, All: args.All})
	return nil
}

// subset returns the members names of tree.
func subset(tree *jobject, names []string) *jobject {
	sub := newObject()
	for _, n := range names {
		if v, ok := tree.get(n); ok {
			sub.set(n, v)
		}
	}
	return sub
}

// render returns the members names of tree in format, redacted.
func (a *Assistant) render(ctx context.Context, red *redactor, tree *jobject, names []string, format string) (string, error) {
	// A tree encodes as JSON, which redactJSON takes.
	data, _ := red.redactJSON(encodeTree(subset(tree, names)), hiddenMembers)
	if format == "json" {
		var out bytes.Buffer
		_ = json.Indent(&out, data, "", "  ") // redactJSON's output is JSON
		out.WriteByte('\n')
		return out.String(), nil
	}
	text, err := a.apiJSON(ctx, "POST", "/config/render?format="+format, "application/json", data)
	return string(text), err
}

// diff returns the diff of the changed members in format, from the running
// config to what the proposal makes of it, redacted and bounded.
func (a *Assistant) diff(ctx context.Context, b *built, format string) (string, error) {
	if len(b.changed) == 0 {
		return "", nil
	}
	from, err := a.render(ctx, b.red, b.base, b.changed, format)
	if err != nil {
		return "", err
	}
	to, err := a.render(ctx, b.red, b.after, b.changed, format)
	if err != nil {
		return "", err
	}
	d := unifiedDiff(fmt.Sprintf("version %d", b.cur.version), "proposed", from, to)
	d, _ = cutText(d, maxDiffBytes)
	return d, nil
}
