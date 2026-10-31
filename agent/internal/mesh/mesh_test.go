package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/wire"
	"github.com/lesismal/fit/upstream"
)

// testEngine is the engine the tests listen and dial on.
var testEngine = sync.OnceValue(func() *fib.Engine {
	e, _, err := upstream.StartEngine()
	if err != nil {
		panic(err)
	}
	return e
})

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func newTestMesh(t *testing.T, role, id string, accept []string) (*Mesh, *Router) {
	r := NewRouter()
	Handle(r, "/echo", []string{model.RoleHub, model.RoleNode}, 0, func(_ context.Context, caller model.Identity, req *map[string]string) (any, error) {
		return map[string]string{"from": caller.ID, "msg": (*req)["msg"]}, nil
	})
	m := New(Options{
		Engine:        testEngine(),
		Self:          func() model.Identity { return model.Identity{Role: role, ID: id, Name: id} },
		AcceptRoles:   accept,
		PingInterval:  time.Second,
		RetryInterval: 500 * time.Millisecond,
	}, r)
	t.Cleanup(m.Stop)
	return m, r
}

// start starts m listening on listen (if not empty) with the test password, dialing peers.
// start 以测试密码在 listen（不为空时）上启动 m 的监听，并连接 peers。
func start(t *testing.T, m *Mesh, listen string, peers ...model.DialReq) {
	t.Helper()
	cfg := model.ListenConfig{}
	if listen != "" {
		cfg = model.ListenConfig{Addr: listen, Password: "pw"}
	}
	if err := m.Configure(cfg, peers); err != nil {
		t.Fatal(err)
	}
	if e := m.Listen().Error; e != "" {
		t.Fatal(e)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

var (
	allPeers = []string{model.RoleNode, model.RoleHub, model.RoleClient}
	toNode   = func(addr string) model.DialReq {
		return model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode}
	}
	toHub = func(addr string) model.DialReq { return model.DialReq{Addr: addr, Password: "pw", Role: model.RoleHub} }
)

// Either side may dial; methods are served in both directions and checked against the caller's role.
// 任一方都可以拨号；两个方向都提供方法，并按调用方角色校验。
func TestMeshBothDirections(t *testing.T) {
	addr := freeAddr(t)
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	start(t, node, addr)
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, hub, "", toNode(addr))
	client, _ := newTestMesh(t, model.RoleClient, "c1", allPeers)
	start(t, client, "", toNode(addr))
	waitFor(t, "hello", func() bool { return hub.Online("n1") && client.Online("n1") && node.Online("h1") && node.Online("c1") })

	ctx := context.Background()
	got, err := Call[map[string]string](ctx, hub, "n1", "/echo", map[string]string{"msg": "hi"})
	if err != nil || got["from"] != "h1" || got["msg"] != "hi" {
		t.Fatalf("hub → node: %v %v", got, err)
	}
	// The listener side calls back over the incoming connection.
	// 监听方经入站连接反向调用。
	got, err = Call[map[string]string](ctx, node, "h1", "/echo", map[string]string{"msg": "back"})
	if err != nil || got["from"] != "n1" {
		t.Fatalf("node → hub: %v %v", got, err)
	}
	// client is not allowed to call /echo.
	// client 无权调用 /echo。
	if _, err := Call[map[string]string](ctx, client, "n1", "/echo", nil); err == nil {
		t.Fatal("client call was not refused")
	}
	if raw, err := hub.CallRaw(ctx, "n1", "/echo", nil); err != nil || !strings.Contains(string(raw), `"h1"`) {
		t.Fatalf("CallRaw: %s %v", raw, err)
	}
	if ts := hub.Targets(); len(ts) != 1 || !ts[0].Connected || ts[0].Peer.ID != "n1" {
		t.Fatalf("targets = %+v", ts)
	}
	if ps := node.PeersOf(model.RoleHub); len(ps) != 1 || ps[0].ID != "h1" {
		t.Fatalf("peers of hub = %+v", ps)
	}

	// Undial: the node sees the hub go away.
	// Undial：node 看到 hub 断开。
	if !hub.Undial(addr) {
		t.Fatal("undial")
	}
	waitFor(t, "disconnect", func() bool { return !node.Online("h1") })
}

