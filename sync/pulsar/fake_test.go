package syncpulsar

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	pulsarwire "github.com/lesismal/fit/sync/pulsar/wire"
)

// fake is an in-memory Pulsar: topics of partitions, each a log of entries
// of messages.
type fake struct {
	mu         sync.Mutex
	partitions map[string]int                  // by the topic's full name; 0 not partitioned
	logs       map[string][]pulsarwire.Message // by the partition's name
	entries    map[string]int
	acked      map[string]int64 // durable subscriptions' entries, by partition and name
	acks       []pulsarwire.MessageID
	consumers  []*fakeConsumer
	closed     int

	dialErr, metaErr, producerErr, subErr, lastErr, recvErr, ackErr error
	// sendErr gives a send's error, at once (now) or as its result.
	sendErr func(part string, now bool) error
	// subscribed counts subscriptions; onSubscribe runs before each.
	onSubscribe func(topic string, o pulsarwire.SubscribeOptions) error
	// onLast runs after each LastMessageID has read its partition's end.
	onLast   func(part string)
	produced [][]pulsarwire.ProducerMessage
}

func newFake() *fake {
	return &fake{partitions: map[string]int{}, logs: map[string][]pulsarwire.Message{}, entries: map[string]int{}, acked: map[string]int64{}}
}

func (f *fake) dial(pulsarwire.Config) (client, error) {
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	return f, nil
}

func (f *fake) PartitionedMetadata(_ context.Context, topic string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.metaErr != nil {
		return 0, f.metaErr
	}
	return f.partitions[topic], nil
}

func (f *fake) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return nil
}

func (f *fake) CreateProducer(_ context.Context, topic, name string, _ pulsarwire.Compression) (producer, error) {
	if f.producerErr != nil {
		return nil, f.producerErr
	}
	return &fakeProducer{f: f, part: topic, name: name}, nil
}

// add appends an entry of msgs to part, as name sent them.
func (f *fake) add(part, name string, msgs ...pulsarwire.ProducerMessage) pulsarwire.MessageID {
	f.mu.Lock()
	e := int64(f.entries[part])
	f.entries[part]++
	for i, m := range msgs {
		id := pulsarwire.MessageID{Ledger: 1, Entry: e, Partition: -1, BatchIndex: int32(i), BatchSize: int32(len(msgs))}
		if len(msgs) == 1 {
			id.BatchIndex, id.BatchSize = -1, 0
		}
		pm := pulsarwire.Message{ID: id, Key: m.Key, Payload: m.Payload, Properties: m.Properties, ProducerName: name,
			EventTime: m.EventTime, PublishTime: time.UnixMilli(1_700_000_000_000)}
		if m.Null {
			pm.Payload = nil
		} else if pm.Payload == nil {
			pm.Payload = []byte{}
		}
		f.logs[part] = append(f.logs[part], pm)
	}
	f.produced = append(f.produced, msgs)
	consumers := append([]*fakeConsumer(nil), f.consumers...)
	f.mu.Unlock()
	for _, c := range consumers {
		if c.o.Notify != nil {
			select {
			case c.o.Notify <- struct{}{}:
			default:
			}
		}
	}
	return pulsarwire.MessageID{Ledger: 1, Entry: e, Partition: -1, BatchIndex: -1}
}

func (f *fake) count(part string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.logs[part])
}

type fakeProducer struct {
	f      *fake
	part   string
	name   string
	closed bool
}

func (p *fakeProducer) Name() string        { return p.name }
func (p *fakeProducer) MaxMessageSize() int { return 5 << 20 }

func (p *fakeProducer) Send(msgs []pulsarwire.ProducerMessage) (<-chan pulsarwire.SendResult, error) {
	if p.closed {
		return nil, pulsarwire.ErrClosed
	}
	if p.f.sendErr != nil {
		if err := p.f.sendErr(p.part, true); err != nil {
			return nil, err
		}
	}
	ch := make(chan pulsarwire.SendResult, 1)
	if p.f.sendErr != nil {
		if err := p.f.sendErr(p.part, false); err != nil {
			ch <- pulsarwire.SendResult{Err: err}
			return ch, nil
		}
	}
	ch <- pulsarwire.SendResult{ID: p.f.add(p.part, p.name, msgs...)}
	return ch, nil
}

func (p *fakeProducer) Close(context.Context) error {
	p.closed = true
	return nil
}

