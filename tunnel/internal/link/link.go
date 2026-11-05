//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

// Package link is the protocol the tunnel's hub, nodes and proxies speak
// over a transport connection: framed calls, replies and
// notifies, and the tunnel frames among them.
//
// # Wire format
//
// The connection carries frames, one after another, and nothing else:
//
//	frame  = length header body
//	length = uint32, big-endian: the bytes of header and body, at least 6
//	         and at most the link's limit, MaxFrame unless SetMaxFrame
//	         lowers it
//	header = kind:uint8 method:uint8 id:uint32 (big-endian)
//
// The kinds:
//
//	1 call    asks the other side to run method with body; id, never 0,
//	          is the call's, unique among the caller's calls waiting
//	2 reply   answers call id: body is the result
//	3 error   answers call id with a failure: body is its message, UTF-8
//	4 notify  runs method with body, unanswered; id is 0
//
// A reply or error carries its call's method too. A call that is not
// answered within the caller's timeout fails; a reply that comes after is
// dropped. The methods are package proto's, one byte each.
//
// The body of a control message - a registration, a ping, a node list - is
// JSON, or nothing for no value; a string, such as an error, a kick's
// reason or a tunnel's close reason, is its bytes. The body of a tunnel
// frame (proto.MethodTunnelData, Ack and Close) is the tunnel's ID, 8 bytes
// big-endian, and then the payload; the hub passes such a body on, as it
// is, to the other end of the tunnel.
//
// With a body password (package bodycrypt) set on both sides, each side
// first sends a hello, before any other frame and without waiting for the
// other's:
//
//	hello = header of kind 0x85, method 0, id 0; body = version:uint8
//	        random:16 bytes
//
// version is Version. Once a side has the other's hello, every frame it
// sends has its body sealed with AES-256-GCM and bit 0x80 of its kind set:
//
//	body = sealed body, tag:16 bytes
//
// with the header, flag included, as the additional data, under the key of
// its direction on this link, derived from the password, both hellos'
// random bytes and which side dialed (see package bodycrypt), and the
// frame's number in that direction, from 0, as the nonce. The side that
// dialed has the Coder's Dialer. A side with a body password closes the connection of a hello of
// another version, of a sealed frame before the hello (a peer of before
// Version 2, whose frames could be replayed), of a hello that is its own
// sent back, and of a frame that is not sealed or does not open as the next
// one: a frame replayed, reordered, reflected or carried over from another
// link. A side without one closes the connection of a hello or sealed
// frame.
//
// A frame of an unknown kind, or a length out of bounds, closes the
// connection too. A call of a method the side does not serve is answered
// with an error.
//
// A link is read on the goroutine that feeds it, a fib worker for the
// transports on fib's engine: inline handlers must not block, and the
// others run on goroutines of their own, at most MaxAsync of a link's at
// once; a call past them is answered with ErrBusy. Call blocks, and is for
// those goroutines and the roles' own, never for a fib worker.
package link

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/logs"

	"github.com/lesismal/fit/tunnel/internal/bodycrypt"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
)

// Sizes of a frame.
const (
	// LengthSize is the size of a frame's length, and HeaderSize of its
	// header.
	LengthSize = 4
	HeaderSize = 6
	// MaxFrame is the most bytes of header and body a frame has, and so
	// the most a link holds of a frame that has not arrived whole.
	MaxFrame = 4 << 20
	// MaxAsync is how many calls and notifies of a link run on goroutines
	// of their own at once.
	MaxAsync = 32
)

// The kinds of frame.
const (
	kindCall   = 1
	kindReply  = 2
	kindError  = 3
	kindNotify = 4
	kindHello  = 5
	// kindSealed marks a frame whose body is sealed, or a hello.
	kindSealed = 0x80
)

// Version is the version of the link protocol a hello names: 2 was the
// first with hellos, version 1 sealing every link's frames under one key,
// and 3 names the direction, dialer to listener or back, in each key, so
// that its peers and version 2's tell each other apart at the hello.
const Version = 3

// maxBeforeHello is the most bytes a link holds of what it sent before the
// peer's hello came: a registration, not the tunnels that come after it.
const maxBeforeHello = MaxFrame

