package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/assistant/model"
	"github.com/lesismal/fit/config"
)

// TestChatAnswersFromTools: a question about a route, its upstream's health
// and a log error is answered from the tools, in turns: the events stream
// as they come, the tools' results go back fenced as data and redacted, no
// secret reaches the model, the user's words reach it unchanged, the page
// is in the system prompt, and the conversation is kept and listed.
func TestChatAnswersFromTools(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{
		{Text: "Let me look.", Calls: []fakeapi.Call{{ID: "c1", Name: "config_get", Args: `{"members":["routes","admin","tls"]}`},
			{ID: "c2", Name: "upstream_health", Args: `{"upstream":"api"}`}}, Prompt: 100, Completion: 20},
		{Calls: []fakeapi.Call{{ID: "c3", Name: "log_tail", Args: `{"level":"debug"}`}}, Thinking: "the log", Prompt: 300, Completion: 10},
		{Text: "The route **api** answers 502: its upstream api is down (connection refused).", Prompt: 400, Completion: 30},
	}}
	f := startChat(t, "", sc.next)
	msg := `Why does the route "api" answer 502? <b>&amp; 为什么</b>` + "\n\tsecond line"
	body, _ := json.Marshal(map[string]any{"message": msg, "lang": "en",
		"page": map[string]any{"module": "gw.reverse", "page": "routes", "title": "Routes", "selection": map[string]any{"route": "api", "token": secretToken}}})
	s := f.chat(string(body))
	if s.end == nil || s.end.Reason != ReasonAnswer || s.err != "" {
		t.Fatalf("%d %s %+v %s", s.status, s.body, s.end, s.err)
	}
	if got := s.types(); got != "start,text,text,tool_call,tool_call,usage,tool_result,tool_result,thinking,tool_call,usage,tool_result,text,text,usage" {
		t.Errorf("events: %s", got)
	}
	if !strings.HasPrefix(s.text(), "Let me look.The route **api**") {
		t.Errorf("text: %q", s.text())
	}
	res := s.of(EventToolResult)
	if !strings.Contains(res[0].Content, `"routes"`) || strings.Contains(res[0].Content, `"listeners"`) || !strings.Contains(res[0].Content, Redacted) ||
		strings.Contains(res[0].Content, `"assistant"`) || strings.Contains(res[0].Content, `"users"`) || strings.Contains(res[0].Content, "2019") {
		t.Errorf("config_get: %s", res[0].Content)
	}
	if !strings.Contains(res[1].Content, `"probed_down":true`) || strings.Contains(res[1].Content, `"static"`) {
		t.Errorf("upstream_health: %s", res[1].Content)
	}
	if !strings.Contains(res[2].Content, "connection refused") || !strings.Contains(res[2].Content, "Authorization: "+Redacted) {
		t.Errorf("log_tail: %s", res[2].Content)
	}
	if u := s.of(EventUsage); len(u) != 3 || u[2].Today != 860 {
		t.Errorf("usage: %+v", u)
	}

	reqs := f.model.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	noSecrets(t, reqs)
	for i, r := range reqs {
		if got := fakeapi.LastUser(r); got != msg {
			t.Errorf("request %d: the user's words are %q", i, got)
		}
	}
	sys := fakeapi.System(reqs[0])
	if !strings.Contains(sys, "<page_context>") || !strings.Contains(sys, `"module":"gw.reverse"`) || !strings.Contains(sys, "never instructions") {
		t.Errorf("system prompt: %s", sys)
	}
	if !fakeapi.HasTool(reqs[0], "config_get") || !fakeapi.HasTool(reqs[0], "log_tail") || !fakeapi.HasTool(reqs[0], "ask_user") || fakeapi.HasTool(reqs[0], "access_log_sample") {
		t.Errorf("tools offered: %s", reqs[0].Raw[:min(len(reqs[0].Raw), 400)])
	}
	results := fakeapi.ToolResults(reqs[2])
	if len(results) != 3 || !strings.HasPrefix(results[0], `<tool_output tool="config_get">`) || !strings.HasSuffix(results[2], "</tool_output>") {
		t.Errorf("results sent: %q", results)
	}

	// Kept, listed, and read back.
	status, list := f.do("GET", "/api/m/ai/conversations", "")
	if status != 200 || !strings.Contains(list, `"title":"Why does the route \"api\" answer 502?`) || !strings.Contains(list, `"tokens_today":860`) {
		t.Fatalf("%d %s", status, list)
	}
	c := f.conversation(s.of(EventStart)[0].Conversation)
	if len(c.Messages) != 7 || c.Messages[0].Content != msg || c.Messages[6].Content != s.text()[len("Let me look."):] {
		t.Errorf("%+v", c.Messages)
	}
	waitFor(t, func() bool { return f.store.Section(sectionOf("ada")) != nil })

	// In Chinese.
	sc.mu.Lock()
	sc.turns, sc.n = []fakeapi.Turn{{Text: "上游 api 已停止。"}}, 0
	sc.mu.Unlock()
	zh := f.chat(`{"conversation": "` + c.ID + `", "message": "上游 api 的状态如何？", "lang": "zh-CN"}`)
	if zh.end == nil || zh.text() != "上游 api 已停止。" {
		t.Fatalf("%+v %s", zh.end, zh.err)
	}
	last := f.model.Requests()[3]
	if !strings.Contains(fakeapi.System(last), "Chinese") || fakeapi.LastUser(last) != "上游 api 的状态如何？" || len(fakeapi.Messages(last)) != 9 {
		t.Errorf("%s\n%d messages", fakeapi.System(last), len(fakeapi.Messages(last)))
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestChatToolResultCut: a tool's result over its bound is cut, and says so
// to the model and the panel.
func TestChatToolResultCut(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{
		{Calls: []fakeapi.Call{{ID: "c1", Name: "config_get", Args: `{}`}}},
		{Text: "done"},
	}}
	f := startChat(t, `"limits": {"max_tool_result_bytes": 300}`, sc.next)
	s := f.chat(`{"message": "show the config"}`)
	if s.end == nil {
		t.Fatal(s.err)
	}
	r := s.of(EventToolResult)[0]
	if !r.Cut || !strings.Contains(r.Content, "[cut: the result is") || len(r.Content) > 600 {
		t.Errorf("%+v", r)
	}
	if got := fakeapi.ToolResults(f.model.Requests()[1]); len(got) != 1 || !strings.Contains(got[0], "[cut: the result is") {
		t.Errorf("%q", got)
	}
	c := f.conversation(r.Name[:0] + s.of(EventStart)[0].Conversation)
	if !c.Messages[2].Cut {
		t.Error("the kept result is not marked cut")
	}
}

