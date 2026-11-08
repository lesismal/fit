// Package pgwire is a PostgreSQL client written in fit, with no driver
// imported: the frontend/backend protocol 3.0 over TCP, TLS with sslmode,
// SCRAM-SHA-256 with and without channel binding, MD5 and cleartext
// passwords only when enabled, the simple and the extended query protocol,
// COPY FROM STDIN and TO STDOUT, and the replication connection's commands
// (IDENTIFY_SYSTEM, CREATE_REPLICATION_SLOT, DROP_REPLICATION_SLOT,
// START_REPLICATION with its CopyBoth stream and standby status updates),
// which sync's PostgreSQL source and sink build on.
//
// A Conn is a blocking connection meant for one goroutine of its own, as
// sync's connections to databases are (decision 036): few, long, and off
// fib's workers. Its methods are not safe for concurrent use, except that
// SendStandbyStatus may run while another goroutine waits in
// ReceiveReplication.
//
// Every call takes a context. When the context ends during a call, a
// CancelRequest is sent to the server and the connection is closed: the
// protocol cannot be resumed at an arbitrary point, so the caller makes a new
// one. Server errors (*Error) leave the connection usable.
package pgwire

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SSL modes, as libpq names them. There is no "prefer" or "allow": a client
// that silently falls back to plain text when TLS fails is what decision 036
// rules out.
const (
	SSLDisable    = "disable"     // no TLS
	SSLRequire    = "require"     // TLS, the server's certificate not checked
	SSLVerifyCA   = "verify-ca"   // TLS, the certificate checked against the roots, not the name
	SSLVerifyFull = "verify-full" // TLS, the certificate and the host name checked (the default)
)

// Channel binding modes for SCRAM-SHA-256-PLUS.
const (
	ChannelBindingPrefer  = "prefer"  // bind when TLS is used and the server offers it (the default)
	ChannelBindingRequire = "require" // fail unless the server is authenticated with SCRAM-SHA-256-PLUS
	ChannelBindingDisable = "disable" // never bind
)

// Config is what a connection needs.
type Config struct {
	// Addr is host:port; the port defaults to 5432.
	Addr     string
	User     string
	Password string
	// Database defaults to the user's name, as the server does.
	Database string

	// SSLMode is one of the SSL constants; empty means verify-full.
	SSLMode string
	// TLS is the base TLS configuration: roots (system roots when nil),
	// client certificates, and the server name (the host of Addr when
	// empty). It is cloned, never changed.
	TLS *tls.Config
	// ChannelBinding is one of the ChannelBinding constants; empty means
	// prefer.
	ChannelBinding string

	// AllowMD5 lets the server ask for an MD5-hashed password, which is
	// refused otherwise; AllowCleartext the same for a cleartext password.
	AllowMD5       bool
	AllowCleartext bool

	// Replication opens a logical replication connection
	// (replication=database), on which the replication commands and simple
	// queries run.
	Replication bool

	// ApplicationName defaults to "fit".
	ApplicationName string
	// Params are other startup parameters (search_path, TimeZone, ...).
	// client_encoding is always UTF8.
	Params map[string]string

	// ConnectTimeout bounds the dial, TLS and authentication when the
	// context given to Connect has no deadline of its own; default 10s.
	ConnectTimeout time.Duration
	// MaxMessageBytes bounds one message from the server, and the rows a
	// SimpleQuery keeps; default 64 MiB.
	MaxMessageBytes int

	// OnNotice, when set, is called with each NoticeResponse.
	OnNotice func(*Error)
}

// Conn is one connection to a server.
type Conn struct {
	cfg  Config
	addr string
	nc   net.Conn
	br   *bufio.Reader

	hdr  [5]byte
	rbuf []byte
	wbuf []byte
	wmu  sync.Mutex

	pid, secret uint32
	params      map[string]string
	txStatus    byte
	bound       bool // authenticated with SCRAM-SHA-256-PLUS
	busy        bool // Rows, CopyIn or a replication stream in progress
	streaming   atomic.Bool

	broken error
	closed atomic.Bool
}

// Error is an ErrorResponse (or a NoticeResponse) from the server.
type Error struct {
	Severity   string
	Code       string // SQLSTATE
	Message    string
	Detail     string
	Hint       string
	Position   string
	Where      string
	Schema     string
	Table      string
	Column     string
	DataType   string
	Constraint string
}

