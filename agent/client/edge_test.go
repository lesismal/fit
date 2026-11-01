package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/wire"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// extraAPI starts another agent API of f's client with cfg, beside the one of its config.
// extraAPI 以 cfg 为 f 的 client 另起一个 agent API，与其配置中的并存。
func extraAPI(t *testing.T, f *fixture, cfg *config.AgentClientAPI) *apiServer {
	t.Helper()
	if cfg.Address == "" {
		cfg.Address = "127.0.0.1:0"
	}
	s, err := startAPI(f.c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stop)
	return s
}

// post sends a chat call to base with f's key.
// post 用 f 的密钥向 base 发送一次 chat 调用。
func (f *fixture) post(t *testing.T, client *http.Client, base, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.key)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, readAll(t, resp)
}

// selfSigned writes a certificate for 127.0.0.1 and its key into a temp dir.
// selfSigned 在临时目录中写入 127.0.0.1 的证书及其私钥。
func selfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func headFrame(status int, ct string) []byte {
	b, _ := json.Marshal(model.ProxyHead{Status: status, ContentType: ct, Node: "node-a"})
	return b
}

// Requests the API refuses or answers itself, and upstream answers that are not a clean response.
// API 自行拒绝或应答的请求，以及不是正常响应的上游应答。
func TestAPIEdgeAnswers(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI), ready("m", model.APIAnthropic), ready("claude", model.APIAnthropic))
	f := newFixture(t, node)
	ctx := context.Background()

	// A name served in two formats is listed once. / 以两种格式提供的名称只列出一次。
	resp := f.do(t, ctx, "GET", "/v1/models", "", f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 200 || strings.Count(body, `"id":"m"`) != 1 {
		t.Fatalf("models: %d %s", resp.StatusCode, body)
	}
	resp = f.do(t, ctx, "DELETE", "/v1/models", "", f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 404 || !strings.Contains(body, "unknown_url") {
		t.Fatalf("DELETE: %d %s", resp.StatusCode, body)
	}
	resp = f.do(t, ctx, "GET", "/v1/other", "", f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 404 || !strings.Contains(body, "unknown_url") {
		t.Fatalf("GET other: %d %s", resp.StatusCode, body)
	}
	resp = f.do(t, ctx, "GET", "/v1/models/nope", "", f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 404 || !strings.Contains(body, "not found") {
		t.Fatalf("GET unknown model: %d %s", resp.StatusCode, body)
	}

	cases := []struct {
		name   string
		path   string
		body   string
		proxy  proxyFunc
		status int
		want   string
	}{
		{"anthropic 502", "/v1/messages", `{"model":"claude"}`, func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
			return nil, mesh.Errorf(model.CodeInternal, "kaput")
		}, 502, `"api_error"`},
		{"rate limited", "/v1/chat/completions", `{"model":"m"}`, func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
			return nil, mesh.Errorf(model.CodeRateLimited, "slow down")
		}, 429, `"rate_limit_exceeded"`},
		{"bad request", "/v1/chat/completions", `{"model":"m"}`, func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
			return nil, mesh.BadRequest("nope")
		}, 400, "invalid_request_error"},
		{"not found", "/v1/chat/completions", `{"model":"m"}`, func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
			return nil, mesh.Errorf(model.CodeNotFound, "gone")
		}, 404, "gone"},
		{"no head", "/v1/chat/completions", `{"model":"m"}`, func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
			return model.ProxyResult{}, nil
		}, 502, "before its head frame"},
		{"bad head", "/v1/chat/completions", `{"model":"m"}`, func(ctx context.Context, _ *model.ProxyReq, st *wire.Stream) (any, error) {
			_ = st.Send(ctx, []byte("not json"))
			<-ctx.Done()
			return nil, ctx.Err()
		}, 502, "bad head frame"},
		{"upstream 500", "/v1/chat/completions", `{"model":"m"}`, func(ctx context.Context, _ *model.ProxyReq, st *wire.Stream) (any, error) {
			_ = st.Send(ctx, headFrame(500, ""))
			_ = st.Send(ctx, []byte(`{"error":"x"}`))
			return model.ProxyResult{Status: 500}, nil
		}, 500, `{"error":"x"}`},
	}
	for _, tc := range cases {
		node.setProxy(tc.proxy)
		resp := f.do(t, ctx, "POST", tc.path, tc.body, f.bearer())
		body := readAll(t, resp)
		if resp.StatusCode != tc.status || !strings.Contains(body, tc.want) || resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: %d %s %v", tc.name, resp.StatusCode, body, resp.Header)
		}
	}

	// A route to a node that is not linked fails to open and the call moves on; with none left, 503.
	// 指向未连接节点的路由打开失败，调用转向下一条；没有剩余路由时为 503。
	f.c.cat.setNode(model.Identity{ID: "aaa-ghost", Name: "aaa-ghost"}, model.NodeInfo{Models: []model.ModelView{ready("ghost", model.APIOpenAI)}})
	resp = f.do(t, ctx, "POST", "/v1/chat/completions", `{"model":"ghost"}`, f.bearer())
	if body := readAll(t, resp); resp.StatusCode != 503 {
		t.Fatalf("ghost route: %d %s", resp.StatusCode, body)
	}

	// The node's link breaks before the head: the call fails over (here to nothing) with the link's error.
	// 节点链路在头帧之前断开：调用改走其他路由（这里没有），以链路的错误失败。
	started := make(chan struct{})
	node.setProxy(func(ctx context.Context, _ *model.ProxyReq, _ *wire.Stream) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	go func() { <-started; node.m.Stop() }()
	resp = f.do(t, ctx, "POST", "/v1/chat/completions", `{"model":"m"}`, f.bearer())
	if body := readAll(t, resp); resp.StatusCode < 500 {
		t.Fatalf("link broke: %d %s", resp.StatusCode, body)
	}
}