// TestChatAskUser: ask_user's question shows as buttons and ends the
// stream; the choice goes back to the model as the call's result.
func TestChatAskUser(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{
		{Text: "Which one?", Calls: []fakeapi.Call{{ID: "q1", Name: "ask_user", Args: `{"question":"Which upstream?","options":["api","static"]}`},
			{ID: "q2", Name: "ask_user", Args: `{"question":"Again?","options":["x"]}`},
			{ID: "s1", Name: "status_get", Args: `{}`}}},
		{Text: "Then static it is."},
	}}
	f := startChat(t, "", sc.next)
	s := f.chat(`{"message": "add a route"}`)
	if s.end == nil || s.end.Reason != ReasonAsk {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	ask := s.of(EventAsk)
	if len(ask) != 1 || ask[0].Question != "Which upstream?" || len(ask[0].Options) != 2 || ask[0].ID != "q1" {
		t.Fatalf("%+v", ask)
	}
	id := s.end.Conversation
	c := f.conversation(id)
	if c.Ask == nil || c.Ask.ID != "q1" || c.Busy {
		t.Fatalf("%+v", c)
	}
	// A second question in one answer is refused; the other tools ran.
	if res := s.of(EventToolResult); len(res) != 2 || !res[0].Error || res[1].Name != "status_get" {
		t.Errorf("%+v", res)
	}

	for _, tc := range []struct{ body, want string }{
		{`{"conversation": "` + id + `", "answer": {"id": "q1", "choice": "nope"}}`, "400"},
		{`{"conversation": "` + id + `", "answer": {"id": "zz", "choice": "api"}}`, "409"},
		{`{"answer": {"id": "q1", "choice": "api"}}`, "400"},
	} {
		if s := f.chat(tc.body); fmt.Sprint(s.status) != tc.want {
			t.Errorf("%s: %d %s", tc.body, s.status, s.body)
		}
	}
	s = f.chat(`{"conversation": "` + id + `", "answer": {"id": "q1", "choice": "static"}}`)
	if s.end == nil || s.end.Reason != ReasonAnswer || s.text() != "Then static it is." {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	got := fakeapi.ToolResults(f.model.Requests()[1])
	if len(got) != 3 || !strings.Contains(got[2], "static") {
		t.Errorf("%q", got)
	}
	if f.conversation(id).Ask != nil {
		t.Error("the question is still open")
	}

	// A message instead of a choice closes the question.
	sc.mu.Lock()
	sc.turns, sc.n = []fakeapi.Turn{{Calls: []fakeapi.Call{{ID: "q3", Name: "ask_user", Args: `{"question":"Sure?","options":["yes","no"]}`}}}, {Text: "ok"}}, 0
	sc.mu.Unlock()
	if s = f.chat(`{"conversation": "` + id + `", "message": "go on"}`); s.end == nil || s.end.Reason != ReasonAsk {
		t.Fatalf("%+v", s.end)
	}
	if s = f.chat(`{"conversation": "` + id + `", "message": "never mind"}`); s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v", s.end)
	}
	reqs := f.model.Requests()
	if got := fakeapi.ToolResults(reqs[len(reqs)-1]); !strings.Contains(got[len(got)-1], "no choice was made") {
		t.Errorf("%q", got)
	}
}

