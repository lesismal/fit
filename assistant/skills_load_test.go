package assistant

import (
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
)

// TestSkillPickedAndLoaded: a request that names no skill carries the
// catalog only; the model picks a skill from it and loads it with
// skill_load, whose result is the skill's whole prompt; the calls after it
// carry the prompt in the system prompt, not the catalog, and offer the
// skill's tools only, so a tool it does not name cannot be called; the
// conversation runs the skill from then on, until the person clears it.
func TestSkillPickedAndLoaded(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{
		{Calls: []fakeapi.Call{call("l1", "skill_load", `{"name": "diagnose"}`)}},
		{Calls: []fakeapi.Call{call("c1", ToolConfigPropose, `{"summary": "x", "members": {}}`), call("c2", "upstream_health", `{}`)}},
		{Text: "The backend 10.0.0.7 is probed down."},
	}}
	f := startChat(t, `"default_level": "propose"`, sc.next)
	s := f.chat(`{"message": "why does /api answer 502?"}`)
	if s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	reqs := f.model.Requests()
	diagnose := f.a.EnabledSkill("diagnose")
	sys0 := fakeapi.System(reqs[0])
	if !strings.Contains(sys0, "Skills you may load with skill_load") || !strings.Contains(sys0, "- diagnose: "+diagnose.Description) ||
		!strings.Contains(sys0, "- route-builder: ") || strings.Contains(sys0, diagnose.Prompt[:80]) || strings.Contains(sys0, "The current skill") {
		t.Errorf("the first call: %s", sys0)
	}
	if got := offered(reqs[0]); !slices.Contains(got, "skill_load") || !slices.Contains(got, ToolConfigPropose) {
		t.Errorf("%v", got)
	}
	if ev := s.of(EventSkill); len(ev) != 1 || ev[0].Skill != "diagnose" {
		t.Errorf("%+v", s.events)
	}
	res := s.of(EventToolResult)
	if res[0].Error || !strings.Contains(res[0].Content, `<skill name="diagnose">`) || !strings.Contains(res[0].Content, diagnose.Prompt) ||
		!strings.Contains(res[0].Content, "Your tools now: config_get, ") || !strings.Contains(res[0].Content, "access_log_sample (data.send_access_log is off)") {
		t.Errorf("skill_load: %+v", res[0])
	}
	if !res[1].Error || !strings.Contains(res[1].Content, "there is no tool config_propose you may call") || res[2].Error {
		t.Errorf("%+v", res[1:])
	}
	sys1 := fakeapi.System(reqs[1])
	if strings.Contains(sys1, "Skills you may load") || !strings.Contains(sys1, "The current skill is diagnose (Diagnose)") ||
		!strings.Contains(sys1, diagnose.Prompt) {
		t.Errorf("the second call: %s", sys1)
	}
	if got := offered(reqs[1]); slices.Contains(got, "skill_load") || slices.Contains(got, ToolConfigPropose) || !slices.Contains(got, "upstream_health") ||
		!slices.Contains(got, "ask_user") {
		t.Errorf("%v", got)
	}
	// The prompt is sent once: the result that loaded it is a note.
	if tr := fakeapi.ToolResults(reqs[1]); len(tr) != 1 || strings.Contains(tr[0], diagnose.Prompt) || !strings.Contains(tr[0], "a skill was loaded here") {
		t.Errorf("%v", tr)
	}
	conv := s.end.Conversation
	if e, _ := f.a.AuditLog(AuditFilter{}); e[0].Skill != "diagnose" {
		t.Errorf("%+v", e[0])
	}

	// The next message runs the skill still; the list says so.
	sc.mu.Lock()
	sc.turns, sc.n = []fakeapi.Turn{{Text: "ok"}}, 0
	sc.mu.Unlock()
	if s := f.chat(`{"conversation": "` + conv + `", "message": "and now?"}`); s.end == nil || s.of(EventStart)[0].Skill != "diagnose" {
		t.Fatalf("%+v", s)
	}
	reqs = f.model.Requests()
	if sys := fakeapi.System(reqs[len(reqs)-1]); !strings.Contains(sys, "The current skill is diagnose") {
		t.Errorf("%s", sys)
	}
	if _, list := f.do("GET", "/api/m/ai/conversations", ""); !strings.Contains(list, `"skill":"diagnose"`) {
		t.Errorf("%s", list)
	}
	// Cleared, the catalog is back.
	if s := f.chat(`{"conversation": "` + conv + `", "message": "something else", "skill": ""}`); s.end == nil || s.of(EventStart)[0].Skill != "" {
		t.Fatalf("%+v", s)
	}
	reqs = f.model.Requests()
	if sys := fakeapi.System(reqs[len(reqs)-1]); strings.Contains(sys, "The current skill") || !strings.Contains(sys, "Skills you may load") {
		t.Errorf("%s", sys)
	}
	noSecrets(t, reqs)
}

