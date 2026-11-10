// Package syncmqtt is sync's MQTT, of versions 3.1.1 and 5, for EMQX,
// Mosquitto and any other broker: the source mqtt, which subscribes to
// topic filters in a persistent session of its client ID and acknowledges
// each message of QoS 1 and 2 (PUBACK, PUBREC) only once every sink has its
// records, so that the broker sends again, on the next connection, what a
// pipeline stopped or killed had not written; and the sink mqtt, which
// publishes records to topics made of a template, at a QoS, retained or
// not, with the publishes in flight bounded. Both speak the protocol
// through sync/mqtt/wire, on connections of their own, never on a fib
// worker. See docs/decisions/036-sync.md.
package syncmqtt

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

// The defaults of the params both connectors have.
const (
	DefaultTimeout   = 30 * time.Second
	DefaultKeepAlive = 60 * time.Second
)

// connParams are the params the source and the sink share, declared by
// each as its own, with the same names and docs.
type connParams struct {
	Servers                               []string
	ClientID                              string
	Version                               string
	Username, PasswordEnv                 string
	TLS                                   bool
	CAFile, CertFile, KeyFile, ServerName string
	Timeout, KeepAlive                    config.Duration
	MaxPacketSize                         int
	Will                                  *Will
	AuthMethod                            string
}

// Will is a connection's will message, which the broker publishes once
// fit's connection ends other than by fit closing it: lost, or kept alive
// in vain.
type Will struct {
	Topic          string            `json:"topic" fit:"required" doc:"The will's topic: a topic name, without + or #."`
	Payload        string            `json:"payload,omitempty" doc:"The will's payload, as text. Empty when left out."`
	QoS            int               `json:"qos,omitempty" min:"0" max:"2" doc:"The will's QoS: 0, 1 or 2. 0 when left out."`
	Retain         bool              `json:"retain,omitempty" doc:"Publish the will retained: the broker keeps it for clients that subscribe later, until a message to the topic replaces it."`
	Delay          config.Duration   `json:"delay,omitempty" doc:"In version 5, how long the broker waits, once the connection is lost, before it publishes the will, rounded up to seconds: nothing is published if fit is back within it. The broker publishes it sooner when the session ends first: at once for the sink, whose session ends with its connection. At once when left out."`
	MessageExpiry  config.Duration   `json:"message_expiry,omitempty" doc:"In version 5, how long the broker keeps the will for a subscriber not connected, rounded up to seconds. None when left out."`
	ContentType    string            `json:"content_type,omitempty" doc:"In version 5, the will's content type, as text/plain."`
	UserProperties map[string]string `json:"user_properties,omitempty" doc:"In version 5, user properties the will carries, in the order of their names."`
}

