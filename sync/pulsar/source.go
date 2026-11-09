package syncpulsar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	pulsarwire "github.com/lesismal/fit/sync/pulsar/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".pulsar", New: func() any { return new(Source) },
		Doc: "Reads Apache Pulsar topics, every partition of each. As a reader (mode reader), from the message IDs in the pipeline's checkpoint, with nothing kept on the broker; or through a durable subscription (mode subscription), acknowledged cumulatively once every sink has the records, the message IDs still in the checkpoint so that nothing read before is emitted again. Each message goes through a codec (raw when none is named) into records, whose key is the message's key unless the codec sets one, and whose namespace is the topic unless the codec or namespace sets one; a message record also holds the message's topic, ID, properties, producer and times. A message with a null value is a delete of its key. In a job (mode once) it reads up to each partition's last message as it was when the job started."})
}

// The defaults of the source's params.
const (
	DefaultReceiverQueue = 1000
	// partitionsEvery is how often the source looks for partitions added
	// to its topics.
	partitionsEvery = time.Minute
	// takeAtOnce is how many messages are taken of a partition before the
	// next partition's turn.
	takeAtOnce = 256
)

// Source is the source pulsar.
type Source struct {
	URL              string            `json:"url" fit:"required" doc:"The service URL: pulsar://host:6650, or pulsar+ssl://host:6651 for TLS, the broker's certificate checked against the system's roots or ca_file's (the check cannot be turned off); several hosts separated by commas are tried in order. Topics are looked up from it, and their brokers connected to, or a proxy, as the lookup says."`
	CAFile           string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the brokers' are checked against, in place of the system's roots; needs a pulsar+ssl:// url."`
	CertFile         string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, for brokers that ask for one; needs key_file."`
	KeyFile          string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName       string            `json:"server_name,omitempty" doc:"The name the brokers' certificates must have; each broker's host when left out."`
	TokenEnv         string            `json:"token_env,omitempty" doc:"The environment variable holding a token (a JWT) to authenticate with, read when the pipeline loads; no authentication when left out."`
	Timeout          config.Duration   `json:"timeout,omitempty" default:"\"30s\"" doc:"How long connecting and each answer may take. 30s when left out or 0."`
	Topics           []string          `json:"topics" fit:"required" doc:"The topics read: a name (public/default's), tenant/namespace/name or persistent://tenant/namespace/name; every partition of a partitioned topic, partitions added found within a minute and read from their start."`
	Mode             string            `json:"mode,omitempty" enum:"reader,subscription" default:"\"reader\"" doc:"How the topics are read: reader, a non-durable reader of each partition from the checkpoint's message IDs, which needs nothing kept on the broker (messages the topic's retention removed before they are read are lost); subscription, the durable subscription named by subscription, which keeps the messages until it acknowledges them, cumulatively, once every sink has them. reader when left out."`
	Subscription     string            `json:"subscription,omitempty" doc:"The durable subscription's name, for mode subscription."`
	SubscriptionType string            `json:"subscription_type,omitempty" enum:"exclusive,failover" default:"\"exclusive\"" doc:"The subscription's type: exclusive, one consumer, a second pipeline refused; failover, one active consumer of each partition and others standing by. exclusive when left out."`
	Start            string            `json:"start,omitempty" enum:"earliest,latest" default:"\"earliest\"" doc:"Where a partition starts when the pipeline has no checkpoint (and, in mode subscription, the subscription is new): earliest, the oldest message kept; latest, after the newest. A partition found later starts at its earliest. earliest when left out."`
	Codec            *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes records of a message's value: raw when left out, a message record holding it as after's value."`
	Namespace        string            `json:"namespace,omitempty" doc:"The records' namespace. The codec's (canal_json's db.table, say), or the topic as written in topics, when left out."`
	MetadataField    string            `json:"metadata_field,omitempty" doc:"The field of after that holds the message's metadata (topic, message_id, properties, producer, publish_time, event_time) in the records a codec makes of rows. A message record has them as fields of its own, beside value; a row has them only when this is set."`
	Tombstones       string            `json:"tombstones,omitempty" enum:"delete,skip" default:"\"delete\"" doc:"What a message with a null value is: delete, a delete record of the message's key, as a compacted topic means it; skip, nothing. delete when left out."`
	ReceiverQueue    int               `json:"receiver_queue,omitempty" min:"0" max:"100000" default:"1000" doc:"How many messages of each partition the broker may send ahead of what the source has taken. 1000 when left out or 0."`

	cfg    pulsarwire.Config
	dial   dialer
	codec  fitsync.Codec
	cl     holder
	topics []string // in full

	ackMu   sync.Mutex
	ackPos  fitsync.Position
	ackWake chan struct{}
}