var (
	// ErrClosed is the error of a call on, or cut short by, a link that
	// closed.
	ErrClosed = errors.New("link: closed")
	// ErrTimeout is the error of a call not answered in time.
	ErrTimeout = errors.New("link: call timed out")
	// ErrBusy is the error of a call a link had no room to run.
	ErrBusy = errors.New("link: too many calls under way; try again")

	errUnsealed = errors.New("link: received an unencrypted frame; is a body password set on both sides?")
	errSealed   = errors.New("link: received an encrypted frame without a body password")
	errOld      = fmt.Errorf("link: the peer sealed a frame without a hello: it runs a fit whose link protocol is version 1, this side's is %d; upgrade it", Version)
	errHello    = errors.New("link: a hello that is not the first frame, or is malformed")
	errBacklog  = errors.New("link: too much sent before the peer's hello")
)

// tooLong is the error of a frame past MaxFrame: the link stays as it was.
type tooLong struct{ n int }

func (e *tooLong) Error() string {
	return fmt.Sprintf("link: a frame of %d bytes is longer than %d", e.n, MaxFrame)
}

// sendFailed is the error of a call whose request the link's connection
// would not send: the connection's own, which is also ErrClosed, since only
// a link that is gone fails a send and its close may not have been seen yet.
type sendFailed struct{ err error }

func (e *sendFailed) Error() string        { return e.err.Error() }
func (e *sendFailed) Unwrap() error        { return e.err }
func (e *sendFailed) Is(target error) bool { return target == ErrClosed }

// RemoteError is a failure the other side answered a call with.
type RemoteError string

func (e RemoteError) Error() string { return string(e) }

// CallFunc serves a call: it gets the call's body and returns the result,
// encoded as Encode does, or the failure.
type CallFunc func(l *Link, body []byte) (any, error)

// NotifyFunc serves a notify.
type NotifyFunc func(l *Link, body []byte)

type route struct {
	call   CallFunc
	notify NotifyFunc
	inline bool
}

// Router is what the links that share it serve.
type Router struct {
	// Guard, if set, runs before each call or notify is served: an error
	// fails the call with it, or drops the notify.
	Guard func(l *Link, m proto.Method) error
	// OnClose, if set, runs once a link closes, with why.
	OnClose func(l *Link, err error)
	// Log, if set, says why a link was dropped.
	Log *logs.Logger

	routes [256]route
}

// NewRouter is a Router that serves nothing.
func NewRouter() *Router { return &Router{} }

// HandleCall has the router serve calls of m with fn. An inline fn runs
// where the link is read, in the order the frames came, and the body is
// only valid until it returns; otherwise it runs on a goroutine of its own,
// with a body of its own.
func (r *Router) HandleCall(m proto.Method, fn CallFunc, inline bool) {
	r.routes[m] = route{call: fn, inline: inline}
}

// HandleNotify has the router serve notifies of m with fn, inline or not as
// HandleCall's.
func (r *Router) HandleNotify(m proto.Method, fn NotifyFunc, inline bool) {
	r.routes[m] = route{notify: fn, inline: inline}
}

type reply struct {
	body []byte
	err  error
}

// waiter is a call waiting for its answer, and what checks the answer
// before any frame after it is served.
type waiter struct {
	ch    chan reply
	check func(body []byte) error
}

// Then is the result of a CallFunc that has more to do once the answer is
// sent: After runs after Rsp has been sent as the answer, or failed to be,
// so that a frame the side sends from then on reaches the caller after
// the answer.
type Then struct {
	Rsp   any
	After func()
}

// Link is one connection's session: it frames what is sent, and serves or
// answers the frames that arrive. It is the connection's transport.Handler.
type Link struct {
	conn   transport.Conn
	router *Router
	coder  *bodycrypt.Coder
	// random is this side's hello's; recv is the session, once the peer's
	// hello came, as the reader sees it.
	random []byte
	recv   *bodycrypt.Session

	// smu orders sealing and sending: a frame's number is its place on
	// the wire. Until the peer's hello, what is sent waits in held.
	smu      sync.Mutex
	sess     *bodycrypt.Session
	held     [][]byte
	heldSize int

	// in is what arrived of a frame not yet whole.
	in []byte
	// maxFrame is the longest frame taken; async counts the handlers
	// running on goroutines of their own.
	maxFrame atomic.Int64
	async    atomic.Int32

	mu      sync.Mutex
	seq     uint32
	pending map[uint32]waiter
	closed  bool
	failing bool
	err     error
	value   any
	done    chan struct{}
}