func (f *fake) Subscribe(_ context.Context, topic string, o pulsarwire.SubscribeOptions) (consumer, error) {
	if f.subErr != nil {
		return nil, f.subErr
	}
	if f.onSubscribe != nil {
		if err := f.onSubscribe(topic, o); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &fakeConsumer{f: f, part: topic, o: o}
	log := f.logs[topic]
	switch {
	case o.Durable:
		if e, ok := f.acked[topic+"|"+o.Subscription]; ok {
			c.next = firstAfter(log, e)
		} else if o.Initial == pulsarwire.Latest {
			c.next = len(log)
		}
	case o.Start != nil && o.Start.Ledger < 0:
	case o.Start != nil && o.Start.Ledger == pulsarwire.LatestID.Ledger:
		c.next = len(log)
	case o.Start != nil && o.Start.BatchIndex >= 0:
		c.next = firstAfter(log, o.Start.Entry-1)
	case o.Start != nil:
		c.next = firstAfter(log, o.Start.Entry)
	}
	f.consumers = append(f.consumers, c)
	return c, nil
}

// firstAfter is the index of the first message of an entry after e.
func firstAfter(log []pulsarwire.Message, e int64) int {
	for i, m := range log {
		if m.ID.Entry > e {
			return i
		}
	}
	return len(log)
}

type fakeConsumer struct {
	f      *fake
	part   string
	o      pulsarwire.SubscribeOptions
	next   int
	closed bool
}

func (c *fakeConsumer) TryReceive() (pulsarwire.Message, bool, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.f.recvErr != nil {
		return pulsarwire.Message{}, false, c.f.recvErr
	}
	if c.next >= len(c.f.logs[c.part]) {
		return pulsarwire.Message{}, false, nil
	}
	m := c.f.logs[c.part][c.next]
	c.next++
	return m, true, nil
}

func (c *fakeConsumer) Receive(ctx context.Context) (pulsarwire.Message, error) {
	for {
		m, ok, err := c.TryReceive()
		if ok || err != nil {
			return m, err
		}
		select {
		case <-ctx.Done():
			return m, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func (c *fakeConsumer) AckCumulative(id pulsarwire.MessageID) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.f.ackErr != nil {
		return c.f.ackErr
	}
	c.f.acks = append(c.f.acks, id)
	c.f.acked[c.part+"|"+c.o.Subscription] = id.Entry
	return nil
}

func (c *fakeConsumer) LastMessageID(context.Context) (pulsarwire.MessageID, error) {
	id, err := c.lastMessageID()
	if c.f.onLast != nil && err == nil {
		c.f.onLast(c.part)
	}
	return id, err
}

func (c *fakeConsumer) lastMessageID() (pulsarwire.MessageID, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.f.lastErr != nil {
		return pulsarwire.MessageID{}, c.f.lastErr
	}
	log := c.f.logs[c.part]
	if len(log) == 0 {
		return pulsarwire.MessageID{Ledger: 1, Entry: -1, Partition: -1, BatchIndex: -1}, nil
	}
	// As a broker does, the last message of a batch, by its index.
	return log[len(log)-1].ID, nil
}

func (c *fakeConsumer) Close(context.Context) error {
	c.closed = true
	return nil
}

func load(t *testing.T, ns, name string, params map[string]any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(ns, config.ModuleRef{Module: name, Params: v}, nil)
	if err == nil {
		t.Cleanup(func() { module.Cleanup(m) })
	}
	return m, err
}

func mustLoad(t *testing.T, ns, name string, params map[string]any) any {
	t.Helper()
	m, err := load(t, ns, name, params)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func source(t *testing.T, f *fake, params map[string]any) *Source {
	t.Helper()
	if params["url"] == nil {
		params["url"] = "pulsar://broker:6650"
	}
	s := mustLoad(t, fitsync.Sources, "pulsar", params).(*Source)
	s.dial = f.dial
	return s
}

func sink(t *testing.T, f *fake, params map[string]any) *Sink {
	t.Helper()
	if params["url"] == nil {
		params["url"] = "pulsar://broker:6650"
	}
	s := mustLoad(t, fitsync.Sinks, "pulsar", params).(*Sink)
	s.dial = f.dial
	return s
}

// collect runs a source until it ends, or until it has n records and then
// is stopped; it returns the records.
func collect(t *testing.T, s fitsync.Source, from fitsync.Position, once bool, n int) ([]*fitsync.Record, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // a hang guard
	defer cancel()
	var out []*fitsync.Record
	err := s.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(r *fitsync.Record) error {
		out = append(out, r)
		if n > 0 && len(out) >= n {
			cancel()
		}
		return nil
	}})
	if errors.Is(err, context.Canceled) && n > 0 && len(out) >= n {
		err = nil
	}
	return out, err
}

// lastPosition is the position of the last record that has one.
func lastPosition(recs []*fitsync.Record) fitsync.Position {
	var p fitsync.Position
	for _, r := range recs {
		if r.Position != nil {
			p = r.Position
		}
	}
	return p
}