// msgID is a partition's place in the position: the last message emitted,
// its batch index -1 when its entry is done.
type msgID struct {
	Ledger int64 `json:"ledger"`
	Entry  int64 `json:"entry"`
	Batch  int32 `json:"batch"`
}

func (m msgID) wire() pulsarwire.MessageID {
	return pulsarwire.MessageID{Ledger: m.Ledger, Entry: m.Entry, Partition: -1, BatchIndex: m.Batch}
}

// position is the source's: the last message emitted of each partition, by
// the partition's full name.
type position struct {
	IDs map[string]msgID `json:"ids"`
}

// Provision checks the params and reads the token and TLS files; nothing
// is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{URL: s.URL, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile,
		ServerName: s.ServerName, TokenEnv: s.TokenEnv, Timeout: s.Timeout})
	s.cfg = cfg
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if len(s.Topics) == 0 {
		add("topics", "no topics")
	}
	seen := map[string]bool{}
	s.topics = nil
	for _, t := range s.Topics {
		full, err := fullTopic(t)
		if err != nil || seen[full] {
			add("topics", "%q is not a topic's name, or named twice", t)
		}
		seen[full] = true
		s.topics = append(s.topics, full)
	}
	if s.Mode == "subscription" && s.Subscription == "" {
		add("subscription", "mode subscription needs subscription")
	}
	if s.Mode != "subscription" && (s.Subscription != "" || s.SubscriptionType != "") {
		add("subscription", "subscription and subscription_type need mode subscription")
	}
	if len(errs) > 0 {
		return errs
	}
	s.ackWake = make(chan struct{}, 1)
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// Cleanup closes the client and lets the codec go.
func (s *Source) Cleanup() {
	s.cl.close()
	module.Cleanup(s.codec)
}

// Test connects and reads each topic's partitions.
func (s *Source) Test(ctx context.Context) error {
	return testTopics(ctx, s.cfg, s.dial, s.topics)
}

// testTopics reads the partitions of topics on a client of its own.
func testTopics(ctx context.Context, cfg pulsarwire.Config, dial dialer, topics []string) error {
	h := holder{cfg: cfg, dial: dial}
	c, err := h.get()
	if err != nil {
		return err
	}
	defer h.close()
	if len(topics) == 0 {
		// The connection alone: a lookup of nothing would fail.
		_, err = c.PartitionedMetadata(ctx, "persistent://public/default/fit-sync-test")
		return err
	}
	for _, t := range topics {
		if _, err := c.PartitionedMetadata(ctx, t); err != nil {
			return fmt.Errorf("pulsar: topic %s: %w", t, err)
		}
	}
	return nil
}

// Acknowledged hands the position every sink has to the run, which
// acknowledges it in mode subscription.
func (s *Source) Acknowledged(p fitsync.Position) {
	if s.Mode != "subscription" {
		return
	}
	s.ackMu.Lock()
	s.ackPos = p
	s.ackMu.Unlock()
	select {
	case s.ackWake <- struct{}{}:
	default:
	}
}

