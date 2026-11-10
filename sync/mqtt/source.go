package syncmqtt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".mqtt", New: func() any { return new(Source) },
		Doc: "Subscribes to MQTT topic filters, of version 3.1.1 or 5, in a persistent session of its client ID, which the broker keeps while fit is away, with the messages of QoS 1 and 2 sent to it. A message of QoS 1 or 2 is acknowledged (PUBACK, PUBREC) only once every sink has its records, in the order the messages came, so the broker sends again, after a restart, those a pipeline stopped or killed had not written: at least once into the sinks, and a QoS 2 message is taken once in a connection. Each message is a record whose namespace is its topic, unless namespace sets one, and whose key is the topic, unless the codec sets one: its after holds the topic, payload, QoS, retain and MQTT 5 properties when no codec is named. The checkpoint holds nothing to resume from, the session being the position, but the topic filters subscribed: a filter dropped from topics is unsubscribed at the next run. In a job (mode once) it reads until no message comes for idle, then ends once the sinks have what it read."})
}

// The defaults of the source's params.
const (
	DefaultSessionExpiry  = 24 * time.Hour
	DefaultReceiveMaximum = 128
	DefaultIdle           = 5 * time.Second
)

// Source is the source mqtt.
type Source struct {
	Servers        []string          `json:"servers" fit:"required" doc:"The brokers, host:port each (1883, or 8883 with tls), tried in turn until one takes the connection: the nodes of one cluster, which share the session."`
	ClientID       string            `json:"client_id" fit:"required" doc:"The client ID, which names the session the broker keeps: one pipeline's, and no other client's, since a second connection of it takes the session over."`
	Version        string            `json:"version,omitempty" enum:"3.1.1,5" default:"\"5\"" doc:"The MQTT version spoken: 3.1.1 or 5. 5 when left out."`
	Username       string            `json:"username,omitempty" doc:"The user name sent in CONNECT."`
	PasswordEnv    string            `json:"password_env,omitempty" doc:"The environment variable holding the password sent in CONNECT, read when the pipeline loads."`
	TLS            bool              `json:"tls,omitempty" doc:"Connect over TLS, the brokers' certificates checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile         string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the brokers' are checked against, in place of the system's roots; needs tls."`
	CertFile       string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, for brokers that ask for one; needs tls and key_file."`
	KeyFile        string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName     string            `json:"server_name,omitempty" doc:"The name the brokers' certificates must have; each broker's host when left out."`
	Timeout        config.Duration   `json:"timeout,omitempty" default:"\"30s\"" doc:"How long connecting, the broker's answers to CONNECT and SUBSCRIBE, and each write may take. 30s when left out or 0."`
	KeepAlive      config.Duration   `json:"keep_alive,omitempty" default:"\"60s\"" doc:"The keep-alive told to the broker: fit pings it every half of it, and takes a ping not answered within it as the connection lost. At most 65535s; a broker of version 5 may set its own. 60s when left out or 0."`
	Topics         []string          `json:"topics" fit:"required" doc:"The topic filters subscribed to, + standing for a level and # for the levels under one, as sensors/+/temperature or devices/#; a shared subscription, $share/group/filter, splits the messages among the clients of the group."`
	QoS            *int              `json:"qos,omitempty" min:"0" max:"2" default:"1" doc:"The most QoS the broker sends the topics' messages at: 0, at most once, lost when fit is away; 1, at least once, kept in the session and sent again until acknowledged; 2, as 1, and taken once in a connection. 1 when left out."`
	SessionExpiry  config.Duration   `json:"session_expiry,omitempty" default:"\"24h\"" doc:"How long a broker of version 5 keeps the session, and the messages for it, while fit is not connected; a broker may hold it shorter (EMQX's session_expiry_interval). In 3.1.1 the broker's own setting (Mosquitto's persistent_client_expiration). 24h when left out or 0."`
	ReceiveMaximum int               `json:"receive_maximum,omitempty" min:"0" max:"65535" default:"128" doc:"The most messages of QoS 1 and 2 a broker of version 5 sends before fit acknowledges them, and the most messages read and not yet handed to the pipeline. Since a message is acknowledged once the sinks have it, keep it above the sinks' batch.max_records; brokers bound it too (EMQX's max_inflight, Mosquitto's max_inflight_messages). 128 when left out or 0."`
	MaxPacketSize  int               `json:"max_packet_size,omitempty" min:"0" max:"268435455" default:"1048576" doc:"The most bytes of a packet fit takes, told to a broker of version 5, which then drops a message larger for fit; a larger packet ends the connection. 1048576 when left out or 0."`
	Codec          *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes records of a message's payload, as json for a JSON object. When left out, a message record whose after holds topic, payload (bytes), qos, retain, and those of the MQTT 5 properties the message has: content_type, response_topic, correlation_data and user_properties (a map, a name sent more than once an array of its values)."`
	Namespace      string            `json:"namespace,omitempty" doc:"The records' namespace. The codec's, or the topic, when left out."`
	Idle           config.Duration   `json:"idle,omitempty" default:"\"5s\"" doc:"In a job (mode once), how long without a message ends the reading: the source ends once the sinks have every message it read. 5s when left out or 0."`
	Will           *Will             `json:"will,omitempty" doc:"A will message the broker publishes once the source's connection ends other than by fit closing it: lost, or kept alive in vain. None when left out."`
	AuthMethod     string            `json:"auth_method,omitempty" enum:"SCRAM-SHA-1,SCRAM-SHA-256,SCRAM-SHA-512" doc:"In version 5, authenticate by enhanced authentication (AUTH) with this SCRAM method, of username and password_env's password, rather than with the password in CONNECT: the password does not cross the wire, and fit checks the broker knows it. EMQX 5's SCRAM authenticator takes SCRAM-SHA-256 and SCRAM-SHA-512. The password in CONNECT when left out."`

	cfg   mqttwire.Config
	dial  dialer
	codec fitsync.Codec
	qos   byte
	now   func() time.Time

	mu  sync.Mutex
	cur *session
}

