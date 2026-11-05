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

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
	"github.com/lesismal/fit/logs"
)

// testLog is a logger that writes nowhere, so that the mesh formats what it logs.
// testLog 是不写到任何地方的日志器，使互联照常格式化其日志。
func testLog(t *testing.T) *logs.Logger {
	t.Helper()
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root.Logger("server")
}

// rawPeer links to a mesh listener with wire alone, so the test says (or does not say) hello itself.
// rawPeer 只用 wire 连接互联的监听方，由测试自己发送（或不发送）hello。
func rawPeer(t *testing.T, addr string, h wire.Handlers) *wire.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wc, err := wire.Dial(ctx, "ws://"+addr+WSPath, wire.DialConfig{Engine: testEngine(), ConnConfig: wire.ConnConfig{
		Auth: wire.Password("pw"), Handlers: func(*wire.Conn) wire.Handlers { return h },
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wc.Close)
	return wc
}

// rawListener is a wire listener a mesh dials; handlers serves each link it takes.
// rawListener 是供互联拨号的 wire 监听方；handlers 处理它接受的每条连接。
func rawListener(t *testing.T, handlers func(*wire.Conn) wire.Handlers, onOpen func(*wire.Conn)) string {
	t.Helper()
	l := wire.NewHandler(wire.ConnConfig{Auth: wire.Password("pw"), Handlers: handlers, OnOpen: onOpen})
	e := testEngine()
	i, err := e.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: l})
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := e.BindingAddrs(i)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveBinding(i); l.CloseAll() })
	return addrs[0].String()
}

func hello(id model.Identity) []byte {
	b, _ := json.Marshal(id)
	return b
}

func codeOf(err error) int {
	code, _ := ErrCode(err)
	return code
}

// A peer that has not said hello is refused everything; a hello that is not one, or names nobody or the mesh
// itself, is refused; saying hello again on a link is fine.
// 未发送 hello 的对端一切都被拒绝；不成形、没有身份或冒充互联自身的 hello 被拒绝；同一连接上再次 hello 没有问题。
func TestHelloEdges(t *testing.T) {
	node, _ := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub})
	addr := freeAddr(t)
	start(t, node, addr)
	ctx := ctxT(t)

	wc := rawPeer(t, addr, wire.Handlers{})
	if _, err := wc.Call(ctx, wire.Head{Method: "/echo"}, []byte(`{}`)); codeOf(err) != model.CodeUnauthorized {
		t.Fatalf("call before hello: %v", err)
	}
	st, err := wc.OpenStream(ctx, wire.Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Recv(ctx); codeOf(err) != model.CodeUnauthorized {
		t.Fatalf("stream before hello: %v", err)
	}
	if err := wc.Push(wire.Head{Method: "/p"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := wc.Call(ctx, wire.Head{Method: model.MethodHello}, []byte("[")); codeOf(err) != model.CodeBadRequest {
		t.Fatalf("bad hello: %v", err)
	}
	hub := model.Identity{Role: model.RoleHub, ID: "h1", Name: "h1"}
	for range 2 {
		if _, err := wc.Call(ctx, wire.Head{Method: model.MethodHello}, hello(hub)); err != nil {
			t.Fatal(err)
		}
	}
	if ps := node.Peers(); len(ps) != 1 || ps[0].ID != "h1" {
		t.Fatalf("peers after two hellos: %+v", ps)
	}
	// A push of a method nobody registered is dropped. / 未注册方法的推送被丢弃。
	if err := wc.Push(wire.Head{Method: "/nobody"}, nil); err != nil {
		t.Fatal(err)
	}

	for _, id := range []model.Identity{{}, {Role: model.RoleHub, ID: "n1"}} {
		wc := rawPeer(t, addr, wire.Handlers{})
		if _, err := wc.Call(ctx, wire.Head{Method: model.MethodHello}, hello(id)); codeOf(err) != model.CodeForbidden {
			t.Fatalf("hello %+v: %v", id, err)
		}
	}
}

// An ID another link holds under one role is refused under another: a peer that knows the password cannot pass
// for a node it is not by its ID; the same ID and role on another link, as a node's pool, is taken.
// 另一条连接以某个角色持有的 ID 不能以其他角色使用：知道密码的对端无法借 ID 冒充它不是的节点；同一 ID、同一角色的另一条连接
// （如节点的连接池）照常接受。
func TestHelloIDConflict(t *testing.T) {
	hub, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode, model.RoleProxy})
	addr := freeAddr(t)
	start(t, hub, addr)
	ctx := ctxT(t)
	node := model.Identity{Role: model.RoleNode, ID: "node-1", Name: "gpu"}
	if _, err := rawPeer(t, addr, wire.Handlers{}).Call(ctx, wire.Head{Method: model.MethodHello}, hello(node)); err != nil {
		t.Fatal(err)
	}
	if _, err := rawPeer(t, addr, wire.Handlers{}).Call(ctx, wire.Head{Method: model.MethodHello}, hello(node)); err != nil {
		t.Fatalf("a second link of the node: %v", err)
	}
	evil := rawPeer(t, addr, wire.Handlers{})
	fake := model.Identity{Role: model.RoleProxy, ID: "node-1", Name: "gpu"}
	if _, err := evil.Call(ctx, wire.Head{Method: model.MethodHello}, hello(fake)); codeOf(err) != model.CodeForbidden {
		t.Fatalf("the node's ID as a proxy: %v", err)
	}
	for _, p := range hub.Peers() {
		if p.ID == "node-1" && p.Role != model.RoleNode {
			t.Fatalf("peers %+v", hub.Peers())
		}
	}
}

