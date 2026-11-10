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
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
	"github.com/lesismal/fit/sync/mssql/wire/krb5"
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
	// Auth is the login other than SQL authentication's, when set.
	Auth *Auth
}

// The login methods of Auth.
const (
	AuthEntra    = "entra"
	AuthKerberos = "kerberos"
	AuthNTLM     = "ntlm"
)

// DefaultKrb5Conf is where Kerberos's realm and KDCs are read from when
// the params name neither.
const DefaultKrb5Conf = "/etc/krb5.conf"

// Auth is the param auth, the same in the source and the sink: a login
// with Microsoft Entra ID (Azure SQL), Kerberos or NTLM in place of SQL
// authentication's username and password.
type Auth struct {
	Method                string   `json:"method" fit:"required" enum:"entra,kerberos,ntlm" doc:"How the login is made: entra, Microsoft Entra ID (Azure SQL Database and Managed Instance), an access token (a service principal's, a managed identity's, or one given) sent in TDS's FEDAUTH, username and password_env unused; kerberos, Windows authentication with a Kerberos ticket, username the principal (user, or user@REALM), its key from password_env or keytab_file; ntlm, Windows authentication with NTLMv2, username DOMAIN\\user and password_env its password, for a server outside a domain (weaker than Kerberos: the server is not authenticated)."`
	TenantID              string   `json:"tenant_id,omitempty" doc:"With entra, the service principal's directory (tenant) ID or domain."`
	ClientID              string   `json:"client_id,omitempty" doc:"With entra, the service principal's application (client) ID; with managed_identity, a user-assigned identity's client ID (the system-assigned identity when left out)."`
	ClientSecretEnv       string   `json:"client_secret_env,omitempty" doc:"With entra, the environment variable holding the service principal's client secret, read when the pipeline loads."`
	ClientCertificateFile string   `json:"client_certificate_file,omitempty" doc:"With entra, a PEM file of the service principal's certificate and its RSA private key, which signs the client assertion in place of a secret."`
	ManagedIdentity       bool     `json:"managed_identity,omitempty" doc:"With entra, the token is the host's managed identity's, from Azure's instance metadata service (or App Service's identity endpoint, when IDENTITY_ENDPOINT is set). false when left out."`
	TokenEnv              string   `json:"token_env,omitempty" doc:"With entra, the environment variable holding an access token for https://database.windows.net/, read at each connection, for a token got elsewhere."`
	TokenFile             string   `json:"token_file,omitempty" doc:"With entra, a file holding an access token for https://database.windows.net/, read at each connection, so that whatever renews it may replace it."`
	Authority             string   `json:"authority,omitempty" default:"\"https://login.microsoftonline.com\"" doc:"With entra and a service principal, the token endpoint's origin: another cloud's (https://login.microsoftonline.us, https://login.chinacloudapi.cn). https://login.microsoftonline.com when left out."`
	Realm                 string   `json:"realm,omitempty" doc:"With kerberos, the principal's realm; username's @REALM, or krb5_conf's default_realm, when left out."`
	KDC                   []string `json:"kdc,omitempty" doc:"With kerberos, the realm's KDCs, as host or host:port (88), tried in turn over TCP; krb5_conf's when left out."`
	Krb5Conf              string   `json:"krb5_conf,omitempty" default:"\"/etc/krb5.conf\"" doc:"With kerberos, the krb5.conf whose default_realm and realms' kdc lines are read when realm or kdc is left out. /etc/krb5.conf when left out."`
	KeytabFile            string   `json:"keytab_file,omitempty" doc:"With kerberos, a keytab file holding the principal's keys (AES), in place of password_env."`
	SPN                   string   `json:"spn,omitempty" doc:"With kerberos, the server's service principal name, as MSSQLSvc/host.example.com:1433; MSSQLSvc/ with address's host and port when left out (the host must then be the name the SPN was registered with, not an IP address)."`
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
	if p.Auth == nil && c.User == "" {
		add("username", "the SQL login is needed, unless auth says another")
	}
	if p.Auth != nil {
		errs = append(errs, authConfig(ctx, p, &c)...)
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

// authConfig checks the param auth and sets c's login from it.
func authConfig(ctx *module.Context, p ConnParams, c *mssqlwire.Config) config.Errors {
	var errs config.Errors
	a := p.Auth
	field := ctx.Params.Field("auth")
	add := func(name, format string, args ...any) {
		errs = append(errs, field.Field(name).Errorf(format, args...))
	}
	// Each method's params, and the others' refused.
	given := map[string]bool{
		"tenant_id": a.TenantID != "", "client_id": a.ClientID != "", "client_secret_env": a.ClientSecretEnv != "",
		"client_certificate_file": a.ClientCertificateFile != "", "managed_identity": a.ManagedIdentity,
		"token_env": a.TokenEnv != "", "token_file": a.TokenFile != "", "authority": a.Authority != "",
		"realm": a.Realm != "", "kdc": len(a.KDC) > 0, "krb5_conf": a.Krb5Conf != "", "keytab_file": a.KeytabFile != "", "spn": a.SPN != "",
	}
	own := map[string][]string{
		AuthEntra:    {"tenant_id", "client_id", "client_secret_env", "client_certificate_file", "managed_identity", "token_env", "token_file", "authority"},
		AuthKerberos: {"realm", "kdc", "krb5_conf", "keytab_file", "spn"},
		AuthNTLM:     nil,
	}
	// The method is one of them: the schema's enum checked it.
	mine := own[a.Method]
	for name, set := range given {
		if set && !strings.Contains(" "+strings.Join(mine, " ")+" ", " "+name+" ") {
			add(name, "%s is not a param of method %s", name, a.Method)
		}
	}
	if a.Method != AuthEntra && c.User == "" {
		errs = append(errs, ctx.Params.Field("username").Errorf("method %s needs username", a.Method))
	}
	switch a.Method {
	case AuthEntra:
		if p.Username != "" || p.PasswordEnv != "" {
			errs = append(errs, ctx.Params.Field("username").Errorf("method entra takes no username or password_env: the token names the identity"))
		}
		if c.Encrypt == mssqlwire.EncryptOff {
			errs = append(errs, ctx.Params.Field("encrypt").Errorf("method entra needs encrypt on or strict: the token would go in clear"))
		}
		tok, err := entraToken(a)
		if err != nil {
			add("method", "%v", err)
		}
		c.AccessToken = tok
	case AuthKerberos:
		kc, err := kerberosClient(a, c.User, c.Password, p.PasswordEnv)
		if err != nil {
			add("method", "%v", err)
			break
		}
		c.User, c.Password, c.SPN = "", "", a.SPN
		c.SSPI = func() mssqlwire.SSPI { return &krb5.Initiator{Client: kc} }
	case AuthNTLM:
		if p.PasswordEnv == "" {
			errs = append(errs, ctx.Params.Field("password_env").Errorf("method ntlm needs password_env"))
		}
		domain, user := mssqlwire.ParseNTLMUser(c.User)
		n := &mssqlwire.NTLM{Domain: domain, User: user, Password: c.Password}
		c.User, c.Password = "", ""
		c.SSPI = func() mssqlwire.SSPI { return n }
	}
	return errs
}

// entraToken is the access token's source of method entra: a token from
// the environment or a file, read at each connection, or EntraClient's.
func entraToken(a *Auth) (func(context.Context) (string, error), error) {
	var sources []string
	for _, s := range []struct {
		name string
		set  bool
	}{{"token_env", a.TokenEnv != ""}, {"token_file", a.TokenFile != ""}, {"managed_identity", a.ManagedIdentity},
		{"client_secret_env", a.ClientSecretEnv != ""}, {"client_certificate_file", a.ClientCertificateFile != ""}} {
		if s.set {
			sources = append(sources, s.name)
		}
	}
	if len(sources) != 1 {
		return nil, fmt.Errorf("method entra takes one of token_env, token_file, managed_identity, client_secret_env and client_certificate_file, not %d", len(sources))
	}
	switch {
	case a.TokenEnv != "":
		if !envName.MatchString(a.TokenEnv) {
			return nil, fmt.Errorf("token_env %q is not an environment variable's name", a.TokenEnv)
		}
		return func(context.Context) (string, error) {
			if t := strings.TrimSpace(os.Getenv(a.TokenEnv)); t != "" {
				return t, nil
			}
			return "", fmt.Errorf("the environment variable %s is not set, or empty", a.TokenEnv)
		}, nil
	case a.TokenFile != "":
		return func(context.Context) (string, error) {
			b, err := os.ReadFile(a.TokenFile)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(b)), nil
		}, nil
	}
	e := &mssqlwire.EntraClient{TenantID: a.TenantID, ClientID: a.ClientID, ManagedIdentity: a.ManagedIdentity, Authority: a.Authority}
	if !a.ManagedIdentity && (a.TenantID == "" || a.ClientID == "") {
		return nil, errors.New("a service principal needs tenant_id and client_id")
	}
	switch {
	case a.ClientSecretEnv != "":
		if !envName.MatchString(a.ClientSecretEnv) {
			return nil, fmt.Errorf("client_secret_env %q is not an environment variable's name", a.ClientSecretEnv)
		}
		if e.ClientSecret = os.Getenv(a.ClientSecretEnv); e.ClientSecret == "" {
			return nil, fmt.Errorf("the environment variable %s is not set, or empty", a.ClientSecretEnv)
		}
	case a.ClientCertificateFile != "":
		cert, err := tls.LoadX509KeyPair(a.ClientCertificateFile, a.ClientCertificateFile)
		if err != nil {
			return nil, fmt.Errorf("client_certificate_file: %w", err)
		}
		e.Certificate = &cert
	}
	return e.Token, nil
}

// kerberosClient makes method kerberos's client: the principal and realm
// from username (or realm, or krb5_conf's default), the KDCs from kdc or
// krb5_conf, the key from the keytab or the password.
func kerberosClient(a *Auth, user, password, passwordEnv string) (*krb5.Client, error) {
	name, realm, _ := strings.Cut(user, "@")
	if a.Realm != "" {
		realm = a.Realm
	}
	kdcs := a.KDC
	if realm == "" || len(kdcs) == 0 {
		path := a.Krb5Conf
		if path == "" {
			path = DefaultKrb5Conf
		}
		conf, err := krb5.LoadConfig(path)
		if err != nil {
			return nil, fmt.Errorf("kerberos needs realm and kdc, or a krb5.conf: %w", err)
		}
		if realm == "" {
			realm = conf.DefaultRealm
		}
		if len(kdcs) == 0 {
			kdcs = conf.KDCs[realm]
		}
	}
	if realm == "" || len(kdcs) == 0 {
		return nil, errors.New("kerberos needs the realm and its KDCs: realm and kdc, or a krb5.conf naming them")
	}
	kc := &krb5.Client{Realm: realm, User: name, KDCs: kdcs}
	switch {
	case a.KeytabFile != "" && passwordEnv != "":
		return nil, errors.New("kerberos takes keytab_file or password_env, not both")
	case a.KeytabFile != "":
		kt, err := krb5.LoadKeytab(a.KeytabFile)
		if err != nil {
			return nil, err
		}
		kc.Keytab = kt
	case password != "":
		kc.Password = password
	default:
		return nil, errors.New("kerberos needs keytab_file or password_env")
	}
	return kc, nil
}
