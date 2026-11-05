package mesh

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

func tokenRec(sec linktoken.Secret, exp int64) config.LinkToken {
	return config.LinkToken{ID: sec.ID, Name: sec.ID, Hash: sec.Hash(), ExpiresAt: exp}
}

// dialToken dials m's listener with sec and says hello as id.
func dialToken(t *testing.T, m *Mesh, sec linktoken.Secret, id model.Identity) (*wire.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wc, err := wire.Dial(ctx, "ws://"+m.ListenAddr()+WSPath, wire.DialConfig{Engine: testEngine(), ConnConfig: wire.ConnConfig{
		Token: &sec, Handlers: func(*wire.Conn) wire.Handlers { return wire.Handlers{} },
	}})
	if err != nil {
		return nil, err
	}
	t.Cleanup(wc.Close)
	_, err = wc.Call(ctxT(t), wire.Head{Method: model.MethodHello}, hello(id))
	return wc, err
}

// A listener's token links a peer of any role it takes, binds the peer's ID to it, and closes its links when it is
// revoked or expires; a listener with tokens alone takes no password.
// 监听方的令牌可连接它接受的任何角色的对端，把对端 ID 绑定到令牌，并在令牌被吊销或过期时关闭其连接；只有令牌的监听方不接受密码。
func TestMeshTokens(t *testing.T) {
	a, b := linktoken.New(), linktoken.New()
	hub, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode, model.RoleProxy})
	var bound []string
	hub.Tokens().OnBind = func(tok, member string) { bound = append(bound, tok+" "+member) }
	hub.Tokens().Set([]config.LinkToken{tokenRec(a, 0), tokenRec(b, 0)})
	if err := hub.SetListen(model.ListenConfig{Addr: freeAddr(t)}); err != nil {
		t.Fatal(err)
	}
	// The mesh dials a peer with a token, and the peer is a node.
	node, _ := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub})
	if err := node.Configure(model.ListenConfig{}, []model.DialReq{{Addr: hub.ListenAddr(), Token: a.String(), Role: model.RoleHub}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the node's hello", func() bool { return hub.Online("n1") })
	if ts := node.Targets(); len(ts) != 1 || ts[0].TokenID != a.ID {
		t.Fatalf("targets %+v", ts)
	}
	if rs := node.DialReqs(); rs[0].Token != a.String() || rs[0].Password != "" {
		t.Fatalf("dial reqs %+v", rs)
	}
	ps := hub.Peers()
	if len(ps) != 1 || ps[0].TokenID != a.ID || ps[0].SharedPassword {
		t.Fatalf("peers %+v", ps)
	}
	// The same token links a proxy: tokens are of no role.
	if _, err := dialToken(t, hub, a, model.Identity{Role: model.RoleProxy, ID: "p1"}); err != nil {
		t.Fatal(err)
	}
	if len(bound) != 2 {
		t.Fatalf("bound %v", bound)
	}
	// Another token cannot say hello as n1.
	if _, err := dialToken(t, hub, b, model.Identity{Role: model.RoleNode, ID: "n1"}); codeOf(err) != model.CodeForbidden ||
		!strings.Contains(err.Error(), "bound to another token") {
		t.Fatalf("took over n1: %v", err)
	}
	// No password: the listener has none.
	if _, err := dialPW(t, hub.ListenAddr(), "pw"); err == nil {
		t.Fatal("a password linked")
	}
	views := hub.Tokens().List()
	if len(views) != 2 {
		t.Fatalf("views %+v", views)
	}
	// Revoking a closes the node's link; the node is refused from then on.
	hub.Tokens().Set([]config.LinkToken{tokenRec(b, 0)})
	waitFor(t, "the node to go", func() bool { return !hub.Online("n1") })
	waitFor(t, "the node's error", func() bool {
		ts := node.Targets()
		return len(ts) == 1 && strings.Contains(ts[0].Error, "no such token")
	})
	// b expires: its link closes.
	wc, err := dialToken(t, hub, b, model.Identity{Role: model.RoleNode, ID: "n2"})
	if err != nil {
		t.Fatal(err)
	}
	hub.Tokens().Set([]config.LinkToken{tokenRec(b, time.Now().Add(time.Second).Unix())})
	select {
	case <-wc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the expired token's link stayed")
	}
	// A dial target changed from a token to a password, and back.
	if err := node.Dial(model.DialReq{Addr: hub.ListenAddr(), Password: "pw", Role: model.RoleHub}); err != nil {
		t.Fatal(err)
	}
	if rs := node.DialReqs(); rs[0].Token != "" || rs[0].Password != "pw" {
		t.Fatalf("dial reqs %+v", rs)
	}
	if err := node.Dial(model.DialReq{Addr: hub.ListenAddr(), Token: "nope", Role: model.RoleHub}); codeOf(err) != model.CodeBadRequest {
		t.Fatalf("a bad token: %v", err)
	}
}

// A password link is listed as one, which the pages warn of.
// 密码连接被标记出来，页面会对此给出警告。
func TestMeshSharedPassword(t *testing.T) {
	hub, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode, model.RoleProxy})
	start(t, hub, freeAddr(t))
	accepted(t, hub, "pw", model.Identity{Role: model.RoleProxy, ID: "p1"})
	ps := hub.Peers()
	if len(ps) != 1 || !ps[0].SharedPassword || ps[0].TokenID != "" {
		t.Fatalf("peers %+v", ps)
	}
}
