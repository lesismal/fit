package syncrabbitmq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	amqpwire "github.com/lesismal/fit/sync/rabbitmq/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".rabbitmq", New: func() any { return new(Source) },
		Doc: "Consumes RabbitMQ queues, classic and quorum (queues), and stream queues (streams), with a prefetch, each delivery acknowledged only once every sink has its records: a queue's position is the broker's, so its unacknowledged messages are delivered again after a restart, and nothing of it is in the checkpoint; a stream's offset is, and a restart reads on after it. Each message goes through a codec (raw when none is named) into records; a message record holds the exchange, routing key, headers and properties beside its value. In a job (mode once) it ends once no message has come for idle_end."})
}

// The defaults of the source's params.
const (
	DefaultPrefetch = 1000
	DefaultIdleEnd  = 2 * time.Second
	// offsetHeader is the header a stream's delivery has its offset in.
	offsetHeader = "x-stream-offset"
)

// Source is the source rabbitmq.
type Source struct {
	Addresses       []string          `json:"addresses" fit:"required" doc:"The nodes, host:port each (5672, or 5671 for TLS), tried in order until one answers."`
	Vhost           string            `json:"vhost,omitempty" default:"\"/\"" doc:"The virtual host. / when left out."`
	Auth            string            `json:"auth,omitempty" enum:"PLAIN,EXTERNAL" default:"\"PLAIN\"" doc:"How to authenticate: PLAIN, with username and password_env (over TLS, or the password goes in clear); EXTERNAL, with the TLS client certificate of cert_file, as the rabbitmq_auth_mechanism_ssl plugin takes it. PLAIN when left out."`
	Username        string            `json:"username,omitempty" doc:"The user, for PLAIN."`
	PasswordEnv     string            `json:"password_env,omitempty" doc:"The environment variable holding the password, read when the pipeline loads."`
	TLS             bool              `json:"tls,omitempty" doc:"Connect over TLS, the nodes' certificates checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile          string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the nodes' are checked against, in place of the system's roots; needs tls."`
	CertFile        string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, for nodes that ask for one or for EXTERNAL; needs tls and key_file."`
	KeyFile         string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName      string            `json:"server_name,omitempty" doc:"The name the nodes' certificates must have; each node's host when left out."`
	Timeout         config.Duration   `json:"timeout,omitempty" default:"\"30s\"" doc:"How long connecting, each answer of the broker, each write and closing may take. 30s when left out or 0."`
	Heartbeat       config.Duration   `json:"heartbeat,omitempty" default:"\"30s\"" doc:"The heartbeat interval asked for, the broker's when lower: a connection silent for two of them is lost. 30s when left out or 0."`
	FrameMax        int               `json:"frame_max,omitempty" min:"0" default:"131072" doc:"The largest frame asked for, the broker's when lower; larger messages come in several frames. 131072 when left out or 0."`
	MaxMessageBytes int               `json:"max_message_bytes,omitempty" min:"0" default:"16777216" doc:"The largest message body taken: a larger one ends the connection, as a broken one would. 16 MiB when left out or 0."`
	Queues          []string          `json:"queues,omitempty" doc:"The classic and quorum queues consumed, by name; they are not declared, and must exist. Their messages are acknowledged once every sink has them, so a restart delivers again what was not."`
	Streams         []string          `json:"streams,omitempty" doc:"The stream queues consumed, by name, each from the offset after the last one in the checkpoint, or from stream_start without one. At least one of queues and streams."`
	StreamStart     string            `json:"stream_start,omitempty" enum:"first,last,next" default:"\"first\"" doc:"Where a stream the checkpoint has no offset of starts: first, its oldest message kept; last, its last chunk; next, after its end, reading only what comes after. first when left out."`
	Prefetch        int               `json:"prefetch,omitempty" min:"0" max:"65535" default:"1000" doc:"How many deliveries of each queue may be unacknowledged at once (basic.qos): bounds what the broker sends ahead of the sinks. 1000 when left out or 0."`
	Codec           *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes records of a message's body: raw when left out, a message record holding it as after's value."`
	Namespace       string            `json:"namespace,omitempty" doc:"The records' namespace. The codec's, or the queue's name, when left out."`
	MetadataField   string            `json:"metadata_field,omitempty" doc:"A field of after to put the message's metadata in (exchange, routing_key, queue, redelivered, headers and the properties set), on the records a codec makes rows of; a message record has them as fields of its own. None when left out."`
	IdleEnd         config.Duration   `json:"idle_end,omitempty" default:"\"2s\"" doc:"In a job (mode once), the source ends once no message has come for this long, every record acknowledged. 2s when left out or 0."`

	cfg   amqpwire.Config
	dial  dialer
	codec fitsync.Codec

	mu   sync.Mutex
	sess *session
}

