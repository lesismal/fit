// Package amqpwire is a client of AMQP 0-9-1, RabbitMQ's protocol: a
// connection dialled over TCP or TLS to the first of a cluster's nodes that
// answers, authenticated with PLAIN or EXTERNAL (a client certificate),
// with heartbeats, the broker's flow control (connection.blocked and
// channel.flow) and one channel, on which it consumes with a prefetch and
// acknowledges, and publishes with publisher confirms. Frames are bounded
// by the frame_max agreed and messages by Config.MaxBody. A goroutine of
// the connection reads its frames and another sends its heartbeats; the
// methods may be called from several goroutines. It is sync's own, written
// in fit as decision 036 has the protocols be.
package amqpwire

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The defaults of a Config.
const (
	DefaultFrameMax  = 128 << 10
	DefaultMaxBody   = 16 << 20
	DefaultTimeout   = 30 * time.Second
	DefaultHeartbeat = 30 * time.Second
	// minFrameMax is the least frame_max the protocol allows.
	minFrameMax = 4096
	// channel is the one channel a connection opens.
	channel = 1
)

// Config is how to connect.
type Config struct {
	// Addrs are the nodes, host:port each, tried in order.
	Addrs []string
	// Vhost is the virtual host opened; / when empty.
	Vhost string
	// Username and Password authenticate with PLAIN; with External, the
	// TLS client certificate does, with EXTERNAL.
	Username, Password string
	External           bool
	// TLS, when not nil, connects over TLS; its ServerName is each node's
	// host when empty.
	TLS *tls.Config
	// Timeout bounds connecting, each method's answer, each write, the
	// wait of a publish while the broker blocks it, and closing.
	// DefaultTimeout when 0.
	Timeout time.Duration
	// Heartbeat is the interval asked for; the server's when 0, or the
	// lower of the two; negative turns heartbeats off.
	Heartbeat time.Duration
	// FrameMax is the largest frame asked for, DefaultFrameMax when 0; the
	// server's when lower.
	FrameMax uint32
	// MaxBody bounds a message's body read or published, DefaultMaxBody
	// when 0.
	MaxBody int
	// Deliveries is how many deliveries are held for Deliveries' reader
	// before the connection stops reading: at least the prefetch. 256
	// when 0.
	Deliveries int
	// Name is the connection's name, shown in the broker's management.
	Name string
	// Dial, when not nil, dials in place of net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Error is a broker's closing of the channel or the connection: a reply
// code (404 not found, 403 access refused, 406 precondition failed, 320
// connection forced...) and its text.
type Error struct {
	Code       uint16
	Text       string
	Connection bool
	// Method is the class and method the broker refused, as
	// class<<16 | method; 0 when none.
	Method uint32
}

func (e *Error) Error() string {
	what := "channel"
	if e.Connection {
		what = "connection"
	}
	return fmt.Sprintf("amqp: the broker closed the %s: %d %s", what, e.Code, e.Text)
}

// The reply codes tests and callers look for.
const (
	CodeNotFound           = 404
	CodeAccessRefused      = 403
	CodePreconditionFailed = 406
)

// ErrClosed is the error of a connection closed by Close.
var ErrClosed = errors.New("amqp: the connection is closed")

// ErrBlocked is a publish's error after Timeout of the broker blocking
// publishing (connection.blocked, an alarm of its memory or disk) or of
// channel.flow stopping it.
var ErrBlocked = errors.New("amqp: the broker blocks publishing")

// ErrTooLarge is a message's body larger than MaxBody.
var ErrTooLarge = errors.New("amqp: a message larger than the most allowed")

// Delivery is a message a consumer was given.
type Delivery struct {
	ConsumerTag string
	DeliveryTag uint64
	Redelivered bool
	Exchange    string
	RoutingKey  string
	Properties  Properties
	Body        []byte
}

// Return is a mandatory message the broker could not route.
type Return struct {
	Code       uint16
	Text       string
	Exchange   string
	RoutingKey string
	Properties Properties
	Body       []byte
}

// QueueInfo is what queue.declare answers.
type QueueInfo struct {
	Name      string
	Messages  uint32
	Consumers uint32
}

// reply is a method answering a call.
type reply struct {
	id uint32
	d  *dec
}

// content is a deliver or return whose header and body are being read.
type content struct {
	deliver *Delivery
	ret     *Return
	size    uint64
	props   Properties
	body    []byte
	header  bool
}

// Conn is a connection with its channel open.
type Conn struct {
	cfg       Config
	nc        net.Conn
	br        *bufio.Reader
	frameMax  uint32
	heartbeat time.Duration
	lastWrite atomic.Int64
	// timeout is cfg.Timeout, which tests change.
	timeout atomic.Int64

	wmu   sync.Mutex
	rpcMu sync.Mutex
	// pmu keeps publishes in the order of their sequence numbers.
	pmu sync.Mutex
	// replies takes the answer of the call under way.
	replies    chan reply
	deliveries chan *Delivery
	closeOK    chan struct{}
	done       chan struct{}

	mu      sync.Mutex
	err     error
	blocked string // the reason, while connection.blocked holds
	stopped bool   // channel.flow stopped publishing
	unblock chan struct{}
	// Publisher confirms: the sequence numbers not confirmed yet, those
	// nacked, and a channel closed at each change.
	confirming bool
	seq        uint64
	pending    map[uint64]bool
	nacked     map[uint64]bool
	changed    chan struct{}
	returns    []Return
}

// Dial connects to the first of cfg.Addrs that answers and opens a
// channel.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	if len(cfg.Addrs) == 0 {
		return nil, errors.New("amqp: no addresses")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.FrameMax == 0 {
		cfg.FrameMax = DefaultFrameMax
	}
	cfg.FrameMax = max(cfg.FrameMax, minFrameMax)
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = DefaultMaxBody
	}
	if cfg.Deliveries <= 0 {
		cfg.Deliveries = 256
	}
	if cfg.Vhost == "" {
		cfg.Vhost = "/"
	}
	var errs []error
	for _, addr := range cfg.Addrs {
		c, err := dial(ctx, cfg, addr)
		if err == nil {
			return c, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", addr, err))
		var ae *Error
		if errors.As(err, &ae) || ctx.Err() != nil {
			// The broker refused, which another node would too.
			break
		}
	}
	return nil, errors.Join(errs...)
}