// session is a run's connection, and its messages waiting for
// acknowledgement.
type session struct {
	c   client
	run string
	seq uint64
	// waiting are the messages emitted, in order, until acknowledged;
	// ackq those acknowledged and not yet answered.
	waiting, ackq []*pending
	wake          chan struct{}
	acked         chan struct{}
	quit          chan struct{}
	ackMu         sync.Mutex
	stopped       bool
	// filters are those the session may hold, which positions carry:
	// topics, and the filters dropped until their UNSUBACK. dropped are
	// the filters this run unsubscribes, whose messages, queued before,
	// are acknowledged and not emitted.
	filters, dropped []string
}

type pending struct {
	seq uint64
	m   *mqttwire.Message
	// free says the message made no record: nothing waits on it.
	free bool
}

// position is the source's: a run and a message's number in it, and the
// topic filters the session may hold. It is not resumed from: the
// broker's session is; its filters say what to unsubscribe from.
type position struct {
	Run     string   `json:"run"`
	Seq     uint64   `json:"seq"`
	Filters []string `json:"filters,omitempty"`
}

// Provision checks the params and reads the password and TLS files;
// nothing is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{Servers: s.Servers, ClientID: s.ClientID, Version: s.Version, Username: s.Username,
		PasswordEnv: s.PasswordEnv, TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName,
		Timeout: s.Timeout, KeepAlive: s.KeepAlive, MaxPacketSize: s.MaxPacketSize, Will: s.Will, AuthMethod: s.AuthMethod})
	if s.ClientID == "" {
		errs = append(errs, ctx.Params.Field("client_id").Errorf("no client ID: it names the session"))
	}
	if len(s.Topics) == 0 {
		errs = append(errs, ctx.Params.Field("topics").Errorf("no topics"))
	}
	seen := map[string]bool{}
	for _, t := range s.Topics {
		if err := mqttwire.ValidTopicFilter(t); err != nil || seen[t] {
			errs = append(errs, ctx.Params.Field("topics").Errorf("%q is not a topic filter, or named twice", t))
		}
		seen[t] = true
	}
	s.qos = 1
	if s.QoS != nil {
		s.qos = byte(*s.QoS)
	}
	cfg.SessionExpiry = DefaultSessionExpiry
	if s.SessionExpiry > 0 {
		cfg.SessionExpiry = s.SessionExpiry.Std()
	}
	cfg.ReceiveMaximum = DefaultReceiveMaximum
	if s.ReceiveMaximum > 0 {
		cfg.ReceiveMaximum = s.ReceiveMaximum
	}
	s.cfg = cfg
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// Cleanup closes the connection, answering what was acknowledged, and lets
// the codec go.
func (s *Source) Cleanup() {
	s.stop()
	module.Cleanup(s.codec)
}

