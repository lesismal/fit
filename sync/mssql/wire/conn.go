// Package mssqlwire is a SQL Server client written in fit, with no driver
// imported: TDS 7.4 over TCP, its pre-login and TLS as SQL Server
// negotiates it inside TDS packets (or TDS 8's TLS first, strict), SQL
// authentication, Microsoft Entra ID's access tokens (LOGIN7's FEDAUTH,
// EntraClient getting them on net/http), integrated authentication (SSPI:
// Kerberos in SPNEGO through sync/mssql/wire/krb5, NTLMv2), Azure's
// routing followed once, SQL batches and RPC calls (sp_executesql with typed
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

	// AccessToken, when set, logs in with Microsoft Entra ID (Azure SQL)
	// in place of User and Password: the OAuth 2 access token it returns,
	// for https://database.windows.net/, goes in LOGIN7's FEDAUTH feature
	// extension, the security token flavour. EntraClient.Token is one.
	AccessToken func(ctx context.Context) (string, error)
	// SSPI, when set, logs in with integrated (Windows) authentication in
	// place of User and Password: it makes each login's exchange,
	// Kerberos's (krb5.Initiator) or NTLM's (NTLM).
	SSPI func() SSPI
	// SPN is the service principal name SSPI authenticates to;
	// MSSQLSvc/host:port of Addr when empty.
	SPN string

	// routed says the connection follows a routing ENVCHANGE.
	routed bool
}

