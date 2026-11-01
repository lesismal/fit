package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/llm/internal/llmtest"
	"github.com/lesismal/fit/llm/proxy"
)

// upload builds a multipart/form-data body: the fields in order, then a
// file of size bytes.
func upload(t *testing.T, fields [][2]string, size int) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for _, f := range fields {
		_ = w.WriteField(f[0], f[1])
	}
	fw, err := w.CreateFormFile("file", "a.wav")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(bytes.Repeat([]byte{'R', 0, 0xff, '\r', '\n', '-', '-'}, size/7))
	_, _ = fw.Write(make([]byte, size%7))
	_ = w.Close()
	return b.Bytes(), w.FormDataContentType()
}

// OpenAI's endpoints beyond chat: uploads as multipart bodies, large ones
// streamed or held to set the model's upstream ID, speech as binary audio,
// and the JSON ones; slow agents and broken upstreams on them.
func TestOpenAIEndpoints(t *testing.T) {
	d := startDirect(t, `[{"name": "oa", "api": "openai", "base_url": "MOCK/u/o/v1", "api_key": "sk-o"},
		{"name": "whisper", "api": "openai", "base_url": "MOCK/u/w/v1", "api_key": "sk-w", "upstream": "whisper-1"},
		{"name": "plain", "api": "openai", "base_url": "MOCK/u/p/v1", "api_key": "sk-p"}]`, "", "")
	const size = 3 << 20 // past the 1 MiB a call carries inline
	for _, tc := range []struct{ name, tag, upstream string }{{"whisper", "w", "whisper-1"}, {"plain", "p", "plain"}} {
		body, ct := upload(t, [][2]string{{"model", tc.name}, {"language", "en"}}, size)
		status, resp := d.a.call(t, "/v1/audio/transcriptions", body, map[string]string{"Content-Type": ct})
		if status != 200 || !strings.Contains(resp, fmt.Sprintf("file:a.wav:%d", size)) {
			t.Fatalf("%s: %d %.300s", tc.name, status, resp)
		}
		last := d.mock.Last(tc.tag)
		if last.Path != "audio/transcriptions" || last.Model != tc.upstream || last.Form["language"] != "en" ||
			last.Form["file"] != fmt.Sprintf("file:a.wav:%d", size) || last.ContentType != ct || d.mock.Stats(tc.tag).BodyBytes != len(body)-len(tc.name)+len(tc.upstream) {
			t.Fatalf("%s: upstream saw %+v, %d bytes", tc.name, last, d.mock.Stats(tc.tag).BodyBytes)
		}
	}
	// A small upload, inline.
	body, ct := upload(t, [][2]string{{"prompt", "a cat"}, {"model", "whisper"}}, 100)
	if status, resp := d.a.call(t, "/v1/images/edits", body, map[string]string{"Content-Type": ct}); status != 200 || !strings.Contains(resp, "b64_json") {
		t.Fatalf("images/edits: %d %s", status, resp)
	}
	if last := d.mock.Last("w"); last.Path != "images/edits" || last.Model != "whisper-1" || last.Form["prompt"] != "a cat" {
		t.Fatalf("images/edits upstream saw %+v", last)
	}

	// Speech: binary audio, its content type passed on.
	resp, err := d.a.post("/v1/audio/speech", map[string]any{"model": "oa", "input": "hi", "voice": "alloy"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	audio, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mpeg" || !bytes.Equal(audio, llmtest.SpeechBytes()) {
		t.Fatalf("speech: %d %s %d bytes", resp.StatusCode, resp.Header.Get("Content-Type"), len(audio))
	}
	for path, want := range map[string]string{
		"/v1/images/generations": "b64_json", "/v1/moderations": `"flagged":false`, "/v1/rerank": "relevance_score",
		"/v1/embeddings": "embedding", "/v1/completions": "echo from o", "/v1/responses": "echo from o",
	} {
		if status, resp := d.a.call(t, path, map[string]any{"model": "oa"}, nil); status != 200 || !strings.Contains(resp, want) {
			t.Fatalf("%s: %d %s", path, status, resp)
		}
		if last := d.mock.Last("o"); "/v1/"+last.Path != path || last.Header["authorization"] != "Bearer sk-o" || last.Header["x-api-key"] != "" {
			t.Fatalf("%s: upstream saw %+v", path, last)
		}
	}

	// A slow agent reads a large answer to an upload whole.
	body, ct = upload(t, [][2]string{{"model", "plain"}, {"big_bytes", fmt.Sprint(8 << 20)}}, 2<<20)
	resp, err = d.a.post("/v1/audio/translations", body, map[string]string{"Content-Type": ct})
	if err != nil {
		t.Fatal(err)
	}
	buf, n := make([]byte, 64<<10), 0
	for i := 0; ; i++ {
		k, err := resp.Body.Read(buf)
		n += k
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("after %d bytes: %v", n, err)
		}
		if i < 40 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	resp.Body.Close()
	if n != 8<<20 {
		t.Fatalf("the slow agent got %d bytes", n)
	}
	// An upstream that breaks mid-answer cuts the agent's response short.
	body, ct = upload(t, [][2]string{{"model", "whisper"}, {"big_bytes", fmt.Sprint(4 << 20)}, {"cut_after", fmt.Sprint(1 << 20)}}, 1000)
	resp, err = d.a.post("/v1/audio/transcriptions", body, map[string]string{"Content-Type": ct})
	if err != nil {
		t.Fatal(err)
	}
	k, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err == nil || k >= 4<<20 {
		t.Fatalf("a broken upstream came whole: %d bytes, %v", k, err)
	}
	// Past the proxy's bound, an upload gets 413.
	body, ct = upload(t, [][2]string{{"model", "plain"}}, 33<<20)
	if resp, err := d.a.post("/v1/audio/transcriptions", body, map[string]string{"Content-Type": ct}); err == nil {
		resp.Body.Close()
		if resp.StatusCode != 413 {
			t.Fatalf("past the bound: %d", resp.StatusCode)
		}
	}
}

// Gemini: the model in the path, set to its upstream ID there; the SDK's
// key header or the key query; alt=sse kept and nothing else; events as
// they come; a batch's models set; Gemini's error shape; an agent going
// away cancels the upstream's call.
func TestGemini(t *testing.T) {
	d := startDirect(t, `[{"name": "gem", "api": "gemini", "base_url": "MOCK/u/g/v1beta", "api_key": "sk-g", "upstream": "gemini-2.5-flash"}]`, "", "")
	key := d.a.key
	g := newAgent(d.cl.API(), "")
	goog := map[string]string{"x-goog-api-key": key}
	status, body := g.call(t, "/v1beta/models/gem:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`, goog)
	if status != 200 || !strings.Contains(body, `"candidates"`) {
		t.Fatalf("generateContent: %d %s", status, body)
	}
	last := d.mock.Last("g")
	if last.Path != "v1beta/models/gemini-2.5-flash:generateContent" || last.Header["x-goog-api-key"] != "sk-g" ||
		last.Header["authorization"] != "" || last.Query != "" || string(last.Body) != `{"contents":[{"parts":[{"text":"hi"}]}]}` {
		t.Fatalf("upstream saw %+v", last)
	}
	// SSE, the key in the query: events come one by one, as the upstream
	// sends them.
	resp, err := g.post("/v1beta/models/gem:streamGenerateContent?alt=sse&key="+key, map[string]any{"chunks": 3, "chunk_ms": 300}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	start := time.Now()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, "#0") || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("first event %q %v after %v", line, err, time.Since(start))
	}
	resp.Body.Close()
	if last := d.mock.Last("g"); last.Query != "alt=sse" || last.Path != "v1beta/models/gemini-2.5-flash:streamGenerateContent" {
		t.Fatalf("stream upstream saw %+v", last)
	}
	llmtest.Eventually(t, "the upstream's call cancelled", func() bool { return d.mock.Stats("g").Canceled == 1 })
	// Without alt=sse, a JSON array.
	status, body = g.call(t, "/v1/models/gem:streamGenerateContent", map[string]any{"chunks": 2, "chunk_ms": 1}, goog)
	var arr []map[string]any
	if status != 200 || json.Unmarshal([]byte(body), &arr) != nil || len(arr) != 2 {
		t.Fatalf("JSON stream: %d %s", status, body)
	}
	status, body = g.call(t, "/v1beta/models/gem:batchEmbedContents", `{"requests":[{"model":"models/gem","content":{}}]}`, goog)
	if status != 200 || !strings.Contains(body, "embeddings") || !strings.Contains(string(d.mock.Last("g").Body), `"model":"models/gemini-2.5-flash"`) {
		t.Fatalf("batch: %d %s %s", status, body, d.mock.Last("g").Body)
	}
	for _, m := range []string{"countTokens", "embedContent"} {
		if status, body := g.call(t, "/v1beta/models/gem:"+m, `{}`, goog); status != 200 {
			t.Fatalf("%s: %d %s", m, status, body)
		}
	}
	status, body = g.call(t, "/v1beta/models/nope:generateContent", `{}`, goog)
	if status != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Fatalf("unknown model: %d %s", status, body)
	}
	list, err := g.hc.Get(g.base + "/v1beta/models?key=" + key)
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if list.StatusCode != 200 || !strings.Contains(string(lb), `"name":"models/gem"`) {
		t.Fatalf("models list: %d %s", list.StatusCode, lb)
	}
}

