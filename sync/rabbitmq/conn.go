// Package syncrabbitmq is sync's RabbitMQ, for RabbitMQ 3.13 and 4.x: the
// source rabbitmq, which consumes queues (classic and quorum) with a
// prefetch and acknowledges each delivery only once every sink has its
// records, and stream queues from offsets kept in the pipeline's
// checkpoint; and the sink rabbitmq, which publishes records to an
// exchange, persistent, a batch held until the broker confirms all of it.
// Both speak AMQP 0-9-1 through sync/rabbitmq/wire, on connections of their
// own, never on a fib worker. See docs/decisions/036-sync.md.
package syncrabbitmq

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	synckafka "github.com/lesismal/fit/sync/kafka"
	amqpwire "github.com/lesismal/fit/sync/rabbitmq/wire"
)

// The defaults of the connection's params.
const (
	DefaultTimeout   = 30 * time.Second
	DefaultHeartbeat = 30 * time.Second
	DefaultMaxBytes  = 16 << 20
	// keyHeader is the header the sink writes a record's key in, and the
	// source reads it from.
	keyHeader = "fit-key"
)

// connParams are the params the source and the sink share, declared by
// each as its own, with the same names and docs.
type connParams struct {
	Addresses                             []string
	Vhost, Username, PasswordEnv, Auth    string
	TLS                                   bool
	CAFile, CertFile, KeyFile, ServerName string
	Timeout, Heartbeat                    config.Duration
	FrameMax, MaxMessageBytes             int
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// wireConfig checks p and makes the client's config from it, reading the
// password from the environment and the TLS files.
func wireConfig(ctx *module.Context, p connParams) (amqpwire.Config, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	c := amqpwire.Config{Addrs: p.Addresses, Vhost: p.Vhost, Username: p.Username, Timeout: DefaultTimeout,
		Heartbeat: DefaultHeartbeat, FrameMax: uint32(p.FrameMax), MaxBody: DefaultMaxBytes, Name: "fit-sync"}
	if len(p.Addresses) == 0 {
		add("addresses", "no addresses")
	}
	for _, a := range p.Addresses {
		if _, _, err := net.SplitHostPort(a); err != nil {
			add("addresses", "%q is not a host and port", a)
		}
	}
	if p.Timeout > 0 {
		c.Timeout = p.Timeout.Std()
	}
	if p.Heartbeat > 0 {
		c.Heartbeat = p.Heartbeat.Std()
	}
	if p.MaxMessageBytes > 0 {
		c.MaxBody = p.MaxMessageBytes
	}
	switch strings.ToUpper(p.Auth) {
	case "", "PLAIN":
		switch {
		case p.Username == "":
			add("username", "PLAIN needs username")
		case !envName.MatchString(p.PasswordEnv):
			add("password_env", "%q is not an environment variable's name", p.PasswordEnv)
		default:
			if c.Password = os.Getenv(p.PasswordEnv); c.Password == "" {
				add("password_env", "the environment variable %s is not set, or empty", p.PasswordEnv)
			}
		}
	case "EXTERNAL":
		c.External = true
		if !p.TLS || p.CertFile == "" {
			add("auth", "EXTERNAL needs tls and cert_file: the client certificate is the credential")
		}
		if p.Username != "" || p.PasswordEnv != "" {
			add("auth", "EXTERNAL takes no username or password_env")
		}
	default:
		add("auth", "%q is not PLAIN or EXTERNAL", p.Auth)
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

// client is what the connectors ask of RabbitMQ: *amqpwire.Conn, or a fake
// in the tests.
type client interface {
	Qos(ctx context.Context, prefetch uint16) error
	Consume(ctx context.Context, queue, tag string, args amqpwire.Table) (string, error)
	Deliveries() <-chan *amqpwire.Delivery
	Ack(tag uint64, multiple bool) error
	QueueDeclare(ctx context.Context, name string, passive, durable bool, args amqpwire.Table) (amqpwire.QueueInfo, error)
	ConfirmSelect(ctx context.Context) error
	Publish(ctx context.Context, exchange, key string, mandatory bool, p amqpwire.Properties, body []byte) (uint64, error)
	WaitConfirms(ctx context.Context, from, to uint64) ([]uint64, error)
	TakeReturns() []amqpwire.Return
	Done() <-chan struct{}
	Err() error
	Close() error
}

// dialer makes a client of a config.
type dialer func(ctx context.Context, cfg amqpwire.Config) (client, error)

func newWire(ctx context.Context, cfg amqpwire.Config) (client, error) {
	c, err := amqpwire.Dial(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: %w", err)
	}
	return c, nil
}

func (d dialer) dial(ctx context.Context, cfg amqpwire.Config) (client, error) {
	if d == nil {
		return newWire(ctx, cfg)
	}
	return d(ctx, cfg)
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

// value makes a field table's value a record's.
func value(v any) fitsync.Value {
	switch v := v.(type) {
	case bool:
		return fitsync.Bool(v)
	case int8:
		return fitsync.Int(int64(v))
	case uint8:
		return fitsync.Int(int64(v))
	case int16:
		return fitsync.Int(int64(v))
	case uint16:
		return fitsync.Int(int64(v))
	case int32:
		return fitsync.Int(int64(v))
	case uint32:
		return fitsync.Int(int64(v))
	case int64:
		return fitsync.Int(v)
	case float32:
		return fitsync.Float(float64(v))
	case float64:
		return fitsync.Float(v)
	case amqpwire.Decimal:
		return fitsync.Decimal(decimal(v))
	case string:
		return fitsync.String(v)
	case []byte:
		return fitsync.Bytes(v)
	case time.Time:
		return fitsync.Time(v)
	case amqpwire.Table:
		return table(v)
	case []any:
		elems := make([]fitsync.Value, len(v))
		for i, e := range v {
			elems[i] = value(e)
		}
		return fitsync.Array(elems...)
	}
	return fitsync.Null()
}

// table makes a field table a map, its fields in name order.
func table(t amqpwire.Table) fitsync.Value {
	names := make([]string, 0, len(t))
	for k := range t {
		names = append(names, k)
	}
	sort.Strings(names)
	fs := make([]fitsync.Field, len(names))
	for i, k := range names {
		fs[i] = fitsync.Field{Name: k, Value: value(t[k])}
	}
	return fitsync.Map(fs...)
}

// decimal writes a field table's decimal as text.
func decimal(d amqpwire.Decimal) string {
	neg := d.Value < 0
	v := int64(d.Value)
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%0*d", int(d.Scale)+1, v)
	if d.Scale > 0 {
		s = s[:len(s)-int(d.Scale)] + "." + s[len(s)-int(d.Scale):]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// text makes a key a string when it is UTF-8, bytes otherwise.
func text(b string) fitsync.Value {
	if utf8.ValidString(b) {
		return fitsync.String(b)
	}
	return fitsync.Bytes([]byte(b))
}
