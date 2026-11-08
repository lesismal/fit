// Package syncpgsink is sync's PostgreSQL sink, postgres: upserts by key
// (INSERT ... ON CONFLICT DO UPDATE) and deletes by key, batched per table
// into multi-row statements, a large run of upserts copied (COPY) into a
// temporary table and merged, each batch in one transaction, or each source
// transaction in one. It speaks PostgreSQL's protocol through
// sync/postgres/wire, on blocking connections of its own on the pipeline's
// goroutines, never on fib's workers. See docs/decisions/036-sync.md.
package syncpgsink

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// The defaults of the connection's params.
const (
	DefaultAddress = "127.0.0.1:5432"
	DefaultTimeout = 60 * time.Second
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// connConfig checks the connection's params and makes the wire's config
// from them, reading the password from the environment and the TLS files.
func (s *Sink) connConfig(ctx *module.Context) (pgwire.Config, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	cfg := pgwire.Config{Addr: s.Address, User: s.User, Database: s.Database, SSLMode: s.SSLMode,
		ChannelBinding: s.ChannelBinding, ApplicationName: "fit-sync"}
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddress
	}
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		add("address", "%q is not a host and port", cfg.Addr)
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = pgwire.SSLVerifyFull
	}
	switch {
	case s.PasswordEnv != "" && !envName.MatchString(s.PasswordEnv):
		add("password_env", "%q is not an environment variable's name", s.PasswordEnv)
	case s.PasswordEnv != "":
		if cfg.Password = os.Getenv(s.PasswordEnv); cfg.Password == "" {
			add("password_env", "the environment variable %s is not set, or empty", s.PasswordEnv)
		}
	}
	if cfg.SSLMode == pgwire.SSLDisable {
		for _, f := range []struct{ name, value string }{{"ca_file", s.CAFile}, {"cert_file", s.CertFile}, {"key_file", s.KeyFile}, {"server_name", s.ServerName}} {
			if f.value != "" {
				add(f.name, "%s needs TLS: sslmode is disable", f.name)
			}
		}
		if s.ChannelBinding == pgwire.ChannelBindingRequire {
			add("channel_binding", "channel binding needs TLS: sslmode is disable")
		}
		return cfg, errs
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.ServerName}
	if s.CAFile != "" {
		pem, err := os.ReadFile(s.CAFile)
		tc.RootCAs = x509.NewCertPool()
		if err != nil {
			add("ca_file", "%v", err)
		} else if !tc.RootCAs.AppendCertsFromPEM(pem) {
			add("ca_file", "%s holds no PEM certificate", s.CAFile)
		}
	}
	if (s.CertFile == "") != (s.KeyFile == "") {
		add("cert_file", "cert_file and key_file go together")
	} else if s.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
		if err != nil {
			add("cert_file", "%v", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	cfg.TLS = tc
	return cfg, errs
}

// pool keeps the sink's idle connections: one for a stream's writer, one
// for each of a job's writers. A connection is made when none is idle, and
// one that broke, or is left inside a transaction, is closed rather than
// kept.
type pool struct {
	cfg    pgwire.Config
	mu     sync.Mutex
	idle   []*pgwire.Conn
	closed bool
}

func (p *pool) get(ctx context.Context) (*pgwire.Conn, error) {
	p.mu.Lock()
	var c *pgwire.Conn
	if n := len(p.idle); n > 0 {
		c, p.idle = p.idle[n-1], p.idle[:n-1]
	}
	p.mu.Unlock()
	if c != nil {
		return c, nil
	}
	return pgwire.Connect(ctx, p.cfg)
}

func (p *pool) put(c *pgwire.Conn) {
	if c.Err() != nil || c.TxStatus() != 'I' {
		c.Close()
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		c.Close()
		return
	}
	p.idle = append(p.idle, c)
	p.mu.Unlock()
}

func (p *pool) close() {
	p.mu.Lock()
	idle := p.idle
	p.idle, p.closed = nil, true
	p.mu.Unlock()
	for _, c := range idle {
		c.Close()
	}
}
