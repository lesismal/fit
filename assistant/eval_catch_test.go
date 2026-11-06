package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/certtest"
)

// evalCopy copies the set's case name (set/case) to a directory of its own,
// with base.json, and returns it with its recorded answers changed by edit.
func evalCopy(t *testing.T, name string, edit func(*evalRecord)) *evalCase {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(evalDir, name)
	dst := filepath.Join(dir, name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	base, _ := os.ReadFile(filepath.Join(evalDir, "base.json"))
	_ = os.WriteFile(filepath.Join(dir, "base.json"), base, 0o644)
	files, _ := os.ReadDir(src)
	for _, f := range files {
		data, _ := os.ReadFile(filepath.Join(src, f.Name()))
		if f.Name() == "recorded.json" && edit != nil {
			var rec evalRecord
			if err := json.Unmarshal(data, &rec); err != nil {
				t.Fatal(err)
			}
			edit(&rec)
			data, _ = json.Marshal(rec)
		}
		if err := os.WriteFile(filepath.Join(dst, f.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, _, _ := strings.Cut(name, "/")
	c := &evalCase{Name: name, Set: set, Skill: evalSkills[set], dir: dst}
	data, _ := os.ReadFile(filepath.Join(dst, "case.json"))
	if err := json.Unmarshal(data, c); err != nil {
		t.Fatal(err)
	}
	return c
}

// proposeArgs returns the arguments of the record's config_propose call.
func proposeArgs(t *testing.T, rec *evalRecord) (map[string]any, *evalCall) {
	t.Helper()
	for i := range rec.Turns {
		for j := range rec.Turns[i].Calls {
			if c := &rec.Turns[i].Calls[j]; c.Name == ToolConfigPropose {
				var args map[string]any
				_ = json.Unmarshal(c.Args, &args)
				return args, c
			}
		}
	}
	t.Fatal("no config_propose in the record")
	return nil, nil
}

// TestAssistantEvalCatches: the evaluation fails what it should: a
// migration whose proposal drops a route's restriction and names nothing
// as not converted, one that makes no proposal, one whose proposal does not
// check, and a review that misses a planted problem or gives no fix.
func TestAssistantEvalCatches(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "eval.example")
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, edit func(*evalRecord)) *evalResult {
		t.Helper()
		return runEvalCase(t, nil, evalCopy(t, name, edit), cert)
	}
	has := func(r *evalResult, want ...string) {
		t.Helper()
		all := strings.Join(r.Problems, "\n")
		for _, w := range want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: no problem says %q:\n%s", r.Case, w, all)
			}
		}
	}

	// The office-only route left open to all, and no item named.
	r := run("migrate/nginx-shop", func(rec *evalRecord) {
		args, c := proposeArgs(t, rec)
		members := args["members"].(map[string]any)
		var routes []any
		for _, x := range members["routes"].([]any) {
			if m := x.(map[string]any); m["name"] != "shop.example.com-admin-others" {
				delete(m["match"].(map[string]any), "remote_ip")
				routes = append(routes, m)
			}
		}
		members["routes"] = routes
		delete(args, "unconverted")
		c.Args, _ = json.Marshal(args)
	})
	if r.Passed() || !r.Applied || r.RoutesKept != r.Routes-1 || r.Named != 0 {
		t.Errorf("%+v", r)
	}
	has(r, `/admin/users on :443 from 203.0.113.9: route "shop.example.com-admin", handler proxy, not refused`,
		`does not name "sub_filter"`, `does not name "error_page"`)

	// The upstream of the API pointed elsewhere, and the redirect lost.
	r = run("migrate/nginx-shop", func(rec *evalRecord) {
		args, c := proposeArgs(t, rec)
		members := args["members"].(map[string]any)
		members["upstreams"].([]any)[0].(map[string]any)["addresses"] = []string{"10.9.9.9:80"}
		for _, x := range members["routes"].([]any) {
			if m := x.(map[string]any); m["name"] == "shop.example.com" {
				delete(m, "middleware")
			}
		}
		c.Args, _ = json.Marshal(args)
	})
	has(r, "to [10.9.9.9:80], not [10.0.1.10:8080 10.0.1.11:8080]", "not a redirect to https://")

	// No proposal: the answer alone.
	r = run("migrate/traefik-labels", func(rec *evalRecord) {
		rec.Turns = rec.Turns[len(rec.Turns)-1:]
	})
	has(r, "no proposal")

	// A proposal that does not check is not applied.
	r = run("migrate/traefik-labels", func(rec *evalRecord) {
		args, c := proposeArgs(t, rec)
		args["members"].(map[string]any)["upstreams"].([]any)[0].(map[string]any)["addresses"] = []string{}
		c.Args, _ = json.Marshal(args)
	})
	if r.Applied || r.Valid {
		t.Errorf("%+v", r)
	}
	has(r, "the proposal does not check")

	// A review that misses one problem, and gives another no fix.
	r = run("review/tls-and-limits", func(rec *evalRecord) {
		last := &rec.Turns[len(rec.Turns)-1]
		var keep []string
		for _, l := range strings.Split(last.Text, "\n") {
			switch {
			case strings.Contains(l, "`routes[3]`"):
			case strings.Contains(l, "`upstreams[1]`"):
				keep = append(keep, l[:strings.Index(l, "Fix:")])
			default:
				keep = append(keep, l)
			}
		}
		last.Text = strings.Join(keep, "\n")
	})
	if r.Found != r.Planted-2 {
		t.Errorf("%+v", r)
	}
	has(r, "no finding with a fix names browse-and-dotfiles", "no finding with a fix names long-timeouts")

	// A review with no findings at all, and a proposal that does not check.
	r = run("review/exposed", func(rec *evalRecord) {
		rec.Turns = []evalTurn{
			{Calls: []evalCall{{Name: ToolConfigPropose, Args: json.RawMessage(`{"summary": "Drop the routes", "members": {"routes": [{"name": "x"}]}}`)}}},
			{Text: "Nothing to report."}}
	})
	if r.Found != 0 || r.Findings != 0 || !r.Proposal || r.Valid {
		t.Errorf("%+v", r)
	}
	has(r, "no finding with a fix names console-everywhere", "the proposal does not check")
}

