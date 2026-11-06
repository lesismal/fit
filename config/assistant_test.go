package config

import (
	"strings"
	"testing"
	"time"
)

const assistantConsole = `"console": {"users": [{"username": "ada", "password_hash": "$2a$10$x"}]}`

func loadAssistantConfig(t *testing.T, assistant string) (*Config, error) {
	t.Helper()
	return Load([]byte(`{"admin": {` + assistantConsole + `, "assistant": ` + assistant + `}}`))
}

func TestAssistantLoads(t *testing.T) {
	c, err := loadAssistantConfig(t, `{
  "providers": [
    {"name": "claude", "kind": "anthropic", "models": [{"name": "claude-x", "context": 200000}]},
    {"name": "local", "kind": "openai_compatible", "base_url": "http://127.0.0.1:8000/v1/", "headers": {"X-Org": "fit"},
     "connect_timeout": "1s", "first_byte_timeout": "2s", "idle_timeout": "3s", "timeout": "4s", "retries": 0,
     "models": [{"name": "m", "tools": false}]},
    {"name": "az", "kind": "azure_openai", "base_url": "https://x.openai.azure.com", "api_version": "2024-10-21", "api_key_env": "AZ_KEY"},
    {"name": "mesh", "kind": "llm_proxy"}
  ],
  "default_model": {"provider": "local", "model": "m"},
  "params": {"max_output_tokens": 100, "temperature": 0.5, "thinking": "high"},
  "limits": {"max_turns": 1, "max_tool_calls": 2, "max_tool_result_bytes": 3, "max_conversations": 4, "max_concurrent_chats": 5,
             "max_tokens_per_day": 6, "max_stream_bytes": 7, "max_audit_entries": 8},
  "data": {"send_logs": true, "redact": ["secret-[0-9]+"]},
  "users": [{"username": "ada", "level": "apply"}],
  "default_level": "off"
}`)
	if err != nil {
		t.Fatal(err)
	}
	a := c.Admin.Assistant
	claude, local := a.Provider("claude"), a.Provider("local")
	if a.Provider("none") != nil || claude == nil || local == nil {
		t.Fatal("providers by name")
	}
	if claude.URL() != "https://api.anthropic.com/v1" || local.URL() != "http://127.0.0.1:8000/v1" || a.Provider("mesh").URL() != "" {
		t.Errorf("URLs: %q %q", claude.URL(), local.URL())
	}
	if claude.Connect() != DefaultAssistantConnectTimeout || claude.FirstByte() != DefaultAssistantFirstByteTimeout ||
		claude.Idle() != DefaultAssistantIdleTimeout || claude.Total() != DefaultAssistantTimeout || claude.RetryCount() != DefaultAssistantRetries {
		t.Error("the defaults of a provider's bounds")
	}
	if local.Connect() != time.Second || local.FirstByte() != 2*time.Second || local.Idle() != 3*time.Second || local.Total() != 4*time.Second || local.RetryCount() != 0 {
		t.Error("a provider's own bounds")
	}
	if !claude.Models[0].CallsTools() || local.Models[0].CallsTools() {
		t.Error("tools")
	}
	if p, m := a.Default(); p != "local" || m != "m" {
		t.Errorf("default: %s %s", p, m)
	}
	if a.Level("ada") != AssistantApply || a.Level("bob") != AssistantOff {
		t.Error("levels")
	}
	if a.Params.MaxOutput() != 100 || a.Params.ThinkingLevel() != "high" {
		t.Error("params")
	}
	l := a.Limits
	if l.Turns() != 1 || l.ToolCalls() != 2 || l.ToolResultBytes() != 3 || l.Conversations() != 4 || l.ConcurrentChats() != 5 ||
		l.TokensPerDay() != 6 || l.StreamBytes() != 7 || l.AuditEntries() != 8 {
		t.Error("limits")
	}
}

