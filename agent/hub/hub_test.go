package hub

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

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/wire"
	"github.com/lesismal/fit/upstream"

	"github.com/lesismal/fib"
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

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func newMesh(t *testing.T, role, id string, started int64, accept []string, r *mesh.Router) *mesh.Mesh {
	m := mesh.New(mesh.Options{
		Engine:        testEngine(),
		Self:          func() model.Identity { return model.Identity{Role: role, ID: id, Name: id, StartedAt: started} },
		AcceptRoles:   accept,
		PingInterval:  time.Second,
		RetryInterval: 300 * time.Millisecond,
	}, r)
	t.Cleanup(m.Stop)
	return m
}

// fakeNode is an in-process node: its info, a proxy behaviour and a couple of management methods.
// fakeNode 进程内的假节点：其信息、转发行为与几个管理方法。
type fakeNode struct {
	id    string
	m     *mesh.Mesh
	mu    sync.Mutex
	info  model.NodeInfo
	proxy func(ctx context.Context, req *model.ProxyReq, st *wire.Stream) (any, error)
	calls atomic.Int32 // proxy streams opened / 打开的转发流数
}

func (n *fakeNode) setProxy(fn func(ctx context.Context, req *model.ProxyReq, st *wire.Stream) (any, error)) {
	n.mu.Lock()
	n.proxy = fn
	n.mu.Unlock()
}

func (n *fakeNode) setInfo(fn func(*model.NodeInfo)) model.NodeInfo {
	n.mu.Lock()
	defer n.mu.Unlock()
	fn(&n.info)
	n.info.Seq++
	return n.info
}

func serve(ctx context.Context, st *wire.Stream, node string, frames ...string) (any, error) {
	head, _ := json.Marshal(model.ProxyHead{Status: 200, ContentType: "text/event-stream", Node: node})
	if err := st.Send(ctx, head); err != nil {
		return nil, err
	}
	var n int64
	for _, f := range frames {
		if err := st.Send(ctx, []byte(f)); err != nil {
			return nil, err
		}
		n += int64(len(f))
	}
	return model.ProxyResult{Node: node, Status: 200, Bytes: n}, nil
}

func startNode(t *testing.T, id, hubAddr string, models ...model.ModelView) *fakeNode {
	n := &fakeNode{id: id, info: model.NodeInfo{ID: id, Name: "name-" + id, Models: models, Seq: 1}}
	n.proxy = func(ctx context.Context, req *model.ProxyReq, st *wire.Stream) (any, error) {
		return serve(ctx, st, id, "a", "b", "c")
	}
	r := mesh.NewRouter()
	peers := []string{model.RoleHub, model.RoleClient}
	mesh.Handle(r, model.MethodNodeInfo, peers, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.info, nil
	})
	mesh.HandleStream(r, model.MethodNodeProxy, peers, func(ctx context.Context, _ model.Identity, req *model.ProxyReq, st *wire.Stream) (any, error) {
		n.calls.Add(1)
		n.mu.Lock()
		fn := n.proxy
		n.mu.Unlock()
		return fn(ctx, req, st)
	})
	mesh.Handle(r, model.MethodNodeModelList, peers, 0, func(_ context.Context, c model.Identity, _ *struct{}) (any, error) {
		if id == "n2" {
			return nil, mesh.Errorf(model.CodeForbidden, "hub management is off")
		}
		return []model.Model{{Name: "m1", API: model.APIOpenAI, BaseURL: "http://x", APIKey: "sk-…" + c.ID}}, nil
	})
	mesh.HandleStream(r, model.MethodNodeJobFollow, peers, func(ctx context.Context, _ model.Identity, req *model.JobGetReq, st *wire.Stream) (any, error) {
		for _, s := range []string{"one ", "two "} {
			b, _ := json.Marshal(model.Job{ID: req.ID, Status: model.JobRunning, Output: s})
			if err := st.Send(ctx, b); err != nil {
				return nil, err
			}
		}
		return model.Job{ID: req.ID, Status: model.JobDone}, nil
	})
	n.m = newMesh(t, model.RoleNode, id, time.Now().UnixNano(), []string{model.RoleHub}, r)
	if err := n.m.Configure(model.ListenConfig{}, []model.DialReq{{Addr: hubAddr, Password: "pw", Role: model.RoleHub}}); err != nil {
		t.Fatal(err)
	}
	return n
}

