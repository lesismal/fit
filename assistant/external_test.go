package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestCallTool: an outside caller's call is checked against its level and
// what the registry serves outside, its arguments must be an object, and a
// propose call keeps its step in a conversation of the user's, made when
// there is none and refused while the panel answers in it.
func TestCallTool(t *testing.T) {
	f := startWith(t, loadConfig(t, `{"users": [{"username": "ada", "level": "apply"}]}`), &memStore{}, func(o *Options) { o.API = fakeAdmin() })
	x := External{User: "ada", Level: config.AssistantRead, Source: SourceMCP, Token: "t"}
	ctx := context.Background()
	for name, c := range map[string]struct {
		tool, args string
		x          External
		want       string
	}{
		"unknown":       {"nothing", `{}`, x, "there is no tool"},
		"not served":    {"ask_user", `{}`, x, "there is no tool"},
		"above level":   {ToolConfigPropose, `{}`, x, "needs the level propose"},
		"not an object": {"status_get", `[1]`, x, "not a JSON object"},
		"tool error":    {"versions_diff", `{"from": 9}`, x, "error:"},
	} {
		res := f.a.CallTool(ctx, c.x, c.tool, json.RawMessage(c.args))
		if !res.Error || !strings.Contains(res.Text, c.want) {
			t.Errorf("%s: %+v", name, res)
		}
	}
	if res := f.a.CallTool(ctx, x, "status_get", nil); res.Error {
		t.Errorf("no arguments: %+v", res)
	}

	// A propose call without a conversation makes one, titled MCP when
	// the caller names none.
	p := External{User: "ada", Level: config.AssistantPropose, Source: SourceMCP, Token: "t"}
	args := json.RawMessage(`{"summary": "x", "members": {"routes": []}}`)
	res := f.a.CallTool(ctx, p, ToolConfigPropose, args)
	if res.Conversation == "" {
		t.Fatalf("no conversation: %+v", res)
	}
	f.a.chats.mu.Lock()
	conv := f.a.userLocked("ada").conversation(res.Conversation)
	title := conv.Title
	conv.busy = &chat{}
	f.a.chats.mu.Unlock()
	if title != "MCP" {
		t.Errorf("the title: %q", title)
	}
	p.Conversation = res.Conversation
	if r := f.a.CallTool(ctx, p, ToolConfigPropose, args); !r.Error || !strings.Contains(r.Text, "answering in the workbench") {
		t.Errorf("a busy conversation: %+v", r)
	}
	f.a.chats.mu.Lock()
	f.a.userLocked("ada").conversation(res.Conversation).busy = nil
	f.a.chats.mu.Unlock()
}

// TestReadResource: resources are read at the level read or above, an
// answer of the Admin API that fails says so, and a version that is not
// JSON is refused.
func TestReadResource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /versions/{n}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("n") == "5" {
			_, _ = w.Write([]byte("not json"))
			return
		}
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"the disk"}`))
	})
	f := startWith(t, loadConfig(t, `{}`), nil, func(o *Options) { o.API = mux })
	ctx := context.Background()
	if _, _, err := f.a.ReadResource(ctx, External{User: "ada", Level: config.AssistantOff}, "fit://status"); err == nil || !strings.Contains(err.Error(), "reads nothing") {
		t.Errorf("level off: %v", err)
	}
	x := External{User: "ada", Level: config.AssistantRead}
	if _, _, err := f.a.ReadResource(ctx, x, "fit://versions/4"); err == nil || errors.Is(err, ErrNoResource) || !strings.Contains(err.Error(), "the disk") {
		t.Errorf("a failing API: %v", err)
	}
	if _, _, err := f.a.ReadResource(ctx, x, "fit://versions/5"); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("a version not JSON: %v", err)
	}
	if len(f.a.Resources()) == 0 || len(f.a.ResourceTemplates()) == 0 {
		t.Error("no resources")
	}
}
