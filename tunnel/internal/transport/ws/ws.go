//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

// Package ws is the WebSocket transport of the tunnel's links, for networks
// that let only HTTP through. Importing it registers the schemes ws,
// ws://host:port/path, and wss, the same over TLS (fib's, package fib/tls),
// both to dial and to listen on. A wss listener presents the certificate
// transport.ListenOptions gives it, and a wss dial checks the hub's as the
// query of its address says (see package transport): wss://host:port/path?pin=...
// The query is not sent to the hub.
//
// It is a fib.Layer below the link rather than a WebSocket handler beside
// it: once the opening handshake is done, every send of the connection goes
// out as binary frames, and the payloads of the frames that arrive go to the
// wrapped handler as if they had been read off a plain TCP connection. A
// link is a byte stream, so where its frames fall in WebSocket frames does
// not matter. Over TLS the connection's layer is TLS's, and the frames go
// through it: the link sends through the frames itself
// (transport.FibHandlerVia).
package ws

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/bufferpool"
	fibtls "github.com/lesismal/fib/tls"
	"github.com/lesismal/fib/websocket"
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

// wsTransport dials and listens on ws:// and wss:// URLs.
type wsTransport struct{}

func (wsTransport) Dial(engine *fib.Engine, addr string, timeout time.Duration, open transport.OpenFunc) (transport.Conn, error) {
	dial, err := dialer(engine, addr, timeout, func(out fib.Layer) fib.Handler {
		if out == nil {
			return transport.FibHandler(open)
		}
		return transport.FibHandlerVia(open, func(*fib.Connection) fib.Layer { return out })
	})
	if err != nil {
		return nil, err
	}
	return transport.DialFib(dial)
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
	switch u.Scheme {
	case "ws":
		return transport.ListenFib(engine, u.Host, NewServer(transport.FibHandler(open), path), func(hostport string) string { return "ws://" + hostport + path })
	case "wss":
		if o.Cert == nil {
			return nil, errors.New("wss: a listener needs a certificate")
		}
		s := &Server{path: path, tls: true}
		s.inner = transport.FibHandlerVia(open, s.framesOf)
		h := fibtls.NewServer(transport.ServerTLSConfig(*o.Cert, ALPN), s)
		return transport.ListenFib(engine, u.Host, h, func(hostport string) string { return "wss://" + hostport + path })
	}
	return nil, fmt.Errorf("ws: unsupported scheme %q, only ws:// and wss:// are", u.Scheme)
}

const (
	// maxFrame is the most payload one frame carries; a longer send is
	// split.
	maxFrame = 1 << 20
	// maxHandshake bounds the opening handshake's head.
	maxHandshake = 16 << 10
	// HandshakeTimeout bounds the opening handshake.
	HandshakeTimeout = 10 * time.Second
	guid             = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

var (
	// ErrBadHandshake is the error of a connection whose opening handshake
	// failed.
	ErrBadHandshake = errors.New("ws: bad websocket handshake")
	errTimeout      = errors.New("ws: websocket handshake timed out")
)

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(h[:])
}

// frames is the layer of one connection: it frames what is sent, masked when
// the connection is a client's. A send longer than maxFrame takes several
// frames, and mu keeps another send's from landing between them. Over TLS,
// under is TLS's layer, which the frames go out through; otherwise they go
// straight to the socket.
type frames struct {
	c      *fib.Connection
	under  fib.Layer
	client bool
	mu     sync.Mutex
	closed bool
}

// raw sends b, a buffer of package bufferpool, below the frames.
func (f *frames) raw(b []byte) error {
	if f.under != nil {
		err := f.under.Send(b, nil)
		bufferpool.Put(b)
		return err
	}
	return f.c.SendRawPooled(b)
}

// rawCopy sends b below the frames, copying it.
func (f *frames) rawCopy(b []byte) error {
	if f.under != nil {
		return f.under.Send(b, nil)
	}
	return f.c.SendRaw(b)
}

