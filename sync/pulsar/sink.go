package syncpulsar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	pulsarwire "github.com/lesismal/fit/sync/pulsar/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".pulsar", New: func() any { return new(Sink) },
		Doc: "Sends records to Apache Pulsar: each a message of the topic its namespace names (or topic's template), its key the record's key (a string as it is, bytes in base64, any other key as plain JSON), which the Java client's default router (the key's Java string hash) places in a partition, so a key's messages stay in order; its value what the codec makes of it (raw when none is named), and a delete a message with a null value. A batch's messages for a partition go out as Pulsar batches, compressed (lz4), from a producer of producer_name, whose sequence IDs carry on from the broker's, for its deduplication. Each message carries the property fit-id, made from the record's origin (a Kafka offset, a Pulsar message ID, a CDC transaction and place) or, without one, its position; with dedup read_back the sink reads back the last messages it stored of each partition when it makes its producer, and drops the records of a batch written again that are stored already, so a batch sent twice after a crash is stored once: a record with an origin keeps its fit-id however a replay interleaves partitions, one without one when the source reads it again at the same position (a database, a file, one partition)."})
}

// The defaults of the sink's params.
const (
	DefaultMaxBatchBytes    = 1 << 20
	DefaultMaxBatchMessages = 1000
	DefaultProducerName     = "fit-sync"
	defaultTopic            = "{namespace}"
	// idProperty is the property holding a message's ID.
	idProperty = "fit-id"
	// partitionsTTL is how long the sink trusts a topic's partition count.
	partitionsTTL = time.Minute
	// storedWindow is how many IDs of the messages stored the sink keeps
	// of each partition, to drop them when they come again.
	storedWindow = 100000
	// readBackEntries is how many of a partition's last entries the sink
	// reads back.
	readBackEntries = 64
)

// Sink is the sink pulsar.
type Sink struct {
	URL              string            `json:"url" fit:"required" doc:"The service URL: pulsar://host:6650, or pulsar+ssl://host:6651 for TLS, the broker's certificate checked against the system's roots or ca_file's (the check cannot be turned off); several hosts separated by commas are tried in order."`
	CAFile           string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the brokers' are checked against, in place of the system's roots; needs a pulsar+ssl:// url."`
	CertFile         string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, for brokers that ask for one; needs key_file."`
	KeyFile          string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName       string            `json:"server_name,omitempty" doc:"The name the brokers' certificates must have; each broker's host when left out."`
	TokenEnv         string            `json:"token_env,omitempty" doc:"The environment variable holding a token (a JWT) to authenticate with, read when the pipeline loads; no authentication when left out."`
	Timeout          config.Duration   `json:"timeout,omitempty" default:"\"30s\"" doc:"How long connecting and each answer, the broker's receipt of a batch among them, may take. 30s when left out or 0."`
	Topic            string            `json:"topic,omitempty" default:"\"{namespace}\"" doc:"The topic a record goes to: text with {namespace}, the record's namespace (a route transform can map namespaces to topics first), a name (public/default's), tenant/namespace/name or persistent://tenant/namespace/name. A record whose topic is not a name Pulsar takes fails. The topic is not made by the sink, unless the brokers make topics when first used. {namespace} when left out."`
	Codec            *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes a message's value of a record: raw when left out, after's value field as it is, or the record's canonical JSON when it has none; json for the row as plain JSON (form plain) or the whole record (form record)."`
	Deletes          string            `json:"deletes,omitempty" enum:"tombstone,encode" default:"\"tombstone\"" doc:"What a delete with a key is: tombstone, a message of the key with a null value, which topic compaction takes as the key gone and the source pulsar reads back as a delete; encode, a message of what the codec makes of it. tombstone when left out."`
	ProducerName     string            `json:"producer_name,omitempty" default:"\"fit-sync\"" doc:"The producers' name, the same for every partition: the brokers deduplicate by it, so it must stay the same across restarts, and differ between two sinks writing one topic (the broker refuses a second producer of a name). fit-sync when left out."`
	Compression      string            `json:"compression,omitempty" enum:"none,lz4,zlib,zstd,snappy" default:"\"lz4\"" doc:"How batches are compressed. lz4 when left out."`
	MaxBatchBytes    int               `json:"max_batch_bytes,omitempty" min:"0" default:"1048576" doc:"The most bytes, keys and values before compression, of one Pulsar batch; a sink batch with more for a partition sends several, in order. Keep it below the brokers' maxMessageSize (5 MiB by default). 1048576 when left out or 0."`
	MaxBatchMessages int               `json:"max_batch_messages,omitempty" min:"0" default:"1000" doc:"The most messages of one Pulsar batch. 1000 when left out or 0."`
	Dedup            string            `json:"dedup,omitempty" enum:"read_back,off" default:"\"read_back\"" doc:"How a batch written again is kept from being stored twice: read_back, the last 64 entries (Pulsar batches) of each partition read back when the producer is made (which needs the permission to consume the topic), and the records of a batch whose fit-id it, or a message stored since, has dropped; off, nothing, every record sent, so a batch written again after a crash is stored again. read_back when left out."`

	cfg         pulsarwire.Config
	dial        dialer
	codec       fitsync.Codec
	cl          holder
	compression pulsarwire.Compression
	plain       fitsync.JSONCodec
	parts       map[string]topicParts
	producers   map[string]*sinkProducer
	sticky      int
	maxBytes    int
	maxMsgs     int
}

