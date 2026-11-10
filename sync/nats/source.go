package syncnats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	natswire "github.com/lesismal/fit/sync/nats/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".nats", New: func() any { return new(Source) },
		Doc: "Reads a NATS JetStream stream through a durable pull consumer, each message acknowledged once every sink has it, so that none is lost to a crash: at least once. The stream sequence is in the record's position and the pipeline's checkpoint; a consumer fit makes (create_consumer) is made again from the checkpoint when it is lost, or when a run finds it holding messages delivered and not acknowledged, so that they come again first and in order. Or, with mode core, core NATS subjects, at most once: what arrives while fit is down, or faster than it keeps up, is lost, for telemetry. Each message goes through a codec (raw when none is named) into records, the subject their namespace unless the codec or namespace sets one; a message record holds the subject, headers, stream, sequence and time beside its value. In a job (mode once) it reads up to the stream's last message as it was when the job started."})
}

// The defaults of the source's params.
const (
	DefaultBatch         = 256
	DefaultMaxWait       = 5 * time.Second
	DefaultAckWait       = time.Minute
	DefaultMaxAckPending = 10000
	DefaultBuffer        = 10000
)

// Source is the source nats.
type Source struct {
	Servers        []string          `json:"servers" fit:"required" doc:"The servers, host:port each; the others of the cluster are learnt from them, and the client connects to another when one fails."`
	Name           string            `json:"name,omitempty" default:"\"fit-sync\"" doc:"The connection's name, in the servers' monitoring. fit-sync when left out."`
	TLS            bool              `json:"tls,omitempty" doc:"Connect over TLS, the servers' certificates checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile         string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the servers' are checked against, in place of the system's roots; needs tls."`
	CertFile       string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, for servers that verify clients; needs tls and key_file."`
	KeyFile        string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName     string            `json:"server_name,omitempty" doc:"The name the servers' certificates must have; each server's host when left out."`
	TokenEnv       string            `json:"token_env,omitempty" doc:"The environment variable holding a token to authenticate with, read when the pipeline loads."`
	Username       string            `json:"username,omitempty" doc:"The user to authenticate as, with password_env."`
	PasswordEnv    string            `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	NKeyFile       string            `json:"nkey_file,omitempty" doc:"A file holding a user's NKey seed (SU...), whose key signs the server's nonce."`
	CredsFile      string            `json:"creds_file,omitempty" doc:"A creds file, as nsc writes it, holding a user's JWT and NKey seed, for servers in operator mode."`
	Timeout        config.Duration   `json:"timeout,omitempty" default:"\"10s\"" doc:"How long connecting, and each answer of JetStream's API beyond a pull's wait, may take. 10s when left out or 0."`
	Mode           string            `json:"mode,omitempty" enum:"jetstream,core" default:"\"jetstream\"" doc:"jetstream, a stream read through a durable consumer, at least once; core, subjects subscribed to, at most once. jetstream when left out."`
	Stream         string            `json:"stream,omitempty" doc:"The stream read; needed with mode jetstream."`
	Consumer       string            `json:"consumer,omitempty" doc:"The durable pull consumer the stream is read through; needed with mode jetstream. One pipeline a consumer. A consumer fit does not make is read from its acknowledgement floor, and needs ack policy explicit (none reads at most once)."`
	CreateConsumer bool              `json:"create_consumer,omitempty" doc:"Make the consumer when it does not exist, and make it again when it is lost or a run finds it holding messages delivered and not acknowledged: from after the checkpoint's sequence (or its acknowledgement floor, when later), its ack policy all, its filter subjects, start, ack_wait and max_ack_pending these params'. A consumer that does not exist is an error when left out."`
	Subjects       []string          `json:"subjects,omitempty" doc:"With mode jetstream, the filter subjects of a consumer made (the whole stream when left out); with mode core, the subjects subscribed to, which are needed; * and > are wildcards."`
	Queue          string            `json:"queue,omitempty" doc:"With mode core, the queue group the subscriptions join, so that several fits share the messages."`
	Start          string            `json:"start,omitempty" enum:"all,new" default:"\"all\"" doc:"Where a consumer made without a checkpoint starts: all, the stream's first message; new, after its last, reading only what comes after. all when left out."`
	AckWait        config.Duration   `json:"ack_wait,omitempty" default:"\"1m\"" doc:"A consumer made's ack_wait: how long the server waits for a message's acknowledgement before it delivers it again. Keep it above the time the sinks take. 1m when left out or 0."`
	MaxAckPending  int               `json:"max_ack_pending,omitempty" min:"0" default:"10000" doc:"A consumer made's max_ack_pending: the most messages delivered and not acknowledged. 10000 when left out or 0."`
	Batch          int               `json:"batch,omitempty" min:"0" max:"10000" default:"256" doc:"The most messages one pull asks for. 256 when left out or 0."`
	MaxWait        config.Duration   `json:"max_wait,omitempty" default:"\"5s\"" doc:"How long a pull waits for messages. 5s when left out or 0."`
	Buffer         int               `json:"buffer,omitempty" min:"0" max:"1000000" default:"10000" doc:"With mode core, the most messages held between the connection and the pipeline; more are dropped. 10000 when left out or 0."`
	Codec          *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes records of a message's data: raw when left out, a message record holding it as after's value."`
	Namespace      string            `json:"namespace,omitempty" doc:"The records' namespace. The codec's, or the message's subject, when left out."`
	MetadataField  string            `json:"metadata_field,omitempty" doc:"A field of after that the message's subject, headers, stream, sequence and time go in, as a map, in records a codec makes rows of; a message record holds them beside its value without it."`

	cfg     natswire.Config
	timeout time.Duration
	codec   fitsync.Codec
	cl      holder

	// acks are the messages emitted and not yet acknowledged to the
	// server, and the stream sequence every sink has, which Acknowledged
	// raises and the acker goroutine acts on.
	ackMu    sync.Mutex
	pending  []pendingAck
	acked    uint64
	policy   string
	ackKick  chan struct{}
	ackStop  chan struct{}
	ackDone  chan struct{}
	ackStart sync.Once
}

