package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/model"
)

func TestManageAccess(t *testing.T) {
	n := startNode(t, &config.LLMNode{}, nil)
	if _, err := call(n, asHub, model.MethodNodeModelList, struct{}{}); code(err) != model.CodeForbidden {
		t.Fatalf("hub without allow_hub_manage: %v", err)
	}
	if _, _, err := follow(t, n, asHub, 1); code(err) != model.CodeForbidden {
		t.Fatalf("hub follow without allow_hub_manage: %v", err)
	}
	if err := n.allowManage(client); code(err) != model.CodeForbidden {
		t.Fatalf("proxy: %v", err)
	}
	// The local console gets an empty list, never null.
	res, err := call(n, local, model.MethodNodeModelList, struct{}{})
	if ms, ok := res.([]model.Model); err != nil || !ok || ms == nil || len(ms) != 0 {
		t.Fatalf("model list = %#v %v", res, err)
	}
	if err := n.Apply(&config.LLMNode{ID: "node-t", Settings: &config.LLMNodeSettings{AllowHubManage: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(n, asHub, model.MethodNodeModelList, struct{}{}); err != nil {
		t.Fatalf("hub with allow_hub_manage: %v", err)
	}
	if err := n.Apply((*config.LLMNode)(nil)); err == nil {
		t.Fatal("Apply of no member did not fail")
	}
	for _, i := range app.Roles() {
		if i.ID == "llm.node" && i.Member(&config.Config{}) != nil {
			t.Fatal("a config without llm.node has a member")
		}
	}
}

func TestJobsThroughMethods(t *testing.T) {
	fakeTools(t, map[string]string{"ollama": `case "$1" in ps) sleep 30 ;; *) echo "listed $1" ;; esac`})
	n := startNode(t, &config.LLMNode{Settings: noSudo()}, nil)
	for _, m := range []string{model.MethodNodeActionList, model.MethodNodeJobList, model.MethodNodeGPUCatalog} {
		if _, err := call(n, local, m, struct{}{}); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}
	if _, err := call(n, local, model.MethodNodeActionRun, model.ActionRunReq{ID: "rm_rf"}); code(err) != model.CodeBadRequest {
		t.Fatalf("unknown action: %v", err)
	}
	res, err := call(n, local, model.MethodNodeActionRun, model.ActionRunReq{ID: "ollama_list"})
	if err != nil {
		t.Fatal(err)
	}
	j := res.(model.Job)
	out, end, err := follow(t, n, local, j.ID)
	if err != nil || end.Status != model.JobDone || !strings.Contains(out, "listed list") {
		t.Fatalf("follow = %q %+v %v", out, end, err)
	}
	if res, err := call(n, local, model.MethodNodeJobGet, model.JobGetReq{ID: j.ID}); err != nil || !strings.Contains(res.(model.Job).Output, "listed") {
		t.Fatalf("job get = %v %v", res, err)
	}
	if _, err := call(n, local, model.MethodNodeJobGet, model.JobGetReq{ID: 99}); code(err) != model.CodeNotFound {
		t.Fatalf("job get unknown: %v", err)
	}
	if _, err := call(n, local, model.MethodNodeJobCancel, model.JobIDReq{ID: 99}); code(err) != model.CodeNotFound {
		t.Fatalf("job cancel unknown: %v", err)
	}
	if _, _, err := follow(t, n, local, 99); code(err) != model.CodeNotFound {
		t.Fatalf("follow unknown: %v", err)
	}
	// A running job is cancelled through the method.
	res, err = call(n, local, model.MethodNodeActionRun, model.ActionRunReq{ID: "ollama_ps"})
	if err != nil {
		t.Fatal(err)
	}
	long := res.(model.Job)
	if _, err := call(n, local, model.MethodNodeJobCancel, model.JobIDReq{ID: long.ID}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, end, _ := follow(t, n, local, long.ID); end.Status != model.JobCanceled {
		t.Fatalf("cancelled job = %+v", end)
	}
	// Past the bound on running jobs, a job is refused as rate limited;
	// those left running are cancelled when the node stops.
	for range maxRunning {
		if _, err := call(n, local, model.MethodNodeActionRun, model.ActionRunReq{ID: "ollama_ps"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call(n, local, model.MethodNodeActionRun, model.ActionRunReq{ID: "ollama_ps"}); code(err) != model.CodeRateLimited {
		t.Fatalf("past the bound: %v", err)
	}
}

func TestProfile(t *testing.T) {
	ed := &editor{}
	n := startNode(t, &config.LLMNode{}, ed)
	for _, req := range []model.ProfileReq{{Name: " "}, {Name: strings.Repeat("x", 65)}, {Name: "n", Description: strings.Repeat("d", 1025)}} {
		if _, err := call(n, local, model.MethodNodeProfileSet, req); code(err) != model.CodeBadRequest {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	if _, err := call(n, local, model.MethodNodeProfileSet, model.ProfileReq{Name: " box ", Description: "gpu box"}); err != nil {
		t.Fatal(err)
	}
	if m := ed.member(t); string(m["name"]) != `"box"` || string(m["description"]) != `"gpu box"` {
		t.Fatalf("member = %v", m)
	}
	ed.set("{}", errors.New("read-only"))
	if _, err := call(n, local, model.MethodNodeProfileSet, model.ProfileReq{Name: "box"}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("failed edit: %v", err)
	}
}

func TestSettings(t *testing.T) {
	ed := &editor{}
	n := startNode(t, &config.LLMNode{}, ed)
	for _, req := range []model.NodeSettings{
		{RefreshInterval: "1s"},
		{RefreshInterval: "soon"},
		{JobTimeout: "1s"},
		{GPUDataDir: strings.Repeat("d", 4097)},
	} {
		if _, err := call(n, local, model.MethodNodeSettingsSet, req); code(err) != model.CodeBadRequest {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	// The defaults and false values are left out of the member.
	res, err := call(n, local, model.MethodNodeSettingsSet, model.NodeSettings{GPUSudo: true,
		RefreshInterval: config.DefaultLLMRefreshInterval.String(), JobTimeout: config.DefaultLLMJobTimeout.String()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.(model.NodeSettings); !ok {
		t.Fatalf("settings = %#v", res)
	}
	if s := ed.member(t)["settings"]; s != nil {
		t.Fatalf("default settings written: %s", s)
	}
	// Every other value is written.
	if _, err := call(n, local, model.MethodNodeSettingsSet, model.NodeSettings{AllowHubManage: true,
		RefreshInterval: "10s", JobTimeout: "20s", GPUDataDir: "/data"}); err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	_ = json.Unmarshal(ed.member(t)["settings"], &s)
	if s["allow_hub_manage"] != true || s["gpu_sudo"] != false || s["refresh_interval"] != "10s" || s["job_timeout"] != "20s" || s["gpu_data_dir"] != "/data" {
		t.Fatalf("settings written = %v", s)
	}
	if res, err := call(n, local, model.MethodNodeSettingsGet, struct{}{}); err != nil || res.(model.NodeSettings).JobTimeout == "" {
		t.Fatalf("settings get = %v %v", res, err)
	}
	ed.set("{}", errors.New("read-only"))
	if _, err := call(n, local, model.MethodNodeSettingsSet, model.NodeSettings{}); err == nil {
		t.Fatal("a failed edit succeeded")
	}
}

func TestModelEdits(t *testing.T) {
	ed := &editor{}
	n := startNode(t, &config.LLMNode{}, ed)
	save := func(m model.Model) (any, error) { return call(n, local, model.MethodNodeModelSave, m) }
	models := func() []model.Model {
		var ms []model.Model
		_ = json.Unmarshal(ed.member(t)["models"], &ms)
		return ms
	}
	if _, err := save(model.Model{Name: "m1", API: "nope", BaseURL: "http://127.0.0.1:1/v1"}); code(err) != model.CodeBadRequest {
		t.Fatalf("bad api: %v", err)
	}
	key := "sk-0123456789abcdef"
	res, err := save(model.Model{Name: "m1", API: model.APIOpenAI, BaseURL: "http://127.0.0.1:1/v1/", APIKey: key})
	if err != nil || res.(model.Model).APIKey != "sk-0****cdef" {
		t.Fatalf("save = %v %v", res, err)
	}
	// The masked key sent back keeps the key; the rest is replaced.
	if _, err := save(model.Model{Name: "m1", API: model.APIOpenAI, BaseURL: "http://127.0.0.1:2/v1", APIKey: "sk-0****cdef"}); err != nil {
		t.Fatal(err)
	}
	if ms := models(); len(ms) != 1 || ms[0].APIKey != key || ms[0].BaseURL != "http://127.0.0.1:2/v1" {
		t.Fatalf("models = %+v", ms)
	}
	// The same name for another API format: deleting needs the api.
	if _, err := save(model.Model{Name: "m1", API: model.APIAnthropic, BaseURL: "http://127.0.0.1:2"}); err != nil {
		t.Fatal(err)
	}
	del := func(r model.NameReq) error { _, err := call(n, local, model.MethodNodeModelDelete, r); return err }
	if err := del(model.NameReq{Name: "m1"}); code(err) != model.CodeBadRequest {
		t.Fatalf("ambiguous delete: %v", err)
	}
	if err := del(model.NameReq{Name: "zz"}); code(err) != model.CodeNotFound {
		t.Fatalf("unknown delete: %v", err)
	}
	if err := del(model.NameReq{Name: "m1", API: model.APIAnthropic}); err != nil || len(models()) != 1 {
		t.Fatalf("delete: %v %+v", err, models())
	}
	// A member whose models do not decode, or that has too many.
	ed.set(`{"models": 1}`, nil)
	if _, err := save(model.Model{Name: "m2", API: model.APIOpenAI, BaseURL: "http://x"}); err == nil {
		t.Fatal("save over bad models succeeded")
	}
	full := make([]model.Model, config.MaxLLMModels)
	for i := range full {
		full[i] = model.Model{Name: fmt.Sprintf("m%d", i), API: model.APIOpenAI, BaseURL: "http://x"}
	}
	b, _ := json.Marshal(map[string]any{"models": full})
	ed.set(string(b), nil)
	if _, err := save(model.Model{Name: "extra", API: model.APIOpenAI, BaseURL: "http://x"}); code(err) != model.CodeBadRequest {
		t.Fatalf("past the bound: %v", err)
	}
	ed.set("{}", errors.New("read-only"))
	if _, err := save(model.Model{Name: "m2", API: model.APIOpenAI, BaseURL: "http://x"}); err == nil {
		t.Fatal("save with a failed edit succeeded")
	}
	if err := del(model.NameReq{Name: "m1"}); err == nil {
		t.Fatal("delete with a failed edit succeeded")
	}
}

func TestCheckModel(t *testing.T) {
	var status atomic.Int64
	status.Store(stdhttp.StatusUnauthorized)
	release := make(chan struct{})
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if strings.HasPrefix(r.URL.Path, "/hang") {
			<-release
			return
		}
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(strings.Repeat("e", 1000)))
	}))
	defer srv.Close()
	defer close(release)
	n := startNode(t, &config.LLMNode{Models: []config.LLMModel{
		{Name: "m1", API: model.APIOpenAI, BaseURL: srv.URL, APIKey: "k"},
		{Name: "slow", API: model.APIOpenAI, BaseURL: srv.URL + "/hang"},
	}}, nil)
	check := func(ctx context.Context, name string) (model.ModelView, error) {
		raw, _ := json.Marshal(model.NameReq{Name: name})
		res, err := n.Router.Invoke(ctx, local, model.MethodNodeModelCheck, raw)
		v, _ := res.(model.ModelView)
		return v, err
	}
	v, err := check(context.Background(), "m1")
	if err != nil || v.State != model.ModelError || !strings.Contains(v.Error, "upstream HTTP 401") || len(v.Error) > 600 {
		t.Fatalf("401 check = %+v %v", v, err)
	}
	status.Store(stdhttp.StatusNotFound)
	if v, err := check(context.Background(), "m1"); err != nil || v.State != model.ModelReady {
		t.Fatalf("404 check = %+v %v", v, err)
	}
	if _, err := check(context.Background(), "zz"); code(err) != model.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := check(ctx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("check past ctx: %v", err)
	}
	// A base URL that is no URL fails the probe at once.
	done := make(chan error, 1)
	n.probeModel(model.Model{BaseURL: "http://a b"}, func(err error) { done <- err })
	if err := <-done; err == nil {
		t.Fatal("bad URL probed")
	}
}

func TestCheckLoopRechecksErrors(t *testing.T) {
	tick, every, errEvery := checkTick, checkEvery, checkErrorEvery
	t.Cleanup(func() { checkTick, checkEvery, checkErrorEvery = tick, every, errEvery })
	checkTick, checkEvery, checkErrorEvery = 10*time.Millisecond, time.Hour, 0
	var hits atomic.Int64
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		hits.Add(1)
		w.WriteHeader(stdhttp.StatusInternalServerError)
	}))
	defer srv.Close()
	n := startNode(t, &config.LLMNode{Models: []config.LLMModel{
		{Name: "m1", API: model.APIOpenAI, BaseURL: srv.URL},
		{Name: "off", API: model.APIOpenAI, BaseURL: srv.URL, Disabled: true},
	}}, nil)
	// A model in error is checked again at checkErrorEvery, not checkEvery.
	waitFor(t, "rechecks", func() bool { return hits.Load() >= 3 })
	n.mu.Lock()
	v := n.viewLocked(n.models[1])
	n.mu.Unlock()
	if v.State != model.ModelDisabled {
		t.Fatalf("disabled model = %+v", v)
	}
}

func TestApplyResetsCheck(t *testing.T) {
	member := func(base string) *config.LLMNode {
		return &config.LLMNode{ID: "node-t", Models: []config.LLMModel{{Name: "m1", API: model.APIOpenAI, BaseURL: base}}}
	}
	n := startNode(t, member("http://127.0.0.1:1"), nil)
	n.setModelErr(model.Model{Name: "m1", API: model.APIOpenAI}, "down", true)
	if err := n.Apply(member("http://127.0.0.1:2")); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	r := *n.rt[key("m1", model.APIOpenAI)]
	n.mu.Unlock()
	if r.err != "" || r.checkedAt != 0 {
		t.Fatalf("check kept after the endpoint changed: %+v", r)
	}
}

func TestHelpers(t *testing.T) {
	if maskKey("") != "" || maskKey("short") != "****" || maskKey("sk-0123456789abcdef") != "sk-0****cdef" {
		t.Fatal("maskKey")
	}
	ue := &url.Error{Op: "Get", URL: "http://x", Err: context.DeadlineExceeded}
	if unwrapURLErr(ue) != context.DeadlineExceeded || unwrapURLErr(context.Canceled) != context.Canceled {
		t.Fatal("unwrapURLErr")
	}
	h := stdhttp.Header{}
	setAuth(h, model.Model{API: model.APIOpenAI, APIKey: "k"}, map[string]string{"OpenAI-Beta": "assistants=v2"})
	if h.Get("Authorization") != "Bearer k" || h.Get("OpenAI-Beta") != "assistants=v2" {
		t.Fatalf("openai headers = %v", h)
	}
	h = stdhttp.Header{}
	setAuth(h, model.Model{API: model.APIAnthropic, APIKey: "k"}, map[string]string{"anthropic-version": "2024-01-01", "anthropic-beta": "b"})
	if h.Get("x-api-key") != "k" || h.Get("anthropic-version") != "2024-01-01" || h.Get("anthropic-beta") != "b" {
		t.Fatalf("anthropic headers = %v", h)
	}
	if _, err := rewriteModel([]byte("[1]"), "x"); err == nil {
		t.Fatal("rewriteModel of an array")
	}
	if _, err := (Command{}).Run(context.Background(), nil); err == nil {
		t.Fatal("empty command ran")
	}
	if n, err := (&outBuf{}).Write(nil); n != 0 || err != nil {
		t.Fatal("empty write")
	}
}

// A node stops while its pusher waits for a change.
func TestPushStopsIdle(t *testing.T) {
	delay := pushDelay
	t.Cleanup(func() { pushDelay = delay })
	pushDelay = time.Millisecond
	n := startNode(t, &config.LLMNode{}, nil)
	waitFor(t, "the changes pushed", func() bool {
		n.push.mu.Lock()
		defer n.push.mu.Unlock()
		return len(n.push.dirty) == 0 && n.push.last != nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n.Stop(ctx)
}
