package fakeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// Turn is a scripted answer of a model: text, tool calls, or both, with the
// tokens it took; or an HTTP error.
type Turn struct {
	Text     string
	Thinking string
	Calls    []Call
	// Prompt and Completion are the tokens the answer says it took.
	Prompt, Completion int
	// Status, when not 0 or 200, answers that error instead.
	Status int
	// Hold, when not nil, holds the answer after its first piece until it
	// is closed or the client goes away.
	Hold chan struct{}
}

// Call is a tool call of a scripted answer.
type Call struct {
	ID, Name, Args string
}

// StartScripted starts a fake of the Chat Completions API (kinds openai and
// openai_compatible, at BaseURL("openai")) whose answers next makes from
// each request, streamed in the recorded shape of chat-1.sse, a piece at a
// time. It records every request, as Start's fake does, and wants key.
func StartScripted(t testing.TB, key string, next func(Request) Turn) *Server {
	s := &Server{key: key}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		req := Request{Kind: "chat", Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Raw: raw}
		_ = json.Unmarshal(raw, &req.Body)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
			fail(w, 401, "Incorrect API key provided")
			return
		}
		s.record(req)
		turn := next(req)
		if turn.Status != 0 && turn.Status != 200 {
			fail(w, turn.Status, "scripted failure")
			return
		}
		writeTurn(w, r, turn)
	}))
	t.Cleanup(s.Close)
	return s
}

func chunk(w io.Writer, delta any, finish any, usage any) {
	b, _ := json.Marshal(map[string]any{"id": "chatcmpl-s", "object": "chat.completion.chunk", "created": 1762387200, "model": "gpt-test",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": usage})
	if usage != nil {
		b, _ = json.Marshal(map[string]any{"id": "chatcmpl-s", "object": "chat.completion.chunk", "model": "gpt-test", "choices": []any{}, "usage": usage})
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}

// halves splits s in two on a rune's boundary.
func halves(s string) (string, string) {
	i := len(s) / 2
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i], s[i:]
}

func writeTurn(w http.ResponseWriter, r *http.Request, t Turn) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	f := w.(http.Flusher)
	flush := func() { f.Flush() }
	chunk(w, map[string]any{"role": "assistant", "content": ""}, nil, nil)
	flush()
	if t.Thinking != "" {
		chunk(w, map[string]any{"reasoning_content": t.Thinking}, nil, nil)
		flush()
	}
	a, b := halves(t.Text)
	if len(a) > 64<<10 {
		a, b = t.Text[:0], t.Text
	}
	if a != "" {
		chunk(w, map[string]any{"content": a}, nil, nil)
		flush()
	}
	if t.Hold != nil {
		select {
		case <-t.Hold:
		case <-r.Context().Done():
			return
		}
	}
	// The rest goes in pieces of at most 64 KiB, as an API streams a long
	// answer.
	for b != "" {
		n := min(len(b), 64<<10)
		for n < len(b) && !utf8.RuneStart(b[n]) {
			n++
		}
		chunk(w, map[string]any{"content": b[:n]}, nil, nil)
		flush()
		b = b[n:]
	}
	for i, c := range t.Calls {
		x, y := halves(c.Args)
		chunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": x}}}}, nil, nil)
		chunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": i, "function": map[string]any{"arguments": y}}}}, nil, nil)
		flush()
	}
	finish := "stop"
	if len(t.Calls) > 0 {
		finish = "tool_calls"
	}
	chunk(w, map[string]any{}, finish, nil)
	chunk(w, nil, nil, map[string]any{"prompt_tokens": t.Prompt, "completion_tokens": t.Completion, "total_tokens": t.Prompt + t.Completion})
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flush()
}

// Messages returns the messages of a Chat Completions request, each as
// role and content, the tool calls' names and arguments after an
// assistant's content.
func Messages(req Request) []map[string]any {
	list, _ := req.Body["messages"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, m := range list {
		mm, _ := m.(map[string]any)
		out = append(out, mm)
	}
	return out
}

// LastUser returns the content of the request's last user message.
func LastUser(req Request) string {
	ms := Messages(req)
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i]["role"] == "user" {
			s, _ := ms[i]["content"].(string)
			return s
		}
	}
	return ""
}

// ToolResults returns the contents of the request's tool messages, in
// order.
func ToolResults(req Request) []string {
	var out []string
	for _, m := range Messages(req) {
		if m["role"] == "tool" {
			s, _ := m["content"].(string)
			out = append(out, s)
		}
	}
	return out
}

// System returns the request's system prompt.
func System(req Request) string {
	for _, m := range Messages(req) {
		if m["role"] == "system" {
			s, _ := m["content"].(string)
			return s
		}
	}
	return ""
}

// HasTool reports whether the request offers the tool name.
func HasTool(req Request, name string) bool {
	return strings.Contains(string(req.Raw), `"name":"`+name+`"`)
}
