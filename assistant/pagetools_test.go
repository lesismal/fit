package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/assistant/model"
)

// routesPage is the page context of a page that edits routes, as the panel
// sends it.
const routesPage = `{"module": "gw.http", "page": "routes", "title": "Routes",
	"editable": {"members": ["routes"], "formats": ["conf", "yaml", "json"], "tab": "ui"}}`

// pageAnswers answers each page tool's call of a chat with answer, as the
// open page does, and keeps the calls.
type pageAnswers struct {
	f      *fixture
	answer func(Event) string // the body's result or error, as JSON members
	mu     sync.Mutex
	calls  []Event
	status []int
}

func (p *pageAnswers) on(conv *string) func(Event) {
	return func(e Event) {
		switch e.Type {
		case EventStart:
			*conv = e.Conversation
		case EventPageCall:
			p.mu.Lock()
			p.calls = append(p.calls, e)
			p.mu.Unlock()
			if p.answer == nil {
				return
			}
			status, body := p.f.do("POST", "/api/m/ai/chat/page", `{"conversation": "`+*conv+`", "id": "`+e.ID+`", `+p.answer(e)+`}`)
			p.mu.Lock()
			p.status = append(p.status, status)
			p.mu.Unlock()
			if status != 200 {
				p.f.t.Errorf("the page's answer: %d %s", status, body)
			}
		}
	}
}

// TestPageEditDraft: asked on a page to change a value, the assistant reads
// the page and drafts the change into it through the page tools; the page's
// answers are the calls' results, redacted; no config request is made, the
// version stays, and the audit log has the calls as tool calls, never as a
// change.
func TestPageEditDraft(t *testing.T) {
	edit := `{"summary": "Send /api to app2", "ops": [{"op": "replace", "path": "/routes/0/handler/params/upstream", "value": "app2"}]}`
	f := startProposals(t, `"default_level": "edit"`,
		fakeapi.Turn{Calls: []fakeapi.Call{call("g1", ToolPageGet, `{}`), call("e1", ToolPageEdit, edit)}},
		fakeapi.Turn{Text: "I drafted it on the page: review it and click Save and apply."})
	p := &pageAnswers{f: f.fixture, answer: func(e Event) string {
		if e.Name == ToolPageGet {
			return `"result": {"members": ["routes"], "tab": "ui", "unsaved": false, "draft": {"routes": [{"name": "api",
				"middleware": [{"module": "headers", "params": {"secret": "` + propSecret + `"}}]}]}}`
		}
		return `"result": {"drafted": true, "changed": ["routes[0].handler.params.upstream"], "note": "Authorization: Bearer abcdefghijklmnop1234"}`
	}}
	conv := ""
	s := f.chatCtx(context.Background(), `{"message": "send /api to app2", "page": `+routesPage+`}`, p.on(&conv))
	if s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	if len(p.calls) != 2 || p.calls[0].Name != ToolPageGet || p.calls[1].Name != ToolPageEdit || string(p.calls[1].Arguments) != compact(edit) {
		t.Fatalf("%+v", p.calls)
	}
	res := s.of(EventToolResult)
	if len(res) != 2 || res[0].Error || res[1].Error || strings.Contains(res[0].Content, propSecret) || !strings.Contains(res[0].Content, `"secret":"[redacted]"`) ||
		!strings.Contains(res[1].Content, `"drafted":true`) || strings.Contains(res[1].Content, "abcdefghijklmnop1234") {
		t.Fatalf("%+v", res)
	}
	// Nothing was asked of the config: no change, the version as it was.
	if got := f.cfgs.changed(); len(got) != 0 {
		t.Errorf("a page tool changed the config: %v", got)
	}
	if n, _ := f.cfgs.current(); n != 1 {
		t.Errorf("version %d", n)
	}
	reqs := f.model.Requests()
	sys := fakeapi.System(reqs[0])
	if !strings.Contains(sys, "Page drafts:") || !strings.Contains(sys, `"editable":{"members":["routes"]`) || !strings.Contains(sys, "describe it, and the page") ||
		!fakeapi.HasTool(reqs[0], ToolPageEdit) || !fakeapi.HasTool(reqs[0], ToolPageRevert) || fakeapi.HasTool(reqs[0], ToolConfigPropose) {
		t.Errorf("%s", sys)
	}
	for i, r := range reqs {
		if strings.Contains(string(r.Raw), propSecret) {
			t.Errorf("request %d carries the secret", i)
		}
	}
	entries := f.audit("")
	if len(entries) != 1 || entries[0].Kind != AuditChat || entries[0].Proposal != nil || entries[0].Version != 0 || len(entries[0].Calls) != 2 ||
		entries[0].Calls[1].Tool != ToolPageEdit || entries[0].Calls[1].Error {
		t.Errorf("%+v", entries)
	}
	// The call answered is not waited for any more.
	if status, _ := f.do("POST", "/api/m/ai/chat/page", `{"conversation": "`+conv+`", "id": "e1", "result": {}}`); status != 409 {
		t.Errorf("a second answer: %d", status)
	}
}

