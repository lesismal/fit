package agenttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// MockLLM is an upstream model service for tests: a tiny server of every
// kind a node calls (OpenAI and its uploads, Anthropic, Gemini, Azure
// OpenAI, Ollama, Cohere) that echoes what it received, so that a test can
// check what a node forwarded. Every route is under a tag (/u/<tag>/...),
// one tag per upstream; the API key of tag t is "sk-<t>", in whichever
// header the kind sends it. A call's body (a JSON member or a form field)
// may ask for sleep_ms before the answer, chunks events chunk_ms apart when
// it streams, and big_bytes of body in its answer.
type MockLLM struct {
	*httptest.Server
	s *server
}

// StartMockLLM starts a MockLLM, closed when t ends.
func StartMockLLM(t testing.TB) *MockLLM {
	s := &server{tags: map[string]*tag{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /u/{tag}/{path...}", s.models)
	mux.HandleFunc("POST /u/{tag}/{path...}", s.call)
	m := &MockLLM{Server: httptest.NewServer(mux), s: s}
	t.Cleanup(m.Close)
	return m
}

// BaseURL returns the base URL of tag's OpenAI-style upstream, /u/<tag>/v1;
// the other kinds take m.URL + "/u/<tag>" with their own suffix.
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
	Path  string `json:"path"` // after /u/<tag>/, and v1/ when it starts so / 在 /u/<tag>/ 之后，以 v1/ 开头时也去掉
	Query string `json:"query,omitempty"`
	// Model is the body's model, a form's model field, or the model in a Gemini path.
	// Model 为请求体的 model、表单的 model 字段或 Gemini 路径中的模型。
	Model       string            `json:"model"`
	ContentType string            `json:"content_type,omitempty"`
	Header      map[string]string `json:"header"`
	Body        json.RawMessage   `json:"body,omitempty"` // a JSON body / JSON 请求体
	// Form holds a multipart body's fields; a file is "file:<filename>:<bytes>".
	// Form 为 multipart 请求体的字段；文件记为 "file:<filename>:<bytes>"。
	Form map[string]string `json:"form,omitempty"`
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
var echoHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key", "Anthropic-Version", "Anthropic-Beta",
	"Openai-Beta", "Cookie", "Content-Type"}

// authorized checks the key of tag: Bearer (OpenAI, Ollama, Cohere), x-api-key (Anthropic), x-goog-api-key (Gemini)
// or api-key (Azure OpenAI).
// authorized 检查标签的 Key：Bearer（OpenAI、Ollama、Cohere）、x-api-key（Anthropic）、x-goog-api-key（Gemini）或
// api-key（Azure OpenAI）。
func authorized(r *http.Request, name string) bool {
	want := "sk-" + name
	return r.Header.Get("Authorization") == "Bearer "+want || r.Header.Get("X-Api-Key") == want ||
		r.Header.Get("X-Goog-Api-Key") == want || r.Header.Get("Api-Key") == want
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
	Stream   *bool  `json:"stream"`
	SleepMs  int    `json:"sleep_ms"`
	Chunks   int    `json:"chunks"`
	ChunkMs  int    `json:"chunk_ms"`
}

// readForm reads a multipart body: its fields, its files as "file:<filename>:<bytes>", and the control fields
// callBody takes.
func readForm(raw []byte, boundary string) (map[string]string, callBody, error) {
	form := map[string]string{}
	var b callBody
	mr := multipart.NewReader(bytes.NewReader(raw), boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, b, err
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, b, err
		}
		if fn := part.FileName(); fn != "" {
			form[part.FormName()] = fmt.Sprintf("file:%s:%d", fn, len(data))
			continue
		}
		v := string(data)
		form[part.FormName()] = v
		n, _ := strconv.Atoi(v)
		switch part.FormName() {
		case "model":
			b.Model = v
		case "big_bytes":
			b.BigBytes = n
		case "cut_after":
			b.CutAfter = n
		case "sleep_ms":
			b.SleepMs = n
		case "stream":
			on := v == "true"
			b.Stream = &on
		}
	}
	return form, b, nil
}

// The kinds of upstream the mock answers as. / 模拟服务所扮演的上游种类。
const (
	kindOpenAI    = "openai"
	kindAnthropic = "anthropic"
	kindGemini    = "gemini"
	kindOllama    = "ollama"
	kindCohere    = "cohere"
)

