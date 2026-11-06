package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/config"
)

// TestProposeEdges: a member removed, a step that fails once the config
// moved, a check that is not config.Errors or lists many, a step of no
// propose tool, a render that fails, and the cache's zones not read.
func TestProposeEdges(t *testing.T) {
	a, cfgs := proposeAssistant(t)
	// A member removed; a secret in a member set whole is refused.
	out := mustPropose(t, a, "m", ToolConfigPropose, `{"summary": "No upstreams", "members": {"upstreams": null, "x_none": null}}`)
	if c := out["changed"].([]any); len(c) != 1 || c[0] != "upstreams" {
		t.Errorf("%v", out)
	}
	wantPropose(t, a, "m", ToolConfigPropose, `{"summary": "x", "members": {"routes": [{"middleware": [{"module": "m", "params": {"secret": "new"}}], "handler": {"module": "respond"}}]}}`, "holds a secret")
	// A render that fails: the view says so in the diff's place.
	cfgs.mu.Lock()
	cfgs.broken = map[string]bool{"POST /config/render": true}
	cfgs.mu.Unlock()
	v, _, err := a.viewProposal(context.Background(), "ada", "m", pendingOf(a, "m"), "ada")
	if err != nil || !strings.Contains(v.Diff["conf"], "could not be rendered in conf") || !strings.Contains(v.Diff["json"], "+++ proposed") {
		t.Errorf("%v %+v", err, v.Diff)
	}
	cfgs.mu.Lock()
	cfgs.broken = nil
	cfgs.mu.Unlock()
	mustPropose(t, a, "r", ToolConfigPropose, `{"summary": "x", "ops": [{"op": "add", "path": "/upstreams/0/addresses/-", "value": "render-fails"}]}`)
	if v, _, _ := a.viewProposal(context.Background(), "ada", "r", pendingOf(a, "r"), "ada"); !strings.Contains(v.Diff["yaml"], "could not be rendered in yaml") {
		t.Errorf("%+v", v.Diff)
	}

	// A step that applied once fails when the config moves: the next step
	// hears of it.
	mustPropose(t, a, "s", ToolConfigPropose, `{"summary": "Drop the middleware", "ops": [{"op": "remove", "path": "/routes/0/middleware"}]}`)
	cfgs.set(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`)
	out = mustPropose(t, a, "s", ToolPurgePropose, `{"summary": "Purge", "all": true}`)
	if errs, _ := out["step_errors"].(map[string]any); errs["1"] == nil || out["valid"] != false {
		t.Errorf("%v", out)
	}

	// The cache's zones not read.
	cfgs.mu.Lock()
	cfgs.broken = map[string]bool{"GET /cache": true}
	cfgs.mu.Unlock()
	wantPropose(t, a, "z", ToolPurgePropose, `{"summary": "x", "zone": "main", "all": true}`, "zones could not be read")
	cfgs.mu.Lock()
	cfgs.broken = nil
	cfgs.mu.Unlock()

	// A step of no propose tool, as from a store an older fit wrote.
	b, err := a.build(context.Background(), a.redactor(a.Settings()), []ProposalStep{{Tool: "nope", Summary: "x", Args: json.RawMessage(`{"summary": "x"}`)}})
	if err != nil || b.stepErrs[0] != "nope is not a propose tool" || b.valid {
		t.Errorf("%v %+v", err, b)
	}

	// A check whose error is not config.Errors, and one with many.
	plain := toolsAssistant(t, &config.Config{}, func(o *Options) {
		o.API = newFakeConfigs(propBase).handler()
		o.Validate = func([]byte) error { return errors.New("compiling: no such module") }
	})
	out = mustPropose(t, plain, "v", ToolConfigPropose, `{"summary": "x", "ops": [{"op": "add", "path": "/upstreams/-", "value": {"name": "b"}}]}`)
	if out["valid"] != false || !strings.Contains(fmt.Sprint(out["errors"]), "no such module") {
		t.Errorf("%v", out)
	}
	many := toolsAssistant(t, &config.Config{}, func(o *Options) {
		o.API = newFakeConfigs(propBase).handler()
		o.Validate = func([]byte) error {
			var errs config.Errors
			for i := range 60 {
				errs = append(errs, &config.Error{Path: fmt.Sprint("x", i), Msg: "bad"})
			}
			return errs
		}
	})
	out = mustPropose(t, many, "v", ToolConfigPropose, `{"summary": "x", "ops": [{"op": "add", "path": "/upstreams/-", "value": {"name": "b"}}]}`)
	if errs := out["errors"].([]any); len(errs) != 51 || !strings.Contains(fmt.Sprint(errs[50]), "and 10 more") {
		t.Errorf("%v", out["errors"])
	}

	// An apply or discard that finished after a step was added leaves the
	// proposal the step made.
	p := pendingOf(a, "m")
	a.setProposal("ada", "m", p.ID, p.Revision-1, nil, "applied")
	a.setProposal("ada", "m", "other", p.Revision, nil, "applied")
	if q := pendingOf(a, "m"); q == nil || q.Revision != p.Revision {
		t.Errorf("%+v", q)
	}

	// What the log keeps of a proposal is bounded.
	if auditOf(nil, nil) != nil {
		t.Error("auditOf(nil)")
	}
	big := &Proposal{ID: "p"}
	for range MaxProposalSteps + 5 {
		big.Steps = append(big.Steps, ProposalStep{Tool: ToolPurgePropose, Summary: strings.Repeat("s", 500)})
	}
	changed := make([]string, 70)
	if ap := auditOf(big, changed); len(ap.Steps) != maxAuditSteps || len(ap.Changed) != maxAuditNames || len(ap.Steps[0].Summary) > maxSummaryBytes+4 {
		t.Errorf("%d %d", len(ap.Steps), len(ap.Changed))
	}
}

// TestReconcileOrder: the removals of several fixes go deepest and highest
// first, by index or by name, and a fix that sets an element sets it in
// place.
func TestReconcileOrder(t *testing.T) {
	a, cfgs := proposeAssistant(t)
	cfgs.set(`{"listeners": [{"name": "a", "address": ":1"}, {"name": "b", "address": ":2"}, {"name": "c", "address": ":3"}],
	  "routes": [{"handler": {"module": "respond"}}], "upstreams": [{"name": "u", "addresses": ["x", "y"]}]}`)
	cfgs.check = `{"differences": [
	  {"id": "l0", "fixes": [{"action": "delete", "patch": {"op": "delete", "path": "/listeners/0"}}]},
	  {"id": "l2", "fixes": [{"action": "delete", "patch": {"op": "delete", "path": "/listeners/2"}}]},
	  {"id": "u", "fixes": [{"action": "delete", "patch": {"op": "delete", "path": "/upstreams"}}]},
	  {"id": "set", "fixes": [{"action": "set", "patch": {"op": "replace", "path": "/listeners/1/address", "value": ":9"}}]},
	  {"id": "el", "fixes": [{"action": "set", "patch": {"op": "add", "path": "/upstreams/0/addresses/1", "value": "z"}}]}
	]}`
	mustPropose(t, a, "o", ToolReconcilePropose, `{"summary": "Fixes", "fixes": [{"id": "l0", "action": "delete"}, {"id": "u", "action": "delete"},
	  {"id": "l2", "action": "delete"}, {"id": "set", "action": "set"}]}`)
	b := builtOf(t, a, "o")
	if s := string(encodeTree(b.after)); !b.valid || s != `{"listeners":[{"name":"b","address":":9"}],"routes":[{"handler":{"module":"respond"}}]}` {
		t.Errorf("%s %v", s, b.stepErrs)
	}
	mustPropose(t, a, "e", ToolReconcilePropose, `{"summary": "In place", "fixes": [{"id": "el", "action": "set"}]}`)
	if s := string(encodeTree(builtOf(t, a, "e").after)); !strings.Contains(s, `"addresses":["x","z"]`) {
		t.Errorf("%s", s)
	}
}

// TestChatAuditOnError: an answer that called tools and then failed is in
// the audit log with its error.
func TestChatAuditOnError(t *testing.T) {
	f := startProposals(t, "", fakeapi.Turn{Calls: []fakeapi.Call{call("c", "status_get", `{}`)}}, fakeapi.Turn{Status: 400})
	s := f.chat(`{"message": "status?"}`)
	if s.err == "" {
		t.Fatalf("%+v", s.end)
	}
	if e := f.audit(""); len(e) != 1 || e[0].Outcome != ReasonError || e[0].Detail == "" || e[0].Calls[0].Tool != "status_get" {
		t.Errorf("%+v", e)
	}
}

// TestProposalsList: a user who applies sees every user's pending
// proposals, the latest first, and discards another's.
func TestProposalsList(t *testing.T) {
	f := startProposals(t, `"users": [{"username": "ada", "level": "apply"}, {"username": "bob", "level": "propose"}]`)
	conv1, _ := f.proposeIn(t, call("a", ToolPurgePropose, `{"summary": "First", "all": true}`))
	bob := &propFixture{chatFixture: &chatFixture{fixture: f.as("bob"), model: f.model, store: f.store}, cfgs: f.cfgs, sc: f.sc}
	conv2, v2 := bob.proposeIn(t, call("a", ToolPurgePropose, `{"summary": "Second", "all": true}`))
	status, body := f.do("GET", "/api/m/ai/proposals", "")
	var list struct {
		Proposals []pendingView `json:"proposals"`
	}
	_ = json.Unmarshal([]byte(body), &list)
	if status != 200 || len(list.Proposals) != 2 || list.Proposals[0].Conversation != conv2 || list.Proposals[1].Conversation != conv1 {
		t.Fatalf("%d %s", status, body)
	}
	if status, body := f.do("POST", "/api/m/ai/proposals/"+conv2+"/discard", `{"user": "bob", "id": "`+v2.ID+`"}`); status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	if e := f.audit("?kind=discard"); len(e) != 1 || e[0].Owner != "bob" || e[0].User != "ada" {
		t.Errorf("%+v", e)
	}
}
