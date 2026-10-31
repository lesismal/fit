package quic

import (
	"fmt"
	"net"
	"sync"
	"time"

	fq "github.com/lesismal/fib/http3/quic"

	"github.com/lesismal/fit/tunnel/internal/transport"
)

// streamVersion is the byte the opener of a stream sends first (see the
// package doc).
const streamVersion byte = 1

// Application error codes streams are reset, and connections closed, with,
// fibhub's.
const (
	codeNoError  = 0
	codeRefused  = 1
	codeProtocol = 2
	codeClosed   = 3
)

// The defaults of a connection's settings.
const (
	defaultIdle      = 30 * time.Second
	defaultKeepAlive = 10 * time.Second
	defaultHandshake = 10 * time.Second
)

// StreamError is why a link closed whose stream the peer reset, or asked
// this side to stop sending on.
type StreamError struct {
	Code uint64
}

func (e *StreamError) Error() string {
	return fmt.Sprintf("quic: stream reset by peer, code %d", e.Code)
}

// conn is one QUIC connection and the one stream on it that carries a link:
// the connection's fq.Handler, and the link's transport.Conn. The handler's
// methods are called one at a time, on the goroutine that fed the
// connection a datagram or wrote to it, and none of them blocks.
type conn struct {
	client bool
	local  net.Addr

	// open makes the link of the stream a client opens, on a server; the
	// client's link is made by its dial.
	open transport.OpenFunc
	// handshake is told once the handshake is over, on a client, which then
	// opens its stream; over is told once the connection is.
	handshake func(*conn)
	over      func(*conn, error)

	mu       sync.Mutex
	qc       *fq.Conn
	qs       *fq.Stream
	h        transport.Handler
	preamble bool
	closing  bool
	finished bool
	// shook is set once the handshake is over.
	shook bool
}

// Conn returns the QUIC connection.
func (c *conn) conn() *fq.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.qc
}

// Send queues b on the stream; it does not block.
func (c *conn) Send(b []byte) error {
	c.mu.Lock()
	qs, closing := c.qs, c.closing
	c.mu.Unlock()
	if qs == nil || closing {
		return net.ErrClosed
	}
	if err := qs.WriteOwned(b, false); err != nil {
		return net.ErrClosed
	}
	return nil
}

// Close ends the connection at once; the link's OnClose follows, from the
// connection's handler.
func (c *conn) Close() error {
	c.mu.Lock()
	c.closing = true
	qc := c.qc
	c.mu.Unlock()
	if qc != nil {
		qc.Close(codeNoError, "")
	}
	return nil
}

// CloseAfterSend ends the stream once what was written is sent, and the
// connection once that is delivered.
func (c *conn) CloseAfterSend() {
	c.mu.Lock()
	c.closing = true
	qc, qs := c.qc, c.qs
	c.mu.Unlock()
	if qs != nil {
		_ = qs.Close()
	}
	if qc != nil {
		qc.CloseWhenDone(codeNoError, "")
	}
}

func (c *conn) LocalAddr() net.Addr { return c.local }

func (c *conn) RemoteAddr() net.Addr {
	if qc := c.conn(); qc != nil {
		return qc.RemoteAddr()
	}
	return nil
}

func (c *conn) String() string { return fmt.Sprintf("quic %v", c.RemoteAddr()) }

// finish tells the link, once, that the connection is over.
func (c *conn) finish(err error) {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	c.finished, c.closing = true, true
	h := c.h
	c.mu.Unlock()
	if h != nil {
		h.OnClose(err)
	}
}

func (c *conn) OnHandshake(*fq.Conn) {
	c.mu.Lock()
	c.shook = true
	c.mu.Unlock()
	if c.handshake != nil {
		c.handshake(c)
	}
}

// refuse turns a stream away.
func refuse(qs *fq.Stream, code uint64) {
	qs.StopSending(code)
	qs.Reset(code)
}

func (c *conn) OnStreamData(qs *fq.Stream, data []byte, fin bool) {
	c.mu.Lock()
	switch {
	case qs.Context != nil:
	case c.client || c.qs != nil || !qs.Bidirectional() || c.open == nil:
		// A client takes no stream from the server, and a server one link
		// per connection.
		c.mu.Unlock()
		refuse(qs, codeRefused)
		return
	default:
		c.qs, c.preamble = qs, true
		qs.Context = c
	}
	preamble := c.preamble
	c.mu.Unlock()
	if preamble {
		if len(data) == 0 {
			if fin {
				refuse(qs, codeProtocol)
				c.Close()
			}
			return
		}
		if data[0] != streamVersion {
			refuse(qs, codeProtocol)
			c.Close()
			return
		}
		data = data[1:]
		h := c.open(c)
		c.mu.Lock()
		c.preamble, c.h = false, h
		c.mu.Unlock()
		if h == nil {
			refuse(qs, codeRefused)
			c.Close()
			return
		}
	}
	c.mu.Lock()
	h := c.h
	c.mu.Unlock()
	if h != nil && len(data) > 0 {
		h.OnData(data)
	}
	if fin {
		// The peer is done sending: the link is over.
		c.finish(nil)
		c.CloseAfterSend()
	}
}

func (c *conn) OnStreamReset(qs *fq.Stream, code uint64) {
	if qs.Context == c {
		c.finish(&StreamError{Code: code})
		c.Close()
	}
}

func (c *conn) OnStopSending(qs *fq.Stream, code uint64) {
	if qs.Context == c {
		c.finish(&StreamError{Code: code})
		c.Close()
	}
}

func (c *conn) OnStreamsAvailable(*fq.Conn) {}

func (c *conn) OnClose(_ *fq.Conn, err error) {
	if err == nil {
		err = net.ErrClosed
	}
	c.finish(err)
	if c.over != nil {
		c.over(c, err)
	}
}

// config is the fq.Config of a connection with o's settings.
func config(o transport.QUIC) fq.Config {
	idle, keepAlive, handshake := o.IdleTimeout, o.KeepAlive, o.HandshakeTimeout
	if idle <= 0 {
		idle = defaultIdle
	}
	if keepAlive <= 0 {
		keepAlive = defaultKeepAlive
	}
	if handshake <= 0 {
		handshake = defaultHandshake
	}
	return fq.Config{
		MaxIdleTimeout:   idle,
		KeepAlivePeriod:  keepAlive,
		HandshakeTimeout: handshake,
		// One link per connection: a stream from the client, none from the
		// server.
		MaxIncomingStreams:    1,
		MaxIncomingUniStreams: 1,
		// fib hands each datagram over as a buffer of its own from the
		// pool, which QUIC gives back once it has done with it.
		RecycleDatagrams: true,
		DisableNewTokens: true,
	}
}

var _ fq.Handler = (*conn)(nil)
var _ transport.Conn = (*conn)(nil)
