package mesh

import (
	"log/slog"
	"net"
	"strconv"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/wire"
)

// WSPath is the WebSocket path of the mesh listener.
const WSPath = wire.Path

// SetListen replaces the listener: the current one stops (its binding is
// removed and its inbound links closed; their dialers link again) and, unless
// cfg.Addr is empty, a new one starts as a binding of its own on the
// engine, serving only WSPath. A non-empty Addr needs a Password. The new
// configuration is kept even when it cannot bind: the error is returned and
// shown in Listen().Error, and the role runs without a listener until it is
// fixed. Setting what already runs, with the same limits, does nothing.
func (m *Mesh) SetListen(cfg model.ListenConfig) error {
	if cfg.Addr != "" {
		addr, err := ValidateAddr(cfg.Addr, false)
		if err != nil {
			return err
		}
		cfg.Addr = addr
		if cfg.Password == "" {
			return BadRequest("a listener needs a password")
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
	l := wire.NewHandler(m.connConfig(nil, cfg.Password))
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
	return model.ListenState{Addr: m.lcfg.Addr, HasPassword: m.lcfg.Password != "", Error: m.lerr}
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
