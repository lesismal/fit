package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesismal/fit/agent/internal/model"
)

func TestAgentEndpoint(t *testing.T) {
	for path, want := range map[string]endpoint{
		"/v1/chat/completions":                        {api: "openai", path: "chat/completions"},
		"/v1/audio/transcriptions":                    {api: "openai", path: "audio/transcriptions"},
		"/v1/rerank":                                  {api: "openai", path: "rerank"},
		"/v1/messages/count_tokens":                   {api: "anthropic", path: "messages/count_tokens"},
		"/v1beta/models/gemini-2.5-flash:countTokens": {api: "gemini", path: "countTokens", model: "gemini-2.5-flash"},
		"/v1/models/qwen3:8b:generateContent":         {api: "gemini", path: "generateContent", model: "qwen3:8b"},
		"/api/chat":                                   {api: "ollama", path: "api/chat"},
		"/v2/rerank":                                  {api: "cohere", path: "rerank"},
	} {
		if got, ok := agentEndpoint(path); !ok || got != want {
			t.Errorf("%s: %+v %v", path, got, ok)
		}
	}
	for path, api := range map[string]string{
		"/v1/files": "", "/v1beta/models/g:tuneModel": "gemini", "/v1beta/models/:generateContent": "gemini",
		"/v1beta/models/g": "gemini", "/api/tags": "", "/v1/chat/completions/x": "",
	} {
		if got, ok := agentEndpoint(path); ok || got.api != api {
			t.Errorf("%s: %+v %v", path, got, ok)
		}
	}
}

