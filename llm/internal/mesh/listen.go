package mesh

import (
	"crypto/tls"
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
// engine, serving only WSPath. A non-empty Addr needs a password for at
// least one role the mesh accepts (see credentials). The new
// configuration is kept even when it cannot bind: the error is returned and
// shown in Listen().Error, and the role runs without a listener until it is
// fixed. Setting what already runs, with the same limits, does nothing but
// read the cert_file and key_file of a listener with them again: a
// certificate rotated on disk is taken by the links that open from then on,
// and those open stay (see reloadCertLocked).
func (m *Mesh) SetListen(cfg model.ListenConfig) error {
	if cfg.Addr != "" {
		addr, err := ValidateAddr(cfg.Addr, false)
		if err != nil {
			return err
		}
		cfg.Addr = addr
		if host, _, _ := net.SplitHostPort(addr); !Loopback(host) && !cfg.TLS() {
			return BadRequest("%s is not a loopback address: set cert_file and key_file, or self_signed, so that links are taken over TLS and peers can check whose listener they reach", addr)
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
		if running && cfg.CertFile != "" {
			return m.reloadCertLocked(cfg)
		}
		return nil
	}
	m.stopListenerLocked()
	m.lcfg, m.lerr, m.lopts, m.lfp = cfg, "", opts, ""
	if cfg.Addr == "" {
		m.log(slog.LevelInfo, "mesh: not listening")
		return nil
	}
	if m.opts.Engine == nil {
		m.lerr = "no engine to listen on"
		return Errorf(model.CodeInternal, "%s", m.lerr)
	}
	tc, fp, err := serverTLS(cfg)
	if err != nil {
		m.lerr = err.Error()
		return err
	}
	if tc != nil && cfg.CertFile != "" {
		// The files' certificate is swapped in place when they change, for
		// the handshakes after.
		m.lcert.Store(&tc.Certificates[0])
		tc.Certificates = nil
		tc.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return m.lcert.Load(), nil }
	}
	creds := m.credentials(cfg)
	wc := m.connConfig(nil, creds)
	wc.TLS = tc
	l := wire.NewHandler(wc)
	i, err := m.opts.Engine.AddBinding(fib.Binding{Addr: cfg.Addr, Handler: l})
	if err != nil {
		m.lerr = "listen " + cfg.Addr + ": " + err.Error()
		return Errorf(model.CodeInternal, "%s", m.lerr)
	}
	m.binding, m.lst, m.lfp = i, &l, fp
	if addrs, err := m.opts.Engine.BindingAddrs(i); err == nil && len(addrs) > 0 {
		m.laddr = addrs[0].String()
	}
	if tc != nil {
		m.log(slog.LevelInfo, "mesh listening over TLS (wss://); peers pin its certificate by this fingerprint, or check it with ca_file",
			"addr", m.laddr, "path", WSPath, "fingerprint", fp)
	} else {
		m.log(slog.LevelInfo, "mesh listening", "addr", m.laddr, "path", WSPath)
	}
	if len(creds) == 0 && m.tokens.Len() == 0 {
		m.log(slog.LevelWarn, "mesh: the listener has no password and no token yet: it takes no link until its pages, its Admin API module or fit token issue one",
			"addr", m.laddr)
	}
	for _, c := range creds {
		if len(c.Roles) > 1 {
			m.log(slog.LevelWarn, "mesh: the listener takes one password from several roles: whoever holds it may link as any of them; give each role a password of its own",
				"addr", m.laddr, "roles", strings.Join(c.Roles, ","))
		}
	}
	return nil
}

// reloadCertLocked reads the running listener's cert_file and key_file
// again, and, when they hold another certificate, has the handshakes from
// now on present it; the links open keep theirs. Files that do not load, as
// when one is written and the other not yet, leave the listener as it is,
// running with the certificate it has, which is logged; their next change
// is read again. lmu is held.
func (m *Mesh) reloadCertLocked(cfg model.ListenConfig) error {
	tc, fp, err := serverTLS(cfg)
	if err != nil {
		m.log(slog.LevelWarn, "mesh: the listener's certificate files do not load; it keeps the certificate it has", "addr", m.laddr, "error", err.Error())
		return nil
	}
	if fp == m.lfp {
		return nil
	}
	m.lcert.Store(&tc.Certificates[0])
	m.lfp = fp
	m.log(slog.LevelInfo, "mesh: the listener's certificate changed on disk; links from now on are taken with it, and peers that pin it need its new fingerprint",
		"addr", m.laddr, "fingerprint", fp)
	return nil
}

// credentials are the passwords a listener of cfg takes, one per distinct
// password, each with the roles the mesh accepts whose password it is (see
// model.ListenConfig.PasswordFor); a role without one is not taken.
func (m *Mesh) credentials(cfg model.ListenConfig) []wire.Credential {
	var out []wire.Credential
	for _, role := range m.opts.AcceptRoles {
		pw := cfg.PasswordFor(role)
		if pw == "" || role == model.RoleLocal {
			continue
		}
		i := slices.IndexFunc(out, func(c wire.Credential) bool { return string(c.Password) == pw })
		if i < 0 {
			out = append(out, wire.Credential{Password: wire.Password(pw)})
			i = len(out) - 1
		}
		out[i].Roles = append(out[i].Roles, role)
	}
	return out
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

// Listen returns the listener as the pages show it: no password and no key,
// the fingerprint of its certificate, the last failure to listen.
func (m *Mesh) Listen() model.ListenState {
	m.lmu.Lock()
	defer m.lmu.Unlock()
	return model.ListenState{Addr: m.lcfg.Addr, HasPassword: m.lcfg.HasPassword(), Error: m.lerr,
		TLS: m.lcfg.TLS(), CertFile: m.lcfg.CertFile, KeyFile: m.lcfg.KeyFile, SelfSigned: m.lcfg.SelfSigned, Fingerprint: m.lfp}
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
