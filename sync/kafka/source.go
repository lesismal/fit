package synckafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".kafka", New: func() any { return new(Source) },
		Doc: "Reads Kafka or Redpanda topics, every partition of each, from the offsets in the pipeline's checkpoint: no consumer group is joined or committed, so the checkpoint is the only cursor, saved with the rest once every sink has the records. Each message goes through a codec (raw when none is named; json, canal_json, debezium, ticdc_simple or any other) into records, whose key is the message's key unless the codec sets one, and whose namespace is the topic unless the codec or namespace sets one. In a job (mode once) it reads up to each partition's end as it was when the job started."})
}

// The defaults of the source's params.
const (
	DefaultMaxWait           = 500 * time.Millisecond
	DefaultPartitionMaxBytes = 1 << 20
	// partitionsEvery is how often the source looks for partitions added
	// to its topics.
	partitionsEvery = time.Minute
)

// Source is the source kafka.
type Source struct {
	Brokers     []string          `json:"brokers" fit:"required" doc:"The bootstrap brokers, host:port each; the others are found from them."`
	ClientID    string            `json:"client_id,omitempty" default:"\"fit-sync\"" doc:"The client ID the brokers see, in their logs and quotas. fit-sync when left out."`
	TLS         bool              `json:"tls,omitempty" doc:"Connect over TLS, the brokers' certificates checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile      string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the brokers' are checked against, in place of the system's roots; needs tls."`
	CertFile    string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, for brokers that ask for one; needs tls and key_file."`
	KeyFile     string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName  string            `json:"server_name,omitempty" doc:"The name the brokers' certificates must have; each broker's host when left out."`
	SASL        string            `json:"sasl,omitempty" enum:"PLAIN,SCRAM-SHA-256,SCRAM-SHA-512" doc:"The SASL mechanism to authenticate with, with username and password_env: PLAIN (over TLS, or the password goes in clear), SCRAM-SHA-256 or SCRAM-SHA-512. None when left out."`
	Username    string            `json:"username,omitempty" doc:"The SASL user."`
	PasswordEnv string            `json:"password_env,omitempty" doc:"The environment variable holding the SASL password, read when the pipeline loads."`
	Timeout     config.Duration   `json:"timeout,omitempty" default:"\"30s\"" doc:"How long connecting, each answer beyond the fetch's wait, and the retries of a leader that moved may take. 30s when left out or 0."`
	Topics      []string          `json:"topics" fit:"required" doc:"The topics read, every partition of each; partitions added to them are found within a minute and read from their start."`
	Codec       *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes records of a message's value: raw when left out, a message record holding it as after's value."`
	Namespace   string            `json:"namespace,omitempty" doc:"The records' namespace. The codec's (canal_json's db.table, say), or the topic, when left out."`
	Start       string            `json:"start,omitempty" enum:"earliest,latest" default:"\"earliest\"" doc:"Where a partition the checkpoint does not hold starts, when the pipeline has no checkpoint: earliest, the oldest message kept; latest, after the newest, reading only what comes after. A partition found later, or missing from a checkpoint, starts at its earliest. earliest when left out."`
	OffsetReset string            `json:"offset_reset,omitempty" enum:"error,earliest,latest" default:"\"error\"" doc:"What happens when the checkpoint's offset of a partition is no longer in its log, its messages deleted by retention before they were read: error, the pipeline fails and says so, for a person to decide; earliest or latest, it goes on from there, the messages between lost. error when left out."`
	Tombstones  string            `json:"tombstones,omitempty" enum:"delete,skip" default:"\"delete\"" doc:"What a message with a null value is: delete, a delete record of the message's key, as a compacted topic means it; skip, nothing, as after a Debezium delete event. delete when left out."`
	Uncommitted bool              `json:"read_uncommitted,omitempty" doc:"Read transactional producers' messages before they commit, and those of aborted transactions; otherwise only committed ones, up to the last stable offset."`
	MaxWait     config.Duration   `json:"max_wait,omitempty" default:"\"500ms\"" doc:"How long a broker holds a fetch waiting for messages. 500ms when left out or 0."`
	MaxBytes    int               `json:"partition_max_bytes,omitempty" min:"0" default:"1048576" doc:"The most bytes a fetch reads of a partition, though a partition's first batch comes whole. 1048576 when left out or 0."`

	cfg   kafkawire.Config
	dial  dialer
	codec fitsync.Codec
	cl    holder
}