// topicParts is a topic's partition count, and when it was read.
type topicParts struct {
	n  int
	at time.Time
}

// sinkProducer is a partition's producer, and the IDs of what it stored.
type sinkProducer struct {
	p      producer
	stored map[string]bool
	order  []string
}

func (sp *sinkProducer) store(id string) {
	if sp.stored[id] {
		return
	}
	sp.stored[id] = true
	sp.order = append(sp.order, id)
	if len(sp.order) > storedWindow {
		delete(sp.stored, sp.order[0])
		sp.order = sp.order[1:]
	}
}

// Provision checks the params and reads the token and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{URL: s.URL, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile,
		ServerName: s.ServerName, TokenEnv: s.TokenEnv, Timeout: s.Timeout})
	s.cfg = cfg
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if s.Topic != "" {
		if rest := strings.ReplaceAll(s.Topic, defaultTopic, ""); strings.ContainsAny(rest, "{}") {
			add("topic", "%q is text with something other than {namespace}", s.Topic)
		} else if rest == s.Topic {
			if _, err := fullTopic(s.Topic); err != nil {
				add("topic", "%v", err)
			}
		}
	}
	var err error
	if s.compression, err = pulsarwire.ParseCompression(s.Compression); err != nil {
		add("compression", "%v", err)
	}
	if s.Compression == "" {
		s.compression = pulsarwire.LZ4
	}
	if s.ProducerName != "" && !validName(s.ProducerName, false) {
		add("producer_name", "%q has a character other than letters, digits and -_.=:", s.ProducerName)
	}
	s.maxBytes, s.maxMsgs = DefaultMaxBatchBytes, DefaultMaxBatchMessages
	if s.MaxBatchBytes > 0 {
		s.maxBytes = s.MaxBatchBytes
	}
	if s.MaxBatchMessages > 0 {
		s.maxMsgs = s.MaxBatchMessages
	}
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// Cleanup closes the producers and the client, and lets the codec go.
func (s *Sink) Cleanup() {
	s.dropProducers()
	s.cl.close()
	module.Cleanup(s.codec)
}

// Test connects, and reads the topic's partitions when it is fixed.
func (s *Sink) Test(ctx context.Context) error {
	var topics []string
	if s.Topic != "" && !strings.Contains(s.Topic, defaultTopic) {
		t, _ := fullTopic(s.Topic) // checked by Provision
		topics = []string{t}
	}
	return testTopics(ctx, s.cfg, s.dial, topics)
}

func (s *Sink) producerName() string {
	if s.ProducerName == "" {
		return DefaultProducerName
	}
	return s.ProducerName
}

// keyOf makes a record's key a message's: strings and bytes as they are,
// null as none, anything else as plain JSON.
func (s *Sink) keyOf(r *fitsync.Record) ([]byte, error) {
	switch r.Key.Kind() {
	case fitsync.KindNull:
		return nil, nil
	case fitsync.KindString, fitsync.KindBytes, fitsync.KindDecimal, fitsync.KindJSON:
		return []byte(r.Key.Text()), nil
	}
	b, err := s.plain.Encode(nil, []*fitsync.Record{{After: r.Key}})
	return bytes.TrimSuffix(b, []byte{'\n'}), err
}

