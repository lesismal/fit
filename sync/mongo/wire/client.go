package mongowire

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
)

// Options are a Client's settings.
type Options struct {
	// Hosts are the servers to find the primary from, "host:port" (27017
	// when the port is left out); the members the servers name are tried
	// too.
	Hosts []string
	// ReplicaSet, when set, is the replica set's name; a server of
	// another set is not taken for the primary.
	ReplicaSet string
	// Username and Password authenticate with SCRAM-SHA-256 against
	// AuthSource ("admin" by default); no username, no authentication.
	Username, Password, AuthSource string
	// TLS, when set, is the TLS for every connection; its ServerName is
	// each host's name when it has none.
	TLS *tls.Config
	// ConnectTimeout bounds a connection's dial, TLS, hello and
	// authentication: 10s by default.
	ConnectTimeout time.Duration
	// Timeout bounds a command whose context has no deadline: 60s by
	// default; a negative Timeout leaves it unbounded.
	Timeout time.Duration
	// ServerSelectionTimeout bounds finding a primary: 30s by default.
	ServerSelectionTimeout time.Duration
	// MaxConns bounds the connections open to the primary, the open
	// cursors' among them: 4 by default.
	MaxConns int
	// MaxMessageBytes bounds a message, sent or read, below the server's
	// own maxMessageSizeBytes: 48,000,000 by default.
	MaxMessageBytes int
	// AppName is sent in hello and shown in the server's logs.
	AppName string
}

func (o Options) withDefaults() Options {
	if o.AuthSource == "" {
		o.AuthSource = "admin"
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 10 * time.Second
	}
	if o.Timeout == 0 {
		o.Timeout = 60 * time.Second
	} else if o.Timeout < 0 {
		o.Timeout = 0
	}
	if o.ServerSelectionTimeout <= 0 {
		o.ServerSelectionTimeout = 30 * time.Second
	}
	if o.MaxConns <= 0 {
		o.MaxConns = 4
	}
	if o.MaxMessageBytes <= 0 || o.MaxMessageBytes > 48_000_000 {
		o.MaxMessageBytes = 48_000_000
	}
	return o
}

// withPort adds MongoDB's port to a host without one.
func withPort(h string) string {
	if _, _, err := net.SplitHostPort(h); err == nil {
		return h
	}
	if strings.Count(h, ":") > 1 && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	return h + ":27017"
}

// ErrClosed is returned by a Client's methods once it is closed.
var ErrClosed = errors.New("mongowire: the client is closed")

// Client sends commands to a replica set's primary (or a standalone
// server's, or a mongos), which it finds and finds again when it changes,
// over a bounded pool of connections.
type Client struct {
	o     Options
	sem   chan struct{}
	salts saltCache

	mu      sync.Mutex
	closed  bool
	primary string // "" until found
	gen     uint64 // advanced when the primary is lost
	idle    []*Conn
	known   []string // the members servers named, after the seeds
}

// New returns a Client of o; it connects when first used.
func New(o Options) (*Client, error) {
	if len(o.Hosts) == 0 {
		return nil, errors.New("mongowire: no hosts")
	}
	o = o.withDefaults()
	hosts := make([]string, 0, len(o.Hosts))
	for _, h := range o.Hosts {
		h = withPort(strings.TrimSpace(h))
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	o.Hosts = hosts
	return &Client{o: o, sem: make(chan struct{}, o.MaxConns)}, nil
}

// Options returns the client's options, with their defaults.
func (c *Client) Options() Options { return c.o }

// Close closes the idle connections; those in use close when they are
// put back.
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	idle := c.idle
	c.idle = nil
	c.mu.Unlock()
	for _, cn := range idle {
		cn.Close()
	}
	return nil
}

// Primary returns the primary's address, finding it when it is not known.
func (c *Client) Primary(ctx context.Context) (string, error) {
	cn, err := c.acquire(ctx)
	if err != nil {
		return "", err
	}
	addr := cn.addr
	c.release(cn, nil)
	return addr, nil
}

// acquire takes a connection to the primary, waiting for one of MaxConns.
func (c *Client) acquire(ctx context.Context) (*Conn, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	cn, err := c.conn(ctx)
	if err != nil {
		<-c.sem
		return nil, err
	}
	return cn, nil
}

func (c *Client) conn(ctx context.Context) (*Conn, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	for len(c.idle) > 0 {
		cn := c.idle[len(c.idle)-1]
		c.idle = c.idle[:len(c.idle)-1]
		if cn.gen == c.gen && cn.Err() == nil {
			c.mu.Unlock()
			return cn, nil
		}
		cn.Close()
	}
	addr, gen := c.primary, c.gen
	c.mu.Unlock()
	if addr != "" {
		cn, err := dialHello(ctx, addr, &c.o)
		if err == nil && c.isPrimary(cn.hello) {
			if err = cn.authenticate(ctx, &c.o, &c.salts); err == nil {
				cn.gen = gen
				return cn, nil
			}
			cn.Close()
			if !IsNetError(err) {
				return nil, err
			}
		} else if err == nil {
			cn.Close()
		}
		c.lost(gen)
	}
	return c.findPrimary(ctx)
}

