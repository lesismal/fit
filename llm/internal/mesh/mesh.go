// Package mesh is the interconnect between node, hub and proxy, on pkg/wire sessions over WebSocket.
// Every process may run a listener (changeable at runtime) and may dial any number of peers; whichever side dials,
// the dialer first calls MethodHello and both sides learn each other's identity (role + stable id). From then on both
// ends serve their methods, streams and pushes on the connection, and each method decides which caller roles it
// accepts (see Router).
//
// Package mesh 是 node、hub、proxy 之间的互联，基于 WebSocket 上的 pkg/wire 会话。每个进程都可以运行监听（可在运行时
// 修改），也可以主动连接任意多个对端；无论哪一方拨号，拨号方都先调用 MethodHello，双方由此得知对方身份（角色 + 稳定 ID）。
// 之后两端都在该连接上提供各自的方法、流与推送，每个方法决定接受哪些调用方角色（见 Router）。
package mesh

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/logs"

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// dialTimeout bounds one dial, authentication included.
// dialTimeout 一次拨号（含认证）的时限。
const dialTimeout = 10 * time.Second

// helloTimeout is how long an incoming connection may stay anonymous before it is closed.
// helloTimeout 入站连接在被关闭前允许保持匿名的时长。
const helloTimeout = 15 * time.Second

// ErrOffline is returned when no usable connection to the peer exists.
// ErrOffline 不存在到该对端的可用连接时返回。
var ErrOffline = Errorf(model.CodeUnavailable, "peer is offline")

// DefaultMaxConns is the default cap on the inbound connections of a listener.
// DefaultMaxConns 监听方入站连接数的默认上限。
const DefaultMaxConns = 10000

// Options configures a Mesh.
// Options Mesh 配置。
type Options struct {
	Self func() model.Identity // called for every hello, so it may change at runtime / 每次 hello 都会调用，可在运行时变化
	// AcceptRoles are the peer roles accepted, in both directions; any other role is refused at hello. A peer of this
	// process's own role is always refused, whatever AcceptRoles says.
	// AcceptRoles 接受的对端角色（两个方向）；其他角色在 hello 时被拒绝。与本进程同角色的对端无论 AcceptRoles 如何一律拒绝。
	AcceptRoles []string

	// PingInterval is wire's heartbeat: a ping after this long without a frame, the connection closed after three
	// (≤ 0: 15s).
	// PingInterval wire 心跳：这么久没收到帧就发 ping，三个周期没收到就关闭连接（≤ 0 时为 15s）。
	PingInterval time.Duration
	// RetryInterval caps the wait between dial attempts to a peer that is unreachable (≤ 0: 5s): the wait grows
	// exponentially from backoffBase up to it, each one a random fraction of the current step (see backoff).
	// RetryInterval 对端不可达时两次拨号之间等待时间的上限（≤ 0 时为 5s）：等待从 backoffBase 起指数增长到该上限，
	// 每次取当前步长内的随机值（见 backoff）。
	RetryInterval time.Duration
	// SlowTimeout drops a connection whose ping waited this long for its pong (0: wire.DefaultSlowTimeout, < 0: off).
	// SlowTimeout ping 等待 pong 超过这么久即断开连接（0：wire.DefaultSlowTimeout，< 0：关闭）。
	SlowTimeout time.Duration
	// MaxConns caps the inbound connections of the listener (≤ 0: DefaultMaxConns).
	// MaxConns 监听方入站连接数上限（≤ 0 时为 DefaultMaxConns）。
	MaxConns int
	// MaxInflight caps the requests and streams a peer may have running here per connection; one more fails at once
	// with CodeRateLimited (0: wire.DefaultMaxInbound, < 0: no limit).
	// MaxInflight 每条连接上对端在本方同时运行的请求与流的上限；超出的立即以 CodeRateLimited 失败（0：wire.DefaultMaxInbound，
	// < 0：不限制）。
	MaxInflight int
	// StreamIdle is wire's stream idle timeout (0: wire.DefaultStreamIdle); tests shorten it.
	// StreamIdle wire 的流空闲超时（0：wire.DefaultStreamIdle）；测试中缩短它。
	StreamIdle time.Duration

	// Engine is the fib engine the listener is a binding of, and links are
	// dialed on.
	Engine *fib.Engine
	// Log is where the mesh says what it does; nil says nothing.
	Log *logs.Logger
	// Filter, when not nil, is the listener binding's fib.Binding.Filter,
	// asked of every connection it accepts before it costs anything: false
	// closes it. The role's access gate, whose policy changes without the
	// listener starting again.
	// Filter 非 nil 时作为监听绑定的 fib.Binding.Filter，在每个接受的连接产生任何开销前询问：
	// 返回 false 即关闭。它是角色的访问控制 gate，其策略变化时监听无需重启。
	Filter func(netip.AddrPort) bool
}

// log writes msg and its key-value pairs at level.
func (m *Mesh) log(level slog.Level, msg string, kv ...any) {
	l := m.opts.Log
	if l == nil || !l.Enabled(level) {
		return
	}
	var b strings.Builder
	b.WriteString(msg)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, " %v=%v", kv[i], kv[i+1])
	}
	l.Msg(level, b.String())
}

