// Package kafkawire is a Kafka client written in fit, with no driver
// imported: the protocol's APIs for metadata, produce, fetch, list offsets
// and API versions (and creating and deleting topics, for tools and
// tests), record batches of magic 2 with every codec (gzip from the
// standard library, snappy and zstd from klauspost/compress, which fit
// already uses for HTTP, and LZ4 written here), SASL PLAIN and SCRAM, and
// TLS. It speaks Kafka 3.x and 4.x and Redpanda.
//
// The client follows partitions' leaders: a request a broker refuses
// because it no longer leads the partition (or a connection that fails)
// reads the metadata again and goes to the new leader, until the call's
// context ends or Config.RetryFor has passed. It is not a consumer group
// and not idempotent: offsets are the caller's to keep (sync keeps them in
// its checkpoint), and a produce retried after its answer was lost writes
// its records again, so delivery is at least once.
//
// Connections are blocking net.Conns, one goroutine reading each, not on
// fib's workers: sync runs off the request path, and a broker's
// connections are few and long (decision 036). See
// docs/decisions/036-sync.md.
package kafkawire

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"sync"
	"time"
)

// Config is a client's settings; New fills the zero values.
type Config struct {
	// Brokers are the bootstrap brokers' host:port addresses.
	Brokers []string
	// ClientID names the client in brokers' logs and quotas ("fit").
	ClientID string
	// TLS, when set, is used for every connection; its ServerName
	// defaults to the broker's host.
	TLS *tls.Config
	// SASL, when set, authenticates every connection.
	SASL *SASL
	// Dial replaces the net.Dialer, for tests and proxies.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)

	DialTimeout    time.Duration // a connection's dial, TLS and SASL (10s)
	RequestTimeout time.Duration // an answer, beyond the time the broker is asked to wait (30s)
	RetryFor       time.Duration // how long a call retries retriable errors (30s)
	RetryBackoff   time.Duration // the first wait between attempts, doubling to 1s (100ms)
	MetadataMaxAge time.Duration // metadata older than this is read again (5m)

	// MaxResponseBytes bounds a response (128 MiB); MaxBatchBytes bounds
	// a record batch decompressed (64 MiB).
	MaxResponseBytes int
	MaxBatchBytes    int
	// AllowAutoTopicCreation lets a metadata request create the topics
	// it names, when the brokers allow that.
	AllowAutoTopicCreation bool
}

// Client is a connection to a Kafka cluster. It is safe for use by many
// goroutines; calls to different brokers run in parallel.
type Client struct {
	cfg Config
	dec decompressor

	mu      sync.Mutex
	closed  bool
	brokers map[int32]string // node ID -> host:port
	conns   map[connKey]*connSlot
	meta    *Metadata
	metaAt  time.Time
	stale   bool
	topics  map[string]struct{} // topics the client has asked about

	metaMu sync.Mutex // one metadata request at a time
}

// connKey names a connection: a broker's address and what it carries.
type connKey struct {
	addr  string
	fetch bool
}

// connSlot holds a connection, or the dial in progress.
type connSlot struct {
	ready chan struct{}
	bc    *brokerConn
	err   error
}

// New returns a client of cfg. It connects when first used.
func New(cfg Config) (*Client, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka: no brokers")
	}
	for _, b := range cfg.Brokers {
		if _, _, err := net.SplitHostPort(b); err != nil {
			return nil, fmt.Errorf("kafka: broker %q: %w", b, err)
		}
	}
	if cfg.SASL != nil {
		switch cfg.SASL.Mechanism {
		case SASLPlain, SASLScramSHA256, SASLScramSHA512:
		default:
			return nil, fmt.Errorf("kafka: SASL mechanism %q (PLAIN, SCRAM-SHA-256, SCRAM-SHA-512)", cfg.SASL.Mechanism)
		}
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&cfg.DialTimeout, 10*time.Second)
	def(&cfg.RequestTimeout, 30*time.Second)
	def(&cfg.RetryFor, 30*time.Second)
	def(&cfg.RetryBackoff, 100*time.Millisecond)
	def(&cfg.MetadataMaxAge, 5*time.Minute)
	if cfg.ClientID == "" {
		cfg.ClientID = "fit"
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 128 << 20
	}
	if cfg.MaxBatchBytes <= 0 {
		cfg.MaxBatchBytes = 64 << 20
	}
	c := &Client{cfg: cfg, brokers: map[int32]string{}, conns: map[connKey]*connSlot{}, topics: map[string]struct{}{}}
	c.dec.max = cfg.MaxBatchBytes
	return c, nil
}