// New is the link on conn, serving what router does, with bodies sealed by
// coder unless it is nil.
func New(conn transport.Conn, router *Router, coder *bodycrypt.Coder) *Link {
	l := &Link{conn: conn, router: router, coder: coder, pending: map[uint32]waiter{}, done: make(chan struct{})}
	l.maxFrame.Store(MaxFrame)
	// What waits on a link is bounded by its tunnels' windows, which may
	// be more than the engine lets a connection queue by default.
	if q, ok := conn.(transport.QueueLimiter); ok {
		q.SetQueueLimit(proto.LinkQueue)
	}
	if coder != nil {
		l.random = bodycrypt.Random()
		hello := make([]byte, LengthSize+HeaderSize+1+bodycrypt.RandomSize)
		binary.BigEndian.PutUint32(hello, uint32(len(hello)-LengthSize))
		hello[LengthSize] = kindSealed | kindHello
		hello[LengthSize+HeaderSize] = Version
		copy(hello[LengthSize+HeaderSize+1:], l.random)
		_ = conn.Send(hello)
	}
	return l
}

// SetMaxFrame sets the longest frame the link takes, at most MaxFrame: a
// hub keeps it short until a link has registered.
func (l *Link) SetMaxFrame(n int) { l.maxFrame.Store(int64(min(n, MaxFrame))) }

// Conn is the link's connection.
func (l *Link) Conn() transport.Conn { return l.conn }

// RemoteAddr is the address of the other side, or "".
func (l *Link) RemoteAddr() string {
	if a := l.conn.RemoteAddr(); a != nil {
		return a.String()
	}
	return ""
}

// Done is closed once the link is.
func (l *Link) Done() <-chan struct{} { return l.done }

// Err is why the link closed, once it has.
func (l *Link) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Close closes the link at once.
func (l *Link) Close() error { return l.conn.Close() }

// CloseAfterSend closes the link once what was sent has gone out.
func (l *Link) CloseAfterSend() { l.conn.CloseAfterSend() }

// SetValue keeps v with the link.
func (l *Link) SetValue(v any) {
	l.mu.Lock()
	l.value = v
	l.mu.Unlock()
}

// Value is what SetValue kept.
func (l *Link) Value() any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.value
}

// Encode is the body of v: nothing for nil, the bytes of a []byte or a
// string, and JSON otherwise.
func Encode(v any) ([]byte, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	}
	return json.Marshal(v)
}