// closeRaw closes the connection once what was sent has gone out.
func (f *frames) closeRaw() {
	if f.under != nil {
		f.under.CloseAfterSend()
		return
	}
	f.c.CloseAfterSendRaw()
}

func (f *frames) Send(first, second []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return net.ErrClosed
	}
	for len(first)+len(second) > 0 {
		n := min(len(first)+len(second), maxFrame)
		head := frameHead(websocket.Binary, n, f.client)
		b := bufferpool.Get(len(head) + n)[:len(head)+n]
		copy(b, head)
		m := copy(b[len(head):], first)
		first = first[m:]
		if m < n {
			k := copy(b[len(head)+m:], second[:n-m])
			second = second[k:]
		}
		if f.client {
			mask(b[len(head):], head[len(head)-4:])
		}
		if err := f.raw(b); err != nil {
			return err
		}
	}
	return nil
}

func (f *frames) CloseAfterSend() {
	f.sendControl(websocket.Close, []byte{0x03, 0xe8}) // 1000, normal closure
	f.closeRaw()
}

// sendControl sends a control frame, nothing after a Close.
func (f *frames) sendControl(op websocket.Opcode, payload []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = op == websocket.Close
	head := frameHead(op, len(payload), f.client)
	b := append(head, payload...)
	if f.client {
		mask(b[len(head):], head[len(head)-4:])
	}
	_ = f.rawCopy(b)
}

