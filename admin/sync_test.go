package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	fitsync "github.com/lesismal/fit/sync"
)

// syncFixture runs a sync runtime with pipelines, registered for the Admin
// API's engine.
func syncFixture(t *testing.T, engineKey *Configs, pipelines ...string) (*fitsync.Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	src := fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [%s]}}`, dir, strings.Join(pipelines, ","))
	c, err := config.Load([]byte(src))
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	rt := fitsync.NewRuntime(fitsync.Options{})
	if err := rt.Apply(c.Sync); err != nil {
		t.Fatal(err)
	}
	engine := engineKey.env.Engine
	fitsync.Register(engine, rt)
	t.Cleanup(func() {
		fitsync.Unregister(engine, rt)
		rt.Close(context.Background())
	})
	return rt, dir
}

// syncCaller sends requests to the Admin API, or to the console's /api/.
type syncCaller func(method, path, body string) (int, string)

func (call syncCaller) json(t *testing.T, method, path, body string, want int) map[string]any {
	t.Helper()
	code, got := call(method, path, body)
	if code != want {
		t.Fatalf("%s %s: %d %s", method, path, code, got)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("%s %s: %v %s", method, path, err, got)
	}
	return v
}

func apiCaller(t *testing.T, f *fixture) syncCaller {
	return func(method, path, body string) (int, string) {
		t.Helper()
		var req *http.Request
		if body == "" {
			req, _ = http.NewRequest(method, f.api+path, nil)
		} else {
			req, _ = http.NewRequest(method, f.api+path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		resp, got := f.do(t, req)
		return resp.StatusCode, got
	}
}

func waitSync(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// testSyncEndpoints drives every /sync endpoint through call.
func testSyncEndpoints(t *testing.T, call syncCaller, cs *Configs) {
	// Without the role.
	call.json(t, "GET", "/sync/pipelines", "", 404)
	call.json(t, "GET", "/sync/pipelines/p", "", 404)
	call.json(t, "POST", "/sync/pipelines/p", `{"action": "pause"}`, 404)
	call.json(t, "GET", "/sync/pipelines/p/checkpoint", "", 404)
	call.json(t, "PUT", "/sync/pipelines/p/checkpoint", `{"position": 1}`, 404)
	call.json(t, "DELETE", "/sync/pipelines/p/checkpoint", "", 404)
	call.json(t, "POST", "/sync/pipelines/p/test", "", 404)
	call.json(t, "POST", "/sync/pipelines/p/dry-run", "", 404)
	// The pipeline's config is the request's: no role is needed.
	call.json(t, "POST", "/sync/dry-run", `{"pipeline": {"name": "x", "source": {"module": "generate", "params": {"records": 1}}, "sinks": [{"name": "o", "sink": {"module": "stdout"}}]}}`, 200)

	store := fitsync.MemoryStore(t.Name())
	t.Cleanup(store.Reset)
	stream := fitsync.MemoryStore(t.Name() + "-stream")
	t.Cleanup(stream.Reset)
	_, _ = syncFixture(t, cs,
		fmt.Sprintf(`{"name": "job", "mode": "once", "source": {"module": "generate", "params": {"records": 50}},
			"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, t.Name()),
		fmt.Sprintf(`{"name": "live", "checkpoint_interval": "5ms", "source": {"module": "generate", "params": {"interval": "1ms"}},
			"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, t.Name()+"-stream"))

	stateOf := func(name string) map[string]any { return call.json(t, "GET", "/sync/pipelines/"+name, "", 200) }
	waitSync(t, "the job", func() bool { return stateOf("job")["state"] == "finished" && len(store.Records()) == 50 })
	list := call.json(t, "GET", "/sync/pipelines", "", 200)["pipelines"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["name"] != "job" || list[1].(map[string]any)["name"] != "live" {
		t.Fatalf("%v", list)
	}
	job := stateOf("job")
	if job["checkpoint"].(map[string]any)["n"] != 50.0 || job["job"].(map[string]any)["written"] != 50.0 || job["acknowledged"] != 50.0 {
		t.Fatalf("%v", job)
	}
	call.json(t, "GET", "/sync/pipelines/none", "", 404)

	// Pause and resume.
	waitSync(t, "records", func() bool { return len(stream.Records()) > 10 })
	if st := call.json(t, "POST", "/sync/pipelines/live", `{"action": "pause"}`, 200); st["state"] != "paused" || st["paused"] != true {
		t.Fatalf("%v", st)
	}
	waitSync(t, "the queue to drain", func() bool {
		st := stateOf("live")
		return st["queue"].(map[string]any)["records"] == 0.0 && st["read"] == float64(len(stream.Records()))
	})
	if st := call.json(t, "POST", "/sync/pipelines/live", `{"action": "resume"}`, 200); st["state"] != "running" {
		t.Fatalf("%v", st)
	}
	call.json(t, "POST", "/sync/pipelines/live", `{"action": "rewind"}`, 400)
	call.json(t, "POST", "/sync/pipelines/live", `{"action": `, 400)
	call.json(t, "POST", "/sync/pipelines/none", `{"action": "pause"}`, 404)

	// The checkpoint, shown, set, reset.
	c := call.json(t, "GET", "/sync/pipelines/job/checkpoint", "", 200)["checkpoint"].(map[string]any)
	if c["position"].(map[string]any)["n"] != 50.0 || c["pipeline"] != "job" {
		t.Fatalf("%v", c)
	}
	call.json(t, "PUT", "/sync/pipelines/job/checkpoint", `{"position": {"n": 40}}`, 409)
	call.json(t, "PUT", "/sync/pipelines/job/checkpoint", `{"position": {, "confirm": true}`, 400)
	call.json(t, "PUT", "/sync/pipelines/job/checkpoint", `{"confirm": true}`, 400)
	call.json(t, "PUT", "/sync/pipelines/none/checkpoint", `{"position": {"n": 40}, "confirm": true}`, 404)
	c = call.json(t, "PUT", "/sync/pipelines/job/checkpoint", `{"position": {"n": 40}, "confirm": true}`, 200)["checkpoint"].(map[string]any)
	if c["position"].(map[string]any)["n"] != 40.0 {
		t.Fatalf("%v", c)
	}
	waitSync(t, "the job from 40", func() bool { return stateOf("job")["state"] == "finished" && len(store.Records()) == 60 })
	call.json(t, "DELETE", "/sync/pipelines/job/checkpoint", "", 409)
	call.json(t, "DELETE", "/sync/pipelines/job/checkpoint?confirm=1", "", 200)
	waitSync(t, "the job anew", func() bool { return stateOf("job")["state"] == "finished" && len(store.Records()) == 110 })
	call.json(t, "POST", "/sync/pipelines/job", `{"action": "snapshot"}`, 409)
	call.json(t, "POST", "/sync/pipelines/job", `{"action": "snapshot", "confirm": true}`, 200)
	waitSync(t, "the snapshot", func() bool { return stateOf("job")["state"] == "finished" && len(store.Records()) == 160 })

	// Connection tests.
	res := call.json(t, "POST", "/sync/pipelines/job/test", "", 200)
	if res["ok"] != true || len(res["results"].([]any)) != 2 {
		t.Fatalf("%v", res)
	}
	call.json(t, "POST", "/sync/pipelines/none/test", "", 404)
	res = call.json(t, "POST", "/sync/test", `{"pipeline": {"name": "x", "source": {"module": "generate"}, "sinks": [{"name": "o", "sink": {"module": "stdout"}}]}}`, 200)
	if r := res["results"].([]any); res["ok"] != true || len(r) != 2 || r[1].(map[string]any)["part"] != "sink o" {
		t.Fatalf("%v", res)
	}
	call.json(t, "POST", "/sync/test", `{}`, 400)
	call.json(t, "POST", "/sync/test", `{"pipeline": {"name": "x", "source": {"module": "nope"}, "sinks": [{"name": "o", "sink": {"module": "stdout"}}]}}`, 400)

	// Dry runs: nothing written, no checkpoint saved.
	before := call.json(t, "GET", "/sync/pipelines/job/checkpoint", "", 200)
	n := len(store.Records())
	d := call.json(t, "POST", "/sync/pipelines/job/dry-run", `{"from": "start", "limit": 3}`, 200)
	if recs := d["records"].([]any); len(recs) != 3 || len(recs[0].(map[string]any)["out"].([]any)) != 1 {
		t.Fatalf("%v", d)
	}
	d = call.json(t, "POST", "/sync/pipelines/job/dry-run", "", 200)
	if d["ended"] != true || len(d["records"].([]any)) != 0 {
		t.Fatalf("%v", d)
	}
	after := call.json(t, "GET", "/sync/pipelines/job/checkpoint", "", 200)
	if fmt.Sprint(before) != fmt.Sprint(after) || len(store.Records()) != n {
		t.Fatalf("the dry run changed something: %v %v", before, after)
	}
	call.json(t, "POST", "/sync/pipelines/job/dry-run", `{"from": "middle"}`, 400)
	call.json(t, "POST", "/sync/pipelines/job/dry-run", `{"limit": 5000}`, 400)
	call.json(t, "POST", "/sync/pipelines/job/dry-run", `{"timeout": "2h"}`, 400)
	call.json(t, "POST", "/sync/pipelines/none/dry-run", "", 404)
	d = call.json(t, "POST", "/sync/dry-run", `{"pipeline": {"name": "x", "source": {"module": "generate", "params": {"records": 2}}, "sinks": [{"name": "o", "sink": {"module": "stdout"}}]}, "timeout": "10s"}`, 200)
	if d["ended"] != true || len(d["records"].([]any)) != 2 {
		t.Fatalf("%v", d)
	}
	call.json(t, "POST", "/sync/dry-run", `{"pipeline": {"name": "x", "source": {"module": "generate"}, "sinks": [{"name": "o", "sink": {"module": "stdout"}}]}, "from": "checkpoint"}`, 400)
	call.json(t, "POST", "/sync/dry-run", `{"pipeline": [1]}`, 400)
	call.json(t, "POST", "/sync/dry-run", `{"pipeline": {"name": "x", "source": {"module": "generate"}, "sinks": [{"name": "o", "sink": {"module": "stdout"}}]}, "limit": 5000}`, 400)
	call.json(t, "POST", "/sync/dry-run", `{"pipeline": {"name": "x", "source": {"module": "nope"}, "sinks": [{"name": "o", "sink": {"module": "stdout"}}]}}`, 400)
	call.json(t, "POST", "/sync/dry-run", `{`, 400)
	call.json(t, "POST", "/sync/test", `{`, 400)
	call.json(t, "POST", "/sync/pipelines/job/dry-run", `{"limit": "x"}`, 400)
	call.json(t, "GET", "/sync/pipelines/none/checkpoint", "", 404)
	call.json(t, "DELETE", "/sync/pipelines/none/checkpoint?confirm=1", "", 404)
}

// TestSyncAPI drives the sync endpoints through the Admin API.
func TestSyncAPI(t *testing.T) {
	f := startEnv(t, doc("x"), &config.Admin{}, runtime.Env{Metrics: observe.NewMetrics(), Engine: testEngine(t)})
	testSyncEndpoints(t, apiCaller(t, f), f.configs)
	testSyncDeadLetters(t, apiCaller(t, f), f.configs)
	for _, r := range []struct{ method, path string }{{"PUT", "/sync/pipelines/job/checkpoint"}, {"POST", "/sync/pipelines/job/dry-run"}} {
		req, _ := http.NewRequest(r.method, f.api+r.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "text/plain")
		if resp, body := f.do(t, req); resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("%s %s as text: %d %s", r.method, r.path, resp.StatusCode, body)
		}
	}
	if _, body := f.get(t, "/node"); !strings.Contains(body, `"`+CapSync+`"`) {
		t.Errorf("/node: %s", body)
	}
}

// TestSyncConsole drives them through the console, which needs a session.
func TestSyncConsole(t *testing.T) {
	f := startConsole(t, &config.Console{})
	b := f.browser(t)
	if code, _, _ := b.do("GET", "/api/m/admin/sync/pipelines", ""); code != http.StatusUnauthorized {
		t.Fatalf("without a login: %d", code)
	}
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	call := func(method, path, body string) (int, string) {
		code, got, _ := b.do(method, "/api/m/admin"+path, body)
		return code, got
	}
	testSyncEndpoints(t, call, f.configs)
	testSyncDeadLetters(t, call, f.configs)
	if !f.logged(`"ada": POST /sync/pipelines/live: 200`) {
		t.Errorf("the action is not logged: %q", f.logLines())
	}
}
