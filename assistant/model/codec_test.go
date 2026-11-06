package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
)

// conversation is every kind of message: a user's, an answer with text and
// two calls (one whose arguments were not JSON), their results (one an
// object, one an error), and the user again.
func conversation() *Request {
	temp := 0.7
	return &Request{Model: "m", System: "sys", Temperature: &temp, Thinking: ThinkingHigh, MaxTokens: 100,
		Tools: []Tool{statusTool, {Name: "bare", Description: "no params"}},
		Messages: []Message{
			{Role: RoleUser, Content: "q"},
			{Role: RoleAssistant, Content: "let me look", Thinking: "hmm", ThinkingSignature: "sig",
				ToolCalls: []ToolCall{{ID: "c1", Name: "get_status", Arguments: json.RawMessage(`{"listener":"web"}`), Signature: "ts"},
					{ID: "c2", Name: "bare", Arguments: json.RawMessage(`"not json {"`), Invalid: true}}},
			{Role: RoleTool, ToolCallID: "c1", ToolName: "get_status", Content: `{"up":true}`},
			{Role: RoleTool, ToolCallID: "c2", ToolName: "bare", Content: "bad arguments", IsError: true},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c3", Name: "bare"}}},
			{Role: RoleTool, ToolCallID: "c3", ToolName: "bare", Content: "plain text"},
			{Role: RoleUser, Content: ""},
			{Role: RoleAssistant},
			{Role: RoleUser, Content: "thanks"},
		}}
}

func body(t *testing.T, kind string, p *Provider, r *Request) (string, map[string]any) {
	t.Helper()
	u, b := newCodec(kind).call(p, r)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	return u, m
}

var get = fakeapi.Get

func TestChatRequest(t *testing.T) {
	p := &Provider{BaseURL: "https://x/v1", APIVersion: "2024"}
	u, m := body(t, KindOpenAI, p, conversation())
	msgs := m["messages"].([]any)
	if u != "https://x/v1/chat/completions" || get(msgs, 0, "content") != "sys" || m["max_completion_tokens"] != 100.0 ||
		m["reasoning_effort"] != "high" || m["temperature"] != 0.7 || m["model"] != "m" {
		t.Fatalf("openai: %s %v", u, m)
	}
	if get(msgs, 2, "tool_calls", 1, "function", "arguments") != `not json {` || get(msgs, 4, "role") != "tool" ||
		get(msgs, 5, "content") != nil || get(msgs, 5, "tool_calls", 0, "function", "arguments") != "{}" {
		t.Errorf("openai's messages: %v", msgs)
	}
	u, m = body(t, KindAzureOpenAI, p, &Request{Model: "dep a", Messages: []Message{{Role: RoleUser, Content: "q"}}})
	if u != "https://x/v1/openai/deployments/dep%20a/chat/completions?api-version=2024" || m["model"] != nil || m["tools"] != nil || m["reasoning_effort"] != nil {
		t.Errorf("azure: %s %v", u, m)
	}
	_, m = body(t, KindOpenAICompatible, p, conversation())
	if m["max_tokens"] != 100.0 || m["reasoning_effort"] != nil || m["max_completion_tokens"] != nil {
		t.Errorf("compatible: %v", m)
	}
}

func TestAnthropicRequest(t *testing.T) {
	u, m := body(t, KindAnthropic, &Provider{BaseURL: "https://a/v1"}, conversation())
	msgs := m["messages"].([]any)
	if u != "https://a/v1/messages" || m["system"] != "sys" || m["temperature"] != nil || get(m, "thinking", "budget_tokens") != 24576.0 ||
		m["max_tokens"] != 100.0+24576 {
		t.Fatalf("%s %v", u, m)
	}
	// user, assistant, user (two results), assistant, user (result, then
	// the user's words merged in).
	if len(msgs) != 5 || get(msgs, 1, "content", 0, "type") != "thinking" || get(msgs, 1, "content", 3, "input") == nil ||
		len(get(msgs, 2, "content").([]any)) != 2 || get(msgs, 2, "content", 1, "is_error") != true ||
		get(msgs, 4, "content", 1, "text") != "thanks" {
		t.Errorf("messages: %v", msgs)
	}
	if v, _ := json.Marshal(get(msgs, 1, "content", 3, "input")); string(v) != "{}" {
		t.Errorf("an invalid call's input: %s", v)
	}
	if get(m, "tools", 1, "input_schema", "type") != "object" {
		t.Errorf("a tool without parameters: %v", m["tools"])
	}
	temp := 0.1
	_, m = body(t, KindAnthropic, &Provider{}, &Request{Temperature: &temp})
	if m["temperature"] != 0.1 || m["max_tokens"] != 4096.0 || m["system"] != nil || len(m["messages"].([]any)) != 0 {
		t.Errorf("no thinking: %v", m)
	}
}

