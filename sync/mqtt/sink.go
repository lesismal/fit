package syncmqtt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".mqtt", New: func() any { return new(Sink) },
		Doc: "Publishes each record to an MQTT broker, of version 3.1.1 or 5, as a message of the topic topic's template makes of its namespace and key, at a QoS, retained or not, its payload what the codec makes of the record (when none is named, after's payload field, or the record's canonical JSON). A batch's messages go out in order, at most max_inflight unacknowledged at once, and the batch is written once the broker has acknowledged each (PUBACK for QoS 1, PUBCOMP for QoS 2). A delete, retained, is an empty retained message, which clears the topic's. Delivery is at least once: a batch written again after a crash, or a publish whose acknowledgement was lost, publishes its messages again."})
}

// The defaults of the sink's params.
const (
	DefaultMaxInflight = 64
	defaultTopic       = "{namespace}"
)

// Sink is the sink mqtt.
type Sink struct {
	Servers        []string          `json:"servers" fit:"required" doc:"The brokers, host:port each (1883, or 8883 with tls), tried in turn until one takes the connection."`
	ClientID       string            `json:"client_id,omitempty" doc:"The client ID, in a clean session of its own; fit-sync- and random letters when left out."`
	Version        string            `json:"version,omitempty" enum:"3.1.1,5" default:"\"5\"" doc:"The MQTT version spoken: 3.1.1 or 5. 5 when left out."`
	Username       string            `json:"username,omitempty" doc:"The user name sent in CONNECT."`
	PasswordEnv    string            `json:"password_env,omitempty" doc:"The environment variable holding the password sent in CONNECT, read when the pipeline loads."`
	TLS            bool              `json:"tls,omitempty" doc:"Connect over TLS, the brokers' certificates checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile         string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the brokers' are checked against, in place of the system's roots; needs tls."`
	CertFile       string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, for brokers that ask for one; needs tls and key_file."`
	KeyFile        string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName     string            `json:"server_name,omitempty" doc:"The name the brokers' certificates must have; each broker's host when left out."`
	Timeout        config.Duration   `json:"timeout,omitempty" default:"\"30s\"" doc:"How long connecting, the broker's answer to CONNECT, and each write may take. 30s when left out or 0."`
	KeepAlive      config.Duration   `json:"keep_alive,omitempty" default:"\"60s\"" doc:"The keep-alive told to the broker: fit pings it every half of it, and takes a ping not answered within it as the connection lost. At most 65535s; a broker of version 5 may set its own. 60s when left out or 0."`
	MaxPacketSize  int               `json:"max_packet_size,omitempty" min:"0" max:"268435455" default:"1048576" doc:"The most bytes of a packet fit takes from the broker. A message larger than the broker takes (its Maximum Packet Size, in version 5) fails, at once. 1048576 when left out or 0."`
	Topic          string            `json:"topic,omitempty" default:"\"{namespace}\"" doc:"The topic a record goes to: text with {namespace}, the record's namespace, and {key}, its key's text (a route transform can map namespaces first). A record whose topic is empty or holds + or # fails. {namespace} when left out."`
	QoS            *int              `json:"qos,omitempty" min:"0" max:"2" default:"1" doc:"The QoS messages are published at: 0, sent and not acknowledged, so a message the connection loses is lost; 1, acknowledged (PUBACK); 2, acknowledged once handed on (PUBCOMP). 1 when left out."`
	Retain         bool              `json:"retain,omitempty" doc:"Publish retained messages: the broker keeps each topic's last, for clients that subscribe later, and a delete clears it."`
	Codec          *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes a message's payload of a record, as json for a JSON object. When left out, after's (or before's) payload field, its bytes or text as they are, or the record's canonical JSON when it has none; and, in version 5, the field content_type, and the map user_properties, become the message's properties, so that the source mqtt's records are published as they were read."`
	MessageExpiry  config.Duration   `json:"message_expiry,omitempty" doc:"In version 5, how long the broker keeps a message for a subscriber not connected, rounded up to seconds. None when left out."`
	ContentType    string            `json:"content_type,omitempty" doc:"In version 5, the messages' content type, as application/json."`
	UserProperties map[string]string `json:"user_properties,omitempty" doc:"In version 5, user properties every message carries, in the order of their names."`
	MaxInflight    int               `json:"max_inflight,omitempty" min:"0" max:"65535" default:"64" doc:"The most messages of QoS 1 and 2 published and not yet acknowledged; lowered to the broker's receive maximum in version 5. 64 when left out or 0."`
	Will           *Will             `json:"will,omitempty" doc:"A will message the broker publishes once the sink's connection ends other than by fit closing it: lost, or kept alive in vain. None when left out."`
	AuthMethod     string            `json:"auth_method,omitempty" enum:"SCRAM-SHA-1,SCRAM-SHA-256,SCRAM-SHA-512" doc:"In version 5, authenticate by enhanced authentication (AUTH) with this SCRAM method, of username and password_env's password, rather than with the password in CONNECT: the password does not cross the wire, and fit checks the broker knows it. EMQX 5's SCRAM authenticator takes SCRAM-SHA-256 and SCRAM-SHA-512. The password in CONNECT when left out."`

	cfg   mqttwire.Config
	dial  dialer
	codec fitsync.Codec
	qos   byte
	ups   []mqttwire.UserProperty
	c     client
}

// Provision checks the params and reads the password and TLS files;
// nothing is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{Servers: s.Servers, ClientID: s.ClientID, Version: s.Version, Username: s.Username,
		PasswordEnv: s.PasswordEnv, TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName,
		Timeout: s.Timeout, KeepAlive: s.KeepAlive, MaxPacketSize: s.MaxPacketSize, Will: s.Will, AuthMethod: s.AuthMethod})
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if cfg.ClientID == "" {
		var b [6]byte
		_, _ = rand.Read(b[:])
		cfg.ClientID = "fit-sync-" + hex.EncodeToString(b[:])
	}
	cfg.CleanStart = true
	cfg.MaxInflight = DefaultMaxInflight
	if s.MaxInflight > 0 {
		cfg.MaxInflight = s.MaxInflight
	}
	if s.Topic != "" {
		rest := strings.NewReplacer("{namespace}", "", "{key}", "").Replace(s.Topic)
		if strings.ContainsAny(rest, "{}+#") || rest == s.Topic && mqttwire.ValidTopicName(s.Topic) != nil {
			add("topic", "%q is not a topic name, or text with {namespace} and {key}", s.Topic)
		}
	}
	s.qos = 1
	if s.QoS != nil {
		s.qos = byte(*s.QoS)
	}
	if cfg.Version == mqttwire.Version311 {
		for _, f := range []struct {
			name string
			set  bool
		}{{"message_expiry", s.MessageExpiry > 0}, {"content_type", s.ContentType != ""}, {"user_properties", len(s.UserProperties) > 0}} {
			if f.set {
				add(f.name, "%s needs version 5", f.name)
			}
		}
	}
	s.ups = userProperties(s.UserProperties)
	s.cfg = cfg
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// Cleanup closes the connection and lets the codec go.
func (s *Sink) Cleanup() {
	if s.c != nil {
		s.c.Close()
		s.c = nil
	}
	module.Cleanup(s.codec)
}

