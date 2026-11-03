package wire

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/websocket"
)

// Transport defaults.
// 传输层默认值。
const (
	DefaultPingInterval = 10 * time.Second
	DefaultSlowTimeout  = 30 * time.Second
	HandshakeTimeout    = 5 * time.Second

	challengeSize = 32
	ackOK         = 0x01

	closeAuthFailed = 4001 // the listener rejected the proof / 监听方拒绝了证明
	closeHeartbeat  = 4002 // no frame for three ping intervals / 三个心跳周期没有收到任何帧
	closeRefused    = 4003 // the listener is full or shutting down / 监听方已满或正在下线
	closeSlow       = 4004 // a ping went unanswered for SlowTimeout / ping 在 SlowTimeout 内未得到应答
)

var (
	// ErrHeartbeat closes a connection whose peer sent nothing for three ping intervals.
	// ErrHeartbeat 表示对端三个心跳周期没有发送任何帧，连接被关闭。
	ErrHeartbeat = errors.New("wire: heartbeat timeout")
	// ErrSlow closes a connection so far behind that a ping waited SlowTimeout for its pong.
	// ErrSlow 表示连接积压严重，ping 等待 pong 超过 SlowTimeout，连接被关闭。
	ErrSlow = errors.New("wire: connection too slow")
	// ErrRefused fails a dial the listener turned away before authentication (too many connections, shutting down).
	// ErrRefused 表示监听方在认证之前拒绝了拨号（连接数已满、正在下线）。
	ErrRefused = errors.New("wire: connection refused by the listener")
)

// ConnConfig is shared by both ends of a connection.
// ConnConfig 连接两端通用的配置。
type ConnConfig struct {
	// Session is the template for every connection's session; Dialer and Handlers are set per connection.
	// Session 是每条连接会话的模板；Dialer 与 Handlers 按连接设置。
	Session Options
	// Auth runs the post-upgrade handshake; nil accepts / proves nothing.
	// Auth 执行升级之后的握手；为 nil 时不做认证。
	Auth Authenticator
	// PingInterval: send a ping after this long without receiving a frame, close after three
	// (0: DefaultPingInterval, < 0: off).
	// PingInterval 这么久没收到帧就发 ping，三个周期没收到就关闭（0：DefaultPingInterval，< 0：关闭心跳）。
	PingInterval time.Duration
	// SlowTimeout closes a connection whose ping has waited this long for its pong: everything sent before the ping,
	// in both directions, must drain first, so a peer that cannot keep up, or whose reads the fib write watermark has
	// paused on this side, gets dropped instead of piling up (0: DefaultSlowTimeout, < 0: off). Checked once per
	// ping interval.
	// SlowTimeout ping 等待 pong 超过这么久即关闭连接：两个方向上在 ping 之前发送的所有内容都要先排空，因此跟不上的对端、
	// 或本端因 fib 写水位而暂停读取的连接会被断开，而不是不断积压（0：DefaultSlowTimeout，< 0：关闭）。每个心跳周期检查一次。
	SlowTimeout time.Duration
	// MaxConns caps the connections a listener holds at once, handshakes included (≤ 0: no limit); one more is
	// closed with ErrRefused at the dialer.
	// MaxConns 监听方同时持有的连接数上限，含握手中的连接（≤ 0：不限制）；超出的连接被关闭，拨号方得到 ErrRefused。
	MaxConns int
	// Accept, if set, vets each new connection on the listener before the handshake; an error closes it with
	// ErrRefused at the dialer (e.g. while shutting down).
	// Accept 如已设置，在握手之前审查监听方的每条新连接；返回错误则关闭连接，拨号方得到 ErrRefused（例如正在下线时）。
	Accept func(*http.Request) error
	// Handlers builds the handlers of a new connection (called before any frame of it is handled; Request is
	// already set).
	// Handlers 为新连接构造处理函数（在处理该连接的任何帧之前调用；此时 Request 已设置）。
	Handlers func(*Conn) Handlers
	// OnAuthFailed (optional) runs on the listener when a dialer's proof is rejected, before the connection is closed.
	// OnAuthFailed（可选）在监听方拒绝拨号方的证明时、关闭连接之前执行。
	OnAuthFailed func(r *http.Request)
	// OnOpen runs on the listener once a connection is authenticated. Dialers get the Conn from Dial.
	// OnOpen 在监听方的连接认证通过后执行。拨号方从 Dial 的返回值得到 Conn。
	OnOpen func(*Conn)
	// OnClose runs when an authenticated connection closes, on either end.
	// OnClose 在已认证的连接关闭时执行，两端都会调用。
	OnClose func(*Conn, error)
}