// TestChatLimits: the loop stops at its limits: tool calls per answer,
// turns per conversation, chats at once, tokens per day.
func TestChatLimits(t *testing.T) {
	hold := make(chan struct{})
	var mu sync.Mutex
	holding := false
	f := startChat(t, `"limits": {"max_tool_calls": 3, "max_turns": 2, "max_concurrent_chats": 1, "max_tokens_per_day": 5000}`,
		func(r fakeapi.Request) fakeapi.Turn {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case holding:
				return fakeapi.Turn{Text: "slow answer", Hold: hold}
			case strings.Contains(fakeapi.LastUser(r), "spend"):
				return fakeapi.Turn{Text: "costly", Prompt: 4000, Completion: 2000}
			}
			return fakeapi.Turn{Calls: []fakeapi.Call{{ID: fmt.Sprint("a", len(fakeapi.ToolResults(r))), Name: "status_get", Args: `{}`},
				{ID: fmt.Sprint("b", len(fakeapi.ToolResults(r))), Name: "cache_zones", Args: `{}`}}, Prompt: 10, Completion: 10}
		})
	s := f.chat(`{"message": "loop"}`)
	if s.end == nil || s.end.Reason != ReasonToolCalls {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	res := s.of(EventToolResult)
	if len(res) != 4 || res[2].Error || !res[3].Error || !strings.Contains(res[3].Content, "limit of 3 tool calls") {
		t.Errorf("%+v", res)
	}
	id := s.end.Conversation
	if s = f.chat(`{"conversation": "` + id + `", "message": "spend"}`); s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	if s = f.chat(`{"conversation": "` + id + `", "message": "a third turn"}`); s.status != 409 || !strings.Contains(s.body, "2 turns") {
		t.Errorf("turns: %d %s", s.status, s.body)
	}
	if s = f.chat(`{"message": "today's tokens are spent"}`); s.status != 429 || !strings.Contains(s.body, "5000 tokens") {
		t.Errorf("tokens: %d %s", s.status, s.body)
	}

	// One chat at a time.
	f.a.chats.mu.Lock()
	f.a.chats.users["ada"].tokens = 0
	f.a.chats.mu.Unlock()
	mu.Lock()
	holding = true
	mu.Unlock()
	started := make(chan string, 1)
	done := make(chan *sse, 1)
	go func() {
		done <- f.chatCtx(context.Background(), `{"message": "hold"}`, func(e Event) {
			if e.Type == EventText {
				started <- e.Text
			}
		})
	}()
	<-started
	if s = f.chat(`{"message": "another"}`); s.status != 429 {
		t.Errorf("a second chat at once: %d %s", s.status, s.body)
	}
	close(hold)
	if s = <-done; s.end == nil || s.text() != "slow answer" {
		t.Errorf("%+v %s", s.end, s.err)
	}
}

// TestChatTokensSpentInLoop: a loop that spends the day's tokens stops
// before its next call.
func TestChatTokensSpentInLoop(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{{Calls: []fakeapi.Call{{ID: "c1", Name: "cache_zones", Args: `{}`}}, Prompt: 900, Completion: 200}}}
	f := startChat(t, `"limits": {"max_tokens_per_day": 1000}`, sc.next)
	s := f.chat(`{"message": "go"}`)
	if s.end == nil || s.end.Reason != ReasonTokensPerDay || len(f.model.Requests()) != 1 {
		t.Fatalf("%+v %s", s.end, s.err)
	}
}