// Test connects with a client ID of its own, and disconnects.
func (s *Sink) Test(ctx context.Context) error {
	cfg := s.cfg
	cfg.ClientID = testID("")
	c, err := connect(ctx, cfg, s.Servers, s.dial)
	if err != nil {
		return err
	}
	return c.Close()
}

// conn returns the connection, made again when lost.
func (s *Sink) conn(ctx context.Context) (client, error) {
	if s.c != nil {
		select {
		case <-s.c.Done():
			s.c.Close()
			s.c = nil
		default:
			return s.c, nil
		}
	}
	c, err := connect(ctx, s.cfg, s.Servers, s.dial)
	if err != nil {
		return nil, err
	}
	s.c = c
	return c, nil
}

// Write publishes the batch, in order, and waits for every message's
// acknowledgement.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	c, err := s.conn(ctx)
	if err != nil {
		return err
	}
	sent := make([]waiter, 0, len(batch))
	for i, r := range batch {
		m, err := s.message(r)
		if err == nil {
			var w waiter
			if w, err = c.Send(ctx, m); err == nil {
				sent = append(sent, w)
				continue
			}
			if permanent(err) {
				err = fitsync.Permanent(err)
			}
		}
		// What was sent before it is waited for: the error is the first
		// record's not written.
		if werr := s.wait(ctx, c, sent); werr != nil {
			return werr
		}
		return &fitsync.BatchError{Index: i, Err: err}
	}
	return s.wait(ctx, c, sent)
}