func (e *Error) Error() string {
	s := "postgres: " + e.Severity + ": " + e.Message + " (SQLSTATE " + e.Code + ")"
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// Errors of the client's own.
var (
	ErrMD5Refused       = errors.New("postgres: the server asks for an MD5 password, which is refused unless allowed")
	ErrCleartextRefused = errors.New("postgres: the server asks for a cleartext password, which is refused unless allowed")
	ErrTLSRefused       = errors.New("postgres: the server does not accept TLS")
	ErrChannelBinding   = errors.New("postgres: channel binding is required, and the server did not authenticate with SCRAM-SHA-256-PLUS")
	ErrBusy             = errors.New("postgres: the connection is busy with rows, a copy or a replication stream")
	ErrClosed           = errors.New("postgres: the connection is closed")
)

const (
	protocolVersion = 196608 // 3.0
	sslRequestCode  = 80877103
	cancelCode      = 80877102
)

// Connect dials, negotiates TLS, starts up and authenticates.
func Connect(ctx context.Context, cfg Config) (*Conn, error) {
	if cfg.SSLMode == "" {
		cfg.SSLMode = SSLVerifyFull
	}
	switch cfg.SSLMode {
	case SSLDisable, SSLRequire, SSLVerifyCA, SSLVerifyFull:
	default:
		return nil, fmt.Errorf("postgres: unknown sslmode %q", cfg.SSLMode)
	}
	if cfg.ChannelBinding == "" {
		cfg.ChannelBinding = ChannelBindingPrefer
	}
	switch cfg.ChannelBinding {
	case ChannelBindingPrefer, ChannelBindingRequire, ChannelBindingDisable:
	default:
		return nil, fmt.Errorf("postgres: unknown channel_binding %q", cfg.ChannelBinding)
	}
	if cfg.ChannelBinding == ChannelBindingRequire && cfg.SSLMode == SSLDisable {
		return nil, errors.New("postgres: channel binding needs TLS")
	}
	if cfg.ApplicationName == "" {
		cfg.ApplicationName = "fit"
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	if cfg.MaxMessageBytes <= 0 {
		cfg.MaxMessageBytes = 64 << 20
	}
	addr := cfg.Addr
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "5432")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.ConnectTimeout)
		defer cancel()
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	c := &Conn{cfg: cfg, addr: addr, nc: nc, params: map[string]string{}}
	stop := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	err = c.startup(ctx)
	if !stop() {
		if err == nil {
			err = ctx.Err()
		}
	}
	if err != nil {
		nc.Close()
		if ctx.Err() != nil && !isServerError(err) {
			return nil, fmt.Errorf("postgres: connect: %w", ctx.Err())
		}
		return nil, err
	}
	c.nc.SetDeadline(time.Time{})
	return c, nil
}

func isServerError(err error) bool {
	var pe *Error
	return errors.As(err, &pe)
}

func (c *Conn) startup(ctx context.Context) error {
	if c.cfg.SSLMode != SSLDisable {
		var req [8]byte
		binary.BigEndian.PutUint32(req[0:], 8)
		binary.BigEndian.PutUint32(req[4:], sslRequestCode)
		if _, err := c.nc.Write(req[:]); err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		var ans [1]byte
		if _, err := io.ReadFull(c.nc, ans[:]); err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		if ans[0] != 'S' {
			return ErrTLSRefused
		}
		tconn := tls.Client(c.nc, c.tlsConfig())
		if err := tconn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("postgres: TLS: %w", err)
		}
		c.nc = tconn
	}
	c.br = bufio.NewReaderSize(c.nc, 32<<10)

	i := c.begin(0)
	c.wbuf = binary.BigEndian.AppendUint32(c.wbuf, protocolVersion)
	put := func(k, v string) {
		c.wbuf = append(append(c.wbuf, k...), 0)
		c.wbuf = append(append(c.wbuf, v...), 0)
	}
	put("user", c.cfg.User)
	if c.cfg.Database != "" {
		put("database", c.cfg.Database)
	}
	put("client_encoding", "UTF8")
	put("application_name", c.cfg.ApplicationName)
	if c.cfg.Replication {
		put("replication", "database")
	}
	for k, v := range c.cfg.Params {
		switch k {
		case "user", "database", "client_encoding", "replication":
			continue
		}
		put(k, v)
	}
	c.wbuf = append(c.wbuf, 0)
	c.end(i)
	if err := c.flush(); err != nil {
		return err
	}
	if err := c.authenticate(); err != nil {
		return err
	}
	for {
		t, body, err := c.recv()
		if err != nil {
			return err
		}
		switch t {
		case 'K':
			if len(body) < 8 {
				return c.protoErr("BackendKeyData")
			}
			c.pid = binary.BigEndian.Uint32(body)
			c.secret = binary.BigEndian.Uint32(body[4:])
		case 'Z':
			if len(body) > 0 {
				c.txStatus = body[0]
			}
			return nil
		case 'E':
			return parseError(body)
		default:
			return c.unexpected(t, "startup")
		}
	}
}

