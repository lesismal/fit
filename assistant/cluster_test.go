package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/config"
)

// memberSecret is a member's credential as a manager keeps it, which a
// fake cluster's answers carry where a careless manager would, and no
// request to a model may.
const memberSecret = "pa55-w0rd-of-the-member"

// fakeCluster is a manager as the cluster tools reach it: canned answers,
// each call recorded with its user, a member's Admin API served by api,
// and refusals for the user "opal", an operator who may not.
type fakeCluster struct {
	api http.Handler

	mu    sync.Mutex
	calls []string
	plan  []byte
	slow  atomic.Bool
}

var errFakeForbidden = errors.New(`"opal" may not do this: it needs the role admin cluster-wide`)

func (fc *fakeCluster) record(format string, args ...any) {
	fc.mu.Lock()
	fc.calls = append(fc.calls, fmt.Sprintf(format, args...))
	fc.mu.Unlock()
}

func (fc *fakeCluster) called() string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return strings.Join(fc.calls, "\n")
}

func (fc *fakeCluster) Members(user, group, selector, status string) ([]byte, error) {
	fc.record("members %s %s %s %s", user, group, selector, status)
	return []byte(`{"members":[{"name":"m1","status":"up","groups":["edge"],"credentials":{"password":"` + memberSecret + `"}},{"name":"m2","status":"down"}],"by_status":{"up":1,"down":1}}`), nil
}

func (fc *fakeCluster) Templates(user, name string) ([]byte, error) {
	fc.record("templates %s %s", user, name)
	if name == "nope" {
		return nil, errors.New(`no template is named "nope"`)
	}
	return []byte(`{"template":{"name":"api","parts":["routes[api]"]},"config":{"routes":[{"name":"api","api_key":"[secret]"}]}}`), nil
}

func (fc *fakeCluster) Rollouts(user, id string) ([]byte, error) {
	fc.record("rollouts %s %s", user, id)
	if id == "bad" {
		return []byte("not json"), nil
	}
	return []byte(`{"rollouts":[{"id":"r1","state":"failed","batches":[{"index":1,"state":"failed"}]}],"operations":[]}`), nil
}

func (fc *fakeCluster) Metrics(ctx context.Context, user, group string, nodes []string) ([]byte, error) {
	fc.record("metrics %s %s %v", user, group, nodes)
	if fc.slow.Load() {
		<-ctx.Done()
		return []byte(`{"members":[{"node":"m2","error":{"kind":"timeout"}}]}`), nil
	}
	return []byte(`{"members":[{"node":"m1","requests":10}],"by_group":{"edge":{"members":1}}}`), nil
}

func (fc *fakeCluster) Certs(ctx context.Context, user, group string, nodes []string, within int) ([]byte, error) {
	fc.record("certs %s %s %v %d", user, group, nodes, within)
	return []byte(`{"certificates":[{"node":"m1","names":["a.example"],"days_left":3}]}`), nil
}

func (fc *fakeCluster) Logs(ctx context.Context, user, group string, nodes []string, logger, level, contains string, limit int) ([]byte, error) {
	fc.record("logs %s %s %v %s %s %s %d", user, group, nodes, logger, level, contains, limit)
	return []byte(`{"records":[{"node":"m1","seq":3,"level":"error","logger":"server","message":"dial: refused, Authorization: Bearer abcdefghijklmnop1234"}],"nodes":[{"name":"m1","count":1}]}`), nil
}

func (fc *fakeCluster) PlanRollout(ctx context.Context, user string, req []byte) ([]byte, error) {
	fc.record("plan %s", user)
	fc.mu.Lock()
	fc.plan = append([]byte(nil), req...)
	fc.mu.Unlock()
	if user == "opal" {
		return nil, errFakeForbidden
	}
	return []byte(`{"link":"#/cluster/rollout/r9","id":"r9","state":"planned","members":[{"node":"m1","status":"ready","changes":[{"path":"log"}]}]}`), nil
}

func (fc *fakeCluster) CheckOp(user string, req []byte) ([]byte, error) {
	fc.record("op %s %s", user, req)
	return []byte(`{"op":"reload","members":["m1"],"link":"#/cluster/ops?op=reload&members=m1"}`), nil
}

func (fc *fakeCluster) FixDrift(ctx context.Context, user string, nodes []string) ([]byte, error) {
	fc.record("fix %s %v", user, nodes)
	return []byte(`{"link":"#/cluster/rollout/r10","rollout":{"id":"r10"}}`), nil
}