func (cfg *ConnConfig) pingInterval() time.Duration {
	if cfg.PingInterval == 0 {
		return DefaultPingInterval
	}
	return cfg.PingInterval
}

func (cfg *ConnConfig) slowTimeout() time.Duration {
	if cfg.SlowTimeout == 0 {
		return DefaultSlowTimeout
	}
	return cfg.SlowTimeout
}

func (cfg *ConnConfig) maxFrame() int {
	if cfg.Session.MaxFrame > 0 {
		return cfg.Session.MaxFrame
	}
	return DefaultMaxFrame
}

// Conn is an authenticated WebSocket connection carrying a Session.
// Conn 是承载一个 Session 的已认证 WebSocket 连接。
type Conn struct {
	*Session
	// Request is the upgrade request (listener side).
	// Request 是升级请求（监听方）。
	Request *http.Request

	ws  *websocket.Connection
	cfg *ConnConfig
	// abort drops the TCP connection without the close handshake (listener side; nil on the dialing side, see
	// forceClose).
	// abort 跳过关闭握手直接断开 TCP 连接（监听方；拨号方为 nil，见 forceClose）。
	abort     func()
	hb        *time.Timer
	lastBeat  time.Time
	closeOnce sync.Once
}

func newConn(ws *websocket.Connection, cfg *ConnConfig, dialer bool, req *http.Request) *Conn {
	c := &Conn{ws: ws, cfg: cfg, Request: req}
	opts := cfg.Session
	opts.Dialer = dialer
	if cfg.Handlers != nil {
		opts.Handlers = cfg.Handlers(c)
	}
	c.Session = NewSession(opts, ws.WriteBinary)
	return c
}

// RemoteAddr returns the peer address of an accepted connection, or "" on the dialing side.
// RemoteAddr 返回监听方连接的对端地址；拨号方为 ""。
func (c *Conn) RemoteAddr() string {
	if c.Request != nil {
		return c.Request.RemoteAddr
	}
	return ""
}

// Close closes the connection and its session.
// Close 关闭连接及其会话。
func (c *Conn) Close() { c.closeWith(websocket.CloseNormal, "", ErrClosed) }

// The session closes first, with err: once the WebSocket is closed, its
// OnClose may run on a worker at once and close the session with the
// transport's error, which OnClose would then report instead of err.
// 先以 err 关闭会话：WebSocket 一关闭，其 OnClose 可能立即在 worker 上运行，用传输层的错误关闭会话，OnClose 报告的就不是 err 了。
func (c *Conn) closeWith(code uint16, reason string, err error) {
	c.Session.Close(err)
	_ = c.ws.Close(code, reason)
}

// forceClose closes a connection whose peer is dead or too slow. The close frame queues behind whatever is still
// unsent, so on the listener side the TCP connection is dropped at once (abort). The dialing side has no handle on
// its TCP connection (fib's websocket.Connection does not expose it): it can only send the close frame, and its
// socket lingers until the queue drains or the kernel gives up; the session closes right away either way.
//
// forceClose 关闭对端已死或过慢的连接。关闭帧排在所有未发送的内容之后，所以监听方立即断开 TCP 连接（abort）。拨号方拿不到
// 其 TCP 连接（fib 的 websocket.Connection 没有暴露）：只能发送关闭帧，套接字要等队列排空或内核放弃才释放；无论哪种情况，
// 会话都会立即关闭。
func (c *Conn) forceClose(code uint16, reason string, err error) {
	c.closeWith(code, reason, err)
	if c.abort != nil {
		c.abort()
	}
}

func (c *Conn) receive(data []byte) {
	// fib reuses data after the callback; the session keeps payloads, so copy.
	// fib 在回调返回后会复用 data，而会话会保留载荷，所以先复制。
	if err := c.Session.Receive(bytes.Clone(data)); err != nil {
		_ = c.ws.Close(websocket.CloseProtocolError, "protocol error")
	}
}

// start arms the heartbeat. A timer per connection keeps idle connections free of goroutines.
// start 启动心跳。每条连接一个定时器，空闲连接不占协程。
func (c *Conn) start() {
	d := c.cfg.pingInterval()
	if d <= 0 {
		return
	}
	c.lastBeat = time.Now()
	c.hb = time.AfterFunc(d, func() { c.beat(d) })
}

