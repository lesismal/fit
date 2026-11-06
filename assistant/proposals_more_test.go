package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/config"
)

// as returns f as user's browser, logged in.
func (f *fixture) as(user string) *fixture {
	f.t.Helper()
	g := &fixture{t: f.t, a: f.a, cfg: f.cfg, url: f.url}
	jar, _ := cookiejar.New(nil)
	g.c = &http.Client{Jar: jar, Timeout: 20 * time.Second}
	status, body := g.do("POST", "/api/login", `{"username":"`+user+`","password":"pw"}`)
	if status != 200 {
		f.t.Fatalf("login %s: %d %s", user, status, body)
	}
	var s struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &s)
	g.csrf = s.CSRF
	return g
}

// proposeAssistant is an assistant on fakeConfigs, without a console.
func proposeAssistant(t *testing.T) (*Assistant, *fakeConfigs) {
	t.Helper()
	cfgs := newFakeConfigs(propBase)
	a := toolsAssistant(t, &config.Config{}, func(o *Options) { o.API = cfgs.handler() })
	return a, cfgs
}

// propose calls a propose tool in ada's conversation conv, made when it is
// not there.
func propose(t *testing.T, a *Assistant, conv, tool, args string) (string, error) {
	t.Helper()
	a.chats.mu.Lock()
	u := a.userLocked("ada")
	if u.conversation(conv) == nil {
		u.convs = append(u.convs, &Conversation{ID: conv, Title: conv})
	}
	a.chats.mu.Unlock()
	return a.Registry().Tool(tool).Run(&Call{Context: context.Background(), Args: json.RawMessage(args), User: "ada", Level: "apply",
		Settings: a.Settings(), Conversation: conv, a: a})
}

func pendingOf(a *Assistant, conv string) *Proposal {
	_, p := a.proposalOf("ada", conv)
	return p
}

// TestProposeLevels: a read or edit user gets no propose tools; a propose
// user proposes but cannot apply, while a user who applies sees and applies
// others' proposals; a level lowered during a chat stops its next call.
func TestProposeLevels(t *testing.T) {
	addRoute := `{"summary": "Add /health", "ops": [{"op": "add", "path": "/routes/-", "value": {"handler": {"module": "respond", "params": {"body": "ok"}}}}]}`
	f := startProposals(t, `"users": [{"username": "ada", "level": "apply"}, {"username": "bob", "level": "propose"}]`,
		fakeapi.Turn{Calls: []fakeapi.Call{call("p", ToolConfigPropose, addRoute)}}, fakeapi.Turn{Text: "Proposed."})
	bob := f.as("bob")
	s := bob.chat(`{"message": "add /health"}`)
	if s.end == nil || len(s.of(EventProposal)) != 1 {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	if !fakeapi.HasTool(f.model.Requests()[0], ToolConfigPropose) {
		t.Error("a propose user is not offered config_propose")
	}
	conv := s.of(EventStart)[0].Conversation
	status, body := bob.do("GET", "/api/m/ai/proposals/"+conv, "")
	var got struct{ Proposal *ProposalView }
	_ = json.Unmarshal([]byte(body), &got)
	if status != 200 || got.Proposal == nil || got.Proposal.CanApply || got.Proposal.Level != config.AssistantPropose {
		t.Fatalf("%d %s", status, body)
	}
	v := got.Proposal
	b, _ := json.Marshal(map[string]any{"id": v.ID, "revision": v.Revision, "etag": v.ETag})
	if status, body := bob.do("POST", "/api/m/ai/proposals/"+conv+"/apply", string(b)); status != 403 || !strings.Contains(body, "only a user whose level is apply") {
		t.Fatalf("%d %s", status, body)
	}
	if len(f.cfgs.changed()) != 0 {
		t.Fatal("a propose user applied")
	}
	// bob reads only his own; ada reads every user's.
	if status, _ := bob.do("GET", "/api/m/ai/proposals/x?user=ada", ""); status != 403 {
		t.Errorf("bob read ada's: %d", status)
	}
	if status, _ := bob.do("POST", "/api/m/ai/proposals/x/discard", `{"user": "ada", "id": "x"}`); status != 403 {
		t.Errorf("bob discarded ada's: %d", status)
	}
	if status, body := bob.do("GET", "/api/m/ai/proposals", ""); status != 200 || !strings.Contains(body, `"user":"bob"`) || !strings.Contains(body, "Add /health") {
		t.Errorf("%d %s", status, body)
	}
	status, list := f.do("GET", "/api/m/ai/proposals", "")
	if status != 200 || !strings.Contains(list, `"user":"bob"`) || !strings.Contains(list, `"level":"apply"`) {
		t.Fatalf("%d %s", status, list)
	}
	status, body = f.do("GET", "/api/m/ai/proposals/"+conv+"?user=bob", "")
	_ = json.Unmarshal([]byte(body), &got)
	if status != 200 || got.Proposal == nil || !got.Proposal.CanApply || got.Proposal.User != "bob" {
		t.Fatalf("%d %s", status, body)
	}
	status, out := f.applyIt(conv, got.Proposal)
	if status != 200 || out.Version != 2 {
		t.Fatalf("%d %s", status, out.raw)
	}
	entries := f.audit("?kind=apply")
	if len(entries) != 2 || entries[0].User != "ada" || entries[0].Owner != "bob" || entries[1].User != "bob" || entries[1].Outcome != OutcomeRefused {
		t.Errorf("%+v", entries)
	}
	if e := f.audit("?user=bob&kind=chat"); len(e) != 1 || e[0].Calls[0].Tool != ToolConfigPropose {
		t.Errorf("%+v", e)
	}

	// read and edit users are offered no propose tools.
	for _, level := range []string{"read", "edit"} {
		g := startProposals(t, `"default_level": "`+level+`"`, fakeapi.Turn{Calls: []fakeapi.Call{call("p", ToolConfigPropose, addRoute)}}, fakeapi.Turn{Text: "no"})
		s := g.chat(`{"message": "add /health"}`)
		if s.end == nil || fakeapi.HasTool(g.model.Requests()[0], ToolConfigPropose) || !s.of(EventToolResult)[0].Error || len(s.of(EventProposal)) != 0 {
			t.Errorf("%s: %+v", level, s.events)
		}
		if sys := fakeapi.System(g.model.Requests()[0]); strings.Contains(sys, "Proposals: to change") {
			t.Errorf("%s: %s", level, sys)
		}
	}

	// A level lowered while a chat runs, here as the model is called, after
	// the chat took its tools, stops the call that comes.
	var h *chatFixture
	var calls int
	h = startChat(t, `"default_level": "propose"`, func(r fakeapi.Request) fakeapi.Turn {
		h.a.mu.Lock()
		defer h.a.mu.Unlock()
		calls++
		if calls == 1 {
			lower := *h.a.settings
			lower.DefaultLevel = config.AssistantRead
			h.a.settings = &lower
			return fakeapi.Turn{Calls: []fakeapi.Call{call("p", ToolConfigPropose, addRoute)}}
		}
		return fakeapi.Turn{Text: "no"}
	}, func(o *Options) { o.API = newFakeConfigs(propBase).handler() })
	s = h.chat(`{"message": "add /health"}`)
	if s.end == nil || !strings.Contains(s.of(EventToolResult)[0].Content, "level is read now") || len(s.of(EventProposal)) != 0 {
		t.Errorf("%+v", s.events)
	}
}