func dial(ctx context.Context, cfg Config, addr string) (*Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	var nc net.Conn
	var err error
	if cfg.Dial != nil {
		nc, err = cfg.Dial(dctx, "tcp", addr)
	} else {
		nc, err = (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(dctx, "tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	deadline, _ := dctx.Deadline()
	nc.SetDeadline(deadline)
	if cfg.TLS != nil {
		tc := cfg.TLS.Clone()
		if tc.ServerName == "" {
			tc.ServerName, _, _ = net.SplitHostPort(addr)
		}
		t := tls.Client(nc, tc)
		if err := t.HandshakeContext(dctx); err != nil {
			nc.Close()
			return nil, err
		}
		nc = t
	}
	c := &Conn{cfg: cfg, nc: nc, br: bufio.NewReaderSize(nc, 64<<10), frameMax: cfg.FrameMax,
		replies: make(chan reply, 1), deliveries: make(chan *Delivery, cfg.Deliveries),
		closeOK: make(chan struct{}), done: make(chan struct{}), unblock: make(chan struct{}),
		pending: map[uint64]bool{}, nacked: map[uint64]bool{}, changed: make(chan struct{})}
	c.timeout.Store(int64(cfg.Timeout))
	stop := context.AfterFunc(dctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	err = c.open()
	if !stop() && err == nil {
		err = dctx.Err()
	}
	if err != nil {
		nc.Close()
		var ne net.Error
		if cause := dctx.Err(); cause != nil && !isAMQP(err) {
			err = fmt.Errorf("%w: %v", cause, err)
		} else if errors.As(err, &ne) && ne.Timeout() {
			// The deadline passed before dctx's timer told it.
			err = fmt.Errorf("%w: %v", context.DeadlineExceeded, err)
		}
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	go c.readLoop()
	if c.heartbeat > 0 {
		go c.beat()
	}
	return c, nil
}

func isAMQP(err error) bool {
	var ae *Error
	return errors.As(err, &ae)
}

// open negotiates the connection and opens the channel, before the
// connection's goroutines start.
func (c *Conn) open() error {
	if _, err := c.nc.Write([]byte("AMQP\x00\x00\x09\x01")); err != nil {
		return err
	}
	d, err := c.expect(0, connectionStart)
	if err != nil {
		return err
	}
	d.u8() // version-major
	d.u8() // version-minor
	d.table(0)
	mechs := strings.Fields(d.long())
	if d.err != nil {
		return d.err
	}
	mech, resp := "PLAIN", "\x00"+c.cfg.Username+"\x00"+c.cfg.Password
	if c.cfg.External {
		mech, resp = "EXTERNAL", ""
	}
	found := false
	for _, m := range mechs {
		found = found || m == mech
	}
	if !found {
		return fmt.Errorf("amqp: the broker does not take %s, only %s", mech, strings.Join(mechs, " "))
	}
	props := Table{"product": "fit", "platform": "Go", "capabilities": Table{
		"publisher_confirms": true, "consumer_cancel_notify": true, "connection.blocked": true,
		"basic.nack": true, "authentication_failure_close": true}}
	if c.cfg.Name != "" {
		props["connection_name"] = c.cfg.Name
	}
	if err := c.writeMethod(0, method(connectionStartOK).table(props).short(mech).long(resp).short("en_US")); err != nil {
		return err
	}
	d, err = c.expect(0, connectionTune)
	if err != nil {
		return err
	}
	chMax, frameMax, hb := d.u16(), d.u32(), d.u16()
	if frameMax != 0 && frameMax < c.frameMax {
		c.frameMax = max(frameMax, minFrameMax)
	}
	server := time.Duration(hb) * time.Second
	switch want := c.cfg.Heartbeat; {
	case want < 0:
	case want == 0 || server > 0 && server < want:
		c.heartbeat = server
	default:
		c.heartbeat = want
	}
	secs := uint16(min(c.heartbeat/time.Second, 65535))
	if err := c.writeMethod(0, method(connectionTuneOK).u16(chMax).u32(c.frameMax).u16(secs)); err != nil {
		return err
	}
	if err := c.writeMethod(0, method(connectionOpen).short(c.cfg.Vhost).short("").bits(false)); err != nil {
		return err
	}
	if _, err := c.expect(0, connectionOpenOK); err != nil {
		return err
	}
	if err := c.writeMethod(channel, method(channelOpen).short("")); err != nil {
		return err
	}
	_, err = c.expect(channel, channelOpenOK)
	return err
}

// expect reads the method id on ch, while the connection opens; a close
// is the broker's refusal.
func (c *Conn) expect(ch uint16, id uint32) (*dec, error) {
	for {
		f, err := readFrame(c.br, c.frameMax)
		if err != nil {
			return nil, err
		}
		if f.typ == frameHeartbeat {
			continue
		}
		d := &dec{b: f.payload}
		got := d.u32()
		switch {
		case f.typ != frameMethod || d.err != nil:
			return nil, errBadFrame
		case got == connectionClose || got == channelClose:
			return nil, closeError(d, got == connectionClose)
		case got == connectionSecure:
			return nil, errors.New("amqp: the broker asks a challenge of the mechanism, which neither PLAIN nor EXTERNAL answers")
		case got != id || f.channel != ch:
			return nil, fmt.Errorf("amqp: method %d.%d where %d.%d was expected", got>>16, got&0xffff, id>>16, id&0xffff)
		}
		return d, nil
	}
}

func closeError(d *dec, conn bool) error {
	e := &Error{Connection: conn}
	e.Code = d.u16()
	e.Text = d.short()
	e.Method = d.u32()
	return e
}

// limit is Timeout.
func (c *Conn) limit() time.Duration { return time.Duration(c.timeout.Load()) }

// writeMethod writes a method frame.
func (c *Conn) writeMethod(ch uint16, m enc) error {
	return c.write(appendFrame(nil, frameMethod, ch, m))
}

// write writes frames whole, within Timeout.
func (c *Conn) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.SetWriteDeadline(time.Now().Add(c.limit()))
	_, err := c.nc.Write(b)
	c.lastWrite.Store(time.Now().UnixNano())
	if err != nil {
		c.fail(err)
	}
	return err
}

// fail closes the connection with err, the first one kept.
func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	close(c.done)
	c.mu.Unlock()
	c.nc.Close()
}

// Done is closed once the connection has failed or is closed; Err says
// why.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is why the connection ended; nil while it works.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// FrameMax and Heartbeat are what was agreed.
func (c *Conn) FrameMax() uint32 { return c.frameMax }

// Heartbeat is the heartbeat interval agreed; 0 when off.
func (c *Conn) Heartbeat() time.Duration { return c.heartbeat }

// beat sends a heartbeat when nothing was written for half the interval.
func (c *Conn) beat() {
	t := time.NewTicker(c.heartbeat / 2)
	defer t.Stop()
	hb := appendFrame(nil, frameHeartbeat, 0, nil)
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if time.Since(time.Unix(0, c.lastWrite.Load())) >= c.heartbeat/2 {
				c.write(hb)
			}
		}
	}
}

// readLoop reads frames until the connection fails, missing two
// heartbeats failing it.
func (c *Conn) readLoop() {
	defer close(c.deliveries)
	var cur *content
	for {
		if c.heartbeat > 0 {
			c.nc.SetReadDeadline(time.Now().Add(2*c.heartbeat + time.Second))
		}
		f, err := readFrame(c.br, c.frameMax)
		if err == nil {
			cur, err = c.handle(f, cur)
		}
		if err != nil {
			c.mu.Lock()
			closed := c.err != nil
			c.mu.Unlock()
			if !closed {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					err = fmt.Errorf("amqp: no heartbeat from the broker in %v: %w", 2*c.heartbeat, err)
				}
			}
			c.fail(err)
			return
		}
	}
}

