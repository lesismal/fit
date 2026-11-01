package wire

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/agent/internal/model"
)

// Defaults for Options.
// Options 的默认值。
const (
	DefaultWindow     = 256 << 10
	DefaultStreamIdle = 2 * time.Minute
	DefaultMaxInbound = 1024
	// MaxPushQueue bounds the pushes of a connection waiting for their
	// handler; a peer that sends more than its handler keeps up with is
	// cut off, and its pushes are sent again when it links again.
	MaxPushQueue = 1024
)

// Request is an inbound request or push.
// Request 是收到的请求或推送。
type Request struct {
	Head
	Payload []byte
}

// Handlers serve what the peer initiates. Request and Stream run in their own goroutine with a context that
// is canceled when the peer cancels or the session closes; their result becomes the response / stream-end.
// Push handlers run one at a time in arrival order. A nil handler rejects that kind of frame.
//
// Drain, if set, is called once when the peer announces it is draining (see Session.Drain); it runs on the
// receiving goroutine and must not block.
//
// Handlers 处理对端发起的调用。Request 与 Stream 各在独立协程中运行，对端取消或会话关闭时其 context 被取消；
// 返回值即为 response / stream-end。Push 按到达顺序逐个执行。为 nil 时拒绝对应类型的帧。
// Drain 如已设置，在对端宣告下线（见 Session.Drain）时调用一次；它在接收协程中执行，不得阻塞。
type Handlers struct {
	Request func(ctx context.Context, req *Request) ([]byte, error)
	Stream  func(ctx context.Context, st *Stream) ([]byte, error)
	Push    func(msg *Request)
	Drain   func()
}

// Options configure a Session. Both ends must use the same Window.
// Options 配置会话。两端的 Window 必须一致。
type Options struct {
	// Dialer selects the stream ID space: the dialing side uses odd IDs, the listening side even ones.
	// Dialer 决定流 ID 空间：拨号方用奇数，监听方用偶数。
	Dialer bool
	// Window is the per-stream receive window in payload bytes (default DefaultWindow).
	// Window 每个流的接收窗口，按载荷字节计（默认 DefaultWindow）。
	Window int
	// StreamIdle ends a stream with no data frame in either direction for this long (0: default, < 0: never).
	// StreamIdle 流在任一方向上这么久没有数据帧即结束（0：默认值，< 0：不限制）。
	StreamIdle time.Duration
	// MaxFrame bounds an encoded or inflated frame (default DefaultMaxFrame).
	// MaxFrame 编码后或解压后帧的上限（默认 DefaultMaxFrame）。
	MaxFrame int
	// CompressMin is the smallest payload worth compressing (0: default, < 0: never).
	// CompressMin 值得压缩的最小载荷（0：默认值，< 0：不压缩）。
	CompressMin int
	// MaxInbound caps the requests and streams the peer may have running here at once (0: DefaultMaxInbound,
	// < 0: no limit). One more is refused at once with a CodeRateLimited error instead of queuing.
	// MaxInbound 对端在本方同时运行的请求与流的上限（0：DefaultMaxInbound，< 0：不限制）。超出的立即以 CodeRateLimited
	// 错误拒绝，而不是排队。
	MaxInbound int
	Handlers   Handlers
}

// Session multiplexes requests, streams and pushes over one connection. The transport passes every inbound
// message to Receive and supplies send for outbound ones; send must be safe for concurrent use. A failed send
// closes the session. Closing the session does not close the transport: watch Done for that.
//
// Session 在一条连接上复用请求、流与推送。传输层把每条收到的消息交给 Receive，并提供可并发调用的 send 发送消息；
// send 失败会关闭会话。关闭会话不会关闭传输，传输层应监听 Done。
type Session struct {
	opts   Options
	send   func([]byte) error
	ctx    context.Context // canceled on close; parent of handler contexts / 关闭时取消，是处理函数 context 的父级
	cancel context.CancelFunc

	mu       sync.Mutex
	closeErr error
	nextID   uint32
	calls    map[uint32]chan result        // outbound requests / 发出的请求
	served   map[uint32]context.CancelFunc // inbound requests being served / 正在处理的请求
	streams  map[uint32]*Stream

	inbound int // requests being served + streams the peer opened / 正在处理的请求 + 对端打开的流

	pushQ       []*Request
	pushRunning bool

	lastRecv  atomic.Int64
	lastSend  atomic.Int64 // last frame other than ping / pong / 最近一次发送 ping / pong 以外的帧
	pingSeq   atomic.Uint64
	pingAt    atomic.Int64 // when the unanswered ping was sent, 0 if none / 未应答的 ping 的发送时间，没有则为 0
	drainSent atomic.Bool
	peerDrain atomic.Bool
}

