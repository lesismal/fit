package syncrabbitmq

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	amqpwire "github.com/lesismal/fit/sync/rabbitmq/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".rabbitmq", New: func() any { return new(Sink) },
		Doc: "Publishes records to a RabbitMQ exchange, each a message of the routing key routing_key's template makes of it, its body what the codec makes of it (raw when none is named), persistent, its message_id the same each time the record is written again (from the record's position), its key in the fit-key header. A batch is held until the broker has confirmed every message of it (publisher confirms); a message the broker nacks fails, from there, to be written again; one it cannot route to any queue fails its record, which on_error takes, unless unroutable is drop. Delivery is at least once: a batch written again after a crash publishes its messages again, which a consumer can drop by their message_id."})
}

const defaultRoutingKey = "{namespace}"

// Sink is the sink rabbitmq.
type Sink struct {
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
	Timeout         config.Duration   `json:"timeout,omitempty" default:"\"30s\"" doc:"How long connecting, each answer of the broker, each write, the wait for a batch's confirms (from the last one), the wait while the broker blocks publishing (a memory or disk alarm), and closing may take. 30s when left out or 0."`
	Heartbeat       config.Duration   `json:"heartbeat,omitempty" default:"\"30s\"" doc:"The heartbeat interval asked for, the broker's when lower: a connection silent for two of them is lost. 30s when left out or 0."`
	FrameMax        int               `json:"frame_max,omitempty" min:"0" default:"131072" doc:"The largest frame asked for, the broker's when lower; larger messages go in several frames. 131072 when left out or 0."`
	MaxMessageBytes int               `json:"max_message_bytes,omitempty" min:"0" default:"16777216" doc:"The largest message body published: a record that makes a larger one fails, and on_error takes it. Keep it at or below the broker's max_message_size. 16 MiB when left out or 0."`
	Exchange        string            `json:"exchange,omitempty" doc:"The exchange published to, which must exist; the default exchange when left out, which routes a message to the queue its routing key names."`
	RoutingKey      string            `json:"routing_key,omitempty" default:"\"{namespace}\"" doc:"The routing key of a record's message: text with {namespace}, the record's namespace, and {key}, its key (a string as it is, anything else as JSON). At most 255 bytes once made. {namespace} when left out."`
	Codec           *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes a message's body of a record: raw when left out, after's value field as it is, or the record's canonical JSON when it has none; json for the row as plain JSON (form plain) or the whole record (form record)."`
	ContentType     string            `json:"content_type,omitempty" doc:"The content_type property of every message, as application/json; none when left out."`
	Transient       bool              `json:"transient,omitempty" doc:"Publish messages as transient (delivery mode 1), which a classic queue keeps in memory alone; persistent when left out. Quorum and stream queues keep every message on disk."`
	Unroutable      string            `json:"unroutable,omitempty" enum:"error,drop" default:"\"error\"" doc:"What a message the exchange routes to no queue is: error, its record fails (the message is published mandatory and returned), on_error then taking it; drop, it is dropped by the broker unseen. error when left out."`

	cfg   amqpwire.Config
	dial  dialer
	codec fitsync.Codec
	c     client
}

// Provision checks the params and reads the password and TLS files;
// nothing is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{Addresses: s.Addresses, Vhost: s.Vhost, Username: s.Username, PasswordEnv: s.PasswordEnv, Auth: s.Auth,
		TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName,
		Timeout: s.Timeout, Heartbeat: s.Heartbeat, FrameMax: s.FrameMax, MaxMessageBytes: s.MaxMessageBytes})
	s.cfg = cfg
	if len(s.Exchange) > 255 {
		errs = append(errs, ctx.Params.Field("exchange").Errorf("longer than 255 bytes"))
	}
	if rest := strings.NewReplacer("{namespace}", "", "{key}", "").Replace(s.RoutingKey); strings.ContainsAny(rest, "{}") {
		errs = append(errs, ctx.Params.Field("routing_key").Errorf("%q is not text with {namespace} and {key}", s.RoutingKey))
	}
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// Cleanup closes the connection and lets the codec go.
func (s *Sink) Cleanup() {
	s.drop()
	module.Cleanup(s.codec)
}

func (s *Sink) drop() {
	if s.c != nil {
		s.c.Close()
		s.c = nil
	}
}

