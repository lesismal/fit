// Package mssqlwire is a SQL Server client written in fit, with no driver
// imported: TDS 7.4 over TCP, its pre-login and TLS as SQL Server
// negotiates it inside TDS packets (or TDS 8's TLS first, strict), SQL
// authentication, SQL batches and RPC calls (sp_executesql with typed
// parameters), result sets with every column type the server sends
// (PLP streams of the max types among them), bulk load (INSERT BULK),
// transactions, and cancellation with attention packets. sync's SQL
// Server source and sink build on it.
//
// A Conn is a blocking connection meant for one goroutine of its own, as
// sync's connections to databases are (decision 036): few, long, and off
// fib's workers. Its methods are not safe for concurrent use.
//
// Every call takes a context. When it ends during a call, the client sends
// an attention, which the server answers by cancelling the request; the
// call returns the context's error and the connection stays usable. A
// server that answers neither the call nor the attention within the
// timeouts has the connection closed. Server errors (*Error) leave the
// connection usable unless their class is 20 or above.
package mssqlwire

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Encryption modes, as the client asks for them in its pre-login.
const (
	// EncryptOn is TLS for the whole connection, negotiated in the
	// pre-login and its handshake carried in TDS packets (TDS 7.x); a
	// server that cannot is refused. The default.
	EncryptOn = "on"
	// EncryptStrict is TDS 8: TLS first, with the ALPN protocol tds/8.0,
	// and TDS inside it (SQL Server 2022 and later).
	EncryptStrict = "strict"
	// EncryptOff is no TLS: the password and every row go in clear. A
	// server that requires encryption is refused rather than followed.
	EncryptOff = "off"
)

// The defaults of Config.
const (
	DefaultPort          = "1433"
	DefaultTimeout       = 60 * time.Second
	DefaultMaxValueBytes = 64 << 20
	// tdsVersion is TDS 7.4, SQL Server 2012 and later.
	tdsVersion = 0x74000004
)

// attentionWait bounds the wait for the server's answer to an attention.
var attentionWait = 10 * time.Second

// Config is what a connection needs.
type Config struct {
	// Addr is host:port; the port defaults to 1433.
	Addr     string
	User     string
	Password string
	// Database is the session's initial database; the login's default
	// when empty.
	Database string
	// AppName is the program name the server shows (fit when empty).
	AppName string
	// Encrypt is one of the Encrypt constants; empty means EncryptOn.
	Encrypt string
	// TLS is the base TLS configuration: roots (system roots when nil),
	// client certificates, and the server name (the host of Addr when
	// empty). It is cloned, never changed.
	TLS *tls.Config
	// Timeout bounds connecting and logging in, each wait for the
	// server's next packet within a call, and each write; default 60s.
	// A call's whole length is its context's.
	Timeout time.Duration
	// PacketSize is the packet size asked for; default 4096.
	PacketSize int
	// MaxValueBytes bounds one value read (a PLP stream, a text) and one
	// row; default 64 MiB.
	MaxValueBytes int
	// OnInfo, when set, is called with each informational message (PRINT,
	// RAISERROR below class 11, "changed database context").
	OnInfo func(*Error)
	// Dial, when set, opens the TCP connection in place of net.Dialer.
	Dial func(ctx context.Context, addr string) (net.Conn, error)
}

// Conn is one connection to a server.
type Conn struct {
	cfg Config
	raw net.Conn
	nc  net.Conn
	rd  *bufio.Reader

	wmu        sync.Mutex
	wbuf       []byte
	packetSize int
	msg        msgReader

	// tx is the transaction descriptor, from the server's ENVCHANGE, all
	// zeros outside a transaction.
	tx        [8]byte
	collation Collation
	database  string
	host      string
	version   uint32

	// attn says an attention was sent for the call under way and its
	// answer not read; attnAt is when.
	attn   atomic.Bool
	attnAt atomic.Int64

	// until, when not 0, is the deadline of every read and write: the
	// login's.
	until atomic.Int64

	broken error
	closed atomic.Bool
}

// Error is an ERROR or INFO token of the server.
type Error struct {
	Number  int32
	State   uint8
	Class   uint8
	Message string
	Server  string
	Proc    string
	Line    int32
	// More are the errors the server sent after this one in the same
	// answer.
	More []*Error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("mssql: error %d, class %d, state %d: %s", e.Number, e.Class, e.State, e.Message)
	if e.Proc != "" {
		s += fmt.Sprintf(" (in %s, line %d)", e.Proc, e.Line)
	}
	return s
}

