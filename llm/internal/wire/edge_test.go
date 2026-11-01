package wire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/agent/internal/model"
)

// recorder is a transport that keeps what a session sends, decoded; fail, when set, fails the send of a frame.
// recorder 是记录会话所发送帧（已解码）的传输；fail 如已设置，可让某帧的发送失败。
type recorder struct {
	mu     sync.Mutex
	frames []*Frame
	fail   func(*Frame) bool
}

func (r *recorder) send(b []byte) error {
	f, err := Unmarshal(b, 0)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, f)
	if r.fail != nil && r.fail(f) {
		return errors.New("send failed")
	}
	return nil
}

// find returns the last recorded frame of type typ, or nil.
// find 返回最后一个类型为 typ 的已记录帧，没有则为 nil。
func (r *recorder) find(typ Type) *Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.frames) - 1; i >= 0; i-- {
		if r.frames[i].Type == typ {
			return r.frames[i]
		}
	}
	return nil
}

func (r *recorder) setFail(fn func(*Frame) bool) {
	r.mu.Lock()
	r.fail = fn
	r.mu.Unlock()
}

// recorded returns a session whose frames go to a recorder; the test feeds the peer's frames with in.
// recorded 返回一个帧发往记录器的会话；测试用 in 送入对端的帧。
func recorded(opts Options) (*Session, *recorder) {
	r := &recorder{}
	return NewSession(opts, r.send), r
}

// in encodes a frame as the peer would send it.
// in 按对端发送的方式编码一个帧。
func in(f Frame) []byte {
	b, err := f.Marshal(0, 0)
	if err != nil {
		panic(err)
	}
	return b
}

func TestTypeStringAndHelpers(t *testing.T) {
	if TypeStreamData.String() != "stream-data" || TypeDrain.String() != "drain" {
		t.Fatalf("names: %s %s", TypeStreamData, TypeDrain)
	}
	if Type(0).String() != "type(0)" || Type(99).String() != "type(99)" {
		t.Fatalf("unknown names: %s %s", Type(0), Type(99))
	}
	// An empty password proves nothing and accepts any proof.
	// 空密码不做证明，并接受任何证明。
	if p, err := Password("").Prove([]byte("c")); p != nil || err != nil {
		t.Fatalf("empty prove: %v %v", p, err)
	}
	if Password("").Verify([]byte("c"), []byte("x")) != nil {
		t.Fatal("empty password rejected a proof")
	}
	for _, p := range [][]byte{[]byte("{"), []byte(`{"code":1}`)} {
		var e *Error
		if err := decodeError(p); !errors.As(err, &e) || e.Code != model.CodeInternal || !strings.Contains(e.Msg, "malformed") {
			t.Fatalf("%s: %v", p, err)
		}
	}
}

func TestUnmarshalErrors(t *testing.T) {
	if _, err := Unmarshal(make([]byte, 20), 10); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("raw too large: %v", err)
	}
	// FlagGzip on a payload that is not gzip, and on a truncated gzip stream.
	// 非 gzip 载荷带 FlagGzip，以及被截断的 gzip 流。
	notGzip := append([]byte{byte(TypeStreamData), byte(FlagGzip), 0, 0, 0, 1}, "plain"...)
	if _, err := Unmarshal(notGzip, 0); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("not gzip: %v", err)
	}
	z := gzipBytes(bytes.Repeat([]byte("abcdefgh"), 100))
	truncated := append([]byte{byte(TypeStreamData), byte(FlagGzip), 0, 0, 0, 1}, z[:len(z)-10]...)
	if _, err := Unmarshal(truncated, 0); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("truncated gzip: %v", err)
	}
}