// TestChatSummarizes: a conversation that outgrows its model's window has
// its early turns summarized by the model, the summary kept with it, and
// the calls after it carry the summary instead of the turns.
func TestChatSummarizes(t *testing.T) {
	// The window holds the system prompt and the read tools, with some room.
	const window = 6500
	long := strings.Repeat("The upstream api has backends in three zones. ", 60)
	var summaries int
	var mu sync.Mutex
	f := startChat(t, "", func(r fakeapi.Request) fakeapi.Turn {
		mu.Lock()
		defer mu.Unlock()
		if fakeapi.System(r) == summaryPrompt {
			summaries++
			return fakeapi.Turn{Text: "The person asked about the upstream api and its zones.", Prompt: 50, Completion: 20}
		}
		return fakeapi.Turn{Text: long[:400]}
	}, func(o *Options) {
		o.Settings.Providers[0].Models[0].Context = window
	})
	id := ""
	for i := range 8 {
		body, _ := json.Marshal(map[string]any{"conversation": id, "message": fmt.Sprintf("question %d: %s", i, long[:1200])})
		s := f.chat(string(body))
		if s.end == nil {
			t.Fatalf("turn %d: %s %d %s", i, s.err, s.status, s.body)
		}
		id = s.end.Conversation
	}
	c := f.conversation(id)
	if summaries == 0 || c.Summary == "" || c.Folded == 0 {
		t.Fatalf("%d summaries, %q folded at %d", summaries, c.Summary, c.Folded)
	}
	reqs := f.model.Requests()
	last := reqs[len(reqs)-1]
	if !strings.Contains(fakeapi.System(last), "<summary>") || strings.Contains(string(last.Raw), "question 0:") {
		t.Errorf("the last call: %d bytes, %s", len(last.Raw), fakeapi.System(last)[len(fakeapi.System(last))-200:])
	}
	for _, r := range reqs {
		if fakeapi.System(r) != summaryPrompt && tokens(string(r.Raw)) > window {
			t.Errorf("a call of %d bytes outgrew the window", len(r.Raw))
		}
	}
	if len(c.Messages) != 16 {
		t.Errorf("%d messages kept", len(c.Messages))
	}
}

