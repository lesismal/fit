package synckafka

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".kafka", New: func() any { return new(Sink) },
		Doc: "Produces records to Kafka or Redpanda: each a message of the topic its namespace names (or topic's template), its key the record's key (a string or bytes as they are, any other key as plain JSON), its value what the codec makes of it (raw when none is named), and a delete a tombstone; a key goes to the partition the Java client's partitioner gives it, so a key's messages stay in order in one partition. A batch's messages go out together, a record batch per partition, compressed, acknowledged by every in-sync replica by default. Delivery is at least once: a batch written again after a crash, or a produce whose answer was lost, writes its messages again."})
}

// The defaults of the sink's params.
const (
	DefaultMaxBatchBytes = 1 << 20
	defaultTopic         = "{namespace}"
	// partitionsTTL is how long the sink trusts a topic's partition count.
	partitionsTTL = time.Minute
)

// Sink is the sink kafka.
type Sink struct {
	Brokers          []string          `json:"brokers" fit:"required" doc:"The bootstrap brokers, host:port each; the others are found from them."`
	ClientID         string            `json:"client_id,omitempty" default:"\"fit-sync\"" doc:"The client ID the brokers see, in their logs and quotas. fit-sync when left out."`
	TLS              bool              `json:"tls,omitempty" doc:"Connect over TLS, the brokers' certificates checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile           string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the brokers' are checked against, in place of the system's roots; needs tls."`
	CertFile         string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, for brokers that ask for one; needs tls and key_file."`
	KeyFile          string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName       string            `json:"server_name,omitempty" doc:"The name the brokers' certificates must have; each broker's host when left out."`
	SASL             string            `json:"sasl,omitempty" enum:"PLAIN,SCRAM-SHA-256,SCRAM-SHA-512" doc:"The SASL mechanism to authenticate with, with username and password_env: PLAIN (over TLS, or the password goes in clear), SCRAM-SHA-256 or SCRAM-SHA-512. None when left out."`
	Username         string            `json:"username,omitempty" doc:"The SASL user."`
	PasswordEnv      string            `json:"password_env,omitempty" doc:"The environment variable holding the SASL password, read when the pipeline loads."`
	Timeout          config.Duration   `json:"timeout,omitempty" default:"\"30s\"" doc:"How long connecting, each answer, and the retries of a leader that moved may take. 30s when left out or 0."`
	Topic            string            `json:"topic,omitempty" default:"\"{namespace}\"" doc:"The topic a record goes to: text with {namespace}, the record's namespace (a route transform can map namespaces to topics first). A record whose topic is not a name Kafka takes fails. {namespace} when left out."`
	Codec            *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes a message's value of a record: raw when left out, after's value field as it is, or the record's canonical JSON when it has none; json for the row as plain JSON (form plain) or the whole record (form record); canal_json or debezium for those formats."`
	Deletes          string            `json:"deletes,omitempty" enum:"tombstone,encode" default:"\"tombstone\"" doc:"What a delete with a key is: tombstone, a message of the key with a null value, which a compacted topic takes as the key gone and the source kafka reads back as a delete; encode, a message of what the codec makes of it. tombstone when left out."`
	Acks             string            `json:"acks,omitempty" enum:"all,leader,none" default:"\"all\"" doc:"Who acknowledges a batch: all, every in-sync replica (min.insync.replicas of them at least); leader, the partition's leader alone, so a leader lost before its followers copy the batch loses it; none, no one, the batch counted as written once sent. all when left out."`
	Compression      string            `json:"compression,omitempty" enum:"none,gzip,snappy,lz4,zstd" default:"\"lz4\"" doc:"How record batches are compressed. zstd needs Kafka 2.1 or later. lz4 when left out."`
	CompressionLevel int               `json:"compression_level,omitempty" min:"0" max:"22" doc:"The codec's level: gzip 1 to 9, zstd 1 to 22; the codec's default when left out or 0."`
	MaxBatchBytes    int               `json:"max_batch_bytes,omitempty" min:"0" default:"1048576" doc:"The most bytes, keys and values before compression, of one record batch to a partition; a sink batch with more for a partition sends several, in order. Keep it below the topic's max.message.bytes. 1048576 when left out or 0."`
	CreateTopics     *TopicCreate      `json:"create_topics,omitempty" doc:"Create a topic that does not exist when a record first goes to it, with these settings; a record for a missing topic fails when left out."`

	cfg     kafkawire.Config
	dial    dialer
	codec   fitsync.Codec
	cl      holder
	opts    kafkawire.ProduceOptions
	plain   fitsync.JSONCodec
	parts   map[string]topicParts
	sticky  int32
	maxSize int
	now     func() time.Time
}