// A frame that cannot be encoded fails the call, push or stream at once, without closing the session.
// 无法编码的帧让调用、推送或流立即失败，不会关闭会话。
func TestSessionEncodeFailures(t *testing.T) {
	s, _ := recorded(Options{Dialer: true})
	if !s.LastSend().IsZero() {
		t.Fatal("LastSend set before any send")
	}
	long := Head{Method: strings.Repeat("m", 300)}
	if err := s.Push(long, nil); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("push: %v", err)
	}
	if _, err := s.Call(context.Background(), long, nil); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("call: %v", err)
	}
	if _, err := s.OpenStream(context.Background(), long, nil); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("open stream: %v", err)
	}
	if n := s.Inflight(); n != 0 || s.Err() != nil {
		t.Fatalf("inflight %d, err %v", n, s.Err())
	}
	s.Close(nil)
	if err := s.Push(Head{Method: "/p"}, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("push after close: %v", err)
	}
}

// allocLocked skips IDs in use and wraps around within its parity.
// allocLocked 跳过已占用的 ID，并在本方奇偶性内回绕。
func TestAllocWrapsAndSkips(t *testing.T) {
	s, _ := recorded(Options{Dialer: true})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID = math.MaxUint32
	s.calls[math.MaxUint32] = make(chan result, 1)
	s.streams[1] = &Stream{}
	if id := s.allocLocked(); id != 3 {
		t.Fatalf("got id %d, want 3", id)
	}
	l, _ := recorded(Options{})
	l.nextID = math.MaxUint32 - 1
	if a, b := l.allocLocked(), l.allocLocked(); a != math.MaxUint32-1 || b != 2 {
		t.Fatalf("listener ids %d %d", a, b)
	}
}

