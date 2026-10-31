package client

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/transport/ws"
	"github.com/lesismal/fit/tunnel/internal/wscrypt"
)

// A web proxy serves a server's console pages on a local address, through
// the server's console socket: each connection to the address gets a
// WebSocket of its own to the socket, which carries what the two exchange,
// encrypted with the socket's password, one message per piece of at most
// sealChunk. Each side's reads wait for the other's backlog (rule 8), and
// the local connection's wait for the WebSocket to open.

// sealChunk is the most of the local connection's bytes one message
// carries.
const sealChunk = 64 << 10

// proxyDialTimeout bounds the dial of a WebSocket and its handshake.
const proxyDialTimeout = 10 * time.Second

// proxyCfg is a web proxy's settings with the secret of its password.
type proxyCfg struct {
	cfg    *config.TunnelWebProxy
	secret wscrypt.Secret
}

// webProxy is a web proxy of the config and its binding: listening on
// addr, or failed to with err.
type webProxy struct {
	c     *Client
	set   atomic.Pointer[proxyCfg]
	index int
	addr  string
	err   string

	mu     sync.Mutex
	conns  map[*fib.Connection]*proxyConn
	closed bool
}

// applyProxies listens for the web proxies the config adds, stops those it
// leaves out, and moves the rest to their new settings, for the connections
// they accept from now on. amu is held.
func (c *Client) applyProxies(want []config.TunnelWebProxy) []error {
	c.mu.Lock()
	have := map[string]*webProxy{}
	for _, p := range c.proxies {
		have[p.set.Load().cfg.Listen] = p
	}
	c.mu.Unlock()
	keep := map[*webProxy]bool{}
	for _, pc := range want {
		if p := have[pc.Listen]; p != nil && p.index >= 0 {
			keep[p] = true
		}
	}
	for _, p := range have {
		if !keep[p] && p.index >= 0 {
			p.close()
		}
	}
	var errs []error
	next := make([]*webProxy, 0, len(want))
	for i := range want {
		pc := &want[i]
		if p := have[pc.Listen]; p != nil && keep[p] {
			old := p.set.Load()
			set := &proxyCfg{cfg: pc, secret: old.secret}
			if old.cfg.Password != pc.Password {
				var err error
				if set.secret, err = wscrypt.NewSecret(pc.Password); err != nil {
					errs = append(errs, fmt.Errorf("web proxy %s: %w", pc.Listen, err))
					set.secret = old.secret
					set.cfg = old.cfg
				}
			}
			p.set.Store(set)
			next = append(next, p)
			continue
		}
		p := c.startProxy(pc)
		if p.err != "" {
			errs = append(errs, fmt.Errorf("web proxy %s: %s", pc.Listen, p.err))
		}
		next = append(next, p)
	}
	c.mu.Lock()
	c.proxies = next
	c.mu.Unlock()
	return errs
}

// startProxy listens for pc.
func (c *Client) startProxy(pc *config.TunnelWebProxy) *webProxy {
	p := &webProxy{c: c, index: -1, conns: map[*fib.Connection]*proxyConn{}}
	secret, err := wscrypt.NewSecret(pc.Password)
	p.set.Store(&proxyCfg{cfg: pc, secret: secret})
	if err != nil {
		p.err = err.Error()
		return p
	}
	i, err := c.h.Engine.AddBinding(fib.Binding{Addr: pc.Listen, Handler: p})
	if err != nil {
		p.err = err.Error()
		c.log.Warnf("web proxy %s: %v", pc.Listen, err)
		return p
	}
	p.index = i
	if addrs, err := c.h.Engine.BindingAddrs(i); err == nil && len(addrs) > 0 {
		p.addr = addrs[0].String()
	}
	c.log.Infof("serving the console of %s on http://%s", pc.URL, p.addr)
	return p
}