// TestChatStopAndRetry: a chat stopped keeps what the answer came to; a
// retry answers the last message again.
func TestChatStopAndRetry(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	var mu sync.Mutex
	n := 0
	f := startChat(t, "", func(r fakeapi.Request) fakeapi.Turn {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return fakeapi.Turn{Text: "partial answer", Hold: hold}
		}
		return fakeapi.Turn{Text: "fresh answer"}
	})
	conv := make(chan string, 1)
	done := make(chan *sse, 1)
	go func() {
		done <- f.chatCtx(context.Background(), `{"message": "explain"}`, func(e Event) {
			if e.Type == EventStart {
				conv <- e.Conversation
			}
		})
	}()
	id := <-conv
	waitFor(t, func() bool {
		f.a.chats.mu.Lock()
		defer f.a.chats.mu.Unlock()
		c := f.a.chats.users["ada"].conversation(id)
		if c.busy == nil {
			return false
		}
		c.busy.mu.Lock()
		defer c.busy.mu.Unlock()
		return c.busy.partial.Len() > 0
	})
	if s, _ := f.do("DELETE", "/api/m/ai/conversations/"+id, ""); s != 409 {
		t.Errorf("deleting a busy conversation: %d", s)
	}
	if s := f.chat(`{"conversation": "` + id + `", "message": "again"}`); s.status != 409 {
		t.Errorf("a busy conversation: %d", s.status)
	}
	if status, body := f.do("POST", "/api/m/ai/chat/stop", `{"conversation": "`+id+`"}`); status != 200 || !strings.Contains(body, "true") {
		t.Errorf("%d %s", status, body)
	}
	s := <-done
	if s.end == nil || s.end.Reason != ReasonStopped {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	c := f.conversation(id)
	if len(c.Messages) != 2 || !c.Messages[1].Stopped || c.Messages[1].Content == "" || c.Busy {
		t.Fatalf("%+v", c.Messages)
	}
	if _, body := f.do("POST", "/api/m/ai/chat/stop", `{"conversation": "`+id+`"}`); !strings.Contains(body, "false") {
		t.Errorf("stopping a stopped one: %s", body)
	}

	s = f.chat(`{"conversation": "` + id + `", "retry": true}`)
	if s.end == nil || s.text() != "fresh answer" {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	c = f.conversation(id)
	if len(c.Messages) != 2 || c.Messages[1].Content != "fresh answer" || c.Messages[1].Stopped {
		t.Errorf("%+v", c.Messages)
	}
	reqs := f.model.Requests()
	if fakeapi.LastUser(reqs[1]) != "explain" || len(fakeapi.Messages(reqs[1])) != 2 {
		t.Errorf("the retry sent %d messages", len(fakeapi.Messages(reqs[1])))
	}
}

// TestChatBrowserLeaves: a browser that goes away stops its chat and the
// model call.
func TestChatBrowserLeaves(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	f := startChat(t, "", func(fakeapi.Request) fakeapi.Turn { return fakeapi.Turn{Text: "never ends", Hold: hold} })
	ctx, cancel := context.WithCancel(context.Background())
	conv := ""
	s := f.chatCtx(ctx, `{"message": "hi"}`, func(e Event) {
		if e.Type == EventStart {
			conv = e.Conversation
		}
		if e.Type == EventText {
			cancel()
		}
	})
	if s.end != nil {
		t.Fatalf("%+v", s.end)
	}
	waitFor(t, func() bool { return !f.conversation(conv).Busy })
	f.a.mu.Lock()
	running := f.a.running
	f.a.mu.Unlock()
	if running != 0 {
		t.Errorf("%d chats running", running)
	}
}

// TestChatRefused: what a chat refuses before it starts.
func TestChatRefused(t *testing.T) {
	f := startChat(t, `"users": [{"username": "bob", "level": "off"}]`, func(fakeapi.Request) fakeapi.Turn { return fakeapi.Turn{Status: 500} })
	for _, tc := range []struct{ body, want string }{
		{`{}`, "400"},
		{`{"message": "a", "retry": true}`, "400"},
		{`{"message": ""}`, "400"},
		{`{"message": "` + strings.Repeat("x", MaxMessageBytes+1) + `"}`, "400"},
		{`{"retry": true}`, "400"},
		{`{"conversation": "nope", "message": "a"}`, "404"},
		{`nope`, "400"},
	} {
		if s := f.chat(tc.body); fmt.Sprint(s.status) != tc.want {
			t.Errorf("%.40s: %d %s", tc.body, s.status, s.body)
		}
	}
	// A model that fails ends the stream with an error, kept with the
	// conversation, and not sent again.
	s := f.chat(`{"message": "hello"}`)
	if s.err == "" || !strings.Contains(s.err, "500") {
		t.Fatalf("%+v %q", s.end, s.err)
	}
	id := s.of(EventStart)[0].Conversation
	c := f.conversation(id)
	if len(c.Messages) != 2 || c.Messages[1].Error == "" {
		t.Errorf("%+v", c.Messages)
	}
	if s := f.chat(`{"conversation": "` + id + `", "retry": true}`); s.err == "" {
		t.Error("the retry")
	}

	// A user whose level is off.
	f.a.settings.Users = []config.AssistantUser{{Username: "ada", Level: config.AssistantOff}}
	if s := f.chat(`{"message": "a"}`); s.status != 403 {
		t.Errorf("off: %d", s.status)
	}
	// No model.
	f.a.settings = &config.Assistant{}
	if s := f.chat(`{"message": "a"}`); s.status != 409 || !strings.Contains(s.body, "no model") {
		t.Errorf("no model: %d %s", s.status, s.body)
	}
	// A provider that does not resolve.
	f.a.settings = &config.Assistant{DefaultModel: &config.AssistantModelRef{Provider: "x", Model: "y"}}
	if s := f.chat(`{"message": "a"}`); s.status != 409 || !strings.Contains(s.body, "no provider") {
		t.Errorf("no provider: %d %s", s.status, s.body)
	}
}

// TestChatToolsNotCalled: a model whose settings say it calls no tools is
// offered none; a call of a tool not offered, or with arguments that are
// not JSON, is handed back as an error.
func TestChatToolsNotCalled(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{
		{Calls: []fakeapi.Call{{ID: "c1", Name: "nope", Args: `{}`}, {ID: "c2", Name: "status_get", Args: `{"x":`}, {ID: "c3", Name: "access_log_sample", Args: `{}`}}},
		{Text: "ok"},
	}}
	f := startChat(t, "", sc.next)
	s := f.chat(`{"message": "go"}`)
	res := s.of(EventToolResult)
	if s.end == nil || len(res) != 3 || !res[0].Error || !res[1].Error || !strings.Contains(res[1].Content, "not JSON") || !strings.Contains(res[2].Content, "no tool access_log_sample") {
		t.Fatalf("%+v %+v", s.end, res)
	}

	no := false
	f.a.settings.Providers[0].Models[0].Tools = &no
	sc.mu.Lock()
	sc.turns, sc.n = []fakeapi.Turn{{Text: "plain"}}, 0
	sc.mu.Unlock()
	if s = f.chat(`{"message": "go"}`); s.end == nil {
		t.Fatal(s.err)
	}
	reqs := f.model.Requests()
	if fakeapi.HasTool(reqs[len(reqs)-1], "status_get") {
		t.Error("tools offered to a model that calls none")
	}
	status, body := f.do("GET", "/api/m/ai/tools", "")
	if status != 200 || !strings.Contains(body, `"use_tools":false`) || !strings.Contains(body, "does not call tools") {
		t.Errorf("%d %s", status, body)
	}
}