// handle takes a frame, cur being a message whose content is being read.
func (c *Conn) handle(f frame, cur *content) (*content, error) {
	switch f.typ {
	case frameHeartbeat:
		return cur, nil
	case frameHeader, frameBody:
		if cur == nil || f.channel != channel || (f.typ == frameBody) != cur.header {
			return nil, errBadFrame
		}
		if f.typ == frameHeader {
			size, props, err := readHeader(f.payload)
			if err != nil {
				return nil, err
			}
			if size > uint64(c.cfg.MaxBody) {
				return nil, fmt.Errorf("%w: %d bytes, more than %d", ErrTooLarge, size, c.cfg.MaxBody)
			}
			cur.size, cur.props, cur.header = size, props, true
			cur.body = make([]byte, 0, size)
		} else {
			if uint64(len(cur.body)+len(f.payload)) > cur.size {
				return nil, errBadFrame
			}
			cur.body = append(cur.body, f.payload...)
		}
		if !cur.header || uint64(len(cur.body)) < cur.size {
			return cur, nil
		}
		c.complete(cur)
		return nil, nil
	case frameMethod:
	default:
		return nil, errBadFrame
	}
	if cur != nil {
		return nil, errBadFrame
	}
	d := &dec{b: f.payload}
	id := d.u32()
	if d.err != nil {
		return nil, errBadFrame
	}
	if f.channel == 0 {
		return nil, c.connMethod(id, d)
	}
	if f.channel != channel {
		return nil, errBadFrame
	}
	switch id {
	case basicDeliver:
		dl := &Delivery{ConsumerTag: d.short(), DeliveryTag: d.u64()}
		dl.Redelivered = d.u8()&1 != 0
		dl.Exchange, dl.RoutingKey = d.short(), d.short()
		return &content{deliver: dl}, d.err
	case basicReturn:
		r := &Return{Code: d.u16(), Text: d.short(), Exchange: d.short(), RoutingKey: d.short()}
		return &content{ret: r}, d.err
	case basicAck, basicNack:
		tag := d.u64()
		multiple := d.u8()&1 != 0
		c.confirm(tag, multiple, id == basicNack)
		return nil, d.err
	case channelFlow:
		active := d.u8()&1 != 0
		c.mu.Lock()
		c.stopped = !active
		c.release()
		c.mu.Unlock()
		return nil, c.writeMethod(channel, method(channelFlowOK).bits(active))
	case channelClose:
		err := closeError(d, false)
		c.writeMethod(channel, method(channelCloseOK))
		return nil, err
	case basicCancel:
		return nil, fmt.Errorf("amqp: the broker cancelled the consumer %s: its queue was deleted, or its node lost", d.short())
	}
	select {
	case c.replies <- reply{id, d}:
	default:
		// No call waits for it: a late answer of one that gave up.
	}
	return nil, nil
}

