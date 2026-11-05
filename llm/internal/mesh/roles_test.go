package mesh

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
	"github.com/lesismal/fit/logs"
)

// dialPW links to a mesh listener with wire alone, proving pw.
// dialPW 只用 wire 连接互联的监听方，证明 pw。
func dialPW(t *testing.T, addr, pw string) (*wire.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wc, err := wire.Dial(ctx, "ws://"+addr+WSPath, wire.DialConfig{Engine: testEngine(), ConnConfig: wire.ConnConfig{
		Auth: wire.Password(pw), Handlers: func(*wire.Conn) wire.Handlers { return wire.Handlers{} },
	}})
	if wc != nil {
		t.Cleanup(wc.Close)
	}
	return wc, err
}

// roleListener starts a mesh of role, accepting the roles it links with, on listen, with a method only hubs and
// nodes may call, which counts its calls.
// roleListener 在 listen 上启动 role 角色的互联，接受它所连接的角色，并带一个只有中心与节点可以调用、会计数的方法。
func roleListener(t *testing.T, role string, listen model.ListenConfig) (*Mesh, *atomic.Int32) {
	t.Helper()
	accept := map[string][]string{
		model.RoleHub:   {model.RoleNode, model.RoleProxy},
		model.RoleNode:  {model.RoleHub, model.RoleProxy},
		model.RoleProxy: {model.RoleHub, model.RoleNode},
	}[role]
	m, r := newTestMesh(t, role, role+"-self", accept)
	var calls atomic.Int32
	Handle(r, "/manage", []string{model.RoleHub, model.RoleNode}, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		calls.Add(1)
		return nil, nil
	})
	listen.Addr = freeAddr(t)
	if err := m.SetListen(listen); err != nil {
		t.Fatal(err)
	}
	return m, &calls
}

// refused says hello as id with pw and checks that the hello is refused, that no method runs on the link after it,
// and that the link is closed.
// refused 以 pw 证明、以 id hello，检查 hello 被拒绝、之后该连接上没有任何方法运行，且连接被关闭。
func refused(t *testing.T, m *Mesh, calls *atomic.Int32, pw string, id model.Identity) {
	t.Helper()
	wc, err := dialPW(t, m.ListenAddr(), pw)
	if err != nil {
		t.Fatalf("%s with its password: %v", id.Role, err)
	}
	ctx := ctxT(t)
	if _, err := wc.Call(ctx, wire.Head{Method: model.MethodHello}, hello(id)); codeOf(err) != model.CodeForbidden {
		t.Fatalf("hello as a %s: %v", id.Role, err)
	}
	if _, err := wc.Call(ctx, wire.Head{Method: "/manage"}, []byte(`{}`)); err == nil {
		t.Fatalf("a method ran after a refused hello as a %s", id.Role)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the method ran %d times", n)
	}
	select {
	case <-wc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the refused link is still open")
	}
	for _, p := range m.Peers() {
		if p.ID == id.ID {
			t.Fatalf("%s is linked: %+v", id.ID, p)
		}
	}
}

// accepted says hello as id with pw and checks that it is taken, and that a method of its role runs.
// accepted 以 pw 证明、以 id hello，检查被接受，且其角色可调用的方法得以运行。
func accepted(t *testing.T, m *Mesh, pw string, id model.Identity) {
	t.Helper()
	wc, err := dialPW(t, m.ListenAddr(), pw)
	if err != nil {
		t.Fatalf("%s: %v", id.Role, err)
	}
	ctx := ctxT(t)
	if _, err := wc.Call(ctx, wire.Head{Method: model.MethodHello}, hello(id)); err != nil {
		t.Fatalf("hello as a %s: %v", id.Role, err)
	}
	if id.Role != model.RoleProxy {
		if _, err := wc.Call(ctx, wire.Head{Method: "/manage"}, []byte(`{}`)); err != nil {
			t.Fatalf("a %s's method: %v", id.Role, err)
		}
	}
}

