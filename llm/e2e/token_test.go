package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/llm/internal/llmtest"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/role"
)

// A hub over TLS that takes tokens alone: its pages issue a token for a node
// and one for a proxy, shown once and kept as hashes; the node and the proxy
// link with them, their IDs bound to them; the pages change a token's expiry
// and revoke one, which closes its links at once.
func TestMeshTokens(t *testing.T) {
	hubAddr := freeAddr(t)
	hf := llmtest.Start(t, member("hub", `{"console": `+llmtest.Console()+`, "listen": {"address": "`+hubAddr+`", "self_signed": true}}`), "")
	h := hubOf(t, hf)
	fp := h.Mesh.Listen().Fingerprint
	hp := pages(t, h.Console().URL())
	issue := func(name string) linktoken.Issued {
		var is linktoken.Issued
		if err := hp.RPC(role.MethodTokensCreate, linktoken.IssueReq{Name: name, TTL: "30d"}, &is); err != nil {
			t.Fatal(err)
		}
		return is
	}
	nt, pt := issue("gpu-1"), issue("laptop")
	cur := string(hf.Current())
	if strings.Contains(cur, nt.Token) || !strings.Contains(cur, `"tokens"`) {
		t.Fatalf("the member keeps the token: %s", cur)
	}
	if err := hp.RPC(role.MethodTokensCreate, linktoken.IssueReq{Name: ""}, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
		t.Fatalf("an empty name: %v", err)
	}

	nf := llmtest.Start(t, member("node", `{"peers": [{"address": "`+hubAddr+`", "token": "`+nt.Token+`", "role": "hub", "pin": "`+fp+`"}]}`), "")
	cf := llmtest.Start(t, member("proxy", `{"peers": [{"address": "`+hubAddr+`", "token": "`+pt.Token+`", "role": "hub", "pin": "`+fp+`"}]}`), "")
	n, c := nodeOf(t, nf), proxyOf(t, cf)
	llmtest.Eventually(t, "the links with tokens", func() bool {
		return h.Mesh.Online(n.Self().ID) && h.Mesh.Online(c.Self().ID)
	})
	llmtest.Eventually(t, "the bindings in the hub's member", func() bool {
		cur := string(hf.Current())
		return strings.Contains(cur, n.Self().ID) && strings.Contains(cur, c.Self().ID)
	})
	var list struct {
		Tokens []linktoken.View `json:"tokens"`
	}
	if err := hp.RPC(role.MethodTokensList, nil, &list); err != nil || len(list.Tokens) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	for _, v := range list.Tokens {
		if len(v.Links) != 1 || v.LastUsed == 0 {
			t.Fatalf("token %+v", v)
		}
	}
	var peers model.PeersResp
	if err := hp.RPC(model.MethodPeers, nil, &peers); err != nil {
		t.Fatal(err)
	}
	for _, p := range peers.Conns {
		if p.TokenID == "" || p.SharedPassword {
			t.Fatalf("conn %+v", p)
		}
	}
	if b, _ := json.Marshal(peers); strings.Contains(string(b), nt.Token) {
		t.Fatal("the pages show a token")
	}

	// The node's token: expiry changed, then revoked.
	if err := hp.RPC(role.MethodTokensExpiry, role.TokenIDReq{ID: nt.ID, ExpiryReq: linktoken.ExpiryReq{TTL: "1h"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := hp.RPC(role.MethodTokensExpiry, role.TokenIDReq{ID: "0000000000000000"}, nil); llmtest.CodeOf(err) != model.CodeNotFound {
		t.Fatalf("expiry of none: %v", err)
	}
	if err := hp.RPC(role.MethodTokensExpiry, role.TokenIDReq{ID: nt.ID, ExpiryReq: linktoken.ExpiryReq{TTL: "x"}}, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
		t.Fatalf("a bad ttl: %v", err)
	}
	if err := hp.RPC(role.MethodTokensRevoke, role.TokenIDReq{ID: nt.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := hp.RPC(role.MethodTokensRevoke, role.TokenIDReq{ID: nt.ID}, nil); llmtest.CodeOf(err) != model.CodeNotFound {
		t.Fatalf("revoke twice: %v", err)
	}
	llmtest.Eventually(t, "the node to lose the hub", func() bool { return !h.Mesh.Online(n.Self().ID) })
	if !h.Mesh.Online(c.Self().ID) {
		t.Fatal("the proxy lost the hub too")
	}
	if h.LinkTokens() == nil {
		t.Fatal("no token manager")
	}
}