// connMethod takes a method of channel 0.
func (c *Conn) connMethod(id uint32, d *dec) error {
	switch id {
	case connectionClose:
		err := closeError(d, true)
		c.writeMethod(0, method(connectionCloseOK))
		return err
	case connectionCloseOK:
		close(c.closeOK)
		return ErrClosed
	case connectionBlocked:
		reason := d.short()
		c.mu.Lock()
		c.blocked = reason + " "
		c.mu.Unlock()
	case connectionUnblocked:
		c.mu.Lock()
		c.blocked = ""
		c.release()
		c.mu.Unlock()
	}
	return nil
}

// release wakes publishers waiting for the broker to let them on; c.mu is
// held.
func (c *Conn) release() {
	if c.blocked == "" && !c.stopped {
		close(c.unblock)
		c.unblock = make(chan struct{})
	}
}

// complete hands on a message read whole.
func (c *Conn) complete(m *content) {
	if m.ret != nil {
		m.ret.Properties, m.ret.Body = m.props, m.body
		c.mu.Lock()
		if len(c.returns) < 65536 {
			c.returns = append(c.returns, *m.ret)
		}
		c.mu.Unlock()
		return
	}
	m.deliver.Properties, m.deliver.Body = m.props, m.body
	select {
	case c.deliveries <- m.deliver:
	case <-c.done:
	}
}