// The password a dialer proves decides its role: on a hub's listener a proxy's password says hello as a proxy and
// not as a node; on a node's listener it does not say hello as a hub; on a proxy's listener a node's password does
// not say hello as a hub. A refused hello closes the link before any method runs on it.
// 拨号方证明的密码决定其角色：在中心的监听上，代理的密码只能以代理而不能以节点 hello；在节点的监听上不能以中心 hello；在代理的
// 监听上，节点的密码不能以中心 hello。被拒绝的 hello 在该连接上运行任何方法之前就关闭连接。
func TestRolePasswords(t *testing.T) {
	node := model.Identity{Role: model.RoleNode, ID: "node-1"}
	proxy := model.Identity{Role: model.RoleProxy, ID: "proxy-1"}
	hub := model.Identity{Role: model.RoleHub, ID: "hub-1"}

	h, calls := roleListener(t, model.RoleHub, model.ListenConfig{NodePassword: "np", ProxyPassword: "pp"})
	refused(t, h, calls, "pp", model.Identity{Role: model.RoleNode, ID: "proxy-as-node"})
	accepted(t, h, "pp", proxy)
	accepted(t, h, "np", node)
	if _, err := dialPW(t, h.ListenAddr(), "pw"); !errors.Is(err, wire.ErrAuth) {
		t.Fatalf("a password of no role: %v", err)
	}

	n, calls := roleListener(t, model.RoleNode, model.ListenConfig{HubPassword: "hp", ProxyPassword: "pp"})
	refused(t, n, calls, "pp", model.Identity{Role: model.RoleHub, ID: "proxy-as-hub"})
	accepted(t, n, "hp", hub)

	p, calls := roleListener(t, model.RoleProxy, model.ListenConfig{HubPassword: "hp", NodePassword: "np"})
	refused(t, p, calls, "np", model.Identity{Role: model.RoleHub, ID: "node-as-hub"})
	accepted(t, p, "hp", hub)
	accepted(t, p, "np", node)
}

// password is the password of every role without one of its own: a node's listener with a hub's password of its
// own and password takes a proxy with password, but not a hub; a hub's listener with password alone takes both
// roles with it, as before, and warns that it does.
// password 是没有自己密码的各角色共用的密码：有中心自己的密码和 password 的节点监听，接受用 password 的代理，但不接受用它的中心；
// 只有 password 的中心监听照旧以它接受两种角色，并发出警告。
func TestRolePasswordFallback(t *testing.T) {
	n, calls := roleListener(t, model.RoleNode, model.ListenConfig{Password: "pw", HubPassword: "hp"})
	refused(t, n, calls, "pw", model.Identity{Role: model.RoleHub, ID: "proxy-as-hub"})
	accepted(t, n, "pw", model.Identity{Role: model.RoleProxy, ID: "proxy-1"})
	accepted(t, n, "hp", model.Identity{Role: model.RoleHub, ID: "hub-1"})

	var out syncBuffer
	root, err := logs.New(nil, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	r := NewRouter()
	Handle(r, "/manage", []string{model.RoleNode}, 0, func(context.Context, model.Identity, *struct{}) (any, error) { return nil, nil })
	h := New(Options{Engine: testEngine(), Self: func() model.Identity { return model.Identity{Role: model.RoleHub, ID: "h1"} },
		AcceptRoles: []string{model.RoleNode, model.RoleProxy}, Log: root.Logger("server")}, r)
	t.Cleanup(h.Stop)
	if err := h.SetListen(model.ListenConfig{Addr: freeAddr(t), Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	accepted(t, h, "pw", model.Identity{Role: model.RoleNode, ID: "node-1"})
	accepted(t, h, "pw", model.Identity{Role: model.RoleProxy, ID: "proxy-1"})
	if s := out.String(); !strings.Contains(s, "one password from several roles") || !strings.Contains(s, "node,proxy") {
		t.Fatalf("no warning: %s", s)
	}
	out.Reset()
	if err := h.SetListen(model.ListenConfig{Addr: freeAddr(t), Password: "pw", ProxyPassword: "pp"}); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); strings.Contains(s, "several roles") {
		t.Fatalf("a warning for distinct passwords: %s", s)
	}
}

// A listener needs a password of a role it links with; the credentials merge the roles of one password.
// 监听方需要它所连接角色之一的密码；凭据合并同一密码的角色。
func TestRoleCredentials(t *testing.T) {
	h, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode, model.RoleProxy})
	if err := h.SetListen(model.ListenConfig{Addr: freeAddr(t), HubPassword: "hp"}); err == nil {
		t.Fatal("a hub listening for hubs alone")
	}
	got := h.credentials(model.ListenConfig{Password: "a", NodePassword: "b", ProxyPassword: "b"})
	if len(got) != 1 || got[0].Password != "b" || strings.Join(got[0].Roles, ",") != "node,proxy" {
		t.Fatalf("credentials %+v", got)
	}
	got = h.credentials(model.ListenConfig{Password: "a", NodePassword: "b"})
	if len(got) != 2 || strings.Join(got[0].Roles, ",") != "node" || strings.Join(got[1].Roles, ",") != "proxy" {
		t.Fatalf("credentials %+v", got)
	}
	if !(model.ListenConfig{HubPassword: "x"}).HasPassword() || (model.ListenConfig{}).HasPassword() {
		t.Fatal("HasPassword")
	}
}