func TestGeminiRequest(t *testing.T) {
	u, m := body(t, KindGemini, &Provider{BaseURL: "https://g/v1beta"}, conversation())
	c := m["contents"].([]any)
	if u != "https://g/v1beta/models/m:streamGenerateContent?alt=sse" || get(m, "generationConfig", "thinkingConfig", "thinkingBudget") != 24576.0 ||
		get(m, "generationConfig", "temperature") != 0.7 || get(m, "systemInstruction", "parts", 0, "text") != "sys" {
		t.Fatalf("%s %v", u, m)
	}
	if len(c) != 5 || get(c, 1, "role") != "model" || get(c, 1, "parts", 1, "thoughtSignature") != "ts" ||
		get(c, 2, "parts", 0, "functionResponse", "response", "up") != true ||
		get(c, 2, "parts", 1, "functionResponse", "response", "error") != "bad arguments" ||
		get(c, 4, "parts", 0, "functionResponse", "response", "result") != "plain text" {
		t.Errorf("contents: %v", c)
	}
	_, m = body(t, KindGemini, &Provider{}, &Request{})
	if get(m, "generationConfig", "thinkingConfig") != nil || m["systemInstruction"] != nil || len(m["contents"].([]any)) != 0 || m["tools"] != nil {
		t.Errorf("a bare call: %v", m)
	}
}

func TestOllamaAndResponsesRequests(t *testing.T) {
	u, m := body(t, KindOllama, &Provider{BaseURL: "http://o"}, conversation())
	msgs := m["messages"].([]any)
	if u != "http://o/api/chat" || m["think"] != true || get(m, "options", "num_predict") != 100.0 || get(m, "options", "temperature") != 0.7 ||
		get(msgs, 2, "tool_calls", 0, "function", "arguments", "listener") != "web" || get(msgs, 4, "tool_name") != "bare" {
		t.Fatalf("ollama: %s %v", u, m)
	}
	_, m = body(t, KindOllama, &Provider{}, &Request{})
	if m["think"] != nil || m["tools"] != nil {
		t.Errorf("ollama bare: %v", m)
	}
	u, m = body(t, KindOpenAIResponses, &Provider{BaseURL: "https://x/v1"}, conversation())
	in := m["input"].([]any)
	if u != "https://x/v1/responses" || m["instructions"] != "sys" || get(m, "reasoning", "effort") != "high" || m["temperature"] != 0.7 ||
		get(in, 1, "content") != "let me look" || get(in, 3, "arguments") != "not json {" || get(in, 4, "type") != "function_call_output" ||
		get(m, "tools", 1, "parameters", "type") != "object" {
		t.Fatalf("responses: %s %v", u, m)
	}
	_, m = body(t, KindOpenAIResponses, &Provider{}, &Request{})
	if m["instructions"] != nil || m["reasoning"] != nil || m["tools"] != nil {
		t.Errorf("responses bare: %v", m)
	}
}

func TestRequestCheck(t *testing.T) {
	c := newClient(t, Limits{})
	for _, r := range []*Request{
		{Tools: []Tool{{Name: "t", Parameters: json.RawMessage(`{`)}}},
		{Messages: []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Arguments: json.RawMessage(`{`)}}}}},
	} {
		var got error
		k := c.Stream(compat("http://127.0.0.1:1"), r, 0, nil, func(_ *Result, err error) { got = err })
		if got == nil || !strings.Contains(got.Error(), "not JSON") {
			t.Errorf("%v", got)
		}
		k.Cancel(nil)
	}
}

// parse runs events of kind through its codec, and returns the answer and
// the first error.
func parse(kind string, events ...string) (*answer, error) {
	cd := newCodec(kind)
	a := &answer{maxTools: 4, emit: func(Event) bool { return true }}
	for _, e := range events {
		name, data, ok := strings.Cut(e, "|")
		if !ok {
			name, data = "", e
		}
		if err := cd.parse(a, name, []byte(data)); err != nil {
			return a, err
		}
	}
	return a, cd.finish(a)
}