// part is a partition the run reads.
type part struct {
	topic string // as written in topics
	name  string // the partition's full name
	cs    consumer
	// last is the last message emitted, nil before one.
	last *msgID
	// end is the last message a job reads, nil in a stream.
	end  *pulsarwire.MessageID
	done bool
	// acked is the last ID acknowledged, in mode subscription.
	acked *pulsarwire.MessageID
}

// reader is a run of the source.
type reader struct {
	s      *Source
	c      client
	run    *fitsync.Run
	parts  []*part
	counts map[string]int // partitions known of each topic, -1 none yet
	notify chan struct{}
	from   map[string]msgID
}

// Run reads the topics until ctx ends, or, in a job, until every partition
// reaches the last message it had.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	s.cl.cfg, s.cl.dial = s.cfg, s.dial
	c, err := s.cl.get()
	if err != nil {
		return err
	}
	var from position
	if r.From != nil {
		if err := json.Unmarshal(r.From, &from); err != nil {
			return fmt.Errorf("pulsar: the checkpoint %s: %w", r.From, err)
		}
	}
	rd := &reader{s: s, c: c, run: r, counts: map[string]int{}, notify: make(chan struct{}, 1), from: from.IDs}
	defer rd.close()
	if err := rd.addPartitions(ctx, r.From == nil); err != nil {
		return err
	}
	tick := time.NewTicker(partitionsCheck)
	defer tick.Stop()
	for {
		if err := rd.ack(); err != nil {
			return err
		}
		took, err := rd.take(ctx)
		if err != nil {
			return err
		}
		if r.Once && rd.allDone() {
			return nil
		}
		if took {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-rd.notify:
		case <-s.ackWake:
		case <-tick.C:
			if !r.Once {
				if err := rd.addPartitions(ctx, false); err != nil {
					return err
				}
			}
		}
	}
}

func (rd *reader) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, p := range rd.parts {
		p.cs.Close(ctx)
	}
}

func (rd *reader) allDone() bool {
	for _, p := range rd.parts {
		if !p.done {
			return false
		}
	}
	return true
}

// addPartitions subscribes to the partitions of the topics the reader does
// not read yet: from the checkpoint's IDs, or at start when fresh (the
// pipeline has no checkpoint), or at their earliest.
func (rd *reader) addPartitions(ctx context.Context, fresh bool) error {
	for i, t := range rd.s.topics {
		n, err := rd.c.PartitionedMetadata(ctx, t)
		if err != nil {
			return fmt.Errorf("pulsar: the partitions of %s: %w", t, err)
		}
		known, ok := rd.counts[t]
		if !ok {
			known = -1
		}
		if n == 0 && known >= 0 {
			continue
		}
		for j := max(known, 0); j < max(n, 1); j++ {
			if err := rd.subscribe(ctx, rd.s.Topics[i], partitionName(t, n, j), fresh); err != nil {
				return err
			}
		}
		rd.counts[t] = max(n, 1)
	}
	return nil
}