// isPrimary reports whether h is a primary this client takes.
func (c *Client) isPrimary(h Hello) bool {
	return h.IsWritablePrimary && (c.o.ReplicaSet == "" || h.SetName == c.o.ReplicaSet)
}

// lost forgets the primary of generation gen, and the connections to it.
func (c *Client) lost(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	c.gen++
	c.primary = ""
	for _, cn := range c.idle {
		cn.Close()
	}
	c.idle = nil
}

// findPrimary asks the seeds and the members they name, in turn, until one
// is the primary, for at most ServerSelectionTimeout.
func (c *Client) findPrimary(ctx context.Context) (*Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, c.o.ServerSelectionTimeout)
	defer cancel()
	var errs []string
	note := func(addr string, err error) {
		if len(errs) < 8 {
			errs = append(errs, addr+": "+err.Error())
		}
	}
	for wait := 100 * time.Millisecond; ; wait = min(2*wait, time.Second) {
		c.mu.Lock()
		cands := append(slices.Clone(c.o.Hosts), c.known...)
		gen := c.gen
		c.mu.Unlock()
		tried := map[string]bool{}
		for len(cands) > 0 {
			addr := cands[0]
			cands = cands[1:]
			if tried[addr] {
				continue
			}
			tried[addr] = true
			cn, err := dialHello(ctx, addr, &c.o)
			if err != nil {
				note(addr, err)
				if ctx.Err() != nil {
					return nil, fmt.Errorf("mongowire: no primary found (%v): %s", ctx.Err(), strings.Join(errs, "; "))
				}
				continue
			}
			h := cn.hello
			c.learn(h)
			if c.isPrimary(h) {
				if err := cn.authenticate(ctx, &c.o, &c.salts); err != nil {
					cn.Close()
					return nil, err
				}
				c.mu.Lock()
				if c.gen == gen {
					c.primary = addr
				}
				cn.gen = c.gen
				c.mu.Unlock()
				return cn, nil
			}
			cn.Close()
			switch {
			case c.o.ReplicaSet != "" && h.SetName != c.o.ReplicaSet:
				note(addr, fmt.Errorf("of replica set %q, not %q", h.SetName, c.o.ReplicaSet))
			default:
				note(addr, errors.New("not the primary"))
			}
			if h.Primary != "" && !tried[h.Primary] {
				cands = append([]string{h.Primary}, cands...)
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("mongowire: no primary found (%v): %s", ctx.Err(), strings.Join(errs, "; "))
		case <-time.After(wait):
		}
		errs = errs[:0]
	}
}