// position is the source's: the next offset to read of each partition of
// each topic.
type position struct {
	Offsets map[string]map[string]int64 `json:"offsets"`
}

// Provision checks the params and reads the password and TLS files;
// nothing is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{Brokers: s.Brokers, ClientID: s.ClientID, TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile,
		KeyFile: s.KeyFile, ServerName: s.ServerName, SASL: s.SASL, Username: s.Username, PasswordEnv: s.PasswordEnv, Timeout: s.Timeout})
	s.cfg = cfg
	if len(s.Topics) == 0 {
		errs = append(errs, ctx.Params.Field("topics").Errorf("no topics"))
	}
	seen := map[string]bool{}
	for _, t := range s.Topics {
		if !validTopic(t) || seen[t] {
			errs = append(errs, ctx.Params.Field("topics").Errorf("%q is not a topic's name, or named twice", t))
		}
		seen[t] = true
	}
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// validTopic reports whether t is a name Kafka takes for a topic.
func validTopic(t string) bool {
	if t == "" || len(t) > 249 || t == "." || t == ".." {
		return false
	}
	for _, c := range []byte(t) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// Cleanup closes the client and lets the codec go.
func (s *Source) Cleanup() {
	s.cl.close()
	module.Cleanup(s.codec)
}

// Test connects and reads the topics' metadata, each of which must exist.
func (s *Source) Test(ctx context.Context) error {
	return testTopics(ctx, s.cfg, s.dial, s.Topics)
}

// testTopics reads the metadata of topics on a client of its own.
func testTopics(ctx context.Context, cfg kafkawire.Config, dial dialer, topics []string) error {
	h := holder{cfg: cfg, dial: dial}
	c, err := h.get()
	if err != nil {
		return err
	}
	defer h.close()
	md, err := c.Metadata(ctx, topics...)
	if err != nil {
		return err
	}
	for _, t := range topics {
		tm := md.Topic(t)
		if tm == nil {
			return fmt.Errorf("kafka: no topic %s", t)
		}
		if tm.Err != nil {
			return fmt.Errorf("kafka: topic %s: %w", t, tm.Err)
		}
	}
	return nil
}

// partState is where the source is in a partition.
type partState struct {
	topic     string
	partition int32
	next      int64
	// high is the high watermark the last fetch gave; 0 before one.
	high int64
	// end is the offset a job stops at; -1 in a stream.
	end int64
}

func (p *partState) done() bool { return p.end >= 0 && p.next >= p.end }

// reader is a run of the source.
type reader struct {
	s     *Source
	c     client
	run   *fitsync.Run
	parts []*partState
	// counts are the partitions known of each topic.
	counts map[string]int32
}

// Run reads the topics from the checkpoint's offsets, or from start, until
// ctx ends, or, in a job, until every partition reaches the end it had.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	s.cl.cfg, s.cl.dial = s.cfg, s.dial
	c, err := s.cl.get()
	if err != nil {
		return err
	}
	var from position
	if r.From != nil {
		if err := json.Unmarshal(r.From, &from); err != nil {
			return fmt.Errorf("kafka: the checkpoint %s: %w", r.From, err)
		}
	}
	rd := &reader{s: s, c: c, run: r, counts: map[string]int32{}}
	if err := rd.addPartitions(ctx, from.Offsets, r.From == nil); err != nil {
		return err
	}
	if r.Once {
		if err := rd.ends(ctx); err != nil {
			return err
		}
	}
	checked := time.Now()
	for {
		if !r.Once && time.Since(checked) >= partitionsCheck {
			if err := rd.addPartitions(ctx, nil, false); err != nil {
				return err
			}
			checked = time.Now()
		}
		var fps []kafkawire.FetchPartition
		var states []*partState
		for _, p := range rd.parts {
			if !p.done() {
				fps = append(fps, kafkawire.FetchPartition{Topic: p.topic, Partition: p.partition, Offset: p.next})
				states = append(states, p)
			}
		}
		if len(fps) == 0 {
			return nil
		}
		res, _ := c.Fetch(ctx, s.fetchOptions(), fps...)
		if err := ctx.Err(); err != nil {
			return err
		}
		for i, fr := range res {
			if fr.Err == nil && fr.HighWatermark >= 0 {
				states[i].high = fr.HighWatermark
			}
			if err := rd.take(ctx, states[i], fr); err != nil {
				return err
			}
		}
		r.ReportLag(rd.lag(), fitsync.LagOffsets)
	}
}