// TopicCreate is how the sink kafka creates a topic.
type TopicCreate struct {
	Partitions        int32             `json:"partitions,omitempty" min:"0" doc:"The topic's partitions; the brokers' num.partitions when left out or 0."`
	ReplicationFactor int16             `json:"replication_factor,omitempty" min:"0" doc:"The copies of each partition; the brokers' default.replication.factor when left out or 0."`
	Configs           map[string]string `json:"configs,omitempty" doc:"The topic's configs, as cleanup.policy compact or retention.ms."`
}

// topicParts is a topic's partition count, and when it was read.
type topicParts struct {
	n  int32
	at time.Time
}

// Provision checks the params and reads the password and TLS files;
// nothing is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{Brokers: s.Brokers, ClientID: s.ClientID, TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile,
		KeyFile: s.KeyFile, ServerName: s.ServerName, SASL: s.SASL, Username: s.Username, PasswordEnv: s.PasswordEnv, Timeout: s.Timeout})
	s.cfg = cfg
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if s.Topic != "" {
		if rest := strings.ReplaceAll(s.Topic, defaultTopic, ""); strings.ContainsAny(rest, "{}") || (rest == s.Topic && !validTopic(s.Topic)) {
			add("topic", "%q is not a topic's name, or text with {namespace}", s.Topic)
		}
	}
	s.opts = kafkawire.ProduceOptions{Level: s.CompressionLevel, Timeout: cfg.RequestTimeout}
	switch s.Acks {
	case "leader":
		s.opts.Acks = kafkawire.AcksLeader
	case "none":
		s.opts.Acks = kafkawire.AcksNone
	}
	comp := s.Compression
	if comp == "" {
		comp = "lz4"
	}
	var err error
	if s.opts.Compression, err = kafkawire.ParseCompression(comp); err != nil {
		add("compression", "%v", err)
	}
	if s.CreateTopics != nil && s.CreateTopics.ReplicationFactor < 0 {
		add("create_topics", "replication_factor %d is below 0", s.CreateTopics.ReplicationFactor)
	}
	s.maxSize = DefaultMaxBatchBytes
	if s.MaxBatchBytes > 0 {
		s.maxSize = s.MaxBatchBytes
	}
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// Cleanup closes the client and lets the codec go.
func (s *Sink) Cleanup() {
	s.cl.close()
	module.Cleanup(s.codec)
}

// Test connects and reads the cluster's metadata, and the topic's when it
// is fixed and none is to be created.
func (s *Sink) Test(ctx context.Context) error {
	var topics []string
	if s.Topic != "" && !strings.Contains(s.Topic, defaultTopic) && s.CreateTopics == nil {
		topics = []string{s.Topic}
	}
	return testTopics(ctx, s.cfg, s.dial, topics)
}

func (s *Sink) topicOf(r *fitsync.Record) string {
	if s.Topic == "" {
		return r.Namespace
	}
	return strings.ReplaceAll(s.Topic, defaultTopic, r.Namespace)
}