func compact(s string) string {
	var b bytes.Buffer
	_ = json.Compact(&b, []byte(s))
	return b.String()
}

// TestPageToolsOffered: the page tools by level and page: a read user gets
// page_get only, an edit user all three, and nobody gets them without a
// page that edits the config.
func TestPageToolsOffered(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{{Text: "ok"}}}
	f := startChat(t, `"default_level": "edit", "users": [{"username": "bob", "level": "read"}]`, sc.next)
	bob := f.as("bob")
	for _, tc := range []struct {
		who        *fixture
		page       string
		get, edits bool
		prompt     string
	}{
		{f.fixture, routesPage, true, true, "Page drafts:"},
		{bob, routesPage, true, false, "page_get reads what the open page holds"},
		{f.fixture, `{"module": "gw.overview", "page": "overview", "title": "Overview"}`, false, false, ""},
		{f.fixture, `{"module": "x", "editable": {"members": ["admin"]}}`, false, false, ""},
		{f.fixture, `{"module": "x", "editable": "nonsense"}`, false, false, ""},
	} {
		s := tc.who.chat(`{"message": "hi", "page": ` + tc.page + `}`)
		if s.end == nil {
			t.Fatalf("%s %s", s.err, s.body)
		}
		reqs := f.model.Requests()
		r := reqs[len(reqs)-1]
		if fakeapi.HasTool(r, ToolPageGet) != tc.get || fakeapi.HasTool(r, ToolPageEdit) != tc.edits || fakeapi.HasTool(r, ToolPageRevert) != tc.edits {
			t.Errorf("%s: tools %s", tc.page, r.Raw)
		}
		if sys := fakeapi.System(r); tc.prompt != "" && !strings.Contains(sys, tc.prompt) || tc.prompt == "" && strings.Contains(sys, "page_get") {
			t.Errorf("%s: %s", tc.page, sys)
		}
	}
	// A skill that names the page tools offers them on such a page.
	if s := f.chat(`{"message": "/route-builder answer /x with 200", "page": ` + routesPage + `}`); s.end == nil {
		t.Fatalf("%s %s", s.err, s.body)
	}
	reqs := f.model.Requests()
	if r := reqs[len(reqs)-1]; !fakeapi.HasTool(r, ToolPageEdit) || fakeapi.HasTool(r, ToolConfigPropose) || !strings.Contains(fakeapi.System(r), "draft the change there instead") {
		t.Errorf("%s", r.Raw)
	}
	// Without the page, the page tools go, and a skill naming them is told why.
	sk := &Skill{Name: "s", Tools: []string{ToolPageEdit, "config_get"}}
	c := &chat{a: f.a, level: "edit", s: f.a.Settings(), base: withPage(f.a.registry.For("edit", f.a.Settings()), nil)}
	if not := c.notOffered(sk, narrow(c.base, sk)); len(not) != 1 || !strings.Contains(not[0], "no open page edits the config") {
		t.Errorf("%v", not)
	}
	c.base = nil
	if not := c.notOffered(sk, nil); len(not) != 2 || !strings.Contains(not[0], "the model calls no tools") {
		t.Errorf("%v", not)
	}
}

