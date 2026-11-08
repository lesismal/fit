package syncmongo

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"regexp"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

// sinkConnParams are the sink's params for the connection.
type sinkConnParams struct {
	URI                                   string
	Hosts                                 []string
	ReplicaSet, Username, PasswordEnv     string
	AuthSource                            string
	TLS                                   bool
	CAFile, CertFile, KeyFile, ServerName string
	ConnectTimeout, Timeout               config.Duration
	MaxConns                              int
	AppName                               string
}

var sinkEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// sinkOptions checks p and makes the client's options from it, reading the
// password from the environment and the TLS files.
func sinkOptions(ctx *module.Context, p sinkConnParams) (mongowire.Options, config.Errors) {
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
			add("uri", "the connection string holds a password: name its variable with password_env instead")
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
	case p.PasswordEnv != "" && !sinkEnvName.MatchString(p.PasswordEnv):
		add("password_env", "%q is not an environment variable's name", p.PasswordEnv)
	case p.PasswordEnv != "" && o.Username == "":
		add("password_env", "password_env needs username")
	case p.PasswordEnv != "":
		if o.Password = os.Getenv(p.PasswordEnv); o.Password == "" {
			add("password_env", "the environment variable %s is not set, or empty", p.PasswordEnv)
		}
	case o.Username != "":
		add("username", "username needs password_env")
	}
	if p.ConnectTimeout > 0 {
		o.ConnectTimeout = p.ConnectTimeout.Std()
	}
	if p.Timeout > 0 {
		o.Timeout = p.Timeout.Std()
	}
	if p.MaxConns > 0 {
		o.MaxConns = p.MaxConns
	}
	o.AppName = p.AppName
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
