package wire

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Stream is one flow-controlled stream. The opener may Send any number of frames and CloseSend to half-close;
// the responder (a Handlers.Stream function) Sends frames and ends the whole stream by returning, whose
// result arrives at the opener as the final Recv error (io.EOF on success) plus Result. Either side's failure
// (cancel, idle timeout, context end, session close) ends the stream for both.
//
// Each direction has a receive window of Options.Window payload bytes: Send blocks while the peer has granted
// no credit, and Recv returns credit as data is consumed, so a slow reader stalls only its own stream.
//
// Stream 是一个带流控的流。打开方可以 Send 任意多帧，并用 CloseSend 半关闭；响应方（Handlers.Stream 函数）
// Send 数据帧，并通过返回结束整个流，返回结果在打开方表现为最后一次 Recv 的错误（成功时为 io.EOF）与 Result。
// 任一方失败（取消、空闲超时、context 结束、会话关闭）都会结束双方的流。
//
// 每个方向有 Options.Window 字节的接收窗口：对端未授予额度时 Send 阻塞，Recv 消费数据后归还额度，
// 所以慢读者只会阻塞自己的流。
type Stream struct {
	s       *Session
	id      uint32
	head    Head
	payload []byte
	opener  bool
	ctx     context.Context
	cancel  context.CancelCauseFunc
	done    chan struct{}
	recvSig chan struct{}
	sendSig chan struct{}
	idle    *time.Timer
	stopCtx func() bool // detaches the opener's context / 解除与打开方 context 的关联

	mu        sync.Mutex
	queue     [][]byte
	buffered  int  // bytes queued, not yet read / 已入队未读取的字节
	consumed  int  // bytes read, not yet credited back / 已读取但未归还额度的字节
	credit    int  // bytes we may still send / 本方还可发送的字节
	peerEnded bool // responder side: the opener half-closed / 响应方：打开方已半关闭
	sentEnd   bool // opener side: CloseSend called / 打开方：已调用 CloseSend
	finished  bool
	err       error
	result    []byte
	// onFinish (OnFinish) runs once when the stream ends. / onFinish（OnFinish）在流结束时执行一次。
	onFinish func(error)
	// receiver, when set (OnData), takes the data frames as they arrive,
	// in place of Recv; deliver keeps its calls in order. creditFn runs
	// once when credit comes back, or the stream ends (OnCredit).
	receiver   func(data []byte, err error)
	delivered  bool // the receiver heard the end
	delivering bool // a goroutine is in deliver's loop
	again      bool // more came while it was
	creditFn   func()

	// rx and tx count the bytes of the data frames received and sent.
	rx, tx atomic.Int64
}

func newStream(s *Session, id uint32, head Head, payload []byte, opener bool) *Stream {
	st := &Stream{
		s: s, id: id, head: head, payload: payload, opener: opener,
		done:    make(chan struct{}),
		recvSig: make(chan struct{}, 1),
		sendSig: make(chan struct{}, 1),
		credit:  s.opts.Window,
	}
	st.ctx, st.cancel = context.WithCancelCause(s.ctx)
	if d := s.opts.StreamIdle; d > 0 {
		st.mu.Lock() // the timer callback reads st.idle in finish / 计时器回调会在 finish 中读取 st.idle
		st.idle = time.AfterFunc(d, func() { st.abort(ErrStreamIdle) })
		st.mu.Unlock()
	}
	return st
}

// ID returns the stream ID.
// ID 返回流 ID。
func (st *Stream) ID() uint32 { return st.id }

// Head returns the method and route header the stream was opened with.
// Head 返回打开流时的方法与路由头。
func (st *Stream) Head() Head { return st.head }

// Payload returns the payload of the stream-open frame.
// Payload 返回 stream-open 帧的载荷。
func (st *Stream) Payload() []byte { return st.payload }

// Context is canceled when the stream ends; context.Cause tells why (ErrCanceled when the opener canceled,
// ErrStreamIdle, ErrClosed, the opener's context error, ...). It is the context the responder's handler runs with.
//
// Context 在流结束时取消；context.Cause 给出原因（打开方取消为 ErrCanceled，以及 ErrStreamIdle、ErrClosed、
// 打开方 context 的错误等）。响应方处理函数就运行在这个 context 上。
func (st *Stream) Context() context.Context { return st.ctx }

// Done is closed when the stream ends.
// Done 在流结束时关闭。
func (st *Stream) Done() <-chan struct{} { return st.done }

// Bytes returns the bytes of the data frames received and sent on the
// stream so far, its opening payload and its result left out.
// Bytes 返回流上目前收到与发出的数据帧字节数，不含打开时的载荷与结果。
func (st *Stream) Bytes() (in, out int64) { return st.rx.Load(), st.tx.Load() }

