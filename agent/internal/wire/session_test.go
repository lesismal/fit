package wire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/agent/internal/model"
)

// pair connects a dialer and a listener session; closing either "connection" closes both sessions.
// pair 连接拨号方与监听方会话；关闭任一“连接”会关闭两端会话。
func pair(t *testing.T, opts Options, dh, lh Handlers) (dialer, listener *Session, drop func()) {
	t.Helper()
	d, l, drop := Pipe(opts, dh, lh)
	t.Cleanup(drop)
	return d, l, drop
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestFrameRoundTrip(t *testing.T) {
	big := bytes.Repeat([]byte("abcdefgh"), 1000)
	cases := []Frame{
		{Type: TypeRequest, ID: 7, Head: Head{Method: "/node/info"}, Payload: []byte(`{"a":1}`)},
		{Type: TypeStreamOpen, ID: 9, Head: Head{Method: "/node/chat"}, Payload: big},
		{Type: TypeRequest, ID: 10, Head: Head{Method: "/node/jobs"}, Payload: []byte(`{}`)},
		{Type: TypeStreamEnd, ID: 9, Flags: FlagError, Payload: []byte(`{"code":1,"message":"x"}`)},
		{Type: TypeCredit, ID: 3, Payload: []byte{0, 0, 1, 0}},
		{Type: TypePing},
	}
	for _, f := range cases {
		b, err := f.Marshal(DefaultCompress, DefaultMaxFrame)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.Payload) == len(big) && len(b) >= len(big) {
			t.Fatalf("large payload not compressed: %d bytes", len(b))
		}
		g, err := Unmarshal(b, DefaultMaxFrame)
		if err != nil {
			t.Fatal(err)
		}
		if g.Type != f.Type || g.ID != f.ID || g.Flags != f.Flags || g.Head != f.Head || !bytes.Equal(g.Payload, f.Payload) {
			t.Fatalf("round trip mismatch: %+v != %+v", g.Head, f.Head)
		}
	}
}

func TestFrameLimits(t *testing.T) {
	f := Frame{Type: TypeStreamData, ID: 1, Payload: make([]byte, 2000)}
	if _, err := f.Marshal(-1, 1000); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
	// Compresses below the limit on the wire but inflates beyond it.
	// 线上压缩后低于上限，解压后超出上限。
	b, err := f.Marshal(1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unmarshal(b, 1000); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge on inflate, got %v", err)
	}
	for _, bad := range [][]byte{{1, 0}, {99, 0, 0, 0, 0, 1}, {1, 0, 0, 0, 0, 1, 5, 'a'}} {
		if _, err := Unmarshal(bad, 0); !errors.Is(err, ErrBadFrame) {
			t.Fatalf("%v: want ErrBadFrame, got %v", bad, err)
		}
	}
	long := Frame{Type: TypeRequest, Head: Head{Method: strings.Repeat("m", 256)}}
	if _, err := long.Marshal(0, 0); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("want ErrBadFrame for long method, got %v", err)
	}
}

