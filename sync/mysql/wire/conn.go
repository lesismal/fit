// Package mysqlwire is a client of MySQL's and MariaDB's protocol, written
// for sync (decision 036): a connection's handshake, TLS, the password
// plugins mysql_native_password, caching_sha2_password (its fast path, and
// its full authentication over TLS or with the server's RSA key),
// sha256_password and mysql_clear_password, text queries and their result
// sets, prepared statements with the binary protocol, multi-row inserts
// with parameters and transactions. It has no dependencies past the
// standard library.
//
// A Conn is a blocking connection for one goroutine at a time, as sync
// runs a database's connections: few, long, each on a goroutine of its
// own, off fib's workers. Every call takes a context, whose deadline and
// cancellation bound it, and every wait for the server is bounded by
// Config.Timeout as well; a call ended by either, or by a broken or
// malformed stream, closes the connection, since what the server sends
// next can no longer be told apart. An error the server answers (*Error)
// leaves the connection usable.
//
// Values come back typed by their column: int64 or uint64 (by the
// column's UNSIGNED flag) for the integer types and YEAR, float32 for
// FLOAT, float64 for DOUBLE, DateTime for DATE, DATETIME and TIMESTAMP,
// time.Duration for TIME, nil for NULL, and []byte for everything else
// (character and binary strings, DECIMAL as its text, BIT, ENUM, SET,
// JSON, GEOMETRY), the same from the text protocol and the binary one.
package mysqlwire