// position is the source's: the session the deliveries came in and the
// last delivery tag of the records it covers, which only that session can
// acknowledge, and each stream's next offset.
type position struct {
	Run     string           `json:"run"`
	Tag     uint64           `json:"tag"`
	Streams map[string]int64 `json:"streams,omitempty"`
}

// Provision checks the params and reads the password and TLS files;
// nothing is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{Addresses: s.Addresses, Vhost: s.Vhost, Username: s.Username, PasswordEnv: s.PasswordEnv, Auth: s.Auth,
		TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName,
		Timeout: s.Timeout, Heartbeat: s.Heartbeat, FrameMax: s.FrameMax, MaxMessageBytes: s.MaxMessageBytes})
	if len(s.Queues)+len(s.Streams) == 0 {
		errs = append(errs, ctx.Params.Field("queues").Errorf("no queues or streams"))
	}
	seen := map[string]bool{}
	for _, list := range []struct {
		field string
		names []string
	}{{"queues", s.Queues}, {"streams", s.Streams}} {
		for _, q := range list.names {
			if q == "" || len(q) > 255 || seen[q] {
				errs = append(errs, ctx.Params.Field(list.field).Errorf("%q is empty, longer than 255 bytes, or named twice", q))
			}
			seen[q] = true
		}
	}
	cfg.Deliveries = s.prefetch() * (len(s.Queues) + len(s.Streams))
	s.cfg = cfg
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

func (s *Source) prefetch() int {
	if s.Prefetch > 0 {
		return s.Prefetch
	}
	return DefaultPrefetch
}

// Cleanup closes the connection and lets the codec go.
func (s *Source) Cleanup() {
	s.setSession(nil)
	module.Cleanup(s.codec)
}

// Test connects and checks that each queue and stream is there.
func (s *Source) Test(ctx context.Context) error {
	c, err := s.dial.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	for _, q := range append(append([]string(nil), s.Queues...), s.Streams...) {
		if _, err := c.QueueDeclare(ctx, q, true, false, nil); err != nil {
			return fmt.Errorf("rabbitmq: queue %s: %w", q, err)
		}
	}
	return nil
}

// session is a connection the source consumes on, and what its deliveries
// are acknowledged up to. Its goroutine sends the acknowledgements, so
// that Acknowledged never blocks.
type session struct {
	c    client
	run  string
	want atomic.Uint64 // the last tag every sink has
	sent atomic.Uint64 // the last tag acknowledged
	kick chan struct{}
	acks chan struct{} // a signal at each acknowledgement sent
	stop chan struct{}
	done chan struct{}
}

func newSession(c client) *session {
	var b [8]byte
	rand.Read(b[:])
	ss := &session{c: c, run: hex.EncodeToString(b[:]), kick: make(chan struct{}, 1), acks: make(chan struct{}, 1),
		stop: make(chan struct{}), done: make(chan struct{})}
	go ss.acker()
	return ss
}

func (ss *session) acker() {
	defer close(ss.done)
	for {
		select {
		case <-ss.stop:
			return
		case <-ss.c.Done():
			return
		case <-ss.kick:
		}
		if t := ss.want.Load(); t > ss.sent.Load() {
			if ss.c.Ack(t, true) != nil {
				return
			}
			ss.sent.Store(t)
			select {
			case ss.acks <- struct{}{}:
			default:
			}
		}
	}
}

// close stops the goroutine and closes the connection: what was not
// acknowledged goes back to its queues.
func (ss *session) close() {
	close(ss.stop)
	<-ss.done
	ss.c.Close()
}

// setSession closes the session the source had, and keeps ss.
func (s *Source) setSession(ss *session) {
	s.mu.Lock()
	old := s.sess
	s.sess = ss
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
}

