// Package syncredis is sync's Redis, for Redis 6 and later and Valkey: the
// source redis, which reads streams with XREAD from the checkpoint's entry
// IDs, and the sink redis, which adds records to streams with XADD or keeps
// one key per record (SET, HSET, DEL), pipelined. Both speak RESP2 or RESP3
// through sync/redis/wire, over TCP or TLS, on a connection of their own on
// the pipeline's goroutine. See docs/decisions/036-sync.md.
package syncredis

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/sync/redis/wire"
)

// The defaults of the connection's params.
const (
	DefaultAddress = "127.0.0.1:6379"
	DefaultTimeout = 10 * time.Second
)

// connParams are the params the source and the sink share, declared by
// each as its own, with the same names and docs.
type connParams struct {
	Address, Username, PasswordEnv        string
	DB, Protocol                          int
	TLS                                   bool
	CAFile, CertFile, KeyFile, ServerName string
	Timeout                               config.Duration
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// options checks p and makes the wire's options from it, reading the
// password from the environment and the TLS files.
func options(ctx *module.Context, p connParams) (wire.Options, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	o := wire.Options{Addr: p.Address, Username: p.Username, DB: p.DB, Protocol: p.Protocol, ClientName: "fit-sync"}
	if o.Addr == "" {
		o.Addr = DefaultAddress
	}
	if _, _, err := net.SplitHostPort(o.Addr); err != nil {
		add("address", "%q is not a host and port", o.Addr)
	}
	if p.Protocol != 0 && p.Protocol != 2 && p.Protocol != 3 {
		add("protocol", "%d is not 2 or 3", p.Protocol)
	}
	switch {
	case p.PasswordEnv != "" && !envName.MatchString(p.PasswordEnv):
		add("password_env", "%q is not an environment variable's name", p.PasswordEnv)
	case p.PasswordEnv != "":
		if o.Password = os.Getenv(p.PasswordEnv); o.Password == "" {
			add("password_env", "the environment variable %s is not set, or empty", p.PasswordEnv)
		}
	case p.Username != "":
		add("username", "username needs password_env")
	}
	o.DialTimeout = timeout(p.Timeout)
	if !p.TLS {
		for _, f := range []struct{ name, value string }{{"ca_file", p.CAFile}, {"cert_file", p.CertFile}, {"key_file", p.KeyFile}, {"server_name", p.ServerName}} {
			if f.value != "" {
				add(f.name, "%s needs tls", f.name)
			}
		}
		return o, errs
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
	o.TLS = tc
	return o, errs
}

func timeout(d config.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	return d.Std()
}

// conn holds a connection, made when first needed and made again once it
// breaks.
type conn struct {
	opts wire.Options
	c    *wire.Conn
}

// get returns the connection, dialling it when there is none or it broke.
func (c *conn) get(ctx context.Context) (*wire.Conn, error) {
	if c.c != nil && c.c.Broken() == nil {
		return c.c, nil
	}
	c.close()
	wc, err := wire.Dial(ctx, c.opts)
	if err != nil {
		return nil, err
	}
	c.c = wc
	return wc, nil
}

// test dials a connection of its own, since the module's belongs to its
// pipeline's goroutine: the dial says HELLO, authenticates and selects the
// database; then PING, and the connection is closed (I188b).
func (c *conn) test(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.DialTimeout)
	defer cancel()
	wc, err := wire.Dial(ctx, c.opts)
	if err != nil {
		return err
	}
	defer wc.Close()
	v, err := wc.Do(ctx, "PING")
	if err != nil {
		return err
	}
	if err := v.Err(); err != nil {
		return fmt.Errorf("PING: %w", err)
	}
	return nil
}

func (c *conn) close() {
	if c.c != nil {
		c.c.Close()
		c.c = nil
	}
}