// route tells a call's kind, its operation and, for Gemini, the model in its path; an Azure OpenAI call
// (openai/deployments/{d}/... or openai/responses, with api-version) is an OpenAI one. ok is false for a path the mock
// does not serve.
// route 判断调用的种类、操作，以及 Gemini 路径中的模型；Azure OpenAI 调用（openai/deployments/{d}/... 或
// openai/responses，带 api-version）视作 OpenAI 调用。模拟服务不提供的路径 ok 为 false。
func route(path string, q url.Values) (kind, op, pathModel string, ok bool) {
	if rest, found := strings.CutPrefix(path, "openai/"); found {
		if q.Get("api-version") == "" {
			return "", "", "", false
		}
		if rest == "responses" {
			return kindOpenAI, rest, "", true
		}
		dep, after, found := strings.Cut(strings.TrimPrefix(rest, "deployments/"), "/")
		if !strings.HasPrefix(rest, "deployments/") || !found || dep == "" {
			return "", "", "", false
		}
		return kindOpenAI, after, "", true
	}
	if i := strings.Index(path, "models/"); i >= 0 {
		rest := path[i+len("models/"):]
		if j := strings.LastIndexByte(rest, ':'); j > 0 {
			return kindGemini, rest[j+1:], rest[:j], true
		}
	}
	switch {
	case strings.HasPrefix(path, "api/"):
		return kindOllama, strings.TrimPrefix(path, "api/"), "", true
	case strings.HasPrefix(path, "v2/"):
		return kindCohere, strings.TrimPrefix(path, "v2/"), "", true
	case strings.HasPrefix(path, "messages"):
		return kindAnthropic, path, "", true
	}
	return kindOpenAI, path, "", true
}

func (s *server) call(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("tag")
	path := strings.TrimPrefix(r.PathValue("path"), "v1/")
	raw, _ := io.ReadAll(r.Body)
	req := &Request{Path: path, Query: r.URL.RawQuery, ContentType: r.Header.Get("Content-Type"), Header: map[string]string{}}
	for _, h := range echoHeaders {
		if v := r.Header.Get(h); v != "" {
			req.Header[strings.ToLower(h)] = v
		}
	}
	var b callBody
	if mt, params, _ := mime.ParseMediaType(req.ContentType); mt == "multipart/form-data" {
		form, fb, err := readForm(raw, params["boundary"])
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "a bad form: " + err.Error()}})
			return
		}
		req.Form, b = form, fb
	} else {
		if err := json.Unmarshal(raw, &b); err != nil {
			writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "body is not JSON: " + err.Error()}})
			return
		}
		req.Body = raw
	}
	kind, op, pathModel, ok := route(path, r.URL.Query())
	req.Model = b.Model
	if pathModel != "" {
		req.Model, b.Model = pathModel, pathModel
	}
	down := false
	s.with(name, func(t *tag) { t.Calls++; t.last = req; t.BodyBytes = len(raw); down = t.Down })
	switch {
	case !ok:
		writeJSON(w, 404, map[string]any{"error": map[string]any{"message": "no route " + path}})
		return
	case down:
		writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "upstream " + name + " is down"}})
		return
	case !authorized(r, name):
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
	if kind == kindOpenAI && op == "audio/speech" {
		// Speech: binary audio bytes, 0 to 255 over and over.
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(200)
		_, _ = w.Write(SpeechBytes())
		return
	}
	// Gemini streams on streamGenerateContent; Ollama's chat and generate stream unless told not to; the others when
	// told to.
	stream := b.Stream != nil && *b.Stream
	switch kind {
	case kindGemini:
		stream = op == "streamGenerateContent"
	case kindOllama:
		stream = (op == "chat" || op == "generate") && (b.Stream == nil || *b.Stream)
	}
	if !stream {
		writeJSON(w, 200, answer(kind, op, b.Model, text, req))
		return
	}
	chunks, gap := b.Chunks, time.Duration(b.ChunkMs)*time.Millisecond
	if chunks <= 0 {
		chunks = 3
	}
	if gap <= 0 {
		gap = 20 * time.Millisecond
	}
	sse := kind != kindOllama && !(kind == kindGemini && r.URL.Query().Get("alt") != "sse")
	switch {
	case sse:
		w.Header().Set("Content-Type", "text/event-stream")
	case kind == kindOllama:
		w.Header().Set("Content-Type", "application/x-ndjson")
	default:
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(200)
	fl := w.(http.Flusher)
	// send writes an event: an SSE event, an NDJSON line, or an element of Gemini's JSON array.
	n := 0
	send := func(event, data string) bool {
		switch {
		case sse:
			if event != "" {
				fmt.Fprintf(w, "event: %s\n", event)
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
		case kind == kindOllama:
			fmt.Fprintf(w, "%s\n", data)
		default:
			sep := ","
			if n == 0 {
				sep = "["
			}
			fmt.Fprintf(w, "%s%s\n", sep, data)
		}
		n++
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
		switch kind {
		case kindAnthropic:
			ok = send("content_block_delta", mustJSON(map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "text_delta", "text": part}}))
		case kindGemini:
			ok = send("", mustJSON(geminiAnswer(part, b.Model)))
		case kindOllama:
			ok = send("", mustJSON(ollamaAnswer(op, part, b.Model, false)))
		case kindCohere:
			ok = send("content-delta", mustJSON(map[string]any{"type": "content-delta", "index": 0,
				"delta": map[string]any{"message": map[string]any{"content": map[string]any{"text": part}}}}))
		default:
			ok = send("", mustJSON(map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": b.Model,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": part}}}}))
		}
		if !ok {
			return
		}
	}
	switch {
	case kind == kindAnthropic:
		send("message_stop", `{"type":"message_stop"}`)
	case kind == kindCohere:
		send("message-end", `{"type":"message-end"}`)
	case kind == kindOllama:
		send("", mustJSON(ollamaAnswer(op, "", b.Model, true)))
	case kind == kindGemini && !sse:
		fmt.Fprint(w, "]")
	case kind == kindOpenAI:
		send("", "[DONE]")
	}
}

