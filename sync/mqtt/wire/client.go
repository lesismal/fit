// Package mqttwire is an MQTT client, of versions 3.1.1 and 5, written for
// fit's sync connectors: TLS, a user and password, keep-alive, persistent
// sessions, QoS 0, 1 and 2 in both directions, topic filters, and MQTT 5's
// properties (user properties, message expiry, topic aliases both ways,
// content types, reason codes and strings). A Client is one connection,
// on goroutines of its own: one reads the server's packets, one keeps the
// connection alive. Messages the server sends are handed out on a bounded
// channel, and acknowledged only when the caller says so, with Ack, in the
// order they came. Publishes are bounded in flight. Packets are bounded in
// size both ways. A connection lost is an error, never retried here.
package mqttwire

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"
)

// The protocol versions, as CONNECT's protocol level names them.
const (
	Version311 byte = 4
	Version5   byte = 5
)

// The defaults of Config.
const (
	DefaultTimeout        = 30 * time.Second
	DefaultMaxPacketSize  = 1 << 20
	DefaultReceiveMaximum = 64
	DefaultMaxInflight    = 64
)

// The errors of a connection.
var (
	// ErrClosed says Close closed the connection.
	ErrClosed = errors.New("mqtt: the connection is closed")
	// ErrKeepAlive says the server did not answer a ping within the
	// keep-alive.
	ErrKeepAlive = errors.New("mqtt: the server did not answer a ping within the keep-alive")
	// ErrPacketTooLarge says a packet is past the size the receiver takes.
	ErrPacketTooLarge = errors.New("mqtt: a packet past the most bytes the receiver takes")
	// ErrNotSupported says the server does not take what a publish asks:
	// its QoS, or retain.
	ErrNotSupported = errors.New("mqtt: the server does not support it")
)

// Config is how a Client connects.
type Config struct {
	// Address is the server's, host:port.
	Address string
	// Dial makes the connection, in place of net.Dialer's.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLS, when not nil, has the connection go over TLS; its ServerName
	// is Address's host when empty.
	TLS *tls.Config
	// Version is Version311 or Version5; Version5 when 0.
	Version byte
	// ClientID names the client, and its session; a server of version 5,
	// or of 3.1.1 with CleanStart, assigns one when it is empty.
	ClientID string
	// Username and Password are sent when not empty.
	Username, Password string
	// KeepAlive is the most time between the client's packets; the client
	// pings the server every half of it, and takes no answer within it as
	// the connection lost. None when 0. A server of version 5 may set its
	// own.
	KeepAlive time.Duration
	// CleanStart starts a new session, the server's of the client ID
	// discarded; otherwise the server resumes it, with the messages it
	// kept for the client, and keeps it after the connection ends:
	// SessionExpiry long in version 5.
	CleanStart bool
	// SessionExpiry is how long a version 5 server keeps the session after
	// the connection ends; 0 ends it with the connection.
	SessionExpiry time.Duration
	// Timeout bounds connecting, the CONNACK, and each write.
	// DefaultTimeout when 0.
	Timeout time.Duration
	// MaxPacketSize is the most bytes of a packet the client takes, told
	// to a server of version 5; a larger one ends the connection.
	// DefaultMaxPacketSize when 0.
	MaxPacketSize int
	// ReceiveMaximum is the most messages of QoS 1 and 2 a version 5
	// server sends before the client acknowledges them, and the room of
	// Messages' channel. DefaultReceiveMaximum when 0.
	ReceiveMaximum int
	// TopicAliasMaximum is the most topic aliases a version 5 server may
	// use in the messages it sends; none when 0.
	TopicAliasMaximum int
	// MaxInflight is the most publishes of QoS 1 and 2 sent and not yet
	// acknowledged, lowered to a version 5 server's receive maximum.
	// DefaultMaxInflight when 0.
	MaxInflight int
}

