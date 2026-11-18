package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// linkAs links to a mesh listener with wire alone, proving role with password.
// linkAs 只用 wire 连接互联的监听方，用 password 证明 role。
func linkAs(t *testing.T, addr, role, password string) (*wire.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wc, err := wire.Dial(ctx, "ws://"+addr+WSPath, wire.DialConfig{Engine: testEngine(), ConnConfig: wire.ConnConfig{
		DialAuth: &wire.DialAuth{Role: role, Password: password},
	}})
	if wc != nil {
		t.Cleanup(wc.Close)
	}
	return wc, err
}

func helloAs(ctx context.Context, wc *wire.Conn, id model.Identity, instance string) error {
	b, _ := json.Marshal(helloMsg{Identity: id, Instance: instance})
	_, err := wc.Call(ctx, wire.Head{Method: model.MethodHello}, b)
	return err
}

// A proxy that dials a node directly knows the node's proxy password, not its hub password: it cannot link as a
// hub, whether it proves the hub role with its own password or proves the proxy role and says hello as a hub.
// 直接连接节点的 proxy 知道节点的 proxy 密码，而不是 hub 密码：无论是用自己的密码证明 hub 角色，还是证明 proxy 角色却以
// hub 身份 hello，它都无法以 hub 身份连接。
func TestProxyCannotLinkAsHub(t *testing.T) {
	node, _ := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub, model.RoleProxy})
	addr := freeAddr(t)
	if err := node.Configure(model.ListenConfig{Addr: addr, Passwords: model.RolePasswords{Hub: "pw-hub", Proxy: "pw-proxy"}}, nil); err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)

	if _, err := linkAs(t, addr, model.RoleHub, "pw-proxy"); !errors.Is(err, wire.ErrAuth) {
		t.Fatalf("the proxy password proved the hub role: %v", err)
	}
	wc, err := linkAs(t, addr, model.RoleProxy, "pw-proxy")
	if err != nil {
		t.Fatal(err)
	}
	err = helloAs(ctx, wc, model.Identity{Role: model.RoleHub, ID: "h-fake"}, "x")
	if codeOf(err) != model.CodeForbidden || !strings.Contains(err.Error(), "proved the password of a \"proxy\"") {
		t.Fatalf("a proxy said hello as a hub: %v", err)
	}
	if len(node.PeersOf(model.RoleHub)) != 0 {
		t.Fatalf("a hub linked: %+v", node.PeersOf(model.RoleHub))
	}

	// The real hub and proxy link, each as itself. / 真正的 hub 与 proxy 各以自身角色连接。
	hub, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode})
	start(t, hub, "", model.DialReq{Addr: addr, Password: "pw-hub", Role: model.RoleNode})
	proxy, _ := newTestMesh(t, model.RoleProxy, "p1", []string{model.RoleNode})
	start(t, proxy, "", model.DialReq{Addr: addr, Password: "pw-proxy", Role: model.RoleNode})
	waitFor(t, "hello", func() bool { return node.Online("h1") && node.Online("p1") })
	if ps := node.PeersOf(model.RoleHub); len(ps) != 1 || ps[0].ID != "h1" {
		t.Fatalf("hubs: %+v", ps)
	}
}

// A node that holds the hub's node password and says hello with the ID of a node linked to the hub is refused,
// instead of being pooled with it and handed its calls; links of the same process (it dials the hub, and the hub
// dials it) are pooled.
// 持有 hub 的 node 密码、以已连接 hub 的节点 ID 发送 hello 的节点被拒绝，而不是与之合并为连接池并接手其调用；同一进程的连接
// （它拨号 hub，hub 也拨号它）则合并。
func TestImpostorNodeIDRefused(t *testing.T) {
	hubAddr, nodeAddr := freeAddr(t), freeAddr(t)
	hub, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode, model.RoleProxy})
	if err := hub.Configure(model.ListenConfig{Addr: hubAddr, Passwords: model.RolePasswords{Node: "pw-node", Proxy: "pw-proxy"}}, nil); err != nil {
		t.Fatal(err)
	}
	node, _ := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub, model.RoleProxy})
	if err := node.Configure(model.ListenConfig{Addr: nodeAddr, Passwords: model.RolePasswords{Hub: "pw-n1-hub"}},
		[]model.DialReq{{Addr: hubAddr, Password: "pw-node", Role: model.RoleHub}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the node", func() bool { return hub.Online("n1") })
	ctx := ctxT(t)

	for _, instance := range []string{"", "forged"} {
		wc, err := linkAs(t, hubAddr, model.RoleNode, "pw-node")
		if err != nil {
			t.Fatal(err)
		}
		err = helloAs(ctx, wc, model.Identity{Role: model.RoleNode, ID: "n1"}, instance)
		if codeOf(err) != model.CodeConflict {
			t.Fatalf("an impostor of n1 (instance %q): %v", instance, err)
		}
	}
	// A proxy cannot even prove the node role at the hub. / proxy 甚至无法在 hub 上证明 node 角色。
	if _, err := linkAs(t, hubAddr, model.RoleNode, "pw-proxy"); !errors.Is(err, wire.ErrAuth) {
		t.Fatalf("the proxy password proved the node role: %v", err)
	}

	// The hub dials the node too: both links are the node's, and pooled. / hub 也拨号节点：两条连接都属于该节点，合并为连接池。
	if err := hub.Dial(model.DialReq{Addr: nodeAddr, Password: "pw-n1-hub", Role: model.RoleNode}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two links", func() bool { return len(hub.Peers()) == 2 })
	for _, p := range hub.Peers() {
		if p.ID != "n1" {
			t.Fatalf("peer %+v", p)
		}
	}

	// Once the node's links are gone, its ID is free again. / 节点的连接都断开后，其 ID 再次可用。
	node.Stop()
	waitFor(t, "the node gone", func() bool { return !hub.Online("n1") })
	wc, err := linkAs(t, hubAddr, model.RoleNode, "pw-node")
	if err != nil {
		t.Fatal(err)
	}
	if err := helloAs(ctx, wc, model.Identity{Role: model.RoleNode, ID: "n1"}, "next"); err != nil {
		t.Fatalf("the ID of a node that left: %v", err)
	}
}

// The config's check of a listener's passwords.
// 配置对监听方密码的检查。
func TestCheckPasswords(t *testing.T) {
	accept := []string{model.RoleHub, model.RoleProxy}
	for _, c := range []struct {
		p   model.RolePasswords
		bad string
	}{
		{model.RolePasswords{Hub: "a", Proxy: "b"}, ""},
		{model.RolePasswords{Hub: "a"}, ""},
		{model.RolePasswords{}, "at least one"},
		{model.RolePasswords{Hub: "a", Proxy: "a"}, "the same"},
		{model.RolePasswords{Hub: "a", Node: "b"}, "never link here"},
	} {
		err := CheckPasswords(c.p, accept)
		if (c.bad == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.bad) {
			t.Errorf("%+v: %v", c.p, err)
		}
	}
}
