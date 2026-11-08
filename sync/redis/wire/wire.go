// Package wire is a client of RESP, the protocol of Redis and Valkey, in
// its versions 2 and 3: a connection dialled over TCP or TLS, authenticated
// with a password or an ACL user, on which commands are sent one at a time or
// pipelined, their replies read within bounds. It is sync's own, written in
// fit as decision 036 has the protocols be; a Conn is used by one goroutine at
// a time.
package wire

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Kind is the type of a reply.
type Kind uint8

// The kinds of a reply. RESP2 has the simple string, the error, the integer,
// the bulk string, the array and null; RESP3 adds the others.
const (
	KindNull Kind = iota
	KindString
	KindError
	KindInt
	KindBulk
	KindArray
	KindDouble
	KindBool
	KindBigNumber
	KindVerbatim
	KindMap
	KindSet
	KindPush
)

var kindNames = [...]string{"null", "simple string", "error", "integer", "bulk string", "array", "double",
	"boolean", "big number", "verbatim string", "map", "set", "push"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// Value is a reply.
type Value struct {
	Kind Kind
	// Str is a simple string's, an error's, a bulk string's, a big number's
	// or a double's text, and a verbatim string's without its format.
	Str string
	// Int is an integer's, or 1 for a true boolean.
	Int int64
	// Float is a double's.
	Float float64
	// Elems are an array's, a set's or a push's elements, or a map's keys
	// and values, one after the other.
	Elems []Value
}

// IsNull reports whether v is null: RESP3's null, or RESP2's null bulk
// string or array.
func (v Value) IsNull() bool { return v.Kind == KindNull }

// Err returns v as an *Error when it is one, nil otherwise.
func (v Value) Err() error {
	if v.Kind == KindError {
		return &Error{Msg: v.Str}
	}
	return nil
}

// Text returns v's text, for the kinds that have one: a string of any kind,
// an integer or a double.
func (v Value) Text() (string, bool) {
	switch v.Kind {
	case KindString, KindBulk, KindVerbatim, KindBigNumber, KindDouble:
		return v.Str, true
	case KindInt:
		return strconv.FormatInt(v.Int, 10), true
	}
	return "", false
}

// Pairs returns a map's keys and values, one after the other, from RESP3's
// map or from the even array RESP2 has for one.
func (v Value) Pairs() ([]Value, bool) {
	switch v.Kind {
	case KindMap:
		return v.Elems, true
	case KindArray:
		return v.Elems, len(v.Elems)%2 == 0
	}
	return nil, false
}

// Error is an error the server answered with.
type Error struct{ Msg string }

func (e *Error) Error() string { return "redis: " + e.Msg }

// Code returns the error's first word, as ERR, WRONGTYPE or NOAUTH.
func (e *Error) Code() string {
	code, _, _ := strings.Cut(e.Msg, " ")
	return code
}

// Limits bound what a reply may hold.
type Limits struct {
	// MaxBulk is the most bytes of one string: 64 MiB when 0.
	MaxBulk int
	// MaxElems is the most elements of one reply, its nested aggregates'
	// counted too: 1<<20 when 0.
	MaxElems int
	// MaxBytes is the most bytes of one reply's strings, together: 256 MiB
	// when 0.
	MaxBytes int
	// MaxDepth is how deep aggregates may nest: 8 when 0.
	MaxDepth int
}

// The default limits.
const (
	DefaultMaxBulk  = 64 << 20
	DefaultMaxElems = 1 << 20
	DefaultMaxBytes = 256 << 20
	DefaultMaxDepth = 8
)

func (l Limits) withDefaults() Limits {
	if l.MaxBulk <= 0 {
		l.MaxBulk = DefaultMaxBulk
	}
	if l.MaxElems <= 0 {
		l.MaxElems = DefaultMaxElems
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	return l
}

// Options say how to reach a server.
type Options struct {
	// Addr is the server's host and port.
	Addr string
	// Username is the ACL user authenticated as, with Password; default
	// when empty and Password is set.
	Username string
	// Password authenticates when set.
	Password string
	// DB is the database selected, when not 0.
	DB int
	// TLS, when set, wraps the connection in TLS with this config.
	TLS *tls.Config
	// Protocol is the RESP version spoken: 2, 3, or 0 for 3 when the
	// server knows HELLO and 2 otherwise.
	Protocol int
	// DialTimeout bounds dialling, the TLS handshake and authentication,
	// within ctx's deadline: 10s when 0.
	DialTimeout time.Duration
	// ClientName, when set, names the connection (CLIENT SETNAME).
	ClientName string
	Limits     Limits
}

// Conn is a connection to a server, used by one goroutine at a time.
type Conn struct {
	nc     net.Conn
	r      *bufio.Reader
	w      *bufio.Writer
	lim    Limits
	proto  int
	broken error
	// budget is what is left of the reply being read.
	elems, bytes int
}

// ErrBroken wraps the error that broke a connection: a Conn is broken by an
// error of the transport, a reply out of its limits, or a context ending
// while a command was in flight, and is closed then.
var ErrBroken = errors.New("redis: the connection is broken")

// Dial connects to the server, wraps the connection in TLS, says HELLO in
// the protocol asked (falling back to RESP2 for 0 when the server does not
// know it), authenticates, names the connection and selects the database.
func Dial(ctx context.Context, o Options) (*Conn, error) {
	t := o.DialTimeout
	if t <= 0 {
		t = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	d := net.Dialer{KeepAlive: 15 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", o.Addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := nc.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	if o.TLS != nil {
		cfg := o.TLS
		if cfg.ServerName == "" && !cfg.InsecureSkipVerify {
			cfg = cfg.Clone()
			cfg.ServerName, _, _ = net.SplitHostPort(o.Addr)
		}
		tc := tls.Client(nc, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			nc.Close()
			return nil, err
		}
		nc = tc
	}
	c := &Conn{nc: nc, r: bufio.NewReaderSize(nc, 32<<10), w: bufio.NewWriterSize(nc, 32<<10), lim: o.Limits.withDefaults(), proto: 2}
	if err := c.handshake(ctx, o); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Conn) handshake(ctx context.Context, o Options) error {
	user := o.Username
	if user == "" && o.Password != "" {
		user = "default"
	}
	if o.Protocol != 2 {
		hello := []string{"HELLO", "3"}
		if o.Password != "" {
			hello = append(hello, "AUTH", user, o.Password)
		}
		if o.ClientName != "" {
			hello = append(hello, "SETNAME", o.ClientName)
		}
		v, err := c.Do(ctx, hello...)
		if err != nil {
			return err
		}
		var re *Error
		switch err := v.Err(); {
		case err == nil:
			c.proto = 3
		case o.Protocol == 0 && errors.As(err, &re) && (re.Code() == "NOPROTO" || strings.HasPrefix(re.Msg, "ERR unknown command")):
			// RESP2 only: the server is older than Redis 6.
		default:
			return fmt.Errorf("HELLO: %w", err)
		}
	}
	if c.proto == 2 {
		if o.Password != "" {
			args := []string{"AUTH", o.Password}
			if o.Username != "" {
				args = []string{"AUTH", o.Username, o.Password}
			}
			if err := c.ok(ctx, args...); err != nil {
				return fmt.Errorf("AUTH: %w", err)
			}
		}
		if o.ClientName != "" {
			if err := c.ok(ctx, "CLIENT", "SETNAME", o.ClientName); err != nil {
				return fmt.Errorf("CLIENT SETNAME: %w", err)
			}
		}
	}
	if o.DB != 0 {
		if err := c.ok(ctx, "SELECT", strconv.Itoa(o.DB)); err != nil {
			return fmt.Errorf("SELECT: %w", err)
		}
	}
	return nil
}

// ok sends a command, returning the error it was answered with, if any.
func (c *Conn) ok(ctx context.Context, args ...string) error {
	v, err := c.Do(ctx, args...)
	if err != nil {
		return err
	}
	return v.Err()
}

// Protocol returns the RESP version the connection speaks: 2 or 3.
func (c *Conn) Protocol() int { return c.proto }

// Broken returns the error that broke the connection, nil while it works.
func (c *Conn) Broken() error { return c.broken }

// Close closes the connection.
func (c *Conn) Close() error {
	if c.broken == nil {
		c.broken = fmt.Errorf("%w: closed", ErrBroken)
	}
	return c.nc.Close()
}

// Do sends a command and reads its reply. An error the server answers with
// is the reply, of KindError; Do's own error says the connection broke.
func (c *Conn) Do(ctx context.Context, args ...string) (Value, error) {
	vs, err := c.Pipeline(ctx, [][]string{args})
	if err != nil {
		return Value{}, err
	}
	return vs[0], nil
}

// aLongTimeAgo is a deadline that has passed, which wakes a blocked read.
var aLongTimeAgo = time.Unix(1, 0)

// Pipeline sends the commands at once and reads their replies, in order:
// one round trip. Errors the server answers with are replies, of KindError;
// Pipeline's own error says the connection broke, and how many commands ran
// is then unknown. ctx's deadline bounds the round trip, and ctx's end
// interrupts it, breaking the connection. Push messages (RESP3) are
// dropped, and attributes skipped.
func (c *Conn) Pipeline(ctx context.Context, cmds [][]string) (_ []Value, err error) {
	if c.broken != nil {
		return nil, c.broken
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.nc.SetDeadline(dl)
	} else {
		_ = c.nc.SetDeadline(time.Time{})
	}
	woke := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = c.nc.SetDeadline(aLongTimeAgo)
		close(woke)
	})
	defer func() {
		if !stop() {
			<-woke
		}
		if err != nil {
			var ne net.Error
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if _, ok := ctx.Deadline(); ok && errors.As(err, &ne) && ne.Timeout() {
				// The connection's deadline, ctx's, passed before ctx noticed.
				err = context.DeadlineExceeded
			}
			c.broken = fmt.Errorf("%w: %v", ErrBroken, err)
			c.nc.Close()
		}
	}()
	for _, args := range cmds {
		c.writeCommand(args)
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	out := make([]Value, 0, len(cmds))
	for len(out) < len(cmds) {
		c.elems, c.bytes = c.lim.MaxElems, c.lim.MaxBytes
		v, err := c.read(0)
		if err != nil {
			return nil, err
		}
		if v.Kind == KindPush {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func (c *Conn) writeCommand(args []string) {
	var n [24]byte
	c.w.WriteByte('*')
	c.w.Write(strconv.AppendInt(n[:0], int64(len(args)), 10))
	c.w.WriteString("\r\n")
	for _, a := range args {
		c.w.WriteByte('$')
		c.w.Write(strconv.AppendInt(n[:0], int64(len(a)), 10))
		c.w.WriteString("\r\n")
		c.w.WriteString(a)
		c.w.WriteString("\r\n")
	}
}

// line reads a line, without its CRLF.
func (c *Conn) line() ([]byte, error) {
	l, err := c.r.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		return nil, errors.New("redis: a reply's line is too long")
	}
	if err != nil {
		return nil, err
	}
	if len(l) < 2 || l[len(l)-2] != '\r' {
		return nil, errors.New("redis: a reply that is not RESP")
	}
	return l[:len(l)-2], nil
}

// length parses an aggregate's or a string's length; -1 is null.
func length(b []byte) (int, error) {
	n, err := strconv.Atoi(string(b))
	if err != nil || n < -1 {
		return 0, fmt.Errorf("redis: a length of %q", b)
	}
	return n, nil
}

// read reads one value, at depth.
func (c *Conn) read(depth int) (Value, error) {
	l, err := c.line()
	if err != nil {
		return Value{}, err
	}
	if len(l) == 0 {
		return Value{}, errors.New("redis: an empty line in a reply")
	}
	kind, rest := l[0], l[1:]
	switch kind {
	case '+':
		return Value{Kind: KindString, Str: string(rest)}, nil
	case '-':
		return Value{Kind: KindError, Str: string(rest)}, nil
	case ':':
		n, err := strconv.ParseInt(string(rest), 10, 64)
		if err != nil {
			return Value{}, fmt.Errorf("redis: an integer of %q", rest)
		}
		return Value{Kind: KindInt, Int: n}, nil
	case '_':
		return Value{}, nil
	case '#':
		switch string(rest) {
		case "t":
			return Value{Kind: KindBool, Int: 1}, nil
		case "f":
			return Value{Kind: KindBool}, nil
		}
		return Value{}, fmt.Errorf("redis: a boolean of %q", rest)
	case ',':
		s := string(rest)
		f, err := strconv.ParseFloat(s, 64) // inf, -inf and nan among them
		if err != nil {
			return Value{}, fmt.Errorf("redis: a double of %q", rest)
		}
		return Value{Kind: KindDouble, Str: s, Float: f}, nil
	case '(':
		return Value{Kind: KindBigNumber, Str: string(rest)}, nil
	case '$', '!', '=':
		n, err := length(rest)
		if err != nil {
			return Value{}, err
		}
		if n == -1 {
			return Value{}, nil
		}
		s, err := c.bulk(n)
		if err != nil {
			return Value{}, err
		}
		switch kind {
		case '!':
			return Value{Kind: KindError, Str: s}, nil
		case '=':
			if len(s) < 4 || s[3] != ':' {
				return Value{}, errors.New("redis: a verbatim string without its format")
			}
			return Value{Kind: KindVerbatim, Str: s[4:]}, nil
		}
		return Value{Kind: KindBulk, Str: s}, nil
	case '*', '~', '>', '%', '|':
		n, err := length(rest)
		if err != nil {
			return Value{}, err
		}
		if n == -1 {
			return Value{}, nil
		}
		if depth >= c.lim.MaxDepth {
			return Value{}, fmt.Errorf("redis: a reply nested more than %d deep", c.lim.MaxDepth)
		}
		count := n
		if kind == '%' || kind == '|' {
			count = 2 * n
		}
		if c.elems -= count; c.elems < 0 {
			return Value{}, fmt.Errorf("redis: a reply of more than %d elements", c.lim.MaxElems)
		}
		elems := make([]Value, count)
		for i := range elems {
			if elems[i], err = c.read(depth + 1); err != nil {
				return Value{}, err
			}
		}
		switch kind {
		case '~':
			return Value{Kind: KindSet, Elems: elems}, nil
		case '>':
			return Value{Kind: KindPush, Elems: elems}, nil
		case '%':
			return Value{Kind: KindMap, Elems: elems}, nil
		case '|':
			// An attribute: what it says of the reply after it is dropped.
			return c.read(depth)
		}
		return Value{Kind: KindArray, Elems: elems}, nil
	}
	return Value{}, fmt.Errorf("redis: a reply of type %q", kind)
}

// bulk reads a string of n bytes and its CRLF.
func (c *Conn) bulk(n int) (string, error) {
	if n > c.lim.MaxBulk {
		return "", fmt.Errorf("redis: a string of %d bytes, more than %d", n, c.lim.MaxBulk)
	}
	if c.bytes -= n; c.bytes < 0 {
		return "", fmt.Errorf("redis: a reply of more than %d bytes", c.lim.MaxBytes)
	}
	buf := make([]byte, n+2)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return "", err
	}
	if buf[n] != '\r' || buf[n+1] != '\n' {
		return "", errors.New("redis: a string not followed by CRLF")
	}
	return string(buf[:n]), nil
}