// Reconnect backoff: the n-th consecutive failure waits a random duration in (0, min(RetryInterval, backoffBase·2ⁿ)]
// (at least backoffFloor). A connection that lived at least stableConn resets n.
// 重连退避：连续第 n 次失败后等待 (0, min(RetryInterval, backoffBase·2ⁿ)] 内的随机时长（至少 backoffFloor）。
// 存活至少 stableConn 的连接使 n 归零。
const (
	backoffBase  = time.Second
	backoffFloor = 100 * time.Millisecond
	stableConn   = 10 * time.Second
)

// backoff returns the wait before the next dial after n consecutive failures ("full jitter"): spreading the
// redials of many dialers over the whole step, instead of a fixed delay, keeps them from reconnecting in lockstep
// when a listener they all used comes back.
// backoff 返回连续失败 n 次后下次拨号前的等待（“全抖动”）：把大量拨号方的重连分散到整个步长内，而不是固定延迟，
// 避免它们共同的监听方恢复时同时重连。
func backoff(n int, ceiling time.Duration) time.Duration {
	step := ceiling
	if n < 30 && backoffBase<<n < ceiling {
		step = backoffBase << n
	}
	return max(backoffFloor, time.Duration(rand.Int64N(int64(step)))+1)
}

// refusedRetry is the least delay before dialing again a peer that refused the hello (see refusedBackoff).
// refusedRetry 对端拒绝 hello 后再次拨号前的最短等待时间（见 refusedBackoff）。
const refusedRetry = 30 * time.Second

// refusedBackoff is the wait after a refused hello: at least max(refusedRetry, ceiling), plus up to half of it.
// refusedBackoff hello 被拒绝后的等待：至少 max(refusedRetry, ceiling)，再加上其一半以内的随机值。
func refusedBackoff(ceiling time.Duration) time.Duration {
	base := max(refusedRetry, ceiling)
	return base + time.Duration(rand.Int64N(int64(base/2)+1))
}

// PushFunc handles a push from a peer. Pushes of one connection run one at a time, in arrival order.
// PushFunc 处理对端的推送。同一连接的推送按到达顺序逐个执行。
type PushFunc func(caller model.Identity, msg json.RawMessage)

type pushRoute struct {
	roles []string
	fn    PushFunc
}

// conn is one wire connection, inbound or outbound. A reconnect is a new conn.
// conn 一条 wire 连接，入站或出站。重新连接即新的 conn。
type conn struct {
	wc     *wire.Conn
	id     int64
	dir    string
	remote string
	target *target // outbound only / 仅出站
	// roles are the roles an inbound connection may say hello as: those of the password its dialer proved.
	// roles 是入站连接可以 hello 的角色：其拨号方所证明密码的角色。
	roles []string
	// token is the link token an inbound connection proved, "" for a password, and use its record in the
	// registry once the hello bound the peer's ID to it.
	// token 是入站连接所证明的连接令牌，密码则为 ""；use 是 hello 把对端 ID 绑定到令牌后在登记表中的记录。
	token string
	use   *linktoken.Use
	ready chan struct{} // closed once the hello completed / hello 完成后关闭

	mu         sync.Mutex
	registered bool
	closed     bool
	peer       model.PeerConn
}

func (c *conn) live() bool { return c.wc.Err() == nil }

// usable reports whether new calls, streams and pushes may go to c: it is open and neither side has sent drain.
// usable 表示新的调用、流与推送可以发往 c：连接未关闭，且双方都未发送 drain。
func (c *conn) usable() bool { return c.live() && !c.wc.Draining() }

type target struct {
	addr     string
	password string
	// token is the link token the target is dialed with, in place of password, and sec it parsed.
	// token 是拨号该目标所用的连接令牌（代替 password），sec 为其解析结果。
	token string
	sec   *linktoken.Secret
	role  string // the role the peer must have / 对端必须具有的角色
	// req is what the target was set with, its TLS settings among it, and tls the configuration it is dialed with
	// (nil: plain ws://).
	// req 是设置该目标时的请求（含其 TLS 设置），tls 为拨号所用的配置（nil 表示普通 ws://）。
	req model.DialReq
	tls *tls.Config

	mu     sync.Mutex
	conn   *conn
	err    string
	stopCh chan struct{}
	// nextDelay overrides the wait before the next dial once (a refused hello waits longer).
	// nextDelay 一次性覆盖下次拨号前的等待（hello 被拒绝时等待更久）。
	nextDelay time.Duration
}

func (t *target) setErr(s string) {
	t.mu.Lock()
	t.err = s
	t.mu.Unlock()
}