// pendingAck is a message to acknowledge once the sinks have its sequence.
type pendingAck struct {
	seq   uint64
	reply string
}

// position is the source's: the stream sequence of the last message read.
type position struct {
	Stream string `json:"stream"`
	Seq    uint64 `json:"seq"`
}

// Provision checks the params and reads the secrets and TLS files; nothing
// is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	cfg, t, errs := wireConfig(ctx, connParams{Servers: s.Servers, Name: s.Name, TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile,
		ServerName: s.ServerName, TokenEnv: s.TokenEnv, Username: s.Username, PasswordEnv: s.PasswordEnv, NKeyFile: s.NKeyFile, CredsFile: s.CredsFile, Timeout: s.Timeout})
	s.cfg, s.timeout = cfg, t
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	for _, f := range s.Subjects {
		if !validFilter(f) {
			add("subjects", "%q is not a subject", f)
		}
	}
	if s.Mode == "core" {
		if len(s.Subjects) == 0 {
			add("subjects", "mode core needs subjects")
		}
		if s.Queue != "" && !validName(s.Queue) {
			add("queue", "%q is not a queue group's name", s.Queue)
		}
	} else {
		if !validName(s.Stream) {
			add("stream", "mode jetstream needs a stream, and %q is not a stream's name", s.Stream)
		}
		if !validName(s.Consumer) {
			add("consumer", "mode jetstream needs a consumer, and %q is not a consumer's name", s.Consumer)
		}
		if s.Queue != "" {
			add("queue", "queue is for mode core")
		}
	}
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// Cleanup stops the acknowledgements, closes the client and lets the
// codec go.
func (s *Source) Cleanup() {
	if s.ackStop != nil {
		close(s.ackStop)
		<-s.ackDone
	}
	s.cl.close()
	module.Cleanup(s.codec)
}

func (s *Source) client(ctx context.Context) (*natswire.Client, error) {
	s.cl.cfg = s.cfg
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.cl.get(ctx)
}

// Test connects, and, with mode jetstream, reads the stream's info and the
// consumer's, unless it is to be made.
func (s *Source) Test(ctx context.Context) error {
	h := holder{cfg: s.cfg}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	c, err := h.get(ctx)
	if err != nil {
		return err
	}
	defer h.close()
	if s.Mode == "core" {
		return c.Flush(ctx)
	}
	if _, err := c.StreamInfo(ctx, s.Stream); err != nil {
		return fmt.Errorf("nats: stream %s: %w", s.Stream, err)
	}
	if _, err := c.ConsumerInfo(ctx, s.Stream, s.Consumer); err != nil && !(s.CreateConsumer && natswire.IsAPIError(err, natswire.ErrCodeConsumerNotFound)) {
		return fmt.Errorf("nats: consumer %s of %s: %w", s.Consumer, s.Stream, err)
	}
	return nil
}