// Decode stores body in v, as Encode made it: in a *[]byte a copy, in a
// *string the string, and otherwise what the JSON says. A nil v, or an
// empty body, leaves v as it is.
func Decode(body []byte, v any) error {
	switch v := v.(type) {
	case nil:
		return nil
	case *[]byte:
		*v = append([]byte(nil), body...)
		return nil
	case *string:
		*v = string(body)
		return nil
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

// frame is a frame of kind, m and id whose body is parts, one after
// another, with room for the tag when the link seals bodies: send seals it.
func (l *Link) frame(kind byte, m proto.Method, id uint32, parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	extra := 0
	if l.coder != nil {
		extra = bodycrypt.Overhead
		kind |= kindSealed
	}
	const head = LengthSize + HeaderSize
	b := make([]byte, head+extra+n)
	header := b[LengthSize:head]
	header[0] = kind
	header[1] = byte(m)
	binary.BigEndian.PutUint32(header[2:], id)
	off := head
	for _, p := range parts {
		off += copy(b[off:], p)
	}
	binary.BigEndian.PutUint32(b, uint32(len(b)-LengthSize))
	return b
}

// send sends b, a frame made by frame: sealed under the frame's number,
// once the peer's hello came, when the link seals bodies.
func (l *Link) send(b []byte) error {
	if len(b)-LengthSize > MaxFrame {
		return &tooLong{n: len(b) - LengthSize}
	}
	if l.coder == nil {
		return l.conn.Send(b)
	}
	l.smu.Lock()
	if l.sess == nil {
		if l.heldSize+len(b) > maxBeforeHello {
			l.smu.Unlock()
			l.fail(errBacklog)
			return errBacklog
		}
		l.held = append(l.held, b)
		l.heldSize += len(b)
		l.smu.Unlock()
		return nil
	}
	l.seal(b)
	err := l.conn.Send(b)
	l.smu.Unlock()
	return err
}

// seal seals the body of b, a frame made by frame, under the next number:
// with smu held.
func (l *Link) seal(b []byte) {
	const head = LengthSize + HeaderSize
	l.sess.SealInPlace(b[head:len(b)-bodycrypt.Overhead], b[LengthSize:head])
}

// hello takes the peer's hello, body, and sends what waited for it.
func (l *Link) hello(body []byte) error {
	if len(body) < 1 {
		return errHello
	}
	if body[0] != Version {
		return fmt.Errorf("link: the peer's link protocol is version %d, this side's %d; run the same fit version on both sides", body[0], Version)
	}
	if len(body) != 1+bodycrypt.RandomSize {
		return errHello
	}
	sess, err := l.coder.Session(l.random, body[1:])
	if err != nil {
		return err
	}
	l.recv = sess
	l.smu.Lock()
	defer l.smu.Unlock()
	l.sess = sess
	held := l.held
	l.held, l.heldSize = nil, 0
	for _, b := range held {
		l.seal(b)
		if err := l.conn.Send(b); err != nil {
			return err
		}
	}
	return nil
}

// Call calls m with req, encoded as Encode does, and decodes the result
// into rsp as Decode does; it fails after timeout.
func (l *Link) Call(m proto.Method, req, rsp any, timeout time.Duration) error {
	return l.CallChecked(m, req, rsp, timeout, nil)
}

// CallChecked is Call whose result check, unless nil, checks where the
// link is read, before any frame that came after it is served: an error
// fails the call with it and closes the link. check must not block, and
// body is valid only until it returns.
func (l *Link) CallChecked(m proto.Method, req, rsp any, timeout time.Duration, check func(body []byte) error) error {
	body, err := Encode(req)
	if err != nil {
		return err
	}
	ch := make(chan reply, 1)
	l.mu.Lock()
	if l.closed {
		err := l.closedErrLocked()
		l.mu.Unlock()
		return err
	}
	l.seq++
	if l.seq == 0 {
		l.seq++
	}
	id := l.seq
	l.pending[id] = waiter{ch: ch, check: check}
	l.mu.Unlock()
	if err := l.send(l.frame(kindCall, m, id, body)); err != nil {
		l.forget(id)
		if _, ok := err.(*tooLong); ok {
			return err
		}
		return &sendFailed{err}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		return Decode(r.body, rsp)
	case <-timer.C:
		l.forget(id)
		return ErrTimeout
	}
}

func (l *Link) forget(id uint32) {
	l.mu.Lock()
	delete(l.pending, id)
	l.mu.Unlock()
}

func (l *Link) closedErrLocked() error {
	if l.err != nil {
		return fmt.Errorf("%w: %v", ErrClosed, l.err)
	}
	return ErrClosed
}

// Notify notifies m with body, encoded as Encode does.
func (l *Link) Notify(m proto.Method, body any) error {
	b, err := Encode(body)
	if err != nil {
		return err
	}
	return l.send(l.frame(kindNotify, m, 0, b))
}

// SendTunnel notifies m, a tunnel method, with the frame of tunnel id and
// payload.
func (l *Link) SendTunnel(m proto.Method, id uint64, payload []byte) error {
	var head [8]byte
	binary.BigEndian.PutUint64(head[:], id)
	return l.send(l.frame(kindNotify, m, 0, head[:], payload))
}

func (l *Link) answer(m proto.Method, id uint32, rsp any, err error) {
	if err == nil {
		var body []byte
		if body, err = Encode(rsp); err == nil {
			_ = l.send(l.frame(kindReply, m, id, body))
			return
		}
	}
	_ = l.send(l.frame(kindError, m, id, []byte(err.Error())))
}

// answerThen answers call id with what a CallFunc returned, and runs its
// After once the answer is sent.
func (l *Link) answerThen(m proto.Method, id uint32, rsp any, err error) {
	t, ok := rsp.(*Then)
	if !ok {
		l.answer(m, id, rsp, err)
		return
	}
	l.answer(m, id, t.Rsp, err)
	if t.After != nil {
		t.After()
	}
}

// OnData parses the frames that arrived and serves them.
func (l *Link) OnData(data []byte) {
	l.mu.Lock()
	failing := l.failing
	l.mu.Unlock()
	if failing {
		return
	}
	aliased := false
	if len(l.in) > 0 {
		l.in = append(l.in, data...)
		data, aliased = l.in, true
	}
	limit := l.maxFrame.Load()
	for len(data) >= LengthSize {
		n := binary.BigEndian.Uint32(data)
		if n < HeaderSize || int64(n) > limit {
			l.fail(fmt.Errorf("link: a frame of %d bytes", n))
			return
		}
		if len(data) < LengthSize+int(n) {
			break
		}
		if !l.handle(data[LengthSize : LengthSize+int(n)]) {
			return
		}
		data = data[LengthSize+int(n):]
	}
	switch {
	case len(data) == 0:
		if cap(l.in) > 1<<20 {
			l.in = nil
		} else {
			l.in = l.in[:0]
		}
	case aliased:
		l.in = l.in[:copy(l.in, data)]
	default:
		l.in = append(l.in[:0], data...)
	}
}

// handle serves frame f, reporting whether the link goes on.
func (l *Link) handle(f []byte) bool {
	header, body := f[:HeaderSize], f[HeaderSize:]
	kind := header[0]
	switch {
	case l.coder != nil && kind&kindSealed == 0:
		l.fail(errUnsealed)
		return false
	case l.coder != nil && l.recv == nil:
		err := errOld
		if kind == kindSealed|kindHello {
			err = l.hello(body)
		}
		if err != nil {
			l.fail(err)
			return false
		}
		return true
	case l.coder != nil:
		if kind == kindSealed|kindHello {
			l.fail(errHello)
			return false
		}
		plain, err := l.recv.OpenInPlace(body, header)
		if err != nil {
			l.fail(err)
			return false
		}
		body = plain
		kind &^= kindSealed
	case kind&kindSealed != 0:
		l.fail(errSealed)
		return false
	}
	m := proto.Method(header[1])
	id := binary.BigEndian.Uint32(header[2:])
	switch kind {
	case kindReply, kindError:
		l.mu.Lock()
		w, ok := l.pending[id]
		delete(l.pending, id)
		l.mu.Unlock()
		if ok {
			r := reply{body: append([]byte(nil), body...)}
			if kind == kindError {
				r = reply{err: RemoteError(body)}
			} else if w.check != nil {
				if err := w.check(body); err != nil {
					w.ch <- reply{err: err}
					l.fail(err)
					return false
				}
			}
			w.ch <- r
		}
	case kindCall:
		l.serveCall(m, id, body)
	case kindNotify:
		l.serveNotify(m, body)
	default:
		l.fail(fmt.Errorf("link: a frame of kind %d", kind))
		return false
	}
	return true
}

func (l *Link) serveCall(m proto.Method, id uint32, body []byte) {
	if g := l.router.Guard; g != nil {
		if err := g(l, m); err != nil {
			l.answer(m, id, nil, err)
			return
		}
	}
	rt := l.router.routes[m]
	if rt.call == nil {
		l.answer(m, id, nil, fmt.Errorf("link: no method %v", m))
		return
	}
	if rt.inline {
		rsp, err := rt.call(l, body)
		l.answerThen(m, id, rsp, err)
		return
	}
	if !l.goAsync() {
		l.answer(m, id, nil, ErrBusy)
		return
	}
	body = append([]byte(nil), body...)
	go func() {
		defer l.async.Add(-1)
		rsp, err := rt.call(l, body)
		l.answerThen(m, id, rsp, err)
	}()
}

// goAsync takes a place for a handler on a goroutine of its own, reporting
// false when the link has none left.
func (l *Link) goAsync() bool {
	if l.async.Add(1) > MaxAsync {
		l.async.Add(-1)
		return false
	}
	return true
}

func (l *Link) serveNotify(m proto.Method, body []byte) {
	if g := l.router.Guard; g != nil {
		if err := g(l, m); err != nil {
			return
		}
	}
	rt := l.router.routes[m]
	if rt.notify == nil {
		return
	}
	if rt.inline {
		rt.notify(l, body)
		return
	}
	if !l.goAsync() {
		return
	}
	body = append([]byte(nil), body...)
	go func() {
		defer l.async.Add(-1)
		rt.notify(l, body)
	}()
}

// fail closes the link for err, a breach of the protocol.
func (l *Link) fail(err error) {
	l.mu.Lock()
	if l.failing || l.closed {
		l.mu.Unlock()
		return
	}
	l.failing = true
	l.err = err
	l.mu.Unlock()
	l.router.Log.Warnf("dropped a link from %s: %v", l.RemoteAddr(), err)
	_ = l.conn.Close()
}

// OnClose ends the calls waiting on the link and tells the router.
func (l *Link) OnClose(err error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	if l.err == nil {
		l.err = err
	}
	err = l.err
	pending := l.pending
	l.pending = map[uint32]waiter{}
	cerr := l.closedErrLocked()
	l.mu.Unlock()
	for _, w := range pending {
		w.ch <- reply{err: cerr}
	}
	close(l.done)
	if l.router.OnClose != nil {
		l.router.OnClose(l, err)
	}
}