// readerName is a reader's subscription's name, its own each time.
func readerName(prefix string) string {
	var b [8]byte
	rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func (rd *reader) subscribe(ctx context.Context, topic, name string, fresh bool) error {
	s := rd.s
	p := &part{topic: topic, name: name}
	if id, ok := rd.from[name]; ok {
		p.last = &id
	}
	start := pulsarwire.EarliestID
	initial := pulsarwire.Earliest
	if fresh && s.Start == "latest" {
		start, initial = pulsarwire.LatestID, pulsarwire.Latest
	}
	if p.last != nil {
		start = p.last.wire()
	}
	o := pulsarwire.SubscribeOptions{Subscription: readerName("fit-reader-"), Start: &start, ReceiverQueue: s.ReceiverQueue, Notify: rd.notify}
	if s.Mode == "subscription" {
		o = pulsarwire.SubscribeOptions{Subscription: s.Subscription, Durable: true, Initial: initial, ReceiverQueue: s.ReceiverQueue, Notify: rd.notify}
		if s.SubscriptionType == "failover" {
			o.Type = pulsarwire.Failover
		}
	}
	cs, err := rd.c.Subscribe(ctx, name, o)
	if err != nil {
		return fmt.Errorf("pulsar: subscribe to %s: %w", name, err)
	}
	p.cs = cs
	rd.parts = append(rd.parts, p)
	if s.Mode == "subscription" && p.last != nil {
		// What the checkpoint has is acknowledged, in case the
		// acknowledgement was lost.
		if err := rd.ackPart(p, *p.last); err != nil {
			return err
		}
	}
	if rd.run.Once {
		end, err := cs.LastMessageID(ctx)
		if err != nil {
			return fmt.Errorf("pulsar: the last message of %s: %w", name, err)
		}
		// The end is the last entry, whole: the broker may name the last
		// message of its batch.
		end = pulsarwire.MessageID{Ledger: end.Ledger, Entry: end.Entry, Partition: -1, BatchIndex: -1}
		p.end = &end
		p.done = end.Entry < 0 || p.last != nil && p.last.wire().Compare(end) >= 0
	}
	return nil
}

// take emits the messages received, a partition's at a time, and reports
// whether there were any.
func (rd *reader) take(ctx context.Context) (bool, error) {
	took := false
	for _, p := range rd.parts {
		for n := 0; n < takeAtOnce && !p.done; n++ {
			m, ok, err := p.cs.TryReceive()
			if err != nil {
				return took, fmt.Errorf("pulsar: %s: %w", p.name, err)
			}
			if !ok {
				break
			}
			took = true
			if err := rd.message(p, m); err != nil {
				return took, err
			}
		}
		if err := ctx.Err(); err != nil {
			return took, err
		}
	}
	return took, nil
}

// message emits m's records, unless the checkpoint has it.
func (rd *reader) message(p *part, m pulsarwire.Message) error {
	id := msgID{Ledger: m.ID.Ledger, Entry: m.ID.Entry, Batch: m.ID.BatchIndex}
	if m.ID.BatchSize > 0 && m.ID.BatchIndex == m.ID.BatchSize-1 {
		id.Batch = -1
	}
	if p.last != nil && m.ID.Compare(p.last.wire()) <= 0 {
		return nil
	}
	if p.end != nil {
		entry := pulsarwire.MessageID{Ledger: m.ID.Ledger, Entry: m.ID.Entry, BatchIndex: -1}
		switch c := entry.Compare(*p.end); {
		case c > 0:
			p.done = true
			return nil
		case c == 0 && id.Batch == -1:
			p.done = true
		}
	}
	p.last = &id
	recs := rd.decode(p, m)
	if len(recs) == 0 {
		return nil
	}
	recs[len(recs)-1].Position = rd.position()
	for _, rec := range recs {
		if err := rd.run.Emit(rec); err != nil {
			return err
		}
	}
	return nil
}

// decode makes a message's records.
func (rd *reader) decode(p *part, m pulsarwire.Message) []*fitsync.Record {
	s := rd.s
	var recs []*fitsync.Record
	if m.Payload == nil {
		if s.Tombstones == "skip" || m.Key == nil {
			return nil
		}
		recs = append(recs, &fitsync.Record{Op: fitsync.OpDelete})
	} else if err := s.codec.Decode(m.Payload, func(r *fitsync.Record) { recs = append(recs, r) }); err != nil {
		recs = append(recs[:0], &fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(
			fitsync.Field{Name: "value", Value: fitsync.Bytes(m.Payload)},
			fitsync.Field{Name: "error", Value: fitsync.String(err.Error())})})
	}
	at := m.EventTime
	if at.IsZero() {
		at = m.PublishTime
	}
	var meta []fitsync.Field
	for i, r := range recs {
		r.Source, r.Position = "pulsar", nil
		if s.Namespace != "" {
			r.Namespace = s.Namespace
		} else if r.Namespace == "" {
			r.Namespace = p.topic
		}
		if r.Key.IsNull() && m.Key != nil {
			r.Key = text(m.Key)
		}
		if r.CommitTime.IsZero() {
			r.CommitTime = at
		}
		if r.After.Kind() == fitsync.KindMap && (r.Op == fitsync.OpMessage || s.MetadataField != "") {
			if meta == nil {
				meta = metadataFields(p, m)
			}
			fs := append([]fitsync.Field(nil), r.After.Fields()...)
			if r.Op == fitsync.OpMessage {
				fs = append(fs, meta...)
			} else {
				fs = append(fs, fitsync.Field{Name: s.MetadataField, Value: fitsync.Map(meta...)})
			}
			r.After = fitsync.Map(fs...)
		}
		r.Tx = fitsync.Tx{}
		if len(recs) > 1 {
			r.Tx = fitsync.Tx{ID: p.name + "@" + m.ID.String(), First: i == 0, Last: i == len(recs)-1}
		}
	}
	return recs
}

