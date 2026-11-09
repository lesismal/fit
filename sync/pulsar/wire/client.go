// Package pulsarwire is a client of Apache Pulsar's binary protocol, for
// Pulsar 2.10 and later: connections over TCP or TLS, authenticated with a
// token, topic lookups followed across brokers and proxies, partitioned
// topics' metadata, producers that send batches compressed with LZ4, zlib,
// zstd or snappy and wait for the broker's receipts, and consumers, of a
// durable subscription or a reader's, that read batches with flow control
// and acknowledge cumulatively. Its protobuf commands are encoded by hand:
// it is sync's own, written in fit as decision 036 has the protocols be.
// Each connection has a goroutine of its own reading the broker's frames,
// which are bounded; nothing runs on a fib worker.
package pulsarwire

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The defaults of Config.
const (
	DefaultTimeout         = 30 * time.Second
	DefaultKeepAlive       = 30 * time.Second
	DefaultMaxDecompressed = 64 << 20
	DefaultReceiverQueue   = 1000
	maxRedirects           = 20
)

// Config is a client's.
type Config struct {
	// URL is the service URL: pulsar://host:port, or pulsar+ssl://host:port
	// for TLS, with several hosts separated by commas, tried in order.
	URL string
	// TLS is the TLS config of pulsar+ssl://; the system's roots when nil.
	TLS *tls.Config
	// Token, when set, authenticates with the token method.
	Token string
	// DialTimeout bounds connecting, RequestTimeout each request's answer
	// and each write; KeepAlive is how often the broker is pinged, and a
	// connection with nothing from the broker for two of them is closed.
	DialTimeout, RequestTimeout, KeepAlive time.Duration
	// ClientVersion is the version the brokers see; fit-sync when empty.
	ClientVersion string
	// MaxDecompressed bounds a message's bytes once decompressed.
	MaxDecompressed int
}

// Client is a Pulsar client: its connections, by broker. Its methods may be
// called on several goroutines.
type Client struct {
	cfg   Config
	hosts []string
	tls   bool

	reqID, ids atomic.Uint64

	mu     sync.Mutex
	conns  map[string]*conn
	closed bool
}

// New makes a client of cfg; nothing is dialed until a call needs it.
func New(cfg Config) (*Client, error) {
	hosts, tlsOn, err := parseURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = DefaultTimeout
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = DefaultTimeout
	}
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = DefaultKeepAlive
	}
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = "fit-sync"
	}
	if cfg.MaxDecompressed <= 0 {
		cfg.MaxDecompressed = DefaultMaxDecompressed
	}
	if tlsOn && cfg.TLS == nil {
		cfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &Client{cfg: cfg, hosts: hosts, tls: tlsOn, conns: map[string]*conn{}}, nil
}

// parseURL reads a service URL's hosts, and whether it is TLS.
func parseURL(u string) ([]string, bool, error) {
	var rest string
	var tlsOn bool
	var ok bool
	if rest, ok = strings.CutPrefix(u, "pulsar://"); !ok {
		if rest, ok = strings.CutPrefix(u, "pulsar+ssl://"); !ok {
			return nil, false, fmt.Errorf("pulsar: %q is not a pulsar:// or pulsar+ssl:// URL", u)
		}
		tlsOn = true
	}
	rest = strings.TrimSuffix(rest, "/")
	var hosts []string
	for h := range strings.SplitSeq(rest, ",") {
		if _, _, err := net.SplitHostPort(h); err != nil {
			port := "6650"
			if tlsOn {
				port = "6651"
			}
			h = net.JoinHostPort(h, port)
		}
		if host, _, _ := net.SplitHostPort(h); host == "" || strings.ContainsAny(h, "/?#@ ") {
			return nil, false, fmt.Errorf("pulsar: %q in %q is not a host and port", h, u)
		}
		hosts = append(hosts, h)
	}
	return hosts, tlsOn, nil
}

// Close closes every connection, failing what waits on them.
func (cl *Client) Close() error {
	cl.mu.Lock()
	cl.closed = true
	conns := cl.conns
	cl.conns = map[string]*conn{}
	cl.mu.Unlock()
	for _, c := range conns {
		c.close(ErrClosed)
	}
	return nil
}

// getConn returns the open connection to addr (through it to proxyTo when
// set), dialing one when there is none.
func (cl *Client) getConn(ctx context.Context, addr, proxyTo string) (*conn, error) {
	key := addr + "|" + proxyTo
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return nil, ErrClosed
	}
	if c := cl.conns[key]; c != nil && c.failure() == nil {
		return c, nil
	}
	c, err := cl.dial(ctx, addr, cl.tls, proxyTo)
	if err != nil {
		return nil, err
	}
	cl.conns[key] = c
	return c, nil
}