// SSPI is integrated authentication: the security tokens of TDS's SSPI
// exchange, in LOGIN7 and its SSPI messages.
type SSPI interface {
	// Start returns the first token, which LOGIN7 carries, for the
	// service spn.
	Start(ctx context.Context, spn string) ([]byte, error)
	// Next takes the server's token and returns the answer, nil when
	// there is none to send.
	Next(ctx context.Context, in []byte) ([]byte, error)
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
	// fedAuthEcho is the server's FEDAUTHREQUIRED, nonce its NONCEOPT.
	fedAuthEcho bool
	nonce       []byte
	// route is the server a routing ENVCHANGE sends the client on to.
	route string

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
	if cfg.AccessToken != nil && cfg.SSPI != nil {
		return nil, errors.New("mssql: an access token and SSPI at once")
	}
	if cfg.AccessToken != nil && cfg.Encrypt == EncryptOff {
		return nil, errors.New("mssql: an access token needs encrypt on or strict: it would go in clear")
	}
	addr := cfg.Addr
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port, addr = addr, DefaultPort, net.JoinHostPort(addr, DefaultPort)
	}
	given := cfg
	if cfg.SPN == "" {
		cfg.SPN = "MSSQLSvc/" + host + ":" + port
	}
	outer := ctx
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
	err = c.handshake(ctx, host)
	if !stop() && err != nil {
		err = fmt.Errorf("mssql: logging in to %s: %w", addr, ctx.Err())
	}
	if err != nil {
		raw.Close()
		return nil, err
	}
	c.until.Store(0)
	raw.SetDeadline(time.Time{})
	if c.route != "" && !cfg.routed {
		// Azure SQL's gateway sends the client on to the server that
		// holds the database, once; the certificate and the SPN are the
		// new host's unless the config names them.
		c.Close()
		given.Addr, given.routed = c.route, true
		return Dial(outer, given)
	}
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
func (c *Conn) handshake(ctx context.Context, host string) error {
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
	return c.login(ctx)
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
	if c.cfg.AccessToken != nil {
		opts = append(opts, struct {
			token byte
			data  []byte
		}{6, []byte{1}}) // FEDAUTHREQUIRED
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
	srv, found := byte(0), false
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
			if n >= 1 && !found {
				srv, found = b[o], true
			}
		case 6:
			// FEDAUTHREQUIRED, which LOGIN7's FEDAUTH echoes.
			c.fedAuthEcho = n >= 1 && b[o] == 1
		case 7:
			// NONCEOPT, which LOGIN7's FEDAUTH carries back.
			if n == 32 {
				c.nonce = append([]byte(nil), b[o:o+n]...)
			}
		}
	}
	if found {
		return srv, nil
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

// login sends LOGIN7 and reads the answer up to its LOGINACK and DONE,
// going through SSPI's exchange when it is integrated authentication.
func (c *Conn) login(ctx context.Context) error {
	user, password := c.cfg.User, c.cfg.Password
	var auth SSPI
	var token string
	var sspi []byte
	var err error
	switch {
	case c.cfg.AccessToken != nil:
		if token, err = c.cfg.AccessToken(ctx); err != nil {
			return fmt.Errorf("mssql: getting an access token: %w", err)
		}
		if token == "" {
			return errors.New("mssql: the access token is empty")
		}
		user, password = "", ""
	case c.cfg.SSPI != nil:
		auth = c.cfg.SSPI()
		if sspi, err = auth.Start(ctx, c.cfg.SPN); err != nil {
			return fmt.Errorf("mssql: SSPI: %w", err)
		}
		user, password = "", ""
	}
	w := c.newWriter(pktLogin7)
	w.Write(c.login7(user, password, token, sspi))
	if err := w.close(); err != nil {
		return err
	}
	for {
		var in []byte
		r := &response{c: c}
		acked := false
		r.onLoginAck = func() { acked = true }
		r.onSSPI = func(b []byte) { in = append([]byte(nil), b...) }
		// An answer that needs SSPI's next token ends with its SSPI
		// token, without a DONE.
		for !r.final && !(r.begun && c.msg.eom && len(c.msg.buf) == 0) {
			if _, err := r.step(); err != nil {
				return err
			}
		}
		if err := r.err(); err != nil {
			return fmt.Errorf("mssql: the server refused the login: %w", err)
		}
		if in != nil {
			if auth == nil {
				return c.fail(errors.New("mssql: the server sent an SSPI token to a login without SSPI"))
			}
			out, err := auth.Next(ctx, in)
			if err != nil {
				return c.fail(fmt.Errorf("mssql: SSPI: %w", err))
			}
			if !r.final {
				w := c.newWriter(pktSSPI)
				w.Write(out)
				if err := w.close(); err != nil {
					return err
				}
				continue
			}
		}
		if !r.final {
			return c.fail(errors.New("mssql: the server's login answer ended without a DONE"))
		}
		if !acked {
			return errors.New("mssql: the server answered the login without a LOGINACK")
		}
		return nil
	}
}

// login7 is the LOGIN7 message: SQL authentication with user and
// password, FEDAUTH's security token with token, or SSPI's first token.
func (c *Conn) login7(user, password, token string, sspi []byte) []byte {
	hostname, _ := os.Hostname()
	strs := []string{hostname, user, "", c.cfg.AppName, c.host, "", "fit", "", c.cfg.Database}
	const fixed = 94
	b := make([]byte, fixed, fixed+256+len(sspi)+2*len(token))
	binary.LittleEndian.PutUint32(b[4:], tdsVersion)
	binary.LittleEndian.PutUint32(b[8:], uint32(c.cfg.PacketSize))
	binary.LittleEndian.PutUint32(b[12:], 7)
	binary.LittleEndian.PutUint32(b[16:], uint32(os.Getpid()))
	b[24] = 0xe0 // USE_DB_ON, INIT_DB_FATAL, SET_LANG_ON
	b[25] = 0x03 // INIT_LANG_FATAL, ODBC_ON
	if sspi != nil {
		b[25] |= 0x80 // fIntSecurity
	}
	if token != "" {
		b[27] = 0x10 // fExtension
	}
	binary.LittleEndian.PutUint32(b[32:], 0x0409)
	at, ext := 36, 0
	for i, s := range strs {
		u := appendUCS2(nil, s)
		if i == 2 {
			u = appendUCS2(nil, password)
			for j, x := range u {
				u[j] = (x<<4 | x>>4) ^ 0xa5
			}
		}
		binary.LittleEndian.PutUint16(b[at:], uint16(len(b)))
		binary.LittleEndian.PutUint16(b[at+2:], uint16(len(u)/2))
		if i == 5 && token != "" {
			// ibExtension: the offset of a DWORD, cbExtension its 4
			// bytes; the DWORD is the feature extensions' offset.
			binary.LittleEndian.PutUint16(b[at+2:], 4)
			ext = len(b)
			u = make([]byte, 4)
		}
		b = append(b, u...)
		at += 4
	}
	// ClientID (6 bytes), then SSPI, AtchDBFile and ChangePassword at the
	// end of the data, and cbSSPILong.
	binary.LittleEndian.PutUint16(b[78:], uint16(len(b)))
	if len(sspi) < 0xffff {
		binary.LittleEndian.PutUint16(b[80:], uint16(len(sspi)))
	} else {
		binary.LittleEndian.PutUint16(b[80:], 0xffff)
		binary.LittleEndian.PutUint32(b[90:], uint32(len(sspi)))
	}
	b = append(b, sspi...)
	for _, o := range []int{82, 86} {
		binary.LittleEndian.PutUint16(b[o:], uint16(len(b)))
	}
	if token != "" {
		binary.LittleEndian.PutUint32(b[ext:], uint32(len(b)))
		b = appendFedAuth(b, token, c.fedAuthEcho, c.nonce)
		b = append(b, 0xff)
	}
	binary.LittleEndian.PutUint32(b, uint32(len(b)))
	return b
}

// appendFedAuth appends the FEDAUTH feature extension of a security token:
// its options (the library, security token, and the echo of the server's
// FEDAUTHREQUIRED), the token in UTF-16LE, and the server's nonce.
func appendFedAuth(b []byte, token string, echo bool, nonce []byte) []byte {
	const featureFedAuth, librarySecurityToken = 0x02, 0x01
	t := appendUCS2(nil, token)
	data := []byte{librarySecurityToken << 1}
	if echo {
		data[0] |= 1
	}
	data = binary.LittleEndian.AppendUint32(data, uint32(len(t)))
	data = append(append(data, t...), nonce...)
	b = append(b, featureFedAuth)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	return append(b, data...)
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