func TestMeshRefusesRoleAndPassword(t *testing.T) {
	addr := freeAddr(t)
	node, _ := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub})
	start(t, node, addr)
	client, _ := newTestMesh(t, model.RoleClient, "c1", allPeers)
	start(t, client, "", toNode(addr))
	bad, _ := newTestMesh(t, model.RoleHub, "h2", allPeers)
	start(t, bad, "", model.DialReq{Addr: addr, Password: "wrong", Role: model.RoleNode})
	waitFor(t, "errors", func() bool {
		a, b := client.Targets(), bad.Targets()
		return a[0].Error != "" && b[0].Error != ""
	})
	if client.Online("n1") || node.Online("c1") || bad.Online("n1") {
		t.Fatal("refused peers are online")
	}
	if e := client.Targets()[0].Error; !strings.Contains(e, "not accepted") {
		t.Fatalf("client error = %q", e)
	}
	if e := bad.Targets()[0].Error; !strings.Contains(e, "authentication") {
		t.Fatalf("wrong password error = %q", e)
	}
}

// A peer of the process's own role is refused at hello even when AcceptRoles lists it, and a dial target of the
// own role is rejected up front.
// 与进程同角色的对端即使列在 AcceptRoles 中也在 hello 时被拒绝；自身角色的连接目标直接被拒绝。
func TestMeshSameRoleRefused(t *testing.T) {
	addr := freeAddr(t)
	n1, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	start(t, n1, addr)
	var role atomic.Value
	role.Store(model.RoleHub)
	n2 := New(Options{Engine: testEngine(),
		Self:          func() model.Identity { return model.Identity{Role: role.Load().(string), ID: "n0"} },
		AcceptRoles:   allPeers,
		RetryInterval: 300 * time.Millisecond,
	}, NewRouter())
	t.Cleanup(n2.Stop)
	if err := n2.Dial(toHub(addr)); err == nil {
		t.Fatal("a hub dialing a hub was accepted")
	}
	role.Store(model.RoleNode) // pretend to be a node from now on / 从此冒充 node
	if err := n2.Dial(toNode(addr)); err == nil {
		t.Fatal("a node dial target accepted by a node")
	}
	role.Store(model.RoleHub)
	if err := n2.Dial(toNode(addr)); err != nil {
		t.Fatal(err)
	}
	role.Store(model.RoleNode) // the hello says node / hello 中声明 node
	waitFor(t, "the refusal", func() bool {
		ts := n2.Targets()
		return len(ts) == 1 && strings.Contains(ts[0].Error, "same role")
	})
	if len(n1.Peers()) != 0 || len(n2.Peers()) != 0 {
		t.Fatalf("nodes connected: %v / %v", n1.Peers(), n2.Peers())
	}
}

// A peer configured with one role but announcing another is refused.
// 配置为某个角色、却声明了另一个角色的对端会被拒绝。
func TestMeshExpectedRole(t *testing.T) {
	addr := freeAddr(t)
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	start(t, node, addr)
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, hub, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleClient})
	waitFor(t, "refusal", func() bool { ts := hub.Targets(); return len(ts) == 1 && ts[0].Error != "" })
	if hub.Online("n1") {
		t.Fatal("a node accepted as a client peer")
	}
	// Re-dialing with only the role changed keeps the password.
	// 只改角色重新拨号时保留密码。
	if err := hub.Dial(model.DialReq{Addr: addr, Role: model.RoleNode}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "hello", func() bool { return hub.Online("n1") })
	if rs := hub.DialReqs(); len(rs) != 1 || rs[0].Role != model.RoleNode || rs[0].Password != "pw" {
		t.Fatalf("dial reqs = %+v", rs)
	}
}

