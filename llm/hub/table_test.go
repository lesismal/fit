package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/role"
	"github.com/lesismal/fit/llm/internal/wire"
)

var local = model.Identity{Role: model.RoleLocal, ID: "console"}

func TestTableOfflineNodes(t *testing.T) {
	h := newHub(newMesh(t, model.RoleHub, "h1", 1, nil, mesh.NewRouter()), func() model.Identity { return model.Identity{ID: "h1"} }, nil)
	defer h.close()
	h.apply(model.Identity{ID: "b", Name: "same", StartedAt: 1}, model.NodeInfo{Seq: 1})
	h.apply(model.Identity{ID: "a", Name: "same", StartedAt: 1}, model.NodeInfo{Seq: 1})
	h.touch(model.Identity{ID: "c", Name: "cee"})
	h.touch(model.Identity{ID: "c", Name: "other"}) // known already: left alone
	ns := h.Nodes()
	if len(ns) != 3 || ns[0].Info.ID != "c" || ns[1].Info.ID != "a" || ns[2].Info.ID != "b" || ns[1].Info.Name != "same" {
		t.Fatalf("nodes = %+v", ns)
	}
	// Over the bound, the offline nodes seen longest ago go first.
	h.mu.Lock()
	h.nodes["a"].lastSeen, h.nodes["b"].lastSeen, h.nodes["c"].lastSeen = 3, 1, 2
	h.mu.Unlock()
	h.setMaxNodes(1)
	if ns := h.Nodes(); len(ns) != 1 || ns[0].Info.ID != "a" {
		t.Fatalf("pruned to %+v", ns)
	}
	if got := h.nodeName("a"); got != "same (a)" {
		t.Fatalf("nodeName = %q", got)
	}
	if got := h.nodeName("zz"); got != "zz" {
		t.Fatalf("nodeName of an unknown node = %q", got)
	}
	// After close, changes schedule no flush.
	h.close()
	h.mu.Lock()
	h.timer = nil // the one the applies above scheduled, stopped
	h.mu.Unlock()
	h.changed()
	h.mu.Lock()
	timer := h.timer
	h.mu.Unlock()
	if timer != nil {
		t.Fatal("a flush was scheduled after close")
	}
}

func TestManageErr(t *testing.T) {
	h := newHub(newMesh(t, model.RoleHub, "h1", 1, nil, mesh.NewRouter()), nil, nil)
	defer h.close()
	var we *wire.Error
	if err := h.manageErr("n", mesh.Errorf(model.CodeNotFound, "gone")); !errors.As(err, &we) || we.Code != model.CodeNotFound {
		t.Fatalf("wire error = %v", err)
	}
	if err := h.manageErr("n", io.ErrUnexpectedEOF); !errors.Is(err, io.ErrUnexpectedEOF) || !strings.HasPrefix(err.Error(), "node n:") {
		t.Fatalf("other error = %v", err)
	}
}

func TestOnlineNodesNotPruned(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.hub.setMaxNodes(1)
	if ns := e.hub.Nodes(); len(ns) != 2 {
		t.Fatalf("online nodes pruned: %+v", ns)
	}
	// MethodHubNodes for the console.
	res, err := e.hr.Invoke(context.Background(), local, model.MethodHubNodes, nil)
	if ns, ok := res.([]model.HubNode); err != nil || !ok || len(ns) != 2 {
		t.Fatalf("hub nodes = %v %v", res, err)
	}
}

func TestAggregateByAPI(t *testing.T) {
	anth := ready("m1", 0)
	anth.API = model.APIAnthropic
	e := newEnv(t, []model.ModelView{ready("m1", 0), anth, {Name: "m0", API: model.APIOpenAI, State: model.ModelDisabled}}, nil)
	waitFor(t, "models", func() bool { return len(e.last().Models) == 2 })
	ms := e.last().Models
	if ms[0].API != model.APIAnthropic || ms[1].API != model.APIOpenAI || ms[0].Name != "m1" {
		t.Fatalf("models = %+v", ms)
	}
	// A node update that does not decode is dropped; the next one applies.
	if err := e.n1.m.Push("h1", model.MethodNodeUpdate, "not an object"); err != nil {
		t.Fatal(err)
	}
	if err := e.n1.m.Push("h1", model.MethodNodeUpdate, e.n1.setInfo(func(i *model.NodeInfo) { i.Models = i.Models[:1] })); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "update", func() bool { return len(e.last().Models) == 1 })
}