// TestToolsEndpoint: GET /api/tools lists every tool with its level and
// schema, and whether this user may call it.
func TestToolsEndpoint(t *testing.T) {
	f := startChat(t, "", func(fakeapi.Request) fakeapi.Turn { return fakeapi.Turn{} })
	status, body := f.do("GET", "/api/m/ai/tools", "")
	var b toolsBody
	if status != 200 || json.Unmarshal([]byte(body), &b) != nil {
		t.Fatalf("%d %s", status, body)
	}
	if b.Level != "read" || b.Model != "gpt-test" || !b.UseTools || len(b.Tools) != len(readTools())+len(proposeTools())+len(mediaTools())+len(syncTools())+len(btTools())+len(pageTools())+1 || b.Tools[0].Group != GroupConfig {
		t.Fatalf("%+v", b)
	}
	byName := map[string]toolView{}
	for _, tv := range b.Tools {
		byName[tv.Name] = tv
	}
	if !byName["log_tail"].Enabled || byName["access_log_sample"].Enabled || byName["access_log_sample"].Why != "data.send_access_log is off" ||
		byName["ask_user"].Kind != KindAsk || !json.Valid(byName["config_get"].Parameters) || byName["config_propose"].Enabled || byName["config_propose"].Why != "needs the level propose" {
		t.Errorf("%+v", byName)
	}
}

// TestConversationsPerUser: two users' conversations stay apart, are
// bounded in number, deleted, and hold across a restart.
func TestConversationsPerUser(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{{Text: "hi"}}}
	f := startChat(t, `"limits": {"max_conversations": 2}`, sc.next)
	var ids []string
	for i := range 3 {
		s := f.chat(fmt.Sprintf(`{"message": "ada %d"}`, i))
		if s.end == nil {
			t.Fatal(s.err)
		}
		ids = append(ids, s.end.Conversation)
	}
	_, list := f.do("GET", "/api/m/ai/conversations", "")
	if strings.Contains(list, ids[0]) || !strings.Contains(list, ids[1]) || !strings.Contains(list, ids[2]) {
		t.Errorf("the oldest is not removed: %s", list)
	}

	// bob.
	bob := &fixture{t: t, a: f.a, url: f.url, c: newClient()}
	if status, body := bob.do("POST", "/api/login", `{"username":"bob","password":"pw"}`); status != 200 {
		t.Fatal(body)
	} else {
		var s struct{ CSRF string }
		_ = json.Unmarshal([]byte(body), &s)
		bob.csrf = s.CSRF
	}
	s := bob.chat(`{"message": "bob's"}`)
	if s.end == nil {
		t.Fatal(s.err)
	}
	if status, _ := bob.do("GET", "/api/m/ai/conversations/"+ids[2], ""); status != 404 {
		t.Errorf("bob reads ada's: %d", status)
	}
	if s := bob.chat(`{"conversation": "` + ids[2] + `", "message": "x"}`); s.status != 404 {
		t.Errorf("bob writes to ada's: %d", s.status)
	}
	_, bl := bob.do("GET", "/api/m/ai/conversations", "")
	if strings.Contains(bl, ids[2]) || !strings.Contains(bl, s.end.Conversation) {
		t.Errorf("bob's list: %s", bl)
	}

	if status, _ := f.do("DELETE", "/api/m/ai/conversations/"+ids[1], ""); status != 200 {
		t.Error(status)
	}
	if status, _ := f.do("DELETE", "/api/m/ai/conversations/"+ids[1], ""); status != 404 {
		t.Error("deleted twice", status)
	}
	if status, _ := f.do("PUT", "/api/m/ai/conversations/"+ids[2], "{}"); status != 405 {
		t.Error("PUT", status)
	}

	// A restart: a new assistant on the same store.
	f.a.Close()
	g := startWith(t, f.cfg, f.store, func(o *Options) { o.API = fakeAdmin() })
	_, list = g.do("GET", "/api/m/ai/conversations", "")
	if !strings.Contains(list, ids[2]) || strings.Contains(list, ids[1]) || strings.Contains(list, s.end.Conversation) {
		t.Errorf("after a restart: %s", list)
	}
	c := g.conversation(ids[2])
	if len(c.Messages) != 2 || c.Messages[0].Content != "ada 2" {
		t.Errorf("%+v", c)
	}
	// The store's section is the store's: sealed with it, never in the
	// clear here, and per user.
	if f.store.Section(sectionOf("bob")) == nil || sectionOf("bob") == sectionOf("ada") || len(sectionOf("ada")) > 64 {
		t.Error("sections")
	}
}