// A listener that answers the mesh's hello badly: a reply that is not an identity, a link that drops, a hello the
// other way round, a call before the hello is done.
// 对互联的 hello 应答不当的监听方：不是身份的应答、断开的连接、反方向的 hello、hello 完成前的调用。
func TestHelloOutboundEdges(t *testing.T) {
	ctx := ctxT(t)

	// A reply that does not decode: the mesh waits, and gives up on the link when it drops.
	// 无法解码的应答：互联等待，连接断开时放弃该连接。
	var answered sync.Map
	addr := rawListener(t, func(c *wire.Conn) wire.Handlers {
		return wire.Handlers{Request: func(context.Context, *wire.Request) ([]byte, error) {
			if _, again := answered.LoadOrStore(c, true); again {
				return nil, nil
			}
			time.AfterFunc(100*time.Millisecond, c.Close)
			return []byte("["), nil
		}}
	}, nil)
	hub, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode})
	start(t, hub, "", toNode(addr))
	waitFor(t, "hello error", func() bool { ts := hub.Targets(); return len(ts) == 1 && strings.Contains(ts[0].Error, "decode") })

	// The same, and the mesh stops while it waits. / 同上，互联在等待时停止。
	addr2 := rawListener(t, func(c *wire.Conn) wire.Handlers {
		return wire.Handlers{Request: func(context.Context, *wire.Request) ([]byte, error) { return []byte("["), nil }}
	}, nil)
	hub2 := New(Options{Engine: testEngine(), Self: func() model.Identity { return model.Identity{Role: model.RoleHub, ID: "h2"} },
		AcceptRoles: []string{model.RoleNode}, Log: testLog(t)}, NewRouter())
	start(t, hub2, "", toNode(addr2))
	waitFor(t, "hello error", func() bool { ts := hub2.Targets(); return len(ts) == 1 && ts[0].Error != "" })
	hub2.Stop()

	// The link drops while the hello waits for its answer. / hello 等待应答时连接断开。
	var hellos atomic.Int32
	addr3 := rawListener(t, func(c *wire.Conn) wire.Handlers {
		return wire.Handlers{Request: func(ctx context.Context, _ *wire.Request) ([]byte, error) {
			hellos.Add(1)
			c.Close()
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	}, nil)
	hub3, _ := newTestMesh(t, model.RoleHub, "h3", []string{model.RoleNode})
	start(t, hub3, "", toNode(addr3))
	waitFor(t, "a hello again", func() bool { return hellos.Load() >= 2 })
	hub3.Undial(addr3)

	// Before the mesh's hello is answered, the listener calls the mesh: a hello is refused on an outbound link, and a
	// call waits for the hello until its context ends.
	// 在互联的 hello 得到应答之前，监听方调用互联：出站连接上的 hello 被拒绝，调用等待 hello 直到其 context 结束。
	release := make(chan struct{})
	defer close(release)
	results := make(chan error, 2)
	addr4 := rawListener(t, func(*wire.Conn) wire.Handlers {
		return wire.Handlers{Request: func(ctx context.Context, _ *wire.Request) ([]byte, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, errors.New("no")
		}}
	}, func(c *wire.Conn) {
		go func() {
			_, err := c.Call(ctx, wire.Head{Method: model.MethodHello}, hello(model.Identity{Role: model.RoleNode, ID: "x"}))
			results <- err
			short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			_, err = c.Call(short, wire.Head{Method: "/echo"}, []byte(`{}`))
			results <- err
		}()
	})
	hub4, _ := newTestMesh(t, model.RoleHub, "h4", []string{model.RoleNode})
	start(t, hub4, "", toNode(addr4))
	if err := <-results; codeOf(err) != model.CodeForbidden {
		t.Fatalf("hello on an outbound link: %v", err)
	}
	if err := <-results; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call before the hello: %v", err)
	}
}