// The API's own limits: its timeouts, calls in flight and connections, on APIs of their own.
// API 自身的限制：超时、在途调用与连接数，各用一个独立的 API。
func TestAPILimits(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI))
	f := newFixture(t, node)
	hc := &http.Client{}

	// No head within first_byte_timeout: 503. / first_byte_timeout 内没有头帧：503。
	node.setProxy(func(ctx context.Context, _ *model.ProxyReq, _ *wire.Stream) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	fb := extraAPI(t, f, &config.AgentClientAPI{FirstByteTimeout: config.Duration(100 * time.Millisecond)})
	if status, body := f.post(t, hc, fb.url(), "/v1/chat/completions", `{"model":"m"}`); status != 503 || !strings.Contains(body, "no response began") {
		t.Fatalf("first byte: %d %s", status, body)
	}
	// Both timers run out together: the first to cancel decides. / 两个计时器同时到期：先取消的那个决定结果。
	both := extraAPI(t, f, &config.AgentClientAPI{FirstByteTimeout: config.Duration(50 * time.Millisecond), Timeout: config.Duration(50 * time.Millisecond)})
	if status, body := f.post(t, hc, both.url(), "/v1/chat/completions", `{"model":"m"}`); status != 503 && status != 504 {
		t.Fatalf("both timers: %d %s", status, body)
	}
	// The whole call past timeout: 504 before the head, a cut response after it.
	// 整个调用超过 timeout：头帧之前为 504，之后为被截断的响应。
	total := extraAPI(t, f, &config.AgentClientAPI{Timeout: config.Duration(100 * time.Millisecond)})
	if status, body := f.post(t, hc, total.url(), "/v1/chat/completions", `{"model":"m"}`); status != 504 {
		t.Fatalf("total before head: %d %s", status, body)
	}
	node.setProxy(func(ctx context.Context, _ *model.ProxyReq, st *wire.Stream) (any, error) {
		_ = st.Send(ctx, headFrame(200, "text/event-stream"))
		for ctx.Err() == nil {
			_ = st.Send(ctx, []byte("data: x\n\n"))
			time.Sleep(5 * time.Millisecond)
		}
		return nil, ctx.Err()
	})
	req, _ := http.NewRequest("POST", total.url()+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer "+f.key)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); err == nil || resp.StatusCode != 200 {
		t.Fatalf("total after head: %d, read error %v", resp.StatusCode, err)
	}
	resp.Body.Close()

	// One call in flight at most: the second gets 429. / 最多一个在途调用：第二个得到 429。
	gate := make(chan struct{})
	node.setProxy(func(ctx context.Context, req *model.ProxyReq, st *wire.Stream) (any, error) {
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return answer("node-a", 200, "ok")(ctx, req, st)
	})
	one := extraAPI(t, f, &config.AgentClientAPI{MaxInflight: 1})
	first := make(chan int, 1)
	go func() {
		status, _ := f.post(t, hc, one.url(), "/v1/chat/completions", `{"model":"m"}`)
		first <- status
	}()
	waitFor(t, "first call in flight", func() bool { return one.inflight.Load() == 1 })
	if status, body := f.post(t, hc, one.url(), "/v1/chat/completions", `{"model":"m"}`); status != 429 {
		t.Fatalf("second call: %d %s", status, body)
	}
	close(gate)
	if status := <-first; status != 200 {
		t.Fatalf("first call: %d", status)
	}

	// One connection at most: a second is closed as it opens. / 最多一条连接：第二条一打开即被关闭。
	conns := extraAPI(t, f, &config.AgentClientAPI{MaxConns: 1})
	addr := strings.TrimPrefix(conns.url(), "http://")
	held, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first connection held", func() bool { return conns.limit.n.Load() == 1 })
	second, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatalf("second connection served (%d bytes)", n)
	}
	second.Close()
	held.Close()
}

