package agenttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// MockLLM is an upstream model service for tests: a tiny OpenAI- and
// Anthropic-compatible server that echoes what it received, so that a test
// can check what a node forwarded. Every route is under a tag
// (/u/<tag>/v1/...), one tag per upstream; the API key of tag t is "sk-<t>".
// A call's body may ask for sleep_ms before the answer, chunks events
// chunk_ms apart when it streams, and big_bytes of body in its answer.
type MockLLM struct {
	*httptest.Server
	s *server
}

// StartMockLLM starts a MockLLM, closed when t ends.
func StartMockLLM(t testing.TB) *MockLLM {
	s := &server{tags: map[string]*tag{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /u/{tag}/v1/models", s.models)
	mux.HandleFunc("POST /u/{tag}/v1/{path...}", s.call)
	m := &MockLLM{Server: httptest.NewServer(mux), s: s}
	t.Cleanup(m.Close)
	return m
}

// BaseURL returns the base URL of tag's upstream.
func (m *MockLLM) BaseURL(tag string) string { return m.URL + "/u/" + tag + "/v1" }

// Down takes tag down, or brings it up.
func (m *MockLLM) Down(name string, down bool) { m.s.with(name, func(t *tag) { t.Down = down }) }

// Stats returns tag's counters.
func (m *MockLLM) Stats(name string) Counters {
	var c Counters
	m.s.with(name, func(t *tag) { c = t.Counters })
	return c
}

// Last returns tag's last call, or nil.
func (m *MockLLM) Last(name string) *Request {
	var r *Request
	m.s.with(name, func(t *tag) { r = t.last })
	return r
}

// Request is what the mock saw of one call (GET /admin/<tag>/last).
// Request 模拟服务看到的一次调用（GET /admin/<tag>/last）。
type Request struct {
	Path   string            `json:"path"`
	Model  string            `json:"model"`
	Header map[string]string `json:"header"`
	Body   json.RawMessage   `json:"body"`
}

// Counters of a tag (GET /admin/<tag>/stats).
// Counters 一个标签的计数（GET /admin/<tag>/stats）。
type Counters struct {
	Calls     int  `json:"calls"`      // model calls / 模型调用
	BodyBytes int  `json:"body_bytes"` // bytes of the last call's body
	Checks    int  `json:"checks"`     // GET /models
	Canceled  int  `json:"canceled"`   // calls whose caller went away before the end / 调用方在结束前离开的调用
	Down      bool `json:"down"`
}

type tag struct {
	Counters
	last *Request
}

type server struct {
	mu   sync.Mutex
	tags map[string]*tag
}

func (s *server) tag(name string) *tag {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tags[name]
	if t == nil {
		t = &tag{}
		s.tags[name] = t
	}
	return t
}

func (s *server) with(name string, fn func(t *tag)) {
	t := s.tag(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(t)
}

// recorded request headers / 记录的请求头
var echoHeaders = []string{"Authorization", "X-Api-Key", "Anthropic-Version", "Anthropic-Beta", "Openai-Beta", "Cookie", "X-Agenthub", "Content-Type"}

// authorized checks the key of tag: Bearer (OpenAI) or x-api-key (Anthropic).
// authorized 检查标签的 Key：Bearer（OpenAI）或 x-api-key（Anthropic）。
func authorized(r *http.Request, name string) bool {
	want := "sk-" + name
	return r.Header.Get("Authorization") == "Bearer "+want || r.Header.Get("X-Api-Key") == want
}

func (s *server) models(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("tag")
	down := false
	s.with(name, func(t *tag) { t.Checks++; down = t.Down })
	switch {
	case down:
		writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "upstream " + name + " is down"}})
	case !authorized(r, name):
		writeJSON(w, 401, map[string]any{"error": map[string]any{"message": "bad key"}})
	default:
		writeJSON(w, 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "up-" + name, "object": "model"}}})
	}
}