func TestRequestResponse(t *testing.T) {
	h := Handlers{Request: func(_ context.Context, r *Request) ([]byte, error) {
		switch r.Method {
		case "/echo":
			return []byte(r.Method + ":" + string(r.Payload)), nil
		case "/biz":
			return nil, &Error{Code: model.CodeConflict, Msg: "taken"}
		}
		return nil, errors.New("boom")
	}}
	d, l, _ := pair(t, Options{}, h, h)
	ctx := ctxT(t)
	for _, s := range []*Session{d, l} { // both directions / 双向
		got, err := s.Call(ctx, Head{Method: "/echo"}, []byte("hi"))
		if err != nil || string(got) != "/echo:hi" {
			t.Fatalf("echo: %q %v", got, err)
		}
		_, err = s.Call(ctx, Head{Method: "/biz"}, nil)
		var e *Error
		if !errors.As(err, &e) || e.Code != model.CodeConflict || e.Msg != "taken" {
			t.Fatalf("biz error: %v", err)
		}
		_, err = s.Call(ctx, Head{Method: "/other"}, nil)
		if !errors.As(err, &e) || e.Code != model.CodeInternal {
			t.Fatalf("internal error: %v", err)
		}
	}
	// A session without a request handler answers NotFound.
	// 没有请求处理函数的会话返回 NotFound。
	d2, _, _ := pair(t, Options{}, Handlers{}, Handlers{})
	_, err := d2.Call(ctx, Head{Method: "/x"}, nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != model.CodeNotFound {
		t.Fatalf("no handler: %v", err)
	}
}

func TestCallCancelReachesHandler(t *testing.T) {
	canceled := make(chan struct{})
	h := Handlers{Request: func(ctx context.Context, _ *Request) ([]byte, error) {
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	}}
	d, _, _ := pair(t, Options{}, Handlers{}, h)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := d.Call(ctx, Head{Method: "/slow"}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler context not canceled")
	}
}

// echoStream sends every received frame back, then returns the frame count.
// echoStream 把收到的每一帧原样发回，最后返回帧数。
func echoStream(ctx context.Context, st *Stream) ([]byte, error) {
	n := 0
	for {
		p, err := st.Recv(ctx)
		if err == io.EOF {
			return []byte(fmt.Sprint(n)), nil
		}
		if err != nil {
			return nil, err
		}
		n++
		if err := st.Send(ctx, p); err != nil {
			return nil, err
		}
	}
}

func TestConcurrentStreams(t *testing.T) {
	d, _, _ := pair(t, Options{Window: 1024}, Handlers{}, Handlers{Stream: echoStream})
	ctx := ctxT(t)
	const streams, frames = 50, 40
	var wg sync.WaitGroup
	errs := make(chan error, streams)
	for i := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := d.OpenStream(ctx, Head{Method: "/echo"}, nil)
			if err != nil {
				errs <- err
				return
			}
			go func() {
				for j := range frames {
					if err := st.Send(ctx, []byte(fmt.Sprintf("%d-%d-%s", i, j, strings.Repeat("x", 100)))); err != nil {
						errs <- err
						return
					}
				}
				_ = st.CloseSend()
			}()
			for j := 0; ; j++ {
				p, err := st.Recv(ctx)
				if err == io.EOF {
					if j != frames || string(st.Result()) != fmt.Sprint(frames) {
						errs <- fmt.Errorf("stream %d: got %d frames, result %q", i, j, st.Result())
					}
					return
				}
				if err != nil {
					errs <- err
					return
				}
				if want := fmt.Sprintf("%d-%d-", i, j); !strings.HasPrefix(string(p), want) {
					errs <- fmt.Errorf("stream %d: out of order %q", i, p[:10])
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestSlowStreamDoesNotBlockOthers(t *testing.T) {
	const window = 1024
	stalled := make(chan struct{})
	h := Handlers{
		Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
			if st.Head().Method == "/flood" {
				// Fill the opener's window; it never reads, so Send must stall.
				// 填满打开方的窗口；打开方不读，所以 Send 必然阻塞。
				for {
					sctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
					err := st.Send(sctx, make([]byte, 256))
					cancel()
					if errors.Is(err, context.DeadlineExceeded) {
						close(stalled)
						<-ctx.Done()
						return nil, ctx.Err()
					}
					if err != nil {
						return nil, err
					}
				}
			}
			return echoStream(ctx, st)
		},
		Request: func(_ context.Context, r *Request) ([]byte, error) { return r.Payload, nil },
	}
	d, _, _ := pair(t, Options{Window: window}, Handlers{}, h)
	ctx := ctxT(t)
	slow, err := d.OpenStream(ctx, Head{Method: "/flood"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-stalled

	// Another stream and a request still work while /flood is stalled.
	// /flood 阻塞期间，其他流与请求仍然可用。
	st, _ := d.OpenStream(ctx, Head{Method: "/echo"}, nil)
	for i := range 20 {
		if err := st.Send(ctx, make([]byte, 200)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Recv(ctx); err != nil {
			t.Fatal(i, err)
		}
	}
	_ = st.CloseSend()
	if _, err := st.Recv(ctx); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	if got, err := d.Call(ctx, Head{Method: "/r"}, []byte("ok")); err != nil || string(got) != "ok" {
		t.Fatalf("call during stall: %q %v", got, err)
	}

	// The responder never overshoots the window by more than one frame.
	// 响应方超出窗口不超过一帧。
	slow.mu.Lock()
	buffered := slow.buffered
	slow.mu.Unlock()
	if buffered > window+256 {
		t.Fatalf("buffered %d bytes beyond window %d", buffered, window)
	}
	slow.Cancel()
}

func TestWindowPauseResume(t *testing.T) {
	const window = 1000
	recvReady := make(chan *Stream, 1)
	h := Handlers{Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
		recvReady <- st
		<-ctx.Done()
		return nil, nil
	}}
	d, _, _ := pair(t, Options{Window: window}, Handlers{}, h)
	ctx := ctxT(t)
	st, _ := d.OpenStream(ctx, Head{Method: "/sink"}, nil)
	peer := <-recvReady

	sent := 0
	for {
		sctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		err := st.Send(sctx, make([]byte, 100))
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		sent++
	}
	if sent != window/100 {
		t.Fatalf("sent %d frames before pausing, want %d", sent, window/100)
	}

	// Reading half the window returns credit and lets the sender continue.
	// 读取半个窗口后归还额度，发送方得以继续。
	resumed := make(chan error, 1)
	go func() { resumed <- st.Send(ctx, make([]byte, 100)) }()
	select {
	case <-resumed:
		t.Fatal("sent without credit")
	case <-time.After(50 * time.Millisecond):
	}
	for range window / 100 / 2 {
		if _, err := peer.Recv(ctx); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-resumed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not resume after credit")
	}
	st.Cancel()
}

func TestStreamCancel(t *testing.T) {
	handlerDone := make(chan error, 1)
	handlerCtx := make(chan context.Context, 2)
	h := Handlers{Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
		handlerCtx <- ctx
		for {
			if err := st.Send(ctx, []byte("tok")); err != nil {
				handlerDone <- err
				return nil, err
			}
			time.Sleep(time.Millisecond)
		}
	}}
	d, _, _ := pair(t, Options{}, Handlers{}, h)

	// Explicit cancel by the opener.
	// 打开方显式取消。
	ctx := ctxT(t)
	st, _ := d.OpenStream(ctx, Head{Method: "/gen"}, nil)
	if _, err := st.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	st.Cancel()
	if err := <-handlerDone; !errors.Is(err, ErrCanceled) && !errors.Is(err, context.Canceled) {
		t.Fatalf("handler saw %v", err)
	}
	if cause := context.Cause(<-handlerCtx); !errors.Is(cause, ErrCanceled) {
		t.Fatalf("handler context cause %v, want ErrCanceled", cause)
	}
	if err := st.Send(ctx, nil); !errors.Is(err, ErrCanceled) {
		t.Fatalf("send after cancel: %v", err)
	}

	// The opener's context ending cancels the stream too.
	// 打开方的 context 结束同样会取消流。
	cctx, cancel := context.WithCancel(ctx)
	st, _ = d.OpenStream(cctx, Head{Method: "/gen"}, nil)
	if _, err := st.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-handlerDone; err == nil {
		t.Fatal("handler not canceled")
	}
	<-st.Done()
}

func TestStreamErrorEnd(t *testing.T) {
	h := Handlers{Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
		_ = st.Send(ctx, []byte("partial"))
		return nil, &Error{Code: model.CodeUnavailable, Msg: "model gone"}
	}}
	d, _, _ := pair(t, Options{}, Handlers{}, h)
	ctx := ctxT(t)
	st, _ := d.OpenStream(ctx, Head{Method: "/x"}, nil)
	// Data that arrived before the error is still delivered.
	// 错误之前到达的数据仍会交付。
	if p, err := st.Recv(ctx); err != nil || string(p) != "partial" {
		t.Fatalf("got %q %v", p, err)
	}
	_, err := st.Recv(ctx)
	var e *Error
	if !errors.As(err, &e) || e.Code != model.CodeUnavailable {
		t.Fatalf("want unavailable, got %v", err)
	}
}

func TestStreamIdleTimeout(t *testing.T) {
	handlerCtx := make(chan context.Context, 1)
	h := Handlers{Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
		<-ctx.Done()
		handlerCtx <- ctx
		return nil, nil
	}}
	d, _, _ := pair(t, Options{StreamIdle: 100 * time.Millisecond}, Handlers{}, h)
	ctx := ctxT(t)
	st, _ := d.OpenStream(ctx, Head{Method: "/quiet"}, nil)
	start := time.Now()
	defer func() {
		// The handler's context says why it ended: its own idle timer, or the opener's (which cancels).
		// 处理函数的 context 给出结束原因：自身的空闲计时器，或打开方的（打开方随即取消）。
		if cause := context.Cause(<-handlerCtx); !errors.Is(cause, ErrStreamIdle) && !errors.Is(cause, ErrCanceled) {
			t.Errorf("handler context cause %v", cause)
		}
	}()
	if _, err := st.Recv(ctx); !errors.Is(err, ErrStreamIdle) {
		var e *Error
		// The responder's idle timer may fire first and report it as an error end.
		// 响应方的空闲计时器可能先触发，以带错误的结束帧告知。
		if !errors.As(err, &e) || !strings.Contains(e.Msg, "idle") {
			t.Fatalf("want idle timeout, got %v", err)
		}
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("idle timeout too slow")
	}
}

func TestCloseFailsEverything(t *testing.T) {
	started := make(chan struct{}, 10)
	h := Handlers{
		Request: func(ctx context.Context, _ *Request) ([]byte, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	d, l, drop := pair(t, Options{}, Handlers{}, h)
	ctx := ctxT(t)
	var streams []*Stream
	for range 3 {
		st, _ := d.OpenStream(ctx, Head{Method: "/s"}, nil)
		streams = append(streams, st)
	}
	calls := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := d.Call(ctx, Head{Method: "/r"}, nil)
			calls <- err
		}()
	}
	for range 6 {
		<-started
	}
	drop()
	for range 3 {
		if err := <-calls; !errors.Is(err, ErrClosed) {
			t.Fatalf("call: want ErrClosed, got %v", err)
		}
	}
	for _, st := range streams {
		if _, err := st.Recv(ctx); !errors.Is(err, ErrClosed) {
			t.Fatalf("stream: want ErrClosed, got %v", err)
		}
	}
	<-l.Done()
	if _, err := d.Call(ctx, Head{Method: "/r"}, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after close: %v", err)
	}
	if _, err := d.OpenStream(ctx, Head{Method: "/s"}, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("open after close: %v", err)
	}
}

func TestPushOrderAndPing(t *testing.T) {
	got := make(chan string, 100)
	d, l, _ := pair(t, Options{}, Handlers{}, Handlers{Push: func(m *Request) { got <- string(m.Payload) }})
	for i := range 100 {
		if err := d.Push(Head{Method: "/delta"}, []byte(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 100 {
		if s := <-got; s != fmt.Sprint(i) {
			t.Fatalf("push %d arrived as %s", i, s)
		}
	}
	before := l.LastRecv()
	time.Sleep(5 * time.Millisecond)
	if err := l.Ping(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !l.LastRecv().After(before) {
		if time.Now().After(deadline) {
			t.Fatal("no pong")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPeerStreamIDParity(t *testing.T) {
	_, l, _ := pair(t, Options{}, Handlers{}, Handlers{})
	// The listener allocates even IDs, so an even ID from the dialer is a protocol violation.
	// 监听方分配偶数 ID，因此拨号方发来的偶数 ID 违反协议。
	b, _ := (&Frame{Type: TypeRequest, ID: 2, Head: Head{Method: "/x"}}).Marshal(0, 0)
	if err := l.Receive(b); !errors.Is(err, ErrProtocol) {
		t.Fatalf("want ErrProtocol, got %v", err)
	}
	<-l.Done()
}

// OnFinish runs once when a stream ends (nil for a clean end), or at once on a stream already ended.
// OnFinish 在流结束时执行一次（正常结束为 nil），流已结束时立即执行。
func TestStreamOnFinish(t *testing.T) {
	d, _, closeFn := Pipe(Options{}, Handlers{}, Handlers{
		Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
			return []byte("done"), st.Send(ctx, []byte("x"))
		},
	})
	defer closeFn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := d.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 2)
	st.OnFinish(func(err error) { ended <- err })
	for {
		if _, err := st.Recv(ctx); err != nil {
			break
		}
	}
	if err := <-ended; err != nil {
		t.Fatalf("clean end reported %v", err)
	}
	st.OnFinish(func(err error) { ended <- errors.New("again") })
	if err := <-ended; err == nil || err.Error() != "again" {
		t.Fatalf("late OnFinish: %v", err)
	}
}