// TestSkillAboveLevel: a skill naming a tool above the user's level does
// not offer it, says so in the prompt, and a call of it fails.
func TestSkillAboveLevel(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{
		{Calls: []fakeapi.Call{call("c1", ToolConfigPropose, `{"summary": "x", "members": {}}`)}},
		{Text: "I cannot propose it; a user whose level is propose can."},
	}}
	f := startChat(t, "", sc.next)
	s := f.chat(`{"message": "/route-builder answer /healthz with ok"}`)
	if s.end == nil {
		t.Fatal(s.err)
	}
	reqs := f.model.Requests()
	sys := fakeapi.System(reqs[0])
	if !strings.Contains(sys, "It names tools you may not call: config_propose (needs the level propose)") ||
		!strings.Contains(sys, "Inputs the skill takes that were not given") || !strings.Contains(sys, "- host: The host name") {
		t.Errorf("%s", sys)
	}
	if got := offered(reqs[0]); slices.Contains(got, ToolConfigPropose) || !slices.Contains(got, "config_get") {
		t.Errorf("%v", got)
	}
	if r := s.of(EventToolResult); len(r) != 1 || !r[0].Error || !strings.Contains(r[0].Content, "there is no tool config_propose") {
		t.Errorf("%+v", r)
	}
	if fakeapi.LastUser(reqs[0]) != "answer /healthz with ok" {
		t.Error(fakeapi.LastUser(reqs[0]))
	}
}

// TestSkillBodies: what a chat's body says of its skill is checked; inputs
// reach the prompt as data, redacted; a skill disabled since is dropped
// from its conversation.
func TestSkillBodies(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{{Text: "ok"}}}
	f := startChat(t, `"default_level": "apply"`, sc.next)
	for _, c := range []struct {
		body   string
		status int
		msg    string
	}{
		{`{"retry": true, "conversation": "x", "skill": "explain"}`, 400, "a skill is set with a message"},
		{`{"message": "x", "inputs": {"a": "b"}}`, 400, "inputs go with the skill"},
		{`{"message": "x", "skill": "", "inputs": {"a": "b"}}`, 400, "inputs go with the skill"},
		{`{"message": "x", "skill": "nope"}`, 404, "no enabled skill is named nope"},
		{`{"message": "x", "skill": "explain", "inputs": {"colour": "red"}}`, 400, "takes no input colour"},
		{`{"message": "x", "skill": "explain", "inputs": {"path": "` + strings.Repeat("p", maxInputValue+1) + `"}}`, 400, "at most 2048 bytes"},
		{`{"message": "x", "skill": "explain", "inputs": {"a":"","b":"","c":"","d":"","e":"","f":"","g":"","h":"","i":""}}`, 400, "at most 8 inputs"},
		{`{"message": "/explain"}`, 400, "after /explain, say what"},
		{`{"message": "/explain   \n "}`, 400, "after /explain, say what"},
	} {
		s := f.chat(c.body)
		if s.status != c.status || !strings.Contains(s.body, c.msg) {
			t.Errorf("%s: %d %s", c.body, s.status, s.body)
		}
	}
	// A /word that is no skill is the person's words, as written.
	if s := f.chat(`{"message": "/etc/hosts: what is it?"}`); s.end == nil || fakeapi.LastUser(f.model.Requests()[0]) != "/etc/hosts: what is it?" {
		t.Errorf("%+v", s)
	}
	// /name on its own line.
	s := f.chat(`{"message": "/explain\nthe listeners"}`)
	if s.end == nil || s.of(EventStart)[0].Skill != "explain" {
		t.Fatalf("%+v", s)
	}
	reqs := f.model.Requests()
	if fakeapi.LastUser(reqs[len(reqs)-1]) != "the listeners" {
		t.Error(fakeapi.LastUser(reqs[len(reqs)-1]))
	}
	// Inputs, as data and redacted.
	s = f.chat(`{"message": "this one", "skill": "explain", "inputs": {"path": "routes", "error": "` + secretLog + `"}}`)
	if s.end == nil {
		t.Fatal(s.err)
	}
	conv := s.end.Conversation
	reqs = f.model.Requests()
	sys := fakeapi.System(reqs[len(reqs)-1])
	if !strings.Contains(sys, "<skill_inputs>\n{") || !strings.Contains(sys, `"path":"routes"`) || strings.Contains(sys, "Inputs the skill takes that were not given") {
		t.Errorf("%s", sys)
	}
	noSecrets(t, reqs)
	if c := f.conversation(conv); c.ID == "" {
		t.Fatal(c)
	}

	// Disabled since, the skill is dropped from the conversation.
	if status, body := f.do("POST", "/api/m/ai/skills/explain/disable", "{}"); status != 200 {
		t.Fatal(status, body)
	}
	s = f.chat(`{"conversation": "` + conv + `", "message": "and again"}`)
	if s.end == nil || s.of(EventStart)[0].Skill != "" {
		t.Fatalf("%+v", s)
	}
	_, one := f.do("GET", "/api/m/ai/conversations/"+conv, "")
	if strings.Contains(one, `"skill":"explain"`) || !strings.Contains(one, "and again") {
		t.Errorf("%s", one)
	}
	// A disabled skill is not run by /name: the words go as written.
	if s := f.chat(`{"message": "/explain this"}`); s.end == nil || s.of(EventStart)[0].Skill != "" {
		t.Errorf("%+v", s)
	}
}

