package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/agent/internal/agenttest"
	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/wire"
	"github.com/lesismal/fit/upstream"

	"github.com/lesismal/fib"
)

// testEngine is the engine the fake peers listen on.
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
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// proxyFunc fakes a proxy stream handler of a node or hub.
// proxyFunc 模拟节点或 hub 的转发流处理函数。
type proxyFunc func(ctx context.Context, req *model.ProxyReq, st *wire.Stream) (any, error)

// peer is a fake hub or node on its own mesh.
// peer 在自己的互联上模拟的 hub 或节点。
type peer struct {
	id, addr string
	m        *mesh.Mesh
	mu       sync.Mutex
	proxy    proxyFunc
	reqs     []model.ProxyReq
}

func (p *peer) setProxy(fn proxyFunc) { p.mu.Lock(); p.proxy = fn; p.mu.Unlock() }

func (p *peer) requests() []model.ProxyReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]model.ProxyReq(nil), p.reqs...)
}

func (p *peer) serve(ctx context.Context, _ model.Identity, req *model.ProxyReq, st *wire.Stream) (any, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, *req)
	fn := p.proxy
	p.mu.Unlock()
	return fn(ctx, req, st)
}

// answer returns a proxy that answers with status and the given frames, naming node as the server.
// answer 返回以 status 与给定帧应答、并声明由 node 提供服务的转发函数。
func answer(node string, status int, frames ...string) proxyFunc {
	return func(ctx context.Context, req *model.ProxyReq, st *wire.Stream) (any, error) {
		head, _ := json.Marshal(model.ProxyHead{Status: status, ContentType: "text/event-stream", Node: node})
		if err := st.Send(ctx, head); err != nil {
			return nil, err
		}
		for _, f := range frames {
			if err := st.Send(ctx, []byte(f)); err != nil {
				return nil, err
			}
		}
		return model.ProxyResult{Node: node, Model: req.Model, Status: status}, nil
	}
}

func newPeer(t *testing.T, role, id string, setup func(r *mesh.Router, p *peer)) *peer {
	t.Helper()
	p := &peer{id: id, addr: freeAddr(t)}
	r := mesh.NewRouter()
	setup(r, p)
	p.m = mesh.New(mesh.Options{
		Engine:      testEngine(),
		Self:        func() model.Identity { return model.Identity{Role: role, ID: id, Name: id, StartedAt: 1} },
		AcceptRoles: []string{model.RoleClient, model.RoleHub, model.RoleNode}, PingInterval: time.Second,
		RetryInterval: 300 * time.Millisecond,
	}, r)
	t.Cleanup(p.m.Stop)
	if err := p.m.Configure(model.ListenConfig{Addr: p.addr, Password: "pw"}, nil); err != nil {
		t.Fatal(err)
	}
	return p
}

func newNode(t *testing.T, id string, models ...model.ModelView) *peer {
	return newPeer(t, model.RoleNode, id, func(r *mesh.Router, p *peer) {
		mesh.Handle(r, model.MethodNodeInfo, []string{model.RoleClient}, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
			return model.NodeInfo{ID: id, Name: id, Models: models, Seq: 1}, nil
		})
		mesh.HandleStream(r, model.MethodNodeProxy, []string{model.RoleClient}, p.serve)
		p.proxy = answer(id, 200, "node:"+id)
	})
}

func newHub(t *testing.T, id string, models ...model.HubModel) *peer {
	return newPeer(t, model.RoleHub, id, func(r *mesh.Router, p *peer) {
		mesh.Handle(r, model.MethodHubModels, []string{model.RoleClient}, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
			return model.HubModels{Hub: id, Models: models, Seq: 1}, nil
		})
		mesh.HandleStream(r, model.MethodHubProxy, []string{model.RoleClient}, p.serve)
		p.proxy = answer(id+"-node", 200, "hub:"+id)
	})
}

type fixture struct {
	c    *Client
	r    *mesh.Router
	fit  *agenttest.Fit
	base string
	key  string
}

var clientID = "client-test"

