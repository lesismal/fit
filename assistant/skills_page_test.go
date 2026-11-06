package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
)

// skillsList is GET /api/skills's answer.
type skillsList struct {
	Skills    []skillView `json:"skills"`
	Level     string      `json:"level"`
	CanChange bool        `json:"can_change"`
	Kept      bool        `json:"kept"`
	Max       int         `json:"max"`
}

func (f *fixture) skills() skillsList {
	f.t.Helper()
	status, body := f.do("GET", "/api/m/ai/skills", "")
	var l skillsList
	if status != 200 || json.Unmarshal([]byte(body), &l) != nil {
		f.t.Fatalf("%d %s", status, body)
	}
	return l
}

func skillJSON(src string) string {
	b, _ := json.Marshal(skillBody{Source: src})
	return string(b)
}

const opsSkill = `---
name: ops-check
title: Ops check
title_zh: 运维检查
description: Checks what the ops team checks every morning.
tools: [status_get, config_propose, no_such_tool]
---
Read the status and say whether the listeners are bound.
`

// TestUserSkills: a skill added on the Skills page is listed, read whole,
// changed, enabled and disabled; runs by /name; holds across a restart with
// the built-ins disabled; and is refused, with its line, when its front
// matter is wrong. Only a user whose level is apply changes skills.
func TestUserSkills(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{{Text: "All bound."}}}
	f := startChat(t, `"default_level": "apply", "users": [{"username": "bob", "level": "propose"}]`, sc.next)
	l := f.skills()
	if len(l.Skills) != len(builtinOrder) || !l.CanChange || !l.Kept || l.Max != MaxUserSkills || l.Level != "apply" ||
		!l.Skills[0].Builtin || !l.Skills[0].Enabled || l.Skills[0].Prompt != "" || l.Skills[0].Source != "" {
		t.Fatalf("%+v", l)
	}

	// Refused with its line.
	if status, _ := f.do("POST", "/api/m/ai/skills", "not json"); status != 400 {
		t.Error(status)
	}
	status, body := f.do("POST", "/api/m/ai/skills", skillJSON("---\nname: x\ntitle: X\ndescription: d\ncolour: red\n---\np"))
	if status != 400 || !strings.Contains(body, `"line":5`) || !strings.Contains(body, `line 5: \"colour\" is not a key`) {
		t.Errorf("%d %s", status, body)
	}
	if status, body := f.do("POST", "/api/m/ai/skills", skillJSON(strings.Replace(opsSkill, "ops-check", "diagnose", 1))); status != 409 ||
		!strings.Contains(body, "a built-in skill is named diagnose") {
		t.Errorf("%d %s", status, body)
	}

	// Added.
	status, body = f.do("POST", "/api/m/ai/skills", skillJSON(opsSkill))
	var v skillView
	if status != 200 || json.Unmarshal([]byte(body), &v) != nil || v.Name != "ops-check" || v.Builtin || !v.Enabled || v.By != "ada" ||
		v.Source != opsSkill || v.Prompt == "" || len(v.State) != 3 || !v.State[0].OK || !v.State[1].OK || v.State[2].OK ||
		v.State[2].Why != "this fit has no such tool" {
		t.Fatalf("%d %s", status, body)
	}
	if status, body := f.do("POST", "/api/m/ai/skills", skillJSON(opsSkill)); status != 409 || !strings.Contains(body, "named ops-check already") {
		t.Errorf("%d %s", status, body)
	}
	if !strings.Contains(f.logText(), `console: "ada" added the assistant's skill ops-check`) {
		t.Error(f.logText())
	}
	if status, body := f.do("GET", "/api/m/ai/skills/ops-check", ""); status != 200 || !strings.Contains(body, `"source":"---\nname: ops-check`) {
		t.Errorf("%d %s", status, body)
	}
	if status, _ := f.do("GET", "/api/m/ai/skills/nope", ""); status != 404 {
		t.Error(status)
	}
	if status, body := f.do("GET", "/api/m/ai/skills/explain", ""); status != 200 || !strings.Contains(body, `"builtin":true`) || !strings.Contains(body, `"prompt":"You explain fit`) {
		t.Errorf("%d %s", status, body)
	}

	// It runs, by /name.
	s := f.chat(`{"message": "/ops-check is everything bound?"}`)
	if s.end == nil || s.of(EventStart)[0].Skill != "ops-check" {
		t.Fatalf("%+v", s)
	}
	sys := fakeapi.System(f.model.Requests()[0])
	if !strings.Contains(sys, "Read the status and say whether the listeners are bound.") || !strings.Contains(sys, "no_such_tool (this fit has no such tool)") {
		t.Errorf("%s", sys)
	}

	// Changed, and renamed; a built-in is neither changed nor deleted.
	changed := strings.Replace(opsSkill, "every morning", "every evening", 1)
	if status, body := f.do("PUT", "/api/m/ai/skills/ops-check", skillJSON(changed)); status != 200 || !strings.Contains(body, "every evening") {
		t.Errorf("%d %s", status, body)
	}
	renamed := strings.Replace(changed, "name: ops-check", "name: ops-daily", 1)
	if status, body := f.do("PUT", "/api/m/ai/skills/ops-check", skillJSON(renamed)); status != 200 || !strings.Contains(body, `"name":"ops-daily"`) {
		t.Errorf("%d %s", status, body)
	}
	for _, c := range []struct {
		method, path, body string
		status             int
		msg                string
	}{
		{"PUT", "/api/m/ai/skills/explain", skillJSON(opsSkill), 409, "a built-in skill is not changed"},
		{"PUT", "/api/m/ai/skills/nope", skillJSON(opsSkill), 404, "no skill nope was added"},
		{"PUT", "/api/m/ai/skills/ops-daily", skillJSON(strings.Replace(opsSkill, "ops-check", "explain", 1)), 409, "a built-in skill is named explain"},
		{"PUT", "/api/m/ai/skills/ops-daily", skillJSON("x"), 400, "line 1"},
		{"DELETE", "/api/m/ai/skills/explain", "", 409, "disable it"},
		{"DELETE", "/api/m/ai/skills/nope", "", 404, "no skill nope was added"},
		{"POST", "/api/m/ai/skills/nope/enable", "{}", 404, "no skill nope"},
		{"PATCH", "/api/m/ai/skills/explain", "{}", 405, "GET, PUT or DELETE"},
	} {
		if status, body := f.do(c.method, c.path, c.body); status != c.status || !strings.Contains(body, c.msg) {
			t.Errorf("%s %s: %d %s", c.method, c.path, status, body)
		}
	}

	// Enabled and disabled: a disabled skill is out of the catalog and not
	// run.
	for _, p := range []string{"/api/m/ai/skills/ops-daily/disable", "/api/m/ai/skills/explain/disable", "/api/m/ai/skills/diagnose/disable",
		"/api/m/ai/skills/diagnose/enable"} {
		if status, body := f.do("POST", p, "{}"); status != 200 {
			t.Fatalf("%s: %d %s", p, status, body)
		}
	}
	if s := f.chat(`{"message": "x", "skill": "ops-daily"}`); s.status != 404 {
		t.Errorf("%d %s", s.status, s.body)
	}
	cat := f.a.Catalog()
	for _, e := range cat {
		if e.Name == "explain" || e.Name == "ops-daily" {
			t.Errorf("%+v", cat)
		}
	}
	if len(cat) != len(builtinOrder)-1 {
		t.Errorf("%d in the catalog", len(cat))
	}
	if status, _ := f.do("POST", "/api/m/ai/skills/ops-daily/enable", "{}"); status != 200 {
		t.Error(status)
	}

	// bob, whose level is propose, reads and runs them, and changes none.
	bob := &fixture{t: t, a: f.a, url: f.url, c: newClient()}
	if status, body := bob.do("POST", "/api/login", `{"username":"bob","password":"pw"}`); status != 200 {
		t.Fatal(body)
	} else {
		var s struct{ CSRF string }
		_ = json.Unmarshal([]byte(body), &s)
		bob.csrf = s.CSRF
	}
	if l := bob.skills(); l.CanChange || len(l.Skills) != len(builtinOrder)+1 {
		t.Errorf("%+v", l)
	}
	for _, c := range [][3]string{{"POST", "/api/m/ai/skills", skillJSON(opsSkill)}, {"PUT", "/api/m/ai/skills/ops-daily", skillJSON(opsSkill)},
		{"DELETE", "/api/m/ai/skills/ops-daily", ""}, {"POST", "/api/m/ai/skills/ops-daily/disable", "{}"}} {
		if status, body := bob.do(c[0], c[1], c[2]); status != 403 || !strings.Contains(body, "needs the level apply") {
			t.Errorf("%v: %d %s", c, status, body)
		}
	}
	if s := bob.chat(`{"message": "/ops-daily go"}`); s.end == nil || s.of(EventStart)[0].Skill != "ops-daily" {
		t.Errorf("%+v", s)
	}

	// A restart: the skill and the built-in disabled hold.
	f.a.Close()
	g := startWith(t, f.cfg, f.store, func(o *Options) { o.API = fakeAdmin() })
	l = g.skills()
	byName := map[string]skillView{}
	for _, v := range l.Skills {
		byName[v.Name] = v
	}
	if v := byName["ops-daily"]; v.Builtin || !v.Enabled || v.By != "ada" || v.At == 0 || byName["explain"].Enabled || !byName["diagnose"].Enabled {
		t.Errorf("after a restart: %+v", l.Skills)
	}
	if g.a.EnabledSkill("ops-daily") == nil || g.a.EnabledSkill("explain") != nil {
		t.Error("enabled")
	}

	// Deleted.
	if status, body := g.do("DELETE", "/api/m/ai/skills/ops-daily", ""); status != 200 || !strings.Contains(body, `"deleted":"ops-daily"`) {
		t.Errorf("%d %s", status, body)
	}
	if g.a.EnabledSkill("ops-daily") != nil {
		t.Error("still there")
	}
}