// lag is how many offsets the partitions are behind their high
// watermarks, as the last fetches gave them.
func (rd *reader) lag() int64 {
	var n int64
	for _, p := range rd.parts {
		n += max(p.high-p.next, 0)
	}
	return n
}

func (s *Source) fetchOptions() kafkawire.FetchOptions {
	o := kafkawire.FetchOptions{MaxWait: DefaultMaxWait, PartitionMaxBytes: DefaultPartitionMaxBytes, ReadCommitted: !s.Uncommitted}
	if s.MaxWait > 0 {
		o.MaxWait = s.MaxWait.Std()
	}
	if s.MaxBytes > 0 {
		o.PartitionMaxBytes = int32(min(s.MaxBytes, 1<<30))
	}
	return o
}

// addPartitions adds the topics' partitions the reader does not have yet:
// at the offsets of from, or at start when fresh (the pipeline has no
// checkpoint), or at their earliest.
func (rd *reader) addPartitions(ctx context.Context, from map[string]map[string]int64, fresh bool) error {
	var added []*partState
	var queries []kafkawire.OffsetQuery
	for _, t := range rd.s.Topics {
		n, err := rd.c.Partitions(ctx, t)
		if err != nil {
			return fmt.Errorf("kafka: the partitions of %s: %w", t, err)
		}
		for p := rd.counts[t]; p < n; p++ {
			ps := &partState{topic: t, partition: p, end: -1}
			if off, ok := from[t][strconv.Itoa(int(p))]; ok {
				ps.next = off
			} else {
				ts := kafkawire.OffsetEarliest
				if fresh && rd.s.Start == "latest" {
					ts = kafkawire.OffsetLatest
				}
				ps.next = -1
				queries = append(queries, kafkawire.OffsetQuery{Topic: t, Partition: p, Timestamp: ts})
			}
			added = append(added, ps)
		}
		rd.counts[t] = max(rd.counts[t], n)
	}
	if len(queries) > 0 {
		res, err := rd.c.ListOffsets(ctx, !rd.s.Uncommitted, queries...)
		if err != nil {
			return fmt.Errorf("kafka: the start offsets: %w", err)
		}
		i := 0
		for _, ps := range added {
			if ps.next < 0 {
				ps.next = res[i].Offset
				i++
			}
		}
	}
	rd.parts = append(rd.parts, added...)
	return nil
}

// ends sets each partition's end, for a job: its high watermark (its last
// stable offset when reading committed messages) now.
func (rd *reader) ends(ctx context.Context) error {
	qs := make([]kafkawire.OffsetQuery, len(rd.parts))
	for i, p := range rd.parts {
		qs[i] = kafkawire.OffsetQuery{Topic: p.topic, Partition: p.partition, Timestamp: kafkawire.OffsetLatest}
	}
	res, err := rd.c.ListOffsets(ctx, !rd.s.Uncommitted, qs...)
	if err != nil {
		return fmt.Errorf("kafka: the end offsets: %w", err)
	}
	for i, p := range rd.parts {
		p.end = res[i].Offset
	}
	return nil
}

// take emits a partition's fetched messages as records, the position on
// the last record of each message.
func (rd *reader) take(ctx context.Context, p *partState, fr kafkawire.FetchResult) error {
	if fr.Err != nil {
		if !errors.Is(fr.Err, kafkawire.ErrOffsetOutOfRange) {
			return fmt.Errorf("kafka: fetch %s/%d at %d: %w", p.topic, p.partition, p.next, fr.Err)
		}
		return rd.reset(ctx, p, fr)
	}
	var recs []*fitsync.Record
	for _, m := range fr.Records {
		if m.Offset < p.next {
			continue
		}
		if p.end >= 0 && m.Offset >= p.end {
			break
		}
		recs = rd.decode(recs[:0], p, m)
		p.next = m.Offset + 1
		if len(recs) == 0 {
			continue
		}
		recs[len(recs)-1].Position = rd.position()
		for _, rec := range recs {
			if err := rd.run.Emit(rec); err != nil {
				return err
			}
		}
	}
	if fr.NextOffset > p.next && (p.end < 0 || fr.NextOffset <= p.end) {
		// Past control batches and aborted messages: nothing to emit, and
		// read from there again after a restart.
		p.next = fr.NextOffset
	}
	if p.end >= 0 && fr.HighWatermark >= 0 && p.next >= fr.HighWatermark && p.next < p.end {
		// The log is shorter than the end asked: a job's end read
		// uncommitted, or messages compacted away.
		p.end = p.next
	}
	return nil
}