// ErrClosed is a call on a closed connection.
var ErrClosed = errors.New("mssql: the connection is closed")

// Dial connects to a server, negotiates encryption and logs in.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxValueBytes <= 0 {
		cfg.MaxValueBytes = DefaultMaxValueBytes
	}
	if cfg.PacketSize == 0 {
		cfg.PacketSize = DefaultPacketSize
	}
	if cfg.PacketSize < minPacketSize || cfg.PacketSize > maxPacketSize {
		return nil, fmt.Errorf("mssql: a packet size of %d is not from %d to %d", cfg.PacketSize, minPacketSize, maxPacketSize)
	}
	if cfg.AppName == "" {
		cfg.AppName = "fit"
	}
	switch cfg.Encrypt {
	case "":
		cfg.Encrypt = EncryptOn
	case EncryptOn, EncryptOff, EncryptStrict:
	default:
		return nil, fmt.Errorf("mssql: encrypt %q is not on, strict or off", cfg.Encrypt)
	}
	addr := cfg.Addr
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host, addr = addr, net.JoinHostPort(addr, DefaultPort)
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	var raw net.Conn
	if cfg.Dial != nil {
		raw, err = cfg.Dial(ctx, addr)
	} else {
		var d net.Dialer
		raw, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("mssql: connecting to %s: %w", addr, err)
	}
	c := &Conn{cfg: cfg, raw: raw, nc: raw, packetSize: DefaultPacketSize, wbuf: make([]byte, 0, maxPacketSize)}
	c.msg.c = c
	c.rd = bufio.NewReaderSize(raw, 32<<10)
	d, _ := ctx.Deadline()
	c.until.Store(d.UnixNano())
	stop := context.AfterFunc(ctx, func() {
		c.until.Store(1)
		raw.SetDeadline(time.Unix(1, 0))
	})
	c.host = host
	err = c.handshake(host)
	if !stop() && err != nil {
		err = fmt.Errorf("mssql: logging in to %s: %w", addr, ctx.Err())
	}
	if err != nil {
		raw.Close()
		return nil, err
	}
	c.until.Store(0)
	raw.SetDeadline(time.Time{})
	return c, nil
}

func (c *Conn) tlsConfig(host string) *tls.Config {
	var tc *tls.Config
	if c.cfg.TLS != nil {
		tc = c.cfg.TLS.Clone()
	} else {
		tc = &tls.Config{}
	}
	if tc.ServerName == "" {
		tc.ServerName = host
	}
	if tc.MinVersion == 0 {
		tc.MinVersion = tls.VersionTLS12
	}
	// A TDS packet goes in one TLS record: SQL Server drops a connection
	// whose packet is split across records, as Go's dynamic record sizing
	// does to the first ones.
	tc.DynamicRecordSizingDisabled = true
	return tc
}

// handshake negotiates encryption and logs in.
func (c *Conn) handshake(host string) error {
	if c.cfg.Encrypt == EncryptStrict {
		tc := c.tlsConfig(host)
		tc.NextProtos = []string{"tds/8.0"}
		tlsc := tls.Client(c.raw, tc)
		if err := tlsc.Handshake(); err != nil {
			return fmt.Errorf("mssql: TLS: %w", err)
		}
		c.nc, c.rd = tlsc, bufio.NewReaderSize(tlsc, 32<<10)
	}
	enc, err := c.prelogin()
	if err != nil {
		return err
	}
	if c.cfg.Encrypt == EncryptOn {
		if enc != encryptOn && enc != encryptReq {
			return errors.New("mssql: the server does not offer encryption, and encrypt is on (set it off to connect in clear)")
		}
		hc := &handshakeConn{c: c, rd: c.rd}
		tc := c.tlsConfig(host)
		if tc.MaxVersion == 0 {
			// TLS 1.3 is TDS 8's (strict); inside TDS 7.4's pre-login
			// SQL Server speaks 1.2.
			tc.MaxVersion = tls.VersionTLS12
		}
		tlsc := tls.Client(hc, tc)
		if err := tlsc.Handshake(); err != nil {
			return fmt.Errorf("mssql: TLS: %w", err)
		}
		hc.done = true
		c.nc, c.rd = tlsc, bufio.NewReaderSize(tlsc, 32<<10)
	} else if c.cfg.Encrypt == EncryptOff && enc == encryptReq {
		return errors.New("mssql: the server requires encryption, and encrypt is off")
	}
	return c.login()
}