// SpeechBytes is the audio the mock answers audio/speech with.
// SpeechBytes 模拟服务对 audio/speech 应答的音频字节。
func SpeechBytes() []byte {
	b := make([]byte, 16<<10)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func geminiAnswer(text, model string) map[string]any {
	return map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model",
		"parts": []any{map[string]any{"text": text}}}}}, "modelVersion": model}
}

func ollamaAnswer(op, text, model string, done bool) map[string]any {
	out := map[string]any{"model": model, "created_at": "1970-01-01T00:00:00Z", "done": done}
	if op == "generate" {
		out["response"] = text
	} else {
		out["message"] = map[string]any{"role": "assistant", "content": text}
	}
	return out
}

// answer is the whole answer of a call that does not stream.
func answer(kind, op, model, text string, req *Request) any {
	switch kind {
	case kindAnthropic:
		if op == "messages/count_tokens" {
			return map[string]any{"input_tokens": 7}
		}
		return map[string]any{"id": "msg_mock", "type": "message", "role": "assistant", "model": model,
			"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn"}
	case kindGemini:
		switch op {
		case "countTokens":
			return map[string]any{"totalTokens": 7}
		case "embedContent":
			return map[string]any{"embedding": map[string]any{"values": []float64{0.1, 0.2}}}
		case "batchEmbedContents":
			return map[string]any{"embeddings": []any{map[string]any{"values": []float64{0.1, 0.2}}}}
		}
		return geminiAnswer(text, model)
	case kindOllama:
		if op == "embed" {
			return map[string]any{"model": model, "embeddings": [][]float64{{0.1, 0.2}}}
		}
		return ollamaAnswer(op, text, model, true)
	case kindCohere:
		switch op {
		case "embed":
			return map[string]any{"id": "emb-mock", "embeddings": map[string]any{"float": [][]float64{{0.1, 0.2}}}}
		case "rerank":
			return map[string]any{"id": "rr-mock", "results": []any{map[string]any{"index": 0, "relevance_score": 0.9}}}
		}
		return map[string]any{"id": "chat-mock", "finish_reason": "COMPLETE", "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": text}}}}
	}
	switch op {
	case "embeddings":
		return map[string]any{"object": "list", "model": model,
			"data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2}}}}
	case "moderations":
		return map[string]any{"id": "modr-mock", "model": model, "results": []any{map[string]any{"flagged": false}}}
	case "rerank":
		return map[string]any{"id": "rerank-mock", "model": model, "results": []any{map[string]any{"index": 0, "relevance_score": 0.9}}}
	case "images/generations", "images/edits", "images/variations":
		return map[string]any{"created": 0, "data": []any{map[string]any{"b64_json": "aGVsbG8="}}, "echo": req.Form}
	case "audio/transcriptions", "audio/translations":
		return map[string]any{"text": text + " " + req.Form["file"]}
	}
	return map[string]any{"id": "chatcmpl-mock", "object": "chat.completion", "model": model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}}}}
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