// Dialers prove the password of their own role, which the listener proves back: a mesh node and proxy link to a
// hub with role passwords, and a hub to a node.
// 拨号方证明自己角色的密码，监听方回证同一密码：互联的节点与代理用角色密码连到中心，中心连到节点。
func TestRolePasswordsDial(t *testing.T) {
	hub, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode, model.RoleProxy})
	hubAddr := freeAddr(t)
	if err := hub.Configure(model.ListenConfig{Addr: hubAddr, NodePassword: "np", ProxyPassword: "pp"}, nil); err != nil {
		t.Fatal(err)
	}
	node, _ := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub, model.RoleProxy})
	nodeAddr := freeAddr(t)
	if err := node.Configure(model.ListenConfig{Addr: nodeAddr, HubPassword: "hp"},
		[]model.DialReq{{Addr: hubAddr, Password: "np", Role: model.RoleHub}}); err != nil {
		t.Fatal(err)
	}
	proxy, _ := newTestMesh(t, model.RoleProxy, "p1", []string{model.RoleHub, model.RoleNode})
	if err := proxy.Configure(model.ListenConfig{}, []model.DialReq{{Addr: hubAddr, Password: "pp", Role: model.RoleHub},
		{Addr: nodeAddr, Password: "np", Role: model.RoleNode}}); err != nil {
		t.Fatal(err)
	}
	hub2, _ := newTestMesh(t, model.RoleHub, "h2", []string{model.RoleNode, model.RoleProxy})
	if err := hub2.Configure(model.ListenConfig{}, []model.DialReq{{Addr: nodeAddr, Password: "hp", Role: model.RoleNode}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "links", func() bool {
		return hub.Online("n1") && hub.Online("p1") && node.Online("h2") && hub2.Online("n1")
	})
	// The proxy proved the node's own password to the node, which takes no proxy password: no link.
	// 代理向节点证明的是节点的密码，而节点没有代理的密码：不能连上。
	waitFor(t, "the proxy's refusal", func() bool {
		for _, tg := range proxy.Targets() {
			if tg.Addr == nodeAddr {
				return strings.Contains(tg.Error, "authentication")
			}
		}
		return false
	})
	if node.Online("p1") {
		t.Fatal("a proxy linked to a node without a proxy password")
	}
}

// syncBuffer is a bytes.Buffer safe for the log's writes and the test's reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}