// Message is an application message: one the server sends the client, or
// one the client publishes.
type Message struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
	// Dup says the server sent it before, unacknowledged.
	Dup bool

	// The properties of MQTT 5, left out in 3.1.1.

	// PayloadUTF8 says the payload is UTF-8 text.
	PayloadUTF8 bool
	// MessageExpiry is how long a server keeps the message for a client
	// not connected, in seconds; none when 0.
	MessageExpiry   time.Duration
	ContentType     string
	ResponseTopic   string
	CorrelationData []byte
	UserProperties  []UserProperty
	// SubscriptionIDs are those of the subscriptions it matched.
	SubscriptionIDs []int

	id uint16
}

// Subscription is a topic filter subscribed to, with the most QoS the
// server sends its messages at, and MQTT 5's options.
type Subscription struct {
	Filter string
	QoS    byte
	// NoLocal says the client's own publishes are not sent back to it.
	NoLocal bool
	// RetainAsPublished keeps the retain flag of messages forwarded.
	RetainAsPublished bool
	// RetainHandling says when the server sends the retained messages: 0
	// at each subscribe, 1 only when the subscription is new, 2 never.
	RetainHandling byte
}

// Client is a connection to an MQTT server.
type Client struct {
	cfg     Config
	version byte
	nc      net.Conn
	br      *bufio.Reader
	msgs    chan *Message
	sem     chan struct{}
	done    chan struct{}

	sessionPresent bool
	maxQoS         byte
	retainOK       bool
	maxOut         int // the server's most packet bytes; 0: no bound
	aliasMax       uint16

	// wmu orders writes, and holds the topic aliases sent.
	wmu     sync.Mutex
	aliases map[string]uint16

	mu        sync.Mutex
	err       error
	closing   bool // Close began: the connection ends as ErrClosed
	nextID    uint16
	pending   map[uint16]*Delivery
	subs      map[uint16]chan subResult
	inQoS2    map[uint16]bool // true once PUBREC is sent, until PUBREL
	inAliases map[uint16]string
	// pingSent is when the ping not answered yet was sent; zero when none.
	pingSent   time.Time
	delivering bool
}

type subResult struct {
	codes  []byte
	reason string
	err    error
}

// Delivery is a publish under way.
type Delivery struct {
	qos   byte
	stage int // QoS 2: 1 once PUBREC is taken and PUBREL sent
	done  chan struct{}
	err   error
}

