package fastcgi

import (
	"errors"
	"sync"
	"time"

	"github.com/lesismal/fib"
)

// errPoolClosed is what a request waiting for a connection gets when the
// handler's config is gone.
var errPoolClosed = errors.New("fastcgi: the handler was cleaned up")

// errTooMany is what a request gets when as many wait for a connection as
// may.
var errTooMany = errors.New("fastcgi: too many requests wait for a connection")

// pool keeps a handler's connections to its application: dialed through
// fib's engine, one request on each at a time, idle ones kept for the next.
type pool struct {
	engine        *fib.Engine
	network, addr string
	connect, idle time.Duration
	maxConns      int
	maxIdle       int
	maxPending    int

	mu      sync.Mutex
	open    int // dialing, busy or idle
	idleSet []*conn
	waiting []func(*conn, error)
	closed  bool
	all     map[*conn]struct{}
}

// get hands a connection to fn: an idle one at once, a new one once it is
// dialed, or one another request gives back. fn runs once, maybe on this
// goroutine, maybe on fib's; it never runs while get holds the pool's lock.
func (p *pool) get(fn func(*conn, error)) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		fn(nil, errPoolClosed)
		return
	}
	for len(p.idleSet) > 0 {
		c := p.idleSet[len(p.idleSet)-1]
		p.idleSet = p.idleSet[:len(p.idleSet)-1]
		if c.takeIdle() {
			p.mu.Unlock()
			fn(c, nil)
			return
		}
	}
	if p.open >= p.maxConns {
		if len(p.waiting) >= p.maxPending {
			p.mu.Unlock()
			fn(nil, errTooMany)
			return
		}
		p.waiting = append(p.waiting, fn)
		p.mu.Unlock()
		return
	}
	p.open++
	p.mu.Unlock()
	p.dial(fn)
}

// dial opens a connection for fn.
func (p *pool) dial(fn func(*conn, error)) {
	c := &conn{p: p}
	err := p.engine.DialWithHandler(p.network, p.addr, p.connect, c, func(fc *fib.Connection, err error) {
		if err != nil {
			p.lost(c)
			fn(nil, err)
			return
		}
		fn(c, nil)
	})
	if err != nil {
		p.lost(c)
		fn(nil, err)
	}
}

// put gives a connection back after a request: to a request waiting for
// one, to the idle set, or closed.
func (p *pool) put(c *conn) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		c.close()
		return
	}
	if len(p.waiting) > 0 {
		fn := p.waiting[0]
		p.waiting = p.waiting[1:]
		p.mu.Unlock()
		fn(c, nil)
		return
	}
	if len(p.idleSet) >= p.maxIdle {
		p.mu.Unlock()
		c.close()
		return
	}
	p.idleSet = append(p.idleSet, c)
	p.mu.Unlock()
	c.setIdle(p.idle)
}

// lost counts a connection closed or never opened, and lets a request
// waiting for one dial in its place.
func (p *pool) lost(c *conn) {
	p.mu.Lock()
	delete(p.all, c)
	for i, ic := range p.idleSet {
		if ic == c {
			p.idleSet = append(p.idleSet[:i], p.idleSet[i+1:]...)
			break
		}
	}
	if len(p.waiting) > 0 && !p.closed {
		fn := p.waiting[0]
		p.waiting = p.waiting[1:]
		p.mu.Unlock()
		p.dial(fn)
		return
	}
	p.open--
	p.mu.Unlock()
}

// close closes the idle connections, and every other when all is set, and
// fails the requests waiting.
func (p *pool) close(all bool) {
	p.mu.Lock()
	p.closed = true
	idle := p.idleSet
	p.idleSet = nil
	waiting := p.waiting
	p.waiting = nil
	var busy []*conn
	if all {
		for c := range p.all {
			busy = append(busy, c)
		}
	}
	p.mu.Unlock()
	for _, c := range idle {
		c.close()
	}
	for _, c := range busy {
		c.close()
	}
	for _, fn := range waiting {
		fn(nil, errPoolClosed)
	}
}

// conn is a connection to the application, and the request on it.
type conn struct {
	p  *pool
	fc *fib.Connection

	mu sync.Mutex
	// x is the request the connection carries, or nil while idle.
	x      *exchange
	idle   *time.Timer
	closed bool
	r      reader
}

// OnOpen keeps the connection.
func (c *conn) OnOpen(fc *fib.Connection) {
	fc.SetWriteBackpressure(false)
	c.mu.Lock()
	c.fc = fc
	c.mu.Unlock()
	c.p.mu.Lock()
	if c.p.all == nil {
		c.p.all = map[*conn]struct{}{}
	}
	c.p.all[c] = struct{}{}
	c.p.mu.Unlock()
}

// OnData reads the records the application sends, for the request on the
// connection; anything while idle breaks the protocol.
func (c *conn) OnData(_ *fib.Connection, data []byte) {
	c.mu.Lock()
	x := c.x
	c.mu.Unlock()
	if x == nil {
		c.close()
		return
	}
	if err := c.r.feed(data, x.record); err != nil {
		x.fail(err)
		c.close()
	}
}

func (c *conn) OnPriorityData(*fib.Connection, []byte) {}

// OnClose fails the request on the connection, if any.
func (c *conn) OnClose(_ *fib.Connection, err error) {
	c.mu.Lock()
	x := c.x
	c.x = nil
	c.closed = true
	if c.idle != nil {
		c.idle.Stop()
	}
	c.mu.Unlock()
	c.p.lost(c)
	if x != nil {
		if err == nil {
			err = errors.New("fastcgi: the application closed the connection")
		}
		x.fail(err)
	}
}

// start gives the connection to x.
func (c *conn) start(x *exchange) {
	c.mu.Lock()
	c.x = x
	c.mu.Unlock()
}

// done takes the request off the connection, which goes back to the pool
// when it can carry another.
func (c *conn) done(reuse bool) {
	c.mu.Lock()
	c.x = nil
	closed := c.closed
	c.mu.Unlock()
	switch {
	case closed:
	case reuse && len(c.r.buf) == 0:
		c.p.put(c)
	default:
		c.close()
	}
}

// setIdle closes the connection if it stays idle for d.
func (c *conn) setIdle(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idle == nil {
		c.idle = time.AfterFunc(d, c.idleTimeout)
	} else {
		c.idle.Reset(d)
	}
}

func (c *conn) idleTimeout() {
	c.p.mu.Lock()
	for i, ic := range c.p.idleSet {
		if ic == c {
			c.p.idleSet = append(c.p.idleSet[:i], c.p.idleSet[i+1:]...)
			c.p.mu.Unlock()
			c.close()
			return
		}
	}
	c.p.mu.Unlock()
}

// takeIdle reports whether an idle connection is still open, and stops its
// idle timer. p.mu is held.
func (c *conn) takeIdle() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idle != nil {
		c.idle.Stop()
	}
	return !c.closed
}

// fib returns the connection's fib connection.
func (c *conn) fib() *fib.Connection {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fc
}

func (c *conn) send(b []byte) error {
	c.mu.Lock()
	fc := c.fc
	c.mu.Unlock()
	if fc == nil {
		return errors.New("fastcgi: not connected")
	}
	return fc.Send(b)
}

func (c *conn) close() {
	c.mu.Lock()
	fc := c.fc
	c.mu.Unlock()
	if fc != nil {
		_ = fc.Close()
	}
}