// confirm records the broker's ack or nack of published messages.
func (c *Conn) confirm(tag uint64, multiple, nack bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for s := range c.pending {
		if s == tag || multiple && s < tag {
			delete(c.pending, s)
			if nack {
				c.nacked[s] = true
			}
		}
	}
	close(c.changed)
	c.changed = make(chan struct{})
}

// call sends a method on the channel and waits for its answer, want.
func (c *Conn) call(ctx context.Context, m enc, want uint32) (*dec, error) {
	c.rpcMu.Lock()
	defer c.rpcMu.Unlock()
	select {
	case <-c.replies:
	default:
	}
	if err := c.writeMethod(channel, m); err != nil {
		return nil, err
	}
	t := time.NewTimer(c.limit())
	defer t.Stop()
	select {
	case r := <-c.replies:
		if r.id != want {
			err := fmt.Errorf("amqp: method %d.%d where %d.%d was expected", r.id>>16, r.id&0xffff, want>>16, want&0xffff)
			c.fail(err)
			return nil, err
		}
		return r.d, nil
	case <-c.done:
		return nil, c.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, fmt.Errorf("amqp: no answer %d.%d in %v", want>>16, want&0xffff, c.limit())
	}
}

// Qos sets how many deliveries each consumer of the channel may have
// unacknowledged.
func (c *Conn) Qos(ctx context.Context, prefetch uint16) error {
	_, err := c.call(ctx, method(basicQos).u32(0).u16(prefetch).bits(false), basicQosOK)
	return err
}

// Consume starts a consumer of queue, its deliveries acknowledged by hand,
// with args (x-stream-offset for a stream queue); the tag is the broker's
// when empty. It returns the consumer's tag.
func (c *Conn) Consume(ctx context.Context, queue, tag string, args Table) (string, error) {
	d, err := c.call(ctx, method(basicConsume).u16(0).short(queue).short(tag).bits(false, false, false, false).table(args), basicConsumeOK)
	if err != nil {
		return "", err
	}
	return d.short(), d.err
}

// Deliveries are the consumers' messages, closed once the connection
// ends.
func (c *Conn) Deliveries() <-chan *Delivery { return c.deliveries }

// Ack acknowledges the delivery tag, and every one before it when
// multiple.
func (c *Conn) Ack(tag uint64, multiple bool) error {
	return c.writeMethod(channel, method(basicAck).u64(tag).bits(multiple))
}

// Nack refuses the delivery tag, and every one before it when multiple,
// the broker requeueing them when requeue says to.
func (c *Conn) Nack(tag uint64, multiple, requeue bool) error {
	return c.writeMethod(channel, method(basicNack).u64(tag).bits(multiple, requeue))
}

// QueueDeclare declares a queue, or with passive checks that it is there,
// and returns its counts.
func (c *Conn) QueueDeclare(ctx context.Context, name string, passive, durable bool, args Table) (QueueInfo, error) {
	d, err := c.call(ctx, method(queueDeclare).u16(0).short(name).bits(passive, durable, false, false, false).table(args), queueDeclareOK)
	if err != nil {
		return QueueInfo{}, err
	}
	q := QueueInfo{Name: d.short(), Messages: d.u32(), Consumers: d.u32()}
	return q, d.err
}

