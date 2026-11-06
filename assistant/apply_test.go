package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
)

// proposeIn has ada's chat make a proposal of the calls given, and returns
// its conversation and view.
func (f *propFixture) proposeIn(t *testing.T, calls ...fakeapi.Call) (string, *ProposalView) {
	t.Helper()
	f.sc.mu.Lock()
	f.sc.turns, f.sc.n = []fakeapi.Turn{{Calls: calls}, {Text: "Proposed."}}, 0
	f.sc.mu.Unlock()
	s := f.chat(`{"message": "change it"}`)
	if s.end == nil {
		t.Fatalf("%s", s.err)
	}
	conv := s.of(EventStart)[0].Conversation
	_, v, body := f.view(conv)
	if v == nil {
		t.Fatalf("no proposal: %s", body)
	}
	return conv, v
}

// TestApplyRefusals: a proposal that does not check, one confirmed on an
// older config or revision, or one whose conversation is answering, is not
// applied; the dialog's cancel sends nothing, so nothing changes.
func TestApplyRefusals(t *testing.T) {
	f := startProposals(t, `"default_level": "apply"`)
	// Invalid: a route without a handler.
	conv, v := f.proposeIn(t, call("p", ToolConfigPropose, `{"summary": "Break", "ops": [{"op": "remove", "path": "/routes/0/handler"}]}`))
	if v.Valid || len(v.Errors) == 0 {
		t.Fatalf("%+v", v)
	}
	if status, out := f.applyIt(conv, v); status != 422 || out.Outcome != OutcomeRefused || len(f.cfgs.changed()) != 0 {
		t.Fatalf("%d %s", status, out.raw)
	}

	// Stale: the config changed after the proposal was made.
	conv, v = f.proposeIn(t, call("p", ToolConfigPropose, `{"summary": "Second backend", "ops": [{"op": "add", "path": "/upstreams/0/addresses/-", "value": "10.0.0.8:9000"}]}`))
	f.cfgs.set(strings.Replace(propBase, `"max_conns": 8`, `"max_conns": 9`, 1))
	_, now, _ := f.view(conv)
	if !now.Stale || now.Version != 2 || now.BaseVersion != 1 {
		t.Fatalf("%+v", now)
	}
	status, out := f.applyIt(conv, v)
	if status != 412 || out.Outcome != OutcomeStale || out.Proposal == nil || out.Proposal.Version != 2 || out.Proposal.Revision != v.Revision+1 ||
		out.Proposal.Stale || !strings.Contains(out.Error, "offered again on version 2") {
		t.Fatalf("%d %s", status, out.raw)
	}
	if len(f.cfgs.changed()) != 0 {
		t.Fatal("a stale proposal was applied")
	}
	// The page that confirmed the old one is refused again; the one
	// offered again applies.
	if status, out := f.applyIt(conv, v); status != 409 {
		t.Fatalf("%d %s", status, out.raw)
	}
	status, out = f.applyIt(conv, out.Proposal)
	if status != 200 || out.Version != 3 {
		t.Fatalf("%d %s", status, out.raw)
	}
	_, data := f.cfgs.current()
	if !strings.Contains(string(data), `"max_conns":9`) || !strings.Contains(string(data), "10.0.0.8") {
		t.Errorf("%s", data)
	}

	// An Admin API that finds it stale at the last moment.
	conv, v = f.proposeIn(t, call("p", ToolConfigPropose, `{"summary": "Third", "ops": [{"op": "add", "path": "/upstreams/0/addresses/-", "value": "10.0.0.9:9000"}]}`))
	f.cfgs.mu.Lock()
	f.cfgs.fail = 412
	f.cfgs.mu.Unlock()
	if status, out := f.applyIt(conv, v); status != 412 || out.Outcome != OutcomeStale {
		t.Fatalf("%d %s", status, out.raw)
	}
	f.cfgs.mu.Lock()
	f.cfgs.fail = 0
	f.cfgs.mu.Unlock()

	// Answering: the conversation may change its proposal.
	_, v, _ = f.view(conv)
	f.a.chats.mu.Lock()
	f.a.userLocked("ada").conversation(conv).busy = &chat{}
	f.a.chats.mu.Unlock()
	if status, body := f.do("POST", "/api/m/ai/proposals/"+conv+"/apply", `{"id": "`+v.ID+`", "revision": 1}`); status != 409 || !strings.Contains(body, "answering") {
		t.Errorf("%d %s", status, body)
	}
	f.a.chats.mu.Lock()
	f.a.userLocked("ada").conversation(conv).busy = nil
	f.a.chats.mu.Unlock()

	// Requests that name nothing.
	for _, x := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/m/ai/proposals/nope", "", 404},
		{"POST", "/api/m/ai/proposals/nope/apply", `{"id": "x"}`, 404},
		{"POST", "/api/m/ai/proposals/" + conv + "/apply", `{"id": "other"}`, 409},
		{"POST", "/api/m/ai/proposals/" + conv + "/discard", `{"id": "other"}`, 409},
		{"POST", "/api/m/ai/proposals/" + conv + "/apply", `nope`, 400},
		{"PUT", "/api/m/ai/proposals/" + conv, `{}`, 405},
		{"POST", "/api/m/ai/proposals/" + conv + "/other", `{}`, 405},
	} {
		if status, body := f.do(x.method, x.path, x.body); status != x.status {
			t.Errorf("%s %s: %d %s", x.method, x.path, status, body)
		}
	}
	// A conversation without one.
	s := f.chat(`{"message": "hello"}`)
	if status, body := f.do("GET", "/api/m/ai/proposals/"+s.of(EventStart)[0].Conversation, ""); status != 200 || body != `{"proposal":null}` {
		t.Errorf("%d %s", status, body)
	}
}