// Mesh is one process's side of the interconnect.
// Mesh 互联中一个进程的一侧。
type Mesh struct {
	optsMu sync.Mutex
	opts   Options
	router *Router

	// The listener (listen.go); lmu serializes its changes.
	// 监听（listen.go）；lmu 使其变更串行化。
	lmu     sync.Mutex
	lcfg    model.ListenConfig
	lerr    string
	lopts   Options // the options the listener runs with
	binding int     // the listener's binding on the engine, or -1
	lst     *wire.Listener
	laddr   string
	lfp     string // the fingerprint of the listener's certificate, "" without TLS
	// lcert is the certificate of a listener with cert_file, which a new
	// handshake takes, swapped when the files change.
	// lcert 是带 cert_file 的监听的证书，新握手取用它，文件变化时替换。
	lcert atomic.Pointer[tls.Certificate]

	nextConn atomic.Int64
	round    atomic.Uint64 // round robin over the connections of a peer / 在对端的连接之间轮询

	mu      sync.RWMutex
	conns   map[*wire.Conn]*conn
	byID    map[string][]*conn // registered connections per peer id / 每个对端 ID 的已注册连接
	targets map[string]*target
	pushes  map[string]pushRoute
	onUp    []func(model.PeerConn)
	onDown  []func(model.PeerConn)

	draining atomic.Bool // Shutdown began / Shutdown 已开始
	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup

	routes *Routes
	// tokens are the link tokens the listener issued, and the links that use them.
	// tokens 是监听方签发的连接令牌，以及使用它们的连接。
	tokens *linktoken.Registry

	// helloWait is helloTimeout; a test may shorten it before the mesh listens.
	// helloWait 即 helloTimeout；测试可在互联监听之前缩短它。
	helloWait time.Duration
}

// New creates a Mesh serving the methods of router. Routes may still be added to router until Start.
// New 创建提供 router 中方法的 Mesh。在 Start 之前仍可向 router 添加路由。
func New(opts Options, router *Router) *Mesh {
	opts = withDefaults(opts)
	return &Mesh{
		binding: -1,
		opts:    opts,
		router:  router,
		conns:   map[*wire.Conn]*conn{},
		byID:    map[string][]*conn{},
		targets: map[string]*target{},
		pushes:  map[string]pushRoute{},
		stopCh:  make(chan struct{}),
		routes:  newRoutes(),
		tokens:  &linktoken.Registry{},

		helloWait: helloTimeout,
	}
}

// Tokens returns the listener's link tokens: the role sets them from its config, and its OnBind.
// Tokens 返回监听方的连接令牌：由角色根据其配置设置，并设置其 OnBind。
func (m *Mesh) Tokens() *linktoken.Registry { return m.tokens }

// tokenRoles are the roles a link that proved a token may say hello as: every role the mesh accepts, since a token is
// of no role.
// tokenRoles 是证明了令牌的连接可以 hello 的角色：mesh 接受的所有角色，因为令牌不绑定角色。
func (m *Mesh) tokenRoles() []string {
	var out []string
	for _, r := range m.opts.AcceptRoles {
		if r != model.RoleLocal {
			out = append(out, r)
		}
	}
	return out
}

// Routes returns the counts of the model calls the role relays, per route.
// Routes 返回角色按路由统计的模型调用计数。
func (m *Mesh) Routes() *Routes { return m.routes }

func withDefaults(opts Options) Options {
	if opts.PingInterval <= 0 {
		opts.PingInterval = 15 * time.Second
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = 5 * time.Second
	}
	if opts.MaxConns <= 0 {
		opts.MaxConns = DefaultMaxConns
	}
	return opts
}

// o returns the options, as SetLimits last changed them.
func (m *Mesh) o() Options {
	m.optsMu.Lock()
	defer m.optsMu.Unlock()
	return m.opts
}

// SetLimits changes the bounds of the links: PingInterval, RetryInterval,
// SlowTimeout, MaxConns, MaxInflight and StreamIdle of opts, its other fields
// ignored. Links made from now on take them; a listener whose MaxConns
// changed is started again by the next SetListen.
func (m *Mesh) SetLimits(opts Options) {
	opts = withDefaults(opts)
	m.optsMu.Lock()
	m.opts.PingInterval, m.opts.RetryInterval, m.opts.SlowTimeout = opts.PingInterval, opts.RetryInterval, opts.SlowTimeout
	m.opts.MaxConns, m.opts.MaxInflight, m.opts.StreamIdle = opts.MaxConns, opts.MaxInflight, opts.StreamIdle
	m.optsMu.Unlock()
}

// Self returns the identity of this process.
// Self 返回本进程的身份。
func (m *Mesh) Self() model.Identity { return m.opts.Self() }

// OnPeerUp registers fn to be called (in its own goroutine) when a connection completes its hello.
// OnPeerUp 注册在连接完成 hello 时调用的 fn（在独立 goroutine 中调用）。
func (m *Mesh) OnPeerUp(fn func(model.PeerConn)) { m.onUp = append(m.onUp, fn) }

// OnPeerDown registers fn to be called (in its own goroutine) when a registered connection is lost.
// OnPeerDown 注册在已注册连接断开时调用的 fn（在独立 goroutine 中调用）。
func (m *Mesh) OnPeerDown(fn func(model.PeerConn)) { m.onDown = append(m.onDown, fn) }

// OnPush registers fn for the pushes of method (see Push) sent by peers of the given roles; pushes from other roles
// are dropped. Register before Start.
// OnPush 为指定角色的对端发来的 method 推送（见 Push）注册 fn；其他角色的推送被丢弃。须在 Start 之前注册。
func (m *Mesh) OnPush(method string, roles []string, fn PushFunc) {
	m.mu.Lock()
	m.pushes[method] = pushRoute{roles: roles, fn: fn}
	m.mu.Unlock()
}