// newFixture runs a client in fit dialing peers, with one API key made on
// its pages, and its agent API on a binding of its own.
func newFixture(t *testing.T, peers ...*peer) *fixture {
	t.Helper()
	var dials []string
	for _, p := range peers {
		role := model.RoleNode
		if strings.HasPrefix(p.id, "hub") {
			role = model.RoleHub
		}
		dials = append(dials, `{"address": "`+p.addr+`", "password": "pw", "role": "`+role+`"}`)
	}
	f := &fixture{}
	f.fit = agenttest.Start(t, `{"agent": {"client": {"id": "`+clientID+`", "name": "client", "console": `+agenttest.Console()+`,
		"api": {"address": "127.0.0.1:0"}, "peers": [`+strings.Join(dials, ",")+`]}}}`, "")
	f.c = f.fit.Role("client").(*Client)
	f.r = f.c.Router
	f.base = f.c.API()
	var k KeyCreateResp
	if err := agenttest.Login(t, f.c.Console().URL()).RPC(MethodClientKeysCreate, KeyCreateReq{Name: "test"}, &k); err != nil {
		t.Fatal(err)
	}
	f.key = k.Key
	if !strings.HasPrefix(f.key, KeyPrefix) || len(f.key) != len(KeyPrefix)+43 {
		t.Fatalf("bad key format %q", f.key)
	}
	if cur := string(f.fit.Current()); !strings.Contains(cur, hashKey(f.key)) || strings.Contains(cur, f.key) {
		t.Fatalf("key not saved hashed: %s", cur)
	}
	waitFor(t, "catalog", func() bool {
		n := 0
		for _, m := range f.c.cat.list() {
			n += len(m.Routes)
		}
		return n >= len(peers)
	})
	return f
}