func (c *Conn) tlsConfig() *tls.Config {
	var tc *tls.Config
	if c.cfg.TLS != nil {
		tc = c.cfg.TLS.Clone()
	} else {
		tc = &tls.Config{}
	}
	if tc.ServerName == "" {
		host, _, _ := net.SplitHostPort(c.addr)
		tc.ServerName = host
	}
	switch c.cfg.SSLMode {
	case SSLRequire:
		tc.InsecureSkipVerify = true
	case SSLVerifyCA:
		roots := tc.RootCAs
		tc.InsecureSkipVerify = true
		tc.VerifyConnection = func(cs tls.ConnectionState) error {
			// A TLS client always has the server's certificate here.
			opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
			for _, ic := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(ic)
			}
			_, err := cs.PeerCertificates[0].Verify(opts)
			return err
		}
	}
	return tc
}

// Close ends the connection with a Terminate message.
func (c *Conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	if c.broken == nil {
		c.nc.SetWriteDeadline(time.Now().Add(time.Second))
		c.wmu.Lock()
		c.nc.Write([]byte{'X', 0, 0, 0, 4})
		c.wmu.Unlock()
	}
	return c.nc.Close()
}

// Err is the error that broke the connection, or nil while it is usable.
func (c *Conn) Err() error {
	if c.broken != nil {
		return c.broken
	}
	if c.closed.Load() {
		return ErrClosed
	}
	return nil
}

// ParameterStatus returns a run-time parameter the server reported
// (server_version, TimeZone, integer_datetimes, ...).
func (c *Conn) ParameterStatus(name string) string { return c.params[name] }

// ServerVersion is server_version as a number, 170002 for 17.2, 130018 for
// 13.18; 0 when it cannot be read.
func (c *Conn) ServerVersion() int {
	v := c.params["server_version"]
	if i := strings.IndexAny(v, " ("); i >= 0 {
		v = v[:i]
	}
	// The leading digits of each part: "18beta1" is 18, "17.2" is 17 and 2.
	digits := func(s string) int {
		n := 0
		for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
			n = n*10 + int(s[i]-'0')
		}
		return n
	}
	major, minor, _ := strings.Cut(v, ".")
	ma := digits(major)
	if ma < 10 { // before 10 the numbering was different; fit needs 13 and later
		return 0
	}
	return ma*10000 + digits(minor)
}

// TxStatus is the transaction status of the last ReadyForQuery: 'I' idle,
// 'T' in a transaction, 'E' in a failed transaction.
func (c *Conn) TxStatus() byte { return c.txStatus }

// ChannelBound says whether the connection authenticated with
// SCRAM-SHA-256-PLUS, bound to its TLS channel.
func (c *Conn) ChannelBound() bool { return c.bound }

// TLS is the connection's TLS state, or nil without TLS.
func (c *Conn) TLS() *tls.ConnectionState {
	if tc, ok := c.nc.(*tls.Conn); ok {
		s := tc.ConnectionState()
		return &s
	}
	return nil
}

// BackendPID is the server process serving the connection.
func (c *Conn) BackendPID() uint32 { return c.pid }