// Run reads the stream from its consumer, or the subjects, until ctx ends,
// or, in a job, until the stream's last message as it was.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	c, err := s.client(ctx)
	if err != nil {
		return err
	}
	if s.Mode == "core" {
		return s.runCore(ctx, c, r)
	}
	var from position
	if r.From != nil {
		if err := json.Unmarshal(r.From, &from); err != nil {
			return fmt.Errorf("nats: the checkpoint %s: %w", r.From, err)
		}
		if from.Stream != s.Stream {
			return fmt.Errorf("nats: the checkpoint is of stream %s, not %s", from.Stream, s.Stream)
		}
	}
	rd := &reader{s: s, c: c, run: r, done: from.Seq}
	s.ackMu.Lock()
	s.pending, s.acked = nil, from.Seq
	s.ackMu.Unlock()
	s.ackStart.Do(s.startAcker)
	if err := rd.consumer(ctx, true, false); err != nil {
		return err
	}
	if r.From == nil {
		pos, _ := json.Marshal(position{Stream: s.Stream, Seq: rd.done})
		if err := r.SaveStart(pos); err != nil {
			return err
		}
	}
	if r.Once {
		si, err := s.streamInfo(ctx, c)
		if err != nil {
			return err
		}
		if rd.end = si.State.LastSeq; rd.end <= rd.emitted {
			return nil
		}
	}
	return rd.loop(ctx)
}

func (s *Source) streamInfo(ctx context.Context, c *natswire.Client) (*natswire.StreamInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	si, err := c.StreamInfo(ctx, s.Stream)
	if err != nil {
		return nil, fmt.Errorf("nats: stream %s: %w", s.Stream, err)
	}
	return si, nil
}

// reader is a run of the source on a stream.
type reader struct {
	s   *Source
	c   *natswire.Client
	run *fitsync.Run
	// done is the sequence up to which the sinks have every message, as
	// the checkpoint says; emitted, the last sequence emitted; end, a
	// job's last sequence, 0 in a stream.
	done, emitted, end uint64
	// dseq is the consumer sequence of the last delivery taken from a
	// consumer fit made; check, whether the next must follow it.
	dseq  uint64
	check bool
}

// errLost is take's when a delivery of a consumer fit made is not the one
// after the last taken: the consumer delivered messages this run never
// read, a pull's answer lost to a reconnect, or a run killed before it,
// which a consumer's info read while its leader moves can hide.
var errLost = errors.New("nats: deliveries this run did not read")

// consumer finds the consumer, or makes it, when the run starts (start) and
// when a pull says it is gone. A consumer fit makes (create_consumer)
// delivers in order from the floor, the sequence up to which the sinks have
// every message: the checkpoint's, or the consumer's acknowledgement floor,
// whichever is later. One that delivered past its floor messages nobody
// acknowledged, as a run killed leaves it, is made again from the floor
// when a run starts, so that those messages come again first, in order,
// rather than after ack_wait among later ones, where the run would take a
// later one's sequence for theirs. A consumer of the user's own is read as
// it is, from its acknowledgement floor, its ack policy explicit (or none,
// at most once): under all, acknowledging a message would acknowledge one
// delivered again before it and not read yet.
//
// A consumer fit makes is made again too, from the floor, when it delivered
// messages this run never read, which a later message acknowledged under
// ack all would acknowledge unread: when take finds a gap in its consumer
// sequence, which every delivery raises, redeliveries too (fresh), and
// when, found again after a pull failed (409), it delivered past the last
// delivery taken (to that pull). A consumer's info read while its leader
// moves can say nothing is pending when a killed run left messages
// delivered; the gap shows it at the first delivery.
func (rd *reader) consumer(ctx context.Context, start, fresh bool) error {
	s := rd.s
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	ci, err := rd.c.ConsumerInfo(ctx, s.Stream, s.Consumer)
	missing := natswire.IsAPIError(err, natswire.ErrCodeConsumerNotFound)
	if err != nil && !missing {
		return fmt.Errorf("nats: consumer %s of %s: %w", s.Consumer, s.Stream, err)
	}
	switch {
	case missing && !s.CreateConsumer:
		return fmt.Errorf("nats: no consumer %s of stream %s, and create_consumer is not set", s.Consumer, s.Stream)
	case !s.CreateConsumer:
		if ci.Config.AckPolicy == "all" {
			return fmt.Errorf("nats: consumer %s of %s has ack policy all: one fit does not make needs explicit, or none", s.Consumer, s.Stream)
		}
		if start {
			rd.done = ci.AckFloor.Stream
		}
	default:
		floor := max(rd.done, s.ackedSeq())
		if ci != nil {
			floor = max(floor, ci.AckFloor.Stream)
		}
		lost := fresh || !start && ci != nil && ci.Delivered.Consumer != rd.dseq
		if ci != nil && (lost || start && (ci.NumAckPending > 0 || ci.Delivered.Stream > floor)) {
			if err := rd.c.DeleteConsumer(ctx, s.Stream, s.Consumer); err != nil && !natswire.IsAPIError(err, natswire.ErrCodeConsumerNotFound) {
				return fmt.Errorf("nats: consumer %s of %s: %w", s.Consumer, s.Stream, err)
			}
			ci = nil
		}
		if ci == nil {
			if ci, err = rd.c.CreateConsumer(ctx, s.Stream, s.consumerConfig(floor)); err != nil {
				return fmt.Errorf("nats: consumer %s of %s: %w", s.Consumer, s.Stream, err)
			}
		}
		if start {
			rd.done = max(floor, ci.AckFloor.Stream)
		}
		rd.dseq, rd.check = ci.Delivered.Consumer, true
	}
	if start {
		rd.emitted = rd.done
	}
	s.ackMu.Lock()
	s.policy = ci.Config.AckPolicy
	s.ackMu.Unlock()
	return nil
}

