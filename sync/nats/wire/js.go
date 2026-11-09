package natswire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// APIPrefix is where JetStream's API is, in the account's own domain.
const APIPrefix = "$JS.API."

// JetStream's error codes the connectors act on.
const (
	ErrCodeConsumerNotFound = 10014
	ErrCodeStreamNotFound   = 10059
)

// APIError is a JetStream API's error answer.
type APIError struct {
	Code        int    `json:"code"`
	ErrCode     int    `json:"err_code"`
	Description string `json:"description"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("nats: jetstream: %s (%d, %d)", e.Description, e.Code, e.ErrCode)
}

// IsAPIError reports whether err is a JetStream API error of errCode.
func IsAPIError(err error, errCode int) bool {
	var e *APIError
	return errors.As(err, &e) && e.ErrCode == errCode
}

// StatusError is a status message where messages were expected, as a pull
// consumer's 409 when it is deleted.
type StatusError struct {
	Status      int
	Description string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("nats: jetstream: status %d %s", e.Status, e.Description)
}

// StreamConfig is a stream's config, as much of it as fit sets.
type StreamConfig struct {
	Name       string   `json:"name"`
	Subjects   []string `json:"subjects,omitempty"`
	Storage    string   `json:"storage,omitempty"`
	Replicas   int      `json:"num_replicas,omitempty"`
	Duplicates int64    `json:"duplicate_window,omitempty"`
}

// StreamState is where a stream is.
type StreamState struct {
	Msgs     uint64 `json:"messages"`
	Bytes    uint64 `json:"bytes"`
	FirstSeq uint64 `json:"first_seq"`
	LastSeq  uint64 `json:"last_seq"`
}

// StreamInfo is STREAM.INFO's answer.
type StreamInfo struct {
	Config StreamConfig `json:"config"`
	State  StreamState  `json:"state"`
}

// ConsumerConfig is a consumer's config, as much of it as fit sets.
type ConsumerConfig struct {
	Durable        string   `json:"durable_name,omitempty"`
	DeliverPolicy  string   `json:"deliver_policy,omitempty"`
	OptStartSeq    uint64   `json:"opt_start_seq,omitempty"`
	AckPolicy      string   `json:"ack_policy,omitempty"`
	AckWait        int64    `json:"ack_wait,omitempty"`
	MaxAckPending  int      `json:"max_ack_pending,omitempty"`
	MaxDeliver     int      `json:"max_deliver,omitempty"`
	FilterSubject  string   `json:"filter_subject,omitempty"`
	FilterSubjects []string `json:"filter_subjects,omitempty"`
	ReplayPolicy   string   `json:"replay_policy,omitempty"`
}

// SequencePair is a consumer's and the stream's sequences of a message.
type SequencePair struct {
	Consumer uint64 `json:"consumer_seq"`
	Stream   uint64 `json:"stream_seq"`
}

// ConsumerInfo is CONSUMER.INFO's answer.
type ConsumerInfo struct {
	Stream        string         `json:"stream_name"`
	Name          string         `json:"name"`
	Config        ConsumerConfig `json:"config"`
	Delivered     SequencePair   `json:"delivered"`
	AckFloor      SequencePair   `json:"ack_floor"`
	NumAckPending int            `json:"num_ack_pending"`
	NumPending    uint64         `json:"num_pending"`
}

// PubAck is JetStream's answer to a publish.
type PubAck struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	Duplicate bool   `json:"duplicate"`
}

// api sends a request to JetStream's API and reads its answer into out.
func (c *Client) api(ctx context.Context, subject string, req, out any) error {
	var body []byte
	if req != nil {
		body, _ = json.Marshal(req)
	}
	m, err := c.Request(ctx, APIPrefix+subject, nil, body)
	if err != nil {
		return err
	}
	return decodeAnswer(m.Data, out)
}

// decodeAnswer reads an API's JSON answer, or its error.
func decodeAnswer(data []byte, out any) error {
	var e struct {
		Error *APIError `json:"error"`
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return fmt.Errorf("nats: jetstream: an answer %.80q: %w", data, err)
	}
	if e.Error != nil {
		return e.Error
	}
	return json.Unmarshal(data, out) // it was JSON above
}

// validName reports whether s may name a stream or consumer.
func validName(s string) bool {
	return validSubject(s) && !strings.ContainsAny(s, ".*>")
}

// StreamInfo returns a stream's config and state.
func (c *Client) StreamInfo(ctx context.Context, stream string) (*StreamInfo, error) {
	if !validName(stream) {
		return nil, fmt.Errorf("nats: %q is not a stream's name", stream)
	}
	var si StreamInfo
	if err := c.api(ctx, "STREAM.INFO."+stream, nil, &si); err != nil {
		return nil, err
	}
	return &si, nil
}

// CreateStream creates a stream, or updates it to cfg.
func (c *Client) CreateStream(ctx context.Context, cfg StreamConfig) (*StreamInfo, error) {
	if !validName(cfg.Name) {
		return nil, fmt.Errorf("nats: %q is not a stream's name", cfg.Name)
	}
	var si StreamInfo
	if err := c.api(ctx, "STREAM.CREATE."+cfg.Name, cfg, &si); err != nil {
		return nil, err
	}
	return &si, nil
}

// ConsumerInfo returns a consumer's config and state.
func (c *Client) ConsumerInfo(ctx context.Context, stream, consumer string) (*ConsumerInfo, error) {
	if !validName(stream) || !validName(consumer) {
		return nil, fmt.Errorf("nats: %q or %q is not a stream's or consumer's name", stream, consumer)
	}
	var ci ConsumerInfo
	if err := c.api(ctx, "CONSUMER.INFO."+stream+"."+consumer, nil, &ci); err != nil {
		return nil, err
	}
	return &ci, nil
}

// CreateConsumer creates a durable consumer of cfg.Durable's name, or
// updates it to cfg (nats-server 2.9 and later).
func (c *Client) CreateConsumer(ctx context.Context, stream string, cfg ConsumerConfig) (*ConsumerInfo, error) {
	if !validName(stream) || !validName(cfg.Durable) {
		return nil, fmt.Errorf("nats: %q or %q is not a stream's or consumer's name", stream, cfg.Durable)
	}
	req := struct {
		Stream string         `json:"stream_name"`
		Config ConsumerConfig `json:"config"`
	}{stream, cfg}
	var ci ConsumerInfo
	if err := c.api(ctx, "CONSUMER.CREATE."+stream+"."+cfg.Durable, req, &ci); err != nil {
		return nil, err
	}
	return &ci, nil
}

// DeleteConsumer deletes a consumer.
func (c *Client) DeleteConsumer(ctx context.Context, stream, consumer string) error {
	if !validName(stream) || !validName(consumer) {
		return fmt.Errorf("nats: %q or %q is not a stream's or consumer's name", stream, consumer)
	}
	var out struct{}
	return c.api(ctx, "CONSUMER.DELETE."+stream+"."+consumer, nil, &out)
}

// FetchRequest is a pull consumer's request for messages.
type FetchRequest struct {
	Batch   int           `json:"batch"`
	Expires time.Duration `json:"expires,omitempty"`
	NoWait  bool          `json:"no_wait,omitempty"`
}

// Fetch asks a pull consumer for up to req.Batch messages and returns
// those that come before the request ends: when the batch is full, at a
// status 404 (no messages, with no_wait) or 408 (the request expired), or
// when ctx ends with some. Another status, as 409 when the consumer is
// deleted or its leader changes, is a *StatusError, with the messages
// before it.
func (c *Client) Fetch(ctx context.Context, stream, consumer string, req FetchRequest) ([]*Msg, error) {
	if !validName(stream) || !validName(consumer) {
		return nil, fmt.Errorf("nats: %q or %q is not a stream's or consumer's name", stream, consumer)
	}
	req.Batch = max(req.Batch, 1)
	ib, err := c.newSubInbox(req.Batch + 1)
	if err != nil {
		return nil, err
	}
	defer ib.Close()
	body, _ := json.Marshal(req)
	if err := c.Publish(APIPrefix+"CONSUMER.MSG.NEXT."+stream+"."+consumer, ib.Subject(), nil, body); err != nil {
		return nil, err
	}
	var msgs []*Msg
	for len(msgs) < req.Batch {
		m, err := ib.Next(ctx)
		if err != nil {
			if len(msgs) > 0 && ctx.Err() != nil {
				return msgs, nil
			}
			return msgs, err
		}
		switch m.Status {
		case 0:
			msgs = append(msgs, m)
		case 100: // a heartbeat
		case 404, 408:
			return msgs, nil
		case 503:
			return msgs, fmt.Errorf("%w for consumer %s of %s", ErrNoResponders, consumer, stream)
		default:
			return msgs, &StatusError{m.Status, m.Description}
		}
	}
	return msgs, nil
}

// Meta is what a JetStream message's reply subject says of it.
type Meta struct {
	Stream, Consumer       string
	Delivered              uint64
	StreamSeq, ConsumerSeq uint64
	Timestamp              time.Time
	Pending                uint64
}

// ParseMeta reads a JetStream message's reply subject: $JS.ACK.<stream>.
// <consumer>.<delivered>.<stream seq>.<consumer seq>.<time>.<pending>, or
// the form with a domain and an account hash before the stream, and a
// token after.
func ParseMeta(reply string) (Meta, error) {
	t := strings.Split(reply, ".")
	if len(t) < 9 || t[0] != "$JS" || t[1] != "ACK" {
		return Meta{}, fmt.Errorf("nats: %q is not a JetStream message's reply", reply)
	}
	if len(t) >= 12 {
		t = t[4:]
	} else if len(t) == 9 {
		t = t[2:]
	} else {
		return Meta{}, fmt.Errorf("nats: %q is not a JetStream message's reply", reply)
	}
	var n [5]uint64
	for i := range n {
		v, err := strconv.ParseUint(t[2+i], 10, 64)
		if err != nil {
			return Meta{}, fmt.Errorf("nats: %q is not a JetStream message's reply", reply)
		}
		n[i] = v
	}
	return Meta{Stream: t[0], Consumer: t[1], Delivered: n[0], StreamSeq: n[1], ConsumerSeq: n[2],
		Timestamp: time.Unix(0, int64(n[3])).UTC(), Pending: n[4]}, nil
}

// Ack acknowledges a JetStream message by its reply subject; under the
// consumer's AckAll policy, every message before it too.
func (c *Client) Ack(reply string) error { return c.Publish(reply, "", nil, []byte("+ACK")) }

// PendingAck is a JetStream publish waiting for its acknowledgement.
type PendingAck struct {
	ib      *Inbox
	subject string
}

// PublishJS publishes to a stream's subject, with hdr (a Nats-Msg-Id for
// the stream's duplicate window), without waiting: Wait waits for the
// stream's acknowledgement. Several may be in flight.
func (c *Client) PublishJS(subject string, hdr Header, data []byte) (*PendingAck, error) {
	ib, err := c.NewInbox(1)
	if err != nil {
		return nil, err
	}
	if err := c.Publish(subject, ib.Subject(), hdr, data); err != nil {
		ib.Close()
		return nil, err
	}
	return &PendingAck{ib, subject}, nil
}

// Wait waits for the publish's acknowledgement: no stream's subject is
// ErrNoResponders.
func (p *PendingAck) Wait(ctx context.Context) (*PubAck, error) {
	defer p.ib.Close()
	m, err := p.ib.Next(ctx)
	if err != nil {
		return nil, fmt.Errorf("nats: publish to %s: %w", p.subject, err)
	}
	if m.Status == 503 {
		return nil, fmt.Errorf("%w: no stream takes %s", ErrNoResponders, p.subject)
	}
	var a PubAck
	if err := decodeAnswer(m.Data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// Cancel stops waiting for the acknowledgement.
func (p *PendingAck) Cancel() { p.ib.Close() }