// connConfig is the wire configuration of a connection: t is the dial target of an outbound one; an inbound one
// (t == nil) is checked with the listener's credentials, which decide the roles it may say hello as.
// connConfig 连接的 wire 配置：出站连接的 t 为其连接目标；入站连接（t == nil）用监听方的凭据校验，凭据决定它可以以哪些角色
// hello。
func (m *Mesh) connConfig(t *target, creds []wire.Credential) wire.ConnConfig {
	cfg := wire.ConnConfig{
		Session:      wire.Options{StreamIdle: m.o().StreamIdle, MaxInbound: m.o().MaxInflight},
		PingInterval: m.o().PingInterval,
		SlowTimeout:  m.o().SlowTimeout,
		OnClose:      func(wc *wire.Conn, err error) { m.disconnected(wc, err) },
	}
	if t == nil {
		cfg.MaxConns = m.o().MaxConns
		cfg.Accept = func(*http.Request) error {
			if m.draining.Load() {
				return errors.New("shutting down")
			}
			return nil
		}
		cfg.Credentials = creds
		cfg.Tokens, cfg.TokenRoles = m.tokens, m.tokenRoles()
		cfg.OnAuthFailed = func(r *http.Request) {
			m.log(slog.LevelWarn, "mesh: authentication failed (wrong password or token)", "remote", r.RemoteAddr)
		}
		cfg.Handlers = func(wc *wire.Conn) wire.Handlers { return m.handlers(m.connected(wc, nil)) }
		cfg.OnOpen = m.awaitHello
	} else {
		if t.sec != nil {
			cfg.Token = t.sec
		} else {
			cfg.Auth = wire.Password(t.password)
		}
		cfg.Handlers = func(wc *wire.Conn) wire.Handlers { return m.handlers(m.connected(wc, t)) }
	}
	return cfg
}

// Configure moves the mesh to listen and peers: the listener is started,
// changed or stopped as listen says (an empty Addr: none), the dial targets
// not in peers are dropped, and those in it are added, or changed when their
// password or role did. What is unchanged, its links among it, is left
// alone. A listener that cannot bind is not an error here: it is logged and
// kept in Listen().Error, so that a role still starts and its pages can fix
// it. The error lists the dial targets skipped as invalid.
func (m *Mesh) Configure(listen model.ListenConfig, peers []model.DialReq) error {
	if err := m.SetListen(listen); err != nil {
		m.log(slog.LevelError, "mesh: listener not started", "addr", listen.Addr, "error", err)
	}
	keep := map[string]bool{}
	var errs []error
	for _, p := range peers {
		if err := m.Dial(p); err != nil {
			errs = append(errs, fmt.Errorf("dial target %s: %w", p.Addr, err))
			continue
		}
		if addr, err := ValidateAddr(p.Addr, true); err == nil {
			keep[addr] = true
		}
	}
	m.mu.RLock()
	var drop []string
	for addr := range m.targets {
		if !keep[addr] {
			drop = append(drop, addr)
		}
	}
	m.mu.RUnlock()
	for _, addr := range drop {
		m.Undial(addr)
	}
	return errors.Join(errs...)
}

// Stop closes every connection and the listener.
// Stop 关闭所有连接与监听。
func (m *Mesh) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
		m.mu.Lock()
		targets := make([]*target, 0, len(m.targets))
		for _, t := range m.targets {
			targets = append(targets, t)
		}
		m.mu.Unlock()
		for _, t := range targets {
			m.stopTarget(t)
		}
		m.closeConns(func(*conn) bool { return true })
		m.lmu.Lock()
		m.stopListenerLocked()
		m.lmu.Unlock()
	})
	m.wg.Wait()
}

// closeConns closes the connections for which match is true.
// closeConns 关闭 match 为真的连接。
func (m *Mesh) closeConns(match func(*conn) bool) {
	m.mu.RLock()
	var list []*wire.Conn
	for wc, c := range m.conns {
		if match(c) {
			list = append(list, wc)
		}
	}
	m.mu.RUnlock()
	for _, wc := range list {
		wc.Close()
		m.disconnected(wc, nil)
	}
}

// Shutdown drains the mesh, then stops it. The listener refuses new connections; every connection is told to drain
// (wire.Session.Drain), so peers stop starting calls, streams and pushes on it and this process stops picking it;
// calls and streams already running, in both directions, get until ctx ends to finish. Peers dialing this process
// redial (with backoff) once it closes their connection.
//
// Shutdown 先让互联下线，再停止它。监听方拒绝新连接；每条连接都收到 drain（wire.Session.Drain），对端不再在其上发起调用、
// 流与推送，本进程也不再选用它；两个方向上已在进行的调用与流在 ctx 结束前可以继续完成。主动连接本进程的对端在连接关闭后
// 重新拨号（带退避）。
func (m *Mesh) Shutdown(ctx context.Context) {
	if !m.draining.Swap(true) {
		m.mu.RLock()
		conns := make([]*wire.Conn, 0, len(m.conns))
		for wc := range m.conns {
			conns = append(conns, wc)
		}
		m.mu.RUnlock()
		for _, wc := range conns {
			_ = wc.Drain()
		}
		m.log(slog.LevelInfo, "mesh draining", "conns", len(conns), "inflight", m.inflight())
		tick := time.NewTicker(50 * time.Millisecond)
	wait:
		for m.inflight() > 0 {
			select {
			case <-ctx.Done():
				m.log(slog.LevelWarn, "mesh: shutdown grace expired, closing with work in flight", "inflight", m.inflight())
				break wait
			case <-tick.C:
			}
		}
		tick.Stop()
	}
	m.Stop()
}

