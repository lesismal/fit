package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
)

// TestProposeUnconverted: config_propose's unconverted items, what a
// migration did not carry over, are kept with the step and shown with the
// proposal, in order, each once and redacted; too many, or an empty or
// long one, refuse the step.
func TestProposeUnconverted(t *testing.T) {
	route := `{"op": "add", "path": "/routes/-", "value": {"name": "site", "handler": {"module": "respond"}}}`
	many := make([]string, maxUnconverted+1)
	for i := range many {
		many[i] = "x"
	}
	tooMany, _ := json.Marshal(many)
	f := startProposals(t, `"default_level": "propose"`,
		fakeapi.Turn{Calls: []fakeapi.Call{
			call("p1", ToolConfigPropose, `{"summary": "The site", "ops": [`+route+`], "unconverted": ["line 3: sub_filter: no body rewriting", " line 9: password=hunter22 in auth "]}`),
			call("p2", ToolConfigPropose, `{"summary": "Its limit", "ops": [{"op": "add", "path": "/routes/1/middleware", "value": [{"module": "rate_limit", "params": {"rate": 5, "key": "header:X-Client"}}]}], "unconverted": ["line 3: sub_filter: no body rewriting", "line 12: error_page: none"]}`),
			call("p3", ToolConfigPropose, `{"summary": "Too many", "ops": [`+route+`], "unconverted": `+string(tooMany)+`}`),
			call("p4", ToolConfigPropose, `{"summary": "Empty", "ops": [`+route+`], "unconverted": ["  "]}`),
			call("p5", ToolConfigPropose, `{"summary": "Long", "ops": [`+route+`], "unconverted": ["`+strings.Repeat("y", maxUnconvertedBytes+1)+`"]}`),
			call("p6", ToolPurgePropose, `{"summary": "Purge", "zone": "main", "all": true}`)}},
		fakeapi.Turn{Text: "Proposed."})
	s := f.chat(`{"message": "move my nginx config"}`)
	if s.end == nil {
		t.Fatal(s.err)
	}
	res := s.of(EventToolResult)
	if res[0].Error || res[1].Error || res[5].Error {
		t.Fatalf("%+v", res)
	}
	for i, want := range map[int]string{2: "at most 100 items", 3: "1 to 500 bytes", 4: "1 to 500 bytes"} {
		if !res[i].Error || !strings.Contains(res[i].Content, want) {
			t.Errorf("%d: %+v", i, res[i])
		}
	}
	_, v, body := f.view(s.end.Conversation)
	if v == nil || !v.Valid || len(v.Steps) != 3 {
		t.Fatalf("%s", body)
	}
	want := []string{"line 3: sub_filter: no body rewriting", "line 9: password=[redacted] in auth", "line 12: error_page: none"}
	if strings.Join(v.Unconverted, "|") != strings.Join(want, "|") {
		t.Errorf("%q", v.Unconverted)
	}
	// A rate limit's key is a selector, written and shown as it is.
	if !strings.Contains(v.Diff["json"], `"key": "header:X-Client"`) {
		t.Errorf("%s", v.Diff["json"])
	}
	if got := unconverted([]ProposalStep{{Tool: ToolConfigPropose, Args: json.RawMessage(`{"unconverted": 1}`)}, {Tool: ToolPurgePropose, Args: json.RawMessage(`{"unconverted": ["z"]}`)}}); got != nil {
		t.Errorf("%q", got)
	}
}

// TestSelectorNotSecret: rate_limit's key is a selector, not a secret,
// though a member named key holds one elsewhere: it is read as it is, and
// written; anything else under key is still a secret.
func TestSelectorNotSecret(t *testing.T) {
	for _, c := range []struct {
		name, v string
		want    bool
	}{
		{"key", "ip", true}, {"key", "route", true}, {"key", "header:X-Api-Key", true},
		{"key", "header:", false}, {"key", "header:a b", false}, {"key", "-----BEGIN EC PRIVATE KEY-----", false},
		{"hash_key", "ip", false}, {"token", "route", false},
	} {
		if got := selectorValue(c.name, c.v); got != c.want {
			t.Errorf("%s=%q: %v", c.name, c.v, got)
		}
	}
	r := newRedactor(nil, nil)
	got, err := r.redactJSON([]byte(`{"middleware":[{"params":{"key":"ip","b":{"key":"header:X-User"}}}],"tls":{"key":"pem-of-it"},"keys":["ip"]}`), nil)
	if err != nil || string(got) != `{"middleware":[{"params":{"key":"ip","b":{"key":"header:X-User"}}}],"tls":{"key":"[redacted]"},"keys":["[redacted]"]}` {
		t.Errorf("%s %v", got, err)
	}

	// Written: a new rate limit by key, but no new secret under key.
	f := startProposals(t, `"default_level": "propose"`,
		fakeapi.Turn{Calls: []fakeapi.Call{
			call("p1", ToolConfigPropose, `{"summary": "Limit", "ops": [{"op": "add", "path": "/routes/0/middleware/-", "value": {"module": "rate_limit", "params": {"rate": 5, "key": "route"}}}]}`),
			call("p2", ToolConfigPropose, `{"summary": "Key", "ops": [{"op": "add", "path": "/routes/0/middleware/-", "value": {"module": "headers", "params": {"key": "s3cr3t-value"}}}]}`)}},
		fakeapi.Turn{Text: "ok"})
	s := f.chat(`{"message": "limit /api"}`)
	if res := s.of(EventToolResult); len(res) != 2 || res[0].Error || !res[1].Error || !strings.Contains(res[1].Content, "holds a secret") {
		t.Errorf("%+v", res)
	}
}

// TestConfigGetAbsent: a member asked for that the config does not set is
// null in JSON, and said in a comment in .conf and YAML; not an error.
func TestConfigGetAbsent(t *testing.T) {
	a := toolsAssistant(t, loadConfig(t, `{"providers": [{"name": "p", "kind": "openai"}]}`))
	if out := mustRun(t, a, "config_get", `{"members": ["upstreams", "acme", "acme"]}`); !strings.HasPrefix(out, `{"upstreams":[`) || !strings.HasSuffix(out, `],"acme":null}`) {
		t.Error(out)
	}
	if out := mustRun(t, a, "config_get", `{"members": ["acme"]}`); out != `{"acme":null}` {
		t.Error(out)
	}
	if out := mustRun(t, a, "config_get", `{"members": ["acme", "cache"], "format": "conf"}`); !strings.HasPrefix(out, "conf of {}") || !strings.HasSuffix(out, "\n# The config sets no acme, cache.\n") {
		t.Error(out)
	}
	if out := mustRun(t, a, "config_get", `{"members": ["routes"], "format": "yaml"}`); strings.Contains(out, "sets no") {
		t.Error(out)
	}
}