// frameHead is the head of a final frame of op with n bytes of payload,
// with a random mask key at its end when masked.
func frameHead(op websocket.Opcode, n int, masked bool) []byte {
	head := make([]byte, 2, 14)
	head[0] = 0x80 | byte(op)
	switch {
	case n < 126:
		head[1] = byte(n)
	case n <= 0xffff:
		head[1] = 126
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	default:
		head[1] = 127
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	if masked {
		head[1] |= 0x80
		var key [4]byte
		_, _ = rand.Read(key[:])
		head = append(head, key[:]...)
	}
	return head
}

func mask(b, key []byte) {
	for i := range b {
		b[i] ^= key[i&3]
	}
}

// deliver feeds data to parser and hands the payloads to inner.
func deliver(f *frames, parser *websocket.Parser, inner fib.Handler, data []byte) {
	for {
		ev, ok, err := parser.FeedOne(data)
		data = nil
		if err != nil {
			f.c.CloseWithError(fmt.Errorf("ws: %w", err))
			return
		}
		if !ok {
			return
		}
		switch ev.Opcode {
		case websocket.Binary, websocket.Text:
			inner.OnData(f.c, ev.Payload)
		case websocket.Ping:
			f.sendControl(websocket.Pong, ev.Payload)
		case websocket.Close:
			f.sendControl(websocket.Close, ev.Payload)
			f.closeRaw()
			return
		}
	}
}

// Server serves inner to WebSocket clients that ask for its path.
type Server struct {
	inner fib.Handler
	path  string
	// tls says that the connections run TLS, whose layer the frames go
	// through.
	tls   bool
	conns sync.Map // *fib.Connection -> *serverConn
}

// framesOf is the frames of c, which the link sends through over TLS.
func (s *Server) framesOf(c *fib.Connection) fib.Layer {
	if v, ok := s.conns.Load(c); ok {
		return &v.(*serverConn).frames
	}
	return nil
}

// NewServer wraps inner for clients that open WebSockets on path; an empty
// path takes any.
func NewServer(inner fib.Handler, path string) *Server {
	return &Server{inner: inner, path: path}
}

type serverConn struct {
	frames
	head   []byte
	open   bool
	parser *websocket.Parser
	timer  *time.Timer
}

func (s *Server) OnOpen(c *fib.Connection) {
	sc := &serverConn{frames: frames{c: c}}
	if s.tls {
		sc.under = c.Layer()
	} else {
		c.SetLayer(&sc.frames)
	}
	s.conns.Store(c, sc)
	sc.timer = time.AfterFunc(HandshakeTimeout, func() { c.CloseWithError(errTimeout) })
}

func (s *Server) OnData(c *fib.Connection, data []byte) {
	v, ok := s.conns.Load(c)
	if !ok {
		return
	}
	sc := v.(*serverConn)
	if !sc.open {
		sc.head = append(sc.head, data...)
		end := bytes.Index(sc.head, []byte("\r\n\r\n"))
		if end < 0 {
			if len(sc.head) > maxHandshake {
				c.CloseWithError(ErrBadHandshake)
			}
			return
		}
		rest := sc.head[end+4:]
		if !s.handshake(sc, sc.head[:end+4]) {
			return
		}
		sc.timer.Stop()
		sc.open, sc.head = true, nil
		sc.parser = websocket.NewParser(2 * maxFrame)
		s.inner.OnOpen(c)
		if len(rest) == 0 {
			return
		}
		data = rest
	}
	deliver(&sc.frames, sc.parser, s.inner, data)
}

// handshake answers the opening handshake in head, reporting whether it
// switched protocols.
func (s *Server) handshake(sc *serverConn, head []byte) bool {
	fail := func(status int) bool {
		_ = sc.rawCopy([]byte(fmt.Sprintf("HTTP/1.1 %d %s\r\nConnection: close\r\nContent-Length: 0\r\n\r\n", status, stdhttp.StatusText(status))))
		sc.closeRaw()
		return false
	}
	req, err := stdhttp.ReadRequest(bufio.NewReader(bytes.NewReader(head)))
	if err != nil {
		return fail(stdhttp.StatusBadRequest)
	}
	if s.path != "" && req.URL.Path != s.path {
		return fail(stdhttp.StatusNotFound)
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	if req.Method != stdhttp.MethodGet || !headerHas(req.Header, "Connection", "upgrade") ||
		!headerHas(req.Header, "Upgrade", "websocket") || req.Header.Get("Sec-WebSocket-Version") != "13" || key == "" {
		return fail(stdhttp.StatusBadRequest)
	}
	_ = sc.rawCopy([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"))
	return true
}

func headerHas(h stdhttp.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

func (s *Server) OnPriorityData(*fib.Connection, []byte) {}

func (s *Server) OnClose(c *fib.Connection, err error) {
	v, ok := s.conns.LoadAndDelete(c)
	if !ok {
		return
	}
	sc := v.(*serverConn)
	sc.timer.Stop()
	if sc.open {
		s.inner.OnClose(c, err)
	}
}

// Dialer is a dial that opens a WebSocket to rawURL, a ws:// URL, on engine,
// and hands the connection to handler once the server has switched
// protocols; done is told the connection then, or why there is none.
func Dialer(engine *fib.Engine, rawURL string, timeout time.Duration, handler fib.Handler) (func(done func(*fib.Connection, error)) error, error) {
	if u, err := url.Parse(rawURL); err == nil && u.Scheme != "ws" {
		return nil, fmt.Errorf("ws: unsupported scheme %q, only ws:// is", u.Scheme)
	}
	return dialer(engine, rawURL, timeout, func(fib.Layer) fib.Handler { return handler })
}

// dialer is a dial that opens a WebSocket to rawURL, a ws:// or wss:// URL,
// on engine, and hands the connection to the handler handler makes once the
// server has switched protocols. Over TLS, handler is given the layer to
// send through; otherwise nil, and the frames are the connection's layer.
func dialer(engine *fib.Engine, rawURL string, timeout time.Duration, handler func(out fib.Layer) fib.Handler) (func(done func(*fib.Connection, error)) error, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	var conf *tls.Config
	switch u.Scheme {
	case "ws":
	case "wss":
		if u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "443")
		}
		// The query says how the hub's certificate is checked, and is
		// not the hub's to see.
		hostURL := *u
		hostURL.Path, hostURL.RawPath = "", ""
		if _, conf, err = transport.TLSClientConfig(hostURL.String(), ALPN, false); err != nil {
			return nil, err
		}
		u.RawQuery = ""
	default:
		return nil, fmt.Errorf("ws: unsupported scheme %q, only ws:// and wss:// are", u.Scheme)
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "80")
	}
	path := u.RequestURI()
	return func(done func(*fib.Connection, error)) error {
		var k [16]byte
		_, _ = rand.Read(k[:])
		key := base64.StdEncoding.EncodeToString(k[:])
		cc := &clientConn{
			done:   done,
			accept: acceptKey(key),
			req: []byte("GET " + path + " HTTP/1.1\r\nHost: " + u.Host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
				"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\nUser-Agent: fit\r\n\r\n"),
			timeout: timeout,
			tls:     conf != nil,
		}
		failed := func(_ *fib.Connection, err error) {
			if err != nil {
				cc.finish(nil, err)
			}
		}
		if conf == nil {
			cc.inner = handler(nil)
			return engine.DialWithHandler("tcp", addr, timeout, cc, failed)
		}
		cc.inner = handler(&cc.frames)
		return fibtls.DialWithOptions(engine, "tcp", addr, timeout, conf, fibtls.DialOptions{HandshakeTimeout: timeout}, cc, failed)
	}, nil
}

// clientConn is the handler of a dialed WebSocket until, and around, the
// one it wraps.
type clientConn struct {
	frames
	inner   fib.Handler
	done    func(*fib.Connection, error)
	req     []byte
	accept  string
	timeout time.Duration
	// tls says that the connection runs TLS, whose layer the frames go
	// through.
	tls bool

	head     []byte
	open     bool
	parser   *websocket.Parser
	timer    *time.Timer
	doneOnce sync.Once
}

func (cc *clientConn) finish(c *fib.Connection, err error) {
	cc.doneOnce.Do(func() { cc.done(c, err) })
}

func (cc *clientConn) OnOpen(c *fib.Connection) {
	cc.c, cc.client = c, true
	if cc.tls {
		cc.under = c.Layer()
	} else {
		c.SetLayer(&cc.frames)
	}
	timeout := cc.timeout
	if timeout <= 0 {
		timeout = HandshakeTimeout
	}
	cc.timer = time.AfterFunc(timeout, func() { c.CloseWithError(errTimeout) })
	_ = cc.rawCopy(cc.req)
}

func (cc *clientConn) OnData(c *fib.Connection, data []byte) {
	if !cc.open {
		cc.head = append(cc.head, data...)
		end := bytes.Index(cc.head, []byte("\r\n\r\n"))
		if end < 0 {
			if len(cc.head) > maxHandshake {
				c.CloseWithError(ErrBadHandshake)
			}
			return
		}
		rest := cc.head[end+4:]
		rsp, err := stdhttp.ReadResponse(bufio.NewReader(bytes.NewReader(cc.head[:end+4])), nil)
		if err != nil || rsp.StatusCode != stdhttp.StatusSwitchingProtocols || rsp.Header.Get("Sec-WebSocket-Accept") != cc.accept {
			if err == nil {
				err = fmt.Errorf("%w: %s", ErrBadHandshake, rsp.Status)
			}
			c.CloseWithError(err)
			return
		}
		cc.timer.Stop()
		cc.open, cc.head = true, nil
		cc.parser = websocket.NewServerFrameParser(2 * maxFrame)
		cc.inner.OnOpen(c)
		cc.finish(c, nil)
		if len(rest) == 0 {
			return
		}
		data = rest
	}
	deliver(&cc.frames, cc.parser, cc.inner, data)
}

func (cc *clientConn) OnPriorityData(*fib.Connection, []byte) {}

func (cc *clientConn) OnClose(c *fib.Connection, err error) {
	if cc.timer != nil {
		cc.timer.Stop()
	}
	if cc.open {
		cc.inner.OnClose(c, err)
		return
	}
	if err == nil {
		err = ErrBadHandshake
	}
	cc.finish(nil, err)
}