func TestAssistantDefaults(t *testing.T) {
	a := &Assistant{Providers: []AssistantProvider{{Name: "o", Kind: AssistantOllama}, {Name: "p", Kind: AssistantOpenAI, Models: []AssistantModel{{Name: "gpt"}}}}}
	if p, m := a.Default(); p != "p" || m != "gpt" {
		t.Errorf("default: %s %s", p, m)
	}
	if p, m := (&Assistant{}).Default(); p != "" || m != "" {
		t.Error("no default without models")
	}
	if a.Level("x") != AssistantRead || a.Provider("o").URL() != "http://127.0.0.1:11434" {
		t.Error("level or URL")
	}
	var p *AssistantParams
	var l *AssistantLimits
	if p.MaxOutput() != DefaultAssistantMaxOutputTokens || p.ThinkingLevel() != "off" {
		t.Error("nil params")
	}
	if l.Turns() != DefaultAssistantMaxTurns || l.ToolCalls() != DefaultAssistantMaxToolCalls || l.ToolResultBytes() != DefaultAssistantMaxToolResultBytes ||
		l.Conversations() != DefaultAssistantMaxConversations || l.ConcurrentChats() != DefaultAssistantMaxConcurrentChats ||
		l.TokensPerDay() != DefaultAssistantMaxTokensPerDay || l.StreamBytes() != DefaultAssistantMaxStreamBytes || l.AuditEntries() != DefaultAssistantMaxAuditEntries {
		t.Error("nil limits")
	}
	l = &AssistantLimits{}
	if l.Turns() != DefaultAssistantMaxTurns || l.StreamBytes() != DefaultAssistantMaxStreamBytes {
		t.Error("zero limits")
	}
}

func TestAssistantErrors(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		want      []string
	}{
		{"no console", `{"admin": {"assistant": {}}}`, []string{"admin.assistant: the assistant lives in the console's workbench"}},
		{"bad names", `{"admin": {` + assistantConsole + `, "assistant": {"providers": [{"name": "-x", "kind": "openai"}, {"name": "a", "kind": "openai"}, {"name": "a", "kind": "openai"}]}}}`,
			[]string{"providers[0].name: \"-x\" is not a valid name", "providers[2].name: another provider is already named \"a\""}},
		{"kinds", `{"admin": {` + assistantConsole + `, "assistant": {"providers": [{"name": "a", "kind": "x"}]}}}`,
			[]string{"providers[0].kind: \"x\" is not one of"}},
		{"base urls", `{"admin": {` + assistantConsole + `, "assistant": {"providers": [
			{"name": "a", "kind": "llm_proxy", "base_url": "http://x"},
			{"name": "b", "kind": "openai_compatible"},
			{"name": "c", "kind": "azure_openai", "api_version": "v"},
			{"name": "d", "kind": "openai", "base_url": "ftp://x"},
			{"name": "e", "kind": "openai", "base_url": "https://x/?k=1"},
			{"name": "f", "kind": "openai", "api_version": "v"},
			{"name": "g", "kind": "openai", "api_key_env": "1X"}]}}}`,
			[]string{"providers[0].base_url: an llm_proxy provider is reached", "providers[1].base_url: a openai_compatible provider needs base_url",
				"providers[2].base_url: a azure_openai provider needs base_url", "providers[3].base_url: \"ftp://x\" is not an http or https URL",
				"providers[4].base_url:", "providers[5].api_version: api_version is for azure_openai", "providers[6].api_key_env: \"1X\" is not"}},
		{"azure without api_version", `{"admin": {` + assistantConsole + `, "assistant": {"providers": [{"name": "a", "kind": "azure_openai", "base_url": "https://x"}]}}}`,
			[]string{"providers[0].api_version: api_version is for azure_openai, which needs it"}},
		{"headers", `{"admin": {` + assistantConsole + `, "assistant": {"providers": [{"name": "a", "kind": "openai", "headers": {"Authorization": "x", "bad name": "x", "X-Ok": "a\nb"}}]}}}`,
			[]string{"headers.Authorization: Authorization is the call's own", "\"bad name\" is not a header's name", "headers.X-Ok: the value holds a control character"}},
		{"models", `{"admin": {` + assistantConsole + `, "assistant": {"providers": [{"name": "a", "kind": "openai", "models": [{"name": "m"}, {"name": "m"}, {"name": ""}]}]}}}`,
			[]string{"models[1].name: \"m\" is listed twice", "models[2].name: a model's ID is 1 to 256 printable bytes"}},
		{"default model", `{"admin": {` + assistantConsole + `, "assistant": {"default_model": {"provider": "x", "model": "m"}}}}`,
			[]string{"default_model.provider: no provider is named \"x\""}},
		{"redact", `{"admin": {` + assistantConsole + `, "assistant": {"data": {"redact": ["(", ""]}}}}`,
			[]string{"data.redact[0]: \"(\" is not a regular expression", "data.redact[1]:"}},
		{"users", `{"admin": {` + assistantConsole + `, "assistant": {"users": [{"username": "bob", "level": "read"}, {"username": "ada", "level": "x"}]}}}`,
			[]string{"users[1].level: \"x\" is not one of"}},
		{"not a console user", `{"admin": {` + assistantConsole + `, "assistant": {"users": [{"username": "bob", "level": "read"}]}}}`, []string{"users[0].username: \"bob\" is not a console user"}},
		{"users twice", `{"admin": {` + assistantConsole + `, "assistant": {"users": [{"username": "ada", "level": "read"}, {"username": "ada", "level": "edit"}]}}}`,
			[]string{"users[1].username: \"ada\" is listed twice"}},
		{"bounds", `{"admin": {` + assistantConsole + `, "assistant": {"providers": [{"name": "a", "kind": "openai", "retries": 6}], "params": {"temperature": 3}}}}`,
			[]string{"providers[0].retries", "params.temperature"}},
	} {
		_, err := Load([]byte(tc.doc))
		if err == nil {
			t.Errorf("%s: loaded", tc.name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q lacks %q", tc.name, err, w)
			}
		}
	}
}