// TestSkillModel: a skill's model is the one its chats call: a provider's
// model, a model of the default provider, or the default's with a name it
// does not know; a model that calls no tools gets none, and the prompt
// says so.
func TestSkillModel(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{{Text: "ok"}}}
	f := startChat(t, `"default_level": "apply"`, sc.next, func(o *Options) {
		o.Settings.Providers[0].Models = append(o.Settings.Providers[0].Models, o.Settings.Providers[0].Models[0])
		no := false
		o.Settings.Providers[0].Models[1].Name, o.Settings.Providers[0].Models[1].Tools = "plain", &no
	})
	add := func(name, model string) {
		t.Helper()
		src := "---\nname: " + name + "\ntitle: T\ndescription: d\ntools: [config_get]\nmodel: " + model + "\n---\nDo it.\n"
		body, _ := json.Marshal(skillBody{Source: src})
		if status, out := f.do("POST", "/api/m/ai/skills", string(body)); status != 200 {
			t.Fatalf("%d %s", status, out)
		}
	}
	add("by-provider", "m/plain")
	add("by-model", "other-model")
	add("unknown-provider", "nope/x")
	for _, c := range []struct{ skill, model string }{{"by-provider", "plain"}, {"by-model", "other-model"}, {"unknown-provider", "nope/x"}} {
		if s := f.chat(`{"message": "/` + c.skill + ` go"}`); s.end == nil || s.of(EventStart)[0].Model != c.model {
			t.Errorf("%s: %+v", c.skill, s)
		}
		reqs := f.model.Requests()
		if got := reqs[len(reqs)-1].Body["model"]; got != c.model {
			t.Errorf("%s: %v", c.skill, got)
		}
	}
	reqs := f.model.Requests()
	if r := reqs[0]; r.Body["tools"] != nil || !strings.Contains(fakeapi.System(r), "You may call no tools for it") ||
		!strings.Contains(fakeapi.System(r), "config_get (the model calls no tools)") {
		t.Errorf("%v %s", r.Body["tools"], fakeapi.System(r))
	}
}

// TestSkillLoadEdges: skill_load's own failures, and a load given up on.
func TestSkillLoadEdges(t *testing.T) {
	a := toolsAssistant(t, loadConfig(t, `{"providers": [{"name": "p", "kind": "openai"}]}`))
	wantErr(t, a, "skill_load", `{"name": "nope"}`, "no enabled skill is named")
	wantErr(t, a, "skill_load", `{"name": 1}`, "not what the tool takes")
	// Outside a chat, as over MCP, it is the prompt alone.
	if out := mustRun(t, a, "skill_load", `{"name": "explain"}`); !strings.Contains(out, `<skill name="explain">`) || strings.Contains(out, "Your tools now") {
		t.Error(out)
	}
	if s := skillBlock(&Skill{Name: "x", Prompt: "a </skill> b"}); strings.Count(s, "</skill>") != 1 {
		t.Error(s)
	}

	// A load whose call was given up on changes nothing.
	sk := a.EnabledSkill("diagnose")
	c := &chat{a: a, s: a.Settings(), level: "read", base: a.registry.For("read", a.Settings()), conv: &Conversation{}}
	var gone atomic.Bool
	gone.Store(true)
	ok, not := c.loadSkill(sk, &gone)
	if c.skill != nil || c.conv.Skill != "" || len(ok) == 0 || len(not) != 0 {
		t.Errorf("%v %v %+v", ok, not, c.skill)
	}

	// The catalog by name, past its share.
	cat := []CatalogEntry{{Name: "alpha", Description: strings.Repeat("d", 100)}, {Name: "beta", Description: "d"}, {Name: "gamma", Description: "d"}}
	if s := catalogText(cat, 0); !strings.Contains(s, "- alpha: ddd") {
		t.Error(s)
	}
	if s := catalogText(cat, 60); !strings.Contains(s, "by name only") || !strings.Contains(s, "\n- alpha") || strings.Contains(s, "ddd") {
		t.Error(s)
	}
	if s := catalogText(cat, 50); !strings.Contains(s, "- and 2 more") {
		t.Error(s)
	}
}