// do sends an agent request; hdr sets headers ("" values skipped).
// do 发送 agent 请求；hdr 设置请求头（值为空的跳过）。
func (f *fixture) do(t *testing.T, ctx context.Context, method, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, f.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (f *fixture) bearer() map[string]string {
	return map[string]string{"Authorization": "Bearer " + f.key}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ready(name, api string) model.ModelView {
	return model.ModelView{Name: name, API: api, State: model.ModelReady}
}

func TestAuth(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI), ready("claude", model.APIAnthropic))
	f := newFixture(t, node)
	chat := `{"model":"m","messages":[]}`

	resp := f.do(t, context.Background(), "POST", "/v1/chat/completions", chat, nil)
	body := readAll(t, resp)
	var oe struct {
		Error struct {
			Message, Type string
			Code          *string
		}
	}
	if resp.StatusCode != 401 || json.Unmarshal([]byte(body), &oe) != nil || oe.Error.Type == "" || oe.Error.Message == "" ||
		oe.Error.Code == nil || *oe.Error.Code != "invalid_api_key" {
		t.Fatalf("missing key: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		t.Fatal("console page served for the agent API")
	}
	resp = f.do(t, context.Background(), "POST", "/v1/chat/completions", chat, map[string]string{"Authorization": "Bearer ah-wrong"})
	if readAll(t, resp); resp.StatusCode != 401 {
		t.Fatalf("bad key: %d", resp.StatusCode)
	}
	// Anthropic shape for /v1/messages. / /v1/messages 使用 Anthropic 格式。
	resp = f.do(t, context.Background(), "POST", "/v1/messages", `{"model":"claude"}`, map[string]string{"x-api-key": "nope"})
	body = readAll(t, resp)
	var ae struct {
		Type  string
		Error struct{ Type, Message string }
	}
	if resp.StatusCode != 401 || json.Unmarshal([]byte(body), &ae) != nil || ae.Type != "error" || ae.Error.Type != "authentication_error" {
		t.Fatalf("anthropic bad key: %d %s", resp.StatusCode, body)
	}

	resp = f.do(t, context.Background(), "POST", "/v1/chat/completions", chat, f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 200 || body != "node:node-a" {
		t.Fatalf("bearer: %d %q", resp.StatusCode, body)
	}
	resp = f.do(t, context.Background(), "POST", "/v1/messages", `{"model":"claude"}`,
		map[string]string{"x-api-key": f.key, "anthropic-version": "2023-06-01", "anthropic-beta": "x", "Cookie": "c=1"})
	if body := readAll(t, resp); resp.StatusCode != 200 || body != "node:node-a" {
		t.Fatalf("x-api-key: %d %q", resp.StatusCode, body)
	}
	last := node.requests()[len(node.requests())-1]
	if last.API != model.APIAnthropic || last.Path != "messages" || last.Model != "claude" || string(last.Body) != `{"model":"claude"}` ||
		len(last.Header) != 2 || last.Header["anthropic-version"] != "2023-06-01" || last.Header["anthropic-beta"] != "x" {
		t.Fatalf("proxy req = %+v", last)
	}
	keys := f.c.keys.list()
	if len(keys) != 1 || keys[0].Calls != 2 || keys[0].LastUsedAt == 0 {
		t.Fatalf("usage = %+v", keys)
	}
	// Usage is counted in memory, never written back.
	if strings.Contains(string(f.fit.Current()), "calls") {
		t.Fatal("usage written into the config")
	}
}

func TestModelsAndErrors(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI), model.ModelView{Name: "busy", API: model.APIOpenAI, State: model.ModelError})
	hub := newHub(t, "hub-a", model.HubModel{Name: "claude", API: model.APIAnthropic, Nodes: []string{"x"}, Ready: 1})
	f := newFixture(t, node, hub)

	resp := f.do(t, context.Background(), "GET", "/v1/models", "", f.bearer())
	body := readAll(t, resp)
	var list struct {
		Object string
		Data   []struct{ ID, Object, Type string }
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &list) != nil || list.Object != "list" || len(list.Data) != 3 ||
		list.Data[0].ID != "busy" || list.Data[1].ID != "claude" || list.Data[2].ID != "m" || list.Data[0].Object != "model" || list.Data[0].Type != "model" {
		t.Fatalf("models: %d %s", resp.StatusCode, body)
	}
	if resp := f.do(t, context.Background(), "GET", "/v1/models", "", nil); readAll(t, resp) == "" || resp.StatusCode != 401 {
		t.Fatalf("models without key: %d", resp.StatusCode)
	}
	if resp := f.do(t, context.Background(), "GET", "/v1/models/claude", "", map[string]string{"x-api-key": f.key}); !strings.Contains(readAll(t, resp), `"id":"claude"`) {
		t.Fatalf("model get: %d", resp.StatusCode)
	}

	cases := []struct {
		path, body string
		status     int
		want       string
	}{
		{"/v1/chat/completions", `{"model":"nope"}`, 404, "model_not_found"},
		{"/v1/chat/completions", `{"model":"claude"}`, 404, "anthropic API"},
		{"/v1/chat/completions", `{"model":"busy"}`, 503, "no ready node"},
		{"/v1/chat/completions", `{"model":`, 400, "invalid_request_error"},
		{"/v1/chat/completions", `{"messages":[]}`, 400, "missing"},
		{"/v1/messages", `{"model":"m"}`, 404, "not_found_error"},
		{"/v1/unknown", `{"model":"m"}`, 404, "unknown_url"},
	}
	for _, tc := range cases {
		resp := f.do(t, context.Background(), "POST", tc.path, tc.body, f.bearer())
		body := readAll(t, resp)
		if resp.StatusCode != tc.status || !strings.Contains(body, tc.want) || resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s: %d %s", tc.path, tc.body, resp.StatusCode, body)
		}
	}
	// An upstream failure that is not retryable: 502. / 不可重试的上游失败：502。
	node.setProxy(func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
		return nil, mesh.Errorf(model.CodeInternal, "upstream exploded")
	})
	resp = f.do(t, context.Background(), "POST", "/v1/chat/completions", `{"model":"m"}`, f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 502 || !strings.Contains(body, "upstream exploded") || !strings.Contains(body, `"server_error"`) {
		t.Fatalf("upstream failure: %d %s", resp.StatusCode, body)
	}
	if len(node.requests()) != 1 {
		t.Fatalf("non-retryable error retried: %d", len(node.requests()))
	}

	out, err := f.r.Invoke(context.Background(), model.Identity{Role: model.RoleLocal}, MethodClientModels, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ms := out.([]CatalogModel); len(ms) != 3 || ms[1].Name != "claude" || ms[1].Routes[0].Kind != RouteHub || !ms[1].Ready || ms[0].Ready {
		t.Fatalf("catalog = %+v", ms)
	}
	st := f.c.stats.snapshot()
	if st.Total.Calls != 1 || st.Total.Errors != 1 || st.Total.Inflight != 0 {
		t.Fatalf("stats = %+v", st)
	}
	// A peer that goes away leaves the catalog. / 离开的对端从目录中移除。
	hub.m.Stop()
	waitFor(t, "hub dropped", func() bool { _, known, _ := f.c.cat.routes("claude", model.APIAnthropic); return !known })
}

func TestRoutingPreferenceAndRetry(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI))
	hub1 := newHub(t, "hub-1", model.HubModel{Name: "m", API: model.APIOpenAI, Nodes: []string{"n1"}, Ready: 1})
	hub2 := newHub(t, "hub-2", model.HubModel{Name: "m", API: model.APIOpenAI, Nodes: []string{"n2"}, Ready: 1})
	f := newFixture(t, node, hub1, hub2)
	call := func() (string, string) {
		resp := f.do(t, context.Background(), "POST", "/v1/chat/completions", `{"model":"m"}`, f.bearer())
		body := readAll(t, resp)
		if resp.StatusCode != 200 {
			t.Fatalf("call: %d %s", resp.StatusCode, body)
		}
		return body, resp.Header.Get("X-Agenthub-Node")
	}
	// The direct node wins. / 直连节点优先。
	for i := 0; i < 3; i++ {
		if body, by := call(); body != "node:node-a" || by != "node-a" {
			t.Fatalf("direct node not preferred: %q %q", body, by)
		}
	}
	// The node cannot serve now: retried through a hub, which is told to skip that node.
	// 节点当前不能提供服务：经 hub 重试，并告知 hub 跳过该节点。
	node.setProxy(func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
		return nil, mesh.Errorf(model.CodeUnavailable, "busy")
	})
	if body, _ := call(); !strings.HasPrefix(body, "hub:") {
		t.Fatalf("not retried on a hub: %q", body)
	}
	var hubReq model.ProxyReq
	for _, h := range []*peer{hub1, hub2} {
		if r := h.requests(); len(r) == 1 {
			hubReq = r[0]
		}
	}
	if len(hubReq.Exclude) != 1 || hubReq.Exclude[0] != "node-a" {
		t.Fatalf("hub request = %+v", hubReq)
	}
	// The node pushes that the model is no longer ready: hubs round robin, a failing one is skipped.
	// 节点推送模型不再就绪：hub 之间轮询，失败的被跳过。
	if err := node.m.Push(clientID, model.MethodNodeUpdate, model.NodeInfo{ID: "node-a", Seq: 2,
		Models: []model.ModelView{{Name: "m", API: model.APIOpenAI, State: model.ModelError}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "node update", func() bool { r, _, _ := f.c.cat.routes("m", model.APIOpenAI); return len(r) == 2 })
	n0 := len(node.requests())
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		body, _ := call()
		seen[body]++
	}
	if seen["hub:hub-1"] != 2 || seen["hub:hub-2"] != 2 || len(node.requests()) != n0 {
		t.Fatalf("round robin = %v", seen)
	}
	hub1.setProxy(func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
		return nil, mesh.Errorf(model.CodeUnavailable, "no node")
	})
	for i := 0; i < 2; i++ {
		if body, _ := call(); body != "hub:hub-2" {
			t.Fatalf("failing hub not skipped: %q", body)
		}
	}
	// Every route unavailable: 503. / 全部路由不可用：503。
	hub2.setProxy(func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
		return nil, mesh.Errorf(model.CodeUnavailable, "no node")
	})
	resp := f.do(t, context.Background(), "POST", "/v1/chat/completions", `{"model":"m"}`, f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 503 || !strings.Contains(body, "no node") {
		t.Fatalf("all unavailable: %d %s", resp.StatusCode, body)
	}
	// A hub push updates the catalog. / hub 推送更新目录。
	if err := hub1.m.Push(clientID, model.MethodHubModelsUpdate, model.HubModels{Hub: "hub-1", Seq: 5}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "hub update", func() bool { r, _, _ := f.c.cat.routes("m", model.APIOpenAI); return len(r) == 1 && r[0].ID == "hub-2" })
}