// javaStringHash is Java's String.hashCode of s, the Java client's default
// hashing of a key for its partition, made positive.
func javaStringHash(s string) int32 {
	var h int32
	for _, c := range utf16.Encode([]rune(s)) {
		h = 31*h + int32(c)
	}
	return h & 0x7fffffff
}

// message is a record made into a message, and where it goes.
type message struct {
	index int
	id    string
	part  string
	msg   pulsarwire.ProducerMessage
	size  int
}

// Write sends the batch: its messages grouped by partition, in order, each
// partition's in Pulsar batches of at most max_batch_bytes and
// max_batch_messages, all of them in flight together; the records already
// stored are dropped.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	s.cl.cfg, s.cl.dial = s.cfg, s.dial
	c, err := s.cl.get()
	if err != nil {
		return err
	}
	s.sticky++
	ids := fitsync.RecordIDs(batch)
	var order []string
	groups := map[string][]message{}
	for i, r := range batch {
		m, err := s.message(ctx, c, r)
		if err != nil {
			return &fitsync.BatchError{Index: i, Err: err}
		}
		m.index, m.id = i, ids[i]
		m.msg.Properties = []pulsarwire.KeyValue{{Key: idProperty, Value: ids[i]}}
		if _, ok := groups[m.part]; !ok {
			order = append(order, m.part)
		}
		groups[m.part] = append(groups[m.part], m)
	}
	type inFlight struct {
		part string
		msgs []message
		ch   <-chan pulsarwire.SendResult
	}
	var flights []inFlight
	failed, permanent := -1, false
	var failure error
	fail := func(index int, err error, perm bool) {
		if failed < 0 || index < failed {
			failed, failure, permanent = index, err, perm
		}
	}
	for _, part := range order {
		sp, err := s.producer(ctx, c, part)
		if err != nil {
			fail(groups[part][0].index, err, false)
			continue
		}
		var chunk []message
		size := 0
		send := func() bool {
			if len(chunk) == 0 {
				return true
			}
			msgs := make([]pulsarwire.ProducerMessage, len(chunk))
			for i, m := range chunk {
				msgs[i] = m.msg
			}
			ch, err := sp.p.Send(msgs)
			if err != nil {
				fail(chunk[0].index, fmt.Errorf("pulsar: send to %s: %w", part, err), len(chunk) == 1 && errors.Is(err, pulsarwire.ErrTooLarge))
				s.dropProducer(part)
				return false
			}
			flights = append(flights, inFlight{part, chunk, ch})
			chunk, size = nil, 0
			return true
		}
		ok := true
		for _, m := range groups[part] {
			if sp.stored[m.id] {
				continue
			}
			if len(chunk) > 0 && (size+m.size > s.maxBytes || len(chunk) >= s.maxMsgs) {
				if ok = send(); !ok {
					break
				}
			}
			chunk = append(chunk, m)
			size += m.size
		}
		if ok {
			send()
		}
	}
	for _, f := range flights {
		select {
		case r := <-f.ch:
			if r.Err != nil {
				fail(f.msgs[0].index, fmt.Errorf("pulsar: send to %s: %w", f.part, r.Err), false)
				s.dropProducer(f.part)
				continue
			}
			if sp := s.producers[f.part]; sp != nil {
				for _, m := range f.msgs {
					sp.store(m.id)
				}
			}
		case <-ctx.Done():
			s.dropProducers()
			return ctx.Err()
		}
	}
	if failed < 0 {
		return nil
	}
	if permanent {
		failure = fitsync.Permanent(failure)
	}
	return &fitsync.BatchError{Index: failed, Err: failure}
}

// message makes r a message and finds its partition.
func (s *Sink) message(ctx context.Context, c client, r *fitsync.Record) (message, error) {
	name := r.Namespace
	if s.Topic != "" {
		name = strings.ReplaceAll(s.Topic, defaultTopic, r.Namespace)
	}
	topic, err := fullTopic(name)
	if err != nil {
		return message{}, fitsync.Permanent(err)
	}
	key, err := s.keyOf(r)
	if err != nil {
		return message{}, fitsync.Permanent(err)
	}
	pm := pulsarwire.ProducerMessage{Key: key, EventTime: r.CommitTime}
	if r.Op == fitsync.OpDelete && key != nil && s.Deletes != "encode" {
		pm.Null = true
	} else {
		v, err := s.codec.Encode(nil, []*fitsync.Record{r})
		if err != nil {
			return message{}, fitsync.Permanent(fmt.Errorf("pulsar: the codec: %w", err))
		}
		pm.Payload = bytes.TrimSuffix(v, []byte{'\n'})
	}
	n, err := s.partitions(ctx, c, topic)
	if err != nil {
		return message{}, err
	}
	p := 0
	if n > 0 {
		if key != nil {
			k, _ := pulsarwire.KeyString(key)
			p = int(javaStringHash(k)) % n
		} else {
			p = s.sticky % n
		}
	}
	return message{part: partitionName(topic, n, p), msg: pm, size: len(key) + len(pm.Payload) + 64}, nil
}