// The pre-login's encryption values.
const (
	encryptOff    = 0
	encryptOn     = 1
	encryptNotSup = 2
	encryptReq    = 3
	encryptStrict = 4
)

// prelogin sends the PRELOGIN message and returns the server's
// encryption.
func (c *Conn) prelogin() (byte, error) {
	enc := byte(encryptOn)
	switch c.cfg.Encrypt {
	case EncryptOff:
		enc = encryptNotSup
	case EncryptStrict:
		enc = encryptStrict
	}
	opts := []struct {
		token byte
		data  []byte
	}{
		{0, []byte{0x11, 0, 0, 0, 0, 0}}, // VERSION
		{1, []byte{enc}},                 // ENCRYPTION
		{2, []byte{0}},                   // INSTOPT
		{3, binary.BigEndian.AppendUint32(nil, uint32(os.Getpid()))},
		{4, []byte{0}}, // MARS off
	}
	off := len(opts)*5 + 1
	var head, data []byte
	for _, o := range opts {
		head = append(head, o.token)
		head = binary.BigEndian.AppendUint16(head, uint16(off+len(data)))
		head = binary.BigEndian.AppendUint16(head, uint16(len(o.data)))
		data = append(data, o.data...)
	}
	head = append(append(head, 0xff), data...)
	w := c.newWriter(pktPrelogin)
	w.Write(head)
	if err := w.close(); err != nil {
		return 0, err
	}
	b, err := c.readMessage()
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(b) && b[i] != 0xff; i += 5 {
		if i+5 > len(b) {
			break
		}
		o, n := int(binary.BigEndian.Uint16(b[i+1:])), int(binary.BigEndian.Uint16(b[i+3:]))
		if o+n > len(b) {
			return 0, errors.New("mssql: a malformed pre-login answer")
		}
		switch b[i] {
		case 0:
			if n >= 4 {
				c.version = binary.BigEndian.Uint32(b[o:])
			}
		case 1:
			if n >= 1 {
				return b[o], nil
			}
		}
	}
	if c.cfg.Encrypt == EncryptStrict {
		return encryptStrict, nil
	}
	return 0, errors.New("mssql: the pre-login answer has no encryption")
}

// readMessage reads a whole message, bounded by MaxValueBytes.
func (c *Conn) readMessage() ([]byte, error) {
	c.msg.begin()
	var out []byte
	for {
		if err := c.msg.packet(); err != nil {
			return nil, err
		}
		out = append(out, c.msg.buf...)
		if len(out) > c.cfg.MaxValueBytes {
			return nil, c.fail(errors.New("mssql: the server's message is too large"))
		}
		if c.msg.eom {
			return out, nil
		}
	}
}

// handshakeConn carries the TLS handshake in PRELOGIN packets, as TDS 7.x
// does, then gets out of the way.
type handshakeConn struct {
	c *Conn
	// rd reads the TCP connection, under TLS.
	rd   *bufio.Reader
	buf  []byte
	done bool
}

func (h *handshakeConn) Read(p []byte) (int, error) {
	if h.done {
		return h.rd.Read(p)
	}
	if len(h.buf) == 0 {
		h.c.msg.begin()
		if err := h.c.msg.packet(); err != nil {
			return 0, err
		}
		h.buf = h.c.msg.buf
	}
	n := copy(p, h.buf)
	h.buf = h.buf[n:]
	return n, nil
}

