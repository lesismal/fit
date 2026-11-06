package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// TestConsoleAssistantChat: the console's assistant reads through the
// Admin API in process, fit's log ring and fit validate's checks: a chat
// whose model calls config_get, log_tail and config_validate gets the
// running config, redacted, the log's lines and a valid config back.
func TestConsoleAssistantChat(t *testing.T) {
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	root.Logger(config.LoggerServer).Errorf("upstream x: connection refused")
	f := &consoleFixture{fixture: startEnv(t, doc("hello from fit"), &config.Admin{}, runtime.Env{Metrics: observe.NewMetrics(), Log: root})}

	var mu sync.Mutex
	var bodies []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[`+
				`{"index":0,"id":"c1","type":"function","function":{"name":"config_get","arguments":"{}"}},`+
				`{"index":1,"id":"c2","type":"function","function":{"name":"log_tail","arguments":"{}"}},`+
				`{"index":2,"id":"c3","type":"function","function":{"name":"config_validate","arguments":"{\"patch\":{}}"}}]},"finish_reason":"tool_calls"}]}`+
				"\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"It answers hello."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()

	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	settings := &config.Assistant{Providers: []config.AssistantProvider{{Name: "m", Kind: config.AssistantOpenAICompatible, BaseURL: model.URL + "/v1",
		Models: []config.AssistantModel{{Name: "x"}}}}, Data: &config.AssistantData{SendLogs: true}}
	cs, err := StartConsole(testEngine(t), c, &config.Admin{Assistant: settings}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Shutdown(context.Background()) })
	f.console, f.url = cs, strings.TrimSuffix(cs.URL(), "/")
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != 200 {
		t.Fatal(code)
	}
	code, body, h := b.do("POST", "/api/m/ai/chat", `{"message": "what does the route answer?"}`)
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") || !strings.Contains(body, `"reason":"answer"`) {
		t.Fatalf("%d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("%d calls", len(bodies))
	}
	second := bodies[1]
	for _, want := range []string{"hello from fit", "connection refused", `\"valid\":true`, "tool_output tool=\\\"config_get\\\""} {
		if !strings.Contains(second, want) {
			t.Errorf("the second call lacks %s: %.2000s", want, second)
		}
	}
	if strings.Contains(second, string(hash)) || strings.Contains(body, string(hash)) {
		t.Error("a password hash reached the model")
	}
	if err := validateConfig([]byte(`{"listeners": [{"name": "w", "address": ":1"}], "routes": [{"handler": {"module": "nope"}}]}`)); err == nil {
		t.Error("a module that is not compiles")
	}
	if err := validateConfig([]byte(`{`)); err == nil {
		t.Error("a config that does not load")
	}
}