// close stops listening, and closes every connection.
func (p *webProxy) close() {
	_ = p.c.h.Engine.RemoveBinding(p.index)
	p.mu.Lock()
	p.closed = true
	conns := make([]*fib.Connection, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// proxyConn is a local connection and its WebSocket.
type proxyConn struct {
	p      *webProxy
	local  *fib.Connection
	lgate  *common.Gate
	secret wscrypt.Secret

	mu      sync.Mutex
	ws      *fib.Connection
	wsGate  *common.Gate
	sess    *wscrypt.Session
	pending [][]byte
	closed  bool
}

func (p *webProxy) OnOpen(local *fib.Connection) {
	set := p.set.Load()
	p.mu.Lock()
	if p.closed || len(p.conns) >= set.cfg.Conns() {
		p.mu.Unlock()
		_ = local.Close()
		return
	}
	pc := &proxyConn{p: p, local: local, lgate: common.NewGate(local), secret: set.secret}
	p.conns[local] = pc
	p.mu.Unlock()
	local.SetAttachment(pc)
	// The local connection waits for the WebSocket.
	pc.lgate.Hold()
	dial, err := ws.Dialer(p.c.h.Engine, set.cfg.URL, proxyDialTimeout, &proxyWS{pc: pc})
	if err == nil {
		err = dial(func(_ *fib.Connection, err error) {
			if err != nil {
				p.c.log.Warnf("web proxy %s: %s: %v", set.cfg.Listen, set.cfg.URL, err)
				_ = local.Close()
			}
		})
	}
	if err != nil {
		_ = local.Close()
	}
}

// OnData seals what the local connection sends, once the session is up.
func (p *webProxy) OnData(local *fib.Connection, data []byte) {
	pc, ok := local.Attachment().(*proxyConn)
	if !ok {
		return
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.closed {
		return
	}
	if pc.sess == nil {
		// What came before the hold took effect: one read at most.
		pc.pending = append(pc.pending, append([]byte(nil), data...))
		return
	}
	pc.sendLocked(data)
}

// sendLocked seals data, a message per piece, and sends it, in the order of
// the messages' numbers. pc.mu is held.
func (pc *proxyConn) sendLocked(data []byte) {
	for len(data) > 0 {
		n := min(len(data), sealChunk)
		_ = pc.ws.Send(pc.sess.Seal(data[:n]))
		data = data[n:]
	}
	pc.lgate.Pace(pc.ws)
}

func (p *webProxy) OnPriorityData(*fib.Connection, []byte) {}

func (p *webProxy) OnClose(local *fib.Connection, _ error) {
	p.mu.Lock()
	pc := p.conns[local]
	delete(p.conns, local)
	p.mu.Unlock()
	if pc == nil {
		return
	}
	pc.mu.Lock()
	pc.closed = true
	w := pc.ws
	pc.mu.Unlock()
	if w != nil {
		w.CloseAfterSend()
	}
}

// proxyWS is the handler of a proxyConn's WebSocket, which ws.Dialer
// calls with each message.
type proxyWS struct{ pc *proxyConn }

func (h *proxyWS) OnOpen(c *fib.Connection) {
	pc := h.pc
	pc.mu.Lock()
	pc.ws, pc.wsGate = c, common.NewGate(c)
	closed := pc.closed
	pc.mu.Unlock()
	if closed {
		_ = c.Close()
	}
}

// OnData is the server's hello, and then what the console sends.
func (h *proxyWS) OnData(c *fib.Connection, data []byte) {
	pc := h.pc
	pc.mu.Lock()
	if pc.closed {
		pc.mu.Unlock()
		return
	}
	if pc.sess == nil {
		hello := wscrypt.Hello()
		sess, err := wscrypt.NewSession(pc.secret, data, hello, false)
		if err != nil {
			pc.mu.Unlock()
			_ = c.Close()
			return
		}
		pc.sess = sess
		_ = c.Send(hello)
		pending := pc.pending
		pc.pending = nil
		for _, b := range pending {
			pc.sendLocked(b)
		}
		pc.mu.Unlock()
		pc.lgate.Release()
		return
	}
	plain, err := pc.sess.Open(data)
	pc.mu.Unlock()
	if err != nil {
		pc.p.c.log.Warnf("web proxy %s: a message failed to decrypt; is the password right?", pc.p.set.Load().cfg.Listen)
		_ = c.Close()
		return
	}
	_ = pc.local.Send(plain)
	pc.wsGate.Pace(pc.local)
}

func (h *proxyWS) OnPriorityData(*fib.Connection, []byte) {}

func (h *proxyWS) OnClose(*fib.Connection, error) {
	h.pc.local.CloseAfterSend()
}

// WebProxyView is a web proxy as the pages show it.
type WebProxyView struct {
	Name   string `json:"name,omitempty"`
	Listen string `json:"listen"`
	Addr   string `json:"addr,omitempty"`
	URL    string `json:"url"`
	Conns  int    `json:"conns"`
	Error  string `json:"error,omitempty"`
}

// WebProxies is the client's web proxies.
func (c *Client) WebProxies() []WebProxyView {
	c.mu.Lock()
	proxies := slices.Clone(c.proxies)
	c.mu.Unlock()
	out := make([]WebProxyView, len(proxies))
	for i, p := range proxies {
		cfg := p.set.Load().cfg
		p.mu.Lock()
		n := len(p.conns)
		p.mu.Unlock()
		out[i] = WebProxyView{Name: cfg.Name, Listen: cfg.Listen, Addr: p.addr, URL: cfg.URL, Conns: n, Error: p.err}
	}
	return out
}