// Wait waits until the server acknowledges the publish: at once for QoS
// 0, a PUBACK for QoS 1, a PUBCOMP for QoS 2. Its error is a ReasonError
// the server answered with, the connection's, or ctx's.
func (d *Delivery) Wait(ctx context.Context) error {
	select {
	case <-d.done:
		return d.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Delivery) finish(err error) {
	d.err = err
	close(d.done)
}

// Dial connects to cfg.Address, sends CONNECT and reads the CONNACK: a
// refusal is a *ConnectError.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Version == 0 {
		cfg.Version = Version5
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxPacketSize <= 0 {
		cfg.MaxPacketSize = DefaultMaxPacketSize
	}
	cfg.MaxPacketSize = min(cfg.MaxPacketSize, maxRemaining+5)
	if cfg.ReceiveMaximum <= 0 {
		cfg.ReceiveMaximum = DefaultReceiveMaximum
	}
	cfg.ReceiveMaximum = min(cfg.ReceiveMaximum, math.MaxUint16)
	cfg.TopicAliasMaximum = min(max(cfg.TopicAliasMaximum, 0), math.MaxUint16)
	if cfg.MaxInflight <= 0 {
		cfg.MaxInflight = DefaultMaxInflight
	}
	connect, err := connectPacket(&cfg)
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	var nc net.Conn
	if cfg.Dial != nil {
		nc, err = cfg.Dial(dctx, "tcp", cfg.Address)
	} else {
		nc, err = (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(dctx, "tcp", cfg.Address)
	}
	if err != nil {
		return nil, fmt.Errorf("mqtt: connect to %s: %w", cfg.Address, err)
	}
	deadline, _ := dctx.Deadline()
	nc.SetDeadline(deadline)
	stop := context.AfterFunc(dctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	c, err := handshake(dctx, nc, cfg, connect)
	stop()
	if err != nil {
		nc.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	go c.readLoop()
	if c.cfg.KeepAlive > 0 {
		go c.keepAlive()
	}
	return c, nil
}

// handshake does TLS's, when asked, sends CONNECT and reads the CONNACK.
func handshake(ctx context.Context, nc net.Conn, cfg Config, connect []byte) (*Client, error) {
	if cfg.TLS != nil {
		tc := cfg.TLS.Clone()
		if tc.ServerName == "" {
			tc.ServerName, _, _ = net.SplitHostPort(cfg.Address)
		}
		t := tls.Client(nc, tc)
		if err := t.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("mqtt: TLS with %s: %w", cfg.Address, err)
		}
		nc = t
	}
	if _, err := nc.Write(connect); err != nil {
		return nil, fmt.Errorf("mqtt: send CONNECT to %s: %w", cfg.Address, err)
	}
	c := &Client{cfg: cfg, version: cfg.Version, nc: nc, br: bufio.NewReaderSize(nc, 16<<10),
		msgs: make(chan *Message, cfg.ReceiveMaximum), done: make(chan struct{}), maxQoS: 2, retainOK: true,
		aliases: map[string]uint16{}, pending: map[uint16]*Delivery{}, subs: map[uint16]chan subResult{},
		inQoS2: map[uint16]bool{}, inAliases: map[uint16]string{}}
	first, body, err := readPacket(c.br, cfg.MaxPacketSize)
	if err != nil {
		return nil, fmt.Errorf("mqtt: read CONNACK from %s: %w", cfg.Address, err)
	}
	if first != typeConnack<<4 {
		return nil, fmt.Errorf("mqtt: %s answered CONNECT with %s", cfg.Address, packetName(first))
	}
	if err := c.connack(body); err != nil {
		return nil, err
	}
	c.sem = make(chan struct{}, cfg.MaxInflight)
	return c, nil
}

// connectPacket makes CONNECT of cfg.
func connectPacket(cfg *Config) ([]byte, error) {
	if cfg.Version != Version311 && cfg.Version != Version5 {
		return nil, fmt.Errorf("mqtt: version %d is not 4 (3.1.1) or 5", cfg.Version)
	}
	if cfg.ClientID == "" && cfg.Version == Version311 && !cfg.CleanStart {
		return nil, errors.New("mqtt: a session kept in MQTT 3.1.1 needs a client ID")
	}
	if cfg.Password != "" && cfg.Username == "" && cfg.Version == Version311 {
		return nil, errors.New("mqtt: a password needs a user name in MQTT 3.1.1")
	}
	for _, s := range []string{cfg.ClientID, cfg.Username, cfg.Password} {
		if !validString(s) {
			return nil, errors.New("mqtt: a client ID, user name or password of more than 65535 bytes, not UTF-8, or with a null character")
		}
	}
	ka := (cfg.KeepAlive + time.Second - 1) / time.Second
	if ka > math.MaxUint16 {
		return nil, fmt.Errorf("mqtt: a keep-alive of %v is past 65535s", cfg.KeepAlive)
	}
	cfg.KeepAlive = ka * time.Second
	var flags byte
	if cfg.Username != "" {
		flags |= 0x80
	}
	if cfg.Password != "" {
		flags |= 0x40
	}
	if cfg.CleanStart {
		flags |= 0x02
	}
	b := appendString(nil, "MQTT")
	b = append(b, cfg.Version, flags)
	b = appendUint16(b, uint16(ka))
	if cfg.Version == Version5 {
		var w propsWriter
		if cfg.SessionExpiry > 0 {
			w.u32(propSessionExpiry, uint32(min((cfg.SessionExpiry+time.Second-1)/time.Second, math.MaxUint32)))
		}
		w.u16(propReceiveMaximum, uint16(cfg.ReceiveMaximum))
		w.u32(propMaximumPacketSize, uint32(cfg.MaxPacketSize))
		if cfg.TopicAliasMaximum > 0 {
			w.u16(propTopicAliasMaximum, uint16(cfg.TopicAliasMaximum))
		}
		b = w.appendTo(b)
	}
	b = appendString(b, cfg.ClientID)
	if cfg.Username != "" {
		b = appendString(b, cfg.Username)
	}
	if cfg.Password != "" {
		b = appendString(b, cfg.Password)
	}
	return packet(typeConnect<<4, b), nil
}

// connack reads the CONNACK's body.
func (c *Client) connack(body []byte) error {
	r := &reader{b: body}
	flags := r.byte()
	code := r.byte()
	var p props
	if c.version == Version5 {
		p = r.props()
	}
	if r.err != nil || r.left() > 0 || flags&^1 != 0 {
		return fmt.Errorf("mqtt: %s sent a malformed CONNACK", c.cfg.Address)
	}
	if code != 0 {
		return &ConnectError{Version: c.version, Code: code, Reason: p.reason}
	}
	c.sessionPresent = flags&1 == 1
	if c.version == Version5 {
		if p.receiveMax > 0 {
			c.cfg.MaxInflight = min(c.cfg.MaxInflight, int(p.receiveMax))
		}
		if p.hasMaxQoS {
			c.maxQoS = p.maxQoS
		}
		if p.hasRetain {
			c.retainOK = p.retainAvailable == 1
		}
		c.maxOut = int(p.maxPacket)
		c.aliasMax = p.aliasMax
		if p.hasKeepAlive {
			c.cfg.KeepAlive = time.Duration(p.serverKeepAlive) * time.Second
		}
	}
	return nil
}

// SessionPresent reports whether the server resumed a session of the
// client ID.
func (c *Client) SessionPresent() bool { return c.sessionPresent }

// Messages are what the server sends the subscriptions, in the order it
// sent them. A message of QoS 1 or 2 is acknowledged once Ack is called;
// the channel is not closed: Done says the connection is lost.
func (c *Client) Messages() <-chan *Message { return c.msgs }

// Done is closed once the connection is lost or closed; Err says why.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is why the connection ended: nil while it is up.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close sends DISCONNECT, normal, and closes the connection: the server
// keeps the session, as CleanStart and SessionExpiry said, with the
// messages not acknowledged in it.
func (c *Client) Close() error {
	// Marked first: the server closes its side on DISCONNECT, and the
	// reader's EOF may come before the fail below.
	c.mu.Lock()
	up := c.err == nil
	c.closing = true
	c.mu.Unlock()
	if up {
		c.write(disconnectPacket(c.version, 0))
	}
	c.fail(ErrClosed)
	return nil
}

func disconnectPacket(version byte, code byte) []byte {
	if version == Version5 && code != 0 {
		return []byte{typeDisconnect << 4, 1, code}
	}
	return []byte{typeDisconnect << 4, 0}
}

// abort ends the connection on err, telling a server of version 5 the
// reason code first.
func (c *Client) abort(code byte, err error) {
	if c.version == Version5 && c.Err() == nil {
		c.write(disconnectPacket(c.version, code))
	}
	c.fail(err)
}

// fail ends the connection on err, the first one, and fails what waits on
// it.
func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	if c.closing {
		err = ErrClosed
	}
	c.err = err
	pending, subs := c.pending, c.subs
	c.pending, c.subs = nil, nil
	c.mu.Unlock()
	close(c.done)
	c.nc.Close()
	for _, d := range pending {
		d.finish(err)
	}
	for _, ch := range subs {
		ch <- subResult{err: err}
	}
}

// write writes a packet, in turn with the others; an error ends the
// connection.
func (c *Client) write(p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.writeLocked(p)
}

func (c *Client) writeLocked(p []byte) error {
	if err := c.Err(); err != nil {
		return err
	}
	c.nc.SetWriteDeadline(time.Now().Add(c.cfg.Timeout))
	if _, err := c.nc.Write(p); err != nil {
		err = fmt.Errorf("mqtt: write to %s: %w", c.cfg.Address, err)
		c.fail(err)
		return err
	}
	return nil
}

// readPacket reads a packet: its first byte and its body, of at most max
// bytes with its fixed header.
func readPacket(br *bufio.Reader, max int) (byte, []byte, error) {
	first, err := br.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, shift := 0, 0
	for i := 0; ; i++ {
		if i == 4 {
			return 0, nil, errMalformed
		}
		c, err := br.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n |= int(c&0x7F) << shift
		shift += 7
		if c&0x80 == 0 {
			if n+2+i > max {
				return 0, nil, fmt.Errorf("%w: %d bytes, past %d", ErrPacketTooLarge, n+2+i, max)
			}
			break
		}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		return 0, nil, err
	}
	return first, body, nil
}

func packetName(first byte) string { return typeNames[first>>4] }

// readLoop reads the server's packets until the connection ends.
func (c *Client) readLoop() {
	for {
		first, body, err := readPacket(c.br, c.cfg.MaxPacketSize)
		switch {
		case errors.Is(err, ErrPacketTooLarge):
			c.abort(0x95, err)
			return
		case errors.Is(err, errMalformed):
			c.abort(0x81, err)
			return
		case err != nil:
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			c.fail(fmt.Errorf("mqtt: the connection to %s: %w", c.cfg.Address, err))
			return
		}
		c.mu.Lock()
		c.pingSent = time.Time{}
		c.mu.Unlock()
		if code, err := c.handle(first, body); err != nil {
			c.abort(code, err)
			return
		}
	}
}

// handle takes a packet; an error ends the connection, with its reason
// code.
func (c *Client) handle(first byte, body []byte) (byte, error) {
	r := &reader{b: body}
	typ, flags := first>>4, first&0x0F
	if typ != typePublish && flags != 0 && !(typ == typePubrel && flags == 2) {
		return 0x81, fmt.Errorf("mqtt: %s with flags %x: %w", packetName(first), flags, errMalformed)
	}
	switch typ {
	case typePublish:
		return c.publish(flags, r)
	case typePuback, typePubrec, typePubcomp:
		return c.ack(typ, r)
	case typePubrel:
		id := r.u16()
		c.reasonOf(r)
		if r.err != nil || flags != 2 {
			return 0x81, fmt.Errorf("mqtt: PUBREL: %w", errMalformed)
		}
		c.mu.Lock()
		delete(c.inQoS2, id)
		c.mu.Unlock()
		c.write([]byte{typePubcomp << 4, 2, byte(id >> 8), byte(id)})
	case typeSuback:
		id := r.u16()
		var p props
		if c.version == Version5 {
			p = r.props()
		}
		codes := r.take(r.left())
		if r.err != nil || len(codes) == 0 {
			return 0x81, fmt.Errorf("mqtt: SUBACK: %w", errMalformed)
		}
		c.mu.Lock()
		ch := c.subs[id]
		delete(c.subs, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- subResult{codes: codes, reason: p.reason}
		}
	case typePingresp:
	case typeDisconnect:
		if c.version == Version311 {
			return 0x82, errors.New("mqtt: the server sent DISCONNECT, which MQTT 3.1.1 servers do not")
		}
		code, reason := c.reasonOf(r)
		return 0, &ReasonError{Packet: "DISCONNECT", Code: code, Reason: reason}
	default:
		return 0x82, fmt.Errorf("mqtt: the server sent %s, which this client does not take", packetName(first))
	}
	return 0, nil
}

// reasonOf reads an acknowledgement's reason code and its reason string,
// in MQTT 5: what is left of the body, which may be nothing (success).
func (c *Client) reasonOf(r *reader) (byte, string) {
	if c.version != Version5 || r.left() == 0 {
		return 0, ""
	}
	code := r.byte()
	if r.left() == 0 {
		return code, ""
	}
	p := r.props()
	return code, p.reason
}

// publish takes a PUBLISH, and hands its message out.
func (c *Client) publish(flags byte, r *reader) (byte, error) {
	m := &Message{QoS: flags >> 1 & 3, Retain: flags&1 == 1, Dup: flags&8 == 8}
	if m.QoS == 3 {
		return 0x81, fmt.Errorf("mqtt: PUBLISH of QoS 3: %w", errMalformed)
	}
	m.Topic = r.string()
	if m.QoS > 0 {
		m.id = r.u16()
	}
	if c.version == Version5 {
		p := r.props()
		m.PayloadUTF8, m.ContentType, m.ResponseTopic = p.payloadUTF8, p.contentType, p.responseTopic
		m.CorrelationData, m.UserProperties, m.SubscriptionIDs = p.correlation, p.user, p.subIDs
		if p.hasExpiry {
			m.MessageExpiry = time.Duration(p.expiry) * time.Second
		}
		if p.alias != 0 {
			if int(p.alias) > c.cfg.TopicAliasMaximum {
				return 0x94, fmt.Errorf("mqtt: topic alias %d past the most, %d", p.alias, c.cfg.TopicAliasMaximum)
			}
			c.mu.Lock()
			if m.Topic == "" {
				m.Topic = c.inAliases[p.alias]
			} else {
				c.inAliases[p.alias] = m.Topic
			}
			c.mu.Unlock()
			if m.Topic == "" {
				return 0x82, fmt.Errorf("mqtt: topic alias %d was never set", p.alias)
			}
		}
	}
	if r.err != nil {
		return 0x81, fmt.Errorf("mqtt: PUBLISH: %w", r.err)
	}
	if err := ValidTopicName(m.Topic); err != nil {
		return 0x82, err
	}
	if m.QoS > 0 && m.id == 0 {
		return 0x82, errors.New("mqtt: PUBLISH of packet identifier 0")
	}
	m.Payload = append([]byte{}, r.take(r.left())...)
	if m.QoS == 2 {
		c.mu.Lock()
		rec, seen := c.inQoS2[m.id]
		if !seen {
			c.inQoS2[m.id] = false
		}
		c.mu.Unlock()
		if seen {
			// A copy of one handed out already: answered once that one
			// is acknowledged, or again now if it was.
			if rec {
				c.write([]byte{typePubrec << 4, 2, byte(m.id >> 8), byte(m.id)})
			}
			return 0, nil
		}
	}
	c.mu.Lock()
	c.delivering = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.delivering = false
		if !c.pingSent.IsZero() {
			c.pingSent = time.Now()
		}
		c.mu.Unlock()
	}()
	select {
	case c.msgs <- m:
	case <-c.done:
	}
	return 0, nil
}