// message is a record made into a message, and where it goes.
type message struct {
	index     int
	topic     string
	partition int32
	rec       kafkawire.Record
	size      int
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

// Write produces the batch: its messages grouped by partition, in order,
// each partition's in record batches of at most max_batch_bytes, the
// partitions' first batches in one produce, their second in the next, and
// so on.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	s.cl.cfg, s.cl.dial = s.cfg, s.dial
	c, err := s.cl.get()
	if err != nil {
		return err
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	s.sticky++
	msgs := make([]message, 0, len(batch))
	for i, r := range batch {
		m, err := s.message(ctx, c, r, now)
		if err != nil {
			return &fitsync.BatchError{Index: i, Err: err}
		}
		m.index = i
		msgs = append(msgs, m)
	}
	return s.produce(ctx, c, msgs)
}

// message makes r a message and finds its partition.
func (s *Sink) message(ctx context.Context, c client, r *fitsync.Record, now func() time.Time) (message, error) {
	topic := s.topicOf(r)
	if !validTopic(topic) {
		return message{}, fitsync.Permanent(fmt.Errorf("kafka: %q is not a topic's name", topic))
	}
	key, err := s.keyOf(r)
	if err != nil {
		return message{}, fitsync.Permanent(err)
	}
	var value []byte
	if r.Op != fitsync.OpDelete || key == nil || s.Deletes == "encode" {
		value, err = s.codec.Encode(nil, []*fitsync.Record{r})
		if err != nil {
			return message{}, fitsync.Permanent(fmt.Errorf("kafka: the codec: %w", err))
		}
		value = bytes.TrimSuffix(value, []byte{'\n'})
		if value == nil {
			value = []byte{}
		}
	}
	n, err := s.partitions(ctx, c, topic)
	if err != nil {
		return message{}, err
	}
	var p int32
	if key != nil {
		p = kafkawire.PartitionFor(key, n)
	} else {
		p = s.sticky % n
	}
	ts := r.CommitTime
	if ts.IsZero() {
		ts = now()
	}
	return message{topic: topic, partition: p, rec: kafkawire.Record{Key: key, Value: value, Timestamp: ts},
		size: len(key) + len(value) + 32}, nil
}

// partitions returns topic's partition count, creating the topic when
// create_topics says to.
func (s *Sink) partitions(ctx context.Context, c client, topic string) (int32, error) {
	if tp, ok := s.parts[topic]; ok && time.Since(tp.at) < partitionsTTL {
		return tp.n, nil
	}
	md, err := c.Metadata(ctx, topic)
	if err != nil {
		return 0, err
	}
	tm := md.Topic(topic)
	if tm == nil || errors.Is(tm.Err, kafkawire.ErrUnknownTopicOrPartition) {
		if s.CreateTopics == nil {
			return 0, fmt.Errorf("kafka: no topic %s, and create_topics is not set", topic)
		}
		spec := kafkawire.TopicSpec{Name: topic, Partitions: s.CreateTopics.Partitions, ReplicationFactor: s.CreateTopics.ReplicationFactor, Configs: s.CreateTopics.Configs}
		if spec.Partitions == 0 {
			spec.Partitions = -1
		}
		if spec.ReplicationFactor == 0 {
			spec.ReplicationFactor = -1
		}
		if err := c.CreateTopics(ctx, s.cfg.RequestTimeout, spec); err != nil && !errors.Is(err, kafkawire.ErrTopicAlreadyExists) {
			return 0, fmt.Errorf("kafka: create topic %s: %w", topic, err)
		}
	} else if tm.Err != nil {
		return 0, fmt.Errorf("kafka: topic %s: %w", topic, tm.Err)
	}
	n, err := c.Partitions(ctx, topic)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("kafka: topic %s has no partitions", topic)
	}
	if s.parts == nil {
		s.parts = map[string]topicParts{}
	}
	s.parts[topic] = topicParts{n, time.Now()}
	return n, nil
}

// chunk is messages for one partition sent as one record batch.
type chunk struct {
	topic     string
	partition int32
	msgs      []message
	size      int
}

// produce sends msgs, each partition's in order, and returns a BatchError
// at the first record of the batch not written.
func (s *Sink) produce(ctx context.Context, c client, msgs []message) error {
	type key struct {
		topic string
		p     int32
	}
	var order []key
	queues := map[key][]chunk{}
	for _, m := range msgs {
		k := key{m.topic, m.partition}
		q, ok := queues[k]
		if !ok {
			order = append(order, k)
		}
		if n := len(q); n == 0 || q[n-1].size+m.size > s.maxSize {
			q = append(q, chunk{topic: m.topic, partition: m.partition})
		}
		q[len(q)-1].msgs = append(q[len(q)-1].msgs, m)
		q[len(q)-1].size += m.size
		queues[k] = q
	}
	failed, permanent := -1, false
	var failure error
	fail := func(index int, err error, perm bool) {
		if failed < 0 || index < failed {
			failed, failure, permanent = index, err, perm
		}
	}
	for {
		var round []chunk
		var keys []key
		for _, k := range order {
			if q := queues[k]; len(q) > 0 {
				round = append(round, q[0])
				keys = append(keys, k)
			}
		}
		if len(round) == 0 {
			break
		}
		batches := make([]kafkawire.ProduceBatch, len(round))
		for i, ch := range round {
			recs := make([]kafkawire.Record, len(ch.msgs))
			for j, m := range ch.msgs {
				recs[j] = m.rec
			}
			batches[i] = kafkawire.ProduceBatch{Topic: ch.topic, Partition: ch.partition, Records: recs}
		}
		res, err := c.Produce(ctx, s.opts, batches...)
		if res == nil {
			return err
		}
		for i, ch := range round {
			k := keys[i]
			rerr := res[i].Err
			switch {
			case rerr == nil:
				queues[k] = queues[k][1:]
			case errors.Is(rerr, kafkawire.ErrMessageTooLarge) && len(ch.msgs) > 1:
				// Sent again a message at a time, to find the one too large.
				var one []chunk
				for _, m := range ch.msgs {
					one = append(one, chunk{topic: ch.topic, partition: ch.partition, msgs: []message{m}})
				}
				queues[k] = append(one, queues[k][1:]...)
			default:
				fail(ch.msgs[0].index, fmt.Errorf("kafka: produce to %s/%d: %w", ch.topic, ch.partition, rerr),
					len(ch.msgs) == 1 && (errors.Is(rerr, kafkawire.ErrMessageTooLarge) || errors.Is(rerr, kafkawire.ErrCorruptMessage)))
				queues[k] = nil
			}
		}
		if ctx.Err() != nil && failed < 0 {
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