// inflight counts the calls and streams running on every connection.
// inflight 统计所有连接上正在进行的调用与流。
func (m *Mesh) inflight() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for wc := range m.conns {
		n += wc.Inflight()
	}
	return n
}

func (m *Mesh) stopped() bool {
	select {
	case <-m.stopCh:
		return true
	default:
		return false
	}
}

// ---- Connections ----
// ---- 连接 ----

// connected records a new authenticated connection (called by wire before any frame of it is handled).
// connected 记录一条新的已认证连接（由 wire 在处理该连接的任何帧之前调用）。
func (m *Mesh) connected(wc *wire.Conn, t *target) *conn {
	c := &conn{wc: wc, id: m.nextConn.Add(1), dir: "in", remote: wc.RemoteAddr(), target: t, roles: wc.Roles,
		token: wc.Token, ready: make(chan struct{})}
	if t != nil {
		c.dir, c.remote = "out", t.addr
	}
	m.mu.Lock()
	m.conns[wc] = c
	m.mu.Unlock()
	return c
}

// awaitHello closes an incoming connection whose dialer does not say hello in time. wire calls it once the
// connection is whole: connected runs while wire still makes it, before its session exists, which the timer's
// Close reads.
// awaitHello 关闭未及时 hello 的入站连接。wire 在连接构造完成后调用它：connected 运行时 wire 仍在构造连接，其会话尚不
// 存在，而定时器的 Close 会读取会话。
func (m *Mesh) awaitHello(wc *wire.Conn) {
	m.mu.RLock()
	c := m.conns[wc]
	m.mu.RUnlock()
	if c == nil {
		return // closed already / 已关闭
	}
	time.AfterFunc(m.helloWait, func() {
		c.mu.Lock()
		stale := !c.registered && !c.closed
		c.mu.Unlock()
		if stale {
			m.log(slog.LevelWarn, "mesh: closing a connection that did not say hello", "remote", c.remote)
			wc.Close()
		}
	})
}

// disconnected forgets a closed connection; err is why it closed, if known. Idempotent.
// disconnected 移除已关闭的连接；err 为关闭原因（如已知）。可重复调用。
func (m *Mesh) disconnected(wc *wire.Conn, err error) {
	m.mu.Lock()
	c := m.conns[wc]
	if c == nil {
		m.mu.Unlock()
		return
	}
	delete(m.conns, wc)
	c.mu.Lock()
	was, peer, use := c.registered, c.peer, c.use
	c.registered, c.closed, c.use = false, true, nil
	c.mu.Unlock()
	use.Release()
	if was {
		m.unindexLocked(c, peer.ID)
	}
	m.mu.Unlock()
	if was {
		attrs := []any{"role", peer.Role, "id", peer.ID, "dir", c.dir, "remote", peer.Remote}
		if err != nil && !errors.Is(err, wire.ErrClosed) {
			attrs = append(attrs, "reason", err)
		}
		m.log(slog.LevelInfo, "mesh peer disconnected", attrs...)
		for _, fn := range m.onDown {
			go fn(peer)
		}
	}
}

func (m *Mesh) unindexLocked(c *conn, id string) {
	list := m.byID[id]
	if i := slices.Index(list, c); i >= 0 {
		list = slices.Delete(slices.Clone(list), i, i+1)
	}
	if len(list) == 0 {
		delete(m.byID, id)
	} else {
		m.byID[id] = list
	}
}

// roleOfLocked is the role under which a connection other than c holds id,
// or "". m.mu is held.
// roleOfLocked 返回 c 之外的连接持有 id 时的角色，没有则为 ""。调用方持有 m.mu。
func (m *Mesh) roleOfLocked(id string, c *conn) string {
	for _, o := range m.byID[id] {
		if o == c {
			continue
		}
		o.mu.Lock()
		role := o.peer.Role
		o.mu.Unlock()
		return role
	}
	return ""
}

