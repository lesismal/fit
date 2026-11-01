package mesh

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fit/llm/internal/model"
)

// A listener that cannot bind at start is not fatal: it is recorded, and the listener can be set at runtime; changing
// its password drops the inbound connections, and a peer with the old password is refused; an empty address stops
// listening.
// 启动时无法绑定的监听不是致命错误：记录下来，之后可在运行时设置监听；修改密码会断开入站连接，旧密码的对端被拒绝；
// 地址为空即停止监听。
func TestSetListenRuntime(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	if err := node.Configure(model.ListenConfig{Addr: busy.Addr().String(), Password: "pw"}, nil); err != nil {
		t.Fatalf("start failed on a busy port: %v", err)
	}
	if st := node.Listen(); st.Error == "" || st.Addr != busy.Addr().String() || !st.HasPassword {
		t.Fatalf("listen state after a failed bind: %+v", st)
	}

	if err := node.SetListen(model.ListenConfig{Addr: "127.0.0.1:1"}); err == nil {
		t.Fatal("a listener without a password was accepted")
	}
	if err := node.SetListen(model.ListenConfig{Addr: "nonsense", Password: "x"}); err == nil {
		t.Fatal("a bad address was accepted")
	}

	addr := freeAddr(t)
	if err := node.SetListen(model.ListenConfig{Addr: addr, Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	if st := node.Listen(); st.Error != "" || st.Addr != addr {
		t.Fatalf("listen state: %+v", st)
	}
	if err := node.SetListen(model.ListenConfig{Addr: addr, Password: "pw"}); err != nil {
		t.Fatalf("same config again: %v", err)
	}
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, hub, "", toNode(addr))
	waitFor(t, "hello", func() bool { return hub.Online("n1") })

	// New password on the same port: the hub is dropped and refused until it has the new password.
	// 同一端口换新密码：hub 被断开并被拒绝，直到它使用新密码。
	if err := node.SetListen(model.ListenConfig{Addr: addr, Password: "new"}); err != nil {
		t.Fatal(err)
	}
	if got := node.ListenConfig(); got.Password != "new" || got.Addr != addr {
		t.Fatalf("listen config = %+v", got)
	}
	waitFor(t, "auth refusal", func() bool {
		ts := hub.Targets()
		return !hub.Online("n1") && strings.Contains(ts[0].Error, "authentication")
	})
	if err := hub.Dial(model.DialReq{Addr: addr, Password: "new", Role: model.RoleNode}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "hello with the new password", func() bool { return hub.Online("n1") })

	// Stop listening.
	// 停止监听。
	if err := node.SetListen(model.ListenConfig{}); err != nil {
		t.Fatal(err)
	}
	if st := node.Listen(); st.Addr != "" || st.HasPassword {
		t.Fatalf("listen state after stop: %+v", st)
	}
	waitFor(t, "disconnect", func() bool { return !hub.Online("n1") })
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Fatal("still listening")
	}
}