// Azure OpenAI, Ollama and Cohere models, each called the way its agents
// call it, each upstream called its own way.
func TestAzureOllamaCohere(t *testing.T) {
	d := startDirect(t, `[{"name": "az", "api": "azure_openai", "base_url": "MOCK/u/z", "api_key": "sk-z", "upstream": "dep-1", "api_version": "2024-10-21"},
		{"name": "llama", "api": "ollama", "base_url": "MOCK/u/l", "api_key": "sk-l", "upstream": "llama3.2:3b"},
		{"name": "cmd", "api": "cohere", "base_url": "MOCK/u/c/v2", "api_key": "sk-c"}]`, "", "")
	// Azure, called as OpenAI.
	resp, err := d.a.post("/v1/chat/completions", map[string]any{"model": "az", "stream": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := events(t, resp.Body)
	resp.Body.Close()
	if len(ev) != 4 || ev[3] != "[DONE]" {
		t.Fatalf("azure stream: %q", ev)
	}
	last := d.mock.Last("z")
	if last.Path != "openai/deployments/dep-1/chat/completions" || last.Query != "api-version=2024-10-21" || last.Header["api-key"] != "sk-z" ||
		last.Header["authorization"] != "" || last.Model != "az" {
		t.Fatalf("azure upstream saw %+v", last)
	}
	if status, body := d.a.call(t, "/v1/responses", map[string]any{"model": "az"}, nil); status != 200 || d.mock.Last("z").Path != "openai/responses" ||
		d.mock.Last("z").Model != "dep-1" {
		t.Fatalf("azure responses: %d %s %+v", status, body, d.mock.Last("z"))
	}
	if status, body := d.a.call(t, "/v1/images/variations", map[string]any{"model": "az"}, nil); status != 404 {
		t.Fatalf("azure images/variations: %d %s", status, body)
	}

	// Ollama: NDJSON as it streams, its model set.
	resp, err = d.a.post("/api/chat", map[string]any{"model": "llama", "messages": []any{}, "chunks": 3, "chunk_ms": 300}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("ollama content type %q", resp.Header.Get("Content-Type"))
	}
	start := time.Now()
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	if err != nil || !strings.Contains(line, "#0") || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("first line %q %v after %v", line, err, time.Since(start))
	}
	rest, _ := io.ReadAll(br)
	resp.Body.Close()
	if lines := strings.Split(strings.TrimSpace(string(rest)), "\n"); len(lines) != 3 || !strings.Contains(lines[2], `"done":true`) {
		t.Fatalf("ollama lines %q", rest)
	}
	if last := d.mock.Last("l"); last.Path != "api/chat" || last.Model != "llama3.2:3b" || last.Header["authorization"] != "Bearer sk-l" {
		t.Fatalf("ollama upstream saw %+v", last)
	}
	if status, body := d.a.call(t, "/api/embed", map[string]any{"model": "llama", "input": "x"}, nil); status != 200 || !strings.Contains(body, "embeddings") {
		t.Fatalf("ollama embed: %d %s", status, body)
	}
	// An agent going away mid-stream cancels the upstream's call.
	resp, err = d.a.post("/api/generate", map[string]any{"model": "llama", "chunks": 100, "chunk_ms": 50}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	llmtest.Eventually(t, "the ollama call cancelled", func() bool { return d.mock.Stats("l").Canceled == 1 })

	// Cohere v2.
	resp, err = d.a.post("/v2/chat", map[string]any{"model": "cmd", "stream": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev = events(t, resp.Body)
	resp.Body.Close()
	if len(ev) != 4 || !strings.Contains(ev[0], "content-delta") {
		t.Fatalf("cohere stream: %q", ev)
	}
	if status, body := d.a.call(t, "/v2/rerank", map[string]any{"model": "cmd"}, nil); status != 200 || !strings.Contains(body, "relevance_score") ||
		d.mock.Last("c").Path != "v2/rerank" {
		t.Fatalf("cohere rerank: %d %s", status, body)
	}
	if status, body := d.a.call(t, "/v2/chat", map[string]any{"model": "llama"}, nil); status != 404 || !strings.Contains(body, `"message"`) {
		t.Fatalf("an ollama model on cohere's path: %d %s", status, body)
	}
}

// Through a hub: the hub picks a node by the API agents speak, so an
// azure_openai model takes OpenAI's calls, and Gemini's path model routes.
func TestKindsThroughHub(t *testing.T) {
	mock := llmtest.StartMockLLM(t)
	hubAddr := freeAddr(t)
	llmtest.Start(t, member("hub", `{"listen": {"address": "`+hubAddr+`", "password": "`+meshPass+`"}}`), "")
	llmtest.Start(t, member("node", `{"peers": [{"address": "`+hubAddr+`", "password": "`+meshPass+`", "role": "hub"}],
		"models": [{"name": "az", "api": "azure_openai", "base_url": "`+mock.URL+`/u/z", "api_key": "sk-z", "api_version": "v1"},
		{"name": "gem", "api": "gemini", "base_url": "`+mock.URL+`/u/g/v1beta", "api_key": "sk-g"}]}`), "")
	cf := llmtest.Start(t, member("proxy", `{"console": `+llmtest.Console()+`, "api": {"address": "127.0.0.1:0"},
		"peers": [{"address": "`+hubAddr+`", "password": "`+meshPass+`", "role": "hub"}]}`), "")
	c := proxyOf(t, cf)
	cp := pages(t, c.Console().URL())
	llmtest.Eventually(t, "the proxy's catalog", func() bool {
		var cat []proxy.CatalogModel
		return cp.RPC(proxy.MethodProxyModels, nil, &cat) == nil && len(cat) == 2 && cat[0].Ready && cat[1].Ready
	})
	var key proxy.KeyCreateResp
	if err := cp.RPC(proxy.MethodProxyKeysCreate, proxy.KeyCreateReq{Name: "k"}, &key); err != nil {
		t.Fatal(err)
	}
	a := newAgent(c.API(), key.Key)
	if status, body := a.call(t, "/v1/chat/completions", map[string]any{"model": "az"}, nil); status != 200 ||
		mock.Last("z").Path != "openai/deployments/az/chat/completions" {
		t.Fatalf("azure through the hub: %d %s", status, body)
	}
	if status, body := a.call(t, "/v1beta/models/gem:generateContent", `{}`, nil); status != 200 ||
		mock.Last("g").Path != "v1beta/models/gem:generateContent" {
		t.Fatalf("gemini through the hub: %d %s", status, body)
	}
	if status, body := a.call(t, "/v1/messages", map[string]any{"model": "az"}, nil); status != 404 || !strings.Contains(body, "openai API") {
		t.Fatalf("azure on anthropic's path: %d %s", status, body)
	}
}