// Over TLS the API speaks HTTP/2, where a response cut after its head ends its stream rather than the connection.
// 在 TLS 上 API 使用 HTTP/2，头帧之后被截断的响应结束其流而不是连接。
func TestAPITLS(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI))
	f := newFixture(t, node)
	cert, key := selfSigned(t)
	s := extraAPI(t, f, &config.AgentClientAPI{CertFile: cert, KeyFile: key})
	if !strings.HasPrefix(s.url(), "https://") {
		t.Fatalf("url %q", s.url())
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true}}
	t.Cleanup(hc.CloseIdleConnections)
	if status, body := f.post(t, hc, s.url(), "/v1/chat/completions", `{"model":"m"}`); status != 200 || body != "node:node-a" {
		t.Fatalf("tls call: %d %q", status, body)
	}
	node.setProxy(func(ctx context.Context, _ *model.ProxyReq, st *wire.Stream) (any, error) {
		_ = st.Send(ctx, headFrame(200, "text/event-stream"))
		_ = st.Send(ctx, []byte("data: 1\n\n"))
		return nil, mesh.Errorf(model.CodeInternal, "broke")
	})
	req, _ := http.NewRequest("POST", s.url()+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer "+f.key)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("proto %s", resp.Proto)
	}
	waitFor(t, "call counted as failed", func() bool { st := f.c.stats.snapshot(); return st.Total.Errors == 1 && st.Total.Inflight == 0 })

	if _, err := startAPI(f.c, &config.AgentClientAPI{Address: "127.0.0.1:0", CertFile: "/nonexistent", KeyFile: "/nonexistent"}); err == nil {
		t.Fatal("missing certificate accepted")
	}
	if _, err := startAPI(f.c, &config.AgentClientAPI{Address: "not an address"}); err == nil {
		t.Fatal("bad address accepted")
	}
	if (&apiServer{}).url() != "" {
		t.Fatal("url without an address")
	}
}