// The peer's protocol errors close the session: a bad credit frame, a request or stream ID already in use.
// 对端的协议错误会关闭会话：错误的 credit 帧、已被占用的请求或流 ID。
func TestPeerProtocolErrors(t *testing.T) {
	d, _ := recorded(Options{Dialer: true})
	if _, err := d.OpenStream(ctxT(t), Head{Method: "/s"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Receive(in(Frame{Type: TypeCredit, ID: 1, Payload: []byte{1, 2, 3}})); !errors.Is(err, ErrProtocol) {
		t.Fatalf("bad credit: %v", err)
	}
	if !errors.Is(d.Err(), ErrProtocol) {
		t.Fatalf("session err %v", d.Err())
	}

	block := make(chan struct{})
	defer close(block)
	h := Handlers{
		Request: func(context.Context, *Request) ([]byte, error) { <-block; return nil, nil },
		Stream:  func(context.Context, *Stream) ([]byte, error) { <-block; return nil, nil },
	}
	for _, typ := range []Type{TypeRequest, TypeStreamOpen} {
		l, _ := recorded(Options{Handlers: h})
		if err := l.Receive(in(Frame{Type: typ, ID: 1, Head: Head{Method: "/m"}})); err != nil {
			t.Fatal(err)
		}
		if err := l.Receive(in(Frame{Type: typ, ID: 1, Head: Head{Method: "/m"}})); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "in use") {
			t.Fatalf("%s reused id: %v", typ, err)
		}
	}
}

// After Close, requests, streams and pushes from the peer are dropped; pushes are dropped without a handler too.
// Close 之后对端的请求、流与推送被丢弃；没有推送处理函数时推送同样被丢弃。
func TestReceiveAfterClose(t *testing.T) {
	var pushes atomic.Int32
	l, r := recorded(Options{Handlers: Handlers{Push: func(*Request) { pushes.Add(1) }}})
	l.Close(nil)
	for _, f := range []Frame{
		{Type: TypeRequest, ID: 1, Head: Head{Method: "/m"}},
		{Type: TypeStreamOpen, ID: 3, Head: Head{Method: "/m"}},
		{Type: TypePush, Head: Head{Method: "/p"}},
	} {
		if err := l.Receive(in(f)); err != nil {
			t.Fatalf("%s: %v", f.Type, err)
		}
	}
	if pushes.Load() != 0 || len(r.frames) != 0 {
		t.Fatalf("pushes %d, frames sent %d", pushes.Load(), len(r.frames))
	}
	n, _ := recorded(Options{})
	if err := n.Receive(in(Frame{Type: TypePush, Head: Head{Method: "/p"}})); err != nil {
		t.Fatal(err)
	}
}

// A session without a stream handler ends every stream with NotFound.
// 没有流处理函数的会话以 NotFound 结束每个流。
func TestStreamNotSupported(t *testing.T) {
	d, _, _ := pair(t, Options{}, Handlers{}, Handlers{})
	st, err := d.OpenStream(ctxT(t), Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var e *Error
	if _, err := st.Recv(ctxT(t)); !errors.As(err, &e) || e.Code != model.CodeNotFound {
		t.Fatalf("got %v", err)
	}
}

// A response or stream result too large for a frame is replaced by an error saying so.
// 超出帧上限的应答或流结果被替换为说明这一点的错误。
func TestResultTooLarge(t *testing.T) {
	big := make([]byte, 2000)
	h := Handlers{
		Request: func(context.Context, *Request) ([]byte, error) { return big, nil },
		Stream:  func(context.Context, *Stream) ([]byte, error) { return big, nil },
	}
	d, _, _ := pair(t, Options{MaxFrame: 1024, CompressMin: -1}, Handlers{}, h)
	ctx := ctxT(t)
	if _, err := d.Call(ctx, Head{Method: "/r"}, nil); err == nil || err.Error() != ErrFrameTooLarge.Error() {
		t.Fatalf("call: %v", err)
	}
	st, err := d.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Recv(ctx); err == nil || err.Error() != ErrFrameTooLarge.Error() {
		t.Fatalf("stream: %v", err)
	}
}

// A peer that sends more pushes than the handler keeps up with is cut off.
// 发送的推送多于处理函数所能跟上的对端会被断开。
func TestPushQueueOverflow(t *testing.T) {
	block := make(chan struct{})
	l, _ := recorded(Options{Handlers: Handlers{Push: func(*Request) { <-block }}})
	defer close(block)
	push := in(Frame{Type: TypePush, Head: Head{Method: "/p"}})
	for range MaxPushQueue + 2 {
		_ = l.Receive(push)
	}
	if err := l.Err(); !errors.Is(err, ErrProtocol) {
		t.Fatalf("session err %v", err)
	}
}

// A call whose context ends while the session closes returns ErrClosed whichever of the two the select sees first.
// 会话关闭的同时调用的 context 结束：无论 select 先看到哪一个，调用都返回 ErrClosed。
func TestCallCanceledWhileClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 64 {
		var s *Session
		s = NewSession(Options{Dialer: true}, func([]byte) error { s.Close(nil); return nil })
		if _, err := s.Call(ctx, Head{Method: "/m"}, nil); !errors.Is(err, ErrClosed) {
			t.Fatalf("got %v", err)
		}
	}
}

// The opener's stream after the responder ended it cleanly, and after CloseSend.
// 响应方正常结束之后，以及 CloseSend 之后，打开方的流。
func TestStreamAfterEnd(t *testing.T) {
	d, _ := recorded(Options{Dialer: true})
	ctx := ctxT(t)
	st, err := d.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.ID() != 1 {
		t.Fatalf("id %d", st.ID())
	}
	if err := d.Receive(in(Frame{Type: TypeStreamEnd, ID: 1, Payload: []byte("r")})); err != nil {
		t.Fatal(err)
	}
	if err := st.Send(ctx, []byte("x")); !errors.Is(err, ErrStreamEnded) {
		t.Fatalf("send: %v", err)
	}
	if _, err := st.TrySend([]byte("x")); !errors.Is(err, ErrStreamEnded) {
		t.Fatalf("try send: %v", err)
	}
	if st.Err() != nil || st.OnCredit(func() {}) || st.CloseSend() != nil {
		t.Fatalf("err %v", st.Err())
	}
	st.End(nil, errors.New("ignored")) // the opener cannot End / 打开方不能 End
	if p, err := st.Recv(ctx); err != io.EOF || string(st.Result()) != "r" {
		t.Fatalf("recv %q %v", p, err)
	}

	st, err = d.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := st.Send(ctx, []byte("x")); !errors.Is(err, ErrStreamEnded) {
		t.Fatalf("send after CloseSend: %v", err)
	}
	if _, err := st.TrySend([]byte("x")); !errors.Is(err, ErrStreamEnded) {
		t.Fatalf("try send after CloseSend: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.Recv(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("recv canceled: %v", err)
	}
}

// A frame too large to encode fails Send and TrySend and gives the credit back.
// 无法编码的过大帧让 Send 与 TrySend 失败，并归还额度。
func TestStreamSendTooLarge(t *testing.T) {
	d, _ := recorded(Options{Dialer: true, MaxFrame: 1024, CompressMin: -1, Window: 4096})
	ctx := ctxT(t)
	st, err := d.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 2000)
	if err := st.Send(ctx, big); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("send: %v", err)
	}
	if !st.Credit() {
		t.Fatal("credit not given back")
	}
	if ok, err := st.TrySend(big); ok || !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("try send: %v %v", ok, err)
	}
}