type result struct {
	data []byte
	err  error
}

// NewSession creates a session; send writes one encoded frame to the transport.
// NewSession 创建会话；send 向传输层写入一个编码后的帧。
func NewSession(opts Options, send func([]byte) error) *Session {
	if opts.Window <= 0 {
		opts.Window = DefaultWindow
	}
	if opts.StreamIdle == 0 {
		opts.StreamIdle = DefaultStreamIdle
	}
	if opts.MaxFrame <= 0 {
		opts.MaxFrame = DefaultMaxFrame
	}
	if opts.CompressMin == 0 {
		opts.CompressMin = DefaultCompress
	}
	if opts.MaxInbound == 0 {
		opts.MaxInbound = DefaultMaxInbound
	}
	s := &Session{
		opts:    opts,
		send:    send,
		calls:   map[uint32]chan result{},
		served:  map[uint32]context.CancelFunc{},
		streams: map[uint32]*Stream{},
		nextID:  2,
	}
	if opts.Dialer {
		s.nextID = 1
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.lastRecv.Store(time.Now().UnixNano())
	return s
}

// Done is closed when the session closes.
// Done 在会话关闭时关闭。
func (s *Session) Done() <-chan struct{} { return s.ctx.Done() }

// Err returns why the session closed, or nil while it is open.
// Err 返回会话关闭的原因，未关闭时为 nil。
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

// LastRecv is when the last frame arrived (any type), for heartbeat timeouts.
// LastRecv 最近一次收到帧（任意类型）的时间，用于心跳超时判断。
func (s *Session) LastRecv() time.Time { return time.Unix(0, s.lastRecv.Load()) }

// LastSend is when a frame other than ping / pong was last sent (zero time if none).
// LastSend 最近一次发送 ping / pong 以外的帧的时间（没有则为零值）。
func (s *Session) LastSend() time.Time {
	if t := s.lastSend.Load(); t != 0 {
		return time.Unix(0, t)
	}
	return time.Time{}
}

// Unanswered is how long the last ping has been waiting for its pong (0 when answered). A pong queues behind
// everything sent before its ping, in both directions, so this measures how far behind the connection is.
// Unanswered 最近一次 ping 等待 pong 的时长（已应答时为 0）。pong 在两个方向上都排在 ping 之前发送的所有内容之后，
// 所以它衡量的是连接积压了多久。
func (s *Session) Unanswered() time.Duration {
	if t := s.pingAt.Load(); t != 0 {
		return time.Since(time.Unix(0, t))
	}
	return 0
}

// Inflight counts the calls and streams not finished yet, in both directions.
// Inflight 统计两个方向上尚未结束的调用与流。
func (s *Session) Inflight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls) + len(s.served) + len(s.streams)
}

// Drain tells the peer that this side is going away (a drain frame, sent once): the peer stops starting calls,
// streams and pushes on this connection, while those already running continue. Nothing is refused here; the
// caller decides when to close (see gopkg/mesh).
// Drain 告知对端本方即将下线（drain 帧，只发送一次）：对端不再在该连接上发起新的调用、流与推送，已在进行的继续。
// 本方并不拒绝任何帧；何时关闭由调用方决定（见 gopkg/mesh）。
func (s *Session) Drain() error {
	if !s.drainSent.CompareAndSwap(false, true) {
		return nil
	}
	return s.write(&Frame{Type: TypeDrain})
}

// Draining reports whether either side has sent drain: new work should go to another connection.
// Draining 表示任一方已发送 drain：新的工作应交给其他连接。
func (s *Session) Draining() bool { return s.drainSent.Load() || s.peerDrain.Load() }

// PeerDraining reports whether the peer has sent drain.
// PeerDraining 表示对端已发送 drain。
func (s *Session) PeerDraining() bool { return s.peerDrain.Load() }

