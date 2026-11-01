package mesh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// A route counts its calls, failures and bodies' bytes, the bytes of a
// call still running as far as it got, from the side that took the stream
// (inbound) and from the side that opened it; past MaxRoutes, calls are
// counted together under "other". The mesh's status shows every link's
// bytes and latency beside the routes.
func TestRoutesAndStatus(t *testing.T) {
	addr := freeAddr(t)
	node, nr := newTestMesh(t, model.RoleNode, "n1", allPeers)
	release := make(chan struct{})
	HandleStream(nr, "/call", []string{model.RoleHub}, func(ctx context.Context, caller model.Identity, req *struct{ Fail bool }, st *wire.Stream) (_ any, err error) {
		rc := node.Routes().Begin(caller.ID, "", "m")
		rc.AddReq(10)
		rc.Attach(st, true)
		defer func() { rc.End(err != nil) }()
		for {
			_, err := st.Recv(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
		}
		if err := st.Send(ctx, make([]byte, 700)); err != nil {
			return nil, err
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if req.Fail {
			return nil, BadRequest("no")
		}
		return struct{}{}, nil
	})
	start(t, node, addr)
	hub, hr := newTestMesh(t, model.RoleHub, "h1", allPeers)
	RegisterAdmin(hr, hub, nil)
	start(t, hub, "", toNode(addr))
	waitFor(t, "hello", func() bool { return hub.Online("n1") && node.Online("h1") })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	call := func(fail bool) (*wire.Stream, *RouteCall) {
		st, err := hub.OpenStream(ctx, "n1", "/call", map[string]bool{"fail": fail})
		if err != nil {
			t.Fatal(err)
		}
		rc := hub.Routes().Begin("p1", "n1", "m")
		rc.Attach(st, false)
		if err := st.Send(ctx, make([]byte, 300)); err != nil {
			t.Fatal(err)
		}
		if err := st.CloseSend(); err != nil {
			t.Fatal(err)
		}
		if b, err := st.Recv(ctx); err != nil || len(b) != 700 {
			t.Fatalf("answer %d %v", len(b), err)
		}
		return st, rc
	}
	st1, rc1 := call(false)
	// While it runs, the call counts as far as it got, on both sides.
	waitFor(t, "a running call", func() bool {
		rs := node.Routes().Snapshot()
		return len(rs) == 1 && rs[0].Inflight == 1 && rs[0].ReqBytes == 310 && rs[0].RespBytes == 700
	})
	if rs := hub.Routes().Snapshot(); len(rs) != 1 || rs[0] != (model.RouteStats{From: "p1", To: "n1", Model: "m", Calls: 1, Inflight: 1,
		ReqBytes: 300, RespBytes: 700, LastAt: rs[0].LastAt}) {
		t.Fatalf("hub routes %+v", rs)
	}
	release <- struct{}{}
	if _, err := st1.Recv(ctx); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	rc1.End(false)
	rc1.End(true) // only the first End counts

	st2, rc2 := call(true)
	release <- struct{}{}
	if _, err := st2.Recv(ctx); err == nil {
		t.Fatal("the failed call ended well")
	}
	rc2.End(true)
	waitFor(t, "the calls' end", func() bool {
		rs := node.Routes().Snapshot()
		return len(rs) == 1 && rs[0].Inflight == 0 && rs[0].Calls == 2
	})
	rs := node.Routes().Snapshot()
	if r := rs[0]; r.From != "h1" || r.To != "" || r.Errors != 1 || r.ReqBytes != 620 || r.RespBytes != 1400 || r.LastAt == 0 {
		t.Fatalf("node routes %+v", r)
	}
	if r := hub.Routes().Snapshot()[0]; r.Calls != 2 || r.Errors != 1 || r.ReqBytes != 600 || r.RespBytes != 1400 {
		t.Fatalf("hub routes %+v", r)
	}

	// The status method: the links' bytes, as both sides count them.
	raw, err := hub.router.Invoke(ctx, model.Identity{Role: model.RoleLocal, ID: "console"}, model.MethodStatus, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(model.MeshStatus)
	if s.Self.ID != "h1" || len(s.Links) != 1 || s.Links[0].ID != "n1" || s.Links[0].Link.BytesOut < 600 ||
		s.Links[0].Link.BytesIn < 1400 || len(s.Routes) != 1 || s.At == 0 {
		t.Fatalf("status %+v", s)
	}
	waitFor(t, "a round trip", func() bool {
		ps := hub.Peers()
		return len(ps) == 1 && ps[0].Link.RTT.Samples > 0
	})

	// A cut link ends the call running on it as failed, its bytes kept.
	st3, err := hub.OpenStream(ctx, "n1", "/call", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st3.Send(ctx, make([]byte, 50)); err != nil {
		t.Fatal(err)
	}
	_ = st3.CloseSend()
	if _, err := st3.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	hub.Kick("n1")
	waitFor(t, "the cut call's end", func() bool {
		rs := node.Routes().Snapshot()
		return rs[0].Calls == 3 && rs[0].Inflight == 0 && rs[0].Errors == 2 && rs[0].ReqBytes == 680
	})
	close(release)
}

func TestRoutesBound(t *testing.T) {
	r := newRoutes()
	for i := range MaxRoutes + 5 {
		r.Begin("a", "b", fmt.Sprint(i)).End(i%2 == 0)
	}
	rs := r.Snapshot()
	if len(rs) != MaxRoutes+1 {
		t.Fatalf("%d routes", len(rs))
	}
	var other model.RouteStats
	for _, x := range rs {
		if x.Model == model.RouteOther {
			other = x
		}
	}
	if other.Calls != 5 || other.From != model.RouteOther || other.To != model.RouteOther {
		t.Fatalf("other %+v", other)
	}
	c := r.Begin("a", "b", "0")
	c.AddReq(5)
	c.End(false)
	c.Attach(nil, true) // after its end: nothing
	if rs := r.Snapshot(); rs[0].Model != "0" || rs[0].Calls != 2 || rs[0].ReqBytes != 5 {
		t.Fatalf("%+v", rs[0])
	}
}