func TestParseEdges(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		events []string
		check  func(a *answer, err error) bool
	}{
		// A tool call with no arguments, one with arguments not JSON, and a
		// stream without [DONE] but with its finish_reason.
		{KindOpenAICompatible, []string{
			`{"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"x"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"y","arguments":"oops"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
		}, func(a *answer, err error) bool {
			tc := a.res.ToolCalls
			return err == nil && len(tc) == 2 && string(tc[0].Arguments) == "{}" && tc[1].Invalid && string(tc[1].Arguments) == `"oops"` &&
				a.res.Usage.OutputTokens == 2
		}},
		{KindOpenAICompatible, []string{`{"choices":[{"delta":{"content":"x"}}],"error":null}`, `[DONE]`},
			func(a *answer, err error) bool { return err == nil && a.res.Text == "x" }},
		// Too many tool calls.
		{KindOpenAICompatible, []string{`{"choices":[{"delta":{"tool_calls":[{"index":0},{"index":1},{"index":2},{"index":3},{"index":4}]}}]}`},
			func(a *answer, err error) bool { return errors.Is(err, ErrTooLarge) }},
		{KindOllama, []string{`{"message":{"tool_calls":[{"function":{"name":"a"}},{"function":{"name":"a"}},{"function":{"name":"a"}},{"function":{"name":"a"}},{"function":{"name":"a"}}]}}`},
			func(a *answer, err error) bool { return errors.Is(err, ErrTooLarge) }},
		{KindAnthropic, []string{`{"type":"content_block_start","index":0}`},
			func(a *answer, err error) bool {
				return err != nil && strings.Contains(err.Error(), "without its block")
			}},
		{KindAnthropic, []string{`{"type":"content_block_delta","index":0}`},
			func(a *answer, err error) bool {
				return err != nil && strings.Contains(err.Error(), "without its delta")
			}},
		{KindAnthropic, []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"a"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"n"}}`,
			`{"type":"message_stop"}`,
		}, func(a *answer, err error) bool {
			return err == nil && a.res.Text == "a" && len(a.res.ToolCalls) == 1 && string(a.res.ToolCalls[0].Arguments) == "{}"
		}},
		{KindAnthropic, []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t0","name":"n"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"n"}}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t2","name":"n"}}`,
			`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"t3","name":"n"}}`,
			`{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"t4","name":"n"}}`,
		}, func(a *answer, err error) bool { return errors.Is(err, ErrTooLarge) }},
		{KindAnthropic, []string{`{"type":"content_block_delta","index":9,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"content_block_delta","index":8,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"content_block_delta","index":7,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"content_block_delta","index":6,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"content_block_delta","index":5,"delta":{"type":"input_json_delta","partial_json":"{}"}}`},
			func(a *answer, err error) bool { return errors.Is(err, ErrTooLarge) }},
		{KindGemini, []string{`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"g1","name":"n","args":{"a":1}}}]},"finishReason":"STOP"}]}`},
			func(a *answer, err error) bool {
				return err == nil && a.res.ToolCalls[0].ID == "g1" && string(a.res.ToolCalls[0].Arguments) == `{"a":1}`
			}},
		{KindOpenAIResponses, []string{
			`response.output_text.delta|{"delta":"hi"}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"i1","call_id":"c1","name":"n"}}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"i1","call_id":"c1","name":"n"}}`,
			`{"type":"response.function_call_arguments.delta","item_id":"i2","delta":"{\"a\":"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"i2","delta":"2}"}`,
			`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":1,"output_tokens":2}}}`,
		}, func(a *answer, err error) bool {
			return err == nil && a.res.Text == "hi" && len(a.res.ToolCalls) == 2 && string(a.res.ToolCalls[1].Arguments) == `{"a":2}` &&
				a.res.StopReason == "max_output_tokens"
		}},
		{KindOpenAIResponses, []string{`{"type":"response.failed","response":{"status":"failed"}}`},
			func(a *answer, err error) bool { var ae *APIError; return errors.As(err, &ae) }},
		{KindOpenAIResponses, []string{`{"type":"response.output_item.added","item":{"type":"function_call","id":"0"}}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"1"}}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"2"}}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"3"}}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"4"}}`},
			func(a *answer, err error) bool { return errors.Is(err, ErrTooLarge) }},
		{KindOpenAIResponses, []string{`{"type":"response.function_call_arguments.delta","item_id":"0"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"1"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"2"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"3"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"4"}`},
			func(a *answer, err error) bool { return errors.Is(err, ErrTooLarge) }},
		{KindOpenAIResponses, []string{`{"type":"response.function_call_arguments.delta","item_id":"0"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"1"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"2"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"3"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"9"}}`},
			func(a *answer, err error) bool { return errors.Is(err, ErrTooLarge) }},
	} {
		a, err := parse(tc.kind, tc.events...)
		if !tc.check(a, err) {
			t.Errorf("%s %v: %v, %+v", tc.kind, tc.events, err, a.res)
		}
	}
}