// metadataFields are a message's metadata, as records hold it.
func metadataFields(p *part, m pulsarwire.Message) []fitsync.Field {
	props := make([]fitsync.Field, len(m.Properties))
	for i, kv := range m.Properties {
		props[i] = fitsync.Field{Name: kv.Key, Value: fitsync.String(kv.Value)}
	}
	fs := []fitsync.Field{
		{Name: "topic", Value: fitsync.String(p.name)},
		{Name: "message_id", Value: fitsync.String(m.ID.String())},
		{Name: "properties", Value: fitsync.Map(props...)},
		{Name: "producer", Value: fitsync.String(m.ProducerName)},
		{Name: "publish_time", Value: fitsync.Time(m.PublishTime)},
	}
	if !m.EventTime.IsZero() {
		fs = append(fs, fitsync.Field{Name: "event_time", Value: fitsync.Time(m.EventTime)})
	}
	return fs
}

// position writes every partition's last message emitted.
func (rd *reader) position() fitsync.Position {
	p := position{IDs: make(map[string]msgID, len(rd.parts))}
	for _, ps := range rd.parts {
		if ps.last != nil {
			p.IDs[ps.name] = *ps.last
		}
	}
	b, _ := json.Marshal(p) // names and numbers
	return b
}

// ack acknowledges, in mode subscription, what Acknowledged last handed
// over.
func (rd *reader) ack() error {
	s := rd.s
	s.ackMu.Lock()
	pos := s.ackPos
	s.ackPos = nil
	s.ackMu.Unlock()
	if pos == nil {
		return nil
	}
	var at position
	if json.Unmarshal(pos, &at) != nil {
		return nil
	}
	var errs []error
	for _, p := range rd.parts {
		if id, ok := at.IDs[p.name]; ok {
			errs = append(errs, rd.ackPart(p, id))
		}
	}
	return errors.Join(errs...)
}

// ackPart acknowledges p up to id: its entry when id ends it, or the entry
// before, for a batch read in part (the rest of it comes again after a
// restart, and is passed over by its ID).
func (rd *reader) ackPart(p *part, id msgID) error {
	to := pulsarwire.MessageID{Ledger: id.Ledger, Entry: id.Entry, Partition: -1, BatchIndex: -1}
	if id.Batch >= 0 {
		if id.Entry == 0 {
			return nil
		}
		to.Entry--
	}
	if p.acked != nil && to.Compare(*p.acked) <= 0 {
		return nil
	}
	if err := p.cs.AckCumulative(to); err != nil {
		return fmt.Errorf("pulsar: acknowledge %s at %v: %w", p.name, to, err)
	}
	p.acked = &to
	return nil
}

// partitionsCheck is partitionsEvery, shortened by tests.
var partitionsCheck = partitionsEvery