// learn keeps the members a server of the set names.
func (c *Client) learn(h Hello) {
	if h.SetName == "" || c.o.ReplicaSet != "" && h.SetName != c.o.ReplicaSet {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range append(slices.Clone(h.Hosts), h.Primary) {
		if m != "" && !slices.Contains(c.o.Hosts, m) && !slices.Contains(c.known, m) && len(c.known) < 64 {
			c.known = append(c.known, m)
		}
	}
}

// release puts cn back after a command whose error was err.
func (c *Client) release(cn *Conn, err error) {
	defer func() { <-c.sem }()
	if err != nil && (IsNetError(err) || IsNotPrimary(err)) {
		cn.Close()
		c.lost(cn.gen)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || cn.gen != c.gen || cn.Err() != nil || len(c.idle) >= c.o.MaxConns {
		cn.Close()
		return
	}
	c.idle = append(c.idle, cn)
}

// Run sends cmd to database db on the primary, with the document
// sequences seqs, and returns the answer; an answer with ok: 0 is a
// *CommandError. When the primary is gone (a connection's error, or an
// answer that it is not the primary) the command is sent once more to the
// primary found then: a write may so be applied twice, which sync's
// writes, upserts and deletes by key, allow.
func (c *Client) Run(ctx context.Context, db string, cmd D, seqs ...Sequence) (Raw, error) {
	for attempt := 0; ; attempt++ {
		cn, err := c.acquire(ctx)
		if err != nil {
			return nil, err
		}
		reply, err := cn.Run(ctx, db, cmd, seqs...)
		c.release(cn, err)
		if err == nil || attempt == 1 || ctx.Err() != nil || !(IsNetError(err) || IsNotPrimary(err)) {
			return reply, err
		}
	}
}

// Cursor reads the batches of a command's cursor, as a find's or an
// aggregate's, with getMore. It holds its connection, one of the client's
// MaxConns, until it is exhausted or closed.
type Cursor struct {
	c         *Client
	cn        *Conn
	db, coll  string
	id        int64
	batch     []Raw
	pos       int
	pbrt      Raw
	batchSize int32
	maxAwait  time.Duration
}

// CursorOptions are a cursor's getMore settings.
type CursorOptions struct {
	// BatchSize is the documents a getMore asks for; 0 is the server's
	// choice.
	BatchSize int32
	// MaxAwait is how long a getMore of a tailable await cursor (a change
	// stream's) waits for documents; 0 is the server's choice.
	MaxAwait time.Duration
}

// Cursor runs cmd, a command answered with a cursor, on the primary and
// returns the cursor with its first batch.
func (c *Client) Cursor(ctx context.Context, db string, cmd D, o CursorOptions) (*Cursor, error) {
	for attempt := 0; ; attempt++ {
		cn, err := c.acquire(ctx)
		if err != nil {
			return nil, err
		}
		reply, err := cn.Run(ctx, db, cmd)
		if err != nil {
			c.release(cn, err)
			if attempt == 1 || ctx.Err() != nil || !(IsNetError(err) || IsNotPrimary(err)) {
				return nil, err
			}
			continue
		}
		cur := &Cursor{c: c, cn: cn, db: db, batchSize: o.BatchSize, maxAwait: o.MaxAwait}
		if err := cur.read(reply, "firstBatch"); err != nil {
			c.release(cn, nil)
			cur.cn = nil
			return nil, err
		}
		return cur, nil
	}
}

// read takes a cursor's answer.
func (cur *Cursor) read(reply Raw, field string) error {
	cv, ok := reply.Lookup("cursor")
	doc := cv.Document()
	if !ok || doc == nil {
		return fmt.Errorf("mongowire: an answer without a cursor: %s", reply)
	}
	idv, _ := doc.Lookup("id")
	id, ok := idv.Int()
	if !ok {
		return errors.New("mongowire: a cursor without an id")
	}
	if ns, ok := doc.Lookup("ns"); ok {
		s, _ := ns.StringValue()
		if i := strings.IndexByte(s, '.'); i >= 0 {
			cur.coll = s[i+1:]
		}
	}
	cur.id = id
	b, _ := doc.Lookup(field)
	cur.batch = cur.batch[:0]
	for _, d := range b.Array() {
		if d.Type != TypeDocument {
			return errors.New("mongowire: a cursor's batch holds a non-document")
		}
		cur.batch = append(cur.batch, Raw(d.Data))
	}
	cur.pos = 0
	if v, ok := doc.Lookup("postBatchResumeToken"); ok {
		cur.pbrt = v.Document()
	}
	if id == 0 {
		cur.done(nil)
	}
	return nil
}

// done gives the connection back.
func (cur *Cursor) done(err error) {
	if cur.cn != nil {
		cur.c.release(cur.cn, err)
		cur.cn = nil
	}
}

// ID returns the cursor's ID: 0 once the server has no more.
func (cur *Cursor) ID() int64 { return cur.id }

// Batch returns the current batch's documents.
func (cur *Cursor) Batch() []Raw { return cur.batch }

// PostBatchResumeToken returns a change stream's resume token after the
// current batch; nil for another cursor.
func (cur *Cursor) PostBatchResumeToken() Raw { return cur.pbrt }

// NextBatch asks for the next batch with getMore and reports whether there
// was one to ask for: false once the cursor is exhausted. A batch may be
// empty, as a change stream's when nothing changed while it waited.
func (cur *Cursor) NextBatch(ctx context.Context) (bool, error) {
	if cur.id == 0 {
		cur.batch, cur.pos = cur.batch[:0], 0
		return false, nil
	}
	cmd := D{{"getMore", cur.id}, {"collection", cur.coll}}
	if cur.batchSize > 0 {
		cmd = append(cmd, E{"batchSize", cur.batchSize})
	}
	if cur.maxAwait > 0 {
		cmd = append(cmd, E{"maxTimeMS", cur.maxAwait.Milliseconds()})
	}
	reply, err := cur.cn.Run(ctx, cur.db, cmd)
	if err != nil {
		cur.id = 0
		cur.done(err)
		return false, err
	}
	if err := cur.read(reply, "nextBatch"); err != nil {
		cur.id = 0
		cur.done(nil)
		return false, err
	}
	return true, nil
}

// Next returns the next document, asking for batches as needed; nil, nil
// once the cursor is exhausted.
func (cur *Cursor) Next(ctx context.Context) (Raw, error) {
	for cur.pos >= len(cur.batch) {
		more, err := cur.NextBatch(ctx)
		if err != nil || !more {
			return nil, err
		}
	}
	d := cur.batch[cur.pos]
	cur.pos++
	return d, nil
}

// Close kills the cursor on the server when it is not exhausted, and gives
// its connection back.
func (cur *Cursor) Close(ctx context.Context) error {
	if cur.cn == nil {
		return nil
	}
	var err error
	if cur.id != 0 {
		_, err = cur.cn.Run(ctx, cur.db, D{{"killCursors", cur.coll}, {"cursors", A{cur.id}}})
		cur.id = 0
	}
	cur.done(err)
	return err
}
