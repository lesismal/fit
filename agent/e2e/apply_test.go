package e2e

import (
	"net"
	"strings"
	"testing"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/agent/internal/agenttest"
)

// A config applied moves a role to its new member without dropping what is
// unchanged: a change of models keeps the links; a role left out stops.
func TestApplyKeepsLinks(t *testing.T) {
	mock := agenttest.StartMockLLM(t)
	nodeAddr := freeAddr(t)
	models := func(names ...string) string {
		var parts []string
		for _, n := range names {
			parts = append(parts, `{"name": "`+n+`", "api": "openai", "base_url": "`+mock.BaseURL("a")+`"}`)
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	nodeCfg := func(names ...string) string {
		return member("node", `{"id": "node-fixed", "listen": {"address": "`+nodeAddr+`", "password": "`+meshPass+`"}, "models": `+models(names...)+`}`)
	}
	nf := agenttest.Start(t, nodeCfg("a"), "")
	hf := agenttest.Start(t, member("hub", `{"peers": [{"address": "`+nodeAddr+`", "password": "`+meshPass+`", "role": "node"}]}`), "")
	h := hubOf(t, hf)
	agenttest.Eventually(t, "the link", func() bool { return h.Mesh.Online("node-fixed") })
	conn := h.Mesh.Peers()[0].ConnID

	if _, err := nf.Configs.Apply([]byte(nodeCfg("a", "b")), admin.SourceAdmin); err != nil {
		t.Fatal(err)
	}
	agenttest.Eventually(t, "the new model at the hub", func() bool {
		ms := h.Hub().Models()
		return len(ms.Models) == 2
	})
	if p := h.Mesh.Peers(); len(p) != 1 || p[0].ConnID != conn {
		t.Fatalf("the link was dropped: %+v", p)
	}
	// The hub's own member changes the peer's password: that link goes,
	// and comes back only with the right one.
	if _, err := hf.Configs.Apply([]byte(member("hub", `{"peers": [{"address": "`+nodeAddr+`", "password": "wrong", "role": "node"}]}`)), admin.SourceAdmin); err != nil {
		t.Fatal(err)
	}
	agenttest.Eventually(t, "the link refused", func() bool {
		ts := h.Mesh.Targets()
		return !h.Mesh.Online("node-fixed") && len(ts) == 1 && strings.Contains(ts[0].Error, "authentication")
	})
	// Left out, the node stops: its listener is gone.
	if _, err := nf.Configs.Apply([]byte(member("client", `{}`)), admin.SourceAdmin); err != nil {
		t.Fatal(err)
	}
	if got := nf.Apps.Running(); len(got) != 1 || got[0] != "agent.client" {
		t.Fatalf("running %v", got)
	}
	if nf.Role("node") != nil {
		t.Fatal("the node is still registered")
	}
	if c, err := net.Dial("tcp", nodeAddr); err == nil {
		c.Close()
		t.Fatal("the node's listener is still there")
	}
}