// An agent that stops reading holds the upstream back; reading again lets it go on, and once the agent leaves, the
// upstream stream is canceled.
// 停止读取的 agent 让上游停下；重新读取后上游继续；agent 离开后上游的流被取消。
func TestAPISlowAgent(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI))
	f := newFixture(t, node)
	piece := []byte(strings.Repeat("x", 32<<10))
	for _, readAgain := range []bool{true, false} {
		var sent atomic.Int64
		canceled := make(chan struct{})
		node.setProxy(func(ctx context.Context, _ *model.ProxyReq, st *wire.Stream) (any, error) {
			defer close(canceled)
			if err := st.Send(ctx, headFrame(200, "text/plain")); err != nil {
				return nil, err
			}
			for {
				if err := st.Send(ctx, piece); err != nil {
					return nil, err
				}
				sent.Add(int64(len(piece)))
			}
		})
		conn, err := net.Dial("tcp", strings.TrimPrefix(f.base, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		body := `{"model":"m"}`
		fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", f.key, len(body), body)
		// Wait until the upstream stalls, held back by the agent that reads nothing.
		// 等待上游停下：被什么都不读的 agent 拖住。
		var last int64 = -1
		waitFor(t, "upstream stalled", func() bool {
			n := sent.Load()
			stalled := n > 0 && n == last
			last = n
			return stalled
		})
		if readAgain {
			stalledAt := sent.Load()
			buf := make([]byte, 64<<10)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			for sent.Load() == stalledAt {
				if _, err := conn.Read(buf); err != nil {
					t.Fatal(err)
				}
			}
		}
		conn.Close()
		select {
		case <-canceled:
		case <-time.After(5 * time.Second):
			t.Fatal("upstream not canceled after the agent left")
		}
	}
}

// A body larger than inlineMax streams after the request, as credit allows.
// 大于 inlineMax 的请求体在请求之后按额度流式发送。
func TestAPILargeBody(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI))
	f := newFixture(t, node)
	node.setProxy(func(ctx context.Context, req *model.ProxyReq, st *wire.Stream) (any, error) {
		time.Sleep(100 * time.Millisecond) // the sender runs out of credit / 发送方用尽额度
		n := 0
		for {
			p, err := st.Recv(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			n += len(p)
		}
		if !req.BodyStream || int64(n) != req.BodySize {
			return nil, mesh.BadRequest("body %v: got %d of %d", req.BodyStream, n, req.BodySize)
		}
		return answer("node-a", 200, fmt.Sprint(n))(ctx, req, st)
	})
	body := `{"model":"m","pad":"` + strings.Repeat("p", 3*inlineMax) + `"}`
	if status, got := f.post(t, http.DefaultClient, f.base, "/v1/chat/completions", body); status != 200 || got != fmt.Sprint(len(body)) {
		t.Fatalf("large body: %d %q", status, got)
	}
}