// TestUserSkillsBounds: the skills added are bounded in number; a store
// that fails keeps what was; a store whose skills no longer load leaves
// them out and says so; without a store they last until a restart.
func TestUserSkillsBounds(t *testing.T) {
	f := startChat(t, `"default_level": "apply"`, func(fakeapi.Request) fakeapi.Turn { return fakeapi.Turn{Text: "ok"} })
	for i := range MaxUserSkills {
		src := fmt.Sprintf("---\nname: s%d\ntitle: S\ndescription: d\n---\np\n", i)
		if status, body := f.do("POST", "/api/m/ai/skills", skillJSON(src)); status != 200 {
			t.Fatalf("%d: %d %s", i, status, body)
		}
	}
	if status, body := f.do("POST", "/api/m/ai/skills", skillJSON("---\nname: one-more\ntitle: S\ndescription: d\n---\np\n")); status != 409 ||
		!strings.Contains(body, "as many as may be") {
		t.Errorf("%d %s", status, body)
	}
	f.store.mu.Lock()
	f.store.fail = true
	f.store.mu.Unlock()
	if status, body := f.do("DELETE", "/api/m/ai/skills/s0", ""); status != 500 || !strings.Contains(body, "the disk is full") {
		t.Errorf("%d %s", status, body)
	}
	if f.a.EnabledSkill("s0") == nil {
		t.Error("a change the store refused was used")
	}

	// The store's section: not JSON, or a skill that no longer loads.
	st := &memStore{}
	_ = st.SetSection(skillsSection, []byte("not json"))
	g := startWith(t, f.cfg, st)
	if !strings.Contains(g.logText(), "the skills kept in the store do not load") || len(g.a.EnabledSkills()) != len(builtinOrder) {
		t.Error(g.logText())
	}
	k, _ := json.Marshal(keptSkills{User: []keptSkill{{Source: "broken"}, {Source: strings.Replace(opsSkill, "ops-check", "diagnose", 1)},
		{Source: opsSkill, Disabled: true}}, Disabled: []string{"explain"}})
	_ = st.SetSection(skillsSection, k)
	g = startWith(t, f.cfg, st)
	if strings.Count(g.logText(), "a skill kept in the store does not load") != 2 || g.a.EnabledSkill("ops-check") != nil || g.a.EnabledSkill("explain") != nil {
		t.Error(g.logText())
	}
	if sk, builtin, on := g.a.skillSnapshot().find("ops-check"); sk == nil || builtin || on {
		t.Error("ops-check")
	}

	// Without a store.
	h := startWith(t, f.cfg, nil)
	if status, body := h.do("POST", "/api/m/ai/skills", skillJSON(opsSkill)); status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	if l := h.skills(); l.Kept || h.a.EnabledSkill("ops-check") == nil {
		t.Errorf("%+v", l)
	}
}