// A Send waiting for credit, and an OnCredit callback, both end with the stream.
// 等待额度的 Send 与 OnCredit 回调都随流的结束而结束。
func TestStreamEndsWaiters(t *testing.T) {
	d, _ := recorded(Options{Dialer: true, Window: 1})
	ctx := ctxT(t)
	st, err := d.OpenStream(ctx, Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.TrySend([]byte("x")); !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, err := st.TrySend([]byte("y")); ok || err != nil {
		t.Fatalf("without credit: %v %v", ok, err)
	}
	credit := make(chan struct{})
	if !st.OnCredit(func() { close(credit) }) {
		t.Fatal("OnCredit refused")
	}
	sent := make(chan error, 1)
	go func() { sent <- st.Send(ctx, []byte("z")) }()
	time.Sleep(20 * time.Millisecond) // let Send wait / 让 Send 进入等待
	if err := d.Receive(in(Frame{Type: TypeStreamCancel, ID: 1})); err != nil {
		t.Fatal(err)
	}
	<-credit
	if err := <-sent; !errors.Is(err, ErrCanceled) {
		t.Fatalf("send: %v", err)
	}
}

// A peer that sends past the window and one frame fails the stream with ErrFlowControl.
// 超出窗口加一帧发送的对端使流以 ErrFlowControl 失败。
func TestStreamFlowControlViolation(t *testing.T) {
	d, r := recorded(Options{Dialer: true, Window: 16, MaxFrame: 64})
	st, err := d.OpenStream(ctxT(t), Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		_ = d.Receive(in(Frame{Type: TypeStreamData, ID: 1, Payload: make([]byte, 32)}))
	}
	if !errors.Is(st.Err(), ErrFlowControl) || r.find(TypeStreamCancel) == nil {
		t.Fatalf("err %v", st.Err())
	}
}

// The responder side: CloseSend is refused, data after the opener's half-close is dropped, End answers.
// 响应方：CloseSend 被拒绝，打开方半关闭之后的数据被丢弃，End 给出结果。
func TestResponderStream(t *testing.T) {
	got := make(chan *Stream, 1)
	release := make(chan struct{})
	l, r := recorded(Options{Handlers: Handlers{Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
		got <- st
		<-release
		return []byte("ignored"), nil
	}}})
	defer close(release)
	if err := l.Receive(in(Frame{Type: TypeStreamOpen, ID: 1, Head: Head{Method: "/s"}, Payload: []byte("open")})); err != nil {
		t.Fatal(err)
	}
	st := <-got
	if string(st.Payload()) != "open" || st.Context().Err() != nil {
		t.Fatalf("payload %q, context %v", st.Payload(), st.Context().Err())
	}
	if err := st.CloseSend(); err == nil {
		t.Fatal("responder CloseSend accepted")
	}
	_ = l.Receive(in(Frame{Type: TypeStreamEnd, ID: 1}))
	_ = l.Receive(in(Frame{Type: TypeStreamData, ID: 1, Payload: []byte("late")}))
	if p, err := st.Recv(ctxT(t)); err != io.EOF {
		t.Fatalf("recv %q %v", p, err)
	}
	st.End([]byte("res"), nil)
	if f := r.find(TypeStreamEnd); f == nil || string(f.Payload) != "res" {
		t.Fatalf("end frame %+v", f)
	}
}