// Close closes every connection; calls in progress fail.
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	conns := c.conns
	c.conns = map[connKey]*connSlot{}
	c.mu.Unlock()
	for _, s := range conns {
		go func() {
			<-s.ready
			if s.bc != nil {
				s.bc.close(errClosed)
			}
		}()
	}
	return nil
}

// conn returns a connection to addr, dialing it when there is none or it
// has failed. Callers of one address share the dial.
func (c *Client) conn(ctx context.Context, addr string, fetch bool) (*brokerConn, error) {
	k := connKey{addr, fetch}
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, errClosed
		}
		s := c.conns[k]
		if s == nil {
			s = &connSlot{ready: make(chan struct{})}
			c.conns[k] = s
			c.mu.Unlock()
			s.bc, s.err = dialBroker(ctx, &c.cfg, addr)
			close(s.ready)
			c.mu.Lock()
			if s.err != nil && c.conns[k] == s {
				delete(c.conns, k)
			}
			closed := c.closed
			c.mu.Unlock()
			if closed && s.bc != nil {
				s.bc.close(errClosed)
				return nil, errClosed
			}
			return s.bc, s.err
		}
		c.mu.Unlock()
		select {
		case <-s.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if s.err != nil {
			return nil, s.err
		}
		if s.bc.usable(time.Now()) {
			return s.bc, nil
		}
		c.mu.Lock()
		if c.conns[k] == s {
			delete(c.conns, k)
		}
		c.mu.Unlock()
		if s.bc.err == nil {
			s.bc.close(errors.New("kafka: SASL session ending"))
		}
	}
}

// Broker is a broker as metadata names it.
type Broker struct {
	NodeID int32
	Host   string
	Port   int32
	Rack   string
}

// Addr is the broker's host:port.
func (b Broker) Addr() string { return net.JoinHostPort(b.Host, strconv.Itoa(int(b.Port))) }

// Metadata is the cluster's brokers and the topics asked about.
type Metadata struct {
	ClusterID    string
	ControllerID int32
	Brokers      []Broker
	Topics       []TopicMetadata
}

// TopicMetadata is a topic's partitions; Err is the broker's error for the
// topic (UNKNOWN_TOPIC_OR_PARTITION for one that does not exist).
type TopicMetadata struct {
	Name       string
	Err        error
	Internal   bool
	Partitions []PartitionMetadata
}

// PartitionMetadata is a partition's leader and replicas; Leader is -1
// while it has none.
type PartitionMetadata struct {
	Partition       int32
	Err             error
	Leader          int32
	LeaderEpoch     int32
	Replicas, ISR   []int32
	OfflineReplicas []int32
}

// Topic returns the topic's metadata, or nil.
func (m *Metadata) Topic(name string) *TopicMetadata {
	for i := range m.Topics {
		if m.Topics[i].Name == name {
			return &m.Topics[i]
		}
	}
	return nil
}

// Metadata asks a broker for the cluster's brokers and topics' partitions
// (every topic when none is named), and keeps what it learns for routing.
func (c *Client) Metadata(ctx context.Context, topics ...string) (*Metadata, error) {
	all := len(topics) == 0
	m, err := c.requestMetadata(ctx, topics, all)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for _, t := range topics {
		c.topics[t] = struct{}{}
	}
	c.mu.Unlock()
	c.merge(m, all)
	return m, nil
}

// requestMetadata sends one Metadata request to a known broker or a
// bootstrap one, trying each until one answers.
func (c *Client) requestMetadata(ctx context.Context, topics []string, all bool) (*Metadata, error) {
	c.mu.Lock()
	addrs := make([]string, 0, len(c.brokers)+len(c.cfg.Brokers))
	for _, a := range c.brokers {
		addrs = append(addrs, a)
	}
	c.mu.Unlock()
	rand.Shuffle(len(addrs), func(i, j int) { addrs[i], addrs[j] = addrs[j], addrs[i] })
	boot := append([]string(nil), c.cfg.Brokers...)
	rand.Shuffle(len(boot), func(i, j int) { boot[i], boot[j] = boot[j], boot[i] })
	addrs = append(addrs, boot...)
	var last error
	for _, addr := range addrs {
		bc, err := c.conn(ctx, addr, false)
		if err != nil {
			last = err
			if ctx.Err() != nil {
				return nil, err
			}
			continue
		}
		m, err := c.metadataOn(ctx, bc, topics, all)
		if err == nil {
			return m, nil
		}
		last = err
		if ctx.Err() != nil || !IsRetriable(err) {
			return nil, err
		}
	}
	return nil, last
}

