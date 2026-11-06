package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/lesismal/fit/internal/managed"
)

// Two rollouts carry config of their own (I153), so that a change asked
// for in words (the assistant's rollout_propose, or a person's) is planned
// as any rollout is, each member's diff checked by its own dry run, before
// anything is saved or applied:
//
//   - a template's draft: a template rollout with Config, the template's
//     config as it would be. Each member is rendered with the draft in the
//     template's place; starting the rollout saves the draft as the
//     template, as a cluster-wide admin who may change templates, and then
//     applies it. The template changed since the plan refuses the start.
//   - a patch: parts of a config (top-level members whole, named elements
//     of the lists by name, as a template's) put on members as they are,
//     as a copy does, but from the request rather than a member: config of
//     no template, so an admin's of each member.
//
// A secret in either may be SecretMask ("[secret]"), which keeps the
// secret there: the template's for a draft, each member's own for a patch.

// RolloutPatch puts the parts of RolloutRequest.Config on members.
const RolloutPatch = "patch"

// MaxRolloutConfigBytes bounds a rollout's own config, as a template's.
const MaxRolloutConfigBytes = MaxTemplateBytes

// errUnchanged is a draft already saved as its template.
var errUnchanged = errors.New("unchanged")

// rolloutConfigLocked checks req's config, and keeps it in r: a patch's
// parts; a template's draft, its secrets put back from the template, which
// user must be allowed to change; the template's hash, which the start
// checks. m.mu is held.
func (m *Manager) rolloutConfigLocked(user string, req *RolloutRequest, r *Rollout) error {
	switch {
	case len(req.Config) == 0:
		if req.Kind == RolloutPatch {
			return fieldErr("config", "a patch carries the parts it puts on the members, as {\"log\": {...}} or {\"routes\": [{\"name\": ...}]}")
		}
		return nil
	case req.Kind != RolloutPatch && req.Kind != RolloutTemplate:
		return fieldErr("config", "only a patch or a template's draft carries config")
	case len(req.Config) > MaxRolloutConfigBytes:
		return fieldErr("config", "the config is %d bytes at most", MaxRolloutConfigBytes)
	}
	if _, err := readTemplateConfig(req.Config, nil); err != nil {
		return err
	}
	root, _ := parseJV(req.Config) // read above
	if req.Kind == RolloutPatch {
		err := root.walkStrings(nil, func(_ []string, s string) (string, error) {
			if strings.Contains(s, "${") {
				return s, fieldErr("config", "%q: a patch takes no variables, which are a template's: write each member's value, or change a template", clip(s, 64))
			}
			return s, nil
		})
		if err != nil {
			return err
		}
		r.Config = root.bytes()
		return nil
	}
	if err := m.allowCluster(&m.doc, user); err != nil {
		return err
	}
	t := &m.doc.Templates[templateIndex(&m.doc, req.Template)] // rolloutTargetsLocked found it
	old, _ := parseJV(t.Config)                                // checked when kept
	if err := restoreSecrets(root, old, "config"); err != nil {
		return fieldErr("config", "%v", err)
	}
	r.Config, r.TemplateBase = root.bytes(), managed.Hash(t.Config)
	return nil
}

// patchParts returns the parts a patch puts on members.
func patchParts(cfg json.RawMessage) []*rendered {
	root, _ := parseJV(cfg) // the bytes of a fragment checked when planned
	return fragmentParts(root)
}

// withOwnSecrets returns parts with each SecretMask taken from the member's
// config cfg, at the part's place, or why one cannot be.
func withOwnSecrets(parts []*rendered, cfg *jv) ([]*rendered, error) {
	out := make([]*rendered, len(parts))
	for i, p := range parts {
		v := p.value.clone()
		if err := restoreSecrets(v, lookupJV(cfg, p.member, p.name), p.display()); err != nil {
			return nil, err
		}
		q := *p
		q.value = v
		out[i] = &q
	}
	return out, nil
}

// planSync reads the member name for r's plan: as a sync does, rendered
// with r's draft in its template's place when it carries one. It blocks:
// the planning goroutine's.
func (m *Manager) planSync(r *Rollout, name string) *syncOut {
	if r.Kind != RolloutTemplate || len(r.Config) == 0 {
		return m.syncSync(name)
	}
	ch := make(chan *syncOut, 1)
	m.draftMember(name, r.Template, r.Config, func(o *syncOut) { ch <- o })
	return <-ch
}