// The client's methods, pushes and config changes beyond the happy path.
// client 的方法、推送与配置变更中正常路径之外的情况。
func TestClientRoleEdges(t *testing.T) {
	node := newNode(t, "node-a", ready("m", model.APIOpenAI))
	hub := newHub(t, "hub-a", model.HubModel{Name: "h", API: model.APIOpenAI, Nodes: []string{"x"}, Ready: 1})
	f := newFixture(t, node, hub)
	ctx := context.Background()
	local := model.Identity{Role: model.RoleLocal}

	// Malformed pushes are dropped; the next good one still applies.
	// 畸形推送被丢弃；之后正常的推送照常生效。
	if err := node.m.Push(clientID, model.MethodNodeUpdate, "bad"); err != nil {
		t.Fatal(err)
	}
	if err := hub.m.Push(clientID, model.MethodHubModelsUpdate, "bad"); err != nil {
		t.Fatal(err)
	}
	if err := hub.m.Push(clientID, model.MethodHubModelsUpdate, model.HubModels{Hub: "hub-a", Seq: 9}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "hub update", func() bool { _, known, _ := f.c.cat.routes("h", model.APIOpenAI); return !known })

	out, err := f.r.Invoke(ctx, local, MethodClientStats, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.(StatsResp); !ok {
		t.Fatalf("stats = %T", out)
	}
	if _, err := f.r.Invoke(ctx, local, MethodClientProfileSet, json.RawMessage(`{"name":"  "}`)); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := f.r.Invoke(ctx, local, MethodClientProfileSet, json.RawMessage(`{"name":"renamed"}`)); err != nil {
		t.Fatal(err)
	}
	// No more than MaxAgentKeys keys. / 密钥不超过 MaxAgentKeys 个。
	if err := f.c.editKeys(ctx, func([]config.AgentClientKey) ([]config.AgentClientKey, error) {
		return make([]config.AgentClientKey, config.MaxAgentKeys+1), nil
	}); err == nil {
		t.Fatal("too many keys accepted")
	}
	if err := f.c.editKeys(ctx, func(keys []config.AgentClientKey) ([]config.AgentClientKey, error) {
		for len(keys) < config.MaxAgentKeys {
			k, _, err := newKey(fmt.Sprint("k", len(keys)))
			if err != nil {
				return nil, err
			}
			keys = append(keys, k)
		}
		return keys, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Invoke(ctx, local, MethodClientKeysCreate, json.RawMessage(`{"name":"one too many"}`)); err == nil {
		t.Fatal("key past the limit created")
	}
	if _, err := f.r.Invoke(ctx, local, MethodClientKeysDelete, json.RawMessage(`{"id":"nope"}`)); err == nil {
		t.Fatal("unknown key deleted")
	}
	if _, err := f.r.Invoke(ctx, local, MethodClientKeysCreate, json.RawMessage(`{"name":"`+strings.Repeat("n", 65)+`"}`)); err == nil {
		t.Fatal("long name accepted")
	}

	// Fetching what a peer offers fails when it is not there. / 对端不在时获取其提供的内容失败。
	f.c.peerUp(model.PeerConn{Identity: model.Identity{Role: model.RoleHub, ID: "ghost-hub"}})
	f.c.peerUp(model.PeerConn{Identity: model.Identity{Role: model.RoleNode, ID: "ghost-node"}})

	if f.c.me()["api_url"] != f.base {
		t.Fatalf("me = %v", f.c.me())
	}
	if err := f.c.Apply("not a member"); err == nil {
		t.Fatal("wrong member applied")
	}
	// An API that cannot start leaves none. / 无法启动的 API 不留下任何 API。
	if err := f.c.Apply(&config.AgentClient{ID: clientID, API: &config.AgentClientAPI{Address: "127.0.0.1:0", CertFile: "/nonexistent", KeyFile: "/nonexistent"}}); err == nil {
		t.Fatal("API with a missing certificate started")
	}
	if f.c.API() != "" || f.c.me() != nil {
		t.Fatalf("API left after a failed start: %q", f.c.API())
	}
}

// The catalog keeps the newest of a peer's messages and orders models and routes.
// 目录保留对端最新的消息，并为模型与路由排序。
func TestCatalogOrder(t *testing.T) {
	c := newCatalog()
	p := model.Identity{ID: "n1", Name: "n1", StartedAt: 1}
	if !c.setNode(p, model.NodeInfo{Seq: 2, Models: []model.ModelView{ready("m", model.APIOpenAI)}}) || c.setNode(p, model.NodeInfo{Seq: 1}) {
		t.Fatal("older node info replaced a newer one")
	}
	if !c.setHub(p, model.HubModels{Seq: 2}) || c.setHub(p, model.HubModels{Seq: 1}) {
		t.Fatal("older hub models replaced newer ones")
	}
	c.setNode(model.Identity{ID: "n0", Name: "n0"}, model.NodeInfo{Models: []model.ModelView{ready("m", model.APIOpenAI), ready("m", model.APIAnthropic)}})
	m3 := ready("m", model.APIOpenAI)
	m3.Inflight = 3
	c.setNode(model.Identity{ID: "n2", Name: "n2"}, model.NodeInfo{Models: []model.ModelView{m3}})
	list := c.list()
	if len(list) != 2 || list[0].API != model.APIAnthropic || list[1].API != model.APIOpenAI {
		t.Fatalf("list = %+v", list)
	}
	routes, _, _ := c.routes("m", model.APIOpenAI)
	if len(routes) != 3 || routes[0].ID != "n0" || routes[1].ID != "n1" || routes[2].ID != "n2" {
		t.Fatalf("routes = %+v", routes)
	}
}

// Setting the keys again keeps the use of those kept.
// 再次设置密钥时，保留下来的密钥的使用记录不变。
func TestKeyStoreKeepsUsage(t *testing.T) {
	k, secret, err := newKey("a")
	if err != nil {
		t.Fatal(err)
	}
	s := &keyStore{}
	s.set([]config.AgentClientKey{k})
	if !s.auth(http.Header{"X-Api-Key": {secret}}) {
		t.Fatal("key refused")
	}
	s.set([]config.AgentClientKey{k})
	if l := s.list(); len(l) != 1 || l[0].Calls != 1 {
		t.Fatalf("usage lost: %+v", l)
	}
}

// Past maxStatModels names, calls are counted under "other"; the snapshot is sorted.
// 超过 maxStatModels 个名称后，调用记在 "other" 名下；快照有序。
func TestStatsOverflow(t *testing.T) {
	s := newStats()
	for i := range maxStatModels + 2 {
		s.begin(fmt.Sprintf("m%04d", i))(1, i%2 == 0)
	}
	snap := s.snapshot()
	if len(snap.Models) != maxStatModels+1 || snap.Models[len(snap.Models)-1].Model != "other" || snap.Total.Calls != maxStatModels+2 {
		t.Fatalf("models %d, last %q, calls %d", len(snap.Models), snap.Models[len(snap.Models)-1].Model, snap.Total.Calls)
	}
}

type plainHandler struct{ opened, closed atomic.Int32 }

func (h *plainHandler) OnOpen(*fib.Connection)                 { h.opened.Add(1) }
func (h *plainHandler) OnData(*fib.Connection, []byte)         {}
func (h *plainHandler) OnPriorityData(*fib.Connection, []byte) {}
func (h *plainHandler) OnClose(*fib.Connection, error)         { h.closed.Add(1) }

type fullHandler struct {
	plainHandler
	shutdown, engineClose atomic.Int32
}

func (h *fullHandler) OnShutdown(*fib.Connection)    { h.shutdown.Add(1) }
func (h *fullHandler) OnEngineClose(*fib.Connection) { h.engineClose.Add(1) }

// connLimit passes the optional handler methods on, and once closed, closes what opens.
// connLimit 转发可选的处理器方法；关闭之后，关闭新打开的连接。
func TestConnLimitHandler(t *testing.T) {
	newConnLimit(&plainHandler{}, 1).OnShutdown(nil)
	newConnLimit(&plainHandler{}, 1).OnEngineClose(nil)
	h := &fullHandler{}
	l := newConnLimit(h, 1)
	l.OnShutdown(nil)
	l.OnEngineClose(nil)
	if h.shutdown.Load() != 1 || h.engineClose.Load() != 1 {
		t.Fatalf("shutdown %d, engine close %d", h.shutdown.Load(), h.engineClose.Load())
	}

	l.closeAll()
	e := testEngine()
	i, err := e.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: l})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.RemoveBinding(i) }()
	addrs, err := e.BindingAddrs(i)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", addrs[0].String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || h.opened.Load() != 0 {
		t.Fatalf("connection served after closeAll: %v, opened %d", err, h.opened.Load())
	}
}

// The registered role finds its member in a config only when there is one.
// 注册的角色只在配置中存在其成员时才找到它。
func TestRegisteredMember(t *testing.T) {
	for _, info := range app.Roles() {
		if info.ID != "agent.client" {
			continue
		}
		if info.Member(&config.Config{}) != nil || info.Member(&config.Config{Agent: &config.Agent{}}) != nil {
			t.Fatal("member without a client")
		}
		cl := &config.AgentClient{}
		if info.Member(&config.Config{Agent: &config.Agent{Client: cl}}) != cl {
			t.Fatal("client member not found")
		}
		return
	}
	t.Fatal("agent.client not registered")
}
