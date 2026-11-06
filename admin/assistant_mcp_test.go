package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

// mcpCall posts a request of MCP's stateless revision to the console's /mcp
// with token, and returns the status and the JSON-RPC response, read from
// the SSE stream when it is one.
func mcpCall(t *testing.T, url, token, method string, params map[string]any) (int, map[string]any) {
	t.Helper()
	params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", url+"/mcp", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	if n, ok := params["name"].(string); ok {
		req.Header.Set("Mcp-Name", n)
	}
	if u, ok := params["uri"].(string); ok {
		req.Header.Set("Mcp-Name", u)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	text := string(b)
	if i := strings.LastIndex(text, "data: "); i >= 0 {
		text = strings.TrimSpace(text[i+6:])
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(text), &out)
	return resp.StatusCode, out
}

// TestConsoleMCP: an outside agent with an MCP token reads the config and
// proposes a change through /mcp on the console's address; the proposal
// shows in the workbench, changes nothing until a user who applies applies
// it there, and then becomes a version as any other; without
// admin.assistant.mcp, /mcp answers 404.
func TestConsoleMCP(t *testing.T) {
	f := &consoleFixture{fixture: start(t, doc("one"), &config.Admin{})}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	settings := &config.Assistant{Users: []config.AssistantUser{{Username: "ada", Level: config.AssistantApply}}, MCP: &config.AssistantMCP{}}
	cs, err := StartConsole(testEngine(t), c, &config.Admin{Assistant: settings}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Shutdown(context.Background()) })
	f.console, f.url = cs, strings.TrimSuffix(cs.URL(), "/")
	b := f.browser(t)
	b.login("ada", "pw")
	code, body, _ := b.do("POST", "/api/m/ai/mcp/tokens", `{"name":"agent","level":"propose","days":7}`)
	var made struct{ Token string }
	if code != 200 || json.Unmarshal([]byte(body), &made) != nil || made.Token == "" {
		t.Fatalf("a token: %d %s", code, body)
	}

	code, res := mcpCall(t, f.url, made.Token, "resources/read", map[string]any{"uri": "fit://config/base"})
	if code != 200 || !strings.Contains(jsonOf(res), `\"one\"`) {
		t.Fatalf("the config: %d %v", code, res)
	}
	before := f.configs.Current().Number
	code, res = mcpCall(t, f.url, made.Token, "tools/call", map[string]any{"name": "config_propose", "arguments": map[string]any{
		"summary": "answer two", "ops": []map[string]any{{"op": "replace", "path": "/routes/0/handler/params/body", "value": "two"}}}})
	if code != 200 || !strings.Contains(jsonOf(res), `\"valid\":true`) || !strings.Contains(jsonOf(res), "#/ai/proposals") {
		t.Fatalf("config_propose: %d %v", code, res)
	}
	if f.configs.Current().Number != before || f.served(t) != "one" {
		t.Fatal("a proposal was applied without the workbench")
	}

	code, body, _ = b.do("GET", "/api/m/ai/proposals", "")
	var list struct {
		Proposals []struct{ Conversation, Title string }
	}
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list.Proposals) != 1 || list.Proposals[0].Title != "MCP: agent" {
		t.Fatalf("the workbench's proposals: %d %s", code, body)
	}
	conv := list.Proposals[0].Conversation
	_, body, _ = b.do("GET", "/api/m/ai/proposals/"+conv, "")
	var got struct {
		Proposal struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
			ETag     string `json:"etag"`
		} `json:"proposal"`
	}
	_ = json.Unmarshal([]byte(body), &got)
	apply, _ := json.Marshal(map[string]any{"id": got.Proposal.ID, "revision": got.Proposal.Revision, "etag": got.Proposal.ETag})
	code, body, _ = b.do("POST", "/api/m/ai/proposals/"+conv+"/apply", string(apply))
	if code != 200 || !strings.Contains(body, `"outcome":"applied"`) {
		t.Fatalf("apply: %d %s", code, body)
	}
	if v := f.configs.Current(); v.Number != before+1 || f.served(t) != "two" {
		t.Fatalf("version %d serves %s", v.Number, f.served(t))
	}
	_, body, _ = b.do("GET", "/api/m/ai/audit", "")
	if !strings.Contains(body, `"source":"mcp"`) || !strings.Contains(body, `"token":"agent"`) {
		t.Errorf("the audit log: %s", body)
	}

	// Without mcp, /mcp answers nothing.
	g := &consoleFixture{fixture: start(t, doc("one"), &config.Admin{})}
	cs2, err := StartConsole(testEngine(t), c, &config.Admin{Assistant: &config.Assistant{}}, g.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs2.Shutdown(context.Background()) })
	if code, _ := mcpCall(t, strings.TrimSuffix(cs2.URL(), "/"), made.Token, "tools/list", map[string]any{}); code != 404 {
		t.Errorf("/mcp without mcp: %d", code)
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