// TestAssistantEvalRecord: a run's answers are written back as the case's
// recorded.json, its source as "{{source}}", and replay the same.
func TestAssistantEvalRecord(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "eval.example")
	if err != nil {
		t.Fatal(err)
	}
	c := evalCopy(t, "migrate/nginx-docs", nil)
	want, _ := os.ReadFile(filepath.Join(c.dir, "recorded.json"))
	var before evalRecord
	_ = json.Unmarshal(want, &before)

	sc := &script{turns: c.turns(t)}
	f := startChat(t, evalExtra, sc.next).fixture
	body, _ := json.Marshal(map[string]any{"message": c.message(t), "skill": c.Skill, "inputs": map[string]string{"from": c.From}})
	s := f.chat(string(body))
	if s.end == nil {
		t.Fatal(s.err)
	}
	recordEval(t, f, c, &evalModel{Kind: "openai", Model: "gpt-x"}, s.end.Conversation)
	got, _ := os.ReadFile(filepath.Join(c.dir, "recorded.json"))
	var after evalRecord
	if err := json.Unmarshal(got, &after); err != nil {
		t.Fatal(err)
	}
	if after.Model != "openai/gpt-x" || len(after.Turns) != len(before.Turns) || !strings.Contains(string(got), `"text": "{{source}}"`) {
		t.Errorf("%s", got)
	}
	if r := runEvalCase(t, nil, c, cert); !r.Passed() {
		t.Errorf("the record does not replay: %+v", r)
	}
}

// TestAssistantEvalSettings: the settings made for a model in the
// environment load, for each kind, and name it.
func TestAssistantEvalSettings(t *testing.T) {
	t.Setenv("FIT_EVAL_KIND", "")
	if m := evalModelFromEnv(t); m != nil || m.String() != "the recorded answers" {
		t.Error(m)
	}
	t.Setenv("FIT_EVAL_KIND", "azure_openai")
	t.Setenv("FIT_EVAL_MODEL", "gpt-4.1")
	t.Setenv("FIT_EVAL_BASE_URL", "https://example.openai.azure.com")
	t.Setenv("FIT_EVAL_API_VERSION", "2025-04-01-preview")
	t.Setenv("FIT_EVAL_CONTEXT", "128000")
	t.Setenv("FIT_EVAL_RECORD", "1")
	m := evalModelFromEnv(t)
	if m == nil || m.Context != 128000 || !m.Record || m.String() != "azure_openai/gpt-4.1" {
		t.Fatalf("%+v", m)
	}
	cfg := loadConfig(t, m.settings(evalExtra))
	s := cfg.Admin.Assistant
	if p := s.Provider("eval"); p == nil || p.Kind != "azure_openai" || p.APIVersion != "2025-04-01-preview" || p.APIKeyEnv != "FIT_EVAL_API_KEY" ||
		s.DefaultModel.Model != "gpt-4.1" || s.Limits.MaxToolCalls != 40 {
		t.Errorf("%+v", s)
	}
	for _, kind := range []string{"anthropic", "openai", "openai_responses", "gemini", "ollama"} {
		loadConfig(t, (&evalModel{Kind: kind, Model: "m"}).settings(evalExtra))
	}
}