// beat runs every ping interval. A ping goes out when nothing arrived for an interval (is the peer alive?), when
// something was sent since the last beat (is the peer keeping up?) or when the latency is two intervals old, unless
// one is still unanswered. No frame for
// three intervals, or a ping unanswered for SlowTimeout, drops the connection.
//
// beat 每个心跳周期执行一次。一个周期内没有收到任何帧（对端是否存活？）或上次以来发送过数据（对端是否跟得上？）时发送 ping，
// 已有未应答的 ping 时不再发送。三个周期没有收到任何帧，或 ping 在 SlowTimeout 内未得到应答，即断开连接。
func (c *Conn) beat(d time.Duration) {
	select {
	case <-c.Done():
		return
	default:
	}
	now := time.Now()
	idle := now.Sub(c.LastRecv())
	if idle >= 3*d {
		c.forceClose(closeHeartbeat, "heartbeat timeout", ErrHeartbeat)
		return
	}
	waited := c.Unanswered()
	if slow := c.cfg.slowTimeout(); slow > 0 && waited >= slow {
		c.forceClose(closeSlow, "too slow", ErrSlow)
		return
	}
	// A ping also goes out when the last round trip is two intervals old,
	// so that both ends of a link that only one of them keeps busy know its
	// latency.
	if waited == 0 && (idle >= d || c.LastSend().After(c.lastBeat) || now.Sub(c.rtt.last()) >= 2*d) {
		_ = c.Ping()
	}
	c.lastBeat = now
	c.hb.Reset(d)
}

// closed runs once when the WebSocket connection is gone.
// closed 在 WebSocket 连接断开时执行一次。
func (c *Conn) closed(err error) {
	c.closeOnce.Do(func() {
		if c.hb != nil {
			c.hb.Stop()
		}
		if err == nil {
			err = ErrClosed
		}
		c.Session.Close(err)
		if c.cfg.OnClose != nil {
			c.cfg.OnClose(c, c.Session.Err())
		}
	})
}

// ---- listener ----

type server struct {
	cfg   ConnConfig
	tcp   *tcpConns
	conns sync.Map // *websocket.Connection → *serverConn
	count atomic.Int64
}

type serverConn struct {
	challenge []byte
	req       *http.Request
	tcp       *fib.Connection
	timer     *time.Timer
	mu        sync.Mutex
	conn      *Conn // set once authenticated / 认证通过后设置
}

// tcpConns wraps the WebSocket handler to learn the fib connection under each WebSocket connection, so that a
// dead or slow peer's socket can be dropped without the close handshake. fib's websocket.Connection does not expose
// it; the upgrade request's RemoteAddr is the fib connection's remote address, which is unique among the
// listener's open connections, so it is the key.
//
// tcpConns 包装 WebSocket 处理器，记录每条 WebSocket 连接下的 fib 连接，以便跳过关闭握手直接断开已死或过慢的对端。
// fib 的 websocket.Connection 没有暴露它；升级请求的 RemoteAddr 就是 fib 连接的对端地址，在监听方的已打开连接中唯一，
// 因此用作键。
type tcpConns struct {
	fib.Handler
	byAddr sync.Map // remote address → *fib.Connection / 对端地址 → *fib.Connection
	closed atomic.Bool
}

func (t *tcpConns) OnOpen(c *fib.Connection) {
	if t.closed.Load() {
		_ = c.Close()
		return
	}
	if a := c.RemoteAddr(); a != nil {
		t.byAddr.Store(a.String(), c)
	}
	t.Handler.OnOpen(c)
}

func (t *tcpConns) OnClose(c *fib.Connection, err error) {
	if a := c.RemoteAddr(); a != nil {
		t.byAddr.CompareAndDelete(a.String(), c)
	}
	t.Handler.OnClose(c, err)
}

func (t *tcpConns) OnShutdown(c *fib.Connection) {
	if h, ok := t.Handler.(fib.ShutdownHandler); ok {
		h.OnShutdown(c)
	}
}

func (t *tcpConns) OnEngineClose(c *fib.Connection) {
	if h, ok := t.Handler.(fib.EngineCloseHandler); ok {
		h.OnEngineClose(c)
	}
}

func (t *tcpConns) lookup(addr string) *fib.Connection {
	if v, ok := t.byAddr.Load(addr); ok {
		return v.(*fib.Connection)
	}
	return nil
}

// Listener is the handler NewHandler returns: a binding's handler that
// takes WebSocket links on Path.
type Listener struct{ *tcpConns }