// serviceConn returns a connection to the service URL's first host that
// answers.
func (cl *Client) serviceConn(ctx context.Context) (*conn, string, error) {
	var err error
	for _, h := range cl.hosts {
		var c *conn
		if c, err = cl.getConn(ctx, h, ""); err == nil {
			return c, h, nil
		}
	}
	return nil, "", err
}

// PartitionedMetadata returns how many partitions topic has: 0 for a topic
// that is not partitioned.
func (cl *Client) PartitionedMetadata(ctx context.Context, topic string) (int, error) {
	c, _, err := cl.serviceConn(ctx)
	if err != nil {
		return 0, err
	}
	resp, err := c.request(ctx, "the partitions of "+topic, func(req uint64) []byte { return partitionedMetadataCmd(topic, req) })
	if err != nil {
		return 0, err
	}
	return int(resp.partitions), nil
}

// lookup finds the broker that serves topic, following redirects, and
// returns a connection to it.
func (cl *Client) lookup(ctx context.Context, topic string) (*conn, error) {
	c, service, err := cl.serviceConn(ctx)
	if err != nil {
		return nil, err
	}
	authoritative := false
	for range maxRedirects {
		resp, err := c.request(ctx, "lookup of "+topic, func(req uint64) []byte { return lookupCmd(topic, req, authoritative) })
		if err != nil {
			return nil, err
		}
		url := resp.brokerURL
		if cl.tls {
			url = resp.brokerURLTLS
		}
		hosts, _, err := parseURL(url)
		if err != nil {
			return nil, fmt.Errorf("pulsar: lookup of %s: the broker %w", topic, err)
		}
		if resp.proxy {
			c, err = cl.getConn(ctx, service, hosts[0])
		} else {
			c, err = cl.getConn(ctx, hosts[0], "")
		}
		if err != nil || resp.lookupType == lookupConnect {
			return c, err
		}
		authoritative = resp.authoritative
	}
	return nil, fmt.Errorf("pulsar: lookup of %s: more than %d redirects", topic, maxRedirects)
}

// SendResult is the broker's answer to a send: the ID of the entry it
// stored, or why it did not.
type SendResult struct {
	ID  MessageID
	Err error
}

// Producer sends messages to a topic, or a partition of one.
type Producer struct {
	c           *conn
	id          uint64
	topic, name string
	lastSeq     int64
	compression Compression

	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan SendResult
	err     error
}

// CreateProducer makes a producer of topic named name (the broker names it
// when name is empty), compressing with compression. With deduplication
// on, a producer of a name the topic had carries on from the last
// sequence ID the broker stored of it.
func (cl *Client) CreateProducer(ctx context.Context, topic, name string, compression Compression) (*Producer, error) {
	c, err := cl.lookup(ctx, topic)
	if err != nil {
		return nil, err
	}
	p := &Producer{c: c, id: cl.ids.Add(1), topic: topic, compression: compression, pending: map[uint64]chan SendResult{}}
	if err := c.add(func() { c.producers[p.id] = p }); err != nil {
		return nil, err
	}
	resp, err := c.request(ctx, "the producer of "+topic, func(req uint64) []byte { return producerCmd(topic, p.id, req, name) })
	if err != nil {
		c.forget(p.id, 0)
		return nil, err
	}
	p.name, p.lastSeq = resp.producerName, resp.lastSequenceID
	p.next = uint64(max(p.lastSeq+1, 0))
	return p, nil
}

// add runs f under c's lock, unless c is closed.
func (c *conn) add(f func()) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	f()
	return nil
}

// forget takes producer p, or consumer cs, off c.
func (c *conn) forget(p, cs uint64) {
	c.mu.Lock()
	delete(c.producers, p)
	delete(c.consumers, cs)
	c.mu.Unlock()
}

// Name is the producer's name, as the broker has it.
func (p *Producer) Name() string { return p.name }

// LastSequenceID is the last sequence ID the broker stored of the
// producer's name when it was made, -1 for none (or deduplication off).
func (p *Producer) LastSequenceID() int64 { return p.lastSeq }

// MaxMessageSize is the most bytes the broker takes in one message, or
// batch.
func (p *Producer) MaxMessageSize() int { return p.c.maxMessage }

