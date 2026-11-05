package quic

import (
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bufferpool"
	fq "github.com/lesismal/fib/http3/quic"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/tunnel/internal/transport"
)

func init() { transport.Register(Scheme, relay{}) }

type relay struct{}

// ParseURL returns the host:port of a "quic://host:port" URL, or of a bare
// "host:port".
func ParseURL(raw string) (string, error) {
	base, _, _ := strings.Cut(raw, "?")
	if !strings.Contains(base, "://") {
		if _, _, err := net.SplitHostPort(base); err != nil {
			return "", errors.New("quic: bad address " + strconv.Quote(raw) + ": " + err.Error())
		}
		return base, nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", errors.New("quic: bad address " + strconv.Quote(raw))
	}
	if u.Scheme != Scheme {
		return "", errors.New("quic: address " + strconv.Quote(raw) + " is not a quic:// URL")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("quic: address " + strconv.Quote(raw) + " has a path")
	}
	if u.Port() == "" {
		return "", errors.New("quic: address " + strconv.Quote(raw) + " has no port")
	}
	return u.Host, nil
}

// Listen serves links on a UDP binding of engine at addr, with the
// certificate o gives.
func (relay) Listen(engine *fib.Engine, addr string, o transport.ListenOptions, open transport.OpenFunc) (transport.Listener, error) {
	hostport, err := ParseURL(addr)
	if err != nil {
		return nil, err
	}
	if o.Cert == nil {
		return nil, errors.New("quic: a listener needs a certificate")
	}
	l := &listener{engine: engine, index: -1, open: open, max: int64(o.QUIC.MaxHandshakes), maxConns: o.QUIC.MaxConns, conns: map[*conn]struct{}{}}
	if l.max <= 0 {
		l.max = 64
	}
	if l.maxConns <= 0 {
		l.maxConns = 1024
	}
	l.streamWait = o.QUIC.HandshakeTimeout
	if l.streamWait <= 0 {
		l.streamWait = defaultHandshake
	}
	tc := ServerTLSConfig(*o.Cert)
	l.config = config(o.QUIC)
	l.config.TLSConfig = tc
	l.resetKey = fq.NewResetKey()
	l.config.ResetKey = &l.resetKey
	// Past half the handshakes it takes, a client proves its address with
	// a Retry before it gets one (see admit); the key also lets Accept
	// take the token the client brings back. The tunnel's clients keep no
	// NEW_TOKEN tokens, so none are sent.
	l.tokenKey = fq.NewTokenKey()
	l.config.TokenKey = l.tokenKey
	l.config.DisableNewTokens = true
	if server := fibtls.NewQUICServer(tc); server != nil {
		l.config.NewTLS = func(bool, *tls.Config) fq.TLSConn { return server.NewConn() }
	}
	if l.index, err = engine.AddBinding(fib.Binding{Network: "udp", Addr: hostport, Handler: l}); err != nil {
		return nil, err
	}
	return l, nil
}

// listener is a UDP binding's handler: it hands each datagram to the
// connection it is for, by the peer's address and the connection ID, and
// starts a connection with a client's first.
type listener struct {
	engine   *fib.Engine
	index    int
	open     transport.OpenFunc
	config   fq.Config
	resetKey fq.ResetKey
	// handshakes counts the handshakes under way, at most max; maxConns
	// bounds the connections, and streamWait how long one may go without
	// its link's stream once its handshake is over.
	handshakes atomic.Int64
	max        int64
	tokenKey   *fq.TokenKey
	maxConns   int
	streamWait time.Duration

	mu     sync.Mutex
	conns  map[*conn]struct{}
	closed bool
}

// peer is the connections of one client address.
type peer struct {
	mu    sync.Mutex
	conns []*conn
}

func peerOf(c *fib.Connection) *peer {
	if p, ok := c.Attachment().(*peer); ok {
		return p
	}
	p := &peer{}
	c.SetAttachment(p)
	return p
}

func (p *peer) find(cid []byte) *fq.Conn {
	if cid == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		if qc := c.conn(); qc != nil && qc.Owns(cid) {
			return qc
		}
	}
	return nil
}

func (p *peer) remove(c *conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, have := range p.conns {
		if have == c {
			p.conns = append(p.conns[:i], p.conns[i+1:]...)
			return
		}
	}
}

func (l *listener) OnOpen(*fib.Connection)                 {}
func (l *listener) OnPriorityData(*fib.Connection, []byte) {}