func TestNodeInfoFails(t *testing.T) {
	e := newEnv(t, nil, nil)
	addr := e.hub.m.ListenAddr()
	// A node without MethodNodeInfo: the hub still lists it.
	m := newMesh(t, model.RoleNode, "n3", 1, []string{model.RoleHub}, mesh.NewRouter())
	if err := m.Configure(model.ListenConfig{}, []model.DialReq{{Addr: addr, Password: "pw", Role: model.RoleHub}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "n3 listed", func() bool {
		for _, n := range e.hub.Nodes() {
			if n.Info.ID == "n3" && n.Info.Name == "n3" && n.Online {
				return true
			}
		}
		return false
	})
}

func TestProxyBadRequestAndNoHead(t *testing.T) {
	e := newEnv(t, []model.ModelView{ready("m1", 0)}, nil)
	var we *wire.Error
	_, _, _, err := openProxy(t, context.Background(), e, model.ProxyReq{API: model.APIOpenAI})
	if !errors.As(err, &we) || we.Code != model.CodeBadRequest {
		t.Fatalf("no model: %v", err)
	}
	e.n1.setProxy(func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
		return model.ProxyResult{Node: "n1"}, nil
	})
	_, _, _, err = openProxy(t, context.Background(), e, model.ProxyReq{API: model.APIOpenAI, Model: "m1"})
	if !errors.As(err, &we) || we.Code != model.CodeInternal || !strings.Contains(we.Msg, "without a head") {
		t.Fatalf("no head: %v", err)
	}
}

// echo answers with a head, then sends back the body the proxy streams.
func echo(node string) func(ctx context.Context, _ *model.ProxyReq, st *wire.Stream) (any, error) {
	return func(ctx context.Context, _ *model.ProxyReq, st *wire.Stream) (any, error) {
		head, _ := json.Marshal(model.ProxyHead{Status: 200, Node: node})
		if err := st.Send(ctx, head); err != nil {
			return nil, err
		}
		for {
			b, err := st.Recv(ctx)
			if errors.Is(err, io.EOF) {
				return model.ProxyResult{Node: node, Status: 200}, nil
			}
			if err != nil {
				return nil, err
			}
			if err := st.Send(ctx, b); err != nil {
				return nil, err
			}
		}
	}
}

