package mesh

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"sort"
	"time"

	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// Dial keeps a connection to req.Addr (reconnecting forever) until Undial; the peer must announce req.Role, which
// must be one of AcceptRoles. Dialing an address already dialed replaces its password and role (an empty password
// keeps the current one).
// Dial 保持到 req.Addr 的连接（持续重连），直到 Undial；对端必须声明 req.Role，且该角色须在 AcceptRoles 中。重复 Dial
// 同一地址会替换其密码与角色（密码为空表示保留当前密码）。
func (m *Mesh) Dial(req model.DialReq) error {
	addr, err := ValidateAddr(req.Addr, true)
	if err != nil {
		return err
	}
	if err := m.ValidateRole(req.Role); err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(addr)
	tc, err := clientTLS(req, host)
	if err != nil {
		return err
	}
	if m.stopped() {
		return wire.ErrClosed
	}
	var sec *linktoken.Secret
	if req.Token != "" {
		s, err := linktoken.Parse(req.Token)
		if err != nil {
			return BadRequest("%v", err)
		}
		sec = &s
	}
	m.mu.Lock()
	old := m.targets[addr]
	// A token or a password given replaces both; neither keeps the old ones.
	// 给出令牌或密码即替换两者；都不给则保留原有的。
	password, token := req.Password, req.Token
	if token != "" {
		password = ""
	}
	if password == "" && token == "" && old != nil {
		password, token, sec = old.password, old.token, old.sec
	}
	if password == "" && token == "" {
		m.mu.Unlock()
		return BadRequest("token or password required")
	}
	if old != nil {
		if old.password == password && old.token == token && old.role == req.Role && sameTLS(old.req, req) {
			m.mu.Unlock()
			return nil
		}
		delete(m.targets, addr)
	}
	t := &target{addr: addr, password: password, token: token, sec: sec, role: req.Role, req: req, tls: tc, stopCh: make(chan struct{})}
	m.targets[addr] = t
	m.wg.Add(1)
	m.mu.Unlock()
	if old != nil {
		m.stopTarget(old)
	}
	m.log(slog.LevelInfo, "mesh: dial target set", "addr", addr, "role", req.Role)
	go m.dialLoop(t)
	return nil
}

// Undial stops dialing addr and closes its connection; false if addr is not a dial target.
// Undial 停止连接 addr 并关闭其连接；addr 不是连接目标时返回 false。
func (m *Mesh) Undial(addr string) bool {
	m.mu.Lock()
	t := m.targets[addr]
	delete(m.targets, addr)
	m.mu.Unlock()
	if t == nil {
		return false
	}
	m.stopTarget(t)
	m.log(slog.LevelInfo, "mesh: dial target removed", "addr", addr)
	return true
}

// stopTarget stops dialing t and closes its connection.
// stopTarget 停止拨号 t 并关闭其连接。
func (m *Mesh) stopTarget(t *target) {
	t.mu.Lock()
	select {
	case <-t.stopCh:
	default:
		close(t.stopCh)
	}
	c := t.conn
	t.conn = nil
	t.mu.Unlock()
	if c != nil {
		c.wc.Close()
		m.disconnected(c.wc, nil)
	}
}