// wait waits for the acknowledgements of the batch's first messages,
// returning a BatchError at the first that failed.
func (s *Sink) wait(ctx context.Context, c client, sent []waiter) error {
	for i, w := range sent {
		if err := w.Wait(ctx); err != nil {
			if permanent(err) {
				err = fitsync.Permanent(err)
			}
			return &fitsync.BatchError{Index: i, Err: err}
		}
	}
	return nil
}

// message makes r a message.
func (s *Sink) message(r *fitsync.Record) (*mqttwire.Message, error) {
	topic := r.Namespace
	if s.Topic != "" {
		topic = strings.NewReplacer("{namespace}", r.Namespace, "{key}", fitsync.TextOf(r.Key)).Replace(s.Topic)
	}
	if err := mqttwire.ValidTopicName(topic); err != nil {
		return nil, fitsync.Permanent(errorf("the topic %q of a record of %s: %w", topic, r.Namespace, err))
	}
	m := &mqttwire.Message{Topic: topic, QoS: s.qos, Retain: s.Retain, MessageExpiry: s.MessageExpiry.Std(),
		ContentType: s.ContentType, UserProperties: s.ups}
	switch {
	case r.Op == fitsync.OpDelete && s.Retain:
		m.Payload = []byte{}
	case s.codec != nil:
		b, err := s.codec.Encode(nil, []*fitsync.Record{r})
		if err != nil {
			return nil, fitsync.Permanent(errorf("the codec: %w", err))
		}
		m.Payload = bytes.TrimSuffix(b, []byte{'\n'})
	default:
		if err := s.plain(r, m); err != nil {
			return nil, fitsync.Permanent(err)
		}
	}
	return m, nil
}

// plain makes m's payload, and properties, of r as the source mqtt's own
// form holds a message.
func (s *Sink) plain(r *fitsync.Record, m *mqttwire.Message) error {
	row := r.After
	if row.IsNull() {
		row = r.Before
	}
	v, ok := row.Get("payload")
	switch {
	case ok && (v.Kind() == fitsync.KindBytes || v.Kind() == fitsync.KindString):
		m.Payload = []byte(v.Text())
	case ok:
		m.Payload = []byte(fitsync.TextOf(v))
	default:
		var err error
		if m.Payload, err = r.AppendJSON(nil); err != nil {
			return err
		}
	}
	if ct, ok := row.Get("content_type"); ok && m.ContentType == "" && ct.Kind() == fitsync.KindString {
		m.ContentType = ct.Text()
	}
	if ups, ok := row.Get("user_properties"); ok && ups.Kind() == fitsync.KindMap {
		m.UserProperties = slices.Clone(m.UserProperties)
		for _, f := range ups.Fields() {
			vals := []fitsync.Value{f.Value}
			if f.Value.Kind() == fitsync.KindArray {
				vals = f.Value.Elems()
			}
			for _, v := range vals {
				m.UserProperties = append(m.UserProperties, mqttwire.UserProperty{Key: f.Name, Value: fitsync.TextOf(v)})
			}
		}
	}
	return nil
}

var (
	_ fitsync.Sink   = (*Sink)(nil)
	_ module.Cleaner = (*Sink)(nil)
)
