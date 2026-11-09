// Package synckafka is sync's Kafka, for Kafka 3.x and later and Redpanda:
// the source kafka, which reads topics' partitions from offsets kept in
// the pipeline's checkpoint (no consumer group), each message made into
// records by a codec, and the sink kafka, which produces records to topics
// named from their namespaces, keyed by the records' keys, batched and
// compressed, acknowledged by every in-sync replica by default. It also
// has the codecs of Kafka's change-data formats: raw, canal_json (Canal's
// and TiCDC's), debezium and ticdc_simple. Both connectors speak the
// protocol through sync/kafka/wire, on connections of their own, never on
// a fib worker. See docs/decisions/036-sync.md.
package synckafka

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
)

// DefaultTimeout bounds connecting and each request's answer.
const DefaultTimeout = 30 * time.Second

// connParams are the params the source and the sink share, declared by
// each as its own, with the same names and docs.
type connParams struct {
	Brokers                               []string
	ClientID                              string
	TLS                                   bool
	CAFile, CertFile, KeyFile, ServerName string
	SASL, Username, PasswordEnv           string
	Timeout                               config.Duration
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// wireConfig checks p and makes the client's config from it, reading the
// password from the environment and the TLS files.
func wireConfig(ctx *module.Context, p connParams) (kafkawire.Config, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	c := kafkawire.Config{Brokers: p.Brokers, ClientID: p.ClientID}
	if c.ClientID == "" {
		c.ClientID = "fit-sync"
	}
	if len(p.Brokers) == 0 {
		add("brokers", "no brokers")
	}
	for _, b := range p.Brokers {
		if _, _, err := net.SplitHostPort(b); err != nil {
			add("brokers", "%q is not a host and port", b)
		}
	}
	t := DefaultTimeout
	if p.Timeout > 0 {
		t = p.Timeout.Std()
	}
	c.DialTimeout, c.RequestTimeout, c.RetryFor = t, t, t
	switch mech := strings.ToUpper(p.SASL); mech {
	case "":
		if p.Username != "" || p.PasswordEnv != "" {
			add("sasl", "username and password_env need sasl")
		}
	case kafkawire.SASLPlain, kafkawire.SASLScramSHA256, kafkawire.SASLScramSHA512:
		s := &kafkawire.SASL{Mechanism: mech, Username: p.Username}
		switch {
		case p.Username == "":
			add("username", "sasl needs username")
		case !envName.MatchString(p.PasswordEnv):
			add("password_env", "%q is not an environment variable's name", p.PasswordEnv)
		default:
			if s.Password = os.Getenv(p.PasswordEnv); s.Password == "" {
				add("password_env", "the environment variable %s is not set, or empty", p.PasswordEnv)
			}
		}
		c.SASL = s
	default:
		add("sasl", "%q is not PLAIN, SCRAM-SHA-256 or SCRAM-SHA-512", p.SASL)
	}
	if !p.TLS {
		for _, f := range []struct{ name, value string }{{"ca_file", p.CAFile}, {"cert_file", p.CertFile}, {"key_file", p.KeyFile}, {"server_name", p.ServerName}} {
			if f.value != "" {
				add(f.name, "%s needs tls", f.name)
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

// client is what the connectors ask of Kafka: *kafkawire.Client, or a fake
// in the tests.
type client interface {
	Metadata(ctx context.Context, topics ...string) (*kafkawire.Metadata, error)
	Partitions(ctx context.Context, topic string) (int32, error)
	ListOffsets(ctx context.Context, readCommitted bool, queries ...kafkawire.OffsetQuery) ([]kafkawire.OffsetResult, error)
	Fetch(ctx context.Context, opts kafkawire.FetchOptions, parts ...kafkawire.FetchPartition) ([]kafkawire.FetchResult, error)
	Produce(ctx context.Context, opts kafkawire.ProduceOptions, batches ...kafkawire.ProduceBatch) ([]kafkawire.ProduceResult, error)
	CreateTopics(ctx context.Context, timeout time.Duration, topics ...kafkawire.TopicSpec) error
	Close() error
}

// dialer makes a client of a config.
type dialer func(kafkawire.Config) (client, error)

func newWire(cfg kafkawire.Config) (client, error) { return kafkawire.New(cfg) }

// holder holds a client, made when first needed.
type holder struct {
	cfg  kafkawire.Config
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
		return &Raw{}, nil
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
