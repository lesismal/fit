//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

// Package transport carries the tunnel's links: ordered, reliable byte
// streams between a node or client and a server, dialed and listened on by
// address, all on fib's engine. What travels on them is package link's
// business; a transport only moves the bytes.
//
// An address names its transport by its scheme: tcp://host:port, or plain
// host:port, is TCP; tls://host:port is the same over TLS (tls.go);
// ws://host:port/path is a WebSocket, and wss:// one over TLS (package
// transport/ws); quic://host:port is a stream of a QUIC connection on a UDP
// binding of the engine (package transport/quic). A transport registers its
// scheme in an init function, and is linked in with a blank import.
//
// An address is only where to dial: how a tls, wss or quic dial checks the
// hub's certificate is the ClientOptions given to Dial beside it, never part
// of the address, which is parsed with net/url and refused with a user, a
// query or a fragment (see ParseURL).
//
// Every transport serves its connections on the engine: their handlers run
// on fib's workers, or on the goroutine that feeds a QUIC connection its
// datagrams, and must not block. Dial waits for the connection, and is for
// the goroutines of the roles' own, never for a fib worker.
package transport

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/logs"
)

// Conn is one connection: an ordered, reliable, duplex byte stream.
type Conn interface {
	// Send queues b to go out after what earlier Sends queued, and does
	// not block on the network. The connection takes b over: the caller
	// does not touch it again. Send is safe to call from many goroutines
	// at once, and the bytes of one call go out together, never mixed
	// with another's.
	Send(b []byte) error
	// Close closes the connection at once, dropping what is queued.
	Close() error
	// CloseAfterSend closes the connection once what is queued has gone
	// out.
	CloseAfterSend()
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// Handler takes one connection's events.
type Handler interface {
	// OnData gets the bytes that arrived, in order, one call at a time.
	// data is only valid during the call, but the handler may change it.
	OnData(data []byte)
	// OnClose runs once, after the last OnData, with why the connection
	// closed (nil for a plain close).
	OnClose(err error)
}

// QueueLimiter is a Conn whose queue of what was sent and has yet to go out
// has a limit, past which it closes.
type QueueLimiter interface {
	SetQueueLimit(n int64)
}

// OpenFunc is called as each connection opens, before any of its data
// arrives, and returns the connection's Handler; nil closes it.
type OpenFunc func(Conn) Handler

// Transport dials and listens on the addresses of one scheme.
type Transport interface {
	// Dial connects to addr, the whole address with its scheme, calls
	// open once the connection is open, and returns the connection; it
	// gives up after timeout. A transport with TLS checks the hub's
	// certificate as opts says; one without ignores opts.
	Dial(engine *fib.Engine, addr string, timeout time.Duration, opts ClientOptions, open OpenFunc) (Conn, error)
	// Listen accepts connections on addr and calls open for each.
	Listen(engine *fib.Engine, addr string, o ListenOptions, open OpenFunc) (Listener, error)
}

// ListenOptions are what a listener may need beside its address.
type ListenOptions struct {
	// Cert is the certificate a listener with TLS, such as QUIC's,
	// presents.
	Cert *tls.Certificate
	// QUIC is how a quic listener's connections behave.
	QUIC QUIC
	// Log says what goes wrong.
	Log *logs.Logger
}

// QUIC is how QUIC connections behave; zero values take the defaults.
type QUIC struct {
	IdleTimeout      time.Duration
	KeepAlive        time.Duration
	HandshakeTimeout time.Duration
	// MaxHandshakes bounds the handshakes a listener has under way, and
	// MaxConns its connections.
	MaxHandshakes int
	MaxConns      int
}

// Listener is where a transport accepts connections.
type Listener interface {
	// URL is the address peers dial, scheme and all, with an unspecified
	// host replaced by the loopback.
	URL() string
	Close() error
}

var (
	mu         sync.RWMutex
	transports = map[string]Transport{"tcp": tcpTransport{}, "tls": tlsTransport{}}
)

// Register makes t the transport of addresses of scheme.
func Register(scheme string, t Transport) {
	mu.Lock()
	transports[scheme] = t
	mu.Unlock()
}

// Scheme is addr's scheme: what comes before "://", or "tcp" without one.
func Scheme(addr string) string {
	if s, _, ok := strings.Cut(addr, "://"); ok {
		return s
	}
	return "tcp"
}

// IsURL reports whether addr has a scheme, rather than being host:port.
func IsURL(addr string) bool { return strings.Contains(addr, "://") }

// Schemes is the schemes registered.
func Schemes() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(transports))
	for s := range transports {
		out = append(out, s)
	}
	return out
}

func lookup(addr string) (Transport, error) {
	scheme := Scheme(addr)
	mu.RLock()
	t, ok := transports[scheme]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("transport: unsupported scheme %q", scheme)
	}
	return t, nil
}