func (h *handshakeConn) Write(p []byte) (int, error) {
	if h.done {
		return h.c.raw.Write(p)
	}
	w := h.c.newWriter(pktPrelogin)
	w.Write(p)
	if err := w.close(); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (h *handshakeConn) Close() error                       { return h.c.raw.Close() }
func (h *handshakeConn) LocalAddr() net.Addr                { return h.c.raw.LocalAddr() }
func (h *handshakeConn) RemoteAddr() net.Addr               { return h.c.raw.RemoteAddr() }
func (h *handshakeConn) SetDeadline(t time.Time) error      { return h.c.raw.SetDeadline(t) }
func (h *handshakeConn) SetReadDeadline(t time.Time) error  { return h.c.raw.SetReadDeadline(t) }
func (h *handshakeConn) SetWriteDeadline(t time.Time) error { return h.c.raw.SetWriteDeadline(t) }

// login sends LOGIN7 and reads the answer up to its LOGINACK and DONE.
func (c *Conn) login() error {
	hostname, _ := os.Hostname()
	strs := []string{hostname, c.cfg.User, "", c.cfg.AppName, c.host, "", "fit", "", c.cfg.Database}
	const fixed = 94
	b := make([]byte, fixed, fixed+256)
	binary.LittleEndian.PutUint32(b[4:], tdsVersion)
	binary.LittleEndian.PutUint32(b[8:], uint32(c.cfg.PacketSize))
	binary.LittleEndian.PutUint32(b[12:], 7)
	binary.LittleEndian.PutUint32(b[16:], uint32(os.Getpid()))
	b[24] = 0xe0 // USE_DB_ON, INIT_DB_FATAL, SET_LANG_ON
	b[25] = 0x03 // INIT_LANG_FATAL, ODBC_ON
	binary.LittleEndian.PutUint32(b[32:], 0x0409)
	at := 36
	for i, s := range strs {
		u := appendUCS2(nil, s)
		if i == 2 {
			u = appendUCS2(nil, c.cfg.Password)
			for j, x := range u {
				u[j] = (x<<4 | x>>4) ^ 0xa5
			}
		}
		binary.LittleEndian.PutUint16(b[at:], uint16(len(b)))
		binary.LittleEndian.PutUint16(b[at+2:], uint16(len(u)/2))
		b = append(b, u...)
		at += 4
	}
	// ClientID (6 bytes), then SSPI, AtchDBFile and ChangePassword, empty
	// at the end of the data, and cbSSPILong 0.
	for _, o := range []int{78, 82, 86} {
		binary.LittleEndian.PutUint16(b[o:], uint16(len(b)))
	}
	binary.LittleEndian.PutUint32(b, uint32(len(b)))
	w := c.newWriter(pktLogin7)
	w.Write(b)
	if err := w.close(); err != nil {
		return err
	}
	r := &response{c: c}
	acked := false
	r.onLoginAck = func() { acked = true }
	if err := r.drain(); err != nil {
		var se *Error
		if errors.As(err, &se) {
			return fmt.Errorf("mssql: the server refused the login: %w", err)
		}
		return err
	}
	if !acked {
		return errors.New("mssql: the server answered the login without a LOGINACK")
	}
	return nil
}

// Close closes the connection.
func (c *Conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.nc.Close()
}

// fail marks the connection broken by err, closing it, and returns err.
func (c *Conn) fail(err error) error {
	if c.broken == nil {
		c.broken = err
	}
	c.Close()
	return err
}

// Err is why the connection broke, nil while it works.
func (c *Conn) Err() error { return c.broken }

// Database is the session's current database.
func (c *Conn) Database() string { return c.database }

// ServerVersion is the server's version, as major.minor.build.
func (c *Conn) ServerVersion() string {
	v := c.version
	return strconv.Itoa(int(v>>24)) + "." + strconv.Itoa(int(v>>16&0xff)) + "." + strconv.Itoa(int(v&0xffff))
}

// Collation is the database's default collation, which varchar
// parameters are sent in.
func (c *Conn) Collation() Collation { return c.collation }

// TLS is the connection's TLS state, and false without TLS.
func (c *Conn) TLS() (tls.ConnectionState, bool) {
	if tc, ok := c.nc.(*tls.Conn); ok {
		return tc.ConnectionState(), true
	}
	return tls.ConnectionState{}, false
}

// PacketSize is the packet size the server set.
func (c *Conn) PacketSize() int { return c.packetSize }

// InTransaction reports whether the session is in a transaction the client
// began.
func (c *Conn) InTransaction() bool { return c.tx != [8]byte{} }

func (c *Conn) readDeadline() time.Time {
	if u := c.until.Load(); u != 0 {
		return time.Unix(0, u)
	}
	if c.attn.Load() {
		return time.Unix(0, c.attnAt.Load()).Add(attentionWait)
	}
	return time.Now().Add(c.cfg.Timeout)
}

func (c *Conn) writeDeadline() time.Time {
	if u := c.until.Load(); u != 0 {
		return time.Unix(0, u)
	}
	return time.Now().Add(c.cfg.Timeout)
}
