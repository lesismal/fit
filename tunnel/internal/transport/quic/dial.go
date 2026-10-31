package quic

import (
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib"
	fq "github.com/lesismal/fib/http3/quic"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/tunnel/internal/transport"
)

// DialURL is the quic:// address of hostport with how to check its
// certificate: pinned to the fingerprint pin, or, if insecure, not checked.
func DialURL(hostport, pin string, insecure bool) string {
	u := Scheme + "://" + hostport
	switch {
	case pin != "":
		u += "?pin=" + url.QueryEscape(pin)
	case insecure:
		u += "?insecure=1"
	}
	return u
}

// splitQuery parses a quic:// address with its query into the host and port
// and the client options it names.
func splitQuery(addr string) (string, ClientOptions, error) {
	hostport, err := ParseURL(addr)
	if err != nil {
		return "", ClientOptions{}, err
	}
	_, query, _ := strings.Cut(addr, "?")
	q, err := url.ParseQuery(query)
	if err != nil {
		return "", ClientOptions{}, err
	}
	insecure, _ := strconv.ParseBool(q.Get("insecure"))
	return hostport, ClientOptions{PinSHA256: q.Get("pin"), InsecureSkipVerify: insecure}, nil
}

// ErrDialTimeout is the error of a dial whose handshake took too long.
var ErrDialTimeout = errors.New("quic: dial timed out")

// Dial opens a QUIC connection to addr on a UDP connection engine dials,
// and a stream on it once the handshake is over, which carries the link
// open makes. It waits for that, for at most timeout: not on a fib worker.
func (relay) Dial(engine *fib.Engine, addr string, timeout time.Duration, open transport.OpenFunc) (transport.Conn, error) {
	hostport, opts, err := splitQuery(addr)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(hostport)
	opts.ServerName = host
	tc, err := ClientTLSConfig(opts)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultHandshake
	}
	d := &dialer{result: make(chan error, 1)}
	c := &conn{client: true}
	c.handshake = d.opened
	c.over = func(_ *conn, err error) { d.finish(err) }
	d.c, d.open = c, open
	cfg := config(transport.QUIC{HandshakeTimeout: timeout})
	cfg.TLSConfig = tc
	if fc := fibtls.NewQUICClient(tc); fc != nil {
		cfg.NewTLS = func(bool, *tls.Config) fq.TLSConn { return fibtls.NewQUICClient(tc) }
	}
	err = engine.DialWithHandler("udp", hostport, timeout, clientPath{c}, func(fc *fib.Connection, err error) {
		if err != nil {
			d.finish(err)
			return
		}
		c.mu.Lock()
		c.local = fc.LocalAddr()
		qc, err := fq.Dial(fc, fc.RemoteAddr(), cfg, c)
		c.qc = qc
		c.mu.Unlock()
		if err != nil {
			_ = fc.Close()
			d.finish(err)
		}
	})
	if err != nil {
		return nil, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err = <-d.result:
	case <-t.C:
		err = ErrDialTimeout
		_ = c.Close()
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// dialer waits for a dial's stream.
type dialer struct {
	c      *conn
	open   transport.OpenFunc
	result chan error
	once   sync.Once
}

func (d *dialer) finish(err error) {
	d.once.Do(func() { d.result <- err })
}

// opened opens the link's stream once the handshake is over, sending the
// version byte first.
func (d *dialer) opened(c *conn) {
	qc := c.conn()
	qs, err := qc.OpenStream()
	if err != nil {
		c.Close()
		d.finish(err)
		return
	}
	qs.Context = c
	c.mu.Lock()
	c.qs = qs
	c.mu.Unlock()
	if err := qs.Write([]byte{streamVersion}, false); err != nil {
		c.Close()
		d.finish(err)
		return
	}
	h := d.open(c)
	if h == nil {
		c.Close()
		d.finish(transport.ErrClosedOpening)
		return
	}
	c.mu.Lock()
	c.h = h
	c.mu.Unlock()
	d.finish(nil)
}

// clientPath is the fib.Handler of a dialed connection's UDP socket, which
// feeds the QUIC connection its datagrams.
type clientPath struct{ c *conn }

func (p clientPath) OnOpen(*fib.Connection)                 {}
func (p clientPath) OnPriorityData(*fib.Connection, []byte) {}

func (p clientPath) OnData(_ *fib.Connection, data []byte) {
	if qc := p.c.conn(); qc != nil {
		qc.HandleDatagram(data)
	}
}

func (p clientPath) OnClose(_ *fib.Connection, err error) {
	if qc := p.c.conn(); qc != nil {
		if err == nil {
			err = net.ErrClosed
		}
		qc.Abort(err)
	}
}

// OnEngineClose tells the server that the connection is going, as
// Engine.Close is about to end it.
func (p clientPath) OnEngineClose(*fib.Connection) {
	if qc := p.c.conn(); qc != nil {
		qc.Close(codeNoError, "client closed")
	}
}

var _ fib.EngineCloseHandler = clientPath{}
