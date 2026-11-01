package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesismal/fit/llm/internal/llmtest"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/proxy"
)

// A hub between two nodes and a proxy: it aggregates the nodes' models,
// relays the proxy's calls to a node that serves the model, streaming both
// ways, moves a call to the other node when one cannot serve it, and
// manages a node only while the node allows it, writing the change into the
// node's config.
func TestHubRelayAndManage(t *testing.T) {
	mock := llmtest.StartMockLLM(t)
	hubAddr, node2Addr := freeAddr(t), freeAddr(t)
	hf := llmtest.Start(t, member("hub", `{"console": `+llmtest.Console()+`, "listen": {"address": "`+hubAddr+`", "password": "`+meshPass+`"},
		"peers": [{"address": "`+node2Addr+`", "password": "`+meshPass+`", "role": "node"}]}`), "")
	// node1 dials the hub; the hub dials node2.
	n1f := llmtest.Start(t, member("node", `{"console": `+llmtest.Console()+`, "settings": {"allow_hub_manage": true},
		"peers": [{"address": "`+hubAddr+`", "password": "`+meshPass+`", "role": "hub"}],
		"models": [{"name": "shared", "api": "openai", "base_url": "`+mock.BaseURL("n1")+`", "api_key": "sk-n1"}]}`), "")
	n2f := llmtest.Start(t, member("node", `{"console": `+llmtest.Console()+`, "listen": {"address": "`+node2Addr+`", "password": "`+meshPass+`"},
		"models": [{"name": "shared", "api": "openai", "base_url": "`+mock.BaseURL("n2")+`", "api_key": "sk-n2"},
		{"name": "only2", "api": "openai", "base_url": "`+mock.BaseURL("n2")+`", "api_key": "sk-n2"}]}`), "")
	cf := llmtest.Start(t, member("proxy", `{"console": `+llmtest.Console()+`, "api": {"address": "127.0.0.1:0"},
		"peers": [{"address": "`+hubAddr+`", "password": "`+meshPass+`", "role": "hub"}]}`), "")
	h, n1, n2, c := hubOf(t, hf), nodeOf(t, n1f), nodeOf(t, n2f), proxyOf(t, cf)
	hp, n1p, cp := pages(t, h.Console().URL()), pages(t, n1.Console().URL()), pages(t, c.Console().URL())

	llmtest.Eventually(t, "the hub's models", func() bool {
		var ms model.HubModels
		if hp.RPC(model.MethodHubModels, nil, &ms) != nil || len(ms.Models) != 2 {
			return false
		}
		return ms.Models[0].Name == "only2" && ms.Models[1].Name == "shared" && ms.Models[1].Ready == 2
	})
	llmtest.Eventually(t, "the proxy's catalog", func() bool {
		var cat []proxy.CatalogModel
		return cp.RPC(proxy.MethodProxyModels, nil, &cat) == nil && len(cat) == 2 && cat[0].Ready && cat[1].Ready
	})
	var key proxy.KeyCreateResp
	if err := cp.RPC(proxy.MethodProxyKeysCreate, proxy.KeyCreateReq{Name: "k"}, &key); err != nil {
		t.Fatal(err)
	}
	a := newAgent(c.API(), key.Key)

	// Both nodes take calls; a large body streams through the hub.
	for range 6 {
		if status, body := a.call(t, "/v1/chat/completions", map[string]any{"model": "shared"}, nil); status != 200 {
			t.Fatalf("%d %s", status, body)
		}
	}
	if s1, s2 := mock.Stats("n1").Calls, mock.Stats("n2").Calls; s1 == 0 || s2 == 0 {
		t.Fatalf("calls n1 %d n2 %d", s1, s2)
	}
	pad := strings.Repeat("q", 3<<20)
	if status, body := a.call(t, "/v1/chat/completions", `{"model":"only2","pad":"`+pad+`"}`, nil); status != 200 {
		t.Fatalf("a large body through the hub: %d %.200s", status, body)
	}
	if got := mock.Stats("n2").BodyBytes; got != len(pad)+len(`{"model":"only2","pad":""}`) {
		t.Fatalf("node2's upstream got %d bytes", got)
	}
	resp, err := a.post("/v1/chat/completions", map[string]any{"model": "only2", "stream": true, "chunks": 5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev := events(t, resp.Body); len(ev) != 6 {
		t.Fatalf("events through the hub %q", ev)
	}
	resp.Body.Close()

	// node2's upstream is down, which its check finds: the hub moves every
	// call to node1. (An upstream's own answer, 503 among them, is passed
	// on as it is.)
	mock.Down("n2", true)
	n2p := pages(t, n2.Console().URL())
	var v model.ModelView
	if err := n2p.RPC(model.MethodNodeModelCheck, model.NameReq{Name: "shared"}, &v); err != nil || v.State != model.ModelError {
		t.Fatalf("check %+v %v", v, err)
	}
	llmtest.Eventually(t, "the hub to hear of it", func() bool {
		var ms model.HubModels
		return hp.RPC(model.MethodHubModels, nil, &ms) == nil && len(ms.Models) == 2 && ms.Models[1].Ready == 1
	})
	before := mock.Stats("n1").Calls
	for range 4 {
		if status, body := a.call(t, "/v1/chat/completions", map[string]any{"model": "shared"}, nil); status != 200 {
			t.Fatalf("failover: %d %s", status, body)
		}
	}
	if got := mock.Stats("n1").Calls - before; got != 4 {
		t.Fatalf("node1 took %d of 4 calls", got)
	}
	mock.Down("n2", false)

	// Management: node1 allows it, node2 does not.
	call := func(id, method string, req any, out any) error {
		raw, _ := json.Marshal(req)
		return hp.RPC(model.MethodHubNodeCall, model.HubNodeCallReq{ID: id, Method: method, Req: raw}, out)
	}
	if err := call(n2.Self().ID, model.MethodNodeModelList, nil, nil); llmtest.CodeOf(err) != model.CodeForbidden {
		t.Fatalf("node2 managed: %v", err)
	}
	var saved model.Model
	if err := call(n1.Self().ID, model.MethodNodeModelSave, model.Model{Name: "via-hub", API: "openai", BaseURL: mock.BaseURL("n1"), APIKey: "sk-n1"}, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != "****" || !strings.Contains(string(n1f.Current()), `"via-hub"`) {
		t.Fatalf("saved %+v; node1's config %s", saved, n1f.Current())
	}
	if err := call(n1.Self().ID, "/node/settings/set", nil, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
		t.Fatalf("settings through the hub: %v", err)
	}
	// Revoked on node1's pages: refused at once.
	var s model.NodeSettings
	if err := n1p.RPC(model.MethodNodeSettingsGet, nil, &s); err != nil || !s.AllowHubManage || !s.GPUSudo || s.JobTimeout != "1h0m0s" {
		t.Fatalf("settings %+v %v", s, err)
	}
	s.AllowHubManage = false
	if err := n1p.RPC(model.MethodNodeSettingsSet, s, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(n1.Self().ID, model.MethodNodeModelList, nil, nil); llmtest.CodeOf(err) != model.CodeForbidden {
		t.Fatalf("after the revoke: %v", err)
	}
	if strings.Contains(string(n1f.Current()), "allow_hub_manage") {
		t.Fatalf("the revoke was not written: %s", n1f.Current())
	}
}