// Test connects with a client ID of its own and a clean session, and
// subscribes to the topics, each of which must be granted.
func (s *Source) Test(ctx context.Context) error {
	cfg := s.cfg
	cfg.ClientID, cfg.CleanStart, cfg.SessionExpiry = testID(s.ClientID), true, 0
	c, err := connect(ctx, cfg, s.Servers, s.dial)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Subscribe(ctx, s.subscriptions()...)
	return err
}

func (s *Source) subscriptions() []mqttwire.Subscription {
	subs := make([]mqttwire.Subscription, len(s.Topics))
	for i, t := range s.Topics {
		// Retained messages only when the subscription is new: not again
		// at each restart of a session.
		subs[i] = mqttwire.Subscription{Filter: t, QoS: s.qos, RetainHandling: 1}
	}
	return subs
}

// Run connects, resuming the session, unsubscribes from the filters the
// checkpoint names and topics no longer does, subscribes, and emits the
// messages as they come, until ctx ends or the connection is lost. Its
// connection stays up after ctx ends, so that the records the sinks write
// then are acknowledged still, until Cleanup or the next Run.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	s.stop()
	var from position
	if r.From != nil {
		_ = json.Unmarshal(r.From, &from)
	}
	c, err := connect(ctx, s.cfg, s.Servers, s.dial)
	if err != nil {
		return err
	}
	var id [6]byte
	_, _ = rand.Read(id[:])
	st := &session{c: c, run: hex.EncodeToString(id[:]), wake: make(chan struct{}, 1), acked: make(chan struct{}, 1), quit: make(chan struct{}),
		filters: s.Topics}
	if c.SessionPresent() {
		for _, f := range from.Filters {
			if !slices.Contains(s.Topics, f) {
				st.dropped = append(st.dropped, f)
			}
		}
		st.filters = append(slices.Clone(s.Topics), st.dropped...)
	}
	s.mu.Lock()
	s.cur = st
	s.mu.Unlock()
	go s.acker(st)
	// The filters subscribed are the checkpoint from the start: a run
	// killed before its first message is acknowledged still knows them.
	if err := r.SaveStart(s.position(st, 0)); err != nil {
		s.stop()
		return err
	}
	// The broker may send the session's messages before the SUBACK, more
	// of them than the client holds: they are taken while it subscribes.
	subscribed := make(chan error, 1)
	go func() {
		subscribed <- s.subscribe(ctx, st)
	}()
	idle := DefaultIdle
	if s.Idle > 0 {
		idle = s.Idle.Std()
	}
	var idleC <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.Done():
			s.stop()
			return c.Err()
		case err := <-subscribed:
			if err != nil {
				s.stop()
				return err
			}
			if r.Once {
				timer = time.NewTimer(idle)
				idleC = timer.C
			}
		case m := <-c.Messages():
			if err := s.emit(st, r, m); err != nil {
				return err
			}
			if timer != nil {
				timer.Reset(idle)
			}
		case <-idleC:
			return s.finish(ctx, st)
		}
	}
}