// env is a hub with two fake nodes and a client.
// env 带两个假节点与一个 client 的 hub。
type env struct {
	hub    *Hub
	hr     *mesh.Router
	n1, n2 *fakeNode
	client *mesh.Mesh

	mu     sync.Mutex
	pushed []model.HubModels
}

func (e *env) last() model.HubModels {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pushed) == 0 {
		return model.HubModels{}
	}
	return e.pushed[len(e.pushed)-1]
}

func (e *env) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pushed)
}

func ready(name string, inflight int) model.ModelView {
	return model.ModelView{Name: name, API: model.APIOpenAI, State: model.ModelReady, Inflight: inflight}
}

func newEnv(t *testing.T, m1, m2 []model.ModelView) *env {
	addr := freeAddr(t)
	e := &env{hr: mesh.NewRouter()}
	hm := newMesh(t, model.RoleHub, "h1", 1, []string{model.RoleNode, model.RoleClient}, e.hr)
	e.hub = newHub(hm, hm.Self, nil)
	e.hub.register(e.hr)
	t.Cleanup(e.hub.close)
	if err := hm.Configure(model.ListenConfig{Addr: addr, Password: "pw"}, nil); err != nil {
		t.Fatal(err)
	}
	e.n1 = startNode(t, "n1", addr, m1...)
	e.n2 = startNode(t, "n2", addr, m2...)
	e.client = newMesh(t, model.RoleClient, "c1", 1, []string{model.RoleHub}, mesh.NewRouter())
	e.client.OnPush(model.MethodHubModelsUpdate, []string{model.RoleHub}, func(_ model.Identity, msg json.RawMessage) {
		var hm model.HubModels
		if err := json.Unmarshal(msg, &hm); err == nil {
			e.mu.Lock()
			e.pushed = append(e.pushed, hm)
			e.mu.Unlock()
		}
	})
	if err := e.client.Configure(model.ListenConfig{}, []model.DialReq{{Addr: addr, Password: "pw", Role: model.RoleHub}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connections", func() bool { return hm.Online("n1") && hm.Online("n2") && e.client.Online("h1") })
	waitFor(t, "node info", func() bool {
		ns := e.hub.Nodes()
		return len(ns) == 2 && ns[0].Info.Seq > 0 && ns[1].Info.Seq > 0
	})
	return e
}

func modelsString(hm model.HubModels) string {
	var b strings.Builder
	for _, m := range hm.Models {
		b.WriteString(m.Name + ":" + strings.Join(m.Nodes, ",") + ":" + string(rune('0'+m.Ready)) + " ")
	}
	return strings.TrimSpace(b.String())
}

func TestAggregationAndPush(t *testing.T) {
	e := newEnv(t, []model.ModelView{ready("m1", 0)},
		[]model.ModelView{ready("m1", 0), ready("m2", 0), {Name: "m3", API: model.APIOpenAI, State: model.ModelError}})
	want := "m1:n1,n2:2 m2:n2:1 m3:n2:0"
	waitFor(t, "aggregated push", func() bool { return modelsString(e.last()) == want })
	seq := e.last().Seq

	// A node update removes m2; a stale update (lower Seq) is dropped.
	// 节点更新移除 m2；旧的更新（Seq 更小）被丢弃。
	e.n2.mu.Lock()
	stale := e.n2.info
	e.n2.mu.Unlock()
	info := e.n2.setInfo(func(i *model.NodeInfo) { i.Models = i.Models[:1] })
	if err := e.n2.m.Push("h1", model.MethodNodeUpdate, info); err != nil {
		t.Fatal(err)
	}
	if err := e.n2.m.Push("h1", model.MethodNodeUpdate, stale); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "update push", func() bool { return modelsString(e.last()) == "m1:n1,n2:2" })
	if e.last().Seq <= seq {
		t.Fatalf("seq did not increase: %d -> %d", seq, e.last().Seq)
	}
	time.Sleep(3 * coalesce)
	if got := modelsString(e.hub.Models()); got != "m1:n1,n2:2" {
		t.Fatalf("stale update applied: %s", got)
	}
	// MethodHubModels over the mesh.
	// 经互联调用 MethodHubModels。
	hm, err := mesh.Call[model.HubModels](context.Background(), e.client, "h1", model.MethodHubModels, nil)
	if err != nil || hm.Hub != "h1" || modelsString(hm) != "m1:n1,n2:2" {
		t.Fatalf("hub models: %+v %v", hm, err)
	}

	// A node going offline leaves the aggregation but stays in the table.
	// 节点离线后退出汇总，但仍留在节点表中。
	n := e.count()
	e.n2.m.Stop()
	waitFor(t, "offline push", func() bool { return modelsString(e.last()) == "m1:n1:1" })
	time.Sleep(3 * coalesce)
	if got := e.count(); got != n+1 {
		t.Fatalf("pushes not coalesced: %d -> %d", n, got)
	}
	ns := e.hub.Nodes()
	if len(ns) != 2 || !ns[0].Online || ns[1].Online || ns[1].Info.ID != "n2" || len(ns[0].Conns) != 1 {
		t.Fatalf("nodes = %+v", ns)
	}
}

func openProxy(t *testing.T, ctx context.Context, e *env, req model.ProxyReq) (head model.ProxyHead, body string, res model.ProxyResult, err error) {
	t.Helper()
	st, err := e.client.OpenStream(ctx, "h1", model.MethodHubProxy, req)
	if err != nil {
		return
	}
	first, err := st.Recv(ctx)
	if err != nil {
		return
	}
	if err = json.Unmarshal(first, &head); err != nil {
		return
	}
	for {
		b, rerr := st.Recv(ctx)
		if errors.Is(rerr, io.EOF) {
			err = json.Unmarshal(st.Result(), &res)
			return
		}
		if rerr != nil {
			err = rerr
			return
		}
		body += string(b) + "|"
	}
}

func TestProxyPicksLeastLoaded(t *testing.T) {
	e := newEnv(t, []model.ModelView{ready("m1", 5)}, []model.ModelView{ready("m1", 1)})
	req := model.ProxyReq{API: model.APIOpenAI, Path: "chat/completions", Model: "m1", Body: []byte(`{"model":"m1"}`)}
	head, body, res, err := openProxy(t, context.Background(), e, req)
	if err != nil || head.Node != "n2" || body != "a|b|c|" || res.Node != "n2" || res.Bytes != 3 {
		t.Fatalf("proxy: head=%+v body=%q res=%+v err=%v", head, body, res, err)
	}
	// Exclude the least loaded one: n1 serves.
	// 排除负载最低的节点：由 n1 提供服务。
	req.Exclude = []string{"n2"}
	if head, _, _, err := openProxy(t, context.Background(), e, req); err != nil || head.Node != "n1" {
		t.Fatalf("exclude: %+v %v", head, err)
	}
	// Equal load: round robin.
	// 负载相同：轮询。
	e.n1.m.Push("h1", model.MethodNodeUpdate, e.n1.setInfo(func(i *model.NodeInfo) { i.Models[0].Inflight = 1 }))
	waitFor(t, "update", func() bool {
		ns := e.hub.Nodes()
		return ns[0].Info.Models[0].Inflight == 1 && ns[1].Info.Models[0].Inflight == 1
	})
	seen := map[string]int{}
	for range 4 {
		head, _, _, err := openProxy(t, context.Background(), e, model.ProxyReq{API: model.APIOpenAI, Path: "chat/completions", Model: "m1"})
		if err != nil {
			t.Fatal(err)
		}
		seen[head.Node]++
	}
	if seen["n1"] != 2 || seen["n2"] != 2 {
		t.Fatalf("round robin = %v", seen)
	}
	// Unknown model.
	// 未知模型。
	_, _, _, err = openProxy(t, context.Background(), e, model.ProxyReq{API: model.APIOpenAI, Model: "nope"})
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != model.CodeUnavailable || !strings.Contains(we.Msg, "no node serves model nope") {
		t.Fatalf("unknown model: %v", err)
	}
}

func TestProxyRetriesUnavailable(t *testing.T) {
	e := newEnv(t, []model.ModelView{ready("m1", 0)}, []model.ModelView{ready("m1", 3)})
	busy := func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
		return nil, mesh.Errorf(model.CodeUnavailable, "upstream busy")
	}
	e.n1.setProxy(busy)
	req := model.ProxyReq{API: model.APIOpenAI, Path: "chat/completions", Model: "m1"}
	head, body, _, err := openProxy(t, context.Background(), e, req)
	if err != nil || head.Node != "n2" || body != "a|b|c|" || e.n1.calls.Load() != 1 {
		t.Fatalf("retry: head=%+v body=%q err=%v n1 calls=%d", head, body, err, e.n1.calls.Load())
	}
	// Every node unavailable: CodeUnavailable.
	// 所有节点都不可用：CodeUnavailable。
	e.n2.setProxy(busy)
	_, _, _, err = openProxy(t, context.Background(), e, req)
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != model.CodeUnavailable {
		t.Fatalf("all unavailable: %v", err)
	}
	// Other errors are returned as they are, without retrying.
	// 其他错误原样返回，不重试。
	e.n1.setProxy(func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
		return nil, mesh.Errorf(model.CodeBadRequest, "bad path")
	})
	before := e.n2.calls.Load()
	_, _, _, err = openProxy(t, context.Background(), e, req)
	if !errors.As(err, &we) || we.Code != model.CodeBadRequest || e.n2.calls.Load() != before {
		t.Fatalf("bad request: %v (n2 calls %d -> %d)", err, before, e.n2.calls.Load())
	}
}

