package node

import (
	"bytes"
	"context"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/model"
)

func TestUpstreamURL(t *testing.T) {
	for _, tc := range []struct {
		m      model.Model
		req    model.ProxyReq
		up     string
		url    string
		inBody bool
	}{
		{model.Model{API: model.APIOpenAI, BaseURL: "http://h/v1"}, model.ProxyReq{Path: "audio/speech"}, "m", "http://h/v1/audio/speech", true},
		{model.Model{API: model.APIAnthropic, BaseURL: "http://h/v1"}, model.ProxyReq{Path: "messages"}, "m", "http://h/v1/messages", true},
		{model.Model{API: model.APIOllama, BaseURL: "http://h"}, model.ProxyReq{Path: "api/chat"}, "m", "http://h/api/chat", true},
		{model.Model{API: model.APICohere, BaseURL: "http://h/v2"}, model.ProxyReq{Path: "rerank"}, "m", "http://h/v2/rerank", true},
		// Gemini: the model in the path, colons kept, only alt passed.
		{model.Model{API: model.APIGemini, BaseURL: "http://h/v1beta"}, model.ProxyReq{Path: "streamGenerateContent", Query: "alt=sse&key=secret"},
			"gem:1", "http://h/v1beta/models/gem:1:streamGenerateContent?alt=sse", false},
		{model.Model{API: model.APIGemini, BaseURL: "http://h/v1beta"}, model.ProxyReq{Path: "countTokens", Query: "key=secret"},
			"g", "http://h/v1beta/models/g:countTokens", false},
		// Azure: the deployment in the path, the api-version set; the
		// responses API names the deployment in the body.
		{model.Model{API: model.APIAzureOpenAI, BaseURL: "https://r.openai.azure.com", APIVersion: "2024-10-21"},
			model.ProxyReq{Path: "chat/completions", Query: "api-version=evil"}, "dep 1",
			"https://r.openai.azure.com/openai/deployments/dep%201/chat/completions?api-version=2024-10-21", false},
		{model.Model{API: model.APIAzureOpenAI, BaseURL: "https://r.openai.azure.com", APIVersion: "preview"},
			model.ProxyReq{Path: "responses"}, "dep", "https://r.openai.azure.com/openai/responses?api-version=preview", true},
	} {
		got, inBody, err := upstreamURL(tc.m, &tc.req, tc.up)
		if err != nil || got != tc.url || inBody != tc.inBody {
			t.Errorf("%s %s: %q %v %v, want %q %v", tc.m.API, tc.req.Path, got, inBody, err, tc.url, tc.inBody)
		}
	}
	g := model.Model{API: model.APIGemini, BaseURL: "http://h"}
	if _, _, err := upstreamURL(g, &model.ProxyReq{Path: "countTokens", Query: strings.Repeat("a", maxQuery+1)}, "g"); code(err) != model.CodeBadRequest {
		t.Errorf("a long query: %v", err)
	}
	if _, _, err := upstreamURL(g, &model.ProxyReq{Path: "countTokens", Query: "a=%zz"}, "g"); code(err) != model.CodeBadRequest {
		t.Errorf("a bad query: %v", err)
	}
}

func TestSetAuthKinds(t *testing.T) {
	for api, want := range map[string]string{
		model.APIOpenAI: "Authorization", model.APIOllama: "Authorization", model.APICohere: "Authorization",
		model.APIAnthropic: "X-Api-Key", model.APIGemini: "X-Goog-Api-Key", model.APIAzureOpenAI: "Api-Key",
	} {
		h := stdhttp.Header{}
		setAuth(h, model.Model{API: api, APIKey: "k"}, map[string]string{"openai-beta": "b"})
		v := h.Get(want)
		if v != "k" && v != "Bearer k" {
			t.Errorf("%s: %v", api, h)
		}
		if (h.Get("OpenAI-Beta") != "") != (api == model.APIOpenAI) {
			t.Errorf("%s: openai-beta %v", api, h)
		}
		h = stdhttp.Header{}
		setAuth(h, model.Model{API: api}, nil)
		if h.Get(want) != "" {
			t.Errorf("%s without a key: %v", api, h)
		}
	}
	for api, want := range map[string]string{
		model.APIOpenAI: "http://h/models", model.APIGemini: "http://h/models", model.APIOllama: "http://h/api/tags",
		model.APIAzureOpenAI: "http://h/openai/models?api-version=v1",
	} {
		if got := probeURL(model.Model{API: api, BaseURL: "http://h", APIVersion: "v1"}); got != want {
			t.Errorf("probe %s: %s", api, got)
		}
	}
}

