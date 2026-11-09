// Package syncpulsar is sync's Apache Pulsar, for Pulsar 2.10 and later:
// the source pulsar, which reads topics, every partition of each, as a
// reader from the message IDs kept in the pipeline's checkpoint, or
// through a durable subscription acknowledged cumulatively once every
// sink has the records, each message made into records by a codec; and
// the sink pulsar, which sends records to topics named from their
// namespaces, a producer for each partition, a key's records to the
// partition the Java client's router gives it, batched and compressed,
// each message carrying an ID made from the record's position, by which a
// batch written again is not stored twice. Both speak Pulsar's binary
// protocol through sync/pulsar/wire, on connections of their own, never on
// a fib worker. See docs/decisions/036-sync.md.
package syncpulsar

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	synckafka "github.com/lesismal/fit/sync/kafka"
	pulsarwire "github.com/lesismal/fit/sync/pulsar/wire"
)

// DefaultTimeout bounds connecting and each request's answer.
const DefaultTimeout = 30 * time.Second

// connParams are the params the source and the sink share, declared by
// each as its own, with the same names and docs.
type connParams struct {
	URL                                   string
	CAFile, CertFile, KeyFile, ServerName string
	TokenEnv                              string
	Timeout                               config.Duration
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// wireConfig checks p and makes the client's config from it, reading the
// token from the environment and the TLS files.
func wireConfig(ctx *module.Context, p connParams) (pulsarwire.Config, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	c := pulsarwire.Config{URL: p.URL}
	if _, err := pulsarwire.New(c); err != nil {
		add("url", "%v", err)
	}
	t := DefaultTimeout
	if p.Timeout > 0 {
		t = p.Timeout.Std()
	}
	c.DialTimeout, c.RequestTimeout = t, t
	if p.TokenEnv != "" {
		if !envName.MatchString(p.TokenEnv) {
			add("token_env", "%q is not an environment variable's name", p.TokenEnv)
		} else if c.Token = os.Getenv(p.TokenEnv); c.Token == "" {
			add("token_env", "the environment variable %s is not set, or empty", p.TokenEnv)
		}
	}
	if !strings.HasPrefix(p.URL, "pulsar+ssl://") {
		for _, f := range []struct{ name, value string }{{"ca_file", p.CAFile}, {"cert_file", p.CertFile}, {"key_file", p.KeyFile}, {"server_name", p.ServerName}} {
			if f.value != "" {
				add(f.name, "%s needs a pulsar+ssl:// url", f.name)
			}
		}
		return c, errs
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.ServerName}
	if p.CAFile != "" {
		pem, err := os.ReadFile(p.CAFile)
		tc.RootCAs = x509.NewCertPool()
		if err != nil {
			add("ca_file", "%v", err)
		} else if !tc.RootCAs.AppendCertsFromPEM(pem) {
			add("ca_file", "%s holds no PEM certificate", p.CAFile)
		}
	}
	if (p.CertFile == "") != (p.KeyFile == "") {
		add("cert_file", "cert_file and key_file go together")
	} else if p.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(p.CertFile, p.KeyFile)
		if err != nil {
			add("cert_file", "%v", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	c.TLS = tc
	return c, errs
}

// client is what the connectors ask of Pulsar: *pulsarwire.Client, or a
// fake in the tests.
type client interface {
	PartitionedMetadata(ctx context.Context, topic string) (int, error)
	CreateProducer(ctx context.Context, topic, name string, c pulsarwire.Compression) (producer, error)
	Subscribe(ctx context.Context, topic string, o pulsarwire.SubscribeOptions) (consumer, error)
	Close() error
}

type producer interface {
	Name() string
	MaxMessageSize() int
	Send(msgs []pulsarwire.ProducerMessage) (<-chan pulsarwire.SendResult, error)
	Close(ctx context.Context) error
}

type consumer interface {
	TryReceive() (pulsarwire.Message, bool, error)
	Receive(ctx context.Context) (pulsarwire.Message, error)
	AckCumulative(id pulsarwire.MessageID) error
	LastMessageID(ctx context.Context) (pulsarwire.MessageID, error)
	Close(ctx context.Context) error
}

// wireClient is *pulsarwire.Client as a client.
type wireClient struct{ *pulsarwire.Client }

func (w wireClient) CreateProducer(ctx context.Context, topic, name string, c pulsarwire.Compression) (producer, error) {
	p, err := w.Client.CreateProducer(ctx, topic, name, c)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (w wireClient) Subscribe(ctx context.Context, topic string, o pulsarwire.SubscribeOptions) (consumer, error) {
	c, err := w.Client.Subscribe(ctx, topic, o)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// dialer makes a client of a config.
type dialer func(pulsarwire.Config) (client, error)

func newWire(cfg pulsarwire.Config) (client, error) {
	c, err := pulsarwire.New(cfg)
	if err != nil {
		return nil, err
	}
	return wireClient{c}, nil
}

// holder holds a client, made when first needed.
type holder struct {
	cfg  pulsarwire.Config
	dial dialer
	c    client
}

func (h *holder) get() (client, error) {
	if h.c != nil {
		return h.c, nil
	}
	dial := h.dial
	if dial == nil {
		dial = newWire
	}
	c, err := dial(h.cfg)
	if err != nil {
		return nil, err
	}
	h.c = c
	return c, nil
}

func (h *holder) close() {
	if h.c != nil {
		h.c.Close()
		h.c = nil
	}
}

// loadCodec loads a codec ref, raw when there is none.
func loadCodec(ctx *module.Context, ref *config.ModuleRef) (fitsync.Codec, error) {
	if ref == nil {
		return &synckafka.Raw{}, nil
	}
	v, err := ctx.Load(fitsync.Codecs, *ref)
	if err != nil {
		return nil, err
	}
	c, ok := v.(fitsync.Codec)
	if !ok {
		module.Cleanup(v)
		return nil, ref.At.Field("module").Errorf("%s is not a codec", ref.Module)
	}
	return c, nil
}

// fullTopic writes a topic's name in full, persistent://tenant/namespace/
// topic: a bare name is public/default's, tenant/namespace/topic is
// persistent.
func fullTopic(s string) (string, error) {
	domain, rest, ok := strings.Cut(s, "://")
	if !ok {
		domain, rest = "persistent", s
	}
	if domain != "persistent" && domain != "non-persistent" {
		return "", fmt.Errorf("pulsar: %q is not a topic's name: %s:// is not persistent:// or non-persistent://", s, domain)
	}
	parts := strings.Split(rest, "/")
	switch len(parts) {
	case 1:
		parts = []string{"public", "default", parts[0]}
	case 3:
	default:
		return "", fmt.Errorf("pulsar: %q is not a topic's name: topic, tenant/namespace/topic or persistent://tenant/namespace/topic", s)
	}
	for i, p := range parts {
		if !validName(p, i == 2) {
			return "", fmt.Errorf("pulsar: %q is not a topic's name: %q", s, p)
		}
	}
	return domain + "://" + strings.Join(parts, "/"), nil
}

// validName reports whether s is a name Pulsar takes for a tenant or a
// namespace, or, when local, a topic.
func validName(s string, local bool) bool {
	if s == "" || len(s) > 255 || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.=:", c) || local && c == '%') {
			return false
		}
	}
	return true
}

// partitionNames are the topics a topic of n partitions is read and
// written as: itself when it is not partitioned.
func partitionName(topic string, n, i int) string {
	if n == 0 {
		return topic
	}
	return fmt.Sprintf("%s-partition-%d", topic, i)
}

// text makes a key a string when it is UTF-8, bytes otherwise.
func text(b []byte) fitsync.Value {
	if utf8.Valid(b) {
		return fitsync.String(string(b))
	}
	return fitsync.Bytes(b)
}
