package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

// TestConsoleAssistantMediaFile: on a real library whose channel names an
// item, the assistant reads the library and proposes deleting that item;
// the step and the proposal's view name the channel; nothing is deleted
// until the confirm, and then the item's files go, through the Admin API's
// media endpoint, forced as the person was warned, audited as a media
// change of theirs beside the apply.
func TestConsoleAssistantMediaFile(t *testing.T) {
	ff := startFiles(t, 1<<20)
	f := &consoleFixture{fixture: ff.fixture}
	var mu sync.Mutex
	var reqs []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, string(body))
		n := len(reqs)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[`+
				`{"index":0,"id":"c1","type":"function","function":{"name":"media_item_get","arguments":"{\"library\":\"films\",\"item\":\"clips/late\"}"}},`+
				`{"index":1,"id":"c2","type":"function","function":{"name":"media_file_propose","arguments":"{\"summary\":\"Delete the late clip\",\"library\":\"films\",\"item\":\"clips/late\",\"op\":\"delete\"}"}}]},"finish_reason":"tool_calls"}]}`+
				"\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"The channel tv/late names it."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()

	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	settings := &config.Assistant{Providers: []config.AssistantProvider{{Name: "m", Kind: config.AssistantOpenAICompatible, BaseURL: model.URL + "/v1",
		Models: []config.AssistantModel{{Name: "x"}}}}, Users: []config.AssistantUser{{Username: "ada", Level: config.AssistantApply}}}
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
	code, body, _ := b.do("POST", "/api/m/ai/chat", `{"message": "delete the late clip"}`)
	if code != 200 || !strings.Contains(body, `"type":"proposal"`) || !strings.Contains(body, `the channel tv/late names it in its list or schedule`) ||
		!strings.Contains(body, `"reason":"answer"`) {
		t.Fatalf("%d %s", code, body)
	}
	var start struct {
		Conversation string `json:"conversation"`
	}
	_ = json.Unmarshal([]byte(strings.SplitN(strings.TrimPrefix(body, "data: "), "\n", 2)[0]), &start)
	file := filepath.Join(ff.root, "clips", "late.mp4")
	if _, err := os.Stat(file); err != nil {
		t.Fatal("proposing deleted the file")
	}

	code, body, _ = b.do("GET", "/api/m/ai/proposals/"+start.Conversation, "")
	var got struct {
		Proposal struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
			ETag     string `json:"etag"`
			Valid    bool   `json:"valid"`
			MediaTag string `json:"media_tag"`
			Media    []struct {
				Warning string `json:"warning"`
				Usage   struct {
					Channels []string `json:"channels"`
				} `json:"usage"`
			} `json:"media"`
		} `json:"proposal"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || !got.Proposal.Valid || len(got.Proposal.Media) != 1 ||
		!strings.Contains(got.Proposal.Media[0].Warning, "tv/late") || got.Proposal.MediaTag == "" {
		t.Fatalf("%d %s", code, body)
	}
	apply, _ := json.Marshal(map[string]any{"id": got.Proposal.ID, "revision": got.Proposal.Revision, "etag": got.Proposal.ETag, "media_tag": got.Proposal.MediaTag})
	code, body, _ = b.do("POST", "/api/m/ai/proposals/"+start.Conversation+"/apply", string(apply))
	if code != 200 || !strings.Contains(body, `"outcome":"applied"`) || !strings.Contains(body, `"op":"delete"`) {
		t.Fatalf("%d %s", code, body)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the file is still there: %v", err)
	}
	code, body, _ = b.do("GET", "/api/m/ai/audit", "")
	if code != 200 || !strings.Contains(body, `"kind":"media"`) || !strings.Contains(body, `"tool":"media.delete"`) || !strings.Contains(body, `\"force\":true`) ||
		!strings.Contains(body, `"kind":"apply"`) || strings.Contains(body, `"user":"admin-api"`) {
		t.Errorf("%d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 || !strings.Contains(reqs[1], "tv/late") {
		t.Errorf("%d requests", len(reqs))
	}
}