// ack takes a PUBACK, PUBREC or PUBCOMP of a publish.
func (c *Client) ack(typ byte, r *reader) (byte, error) {
	id := r.u16()
	code, reason := c.reasonOf(r)
	if r.err != nil || r.left() > 0 {
		return 0x81, fmt.Errorf("mqtt: %s: %w", typeNames[typ], errMalformed)
	}
	c.mu.Lock()
	d := c.pending[id]
	want := map[byte]byte{typePuback: 1, typePubrec: 2, typePubcomp: 2}[typ]
	if d == nil || d.qos != want || (typ == typePubcomp) != (d.stage == 1) {
		again := d != nil && d.stage == 1
		c.mu.Unlock()
		if typ != typePubrec {
			return 0, nil
		}
		// A PUBREC again: PUBREL again. Not one of ours: PUBREL too, so
		// that the server lets it go (MQTT 5: packet identifier not found).
		code := byte(0x92)
		if again {
			code = 0
		}
		return 0, c.write(pubrel(c.version, id, code))
	}
	var err error
	if code >= 0x80 {
		err = &ReasonError{Packet: typeNames[typ], Code: code, Reason: reason}
	}
	if typ == typePubrec && err == nil {
		d.stage = 1
		c.mu.Unlock()
		return 0, c.write(pubrel(c.version, id, 0))
	}
	delete(c.pending, id)
	c.mu.Unlock()
	<-c.sem
	d.finish(err)
	return 0, nil
}