func (fc *fakeCluster) MemberCall(ctx context.Context, user, member, method, path string, body []byte) (int, http.Header, []byte, string, error) {
	fc.record("member %s %s %s %s", user, member, method, path)
	switch member {
	case "down":
		return 0, nil, nil, "", errors.New(`the member "down": unreachable: connection refused`)
	case "slow":
		<-ctx.Done()
		return 0, nil, nil, "", ctx.Err()
	case "bare":
		if path == "/node" {
			return 200, nil, []byte(`[]`), "", nil
		}
		return 200, nil, []byte(`{}`), "", nil
	}
	if path == "/node" {
		return 200, http.Header{}, []byte(`{"roles":["llm.hub"],"uptime_seconds":42}`), "", nil
	}
	note := ""
	if member == "old" {
		note = "the member's config schema is not this manager's"
	}
	req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	fc.api.ServeHTTP(w, req)
	return w.Code, w.Header(), w.Body.Bytes(), note, nil
}

// withCluster sets fc as the manager, and ada's level capped at cap when
// not "".
func withCluster(fc *fakeCluster, cap string) func(*Options) {
	return func(o *Options) {
		o.Cluster = fc
		if cap != "" {
			o.LevelCap = func(string) string { return cap }
		}
	}
}

// startCluster starts an assistant on a manager fc, with settings extra,
// whose model answers turns.
func startCluster(t *testing.T, extra, cap string, turns ...fakeapi.Turn) (*propFixture, *fakeCluster) {
	t.Helper()
	fc := &fakeCluster{api: fakeAdmin()}
	cfgs := newFakeConfigs(propBase)
	sc := &script{turns: turns}
	f := startChat(t, extra, sc.next, func(o *Options) { o.API = cfgs.handler() }, withCluster(fc, cap))
	return &propFixture{chatFixture: f, cfgs: cfgs, sc: sc}, fc
}

