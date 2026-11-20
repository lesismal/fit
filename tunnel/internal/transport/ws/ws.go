//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

// Package ws is the WebSocket transport of the tunnel's links, for networks
// that let only HTTP through. Importing it registers the schemes ws,
// ws://host:port/path, and wss, the same over TLS (fib's, package fib/tls),
// both to dial and to listen on. A wss listener presents the certificate
// transport.ListenOptions gives it, and a wss dial checks the hub's as the
// transport.ClientOptions given to Dial say, never as its address does: a
// dial's address has no query and no fragment (see transport.ParseURL).
//
// The WebSockets are fib's (package fib/websocket), plain RFC 6455: a link's
// sends go out as binary messages of at most maxFrame bytes, and the
// messages that arrive go to the link as if they had been read off a plain
// TCP connection. A link is a byte stream, so where its frames fall in
// messages does not matter. Server is also what the hub's console socket
// serves its WebSockets with.
package ws

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	fibtls "github.com/lesismal/fib/tls"
	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
)

func init() {
	transport.Register("ws", wsTransport{})
	transport.Register("wss", wsTransport{})
}

// ALPN is the TLS application protocol of wss: HTTP/1.1, which carries the
// opening handshake, so that a wss link may pass a TLS-terminating HTTP
// proxy.
const ALPN = "http/1.1"

const (
	// maxFrame is the most of a send one message carries; a longer send,
	// such as a link frame of up to 4 MiB, goes in several. A peer of an
	// earlier release takes messages of at most twice that.
	maxFrame = 1 << 20
	// maxMessage is the longest message a link takes.
	maxMessage = 2 * maxFrame
	// MaxHandshake bounds the opening handshake's head.
	MaxHandshake = 16 << 10
	// HandshakeTimeout bounds the opening handshake.
	HandshakeTimeout = 10 * time.Second
)

// errTimeout is the error of a connection whose opening handshake head did
// not arrive in time.
var errTimeout = errors.New("ws: websocket handshake timed out")

// wsTransport dials and listens on ws:// and wss:// URLs.
type wsTransport struct{}

// Dial opens a link to addr, a ws:// or wss:// URL without a user, a query
// or a fragment (see transport.ParseURL); a wss dial checks the hub's
// certificate as opts says, and a ws dial ignores opts. timeout bounds the
// dial and the handshake both, HandshakeTimeout when it is not positive.
func (wsTransport) Dial(engine *fib.Engine, addr string, timeout time.Duration, opts transport.ClientOptions, open transport.OpenFunc) (transport.Conn, error) {
	u, err := transport.ParseURL(addr)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = HandshakeTimeout
	}
	c := &conn{}
	cfg := websocket.DialerConfig{
		HandshakeTimeout:  timeout,
		MaxMessageBytes:   maxMessage,
		MaxHandshakeBytes: MaxHandshake,
		// The link's TCP connection is readied as it connects, before
		// the handshake.
		Dial: func(e *fib.Engine, network, addr string, timeout time.Duration, h fib.Handler, done func(*fib.Connection, error)) error {
			return e.DialWithHandler(network, addr, timeout, h, func(fc *fib.Connection, err error) {
				if fc != nil {
					prepareLink(fc)
					c.fc.Store(fc)
				}
				done(fc, err)
			})
		},
	}
	switch u.Scheme {
	case "ws":
	case "wss":
		if cfg.TLSConfig, err = transport.TLSClientConfig(u, opts, ALPN, false); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("ws: unsupported scheme %q, only ws:// and wss:// are", u.Scheme)
	}
	// Dial waits for the handshake, as transport.Dial says: on a role's
	// goroutine, never a fib worker.
	ch := make(chan error, 1)
	websocket.NewDialer(engine, cfg).Dial(u.String(), stdhttp.Header{"User-Agent": {"fit"}}, &dialed{c: c, open: open},
		func(_ *websocket.Connection, _ *stdhttp.Response, err error) { ch <- err })
	if err := <-ch; err != nil {
		return nil, err
	}
	if c.h == nil {
		return nil, transport.ErrClosedOpening
	}
	return c, nil
}