// consumerConfig is the consumer made, after sequence after when it is
// not 0.
func (s *Source) consumerConfig(after uint64) natswire.ConsumerConfig {
	cc := natswire.ConsumerConfig{Durable: s.Consumer, AckPolicy: "all", AckWait: int64(DefaultAckWait), MaxAckPending: DefaultMaxAckPending,
		ReplayPolicy: "instant", DeliverPolicy: "all"}
	if s.AckWait > 0 {
		cc.AckWait = int64(s.AckWait.Std())
	}
	if s.MaxAckPending > 0 {
		cc.MaxAckPending = s.MaxAckPending
	}
	switch {
	case after > 0:
		cc.DeliverPolicy, cc.OptStartSeq = "by_start_sequence", after+1
	case s.Start == "new":
		cc.DeliverPolicy = "new"
	}
	switch len(s.Subjects) {
	case 0:
	case 1:
		cc.FilterSubject = s.Subjects[0]
	default:
		cc.FilterSubjects = s.Subjects
	}
	return cc
}

func (s *Source) ackedSeq() uint64 {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	return s.acked
}

// loop pulls messages until ctx ends, or a job's end.
func (rd *reader) loop(ctx context.Context) error {
	s := rd.s
	// A pull takes what the consumer has now, without waiting (no_wait);
	// when it has nothing, a pull of one waits up to max_wait, so that a
	// message is read as soon as it comes, and those after it with the
	// next pull, rather than a pull waiting for its whole batch.
	now := natswire.FetchRequest{Batch: orDefault(s.Batch, DefaultBatch), NoWait: true}
	wait := natswire.FetchRequest{Batch: 1, Expires: DefaultMaxWait}
	if s.MaxWait > 0 {
		wait.Expires = s.MaxWait.Std()
	}
	idle := false
	for {
		req := now
		if idle {
			req = wait
		}
		fctx, cancel := context.WithTimeout(ctx, wait.Expires+s.timeout)
		msgs, err := rd.c.Fetch(fctx, s.Stream, s.Consumer, req)
		cancel()
		idle = len(msgs) == 0 && !idle
		if err := ctx.Err(); err != nil {
			return err
		}
		lost := false
		for _, m := range msgs {
			if err := rd.take(m); errors.Is(err, errLost) {
				// Those after it come again from the consumer made again.
				lost = true
				break
			} else if err != nil {
				return err
			}
			if rd.end > 0 && rd.emitted >= rd.end {
				return nil
			}
		}
		if lost {
			if err := rd.consumer(ctx, false, true); err != nil {
				return err
			}
			idle = false
			continue
		}
		var se *natswire.StatusError
		switch {
		case errors.As(err, &se) && se.Status == 409:
			// The consumer was deleted, or its leader moved: found again,
			// or made again after what the sinks have.
			if err := rd.consumer(ctx, false, false); err != nil {
				return err
			}
		case err != nil && !errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("nats: pull from %s of %s: %w", s.Consumer, s.Stream, err)
		case len(msgs) == 0 && rd.end > 0:
			if fin, err := rd.finished(ctx); fin || err != nil {
				return err
			}
		}
	}
}