// waitOrStop waits d; false when t or the mesh stopped meanwhile.
// waitOrStop 等待 d；期间 t 或互联已停止时返回 false。
func (m *Mesh) waitOrStop(t *target, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-t.stopCh:
			return false
		case <-m.stopCh:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-t.stopCh:
		return false
	case <-m.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

// dial makes one connection to t and starts its hello.
// dial 建立一条到 t 的连接并开始 hello。
func (m *Mesh) dial(t *target) (*conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	go func() {
		select {
		case <-t.stopCh:
			cancel()
		case <-m.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	cfg := m.connConfig(t, nil)
	scheme := "ws://"
	if t.tls != nil {
		scheme, cfg.TLS = "wss://", t.tls
	}
	wc, err := wire.Dial(ctx, scheme+t.addr+WSPath, wire.DialConfig{ConnConfig: cfg, Engine: m.opts.Engine})
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	c := m.conns[wc]
	m.mu.RUnlock()
	if c == nil {
		return nil, errors.New("connection closed during the handshake")
	}
	t.mu.Lock()
	select {
	case <-t.stopCh:
		t.mu.Unlock()
		wc.Close()
		m.disconnected(wc, nil)
		return nil, wire.ErrClosed
	default:
	}
	t.conn = c
	t.mu.Unlock()
	go m.hello(c)
	return c, nil
}

// dialLoop keeps a connection to t until it is stopped: it dials, waits for the connection to be lost, and dials
// again. Failed dials and short-lived connections back off with jitter (backoff); after a refused hello it waits
// t.nextDelay instead. A shutting-down mesh dials no more.
//
// dialLoop 保持到 t 的连接直到 t 被停止：拨号，等待连接丢失后重新拨号。拨号失败与存活很短的连接按带抖动的退避等待
// （backoff）；hello 被拒绝后改为等待 t.nextDelay。正在下线的互联不再拨号。
func (m *Mesh) dialLoop(t *target) {
	defer m.wg.Done()
	var delay time.Duration
	fails := 0 // consecutive failed dials or short-lived connections / 连续的拨号失败或短命连接
	for {
		if !m.waitOrStop(t, delay) || m.draining.Load() {
			return
		}
		c, err := m.dial(t)
		if err != nil {
			if errors.Is(err, wire.ErrClosed) || m.stopped() {
				return
			}
			t.setErr(err.Error())
			delay = backoff(fails, m.o().RetryInterval)
			fails++
			if fails%10 == 1 || m.o().RetryInterval >= 30*time.Second {
				m.log(slog.LevelWarn, "mesh: dial failed, retrying", "addr", t.addr, "retry_in", delay.Round(time.Millisecond),
					"failures", fails, "error", err)
			}
			continue
		}
		since := time.Now()
		select {
		case <-c.wc.Done():
		case <-t.stopCh:
			return // stopTarget closes the connection / 由 stopTarget 关闭连接
		case <-m.stopCh:
			return
		}
		c.wc.Close()
		m.disconnected(c.wc, c.wc.Err())
		t.mu.Lock()
		if t.conn == c {
			t.conn = nil
		}
		next := t.nextDelay
		t.nextDelay = 0
		t.mu.Unlock()
		if time.Since(since) >= stableConn {
			fails = 0
		}
		if next > 0 {
			delay = next
		} else {
			delay = backoff(fails, m.o().RetryInterval)
			fails++
		}
		if !m.stopped() {
			m.log(slog.LevelInfo, "mesh: connection lost, dialing again", "addr", t.addr, "retry_in", delay.Round(time.Millisecond),
				"reason", c.wc.Err())
		}
	}
}

// Targets returns the dial targets and their state, without passwords.
// Targets 返回连接目标及其状态，不含密码。
func (m *Mesh) Targets() []model.DialTarget {
	m.mu.RLock()
	list := make([]*target, 0, len(m.targets))
	for _, t := range m.targets {
		list = append(list, t)
	}
	m.mu.RUnlock()
	out := make([]model.DialTarget, 0, len(list))
	for _, t := range list {
		t.mu.Lock()
		dt := model.DialTarget{Addr: t.addr, Role: t.role, Error: t.err, TLS: t.tls != nil, Pin: t.req.Pin, CAFile: t.req.CAFile,
			ServerName: t.req.ServerName}
		if t.sec != nil {
			dt.TokenID = t.sec.ID
		}
		if c := t.conn; c != nil {
			c.mu.Lock()
			if c.registered && c.live() {
				dt.Connected = true
				id := c.peer.Identity
				dt.Peer = &id
				dt.Error = ""
			}
			c.mu.Unlock()
		}
		t.mu.Unlock()
		out = append(out, dt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}

// DialReqs returns every dial target with its password, sorted by address (for persisting them).
// DialReqs 返回所有连接目标及其密码，按地址排序（用于持久化）。
func (m *Mesh) DialReqs() []model.DialReq {
	m.mu.RLock()
	out := make([]model.DialReq, 0, len(m.targets))
	for _, t := range m.targets {
		out = append(out, model.DialReq{Addr: t.addr, Password: t.password, Token: t.token, Role: t.role, Pin: t.req.Pin, CAFile: t.req.CAFile,
			ServerName: t.req.ServerName})
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}

// sameTLS reports whether a and b dial alike: the same pin, CA file and name.
// sameTLS 表示 a 与 b 的拨号方式是否相同：相同的固定指纹、CA 文件与名称。
func sameTLS(a, b model.DialReq) bool {
	return a.Pin == b.Pin && a.CAFile == b.CAFile && a.ServerName == b.ServerName
}

// ValidateRole checks that role is one this process connects to (in AcceptRoles, not its own).
// ValidateRole 校验 role 是本进程会连接的角色（在 AcceptRoles 中，且不是自身角色）。
func (m *Mesh) ValidateRole(role string) error {
	if role == "" || role == model.RoleLocal || role == m.Self().Role || !slices.Contains(m.opts.AcceptRoles, role) {
		return BadRequest("role must be one of %v", m.opts.AcceptRoles)
	}
	return nil
}