// draftMember reads the member name's /node, /config and /node/managed as
// syncMember does, and compares its config with what its templates render
// with draft as the template tpl's config; it keeps no sync state and
// tells the member nothing. done is called once and must not block.
func (m *Manager) draftMember(name, tpl string, draft json.RawMessage, done func(*syncOut)) {
	var once sync.Once
	end := func(o *syncOut) { once.Do(func() { done(o) }) }
	fail := func(prob *Problem) {
		if prob.At.IsZero() {
			prob.At = m.now().UTC()
		}
		end(&syncOut{sync: MemberSync{Node: name, State: SyncError, Error: prob, Checked: m.now().UTC(), Templates: []string{}, Parts: []PartSync{}}})
	}
	finish := func(info *NodeInfo, cfg *jv, etag string, rec *managed.Record) {
		m.mu.Lock()
		in, ok := m.renderInputLocked(name)
		m.mu.Unlock()
		if !ok {
			fail(&Problem{Kind: KindUnreachable, Message: errNotFound(name).Error()})
			return
		}
		for i := range in.templates {
			if in.templates[i].Name == tpl {
				in.templates[i].Config = slices.Clone(draft)
			}
		}
		in.id, in.hostname = info.ID, info.Hostname
		if rec == nil {
			rec = &managed.Record{}
		}
		res := compare(in, cfg, rec, m.self(), m.now().UTC())
		out := &syncOut{sync: res.sync, config: cfg, desired: res.desired, record: res.record}
		out.sync.ConfigETag = etag
		if kind, why := m.compatible(info.SchemaDigest); kind == ReadOnlyVersion {
			out.sync.Note = why
		}
		end(out)
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
				finish(&info, cfg, etag, nil)
				return
			}
			var rec managed.Record
			m.call(name, "GET", "/node/managed", nil, nil, &rec, func(_ *Reply, prob *Problem) {
				if prob != nil {
					fail(prob)
					return
				}
				rec.Normalize()
				finish(&info, cfg, etag, &rec)
			})
		})
	})
}

// StartRollout starts the planned rollout id as user, with req's strategy,
// its members but those req excludes, and those not ready. A template's
// draft is saved as the template first, as user, who must be allowed to
// change templates; a template changed since the plan refuses the start.
// It writes the store: off a fib worker.
func (m *Manager) StartRollout(user, id string, req *StartRequest) (Rollout, error) {
	if err := m.saveDraft(user, id, req); err != nil {
		return Rollout{}, err
	}
	return m.startRollout(user, id, req)
}

// saveDraft saves the draft of the planned rollout id as its template, as
// user, once what the start checks first passes; a rollout with no draft,
// or not planned, is left to startRollout, which says why it does not
// start.
func (m *Manager) saveDraft(user, id string, req *StartRequest) error {
	m.mu.Lock()
	r := m.rolloutLocked(id)
	if r == nil || r.Kind != RolloutTemplate || len(r.Config) == 0 || r.State != RolloutPlanned || m.closed || m.readOnly != "" {
		m.mu.Unlock()
		return nil
	}
	tpl, draft, base := r.Template, slices.Clone(r.Config), r.TemplateBase
	err := m.allowRolloutLocked(user, r.Kind, r.From, r.nodes())
	if err == nil {
		err = m.allowCluster(&m.doc, user)
	}
	n := 0
	for _, x := range r.Members {
		if x.Status == MemberReady && !slices.Contains(req.Exclude, x.Node) {
			n++
		}
	}
	if err == nil && n > 0 {
		s := req.Strategy
		err = s.check(n)
	}
	m.mu.Unlock()
	if err != nil || n == 0 {
		return err // none ready: startRollout says so, saving nothing
	}
	err = m.change(user, func(d *document) (AuditEntry, error) {
		if err := m.allowCluster(d, user); err != nil {
			return AuditEntry{}, err
		}
		i := templateIndex(d, tpl)
		switch {
		case i < 0:
			return AuditEntry{}, errConflict("the template " + strconv.Quote(tpl) + " was removed since the rollout was planned: plan it again")
		case managed.Hash(d.Templates[i].Config) == managed.Hash(draft):
			return AuditEntry{}, errUnchanged // saved by an earlier start that went no further
		case managed.Hash(d.Templates[i].Config) != base:
			return AuditEntry{}, errConflict("the template " + strconv.Quote(tpl) + " changed since the rollout was planned: plan it again from the template as it is now")
		}
		t := d.Templates[i].clone()
		before := templateText(d, &d.Templates[i])
		t.Config, t.Updated = draft, m.now().UTC()
		d.Templates = slices.Clone(d.Templates)
		d.Templates[i] = t
		v := templateViewIn(d, &t)
		e := AuditEntry{Action: ActionTemplateUpdate, Template: tpl, Rollout: id, Nodes: v.Nodes,
			Detail: clip(fmt.Sprintf("config, saved by starting the rollout %s that planned it", id), 512)}
		e.setDiff(before, templateText(d, &t), tpl, tpl)
		return e, nil
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	if err == nil {
		m.logf("cluster: %q saved the template %q, drafted by the rollout %s it starts", user, tpl, id)
	}
	return err
}

// copyConfig returns r's own config for an answer: its secrets masked
// unless secrets.
func copyConfig(cfg json.RawMessage, secrets bool) json.RawMessage {
	if len(cfg) == 0 || secrets {
		return slices.Clone(cfg)
	}
	root, _ := parseJV(cfg) // the bytes of a fragment checked when planned
	return masked(root).bytes()
}

// partNames returns the parts of a patch, as "log" or "routes[api]".
func partNames(cfg json.RawMessage) []string {
	var out []string
	for _, p := range patchParts(cfg) {
		out = append(out, p.display())
	}
	return out
}