// finished reports, in a job, whether the consumer has nothing left to
// deliver short of the end: no messages pending, none waiting for an
// acknowledgement but those emitted.
func (rd *reader) finished(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, rd.s.timeout)
	defer cancel()
	ci, err := rd.c.ConsumerInfo(ctx, rd.s.Stream, rd.s.Consumer)
	if err != nil {
		return false, fmt.Errorf("nats: consumer %s of %s: %w", rd.s.Consumer, rd.s.Stream, err)
	}
	rd.s.ackMu.Lock()
	ours := len(rd.s.pending)
	rd.s.ackMu.Unlock()
	return ci.NumPending == 0 && ci.NumAckPending <= ours, nil
}

// take emits a message's records, or, for a message the sinks have, only
// acknowledges it, and for one emitted and not yet acknowledged (delivered
// again), only keeps its reply to acknowledge it by.
func (rd *reader) take(m *natswire.Msg) error {
	meta, err := natswire.ParseMeta(m.Reply)
	if err != nil {
		return err
	}
	s := rd.s
	if rd.check {
		if meta.ConsumerSeq != rd.dseq+1 {
			return errLost
		}
		rd.dseq = meta.ConsumerSeq
	}
	if meta.StreamSeq <= rd.done {
		return rd.c.Ack(m.Reply)
	}
	if s.addPending(meta.StreamSeq, m.Reply) {
		return nil
	}
	recs := s.records(m, &meta)
	rd.emitted = max(rd.emitted, meta.StreamSeq)
	pos, _ := json.Marshal(position{Stream: s.Stream, Seq: meta.StreamSeq})
	recs[len(recs)-1].Position = pos
	for _, rec := range recs {
		if err := rd.run.Emit(rec); err != nil {
			return err
		}
	}
	rd.run.ReportLag(int64(meta.Pending), fitsync.LagMessages)
	return nil
}

// addPending keeps a message to acknowledge, in sequence order, and
// reports whether it was kept already: emitted, and delivered again, its
// new reply kept in place of the old, as a consumer made again has it.
func (s *Source) addPending(seq uint64, reply string) bool {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	i := sort.Search(len(s.pending), func(i int) bool { return s.pending[i].seq >= seq })
	if i < len(s.pending) && s.pending[i].seq == seq {
		s.pending[i].reply = reply
		return true
	}
	s.pending = slices.Insert(s.pending, i, pendingAck{seq, reply})
	return false
}

// Acknowledged has the messages up to p's sequence acknowledged to the
// server, on the acker's goroutine.
func (s *Source) Acknowledged(p fitsync.Position) {
	var pos position
	if json.Unmarshal(p, &pos) != nil || pos.Stream != s.Stream {
		return
	}
	s.ackMu.Lock()
	s.acked = max(s.acked, pos.Seq)
	s.ackMu.Unlock()
	if s.ackKick != nil {
		select {
		case s.ackKick <- struct{}{}:
		default:
		}
	}
}

func (s *Source) startAcker() {
	s.ackKick, s.ackStop, s.ackDone = make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	go s.acker()
}

// acker acknowledges what the sinks have: under the ack policy all, the
// last message alone, which covers those before it; under explicit, each.
func (s *Source) acker() {
	defer close(s.ackDone)
	for {
		select {
		case <-s.ackStop:
			return
		case <-s.ackKick:
		}
		s.ackMu.Lock()
		n := sort.Search(len(s.pending), func(i int) bool { return s.pending[i].seq > s.acked })
		due := s.pending[:n:n]
		s.pending = s.pending[n:]
		policy := s.policy
		s.ackMu.Unlock()
		c := s.cl.current()
		if c == nil || len(due) == 0 || policy == "none" {
			continue
		}
		if policy == "all" {
			due = due[len(due)-1:]
		}
		for _, p := range due {
			c.Ack(p.reply) // a message not acknowledged is delivered again
		}
	}
}