func TestProxyCancelReachesNode(t *testing.T) {
	e := newEnv(t, []model.ModelView{ready("m1", 0)}, nil)
	canceled := make(chan struct{})
	e.n1.setProxy(func(ctx context.Context, _ *model.ProxyReq, st *wire.Stream) (any, error) {
		head, _ := json.Marshal(model.ProxyHead{Status: 200, Node: "n1"})
		if err := st.Send(ctx, head); err != nil {
			return nil, err
		}
		if err := st.Send(ctx, []byte("first")); err != nil {
			return nil, err
		}
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := e.client.OpenStream(ctx, "h1", model.MethodHubProxy, model.ProxyReq{API: model.APIOpenAI, Model: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"", "first"} {
		b, err := st.Recv(ctx)
		if err != nil || (want != "" && string(b) != want) {
			t.Fatalf("recv %q: %q %v", want, b, err)
		}
	}
	waitFor(t, "hub inflight", func() bool {
		e.hub.mu.Lock()
		defer e.hub.mu.Unlock()
		return e.hub.inflight["n1"] == 1
	})
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the node handler was not canceled")
	}
	waitFor(t, "hub inflight released", func() bool {
		e.hub.mu.Lock()
		defer e.hub.mu.Unlock()
		return len(e.hub.inflight) == 0
	})
}

func TestNodeCallForwarding(t *testing.T) {
	e := newEnv(t, nil, nil)
	ctx := context.Background()
	local := model.Identity{Role: model.RoleLocal, ID: "console"}
	call := func(id, method string) (json.RawMessage, error) {
		raw, _ := json.Marshal(model.HubNodeCallReq{ID: id, Method: method, Req: json.RawMessage(`{}`)})
		res, err := e.hr.Invoke(ctx, local, model.MethodHubNodeCall, raw)
		if err != nil {
			return nil, err
		}
		b, _ := res.(json.RawMessage)
		return b, nil
	}
	b, err := call("n1", model.MethodNodeModelList)
	if err != nil || !strings.Contains(string(b), `"sk-…h1"`) {
		t.Fatalf("model list: %s %v", b, err)
	}
	if _, err := call("n2", model.MethodNodeModelList); err == nil || !strings.Contains(err.Error(), "does not allow hub management") {
		t.Fatalf("forbidden: %v", err)
	}
	if _, err := call("n1", model.MethodNodeSettingsSet); err == nil {
		t.Fatal("a non-management method was forwarded")
	}
	if _, err := call("n1", model.MethodNodeJobFollow); err == nil {
		t.Fatal("a stream method was forwarded as a call")
	}
	if _, err := call("nx", model.MethodNodeModelList); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline: %v", err)
	}
	// Peers may not call the console methods.
	// 对端不能调用控制台方法。
	if _, err := mesh.Call[json.RawMessage](ctx, e.client, "h1", model.MethodHubNodeCall, model.HubNodeCallReq{ID: "n1", Method: model.MethodNodeModelList}); err == nil {
		t.Fatal("client called the console method")
	}

	// Job follow through the hub stream.
	// 经 hub 流跟随作业。
	raw, _ := json.Marshal(model.HubNodeStreamReq{ID: "n1", Method: model.MethodNodeJobFollow, Req: json.RawMessage(`{"id":7}`)})
	st, err := e.hr.OpenLocal(ctx, local, model.MethodHubNodeStream, raw)
	if err != nil {
		t.Fatal(err)
	}
	var out string
	for {
		b, err := st.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var j model.Job
		_ = json.Unmarshal(b, &j)
		out += j.Output
	}
	var end model.Job
	if err := json.Unmarshal(st.Result(), &end); err != nil || out != "one two " || end.ID != 7 || end.Status != model.JobDone {
		t.Fatalf("follow: %q %+v %v", out, end, err)
	}
}