// subscribe unsubscribes from the filters dropped, then subscribes to
// topics.
func (s *Source) subscribe(ctx context.Context, st *session) error {
	if len(st.dropped) > 0 {
		uctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
		_, err := st.c.Unsubscribe(uctx, st.dropped...)
		cancel()
		if err != nil {
			return err
		}
		s.mu.Lock()
		st.filters = s.Topics
		s.mu.Unlock()
	}
	sctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	_, err := st.c.Subscribe(sctx, s.subscriptions()...)
	return err
}

// position is the position of the run's message seq; s.mu is held, or
// the run not yet shared.
func (s *Source) position(st *session, seq uint64) fitsync.Position {
	b, _ := json.Marshal(position{Run: st.run, Seq: seq, Filters: st.filters})
	return b
}

// unwanted reports whether a message is of the filters dropped alone:
// queued before their UNSUBSCRIBE, it is acknowledged and not emitted.
// A topic matching a filter of topics too is emitted: a broker that
// rewrites topics (EMQX's rewrite rules) may send what the filters ask
// under a topic they do not match, so topics are checked only against
// the filters dropped.
func (s *Source) unwanted(st *session, topic string) bool {
	match := func(filters []string) bool {
		for _, f := range filters {
			if mqttwire.MatchTopic(f, topic) {
				return true
			}
		}
		return false
	}
	return len(st.dropped) > 0 && match(st.dropped) && !match(s.Topics)
}

// finish waits, in a job, for every message emitted to be acknowledged,
// and closes the connection.
func (s *Source) finish(ctx context.Context, st *session) error {
	for {
		s.mu.Lock()
		n := len(st.waiting)
		s.mu.Unlock()
		if n == 0 {
			s.stop()
			return nil
		}
		select {
		case <-st.acked:
		case <-ctx.Done():
			return ctx.Err()
		case <-st.c.Done():
			s.stop()
			return st.c.Err()
		}
	}
}

// stop closes the current run's connection, if any, after answering what
// was acknowledged.
func (s *Source) stop() {
	s.mu.Lock()
	st := s.cur
	s.cur = nil
	s.mu.Unlock()
	if st == nil {
		return
	}
	close(st.quit)
	st.ackMu.Lock()
	s.answer(st)
	st.stopped = true
	st.ackMu.Unlock()
	st.c.Close()
}

// acker answers, on a goroutine of the run's, the messages acknowledged.
func (s *Source) acker(st *session) {
	for {
		select {
		case <-st.wake:
		case <-st.quit:
			return
		}
		st.ackMu.Lock()
		if !st.stopped {
			s.answer(st)
		}
		st.ackMu.Unlock()
	}
}

// answer sends the broker the acknowledgements of ackq, in order; st.ackMu
// is held.
func (s *Source) answer(st *session) {
	s.mu.Lock()
	q := st.ackq
	st.ackq = nil
	s.mu.Unlock()
	for _, p := range q {
		if st.c.Ack(p.m) != nil {
			return // the connection is lost: Run says so
		}
	}
}

// emit makes the message's records and emits them, the last carrying the
// position its acknowledgement waits for.
func (s *Source) emit(st *session, r *fitsync.Run, m *mqttwire.Message) error {
	var recs []*fitsync.Record
	if !s.unwanted(st, m.Topic) {
		recs = s.records(m)
	}
	s.mu.Lock()
	st.seq++
	p := &pending{seq: st.seq, m: m, free: len(recs) == 0}
	st.waiting = append(st.waiting, p)
	if p.free {
		s.release(st, 0)
	}
	pos := s.position(st, p.seq)
	s.mu.Unlock()
	if p.free {
		return nil
	}
	tx := ""
	if len(recs) > 1 {
		tx = "mqtt-" + st.run + "-" + strconv.FormatUint(p.seq, 10)
	}
	for i, rec := range recs {
		rec.Tx = fitsync.Tx{ID: tx, First: tx != "" && i == 0, Last: tx != "" && i == len(recs)-1}
		rec.Position = nil
		if i == len(recs)-1 {
			rec.Position = pos
		}
		if err := r.Emit(rec); err != nil {
			return err
		}
	}
	return nil
}