// TestStoppedParse: once the call has ended, a parse stops at its next
// piece, for every kind.
func TestStoppedParse(t *testing.T) {
	for kind, ev := range map[string]string{
		KindOpenAICompatible: `{"choices":[{"delta":{"reasoning":"r","content":"x","tool_calls":[{"id":"a","function":{"name":"x"}}]},"finish_reason":"stop"}]}`,
		KindOllama:           `{"message":{"thinking":"r","content":"x","tool_calls":[{"function":{"name":"a"}}]}}`,
		KindGemini:           `{"candidates":[{"content":{"parts":[{"text":"r","thought":true},{"text":"x"},{"functionCall":{"name":"n"}}]}}]}`,
	} {
		for skip := 0; skip < 3; skip++ {
			n := 0
			cd := newCodec(kind)
			a := &answer{maxTools: 4, emit: func(Event) bool { n++; return n <= skip }}
			if err := cd.parse(a, "", []byte(ev)); err != errStopped {
				t.Errorf("%s after %d: %v", kind, skip, err)
			}
		}
	}
	for _, ev := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"a"}}`,
	} {
		a := &answer{maxTools: 4, emit: func(Event) bool { return false }}
		if err := newCodec(KindAnthropic).parse(a, "", []byte(ev)); err != errStopped {
			t.Errorf("anthropic %s: %v", ev, err)
		}
	}
	a := &answer{maxTools: 4, emit: func(Event) bool { return false }}
	a.begin("k", "id", "n")
	if err := a.endAll(); err != errStopped || errStopped.Error() == "" {
		t.Error(err)
	}
}

func TestFramer(t *testing.T) {
	var got []string
	f := &framer{max: 100, handle: func(name string, data []byte) error {
		got = append(got, name+"="+string(data))
		return nil
	}}
	for _, p := range []string{"event: a\r\ndata: 1\r\n", "data:2\n\n: comment\nid: 7\n\n", "data: x", "y\nretry: 1\n\ndata: last"} {
		if err := f.write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.end(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a=1\n2,=xy,=last" {
		t.Errorf("%q", got)
	}
	got = nil
	f = &framer{ndjson: true, max: 100, handle: func(name string, data []byte) error { got = append(got, string(data)); return nil }}
	_ = f.write([]byte("{}\n\n  \n{\"a\""))
	_ = f.write([]byte(":1}"))
	if err := f.end(); err != nil || strings.Join(got, ",") != `{},{"a":1}` {
		t.Errorf("%v %q", err, got)
	}
	boom := errors.New("boom")
	f = &framer{ndjson: true, max: 100, handle: func(string, []byte) error { return boom }}
	if f.write([]byte("{}\n")) != boom {
		t.Error("a handler's error")
	}
	_ = f.write([]byte("{}"))
	if f.end() != boom {
		t.Error("a handler's error at the end")
	}
}

func TestAPIMessage(t *testing.T) {
	for body, want := range map[string]string{
		`{"error":{"message":"a"}}`: "a",
		`{"error":"b"}`:             "b",
		`{"message":"c"}`:           "c",
		`{"detail":"d"}`:            "d",
		`plain`:                     "plain",
		``:                          "no message",
		strings.Repeat("z", 600):    strings.Repeat("z", 512) + "...",
	} {
		if got := apiMessage([]byte(body)); got != want {
			t.Errorf("%q: %q", body, got)
		}
	}
	if (&APIError{Provider: "p", Status: 500, Message: "m"}).Error() != "p: HTTP 500: m" || redact("abc", "ab") != "abc" {
		t.Error("APIError or redact")
	}
	for level, want := range map[string]int{ThinkingLow: 2048, ThinkingMedium: 8192, ThinkingHigh: 24576, ThinkingOff: 0} {
		if thinkingBudget(level) != want {
			t.Errorf("%s", level)
		}
	}
}

func TestParseModels(t *testing.T) {
	if _, err := parseModels(KindOpenAI, []byte(`{`)); err == nil {
		t.Error("bad JSON")
	}
	m, _ := parseModels(KindOllama, []byte(`{"models":[{"model":"a"},{"name":""}]}`))
	if len(m) != 1 || m[0].ID != "a" {
		t.Errorf("%v", m)
	}
	m, _ = parseModels(KindOpenAI, []byte(`{"data":[{"id":""},{"id":"x"}]}`))
	if len(m) != 1 {
		t.Errorf("%v", m)
	}
}