// Send sends msgs as one batch, whose result comes on the channel
// returned. Several sends may be in flight; their results come in order.
// Send is called on one goroutine at a time.
func (p *Producer) Send(msgs []ProducerMessage) (<-chan SendResult, error) {
	if len(msgs) == 0 {
		return nil, fmt.Errorf("pulsar: no messages to send")
	}
	p.mu.Lock()
	err, seq, n := p.err, p.next, len(msgs)
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	payload := buildBatch(msgs, seq)
	meta := metadata{producerName: p.name, sequenceID: seq, publishTime: uint64(time.Now().UnixMilli()), compression: p.compression,
		uncompressedSize: uint32(len(payload)), numMessages: int32(n), highestSequenceID: seq + uint64(n) - 1}
	data := compress(p.compression, payload)
	m := meta.encode()
	if len(m)+len(data) > p.c.maxMessage {
		return nil, fmt.Errorf("%w: %d bytes of %d messages, past %d", ErrTooLarge, len(m)+len(data), n, p.c.maxMessage)
	}
	ch := make(chan SendResult, 1)
	p.mu.Lock()
	if p.err != nil {
		p.mu.Unlock()
		return nil, p.err
	}
	p.pending[seq] = ch
	p.next += uint64(n)
	p.mu.Unlock()
	// Not under p.mu: the connection's reader takes it for the receipts.
	if err := p.c.write(payloadFrame(sendCmd(p.id, seq, n, seq+uint64(n)-1), m, data)); err != nil {
		return nil, err
	}
	return ch, nil
}

// receive takes the broker's command for the producer.
func (p *Producer) receive(cmd *command) {
	switch cmd.typ {
	case typeSendReceipt:
		p.mu.Lock()
		ch := p.pending[cmd.sequenceID]
		delete(p.pending, cmd.sequenceID)
		p.mu.Unlock()
		if ch != nil {
			ch <- SendResult{ID: cmd.messageID}
		}
	case typeSendError:
		// The producer's sends after the one that failed cannot be told
		// apart: it is done with.
		p.fail(fmt.Errorf("pulsar: send to %s: %w", p.topic, &ServerError{Code: cmd.serverError, Message: cmd.message}))
	default:
		p.fail(fmt.Errorf("%w: the broker closed the producer of %s", ErrClosed, p.topic))
	}
}

// fail fails the producer, and the sends it waits on.
func (p *Producer) fail(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	pending := p.pending
	p.pending = map[uint64]chan SendResult{}
	p.mu.Unlock()
	for _, ch := range pending {
		ch <- SendResult{Err: err}
	}
}

// Close closes the producer.
func (p *Producer) Close(ctx context.Context) error {
	defer p.fail(ErrClosed)
	defer p.c.forget(p.id, 0)
	if p.c.failure() != nil {
		return nil
	}
	_, err := p.c.request(ctx, "close the producer of "+p.topic, func(req uint64) []byte { return closeProducerCmd(p.id, req) })
	return err
}

// SubscribeOptions are a consumer's.
type SubscribeOptions struct {
	// Subscription is the subscription's name.
	Subscription string
	Type         SubType
	// Durable makes a subscription the broker keeps, acknowledged; a
	// reader's is not durable, and starts at Start.
	Durable bool
	// Start is where a reader starts: after the message it names, or at
	// EarliestID or LatestID; nil leaves it to Initial.
	Start *MessageID
	// Initial is where a durable subscription made now starts.
	Initial      InitialPosition
	ConsumerName string
	// ReceiverQueue is how many messages the broker may send ahead of what
	// is received; DefaultReceiverQueue when 0.
	ReceiverQueue int
	// Notify, when not nil, is signalled, without blocking, when a message
	// arrives or the consumer fails, so that one goroutine can wait on
	// several consumers.
	Notify chan struct{}
}

// Consumer reads a topic, or a partition of one.
type Consumer struct {
	c         *conn
	id        uint64
	topic     string
	queueSize int
	notify    chan struct{}
	signal    chan struct{}

	mu    sync.Mutex
	queue []Message
	taken int
	err   error
	ended bool
}

// Subscribe makes a consumer of topic.
func (cl *Client) Subscribe(ctx context.Context, topic string, o SubscribeOptions) (*Consumer, error) {
	c, err := cl.lookup(ctx, topic)
	if err != nil {
		return nil, err
	}
	if o.ReceiverQueue <= 0 {
		o.ReceiverQueue = DefaultReceiverQueue
	}
	cs := &Consumer{c: c, id: cl.ids.Add(1), topic: topic, queueSize: o.ReceiverQueue, notify: o.Notify, signal: make(chan struct{}, 1)}
	if err := c.add(func() { c.consumers[cs.id] = cs }); err != nil {
		return nil, err
	}
	so := subscribeCmdOpts{topic: topic, subscription: o.Subscription, consumerName: o.ConsumerName, subType: o.Type,
		consumer: cs.id, durable: o.Durable, start: o.Start, initial: o.Initial}
	if _, err := c.request(ctx, "subscribe to "+topic, func(req uint64) []byte { so.req = req; return subscribeCmd(so) }); err != nil {
		c.forget(0, cs.id)
		return nil, err
	}
	if err := c.write(frame(flowCmd(cs.id, cs.queueSize))); err != nil {
		return nil, err
	}
	return cs, nil
}