func (l *listener) OnData(c *fib.Connection, d []byte) {
	p := peerOf(c)
	if qc := p.find(fq.DatagramDCID(d)); qc != nil {
		qc.HandleDatagram(d)
		return
	}
	if vn := fq.VersionNegotiation(d); vn != nil {
		_ = c.Send(vn)
		bufferpool.Put(d)
		return
	}
	if !fq.IsInitial(d) {
		if fq.ShortHeaderDCID(d) != nil {
			// A packet of a connection this listener no longer has: tell
			// the client, so that it need not time out.
			if reset := l.resetKey.StatelessReset(d); reset != nil {
				_ = c.Send(reset)
			}
		}
		bufferpool.Put(d)
		return
	}
	l.mu.Lock()
	closed := l.closed || len(l.conns) >= l.maxConns
	l.mu.Unlock()
	if closed || c.ShuttingDown() || !l.admit(c, d) {
		// The client sends its Initial again, and is let in once a
		// handshake is over, or answers the Retry admit sent.
		bufferpool.Put(d)
		return
	}
	qcConn := &conn{local: c.LocalAddr(), open: l.open}
	path := &peerPath{c: c, p: p, conn: qcConn}
	l.handshakes.Add(1)
	qcConn.handshake = func(cc *conn) {
		l.handshakes.Add(-1)
		// A connection that carries no link is closed: kept alive by its
		// pings, it would hold a place for nothing.
		time.AfterFunc(l.streamWait, func() {
			cc.mu.Lock()
			idle := cc.h == nil
			cc.mu.Unlock()
			if idle {
				_ = cc.Close()
			}
		})
	}
	qcConn.over = func(cc *conn, _ error) {
		cc.mu.Lock()
		shook := cc.shook
		cc.mu.Unlock()
		if !shook {
			l.handshakes.Add(-1)
		}
		l.mu.Lock()
		delete(l.conns, cc)
		l.mu.Unlock()
	}
	qc := fq.Accept(path, c.RemoteAddr(), l.config, qcConn, d)
	if qc == nil {
		l.handshakes.Add(-1)
		return
	}
	qcConn.mu.Lock()
	qcConn.qc = qc
	qcConn.mu.Unlock()
	p.mu.Lock()
	p.conns = append(p.conns, qcConn)
	p.mu.Unlock()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		qc.Close(codeNoError, "listener closed")
		return
	}
	l.conns[qcConn] = struct{}{}
	l.mu.Unlock()
}

// admit reports whether the client Initial in d, from c's address, may
// start a handshake. Past max handshakes it may not. Past half of them, one
// whose token does not prove the client's address is answered with a Retry
// instead: Initials from forged addresses, which cost their sender nothing,
// then hold at most half the handshakes, and a client that answers its
// Retry gets one of the rest.
func (l *listener) admit(c *fib.Connection, d []byte) bool {
	n := l.handshakes.Load()
	if n >= l.max {
		return false
	}
	if n < l.max/2 {
		return true
	}
	status, kind := l.tokenKey.CheckToken(d, c.RemoteAddr())
	if status == fq.TokenValid || status == fq.TokenInvalid && kind == fq.TokenKindRetry {
		// A good token, or a bad Retry token, which Accept answers with
		// INVALID_TOKEN.
		return true
	}
	if retry := l.tokenKey.Retry(d, c.RemoteAddr()); retry != nil {
		_ = c.Send(retry)
	}
	return false
}

// OnClose ends the connections of an address the engine closed.
func (l *listener) OnClose(c *fib.Connection, err error) {
	p, ok := c.Attachment().(*peer)
	if !ok {
		return
	}
	if err == nil {
		err = net.ErrClosed
	}
	p.mu.Lock()
	conns := append([]*conn(nil), p.conns...)
	p.mu.Unlock()
	for _, cc := range conns {
		if qc := cc.conn(); qc != nil {
			qc.Abort(err)
		}
	}
}

// URL is the quic:// address the listener is dialed on, an unspecified
// host replaced by the loopback.
func (l *listener) URL() string {
	addrs, err := l.engine.BindingAddrs(l.index)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	a, ok := addrs[0].(*net.UDPAddr)
	if !ok {
		return Scheme + "://" + addrs[0].String()
	}
	ip := a.IP
	if ip == nil || ip.IsUnspecified() {
		ip = net.IPv4(127, 0, 0, 1)
	}
	return Scheme + "://" + net.JoinHostPort(ip.String(), strconv.Itoa(a.Port))
}

// Close stops listening, and closes every connection accepted.
func (l *listener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	l.closed = true
	conns := make([]*conn, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		if qc := c.conn(); qc != nil {
			qc.Close(codeNoError, "listener closed")
		}
	}
	return l.engine.RemoveBinding(l.index)
}

// peerPath sends a connection's datagrams to its client, on the address's
// fib.Connection.
type peerPath struct {
	c    *fib.Connection
	p    *peer
	conn *conn
}

func (p *peerPath) Send(d []byte) error { return p.c.Send(d) }

// SendDatagrams sends a round of datagrams with one system call where the
// connection can.
func (p *peerPath) SendDatagrams(datagrams [][]byte) (int, error) {
	return p.c.SendDatagrams(datagrams)
}

// NotifyWritable sends what a full socket refused as soon as it has room.
func (p *peerPath) NotifyWritable(fn func()) bool { return p.c.NotifyWritable(fn) }

// Close lets the connection's use of the address go; the address's
// fib.Connection stays, until the engine finds it idle.
func (p *peerPath) Close() error {
	p.p.remove(p.conn)
	return nil
}

var (
	_ fq.DatagramsSender  = (*peerPath)(nil)
	_ fq.WritableNotifier = (*peerPath)(nil)
)
