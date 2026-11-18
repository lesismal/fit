package mesh

import (
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// WSPath is the WebSocket path of the mesh listener.
const WSPath = wire.Path

// SetListen replaces the listener: the current one stops (its binding is
// removed and its inbound links closed; their dialers link again) and, unless
// cfg.Addr is empty, a new one starts as a binding of its own on the
// engine, serving only WSPath. A non-empty Addr needs the password of at
// least one role in AcceptRoles; a role without one cannot link (the config
// checks the rest, with CheckPasswords). The new configuration is kept even
// when it cannot bind: the error is returned and shown in Listen().Error, and
// the role runs without a listener until it is fixed. Setting what already
// runs, with the same limits, does nothing.
func (m *Mesh) SetListen(cfg model.ListenConfig) error {
	if cfg.Addr != "" {
		addr, err := ValidateAddr(cfg.Addr, false)
		if err != nil {
			return err
		}
		cfg.Addr = addr
		if !slices.ContainsFunc(m.opts.AcceptRoles, func(r string) bool { return cfg.Passwords.For(r) != "" }) {
			return BadRequest("a listener needs a password for at least one of %s", strings.Join(m.opts.AcceptRoles, " and "))
		}
	}
	m.lmu.Lock()
	defer m.lmu.Unlock()
	if m.stopped() {
		return wire.ErrClosed
	}
	opts := m.o()
	running := m.binding >= 0
	if cfg == m.lcfg && m.lerr == "" && running == (cfg.Addr != "") && (!running || sameLimits(opts, m.lopts)) {
		return nil
	}
	m.stopListenerLocked()
	m.lcfg, m.lerr, m.lopts = cfg, "", opts
	if cfg.Addr == "" {
		m.log(slog.LevelInfo, "mesh: not listening")
		return nil
	}
	if m.opts.Engine == nil {
		m.lerr = "no engine to listen on"
		return Errorf(model.CodeInternal, "%s", m.lerr)
	}
	// The keys are slow to derive (wire.DeriveKey): once here, not per link.
	// 密钥派生很慢（wire.DeriveKey）：在这里派生一次，而不是每条连接一次。
	pws := map[string]string{}
	for _, r := range m.opts.AcceptRoles {
		if pw := cfg.Passwords.For(r); pw != "" {
			pws[r] = pw
		}
	}
	la, err := wire.NewListenAuth(m.Self().Role, m.salt, pws)
	if err != nil {
		m.lerr = err.Error()
		return Errorf(model.CodeInternal, "%s", m.lerr)
	}
	l := wire.NewHandler(m.connConfig(nil, la))
	i, err := m.opts.Engine.AddBinding(fib.Binding{Addr: cfg.Addr, Handler: l})
	if err != nil {
		m.lerr = "listen " + cfg.Addr + ": " + err.Error()
		return Errorf(model.CodeInternal, "%s", m.lerr)
	}
	m.binding, m.lst = i, &l
	if addrs, err := m.opts.Engine.BindingAddrs(i); err == nil && len(addrs) > 0 {
		m.laddr = addrs[0].String()
	}
	m.log(slog.LevelInfo, "mesh listening", "addr", m.laddr, "path", WSPath)
	return nil
}

func sameLimits(a, b Options) bool {
	return a.MaxConns == b.MaxConns && a.MaxInflight == b.MaxInflight && a.PingInterval == b.PingInterval &&
		a.SlowTimeout == b.SlowTimeout && a.StreamIdle == b.StreamIdle
}

// stopListenerLocked stops the running listener, if any, and closes its
// inbound links. lmu is held.
func (m *Mesh) stopListenerLocked() {
	if m.binding < 0 {
		return
	}
	_ = m.opts.Engine.RemoveBinding(m.binding)
	m.lst.CloseAll()
	m.binding, m.lst, m.laddr = -1, nil, ""
	m.closeConns(func(c *conn) bool { return c.dir == "in" })
}

// ListenConfig returns the listener configuration, password included.
func (m *Mesh) ListenConfig() model.ListenConfig {
	m.lmu.Lock()
	defer m.lmu.Unlock()
	return m.lcfg
}

// ListenAddr returns where the listener listens, its port chosen when the
// configuration's was 0, or "" when it does not.
func (m *Mesh) ListenAddr() string {
	m.lmu.Lock()
	defer m.lmu.Unlock()
	return m.laddr
}

// Listen returns the listener as the pages show it: no password, the last
// failure to listen.
func (m *Mesh) Listen() model.ListenState {
	m.lmu.Lock()
	defer m.lmu.Unlock()
	st := model.ListenState{Addr: m.lcfg.Addr, Roles: []string{}, Error: m.lerr}
	for _, r := range m.lcfg.Passwords.Roles() {
		if slices.Contains(m.opts.AcceptRoles, r) {
			st.Roles = append(st.Roles, r)
		}
	}
	return st
}

// CheckPasswords says what is wrong with a listener's passwords for a role that links with accept: at least one
// role it links with needs a password, none of another role, and no two roles may share one, so that a peer of one
// role cannot link as the other.
// CheckPasswords 说明链接 accept 中角色的监听方密码有何问题：至少一个它连接的角色需要密码，不能有其他角色的密码，
// 且两个角色不能共用一个密码，使一个角色的对端无法以另一个角色连接。
func CheckPasswords(p model.RolePasswords, accept []string) error {
	seen := map[string]string{}
	for _, r := range model.Roles {
		pw := p.For(r)
		if pw == "" {
			continue
		}
		if !slices.Contains(accept, r) {
			return BadRequest("a %s password is set, but %s peers never link here", r, r)
		}
		if o, ok := seen[pw]; ok {
			return BadRequest("the %s and %s passwords are the same: each role needs its own, so that a peer of one cannot link as the other", o, r)
		}
		seen[pw] = r
	}
	if len(seen) == 0 {
		return BadRequest("a listener needs a password for at least one of %s", strings.Join(accept, " and "))
	}
	return nil
}

func (m *Mesh) checkPasswords(p model.RolePasswords) error {
	return CheckPasswords(p, m.opts.AcceptRoles)
}

// ValidateAddr checks and normalizes a host:port address; the host may be
// empty (all interfaces) only when needHost is false (a listener).
func ValidateAddr(addr string, needHost bool) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" || (needHost && host == "") {
		return "", BadRequest("address must be host:port")
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || (needHost && n == 0) {
		return "", BadRequest("invalid port %q", port)
	}
	return net.JoinHostPort(host, port), nil
}