// partitions returns topic's partition count, 0 when it is not
// partitioned.
func (s *Sink) partitions(ctx context.Context, c client, topic string) (int, error) {
	if tp, ok := s.parts[topic]; ok && time.Since(tp.at) < partitionsTTL {
		return tp.n, nil
	}
	n, err := c.PartitionedMetadata(ctx, topic)
	if err != nil {
		return 0, fmt.Errorf("pulsar: the partitions of %s: %w", topic, err)
	}
	if s.parts == nil {
		s.parts = map[string]topicParts{}
	}
	s.parts[topic] = topicParts{n, time.Now()}
	return n, nil
}

// producer returns part's producer, made, and what it stored read back,
// when there is none.
func (s *Sink) producer(ctx context.Context, c client, part string) (*sinkProducer, error) {
	if sp := s.producers[part]; sp != nil {
		return sp, nil
	}
	p, err := c.CreateProducer(ctx, part, s.producerName(), s.compression)
	if err != nil {
		return nil, fmt.Errorf("pulsar: the producer of %s: %w", part, err)
	}
	sp := &sinkProducer{p: p, stored: map[string]bool{}}
	if s.Dedup != "off" {
		if err := s.readBack(ctx, c, part, sp); err != nil {
			p.Close(ctx)
			return nil, fmt.Errorf("pulsar: reading back the last message of %s: %w", part, err)
		}
	}
	if s.producers == nil {
		s.producers = map[string]*sinkProducer{}
	}
	s.producers[part] = sp
	return sp, nil
}

// readBack reads the last entries of part, up to readBackEntries of its
// last ledger, and keeps the IDs of the messages in them the producer's
// name sent: a sink batch written again finds there what it stored before,
// unless it took more Pulsar batches of the partition than that.
func (s *Sink) readBack(ctx context.Context, c client, part string, sp *sinkProducer) error {
	latest := pulsarwire.LatestID
	cs, err := c.Subscribe(ctx, part, pulsarwire.SubscribeOptions{Subscription: readerName("fit-read-back-"), Start: &latest, ReceiverQueue: 1})
	if err != nil {
		return err
	}
	last, err := cs.LastMessageID(ctx)
	cs.Close(ctx)
	if err != nil || last.Entry < 0 {
		return err
	}
	before := pulsarwire.MessageID{Ledger: last.Ledger, Entry: max(last.Entry-readBackEntries, -1), Partition: -1, BatchIndex: -1}
	cs, err = c.Subscribe(ctx, part, pulsarwire.SubscribeOptions{Subscription: readerName("fit-read-back-"), Start: &before, ReceiverQueue: DefaultMaxBatchMessages})
	if err != nil {
		return err
	}
	defer cs.Close(ctx)
	rctx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	end := pulsarwire.MessageID{Ledger: last.Ledger, Entry: last.Entry, BatchIndex: -1}
	for {
		m, err := cs.Receive(rctx)
		if err != nil {
			return err
		}
		entry := pulsarwire.MessageID{Ledger: m.ID.Ledger, Entry: m.ID.Entry, BatchIndex: -1}
		if entry.Compare(end) > 0 {
			return nil
		}
		if m.ProducerName == sp.p.Name() {
			for _, kv := range m.Properties {
				if kv.Key == idProperty {
					sp.store(kv.Value)
				}
			}
		}
		if entry.Compare(end) == 0 && (m.ID.BatchSize == 0 || m.ID.BatchIndex == m.ID.BatchSize-1) {
			return nil
		}
	}
}

// dropProducer closes part's producer, which the next write makes again.
func (s *Sink) dropProducer(part string) {
	if sp := s.producers[part]; sp != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		sp.p.Close(ctx)
		delete(s.producers, part)
	}
}

func (s *Sink) dropProducers() {
	for part := range s.producers {
		s.dropProducer(part)
	}
}