func TestStreamFlushAndCancel(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI))
	f := newFixture(t, node)
	gated := func(gate, canceled chan struct{}) proxyFunc {
		return func(ctx context.Context, req *model.ProxyReq, st *wire.Stream) (any, error) {
			head, _ := json.Marshal(model.ProxyHead{Status: 200, ContentType: "text/event-stream", Node: "node-a"})
			_ = st.Send(ctx, head)
			_ = st.Send(ctx, []byte("data: 1\n\n"))
			_ = st.Send(ctx, []byte("data: 2\n\n"))
			select { // held until the agent saw the first frames / 直到 agent 看到前面的帧
			case <-gate:
			case <-ctx.Done():
				close(canceled)
				return nil, ctx.Err()
			}
			_ = st.Send(ctx, []byte("data: [DONE]\n\n"))
			return model.ProxyResult{Status: 200}, nil
		}
	}
	gate := make(chan struct{})
	node.setProxy(gated(gate, make(chan struct{})))

	resp := f.do(t, context.Background(), "POST", "/v1/chat/completions", `{"model":"m","stream":true}`, f.bearer())
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-cache" ||
		resp.Header.Get("X-Agenthub-Node") != "node-a" {
		t.Fatalf("head: %d %v", resp.StatusCode, resp.Header)
	}
	br := bufio.NewReader(resp.Body)
	var got []string
	for len(got) < 4 { // two events, each "data: n" + blank line / 两个事件，各为 "data: n" + 空行
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read before gate: %v", err)
		}
		got = append(got, line)
	}
	if strings.Join(got, "") != "data: 1\n\ndata: 2\n\n" {
		t.Fatalf("frames = %q", got)
	}
	close(gate)
	rest, _ := io.ReadAll(br)
	resp.Body.Close()
	if string(rest) != "data: [DONE]\n\n" {
		t.Fatalf("rest = %q", rest)
	}

	// The agent disconnects while tokens keep coming: the node's stream is canceled. (pkg/httpserver notices a
	// disconnect when a write fails, so the upstream keeps sending here, as a generating model does.)
	// agent 在 token 持续到达时断开：节点的流被取消。（pkg/httpserver 在写入失败时才发现断开，所以这里上游持续发送，
	// 与正在生成的模型一样。）
	canceled := make(chan struct{})
	node.setProxy(func(ctx context.Context, req *model.ProxyReq, st *wire.Stream) (any, error) {
		head, _ := json.Marshal(model.ProxyHead{Status: 200, ContentType: "text/event-stream", Node: "node-a"})
		_ = st.Send(ctx, head)
		for i := 1; ; i++ {
			if err := st.Send(ctx, []byte(fmt.Sprintf("data: %d\n\n", i))); err != nil || ctx.Err() != nil {
				close(canceled)
				return nil, ctx.Err()
			}
			select {
			case <-ctx.Done():
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	resp = f.do(t, ctx, "POST", "/v1/chat/completions", `{"model":"m","stream":true}`, f.bearer())
	br = bufio.NewReader(resp.Body)
	if line, err := br.ReadString('\n'); err != nil || line != "data: 1\n" {
		t.Fatalf("first line %q %v", line, err)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream stream not canceled after the agent left")
	}
	waitFor(t, "inflight back to 0", func() bool {
		st := f.c.stats.snapshot()
		f.c.cat.mu.Lock()
		n := len(f.c.cat.inflight)
		f.c.cat.mu.Unlock()
		return st.Total.Inflight == 0 && n == 0
	})
}

func TestKeysMethods(t *testing.T) {
	f := newFixture(t)
	local := model.Identity{Role: model.RoleLocal}
	if _, err := f.r.Invoke(context.Background(), local, MethodClientKeysCreate, json.RawMessage(`{"name":" "}`)); err == nil {
		t.Fatal("empty name accepted")
	}
	out, err := f.r.Invoke(context.Background(), local, MethodClientKeysList, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := out.([]KeyView)
	if len(keys) != 1 || keys[0].Name != "test" || !strings.HasPrefix(f.key, keys[0].Prefix) || len(keys[0].Prefix) != shownPrefix {
		t.Fatalf("keys = %+v", keys)
	}
	if b, _ := json.Marshal(keys); strings.Contains(string(b), "hash") {
		t.Fatal("hash listed")
	}
	// No peers: an empty list, not null. / 没有对端：空列表而不是 null。
	if resp := f.do(t, context.Background(), "GET", "/v1/models", "", f.bearer()); !strings.Contains(readAll(t, resp), `"data":[]`) {
		t.Fatalf("empty models: %d", resp.StatusCode)
	}
	if _, err := f.r.Invoke(context.Background(), local, MethodClientKeysDelete, json.RawMessage(`{"id":"`+keys[0].ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.fit.Current()), "api_keys") {
		t.Fatal("delete not written")
	}
	resp := f.do(t, context.Background(), "GET", "/v1/models", "", f.bearer())
	if readAll(t, resp); resp.StatusCode != 401 {
		t.Fatalf("deleted key still works: %d", resp.StatusCode)
	}
	if _, err := f.r.Invoke(context.Background(), model.Identity{Role: model.RoleHub}, MethodClientKeysList, nil); err == nil {
		t.Fatal("a hub may list keys")
	}
}