// CloseAll closes every connection the listener took, authenticated or
// not, and every one it takes from now on, once its binding is removed:
// a removed fib binding leaves its connections open.
func (l Listener) CloseAll() {
	l.closed.Store(true)
	l.byAddr.Range(func(_, v any) bool {
		_ = v.(*fib.Connection).Close()
		return true
	})
}

// Path is the only path a listener upgrades; any other is refused with 403.
const Path = "/ws"

// NewHandler returns the handler of a listener's fib binding: it upgrades
// WebSocket connections on Path (any other path is refused with 403),
// authenticates them and runs a session on each.
func NewHandler(cfg ConnConfig) Listener {
	s := &server{cfg: cfg}
	wsCfg := websocket.DefaultConfig()
	wsCfg.MaxMessageBytes = int64(cfg.maxFrame())
	wsCfg.CheckOrigin = func(r *http.Request) bool { return r.URL.Path == Path }
	wsCfg.HTTP.MaxHeaderBytes = 16 << 10
	wsCfg.HTTP.ReadHeaderTimeout = HandshakeTimeout
	s.tcp = &tcpConns{Handler: websocket.NewHandlerWithConfig(wsCfg, websocket.HandlerFuncs{
		Open:    s.open,
		Message: s.message,
		Close:   s.close,
	})}
	return Listener{s.tcp}
}

func (s *server) open(ws *websocket.Connection, r *http.Request) {
	var refuse error
	if s.cfg.Accept != nil {
		refuse = s.cfg.Accept(r)
	}
	if refuse == nil && s.cfg.MaxConns > 0 && s.count.Load() >= int64(s.cfg.MaxConns) {
		refuse = errors.New("too many connections")
	}
	if refuse != nil {
		_ = ws.Close(closeRefused, refuse.Error())
		return
	}
	s.count.Add(1)
	sc := &serverConn{challenge: make([]byte, challengeSize), req: r, tcp: s.tcp.lookup(r.RemoteAddr)}
	_, _ = rand.Read(sc.challenge)
	s.conns.Store(ws, sc)
	sc.timer = time.AfterFunc(HandshakeTimeout, func() {
		sc.mu.Lock()
		authed := sc.conn != nil
		sc.mu.Unlock()
		if !authed {
			_ = ws.Close(closeAuthFailed, "handshake timeout")
		}
	})
	if err := ws.WriteBinary(sc.challenge); err != nil {
		_ = ws.Close(websocket.CloseInternalError, "")
	}
}

func (s *server) message(ws *websocket.Connection, _ websocket.Opcode, data []byte) {
	v, ok := s.conns.Load(ws)
	if !ok {
		return
	}
	sc := v.(*serverConn)
	sc.mu.Lock()
	c := sc.conn
	sc.mu.Unlock()
	if c != nil {
		c.receive(data)
		return
	}

	var err error
	if s.cfg.Auth != nil {
		err = s.cfg.Auth.Verify(sc.challenge, data)
	}
	if err != nil {
		if s.cfg.OnAuthFailed != nil {
			s.cfg.OnAuthFailed(sc.req)
		}
		_ = ws.Close(closeAuthFailed, "authentication failed")
		return
	}
	sc.timer.Stop()
	c = newConn(ws, &s.cfg, false, sc.req)
	if tcp := sc.tcp; tcp != nil {
		c.abort = func() { _ = tcp.Close() }
	}
	sc.mu.Lock()
	sc.conn = c
	sc.mu.Unlock()
	// The session exists before the ack goes out, so whatever the dialer sends after it finds a session.
	// 确认发出之前会话已建立，拨号方随后发送的任何帧都有会话处理。
	if err := ws.WriteBinary([]byte{ackOK}); err != nil {
		_ = ws.Close(websocket.CloseInternalError, "")
		return
	}
	c.start()
	if s.cfg.OnOpen != nil {
		s.cfg.OnOpen(c)
	}
}

func (s *server) close(ws *websocket.Connection, _ uint16, _ string, err error) {
	v, ok := s.conns.LoadAndDelete(ws)
	if !ok {
		return
	}
	s.count.Add(-1)
	sc := v.(*serverConn)
	sc.timer.Stop()
	sc.mu.Lock()
	c := sc.conn
	sc.mu.Unlock()
	if c != nil {
		c.closed(err)
	}
}

// ---- dialer ----