// TestClusterToolsRead: on a manager, the cluster tools read it as the
// user, and the read tools read a member through it; what reaches the
// model is redacted, and carries no member's credential.
func TestClusterToolsRead(t *testing.T) {
	turns := []fakeapi.Turn{
		{Calls: []fakeapi.Call{
			call("c1", "cluster_members", `{"group": "edge", "selector": "role=edge", "status": "up"}`),
			call("c2", "cluster_templates", `{"template": "api"}`),
			call("c3", "cluster_rollouts", `{"rollout": "r1"}`),
			call("c4", "cluster_metrics", `{"group": "edge"}`),
			call("c5", "cluster_certs", `{"nodes": ["m1"], "within_days": 30}`),
			call("m1", "config_get", `{"member": "m1", "members": ["routes"]}`),
			call("m2", "config_get", `{"member": "m1", "format": "conf"}`),
			call("m3", "status_get", `{"member": "m1"}`),
			call("m4", "upstream_health", `{"member": "old"}`),
			call("m5", "certs_list", `{"member": "m1"}`),
			call("m6", "metrics_query", `{"member": "old", "name": "requests"}`),
			call("m7", "cache_zones", `{"member": "down"}`),
			call("m8", "consistency_check", `{}`),
			call("m9", "status_get", `{"member": "bare"}`),
			call("ma", "cluster_log_tail", `{"nodes": ["m1"], "level": "warn", "contains": "dial", "limit": 5}`),
		}},
		{Text: "m2 is down."},
	}
	f, fc := startCluster(t, "", "", turns...)
	s := f.chat(`{"message": "how is the fleet?"}`)
	if s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	res := s.of(EventToolResult)
	if len(res) != 15 {
		t.Fatalf("%+v", res)
	}
	want := [][]string{
		{`"name":"m1"`, `"password":"[redacted]"`, `"by_status"`},
		{`"api_key":"[redacted]"`, `"routes[api]"`},
		{`"state":"failed"`},
		{`"requests":10`},
		{`"days_left":3`},
		{`"member":"m1"`, `"result":{"routes":[`, `"secret":"[redacted]"`},
		{"member m1:\nconf of"},
		{`"member":"m1"`, `"roles":["llm.hub"]`, `"uptime_seconds":42`, `"listeners"`},
		{`"member":"old"`, `"note":"the member's config schema is not this manager's"`, `"upstreams"`},
		{`"member":"m1"`, `"certificates"`},
		{"member old (the member's config schema is not this manager's):\n", "fit_http_requests_total"},
		{`the member "down": unreachable`},
		{`"differences"`},
		{`the answer is not what it should be`},
		{`"message":"dial: refused, Authorization: [redacted]`},
	}
	for i, w := range want {
		for _, x := range w {
			if !strings.Contains(res[i].Content, x) {
				t.Errorf("%s lacks %s: %s", res[i].Name, x, res[i].Content)
			}
		}
	}
	for i, r := range res {
		if r.Error != (i == 11 || i == 13) {
			t.Errorf("%s: %+v", r.Name, r)
		}
	}
	got := fc.called()
	for _, w := range []string{"members ada edge role=edge up", "templates ada api", "rollouts ada r1", "metrics ada edge []", "certs ada  [m1] 30",
		"member ada m1 GET /config", "member ada m1 POST /config/render?format=conf", "member ada m1 GET /status", "member ada m1 GET /node",
		"member ada old GET /status", "member ada down GET /cache", "logs ada  [m1]  warn dial 5"} {
		if !strings.Contains(got, w) {
			t.Errorf("the manager was not asked %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "member ada m8") {
		t.Error("a call without member read a member")
	}
	reqs := f.model.Requests()
	noSecrets(t, reqs)
	for i, r := range reqs {
		if strings.Contains(string(r.Raw), memberSecret) {
			t.Errorf("request %d carries a member's credential", i)
		}
	}
	if sys := fakeapi.System(reqs[0]); !strings.Contains(sys, "manager of other fits") || !strings.Contains(sys, ToolRolloutPropose) {
		t.Error(sys)
	}
	// The Functions page lists the cluster tools, and member on the read
	// tools that read the Admin API.
	_, body := f.do("GET", "/api/m/ai/tools", "")
	var tl struct {
		Tools []struct {
			Name, Group string
			Parameters  json.RawMessage
		}
	}
	_ = json.Unmarshal([]byte(body), &tl)
	member := map[string]bool{}
	group := map[string]string{}
	for _, x := range tl.Tools {
		member[x.Name] = strings.Contains(string(x.Parameters), `"member"`)
		group[x.Name] = x.Group
	}
	for _, n := range memberTools {
		if !member[n] {
			t.Errorf("%s takes no member", n)
		}
	}
	if member["log_tail"] || member["schema_describe"] || group["cluster_members"] != GroupCluster || group[ToolRolloutPropose] != GroupChange {
		t.Errorf("%v %v", member, group)
	}
	// The cluster's skills are offered.
	for _, n := range clusterSkills {
		if f.a.EnabledSkill(n) == nil {
			t.Errorf("no skill %s", n)
		}
	}
}

// TestClusterReadBounds: a read across members ends with its wait, and a
// member's read with its own, whatever the manager does.
func TestClusterReadBounds(t *testing.T) {
	f, fc := startCluster(t, "", "")
	old, oldMember := clusterReadWait, memberReadWait
	clusterReadWait, memberReadWait = 100*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { clusterReadWait, memberReadWait = old, oldMember })
	fc.slow.Store(true)
	start := time.Now()
	r := f.tool("", "cluster_metrics", `{}`)
	if r.Error || !strings.Contains(r.Text, `"kind":"timeout"`) || time.Since(start) > 5*time.Second {
		t.Fatalf("%+v", r)
	}
	r = f.tool("", "status_get", `{"member": "slow"}`)
	if !r.Error || !strings.Contains(r.Text, "deadline exceeded") {
		t.Fatalf("%+v", r)
	}
}

// TestClusterPropose: the cluster's propose tools plan a rollout or check
// an operation on the manager, as the user, and add nothing to the
// conversation's proposal: Apply never starts them. A secret copied as
// read goes as the manager's mask; one written is refused.
func TestClusterPropose(t *testing.T) {
	turns := []fakeapi.Turn{
		{Calls: []fakeapi.Call{
			call("p1", ToolRolloutPropose, `{"summary": "Debug logs on edge", "kind": "patch", "group": "edge", "config": {"log": {"level": "debug"},
				"upstreams": [{"name": "api", "health_check": {"headers": {"Authorization": "[redacted]"}}, "tls": {"client_key": "[redacted]"}}]},
				"strategy": {"mode": "canary", "canary": 1}}`),
			call("p2", ToolClusterOpPropose, `{"summary": "Reload them", "op": "reload", "group": "edge", "members": ["m1"], "purge": null}`),
			call("p3", ToolDriftFixPropose, `{"summary": "Fix m1", "nodes": ["m1"]}`),
			call("p4", ToolRolloutPropose, `{"summary": "Set a key", "kind": "patch", "nodes": ["m1"], "config": {"log": {"api_key": "s3cret-written"}}}`),
			call("p5", ToolRolloutPropose, `{"kind": "sync"}`),
			call("p6", ToolRolloutPropose, `{"summary": "x", "kind": "patch", "config": [1]}`),
			call("p7", ToolClusterOpPropose, `{"op": "reload"}`),
			call("p8", ToolDriftFixPropose, `{"nodes": ["m1"]}`),
			call("p9", ToolRolloutPropose, `{"summary": "Sync them", "kind": "template", "template": "api", "nodes": ["m1"], "from": "m2", "paths": ["log"], "description": "the api template"}`),
			call("pa", ToolClusterOpPropose, `{"summary": "Purge", "op": "purge", "purge": {"all": true}}`),
		}},
		{Text: "Planned: start it on the Rollouts page."},
	}
	f, fc := startCluster(t, `"default_level": "propose"`, "", turns...)
	s := f.chat(`{"message": "turn on debug logs on the edge group"}`)
	if s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	res := s.of(EventToolResult)
	if len(res) != 10 {
		t.Fatalf("%+v", res)
	}
	for i, w := range []string{`"link":"#/cluster/rollout/r9"`, `"link":"#/cluster/ops?op=reload\u0026members=m1"`, `"link":"#/cluster/rollout/r10"`,
		"which the assistant never writes", "summary says what the rollout does", "config is a JSON object", "summary says what the operation does",
		"summary says what the fix does", `"state":"planned"`, `"op":"reload"`} {
		if !strings.Contains(res[i].Content, w) || res[i].Error != (i >= 3 && i <= 7) {
			t.Errorf("%s: %+v", w, res[i])
		}
	}
	if len(s.of(EventProposal)) != 0 {
		t.Errorf("a cluster tool joined the proposal: %+v", s.of(EventProposal))
	}
	if _, v, _ := f.view(s.end.Conversation); v != nil {
		t.Errorf("the conversation has a proposal: %+v", v)
	}
	fc.mu.Lock()
	plan := string(fc.plan)
	fc.mu.Unlock()
	if !strings.Contains(plan, `"kind":"template"`) || !strings.Contains(plan, `"description":"the api template"`) || !strings.Contains(plan, `"from":"m2"`) {
		t.Errorf("%s", plan)
	}
	got := fc.called()
	if strings.Count(got, "plan ada") != 2 || !strings.Contains(got, `op ada {"group":"edge","members":["m1"],"op":"reload"}`) ||
		!strings.Contains(got, "fix ada [m1]") || !strings.Contains(got, `op ada {"op":"purge","purge":{"all":true}}`) {
		t.Errorf("%s", got)
	}
	for _, r := range f.model.Requests() {
		if strings.Contains(string(r.Raw), "s3cret-written") && !strings.Contains(string(r.Raw), `"name":"`+ToolRolloutPropose+`"`) {
			t.Error("a refused secret went somewhere")
		}
	}

	// The first plan's config, as the manager got it: masks for the
	// secrets read, in the order written.
	f2, fc2 := startCluster(t, `"default_level": "propose"`, "")
	if r := f2.tool("", ToolRolloutPropose, `{"summary": "Debug", "kind": "patch", "group": "edge", "config": {"log": {"level": "debug"},
		"upstreams": [{"name": "api", "health_check": {"headers": {"Authorization": "[redacted]"}}, "tls": {"client_key": "[redacted]"}}]},
		"strategy": {"mode": "canary", "canary": 1}}`); r.Error || r.Link != "#/cluster/rollout/r9" || r.Conversation != "" {
		t.Fatalf("%+v", r)
	}
	fc2.mu.Lock()
	plan = string(fc2.plan)
	fc2.mu.Unlock()
	if plan != `{"config":{"log":{"level":"debug"},"upstreams":[{"name":"api","health_check":{"headers":{"Authorization":"[secret]"}},"tls":{"client_key":"[secret]"}}]},"description":"Debug","group":"edge","kind":"patch","strategy":{"mode":"canary","canary":1}}` {
		t.Errorf("%s", plan)
	}
	// Over MCP, an operator the manager refuses gets its refusal.
	r := f2.a.CallTool(context.Background(), External{User: "opal", Level: config.AssistantPropose, Source: SourceMCP, Token: "t"}, ToolRolloutPropose,
		json.RawMessage(`{"summary": "x", "kind": "template", "template": "api", "config": {"routes": []}}`))
	if !r.Error || !strings.Contains(r.Text, "may not") || r.Link != "" {
		t.Fatalf("%+v", r)
	}
}

// TestClusterLevels: the assistant's level, capped by the manager's roles,
// and the roles themselves both bound the cluster tools: a user the roles
// cap at read has no propose tool, whatever the settings give.
func TestClusterLevels(t *testing.T) {
	f, _ := startCluster(t, `"default_level": "apply"`, config.AssistantRead,
		fakeapi.Turn{Calls: []fakeapi.Call{call("p1", ToolRolloutPropose, `{"summary": "x", "kind": "sync"}`)}}, fakeapi.Turn{Text: "no"})
	s := f.chat(`{"message": "sync them"}`)
	reqs := f.model.Requests()
	if len(reqs) == 0 || fakeapi.HasTool(reqs[0], ToolRolloutPropose) || !fakeapi.HasTool(reqs[0], "cluster_members") {
		t.Fatalf("%v", offered(reqs[0]))
	}
	if res := s.of(EventToolResult); len(res) != 1 || !res[0].Error || !strings.Contains(res[0].Content, "no tool rollout_propose you may call") {
		t.Fatalf("%+v", res)
	}
	level := f.a.Level(f.a.Settings(), "ada")
	for _, tl := range f.a.ExternalTools(level) {
		if tl.Level == config.AssistantPropose {
			t.Errorf("%s is offered at %s", tl.Name, level)
		}
	}
}

// TestClusterSkillsRun: each cluster skill, run by /name with its first
// example, offers its tools (narrowed to its own and the user's level),
// and does its first read against the recorded model; the planners plan.
func TestClusterSkillsRun(t *testing.T) {
	runs := map[string][]fakeapi.Call{
		"fleet-health":    {call("r1", "cluster_members", `{}`), call("r2", "cluster_certs", `{"within_days": 30}`), call("r3", "status_get", `{"member": "m1"}`)},
		"rollout-planner": {call("r1", "cluster_members", `{"group": "eu"}`), call("p1", ToolRolloutPropose, `{"summary": "Raise the timeout", "kind": "patch", "group": "eu", "config": {"upstreams": [{"name": "api", "timeout": "30s"}]}}`)},
		"cluster-drift":   {call("r1", "cluster_members", `{}`), call("p1", ToolDriftFixPropose, `{"summary": "Fix node-3", "nodes": ["node-3"]}`)},
	}
	for _, name := range clusterSkills {
		t.Run(name, func(t *testing.T) {
			turns := []fakeapi.Turn{{Calls: runs[name]}, {Text: "Done."}}
			f, fc := startCluster(t, `"default_level": "propose"`, "", turns...)
			sk := f.a.EnabledSkill(name)
			body, _ := json.Marshal(map[string]string{"message": "/" + name + " " + sk.Examples[0]})
			s := f.chat(string(body))
			if s.end == nil || s.end.Reason != ReasonAnswer {
				t.Fatalf("%+v %s", s.end, s.err)
			}
			for _, r := range s.of(EventToolResult) {
				if r.Error {
					t.Errorf("%s: %s", r.Name, r.Content)
				}
			}
			reqs := f.model.Requests()
			if !strings.Contains(fakeapi.System(reqs[0]), "The current skill is "+name) {
				t.Error(fakeapi.System(reqs[0]))
			}
			want := []string{"ask_user"}
			want = append(want, sk.Tools...)
			sort.Strings(want)
			if got := offered(reqs[0]); !slices.Equal(got, want) {
				t.Errorf("offered %v, want %v", got, want)
			}
			if strings.Contains(fc.called(), "plan") != (name == "rollout-planner") || strings.Contains(fc.called(), "fix ada") != (name == "cluster-drift") {
				t.Error(fc.called())
			}
			for _, r := range reqs {
				if strings.Contains(string(r.Raw), memberSecret) {
					t.Error("a member's credential reached the model")
				}
			}
		})
	}
}

// TestNoCluster: a fit that manages none has no cluster tools, no member
// argument, no cluster skills and no word of a cluster in its prompt; the
// tools made without a manager refuse.
func TestNoCluster(t *testing.T) {
	f := startProposals(t, "", fakeapi.Turn{Text: "hi"})
	s := f.chat(`{"message": "hi"}`)
	if s.end == nil {
		t.Fatal(s.err)
	}
	if strings.Contains(fakeapi.System(f.model.Requests()[0]), "manager of other fits") {
		t.Error("a cluster in the prompt")
	}
	if f.a.registry.Tool("cluster_members") != nil || strings.Contains(string(f.a.registry.Tool("config_get").Parameters), "member\"") {
		t.Error("cluster tools")
	}
	for _, n := range clusterSkills {
		if f.a.EnabledSkill(n) != nil {
			t.Errorf("the skill %s", n)
		}
	}
	for _, tl := range clusterTools(nil) {
		if _, err := tl.Run(&Call{Context: context.Background(), Args: json.RawMessage(`{}`)}); !errors.Is(err, errNoCluster) {
			t.Errorf("%s: %v", tl.Name, err)
		}
	}
	if _, err := f.a.memberAPI(context.Background(), &memberTarget{name: "m1"}, "GET", "/status", nil); !errors.Is(err, errNoCluster) {
		t.Error(err)
	}
}

// TestClusterToolArgs: what the cluster tools refuse before asking the
// manager.
func TestClusterToolArgs(t *testing.T) {
	f, fc := startCluster(t, `"default_level": "propose"`, "")
	for _, c := range []struct{ tool, args, err string }{
		{"cluster_members", `{"group": 1}`, "not what the tool takes"},
		{"cluster_templates", `{"template": 1}`, "not what the tool takes"},
		{"cluster_templates", `{"template": "nope"}`, "no template"},
		{"cluster_rollouts", `{"rollout": 1}`, "not what the tool takes"},
		{"cluster_rollouts", `{"rollout": "bad"}`, "the manager's answer is not JSON"},
		{"cluster_metrics", `{"nodes": "m1"}`, "not what the tool takes"},
		{"cluster_certs", `{"within_days": "x"}`, "not what the tool takes"},
		{"cluster_log_tail", `{"limit": "x"}`, "not what the tool takes"},
		{ToolRolloutPropose, `{"kind": 1}`, "not what the tool takes"},
		{ToolRolloutPropose, `{"summary": "x", "kind": "patch", "config": {"log": {"x_token": 7}}}`, "never writes"},
		{ToolClusterOpPropose, `{"op": 1}`, "not what the tool takes"},
		{ToolDriftFixPropose, `{"nodes": "m1"}`, "not what the tool takes"},
		{"config_get", `{"member": "m1", "source": "nope"}`, "source is base or effective"},
	} {
		if r := f.tool("", c.tool, c.args); !r.Error || !strings.Contains(r.Text, c.err) {
			t.Errorf("%s %s: %+v", c.tool, c.args, r)
		}
	}
	if strings.Contains(fc.called(), "plan") {
		t.Error(fc.called())
	}
	if _, err := masks(json.RawMessage(`{"a":`)); err == nil {
		t.Error("not JSON")
	}
	// A member's argument that is not JSON is the tool's to refuse.
	if out, err := withMember(&Tool{Name: "x", Parameters: json.RawMessage(`{"type":"object"}`), Run: func(c *Call) (string, error) { return "ran", nil }}).Run(
		&Call{Context: context.Background(), Args: json.RawMessage(`[`)}); err != nil || out != "ran" {
		t.Error(out, err)
	}
	// A call given up on plans nothing.
	var gone atomic.Bool
	gone.Store(true)
	c := &Call{Context: context.Background(), Args: json.RawMessage(`{"summary": "x", "kind": "sync"}`), User: "ada", a: f.a, Settings: f.a.Settings(), expired: &gone}
	for _, tl := range []string{ToolRolloutPropose, ToolDriftFixPropose} {
		if _, err := f.a.registry.Tool(tl).Run(c); err == nil || !strings.Contains(err.Error(), "given up") {
			t.Errorf("%s: %v", tl, err)
		}
	}
}