// TestApplyFailsWhole: the config steps go as one change, so a change the
// Admin API refuses applies none of them, nor the purges after them; the
// proposal stays, with why, and the model hears of it.
func TestApplyFailsWhole(t *testing.T) {
	f := startProposals(t, `"default_level": "apply"`)
	conv, v := f.proposeIn(t,
		call("a", ToolConfigPropose, `{"summary": "Move the listener", "ops": [{"op": "replace", "path": "/listeners/0/address", "value": "127.0.0.1:1"}]}`),
		call("b", ToolConfigPropose, `{"summary": "A backend", "ops": [{"op": "add", "path": "/upstreams/0/addresses/-", "value": "10.0.0.8:9000"}]}`),
		call("c", ToolPurgePropose, `{"summary": "Purge", "all": true}`))
	if !v.Valid || len(v.Steps) != 3 {
		t.Fatalf("%+v", v)
	}
	f.cfgs.mu.Lock()
	f.cfgs.fail = 400
	f.cfgs.mu.Unlock()
	status, out := f.applyIt(conv, v)
	if status != 422 || out.Outcome != OutcomeFailed || len(out.Errors) != 1 || out.Errors[0].Message != "address in use" || out.Proposal == nil || out.Proposal.Error == "" {
		t.Fatalf("%d %s", status, out.raw)
	}
	n, _ := f.cfgs.current()
	f.cfgs.mu.Lock()
	purges := len(f.cfgs.purges)
	f.cfgs.mu.Unlock()
	if n != 1 || purges != 0 || len(f.cfgs.changed()) != 1 {
		t.Fatalf("version %d, %d purges, %v", n, purges, f.cfgs.changed())
	}
	if e := f.audit("?kind=apply"); len(e) != 1 || e[0].Outcome != OutcomeFailed || e[0].Detail == "" {
		t.Errorf("%+v", e)
	}
	f.sc.mu.Lock()
	f.sc.turns, f.sc.n = []fakeapi.Turn{{Text: "ok"}}, 0
	f.sc.mu.Unlock()
	f.chat(`{"conversation": "` + conv + `", "message": "why?"}`)
	reqs := f.model.Requests()
	if sys := fakeapi.System(reqs[len(reqs)-1]); !strings.Contains(sys, "Its last apply failed") {
		t.Errorf("%s", sys)
	}
}