// Dial connects to addr over the transport of its scheme, a tls, wss or
// quic one checking the hub's certificate as opts says.
func Dial(engine *fib.Engine, addr string, timeout time.Duration, opts ClientOptions, open OpenFunc) (Conn, error) {
	t, err := lookup(addr)
	if err != nil {
		return nil, err
	}
	return t.Dial(engine, addr, timeout, opts, open)
}

// ParseURL parses addr, an address with a scheme, with net/url, and refuses
// one that is not only where to dial or listen: one with a user, a query or
// a fragment, or without a host. Nothing in an address says how a
// certificate is checked, so that no part of one can change that.
func ParseURL(addr string) (*url.URL, error) {
	u, err := url.Parse(addr)
	switch {
	case err != nil:
		return nil, fmt.Errorf("tunnel: %q is not a URL: %w", addr, err)
	case u.User != nil:
		return nil, fmt.Errorf("tunnel: %q has a user, which an address does not", addr)
	case strings.ContainsAny(addr, "?#"):
		return nil, fmt.Errorf("tunnel: %q has a query or a fragment, which an address does not: the hub's certificate is checked by pin or ca_file", addr)
	case u.Opaque != "" || u.Hostname() == "":
		return nil, fmt.Errorf("tunnel: %q is not a URL with a host", addr)
	}
	return u, nil
}

// HostURL is addr, a scheme:// address of a host and port and no path, as
// ParseURL takes it.
func HostURL(addr, scheme string) (*url.URL, error) {
	u, err := ParseURL(addr)
	if err != nil {
		return nil, err
	}
	if u.Scheme != scheme {
		return nil, fmt.Errorf("tunnel: %q is not a %s:// address", addr, scheme)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("tunnel: %q has a path, which a %s:// address does not", addr, scheme)
	}
	if err := checkPort(addr, u.Port()); err != nil {
		return nil, err
	}
	return u, nil
}

// HostPort is the host and port of addr: plain host:port, or a scheme://
// address HostURL takes.
func HostPort(addr, scheme string) (string, error) {
	if IsURL(addr) {
		u, err := HostURL(addr, scheme)
		if err != nil {
			return "", err
		}
		return u.Host, nil
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || strings.ContainsAny(addr, "/?#@") {
		return "", fmt.Errorf("tunnel: %q is not a host and port", addr)
	}
	if err := checkPort(addr, port); err != nil {
		return "", err
	}
	return addr, nil
}

func checkPort(addr, port string) error {
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("tunnel: %q has no port, or one that is not a number", addr)
	}
	return nil
}

// Listen listens on addr over the transport of its scheme.
func Listen(engine *fib.Engine, addr string, o ListenOptions, open OpenFunc) (Listener, error) {
	t, err := lookup(addr)
	if err != nil {
		return nil, err
	}
	return t.Listen(engine, addr, o, open)
}

// fibConn is a Conn on a fib connection.
type fibConn struct {
	c *fib.Connection
	h Handler
	// out, when set, is the layer what is sent goes through, in place of
	// the connection's own: a WebSocket's frames over TLS, whose layer the
	// connection keeps.
	out fib.Layer
}

func (f *fibConn) Send(b []byte) error {
	if f.out != nil {
		return f.out.Send(b, nil)
	}
	return f.c.SendOwned(b)
}

// SetQueueLimit sets what the connection may have queued for its socket;
// past it, the connection closes.
func (f *fibConn) SetQueueLimit(n int64) { f.c.SetWriteQueueLimit(n) }
func (f *fibConn) Close() error          { return f.c.Close() }
func (f *fibConn) CloseAfterSend() {
	if f.out != nil {
		f.out.CloseAfterSend()
		return
	}
	f.c.CloseAfterSend()
}
func (f *fibConn) LocalAddr() net.Addr  { return f.c.LocalAddr() }
func (f *fibConn) RemoteAddr() net.Addr { return f.c.RemoteAddr() }
func (f *fibConn) String() string       { return fmt.Sprintf("fib %v", f.c.RemoteAddr()) }

// fibHandler is the fib.Handler of FibHandler.
type fibHandler struct {
	open OpenFunc
	out  func(*fib.Connection) fib.Layer
}

// FibHandler is a fib.Handler that makes a Conn of each fib connection as
// it opens and hands it to open. It keeps the Conn as the connection's
// attachment. What a fib connection carries is paced by the tunnels'
// windows, each tunnel holding its own TCP connection's reads while its
// window is spent (rule 8), so the link does not hold its reads back for
// its own write backlog: a link that stopped reading for that could wait on
// its peer doing the same, and would stall every tunnel it carries.
func FibHandler(open OpenFunc) fib.Handler { return fibHandler{open: open} }

