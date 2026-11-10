// Package syncnats is sync's NATS, for nats-server 2.10 and later: the
// source nats, which reads a JetStream stream through a durable pull
// consumer, acknowledging each message once every sink has it, the stream
// sequence in the pipeline's checkpoint so that a consumer lost is made
// again where the checkpoint is; or core NATS subjects, at most once, for
// telemetry; and the sink nats, which publishes records to JetStream with
// a Nats-Msg-Id from their positions, so that the stream's duplicate window
// drops a batch sent again, or to core NATS. Both speak the protocol
// through sync/nats/wire, on connections of their own, never on a fib
// worker. See docs/decisions/036-sync.md.
package syncnats

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	synckafka "github.com/lesismal/fit/sync/kafka"
	natswire "github.com/lesismal/fit/sync/nats/wire"
)

// DefaultTimeout bounds connecting and each request's answer.
const DefaultTimeout = 10 * time.Second

// The headers the sink writes a record's key, op and namespace in, as the
// RabbitMQ sink's fit-key, fit-op and fit-namespace; the source reads the
// key back from keyHeader.
const (
	keyHeader       = "Fit-Key"
	opHeader        = "Fit-Op"
	namespaceHeader = "Fit-Namespace"
)

// connParams are the params the source and the sink share, declared by
// each as its own, with the same names and docs.
type connParams struct {
	Servers                               []string
	Name                                  string
	TLS                                   bool
	CAFile, CertFile, KeyFile, ServerName string
	TokenEnv, Username, PasswordEnv       string
	NKeyFile, CredsFile                   string
	Timeout                               config.Duration
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// wireConfig checks p and makes the client's config from it, reading the
// secrets from the environment and the files.
func wireConfig(ctx *module.Context, p connParams) (natswire.Config, time.Duration, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	c := natswire.Config{Servers: p.Servers, Name: p.Name}
	if c.Name == "" {
		c.Name = "fit-sync"
	}
	if len(p.Servers) == 0 {
		add("servers", "no servers")
	}
	for _, s := range p.Servers {
		if _, _, err := net.SplitHostPort(s); err != nil {
			add("servers", "%q is not a host and port", s)
		}
	}
	t := DefaultTimeout
	if p.Timeout > 0 {
		t = p.Timeout.Std()
	}
	c.DialTimeout = t
	env := func(field, name string) string {
		if !envName.MatchString(name) {
			add(field, "%q is not an environment variable's name", name)
			return ""
		}
		v := os.Getenv(name)
		if v == "" {
			add(field, "the environment variable %s is not set, or empty", name)
		}
		return v
	}
	methods := 0
	for _, m := range []string{p.TokenEnv, p.Username, p.NKeyFile, p.CredsFile} {
		if m != "" {
			methods++
		}
	}
	if methods > 1 {
		add("token_env", "token_env, username, nkey_file and creds_file are each a way to authenticate: name one")
	}
	if p.TokenEnv != "" {
		c.Token = env("token_env", p.TokenEnv)
	}
	switch {
	case p.Username != "" && p.PasswordEnv != "":
		c.User, c.Password = p.Username, env("password_env", p.PasswordEnv)
	case p.Username != "" || p.PasswordEnv != "":
		add("username", "username and password_env go together")
	}
	if p.NKeyFile != "" {
		if b, err := os.ReadFile(p.NKeyFile); err != nil {
			add("nkey_file", "%v", err)
		} else if c.NKey, err = natswire.ParseSeed(string(b)); err != nil {
			add("nkey_file", "%s: %v", p.NKeyFile, err)
		}
	}
	if p.CredsFile != "" {
		b, err := os.ReadFile(p.CredsFile)
		var seed string
		if err == nil {
			c.JWT, seed, err = natswire.ParseCreds(b)
		}
		if err == nil {
			c.NKey, err = natswire.ParseSeed(seed)
		}
		if err != nil {
			add("creds_file", "%v", err)
		}
	}
	if !p.TLS {
		for _, f := range []struct{ name, value string }{{"ca_file", p.CAFile}, {"cert_file", p.CertFile}, {"key_file", p.KeyFile}, {"server_name", p.ServerName}} {
			if f.value != "" {
				add(f.name, "%s needs tls", f.name)
			}
		}
		return c, t, errs
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
	return c, t, errs
}

// holder holds a client, made when first needed, shared by the source's
// run and its acknowledgements.
type holder struct {
	cfg natswire.Config
	mu  sync.Mutex
	c   *natswire.Client
}

func (h *holder) get(ctx context.Context) (*natswire.Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.c != nil {
		return h.c, nil
	}
	c, err := natswire.Dial(ctx, h.cfg)
	if err != nil {
		return nil, err
	}
	h.c = c
	return c, nil
}

// current returns the client, nil when there is none.
func (h *holder) current() *natswire.Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.c
}

func (h *holder) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
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

// validSubject reports whether s may be published to: tokens of no white
// space and no wildcards.
func validSubject(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n*>") {
		return false
	}
	for _, t := range strings.Split(s, ".") {
		if t == "" {
			return false
		}
	}
	return true
}

// validFilter reports whether s may be subscribed to: a subject whose
// tokens may be * or, last, >.
func validFilter(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	t := strings.Split(s, ".")
	for i, x := range t {
		if x == "" || x == ">" && i != len(t)-1 || len(x) > 1 && strings.ContainsAny(x, "*>") {
			return false
		}
	}
	return true
}

// validName reports whether s may name a stream or consumer.
func validName(s string) bool {
	return s != "" && !strings.ContainsAny(s, " \t\r\n.*>/\\")
}
