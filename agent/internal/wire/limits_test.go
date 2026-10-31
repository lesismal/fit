package wire

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/agent/internal/model"
)

// Drain is announced once; the peer sees it while running calls and streams go on, and Inflight counts them.
// drain 只宣告一次；对端看到它，而进行中的调用与流照常继续，Inflight 统计它们。
func TestDrain(t *testing.T) {
	release := make(chan struct{})
	var drained atomic.Int32
	lh := Handlers{
		Request: func(ctx context.Context, r *Request) ([]byte, error) {
			<-release
			return r.Payload, nil
		},
		Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
			<-release
			return []byte("end"), st.Send(ctx, []byte("late"))
		},
	}
	d, l, _ := pair(t, Options{}, Handlers{Drain: func() { drained.Add(1) }}, lh)
	ctx := ctxT(t)

	res := make(chan error, 1)
	go func() {
		_, err := d.Call(ctx, Head{Method: "/slow"}, []byte("x"))
		res <- err
	}()
	st, err := d.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return l.Inflight() == 2 && d.Inflight() == 2 })

	if d.Draining() || l.Draining() {
		t.Fatal("draining before Drain")
	}
	_ = l.Drain()
	_ = l.Drain()
	waitUntil(t, func() bool { return d.PeerDraining() })
	if !d.Draining() || !l.Draining() || l.PeerDraining() || drained.Load() != 1 {
		t.Fatalf("drain flags: d %v/%v l %v/%v, callbacks %d", d.Draining(), d.PeerDraining(), l.Draining(), l.PeerDraining(), drained.Load())
	}

	// In-flight work finishes normally after the drain. / drain 之后进行中的工作正常结束。
	close(release)
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	if p, err := st.Recv(ctx); err != nil || string(p) != "late" {
		t.Fatalf("stream data %q %v", p, err)
	}
	if _, err := st.Recv(ctx); !errors.Is(err, io.EOF) || string(st.Result()) != "end" {
		t.Fatalf("stream end %v %q", err, st.Result())
	}
	waitUntil(t, func() bool { return l.Inflight() == 0 && d.Inflight() == 0 })
}

// Beyond MaxInbound, requests and streams fail at once with CodeRateLimited; capacity comes back as they finish.
// 超过 MaxInbound 的请求与流立即以 CodeRateLimited 失败；随着它们结束，容量恢复。
func TestMaxInbound(t *testing.T) {
	release := make(chan struct{})
	lh := Handlers{
		Request: func(ctx context.Context, r *Request) ([]byte, error) {
			if string(r.Payload) == "block" {
				<-release
			}
			return r.Payload, nil
		},
		Stream: func(ctx context.Context, st *Stream) ([]byte, error) { return []byte("ok"), nil },
	}
	d, l, _ := pair(t, Options{MaxInbound: 2}, Handlers{}, lh)
	ctx := ctxT(t)
	for i := 0; i < 2; i++ {
		go func() { _, _ = d.Call(ctx, Head{Method: "/b"}, []byte("block")) }()
	}
	waitUntil(t, func() bool { return l.Inflight() == 2 })

	start := time.Now()
	_, err := d.Call(ctx, Head{Method: "/x"}, []byte("x"))
	var we *Error
	if !errors.As(err, &we) || we.Code != model.CodeRateLimited || time.Since(start) > time.Second {
		t.Fatalf("third request: %v after %v", err, time.Since(start))
	}
	st, err := d.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Recv(ctx); !errors.As(err, &we) || we.Code != model.CodeRateLimited {
		t.Fatalf("stream over the limit: %v", err)
	}

	close(release)
	waitUntil(t, func() bool { return l.Inflight() == 0 })
	if p, err := d.Call(ctx, Head{Method: "/x"}, []byte("again")); err != nil || string(p) != "again" {
		t.Fatalf("after release: %q %v", p, err)
	}
}

// A stream kept alive outlives StreamIdle without sending data; Recv never sees the keepalive frames.
// 保活的流在不发送数据的情况下活过 StreamIdle；Recv 不会看到保活帧。
func TestStreamKeepAlive(t *testing.T) {
	const idle = 150 * time.Millisecond
	lh := Handlers{Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
		stop := st.KeepAlive(0)
		time.Sleep(4 * idle) // "reading the prompt" / “读取提示词”
		stop()
		return []byte("end"), st.Send(ctx, []byte("first"))
	}}
	d, _, _ := pair(t, Options{StreamIdle: idle}, Handlers{}, lh)
	ctx := ctxT(t)
	st, err := d.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := st.Recv(ctx); err != nil || string(p) != "first" {
		t.Fatalf("first frame %q %v", p, err)
	}
	if _, err := st.Recv(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("end: %v", err)
	}

	// Once stopped, the idle timeout applies again. / 停止后空闲超时重新生效。
	lh2 := Handlers{Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
		st.KeepAlive(0)()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	d2, _, _ := pair(t, Options{StreamIdle: idle}, Handlers{}, lh2)
	st, err = d2.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Both ends time the stream out after StreamIdle: whichever fires first, the dialer sees its own ErrStreamIdle or
	// the responder's, carried as an error frame.
	// 两端都在 StreamIdle 后让流超时：先触发的一方决定拨号方看到的是自己的 ErrStreamIdle，还是经错误帧带来的响应方的。
	_, err = st.Recv(ctx)
	var we *Error
	if !errors.Is(err, ErrStreamIdle) && !(errors.As(err, &we) && we.Msg == ErrStreamIdle.Error()) {
		t.Fatalf("want ErrStreamIdle, got %v", err)
	}
}

// Ping carries a sequence number; Unanswered runs until the matching pong.
// ping 携带序号；Unanswered 一直增长直到收到对应的 pong。
func TestPingUnanswered(t *testing.T) {
	d, _, _ := pair(t, Options{}, Handlers{}, Handlers{})
	if d.Unanswered() != 0 {
		t.Fatal("unanswered before any ping")
	}
	if err := d.Ping(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return d.Unanswered() == 0 })
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

// The listener finds the fib connection under each WebSocket connection, so forceClose drops the TCP connection
// itself; so does the dialing side, through the dialer's Dial hook.
// 监听方能找到每条 WebSocket 连接下的 fib 连接，因此 forceClose 直接断开 TCP 连接（拨号方没有这样的句柄）。
func TestForceCloseListener(t *testing.T) {
	opened := make(chan *Conn, 1)
	addr := Listen(t, NewHandler(ConnConfig{OnOpen: func(c *Conn) { opened <- c }}))

	d, err := Dial(ctxT(t), "ws://"+addr+"/ws", DialConfig{Engine: SharedEngine(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c := <-opened
	if c.abort == nil {
		t.Fatal("listener connection has no handle on its TCP connection")
	}
	if d.abort == nil {
		t.Fatal("dialer connection has no handle on its TCP connection")
	}
	c.forceClose(closeSlow, "too slow", ErrSlow)
	select {
	case <-d.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("dialer did not see the connection drop")
	}
}
