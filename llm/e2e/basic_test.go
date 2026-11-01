package e2e

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/llm/internal/llmtest"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/proxy"
)

// A proxy linked to a node calls its model through the proxy's API: the
// node rewrites the model, uses its key, and streams the answer back.
func TestProxyNodeCall(t *testing.T) {
	mock := llmtest.StartMockLLM(t)
	nodeAddr := freeAddr(t)
	nf := llmtest.Start(t, member("node", `{"console": `+llmtest.Console()+`, "listen": {"address": "`+nodeAddr+`", "password": "`+meshPass+`"},
		"models": [{"name": "m1", "api": "openai", "base_url": "`+mock.BaseURL("a")+`", "upstream": "up-a", "api_key": "sk-a"}]}`), "")
	cf := llmtest.Start(t, member("proxy", `{"console": `+llmtest.Console()+`, "api": {"address": "127.0.0.1:0"},
		"peers": [{"address": "`+nodeAddr+`", "password": "`+meshPass+`", "role": "node"}]}`), "")
	n, c := nodeOf(t, nf), proxyOf(t, cf)
	llmtest.Eventually(t, "the link", func() bool { return c.Mesh.Online(n.Self().ID) })

	p := pages(t, c.Console().URL())
	var key proxy.KeyCreateResp
	if err := p.RPC(proxy.MethodProxyKeysCreate, proxy.KeyCreateReq{Name: "test"}, &key); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key.Key, proxy.KeyPrefix) {
		t.Fatalf("key %+v", key)
	}
	if !strings.Contains(string(cf.Current()), key.KeyView.ID) || strings.Contains(string(cf.Current()), key.Key) {
		t.Fatalf("the config after the key: %s", cf.Current())
	}
	llmtest.Eventually(t, "the model in the catalog", func() bool {
		var cat []proxy.CatalogModel
		return p.RPC(proxy.MethodProxyModels, nil, &cat) == nil && len(cat) == 1 && cat[0].Ready
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