// DialConfig configures Dial.
// DialConfig 配置 Dial。
type DialConfig struct {
	ConnConfig
	// Header is added to the upgrade request.
	// Header 附加到升级请求上。
	Header http.Header
	// Engine is the fib engine the link is dialed on and served by.
	Engine *fib.Engine
}

type dialResult struct {
	conn *Conn
	err  error
}

// Dial connects to a ws:// URL and completes the authentication handshake. ctx bounds the whole dial.
// Dial 连接 ws:// 地址并完成认证握手。ctx 限定整个拨号过程。
func Dial(ctx context.Context, url string, cfg DialConfig) (*Conn, error) {
	engine := cfg.Engine
	if engine == nil {
		return nil, errors.New("wire: no engine to dial on")
	}
	timeout := HandshakeTimeout
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	wsCfg := websocket.DefaultDialerConfig()
	wsCfg.HandshakeTimeout = timeout
	wsCfg.MaxMessageBytes = int64(cfg.maxFrame())
	// The fib connection under the link, so that a dead or slow peer's
	// socket can be dropped without the close handshake.
	var tcp atomic.Pointer[fib.Connection]
	wsCfg.Dial = func(e *fib.Engine, network, addr string, timeout time.Duration, h fib.Handler, done func(*fib.Connection, error)) error {
		return e.DialWithHandler(network, addr, timeout, h, func(c *fib.Connection, err error) {
			if c != nil {
				tcp.Store(c)
			}
			done(c, err)
		})
	}
	dialer := websocket.NewDialer(engine, wsCfg)

	res := make(chan dialResult, 1)
	settle := func(r dialResult) bool {
		select {
		case res <- r:
			return true
		default:
			return false
		}
	}
	var (
		mu      sync.Mutex
		ws      *websocket.Connection
		conn    *Conn
		proved  bool
		aborted bool
	)
	handler := websocket.HandlerFuncs{
		Message: func(w *websocket.Connection, _ websocket.Opcode, data []byte) {
			mu.Lock()
			ws = w
			c, done, ab := conn, proved, aborted
			mu.Unlock()
			switch {
			case ab:
				_ = w.Close(websocket.CloseGoingAway, "")
			case c != nil:
				c.receive(data)
			case !done: // the challenge / 挑战
				var proof []byte
				var err error
				if cfg.Auth != nil {
					proof, err = cfg.Auth.Prove(data)
				}
				if err == nil {
					err = w.WriteBinary(proof)
				}
				if err != nil {
					settle(dialResult{err: err})
					_ = w.Close(websocket.CloseGoingAway, "")
					return
				}
				mu.Lock()
				proved = true
				mu.Unlock()
			default: // the ack / 确认
				if len(data) != 1 || data[0] != ackOK {
					settle(dialResult{err: fmt.Errorf("%w: bad handshake ack", ErrProtocol)})
					_ = w.Close(websocket.CloseProtocolError, "")
					return
				}
				c = newConn(w, &cfg.ConnConfig, true, nil)
				if t := tcp.Load(); t != nil {
					c.abort = func() { _ = t.Close() }
				}
				mu.Lock()
				conn = c
				mu.Unlock()
				c.start()
				if !settle(dialResult{conn: c}) {
					c.Close() // the caller gave up / 调用方已放弃
				}
			}
		},
		Close: func(_ *websocket.Connection, code uint16, reason string, err error) {
			mu.Lock()
			c := conn
			mu.Unlock()
			if c != nil {
				c.closed(err)
				return
			}
			if code == closeAuthFailed {
				err = fmt.Errorf("%w: %s", ErrAuth, reason)
			} else if code == closeRefused {
				err = fmt.Errorf("%w: %s", ErrRefused, reason)
			} else if err == nil {
				err = fmt.Errorf("wire: connection closed during handshake (code %d)", code)
			}
			settle(dialResult{err: err})
		},
	}
	dialer.Dial(url, cfg.Header, handler, func(_ *websocket.Connection, _ *http.Response, err error) {
		if err != nil {
			settle(dialResult{err: err})
		}
	})

	select {
	case r := <-res:
		return r.conn, r.err
	case <-ctx.Done():
		mu.Lock()
		aborted = true
		w, c := ws, conn
		mu.Unlock()
		if !settle(dialResult{err: ctx.Err()}) {
			r := <-res
			if r.conn != nil {
				return r.conn, nil
			}
			return nil, r.err
		}
		if c != nil {
			c.Close()
		} else if w != nil {
			_ = w.Close(websocket.CloseGoingAway, "")
		}
		return nil, ctx.Err()
	}
}