// enter starts a call: it checks the connection, and arranges for the
// context's end to cancel the query and break the connection. The returned
// function ends the call and maps an error to the context's when it fired.
func (c *Conn) enter(ctx context.Context) (func(err error) error, error) {
	if err := c.Err(); err != nil {
		return nil, err
	}
	if c.busy {
		return nil, ErrBusy
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var fired atomic.Bool
	stop := context.AfterFunc(ctx, func() {
		fired.Store(true)
		c.nc.SetDeadline(time.Unix(1, 0))
		go c.cancelRequest()
	})
	return func(err error) error {
		if !stop() || fired.Load() {
			// The I/O error the deadline caused says less than the context.
			c.broken = fmt.Errorf("postgres: %w", ctx.Err())
			c.fail(c.broken)
			return c.broken
		}
		return err
	}, nil
}

// cancelRequest asks the server, on a connection of its own, to cancel what
// the connection runs. It is best effort.
func (c *Conn) cancelRequest() {
	if c.pid == 0 {
		return
	}
	nc, err := net.DialTimeout("tcp", c.addr, 5*time.Second)
	if err != nil {
		return
	}
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	var b [16]byte
	binary.BigEndian.PutUint32(b[0:], 16)
	binary.BigEndian.PutUint32(b[4:], cancelCode)
	binary.BigEndian.PutUint32(b[8:], c.pid)
	binary.BigEndian.PutUint32(b[12:], c.secret)
	if _, err := nc.Write(b[:]); err == nil {
		io.Copy(io.Discard, nc) // the server closes it
	}
}

// fail breaks the connection with err.
func (c *Conn) fail(err error) {
	if c.broken == nil {
		c.broken = err
	}
	c.closed.Store(true)
	c.nc.Close()
}

// begin starts a message of type t (0 for the untyped startup messages) in
// wbuf and returns where its length goes.
func (c *Conn) begin(t byte) int {
	if t != 0 {
		c.wbuf = append(c.wbuf, t)
	}
	i := len(c.wbuf)
	c.wbuf = append(c.wbuf, 0, 0, 0, 0)
	return i
}

func (c *Conn) end(i int) {
	binary.BigEndian.PutUint32(c.wbuf[i:], uint32(len(c.wbuf)-i))
}

func (c *Conn) flush() error {
	c.wmu.Lock()
	_, err := c.nc.Write(c.wbuf)
	c.wmu.Unlock()
	c.wbuf = c.wbuf[:0]
	if err != nil {
		c.fail(fmt.Errorf("postgres: %w", err))
		return c.broken
	}
	return nil
}

// recv reads the next message, handling the asynchronous ones (notices,
// parameter status, notifications). The body is valid until the next recv.
func (c *Conn) recv() (byte, []byte, error) {
	for {
		t, body, err := c.recvRaw()
		if err != nil {
			return 0, nil, err
		}
		switch t {
		case 'N':
			if c.cfg.OnNotice != nil {
				c.cfg.OnNotice(parseError(body))
			}
		case 'S':
			k, rest, ok := cstring(body)
			if ok {
				v, _, _ := cstring(rest)
				c.params[k] = v
			}
		case 'A':
		default:
			return t, body, nil
		}
	}
}

func (c *Conn) recvRaw() (byte, []byte, error) {
	if c.broken != nil {
		return 0, nil, c.broken
	}
	if _, err := io.ReadFull(c.br, c.hdr[:]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		c.fail(fmt.Errorf("postgres: %w", err))
		return 0, nil, c.broken
	}
	n := int(binary.BigEndian.Uint32(c.hdr[1:])) - 4
	if n < 0 || n > c.cfg.MaxMessageBytes {
		c.fail(fmt.Errorf("postgres: a message of %d bytes, beyond the bound of %d", n, c.cfg.MaxMessageBytes))
		return 0, nil, c.broken
	}
	if cap(c.rbuf) < n {
		c.rbuf = make([]byte, n)
	}
	c.rbuf = c.rbuf[:n]
	if _, err := io.ReadFull(c.br, c.rbuf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		c.fail(fmt.Errorf("postgres: %w", err))
		return 0, nil, c.broken
	}
	return c.hdr[0], c.rbuf, nil
}

// drain reads up to ReadyForQuery, keeping the first server error.
func (c *Conn) drain(err error) error {
	for {
		t, body, rerr := c.recv()
		if rerr != nil {
			return rerr
		}
		switch t {
		case 'E':
			if err == nil {
				err = parseError(body)
			}
		case 'Z':
			if len(body) > 0 {
				c.txStatus = body[0]
			}
			return err
		}
	}
}

func (c *Conn) protoErr(what string) error {
	c.fail(fmt.Errorf("postgres: a malformed %s message", what))
	return c.broken
}

func (c *Conn) unexpected(t byte, during string) error {
	c.fail(fmt.Errorf("postgres: an unexpected message %q during %s", t, during))
	return c.broken
}

func parseError(b []byte) *Error {
	e := &Error{}
	for len(b) > 0 && b[0] != 0 {
		f := b[0]
		v, rest, ok := cstring(b[1:])
		if !ok {
			break
		}
		b = rest
		switch f {
		case 'V':
			e.Severity = v
		case 'S':
			if e.Severity == "" {
				e.Severity = v
			}
		case 'C':
			e.Code = v
		case 'M':
			e.Message = v
		case 'D':
			e.Detail = v
		case 'H':
			e.Hint = v
		case 'P':
			e.Position = v
		case 'W':
			e.Where = v
		case 's':
			e.Schema = v
		case 't':
			e.Table = v
		case 'c':
			e.Column = v
		case 'd':
			e.DataType = v
		case 'n':
			e.Constraint = v
		}
	}
	return e
}

func cstring(b []byte) (string, []byte, bool) {
	for i, x := range b {
		if x == 0 {
			return string(b[:i]), b[i+1:], true
		}
	}
	return "", nil, false
}

// QuoteIdentifier quotes a name for SQL: "a""b".
func QuoteIdentifier(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// QuoteLiteral quotes a string for SQL as a standard-conforming literal,
// with E” when it has a backslash.
func QuoteLiteral(s string) string {
	q := "'" + strings.ReplaceAll(s, "'", "''") + "'"
	if strings.Contains(s, `\`) {
		q = "E" + strings.ReplaceAll(q, `\`, `\\`)
	}
	return q
}
