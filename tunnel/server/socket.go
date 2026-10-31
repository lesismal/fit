package server

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/transport/ws"
	"github.com/lesismal/fit/tunnel/internal/wscrypt"
)

// The console socket, fibhub's encrypted web page WebSocket: each WebSocket,
// once the hellos are exchanged, gets a TCP connection of its own to the
// console, and the two pass on what they receive, the WebSocket's messages
// opened, the console's bytes sealed, one message per piece of at most
// sealChunk. Each side's reads wait for the other's backlog (rule 8), and
// what arrives before the console is connected holds the WebSocket's
// reads.

// sealChunk is the most of the console's bytes one message carries.
const sealChunk = 64 << 10

// socketDialTimeout bounds the connect to the console.
const socketDialTimeout = 5 * time.Second

// socket is the console socket's binding.
type socket struct {
	address string
	index   int
	h       *socketHandler
}

// applySocket moves the console socket to cfg, which may be nil for none:
// it listens anew only when its address changes. amu is held.
func (s *Server) applySocket(cfg *config.TunnelConsoleSocket) error {
	if s.socket != nil && cfg != nil && s.socket.address == cfg.Address {
		s.socket.h.max.Store(int64(cfg.Conns()))
		return nil
	}
	if s.socket != nil {
		_ = s.h.Engine.RemoveBinding(s.socket.index)
		s.socket.h.closeAll()
		s.socket = nil
		s.lmu.Lock()
		s.socketAddr = ""
		s.lmu.Unlock()
	}
	if cfg == nil {
		return nil
	}
	h := &socketHandler{s: s, conns: map[*fib.Connection]*proxy{}}
	h.max.Store(int64(cfg.Conns()))
	i, err := s.h.Engine.AddBinding(fib.Binding{Addr: cfg.Address, Handler: ws.NewServer(h, "")})
	if err != nil {
		return err
	}
	s.socket = &socket{address: cfg.Address, index: i, h: h}
	if addrs, err := s.h.Engine.BindingAddrs(i); err == nil && len(addrs) > 0 {
		s.lmu.Lock()
		s.socketAddr = addrs[0].String()
		s.lmu.Unlock()
		s.log.Infof("console over an encrypted WebSocket on ws://%s/", addrs[0])
	}
	return nil
}

// SocketAddr returns where the console socket listens, or "".
func (s *Server) SocketAddr() string {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	return s.socketAddr
}

// socketHandler serves the WebSockets once they are open: ws.Server calls
// it with each one's messages.
type socketHandler struct {
	s   *Server
	max atomic.Int64

	mu     sync.Mutex
	conns  map[*fib.Connection]*proxy
	closed bool
}

// proxy is one WebSocket and its connection to the console.
type proxy struct {
	h      *socketHandler
	ws     *fib.Connection
	wsGate *common.Gate
	hello  []byte
	secret wscrypt.Secret

	mu      sync.Mutex
	sess    *wscrypt.Session
	up      *fib.Connection
	upGate  *common.Gate
	dialing bool
	pending [][]byte
	closed  bool
}

func (h *socketHandler) OnOpen(c *fib.Connection) {
	set := h.s.settings()
	h.mu.Lock()
	if h.closed || int64(len(h.conns)) >= h.max.Load() || set.secret == nil {
		h.mu.Unlock()
		_ = c.Close()
		return
	}
	p := &proxy{h: h, ws: c, wsGate: common.NewGate(c), hello: wscrypt.Hello(), secret: set.secret}
	h.conns[c] = p
	h.mu.Unlock()
	_ = c.Send(p.hello)
}

func (h *socketHandler) get(c *fib.Connection) *proxy {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[c]
}

func (h *socketHandler) OnData(c *fib.Connection, data []byte) {
	if p := h.get(c); p != nil {
		p.message(data)
	}
}

func (h *socketHandler) OnPriorityData(*fib.Connection, []byte) {}

func (h *socketHandler) OnClose(c *fib.Connection, _ error) {
	h.mu.Lock()
	p := h.conns[c]
	delete(h.conns, c)
	h.mu.Unlock()
	if p != nil {
		p.close()
	}
}

// closeAll closes every WebSocket, as the socket stops.
func (h *socketHandler) closeAll() {
	h.mu.Lock()
	h.closed = true
	conns := make([]*fib.Connection, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// message is the client's hello, and then what the browser sends,
// encrypted.
func (p *proxy) message(data []byte) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if p.sess == nil {
		sess, err := wscrypt.NewSession(p.secret, p.hello, data, true)
		p.sess = sess
		p.mu.Unlock()
		if err != nil {
			_ = p.ws.Close()
		}
		return
	}
	plain, err := p.sess.Open(data)
	if err != nil {
		p.mu.Unlock()
		p.h.s.log.Warnf("console socket: a message from %s failed to decrypt; is the password right?", p.ws.RemoteAddr())
		_ = p.ws.Close()
		return
	}
	if up := p.up; up != nil {
		p.mu.Unlock()
		_ = up.Send(plain)
		p.wsGate.Pace(up)
		return
	}
	// The console is not connected yet: what arrives waits, and the
	// WebSocket's reads with it.
	p.pending = append(p.pending, plain)
	p.wsGate.Hold()
	dial := !p.dialing
	p.dialing = true
	p.mu.Unlock()
	if !dial {
		return
	}
	addr := consoleAddr(p.h.s.console.Addr())
	if addr == "" {
		_ = p.ws.Close()
		return
	}
	err = p.h.s.h.Engine.DialWithHandler("tcp", addr, socketDialTimeout, &socketUpstream{p: p}, func(_ *fib.Connection, err error) {
		if err != nil {
			_ = p.ws.Close()
		}
	})
	if err != nil {
		_ = p.ws.Close()
	}
}

// consoleAddr is where the console is dialed: its address, with an
// unspecified host the loopback.
func consoleAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func (p *proxy) close() {
	p.mu.Lock()
	p.closed = true
	up := p.up
	p.pending = nil
	p.mu.Unlock()
	if up != nil {
		up.CloseAfterSend()
	}
}

// socketUpstream is the handler of a proxy's connection to the console.
type socketUpstream struct{ p *proxy }

func (u *socketUpstream) OnOpen(c *fib.Connection) {
	p := u.p
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.up, p.upGate = c, common.NewGate(c)
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()
	for _, b := range pending {
		_ = c.Send(b)
	}
	p.wsGate.Release()
	p.wsGate.Pace(c)
}

// OnData seals what the console sends, a message per piece.
func (u *socketUpstream) OnData(_ *fib.Connection, data []byte) {
	p := u.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.sess == nil {
		return
	}
	for len(data) > 0 {
		n := min(len(data), sealChunk)
		// Sealed and sent under the lock, so that the messages go out in
		// the order of their numbers.
		_ = p.ws.Send(p.sess.Seal(data[:n]))
		data = data[n:]
	}
	p.upGate.Pace(p.ws)
}

func (u *socketUpstream) OnPriorityData(*fib.Connection, []byte) {}

func (u *socketUpstream) OnClose(*fib.Connection, error) {
	u.p.ws.CloseAfterSend()
}