func TestAssistantBoundsOfLists(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"providers": [`)
	for i := 0; i <= MaxAssistantProviders; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name": "p` + strings.Repeat("x", i) + `", "kind": "ollama"}`)
	}
	b.WriteString(`], "data": {"redact": [` + strings.TrimSuffix(strings.Repeat(`"a",`, MaxAssistantRedact+1), ",") + `]}}`)
	_, err := LoadAssistant([]byte(b.String()), nil)
	if err == nil || !strings.Contains(err.Error(), "providers, more than") || !strings.Contains(err.Error(), "patterns, more than") {
		t.Errorf("too many: %v", err)
	}
	headers := `{`
	for i := 0; i <= MaxAssistantHeaders; i++ {
		if i > 0 {
			headers += ","
		}
		headers += `"X-H` + strings.Repeat("h", i) + `": "v"`
	}
	models := `[` + strings.TrimSuffix(strings.Repeat(`{"name": "m"},`, MaxAssistantModels+1), ",") + `]`
	_, err = LoadAssistant([]byte(`{"providers": [{"name": "a", "kind": "openai", "headers": `+headers+`}, "models": `+models+`}]}`), nil)
	if err == nil || !strings.Contains(err.Error(), "headers, more than") || !strings.Contains(err.Error(), "models, more than") {
		t.Errorf("too many headers or models: %v", err)
	}
}

func TestLoadAssistant(t *testing.T) {
	console := &Console{Users: []ConsoleUser{{Username: "ada"}}}
	a, err := LoadAssistant([]byte(`{"providers": [{"name": "o", "kind": "ollama"}], "users": [{"username": "ada", "level": "edit"}]}`), console)
	if err != nil || a.Level("ada") != AssistantEdit {
		t.Fatalf("%v %v", a, err)
	}
	if _, err := LoadAssistant([]byte(`{`), console); err == nil {
		t.Error("bad JSON loaded")
	}
	if _, err := LoadAssistant([]byte(`{"x": 1}`), console); err == nil || !strings.Contains(err.Error(), "admin.assistant.x: unknown field") {
		t.Errorf("an unknown field: %v", err)
	}
	if _, err := LoadAssistant([]byte(`{"users": [{"username": "bob", "level": "edit"}]}`), console); err == nil {
		t.Error("a user who is not a console user")
	}
	if _, err := LoadAssistant([]byte(`{"users": [{"username": "bob", "level": "edit"}]}`), nil); err == nil {
		t.Error("settings without a console loaded")
	}
}