// runCore reads core subjects until ctx ends.
func (s *Source) runCore(ctx context.Context, c *natswire.Client, r *fitsync.Run) error {
	if r.Once {
		return errors.New("nats: core subjects have no end to read to: a job needs mode jetstream")
	}
	ch := make(chan *natswire.Msg, orDefault(s.Buffer, DefaultBuffer))
	for _, subj := range s.Subjects {
		sub, err := c.Subscribe(subj, s.Queue, ch)
		if err != nil {
			return err
		}
		defer sub.Unsubscribe()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m := <-ch:
			for _, rec := range s.records(m, nil) {
				if err := r.Emit(rec); err != nil {
					return err
				}
			}
		}
	}
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// records makes a message's records, at least one: a message record of a
// codec's error.
func (s *Source) records(m *natswire.Msg, meta *natswire.Meta) []*fitsync.Record {
	var recs []*fitsync.Record
	if err := s.codec.Decode(m.Data, func(r *fitsync.Record) { recs = append(recs, r) }); err != nil || len(recs) == 0 {
		why := "the codec made no record of it"
		if err != nil {
			why = err.Error()
		}
		recs = []*fitsync.Record{{Op: fitsync.OpMessage, After: fitsync.Map(
			fitsync.Field{Name: "value", Value: fitsync.Bytes(m.Data)},
			fitsync.Field{Name: "error", Value: fitsync.String(why)})}}
	}
	md := s.metadata(m, meta)
	var txID string
	if len(recs) > 1 {
		txID = m.Subject
		if meta != nil {
			txID = s.Stream + "@" + strconv.FormatUint(meta.StreamSeq, 10)
		}
	}
	for i, r := range recs {
		r.Source, r.Position, r.Origin = "nats", nil, fitsync.Origin{}
		if meta != nil {
			r.Origin = fitsync.Origin{Stream: s.Stream, N: [4]uint64{meta.StreamSeq, uint64(i)}}
		}
		if s.Namespace != "" || r.Namespace == "" {
			r.Namespace = s.Namespace
			if r.Namespace == "" {
				r.Namespace = m.Subject
			}
		}
		if r.CommitTime.IsZero() && meta != nil {
			r.CommitTime = meta.Timestamp
		}
		if txID != "" {
			id := r.Tx.ID
			if id == "" {
				id = txID
			}
			r.Tx = fitsync.Tx{ID: id, First: i == 0, Last: i == len(recs)-1}
		} else {
			r.Tx = fitsync.Tx{}
		}
		switch {
		case r.After.Kind() != fitsync.KindMap && r.After.Kind() != fitsync.KindNull:
		case s.MetadataField != "":
			r.After = withFields(r.After, fitsync.Field{Name: s.MetadataField, Value: fitsync.Map(md...)})
		case r.Op == fitsync.OpMessage:
			r.After = withFields(r.After, md...)
		}
	}
	return recs
}

// metadata is what a message's record says of it: its subject, headers,
// and, from JetStream, its stream, sequence and time.
func (s *Source) metadata(m *natswire.Msg, meta *natswire.Meta) []fitsync.Field {
	md := []fitsync.Field{{Name: "subject", Value: fitsync.String(m.Subject)}}
	if len(m.Header) > 0 {
		names := make([]string, 0, len(m.Header))
		for k := range m.Header {
			names = append(names, k)
		}
		sort.Strings(names)
		hs := make([]fitsync.Field, 0, len(names))
		for _, k := range names {
			v := m.Header[k]
			if len(v) == 1 {
				hs = append(hs, fitsync.Field{Name: k, Value: fitsync.String(v[0])})
				continue
			}
			vs := make([]fitsync.Value, len(v))
			for i, x := range v {
				vs[i] = fitsync.String(x)
			}
			hs = append(hs, fitsync.Field{Name: k, Value: fitsync.Array(vs...)})
		}
		md = append(md, fitsync.Field{Name: "headers", Value: fitsync.Map(hs...)})
	}
	if meta != nil {
		md = append(md, fitsync.Field{Name: "stream", Value: fitsync.String(meta.Stream)},
			fitsync.Field{Name: "sequence", Value: fitsync.Uint(meta.StreamSeq)},
			fitsync.Field{Name: "time", Value: fitsync.Time(meta.Timestamp)})
	}
	return md
}

// withFields returns row, a map or null, with fields added after its own,
// but those it has already.
func withFields(row fitsync.Value, fields ...fitsync.Field) fitsync.Value {
	out := append([]fitsync.Field(nil), row.Fields()...)
	for _, f := range fields {
		if _, ok := row.Get(f.Name); !ok {
			out = append(out, f)
		}
	}
	return fitsync.Map(out...)
}