// TestPageEditChecked: what fit refuses of a page_edit call before the page
// gets it, each with why: a member the page does not edit (proposed instead
// when the user may), admin, a secret, a text that does not parse, and
// arguments that are not a draft.
func TestPageEditChecked(t *testing.T) {
	page := &PageContext{Editable: json.RawMessage(`{"members": ["routes", "routes", "", "admin"], "formats": ["conf", "json"]}`)}
	check := func(level, args string) error {
		_, err := checkPageEdit(&Call{Args: json.RawMessage(args), Level: level, Page: page})
		return err
	}
	for _, tc := range []struct{ args, want string }{
		{`{"summary": "s", "members": {"upstreams": []}}`, "edits routes, not upstreams: a change no open page can hold is proposed instead, with config_propose"},
		{`{"summary": "s", "ops": [{"op": "add", "path": "/admin/max_conns", "value": 1}]}`, "admin is out of the assistant's reach"},
		{`{"summary": "s", "ops": [{"op": "replace", "path": "/routes/0/middleware/0/params/secret", "value": "x"}]}`, "/routes/0/middleware/0/params/secret is a secret field"},
		{`{"summary": "s", "ops": [{"op": "remove", "path": "/routes/0/basic_auth/password_hash"}]}`, "is a secret field"},
		{`{"summary": "s", "members": {"routes": [{"name": "a", "middleware": [{"params": {"password": "hunter2"}}]}]}}`, "/routes/0/middleware/0/params/password holds a secret"},
		{`{"summary": "s", "members": {"routes": [{"name": "a", "token": 5}]}}`, "/routes/0/token holds a secret"},
		{`{"summary": "s", "ops": [{"op": "add", "path": "/routes/-", "value": {"name": "b", "keys": ["k1"]}}]}`, "/routes/-/keys/0 holds a secret"},
		{`{"summary": "s", "ops": [{"op": "move", "path": "/routes/0"}]}`, `op is add, replace or remove, not "move"`},
		{`{"summary": "s", "ops": [{"op": "add", "path": "routes"}]}`, "is not a JSON pointer"},
		{`{"summary": "s", "ops": [{"op": "add", "path": ""}]}`, "the path names the whole config"},
		{`{"summary": "s", "ops": [{"op": "add", "path": "/routes/-"}]}`, "add needs a value"},
		{`{"summary": "s", "ops": [{"op": "add", "path": "/upstreams/-", "value": 1}]}`, "ops[0]: the open page edits routes, not upstreams"},
		{`{"summary": "s", "members": [1]}`, "members is not a JSON object"},
		{`{"summary": "s", "members": {"routes": []}, "text": "x", "format": "json"}`, "give either members and ops, or text"},
		{`{"summary": "s"}`, "give either members and ops, or text"},
		{`{"ops": [{"op": "remove", "path": "/routes/0"}]}`, "say in summary"},
		{`{"summary": "s", "text": "routes: [", "format": "yaml"}`, "the page has no yaml tab"},
		{`{"summary": "s", "text": "x", "format": "toml"}`, `format is conf, yaml or json, not "toml"`},
		{`{"summary": "s", "text": "{\"routes\": [", "format": "json"}`, "the text does not parse as json"},
		{`{"summary": "s", "text": "routes {\n", "format": "conf"}`, "the text does not parse as conf"},
		{`{"summary": "s", "text": "{\"upstreams\": []}", "format": "json"}`, "edits routes, not upstreams"},
		{`{"summary": "s", "text": "{\"routes\": [{\"name\": \"a\", \"middleware\": [{\"params\": {\"secret\": \"zz\"}}]}]}", "format": "json"}`, "holds a secret"},
		{`{"summary": "s", "text": "[1]", "format": "json"}`, "does not parse"},
		{`{"summary": 1}`, "the arguments are not what the tool takes"},
	} {
		err := check("propose", tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.args, err, tc.want)
		}
	}
	// What an edit user is told of a member the page does not edit, and of
	// no page.
	if err := check("edit", `{"summary": "s", "members": {"upstreams": []}}`); err == nil || !strings.Contains(err.Error(), "describe the change, and the page that edits it") {
		t.Errorf("%v", err)
	}
	if _, err := checkPageEdit(&Call{Args: json.RawMessage(`{"summary": "s"}`), Level: "edit"}); err == nil || !strings.Contains(err.Error(), "no open page edits the config") {
		t.Errorf("%v", err)
	}
	// Drafts that pass: redacted values copied back, ops, text, and null to remove.
	for _, args := range []string{
		`{"summary": "s", "members": {"routes": [{"name": "a", "middleware": [{"params": {"secret": "[redacted]"}}]}]}}`,
		`{"summary": "s", "members": {"routes": null}}`,
		`{"summary": "s", "ops": [{"op": "remove", "path": "/routes/0"}, {"op": "add", "path": "/routes/-", "value": {"name": "b", "match": {"path": ["/b"]}}}]}`,
		`{"summary": "s", "text": "{\"routes\": [{\"name\": \"a\", \"middleware\": [{\"params\": {\"secret\": \"[redacted]\"}}]}]}", "format": "json"}`,
		`{"summary": "s", "text": "routes {\n  name \"a\"\n}\n", "format": "conf"}`,
	} {
		if out, err := checkPageEdit(&Call{Args: json.RawMessage(args), Level: "edit", Page: page}); err != nil || out != args {
			t.Errorf("%s: %v %s", args, err, out)
		}
	}
	if e := editableOf(&PageContext{Editable: json.RawMessage(`{"members": ["admin", ""]}`)}); e != nil {
		t.Errorf("%+v", e)
	}
}