// Calls, streams and pushes whose request cannot be encoded or whose answer cannot be decoded, and waits for a peer
// that does not come.
// 请求无法编码或应答无法解码的调用、流与推送，以及等待不会到来的对端。
func TestCallEdges(t *testing.T) {
	node, r := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub})
	block := make(chan struct{})
	defer close(block)
	Handle(r, "/nil", []string{model.RoleHub}, 0, func(context.Context, model.Identity, *struct{}) (any, error) { return nil, nil })
	Handle(r, "/str", []string{model.RoleHub}, 0, func(context.Context, model.Identity, *struct{}) (any, error) { return "s", nil })
	Handle(r, "/block", []string{model.RoleHub}, 0, func(ctx context.Context, _ model.Identity, _ *struct{}) (any, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, nil
	})
	addr := freeAddr(t)
	start(t, node, addr)
	hub, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode})
	start(t, hub, "", toNode(addr), toNode(freeAddr(t)))
	ctx := ctxT(t)
	if !hub.WaitOnline(ctx, "n1", 5*time.Second) {
		t.Fatal("node not online")
	}

	bad := make(chan int)
	if _, err := Call[struct{}](ctx, hub, "n1", "/echo", bad); err == nil {
		t.Fatal("call encoded a channel")
	}
	if _, err := hub.OpenStream(ctx, "n1", "/s", bad); err == nil {
		t.Fatal("stream encoded a channel")
	}
	if err := hub.Push("n1", "/p", bad); err == nil {
		t.Fatal("push encoded a channel")
	}
	if err := hub.Push("nobody", "/p", nil); !errors.Is(err, ErrOffline) {
		t.Fatalf("push offline: %v", err)
	}
	if v, err := Call[map[string]string](ctx, hub, "n1", "/nil", nil); err == nil && v != nil {
		t.Fatalf("null reply: %v", v)
	}
	if _, err := Call[struct{ A int }](ctx, hub, "n1", "/str", nil); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("undecodable reply: %v", err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := Call[struct{}](short, hub, "n1", "/block", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed out call: %v", err)
	}

	// Two dial targets come back sorted. / 两个拨号目标按序返回。
	if ts, ds := hub.Targets(), hub.DialReqs(); len(ts) != 2 || ts[0].Addr > ts[1].Addr || len(ds) != 2 || ds[0].Addr > ds[1].Addr {
		t.Fatalf("targets %+v, dial reqs %+v", ts, ds)
	}

	if hub.WaitOnline(ctx, "nobody", 50*time.Millisecond) {
		t.Fatal("nobody came online")
	}
	canceled, cancel2 := context.WithCancel(ctx)
	cancel2()
	if hub.WaitOnline(canceled, "nobody", time.Minute) {
		t.Fatal("online after the context ended")
	}
	go func() { time.Sleep(50 * time.Millisecond); hub.Stop() }()
	if hub.WaitOnline(ctx, "nobody", time.Minute) {
		t.Fatal("online after the mesh stopped")
	}
}