// TestConversationsNotLoading: a section that does not load is started
// again, and a store that cannot write keeps them in memory.
func TestConversationsNotLoading(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{{Text: "hi"}}}
	st := &memStore{sec: map[string][]byte{sectionOf("ada"): []byte(`[1,2]`)}}
	t.Setenv("FIT_TEST_CHAT_KEY", testKey)
	m := fakeapi.StartScripted(t, testKey, sc.next)
	cfg := loadConfig(t, testSettings(m.BaseURL("openai"), ""))
	f := startWith(t, cfg, st)
	st.fail = true
	if s := f.chat(`{"message": "hello"}`); s.end == nil {
		t.Fatal(s.err)
	}
	waitFor(t, func() bool { return strings.Contains(f.logText(), "kept in memory only") })
	if !strings.Contains(f.logText(), "do not load") {
		t.Error(f.logText())
	}
	_, list := f.do("GET", "/api/m/ai/conversations", "")
	if !strings.Contains(list, `"title":"hello"`) {
		t.Error(list)
	}
}

// TestChatWithoutStore: without a store, conversations live in memory.
func TestChatWithoutStore(t *testing.T) {
	t.Setenv("FIT_TEST_CHAT_KEY", testKey)
	m := fakeapi.StartScripted(t, testKey, func(fakeapi.Request) fakeapi.Turn { return fakeapi.Turn{Text: "hi"} })
	f := startWith(t, loadConfig(t, testSettings(m.BaseURL("openai"), "")), nil)
	if s := f.chat(`{"message": "hello"}`); s.end == nil {
		t.Fatal(s.err)
	}
	if _, list := f.do("GET", "/api/m/ai/conversations", ""); !strings.Contains(list, `"kept":false`) || !strings.Contains(list, "hello") {
		t.Error(list)
	}
}

// TestChatCatalogAndExtraTools: the skills' catalog is in the system
// prompt, and tools a later iteration adds are offered by level.
func TestChatCatalogAndExtraTools(t *testing.T) {
	sc := &script{turns: []fakeapi.Turn{{Calls: []fakeapi.Call{{ID: "p", Name: "extra_tool", Args: `{}`}}}, {Text: "ok"}}}
	var emitted []Event
	f := startChat(t, `"default_level": "propose"`, sc.next, func(o *Options) {
		o.Tools = []*Tool{{Name: "extra_tool", Group: GroupChange, Level: config.AssistantPropose, Parameters: json.RawMessage(`{"type":"object"}`),
			Run: func(c *Call) (string, error) {
				c.Emit(Event{Type: "proposal", Text: c.Conversation})
				emitted = append(emitted, Event{Text: c.User + "/" + c.Level})
				panic("boom")
			}}}
	})
	s := f.chat(`{"message": "x"}`)
	if s.end == nil {
		t.Fatal(s.err)
	}
	sys := fakeapi.System(f.model.Requests()[0])
	if !strings.Contains(sys, "- diagnose: Finds why a route answers 502 or 504") || !strings.Contains(sys, "level is propose") {
		t.Errorf("%s", sys)
	}
	if len(emitted) != 1 || emitted[0].Text != "ada/propose" || len(s.of("proposal")) != 1 || !strings.Contains(s.of(EventToolResult)[0].Content, "boom") {
		t.Errorf("%+v %+v", emitted, s.events)
	}
	if _, err := New(Options{Settings: f.cfg.Admin.Assistant, Config: f.a.o.Config, Tools: []*Tool{{Name: "config_get"}}}); err == nil {
		t.Error("a tool named twice")
	}
}