// TestApplyKinds: reconcile steps go as one POST /config/reconcile, a
// rollback as POST /rollback/{n}, both with If-Match; purges alone change
// no config and are never stale, and a purge that fails after the config
// leaves the outcome partial.
func TestApplyKinds(t *testing.T) {
	f := startProposals(t, `"default_level": "apply"`)
	f.cfgs.check = `{"differences": [{"id": "admin:timeout", "fixes": [{"action": "reapply"}]},
	  {"id": "pool", "fixes": [{"action": "update", "patch": {"op": "replace", "path": "/upstreams/0/addresses", "value": ["10.0.0.9:9000"]}}]}]}`
	conv, v := f.proposeIn(t, call("a", ToolReconcilePropose, `{"summary": "Reapply", "fixes": [{"id": "admin:timeout", "action": "reapply"}]}`),
		call("b", ToolReconcilePropose, `{"summary": "Fix the pool", "fixes": [{"id": "pool", "action": "update"}]}`),
		call("c", ToolPurgePropose, `{"summary": "Purge main", "zone": "main", "all": true}`))
	if v.Kind != KindReconcile || len(v.Fixes) != 2 || !v.Valid {
		t.Fatalf("%+v", v)
	}
	// The zone is gone by the time it is applied.
	f.cfgs.mu.Lock()
	f.cfgs.zones = nil
	f.cfgs.mu.Unlock()
	_, v, _ = f.view(conv)
	if v.Valid || v.Steps[2].Error == "" {
		t.Fatalf("%+v", v)
	}
	f.cfgs.mu.Lock()
	f.cfgs.zones = []string{"main"}
	f.cfgs.mu.Unlock()
	_, v, _ = f.view(conv)
	status, out := f.applyIt(conv, v)
	if status != 200 || out.Version != 2 {
		t.Fatalf("%d %s", status, out.raw)
	}
	f.cfgs.mu.Lock()
	body := f.cfgs.bodies[0]
	f.cfgs.mu.Unlock()
	if got := f.cfgs.changed(); len(got) != 1 || !strings.HasPrefix(got[0], "POST /config/reconcile \"") ||
		body != `{"fixes":[{"id":"admin:timeout","action":"reapply"},{"id":"pool","action":"update"}]}` {
		t.Errorf("%v %s", got, body)
	}

	// A rollback.
	conv, v = f.proposeIn(t, call("a", ToolRollbackPropose, `{"summary": "Back to 1", "version": 1}`))
	if v.Rollback != 1 || v.Kind != KindRollback {
		t.Fatalf("%+v", v)
	}
	if status, out := f.applyIt(conv, v); status != 200 || out.Version != 3 {
		t.Fatalf("%d %s", status, out.raw)
	}
	if got := f.cfgs.changed(); len(got) != 2 || !strings.HasPrefix(got[1], "POST /rollback/1 ") {
		t.Errorf("%v", got)
	}

	// Purges alone: no config change, no ETag; one fails.
	conv, v = f.proposeIn(t, call("a", ToolPurgePropose, `{"summary": "Purge main", "zone": "main", "tags": ["x"]}`),
		call("b", ToolPurgePropose, `{"summary": "Purge all zones", "all": true}`))
	f.cfgs.mu.Lock()
	f.cfgs.failPurge = true
	f.cfgs.mu.Unlock()
	f.cfgs.set(propBase)
	v.ETag = "old"
	v.Revision = 2
	status, out = f.applyIt(conv, v)
	if status != 200 || out.Outcome != OutcomePartial || out.Version != 0 || len(out.Purged) != 2 || out.Purged[0].Error == "" || out.Purged[1].Purged != 2 {
		t.Fatalf("%d %s", status, out.raw)
	}
	if len(f.cfgs.changed()) != 2 {
		t.Errorf("%v", f.cfgs.changed())
	}
	if e := f.audit("?kind=apply&limit=1"); len(e) != 1 || e[0].Outcome != OutcomePartial || e[0].Detail == "" {
		t.Errorf("%+v", e)
	}
}

// TestDiscard: a discarded proposal, or one deleted with its conversation,
// is gone and in the audit log; nothing else changes.
func TestDiscard(t *testing.T) {
	f := startProposals(t, `"users": [{"username": "bob", "level": "off"}], "default_level": "propose"`)
	conv, v := f.proposeIn(t, call("a", ToolPurgePropose, `{"summary": "Purge", "all": true}`))
	if status, body := f.as("bob").do("POST", "/api/m/ai/proposals/"+conv+"/discard", `{"id": "`+v.ID+`"}`); status != 403 {
		t.Errorf("%d %s", status, body)
	}
	if status, body := f.do("POST", "/api/m/ai/proposals/"+conv+"/discard", `{"id": "`+v.ID+`"}`); status != 200 || !strings.Contains(body, v.ID) {
		t.Fatalf("%d %s", status, body)
	}
	if _, v, _ := f.view(conv); v != nil {
		t.Error("still pending")
	}
	c := f.conversation(conv)
	_ = c
	conv2, _ := f.proposeIn(t, call("a", ToolPurgePropose, `{"summary": "Purge", "all": true}`))
	if status, body := f.do("DELETE", "/api/m/ai/conversations/"+conv2, ""); status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	e := f.audit("?kind=discard")
	if len(e) != 2 || e[0].Detail != "deleted with its conversation" || e[1].Outcome != OutcomeDiscarded || len(f.cfgs.changed()) != 0 {
		t.Errorf("%+v", e)
	}
	// The conversation says what became of it.
	status, body := f.do("GET", "/api/m/ai/conversations/"+conv, "")
	var cv struct {
		ProposalNote string `json:"proposal_note"`
	}
	_ = json.Unmarshal([]byte(body), &cv)
	if status != 200 || !strings.Contains(cv.ProposalNote, "ada discarded the proposal") {
		t.Errorf("%d %s", status, body)
	}
}

// TestProposalViewFails: an Admin API that does not give the config fails
// the view and the apply with 502, applying nothing.
func TestProposalViewFails(t *testing.T) {
	f := startProposals(t, `"default_level": "apply"`)
	conv, v := f.proposeIn(t, call("a", ToolPurgePropose, `{"summary": "Purge", "all": true}`))
	f.cfgs.mu.Lock()
	f.cfgs.down = true
	f.cfgs.mu.Unlock()
	if status, _, body := f.view(conv); status != 502 {
		t.Errorf("%d %s", status, body)
	}
	if status, out := f.applyIt(conv, v); status != 502 {
		t.Errorf("%d %s", status, out.raw)
	}
}
