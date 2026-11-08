// Package syncmongo is sync's MongoDB: the source mongo, which reads change
// streams from a resume token after a snapshot by _id, and the sink mongo.
// Both speak to a replica set's primary (or a mongos) through
// sync/mongo/wire, a blocking client whose connections are used by the
// pipeline's own goroutines, never by a fib worker. See
// docs/decisions/036-sync.md.
//
// This file holds what the source and the sink share: one set of params
// for reaching the server, ConnParams, which both declare first among
// their own, with the same names, tags and docs (config reads no embedded
// structs; a test keeps the three alike), and NewClient, which checks them
// and makes the client.
package syncmongo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

// DefaultTimeout bounds a command when neither timeout nor the connection
// string's socketTimeoutMS sets it.
const DefaultTimeout = 60 * time.Second

// ConnParams are the params for reaching MongoDB, the source's and the
// sink's alike.
type ConnParams struct {
	URI            string          `json:"uri,omitempty" doc:"The servers as a connection string, mongodb://[user@]host1[:port1][,host2...][/authdb][?options], as the MongoDB drivers take it, with the options replicaSet, authSource, authMechanism (SCRAM-SHA-256), tls, tlsCAFile, tlsCertificateKeyFile, connectTimeoutMS, socketTimeoutMS, serverSelectionTimeoutMS, maxPoolSize and appName; any other is refused, and so is a password in it: password_env holds it. mongodb+srv is not read. uri or hosts; the params below replace what it sets."`
	Hosts          []string        `json:"hosts,omitempty" doc:"The servers to find the primary from, host or host:port (27017 when left out), in place of uri: a replica set's members (the others are learned from them), a standalone server, or mongos routers. The primary is found again after a failover."`
	ReplicaSet     string          `json:"replica_set,omitempty" doc:"The replica set's name: a server of another set is not taken. Any set when left out."`
	Username       string          `json:"username,omitempty" doc:"The user to authenticate as, with SCRAM-SHA-256 and password_env, in place of a user in uri; no authentication when neither names one."`
	PasswordEnv    string          `json:"password_env,omitempty" doc:"The environment variable holding the user's password, read when the pipeline loads; needs a user, in username or uri."`
	AuthSource     string          `json:"auth_source,omitempty" doc:"The database the user is defined in. admin when left out."`
	TLS            bool            `json:"tls,omitempty" doc:"Connect over TLS, the servers' certificates checked against the system's roots or ca_file's; the check cannot be turned off. These TLS params replace the connection string's TLS options."`
	CAFile         string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the servers' are checked against, in place of the system's roots, for a CA of its own; needs tls."`
	CertFile       string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, presented to servers that ask for one; with its key in it too, as MongoDB's tlsCertificateKeyFile, unless key_file is set. Needs tls."`
	KeyFile        string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key, when cert_file does not hold it."`
	ServerName     string          `json:"server_name,omitempty" doc:"The name the servers' certificates must have, and the TLS server name sent; each server's host when left out."`
	ConnectTimeout config.Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long connecting to a server may take, its TLS, hello and authentication. 10s when left out or 0."`
	Timeout        config.Duration `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one command may take, from sending it to the end of its answer: a batch of a cursor, a bulk write of up to the sink's max_request_bytes. 60s when left out or 0."`
	MaxConns       int             `json:"max_conns,omitempty" min:"0" max:"1000" default:"4" doc:"The most connections open to the primary, the open cursors' among them, shared by a job's parallel readers or writers. 4 when left out or 0."`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewClient checks p and makes a client of it, reading the password from
// the environment and the TLS files; nothing is sent until the client is
// first used. Errors name the params' fields.
func NewClient(ctx *module.Context, p ConnParams) (*mongowire.Client, config.Errors) {
	o, errs := p.options(ctx)
	if len(errs) > 0 {
		return nil, errs
	}
	c, err := mongowire.New(o)
	if err != nil {
		return nil, config.Errors{ctx.Params.Field("uri").Errorf("%v", err)}
	}
	return c, nil
}

// options checks p and makes the client's options of it.
func (p ConnParams) options(ctx *module.Context) (mongowire.Options, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	var o mongowire.Options
	switch {
	case p.URI != "" && len(p.Hosts) > 0:
		add("uri", "uri and hosts are each the whole of the servers: set one")
	case p.URI != "":
		var err error
		if o, err = mongowire.ParseURI(p.URI); err != nil {
			add("uri", "%v", err)
		} else if o.Password != "" {
			add("uri", "the connection string holds a password: name the environment variable holding it with password_env")
		}
	case len(p.Hosts) > 0:
		for _, h := range p.Hosts {
			if strings.TrimSpace(h) == "" || strings.ContainsAny(h, ",/?@") {
				add("hosts", "%q is not a host or host:port", h)
			}
		}
		o.Hosts = p.Hosts
	default:
		add("uri", "the servers are needed: uri or hosts")
	}
	if p.ReplicaSet != "" {
		o.ReplicaSet = p.ReplicaSet
	}
	if p.AuthSource != "" {
		o.AuthSource = p.AuthSource
	}
	if p.Username != "" {
		if o.Username != "" {
			add("username", "the connection string names a user already")
		}
		o.Username = p.Username
	}
	switch {
	case p.PasswordEnv != "" && !envName.MatchString(p.PasswordEnv):
		add("password_env", "%q is not an environment variable's name", p.PasswordEnv)
	case p.PasswordEnv != "" && o.Username == "":
		add("password_env", "password_env needs a user: username, or the user in uri")
	case p.PasswordEnv != "":
		if o.Password = os.Getenv(p.PasswordEnv); o.Password == "" {
			add("password_env", "the environment variable %s is not set, or empty", p.PasswordEnv)
		}
	case p.Username != "":
		add("username", "username needs password_env")
	case o.Username != "":
		add("uri", "the connection string's user needs password_env")
	}
	if p.ConnectTimeout > 0 {
		o.ConnectTimeout = p.ConnectTimeout.Std()
	}
	if p.Timeout > 0 {
		o.Timeout = p.Timeout.Std()
	}
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}
	if p.MaxConns > 0 {
		o.MaxConns = p.MaxConns
	}
	if o.AppName == "" {
		o.AppName = "fit-sync"
	}
	if !p.TLS {
		for _, f := range []struct{ name, value string }{{"ca_file", p.CAFile}, {"cert_file", p.CertFile}, {"key_file", p.KeyFile}, {"server_name", p.ServerName}} {
			if f.value != "" {
				add(f.name, "%s needs tls", f.name)
			}
		}
		return o, errs
	}
	// The fields' TLS replaces the connection string's.
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
	switch {
	case p.KeyFile != "" && p.CertFile == "":
		add("key_file", "key_file needs cert_file")
	case p.CertFile != "":
		key := p.KeyFile
		if key == "" {
			key = p.CertFile // MongoDB's own PEM holds the certificate and its key
		}
		cert, err := tls.LoadX509KeyPair(p.CertFile, key)
		if err != nil {
			add("cert_file", "%v", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	o.TLS = tc
	return o, errs
}

// Ping checks that c reaches a primary and authenticates, for a
// connector's Test.
func Ping(ctx context.Context, c *mongowire.Client) error {
	if c == nil {
		return errors.New("mongo: not provisioned")
	}
	if _, err := c.Run(ctx, "admin", mongowire.Doc("ping", 1)); err != nil {
		return fmt.Errorf("mongo: %w", err)
	}
	return nil
}
