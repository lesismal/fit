package e2e

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/lesismal/fit/llm/internal/llmtest"
	"github.com/lesismal/fit/llm/internal/model"
)

// A role's pages: the shared files beside its own, its API behind the
// login and the CSRF token, /api/me, and a stream method as Server-Sent
// Events: the node's test call.
func TestPages(t *testing.T) {
	mock := llmtest.StartMockLLM(t)
	nf := llmtest.Start(t, member("node", `{"console": `+llmtest.Console()+`,
		"models": [{"name": "m", "api": "openai", "base_url": "`+mock.BaseURL("a")+`", "api_key": "sk-a"}]}`), "")
	n := nodeOf(t, nf)
	base := strings.TrimSuffix(n.Console().URL(), "/")
	for _, path := range []string{"/", "/app.js", "/static/ui.js", "/static/ui.css", "/static/peers.js", "/static/jobs.js", "/static/flow.js", "/static/forms.js"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	resp, err := http.Post(base+"/api/rpc/node/info", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("without a login: %d", resp.StatusCode)
	}
	p := pages(t, base)
	status, body := p.Do("GET", "/api/me", "")
	if status != 200 || !strings.Contains(body, `"user":"admin"`) || !strings.Contains(body, n.Self().ID) {
		t.Fatalf("me: %d %s", status, body)
	}
	// Without the CSRF token.
	if status, _ := p.WithoutCSRF().Do("POST", "/api/rpc/node/info", "{}"); status != 403 {
		t.Fatalf("without the CSRF token: %d", status)
	}
	if status, body := p.Do("POST", "/api/rpc/node/info", "{}"); status != 200 || !strings.Contains(body, `"models"`) {
		t.Fatalf("info: %d %s", status, body)
	}
	if status, _ := p.Do("POST", "/api/rpc/nope", "{}"); status != 404 {
		t.Fatalf("an unknown method: %d", status)
	}
	if status, body := p.Do("POST", "/api/rpc/node/info", "not json"); status != 400 {
		t.Fatalf("not JSON: %d %s", status, body)
	}
	// The test call: frames as events, then the end.
	status, body = p.Do("POST", "/api/stream"+model.MethodNodeProxy,
		`{"api": "openai", "path": "chat/completions", "model": "m", "body": "`+b64(`{"model":"m","stream":true,"chunks":2}`)+`"}`)
	if status != 200 || !strings.Contains(body, "event: end") || strings.Count(body, "\ndata: ")+strings.Count(body[:6], "data: ") < 3 {
		t.Fatalf("stream: %d %s", status, body)
	}
	// A stream that fails before it begins is a JSON error.
	status, body = p.Do("POST", "/api/stream"+model.MethodNodeProxy, `{"api": "openai", "path": "chat/completions", "model": "nope"}`)
	if status != 503 || !strings.Contains(body, `"error"`) {
		t.Fatalf("a failed stream: %d %s", status, body)
	}
	// A job, followed as events to its end.
	var job model.Job
	if err := p.RPC(model.MethodNodeActionRun, model.ActionRunReq{ID: "ollama_list"}, &job); err != nil {
		t.Fatal(err)
	}
	status, body = p.Do("POST", "/api/stream"+model.MethodNodeJobFollow, `{"id": `+strconv.FormatInt(job.ID, 10)+`}`)
	if status != 200 || !strings.Contains(body, "event: end") || !strings.Contains(body, "$ ollama list") {
		t.Fatalf("follow: %d %s", status, body)
	}
	if err := p.RPC(model.MethodNodeActionRun, model.ActionRunReq{ID: "ollama_pull", Params: map[string]string{"model": "-rf"}}, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
		t.Fatalf("a bad parameter: %v", err)
	}
	if status, _ := p.Do("POST", "/api/logout", ""); status != 204 {
		t.Fatalf("logout: %d", status)
	}
	if status, _ := p.Do("GET", "/api/me", ""); status != 401 {
		t.Fatalf("after logout: %d", status)
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
