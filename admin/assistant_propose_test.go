package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

// TestConsoleAssistantProposal: a change asked in words becomes a proposal
// of two steps, checked on the running config and applied, only on the
// confirm, as one change through the Admin API's PUT /config/text: one new
// version, which /versions lists as the console's, served at once and
// saved; reading the proposal changes nothing, and a propose user cannot
// apply it.
func TestConsoleAssistantProposal(t *testing.T) {
	f := &consoleFixture{fixture: start(t, doc("one"), &config.Admin{})}
	var mu sync.Mutex
	var saved []string
	f.configs.UseStore(nil, func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, string(data))
		return nil
	})
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[`+
				`{"index":0,"id":"c1","type":"function","function":{"name":"config_propose","arguments":"{\"summary\":\"Answer two\",\"ops\":[{\"op\":\"replace\",\"path\":\"/routes/0/handler/params/body\",\"value\":\"two\"}]}"}},`+
				`{"index":1,"id":"c2","type":"function","function":{"name":"config_propose","arguments":"{\"summary\":\"Name the route\",\"ops\":[{\"op\":\"add\",\"path\":\"/routes/0/name\",\"value\":\"main\"}]}"}}]},"finish_reason":"tool_calls"}]}`+
				"\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"Proposed."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()

	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}, {Username: "bob", PasswordHash: string(hash)}}}
	settings := &config.Assistant{Providers: []config.AssistantProvider{{Name: "m", Kind: config.AssistantOpenAICompatible, BaseURL: model.URL + "/v1",
		Models: []config.AssistantModel{{Name: "x"}}}}, Users: []config.AssistantUser{{Username: "ada", Level: config.AssistantApply}, {Username: "bob", Level: config.AssistantPropose}}}
	var logMu sync.Mutex
	var logs []string
	cs, err := StartConsole(testEngine(t), c, &config.Admin{Assistant: settings}, f.configs, nil, nil, func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, format)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Shutdown(context.Background()) })
	f.console, f.url = cs, strings.TrimSuffix(cs.URL(), "/")
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != 200 {
		t.Fatal(code)
	}
	code, body, _ := b.do("POST", "/api/m/ai/chat", `{"message": "answer two, and name the route main"}`)
	if code != 200 || strings.Count(body, `"type":"proposal"`) != 2 || !strings.Contains(body, `"reason":"answer"`) {
		t.Fatalf("%d %s", code, body)
	}
	var start struct {
		Conversation string `json:"conversation"`
	}
	_ = json.Unmarshal([]byte(strings.SplitN(strings.TrimPrefix(body, "data: "), "\n", 2)[0]), &start)
	before := f.configs.Current().Number
	code, body, _ = b.do("GET", "/api/m/ai/proposals/"+start.Conversation, "")
	var got struct {
		Proposal struct {
			ID       string            `json:"id"`
			Revision int               `json:"revision"`
			ETag     string            `json:"etag"`
			Valid    bool              `json:"valid"`
			Diff     map[string]string `json:"diff"`
			Changed  []string          `json:"changed"`
		} `json:"proposal"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || !got.Proposal.Valid || got.Proposal.ETag != f.configs.Current().ETag ||
		!strings.Contains(got.Proposal.Diff["conf"], "two") || !strings.Contains(got.Proposal.Diff["yaml"], "+    name: main") || len(got.Proposal.Changed) != 1 {
		t.Fatalf("%d %s", code, body)
	}
	if f.configs.Current().Number != before || f.served(t) != "one" {
		t.Fatal("a proposal was applied before its confirm")
	}

	// bob proposes but cannot apply ada's, nor his own.
	bob := f.browser(t)
	bob.login("bob", "pw")
	apply, _ := json.Marshal(map[string]any{"id": got.Proposal.ID, "revision": got.Proposal.Revision, "etag": got.Proposal.ETag, "user": "ada"})
	if code, body, _ := bob.do("POST", "/api/m/ai/proposals/"+start.Conversation+"/apply", string(apply)); code != 403 {
		t.Errorf("%d %s", code, body)
	}

	code, body, _ = b.do("POST", "/api/m/ai/proposals/"+start.Conversation+"/apply", string(apply))
	if code != 200 || !strings.Contains(body, `"outcome":"applied"`) {
		t.Fatalf("%d %s", code, body)
	}
	if v := f.configs.Current(); v.Number != before+1 || v.Source != SourceConsole || f.served(t) != "two" {
		t.Fatalf("version %d from %s serves %s", v.Number, v.Source, f.served(t))
	}
	code, body, _ = b.do("GET", "/api/m/admin/versions", "")
	if code != 200 || strings.Count(body, `"source":"console"`) != 1 {
		t.Errorf("%d %s", code, body)
	}
	mu.Lock()
	if len(saved) != 1 || !strings.Contains(saved[0], `"name": "main"`) || !strings.Contains(saved[0], `"body": "two"`) {
		t.Errorf("saved: %v", saved)
	}
	mu.Unlock()
	code, body, _ = b.do("GET", "/api/m/ai/audit?kind=apply", "")
	if code != 200 || !strings.Contains(body, `"outcome":"applied"`) || !strings.Contains(body, `"version":`) {
		t.Errorf("%d %s", code, body)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if !strings.Contains(strings.Join(logs, "\n"), "console: %q: %s") {
		t.Errorf("%v", logs)
	}
}
