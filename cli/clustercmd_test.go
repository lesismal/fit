package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/cluster"
)

// fakeManager is a manager's Admin API as fit cluster sees it: canned
// answers by method and path, and the requests it was sent.
type fakeManager struct {
	mu      sync.Mutex
	answers map[string]func() (int, any)
	sent    []string
}

func (fm *fakeManager) on(key string, status int, v any) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.answers[key] = func() (int, any) { return status, v }
}

func (fm *fakeManager) onFn(key string, fn func() (int, any)) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.answers[key] = fn
}

func (fm *fakeManager) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	fm.mu.Lock()
	fm.sent = append(fm.sent, key+" "+r.URL.RawQuery+" "+string(body))
	fn := fm.answers[key]
	fm.mu.Unlock()
	if fn == nil {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"no such endpoint of the manager's"}`))
		return
	}
	status, v := fn()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if s, ok := v.(string); ok {
		_, _ = w.Write([]byte(s))
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// newFakeManager serves a fake manager and writes the bootstrap config
// that names it.
func newFakeManager(t *testing.T) (*fakeManager, string) {
	fm := &fakeManager{answers: map[string]func() (int, any){}}
	srv := httptest.NewServer(http.HandlerFunc(fm.serve))
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "fit.json")
	writeFile(t, cfg, `{"admin": {"address": `+js(strings.TrimPrefix(srv.URL, "http://"))+`}}`)
	return fm, cfg
}

// TestClusterUsage: fit cluster's usage, and the commands and flags it
// refuses before it reaches the manager.
func TestClusterUsage(t *testing.T) {
	if code, _, stderr := runFit("cluster"); code != 2 || !strings.Contains(stderr, "fit cluster rollout start") {
		t.Errorf("%d %s", code, stderr)
	}
	for _, args := range [][]string{{"cluster", "-h"}, {"cluster", "rollout", "-h"}} {
		if code, _, stderr := runFit(args...); code != 0 || !strings.Contains(stderr, "Usage: fit cluster") {
			t.Errorf("%v: %d %s", args, code, stderr)
		}
	}
	if code, _, _ := runFit("cluster", "rollout"); code != 2 {
		t.Errorf("rollout alone: %d", code)
	}
	if code, stdout, _ := runFit("help"); code != 0 || !strings.Contains(stdout, "cluster ") {
		t.Errorf("help: %s", stdout)
	}
	dir := t.TempDir()
	noAdmin := filepath.Join(dir, "none.json")
	writeFile(t, noAdmin, `{"listeners": []}`)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"zap"}, `unknown command "zap"`},
		{[]string{"nodes", "extra"}, `unexpected argument "extra"`},
		{[]string{"nodes", "-nope"}, ""},
		{[]string{"logs"}, "-node is required"},
		{[]string{"diff", "-node", "a", "-template", "t"}, "not both"},
		{[]string{"certs", "-within", "x"}, "-within"},
		{[]string{"certs", "-within", "3xd"}, "-within"},
		{[]string{"nodes", "-c", filepath.Join(dir, "missing.json")}, "missing.json"},
		{[]string{"nodes", "-c", noAdmin}, "fit cluster"},
		{[]string{"rollout", "zap"}, `unknown command "zap"`},
		{[]string{"rollout", "list", "x"}, "unexpected argument"},
		{[]string{"rollout", "status"}, "give the rollout's ID"},
		{[]string{"rollout", "abort", "r1", "x"}, "unexpected argument"},
		{[]string{"rollout", "abort", "r1", "-nope"}, ""},
		{[]string{"rollout", "start", "-nope"}, ""},
		{[]string{"rollout", "start"}, "give one of"},
		{[]string{"rollout", "start", "-sync", "-template", "t"}, "give one of"},
		{[]string{"rollout", "start", "-id", "r1", "-group", "g"}, "-id starts a rollout"},
		{[]string{"rollout", "start", "-sync", "-c", noAdmin}, "fit cluster"},
	} {
		code, _, stderr := runFit(append([]string{"cluster"}, c.args...)...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: %d %s", c.args, code, stderr)
		}
	}
	for s, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "72h": 72 * time.Hour} {
		if d, err := parseDays(s); err != nil || d != want {
			t.Errorf("%s: %v %v", s, d, err)
		}
	}
	for _, s := range []string{"-1d", "-1h", "1.5d"} {
		if _, err := parseDays(s); err == nil {
			t.Errorf("%s: no error", s)
		}
	}
}

// TestClusterCommands drives every command against a fake manager: the
// listings, the JSON, the exit codes, and a rollout planned, asked about,
// declined, taken by ID, started, followed and steered.
func TestClusterCommands(t *testing.T) {
	defer func(s func(time.Duration)) { clusterSleep = s }(clusterSleep)
	clusterSleep = func(time.Duration) {}
	defer func(in io.Reader) { stdin = in }(stdin)
	fm, cfg := newFakeManager(t)
	run := func(args ...string) (int, string, string) {
		t.Helper()
		full := []string{"cluster", args[0]}
		if args[0] == "rollout" {
			full, args = append(full, args[1]), args[1:]
		}
		return runFit(append(append(full, "-c", cfg), args[1:]...)...)
	}
	// A fit that manages no members.
	fm.on("GET /cluster/nodes", 404, `{"error":"this fit manages no members: admin.cluster is not set"}`)
	if code, _, stderr := run("nodes"); code != 1 || !strings.Contains(stderr, "manages no members") {
		t.Errorf("not a manager: %d %s", code, stderr)
	}
	// Empty ones.
	fm.on("GET /cluster/nodes", 200, map[string]any{"nodes": []any{}, "read_only": "a newer fit wrote it"})
	fm.on("GET /cluster/state", 200, map[string]any{"nodes": []any{}})
	fm.on("GET /cluster/groups", 200, map[string]any{"groups": []any{}})
	fm.on("GET /cluster/rollouts", 200, map[string]any{"rollouts": []any{}})
	fm.on("GET /cluster/certificates", 200, cluster.CertsBody{Nodes: []cluster.CertNode{{Name: "a", Status: "down"}}})
	for cmd, want := range map[string]string{"nodes": "no members yet", "state": "no members yet", "groups": "no groups yet", "certs": "no member serves"} {
		if code, stdout, stderr := run(cmd); code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("%s: %d %s %s", cmd, code, stdout, stderr)
		}
	}
	if _, stdout, stderr := run("nodes"); !strings.Contains(stderr, "read-only") || strings.Contains(stdout, "read-only") {
		t.Errorf("read-only: %s %s", stdout, stderr)
	}
	if _, stdout, _ := run("certs"); !strings.Contains(stdout, "a: its certificates were not read yet (down)") {
		t.Errorf("certs: %s", stdout)
	}
	if code, stdout, _ := run("rollout", "list"); code != 0 || !strings.Contains(stdout, "no rollouts yet") {
		t.Errorf("list: %s", stdout)
	}

	// Full ones.
	now := time.Now().UTC()
	fm.on("GET /cluster/nodes", 200, map[string]any{"nodes": []cluster.MemberView{{Name: "a", URL: "https://a:2020", Access: "admin_api", Labels: map[string]string{"env": "prod", "az": "1"}}}})
	fm.on("GET /cluster/state", 200, map[string]any{"nodes": []cluster.State{
		{Name: "a", Status: "up", Node: &cluster.NodeInfo{FitVersion: "v1"}, Config: &cluster.ConfigInfo{Version: 3}, Drift: &cluster.Drift{Count: 1},
			Certificates: &cluster.Certificates{NearestExpiry: now}, Connections: &cluster.Connections{Open: 4}, LatencyMs: 3},
		{Name: "b", Status: "down", Error: &cluster.Problem{Kind: "unreachable", Message: "no\nroute " + strings.Repeat("x", 100)}}}})
	fm.on("GET /cluster/groups", 200, map[string]any{"groups": []cluster.GroupView{{Group: cluster.Group{Name: "prod", Selector: "env=prod"}, Nodes: []string{"a"}}}})
	fm.on("GET /cluster/certificates", 200, cluster.CertsBody{Now: now, Certificates: []cluster.CertEntry{
		{Node: "a", Listener: "web", Names: []string{"a.example"}, Issuer: "CA", NotAfter: now.Add(48 * time.Hour), DaysLeft: 2}}})
	one := func(v float64) []cluster.Value { return []cluster.Value{{}, {V: v, OK: true}} }
	fm.on("GET /cluster/metrics", 200, cluster.MetricsBody{
		Nodes: []cluster.NodeSeries{{Name: "a", Status: "up", Counters: &cluster.Counters{Requests: &cluster.RequestTotals{Total: 99}},
			Series: cluster.Series{T: []int64{1, 2}, Requests: one(2.5), Errors4xx: one(0), Errors5xx: one(0.5), Connections: one(4), PendingBytes: one(0)}}},
		Groups: []cluster.GroupSeries{{Name: "prod", Nodes: []string{"a"}, Series: cluster.Series{T: []int64{1}}}}})
	fm.on("POST /cluster/sync", 200, map[string]any{"pending": []string{"c"}, "nodes": []cluster.MemberSync{
		{Node: "a", State: "in_sync"},
		{Node: "b", State: "out_of_sync", Errors: []string{"t: no var"}, Parts: []cluster.PartSync{
			{Path: "routes[x]", State: "in_sync"},
			{Path: "routes[api]", State: "out_of_sync", Template: "t", Reason: "differs", Have: json.RawMessage(`{"a":1}`), Want: json.RawMessage(`{"a":2}`)}}},
		{Node: "d", State: "error", Error: &cluster.Problem{Kind: "timeout", Message: "slow"}}}})
	for _, c := range []struct {
		args []string
		code int
		want []string
	}{
		{[]string{"nodes"}, 0, []string{"https://a:2020", "az=1,env=prod"}},
		{[]string{"state"}, 1, []string{"v1", "v3", now.Format("2006-01-02"), "unreachable: no route", "…"}},
		{[]string{"groups"}, 0, []string{"prod", "env=prod"}},
		{[]string{"certs", "-within", "3d"}, 1, []string{"a.example", "CA"}},
		{[]string{"certs", "-within", "1d"}, 0, []string{"a.example"}},
		{[]string{"metrics", "-node", "a", "-group", "prod"}, 0, []string{"2.50", "0.50", "99", "group prod"}},
		{[]string{"diff", "-node", "a,b"}, 1, []string{"a: in_sync", "out_of_sync routes[api] (template t): differs", `- {"a":1}`, `+ {"a":2}`, "error: t: no var", "d: error (timeout: slow)", "c: not read in time"}},
		{[]string{"state", "-json"}, 1, []string{`"name":"b"`}},
		{[]string{"nodes", "-json"}, 0, []string{`"name":"a"`}},
		{[]string{"diff", "-json"}, 1, []string{`"pending"`}},
		{[]string{"certs", "-json"}, 0, []string{`"days_left":2`}},
	} {
		code, stdout, stderr := run(c.args...)
		if code != c.code {
			t.Errorf("%v: exit %d, want %d: %s %s", c.args, code, c.code, stdout, stderr)
		}
		for _, w := range c.want {
			if !strings.Contains(stdout, w) {
				t.Errorf("%v: no %q in %s", c.args, w, stdout)
			}
		}
	}
	fm.on("POST /cluster/sync", 200, map[string]any{"nodes": []cluster.MemberSync{{Node: "a", State: "unmanaged"}}})
	if code, _, _ := run("diff"); code != 0 {
		t.Errorf("unmanaged: %d", code)
	}
	fm.on("GET /cluster/groups", 500, `{"error":"broken"}`)
	if code, _, stderr := run("groups"); code != 1 || !strings.Contains(stderr, "500: broken") {
		t.Errorf("an error: %d %s", code, stderr)
	}
	fm.on("GET /cluster/groups", 200, `not json`)
	if code, _, stderr := run("groups"); code != 1 || !strings.Contains(stderr, "not the JSON expected") {
		t.Errorf("not JSON: %d %s", code, stderr)
	}

	// Logs, followed twice: a member's failure said once.
	fm.on("GET /cluster/logs", 200, cluster.TailBody{Cursor: "abc", Records: []cluster.TailRecord{{Node: "a"}}, Nodes: []cluster.TailNode{
		{Name: "a"}, {Name: "b", Error: &cluster.Problem{Kind: "timeout", Message: "slow"}}}})
	defer func(n int) { clusterFollowRounds = n }(clusterFollowRounds)
	clusterFollowRounds = 2
	code, stdout, stderr := run("logs", "-node", "a,b", "-f")
	if code != 1 || strings.Count(stdout, " a ") != 2 || strings.Count(stderr, "b: timeout: slow") != 1 {
		t.Errorf("logs: %d %s %s", code, stdout, stderr)
	}
	fm.mu.Lock()
	sent := strings.Join(fm.sent, "\n")
	fm.mu.Unlock()
	if !strings.Contains(sent, "cursor=abc") {
		t.Errorf("the cursor was not sent back: %s", sent)
	}
	if code, stdout, _ := run("logs", "-node", "a", "-json"); code != 1 || !strings.Contains(stdout, `"cursor":"abc"`) {
		t.Errorf("logs -json: %d %s", code, stdout)
	}
	fm.on("GET /cluster/logs", 400, `{"error":"bad"}`)
	if code, _, _ := run("logs", "-node", "a"); code != 1 {
		t.Errorf("logs refused: %d", code)
	}
	fm.on("GET /cluster/metrics", 200, `{"nodes":[{"name":"a","series":{"t":[1],"requests":[null]}}]}`)
	if code, stdout, _ := run("metrics", "-json"); code != 0 || !strings.Contains(stdout, `"requests":[null]`) {
		t.Errorf("metrics -json: %d %s", code, stdout)
	}

	// A rollout: planned, shown, declined; then taken by its ID and started,
	// followed until it is done.
	plan := cluster.Rollout{ID: "r1", Kind: "template", Template: "t", Group: "prod", State: "planned", Members: []cluster.RolloutMember{
		{Node: "a", Status: "ready", Version: 3, Changes: []cluster.Change{{Path: "routes[api]"}}},
		{Node: "b", Status: "error", Errors: []cluster.ConfigError{{Path: "/routes/1", Message: "bad"}}, Error: &cluster.Problem{Message: "dry run failed"}},
		{Node: "c", Status: "unchanged", Note: "another fit"}}}
	planning := plan
	planning.State = "planning"
	reads := 0
	fm.on("POST /cluster/rollouts", 202, planning)
	fm.onFn("GET /cluster/rollouts/r1", func() (int, any) {
		reads++
		if reads == 1 {
			return 200, planning
		}
		return 200, plan
	})
	stdin = strings.NewReader("n\n")
	code, stdout, stderr = run("rollout", "start", "-template", "t", "-group", "prod", "-node", "a,b", "-description", "d")
	if code != 1 || !strings.Contains(stdout, "Start the rollout r1 on 1 members, all? [y/N]") || !strings.Contains(stdout, "not started") ||
		!strings.Contains(stdout, "/routes/1: bad") || !strings.Contains(stdout, "another fit") {
		t.Fatalf("declined: %d %s %s", code, stdout, stderr)
	}
	running := plan
	running.State, running.Batches = "running", []cluster.Batch{{Index: 0, Nodes: []string{"a"}}}
	done := plan
	done.State, done.Members = "done", []cluster.RolloutMember{{Node: "a", Status: "done", Version: 3, Applied: 4, Changed: []string{"routes[api]"},
		Gate: &cluster.GateResult{Passed: true}}}
	fm.on("POST /cluster/rollouts/r1/start", 200, running)
	reads = 0
	fm.onFn("GET /cluster/rollouts/r1", func() (int, any) {
		reads++
		switch {
		case reads == 1:
			return 200, plan
		case reads < 4:
			return 200, running
		}
		return 200, done
	})
	stdin = strings.NewReader("yes\n")
	code, stdout, stderr = run("rollout", "start", "-id", "r1", "-mode", "rolling", "-batch-size", "1", "-max-5xx", "0.1", "-wait")
	if code != 0 || !strings.Contains(stdout, "rollout r1: running, batch 1 of 1") || !strings.Contains(stdout, "v3 → v4") || !strings.Contains(stdout, ": done") {
		t.Fatalf("started: %d %s %s", code, stdout, stderr)
	}
	fm.mu.Lock()
	sent = strings.Join(fm.sent, "\n")
	fm.mu.Unlock()
	if !strings.Contains(sent, `"mode":"rolling","batch_size":1`) || !strings.Contains(sent, `"max_5xx_ratio":0.1`) || !strings.Contains(sent, `"kind":"template","template":"t","group":"prod","nodes":["a","b"],"description":"d"`) {
		t.Errorf("the requests: %s", sent)
	}
	// Without -wait, and -json.
	reads = 0
	code, stdout, _ = run("rollout", "start", "-id", "r1", "-yes")
	if code != 0 || !strings.Contains(stdout, "started: follow it with fit cluster rollout status -wait r1") {
		t.Errorf("no wait: %d %s", code, stdout)
	}
	reads = 0
	if code, stdout, _ := run("rollout", "start", "-id", "r1", "-yes", "-json"); code != 0 || !strings.HasPrefix(stdout, `{"id":"r1"`) {
		t.Errorf("-json: %d %s", code, stdout)
	}
	reads = 0
	if code, stdout, _ := run("rollout", "start", "-id", "r1", "-yes", "-json", "-wait"); code != 0 || !strings.Contains(stdout, `"state":"done"`) {
		t.Errorf("-json -wait: %d %s", code, stdout)
	}
	// What cannot start.
	fm.on("GET /cluster/rollouts/r1", 200, done)
	if code, _, stderr := run("rollout", "start", "-id", "r1", "-yes"); code != 1 || !strings.Contains(stderr, "is done: only a planned") {
		t.Errorf("done: %d %s", code, stderr)
	}
	noneReady := plan
	noneReady.Members = []cluster.RolloutMember{{Node: "a", Status: "ready"}}
	fm.on("GET /cluster/rollouts/r1", 200, noneReady)
	if code, _, stderr := run("rollout", "start", "-id", "r1", "-yes", "-exclude", "a"); code != 1 || !strings.Contains(stderr, "nothing was started") {
		t.Errorf("none ready: %d %s", code, stderr)
	}
	fm.on("GET /cluster/rollouts/r1", 200, planning)
	defer func(d time.Duration) { rolloutPlanWait = d }(rolloutPlanWait)
	rolloutPlanWait = 0
	if code, _, stderr := run("rollout", "start", "-id", "r1", "-yes"); code != 1 || !strings.Contains(stderr, "still being planned") {
		t.Errorf("planning: %d %s", code, stderr)
	}
	fm.on("POST /cluster/rollouts", 400, `{"error":"no"}`)
	if code, _, _ := run("rollout", "start", "-sync", "-yes"); code != 1 {
		t.Errorf("plan refused: %d", code)
	}
	fm.on("GET /cluster/rollouts/r1", 404, `{"error":"no rollout"}`)
	if code, _, _ := run("rollout", "start", "-id", "r1", "-yes"); code != 1 {
		t.Errorf("no such rollout: %d", code)
	}
	if code, _, _ := run("rollout", "status", "r1"); code != 1 {
		t.Errorf("status of none: %d", code)
	}
	fm.on("GET /cluster/rollouts/r1", 200, plan)
	fm.on("POST /cluster/rollouts/r1/start", 409, `{"error":"busy"}`)
	if code, _, stderr := run("rollout", "start", "-id", "r1", "-yes"); code != 1 || !strings.Contains(stderr, "409: busy") {
		t.Errorf("start refused: %d %s", code, stderr)
	}

	// Steering, and the history.
	waiting := plan
	waiting.State = "waiting"
	fm.on("GET /cluster/rollouts/r1", 200, waiting)
	fm.on("POST /cluster/rollouts/r1/pause", 200, waiting)
	if code, stdout, _ := run("rollout", "pause", "r1", "-wait"); code != 0 || !strings.Contains(stdout, "fit cluster rollout go r1") {
		t.Errorf("pause -wait: %d %s", code, stdout)
	}
	if code, stdout, _ := run("rollout", "pause", "-json", "r1"); code != 0 || !strings.HasPrefix(stdout, `{"id":"r1"`) {
		t.Errorf("pause -json: %d %s", code, stdout)
	}
	fm.on("POST /cluster/rollouts/r1/abort", 409, `{"error":"over"}`)
	if code, _, stderr := run("rollout", "abort", "r1"); code != 1 || !strings.Contains(stderr, "over") {
		t.Errorf("abort refused: %d %s", code, stderr)
	}
	rolledBack := plan
	rolledBack.State, rolledBack.Reason = "rolled_back", "asked"
	rolledBack.Members = []cluster.RolloutMember{{Node: "a", Status: "rolled_back", Version: 3, Applied: 4, RolledBack: 5,
		Gate: &cluster.GateResult{Reasons: []string{"5xx 0.5"}}}}
	fm.on("GET /cluster/rollouts/r1", 200, rolledBack)
	if code, stdout, _ := run("rollout", "status", "r1"); code != 1 || !strings.Contains(stdout, "v3 → v4 → v5 (rolled back)") || !strings.Contains(stdout, "gate: 5xx 0.5") {
		t.Errorf("rolled back: %d %s", code, stdout)
	}
	fm.on("GET /cluster/rollouts", 200, map[string]any{"rollouts": []cluster.Rollout{plan, {ID: "r2", Kind: "copy", From: "a", Paths: []string{"log"}, State: "done"}}})
	if code, stdout, _ := run("rollout", "list"); code != 0 || !strings.Contains(stdout, "a: log") || !strings.Contains(stdout, "r1") {
		t.Errorf("list: %d %s", code, stdout)
	}
	if code, stdout, _ := run("rollout", "list", "-json"); code != 0 || !strings.Contains(stdout, `"r2"`) {
		t.Errorf("list -json: %d %s", code, stdout)
	}
}