func pubrel(version byte, id uint16, code byte) []byte {
	if version == Version5 && code != 0 {
		return []byte{typePubrel<<4 | 2, 3, byte(id >> 8), byte(id), code}
	}
	return []byte{typePubrel<<4 | 2, 2, byte(id >> 8), byte(id)}
}

// Ack acknowledges a message Messages handed out: PUBACK for QoS 1,
// PUBREC for QoS 2 (PUBCOMP follows the server's PUBREL by itself),
// nothing for QoS 0. Messages are acknowledged in the order they came, as
// MQTT asks; the server sends again, on a later connection of the
// session, those that were not.
func (c *Client) Ack(m *Message) error {
	switch m.QoS {
	case 1:
		return c.write([]byte{typePuback << 4, 2, byte(m.id >> 8), byte(m.id)})
	case 2:
		c.mu.Lock()
		if c.inQoS2 != nil {
			c.inQoS2[m.id] = true
		}
		c.mu.Unlock()
		return c.write([]byte{typePubrec << 4, 2, byte(m.id >> 8), byte(m.id)})
	}
	return nil
}

// allocID takes a packet identifier no publish or subscribe under way
// holds; c.mu is held.
func (c *Client) allocID() (uint16, error) {
	for range math.MaxUint16 {
		c.nextID++
		if c.nextID == 0 {
			c.nextID = 1
		}
		_, p := c.pending[c.nextID]
		_, s := c.subs[c.nextID]
		if !p && !s {
			return c.nextID, nil
		}
	}
	return 0, errors.New("mqtt: every packet identifier is in use")
}

