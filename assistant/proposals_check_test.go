package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lesismal/fit/config"
)

func wantPropose(t *testing.T, a *Assistant, conv, tool, args, want string) {
	t.Helper()
	out, err := propose(t, a, conv, tool, args)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s %s: %v %s, want %q", tool, args, err, out, want)
	}
}

func mustPropose(t *testing.T, a *Assistant, conv, tool, args string) map[string]any {
	t.Helper()
	out, err := propose(t, a, conv, tool, args)
	if err != nil {
		t.Fatalf("%s %s: %v", tool, args, err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	return m
}

// built returns what conv's proposal makes of the running config.
func builtOf(t *testing.T, a *Assistant, conv string) *built {
	t.Helper()
	b, err := a.build(context.Background(), a.redactor(a.Settings()), pendingOf(a, conv).Steps)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestProposeChecks: a step is refused, and left out of the proposal, when
// it touches admin, writes a secret or a redacted value where the config
// has none, mixes kinds of config steps, or is not what its tool takes; a
// redacted value written back where it was is the config's own again.
func TestProposeChecks(t *testing.T) {
	a, cfgs := proposeAssistant(t)
	const c = "c1"
	for _, x := range []struct{ tool, args, want string }{
		{ToolConfigPropose, `{"summary": "x", "members": {"admin": {"address": ":1"}}}`, "admin is out"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "replace", "path": "/admin/address", "value": ":1"}]}`, "admin is out"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "replace", "path": "", "value": {}}]}`, "the whole config"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "move", "path": "/routes/0"}]}`, "not \"move\""},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "add", "path": "/routes/-"}]}`, "needs a value"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "add", "path": "routes"}]}`, "starts with /"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "replace", "path": "/nothing", "value": 1}]}`, "nothing at /nothing"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "remove", "path": "/routes/7"}]}`, "past the array"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "add", "path": "/routes/x/y", "value": 1}]}`, "nothing at /routes/x"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "add", "path": "/listeners/0/name/x", "value": 1}]}`, "neither an object nor an array"},
		{ToolConfigPropose, `{"summary": "x", "members": [1]}`, "not a JSON object"},
		{ToolConfigPropose, `{"summary": "x"}`, "give members, ops or both"},
		{ToolConfigPropose, `{"ops": []}`, "summary is 1 to"},
		{ToolConfigPropose, `{"summary": "` + strings.Repeat("x", 400) + `"}`, "summary is 1 to"},
		{ToolConfigPropose, `[1]`, "not what config_propose takes"},
		// A secret set, or a redacted value where the config has none.
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "replace", "path": "/routes/0/middleware/0/params/secret", "value": "mine"}]}`, "holds a secret"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "add", "path": "/routes/-", "value": {"middleware": [{"module": "headers", "params": {"token": "t-1"}}], "handler": {"module": "respond"}}}]}`, "holds a secret"},
		{ToolConfigPropose, `{"summary": "x", "ops": [{"op": "add", "path": "/routes/-", "value": {"handler": {"module": "respond", "params": {"body": "[redacted]"}}}}]}`, "a redacted value"},
		{ToolPurgePropose, `{"summary": "x"}`, "name urls, tags, or all"},
		{ToolPurgePropose, `{"summary": "x", "urls": ["/relative"]}`, "not an absolute"},
		{ToolPurgePropose, `{"summary": "x", "zone": "nope", "all": true}`, "no cache uses the zone"},
		{ToolPurgePropose, `{"summary": "x", "tags": [` + strings.Repeat(`"t",`, 100) + `"t"]}`, "at most 100"},
		{ToolRollbackPropose, `{"summary": "x"}`, "kept version"},
		{ToolRollbackPropose, `{"summary": "x", "version": 1}`, "the one running"},
		{ToolRollbackPropose, `{"summary": "x", "version": 9}`, "answered 404"},
		{ToolReconcilePropose, `{"summary": "x", "fixes": []}`, "at least one fix"},
		{ToolReconcilePropose, `{"summary": "x", "fixes": [{"id": "nope", "action": "x"}]}`, "no difference \"nope\""},
	} {
		wantPropose(t, a, c, x.tool, x.args, x.want)
	}
	if p := pendingOf(a, c); p != nil {
		t.Fatalf("a refused step made a proposal: %+v", p)
	}
	if _, err := a.Registry().Tool(ToolConfigPropose).Run(&Call{Context: context.Background(), Args: json.RawMessage(`{}`), User: "ada", Conversation: "none", a: a}); err != errNoConversation {
		t.Error(err)
	}
	if _, err := propose(t, a, c, ToolConfigPropose, `{"summary": "`+strings.Repeat("x", MaxProposalBytes)+`"}`); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Error(err)
	}

	// The route copied whole, its secret redacted as the model saw it: the
	// secret is the config's again, and a secret set to "" is allowed.
	out := mustPropose(t, a, c, ToolConfigPropose, `{"summary": "Rename the route", "members": {"routes": [{"name": "api2", "match": {"path": ["/api/*"]},
		"middleware": [{"module": "headers", "params": {"secret": "[redacted]"}}], "handler": {"module": "proxy", "params": {"upstream": "app"}}}]}}`)
	if out["valid"] != true || out["revision"].(float64) != 1 {
		t.Fatalf("%v", out)
	}
	b := builtOf(t, a, c)
	if s := string(encodeTree(b.after)); !strings.Contains(s, propSecret) || !strings.Contains(s, `"api2"`) {
		t.Errorf("%s", s)
	}
	// The kinds of config steps do not mix; a purge goes with any.
	wantPropose(t, a, c, ToolReconcilePropose, `{"summary": "x", "fixes": [{"id": "a", "action": "b"}]}`, "cannot join")
	wantPropose(t, a, c, ToolRollbackPropose, `{"summary": "x", "version": 1}`, "cannot join")
	mustPropose(t, a, c, ToolPurgePropose, `{"summary": "Purge all", "all": true}`)
	mustPropose(t, a, c, ToolConfigPropose, `{"summary": "No secret", "ops": [{"op": "replace", "path": "/routes/0/middleware/0/params/secret", "value": ""}]}`)
	if p := pendingOf(a, c); len(p.Steps) != 3 || p.Revision != 3 {
		t.Errorf("%+v", p)
	}

	// A step that leaves the whole invalid is kept, and the proposal
	// cannot be applied until mended; the config changed meanwhile, the
	// proposal moves to it.
	cfgs.set(strings.Replace(propBase, "10.0.0.7", "10.0.0.9", 1))
	out = mustPropose(t, a, c, ToolConfigPropose, `{"summary": "Break it", "ops": [{"op": "remove", "path": "/routes/0/handler"}]}`)
	if out["valid"] != false || out["errors"] == nil || out["rebased"] == nil || out["base_version"].(float64) != 2 {
		t.Errorf("%v", out)
	}
	if p := pendingOf(a, c); p.Valid || p.Problems == 0 || p.BaseVersion != 2 || !strings.Contains(proposalState(p, ""), "does not check") {
		t.Errorf("%+v", p)
	}

	// Bounds: the steps of a proposal.
	const d = "c2"
	for i := range MaxProposalSteps {
		mustPropose(t, a, d, ToolPurgePropose, fmt.Sprintf(`{"summary": "purge %d", "tags": ["t%d"]}`, i, i))
	}
	wantPropose(t, a, d, ToolPurgePropose, `{"summary": "one more", "all": true}`, "has its 32 steps")
	if b := builtOf(t, a, d); !b.valid || b.kind != "" || len(b.purges) != MaxProposalSteps {
		t.Errorf("%+v", b)
	}
	a.chats.mu.Lock()
	a.userLocked("ada").conversation(d).Proposal.Steps[0].Args = json.RawMessage(strings.Repeat(" ", MaxProposalBytes))
	a.chats.mu.Unlock()
	a.chats.mu.Lock()
	a.userLocked("ada").conversation(d).Proposal.Steps = a.userLocked("ada").conversation(d).Proposal.Steps[:1]
	a.chats.mu.Unlock()
	wantPropose(t, a, d, ToolPurgePropose, `{"summary": "one more", "all": true}`, "would pass")

	// A step past its timeout is not added.
	var expired atomic.Bool
	expired.Store(true)
	_, err := a.Registry().Tool(ToolPurgePropose).Run(&Call{Context: context.Background(), Args: json.RawMessage(`{"summary": "late", "all": true}`),
		User: "ada", Conversation: c, a: a, expired: &expired})
	if err == nil || !strings.Contains(err.Error(), "took too long") {
		t.Error(err)
	}

	// The model's view of a proposal without one, and of its outcome.
	if proposalState(nil, "applied.") != "applied." || !strings.Contains(proposalState(&Proposal{Valid: true}, ""), "It checks") ||
		!strings.Contains(proposalState(&Proposal{Error: "no"}, ""), "last apply failed: no") {
		t.Error("proposalState")
	}
}

// TestProposeReconcileAndRollback: reconcile steps' fixes are checked
// against the Consistency check and applied together as it does; a
// rollback is one step, refused when it would change admin.
func TestProposeReconcileAndRollback(t *testing.T) {
	a, cfgs := proposeAssistant(t)
	cfgs.check = `{"differences": [
	  {"id": "upstream:app", "fixes": [{"action": "update", "patch": {"op": "replace", "path": "/upstreams/0/addresses", "value": ["10.0.0.9:9000"]}}, {"action": "ignore"}]},
	  {"id": "route:x", "fixes": [{"action": "add", "patch": {"op": "add", "path": "/extra/list/-", "value": {"n": 1}}}]},
	  {"id": "route:y", "fixes": [{"action": "set", "patch": {"op": "add", "path": "/routes/0/name", "value": "y"}}]},
	  {"id": "del:a", "fixes": [{"action": "delete", "patch": {"op": "delete", "path": "/upstreams/0/addresses/0"}}]},
	  {"id": "del:b", "fixes": [{"action": "delete", "patch": {"op": "delete", "path": "/upstreams/0"}}]},
	  {"id": "admin:x", "fixes": [{"action": "set", "patch": {"op": "replace", "path": "/admin/address", "value": ":1"}}]},
	  {"id": "bad", "fixes": [{"action": "set", "patch": {"op": "add", "path": "/routes/0/name"}}, {"action": "nopath", "patch": {"op": "add", "path": "", "value": 1}}, {"action": "missing", "patch": {"op": "replace", "path": "/no/where", "value": 1}}]}
	]}`
	const c = "r"
	wantPropose(t, a, c, ToolReconcilePropose, `{"summary": "x", "fixes": [{"id": "upstream:app", "action": "nope"}]}`, "offers no \"nope\"")
	wantPropose(t, a, c, ToolReconcilePropose, `{"summary": "x", "fixes": [{"id": "admin:x", "action": "set"}]}`, "admin is out")
	mustPropose(t, a, c, ToolReconcilePropose, `{"summary": "Fix the pool", "fixes": [{"id": "upstream:app", "action": "update"}, {"id": "route:x", "action": "add"}]}`)
	wantPropose(t, a, c, ToolReconcilePropose, `{"summary": "again", "fixes": [{"id": "upstream:app", "action": "update"}]}`, "fixed by the proposal already")
	wantPropose(t, a, c, ToolConfigPropose, `{"summary": "x", "ops": [{"op": "remove", "path": "/routes/0"}]}`, "cannot join")
	mustPropose(t, a, c, ToolReconcilePropose, `{"summary": "Name it", "fixes": [{"id": "route:y", "action": "set"}]}`)
	b := builtOf(t, a, c)
	if s := string(encodeTree(b.after)); !strings.Contains(s, `"addresses":["10.0.0.9:9000"]`) || !strings.Contains(s, `"extra":{"list":[{"n":1}]}`) || b.kind != KindReconcile {
		t.Errorf("%s %+v", s, b.stepErrs)
	}

	// Deletes go deepest and highest first; a fix that does not apply is
	// its step's error.
	const d = "r2"
	mustPropose(t, a, d, ToolReconcilePropose, `{"summary": "Deletes", "fixes": [{"id": "del:b", "action": "delete"}, {"id": "del:a", "action": "delete"}]}`)
	if b := builtOf(t, a, d); !b.valid || !strings.Contains(string(encodeTree(b.after)), `"upstreams":[]`) {
		t.Errorf("%s %v", encodeTree(b.after), b.stepErrs)
	}
	for _, action := range []string{"set", "nopath", "missing"} {
		if _, err := propose(t, a, "r3"+action, ToolReconcilePropose, `{"summary": "bad", "fixes": [{"id": "bad", "action": "`+action+`"}]}`); err == nil {
			t.Errorf("%s: a fix that does not apply", action)
		}
	}
	cfgs.check = `not json`
	wantPropose(t, a, "r4", ToolReconcilePropose, `{"summary": "x", "fixes": [{"id": "a", "action": "b"}]}`, "could not be read")

	// Rollback.
	cfgs.set(strings.Replace(propBase, "10.0.0.7", "10.0.0.9", 1))
	cfgs.set(strings.Replace(propBase, `"max_conns": 8`, `"max_conns": 9`, 1))
	cfgs.set(`[1]`)
	const r = "rb"
	wantPropose(t, a, r, ToolRollbackPropose, `{"summary": "x", "version": 1}`, "not a JSON object") // the running one does not read
	cfgs.set(propBase)
	wantPropose(t, a, r, ToolRollbackPropose, `{"summary": "x", "version": 3}`, "another admin")
	wantPropose(t, a, r, ToolRollbackPropose, `{"summary": "x", "version": 4}`, "not a JSON object")
	mustPropose(t, a, r, ToolRollbackPropose, `{"summary": "Back to 2", "version": 2}`)
	wantPropose(t, a, r, ToolRollbackPropose, `{"summary": "x", "version": 1}`, "one rollback")
	wantPropose(t, a, r, ToolConfigPropose, `{"summary": "x", "ops": [{"op": "remove", "path": "/routes/0"}]}`, "cannot join")
	if b := builtOf(t, a, r); b.rollback != 2 || len(b.changed) != 1 || b.changed[0] != "upstreams" {
		t.Errorf("%+v", b.changed)
	}
}

// TestProposeBuildFails: a config the Admin API does not give is the
// tool's error.
func TestProposeBuildFails(t *testing.T) {
	a := toolsAssistant(t, &config.Config{})
	// fakeAdmin's GET /config has no ETag, but is JSON: a proposal is
	// checked on it; its render is fake, and the diff is left out.
	if _, err := propose(t, a, "x", ToolConfigPropose, `{"summary": "x", "ops": [{"op": "remove", "path": "/tls"}]}`); err != nil {
		t.Error(err)
	}
	none := toolsAssistant(t, &config.Config{}, func(o *Options) { o.API = nil })
	wantPropose(t, none, "x", ToolPurgePropose, `{"summary": "x", "all": true}`, "not reachable")
	arr := newFakeConfigs(`[1]`)
	bad := toolsAssistant(t, &config.Config{}, func(o *Options) { o.API = arr.handler() })
	wantPropose(t, bad, "x", ToolPurgePropose, `{"summary": "x", "all": true}`, "not a JSON object")
}
