package wire_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/agent/internal/wire"

	"github.com/lesismal/fib/websocket"
)

// echoHandlers answer requests with their payload and echo stream frames back.
// echoHandlers 用载荷应答请求，并把流中的帧原样发回。
func echoHandlers(*wire.Conn) wire.Handlers {
	return wire.Handlers{
		Request: func(_ context.Context, r *wire.Request) ([]byte, error) { return r.Payload, nil },
		Stream: func(ctx context.Context, st *wire.Stream) ([]byte, error) {
			for {
				p, err := st.Recv(ctx)
				if err == io.EOF {
					return []byte("done"), nil
				}
				if err != nil {
					return nil, err
				}
				if err := st.Send(ctx, p); err != nil {
					return nil, err
				}
			}
		},
	}
}

type testServer struct {
	addr   string
	opened chan *wire.Conn
	closed chan error
}

// startServer serves HTTP and /ws on one port, like a process's web console will.
// startServer 在同一端口上提供 HTTP 与 /ws，与进程的 Web 控制台将来的用法一致。
func startServer(t *testing.T, cfg wire.ConnConfig) *testServer {
	t.Helper()
	ts := &testServer{opened: make(chan *wire.Conn, 10), closed: make(chan error, 10)}
	cfg.OnOpen = func(c *wire.Conn) { ts.opened <- c }
	cfg.OnClose = func(_ *wire.Conn, err error) { ts.closed <- err }
	if cfg.Handlers == nil {
		cfg.Handlers = echoHandlers
	}
	ts.addr = wire.Listen(t, wire.NewHandler(cfg))
	return ts
}

func dial(t *testing.T, url string, cfg wire.DialConfig) (*wire.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if cfg.Handlers == nil {
		cfg.Handlers = echoHandlers
	}
	cfg.Engine = wire.SharedEngine(t)
	c, err := wire.Dial(ctx, url, cfg)
	if c != nil {
		t.Cleanup(c.Close)
	}
	return c, err
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSharedPortAndAuth(t *testing.T) {
	ts := startServer(t, wire.ConnConfig{Auth: wire.Password("pw")})

	// Plain HTTP on the same port.
	// 同一端口上的普通 HTTP。
	// Another path than /ws is refused.
	resp, err := http.Get("http://" + ts.addr + "/page")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}

	_, err = dial(t, "ws://"+ts.addr+"/ws", wire.DialConfig{ConnConfig: wire.ConnConfig{Auth: wire.Password("wrong")}})
	if !errors.Is(err, wire.ErrAuth) {
		t.Fatalf("wrong password: want ErrAuth, got %v", err)
	}
	select {
	case <-ts.opened:
		t.Fatal("rejected connection was opened")
	default:
	}

	c, err := dial(t, "ws://"+ts.addr+"/ws", wire.DialConfig{ConnConfig: wire.ConnConfig{Auth: wire.Password("pw")}})
	if err != nil {
		t.Fatal(err)
	}
	sc := <-ts.opened
	ctx := ctxT(t)
	// Calls in both directions: connection direction does not limit call direction.
	// 双向调用：连接方向不限制调用方向。
	for _, s := range []*wire.Session{c.Session, sc.Session} {
		got, err := s.Call(ctx, wire.Head{Method: "/echo"}, []byte("ping"))
		if err != nil || string(got) != "ping" {
			t.Fatalf("call: %q %v", got, err)
		}
	}

	c.Close()
	select {
	case <-ts.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not see the close")
	}
}

