package pulsarwire

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// conn is a connection to a broker, or to a proxy for one: its own
// goroutine reads the broker's frames and hands them to the requests,
// producers and consumers they are for; writes are serialized.
type conn struct {
	cl  *Client
	nc  net.Conn
	br  *bufio.Reader
	max int // the most bytes a frame may take
	// maxMessage is the most bytes of a message the broker takes.
	maxMessage int

	wmu sync.Mutex

	mu        sync.Mutex
	pending   map[uint64]chan *command
	producers map[uint64]*Producer
	consumers map[uint64]*Consumer
	err       error
	closed    chan struct{}
	lastRecv  atomic.Int64
}

// dial connects to addr (host:port) over TLS when tlsOn, and sends CONNECT,
// through a proxy to proxyTo when it is set.
func (cl *Client) dial(ctx context.Context, addr string, tlsOn bool, proxyTo string) (*conn, error) {
	ctx, cancel := context.WithTimeout(ctx, cl.cfg.DialTimeout)
	defer cancel()
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pulsar: %w", err)
	}
	if tlsOn {
		tc := cl.cfg.TLS.Clone()
		if tc.ServerName == "" {
			tc.ServerName, _, _ = net.SplitHostPort(addr)
		}
		t := tls.Client(nc, tc)
		if err := t.HandshakeContext(ctx); err != nil {
			nc.Close()
			return nil, fmt.Errorf("pulsar: TLS with %s: %w", addr, err)
		}
		nc = t
	}
	c := &conn{cl: cl, nc: nc, br: bufio.NewReaderSize(nc, 64<<10), max: defaultMaxMessage + frameOverhead, maxMessage: defaultMaxMessage,
		pending: map[uint64]chan *command{}, producers: map[uint64]*Producer{}, consumers: map[uint64]*Consumer{}, closed: make(chan struct{})}
	if dl, ok := ctx.Deadline(); ok {
		nc.SetDeadline(dl)
	}
	if err := c.handshake(proxyTo); err != nil {
		nc.Close()
		return nil, fmt.Errorf("pulsar: connect to %s: %w", addr, err)
	}
	nc.SetDeadline(time.Time{})
	c.lastRecv.Store(time.Now().UnixNano())
	go c.read()
	go c.keepAlive()
	return c, nil
}

// handshake sends CONNECT and reads frames until CONNECTED, answering an
// authentication challenge on the way.
func (c *conn) handshake(proxyTo string) error {
	cfg := &c.cl.cfg
	if _, err := c.nc.Write(frame(connectCmd(cfg.ClientVersion, cfg.Token, proxyTo))); err != nil {
		return err
	}
	for {
		b, _, err := readFrame(c.br, c.max)
		if err != nil {
			return err
		}
		cmd, err := decodeCommand(b)
		if err != nil {
			return err
		}
		switch cmd.typ {
		case typeConnected:
			if cmd.maxMessageSize > 0 {
				c.maxMessage = int(cmd.maxMessageSize)
				c.max = c.maxMessage + frameOverhead
			}
			return nil
		case typeAuthChallenge:
			if _, err := c.nc.Write(frame(authResponseCmd(cfg.ClientVersion, cmd.authMethod, cfg.Token))); err != nil {
				return err
			}
		case typeError:
			return &ServerError{Code: cmd.serverError, Message: cmd.message}
		default:
			return fmt.Errorf("%w: command %d before CONNECTED", ErrFrame, cmd.typ)
		}
	}
}

// write sends a frame, within the request timeout.
func (c *conn) write(f []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.failure(); err != nil {
		return err
	}
	c.nc.SetWriteDeadline(time.Now().Add(c.cl.cfg.RequestTimeout))
	if _, err := c.nc.Write(f); err != nil {
		c.close(fmt.Errorf("pulsar: write: %w", err))
		return c.failure()
	}
	return nil
}

// failure returns why c is closed, nil while it is open.
func (c *conn) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// close closes c with err, failing what waits on it.
func (c *conn) close(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	close(c.closed)
	producers, consumers := c.producers, c.consumers
	c.producers, c.consumers = map[uint64]*Producer{}, map[uint64]*Consumer{}
	c.mu.Unlock()
	c.nc.Close()
	for _, p := range producers {
		p.fail(err)
	}
	for _, cs := range consumers {
		cs.fail(err)
	}
}

// request sends the command made with a new request ID, and waits for the
// answer to it: an ERROR is a *ServerError.
func (c *conn) request(ctx context.Context, what string, build func(req uint64) []byte) (*command, error) {
	req := c.cl.reqID.Add(1)
	ch := make(chan *command, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.pending[req] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, req)
		c.mu.Unlock()
	}()
	if err := c.write(frame(build(req))); err != nil {
		return nil, err
	}
	t := time.NewTimer(c.cl.cfg.RequestTimeout)
	defer t.Stop()
	select {
	case cmd := <-ch:
		if cmd.typ == typeError || cmd.failed {
			return nil, fmt.Errorf("%s: %w", what, &ServerError{Code: cmd.serverError, Message: cmd.message})
		}
		return cmd, nil
	case <-c.closed:
		return nil, c.failure()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, fmt.Errorf("pulsar: %s: no answer within %v", what, c.cl.cfg.RequestTimeout)
	}
}

// read reads the broker's frames until the connection fails.
func (c *conn) read() {
	for {
		b, rest, err := readFrame(c.br, c.max)
		if err == nil {
			c.lastRecv.Store(time.Now().UnixNano())
			var cmd *command
			if cmd, err = decodeCommand(b); err == nil {
				err = c.dispatch(cmd, rest)
			}
		}
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				err = ErrClosed
			}
			c.close(fmt.Errorf("pulsar: the connection: %w", err))
			return
		}
	}
}

// dispatch hands a command to what it is for.
func (c *conn) dispatch(cmd *command, rest []byte) error {
	switch cmd.typ {
	case typePing:
		go c.write(frame(pongCmd))
	case typePong:
	case typeAuthChallenge:
		cfg := &c.cl.cfg
		go c.write(frame(authResponseCmd(cfg.ClientVersion, cmd.authMethod, cfg.Token)))
	case typeSendReceipt, typeSendError, typeCloseProducer:
		c.mu.Lock()
		p := c.producers[cmd.producerID]
		if cmd.typ == typeCloseProducer {
			delete(c.producers, cmd.producerID)
		}
		c.mu.Unlock()
		if p != nil {
			p.receive(cmd)
		}
	case typeMessage, typeCloseConsumer, typeReachedEndOfTopic:
		c.mu.Lock()
		cs := c.consumers[cmd.consumerID]
		if cmd.typ == typeCloseConsumer {
			delete(c.consumers, cmd.consumerID)
		}
		c.mu.Unlock()
		if cs != nil {
			return cs.receive(cmd, rest)
		}
	default:
		if cmd.typ == typeProducerSuccess && !cmd.producerReady {
			// The producer waits for its turn at an exclusive topic: the
			// answer comes later.
			return nil
		}
		c.mu.Lock()
		ch := c.pending[cmd.requestID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- cmd:
			default:
			}
		}
	}
	return nil
}

// keepAlive pings the broker, and closes the connection when nothing came
// from it for two rounds.
func (c *conn) keepAlive() {
	every := c.cl.cfg.KeepAlive
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, c.lastRecv.Load())) > 2*every {
			c.close(fmt.Errorf("pulsar: nothing from the broker for %v", 2*every))
			return
		}
		c.write(frame(pingCmd))
	}
}
