// Package fakeapi is a fake of every model API the assistant calls, for
// tests: it checks each request as the vendor's API would take it (path,
// key header, the body's shape: system prompt, tools, a tool's result going
// back) and answers with an answer recorded from that API, streamed a piece
// at a time. Each kind's conversation is two turns: the first answer calls
// the tool get_status with {"listener": "web"}, and once the request
// carries that call's result, the second answers "The listener web is up."
package fakeapi

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

//go:embed recorded
var recorded embed.FS

// Kinds lists the kinds of API the fake serves.
var Kinds = []string{"anthropic", "openai", "openai_responses", "gemini", "azure_openai", "ollama", "openai_compatible", "llm_proxy"}

// Request is a request the fake took.
type Request struct {
	Kind   string
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
	Raw    []byte
}

// Server is the fake.
type Server struct {
	*httptest.Server
	key string

	mu   sync.Mutex
	reqs []Request
}

// Start starts the fake, closed when t ends; every kind but ollama wants
// key in its own header.
func Start(t testing.TB, key string) *Server {
	s := &Server{key: key}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// BaseURL returns the base URL a provider of kind has on the fake.
func (s *Server) BaseURL(kind string) string {
	switch kind {
	case "gemini":
		return s.URL + "/v1beta"
	case "azure_openai", "ollama":
		return s.URL
	}
	return s.URL + "/v1"
}

// Requests returns the requests taken so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Get returns the value at path, keys and indexes, in a JSON body.
func Get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k < 0 || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

// Find returns the first element of the list at path whose field key is
// value.
func Find(v any, key string, value any, path ...any) map[string]any {
	list, _ := Get(v, path...).([]any)
	for _, e := range list {
		if m, ok := e.(map[string]any); ok && m[key] == value {
			return m
		}
	}
	return nil
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error"}})
	_, _ = w.Write(b)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	req := Request{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Raw: raw}
	_ = json.Unmarshal(raw, &req.Body)
	if r.Method == http.MethodGet {
		s.record(req)
		s.models(w, r)
		return
	}
	var kind, keyHeader, prefix string
	switch {
	case r.URL.Path == "/v1/chat/completions":
		kind, keyHeader, prefix = "chat", "Authorization", "Bearer "
	case strings.HasPrefix(r.URL.Path, "/openai/deployments/") && strings.HasSuffix(r.URL.Path, "/chat/completions"):
		kind, keyHeader = "chat", "Api-Key"
		if r.URL.Query().Get("api-version") == "" {
			fail(w, 400, "api-version is missing")
			return
		}
	case r.URL.Path == "/v1/responses":
		kind, keyHeader, prefix = "responses", "Authorization", "Bearer "
	case r.URL.Path == "/v1/messages":
		kind, keyHeader = "anthropic", "X-Api-Key"
		if r.Header.Get("Anthropic-Version") == "" {
			fail(w, 400, "anthropic-version is missing")
			return
		}
	case strings.HasPrefix(r.URL.Path, "/v1beta/models/") && strings.HasSuffix(r.URL.Path, ":streamGenerateContent"):
		kind, keyHeader = "gemini", "X-Goog-Api-Key"
		if r.URL.Query().Get("alt") != "sse" {
			fail(w, 400, "alt=sse is missing")
			return
		}
	case r.URL.Path == "/api/chat":
		kind = "ollama"
	default:
		fail(w, 404, "no such endpoint: "+r.URL.Path)
		return
	}
	req.Kind = kind
	s.record(req)
	if keyHeader != "" && r.Header.Get(keyHeader) != prefix+s.key {
		// As the APIs do, the key is said back: the client must not pass
		// it on.
		fail(w, 401, "Incorrect API key provided: "+strings.TrimPrefix(r.Header.Get(keyHeader), prefix))
		return
	}
	if req.Body == nil {
		fail(w, 400, "the body is not JSON")
		return
	}
	turn, msg := check(kind, req.Body)
	if msg != "" {
		fail(w, 400, msg)
		return
	}
	s.replay(w, kind, turn)
}

func (s *Server) record(req Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
}

// replay writes the recorded answer of kind's turn, an event, or a line,
// at a time, each flushed.
func (s *Server) replay(w http.ResponseWriter, kind string, turn int) {
	name, ctype, sep := kind+"-"+string(rune('0'+turn))+".sse", "text/event-stream", []byte("\n\n")
	if kind == "ollama" {
		name, ctype, sep = "ollama-"+string(rune('0'+turn))+".ndjson", "application/x-ndjson", []byte("\n")
	}
	data, _ := recorded.ReadFile("recorded/" + name)
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	for _, piece := range bytes.SplitAfter(data, sep) {
		if len(piece) == 0 {
			continue
		}
		_, _ = w.Write(piece)
		if f != nil {
			f.Flush()
		}
	}
}

// models answers a model list, in the shape of the path's API.
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/api/tags":
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen-test","model":"qwen-test","size":1}]}`))
	case r.URL.Path == "/v1beta/models":
		if r.Header.Get("X-Goog-Api-Key") != s.key {
			fail(w, 403, "API key not valid. Please pass a valid API key.")
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-test","inputTokenLimit":1048576,"supportedGenerationMethods":["generateContent","countTokens"]},{"name":"models/embedding-test","supportedGenerationMethods":["embedContent"]}]}`))
	case r.URL.Path == "/v1/models" || r.URL.Path == "/openai/models":
		auth := r.Header.Get("Authorization") == "Bearer "+s.key || r.Header.Get("X-Api-Key") == s.key || r.Header.Get("Api-Key") == s.key
		if !auth {
			fail(w, 401, "Incorrect API key provided")
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-test","object":"model"},{"id":"claude-test","type":"model"}]}`))
	default:
		fail(w, 404, "no such endpoint: "+r.URL.Path)
	}
}

// check says which turn body is, and what is wrong with it as kind's API
// would take it.
func check(kind string, body map[string]any) (turn int, msg string) {
	if body["tools"] == nil {
		// A call without tools, as a provider's test makes, is answered
		// with the second turn's text.
		return 2, ""
	}
	switch kind {
	case "chat":
		if Get(body, "messages", 0, "role") != "system" || Get(body, "stream") != true || Get(body, "tools", 0, "function", "name") != "get_status" {
			return 0, "a chat call has its system message first, streams, and lists get_status"
		}
		if Find(body, "role", "tool", "messages") == nil {
			return 1, ""
		}
		if Get(Find(body, "role", "tool", "messages"), "tool_call_id") != "call_abc" ||
			Get(Find(body, "role", "assistant", "messages"), "tool_calls", 0, "id") != "call_abc" ||
			Get(Find(body, "role", "assistant", "messages"), "tool_calls", 0, "function", "arguments") != `{"listener":"web"}` {
			return 0, "the tool's result does not answer the call"
		}
	case "responses":
		if Get(body, "instructions") == nil || Get(body, "store") != false || Get(body, "tools", 0, "name") != "get_status" {
			return 0, "a responses call has instructions, store false, and lists get_status"
		}
		out := Find(body, "type", "function_call_output", "input")
		if out == nil {
			return 1, ""
		}
		if out["call_id"] != "call_r1" || Get(Find(body, "type", "function_call", "input"), "call_id") != "call_r1" {
			return 0, "the tool's result does not answer the call"
		}
	case "anthropic":
		if Get(body, "system") == nil || Get(body, "max_tokens") == nil || Get(body, "tools", 0, "input_schema") == nil {
			return 0, "a messages call has system, max_tokens and the tools' input_schema"
		}
		msgs, _ := body["messages"].([]any)
		last, _ := Get(msgs, len(msgs)-1).(map[string]any)
		res := Find(last, "type", "tool_result", "content")
		if res == nil {
			return 1, ""
		}
		asst := Find(body, "role", "assistant", "messages")
		if res["tool_use_id"] != "toolu_01" || Get(Find(asst, "type", "tool_use", "content"), "id") != "toolu_01" ||
			Get(Find(asst, "type", "thinking", "content"), "signature") != "EqQBCgIYAhIM" {
			return 0, "the tool's result does not answer the call, or the thinking block is not sent back"
		}
	case "gemini":
		if Get(body, "systemInstruction", "parts", 0, "text") == nil || Get(body, "tools", 0, "functionDeclarations", 0, "name") != "get_status" {
			return 0, "a generateContent call has systemInstruction and declares get_status"
		}
		contents, _ := body["contents"].([]any)
		var resp, call any
		for _, c := range contents {
			for _, p := range Get(c, "parts").([]any) {
				if v := Get(p, "functionResponse"); v != nil {
					resp = v
				}
				if Get(p, "functionCall") != nil {
					call = p
				}
			}
		}
		if resp == nil {
			return 1, ""
		}
		if Get(resp, "name") != "get_status" || Get(call, "thoughtSignature") != "CiQBVKhc7" || Get(resp, "response") == nil {
			return 0, "the function's response does not answer the call"
		}
	case "ollama":
		if Get(body, "messages", 0, "role") != "system" || Get(body, "tools", 0, "function", "name") != "get_status" {
			return 0, "a chat call has its system message first and lists get_status"
		}
		res := Find(body, "role", "tool", "messages")
		if res == nil {
			return 1, ""
		}
		if res["tool_name"] != "get_status" || Get(Find(body, "role", "assistant", "messages"), "tool_calls", 0, "function", "arguments", "listener") != "web" {
			return 0, "the tool's result does not answer the call"
		}
	}
	return 2, ""
}
