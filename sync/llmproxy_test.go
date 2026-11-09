package fitsync

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// llmSeen keeps the llm.proxy's API each llm_env transform was given.
var (
	llmMu   sync.Mutex
	llmSeen []*config.LLMProxyAPI
)

// llmEnv is a transform that needs an llm.proxy, as embed does without a
// URL, and notes the one it is given.
type llmEnv struct{}

func (*llmEnv) Provision(ctx *module.Context) error {
	llmMu.Lock()
	llmSeen = append(llmSeen, ctx.LLMProxy())
	llmMu.Unlock()
	if ctx.LLMProxy() == nil {
		return errors.New("no llm.proxy")
	}
	return nil
}

func (*llmEnv) Apply(r *Record, emit func(*Record)) error { emit(r); return nil }

func init() {
	module.Register(module.Info{ID: Transforms + ".test_llm_env", New: func() any { return new(llmEnv) }, Doc: "Notes the llm.proxy it is given: for tests."})
}

func lastLLM() *config.LLMProxyAPI {
	llmMu.Lock()
	defer llmMu.Unlock()
	if len(llmSeen) == 0 {
		return nil
	}
	return llmSeen[len(llmSeen)-1]
}

func llmConfig(t *testing.T, dir string, proxy bool) *config.Config {
	t.Helper()
	src := `{"sync": {"state_dir": "` + dir + `", "pipelines": [{"name": "p", "mode": "once", "source": {"module": "generate", "params": {"records": 3}},
		"transforms": [{"module": "test_llm_env"}], "sinks": [{"name": "s", "sink": {"module": "memory", "params": {"store": "llm"}}}]}]}`
	if proxy {
		src += `, "llm": {"proxy": {"api": {"address": "127.0.0.1:9999"}}}`
	}
	c, err := config.Load([]byte(src + "}"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func syncRole(t *testing.T) app.Info {
	for _, info := range app.Roles() {
		if info.ID == ID {
			return info
		}
	}
	t.Fatal("no sync role")
	return app.Info{}
}

// TestLLMProxyMember: the role's member carries the config's llm.proxy's
// API to the modules, as checking, applying, running and testing load
// them, and is written as the sync alone.
func TestLLMProxyMember(t *testing.T) {
	info := syncRole(t)
	if info.Member(&config.Config{}) != nil {
		t.Fatal("a member of no sync")
	}
	with, without := llmConfig(t, t.TempDir(), true), llmConfig(t, t.TempDir(), false)
	m, ok := info.Member(with).(*Member)
	if !ok || m.Sync != with.Sync || m.LLMProxy == nil || m.LLMProxy.Address != "127.0.0.1:9999" {
		t.Fatalf("%#v", info.Member(with))
	}
	if m := info.Member(without).(*Member); m.LLMProxy != nil {
		t.Fatal(m.LLMProxy)
	}
	if m := info.Member(&config.Config{Sync: &config.Sync{}, LLM: &config.LLM{}}).(*Member); m.LLMProxy != nil {
		t.Fatal(m.LLMProxy)
	}
	b, _ := json.Marshal(m)
	s, _ := json.Marshal(with.Sync)
	if string(b) != string(s) {
		t.Fatalf("%s, want %s", b, s)
	}
	if err := info.Check(with); err != nil {
		t.Fatal(err)
	}
	if err := info.Check(without); err == nil {
		t.Fatal("checked without an llm.proxy")
	}
	r, err := info.New(&app.Host{Log: nil})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop(context.Background())
	if err := r.Apply(m); err != nil {
		t.Fatal(err)
	}
	if lastLLM() != m.LLMProxy {
		t.Fatal(lastLLM())
	}
	if err := r.Apply(without.Sync); err != nil {
		t.Fatal(err)
	}
	st, err := RunPipeline(context.Background(), with.Sync, "p", Options{LLMProxy: m.LLMProxy})
	if err != nil || st.State != "finished" || lastLLM() != m.LLMProxy {
		t.Fatal(st, err, lastLLM())
	}
	// A pipeline tested or dry run is given the llm.proxy of the runtime
	// registered for its engine.
	e := new(fib.Engine)
	rt := NewRuntime(Options{})
	rt.SetLLMProxy(m.LLMProxy)
	Register(e, rt)
	defer Unregister(e, rt)
	if env := envFor(e); env.LLMProxy != m.LLMProxy || env.Engine != e {
		t.Fatal(env)
	}
	if _, err := TestPipeline(context.Background(), &with.Sync.Pipelines[0], e); err != nil {
		t.Fatal(err)
	}
	if err := CheckWith(nil, nil); err != nil {
		t.Fatal(err)
	}
}