// callBody: the request fields the mock looks at. sleep_ms delays the response; chunks sets the number of stream
// events (default 3) and chunk_ms the pause between them.
// callBody：模拟服务关心的请求字段。sleep_ms 延迟应答；chunks 为流事件数（默认 3），chunk_ms 为事件之间的间隔。
type callBody struct {
	BigBytes int    `json:"big_bytes"`
	CutAfter int    `json:"cut_after"` // with big_bytes: break the connection after this many bytes
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	SleepMs  int    `json:"sleep_ms"`
	Chunks   int    `json:"chunks"`
	ChunkMs  int    `json:"chunk_ms"`
}

func (s *server) call(w http.ResponseWriter, r *http.Request) {
	name, path := r.PathValue("tag"), r.PathValue("path")
	raw, _ := io.ReadAll(r.Body)
	var b callBody
	if err := json.Unmarshal(raw, &b); err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "body is not JSON: " + err.Error()}})
		return
	}
	req := &Request{Path: path, Model: b.Model, Header: map[string]string{}, Body: raw}
	for _, h := range echoHeaders {
		if v := r.Header.Get(h); v != "" {
			req.Header[strings.ToLower(h)] = v
		}
	}
	down := false
	s.with(name, func(t *tag) { t.Calls++; t.last = req; t.BodyBytes = len(raw); down = t.Down })
	if down {
		writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "upstream " + name + " is down"}})
		return
	}
	if !authorized(r, name) {
		writeJSON(w, 401, map[string]any{"error": map[string]any{"message": "bad key"}})
		return
	}
	ctx := r.Context()
	canceled := func() { s.with(name, func(t *tag) { t.Canceled++ }) }
	if b.SleepMs > 0 {
		select {
		case <-time.After(time.Duration(b.SleepMs) * time.Millisecond):
		case <-ctx.Done():
			canceled()
			return
		}
	}
	text := "echo from " + name
	anthropic := strings.HasPrefix(path, "messages")
	if b.BigBytes > 0 {
		// A large body, in pieces, chunk_ms apart.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(b.BigBytes))
		w.WriteHeader(200)
		piece := bytes.Repeat([]byte("z"), 32<<10)
		sent := 0
		for left := b.BigBytes; left > 0; left -= len(piece) {
			if left < len(piece) {
				piece = piece[:left]
			}
			if b.CutAfter > 0 && sent >= b.CutAfter {
				panic(http.ErrAbortHandler)
			}
			sent += len(piece)
			if _, err := w.Write(piece); err != nil {
				canceled()
				return
			}
			w.(http.Flusher).Flush()
			if b.ChunkMs > 0 {
				select {
				case <-time.After(time.Duration(b.ChunkMs) * time.Millisecond):
				case <-ctx.Done():
					canceled()
					return
				}
			}
		}
		return
	}
	if !b.Stream {
		switch {
		case path == "messages/count_tokens":
			writeJSON(w, 200, map[string]any{"input_tokens": 7})
		case anthropic:
			writeJSON(w, 200, map[string]any{"id": "msg_mock", "type": "message", "role": "assistant", "model": b.Model,
				"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn"})
		case path == "embeddings":
			writeJSON(w, 200, map[string]any{"object": "list", "model": b.Model,
				"data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2}}}})
		default:
			writeJSON(w, 200, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion", "model": b.Model,
				"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}}}})
		}
		return
	}
	chunks, gap := b.Chunks, time.Duration(b.ChunkMs)*time.Millisecond
	if chunks <= 0 {
		chunks = 3
	}
	if gap <= 0 {
		gap = 20 * time.Millisecond
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl := w.(http.Flusher)
	send := func(event, data string) bool {
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		fl.Flush()
		select {
		case <-time.After(gap):
			return true
		case <-ctx.Done():
			canceled()
			return false
		}
	}
	for i := 0; i < chunks; i++ {
		part := fmt.Sprintf("%s #%d", text, i)
		var ok bool
		if anthropic {
			ok = send("content_block_delta", mustJSON(map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "text_delta", "text": part}}))
		} else {
			ok = send("", mustJSON(map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": b.Model,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": part}}}}))
		}
		if !ok {
			return
		}
	}
	if anthropic {
		send("message_stop", `{"type":"message_stop"}`)
	} else {
		send("", "[DONE]")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // /admin/<tag>/last shows the body as received / /admin/<tag>/last 按收到的原样显示请求体
	_ = enc.Encode(v)
}