// TestChatToolTimeoutAndBusy: a tool past its timeout, or a pool that is
// full, fails the call and the loop goes on.
func TestChatToolTimeoutAndBusy(t *testing.T) {
	old := toolTimeout
	toolTimeout = 50 * time.Millisecond
	defer func() { toolTimeout = old }()
	release := make(chan struct{})
	sc := &script{turns: []fakeapi.Turn{{Calls: []fakeapi.Call{{ID: "s", Name: "slow", Args: `{}`}}}, {Text: "ok"}}}
	f := startChat(t, "", sc.next, func(o *Options) {
		o.Tools = []*Tool{{Name: "slow", Group: GroupAgent, Level: config.AssistantRead, Parameters: json.RawMessage(`{"type":"object"}`),
			Run: func(*Call) (string, error) { <-release; return "late", nil }}}
	})
	s := f.chat(`{"message": "x"}`)
	close(release)
	if s.end == nil || !strings.Contains(s.of(EventToolResult)[0].Content, "took longer") {
		t.Fatalf("%+v %+v", s.end, s.events)
	}

	// The pool refuses.
	c := &chat{a: f.a, s: f.a.Settings(), red: newRedactor(nil, nil), submit: func(func()) bool { return false }, ctx: context.Background(),
		conv: &Conversation{}, r: nil}
	c.ended = true // nothing is written
	got := ""
	c.runPool(&Tool{Name: "x", Run: func(*Call) (string, error) { return "", nil }}, model.ToolCall{ID: "1"}, func() { got = "next" })
	if got != "next" {
		t.Error("the loop did not go on")
	}
}

// TestChatSlowBrowser: a quiet stream gets pings; a browser slow to take a
// large answer is waited for; one that takes nothing is cut after
// stallTimeout, which stops its chat.
func TestChatSlowBrowser(t *testing.T) {
	oldPing, oldStall := pingInterval, stallTimeout
	pingInterval, stallTimeout = 20*time.Millisecond, 2*time.Second
	defer func() { pingInterval, stallTimeout = oldPing, oldStall }()
	big := strings.Repeat("0123456789abcdef", 6<<16) // 6 MiB
	hold := make(chan struct{})
	var mu sync.Mutex
	n := 0
	f := startChat(t, "", func(fakeapi.Request) fakeapi.Turn {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return fakeapi.Turn{Text: "ab", Hold: hold}
		}
		return fakeapi.Turn{Text: big}
	})
	go func() { time.Sleep(150 * time.Millisecond); close(hold) }()
	post := func(body string) *http.Response {
		req, _ := http.NewRequest("POST", f.url+"/api/m/ai/chat", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Fit-Csrf", f.csrf)
		resp, err := f.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post(`{"message": "ping me"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), ": ping") || !strings.Contains(string(body), `"reason":"answer"`) {
		t.Errorf("%.300s", body)
	}

	// A large answer, read slowly, then not at all.
	s := f.chatCtx(context.Background(), `{"message": "big"}`, func(e Event) {
		if e.Type == EventText {
			time.Sleep(time.Millisecond)
		}
	})
	if s.end == nil || len(s.text()) != len(big) {
		t.Fatalf("%+v %s, %d bytes", s.end, s.err, len(s.text()))
	}
	stallTimeout = 200 * time.Millisecond
	resp = post(`{"message": "big again"}`)
	defer resp.Body.Close()
	var id string
	waitFor(t, func() bool {
		f.a.chats.mu.Lock()
		defer f.a.chats.mu.Unlock()
		for _, c := range f.a.chats.users["ada"].convs {
			if c.Title == "big again" && c.busy == nil {
				id = c.ID
				return true
			}
		}
		return false
	})
	if c := f.conversation(id); len(c.Messages) != 2 {
		t.Errorf("a stalled browser's chat: %d messages", len(c.Messages))
	}
}
