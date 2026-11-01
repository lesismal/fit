package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// backoff spreads waits over the whole step (full jitter), doubling the step from backoffBase up to the ceiling;
// a refused hello waits refusedRetry plus up to half of it.
// backoff 把等待分散到整个步长内（全抖动），步长从 backoffBase 翻倍增长到上限；hello 被拒绝时等待 refusedRetry 再加其一半以内。
func TestBackoff(t *testing.T) {
	const ceiling = 5 * time.Second
	for n, step := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second} {
		if n == 4 {
			n = 40 // no overflow far out / 很大的 n 不会溢出
		}
		const samples = 4000
		var sum, lo, hi time.Duration = 0, step, 0
		for i := 0; i < samples; i++ {
			d := backoff(n, ceiling)
			if d < backoffFloor || d > step {
				t.Fatalf("backoff(%d) = %v, outside [%v, %v]", n, d, backoffFloor, step)
			}
			sum += d
			lo, hi = min(lo, d), max(hi, d)
		}
		// Spread over the step, not bunched: mean near the middle, extremes near the ends.
		// 分散在整个步长内而不是扎堆：均值接近中点，极值接近两端。
		mean := sum / samples
		if mean < step*45/100 || mean > step*55/100 || lo > backoffFloor+step/20 || hi < step*95/100 {
			t.Fatalf("backoff(%d): mean %v, min %v, max %v for step %v", n, mean, lo, hi, step)
		}
	}
	if d := backoff(0, 300*time.Millisecond); d > 300*time.Millisecond {
		t.Fatalf("ceiling below the base: %v", d)
	}
	for i := 0; i < 1000; i++ {
		if d := refusedBackoff(5 * time.Second); d < refusedRetry || d > refusedRetry*3/2 {
			t.Fatalf("refusedBackoff = %v", d)
		}
		if d := refusedBackoff(time.Minute); d < time.Minute || d > 90*time.Second {
			t.Fatalf("refusedBackoff(1m) = %v", d)
		}
	}
}

