package wire

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	fibtls "github.com/lesismal/fib/tls"
	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/internal/linktoken"
)

// Transport defaults.
// 传输层默认值。
const (
	DefaultPingInterval = 10 * time.Second
	DefaultSlowTimeout  = 30 * time.Second
	HandshakeTimeout    = 5 * time.Second
	// DefaultMaxPending and DefaultMaxPendingPerIP bound the connections a
	// listener holds that have not authenticated yet: in all, and from one
	// IP address.
	// DefaultMaxPending 与 DefaultMaxPendingPerIP 限制监听方持有的尚未认证的连接：总数，以及来自同一 IP 地址的数量。
	DefaultMaxPending      = 256
	DefaultMaxPendingPerIP = 16

	challengeSize = 32
	// proofSize is the size of a Password's proof.
	// proofSize 是 Password 证明的长度。
	proofSize = sha256.Size
	ackOK     = 0x01

	closeAuthFailed = 4001 // the listener rejected the proof / 监听方拒绝了证明
	closeRefused    = 4003 // the listener is full or shutting down / 监听方已满或正在下线
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
	// Credentials, on a listener, are the passwords it takes in place of Auth: a dialer's proof is checked against
	// each, the connection's Roles are those of the one it proves, and the listener proves that same password back
	// (see Mutual). A proof of none of them fails the handshake.
	// Credentials 用于监听方，代替 Auth 作为其接受的密码：拨号方的证明逐一与之校验，连接的 Roles 为被证明的那个凭据的
	// 角色，监听方用同一密码回证（见 Mutual）。证明不了任何一个则握手失败。
	Credentials []Credential
	// Tokens, on a listener, are the link tokens it issued, which a dialer may prove in place of a password; the
	// connection's Roles are then TokenRoles and its Token the token's ID. A listener with Tokens takes no password
	// but its Credentials'.
	// Tokens 用于监听方，是其签发的连接令牌，拨号方可用以代替密码证明；此时连接的 Roles 为 TokenRoles，Token 为令牌 ID。
	// 设置了 Tokens 的监听方只接受其 Credentials 中的密码。
	Tokens     Tokens
	TokenRoles []string
	// Token, on a dialer, is the link token it proves in place of Auth, and checks the listener proves back.
	// Token 用于拨号方，是其代替 Auth 证明的连接令牌，并校验监听方的回证。
	Token *linktoken.Secret
	// PingInterval: send a ping after this long without receiving a frame, close after three
	// (0: DefaultPingInterval, < 0: off).
	// PingInterval 这么久没收到帧就发 ping，三个周期没收到就关闭（0：DefaultPingInterval，< 0：关闭心跳）。
	PingInterval time.Duration
	// SlowTimeout closes a connection whose ping has waited this long for its pong: everything sent before the ping,
	// in both directions, must drain first, so a peer that cannot keep up, or whose reads the fib write watermark has
	// paused on this side, gets dropped instead of piling up (0: DefaultSlowTimeout, < 0: off). Checked once per
	// ping interval. It is the last resort: a link whose send queue passes its limit holds its streams, and for a
	// while its reads, long before (see Conn.send).
	// SlowTimeout ping 等待 pong 超过这么久即关闭连接：两个方向上在 ping 之前发送的所有内容都要先排空，因此跟不上的对端、
	// 或本端因 fib 写水位而暂停读取的连接会被断开，而不是不断积压（0：DefaultSlowTimeout，< 0：关闭）。每个心跳周期检查一次。
	// 它是最后的手段：发送队列超过上限的连接早在此之前就停止了其流，并暂停读取一段时间（见 Conn.send）。
	SlowTimeout time.Duration
	// MaxConns caps the connections a listener holds at once, handshakes included (≤ 0: no limit); one more is
	// closed with ErrRefused at the dialer.
	// MaxConns 监听方同时持有的连接数上限，含握手中的连接（≤ 0：不限制）；超出的连接被关闭，拨号方得到 ErrRefused。
	MaxConns int
	// MaxPending caps the connections a listener holds that have not authenticated yet, from their TCP connect to
	// their proof, and MaxPendingPerIP those of one IP address (≤ 0: DefaultMaxPending, DefaultMaxPendingPerIP).
	// Anyone who reaches the listener can open them; one more is closed as it opens, so that they cannot hold
	// every place MaxConns leaves for the peers that know the password.
	// MaxPending 监听方持有的尚未认证的连接（从 TCP 连接建立到证明通过）的上限，MaxPendingPerIP 为同一 IP 地址的上限
	// （≤ 0：DefaultMaxPending、DefaultMaxPendingPerIP）。任何能连到监听方的人都能打开这类连接；超出的连接在打开时即被关闭，
	// 使它们无法占满 MaxConns 留给知道密码的对端的位置。
	MaxPending, MaxPendingPerIP int
	// TLS, on a listener, is the configuration it takes links over TLS (wss://) with, nil for plain ws://; on a
	// dialer, the one a wss:// URL is dialed with, which must check the listener's certificate (a pin, or the
	// authorities with the name) and must not resume a session, which would skip the check. The handshake runs
	// inside TLS either way.
	// TLS 在监听方是以 TLS（wss://）接受连接所用的配置，nil 表示普通 ws://；在拨号方是拨号 wss:// 地址所用的配置，
	// 它必须校验监听方的证书（固定指纹，或证书颁发机构与名称），且不得恢复会话（恢复会跳过校验）。两种情况下握手都在 TLS 之内进行。
	TLS *tls.Config
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

// maxHold bounds how long a link whose send queue is above its limit holds its reads: a quarter of the shorter of
// the ping interval and the slow timeout (2.5 s by default), or of DefaultPingInterval with both off. See Conn.send.
// maxHold 限制发送队列超过上限的连接暂停读取的时长：心跳周期与慢连接超时中较短者的四分之一（默认 2.5 秒），两者都关闭时为
// DefaultPingInterval 的四分之一。见 Conn.send。
func (cfg *ConnConfig) maxHold() time.Duration {
	d := cfg.pingInterval()
	if s := cfg.slowTimeout(); s > 0 && (d <= 0 || s < d) {
		d = s
	}
	if d <= 0 {
		d = DefaultPingInterval
	}
	return d / 4
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
	// Roles are the roles of the credential the dialer proved (listener side, with ConnConfig.Credentials; nil
	// otherwise). Set before Handlers is called.
	// Roles 是拨号方所证明凭据的角色（监听方，使用 ConnConfig.Credentials 时；否则为 nil）。在调用 Handlers 之前设置。
	Roles []string
	// Token is the ID of the link token the dialer proved (listener side), "" for a password.
	// Token 是拨号方所证明的连接令牌 ID（监听方），密码则为 ""。
	Token string

	ws *websocket.Connection
	// remote is the peer's address, kept as the connection opens: the socket's may be gone once it closes.
	// remote 是对端地址，在连接建立时记下：连接关闭后套接字的地址可能已取不到。
	remote    string
	cfg       *ConnConfig
	hb        *time.Timer
	lastBeat  time.Time
	closeOnce sync.Once

	// The send queue's episodes above its limit (see send): above while one lasts, gen counting them, and the timer
	// that ends its hold on reads.
	// 发送队列超过上限的时段（见 send）：above 表示正处于其中，gen 为其计数，hold 为结束读取暂停的定时器。
	flowMu sync.Mutex
	above  bool
	gen    uint64
	hold   *time.Timer
}

func newConn(ws *websocket.Connection, cfg *ConnConfig, dialer bool, req *http.Request, roles []string, token string) *Conn {
	c := &Conn{ws: ws, cfg: cfg, Request: req, Roles: roles, Token: token}
	if a := ws.RemoteAddr(); a != nil {
		c.remote = a.String()
	}
	opts := cfg.Session
	opts.Dialer = dialer
	if cfg.Handlers != nil {
		opts.Handlers = cfg.Handlers(c)
	}
	c.Session = NewSession(opts, c.send)
	return c
}

// RemoteAddr returns the peer's address, on either side, as it was when the connection opened.
// RemoteAddr 返回对端地址（两端皆可），即连接建立时的地址。
func (c *Conn) RemoteAddr() string { return c.remote }

// send writes a frame without waiting. When more than the WebSocket's PendingWriteLimit (fib's default, 64 KiB) then
// waits for the peer, the link holds back what feeds it until the queue falls to half the limit (OnWritable):
//
//   - its streams stop sending data (Session.holdStreams), as if the peer granted no credit, so that the producers
//     writing to them wait, and a Pump into one of them stops releasing, and so granting credit to, the stream it
//     reads;
//   - its reads are held, so that the peer's new calls, streams and pushes, whose replies would queue here too, wait
//     in TCP's flow control. Holding reads leaves the peer's pings unanswered, and the peer may hold its own reads
//     because this end reads too little: the two ends of a link, each holding the other, would wait for each other
//     until their slow timeouts. So a hold lasts at most maxHold, a quarter of the ping interval or the slow timeout,
//     whichever is shorter, once per episode: well within the peer's slow timeout, and then the link reads again
//     while its streams stay held. A peer slow enough for its own ping to wait the slow timeout is still dropped by
//     it, the last resort.
//
// On the listener's side fib's write watermark, on for a connection a listener accepts, also pauses reads while
// more than it waits, for as long as that lasts; it is left on, as it bounds what a peer that sends and never reads
// can queue here. It cannot hold both ends at once with no end: the dialer's own hold ends after maxHold, and its
// reads then drain the listener's queue.
//
// Calls, replies, pushes and control frames are not held: they are bounded by the calls the peer may have running
// (MaxInbound) and by the reads held.
//
// send 不等待地写入一帧。若此后等待发往对端的数据超过 WebSocket 的 PendingWriteLimit（fib 默认 64 KiB），连接就阻住向其
// 写入的一方，直到队列降到上限的一半（OnWritable）：
//
//   - 其各个流停止发送数据（Session.holdStreams），如同对端没有授予额度，使向这些流写入的生产者等待，向其中某个流转发的
//     Pump 也不再释放、因而不再向其读取的流授予额度；
//   - 暂停读取，使对端新的调用、流与推送（其应答也会排在这里）在 TCP 流控中等待。暂停读取会让对端的 ping 得不到应答；对端
//     也可能因本端读得太少而暂停读取：连接的两端互相暂停，会彼此等待，直到慢连接超时。因此每个时段内暂停至多 maxHold，即
//     心跳周期与慢连接超时中较短者的四分之一，远小于对端的慢连接超时；此后连接恢复读取，而其流仍保持停止。慢到连自己的 ping
//     都要等满慢连接超时的对端仍由它断开，这是最后的手段。
//
// 在监听方，fib 的写水位（对监听方接受的连接默认开启）也会在等待的数据超过水位时暂停读取，直到降下来为止；它保持开启，因为
// 它限制了只发不读的对端能在这里排队的数据量。它不会让两端无休止地同时暂停：拨号方自己的暂停在 maxHold 后结束，随后其读取
// 会排空监听方的队列。
//
// 调用、应答、推送与控制帧不受阻：它们受对端可同时运行的调用数（MaxInbound）与暂停的读取约束。
func (c *Conn) send(b []byte) error {
	above, err := c.ws.WriteMessageNoWait(websocket.Binary, b)
	if err == nil && above {
		c.overLimit()
	}
	return err
}

// overLimit begins an episode above the send queue's limit, unless one is under way.
// overLimit 开始一个发送队列超过上限的时段，除非已在其中。
func (c *Conn) overLimit() {
	c.flowMu.Lock()
	if c.above {
		c.flowMu.Unlock()
		return
	}
	c.above = true
	c.gen++
	gen := c.gen
	c.Session.holdStreams(true)
	c.ws.HoldReads(true)
	c.hold = time.AfterFunc(c.cfg.maxHold(), func() { c.releaseReads(gen) })
	c.flowMu.Unlock()
	if !c.ws.OnWritable(func(error) { c.writable(gen) }) {
		c.writable(gen) // drained already / 已经排空
	}
}

// releaseReads ends the hold on reads of episode gen, which goes on otherwise.
// releaseReads 结束第 gen 个时段的读取暂停，时段本身继续。
func (c *Conn) releaseReads(gen uint64) {
	c.flowMu.Lock()
	if c.above && c.gen == gen {
		c.ws.HoldReads(false)
	}
	c.flowMu.Unlock()
}

// writable ends episode gen: the queue fell to half its limit, or the connection ended.
// writable 结束第 gen 个时段：队列已降到上限的一半，或连接已结束。
func (c *Conn) writable(gen uint64) {
	c.flowMu.Lock()
	if !c.above || c.gen != gen {
		c.flowMu.Unlock()
		return
	}
	c.above = false
	c.hold.Stop()
	c.ws.HoldReads(false)
	c.Session.holdStreams(false)
	c.flowMu.Unlock()
	// Outside the lock: a sender woken may go above the limit again at once.
	// 在锁外唤醒：被唤醒的发送方可能立刻再次超过上限。
	c.Session.wakeStreams()
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

// forceClose closes a connection whose peer is dead or too slow at once, on either side: a close frame would queue
// behind whatever is still unsent, so the WebSocket is aborted without the close handshake, what waits for the peer
// dropped. The session closes first, with err (see closeWith).
//
// forceClose 立即关闭对端已死或过慢的连接（两端皆然）：关闭帧会排在所有未发送的内容之后，所以跳过关闭握手直接中止 WebSocket，
// 丢弃等待发往对端的数据。会话先以 err 关闭（见 closeWith）。
func (c *Conn) forceClose(err error) {
	c.Session.Close(err)
	c.ws.Abort(err)
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
		c.forceClose(ErrHeartbeat)
		return
	}
	waited := c.Unanswered()
	if slow := c.cfg.slowTimeout(); slow > 0 && waited >= slow {
		c.forceClose(ErrSlow)
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
		c.flowMu.Lock()
		if c.hold != nil {
			c.hold.Stop()
		}
		c.flowMu.Unlock()
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
	cfg ConnConfig
	tcp *tcpConns
	// conns is every connection upgraded and not yet closed, for CloseAll; each carries its serverConn as its
	// attachment.
	// conns 是每条已升级且尚未关闭的连接，供 CloseAll 使用；每条连接以其 serverConn 作为附件。
	conns sync.Map // *websocket.Connection → struct{}
	count atomic.Int64
}

type serverConn struct {
	challenge []byte
	req       *http.Request
	timer     *time.Timer
	mu        sync.Mutex
	conn      *Conn // set once authenticated / 认证通过后设置
}

// tcpConns wraps the WebSocket handler to bound the connections that have not authenticated yet, from their TCP
// connect (TLS and the upgrade included) to their proof, and to close them all once the listener's binding goes.
// A WebSocket's own TCP connection, its NetConn, takes it off once it authenticates.
//
// tcpConns 包装 WebSocket 处理器，限制尚未认证的连接（从 TCP 连接建立，经 TLS 与升级，到证明通过），并在监听方的绑定移除后
// 将它们全部关闭。WebSocket 认证通过后，由其自身的 TCP 连接（NetConn）将其移出。
type tcpConns struct {
	fib.Handler
	srv *server

	// The connections that have not authenticated, each with its IP address, at most maxPending in all and maxPerIP
	// from one IP address; closed once the listener is.
	// 尚未认证的连接及其 IP 地址，总数至多 maxPending，同一 IP 地址至多 maxPerIP；closed 表示监听方已关闭。
	maxPending, maxPerIP int
	pmu                  sync.Mutex
	closed               bool
	pending              map[*fib.Connection]netip.Addr
	pendingIP            map[netip.Addr]int
}

func (t *tcpConns) OnOpen(c *fib.Connection) {
	if !t.hold(c) {
		_ = c.Close()
		return
	}
	t.Handler.OnOpen(c)
}

func (t *tcpConns) OnClose(c *fib.Connection, err error) {
	t.authenticated(c)
	t.Handler.OnClose(c, err)
}

// hold counts c among the connections that have not authenticated, unless the listener is closed or there are as
// many as it holds, in all or from c's IP address.
// hold 将 c 计入尚未认证的连接，除非监听方已关闭，或总数、来自 c 的 IP 地址的数量已达上限。
func (t *tcpConns) hold(c *fib.Connection) bool {
	ip := remoteIP(c.RemoteAddr())
	t.pmu.Lock()
	defer t.pmu.Unlock()
	if t.closed || len(t.pending) >= t.maxPending || t.pendingIP[ip] >= t.maxPerIP {
		return false
	}
	t.pending[c] = ip
	t.pendingIP[ip]++
	return true
}

// authenticated takes c off the connections that have not authenticated, if it is among them: it has, or it closed.
// authenticated 将 c 移出尚未认证的连接（若在其中）：它已通过认证，或已关闭。
func (t *tcpConns) authenticated(c *fib.Connection) {
	t.pmu.Lock()
	defer t.pmu.Unlock()
	ip, ok := t.pending[c]
	if !ok {
		return
	}
	delete(t.pending, c)
	if t.pendingIP[ip] <= 1 {
		delete(t.pendingIP, ip)
	} else {
		t.pendingIP[ip]--
	}
}

// remoteIP is the IP address of a, or the zero Addr.
// remoteIP 返回 a 的 IP 地址，或零值 Addr。
func remoteIP(a net.Addr) netip.Addr {
	if t, ok := a.(*net.TCPAddr); ok {
		ip, _ := netip.AddrFromSlice(t.IP)
		return ip.Unmap()
	}
	if a == nil {
		return netip.Addr{}
	}
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
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

// Listener is the handler NewHandler returns: a binding's handler that
// takes WebSocket links on Path.
type Listener struct{ *tcpConns }

// CloseAll closes every connection the listener took, authenticated or
// not, and every one it takes from now on, once its binding is removed:
// a removed fib binding leaves its connections open. Links are aborted
// without the close handshake, their sessions closed with ErrClosed.
func (l Listener) CloseAll() {
	l.pmu.Lock()
	l.closed = true
	pending := make([]*fib.Connection, 0, len(l.pending))
	for c := range l.pending {
		pending = append(pending, c)
	}
	l.pmu.Unlock()
	for _, c := range pending {
		_ = c.Close()
	}
	// Every connection upgraded is in conns from its upgrade, and in pending until it authenticates.
	// 每条已升级的连接自升级起就在 conns 中，认证之前也在 pending 中。
	l.srv.conns.Range(func(k, _ any) bool {
		k.(*websocket.Connection).Abort(ErrClosed)
		return true
	})
}

// Path is the only path a listener upgrades; any other is answered 404.
const Path = "/ws"

// NewHandler returns the handler of a listener's fib binding: it upgrades
// WebSocket connections on Path (any other path is answered 404),
// authenticates them and runs a session on each.
func NewHandler(cfg ConnConfig) Listener {
	s := &server{cfg: cfg}
	wsCfg := websocket.DefaultConfig()
	wsCfg.MaxMessageBytes = int64(cfg.maxFrame())
	wsCfg.Path = Path
	// Links are not opened from a browser's page.
	wsCfg.CheckOrigin = func(*http.Request) bool { return true }
	wsCfg.HTTP.MaxHeaderBytes = 16 << 10
	wsCfg.HandshakeTimeout = HandshakeTimeout
	var h fib.Handler = websocket.NewHandlerWithConfig(wsCfg, websocket.HandlerFuncs{
		Open:    s.open,
		Message: s.message,
		Close:   s.close,
	})
	if cfg.TLS != nil {
		// fib's TLS: the handshake takes no worker while it waits, and is
		// bounded by its own timeout; the connection counts as pending
		// from its TCP connect.
		th := fibtls.NewServer(cfg.TLS, h)
		th.HandshakeTimeout = HandshakeTimeout
		h = th
	}
	s.tcp = &tcpConns{Handler: h, srv: s, maxPending: cfg.MaxPending, maxPerIP: cfg.MaxPendingPerIP,
		pending: map[*fib.Connection]netip.Addr{}, pendingIP: map[netip.Addr]int{}}
	if s.tcp.maxPending <= 0 {
		s.tcp.maxPending = DefaultMaxPending
	}
	if s.tcp.maxPerIP <= 0 {
		s.tcp.maxPerIP = DefaultMaxPendingPerIP
	}
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
	sc := &serverConn{challenge: make([]byte, challengeSize), req: r}
	_, _ = rand.Read(sc.challenge)
	ws.SetAttachment(sc)
	s.conns.Store(ws, struct{}{})
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
	sc, ok := ws.Attachment().(*serverConn)
	if !ok {
		return
	}
	sc.mu.Lock()
	c := sc.conn
	sc.mu.Unlock()
	if c != nil {
		c.receive(data)
		return
	}

	ack := []byte{ackOK}
	var roles []string
	var token string
	var err error
	if _, _, _, ok := parseTokenAnswer(data); ok {
		// A dialer that proves a link token: the token's roles, and the listener proves it back.
		// 证明连接令牌的拨号方：令牌的角色，监听方回证该令牌。
		var proof []byte
		token, proof, err = s.verifyToken(sc.challenge, data)
		ack, roles = append(ack, proof...), s.cfg.TokenRoles
	} else {
		// A dialer of this version sends a nonce after its proof, for the listener to prove the password in its ack.
		// 本版本的拨号方在证明之后附上随机数，供监听方在确认中证明密码。
		var nonce []byte
		if len(data) == proofSize+NonceSize {
			nonce = data[proofSize:]
		}
		var prover Mutual
		prover, roles, err = s.verify(sc.challenge, data, nonce != nil)
		if prover != nil && nonce != nil {
			ack = append(ack, prover.ProveListener(sc.challenge, nonce)...)
		}
	}
	if err != nil {
		if s.cfg.OnAuthFailed != nil {
			s.cfg.OnAuthFailed(sc.req)
		}
		reason := "authentication failed"
		if !errors.Is(err, ErrAuth) {
			reason = err.Error() // the token's: unknown, or expired / 令牌的原因：未知或已过期
		}
		_ = ws.Close(closeAuthFailed, reason)
		return
	}
	sc.timer.Stop()
	c = newConn(ws, &s.cfg, false, sc.req, roles, token)
	s.tcp.authenticated(ws.NetConn())
	sc.mu.Lock()
	sc.conn = c
	sc.mu.Unlock()
	// The session exists before the ack goes out, so whatever the dialer sends after it finds a session.
	// 确认发出之前会话已建立，拨号方随后发送的任何帧都有会话处理。
	if err := ws.WriteBinary(ack); err != nil {
		_ = ws.Close(websocket.CloseInternalError, "")
		return
	}
	c.start()
	if s.cfg.OnOpen != nil {
		s.cfg.OnOpen(c)
	}
}

// verify checks a dialer's proof, followed by its nonce when nonced is set, and returns the authenticator the listener
// proves itself with (nil: it proves nothing) and the roles of the credential proven.
// verify 校验拨号方的证明（nonced 为真时其后跟随机数），返回监听方用以证明自身的认证（nil 表示不证明）以及被证明凭据的角色。
func (s *server) verify(challenge, data []byte, nonced bool) (Mutual, []string, error) {
	if len(s.cfg.Credentials) > 0 || s.cfg.Tokens != nil {
		if nonced {
			data = data[:proofSize]
		}
		cred := match(s.cfg.Credentials, challenge, data)
		if cred == nil {
			return nil, nil, ErrAuth
		}
		return cred.Password, cred.Roles, nil
	}
	m, _ := s.cfg.Auth.(Mutual)
	if m == nil || !m.Mutual() {
		m = nil
	} else if nonced {
		data = data[:proofSize]
	}
	if s.cfg.Auth != nil {
		if err := s.cfg.Auth.Verify(challenge, data); err != nil {
			return nil, nil, err
		}
	}
	return m, nil, nil
}

func (s *server) close(ws *websocket.Connection, _ uint16, _ string, err error) {
	sc, ok := ws.Attachment().(*serverConn)
	if !ok {
		return
	}
	s.conns.Delete(ws)
	s.count.Add(-1)
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

// Dial connects to a ws:// or wss:// URL and completes the authentication handshake. ctx bounds the whole dial. A
// wss:// URL needs cfg.TLS.
// Dial 连接 ws:// 或 wss:// 地址并完成认证握手。ctx 限定整个拨号过程。wss:// 地址需要 cfg.TLS。
func Dial(ctx context.Context, url string, cfg DialConfig) (*Conn, error) {
	engine := cfg.Engine
	if engine == nil {
		return nil, errors.New("wire: no engine to dial on")
	}
	if strings.HasPrefix(url, "wss://") && cfg.TLS == nil {
		return nil, errors.New("wire: a wss:// URL needs a TLS configuration that checks the listener's certificate")
	}
	timeout := HandshakeTimeout
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	wsCfg := websocket.DefaultDialerConfig()
	wsCfg.HandshakeTimeout = timeout
	wsCfg.MaxMessageBytes = int64(cfg.maxFrame())
	wsCfg.TLSConfig = cfg.TLS
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
		// challenge and nonce are what the listener proves the password for, when the authenticator is Mutual.
		// challenge 与 nonce 是 Authenticator 为 Mutual 时监听方要为之证明密码的内容。
		challenge, nonce []byte
	)
	mutual, _ := cfg.Auth.(Mutual)
	if mutual != nil && !mutual.Mutual() {
		mutual = nil
	}
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
				if len(data) != challengeSize {
					// A dialer answers only a challenge: what else a listener sends it might be a listener's proof to
					// be had from it.
					// 拨号方只应答挑战：监听方发来的其他内容可能是想从它这里套取监听方的证明。
					err = fmt.Errorf("%w: a challenge of %d bytes", ErrProtocol, len(data))
				}
				switch {
				case err != nil:
				case cfg.Token != nil:
					challenge = bytes.Clone(data)
					proof, nonce = tokenAnswer(cfg.Token, challenge)
				case cfg.Auth != nil:
					proof, err = cfg.Auth.Prove(data)
				}
				if err == nil && mutual != nil && cfg.Token == nil {
					challenge, nonce = bytes.Clone(data), make([]byte, NonceSize)
					_, _ = rand.Read(nonce)
					proof = append(proof, nonce...)
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
				if len(data) < 1 || data[0] != ackOK || mutual == nil && cfg.Token == nil && len(data) != 1 {
					settle(dialResult{err: fmt.Errorf("%w: bad handshake ack", ErrProtocol)})
					_ = w.Close(websocket.CloseProtocolError, "")
					return
				}
				if cfg.Token != nil && cfg.Token.VerifyListener(tokenTranscript(cfg.Token.ID, nonce, challenge), data[1:]) != nil {
					settle(dialResult{err: fmt.Errorf("%w: %w", ErrAuth, ErrTokenListener)})
					_ = w.Close(closeAuthFailed, "")
					return
				}
				if cfg.Token == nil && mutual != nil && mutual.VerifyListener(challenge, nonce, data[1:]) != nil {
					// The listener does not know the password (or is of an earlier version, which proves nothing).
					// 监听方不知道密码（或是不做证明的早先版本）。
					settle(dialResult{err: fmt.Errorf("%w: the listener did not prove the password", ErrAuth)})
					_ = w.Close(closeAuthFailed, "")
					return
				}
				c = newConn(w, &cfg.ConnConfig, true, nil, nil, "")
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