// Acknowledged has the session's goroutine acknowledge every delivery up
// to p's tag.
func (s *Source) Acknowledged(p fitsync.Position) {
	var at position
	if json.Unmarshal(p, &at) != nil {
		return
	}
	s.mu.Lock()
	ss := s.sess
	s.mu.Unlock()
	if ss == nil || ss.run != at.Run {
		return
	}
	for {
		old := ss.want.Load()
		if at.Tag <= old || ss.want.CompareAndSwap(old, at.Tag) {
			break
		}
	}
	select {
	case ss.kick <- struct{}{}:
	default:
	}
}

// reader is a run of the source.
type reader struct {
	s       *Source
	ss      *session
	run     *fitsync.Run
	queues  map[string]string // consumer tag to queue
	streams map[string]bool
	offsets map[string]int64
	// last is the last tag records were emitted for.
	last uint64
}

// Run consumes the queues and streams until ctx ends or the connection is
// lost, or, in a job, until none has a message for idle_end.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	s.setSession(nil)
	var from position
	if r.From != nil {
		if err := json.Unmarshal(r.From, &from); err != nil {
			return fmt.Errorf("rabbitmq: the checkpoint %s: %w", r.From, err)
		}
	}
	c, err := s.dial.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	ss := newSession(c)
	s.setSession(ss)
	rd := &reader{s: s, ss: ss, run: r, queues: map[string]string{}, streams: map[string]bool{}, offsets: map[string]int64{}}
	if err := rd.consume(ctx, from); err != nil {
		return err
	}
	var idle *time.Timer
	var idleC <-chan time.Time
	if r.Once {
		idle = time.NewTimer(s.idleEnd())
		defer idle.Stop()
		idleC = idle.C
	}
	for {
		select {
		case d, ok := <-c.Deliveries():
			if !ok {
				return fmt.Errorf("rabbitmq: %w", c.Err())
			}
			if err := rd.take(d); err != nil {
				return err
			}
			if idle != nil {
				idle.Reset(s.idleEnd())
			}
		case <-idleC:
			return rd.drain(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Source) idleEnd() time.Duration {
	if s.IdleEnd > 0 {
		return s.IdleEnd.Std()
	}
	return DefaultIdleEnd
}

// consume sets the prefetch and starts a consumer of each queue and
// stream.
func (rd *reader) consume(ctx context.Context, from position) error {
	s, c := rd.s, rd.ss.c
	if err := c.Qos(ctx, uint16(s.prefetch())); err != nil {
		return fmt.Errorf("rabbitmq: basic.qos: %w", err)
	}
	for _, q := range s.Queues {
		tag, err := c.Consume(ctx, q, "", nil)
		if err != nil {
			return fmt.Errorf("rabbitmq: consume %s: %w", q, err)
		}
		rd.queues[tag] = q
	}
	for _, q := range s.Streams {
		var at any = s.StreamStart
		if at == "" {
			at = "first"
		}
		if off, ok := from.Streams[q]; ok {
			at = off
			rd.offsets[q] = off
		}
		tag, err := c.Consume(ctx, q, "", amqpwire.Table{offsetHeader: at})
		if err != nil {
			return fmt.Errorf("rabbitmq: consume stream %s: %w", q, err)
		}
		rd.queues[tag] = q
		rd.streams[q] = true
	}
	return nil
}

// drain waits, in a job, until every record emitted is acknowledged.
func (rd *reader) drain(ctx context.Context) error {
	for rd.ss.sent.Load() < rd.last {
		select {
		case <-rd.ss.acks:
		case <-rd.ss.c.Done():
			return fmt.Errorf("rabbitmq: %w", rd.ss.c.Err())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// take emits a delivery's records, the position on the last of them; a
// delivery that makes none is acknowledged at once.
func (rd *reader) take(d *amqpwire.Delivery) error {
	q := rd.queues[d.ConsumerTag]
	off := int64(-1)
	if rd.streams[q] {
		if o, ok := d.Properties.Headers[offsetHeader].(int64); ok {
			if o < rd.offsets[q] {
				// Before the offset asked: a stream starts at its chunk's
				// first message.
				return rd.ss.c.Ack(d.DeliveryTag, false)
			}
			rd.offsets[q], off = o+1, o
		}
	}
	recs := rd.decode(q, d)
	if len(recs) == 0 {
		return rd.ss.c.Ack(d.DeliveryTag, false)
	}
	if off >= 0 {
		// A stream's message keeps its offset; a queue's has no such
		// name: a message delivered again may come with another tag.
		for i, r := range recs {
			r.Origin = fitsync.Origin{Stream: q, N: [4]uint64{uint64(off), uint64(i)}}
		}
	}
	rd.last = d.DeliveryTag
	pos := position{Run: rd.ss.run, Tag: d.DeliveryTag}
	if len(rd.offsets) > 0 {
		pos.Streams = rd.offsets
	}
	recs[len(recs)-1].Position, _ = json.Marshal(pos) // names and numbers
	for _, r := range recs {
		if err := rd.run.Emit(r); err != nil {
			return err
		}
	}
	return nil
}

// decode makes a delivery's records.
func (rd *reader) decode(q string, d *amqpwire.Delivery) []*fitsync.Record {
	s := rd.s
	var recs []*fitsync.Record
	if err := s.codec.Decode(d.Body, func(r *fitsync.Record) { recs = append(recs, r) }); err != nil {
		recs = []*fitsync.Record{{Op: fitsync.OpMessage, After: fitsync.Map(
			fitsync.Field{Name: "value", Value: fitsync.Bytes(d.Body)},
			fitsync.Field{Name: "error", Value: fitsync.String(err.Error())})}}
	}
	var meta []fitsync.Field
	var txID string
	if len(recs) > 1 {
		txID = q + "@" + strconv.FormatUint(d.DeliveryTag, 10)
	}
	for i, r := range recs {
		r.Source, r.Position = "rabbitmq", nil
		if s.Namespace != "" || r.Namespace == "" {
			r.Namespace = q
			if s.Namespace != "" {
				r.Namespace = s.Namespace
			}
		}
		if r.Key.IsNull() {
			switch k := d.Properties.Headers[keyHeader].(type) {
			case string:
				r.Key = text(k)
			case []byte:
				r.Key = fitsync.Bytes(k)
			}
		}
		if r.CommitTime.IsZero() {
			r.CommitTime = d.Properties.Timestamp
		}
		if r.Op == fitsync.OpMessage || s.MetadataField != "" {
			if meta == nil {
				meta = metadata(q, d)
			}
			r.After = withMetadata(r, meta, s.MetadataField)
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

// withMetadata returns r's after with the metadata: under field when it
// names one, beside a message record's fields otherwise.
func withMetadata(r *fitsync.Record, meta []fitsync.Field, field string) fitsync.Value {
	fs := append([]fitsync.Field(nil), r.After.Fields()...)
	if field != "" {
		fs = append(fs, fitsync.Field{Name: field, Value: fitsync.Map(meta...)})
	} else {
		fs = append(fs, meta...)
	}
	return fitsync.Map(fs...)
}

// metadata is a delivery's exchange, routing key, queue and the
// properties set.
func metadata(q string, d *amqpwire.Delivery) []fitsync.Field {
	p := d.Properties
	fs := []fitsync.Field{
		{Name: "exchange", Value: fitsync.String(d.Exchange)},
		{Name: "routing_key", Value: fitsync.String(d.RoutingKey)},
		{Name: "queue", Value: fitsync.String(q)},
		{Name: "redelivered", Value: fitsync.Bool(d.Redelivered)},
	}
	if p.Headers != nil {
		fs = append(fs, fitsync.Field{Name: "headers", Value: table(p.Headers)})
	}
	for _, f := range []struct{ name, v string }{
		{"content_type", p.ContentType}, {"content_encoding", p.ContentEncoding}, {"correlation_id", p.CorrelationID},
		{"reply_to", p.ReplyTo}, {"expiration", p.Expiration}, {"message_id", p.MessageID}, {"type", p.Type},
		{"user_id", p.UserID}, {"app_id", p.AppID},
	} {
		if f.v != "" {
			fs = append(fs, fitsync.Field{Name: f.name, Value: fitsync.String(f.v)})
		}
	}
	if p.Priority != 0 {
		fs = append(fs, fitsync.Field{Name: "priority", Value: fitsync.Int(int64(p.Priority))})
	}
	if !p.Timestamp.IsZero() {
		fs = append(fs, fitsync.Field{Name: "timestamp", Value: fitsync.Time(p.Timestamp)})
	}
	return fs
}
