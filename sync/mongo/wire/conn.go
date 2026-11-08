package mongowire

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"slices"
	"sync"
	"time"
)

// minWireVersion is MongoDB 4.2's; fit is tested with 6.0, 7.0 and 8.0.
const minWireVersion = 8

// Conn is one connection to a server: commands are sent one at a time and
// each waits for its answer. A Conn whose I/O failed is closed and returns
// the error for every command after it.
type Conn struct {
	addr    string
	nc      net.Conn
	rd      *bufio.Reader
	wbuf    []byte
	nextID  int32
	hello   Hello
	maxMsg  int
	timeout time.Duration

	mu  sync.Mutex
	err error

	gen uint64 // the Client's generation it was made in
}

// Hello is what a server says of itself in answer to hello.
type Hello struct {
	IsWritablePrimary bool
	Secondary         bool
	// SetName is the replica set's name; "" for a standalone server or a
	// mongos.
	SetName string
	// Primary is the primary's address as the server knows it.
	Primary string
	Me      string
	// Hosts are the set's members that can be primary, and its passives.
	Hosts []string
	// Msg is "isdbgrid" for a mongos.
	Msg                 string
	MaxBSONObjectSize   int
	MaxMessageSizeBytes int
	MaxWriteBatchSize   int
	MinWireVersion      int
	MaxWireVersion      int
	SaslSupportedMechs  []string
	// Reply is the whole answer.
	Reply Raw
}

func readHello(r Raw) Hello {
	h := Hello{Reply: r, MaxBSONObjectSize: 16 << 20, MaxMessageSizeBytes: 48_000_000, MaxWriteBatchSize: 100_000}
	str := func(k string) string {
		v, _ := r.Lookup(k)
		s, _ := v.StringValue()
		return s
	}
	num := func(k string, def int) int {
		if v, ok := r.Lookup(k); ok {
			if n, ok := v.Int(); ok && n > 0 {
				return int(n)
			}
		}
		return def
	}
	strs := func(k string) []string {
		v, _ := r.Lookup(k)
		var out []string
		for _, e := range v.Array() {
			if s, ok := e.StringValue(); ok {
				out = append(out, s)
			}
		}
		return out
	}
	if v, ok := r.Lookup("isWritablePrimary"); ok {
		h.IsWritablePrimary = v.Truthy()
	} else if v, ok := r.Lookup("ismaster"); ok {
		h.IsWritablePrimary = v.Truthy()
	}
	if v, ok := r.Lookup("secondary"); ok {
		h.Secondary = v.Truthy()
	}
	h.SetName, h.Primary, h.Me, h.Msg = str("setName"), str("primary"), str("me"), str("msg")
	h.Hosts = append(strs("hosts"), strs("passives")...)
	h.MaxBSONObjectSize = num("maxBsonObjectSize", h.MaxBSONObjectSize)
	h.MaxMessageSizeBytes = num("maxMessageSizeBytes", h.MaxMessageSizeBytes)
	h.MaxWriteBatchSize = num("maxWriteBatchSize", h.MaxWriteBatchSize)
	h.MinWireVersion = num("minWireVersion", 0)
	h.MaxWireVersion = num("maxWireVersion", 0)
	h.SaslSupportedMechs = strs("saslSupportedMechs")
	return h
}

// Dial connects to the server at addr ("host:port"), says hello and, with
// a username, authenticates; o's Hosts and ReplicaSet are not used.
func Dial(ctx context.Context, addr string, o Options) (*Conn, error) {
	o = o.withDefaults()
	c, err := dialHello(ctx, addr, &o)
	if err != nil {
		return nil, err
	}
	if err := c.authenticate(ctx, &o, nil); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// dialHello connects to addr and says hello.
func dialHello(ctx context.Context, addr string, o *Options) (*Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, o.ConnectTimeout)
	defer cancel()
	d := net.Dialer{KeepAlive: 30 * time.Second}
	nc, err := d.DialContext(dctx, "tcp", addr)
	if err != nil {
		return nil, &NetError{Addr: addr, Err: err}
	}
	if tc, ok := nc.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	if o.TLS != nil {
		cfg := o.TLS.Clone()
		if cfg.ServerName == "" {
			if host, _, err := net.SplitHostPort(addr); err == nil {
				cfg.ServerName = host
			}
		}
		tc := tls.Client(nc, cfg)
		if err := tc.HandshakeContext(dctx); err != nil {
			nc.Close()
			return nil, &NetError{Addr: addr, Err: err}
		}
		nc = tc
	}
	c := &Conn{addr: addr, nc: nc, rd: bufio.NewReaderSize(nc, 32<<10), maxMsg: o.MaxMessageBytes, timeout: o.Timeout}
	cmd := D{{"hello", int32(1)}, {"helloOk", true}, {"client", clientMetadata(o.AppName)}}
	if o.Username != "" {
		cmd = append(cmd, E{"saslSupportedMechs", o.AuthSource + "." + o.Username})
	}
	hctx, cancel2 := context.WithTimeout(ctx, o.ConnectTimeout)
	defer cancel2()
	reply, err := c.Run(hctx, "admin", cmd)
	if err != nil {
		c.Close()
		return nil, err
	}
	c.hello = readHello(reply)
	if c.hello.MaxWireVersion < minWireVersion {
		c.Close()
		return nil, fmt.Errorf("mongowire: %s: wire version %d is older than MongoDB 4.2's", addr, c.hello.MaxWireVersion)
	}
	if m := c.hello.MaxMessageSizeBytes; m < c.maxMsg {
		c.maxMsg = m
	}
	return c, nil
}

