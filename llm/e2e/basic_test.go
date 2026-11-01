package e2e

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/agent/client"
	"github.com/lesismal/fit/agent/internal/agenttest"
	"github.com/lesismal/fit/agent/internal/model"
)

// A client linked to a node calls its model through the client's API: the
// node rewrites the model, uses its key, and streams the answer back.
func TestClientNodeCall(t *testing.T) {
	mock := agenttest.StartMockLLM(t)
	nodeAddr := freeAddr(t)
	nf := agenttest.Start(t, member("node", `{"console": `+agenttest.Console()+`, "listen": {"address": "`+nodeAddr+`", "password": "`+meshPass+`"},
		"models": [{"name": "m1", "api": "openai", "base_url": "`+mock.BaseURL("a")+`", "upstream": "up-a", "api_key": "sk-a"}]}`), "")
	cf := agenttest.Start(t, member("client", `{"console": `+agenttest.Console()+`, "api": {"address": "127.0.0.1:0"},
		"peers": [{"address": "`+nodeAddr+`", "password": "`+meshPass+`", "role": "node"}]}`), "")
	n, c := nodeOf(t, nf), clientOf(t, cf)
	agenttest.Eventually(t, "the link", func() bool { return c.Mesh.Online(n.Self().ID) })

	p := pages(t, c.Console().URL())
	var key client.KeyCreateResp
	if err := p.RPC(client.MethodClientKeysCreate, client.KeyCreateReq{Name: "test"}, &key); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key.Key, client.KeyPrefix) {
		t.Fatalf("key %+v", key)
	}
	if !strings.Contains(string(cf.Current()), key.KeyView.ID) || strings.Contains(string(cf.Current()), key.Key) {
		t.Fatalf("the config after the key: %s", cf.Current())
	}
	agenttest.Eventually(t, "the model in the catalog", func() bool {
		var cat []client.CatalogModel
		return p.RPC(client.MethodClientModels, nil, &cat) == nil && len(cat) == 1 && cat[0].Ready
	})

	a := newAgent(c.API(), key.Key)
	status, body := a.call(t, "/v1/chat/completions", map[string]any{"model": "m1", "messages": []any{}}, nil)
	if status != 200 || !strings.Contains(body, "echo from a") {
		t.Fatalf("%d %s", status, body)
	}
	if last := mock.Last("a"); last == nil || last.Model != "up-a" || last.Header["authorization"] != "Bearer sk-a" {
		t.Fatalf("the upstream saw %+v", last)
	}
	resp, err := a.post("/v1/chat/completions", map[string]any{"model": "m1", "stream": true, "chunks": 4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := events(t, resp.Body)
	resp.Body.Close()
	if len(ev) != 5 || ev[4] != "[DONE]" {
		t.Fatalf("events %q", ev)
	}
	// A wrong key, an unknown model.
	if status, _ := newAgent(c.API(), "nope").call(t, "/v1/chat/completions", map[string]any{"model": "m1"}, nil); status != 401 {
		t.Fatalf("wrong key: %d", status)
	}
	if status, _ := a.call(t, "/v1/chat/completions", map[string]any{"model": "zz"}, nil); status != 404 {
		t.Fatalf("unknown model: %d", status)
	}
	_ = model.APIOpenAI
}