import (
	"bufio"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// The defaults of Config.
const (
	DefaultTimeout       = 60 * time.Second
	DefaultMaxPacketSize = 64 << 20
	// DefaultCollation is utf8mb4_general_ci, which every supported
	// server has.
	DefaultCollation = 45
)

var (
	// ErrMalformed is a packet that does not parse, or out of order.
	ErrMalformed = errors.New("mysql: malformed packet")
	// ErrPacketTooLarge is a packet past Config.MaxPacketSize.
	ErrPacketTooLarge = errors.New("mysql: packet too large")
	// ErrBusy is a call made while a result's rows are still open.
	ErrBusy = errors.New("mysql: the connection's last result is still open")
	// ErrClosed is a call on a closed connection.
	ErrClosed = errors.New("mysql: connection closed")
)

// Config is how to reach and log in to a server.
type Config struct {
	// Network is "tcp" (the default) or "unix".
	Network string
	// Addr is host:port, or a socket's path.
	Addr     string
	User     string
	Password string
	// Database, when set, is the connection's default database.
	Database string

	// TLS, when set, is required: a server that does not offer it is
	// refused. When it has neither ServerName nor InsecureSkipVerify, the
	// host of Addr is the name verified. There is no "preferred" mode
	// falling back to plain text.
	TLS *tls.Config
	// ServerPublicKey is the server's RSA key, for caching_sha2_password's
	// and sha256_password's full authentication without TLS.
	ServerPublicKey *rsa.PublicKey
	// AllowPublicKeyRetrieval lets the client ask the server for its RSA
	// key when it needs one and has none, which a man in the middle can
	// answer with a key of its own.
	AllowPublicKeyRetrieval bool
	// AllowCleartextPassword allows mysql_clear_password over a connection
	// that is neither TLS nor a Unix socket.
	AllowCleartextPassword bool

	// Timeout bounds every wait for the server, the dial included, and
	// each read and write of a call; a context's deadline may shorten it.
	// Zero is DefaultTimeout; negative is none.
	Timeout time.Duration
	// MaxPacketSize bounds a packet read, a row with all its columns; zero
	// is DefaultMaxPacketSize.
	MaxPacketSize int
	// Collation is the connection's character set and collation, by ID;
	// zero is DefaultCollation.
	Collation uint8
	// Location is the zone of time.Time parameters, which are sent as
	// DATETIME; nil is UTC.
	Location *time.Location
}

// Error is an error the server answered. The connection stays usable.
type Error struct {
	Code    uint16
	State   string
	Message string
}

func (e *Error) Error() string {
	if e.State != "" {
		return fmt.Sprintf("mysql: error %d (%s): %s", e.Code, e.State, e.Message)
	}
	return fmt.Sprintf("mysql: error %d: %s", e.Code, e.Message)
}

// Result is what a statement that returns no rows answers.
type Result struct {
	AffectedRows uint64
	LastInsertID uint64
	Status       uint16
	Warnings     uint16
	Info         string
}

// Server status flags.
const (
	StatusInTrans          = 0x0001
	StatusAutocommit       = 0x0002
	StatusMoreResultsExist = 0x0008
)

// Conn is a connection to a server, for one goroutine at a time.
type Conn struct {
	cfg       Config
	timeout   time.Duration
	maxPacket int
	loc       *time.Location

	raw *deadlineConn
	nc  net.Conn // raw, or TLS over it
	br  *bufio.Reader

	seq  uint8
	rbuf []byte
	wbuf []byte

	ptypes, pvals []byte // a statement's parameters, being encoded

	caps     uint32
	status   uint16
	version  string
	mariadb  bool
	connID   uint32
	secure   bool
	broken   error
	rows     *Rows
	ctx      context.Context
	stop     func() bool
	maxAllow int
	// dumping says the connection is a binlog's stream (StartBinlog).
	dumping bool
}

// deadlineConn sets the deadline of each read and write: Timeout from
// now, or the call's context's deadline when it comes first, or the past
// once the context is done.
type deadlineConn struct {
	net.Conn
	timeout time.Duration

	mu        sync.Mutex
	deadline  time.Time
	cancelled bool
}

var longAgo = time.Unix(1, 0)

func (d *deadlineConn) arm(set func(time.Time) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancelled {
		_ = set(longAgo)
		return
	}
	var t time.Time
	if d.timeout > 0 {
		t = time.Now().Add(d.timeout)
	}
	if !d.deadline.IsZero() && (t.IsZero() || d.deadline.Before(t)) {
		t = d.deadline
	}
	_ = set(t)
}

func (d *deadlineConn) Read(b []byte) (int, error) {
	d.arm(d.Conn.SetReadDeadline)
	return d.Conn.Read(b)
}

func (d *deadlineConn) Write(b []byte) (int, error) {
	d.arm(d.Conn.SetWriteDeadline)
	return d.Conn.Write(b)
}

func (d *deadlineConn) cancel() {
	d.mu.Lock()
	d.cancelled = true
	_ = d.Conn.SetDeadline(longAgo)
	d.mu.Unlock()
}

// Dial connects and logs in.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	c := &Conn{cfg: cfg, timeout: cfg.Timeout, maxPacket: cfg.MaxPacketSize, loc: cfg.Location}
	if c.timeout == 0 {
		c.timeout = DefaultTimeout
	}
	if c.maxPacket <= 0 {
		c.maxPacket = DefaultMaxPacketSize
	}
	if c.loc == nil {
		c.loc = time.UTC
	}
	if c.cfg.Collation == 0 {
		c.cfg.Collation = DefaultCollation
	}
	network := cfg.Network
	if network == "" {
		network = "tcp"
	}
	dctx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		dctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	var d net.Dialer
	nc, err := d.DialContext(dctx, network, cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("mysql: %w", err)
	}
	if tc, ok := nc.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
	}
	c.raw = &deadlineConn{Conn: nc, timeout: c.timeout}
	c.nc = c.raw
	c.br = bufio.NewReaderSize(c.nc, 64<<10)
	c.secure = network == "unix"
	if err := c.begin(ctx); err != nil {
		nc.Close()
		return nil, err
	}
	if err := c.handshake(ctx); err != nil {
		var se *Error
		if errors.As(err, &se) {
			c.end()
			nc.Close()
			return nil, err
		}
		return nil, c.fail(err)
	}
	c.end()
	return c, nil
}