func TestHeartbeat(t *testing.T) {
	const interval = 100 * time.Millisecond
	ts := startServer(t, wire.ConnConfig{PingInterval: interval})

	// A live peer stays connected through many intervals of silence, since pings get answered.
	// 存活的对端即使长时间无业务流量也保持连接，因为 ping 会得到应答。
	c, err := dial(t, "ws://"+ts.addr+"/ws", wire.DialConfig{ConnConfig: wire.ConnConfig{PingInterval: interval}})
	if err != nil {
		t.Fatal(err)
	}
	<-ts.opened
	time.Sleep(8 * interval)
	select {
	case err := <-ts.closed:
		t.Fatalf("live connection closed: %v", err)
	default:
	}
	c.Close()
	<-ts.closed

	// A peer that completes the handshake and then goes silent is dropped after three intervals.
	// 完成握手后不再响应的对端在三个周期后被断开。
	engine := wire.SharedEngine(t)
	var once sync.Once
	mute := websocket.HandlerFuncs{Message: func(ws *websocket.Connection, _ websocket.Opcode, _ []byte) {
		once.Do(func() { _ = ws.WriteBinary(nil) }) // answer the challenge, ignore the rest / 应答挑战，之后全部忽略
	}}
	dialer := websocket.NewDialer(engine, websocket.DefaultDialerConfig())
	if _, _, err := dialer.Go("ws://"+ts.addr+"/ws", nil, mute).Wait(); err != nil {
		t.Fatal(err)
	}
	<-ts.opened
	start := time.Now()
	select {
	case err := <-ts.closed:
		if !errors.Is(err, wire.ErrHeartbeat) {
			t.Fatalf("want ErrHeartbeat, got %v", err)
		}
		if d := time.Since(start); d < 2*interval || d > 10*interval {
			t.Fatalf("closed after %v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("silent peer not dropped")
	}
}

func TestLargePayloadAndConcurrentStreams(t *testing.T) {
	ts := startServer(t, wire.ConnConfig{})
	c, err := dial(t, "ws://"+ts.addr+"/ws", wire.DialConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)

	// 15 MB of compressible JSON-like text, near the 16 MB frame limit, crosses a real connection gzip-compressed.
	// 15 MB 可压缩的类 JSON 文本（接近 16 MB 帧上限）经 gzip 压缩后在真实连接上传输。
	big := bytes.Repeat([]byte(`{"token":"hello","n":12345},`), 15<<20/28)
	got, err := c.Call(ctx, wire.Head{Method: "/echo"}, big)
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("large payload: %d bytes, %v", len(got), err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := c.OpenStream(ctx, wire.Head{Method: "/echo"}, nil)
			if err != nil {
				errs <- err
				return
			}
			for j := range 50 {
				msg := fmt.Sprintf("%d-%d", i, j)
				if err := st.Send(ctx, []byte(msg)); err != nil {
					errs <- err
					return
				}
				p, err := st.Recv(ctx)
				if err != nil || string(p) != msg {
					errs <- fmt.Errorf("stream %d: %q %v", i, p, err)
					return
				}
			}
			_ = st.CloseSend()
			if _, err := st.Recv(ctx); err != io.EOF || string(st.Result()) != "done" {
				errs <- fmt.Errorf("stream %d end: %v %q", i, err, st.Result())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// MaxConns refuses connections beyond the cap (ErrRefused at the dialer) until one closes; Accept can refuse too.
// MaxConns 拒绝超出上限的连接（拨号方得到 ErrRefused），直到有连接关闭；Accept 也可以拒绝。
func TestConnLimitAndAccept(t *testing.T) {
	var closing sync.Map
	ts := startServer(t, wire.ConnConfig{MaxConns: 1, Accept: func(r *http.Request) error {
		if _, ok := closing.Load("yes"); ok {
			return errors.New("shutting down")
		}
		return nil
	}})
	url := "ws://" + ts.addr + "/ws"
	c1, err := dial(t, url, wire.DialConfig{})
	if err != nil {
		t.Fatal(err)
	}
	<-ts.opened
	if _, err := dial(t, url, wire.DialConfig{}); !errors.Is(err, wire.ErrRefused) {
		t.Fatalf("second connection: want ErrRefused, got %v", err)
	}
	c1.Close()
	<-ts.closed
	c2, err := dial(t, url, wire.DialConfig{})
	if err != nil {
		t.Fatalf("after the first closed: %v", err)
	}
	<-ts.opened
	c2.Close()
	<-ts.closed

	closing.Store("yes", true)
	if _, err := dial(t, url, wire.DialConfig{}); !errors.Is(err, wire.ErrRefused) || !bytes.Contains([]byte(err.Error()), []byte("shutting down")) {
		t.Fatalf("refused by Accept: %v", err)
	}
}

// A peer that keeps the connection busy but never answers pings (as if its reads were far behind) is dropped with
// ErrSlow once a ping waited SlowTimeout, even though frames keep arriving (so the heartbeat never fires).
// 让连接保持繁忙、却从不应答 ping 的对端（如同其读取严重滞后）在 ping 等待 SlowTimeout 后以 ErrSlow 断开，即使帧一直在到达
// （心跳因此不会触发）。
func TestSlowConnection(t *testing.T) {
	const interval, slow = 100 * time.Millisecond, 400 * time.Millisecond
	ts := startServer(t, wire.ConnConfig{PingInterval: interval, SlowTimeout: slow})

	engine := wire.SharedEngine(t)
	gone := make(chan struct{})
	var once sync.Once
	busy := websocket.HandlerFuncs{
		Message: func(ws *websocket.Connection, _ websocket.Opcode, _ []byte) {
			once.Do(func() {
				_ = ws.WriteBinary(nil) // answer the challenge / 应答挑战
				go func() {
					for id := uint32(1); ; id += 2 {
						f := wire.Frame{Type: wire.TypeRequest, ID: id, Head: wire.Head{Method: "/x"}}
						b, _ := f.Marshal(0, 0)
						if ws.WriteBinary(b) != nil {
							return
						}
						select {
						case <-gone:
							return
						case <-time.After(20 * time.Millisecond):
						}
					}
				}()
			})
		},
		Close: func(*websocket.Connection, uint16, string, error) { close(gone) },
	}
	dialer := websocket.NewDialer(engine, websocket.DefaultDialerConfig())
	if _, _, err := dialer.Go("ws://"+ts.addr+"/ws", nil, busy).Wait(); err != nil {
		t.Fatal(err)
	}
	<-ts.opened
	start := time.Now()
	select {
	case err := <-ts.closed:
		if !errors.Is(err, wire.ErrSlow) {
			t.Fatalf("want ErrSlow, got %v", err)
		}
		if d := time.Since(start); d < slow || d > slow+5*interval {
			t.Fatalf("closed after %v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow peer not dropped")
	}
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("the peer still holds the connection")
	}
}