// A dialed peer that goes away is reported offline at once, and comes back when it restarts.
// 被拨号的对端离开时立即报告离线，重启后恢复在线。
func TestMeshOutboundLoss(t *testing.T) {
	addr := freeAddr(t)
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	start(t, node, addr)
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	down := make(chan string, 4)
	hub.OnPeerDown(func(p model.PeerConn) { down <- p.ID })
	start(t, hub, "", toNode(addr))
	waitFor(t, "hello", func() bool { return hub.Online("n1") })
	node.Stop()
	select {
	case id := <-down:
		if id != "n1" {
			t.Fatalf("down = %s", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("outbound loss not reported")
	}
	if hub.Online("n1") {
		t.Fatal("still online")
	}
	node2, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	start(t, node2, addr)
	waitFor(t, "reconnect", func() bool { return hub.Online("n1") })
}

// Connections dialed by either side form one pool per peer, and calls go round robin over it.
// 任一方发起的连接构成每个对端的一个连接池，调用在其中轮询。
func TestMeshPoolRoundRobin(t *testing.T) {
	addrN, addrH := freeAddr(t), freeAddr(t)
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, node, addrN, toHub(addrH))
	start(t, hub, addrH, toNode(addrN))
	waitFor(t, "two connections", func() bool { return len(hub.Peers()) == 2 })
	if ids := hub.PeersOf(model.RoleNode); len(ids) != 1 {
		t.Fatalf("peers of node = %+v", ids)
	}
	seen := map[any]bool{}
	for i := 0; i < 4; i++ {
		seen[hub.pick("n1")] = true
	}
	if len(seen) != 2 {
		t.Fatalf("round robin used %d connections, want 2", len(seen))
	}
	for i := 0; i < 4; i++ {
		if _, err := Call[map[string]string](context.Background(), hub, "n1", "/echo", map[string]string{"msg": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	// Kick closes both; the outbound one redials (and the node's own target redials the hub).
	// Kick 关闭两条连接；出站连接重新拨号（node 自己的目标也会重新连接 hub）。
	if n := hub.Kick("n1"); n != 2 {
		t.Fatalf("kicked %d", n)
	}
	if !hub.WaitOnline(context.Background(), "n1", 5*time.Second) {
		t.Fatal("no reconnect after kick")
	}
}

// A peer that stays down for a while is still dialed again: the backoff never exceeds RetryInterval.
// 停机一段时间的对端仍会被重新拨号：退避等待不超过 RetryInterval。
func TestMeshSlowRetry(t *testing.T) {
	addr := freeAddr(t)
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	start(t, node, addr)
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, hub, "", toNode(addr))
	waitFor(t, "hello", func() bool { return hub.Online("n1") })
	node.Stop()
	time.Sleep(3 * time.Second) // several failed dials / 若干次拨号失败
	node2, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	start(t, node2, addr)
	waitFor(t, "reconnect", func() bool { return hub.Online("n1") })
}

// Stream methods and pushes are authorized by the caller's role like ordinary methods, in both directions.
// 流方法与推送和普通方法一样按调用方角色授权，两个方向都可用。
func TestMeshStreamAndPush(t *testing.T) {
	addr := freeAddr(t)
	node, nr := newTestMesh(t, model.RoleNode, "n1", allPeers)
	// /count sends n numbers, then echoes what the caller sends until it half-closes; the result is the echo count.
	// /count 发送 n 个数字，然后回显调用方发来的数据直到其半关闭；结果为回显次数。
	HandleStream(nr, "/count", []string{model.RoleHub}, func(ctx context.Context, caller model.Identity, req *struct{ N int }, st *wire.Stream) (any, error) {
		if req.N < 0 {
			return nil, BadRequest("negative n")
		}
		for i := 0; i < req.N; i++ {
			b, _ := json.Marshal(i)
			if err := st.Send(ctx, b); err != nil {
				return nil, err
			}
		}
		echoed := 0
		for {
			b, err := st.Recv(ctx)
			if errors.Is(err, io.EOF) {
				return map[string]any{"from": caller.ID, "echoed": echoed}, nil
			}
			if err != nil {
				return nil, err
			}
			if err := st.Send(ctx, b); err != nil {
				return nil, err
			}
			echoed++
		}
	})
	nodePushes := make(chan string, 4)
	node.OnPush("/note", []string{model.RoleHub}, func(caller model.Identity, msg json.RawMessage) {
		var s string
		_ = json.Unmarshal(msg, &s)
		nodePushes <- caller.ID + ":" + s
	})
	start(t, node, addr)
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	hubPushes := make(chan string, 4)
	hub.OnPush("/note", []string{model.RoleNode}, func(caller model.Identity, msg json.RawMessage) {
		var s string
		_ = json.Unmarshal(msg, &s)
		hubPushes <- caller.ID + ":" + s
	})
	start(t, hub, "", toNode(addr))
	client, _ := newTestMesh(t, model.RoleClient, "c1", allPeers)
	start(t, client, "", toNode(addr))
	waitFor(t, "hello", func() bool { return hub.Online("n1") && client.Online("n1") && node.Online("h1") && node.Online("c1") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Allowed: data, echo, half-close, result.
	// 允许：数据、回显、半关闭、结果。
	st, err := hub.OpenStream(ctx, "n1", "/count", map[string]int{"n": 3})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		b, err := st.Recv(ctx)
		if err != nil || string(b) != string(rune('0'+i)) {
			t.Fatalf("recv %d: %q %v", i, b, err)
		}
	}
	if err := st.Send(ctx, []byte(`"x"`)); err != nil {
		t.Fatal(err)
	}
	if b, err := st.Recv(ctx); err != nil || string(b) != `"x"` {
		t.Fatalf("echo: %q %v", b, err)
	}
	_ = st.CloseSend()
	if _, err := st.Recv(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("end: %v", err)
	}
	var res struct {
		From   string
		Echoed int
	}
	if err := json.Unmarshal(st.Result(), &res); err != nil || res.From != "h1" || res.Echoed != 1 {
		t.Fatalf("result %s: %v", st.Result(), err)
	}

	// Refused, failed and unknown streams end with the error code.
	// 被拒绝、失败与未知的流以错误码结束。
	recvCode := func(m *Mesh, method string, req any) int {
		t.Helper()
		st, err := m.OpenStream(ctx, "n1", method, req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.Recv(ctx)
		code, _ := ErrCode(err)
		return code
	}
	if code := recvCode(client, "/count", map[string]int{"n": 1}); code != model.CodeForbidden {
		t.Fatalf("client stream: code %d", code)
	}
	if code := recvCode(hub, "/count", map[string]int{"n": -1}); code != model.CodeBadRequest {
		t.Fatalf("bad request stream: code %d", code)
	}
	if code := recvCode(hub, "/nope", nil); code != model.CodeNotFound {
		t.Fatalf("unknown stream: code %d", code)
	}
	if _, err := hub.OpenStream(ctx, "nobody", "/count", nil); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline stream: %v", err)
	}

	// Pushes: the client's is dropped (not allowed), the hub's arrives; node pushes back over the incoming connection.
	// 推送：client 的被丢弃（无权），hub 的送达；node 经入站连接反向推送。
	if err := client.Push("n1", "/note", "from client"); err != nil {
		t.Fatal(err)
	}
	if err := hub.Push("n1", "/note", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := node.Push("h1", "/note", "back"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ch   chan string
		want string
	}{{nodePushes, "h1:hi"}, {hubPushes, "n1:back"}} {
		select {
		case got := <-c.ch:
			if got != c.want {
				t.Fatalf("push = %q, want %q", got, c.want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("push %q not delivered", c.want)
		}
	}
	select {
	case got := <-nodePushes:
		t.Fatalf("unexpected push %q", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// OpenLocal runs a stream method in-process as RoleLocal, which every route allows.
// OpenLocal 以 RoleLocal 身份在进程内运行流方法，所有路由都允许 RoleLocal。
func TestOpenLocal(t *testing.T) {
	r := NewRouter()
	HandleStream(r, "/gen", nil, func(ctx context.Context, caller model.Identity, _ *struct{}, st *wire.Stream) (any, error) {
		return caller.Role, st.Send(ctx, []byte(`1`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := r.OpenLocal(ctx, model.Identity{Role: model.RoleLocal}, "/gen", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := st.Recv(ctx); err != nil || string(b) != "1" {
		t.Fatalf("recv %q %v", b, err)
	}
	if _, err := st.Recv(ctx); !errors.Is(err, io.EOF) || string(st.Result()) != `"local"` {
		t.Fatalf("end %v %s", err, st.Result())
	}
	if _, err := r.InvokeStream(ctx, model.Identity{Role: model.RoleHub}, "/gen", nil, nil); err == nil {
		t.Fatal("a hub opened a local-only stream")
	}
}