// Close fails every pending call and stream with ErrClosed and cancels running handlers. Idempotent.
// Close 让所有未完成的调用与流以 ErrClosed 失败，并取消正在运行的处理函数。可重复调用。
func (s *Session) Close(cause error) {
	s.mu.Lock()
	if s.closeErr != nil {
		s.mu.Unlock()
		return
	}
	if cause == nil {
		cause = ErrClosed
	}
	s.closeErr = cause
	calls, streams := s.calls, s.streams
	s.calls, s.streams = map[uint32]chan result{}, map[uint32]*Stream{}
	s.pushQ = nil
	s.mu.Unlock()

	for _, ch := range calls {
		ch <- result{err: ErrClosed}
	}
	// Streams first, so that their contexts' cause is ErrClosed rather than the session's plain cancel.
	// 先结束流，使流 context 的原因为 ErrClosed，而不是会话的普通取消。
	for _, st := range streams {
		st.finish(ErrClosed, nil)
	}
	s.cancel()
}

func (s *Session) closedErr() error {
	if s.closeErr != nil {
		return ErrClosed
	}
	return nil
}

// allocLocked returns a free stream ID of our parity.
// allocLocked 返回一个本方奇偶性且未被占用的流 ID。
func (s *Session) allocLocked() uint32 {
	for {
		id := s.nextID
		s.nextID += 2
		if s.nextID < id { // wrapped / 回绕
			s.nextID = 2 - id%2
		}
		if _, ok := s.calls[id]; ok {
			continue
		}
		if _, ok := s.streams[id]; ok {
			continue
		}
		return id
	}
}

func (s *Session) write(f *Frame) error {
	b, err := f.Marshal(s.opts.CompressMin, s.opts.MaxFrame)
	if err != nil {
		return err
	}
	if err := s.send(b); err != nil {
		s.Close(fmt.Errorf("wire: send: %w", err))
		return ErrClosed
	}
	if f.Type != TypePing && f.Type != TypePong {
		s.lastSend.Store(time.Now().UnixNano())
	}
	return nil
}

// Call sends a request and waits for its response, the context, or the session closing. When ctx ends first
// the peer is told to cancel the handler.
//
// Call 发送请求并等待应答、context 结束或会话关闭。ctx 先结束时通知对端取消处理。
func (s *Session) Call(ctx context.Context, head Head, payload []byte) ([]byte, error) {
	ch := make(chan result, 1)
	s.mu.Lock()
	if err := s.closedErr(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	id := s.allocLocked()
	s.calls[id] = ch
	s.mu.Unlock()

	if err := s.write(&Frame{Type: TypeRequest, ID: id, Head: head, Payload: payload}); err != nil {
		s.dropCall(id)
		return nil, err
	}
	select {
	case r := <-ch:
		return r.data, r.err
	case <-ctx.Done():
		if s.dropCall(id) {
			_ = s.write(&Frame{Type: TypeStreamCancel, ID: id})
			return nil, ctx.Err()
		}
		r := <-ch // the response or close won the race / 应答或关闭先到
		return r.data, r.err
	}
}

func (s *Session) dropCall(id uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.calls[id]; !ok {
		return false
	}
	delete(s.calls, id)
	return true
}

// Push sends a one-way message.
// Push 发送单向消息。
func (s *Session) Push(head Head, payload []byte) error {
	s.mu.Lock()
	err := s.closedErr()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.write(&Frame{Type: TypePush, Head: head, Payload: payload})
}

// Ping sends a ping carrying a sequence number; the peer echoes it in a pong, which refreshes LastRecv and, for the
// latest ping, clears Unanswered.
// Ping 发送带序号的 ping；对端在 pong 中原样返回，pong 刷新 LastRecv，最新一次 ping 的 pong 还会清零 Unanswered。
func (s *Session) Ping() error {
	seq := s.pingSeq.Add(1)
	s.pingAt.CompareAndSwap(0, time.Now().UnixNano())
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], seq)
	return s.write(&Frame{Type: TypePing, Payload: b[:]})
}