// ConfirmSelect puts the channel in confirm mode: Publish then returns
// sequence numbers WaitConfirms waits on.
func (c *Conn) ConfirmSelect(ctx context.Context) error {
	if _, err := c.call(ctx, method(confirmSelect).bits(false), confirmSelectOK); err != nil {
		return err
	}
	c.mu.Lock()
	c.confirming = true
	c.mu.Unlock()
	return nil
}

// Publish sends a message, waiting while the broker blocks publishing, up
// to Timeout. In confirm mode it returns the message's sequence number, 0
// otherwise.
func (c *Conn) Publish(ctx context.Context, exchange, key string, mandatory bool, p Properties, body []byte) (uint64, error) {
	if len(body) > c.cfg.MaxBody {
		return 0, fmt.Errorf("%w: %d bytes, more than %d", ErrTooLarge, len(body), c.cfg.MaxBody)
	}
	if len(exchange) > 255 || len(key) > 255 {
		return 0, errors.New("amqp: an exchange or routing key longer than 255 bytes")
	}
	c.pmu.Lock()
	defer c.pmu.Unlock()
	if err := c.waitUnblocked(ctx); err != nil {
		return 0, err
	}
	var seq uint64
	c.mu.Lock()
	if c.confirming {
		c.seq++
		seq = c.seq
		c.pending[seq] = true
	}
	c.mu.Unlock()
	b := appendFrame(nil, frameMethod, channel, method(basicPublish).u16(0).short(exchange).short(key).bits(mandatory, false))
	b = appendFrame(b, frameHeader, channel, p.header(uint64(len(body))))
	most := int(c.frameMax) - frameOverhead
	for rest := body; len(rest) > 0; {
		n := min(len(rest), most)
		b = appendFrame(b, frameBody, channel, rest[:n])
		rest = rest[n:]
	}
	if err := c.write(b); err != nil {
		return 0, err
	}
	return seq, nil
}

// waitUnblocked waits, up to Timeout, while the broker blocks publishing.
func (c *Conn) waitUnblocked(ctx context.Context) error {
	var t *time.Timer
	for {
		c.mu.Lock()
		if c.err != nil {
			c.mu.Unlock()
			return c.err
		}
		if c.blocked == "" && !c.stopped {
			c.mu.Unlock()
			return nil
		}
		why, wait := c.blocked, c.unblock
		c.mu.Unlock()
		if t == nil {
			t = time.NewTimer(c.limit())
			defer t.Stop()
		}
		select {
		case <-wait:
		case <-c.done:
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if why == "" {
				why = "channel.flow "
			}
			return fmt.Errorf("%w: %sfor %v", ErrBlocked, why, c.limit())
		}
	}
}

// WaitConfirms waits until the broker has confirmed every message from
// sequence number from to to, and returns those it nacked.
func (c *Conn) WaitConfirms(ctx context.Context, from, to uint64) ([]uint64, error) {
	t := time.NewTimer(c.limit())
	defer t.Stop()
	for {
		c.mu.Lock()
		left := false
		for s := from; s <= to && !left; s++ {
			left = c.pending[s]
		}
		if !left {
			var nacked []uint64
			for s := from; s <= to; s++ {
				if c.nacked[s] {
					nacked = append(nacked, s)
					delete(c.nacked, s)
				}
			}
			c.mu.Unlock()
			return nacked, nil
		}
		ch, err := c.changed, c.err
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		select {
		case <-ch:
			if !t.Stop() {
				<-t.C
			}
			t.Reset(c.limit())
		case <-c.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
			return nil, fmt.Errorf("amqp: the broker confirmed none of the messages left in %v", c.limit())
		}
	}
}

// TakeReturns returns the mandatory messages the broker returned since the
// last call.
func (c *Conn) TakeReturns() []Return {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.returns
	c.returns = nil
	return r
}

// Close closes the connection, telling the broker, within Timeout.
func (c *Conn) Close() error {
	c.mu.Lock()
	failed := c.err != nil
	c.mu.Unlock()
	if !failed && c.writeMethod(0, method(connectionClose).u16(200).short("bye").u32(0)) == nil {
		t := time.NewTimer(c.limit())
		select {
		case <-c.closeOK:
		case <-c.done:
		case <-t.C:
		}
		t.Stop()
	}
	c.fail(ErrClosed)
	return nil
}
