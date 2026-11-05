package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/llm/internal/llmtest"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/proxy"
)

// The three roles linked over wss://: a hub with a self-signed certificate
// it makes, keeps in its member and shows on its pages, which the node and
// the proxy pin; a node with a certificate an authority issued, which the
// proxy checks with ca_file. Hello, roles, calls and streamed responses go
// over TLS, the self-signed certificate outlives a restart, and the pages
// refuse what goes off loopback in the clear.
func TestMeshOverTLS(t *testing.T) {
	mock := llmtest.StartMockLLM(t)
	dir := t.TempDir()
	ca, err := certtest.NewCA("e2e CA")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.Issue(dir, "", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(dir, "ca.pem")
	if err := ca.WriteCert(caFile); err != nil {
		t.Fatal(err)
	}
	hubAddr, nodeAddr := freeAddr(t), freeAddr(t)
	hubStore := filepath.Join(dir, "hub.store")
	hf := llmtest.Start(t, member("hub", `{"console": `+llmtest.Console()+`, "listen": {"address": "`+hubAddr+`",
		"node_password": "node-pw", "proxy_password": "proxy-pw", "self_signed": true}}`), hubStore)
	h := hubOf(t, hf)
	fp := h.Mesh.Listen().Fingerprint
	if len(fp) != 64 || !h.Mesh.Listen().TLS {
		t.Fatalf("the hub's listener %+v", h.Mesh.Listen())
	}
	llmtest.Eventually(t, "the certificate written into the hub's member", func() bool {
		return strings.Contains(string(hf.Current()), "BEGIN CERTIFICATE")
	})
	if got := h.Mesh.Listen().Fingerprint; got != fp {
		t.Fatalf("the fingerprint changed once written: %s, was %s", got, fp)
	}
	hp := pages(t, h.Console().URL())
	var peers model.PeersResp
	if err := hp.RPC(model.MethodPeers, nil, &peers); err != nil {
		t.Fatal(err)
	}
	if !peers.Listen.TLS || !peers.Listen.SelfSigned || peers.Listen.Fingerprint != fp {
		t.Fatalf("the hub's pages %+v", peers.Listen)
	}
	if b, _ := json.Marshal(peers); strings.Contains(string(b), "PRIVATE KEY") || strings.Contains(string(b), "BEGIN CERTIFICATE") {
		t.Fatalf("the pages show the certificate or its key: %s", b)
	}

	nf := llmtest.Start(t, member("node", `{"listen": {"address": "`+nodeAddr+`", "proxy_password": "np-pw",
		"cert_file": "`+leaf.CertFile+`", "key_file": "`+leaf.KeyFile+`"},
		"peers": [{"address": "`+hubAddr+`", "password": "node-pw", "role": "hub", "pin": "`+fp+`"}],
		"models": [{"name": "m1", "api": "openai", "base_url": "`+mock.BaseURL("a")+`", "upstream": "up-a", "api_key": "sk-a"}]}`), "")
	cf := llmtest.Start(t, member("proxy", `{"console": `+llmtest.Console()+`, "api": {"address": "127.0.0.1:0"},
		"peers": [{"address": "`+hubAddr+`", "password": "proxy-pw", "role": "hub", "pin": "`+fp+`"},
			{"address": "`+nodeAddr+`", "password": "np-pw", "role": "node", "ca_file": "`+caFile+`"}]}`), "")
	n, c := nodeOf(t, nf), proxyOf(t, cf)
	llmtest.Eventually(t, "the links over TLS", func() bool {
		return h.Mesh.Online(n.Self().ID) && h.Mesh.Online(c.Self().ID) && c.Mesh.Online(n.Self().ID) && n.Mesh.Online(c.Self().ID)
	})
	for _, l := range c.Mesh.Peers() {
		if l.Role != model.RoleHub && l.Role != model.RoleNode {
			t.Fatalf("the proxy's link %+v", l)
		}
	}

	cp := pages(t, c.Console().URL())
	var key proxy.KeyCreateResp
	if err := cp.RPC(proxy.MethodProxyKeysCreate, proxy.KeyCreateReq{Name: "test"}, &key); err != nil {
		t.Fatal(err)
	}
	llmtest.Eventually(t, "the model in the catalog", func() bool {
		var cat []proxy.CatalogModel
		return cp.RPC(proxy.MethodProxyModels, nil, &cat) == nil && len(cat) == 1 && cat[0].Ready
	})
	a := newAgent(c.API(), key.Key)
	for i := 0; i < 4; i++ {
		status, body := a.call(t, "/v1/chat/completions", map[string]any{"model": "m1", "messages": []any{}}, nil)
		if status != 200 || !strings.Contains(body, "echo from a") {
			t.Fatalf("%d %s", status, body)
		}
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

	// The pages: a peer off loopback needs a pin or a CA file, a listener
	// off loopback needs TLS; a pin they set is kept in the member.
	if err := cp.RPC(model.MethodDial, model.DialReq{Addr: "192.0.2.1:9", Password: "x", Role: model.RoleHub}, nil); llmtest.CodeOf(err) != model.CodeBadRequest ||
		!strings.Contains(err.Error(), "set pin") {
		t.Fatalf("a plain peer off loopback: %v", err)
	}
	if err := cp.RPC(model.MethodListen, model.ListenReq{Addr: "0.0.0.0:0", Password: "x"}, nil); llmtest.CodeOf(err) != model.CodeBadRequest ||
		!strings.Contains(err.Error(), "self_signed") {
		t.Fatalf("a plain listener off loopback: %v", err)
	}
	pin := strings.Repeat("ab", 32)
	if err := cp.RPC(model.MethodDial, model.DialReq{Addr: "192.0.2.1:9", Password: "x", Role: model.RoleHub, Pin: pin}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cf.Current()), pin) || !strings.Contains(string(cf.Current()), caFile) {
		t.Fatalf("the proxy's member after a dial: %s", cf.Current())
	}
	// The hub's listener changed from its pages keeps its certificate.
	if err := hp.RPC(model.MethodListen, model.ListenReq{Addr: hubAddr, SelfSigned: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := h.Mesh.Listen().Fingerprint; got != fp {
		t.Fatalf("the hub's pages changed its certificate: %s, was %s", got, fp)
	}

	// The certificate outlives a restart: the pins stay good.
	hf.Stop()
	hf = llmtest.Start(t, `{}`, hubStore)
	h = hubOf(t, hf)
	if got := h.Mesh.Listen().Fingerprint; got != fp {
		t.Fatalf("the fingerprint after a restart: %s, was %s", got, fp)
	}
	llmtest.Eventually(t, "the links again", func() bool { return h.Mesh.Online(n.Self().ID) && h.Mesh.Online(c.Self().ID) })
}
