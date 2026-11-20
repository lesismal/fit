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
// messages does not matter. NewServer is also what the hub's console socket
// serves its WebSockets with.
package ws

import (
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"net/url"
	"sync"
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
	cfg.HandshakeTimeout = HandshakeTimeout
	srv := NewServer(cfg, &links{open: open}, path)
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

// prepareLink readies the TCP connection of a link as its WebSocket opens:
// write backpressure off and keep-alive on, as transport.FibHandler readies
// a link's; and the queue limit of a link (proto.LinkQueue), which the link
// sets itself on the connections of the other transports.
func prepareLink(ws *websocket.Connection) {
	if c := ws.NetConn(); c != nil {
		c.SetWriteBackpressure(false)
		_ = c.SetKeepAliveConfig(transport.KeepAlive)
		c.SetWriteQueueLimit(proto.LinkQueue)
	}
}

// NewServer returns the handler of WebSockets served on a binding of their
// own: h's, for clients that ask for path, or any path when it is empty,
// as cfg says. A handshake for another path is answered 404 Not Found, and
// any Origin is taken. The handshake head is bounded by MaxHandshake, and
// the handshake by cfg.HandshakeTimeout.
func NewServer(cfg websocket.Config, h websocket.Handler, path string) *websocket.ServerHandler {
	cfg.Path = path
	cfg.CheckOrigin = func(*stdhttp.Request) bool { return true }
	cfg.HTTP.MaxHeaderBytes = MaxHandshake
	return websocket.NewHandlerWithConfig(cfg, h)
}

// conn is a link's transport.Conn on a WebSocket.
type conn struct {
	ws            *websocket.Connection
	h             transport.Handler
	local, remote net.Addr
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

func (c *conn) LocalAddr() net.Addr { return c.local }

func (c *conn) RemoteAddr() net.Addr { return c.remote }

// opened readies the WebSocket that opened, and gives it to c with the
// link's handler; one that open refuses is ended.
func (c *conn) opened(ws *websocket.Connection, open transport.OpenFunc) {
	prepareLink(ws)
	c.ws, c.local, c.remote = ws, ws.LocalAddr(), ws.RemoteAddr()
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

// links is the websocket.Handler of a listener's WebSockets, each of which
// carries its link's conn as its attachment.
type links struct {
	open transport.OpenFunc
}

func (l *links) OnOpen(ws *websocket.Connection, _ *stdhttp.Request) {
	c := &conn{}
	if c.opened(ws, l.open); c.h != nil {
		ws.SetAttachment(c)
	}
}

func (l *links) OnMessage(ws *websocket.Connection, _ websocket.Opcode, data []byte) {
	if c, ok := ws.Attachment().(*conn); ok {
		c.h.OnData(data)
	}
}

func (l *links) OnClose(ws *websocket.Connection, code uint16, reason string, err error) {
	if c, ok := ws.Attachment().(*conn); ok {
		c.h.OnClose(closeErr(code, reason, err))
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