// FibHandlerVia is FibHandler for connections whose Conns send through the
// layer out gives each as it opens, rather than through the connection's
// own layer.
func FibHandlerVia(open OpenFunc, out func(*fib.Connection) fib.Layer) fib.Handler {
	return fibHandler{open: open, out: out}
}

// KeepAlive is how the TCP connections of links and tunnels find a peer
// that is gone without closing them: probed after Idle of quiet, every
// Interval, given up on after Count unanswered (rule 10). A link that
// carries nothing is not closed for it, only one whose peer stopped
// answering.
var KeepAlive = net.KeepAliveConfig{Enable: true, Idle: 30 * time.Second, Interval: 10 * time.Second, Count: 3}

func (h fibHandler) OnOpen(c *fib.Connection) {
	c.SetWriteBackpressure(false)
	_ = c.SetKeepAliveConfig(KeepAlive)
	fc := &fibConn{c: c}
	if h.out != nil {
		fc.out = h.out(c)
	}
	c.SetAttachment(fc)
	if fc.h = h.open(fc); fc.h == nil {
		_ = c.Close()
	}
}

func (h fibHandler) OnData(c *fib.Connection, data []byte) {
	if fc, ok := c.Attachment().(*fibConn); ok && fc.h != nil {
		fc.h.OnData(data)
	}
}

func (h fibHandler) OnPriorityData(*fib.Connection, []byte) {}

func (h fibHandler) OnClose(c *fib.Connection, err error) {
	if fc, ok := c.Attachment().(*fibConn); ok && fc.h != nil {
		fc.h.OnClose(err)
	}
}

// ErrClosedOpening is the error of a dial whose connection closed as it
// opened.
var ErrClosedOpening = errors.New("transport: connection closed as it opened")

// DialFib dials with dial, which starts a dial of fib's engine with a
// handler that is, or wraps, a FibHandler, and waits for it: not on a fib
// worker.
func DialFib(dial func(done func(*fib.Connection, error)) error) (Conn, error) {
	type result struct {
		c   *fib.Connection
		err error
	}
	ch := make(chan result, 1)
	if err := dial(func(c *fib.Connection, err error) { ch <- result{c, err} }); err != nil {
		return nil, err
	}
	r := <-ch
	if r.err != nil {
		return nil, r.err
	}
	fc, ok := r.c.Attachment().(*fibConn)
	if !ok || fc.h == nil {
		return nil, ErrClosedOpening
	}
	return fc, nil
}

// FibListener is a Listener on a binding of fib's engine.
type FibListener struct {
	engine *fib.Engine
	index  int
	// Format makes the listener's URL of the address it can be dialed on.
	format func(hostport string) string
}

// ListenFib adds a binding to engine on addr, served by handler, whose URL
// format makes of the host:port it can be dialed on.
func ListenFib(engine *fib.Engine, addr string, handler fib.Handler, format func(hostport string) string) (*FibListener, error) {
	i, err := engine.AddBinding(fib.Binding{Addr: addr, Handler: handler})
	if err != nil {
		return nil, err
	}
	return &FibListener{engine: engine, index: i, format: format}, nil
}

// URL is the address the listener can be dialed on.
func (l *FibListener) URL() string { return l.format(Dialable(l.engine, l.index)) }

// Close stops listening.
func (l *FibListener) Close() error { return l.engine.RemoveBinding(l.index) }

// Dialable is the address the i-th binding of engine can be dialed on: its
// address, with an unspecified host replaced by the loopback.
func Dialable(engine *fib.Engine, i int) string {
	addrs, err := engine.BindingAddrs(i)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	tcp, ok := addrs[0].(*net.TCPAddr)
	if !ok {
		return addrs[0].String()
	}
	ip := tcp.IP
	if ip == nil || ip.IsUnspecified() {
		// A listener on every address of both families takes IPv4 too.
		ip = net.IPv4(127, 0, 0, 1)
	}
	return net.JoinHostPort(ip.String(), fmt.Sprint(tcp.Port))
}

// tcpTransport is plain TCP on fib's engine.
type tcpTransport struct{}

func (tcpTransport) Dial(engine *fib.Engine, addr string, timeout time.Duration, _ ClientOptions, open OpenFunc) (Conn, error) {
	hostport, err := HostPort(addr, "tcp")
	if err != nil {
		return nil, err
	}
	return DialFib(func(done func(*fib.Connection, error)) error {
		return engine.DialWithHandler("tcp", hostport, timeout, FibHandler(open), done)
	})
}

func (tcpTransport) Listen(engine *fib.Engine, addr string, _ ListenOptions, open OpenFunc) (Listener, error) {
	addr = strings.TrimPrefix(addr, "tcp://")
	return ListenFib(engine, addr, FibHandler(open), func(hostport string) string { return hostport })
}