// Result returns the payload of the responder's stream-end, once Recv has returned io.EOF.
// Result 返回响应方 stream-end 的载荷，在 Recv 返回 io.EOF 之后有效。
func (st *Stream) Result() []byte {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.result
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func cost(p []byte) int { return max(len(p), 1) }

func (st *Stream) touch() {
	if st.idle != nil {
		st.idle.Reset(st.s.opts.StreamIdle)
	}
}

// Send sends one data frame, waiting for window credit.
// Send 发送一个数据帧，必要时等待窗口额度。
func (st *Stream) Send(ctx context.Context, data []byte) error {
	for {
		st.mu.Lock()
		switch {
		case st.finished:
			err := st.err
			st.mu.Unlock()
			if err == nil {
				err = ErrStreamEnded
			}
			return err
		case st.sentEnd:
			st.mu.Unlock()
			return ErrStreamEnded
		case st.credit > 0:
			n := cost(data)
			st.credit -= n
			st.mu.Unlock()
			if err := st.s.write(&Frame{Type: TypeStreamData, ID: st.id, Payload: data}); err != nil {
				st.mu.Lock()
				st.credit += n
				st.mu.Unlock()
				return err
			}
			st.tx.Add(int64(len(data)))
			st.touch()
			return nil
		}
		st.mu.Unlock()
		select {
		case <-st.sendSig:
		case <-st.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Recv returns the next data frame. After the peer finished sending it returns io.EOF, or the stream's error
// if it failed. Frames already received are returned before that.
//
// Recv 返回下一个数据帧。对端发送完毕后返回 io.EOF；流失败时返回其错误。已收到的帧会先返回。
func (st *Stream) Recv(ctx context.Context) ([]byte, error) {
	for {
		st.mu.Lock()
		if len(st.queue) > 0 {
			p := st.queue[0]
			st.queue[0] = nil
			st.queue = st.queue[1:]
			n := cost(p)
			st.buffered -= n
			st.consumed += n
			var grant int
			if !st.finished && !st.peerEnded && st.consumed >= st.s.opts.Window/2 {
				grant, st.consumed = st.consumed, 0
			}
			st.mu.Unlock()
			if grant > 0 {
				var b [4]byte
				binary.BigEndian.PutUint32(b[:], uint32(grant))
				_ = st.s.write(&Frame{Type: TypeCredit, ID: st.id, Payload: b[:]})
			}
			return p, nil
		}
		if st.finished {
			err := st.err
			st.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return nil, err
		}
		if st.peerEnded {
			st.mu.Unlock()
			return nil, io.EOF
		}
		st.mu.Unlock()
		select {
		case <-st.recvSig:
		case <-st.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// CloseSend half-closes the opener's direction; the responder's Recv then returns io.EOF.
// CloseSend 半关闭打开方的发送方向，之后响应方的 Recv 返回 io.EOF。
func (st *Stream) CloseSend() error {
	if !st.opener {
		return errors.New("wire: the responder ends a stream by returning from its handler")
	}
	st.mu.Lock()
	if st.finished || st.sentEnd {
		st.mu.Unlock()
		return nil
	}
	st.sentEnd = true
	st.mu.Unlock()
	return st.s.write(&Frame{Type: TypeStreamEnd, ID: st.id})
}

// KeepAlive sends a stream-keepalive frame every interval (≤ 0: a quarter of StreamIdle) until stop is called or the
// stream ends. The frame resets the idle timeout on both sides; Recv never returns it and it uses no window. A
// side that is still working but has nothing to send yet calls it, e.g. a node whose model is still reading a
// long prompt before its first token. Whatever else bounds the stream (the opener's context, the handler's own
// limits) still applies.
//
// KeepAlive 每隔 interval（≤ 0 时为 StreamIdle 的四分之一）发送一个 stream-keepalive 帧，直到调用 stop 或流结束。
// 该帧重置两端的空闲超时；Recv 不会返回它，也不占用窗口。仍在工作、但暂时没有数据可发的一方调用它，例如模型仍在读取
// 长提示词、尚未产出第一个 token 的节点。流的其他约束（打开方的 context、处理函数自己的上限）依然有效。
func (st *Stream) KeepAlive(interval time.Duration) (stop func()) {
	if interval <= 0 {
		interval = st.s.opts.StreamIdle / 4
	}
	if interval <= 0 { // no idle timeout / 没有空闲超时
		return func() {}
	}
	var (
		mu      sync.Mutex
		stopped bool
		t       *time.Timer
	)
	mu.Lock()
	defer mu.Unlock()
	t = time.AfterFunc(interval, func() {
		mu.Lock()
		defer mu.Unlock()
		select {
		case <-st.done:
			return
		default:
		}
		if stopped || st.s.write(&Frame{Type: TypeStreamKeepalive, ID: st.id}) != nil {
			return
		}
		st.touch()
		t.Reset(interval)
	})
	return func() {
		mu.Lock()
		stopped = true
		t.Stop()
		mu.Unlock()
	}
}

// OnFinish sets fn to run once when the stream ends, with nil when the responder ended it without error, else why it
// ended; at once if it already has. fn runs on whatever goroutine ends the stream and must not block (gopkg/mesh
// observes the duration of the streams it opens with it, without a goroutine per stream).
// OnFinish 设置流结束时执行一次的 fn：响应方无错误地结束时参数为 nil，否则为结束原因；流已结束时立即执行。fn 在结束流的协程中
// 执行，不得阻塞（gopkg/mesh 用它记录所打开的流的时长，而不必为每个流开一个协程）。
func (st *Stream) OnFinish(fn func(error)) {
	st.mu.Lock()
	if st.finished {
		err := st.err
		st.mu.Unlock()
		fn(err)
		return
	}
	st.onFinish = fn
	st.mu.Unlock()
}

// Cancel ends the stream for both sides with ErrCanceled.
// Cancel 以 ErrCanceled 结束双方的流。
func (st *Stream) Cancel() { st.abort(ErrCanceled) }

// finish marks the stream ended locally; it reports whether this call ended it.
// finish 在本地标记流已结束；返回是否由本次调用结束。
func (st *Stream) finish(err error, result []byte) bool {
	st.mu.Lock()
	if st.finished {
		st.mu.Unlock()
		return false
	}
	st.finished, st.err, st.result = true, err, result
	if st.idle != nil {
		st.idle.Stop()
	}
	if st.stopCtx != nil {
		st.stopCtx()
	}
	onFinish, creditFn := st.onFinish, st.creditFn
	st.creditFn = nil
	st.mu.Unlock()
	close(st.done)
	if onFinish != nil {
		onFinish(err)
	}
	if creditFn != nil {
		creditFn()
	}
	st.deliver()
	if err == nil {
		err = ErrStreamEnded
	}
	st.cancel(err)
	st.s.removeStream(st)
	return true
}

// abort fails the stream locally and tells the peer: the opener cancels, the responder sends an error end.
// abort 在本地使流失败并通知对端：打开方发送取消，响应方发送带错误的结束帧。
func (st *Stream) abort(err error) {
	if !st.finish(err, nil) {
		return
	}
	if st.opener {
		_ = st.s.write(&Frame{Type: TypeStreamCancel, ID: st.id})
	} else {
		_ = st.s.write(&Frame{Type: TypeStreamEnd, ID: st.id, Flags: FlagError, Payload: encodeError(err)})
	}
}

// end sends the responder's stream-end with the handler's result.
// end 用处理函数的结果发送响应方的 stream-end。
func (st *Stream) end(data []byte, err error) {
	if !st.finish(ErrStreamEnded, nil) {
		return
	}
	f := &Frame{Type: TypeStreamEnd, ID: st.id, Payload: data}
	if err != nil {
		f.Flags, f.Payload = FlagError, encodeError(err)
	}
	if st.s.write(f) == ErrFrameTooLarge {
		_ = st.s.write(&Frame{Type: TypeStreamEnd, ID: st.id, Flags: FlagError, Payload: encodeError(ErrFrameTooLarge)})
	}
}

func (st *Stream) onData(p []byte) {
	st.mu.Lock()
	if st.finished || st.peerEnded {
		st.mu.Unlock()
		return
	}
	st.buffered += cost(p)
	st.rx.Add(int64(len(p)))
	// The sender may overshoot the window by at most one frame.
	// 发送方最多超出窗口一个帧。
	over := st.buffered > st.s.opts.Window+st.s.opts.MaxFrame
	if !over {
		st.queue = append(st.queue, p)
	}
	st.mu.Unlock()
	if over {
		st.abort(ErrFlowControl)
		return
	}
	st.touch()
	signal(st.recvSig)
	st.deliver()
}

func (st *Stream) onKeepalive() {
	st.mu.Lock()
	finished := st.finished
	st.mu.Unlock()
	if !finished {
		st.touch()
	}
}

func (st *Stream) onCredit(n int) {
	st.mu.Lock()
	st.credit += n
	var fn func()
	if st.credit > 0 {
		fn, st.creditFn = st.creditFn, nil
	}
	st.mu.Unlock()
	signal(st.sendSig)
	if fn != nil {
		fn()
	}
}

func (st *Stream) onEnd(f *Frame) {
	if !st.opener {
		st.mu.Lock()
		st.peerEnded = true
		st.mu.Unlock()
		signal(st.recvSig)
		st.deliver()
		return
	}
	var err error
	if f.Flags&FlagError != 0 {
		err = decodeError(f.Payload)
		st.finish(err, nil)
		return
	}
	st.finish(nil, f.Payload)
}

// OnData makes fn take the stream's data frames as they arrive, in order,
// in place of Recv, and then once more with a nil frame and why the stream
// ended: io.EOF when the opener half-closed (responder) or the responder
// ended it without an error (opener; see Result), or the stream's error.
// Frames that arrived before OnData are handed over first, on the caller's
// goroutine. fn runs on whatever goroutine delivers a frame, the session's
// reader among them, and must not block. A frame stays counted against the
// window until Release returns its bytes, which is how a consumer slower
// than the sender holds the sender back without a goroutine waiting.
func (st *Stream) OnData(fn func(data []byte, err error)) {
	st.mu.Lock()
	st.receiver = fn
	st.mu.Unlock()
	st.deliver()
}

// deliver hands the receiver what waits for it, in order. Whichever
// goroutine finds another delivering leaves the work to it, so that fn is
// never called twice at once nor from inside itself.
func (st *Stream) deliver() {
	st.mu.Lock()
	if st.receiver == nil || st.delivered {
		st.mu.Unlock()
		return
	}
	if st.delivering {
		st.again = true
		st.mu.Unlock()
		return
	}
	st.delivering = true
	for {
		fn := st.receiver
		if len(st.queue) > 0 {
			p := st.queue[0]
			st.queue[0] = nil
			st.queue = st.queue[1:]
			st.mu.Unlock()
			if len(p) == 0 {
				st.release(1)
			}
			fn(p, nil)
			st.mu.Lock()
			continue
		}
		var end error
		switch {
		case st.finished:
			end = st.err
			if end == nil {
				end = io.EOF
			}
		case st.peerEnded:
			end = io.EOF
		}
		if end != nil {
			st.delivered = true
			st.delivering = false
			st.mu.Unlock()
			fn(nil, end)
			return
		}
		if !st.again {
			st.delivering = false
			st.mu.Unlock()
			return
		}
		st.again = false
	}
}

// Release gives back n bytes of the frames OnData handed over, consumed now,
// so that the sender may send as much again. A frame of no bytes is
// released by OnData itself.
func (st *Stream) Release(n int) {
	if n > 0 {
		st.release(n)
	}
}

func (st *Stream) release(n int) {
	st.mu.Lock()
	st.buffered -= n
	st.consumed += n
	var grant int
	if !st.finished && !st.peerEnded && st.consumed >= st.s.opts.Window/2 {
		grant, st.consumed = st.consumed, 0
	}
	st.mu.Unlock()
	if grant > 0 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(grant))
		_ = st.s.write(&Frame{Type: TypeCredit, ID: st.id, Payload: b[:]})
	}
}

// TrySend sends data as one frame if the peer has granted credit, and
// reports whether it did; it never waits. With no credit, OnCredit says when
// to try again. data is copied.
func (st *Stream) TrySend(data []byte) (bool, error) {
	st.mu.Lock()
	switch {
	case st.finished:
		err := st.err
		st.mu.Unlock()
		if err == nil {
			err = ErrStreamEnded
		}
		return false, err
	case st.sentEnd:
		st.mu.Unlock()
		return false, ErrStreamEnded
	case st.credit <= 0:
		st.mu.Unlock()
		return false, nil
	}
	n := cost(data)
	st.credit -= n
	st.mu.Unlock()
	if err := st.s.write(&Frame{Type: TypeStreamData, ID: st.id, Payload: data}); err != nil {
		return false, err
	}
	st.tx.Add(int64(len(data)))
	st.touch()
	return true, nil
}

// Credit reports whether the peer has granted room for another frame.
func (st *Stream) Credit() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.credit > 0
}

// OnCredit arranges for fn to run once, when the peer grants credit or the
// stream ends, and reports true; it reports false, and never runs fn, when
// there is credit already or the stream has ended. One fn at a time: a
// second replaces the first. fn runs on the goroutine that read the credit,
// and must not block.
func (st *Stream) OnCredit(fn func()) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.finished || st.credit > 0 {
		return false
	}
	st.creditFn = fn
	return true
}

// Err returns why the stream ended, nil while it runs or when the responder
// ended it without an error.
func (st *Stream) Err() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.err
}

// End ends the stream from the responder's side with result and err, as
// returning them from the handler would, for a handler that answers from a
// callback; the handler's own return is then ignored.
func (st *Stream) End(result []byte, err error) {
	if st.opener {
		return
	}
	st.end(result, err)
}
