package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/llm/internal/llmtest"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/proxy"
)

// What the pages change is written into the roles' members, kept in
// admin.store, and read back from it when fit starts again: the IDs made
// at the first start, names, links, models, settings and keys.
func TestWriteBackAndRestart(t *testing.T) {
	mock := llmtest.StartMockLLM(t)
	dir := t.TempDir()
	hubAddr, nodeAddr := freeAddr(t), freeAddr(t)
	hubStore, nodeStore, proxyStore := filepath.Join(dir, "hub.store"), filepath.Join(dir, "node.store"), filepath.Join(dir, "proxy.store")
	hubCfg := member("hub", `{"console": `+llmtest.Console()+`, "listen": {"address": "`+hubAddr+`", "password": "`+meshPass+`"}}`)
	nodeCfg := member("node", `{"console": `+llmtest.Console()+`}`)
	proxyCfg := member("proxy", `{"console": `+llmtest.Console()+`, "api": {"address": "127.0.0.1:0"}}`)

	hf := llmtest.Start(t, hubCfg, hubStore)
	nf := llmtest.Start(t, nodeCfg, nodeStore)
	cf := llmtest.Start(t, proxyCfg, proxyStore)
	h, n, c := hubOf(t, hf), nodeOf(t, nf), proxyOf(t, cf)
	hp, np, cp := pages(t, h.Console().URL()), pages(t, n.Console().URL()), pages(t, c.Console().URL())
	ids := map[string]string{"hub": h.Self().ID, "node": n.Self().ID, "proxy": c.Self().ID}
	for role, f := range map[string]*llmtest.Fit{"hub": hf, "node": nf, "proxy": cf} {
		llmtest.Eventually(t, role+"'s ID written", func() bool { return strings.Contains(string(f.Current()), ids[role]) })
	}

	rpc := func(p *llmtest.Browser, method string, req, out any) {
		t.Helper()
		if err := p.RPC(method, req, out); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	// The node: a link, a listener, its profile, models and settings.
	rpc(np, model.MethodDial, model.DialReq{Addr: hubAddr, Password: meshPass, Role: model.RoleHub}, nil)
	rpc(np, model.MethodListen, model.ListenReq{Addr: nodeAddr, Password: "node-pw"}, nil)
	rpc(np, model.MethodNodeProfileSet, model.ProfileReq{Name: "gpu-box", Description: "the big one"}, nil)
	rpc(np, model.MethodNodeModelSave, model.Model{Name: "m1", API: "openai", BaseURL: mock.BaseURL("a"), APIKey: "sk-a", Upstream: "up-a"}, nil)
	rpc(np, model.MethodNodeModelSave, model.Model{Name: "m2", API: "anthropic", BaseURL: mock.BaseURL("a"), APIKey: "sk-a"}, nil)
	rpc(np, model.MethodNodeModelSave, model.Model{Name: "gone", API: "openai", BaseURL: mock.BaseURL("a")}, nil)
	rpc(np, model.MethodNodeModelDelete, model.NameReq{Name: "gone"}, nil)
	rpc(np, model.MethodNodeSettingsSet, model.NodeSettings{GPUSudo: false, RefreshInterval: "10s", JobTimeout: "2m"}, nil)
	if err := np.RPC(model.MethodNodeSettingsSet, model.NodeSettings{RefreshInterval: "1s"}, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
		t.Fatalf("a refresh too short: %v", err)
	}
	if err := np.RPC(model.MethodNodeModelSave, model.Model{Name: "a b", API: "openai", BaseURL: "http://h"}, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
		t.Fatalf("a bad model: %v", err)
	}
	if err := np.RPC(model.MethodDial, model.DialReq{Addr: "127.0.0.1:1", Password: "x", Role: model.RoleNode}, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
		t.Fatalf("a peer of the node's own role: %v", err)
	}
	// The proxy: a link, its profile and keys.
	rpc(cp, model.MethodDial, model.DialReq{Addr: hubAddr, Password: meshPass, Role: model.RoleHub}, nil)
	rpc(cp, proxy.MethodProxyProfileSet, model.ProfileReq{Name: "laptop"}, nil)
	var k1, k2 proxy.KeyCreateResp
	rpc(cp, proxy.MethodProxyKeysCreate, proxy.KeyCreateReq{Name: "one"}, &k1)
	rpc(cp, proxy.MethodProxyKeysCreate, proxy.KeyCreateReq{Name: "two"}, &k2)
	rpc(cp, proxy.MethodProxyKeysDelete, proxy.KeyIDReq{ID: k2.ID}, nil)
	// The hub: its name.
	rpc(hp, model.MethodHubProfileSet, model.ProfileReq{Name: "the-hub"}, nil)

	a := newAgent(c.API(), k1.Key)
	llmtest.Eventually(t, "the call through the hub", func() bool {
		status, _ := a.call(t, "/v1/chat/completions", map[string]any{"model": "m1"}, nil)
		return status == 200
	})
	if status, _ := newAgent(c.API(), k2.Key).call(t, "/v1/chat/completions", map[string]any{"model": "m1"}, nil); status != 401 {
		t.Fatalf("a deleted key: %d", status)
	}
	if strings.Contains(string(cf.Current()), k1.Key) {
		t.Fatal("a key itself in the config")
	}

	// Restart every role from its store; the files' configs are ignored.
	hf.Stop()
	nf.Stop()
	cf.Stop()
	hf = llmtest.Start(t, `{}`, hubStore)
	nf = llmtest.Start(t, `{}`, nodeStore)
	cf = llmtest.Start(t, `{}`, proxyStore)
	h, n, c = hubOf(t, hf), nodeOf(t, nf), proxyOf(t, cf)
	for role, id := range map[string]string{"hub": h.Self().ID, "node": n.Self().ID, "proxy": c.Self().ID} {
		if id != ids[role] {
			t.Fatalf("%s's ID %s, was %s", role, id, ids[role])
		}
	}
	if h.Self().Name != "the-hub" || n.Self().Name != "gpu-box" || c.Self().Name != "laptop" {
		t.Fatalf("names %q %q %q", h.Self().Name, n.Self().Name, c.Self().Name)
	}
	np, cp = pages(t, n.Console().URL()), pages(t, c.Console().URL())
	var models []model.Model
	rpc(np, model.MethodNodeModelList, nil, &models)
	if len(models) != 2 || models[0].Name != "m1" || models[0].APIKey != "****" || models[0].Upstream != "up-a" || models[1].API != "anthropic" {
		t.Fatalf("models %+v", models)
	}
	var s model.NodeSettings
	rpc(np, model.MethodNodeSettingsGet, nil, &s)
	if s.GPUSudo || s.RefreshInterval != "10s" || s.JobTimeout != "2m0s" {
		t.Fatalf("settings %+v", s)
	}
	var peers model.PeersResp
	rpc(np, model.MethodPeers, nil, &peers)
	if peers.Listen.Addr != nodeAddr || !peers.Listen.HasPassword || len(peers.Targets) != 1 || peers.Targets[0].Addr != hubAddr {
		t.Fatalf("node peers %+v", peers)
	}
	if b, _ := json.Marshal(peers); strings.Contains(string(b), "node-pw") || strings.Contains(string(b), meshPass) {
		t.Fatalf("a password in the pages: %s", b)
	}
	var keys []proxy.KeyView
	rpc(cp, proxy.MethodProxyKeysList, nil, &keys)
	if len(keys) != 1 || keys[0].ID != k1.ID || keys[0].Name != "one" {
		t.Fatalf("keys %+v", keys)
	}
	// The links come back, and the key still calls.
	a = newAgent(c.API(), k1.Key)
	llmtest.Eventually(t, "a call after the restart", func() bool {
		status, _ := a.call(t, "/v1/chat/completions", map[string]any{"model": "m1"}, nil)
		return status == 200
	})
	if last := mock.Last("a"); last.Model != "up-a" {
		t.Fatalf("upstream saw %+v", last)
	}
}
