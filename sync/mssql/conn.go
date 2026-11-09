// Package syncmssql is sync's SQL Server: the source mssql, which reads a
// snapshot of the tables by key without locks, then their changes from
// SQL Server's CDC change tables (or, lighter, its change tracking),
// polled by LSN, each transaction's changes together. It speaks TDS
// through sync/mssql/wire, on a connection of its own on the pipeline's
// goroutine, never on a fib worker. See docs/decisions/036-sync.md.
package syncmssql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// The defaults of the connection's params.
const (
	DefaultAddress = "127.0.0.1:1433"
	DefaultTimeout = 60 * time.Second
)

// ConnParams are the params a connector's connection takes, declared by
// each connector as its own, with the same names and docs.
type ConnParams struct {
	Address, Username, PasswordEnv, Database string
	Encrypt, CAFile, ServerName              string
	Timeout                                  config.Duration
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// WireConfig checks p and makes the client's config from it, reading the
// password from the environment and the CA file.
func WireConfig(ctx *module.Context, p ConnParams) (mssqlwire.Config, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	c := mssqlwire.Config{Addr: p.Address, User: p.Username, Database: p.Database, AppName: "fit-sync", Encrypt: p.Encrypt}
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
	if c.Encrypt == mssqlwire.EncryptOff {
		for _, f := range []struct{ name, value string }{{"ca_file", p.CAFile}, {"server_name", p.ServerName}} {
			if f.value != "" {
				add(f.name, "%s needs encrypt on or strict", f.name)
			}
		}
		return c, errs
	}
	tc := &tls.Config{ServerName: p.ServerName}
	if p.CAFile != "" {
		pemText, err := os.ReadFile(p.CAFile)
		tc.RootCAs = x509.NewCertPool()
		if err != nil {
			add("ca_file", "%v", err)
		} else if !tc.RootCAs.AppendCertsFromPEM(pemText) {
			add("ca_file", "%s holds no PEM certificate", p.CAFile)
		}
	}
	c.TLS = tc
	return c, errs
}

// conn is what the source asks of a connection: *mssqlwire.Conn, or a fake
// in the tests.
type conn interface {
	// QueryAll returns a query's first result set whole.
	QueryAll(ctx context.Context, sql string, args ...any) ([]mssqlwire.Column, [][]any, error)
	// Each hands each row of a query's first result set to fn, in order,
	// as it reads it; the values are fn's until it returns.
	Each(ctx context.Context, fn func(cols []mssqlwire.Column, vals []any) error, sql string, args ...any) error
	Begin(ctx context.Context, isolation string) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	Close() error
}

// dialWire connects with the client.
func dialWire(ctx context.Context, cfg mssqlwire.Config) (conn, error) {
	c, err := mssqlwire.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return wireConn{c}, nil
}

// wireConn is the client's connection as a conn.
type wireConn struct{ *mssqlwire.Conn }

func (c wireConn) Each(ctx context.Context, fn func(cols []mssqlwire.Column, vals []any) error, sql string, args ...any) error {
	rows, err := c.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		if err := fn(rows.Columns(), rows.Values()); err != nil {
			rows.Close()
			return err
		}
	}
	return rows.Close()
}