// Listen serves WebSockets on the host and port of addr, a ws:// or wss://
// URL, at its path; a URL without a path takes any. A wss listener presents
// o's certificate.
func (wsTransport) Listen(engine *fib.Engine, addr string, o transport.ListenOptions, open transport.OpenFunc) (transport.Listener, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	path := u.Path
	cfg := websocket.DefaultConfig()
	cfg.MaxMessageBytes = maxMessage
	srv := NewServer(cfg, &links{open: open}, path, prepareLink)
	switch u.Scheme {
	case "ws":
		return transport.ListenFib(engine, u.Host, srv, o.Filter, func(hostport string) string { return "ws://" + hostport + path })
	case "wss":
		if o.Cert == nil {
			return nil, errors.New("wss: a listener needs a certificate")
		}
		h := fibtls.NewServer(transport.ServerTLSConfig(*o.Cert, ALPN), srv)
		return transport.ListenFib(engine, u.Host, h, o.Filter, func(hostport string) string { return "wss://" + hostport + path })
	}
	return nil, fmt.Errorf("ws: unsupported scheme %q, only ws:// and wss:// are", u.Scheme)
}

// prepareLink readies the TCP connection of a link as it opens: write
// backpressure off and keep-alive on, as transport.FibHandler readies a
// link's; and the queue limit of a link (proto.LinkQueue), which the link
// sets itself on the connections of the other transports but cannot reach
// under a WebSocket.
func prepareLink(c *fib.Connection) {
	c.SetWriteBackpressure(false)
	_ = c.SetKeepAliveConfig(transport.KeepAlive)
	c.SetWriteQueueLimit(proto.LinkQueue)
}

// Server is a fib.Handler that serves WebSockets with fib's
// websocket.ServerHandler, adding what a binding of their own needs: a path,
// a bound on the opening handshake, and a hook for each connection as it
// opens.
type Server struct {
	ws      *websocket.ServerHandler
	prepare func(*fib.Connection)
	timeout time.Duration
	// heads is the connections whose handshake head has not all arrived,
	// each with the timer that closes it.
	heads sync.Map // *fib.Connection -> *head
}

// head is the end of what arrived of a connection's handshake head, which
// may hold the start of the "\r\n\r\n" that ends it, and the timer that
// closes the connection if the end does not come.
type head struct {
	timer *time.Timer
	tail  []byte
}

var endOfHead = []byte("\r\n\r\n")

// ended reports whether data, after what came before, ends the head.
func (h *head) ended(data []byte) bool {
	if bytes.Contains(data, endOfHead) {
		return true
	}
	n := len(endOfHead) - 1
	b := append(h.tail, data[:min(len(data), n)]...)
	if bytes.Contains(b, endOfHead) {
		return true
	}
	if len(data) >= n {
		b = data
	}
	h.tail = append([]byte(nil), b[len(b)-min(len(b), n):]...)
	return false
}

// NewServer serves h to WebSocket clients that ask for path, or for any
// path when it is empty, as cfg says; a handshake for another path is
// refused with 403 Forbidden, and any Origin is taken. A connection whose
// handshake head has not arrived within HandshakeTimeout is closed;
// prepare, unless nil, is called with each connection as it opens.
func NewServer(cfg websocket.Config, h websocket.Handler, path string, prepare func(*fib.Connection)) *Server {
	cfg.CheckOrigin = func(r *stdhttp.Request) bool { return path == "" || r.URL.Path == path }
	cfg.HTTP.MaxHeaderBytes = MaxHandshake
	return &Server{ws: websocket.NewHandlerWithConfig(cfg, h), prepare: prepare, timeout: HandshakeTimeout}
}

func (s *Server) OnOpen(c *fib.Connection) {
	if s.prepare != nil {
		s.prepare(c)
	}
	s.heads.Store(c, &head{timer: time.AfterFunc(s.timeout, func() { c.CloseWithError(errTimeout) })})
	s.ws.OnOpen(c)
}

func (s *Server) OnData(c *fib.Connection, data []byte) {
	if v, ok := s.heads.Load(c); ok {
		if h := v.(*head); h.ended(data) {
			h.timer.Stop()
			s.heads.Delete(c)
		}
	}
	s.ws.OnData(c, data)
}

