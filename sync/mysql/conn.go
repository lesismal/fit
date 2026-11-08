// Package syncmysql is sync's MySQL, MariaDB and TiDB: the sink mysql,
// which writes records as upserts (INSERT ... ON DUPLICATE KEY UPDATE) and
// deletes by key, batched per table into multi-row statements, each source
// transaction written as one of the server's. It speaks the protocol
// through sync/mysql/wire, over TCP or TLS, on a connection of its own on
// the pipeline's goroutine, never on a fib worker. See
// docs/decisions/036-sync.md.
package syncmysql

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// The defaults of the connection's params.
const (
	DefaultAddress = "127.0.0.1:3306"
	DefaultTimeout = 60 * time.Second
)

// connParams are the params a connector's connection takes, declared by
// each connector as its own, with the same names and docs.
type connParams struct {
	Address, Username, PasswordEnv, Database string
	TLS                                      bool
	CAFile, CertFile, KeyFile, ServerName    string
	TLSRSA                                   bool
	ServerPublicKeyFile                      string
	Timeout                                  config.Duration
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// wireConfig checks p and makes the client's config from it, reading the
// password from the environment and the TLS and key files.
func wireConfig(ctx *module.Context, p connParams) (mysqlwire.Config, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	c := mysqlwire.Config{Addr: p.Address, User: p.Username, Database: p.Database, Location: time.UTC}
	if c.Addr == "" {
		c.Addr = DefaultAddress
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		add("address", "%q is not a host and port", c.Addr)
	}
	if p.PasswordEnv != "" {
		if !envName.MatchString(p.PasswordEnv) {
			add("password_env", "%q is not an environment variable's name", p.PasswordEnv)
		} else if c.Password = os.Getenv(p.PasswordEnv); c.Password == "" {
			add("password_env", "the environment variable %s is not set, or empty", p.PasswordEnv)
		}
	}
	c.Timeout = DefaultTimeout
	if p.Timeout > 0 {
		c.Timeout = p.Timeout.Std()
	}
	if p.ServerPublicKeyFile != "" {
		pemText, err := os.ReadFile(p.ServerPublicKeyFile)
		if err == nil {
			c.ServerPublicKey, err = mysqlwire.ParsePublicKey(pemText)
		}
		if err != nil {
			add("server_public_key_file", "%v", err)
		}
	}
	if !p.TLS {
		for _, f := range []struct{ name, value string }{{"ca_file", p.CAFile}, {"cert_file", p.CertFile}, {"key_file", p.KeyFile}, {"server_name", p.ServerName}} {
			if f.value != "" {
				add(f.name, "%s needs tls", f.name)
			}
		}
		if p.TLSRSA {
			add("tls_rsa", "tls_rsa needs tls")
		}
		return c, errs
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.ServerName}
	if p.TLSRSA {
		// MySQL 5.7's image offers no ECDHE key exchange, only RSA's, which
		// Go leaves out by default: these are named for it.
		tc.CipherSuites = append(tlsSuites(), tls.TLS_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_RSA_WITH_AES_256_GCM_SHA384)
	}
	if p.CAFile != "" {
		pemText, err := os.ReadFile(p.CAFile)
		tc.RootCAs = x509.NewCertPool()
		if err != nil {
			add("ca_file", "%v", err)
		} else if !tc.RootCAs.AppendCertsFromPEM(pemText) {
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

// tlsSuites are Go's default suites, secure ones, to which tls_rsa adds
// RSA's key exchange.
func tlsSuites() []uint16 {
	var out []uint16
	for _, s := range tls.CipherSuites() {
		out = append(out, s.ID)
	}
	return out
}

// db is what the sink asks of a connection: *mysqlwire.Conn, or a fake in
// the tests.
type db interface {
	Exec(ctx context.Context, query string, args ...any) (mysqlwire.Result, error)
	InsertRows(ctx context.Context, head string, rows [][]any, tail string) (mysqlwire.Result, error)
	// QueryRow returns the first row of query's result, its values copied,
	// nil when there is none.
	QueryRow(ctx context.Context, query string, args ...any) ([]any, error)
	ServerVersion() string
	Err() error
	Close() error
}

// dialWire connects with the client.
func dialWire(ctx context.Context, cfg mysqlwire.Config) (db, error) {
	c, err := mysqlwire.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return wireConn{c}, nil
}

// wireConn is the client's connection as a db.
type wireConn struct{ *mysqlwire.Conn }

func (c wireConn) QueryRow(ctx context.Context, query string, args ...any) ([]any, error) {
	rows, err := c.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []any
	if rows.Next() {
		for _, v := range rows.Values() {
			if b, ok := v.([]byte); ok {
				v = bytes.Clone(b)
			}
			out = append(out, v)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