// Subscribe subscribes to subs, and returns the QoS the server granted
// each; a filter it refused is a *ReasonError.
func (c *Client) Subscribe(ctx context.Context, subs ...Subscription) ([]byte, error) {
	if len(subs) == 0 {
		return nil, errors.New("mqtt: no subscriptions")
	}
	for _, s := range subs {
		if err := ValidTopicFilter(s.Filter); err != nil {
			return nil, err
		}
		if s.QoS > 2 || s.RetainHandling > 2 {
			return nil, fmt.Errorf("mqtt: subscription %s: QoS %d or retain handling %d past 2", s.Filter, s.QoS, s.RetainHandling)
		}
	}
	ch := make(chan subResult, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	id, err := c.allocID()
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	c.subs[id] = ch
	c.mu.Unlock()
	b := appendUint16(nil, id)
	if c.version == Version5 {
		b = append(b, 0)
	}
	for _, s := range subs {
		o := s.QoS
		if c.version == Version5 {
			if s.NoLocal {
				o |= 4
			}
			if s.RetainAsPublished {
				o |= 8
			}
			o |= s.RetainHandling << 4
		}
		b = append(appendString(b, s.Filter), o)
	}
	if err := c.write(packet(typeSubscribe<<4|2, b)); err != nil {
		return nil, err
	}
	var res subResult
	select {
	case res = <-ch:
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.subs, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, res.err
	}
	if len(res.codes) != len(subs) {
		return res.codes, fmt.Errorf("mqtt: SUBACK of %d codes for %d subscriptions", len(res.codes), len(subs))
	}
	for i, code := range res.codes {
		if code >= 0x80 {
			return res.codes, fmt.Errorf("mqtt: subscribe to %s: %w", subs[i].Filter, &ReasonError{Packet: "SUBACK", Code: code, Reason: res.reason})
		}
	}
	return res.codes, nil
}

// Publish publishes m and waits for its acknowledgement: Send, then Wait.
func (c *Client) Publish(ctx context.Context, m *Message) error {
	d, err := c.Send(ctx, m)
	if err != nil {
		return err
	}
	return d.Wait(ctx)
}

// Send publishes m without waiting for its acknowledgement, which the
// Delivery waits for; it waits, though, while MaxInflight publishes are
// not acknowledged. Publishes go out in the order they are sent, a topic
// sent before as its alias when the server takes aliases.
func (c *Client) Send(ctx context.Context, m *Message) (*Delivery, error) {
	if err := ValidTopicName(m.Topic); err != nil {
		return nil, err
	}
	switch {
	case m.QoS > 2:
		return nil, fmt.Errorf("mqtt: QoS %d", m.QoS)
	case m.QoS > c.maxQoS:
		return nil, fmt.Errorf("%w: QoS %d, past the server's most, %d", ErrNotSupported, m.QoS, c.maxQoS)
	case m.Retain && !c.retainOK:
		return nil, fmt.Errorf("%w: retain", ErrNotSupported)
	}
	d := &Delivery{qos: m.QoS, done: make(chan struct{})}
	if m.QoS > 0 {
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.done:
			return nil, c.Err()
		}
		c.mu.Lock()
		if c.err != nil {
			err := c.err
			c.mu.Unlock()
			<-c.sem
			return nil, err
		}
		id, err := c.allocID()
		if err != nil {
			c.mu.Unlock()
			<-c.sem
			return nil, err
		}
		m.id = id
		c.pending[id] = d
		c.mu.Unlock()
	}
	c.wmu.Lock()
	p := c.encodePublish(m, m.Topic, 0)
	if c.maxOut > 0 && len(p) > c.maxOut {
		c.wmu.Unlock()
		c.drop(d, m.id)
		return nil, fmt.Errorf("%w: a message to %s of %d bytes, past the server's %d", ErrPacketTooLarge, m.Topic, len(p), c.maxOut)
	}
	if c.aliasMax > 0 {
		if a, ok := c.aliases[m.Topic]; ok {
			p = c.encodePublish(m, "", a)
		} else if len(c.aliases) < int(c.aliasMax) {
			// The first with the alias carries the topic too.
			a = uint16(len(c.aliases) + 1)
			c.aliases[m.Topic] = a
			p = c.encodePublish(m, m.Topic, a)
		}
	}
	err := c.writeLocked(p)
	c.wmu.Unlock()
	if err != nil {
		return nil, err
	}
	if m.QoS == 0 {
		d.finish(nil)
	}
	return d, nil
}