// A node dials two hubs A and B. A shuts down while it has a stream open to the node: the node stops using its
// connection to A at once (calls to A fail fast, calls to B work), the stream still completes, A closes when it is
// done, and the node redials A, connecting again once A is back.
// 节点连接两个 hub A 与 B。A 在有一条到节点的流时下线：节点立即停止使用到 A 的连接（对 A 的调用立即失败，对 B 的调用
// 正常），该流仍然完成，A 在流结束后关闭，节点重新拨号 A，并在 A 恢复后再次连上。
func TestMeshDrain(t *testing.T) {
	addrA, addrB, addrN := freeAddr(t), freeAddr(t), freeAddr(t)
	n, nr := newTestMesh(t, model.RoleNode, "n1", allPeers)
	release := make(chan struct{})
	HandleStream(nr, "/gen", []string{model.RoleHub}, func(ctx context.Context, _ model.Identity, _ *struct{}, st *wire.Stream) (any, error) {
		if err := st.Send(ctx, []byte(`"a"`)); err != nil {
			return nil, err
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return "done", st.Send(ctx, []byte(`"b"`))
	})
	a, _ := newTestMesh(t, model.RoleHub, "ha", allPeers)
	b, _ := newTestMesh(t, model.RoleHub, "hb", allPeers)
	start(t, a, addrA)
	start(t, b, addrB)
	start(t, n, addrN, toHub(addrA), toHub(addrB))
	waitFor(t, "hello", func() bool { return n.Online("ha") && n.Online("hb") && a.Online("n1") })

	ctx := context.Background()
	st, err := a.OpenStream(ctx, "n1", "/gen", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := st.Recv(ctx); err != nil || string(p) != `"a"` {
		t.Fatalf("first chunk %q %v", p, err)
	}

	shutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stopped := make(chan time.Duration, 1)
	go func() {
		begin := time.Now()
		a.Shutdown(shutCtx)
		stopped <- time.Since(begin)
	}()
	waitFor(t, "drain seen", func() bool { return !n.Online("ha") })

	// New calls skip the draining connection: to A they fail fast, to B they work; A itself starts nothing new.
	// 新调用跳过下线中的连接：发往 A 的立即失败，发往 B 的正常；A 自己也不再发起新调用。
	t0 := time.Now()
	var we *wire.Error
	if _, err := Call[map[string]string](ctx, n, "ha", "/echo", map[string]string{"msg": "x"}); !errors.As(err, &we) ||
		we.Code != model.CodeUnavailable || time.Since(t0) > time.Second {
		t.Fatalf("call to the draining peer: %v after %v", err, time.Since(t0))
	}
	if got, err := Call[map[string]string](ctx, n, "hb", "/echo", map[string]string{"msg": "x"}); err != nil || got["from"] != "n1" {
		t.Fatalf("call to B: %v %v", got, err)
	}
	if _, err := a.OpenStream(ctx, "n1", "/gen", struct{}{}); !errors.Is(err, ErrOffline) {
		t.Fatalf("new stream from the draining side: %v", err)
	}
	if ps := n.Peers(); !hasDraining(ps, "ha") {
		t.Fatalf("peers do not show the drain: %+v", ps)
	}
	select {
	case d := <-stopped:
		t.Fatalf("A stopped after %v with a stream in flight", d)
	case <-time.After(300 * time.Millisecond):
	}

	// The stream in flight completes; then A closes. / 在途的流正常完成；之后 A 关闭。
	close(release)
	if p, err := st.Recv(ctx); err != nil || string(p) != `"b"` {
		t.Fatalf("second chunk %q %v", p, err)
	}
	if _, err := st.Recv(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("stream end: %v", err)
	}
	var res string
	if err := json.Unmarshal(st.Result(), &res); err != nil || res != "done" {
		t.Fatalf("result %q %v", st.Result(), err)
	}
	select {
	case d := <-stopped:
		if d > 5*time.Second {
			t.Fatalf("shutdown took %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("A did not stop after its stream completed")
	}
	waitFor(t, "A gone", func() bool {
		for _, p := range n.Peers() {
			if p.ID == "ha" {
				return false
			}
		}
		return true
	})

	// A comes back on the same address: the node's dial loop reconnects. / A 在同一地址恢复：节点的拨号循环重新连上。
	a2, _ := newTestMesh(t, model.RoleHub, "ha", allPeers)
	start(t, a2, addrA)
	waitFor(t, "redial", func() bool { return n.Online("ha") })
}

func hasDraining(ps []model.PeerConn, id string) bool {
	for _, p := range ps {
		if p.ID == id && p.Draining {
			return true
		}
	}
	return false
}

// Within one peer's pool, a draining connection is skipped while the other one keeps serving calls.
// 在同一对端的连接池中，下线中的连接被跳过，另一条连接继续承载调用。
func TestMeshPoolSkipsDraining(t *testing.T) {
	addrN, addrH := freeAddr(t), freeAddr(t)
	n, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	h, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, n, addrN, toHub(addrH))
	start(t, h, addrH, toNode(addrN))
	waitFor(t, "two connections", func() bool {
		h.mu.RLock()
		defer h.mu.RUnlock()
		return len(h.byID["n1"]) == 2
	})
	h.mu.RLock()
	drained := h.byID["n1"][0].wc
	h.mu.RUnlock()
	_ = drained.Drain()
	for i := 0; i < 20; i++ {
		if wc := h.pick("n1"); wc == nil || wc == drained {
			t.Fatalf("pick %d returned the draining connection (%v)", i, wc)
		}
		if _, err := Call[map[string]string](context.Background(), h, "n1", "/echo", map[string]string{"msg": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if !h.Online("n1") {
		t.Fatal("peer offline with one usable connection")
	}
}

// The listener holds at most MaxConns connections; a dialer beyond that is refused and keeps retrying.
// 监听方最多持有 MaxConns 条连接；超出的拨号方被拒绝并持续重试。
func TestMeshMaxConns(t *testing.T) {
	addr := freeAddr(t)
	n := New(Options{Engine: testEngine(), Self: func() model.Identity { return model.Identity{Role: model.RoleNode, ID: "n1"} },
		AcceptRoles: allPeers, MaxConns: 1, RetryInterval: 300 * time.Millisecond}, NewRouter())
	t.Cleanup(n.Stop)
	start(t, n, addr)
	c1, _ := newTestMesh(t, model.RoleProxy, "c1", allPeers)
	start(t, c1, "", toNode(addr))
	waitFor(t, "first", func() bool { return n.Online("c1") })
	c2, _ := newTestMesh(t, model.RoleProxy, "c2", allPeers)
	start(t, c2, "", toNode(addr))
	waitFor(t, "refusal", func() bool {
		ts := c2.Targets()
		return len(ts) == 1 && strings.Contains(ts[0].Error, "too many connections")
	})
	c1.Stop()
	waitFor(t, "second gets in", func() bool { return n.Online("c2") })
}