// userProperties are props as MQTT 5's user properties, in the order of
// their names.
func userProperties(props map[string]string) []mqttwire.UserProperty {
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	slices.Sort(names)
	var ups []mqttwire.UserProperty
	for _, k := range names {
		ups = append(ups, mqttwire.UserProperty{Key: k, Value: props[k]})
	}
	return ups
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// wireConfig checks p and makes the client's config from it, reading the
// password from the environment and the TLS files.
func wireConfig(ctx *module.Context, p connParams) (mqttwire.Config, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	c := mqttwire.Config{ClientID: p.ClientID, Version: mqttwire.Version5, Username: p.Username, MaxPacketSize: p.MaxPacketSize,
		Timeout: DefaultTimeout, KeepAlive: DefaultKeepAlive}
	if p.Version == "3.1.1" {
		c.Version = mqttwire.Version311
	}
	if len(p.Servers) == 0 {
		add("servers", "no servers")
	}
	for _, s := range p.Servers {
		if _, _, err := net.SplitHostPort(s); err != nil {
			add("servers", "%q is not a host and port", s)
		}
	}
	if p.Timeout > 0 {
		c.Timeout = p.Timeout.Std()
	}
	if p.KeepAlive > 0 {
		c.KeepAlive = p.KeepAlive.Std()
	}
	if c.KeepAlive > 65535*time.Second {
		add("keep_alive", "%v is past 65535s", c.KeepAlive)
	}
	if p.PasswordEnv != "" {
		if !envName.MatchString(p.PasswordEnv) {
			add("password_env", "%q is not an environment variable's name", p.PasswordEnv)
		} else if c.Password = os.Getenv(p.PasswordEnv); c.Password == "" {
			add("password_env", "the environment variable %s is not set, or empty", p.PasswordEnv)
		} else if p.Username == "" && c.Version == mqttwire.Version311 {
			add("password_env", "MQTT 3.1.1 sends a password only with a username")
		}
	}
	if p.AuthMethod != "" {
		a, err := mqttwire.NewSCRAM(p.AuthMethod, p.Username, c.Password)
		switch {
		case c.Version != mqttwire.Version5:
			add("auth_method", "enhanced authentication needs version 5")
		case err != nil || c.Password == "":
			add("auth_method", "%s needs a username and password_env", p.AuthMethod)
		default:
			// The password proves itself in the exchange: CONNECT does not
			// carry it.
			c.Auth, c.Password = a, ""
		}
	}
	if w := p.Will; w != nil {
		c.Will = &mqttwire.Will{Message: mqttwire.Message{Topic: w.Topic, Payload: []byte(w.Payload), QoS: byte(w.QoS), Retain: w.Retain,
			MessageExpiry: w.MessageExpiry.Std(), ContentType: w.ContentType, UserProperties: userProperties(w.UserProperties)},
			Delay: w.Delay.Std()}
		at := ctx.Params.Field("will")
		if err := mqttwire.ValidTopicName(w.Topic); err != nil {
			errs = append(errs, at.Field("topic").Errorf("%v", err))
		}
		if len(w.Payload) > 65535 {
			errs = append(errs, at.Field("payload").Errorf("%d bytes, past 65535", len(w.Payload)))
		}
		if c.Version == mqttwire.Version311 {
			for _, f := range []struct {
				name string
				set  bool
			}{{"delay", w.Delay > 0}, {"message_expiry", w.MessageExpiry > 0}, {"content_type", w.ContentType != ""}, {"user_properties", len(w.UserProperties) > 0}} {
				if f.set {
					errs = append(errs, at.Field(f.name).Errorf("%s needs version 5", f.name))
				}
			}
		}
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

// client is what the connectors ask of a broker: *mqttwire.Client, or a
// fake in the tests.
type client interface {
	Subscribe(ctx context.Context, subs ...mqttwire.Subscription) ([]byte, error)
	Unsubscribe(ctx context.Context, filters ...string) ([]byte, error)
	SessionPresent() bool
	SubscriptionIDsAvailable() bool
	Messages() <-chan *mqttwire.Message
	Ack(m *mqttwire.Message) error
	Send(ctx context.Context, m *mqttwire.Message) (waiter, error)
	Done() <-chan struct{}
	Err() error
	Close() error
}

// waiter is a publish under way: *mqttwire.Delivery.
type waiter interface {
	Wait(ctx context.Context) error
}

// dialer connects a client of a config.
type dialer func(ctx context.Context, cfg mqttwire.Config) (client, error)

type wireClient struct{ *mqttwire.Client }

func (w wireClient) Send(ctx context.Context, m *mqttwire.Message) (waiter, error) {
	d, err := w.Client.Send(ctx, m)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func dialWire(ctx context.Context, cfg mqttwire.Config) (client, error) {
	c, err := mqttwire.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return wireClient{c}, nil
}

// connect connects to the first of servers that takes the connection.
func connect(ctx context.Context, cfg mqttwire.Config, servers []string, dial dialer) (client, error) {
	if dial == nil {
		dial = dialWire
	}
	var errs []error
	for _, s := range servers {
		cfg.Address = s
		c, err := dial(ctx, cfg)
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// testID is a client ID of a connection Test makes, its own so that it does
// not take a running pipeline's session over.
func testID(id string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	if id == "" {
		id = "fit"
	}
	return id + "-test-" + hex.EncodeToString(b[:])
}

// loadCodec loads a codec ref; none is nil, the connectors' own form.
func loadCodec(ctx *module.Context, ref *config.ModuleRef) (fitsync.Codec, error) {
	if ref == nil {
		return nil, nil
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

// permanent reports whether a publish's error is one sending it again
// cannot cure.
func permanent(err error) bool {
	if errors.Is(err, mqttwire.ErrPacketTooLarge) || errors.Is(err, mqttwire.ErrNotSupported) {
		return true
	}
	var re *mqttwire.ReasonError
	if errors.As(err, &re) {
		switch re.Code {
		case 0x87, 0x90, 0x95, 0x99, 0x9A, 0x9B:
			return true
		}
	}
	return false
}

// errorf wraps err, a connector's, with what it was doing.
func errorf(format string, args ...any) error { return fmt.Errorf("mqtt: "+format, args...) }