func clientMetadata(app string) D {
	md := D{
		{"driver", D{{"name", "fit-mongowire"}, {"version", "1"}}},
		{"os", D{{"type", runtime.GOOS}, {"architecture", runtime.GOARCH}}},
		{"platform", runtime.Version()},
	}
	if app != "" {
		if len(app) > 128 {
			app = app[:128]
		}
		md = append(md, E{"application", D{{"name", app}}})
	}
	return md
}

// authenticate runs SCRAM-SHA-256 when o has a username.
func (c *Conn) authenticate(ctx context.Context, o *Options, cache *saltCache) error {
	if o.Username == "" {
		return nil
	}
	if m := c.hello.SaslSupportedMechs; len(m) > 0 && !slices.Contains(m, "SCRAM-SHA-256") {
		return fmt.Errorf("mongowire: %s offers %v for %s, not SCRAM-SHA-256", c.addr, m, o.Username)
	}
	actx, cancel := context.WithTimeout(ctx, o.ConnectTimeout)
	defer cancel()
	return scramAuth(actx, c, o.AuthSource, o.Username, o.Password, cache)
}

// Addr returns the server's address.
func (c *Conn) Addr() string { return c.addr }

// Hello returns what the server said in answer to hello.
func (c *Conn) Hello() Hello { return c.hello }

// Err returns the error that broke the connection, or nil.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close closes the connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = &NetError{Addr: c.addr, Err: net.ErrClosed}
	}
	return c.nc.Close()
}

// fail closes the connection for err and returns err as a NetError.
func (c *Conn) fail(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		err = fmt.Errorf("%w (%v)", cerr, err)
	} else if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
		// The socket's deadline, the context's, ran out before the
		// context's own timer said so.
		err = fmt.Errorf("%w (%v)", context.DeadlineExceeded, err)
	}
	ne := &NetError{Addr: c.addr, Err: err}
	c.err = ne
	c.nc.Close()
	return ne
}

// Run sends cmd to database db, with the document sequences seqs, and
// returns the answer; an answer with ok: 0 is a *CommandError. ctx's
// deadline, or the connection's timeout when it has none, bounds the
// round trip; a round trip cut by either closes the connection, since its
// answer may still come.
func (c *Conn) Run(ctx context.Context, db string, cmd D, seqs ...Sequence) (Raw, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	body := make(D, 0, len(cmd)+1)
	body = append(append(body, cmd...), E{"$db", db})
	raw, err := Marshal(body)
	if err != nil {
		return nil, err
	}
	if n := msgSize(raw, seqs); n > c.maxMsg {
		return nil, fmt.Errorf("mongowire: a message of %d bytes is larger than the server's %d", n, c.maxMsg)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok && c.timeout > 0 {
		deadline = time.Now().Add(c.timeout)
	}
	_ = c.nc.SetDeadline(deadline)
	fired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = c.nc.SetDeadline(time.Unix(1, 0))
		close(fired)
	})
	defer func() {
		if !stop() {
			<-fired // so that it cannot cut the next command
		}
	}()

	c.nextID++
	id := c.nextID
	c.wbuf = appendMsg(c.wbuf[:0], id, raw, seqs)
	if _, err := c.nc.Write(c.wbuf); err != nil {
		return nil, c.fail(ctx, err)
	}
	if cap(c.wbuf) > 1<<20 {
		c.wbuf = nil
	}
	var hdr [headerSize]byte
	if _, err := io.ReadFull(c.rd, hdr[:]); err != nil {
		return nil, c.fail(ctx, err)
	}
	n := int(int32(binary.LittleEndian.Uint32(hdr[:])))
	if n < headerSize+5 || n > c.maxMsg {
		return nil, c.fail(ctx, fmt.Errorf("an answer of %d bytes, the most is %d", n, c.maxMsg))
	}
	if to := int32(binary.LittleEndian.Uint32(hdr[8:])); to != id {
		return nil, c.fail(ctx, fmt.Errorf("an answer to request %d, not %d", to, id))
	}
	if op := binary.LittleEndian.Uint32(hdr[12:]); op != opMsg {
		return nil, c.fail(ctx, fmt.Errorf("an answer of op code %d, not OP_MSG", op))
	}
	msg := make([]byte, n)
	copy(msg, hdr[:])
	if _, err := io.ReadFull(c.rd, msg[headerSize:]); err != nil {
		return nil, c.fail(ctx, err)
	}
	flags, reply, _, err := parseMsg(msg)
	if err != nil {
		return nil, c.fail(ctx, err)
	}
	if flags&flagMoreToCome != 0 {
		return nil, c.fail(ctx, errors.New("an answer with moreToCome, which was not asked for"))
	}
	return reply, commandError(reply)
}