// form builds a multipart/form-data body: fields in order, then a file.
func form(boundary string, fields [][2]string, file []byte) []byte {
	var b bytes.Buffer
	for _, f := range fields {
		b.WriteString("--" + boundary + "\r\nContent-Disposition: form-data; name=\"" + f[0] + "\"\r\n\r\n" + f[1] + "\r\n")
	}
	if file != nil {
		b.WriteString("--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\nContent-Type: audio/wav\r\n\r\n")
		b.Write(file)
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}

func TestRewriteBodies(t *testing.T) {
	file := bytes.Repeat([]byte("\x00\xffmodel"), 1000)
	body := form("XyZ", [][2]string{{"language", "en"}, {"model", "whisper"}}, file)
	got, err := rewriteForm(body, "XyZ", "whisper-large-v3")
	want := form("XyZ", [][2]string{{"language", "en"}, {"model", "whisper-large-v3"}}, file)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("form rewrite: %v\n%q", err, got)
	}
	if _, err := rewriteForm(form("XyZ", [][2]string{{"language", "en"}}, file), "XyZ", "x"); err == nil {
		t.Fatal("a form without a model field rewritten")
	}
	got, err = rewriteBatchModels([]byte(`{"requests":[{"model":"models/g","content":{"parts":[{"text":"<a>"}]}},{"content":{}}]}`), "real")
	if err != nil || string(got) != `{"requests":[{"content":{"parts":[{"text":"<a>"}]},"model":"models/real"},{"content":{},"model":"models/real"}]}` {
		t.Fatalf("batch rewrite: %s %v", got, err)
	}
	for _, bad := range []string{`[]`, `{"requests":1}`, `nope`} {
		if _, err := rewriteBatchModels([]byte(bad), "x"); err == nil {
			t.Errorf("batch rewrite of %s", bad)
		}
	}
	for ct, want := range map[string]string{
		"":                                    "application/json",
		"application/json; charset=utf-8":     "application/json; charset=utf-8",
		"multipart/form-data; boundary=XyZ":   "multipart/form-data; boundary=XyZ",
		"multipart/form-data":                 "",
		"text/plain":                          "",
		"multipart/form-data; boundary=\"a b": "",
	} {
		got, _, err := bodyType(ct)
		if got != want || (err != nil) != (want == "") {
			t.Errorf("bodyType(%q) = %q %v", ct, got, err)
		}
	}
}

// recorder is an upstream that records each request whole and answers
// with a JSON echo of its model.
type recorder struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	path, query, ctype string
	header             stdhttp.Header
	body               []byte
}

func newRecorder(t *testing.T) *recorder {
	r := &recorder{}
	r.Server = httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		b, _ := io.ReadAll(req.Body)
		if req.Method == stdhttp.MethodGet {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		r.mu.Lock()
		r.reqs = append(r.reqs, recorded{req.URL.Path, req.URL.RawQuery, req.Header.Get("Content-Type"), req.Header.Clone(), b})
		r.mu.Unlock()
		if strings.HasSuffix(req.URL.Path, "/audio/speech") {
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte{0, 1, 2, 0xff})
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("{\"done\":false}\n{\"done\":true}\n"))
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) last(t *testing.T) recorded {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		t.Fatal("no request upstream")
	}
	return r.reqs[len(r.reqs)-1]
}

// streamKind runs one call whose body follows in frames.
func streamKind(t *testing.T, n *Node, req model.ProxyReq, frames ...[]byte) (model.ProxyHead, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req.BodyStream = true
	st, err := openProxy(ctx, n, req)
	if err != nil {
		return model.ProxyHead{}, nil, err
	}
	go func() {
		for _, f := range frames {
			if st.Send(ctx, f) != nil {
				return
			}
		}
		_ = st.CloseSend()
	}()
	return readProxy(ctx, st)
}