// Test connects and opens a channel in confirm mode.
func (s *Sink) Test(ctx context.Context) error {
	c, err := s.dial.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.ConfirmSelect(ctx)
}

// client returns the connection, made, or made again once lost, in
// confirm mode.
func (s *Sink) client(ctx context.Context) (client, error) {
	if s.c != nil {
		select {
		case <-s.c.Done():
			s.drop()
		default:
			return s.c, nil
		}
	}
	c, err := s.dial.dial(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	if err := c.ConfirmSelect(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("rabbitmq: confirm.select: %w", err)
	}
	s.c = c
	return c, nil
}

// keyText is a record's key as the fit-key header and {key} have it:
// strings and bytes as they are, anything else as plain JSON.
func keyText(r *fitsync.Record) (string, bool) {
	switch r.Key.Kind() {
	case fitsync.KindNull:
		return "", false
	case fitsync.KindString, fitsync.KindBytes:
		return r.Key.Text(), true
	}
	var plain fitsync.JSONCodec
	b, _ := plain.Encode(nil, []*fitsync.Record{{After: r.Key}}) // a key's values all encode
	return string(bytes.TrimSuffix(b, []byte{'\n'})), true
}

// Write publishes the batch, in order, and returns once the broker has
// confirmed all of it.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	if len(batch) == 0 {
		return nil
	}
	c, err := s.client(ctx)
	if err != nil {
		return err
	}
	ids := fitsync.RecordIDs(batch)
	mandatory := s.Unroutable != "drop"
	seqs := make([]uint64, 0, len(batch))
	index := map[string]int{}
	failed, permanent := len(batch), false
	var failure error
	for i, r := range batch {
		key, props, body, err := s.message(r, ids[i])
		if err != nil {
			failed, failure, permanent = i, err, true
			break
		}
		seq, err := c.Publish(ctx, s.Exchange, key, mandatory, props, body)
		if err != nil {
			failed, failure, permanent = i, err, errors.Is(err, amqpwire.ErrTooLarge)
			break
		}
		seqs = append(seqs, seq)
		index[ids[i]] = i
	}
	if len(seqs) > 0 {
		nacked, err := c.WaitConfirms(ctx, seqs[0], seqs[len(seqs)-1])
		if err != nil {
			s.drop()
			return err
		}
		for _, n := range nacked {
			if i := int(n - seqs[0]); i < failed {
				failed, failure, permanent = i, errors.New("rabbitmq: the broker nacked the message: it could not take it"), false
			}
		}
		for _, ret := range c.TakeReturns() {
			if i, ok := index[ret.Properties.MessageID]; ok && i < failed {
				failed, permanent = i, true
				failure = fmt.Errorf("rabbitmq: exchange %q routes %q to no queue: %d %s", ret.Exchange, ret.RoutingKey, ret.Code, ret.Text)
			}
		}
	}
	if failure == nil {
		return nil
	}
	if c.Err() != nil {
		s.drop()
	}
	if permanent {
		failure = fitsync.Permanent(failure)
	}
	return &fitsync.BatchError{Index: failed, Err: failure}
}

// message makes r a message: its routing key, properties and body.
func (s *Sink) message(r *fitsync.Record, id string) (string, amqpwire.Properties, []byte, error) {
	key, hasKey := keyText(r)
	rk := s.RoutingKey
	if rk == "" {
		rk = defaultRoutingKey
	}
	rk = strings.NewReplacer("{namespace}", r.Namespace, "{key}", key).Replace(rk)
	if len(rk) > 255 {
		return "", amqpwire.Properties{}, nil, fmt.Errorf("rabbitmq: the routing key %.40q... is longer than 255 bytes", rk)
	}
	body, err := s.codec.Encode(nil, []*fitsync.Record{r})
	if err != nil {
		return "", amqpwire.Properties{}, nil, fmt.Errorf("rabbitmq: the codec: %w", err)
	}
	body = bytes.TrimSuffix(body, []byte{'\n'})
	p := amqpwire.Properties{ContentType: s.ContentType, MessageID: id, DeliveryMode: 2, Timestamp: r.CommitTime,
		Headers: amqpwire.Table{"fit-op": r.Op.String(), "fit-namespace": r.Namespace}}
	if s.Transient {
		p.DeliveryMode = 1
	}
	if hasKey {
		p.Headers[keyHeader] = key
	}
	return rk, p, body, nil
}