// TestPageEditRefusedInChat: a draft refused by fit's check, or over its
// bound, never reaches the page; the model is told why, and a change no
// page can hold becomes a proposal.
func TestPageEditRefusedInChat(t *testing.T) {
	big := `{"summary": "big", "text": "` + strings.Repeat("x", MaxDraftBytes) + `", "format": "json"}`
	f := startProposals(t, `"default_level": "propose"`,
		fakeapi.Turn{Calls: []fakeapi.Call{
			call("e1", ToolPageEdit, `{"summary": "an upstream", "members": {"upstreams": [{"name": "app2", "addresses": ["10.0.0.9:80"]}]}}`),
			call("e2", ToolPageEdit, big),
			call("e3", ToolPageEdit, `{"summary": "a secret", "ops": [{"op": "replace", "path": "/routes/0/middleware/0/params/secret", "value": "x"}]}`),
			call("p1", ToolConfigPropose, `{"summary": "an upstream", "members": {"upstreams": [{"name": "app", "addresses": ["10.0.0.7:9000"]}, {"name": "app2", "addresses": ["10.0.0.9:80"]}]}}`)}},
		fakeapi.Turn{Text: "The routes page cannot hold an upstream, so I proposed it."})
	p := &pageAnswers{f: f.fixture}
	conv := ""
	s := f.chatCtx(context.Background(), `{"message": "add an upstream app2", "page": `+routesPage+`}`, p.on(&conv))
	if s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	if len(p.calls) != 0 {
		t.Fatalf("a refused draft reached the page: %+v", p.calls)
	}
	res := s.of(EventToolResult)
	if len(res) != 4 || !res[0].Error || !strings.Contains(res[0].Content, "proposed instead, with config_propose") ||
		!res[1].Error || !strings.Contains(res[1].Content, "over the page's bound of 262144") || !res[2].Error || !strings.Contains(res[2].Content, "secret field") || res[3].Error {
		t.Fatalf("%+v", res)
	}
	if props := s.of(EventProposal); len(props) != 1 {
		t.Errorf("%+v", props)
	}
	if _, v, _ := f.view(conv); v == nil || !v.Valid {
		t.Errorf("the proposal: %+v", v)
	}
	if got := f.cfgs.changed(); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

// TestPageWait: the wait for the page is bounded: a page that does not
// answer fails the call, a page's error is the call's, and a browser that
// leaves ends the wait with the chat.
func TestPageWait(t *testing.T) {
	old := pageTimeout
	pageTimeout = 200 * time.Millisecond
	defer func() { pageTimeout = old }()
	sc := &script{turns: []fakeapi.Turn{
		{Calls: []fakeapi.Call{call("r1", ToolPageRevert, `{}`), call("r2", ToolPageRevert, `{}`)}},
		{Text: "The page did not answer."},
	}}
	f := startChat(t, `"default_level": "edit"`, sc.next)
	n := 0
	p := &pageAnswers{f: f.fixture}
	p.answer = func(e Event) string {
		n++
		if n == 1 {
			return `"error": "there is no draft of yours on this page"`
		}
		return ""
	}
	conv := ""
	on := p.on(&conv)
	s := f.chatCtx(context.Background(), `{"message": "undo it", "page": `+routesPage+`}`, func(e Event) {
		if e.Type == EventPageCall && n == 1 {
			n++ // the second call is left unanswered
			p.mu.Lock()
			p.calls = append(p.calls, e)
			p.mu.Unlock()
			return
		}
		on(e)
	})
	if s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	res := s.of(EventToolResult)
	if len(p.calls) != 2 || len(res) != 2 || !res[0].Error || res[0].Content != "error: there is no draft of yours on this page" ||
		!res[1].Error || !strings.Contains(res[1].Content, "the page did not answer within 200ms") {
		t.Fatalf("%+v %+v", p.calls, res)
	}
	// A late answer finds nothing waiting.
	if status, _ := f.do("POST", "/api/m/ai/chat/page", `{"conversation": "`+conv+`", "id": "r2", "result": {"reverted": true}}`); status != 409 {
		t.Errorf("late: %d", status)
	}

	// The browser leaves while the chat waits on the page.
	sc.mu.Lock()
	sc.turns, sc.n = []fakeapi.Turn{{Calls: []fakeapi.Call{call("g1", ToolPageGet, `{}`)}}}, 0
	sc.mu.Unlock()
	pageTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	conv = ""
	s = f.chatCtx(ctx, `{"conversation": "`+conv+`", "message": "what is on the page?", "page": `+routesPage+`}`, func(e Event) {
		if e.Type == EventStart {
			conv = e.Conversation
		}
		if e.Type == EventPageCall {
			cancel()
		}
	})
	if s.end != nil {
		t.Fatalf("%+v", s.end)
	}
	waitFor(t, func() bool { return !f.conversation(conv).Busy })
	c := f.conversation(conv)
	last := c.Messages[len(c.Messages)-1]
	if last.ToolName != ToolPageGet || !last.IsError {
		t.Errorf("%+v", last)
	}
}

// TestPageAnswerRefused: an answer the chat does not wait for, another
// user's, or one that is not an answer.
func TestPageAnswerRefused(t *testing.T) {
	hold := make(chan struct{})
	sc := &script{turns: []fakeapi.Turn{{Calls: []fakeapi.Call{call("g1", ToolPageGet, `{}`)}}, {Text: "done"}}}
	f := startChat(t, `"default_level": "edit", "users": [{"username": "bob", "level": "edit"}]`, sc.next)
	bob := f.as("bob")
	conv := ""
	done := make(chan *sse)
	called := make(chan Event, 1)
	go func() {
		done <- f.chatCtx(context.Background(), `{"message": "hi", "page": `+routesPage+`}`, func(e Event) {
			if e.Type == EventStart {
				conv = e.Conversation
			}
			if e.Type == EventPageCall {
				called <- e
				<-hold
			}
		})
	}()
	e := <-called
	for _, tc := range []struct {
		who  *fixture
		body string
		want int
	}{
		{f.fixture, `[]`, 400},
		{f.fixture, `{"conversation": "` + conv + `", "id": "g1"}`, 400},
		{f.fixture, `{"conversation": "` + conv + `", "id": "g1", "result": {}, "error": "x"}`, 400},
		{f.fixture, `{"conversation": "` + conv + `", "result": {}}`, 400},
		{f.fixture, `{"conversation": "` + conv + `", "id": "zz", "result": {}}`, 409},
		{f.fixture, `{"conversation": "nope", "id": "g1", "result": {}}`, 409},
		{bob, `{"conversation": "` + conv + `", "id": "g1", "result": {}}`, 409},
	} {
		if status, body := tc.who.do("POST", "/api/m/ai/chat/page", tc.body); status != tc.want {
			t.Errorf("%s: %d %s", tc.body, status, body)
		}
	}
	if status, body := f.do("POST", "/api/m/ai/chat/page", `{"conversation": "`+conv+`", "id": "`+e.ID+`", "result": ["routes"]}`); status != 200 {
		t.Errorf("%d %s", status, body)
	}
	close(hold)
	s := <-done
	if s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	if res := s.of(EventToolResult); len(res) != 1 || res[0].Error || res[0].Content != `["routes"]` {
		t.Errorf("%+v", res)
	}
}

// TestPageRunEdges: a page call's check that the pool refuses, that panics,
// or that ends after the chat did, still lets the loop go on.
func TestPageRunEdges(t *testing.T) {
	old := pageTimeout
	pageTimeout = 50 * time.Millisecond
	defer func() { pageTimeout = old }()
	f := startChat(t, "", func(fakeapi.Request) fakeapi.Turn { return fakeapi.Turn{} })
	var mu sync.Mutex
	got := 0
	next := func() { mu.Lock(); got++; mu.Unlock() }
	run := func(submit func(func()) bool, fn func(*Call) (string, error)) {
		c := &chat{a: f.a, s: f.a.Settings(), red: newRedactor(nil, nil), submit: submit, ctx: context.Background(), conv: &Conversation{}}
		c.ended = true // nothing is written
		c.runPage(&Tool{Name: ToolPageEdit, Kind: KindPage, Run: fn}, model.ToolCall{ID: "1"}, next)
	}
	inline := func(fn func()) bool { fn(); return true }
	run(func(func()) bool { return false }, func(*Call) (string, error) { return "{}", nil })
	run(inline, func(*Call) (string, error) { panic("boom") })
	run(inline, func(*Call) (string, error) { return "{}", nil }) // sent after the end: the timeout ends it
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return got == 3 })

	// Text the converters refuse, in each way they do.
	page := &PageContext{Editable: json.RawMessage(`{"members": ["routes"]}`)}
	for _, tc := range []struct{ args, want string }{
		{`{"summary": "s", "text": "routes: [", "format": "yaml"}`, "the text does not parse as yaml"},
		{`{"summary": "s", "text": "` + strings.Repeat(`x 1\n`, 12) + `", "format": "conf"}`, "does not parse as conf"},
	} {
		if _, err := checkPageEdit(&Call{Args: json.RawMessage(tc.args), Level: "edit", Page: page}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.args, err)
		}
	}
}