// RegisterAdmin serves the console only, validates its requests and persists after every change.
// RegisterAdmin 只为控制台服务，校验请求，并在每次变更后持久化。
func TestRegisterAdmin(t *testing.T) {
	node, r := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub, model.RoleProxy})
	var mu sync.Mutex
	var saved struct {
		listen model.ListenConfig
		dials  []model.DialReq
		n      int
	}
	var cur Settings
	RegisterAdmin(r, node, func(_ context.Context, fn func(*Settings) error) error {
		mu.Lock()
		defer mu.Unlock()
		next := Settings{Peers: append([]model.DialReq(nil), cur.Peers...)}
		if cur.Listen != nil {
			l := *cur.Listen
			next.Listen = &l
		}
		if err := fn(&next); err != nil {
			return err
		}
		cur = next
		var listen model.ListenConfig
		if cur.Listen != nil {
			listen = *cur.Listen
		}
		_ = node.Configure(listen, cur.Peers)
		saved.listen, saved.dials, saved.n = node.ListenConfig(), node.DialReqs(), saved.n+1
		return nil
	})
	start(t, node, "")
	local := model.Identity{Role: model.RoleLocal}
	ctx := context.Background()
	invoke := func(caller model.Identity, method string, req any) (any, int) {
		t.Helper()
		raw, _ := json.Marshal(req)
		out, err := r.Invoke(ctx, caller, method, raw)
		if err != nil {
			code, _ := ErrCode(err)
			return nil, code
		}
		return out, model.CodeOK
	}
	saves := func() int {
		mu.Lock()
		defer mu.Unlock()
		return saved.n
	}

	// Peers never leave the console. / 互联管理方法不对对端开放。
	if _, code := invoke(model.Identity{Role: model.RoleHub, ID: "h1"}, model.MethodPeers, nil); code != model.CodeForbidden {
		t.Fatalf("hub peers: code %d", code)
	}
	// Bad requests change nothing and save nothing. / 错误请求不作任何改变，也不保存。
	for _, req := range []model.DialReq{
		{Addr: "127.0.0.1:9", Password: "pw", Role: model.RoleNode},    // own role / 自身角色
		{Addr: "127.0.0.1:9", Password: "pw", Role: "edge"},            // unknown role / 未知角色
		{Addr: "127.0.0.1", Password: "pw", Role: model.RoleHub},       // no port / 没有端口
		{Addr: ":9", Password: "pw", Role: model.RoleHub},              // no host / 没有主机
		{Addr: "127.0.0.1:9", Role: model.RoleHub},                     // no password / 没有密码
		{Addr: "127.0.0.1:99999", Password: "pw", Role: model.RoleHub}, // bad port / 端口无效
	} {
		if _, code := invoke(local, model.MethodDial, req); code != model.CodeBadRequest {
			t.Fatalf("dial %+v: code %d", req, code)
		}
	}
	if _, code := invoke(local, model.MethodListen, model.ListenReq{Addr: "127.0.0.1:9"}); code != model.CodeBadRequest {
		t.Fatalf("listen without password: code %d", code)
	}
	if _, code := invoke(local, model.MethodUndial, model.UndialReq{Addr: "127.0.0.1:9"}); code != model.CodeNotFound {
		t.Fatalf("undial unknown: code %d", code)
	}
	if n := saves(); n != 0 {
		t.Fatalf("saved %d times on bad requests", n)
	}

	// Dial, listen (an empty password keeps the current one), undial: each change is saved.
	// 拨号、监听（密码为空表示保留当前密码）、取消拨号：每次变更都保存。
	hubAddr := freeAddr(t)
	out, code := invoke(local, model.MethodDial, model.DialReq{Addr: hubAddr, Password: "hpw", Role: model.RoleHub})
	if code != model.CodeOK {
		t.Fatalf("dial: code %d", code)
	}
	if resp := out.(model.PeersResp); len(resp.Targets) != 1 || resp.Targets[0].Addr != hubAddr {
		t.Fatalf("dial reply %+v", resp)
	}
	if b, _ := json.Marshal(out); strings.Contains(string(b), "hpw") {
		t.Fatalf("a password in the reply: %s", b)
	}
	addr := freeAddr(t)
	if _, code := invoke(local, model.MethodListen, model.ListenReq{Addr: addr, Password: "lpw"}); code != model.CodeOK {
		t.Fatalf("listen: code %d", code)
	}
	addr2 := freeAddr(t)
	out, code = invoke(local, model.MethodListen, model.ListenReq{Addr: addr2})
	if code != model.CodeOK {
		t.Fatalf("listen keeping the password: code %d", code)
	}
	if st := out.(model.PeersResp).Listen; st.Addr != addr2 || !st.HasPassword || st.Error != "" {
		t.Fatalf("listen state %+v", st)
	}
	mu.Lock()
	if saved.n != 3 || saved.listen != (model.ListenConfig{Addr: addr2, Password: "lpw"}) ||
		len(saved.dials) != 1 || saved.dials[0] != (model.DialReq{Addr: hubAddr, Password: "hpw", Role: model.RoleHub}) {
		t.Fatalf("saved %+v", saved)
	}
	mu.Unlock()
	if _, code := invoke(local, model.MethodUndial, model.UndialReq{Addr: hubAddr}); code != model.CodeOK {
		t.Fatalf("undial: code %d", code)
	}
	if _, code := invoke(local, model.MethodListen, model.ListenReq{}); code != model.CodeOK {
		t.Fatalf("stop listening: code %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if saved.n != 5 || len(saved.dials) != 0 || saved.listen != (model.ListenConfig{}) {
		t.Fatalf("saved %+v", saved)
	}
}

func TestValidateAddr(t *testing.T) {
	for _, c := range []struct {
		addr     string
		needHost bool
		ok       bool
	}{
		{"127.0.0.1:80", true, true}, {"example.com:443", true, true}, {"[::1]:9", true, true},
		{":9100", false, true}, {":9100", true, false}, {"host", true, false}, {"host:0", true, false},
		{"host:http", true, false},
	} {
		if _, err := ValidateAddr(c.addr, c.needHost); (err == nil) != c.ok {
			t.Errorf("ValidateAddr(%q, %v) = %v", c.addr, c.needHost, err)
		}
	}
}