// drop lets a publish not sent go.
func (c *Client) drop(d *Delivery, id uint16) {
	if d.qos == 0 {
		return
	}
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
	<-c.sem
}

// encodePublish makes a PUBLISH of m to topic, empty when alias stands
// for it, or setting alias to it.
func (c *Client) encodePublish(m *Message, topic string, alias uint16) []byte {
	first := typePublish<<4 | m.QoS<<1
	if m.Retain {
		first |= 1
	}
	b := appendString(make([]byte, 0, len(topic)+len(m.Payload)+32), topic)
	if m.QoS > 0 {
		b = appendUint16(b, m.id)
	}
	if c.version == Version5 {
		var w propsWriter
		if m.PayloadUTF8 {
			w.byte(propPayloadFormat, 1)
		}
		if m.MessageExpiry > 0 {
			w.u32(propMessageExpiry, uint32(min((m.MessageExpiry+time.Second-1)/time.Second, math.MaxUint32)))
		}
		if alias != 0 {
			w.u16(propTopicAlias, alias)
		}
		if m.ResponseTopic != "" {
			w.str(propResponseTopic, m.ResponseTopic)
		}
		if m.CorrelationData != nil {
			w.bin(propCorrelationData, m.CorrelationData)
		}
		w.user(m.UserProperties)
		if m.ContentType != "" {
			w.str(propContentType, m.ContentType)
		}
		b = w.appendTo(b)
	}
	return packet(first, append(b, m.Payload...))
}

// keepAlive pings the server every half of the keep-alive, and ends the
// connection when a ping is not answered within it. While the reader
// waits for room to hand a message out, the server's answer waits unread:
// the wait is not counted.
func (c *Client) keepAlive() {
	ka := c.cfg.KeepAlive
	t := time.NewTicker(ka / 2)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		c.mu.Lock()
		now := time.Now()
		if !c.pingSent.IsZero() {
			late := !c.delivering && now.Sub(c.pingSent) >= ka
			c.mu.Unlock()
			if late {
				c.fail(ErrKeepAlive)
				return
			}
			continue
		}
		c.pingSent = now
		c.mu.Unlock()
		c.write([]byte{typePingreq << 4, 0})
	}
}