// KeepAlive: a no-op without an idle timeout; it stops by itself once the stream ends or its frame cannot be sent.
// KeepAlive：没有空闲超时时不做任何事；流结束或其帧发送失败后自行停止。
func TestKeepAliveStops(t *testing.T) {
	n, _ := recorded(Options{Dialer: true, StreamIdle: -1})
	st, err := n.OpenStream(ctxT(t), Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.KeepAlive(0)()

	d, r := recorded(Options{Dialer: true})
	st, err = d.OpenStream(ctxT(t), Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := st.KeepAlive(5 * time.Millisecond)
	st.Cancel()
	time.Sleep(30 * time.Millisecond) // the timer fires after the end / 计时器在结束之后触发
	stop()

	st, err = d.OpenStream(ctxT(t), Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.setFail(func(f *Frame) bool { return f.Type == TypeStreamKeepalive })
	defer st.KeepAlive(5 * time.Millisecond)()
	select {
	case <-st.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("failed keepalive did not close the session")
	}
}

// OnData hands over an empty frame and releases its cost itself.
// OnData 交出空帧，并自行释放其占用。
func TestOnDataEmptyFrame(t *testing.T) {
	d, _ := recorded(Options{Dialer: true, Window: 2})
	st, err := d.OpenStream(ctxT(t), Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var frames, ends atomic.Int32
	st.OnData(func(p []byte, err error) {
		if err != nil {
			ends.Add(1)
		} else if len(p) == 0 {
			frames.Add(1)
		}
	})
	for range 3 {
		_ = d.Receive(in(Frame{Type: TypeStreamData, ID: 1}))
	}
	_ = d.Receive(in(Frame{Type: TypeStreamEnd, ID: 1}))
	if frames.Load() != 3 || ends.Load() != 1 || st.Err() != nil {
		t.Fatalf("frames %d ends %d err %v", frames.Load(), ends.Load(), st.Err())
	}
}

// A pump whose destination has failed ends with its error and returns every byte to the source, also for frames
// that arrive afterwards.
// 目的端已失败的 Pump 以其错误结束，并把所有字节归还给源端，之后到达的帧也一样。
func TestPumpDestinationFailed(t *testing.T) {
	a, _ := recorded(Options{Dialer: true, Window: 64})
	b, _ := recorded(Options{Dialer: true})
	ctx := ctxT(t)
	src, err := a.OpenStream(ctx, Head{Method: "/src"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := b.OpenStream(ctx, Head{Method: "/dst"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dst.Cancel()
	ended := make(chan error, 1)
	var firsts atomic.Int32
	Pump(src, dst, func() { firsts.Add(1) }, func(err error, frames int) {
		if frames != 1 {
			t.Errorf("frames %d", frames)
		}
		ended <- err
	})
	_ = a.Receive(in(Frame{Type: TypeStreamData, ID: 1, Payload: []byte("one")}))
	if err := <-ended; !errors.Is(err, ErrCanceled) {
		t.Fatalf("pump ended with %v", err)
	}
	_ = a.Receive(in(Frame{Type: TypeStreamData, ID: 1, Payload: []byte("two")}))
	_ = a.Receive(in(Frame{Type: TypeStreamEnd, ID: 1}))
	src.mu.Lock()
	buffered := src.buffered
	src.mu.Unlock()
	if buffered != 0 || firsts.Load() != 1 {
		t.Fatalf("buffered %d, first ran %d times", buffered, firsts.Load())
	}
}

// A stream opened with a context that already ended is canceled at once.
// 以已结束的 context 打开的流立即被取消。
func TestOpenStreamEndedContext(t *testing.T) {
	d, _ := recorded(Options{Dialer: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 32 {
		st, err := d.OpenStream(ctx, Head{Method: "/s"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		<-st.Done()
		if !errors.Is(st.Err(), context.Canceled) {
			t.Fatalf("err %v", st.Err())
		}
	}
}