func TestProxyBodyStreamRetry(t *testing.T) {
	e := newEnv(t, []model.ModelView{ready("m1", 0)}, []model.ModelView{ready("m1", 3)})
	e.n1.setProxy(func(context.Context, *model.ProxyReq, *wire.Stream) (any, error) {
		return nil, mesh.Errorf(model.CodeUnavailable, "busy")
	})
	e.n2.setProxy(echo("n2"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := model.ProxyReq{API: model.APIOpenAI, Model: "m1", BodyStream: true}

	// Nothing of the body was sent when n1 refused: n2 gets the call, and
	// the body.
	st, err := e.client.OpenStream(ctx, "h1", model.MethodHubProxy, req)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.Recv(ctx)
	var head model.ProxyHead
	if err != nil || json.Unmarshal(first, &head) != nil || head.Node != "n2" {
		t.Fatalf("head = %s %v", first, err)
	}
	if err := st.Send(ctx, []byte("body")); err != nil {
		t.Fatal(err)
	}
	if b, err := st.Recv(ctx); err != nil || string(b) != "body" {
		t.Fatalf("echo = %q %v", b, err)
	}
	_ = st.CloseSend()
	if _, err := st.Recv(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("end = %v", err)
	}

	// n1 took some of the body before it refused: the call cannot move.
	e.n1.setProxy(func(ctx context.Context, _ *model.ProxyReq, st *wire.Stream) (any, error) {
		if _, err := st.Recv(ctx); err != nil {
			return nil, err
		}
		return nil, mesh.Errorf(model.CodeUnavailable, "busy")
	})
	before := e.n2.calls.Load()
	st, err = e.client.OpenStream(ctx, "h1", model.MethodHubProxy, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(ctx, []byte("body")); err != nil {
		t.Fatal(err)
	}
	_, err = st.Recv(ctx)
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != model.CodeUnavailable || e.n2.calls.Load() != before {
		t.Fatalf("taken body: %v (n2 calls %d -> %d)", err, before, e.n2.calls.Load())
	}
}

func TestNodeStreamForwarding(t *testing.T) {
	e := newEnv(t, nil, nil)
	ctx := context.Background()
	open := func(id, method string, req json.RawMessage) (*wire.Stream, error) {
		raw, _ := json.Marshal(model.HubNodeStreamReq{ID: id, Method: method, Req: req})
		return e.hr.OpenLocal(ctx, local, model.MethodHubNodeStream, raw)
	}
	recvAll := func(st *wire.Stream) (frames int, err error) {
		for {
			_, err := st.Recv(ctx)
			if err != nil {
				return frames, err
			}
			frames++
		}
	}
	for _, tc := range []struct {
		id, method string
		want       string
	}{
		{"n1", model.MethodNodeModelList, "not a node management stream method"},
		{"nx", model.MethodNodeJobFollow, "offline"},
		{"n1", model.MethodNodeJobFollow, "no job 0"}, // no request: {} is sent, and the node fails it
	} {
		st, err := open(tc.id, tc.method, nil)
		if err == nil {
			_, err = recvAll(st)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s %s: %v, want %q", tc.id, tc.method, err, tc.want)
		}
	}
}

// hubRole runs a hub through an app.Manager whose config edits edit makes.
func hubRole(t *testing.T, edit app.Editor) *Role {
	t.Helper()
	mgr := app.NewManager(testEngine(), nil, nil, edit)
	if err := mgr.Apply(&config.Config{LLM: &config.LLM{Hub: &config.LLMHub{}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		mgr.Stop(ctx)
	})
	r, _ := role.Find(testEngine(), model.RoleHub).(*Role)
	if r == nil {
		t.Fatal("no hub running")
	}
	return r
}

func TestRole(t *testing.T) {
	var fail error
	r := hubRole(t, func(_ string, _ []string, edit func([]byte) ([]byte, error)) ([]string, error) {
		if fail != nil {
			return nil, fail
		}
		_, err := edit([]byte(`{}`))
		return nil, err
	})
	for _, i := range app.Roles() {
		if i.ID == "llm.hub" && i.Member(&config.Config{}) != nil {
			t.Fatal("a config without llm.hub has a member")
		}
	}
	if r.Hub() == nil {
		t.Fatal("no Hub")
	}
	if err := r.Apply((*config.LLMHub)(nil)); err == nil {
		t.Fatal("Apply of no member did not fail")
	}
	if err := r.Apply(&config.LLMHub{ID: "hub-1", Name: "h"}); err != nil {
		t.Fatal(err)
	}
	set := func(name string) (any, error) {
		raw, _ := json.Marshal(model.ProfileReq{Name: name})
		return r.Router.Invoke(context.Background(), local, model.MethodHubProfileSet, raw)
	}
	if res, err := set(" new "); err != nil || res.(model.Identity).ID != "hub-1" {
		t.Fatalf("profile set = %v %v", res, err)
	}
	if _, err := set(strings.Repeat("x", 65)); err == nil || !strings.Contains(err.Error(), "1-64") {
		t.Fatalf("long name = %v", err)
	}
	fail = fmt.Errorf("read-only")
	if _, err := set("ok"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("failed edit = %v", err)
	}
}