// Topic is the topic, or partition, the consumer reads.
func (cs *Consumer) Topic() string { return cs.topic }

// receive takes the broker's command for the consumer.
func (cs *Consumer) receive(cmd *command, rest []byte) error {
	switch cmd.typ {
	case typeCloseConsumer:
		cs.fail(fmt.Errorf("%w: the broker closed the consumer of %s", ErrClosed, cs.topic))
		return nil
	case typeReachedEndOfTopic:
		cs.mu.Lock()
		cs.ended = true
		cs.mu.Unlock()
		cs.wake()
		return nil
	}
	meta, data, err := splitPayload(rest)
	if err != nil {
		return err
	}
	m, err := decodeMetadata(meta, false)
	if err != nil {
		return err
	}
	var msgs []Message
	switch {
	case m.encrypted || m.chunks > 1:
		cs.fail(fmt.Errorf("%w in %s at %v: encrypted or chunked", ErrUnsupported, cs.topic, cmd.messageID))
		return nil
	case !m.marker:
		if data, err = decompress(m.compression, data, int(m.uncompressedSize), cs.c.cl.cfg.MaxDecompressed); err != nil {
			cs.fail(fmt.Errorf("pulsar: %s at %v: %w", cs.topic, cmd.messageID, err))
			return nil
		}
		if msgs, err = unbatch(cmd.messageID, m, data); err != nil {
			return err
		}
	}
	for i := range msgs {
		msgs[i].Redelivery = cmd.redelivery
	}
	cs.mu.Lock()
	// The permits the broker counted that are not received: a marker's,
	// and the compacted-out messages of a batch.
	cs.taken += max(int(m.numMessages)-len(msgs), 0)
	cs.queue = append(cs.queue, msgs...)
	over := len(cs.queue) > 2*cs.queueSize+10000
	cs.mu.Unlock()
	if over {
		return fmt.Errorf("%w: the broker sent %s's consumer more messages than it was let", ErrFrame, cs.topic)
	}
	cs.wake()
	return nil
}

func (cs *Consumer) wake() {
	for _, ch := range []chan struct{}{cs.signal, cs.notify} {
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// fail fails the consumer.
func (cs *Consumer) fail(err error) {
	cs.mu.Lock()
	if cs.err == nil {
		cs.err = err
	}
	cs.mu.Unlock()
	cs.wake()
}

// TryReceive returns the next message received, false when there is none
// yet. It fails once the consumer has failed and its messages are taken,
// with ErrEndOfTopic at a terminated topic's end.
func (cs *Consumer) TryReceive() (Message, bool, error) {
	cs.mu.Lock()
	if len(cs.queue) == 0 {
		err := cs.err
		if err == nil && cs.ended {
			err = ErrEndOfTopic
		}
		cs.mu.Unlock()
		return Message{}, false, err
	}
	m := cs.queue[0]
	cs.queue[0] = Message{}
	cs.queue = cs.queue[1:]
	cs.taken++
	permits := 0
	if cs.taken >= max(cs.queueSize/2, 1) {
		permits, cs.taken = cs.taken, 0
	}
	cs.mu.Unlock()
	if permits > 0 {
		if err := cs.c.write(frame(flowCmd(cs.id, permits))); err != nil {
			return m, true, nil // the next call fails with the connection
		}
	}
	return m, true, nil
}

// Receive waits for the next message.
func (cs *Consumer) Receive(ctx context.Context) (Message, error) {
	for {
		m, ok, err := cs.TryReceive()
		if ok || err != nil {
			return m, err
		}
		select {
		case <-cs.signal:
		case <-ctx.Done():
			return Message{}, ctx.Err()
		}
	}
}

// AckCumulative acknowledges id and every message before it.
func (cs *Consumer) AckCumulative(id MessageID) error {
	return cs.c.write(frame(ackCmd(cs.id, id)))
}

// LastMessageID asks the broker for the ID of the topic's last message; its
// entry is -1 when the topic has none.
func (cs *Consumer) LastMessageID(ctx context.Context) (MessageID, error) {
	resp, err := cs.c.request(ctx, "the last message of "+cs.topic, func(req uint64) []byte { return getLastMessageIDCmd(cs.id, req) })
	if err != nil {
		return MessageID{}, err
	}
	return resp.messageID, nil
}

// Close closes the consumer; a reader's subscription goes with it.
func (cs *Consumer) Close(ctx context.Context) error {
	defer cs.fail(ErrClosed)
	defer cs.c.forget(0, cs.id)
	if cs.c.failure() != nil {
		return nil
	}
	_, err := cs.c.request(ctx, "close the consumer of "+cs.topic, func(req uint64) []byte { return closeConsumerCmd(cs.id, req) })
	return err
}