// reset moves a partition whose offset is out of its log, as offset_reset
// says.
func (rd *reader) reset(ctx context.Context, p *partState, fr kafkawire.FetchResult) error {
	ts := kafkawire.OffsetEarliest
	switch rd.s.OffsetReset {
	case "earliest":
	case "latest":
		ts = kafkawire.OffsetLatest
	default:
		return fmt.Errorf("kafka: %s/%d: offset %d is not in the log (from %d to %d): its messages are gone; set offset_reset, or the checkpoint, to go on",
			p.topic, p.partition, p.next, fr.LogStartOffset, fr.HighWatermark)
	}
	res, err := rd.c.ListOffsets(ctx, !rd.s.Uncommitted, kafkawire.OffsetQuery{Topic: p.topic, Partition: p.partition, Timestamp: ts})
	if err != nil {
		return fmt.Errorf("kafka: %s/%d: %w", p.topic, p.partition, err)
	}
	p.next = res[0].Offset
	return nil
}

// decode makes a message's records, appended to recs.
func (rd *reader) decode(recs []*fitsync.Record, p *partState, m kafkawire.Record) []*fitsync.Record {
	if m.Value == nil {
		if rd.s.Tombstones == "skip" || m.Key == nil {
			return recs
		}
		recs = append(recs, &fitsync.Record{Op: fitsync.OpDelete})
	} else if err := rd.s.codec.Decode(m.Value, func(r *fitsync.Record) { recs = append(recs, r) }); err != nil {
		recs = append(recs[:0], &fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(
			fitsync.Field{Name: "value", Value: fitsync.Bytes(m.Value)},
			fitsync.Field{Name: "error", Value: fitsync.String(err.Error())})})
	}
	var txID string
	if len(recs) > 1 {
		txID = p.topic + "/" + strconv.Itoa(int(p.partition)) + "@" + strconv.FormatInt(m.Offset, 10)
	}
	for i, r := range recs {
		r.Source, r.Position = "kafka", nil
		if rd.s.Namespace != "" || r.Namespace == "" {
			r.Namespace = rd.namespace(p.topic)
		}
		if r.Key.IsNull() && m.Key != nil {
			r.Key = rd.key(m.Key)
		}
		if r.CommitTime.IsZero() && !m.Timestamp.IsZero() {
			r.CommitTime = m.Timestamp
		}
		if len(recs) > 1 {
			id := r.Tx.ID
			if id == "" {
				id = txID
			}
			r.Tx = fitsync.Tx{ID: id, First: i == 0, Last: i == len(recs)-1}
		} else {
			r.Tx = fitsync.Tx{}
		}
	}
	return recs
}

// key makes a record's key of a message's: what the codec decodes of it,
// when it decodes keys, or its text.
func (rd *reader) key(k []byte) fitsync.Value {
	if kd, ok := rd.s.codec.(KeyDecoder); ok {
		if v, err := kd.DecodeKey(k); err == nil && !v.IsNull() {
			return v
		}
	}
	return text(k)
}

func (rd *reader) namespace(topic string) string {
	if rd.s.Namespace != "" {
		return rd.s.Namespace
	}
	return topic
}

// position writes every partition's next offset.
func (rd *reader) position() fitsync.Position {
	p := position{Offsets: make(map[string]map[string]int64, len(rd.s.Topics))}
	for _, ps := range rd.parts {
		m := p.Offsets[ps.topic]
		if m == nil {
			m = map[string]int64{}
			p.Offsets[ps.topic] = m
		}
		m[strconv.Itoa(int(ps.partition))] = ps.next
	}
	b, _ := json.Marshal(p) // topics are ASCII, checked by Provision
	return b
}

// text makes a key a string when it is UTF-8, bytes otherwise.
func text(b []byte) fitsync.Value {
	if utf8.Valid(b) {
		return fitsync.String(string(b))
	}
	return fitsync.Bytes(b)
}

// SetCodec replaces the codec, for a source built on this one (the tidb
// source) that picks it itself; call it after Provision.
func (s *Source) SetCodec(c fitsync.Codec) {
	module.Cleanup(s.codec)
	s.codec = c
}

// partitionsCheck is partitionsEvery, shortened by tests.
var partitionsCheck = partitionsEvery