// release moves the first n messages waiting, and the free ones after
// them, to ackq, and wakes the acker; s.mu is held.
func (s *Source) release(st *session, n int) {
	for n < len(st.waiting) && st.waiting[n].free {
		n++
	}
	if n == 0 {
		return
	}
	st.ackq = append(st.ackq, st.waiting[:n]...)
	st.waiting = st.waiting[n:]
	for _, ch := range []chan struct{}{st.wake, st.acked} {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Acknowledged answers the broker for every message up to p.
func (s *Source) Acknowledged(p fitsync.Position) {
	var at position
	if json.Unmarshal(p, &at) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.cur
	if st == nil || at.Run != st.run {
		return
	}
	n := 0
	for n < len(st.waiting) && st.waiting[n].seq <= at.Seq {
		n++
	}
	s.release(st, n)
}

// records makes a message's records.
func (s *Source) records(m *mqttwire.Message) []*fitsync.Record {
	var recs []*fitsync.Record
	if s.codec == nil {
		recs = append(recs, &fitsync.Record{Op: fitsync.OpMessage, After: plainRow(m)})
	} else if err := s.codec.Decode(m.Payload, func(r *fitsync.Record) { recs = append(recs, r) }); err != nil {
		recs = append(recs[:0], &fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(
			fitsync.Field{Name: "topic", Value: fitsync.String(m.Topic)},
			fitsync.Field{Name: "payload", Value: fitsync.Bytes(m.Payload)},
			fitsync.Field{Name: "error", Value: fitsync.String(err.Error())})})
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	at := now()
	for _, r := range recs {
		r.Source = "mqtt"
		if s.Namespace != "" {
			r.Namespace = s.Namespace
		} else if r.Namespace == "" {
			r.Namespace = m.Topic
		}
		if r.Key.IsNull() {
			r.Key = fitsync.String(m.Topic)
		}
		if r.CommitTime.IsZero() {
			r.CommitTime = at
		}
	}
	return recs
}

// plainRow is a message as the source's own form holds it.
func plainRow(m *mqttwire.Message) fitsync.Value {
	fields := []fitsync.Field{
		{Name: "topic", Value: fitsync.String(m.Topic)},
		{Name: "payload", Value: fitsync.Bytes(m.Payload)},
		{Name: "qos", Value: fitsync.Int(int64(m.QoS))},
		{Name: "retain", Value: fitsync.Bool(m.Retain)},
	}
	if m.ContentType != "" {
		fields = append(fields, fitsync.Field{Name: "content_type", Value: fitsync.String(m.ContentType)})
	}
	if m.ResponseTopic != "" {
		fields = append(fields, fitsync.Field{Name: "response_topic", Value: fitsync.String(m.ResponseTopic)})
	}
	if m.CorrelationData != nil {
		fields = append(fields, fitsync.Field{Name: "correlation_data", Value: fitsync.Bytes(m.CorrelationData)})
	}
	if len(m.UserProperties) > 0 {
		var ups []fitsync.Field
		at := map[string]int{}
		for _, u := range m.UserProperties {
			i, ok := at[u.Key]
			if !ok {
				at[u.Key] = len(ups)
				ups = append(ups, fitsync.Field{Name: u.Key, Value: fitsync.String(u.Value)})
				continue
			}
			if v := ups[i].Value; v.Kind() == fitsync.KindArray {
				ups[i].Value = fitsync.Array(append(v.Elems(), fitsync.String(u.Value))...)
			} else {
				ups[i].Value = fitsync.Array(v, fitsync.String(u.Value))
			}
		}
		fields = append(fields, fitsync.Field{Name: "user_properties", Value: fitsync.Map(ups...)})
	}
	return fitsync.Map(fields...)
}

var (
	_ fitsync.Source       = (*Source)(nil)
	_ fitsync.Acknowledger = (*Source)(nil)
	_ module.Cleaner       = (*Source)(nil)
)