// register records the peer identity of c, already validated with checkPeer.
// register 记录 c 的对端身份（已用 checkPeer 校验）。
func (m *Mesh) register(c *conn, id model.Identity) error {
	peer := model.PeerConn{Identity: id, ConnID: c.id, Direction: c.dir, Remote: c.remote, ConnectedAt: time.Now().Unix()}
	if c.target != nil {
		peer.Target = c.target.addr
	} else {
		peer.TokenID, peer.SharedPassword = c.token, c.token == ""
	}
	m.mu.Lock()
	// An ID is one process's: one that another connection holds under
	// another role is someone claiming it.
	// ID 属于一个进程：另一条连接以其他角色持有的 ID 是有人在冒用。
	if other := m.roleOfLocked(id.ID, c); other != "" && other != id.Role {
		m.mu.Unlock()
		return fmt.Errorf("id %s is connected already as a %s", id.ID, other)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		m.mu.Unlock()
		return wire.ErrClosed
	}
	if c.registered {
		m.unindexLocked(c, c.peer.ID)
	} else {
		close(c.ready)
	}
	c.registered, c.peer = true, peer
	c.mu.Unlock()
	m.byID[id.ID] = append(slices.Clone(m.byID[id.ID]), c)
	m.mu.Unlock()
	if m.draining.Load() {
		_ = c.wc.Drain() // connected while shutting down / 在下线期间建立的连接
	}
	m.log(slog.LevelInfo, "mesh peer connected", "role", id.Role, "id", id.ID, "name", id.Name, "dir", c.dir, "remote", peer.Remote)
	for _, fn := range m.onUp {
		go fn(peer)
	}
	return nil
}

// checkPeer validates the identity a peer announced at hello: its role must be in AcceptRoles and differ from this
// process's own role (same-role peers never connect), and it must not be this process itself.
// checkPeer 校验对端在 hello 中声明的身份：其角色必须在 AcceptRoles 中且与本进程角色不同（同角色对端从不互联），
// 且不能是本进程自身。
func (m *Mesh) checkPeer(id model.Identity) error {
	if id.ID == "" || id.Role == "" {
		return errors.New("missing role or id")
	}
	self := m.Self()
	switch {
	case id.ID == self.ID:
		return errors.New("connected to itself")
	case id.Role == self.Role:
		return fmt.Errorf("role %s is not accepted by %s: peers of the same role never connect", id.Role, self.Role)
	case id.Role == model.RoleLocal || !slices.Contains(m.opts.AcceptRoles, id.Role):
		return fmt.Errorf("role %s is not accepted by %s", id.Role, self.Role)
	}
	return nil
}

// handleHello serves MethodHello on an incoming connection.
// handleHello 在入站连接上处理 MethodHello。
func (m *Mesh) handleHello(c *conn, payload []byte) ([]byte, error) {
	var id model.Identity
	if err := json.Unmarshal(payload, &id); err != nil {
		return nil, BadRequest("bad hello")
	}
	err := m.checkPeer(id)
	if err == nil && !slices.Contains(c.roles, id.Role) {
		// The password the dialer proved is not the role's: a proxy that holds a node's password does not say hello as a
		// node.
		// 拨号方证明的密码不是该角色的：持有节点密码的代理不能以节点身份 hello。
		err = fmt.Errorf("role %s: the password this link proved is not the %s password of this listener", id.Role, id.Role)
	}
	if err == nil && c.token != "" {
		// A token binds the first ID that says hello with it: another token cannot take the ID over.
		// 令牌绑定第一个用它 hello 的 ID：其他令牌无法接管该 ID。
		err = m.admitToken(c, id.ID)
	}
	if err == nil {
		err = m.register(c, id)
	}
	if err != nil {
		m.log(slog.LevelWarn, "mesh: hello refused", "role", id.Role, "id", id.ID, "remote", c.remote, "error", err)
		// Close once the refusal has gone out.
		// 拒绝应答发出之后再关闭。
		time.AfterFunc(time.Second, c.wc.Close)
		return nil, Errorf(model.CodeForbidden, "%s", err.Error())
	}
	return json.Marshal(m.Self())
}

// admitToken records c, which proved its token, as the link of peer id: the token must still be good, and id not
// bound to another token. A connection closed meanwhile releases it.
// admitToken 把证明了令牌的 c 记为对端 id 的连接：令牌须仍然有效，且 id 未绑定到其他令牌。期间已关闭的连接会释放它。
func (m *Mesh) admitToken(c *conn, id string) error {
	use, err := m.tokens.Admit(c.token, id, c.remote, c.wc.Close)
	if err != nil {
		return err
	}
	c.mu.Lock()
	old := c.use
	closed := c.closed
	if !closed {
		c.use = use
	}
	c.mu.Unlock()
	old.Release()
	if closed {
		use.Release()
		return wire.ErrClosed
	}
	return nil
}