// OpenStream opens a stream and returns at once (the peer reports failure through stream-end). The stream is
// canceled when ctx ends, so the context's deadline bounds its total duration.
//
// OpenStream 打开流并立即返回（对端通过 stream-end 报告失败）。ctx 结束时流被取消，因此 context 的截止时间
// 即流的总时长上限。
func (s *Session) OpenStream(ctx context.Context, head Head, payload []byte) (*Stream, error) {
	s.mu.Lock()
	if err := s.closedErr(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	st := newStream(s, s.allocLocked(), head, payload, true)
	s.streams[st.id] = st
	s.mu.Unlock()

	if err := s.write(&Frame{Type: TypeStreamOpen, ID: st.id, Head: head, Payload: payload}); err != nil {
		st.finish(err, nil)
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { st.abort(ctx.Err()) })
	st.mu.Lock()
	if st.finished {
		st.mu.Unlock()
		stop()
	} else {
		st.stopCtx = stop
		st.mu.Unlock()
	}
	return st, nil
}

// Receive handles one inbound message. It never blocks on handlers. An error means the peer broke the
// protocol; the session is closed and the transport should drop the connection.
//
// Receive 处理一条收到的消息，不会阻塞在处理函数上。返回错误表示对端违反协议，此时会话已关闭，传输层应断开连接。
func (s *Session) Receive(msg []byte) error {
	f, err := Unmarshal(msg, s.opts.MaxFrame)
	if err == nil {
		err = s.dispatch(f)
	}
	if err != nil {
		s.Close(err)
	}
	return err
}

func (s *Session) dispatch(f *Frame) error {
	s.lastRecv.Store(time.Now().UnixNano())
	switch f.Type {
	case TypePing:
		_ = s.write(&Frame{Type: TypePong, Payload: f.Payload})
	case TypePong:
		if len(f.Payload) == 8 && binary.BigEndian.Uint64(f.Payload) == s.pingSeq.Load() {
			s.pingAt.Store(0)
		}
	case TypeDrain:
		if s.peerDrain.CompareAndSwap(false, true) && s.opts.Handlers.Drain != nil {
			s.opts.Handlers.Drain()
		}
	case TypePush:
		s.enqueuePush(&Request{Head: f.Head, Payload: f.Payload})
	case TypeRequest:
		return s.serveRequest(f)
	case TypeResponse:
		s.mu.Lock()
		ch, ok := s.calls[f.ID]
		delete(s.calls, f.ID)
		s.mu.Unlock()
		if ok { // otherwise canceled locally; late responses are dropped / 否则已在本地取消，迟到的应答丢弃
			if f.Flags&FlagError != 0 {
				ch <- result{err: decodeError(f.Payload)}
			} else {
				ch <- result{data: f.Payload}
			}
		}
	case TypeStreamOpen:
		return s.serveStream(f)
	case TypeStreamCancel:
		s.mu.Lock()
		cancel, ok := s.served[f.ID]
		st := s.streams[f.ID]
		s.mu.Unlock()
		if ok {
			cancel()
		}
		if st != nil {
			st.finish(ErrCanceled, nil)
		}
	case TypeStreamData, TypeStreamEnd, TypeCredit, TypeStreamKeepalive:
		s.mu.Lock()
		st := s.streams[f.ID]
		s.mu.Unlock()
		if st == nil {
			return nil // already finished here / 本地已结束
		}
		switch f.Type {
		case TypeStreamData:
			st.onData(f.Payload)
		case TypeStreamEnd:
			st.onEnd(f)
		case TypeStreamKeepalive:
			st.onKeepalive()
		default:
			if len(f.Payload) != 4 {
				return fmt.Errorf("%w: bad credit frame", ErrProtocol)
			}
			st.onCredit(int(binary.BigEndian.Uint32(f.Payload)))
		}
	}
	return nil
}

// checkPeerIDLocked verifies a stream ID the peer allocated: its parity, and that it is not in use.
// checkPeerID 校验对端分配的流 ID：奇偶性正确且未被占用。
func (s *Session) checkPeerIDLocked(id uint32) error {
	peerOdd := !s.opts.Dialer
	if id == 0 || (id%2 == 1) != peerOdd {
		return fmt.Errorf("%w: stream id %d has our parity", ErrProtocol, id)
	}
	if _, ok := s.served[id]; ok {
		return fmt.Errorf("%w: stream id %d in use", ErrProtocol, id)
	}
	if _, ok := s.streams[id]; ok {
		return fmt.Errorf("%w: stream id %d in use", ErrProtocol, id)
	}
	return nil
}

func (s *Session) serveRequest(f *Frame) error {
	s.mu.Lock()
	if s.closeErr != nil {
		s.mu.Unlock()
		return nil
	}
	if err := s.checkPeerIDLocked(f.ID); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.overLimitLocked() {
		s.mu.Unlock()
		_ = s.write(&Frame{Type: TypeResponse, ID: f.ID, Flags: FlagError, Payload: encodeError(errTooBusy)})
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.served[f.ID] = cancel
	s.inbound++
	s.mu.Unlock()

	go func() {
		var data []byte
		var err error
		if h := s.opts.Handlers.Request; h != nil {
			data, err = h(ctx, &Request{Head: f.Head, Payload: f.Payload})
		} else {
			err = &Error{Code: model.CodeNotFound, Msg: "requests not supported"}
		}
		s.mu.Lock()
		delete(s.served, f.ID)
		s.inbound--
		s.mu.Unlock()
		canceled := ctx.Err() != nil
		cancel()
		if canceled {
			return // the caller gave up or the session closed / 调用方已放弃或会话已关闭
		}
		resp := &Frame{Type: TypeResponse, ID: f.ID, Payload: data}
		if err != nil {
			resp.Flags, resp.Payload = FlagError, encodeError(err)
		}
		if s.write(resp) == ErrFrameTooLarge {
			_ = s.write(&Frame{Type: TypeResponse, ID: f.ID, Flags: FlagError, Payload: encodeError(ErrFrameTooLarge)})
		}
	}()
	return nil
}

func (s *Session) serveStream(f *Frame) error {
	s.mu.Lock()
	if s.closeErr != nil {
		s.mu.Unlock()
		return nil
	}
	if err := s.checkPeerIDLocked(f.ID); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.overLimitLocked() {
		s.mu.Unlock()
		_ = s.write(&Frame{Type: TypeStreamEnd, ID: f.ID, Flags: FlagError, Payload: encodeError(errTooBusy)})
		return nil
	}
	st := newStream(s, f.ID, f.Head, f.Payload, false)
	s.streams[f.ID] = st
	s.inbound++
	s.mu.Unlock()

	go func() {
		var data []byte
		var err error
		if h := s.opts.Handlers.Stream; h != nil {
			data, err = h(st.ctx, st)
		} else {
			err = &Error{Code: model.CodeNotFound, Msg: "streams not supported"}
		}
		st.end(data, err)
	}()
	return nil
}

func (s *Session) enqueuePush(r *Request) {
	h := s.opts.Handlers.Push
	if h == nil {
		return
	}
	s.mu.Lock()
	if s.closeErr != nil {
		s.mu.Unlock()
		return
	}
	if len(s.pushQ) >= MaxPushQueue {
		s.mu.Unlock()
		s.Close(fmt.Errorf("%w: more than %d pushes waiting", ErrProtocol, MaxPushQueue))
		return
	}
	s.pushQ = append(s.pushQ, r)
	if s.pushRunning {
		s.mu.Unlock()
		return
	}
	s.pushRunning = true
	s.mu.Unlock()

	// A worker only while the queue is non-empty, so idle sessions hold no goroutine.
	// 仅在队列非空时存在处理协程，空闲会话不占协程。
	go func() {
		for {
			s.mu.Lock()
			if len(s.pushQ) == 0 {
				s.pushRunning = false
				s.mu.Unlock()
				return
			}
			r := s.pushQ[0]
			s.pushQ[0] = nil
			s.pushQ = s.pushQ[1:]
			s.mu.Unlock()
			h(r)
		}
	}()
}

func (s *Session) removeStream(st *Stream) {
	s.mu.Lock()
	if s.streams[st.id] == st {
		delete(s.streams, st.id)
		if !st.opener {
			s.inbound--
		}
	}
	s.mu.Unlock()
}

// errTooBusy refuses a request or stream beyond Options.MaxInbound.
// errTooBusy 拒绝超出 Options.MaxInbound 的请求或流。
var errTooBusy = &Error{Code: model.CodeRateLimited, Msg: "too many concurrent requests and streams on this connection"}

func (s *Session) overLimitLocked() bool {
	return s.opts.MaxInbound > 0 && s.inbound >= s.opts.MaxInbound
}