func (s *Server) OnPriorityData(c *fib.Connection, data []byte) { s.ws.OnPriorityData(c, data) }

// OnShutdown sends an open WebSocket a close frame as the engine shuts
// down; see websocket.ServerHandler.OnShutdown.
func (s *Server) OnShutdown(c *fib.Connection) { s.ws.OnShutdown(c) }

func (s *Server) OnClose(c *fib.Connection, err error) {
	if v, ok := s.heads.LoadAndDelete(c); ok {
		v.(*head).timer.Stop()
	}
	s.ws.OnClose(c, err)
}

var _ fib.ShutdownHandler = (*Server)(nil)

// conn is a link's transport.Conn on a WebSocket.
type conn struct {
	ws     *websocket.Connection
	h      transport.Handler
	remote net.Addr
	// fc is the TCP connection under a dialed WebSocket, whose address is
	// the link's local one.
	fc atomic.Pointer[fib.Connection]
	// mu keeps the messages of one send together.
	mu sync.Mutex
}

// Send sends b as binary messages of at most maxFrame bytes.
func (c *conn) Send(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(b) > 0 {
		n := min(len(b), maxFrame)
		if err := c.ws.WriteMessage(websocket.Binary, b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// Close ends the WebSocket at once, without the close handshake, dropping
// what is queued.
func (c *conn) Close() error {
	c.ws.Abort(nil)
	return nil
}

// CloseAfterSend sends a close frame behind what is queued.
func (c *conn) CloseAfterSend() { _ = c.ws.Close(websocket.CloseNormal, "") }

// LocalAddr is a dialed link's local address; an accepted WebSocket does
// not tell its own, and has none.
func (c *conn) LocalAddr() net.Addr {
	if fc := c.fc.Load(); fc != nil {
		return fc.LocalAddr()
	}
	return nil
}

func (c *conn) RemoteAddr() net.Addr { return c.remote }

// opened gives c the WebSocket that opened, and the link's handler; one
// that open refuses is ended.
func (c *conn) opened(ws *websocket.Connection, open transport.OpenFunc) {
	c.ws, c.remote = ws, ws.RemoteAddr()
	if c.h = open(c); c.h == nil {
		ws.Abort(nil)
	}
}

// closeErr is why a link's WebSocket closed: err, or a close code that is
// not a plain end.
func closeErr(code uint16, reason string, err error) error {
	switch {
	case err != nil:
		return err
	case code == websocket.CloseNormal, code == websocket.CloseGoingAway, code == 1005, code == 1006:
		return nil
	}
	return fmt.Errorf("ws: closed with %d %s", code, reason)
}

// links is the websocket.Handler of a listener's WebSockets.
type links struct {
	open  transport.OpenFunc
	conns sync.Map // *websocket.Connection -> *conn
}

func (l *links) OnOpen(ws *websocket.Connection, _ *stdhttp.Request) {
	c := &conn{}
	if c.opened(ws, l.open); c.h != nil {
		l.conns.Store(ws, c)
	}
}

func (l *links) OnMessage(ws *websocket.Connection, _ websocket.Opcode, data []byte) {
	if v, ok := l.conns.Load(ws); ok {
		v.(*conn).h.OnData(data)
	}
}

func (l *links) OnClose(ws *websocket.Connection, code uint16, reason string, err error) {
	if v, ok := l.conns.LoadAndDelete(ws); ok {
		v.(*conn).h.OnClose(closeErr(code, reason, err))
	}
}

// dialed is the websocket.Handler of one dialed WebSocket.
type dialed struct {
	c    *conn
	open transport.OpenFunc
}

func (d *dialed) OnOpen(ws *websocket.Connection, _ *stdhttp.Request) { d.c.opened(ws, d.open) }

func (d *dialed) OnMessage(_ *websocket.Connection, _ websocket.Opcode, data []byte) {
	if d.c.h != nil {
		d.c.h.OnData(data)
	}
}

func (d *dialed) OnClose(_ *websocket.Connection, code uint16, reason string, err error) {
	if d.c.h != nil {
		d.c.h.OnClose(closeErr(code, reason, err))
	}
}