// TestKindsAPI: the calls of every protocol reach a node with their model,
// query and content type, keys come in each SDK's header, errors take each
// API's shape, and the model lists answer as each SDK reads them.
func TestKindsAPI(t *testing.T) {
	node := newNode(t, "node-a", ready("gpt", model.APIOpenAI), ready("az", model.APIAzureOpenAI), ready("gem", model.APIGemini),
		ready("llama", model.APIOllama), ready("cmd", model.APICohere))
	f := newFixture(t, node)
	last := func() model.ProxyReq { r := node.requests(); return r[len(r)-1] }
	const form = "--XX\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n\r\nRIFF\r\n" +
		"--XX\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ngpt\r\n--XX--\r\n"
	for _, tc := range []struct {
		path, body string
		hdr        map[string]string
		want       model.ProxyReq
	}{
		{"/v1/audio/transcriptions", form, map[string]string{"Content-Type": "multipart/form-data; boundary=XX", "Authorization": "Bearer " + f.key},
			model.ProxyReq{API: "openai", Path: "audio/transcriptions", Model: "gpt", ContentType: "multipart/form-data; boundary=XX"}},
		{"/v1/chat/completions", `{"model":"az"}`, f.bearer(), model.ProxyReq{API: "openai", Path: "chat/completions", Model: "az"}},
		{"/v1beta/models/gem:streamGenerateContent?alt=sse&key=" + f.key + "&x=1", `{"contents":[]}`, nil,
			model.ProxyReq{API: "gemini", Path: "streamGenerateContent", Model: "gem", Query: "alt=sse"}},
		{"/v1beta/models/gem:generateContent", `not json is the upstream's business`, map[string]string{"x-goog-api-key": f.key},
			model.ProxyReq{API: "gemini", Path: "generateContent", Model: "gem"}},
		{"/api/chat", `{"model":"llama"}`, f.bearer(), model.ProxyReq{API: "ollama", Path: "api/chat", Model: "llama"}},
		{"/v2/chat", `{"model":"cmd"}`, f.bearer(), model.ProxyReq{API: "cohere", Path: "chat", Model: "cmd"}},
	} {
		resp := f.do(t, context.Background(), "POST", tc.path, tc.body, tc.hdr)
		if body := readAll(t, resp); resp.StatusCode != 200 || body != "node:node-a" || resp.Header.Get("X-Fit-Node") != "node-a" {
			t.Fatalf("%s: %d %s", tc.path, resp.StatusCode, body)
		}
		got := last()
		if got.API != tc.want.API || got.Path != tc.want.Path || got.Model != tc.want.Model || got.ContentType != tc.want.ContentType ||
			got.Query != tc.want.Query || string(got.Body) != tc.body {
			t.Fatalf("%s: %+v", tc.path, got)
		}
	}

	// Errors in each API's shape.
	type shape struct {
		Error   json.RawMessage
		Message string
	}
	for _, tc := range []struct {
		method, path, body string
		hdr                map[string]string
		status             int
		check              func(s shape) bool
	}{
		{"POST", "/v1beta/models/nope:generateContent", `{}`, map[string]string{"x-goog-api-key": f.key}, 404, func(s shape) bool {
			var e struct {
				Code            int
				Status, Message string
			}
			return json.Unmarshal(s.Error, &e) == nil && e.Code == 404 && e.Status == "NOT_FOUND" && strings.Contains(e.Message, "nope")
		}},
		{"POST", "/v1beta/models/gem:generateContent", `{}`, nil, 401, func(s shape) bool { return strings.Contains(string(s.Error), "UNAUTHENTICATED") }},
		{"POST", "/v1beta/models/gem:tuneModel", `{}`, nil, 404, func(s shape) bool { return strings.Contains(string(s.Error), `"status":"NOT_FOUND"`) }},
		{"POST", "/api/chat", `{"model":"gpt"}`, f.bearer(), 404, func(s shape) bool {
			var e string
			return json.Unmarshal(s.Error, &e) == nil && strings.Contains(e, "openai API, not ollama")
		}},
		{"POST", "/api/chat", `{}`, f.bearer(), 400, func(s shape) bool { return len(s.Error) > 0 }},
		{"POST", "/api/pull", `{}`, f.bearer(), 404, func(s shape) bool { return len(s.Error) > 0 }},
		{"POST", "/v2/chat", `{"model":"x"}`, f.bearer(), 404, func(s shape) bool { return s.Message != "" && s.Error == nil }},
		{"POST", "/v2/chat", `{}`, nil, 401, func(s shape) bool { return s.Message != "" }},
		{"POST", "/v1/messages", "--XX\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ngpt\r\n--XX--",
			map[string]string{"Content-Type": "multipart/form-data; boundary=XX", "x-api-key": f.key}, 400,
			func(s shape) bool { return strings.Contains(string(s.Error), "multipart") }},
		{"POST", "/v1/audio/transcriptions", "--XX\r\nContent-Disposition: form-data; name=\"language\"\r\n\r\nen\r\n--XX--",
			map[string]string{"Content-Type": "multipart/form-data; boundary=XX", "Authorization": "Bearer " + f.key}, 400,
			func(s shape) bool { return strings.Contains(string(s.Error), "no model field") }},
		{"POST", "/v1/audio/transcriptions", "--XX\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\n\r\n--XX--",
			map[string]string{"Content-Type": "multipart/form-data; boundary=XX", "Authorization": "Bearer " + f.key}, 400,
			func(s shape) bool { return strings.Contains(string(s.Error), "missing") }},
		{"POST", "/v1/chat/completions", `{"model":"a b"}`, f.bearer(), 400, func(s shape) bool { return strings.Contains(string(s.Error), "without spaces") }},
		{"GET", "/v1beta/models/nope", "", map[string]string{"x-goog-api-key": f.key}, 404, func(s shape) bool { return strings.Contains(string(s.Error), "NOT_FOUND") }},
		{"GET", "/api/tags", "", nil, 401, func(s shape) bool { return len(s.Error) > 0 && s.Error[0] == '"' }},
	} {
		resp := f.do(t, context.Background(), tc.method, tc.path, tc.body, tc.hdr)
		body := readAll(t, resp)
		var s shape
		if resp.StatusCode != tc.status || json.Unmarshal([]byte(body), &s) != nil || !tc.check(s) {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, resp.StatusCode, body)
		}
	}

	// The model lists.
	resp := f.do(t, context.Background(), "GET", "/v1beta/models", "", map[string]string{"x-goog-api-key": f.key})
	body := readAll(t, resp)
	var gl struct {
		Models []struct {
			Name                       string
			SupportedGenerationMethods []string
		}
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &gl) != nil || len(gl.Models) != 1 || gl.Models[0].Name != "models/gem" ||
		len(gl.Models[0].SupportedGenerationMethods) != 5 {
		t.Fatalf("gemini list: %d %s", resp.StatusCode, body)
	}
	resp = f.do(t, context.Background(), "GET", "/v1beta/models/gem?key="+f.key, "", nil)
	if body := readAll(t, resp); resp.StatusCode != 200 || !strings.Contains(body, `"name":"models/gem"`) {
		t.Fatalf("gemini get: %d %s", resp.StatusCode, body)
	}
	resp = f.do(t, context.Background(), "GET", "/api/tags", "", f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 200 || !strings.Contains(body, `{"models":[{"name":"llama","model":"llama"`) {
		t.Fatalf("ollama tags: %d %s", resp.StatusCode, body)
	}
	resp = f.do(t, context.Background(), "GET", "/v1/models", "", f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 200 || strings.Count(body, `"owned_by":"fit"`) != 5 {
		t.Fatalf("models: %d %s", resp.StatusCode, body)
	}
}