// begin starts a call: no other is open, and ctx bounds it until end.
func (c *Conn) begin(ctx context.Context) error {
	if c.broken != nil {
		return c.broken
	}
	if c.rows != nil {
		return ErrBusy
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.ctx = ctx
	dl, _ := ctx.Deadline()
	c.raw.mu.Lock()
	c.raw.deadline = dl
	c.raw.cancelled = false
	c.raw.mu.Unlock()
	if ctx.Done() != nil {
		c.stop = context.AfterFunc(ctx, c.raw.cancel)
	}
	return nil
}

// end ends a call begun by begin.
func (c *Conn) end() {
	if c.stop != nil {
		c.stop()
		c.stop = nil
	}
	c.ctx = nil
}

// fail closes the connection after err, an error of the stream, and
// returns the error every later call returns.
func (c *Conn) fail(err error) error {
	if c.broken != nil {
		return c.broken
	}
	if c.ctx != nil {
		// The context ended the call, or its deadline did as the
		// connection's, a moment before the context's own timer.
		if c.ctx.Err() != nil {
			err = fmt.Errorf("%w (%w)", context.Cause(c.ctx), err)
		} else if dl, ok := c.ctx.Deadline(); ok && !time.Now().Before(dl) {
			err = fmt.Errorf("%w (%w)", context.DeadlineExceeded, err)
		}
	}
	c.broken = fmt.Errorf("mysql: connection broken: %w", err)
	c.end()
	c.rows = nil
	_ = c.raw.Conn.Close()
	return c.broken
}

// Err is the error that broke the connection, nil while it is usable.
func (c *Conn) Err() error { return c.broken }

// Close sends COM_QUIT, without waiting, and closes the connection.
func (c *Conn) Close() error {
	if c.broken != nil {
		c.broken = errClosedConn
		return nil
	}
	c.raw.mu.Lock()
	c.raw.deadline = time.Now().Add(time.Second)
	c.raw.cancelled = false
	c.raw.mu.Unlock()
	if c.rows == nil && !c.dumping {
		_ = c.writePacket(c.startCommand(comQuit))
	}
	c.end()
	c.rows = nil
	c.broken = errClosedConn
	return c.raw.Conn.Close()
}

var errClosedConn = fmt.Errorf("%w", ErrClosed)

// ServerVersion is the version the server announced, without MariaDB's
// "5.5.5-" prefix.
func (c *Conn) ServerVersion() string { return c.version }

// IsMariaDB tells whether the server is MariaDB.
func (c *Conn) IsMariaDB() bool { return c.mariadb }

// ConnectionID is the server's ID of the connection, as KILL takes it.
func (c *Conn) ConnectionID() uint32 { return c.connID }

// Status is the server status flags of the last answer.
func (c *Conn) Status() uint16 { return c.status }

// InTransaction tells whether the server said a transaction is open.
func (c *Conn) InTransaction() bool { return c.status&StatusInTrans != 0 }

// TLS tells whether the connection is over TLS.
func (c *Conn) TLS() bool { _, ok := c.nc.(*tls.Conn); return ok }

// Commands.
const (
	comQuit        = 0x01
	comInitDB      = 0x02
	comQuery       = 0x03
	comPing        = 0x0e
	comStmtPrepare = 0x16
	comStmtExecute = 0x17
	comStmtClose   = 0x19
	comStmtReset   = 0x1a
)

// Ping sends COM_PING.
func (c *Conn) Ping(ctx context.Context) error {
	return c.simple(ctx, comPing, nil)
}

// UseDatabase makes db the connection's default database.
func (c *Conn) UseDatabase(ctx context.Context, db string) error {
	return c.simple(ctx, comInitDB, []byte(db))
}

// simple runs a command answered by OK or ERR.
func (c *Conn) simple(ctx context.Context, cmd byte, body []byte) error {
	if err := c.begin(ctx); err != nil {
		return err
	}
	defer c.end()
	if err := c.writePacket(append(c.startCommand(cmd), body...)); err != nil {
		return c.fail(err)
	}
	p, err := c.readPacket()
	if err != nil {
		return c.fail(err)
	}
	switch {
	case len(p) > 0 && p[0] == 0x00:
		_, err := c.parseOK(p)
		return err
	case len(p) > 0 && p[0] == 0xff:
		return c.parseErr(p)
	}
	return c.fail(fmt.Errorf("%w: unexpected answer 0x%x", ErrMalformed, first(p)))
}

func first(p []byte) byte {
	if len(p) == 0 {
		return 0
	}
	return p[0]
}

// parseOK reads an OK packet, or the OK or EOF that ends a result set,
// and keeps its status.
func (c *Conn) parseOK(p []byte) (Result, error) {
	r := rd{b: p[1:]}
	var res Result
	if p[0] == 0xfe && len(p) < 9 && c.caps&capDeprecateEOF == 0 {
		res.Warnings = r.u16()
		res.Status = r.u16()
	} else {
		res.AffectedRows, _ = r.lenEnc()
		res.LastInsertID, _ = r.lenEnc()
		res.Status = r.u16()
		res.Warnings = r.u16()
		if len(r.b) > 0 {
			if c.caps&capSessionTrack != 0 {
				res.Info = string(r.lenEncBytes())
			} else {
				res.Info = string(r.rest())
			}
		}
	}
	if r.bad {
		return res, c.fail(fmt.Errorf("%w: OK packet", ErrMalformed))
	}
	c.status = res.Status
	return res, nil
}

// parseErr reads an ERR packet.
func (c *Conn) parseErr(p []byte) error {
	r := rd{b: p[1:]}
	e := &Error{Code: r.u16()}
	if len(r.b) > 0 && r.b[0] == '#' {
		r.take(1)
		e.State = string(r.take(5))
	}
	e.Message = string(r.rest())
	if r.bad {
		return c.fail(fmt.Errorf("%w: ERR packet", ErrMalformed))
	}
	return e
}