func (c *Client) metadataOn(ctx context.Context, bc *brokerConn, topics []string, all bool) (*Metadata, error) {
	v, err := bc.version(apiMetadata)
	if err != nil {
		return nil, err
	}
	var e encoder
	if all {
		e.int32(-1)
	} else {
		e.int32(int32(len(topics)))
		for _, t := range topics {
			e.string(t)
		}
	}
	if v >= 4 {
		e.bool(c.cfg.AllowAutoTopicCreation)
	}
	if v >= 8 {
		e.bool(false)
		e.bool(false)
	}
	resp, err := bc.roundTrip(ctx, apiMetadata, v, e.b, c.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	d := decoder{b: resp}
	if v >= 3 {
		d.int32() // throttle
	}
	m := &Metadata{ControllerID: -1}
	for range d.arrayLen() {
		b := Broker{NodeID: d.int32(), Host: d.string(), Port: d.int32()}
		if r := d.nullableString(); r != nil {
			b.Rack = *r
		}
		m.Brokers = append(m.Brokers, b)
	}
	if v >= 2 {
		if id := d.nullableString(); id != nil {
			m.ClusterID = *id
		}
	}
	m.ControllerID = d.int32()
	for range d.arrayLen() {
		t := TopicMetadata{}
		t.Err = codeErr(d.int16())
		t.Name = d.string()
		t.Internal = d.bool()
		for range d.arrayLen() {
			p := PartitionMetadata{Err: codeErr(d.int16()), Partition: d.int32(), Leader: d.int32(), LeaderEpoch: -1}
			if v >= 7 {
				p.LeaderEpoch = d.int32()
			}
			p.Replicas = int32s(&d)
			p.ISR = int32s(&d)
			if v >= 5 {
				p.OfflineReplicas = int32s(&d)
			}
			t.Partitions = append(t.Partitions, p)
		}
		if v >= 8 {
			d.int32()
		}
		m.Topics = append(m.Topics, t)
	}
	if v >= 8 {
		d.int32()
	}
	if d.err != nil {
		return nil, &ConnError{Addr: bc.addr, Err: fmt.Errorf("Metadata: %w", d.err)}
	}
	sortPartitions(m)
	return m, nil
}

func int32s(d *decoder) []int32 {
	n := d.arrayLen()
	if n == 0 {
		return nil
	}
	s := make([]int32, n)
	for i := range s {
		s[i] = d.int32()
	}
	return s
}

func codeErr(code int16) error {
	if code == 0 {
		return nil
	}
	return Error(code)
}

// merge keeps m for routing: its brokers, and its topics over the ones
// known. A full answer (every topic) replaces them.
func (c *Client) merge(m *Metadata, all bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.brokers = make(map[int32]string, len(m.Brokers))
	for _, b := range m.Brokers {
		c.brokers[b.NodeID] = b.Addr()
	}
	merged := *m
	merged.Topics = append([]TopicMetadata(nil), m.Topics...)
	if c.meta != nil && !all {
		for _, t := range c.meta.Topics {
			if m.Topic(t.Name) == nil {
				merged.Topics = append(merged.Topics, t)
			}
		}
	}
	c.meta = &merged
	c.metaAt = time.Now()
	c.stale = false
}

// invalidate marks the metadata to be read again before the next route.
func (c *Client) invalidate() {
	c.mu.Lock()
	c.stale = true
	c.mu.Unlock()
}

// refresh reads the metadata of every topic the client has asked about,
// with topics among them, unless another call has read it since since.
func (c *Client) refresh(ctx context.Context, topics []string, since time.Time) error {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	c.mu.Lock()
	for _, t := range topics {
		c.topics[t] = struct{}{}
	}
	fresh := c.metaAt.After(since) && !c.stale
	if fresh && c.meta != nil {
		missing := false
		for _, t := range topics {
			if c.meta.Topic(t) == nil {
				missing = true
			}
		}
		if !missing {
			c.mu.Unlock()
			return nil
		}
	}
	names := make([]string, 0, len(c.topics))
	for t := range c.topics {
		names = append(names, t)
	}
	c.mu.Unlock()
	m, err := c.requestMetadata(ctx, names, false)
	if err != nil {
		return err
	}
	c.merge(m, false)
	return nil
}

// leader returns the address of a partition's leader from the metadata,
// reading it again when it is stale, old or lacks the topic.
func (c *Client) leader(ctx context.Context, topic string, partition int32) (string, error) {
	for attempt := 0; ; attempt++ {
		now := time.Now()
		c.mu.Lock()
		var t *TopicMetadata
		ok := c.meta != nil && !c.stale && now.Sub(c.metaAt) < c.cfg.MetadataMaxAge
		if ok {
			t = c.meta.Topic(topic)
		}
		if t != nil && attempt == 0 && t.Err != nil {
			t = nil // an error for the topic: ask once more
		}
		if t != nil {
			defer c.mu.Unlock()
			if t.Err != nil {
				return "", t.Err
			}
			for _, p := range t.Partitions {
				if p.Partition != partition {
					continue
				}
				if p.Err != nil && p.Leader < 0 {
					return "", p.Err
				}
				if p.Leader < 0 {
					return "", ErrLeaderNotAvailable
				}
				addr, ok := c.brokers[p.Leader]
				if !ok {
					return "", ErrLeaderNotAvailable
				}
				return addr, nil
			}
			return "", ErrUnknownTopicOrPartition
		}
		c.mu.Unlock()
		if attempt > 0 {
			return "", ErrLeaderNotAvailable
		}
		if err := c.refresh(ctx, []string{topic}, now); err != nil {
			return "", err
		}
	}
}

// Partitions returns the number of a topic's partitions, retrying while
// the topic is unknown or leaderless (as just after it is made) until ctx
// ends or RetryFor passes.
func (c *Client) Partitions(ctx context.Context, topic string) (int32, error) {
	var n int32
	err := c.retry(ctx, func() error {
		_, err := c.leader(ctx, topic, 0)
		if err != nil {
			return err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		n = int32(len(c.meta.Topic(topic).Partitions))
		return nil
	})
	return n, err
}

// retry calls fn until it succeeds, fails for good, ctx ends or RetryFor
// passes, reading the metadata again before each new attempt.
func (c *Client) retry(ctx context.Context, fn func() error) error {
	start := time.Now()
	backoff := c.cfg.RetryBackoff
	for {
		err := fn()
		if err == nil || !IsRetriable(err) || ctx.Err() != nil || time.Since(start)+backoff > c.cfg.RetryFor {
			return err
		}
		c.invalidate()
		if !sleep(ctx, backoff) {
			return err
		}
		backoff = min(backoff*2, time.Second)
	}
}

// sleep waits d, or less when ctx ends first, which it reports false.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// part is a partition a routed call works on.
type part struct {
	topic     string
	partition int32
}

// routed runs send for parts, grouped by their leaders, the leaders in
// parallel, and sends again the parts that fail with a retriable error
// (reading the metadata again when the error says the leader moved),
// until they succeed, ctx ends or RetryFor passes. send gets the indexes
// of the parts it carries and returns an error for each (nil when done).
// The errors returned are the parts' last.
func (c *Client) routed(ctx context.Context, fetch bool, parts []part, send func(ctx context.Context, bc *brokerConn, idx []int) []error) []error {
	errs := make([]error, len(parts))
	todo := make([]int, len(parts))
	for i := range todo {
		todo[i] = i
	}
	start := time.Now()
	backoff := c.cfg.RetryBackoff
	for {
		byAddr := map[string][]int{}
		for _, i := range todo {
			addr, err := c.leader(ctx, parts[i].topic, parts[i].partition)
			if err != nil {
				errs[i] = err
				continue
			}
			byAddr[addr] = append(byAddr[addr], i)
		}
		var wg sync.WaitGroup
		for addr, idx := range byAddr {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bc, err := c.conn(ctx, addr, fetch)
				var res []error
				if err == nil {
					res = send(ctx, bc, idx)
				} else {
					res = make([]error, len(idx))
					for j := range res {
						res[j] = err
					}
				}
				for j, i := range idx {
					errs[i] = res[j]
				}
			}()
		}
		wg.Wait()

		var again []int
		stale := false
		for _, i := range todo {
			err := errs[i]
			if err == nil || !IsRetriable(err) {
				continue
			}
			again = append(again, i)
			var e Error
			if !errors.As(err, &e) || e.staleMetadata() {
				stale = true
			}
		}
		if len(again) == 0 || ctx.Err() != nil || time.Since(start)+backoff > c.cfg.RetryFor {
			return errs
		}
		if stale {
			c.invalidate()
		}
		if !sleep(ctx, backoff) {
			return errs
		}
		backoff = min(backoff*2, time.Second)
		todo = again
	}
}

// partitionErr checks that a response's topic and partition are among
// those asked, by index.
func indexOf(parts []part, idx []int) map[part]int {
	m := make(map[part]int, len(idx))
	for _, i := range idx {
		m[parts[i]] = i
	}
	return m
}

// fill returns n copies of err.
func fill(n int, err error) []error {
	s := make([]error, n)
	for i := range s {
		s[i] = err
	}
	return s
}