// Router misuse: unknown methods, requests that do not decode, a stream method too long to open locally.
// 路由的误用：未知方法、无法解码的请求、过长而无法在本地打开的流方法。
func TestRouterEdges(t *testing.T) {
	r := NewRouter()
	Handle(r, "/m", nil, 0, func(context.Context, model.Identity, *struct{ A int }) (any, error) { return nil, nil })
	HandleStream(r, "/s", nil, func(context.Context, model.Identity, *struct{ A int }, *wire.Stream) (any, error) { return nil, nil })
	local := model.Identity{Role: model.RoleLocal}
	ctx := ctxT(t)
	if _, err := r.Invoke(ctx, local, "/nope", nil); codeOf(err) != model.CodeNotFound {
		t.Fatalf("unknown method: %v", err)
	}
	if _, err := r.Invoke(ctx, local, "/m", json.RawMessage(`{"A":"x"}`)); codeOf(err) != model.CodeBadRequest {
		t.Fatalf("bad request: %v", err)
	}
	st, err := r.OpenLocal(ctx, local, "/s", json.RawMessage(`{"A":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Recv(ctx); codeOf(err) != model.CodeBadRequest {
		t.Fatalf("bad stream request: %v", err)
	}
	if _, err := r.OpenLocal(ctx, local, strings.Repeat("m", 300), nil); err == nil {
		t.Fatal("opened a method too long for a frame")
	}
	if code, msg := ErrCode(context.DeadlineExceeded); code != model.CodeInternal || msg != "timeout" {
		t.Fatalf("deadline: %d %q", code, msg)
	}
}

// Dial and listen settings that are refused, dials stopped while they run, and a mesh used after Stop.
// 被拒绝的拨号与监听设置、运行中被停止的拨号，以及 Stop 之后使用的互联。
func TestLifecycleEdges(t *testing.T) {
	noEngine := New(Options{Self: func() model.Identity { return model.Identity{Role: model.RoleHub, ID: "h0"} }}, NewRouter())
	if err := noEngine.SetListen(model.ListenConfig{Addr: "127.0.0.1:0", Password: "pw"}); err == nil || noEngine.Listen().Error == "" {
		t.Fatalf("listened without an engine: %v", err)
	}
	noEngine.Stop()

	m := New(Options{Engine: testEngine(), Self: func() model.Identity { return model.Identity{Role: model.RoleHub, ID: "h1"} },
		AcceptRoles: []string{model.RoleNode}, Log: testLog(t)}, NewRouter())
	defer m.Stop()
	if err := m.Configure(model.ListenConfig{}, []model.DialReq{{Addr: "bad"}}); err == nil {
		t.Fatal("bad peer configured")
	}
	if err := m.Dial(model.DialReq{Addr: "127.0.0.1:1", Role: model.RoleNode}); codeOf(err) != model.CodeBadRequest {
		t.Fatalf("dial without a password: %v", err)
	}
	if m.Undial("127.0.0.1:1") {
		t.Fatal("undialed an unknown target")
	}
	stopped := &target{stopCh: make(chan struct{})}
	close(stopped.stopCh)
	if m.waitOrStop(stopped, 0) {
		t.Fatal("wait went on for a stopped target")
	}

	// A peer that accepts TCP and says nothing holds the dial until it is stopped.
	// 接受 TCP 后一言不发的对端会拖住拨号，直到拨号被停止。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	silent := ln.Addr().String()
	if err := m.Dial(toNode(silent)); err != nil {
		t.Fatal(err)
	}
	c := <-accepted
	defer c.Close()
	m.Undial(silent)
	if err := m.Dial(toNode(silent)); err != nil {
		t.Fatal(err)
	}
	c2 := <-accepted
	defer c2.Close()
	m.Stop()

	if m.waitOrStop(&target{stopCh: make(chan struct{})}, 0) {
		t.Fatal("wait went on for a stopped mesh")
	}
	if err := m.Dial(toNode("127.0.0.1:2")); !errors.Is(err, wire.ErrClosed) {
		t.Fatalf("dial after stop: %v", err)
	}
	if err := m.SetListen(model.ListenConfig{Addr: "127.0.0.1:0", Password: "pw"}); !errors.Is(err, wire.ErrClosed) {
		t.Fatalf("listen after stop: %v", err)
	}
	if !m.Undial(silent) {
		t.Fatal("target gone after stop")
	}
}

// Shutdown: a grace period that ends with work in flight; while it drains, new links are refused and one that says
// hello is told to drain too.
// Shutdown：宽限期结束时仍有在途工作；排空期间新连接被拒绝，发送 hello 的连接也被告知下线。
func TestShutdownEdges(t *testing.T) {
	node, r := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub})
	node.opts.Log = testLog(t)
	block := make(chan struct{})
	Handle(r, "/block", []string{model.RoleHub}, 0, func(ctx context.Context, _ model.Identity, _ *struct{}) (any, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, nil
	})
	addr := freeAddr(t)
	start(t, node, addr)
	ctx := ctxT(t)

	early := rawPeer(t, addr, wire.Handlers{})
	if _, err := early.Call(ctx, wire.Head{Method: model.MethodHello}, hello(model.Identity{Role: model.RoleHub, ID: "h1"})); err != nil {
		t.Fatal(err)
	}
	late := rawPeer(t, addr, wire.Handlers{})
	called := make(chan struct{})
	go func() {
		close(called)
		_, _ = early.Call(ctx, wire.Head{Method: "/block"}, nil)
	}()
	<-called
	waitFor(t, "call in flight", func() bool { return node.inflight() > 0 })

	done := make(chan struct{})
	go func() { node.Shutdown(ctx); close(done) }()
	waitFor(t, "draining", func() bool { return node.draining.Load() })
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := wire.Dial(dctx, "ws://"+addr+WSPath, wire.DialConfig{Engine: testEngine(), ConnConfig: wire.ConnConfig{Auth: wire.Password("pw")}}); !errors.Is(err, wire.ErrRefused) {
		t.Fatalf("dial while draining: %v", err)
	}
	if _, err := late.Call(ctx, wire.Head{Method: model.MethodHello}, hello(model.Identity{Role: model.RoleHub, ID: "h2"})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "late link told to drain", late.PeerDraining)
	close(block)
	<-done

	// A grace period already over closes at once, work in flight or not.
	// 已结束的宽限期立即关闭，无论是否有在途工作。
	node2, r2 := newTestMesh(t, model.RoleNode, "n2", []string{model.RoleHub})
	node2.opts.Log = testLog(t)
	block2 := make(chan struct{})
	defer close(block2)
	Handle(r2, "/block", []string{model.RoleHub}, 0, func(ctx context.Context, _ model.Identity, _ *struct{}) (any, error) {
		select {
		case <-block2:
		case <-ctx.Done():
		}
		return nil, nil
	})
	addr2 := freeAddr(t)
	start(t, node2, addr2)
	wc := rawPeer(t, addr2, wire.Handlers{})
	if _, err := wc.Call(ctx, wire.Head{Method: model.MethodHello}, hello(model.Identity{Role: model.RoleHub, ID: "h1"})); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = wc.Call(ctx, wire.Head{Method: "/block"}, nil) }()
	waitFor(t, "call in flight", func() bool { return node2.inflight() > 0 })
	over, cancel3 := context.WithCancel(ctx)
	cancel3()
	node2.Shutdown(over)
}

// The admin methods beyond the happy path: a dial target changed in place, too many targets, a kick, a listener
// address refused or one that cannot bind.
// 管理方法正常路径之外的情况：就地修改的拨号目标、过多的目标、踢出、被拒绝或无法绑定的监听地址。
func TestAdminEdges(t *testing.T) {
	node, _ := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub})
	r := NewRouter()
	var mu sync.Mutex
	cur := Settings{}
	RegisterAdmin(r, node, func(_ context.Context, fn func(*Settings) error) error {
		mu.Lock()
		defer mu.Unlock()
		next := Settings{Peers: append([]model.DialReq(nil), cur.Peers...), Listen: cur.Listen}
		if err := fn(&next); err != nil {
			return err
		}
		cur = next
		if cur.Listen != nil {
			_ = node.SetListen(*cur.Listen)
		}
		return nil
	})
	local := model.Identity{Role: model.RoleLocal}
	ctx := ctxT(t)
	invoke := func(method string, req any) (any, error) {
		raw, _ := json.Marshal(req)
		return r.Invoke(ctx, local, method, raw)
	}
	if _, err := invoke(model.MethodDial, model.DialReq{Addr: "127.0.0.1:9", Password: "a", Role: model.RoleHub}); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(model.MethodDial, model.DialReq{Addr: "127.0.0.1:9", Password: "b", Role: model.RoleHub}); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(model.MethodDial, model.DialReq{Addr: "127.0.0.1:9", Role: model.RoleHub}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(cur.Peers) != 1 || cur.Peers[0].Password != "b" {
		t.Fatalf("peers %+v", cur.Peers)
	}
	for i := len(cur.Peers); i < MaxTargets; i++ {
		cur.Peers = append(cur.Peers, model.DialReq{Addr: net.JoinHostPort("127.0.0.2", string(rune('0'+i%10))+"0"), Password: "p"})
	}
	mu.Unlock()
	if _, err := invoke(model.MethodDial, model.DialReq{Addr: "127.0.0.1:10", Password: "a", Role: model.RoleHub}); codeOf(err) != model.CodeBadRequest {
		t.Fatalf("one target too many: %v", err)
	}
	out, err := invoke(model.MethodKick, model.KickReq{ID: "nobody"})
	if err != nil || out.(map[string]int)["closed"] != 0 {
		t.Fatalf("kick: %v %v", out, err)
	}
	if _, err := invoke(model.MethodListen, model.ListenReq{Addr: "nope", Password: "pw"}); codeOf(err) != model.CodeBadRequest {
		t.Fatalf("bad listen address: %v", err)
	}
	if _, err := invoke(model.MethodListen, model.ListenReq{Addr: "192.0.2.1:0", Password: "pw"}); codeOf(err) != model.CodeInternal {
		t.Fatalf("listen that cannot bind: %v", err)
	}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The accessors the roles use: routes listed in order, limits changed at runtime, a peer-up callback, the
// listener's address, the peers method.
// 角色使用的访问器：按序列出的路由、运行时修改的限制、对端上线回调、监听地址、peers 方法。
func TestAccessors(t *testing.T) {
	node, r := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub})
	Handle(r, "/a", nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) { return nil, nil })
	HandleStream(r, "/s2", nil, func(context.Context, model.Identity, *struct{}, *wire.Stream) (any, error) { return nil, nil })
	HandleStream(r, "/s1", nil, func(context.Context, model.Identity, *struct{}, *wire.Stream) (any, error) { return nil, nil })
	RegisterAdmin(r, node, func(context.Context, func(*Settings) error) error { return nil })
	if rs := r.Routes(); len(rs) < 2 || rs[0].Method != "/a" {
		t.Fatalf("routes %v", rs)
	}
	if rs := r.StreamRoutes(); len(rs) != 2 || rs[0].Method != "/s1" {
		t.Fatalf("stream routes %v", rs)
	}
	if _, ok := r.Lookup("/a"); !ok {
		t.Fatal("lookup")
	}
	node.SetLimits(Options{MaxInflight: 7})
	if node.o().MaxInflight != 7 || node.o().PingInterval <= 0 {
		t.Fatalf("limits %+v", node.o())
	}
	up := make(chan model.PeerConn, 1)
	node.OnPeerUp(func(p model.PeerConn) { up <- p })
	addr := freeAddr(t)
	start(t, node, addr)
	if node.ListenAddr() != addr {
		t.Fatalf("listen addr %q", node.ListenAddr())
	}
	hub, _ := newTestMesh(t, model.RoleHub, "h1", []string{model.RoleNode})
	start(t, hub, "", toNode(addr))
	if p := <-up; p.ID != "h1" {
		t.Fatalf("peer up %+v", p)
	}
	out, err := r.Invoke(ctxT(t), model.Identity{Role: model.RoleLocal}, model.MethodPeers, nil)
	if err != nil || len(out.(model.PeersResp).Conns) != 1 {
		t.Fatalf("peers %v %v", out, err)
	}
	if code, msg := ErrCode(errors.New("x")); code != model.CodeInternal || msg != "x" {
		t.Fatalf("plain error: %d %q", code, msg)
	}
}