// hello says hello on an outbound connection, retrying until it succeeds or the connection is gone.
// hello 在出站连接上发送 hello，失败重试，直到成功或连接断开。
func (m *Mesh) hello(c *conn) {
	t := c.target
	for i := 0; !m.stopped(); i++ {
		if !c.live() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		id, err := call[model.Identity](ctx, c.wc, model.MethodHello, m.Self())
		cancel()
		refused := false
		if err == nil {
			// The peer answered: a bad identity will not change by retrying at once.
			// 对端已应答：身份不符时立即重试也不会改变结果。
			refused = true
			err = m.checkPeer(id)
			if err == nil && id.Role != t.role {
				err = fmt.Errorf("%s announced role %s, but it is configured as a %s peer", t.addr, id.Role, t.role)
			}
			if err == nil {
				err = m.register(c, id)
			}
			if err == nil {
				t.setErr("")
				return
			}
		}
		if !c.live() {
			return
		}
		t.setErr(err.Error())
		var we *wire.Error
		if errors.As(err, &we) || refused {
			// Refused (wrong role, connected to itself, ...): retrying at once would only repeat it, so drop the
			// connection and dial again later.
			// 被拒绝（角色不对、连到自身等）：立即重试只会重复失败，因此断开连接，稍后再拨号。
			m.log(slog.LevelWarn, "mesh: hello refused, dialing again later", "addr", t.addr, "retry_after", refusedRetry, "error", err)
			t.mu.Lock()
			t.nextDelay = refusedBackoff(m.o().RetryInterval)
			t.mu.Unlock()
			c.wc.Close()
			return
		}
		if i%10 == 0 {
			m.log(slog.LevelWarn, "mesh: hello failed", "addr", t.addr, "error", err)
		}
		select {
		case <-m.stopCh:
			return
		case <-c.wc.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// ---- Serving ----
// ---- 服务 ----

// handlers serves what the peer sends on c.
// handlers 处理对端在 c 上发来的请求、流与推送。
func (m *Mesh) handlers(c *conn) wire.Handlers {
	return wire.Handlers{
		Request: func(ctx context.Context, req *wire.Request) ([]byte, error) {
			if req.Method == model.MethodHello {
				if c.dir != "in" {
					return nil, Errorf(model.CodeForbidden, "hello is for incoming connections")
				}
				return m.handleHello(c, req.Payload)
			}
			caller, err := m.caller(ctx, c)
			if err != nil {
				return nil, err
			}
			data, err := m.router.Invoke(ctx, caller, req.Method, req.Payload)
			if err != nil {
				return nil, wireErr(req.Method, err)
			}
			return json.Marshal(data)
		},
		Stream: func(ctx context.Context, st *wire.Stream) ([]byte, error) {
			method := st.Head().Method
			caller, err := m.caller(ctx, c)
			if err != nil {
				return nil, err
			}
			data, err := m.router.InvokeStream(ctx, caller, method, st.Payload(), st)
			if err != nil {
				return nil, wireErr(method, err)
			}
			return json.Marshal(data)
		},
		Push: func(msg *wire.Request) {
			// On an outbound connection the peer may push right after answering hello, before this side has
			// registered the connection: wait for it like requests do (pushes run one at a time, off the read loop).
			// 出站连接上对端可能在应答 hello 后立即推送，而本端尚未注册该连接：与请求一样等待注册（推送逐个执行，不在读循环中）。
			caller, err := m.caller(context.Background(), c)
			ok := err == nil
			m.mu.RLock()
			pr, found := m.pushes[msg.Method]
			m.mu.RUnlock()
			switch {
			case !ok:
				m.log(slog.LevelWarn, "mesh: push before hello dropped", "method", msg.Method, "remote", c.remote)
			case !found:
				m.log(slog.LevelWarn, "mesh: push of an unknown method dropped", "method", msg.Method, "from", caller.ID)
			case !slices.Contains(pr.roles, caller.Role):
				m.log(slog.LevelWarn, "mesh: push refused", "method", msg.Method, "role", caller.Role, "from", caller.ID)
			default:
				pr.fn(caller, msg.Payload)
			}
		},
		Drain: func() {
			c.mu.Lock()
			peer := c.peer
			c.mu.Unlock()
			m.log(slog.LevelInfo, "mesh peer draining: no new calls on this connection", "role", peer.Role, "id", peer.ID,
				"dir", c.dir, "remote", c.remote)
		},
	}
}

// caller returns the identity of the peer on c. On an outbound connection the peer may call right after answering
// our hello, before its reply has been processed here, so it waits briefly for the registration.
// caller 返回 c 上对端的身份。在出站连接上，对端可能在应答我们的 hello 后立即发起调用，而此时应答尚未在本端处理完，
// 所以短暂等待注册完成。
func (m *Mesh) caller(ctx context.Context, c *conn) (model.Identity, error) {
	if c.dir == "out" {
		select {
		case <-c.ready:
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.registered {
		return model.Identity{}, Errorf(model.CodeUnauthorized, "hello first")
	}
	return c.peer.Identity, nil
}

// ---- Calls ----
// ---- 调用 ----

// pick returns a usable connection to peer id. All registered connections of a peer, whichever side dialed them,
// form its connection pool, and calls go round robin over it, skipping draining ones: with only draining
// connections left the peer counts as offline, so new calls fail at once (ErrOffline) instead of waiting for it.
// pick 返回到对端 id 的一条可用连接。对端的所有已注册连接（无论由哪一方发起）构成其连接池，调用在其中轮询，跳过正在下线的
// 连接：只剩下线中的连接时该对端视为离线，新调用立即失败（ErrOffline），而不是等待它。
func (m *Mesh) pick(id string) *wire.Conn {
	m.mu.RLock()
	list := m.byID[id]
	m.mu.RUnlock()
	n := uint64(len(list))
	if n == 0 {
		return nil
	}
	start := m.round.Add(1)
	for i := uint64(0); i < n; i++ {
		if c := list[(start+i)%n]; c.usable() {
			return c.wc
		}
	}
	return nil
}

// Online reports whether peer id has a usable (live, not draining) connection.
// Online 判断对端 id 是否有可用（存活且未下线）的连接。
func (m *Mesh) Online(id string) bool { return m.pick(id) != nil }

// WaitOnline waits up to d (and ctx) for peer id to have a usable connection, e.g. while it reconnects.
// WaitOnline 最多等待 d（且受 ctx 约束），直到对端 id 有可用连接，例如在其重连期间。
func (m *Mesh) WaitOnline(ctx context.Context, id string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for !m.Online(id) {
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-m.stopCh:
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return true
}

// call sends method with a JSON request on wc and decodes the JSON reply into T. A context without a deadline gets
// DefaultTimeout. Business errors come back as *wire.Error; transport errors are wrapped with the method name.
// call 在 wc 上以 JSON 请求调用 method，并把 JSON 应答解码为 T。context 无截止时间时使用 DefaultTimeout。
// 业务错误以 *wire.Error 返回；传输错误带上方法名包装。
func call[T any](ctx context.Context, wc *wire.Conn, method string, req any) (T, error) {
	var zero T
	payload, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("rpc %s: encode: %w", method, err)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	data, err := wc.Call(ctx, wire.Head{Method: method}, payload)
	if err != nil {
		var we *wire.Error
		if errors.As(err, &we) {
			return zero, we
		}
		return zero, fmt.Errorf("rpc %s: %w", method, err)
	}
	if len(data) == 0 || string(data) == "null" {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return zero, fmt.Errorf("rpc %s: decode: %w", method, err)
	}
	return out, nil
}

// Call calls method on peer id and decodes the reply data into T.
// Call 在对端 id 上调用 method，并把应答数据解码为 T。
func Call[T any](ctx context.Context, m *Mesh, id, method string, req any) (T, error) {
	wc := m.pick(id)
	if wc == nil {
		var zero T
		return zero, ErrOffline
	}
	return call[T](ctx, wc, method, req)
}

// CallRaw calls method on peer id with a raw JSON request and returns the raw reply data.
// CallRaw 以原始 JSON 请求在对端 id 上调用 method，并返回原始应答数据。
func (m *Mesh) CallRaw(ctx context.Context, id, method string, req json.RawMessage) (json.RawMessage, error) {
	if len(req) == 0 {
		req = json.RawMessage("{}")
	}
	return Call[json.RawMessage](ctx, m, id, method, req)
}

// OpenStream opens the stream method on peer id with a JSON request. Read with st.Recv until io.EOF (the handler's
// JSON result is then st.Result()) or an error (a *wire.Error keeps the handler's code); ending ctx cancels the
// stream.
// OpenStream 以 JSON 请求在对端 id 上打开流方法。用 st.Recv 读取直到 io.EOF（此时处理函数的 JSON 结果为 st.Result()）
// 或错误（*wire.Error 保留处理函数的错误码）；ctx 结束即取消流。
func (m *Mesh) OpenStream(ctx context.Context, id, method string, req any) (*wire.Stream, error) {
	wc := m.pick(id)
	if wc == nil {
		return nil, ErrOffline
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("stream %s: encode: %w", method, err)
	}
	return wc.OpenStream(ctx, wire.Head{Method: method}, payload)
}

// Push sends a one-way JSON message to peer id; the peer handles it with the function registered by OnPush.
// Push 向对端 id 发送单向 JSON 消息；对端用 OnPush 注册的函数处理。
func (m *Mesh) Push(id, method string, msg any) error {
	wc := m.pick(id)
	if wc == nil {
		return ErrOffline
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("push %s: encode: %w", method, err)
	}
	return wc.Push(wire.Head{Method: method}, payload)
}

// Peers returns one entry per live registered connection.
// Peers 返回每条存活的已注册连接。
func (m *Mesh) Peers() []model.PeerConn {
	m.mu.RLock()
	var list []*conn
	for _, l := range m.byID {
		list = append(list, l...)
	}
	m.mu.RUnlock()
	out := make([]model.PeerConn, 0, len(list))
	for _, c := range list {
		c.mu.Lock()
		p := c.peer
		c.mu.Unlock()
		p.Draining = c.wc.Draining()
		p.Link.BytesIn, p.Link.BytesOut = c.wc.Traffic()
		p.Link.RTT = c.wc.RTT()
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ConnID < out[j].ConnID
	})
	return out
}

// PeersOf returns the distinct identities of the connected peers with the given role.
// PeersOf 返回指定角色的已连接对端的不重复身份。
func (m *Mesh) PeersOf(role string) []model.Identity {
	seen := map[string]bool{}
	var out []model.Identity
	for _, p := range m.Peers() {
		if p.Role == role && !seen[p.ID] {
			seen[p.ID] = true
			out = append(out, p.Identity)
		}
	}
	return out
}

// Kick closes every connection to peer id and returns how many there were. Outbound ones redial.
// Kick 关闭到对端 id 的所有连接，返回连接数。出站连接随后重连。
func (m *Mesh) Kick(id string) int {
	m.mu.RLock()
	list := slices.Clone(m.byID[id])
	m.mu.RUnlock()
	for _, c := range list {
		c.wc.Close()
	}
	return len(list)
}