// TestProxyKinds: each kind's URL, auth header and model substitution, as
// the upstream sees them, with bodies inline and streamed.
func TestProxyKinds(t *testing.T) {
	u := newRecorder(t)
	n := startNode(t, &config.LLMNode{Proxy: &config.LLMNodeProxy{MaxBodyBytes: 1 << 20}, Models: []config.LLMModel{
		{Name: "gpt", API: model.APIOpenAI, BaseURL: u.URL + "/v1", APIKey: "ko"},
		{Name: "whisper", API: model.APIOpenAI, BaseURL: u.URL + "/v1", Upstream: "whisper-1"},
		{Name: "gem", API: model.APIGemini, BaseURL: u.URL + "/v1beta", Upstream: "gemini-2.5-flash", APIKey: "kg"},
		{Name: "az", API: model.APIAzureOpenAI, BaseURL: u.URL, Upstream: "my-dep", APIKey: "ka", APIVersion: "2024-10-21"},
		{Name: "llama", API: model.APIOllama, BaseURL: u.URL, Upstream: "llama3.2:3b"},
		{Name: "cmd", API: model.APICohere, BaseURL: u.URL + "/v2", APIKey: "kc"},
	}}, nil)
	file := bytes.Repeat([]byte{0, 0xff, '\r', '\n', '-'}, 300<<10/5)
	upload := form("b0und", [][2]string{{"model", "whisper"}, {"language", "en"}}, file)
	for _, tc := range []struct {
		name   string
		req    model.ProxyReq
		frames [][]byte
		check  func(r recorded, head model.ProxyHead, body []byte) bool
	}{
		{"openai speech", model.ProxyReq{API: "openai", Path: "audio/speech", Model: "gpt", Body: []byte(`{"model":"gpt","input":"hi"}`)}, nil,
			func(r recorded, head model.ProxyHead, body []byte) bool {
				return r.path == "/v1/audio/speech" && r.header.Get("Authorization") == "Bearer ko" && head.ContentType == "audio/mpeg" &&
					bytes.Equal(body, []byte{0, 1, 2, 0xff}) && string(r.body) == `{"model":"gpt","input":"hi"}`
			}},
		{"openai upload, rewritten, inline", model.ProxyReq{API: "openai", Path: "audio/transcriptions", Model: "whisper",
			ContentType: "multipart/form-data; boundary=b0und", Body: upload}, nil,
			func(r recorded, _ model.ProxyHead, _ []byte) bool {
				return r.path == "/v1/audio/transcriptions" && r.ctype == "multipart/form-data; boundary=b0und" &&
					bytes.Equal(r.body, form("b0und", [][2]string{{"model", "whisper-1"}, {"language", "en"}}, file))
			}},
		{"openai upload, rewritten, streamed", model.ProxyReq{API: "openai", Path: "audio/translations", Model: "whisper",
			ContentType: "multipart/form-data; boundary=b0und"}, [][]byte{upload[:1000], upload[1000:]},
			func(r recorded, _ model.ProxyHead, _ []byte) bool {
				return r.path == "/v1/audio/translations" && bytes.Equal(r.body, form("b0und", [][2]string{{"model", "whisper-1"}, {"language", "en"}}, file))
			}},
		{"gemini stream", model.ProxyReq{API: "gemini", Path: "streamGenerateContent", Model: "gem", Query: "alt=sse", Body: []byte(`{"contents":[]}`)}, nil,
			func(r recorded, _ model.ProxyHead, _ []byte) bool {
				return r.path == "/v1beta/models/gemini-2.5-flash:streamGenerateContent" && r.query == "alt=sse" &&
					r.header.Get("X-Goog-Api-Key") == "kg" && r.header.Get("Authorization") == "" && string(r.body) == `{"contents":[]}`
			}},
		{"gemini body streamed through", model.ProxyReq{API: "gemini", Path: "generateContent", Model: "gem"}, [][]byte{[]byte(`{"contents"`), []byte(`:[]}`)},
			func(r recorded, _ model.ProxyHead, _ []byte) bool { return string(r.body) == `{"contents":[]}` }},
		{"gemini batch", model.ProxyReq{API: "gemini", Path: "batchEmbedContents", Model: "gem", Body: []byte(`{"requests":[{"model":"models/gem"}]}`)}, nil,
			func(r recorded, _ model.ProxyHead, _ []byte) bool {
				return string(r.body) == `{"requests":[{"model":"models/gemini-2.5-flash"}]}`
			}},
		{"azure chat", model.ProxyReq{API: "openai", Path: "chat/completions", Model: "az", Body: []byte(`{"model":"az"}`)}, nil,
			func(r recorded, _ model.ProxyHead, _ []byte) bool {
				return r.path == "/openai/deployments/my-dep/chat/completions" && r.query == "api-version=2024-10-21" &&
					r.header.Get("Api-Key") == "ka" && r.header.Get("Authorization") == "" && string(r.body) == `{"model":"az"}`
			}},
		{"azure responses", model.ProxyReq{API: "azure_openai", Path: "responses", Model: "az", Body: []byte(`{"model":"az"}`)}, nil,
			func(r recorded, _ model.ProxyHead, _ []byte) bool {
				return r.path == "/openai/responses" && r.query == "api-version=2024-10-21" && string(r.body) == `{"model":"my-dep"}`
			}},
		{"ollama", model.ProxyReq{API: "ollama", Path: "api/chat", Model: "llama", Body: []byte(`{"model":"llama"}`)}, nil,
			func(r recorded, head model.ProxyHead, body []byte) bool {
				return r.path == "/api/chat" && string(r.body) == `{"model":"llama3.2:3b"}` && r.header.Get("Authorization") == "" &&
					head.ContentType == "application/x-ndjson" && strings.Count(string(body), "\n") == 2
			}},
		{"cohere", model.ProxyReq{API: "cohere", Path: "rerank", Model: "cmd", Body: []byte(`{"model":"cmd"}`)}, nil,
			func(r recorded, _ model.ProxyHead, _ []byte) bool {
				return r.path == "/v2/rerank" && r.header.Get("Authorization") == "Bearer kc"
			}},
	} {
		var head model.ProxyHead
		var body []byte
		var err error
		if tc.frames != nil {
			head, body, err = streamKind(t, n, tc.req, tc.frames...)
		} else {
			head, body, err = doProxy(t, n, tc.req)
		}
		if err != nil || head.Status != 200 {
			t.Fatalf("%s: %d %v", tc.name, head.Status, err)
		}
		if r := u.last(t); !tc.check(r, head, body) {
			t.Fatalf("%s: upstream saw %s?%s %q %v %.200q; head %+v body %.100q", tc.name, r.path, r.query, r.ctype, r.header, r.body, head, body)
		}
	}
	// Refusals: a call azure does not serve, a kind's model asked with
	// another protocol, a form without a model to set, a bad content type,
	// an upload past the bound while held to set its model.
	for _, tc := range []struct {
		req  model.ProxyReq
		code int
	}{
		{model.ProxyReq{API: "openai", Path: "images/variations", Model: "az", Body: []byte(`{}`)}, model.CodeNotFound},
		{model.ProxyReq{API: "anthropic", Path: "messages", Model: "gpt", Body: []byte(`{}`)}, model.CodeUnavailable},
		{model.ProxyReq{API: "openai", Path: "audio/transcriptions", Model: "whisper", ContentType: "multipart/form-data; boundary=b0und",
			Body: form("b0und", nil, file)}, model.CodeBadRequest},
		{model.ProxyReq{API: "openai", Path: "chat/completions", Model: "gpt", ContentType: "text/plain", Body: []byte(`x`)}, model.CodeBadRequest},
		{model.ProxyReq{API: "gemini", Path: "chat/completions", Model: "gem", Body: []byte(`{}`)}, model.CodeBadRequest},
	} {
		if _, _, err := doProxy(t, n, tc.req); code(err) != tc.code {
			t.Errorf("%+v: %v, want code %d", tc.req.Path, err, tc.code)
		}
	}
	big := bytes.Repeat([]byte("z"), 600<<10)
	if _, _, err := streamKind(t, n, model.ProxyReq{API: "openai", Path: "audio/transcriptions", Model: "whisper",
		ContentType: "multipart/form-data; boundary=b0und"}, upload, big, big); code(err) != model.CodeBadRequest {
		t.Errorf("an upload past the bound: %v", err)
	}
	idle(t, n)
}
