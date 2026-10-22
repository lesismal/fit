package config

import (
	"net"
	"time"
)

// SharedStorage is where instances of fit on several hosts keep what ACME
// obtains, and the locks and challenge answers that let them share it.
// Exactly one of its kinds is set.
type SharedStorage struct {
	Redis          *RedisStorage  `json:"redis,omitempty" doc:"Keep them in Redis, a standalone server or a primary (not Redis Cluster), with locks of SET NX and an expiry."`
	Etcd           *EtcdStorage   `json:"etcd,omitempty" doc:"Keep them in etcd, through its v3 JSON gateway, with locks held by leases."`
	Consul         *ConsulStorage `json:"consul,omitempty" doc:"Keep them in Consul's KV store, with locks held by sessions."`
	Prefix         string         `json:"prefix,omitempty" default:"\"fit/\"" doc:"What the keys kept start with, as fit/certificates/<CA>/<name>.pem; instances that share certificates share it. fit/ when left out."`
	LockTTL        Duration       `json:"lock_ttl,omitempty" default:"\"30s\"" doc:"How long a lock outlives an instance that stopped holding it without giving it back; its holder renews it every third of that. At least 10s; 30s when left out."`
	RequestTimeout Duration       `json:"request_timeout,omitempty" default:"\"10s\"" doc:"How long each request to the store may take, connecting included. 10s when left out."`

	At Value `json:"-" fit:"self"`
}

// The defaults of SharedStorage.
const (
	DefaultSharedStoragePrefix = "fit/"
	DefaultRedisAddress        = "127.0.0.1:6379"
)

// The defaults of SharedStorage's durations.
var (
	DefaultSharedStorageLockTTL = Duration(30 * time.Second)
	DefaultSharedStorageTimeout = Duration(10 * time.Second)
)

// KeyPrefix, TTL and Timeout return s's settings, their defaults when left
// out.
func (s *SharedStorage) KeyPrefix() string {
	if s.Prefix == "" {
		return DefaultSharedStoragePrefix
	}
	return s.Prefix
}

func (s *SharedStorage) TTL() time.Duration {
	if s.LockTTL <= 0 {
		return DefaultSharedStorageLockTTL.Std()
	}
	return s.LockTTL.Std()
}

func (s *SharedStorage) Timeout() time.Duration {
	if s.RequestTimeout <= 0 {
		return DefaultSharedStorageTimeout.Std()
	}
	return s.RequestTimeout.Std()
}

// RedisStorage is a Redis server certificates are kept in.
type RedisStorage struct {
	Address      string `json:"address,omitempty" default:"\"127.0.0.1:6379\"" doc:"The server's host and port. 127.0.0.1:6379 when left out."`
	Username     string `json:"username,omitempty" doc:"The ACL user to authenticate as, with password_file; the default user when left out."`
	PasswordFile string `json:"password_file,omitempty" doc:"A file holding the password to authenticate with, read at each connection; none when left out."`
	DB           int    `json:"db,omitempty" min:"0" doc:"The database selected. 0 when left out."`
	TLS          bool   `json:"tls,omitempty" doc:"Connect over TLS."`
	CAFile       string `json:"ca_file,omitempty" doc:"A PEM file with the CAs the server's certificate is verified with, over TLS; the system's when left out."`
	CertFile     string `json:"cert_file,omitempty" doc:"A PEM file with the client certificate sent over TLS; with key_file."`
	KeyFile      string `json:"key_file,omitempty" doc:"A PEM file with cert_file's private key."`

	At Value `json:"-" fit:"self"`
}

// Addr returns r's address, its default when left out.
func (r *RedisStorage) Addr() string {
	if r.Address == "" {
		return DefaultRedisAddress
	}
	return r.Address
}

// EtcdStorage is an etcd cluster certificates are kept in.
type EtcdStorage struct {
	Endpoints    []string `json:"endpoints,omitempty" default:"[\"http://127.0.0.1:2379\"]" doc:"Where etcd's client API is: http:// or https:// and a host and port, for each member to try, in turn when one fails. http://127.0.0.1:2379 when left out."`
	Username     string   `json:"username,omitempty" doc:"The user to authenticate as, with password_file, when etcd's authentication is on; none when left out."`
	PasswordFile string   `json:"password_file,omitempty" doc:"A file holding username's password, read each time fit authenticates."`
	CAFile       string   `json:"ca_file,omitempty" doc:"A PEM file with the CAs etcd's certificate is verified with, over https; the system's when left out."`
	CertFile     string   `json:"cert_file,omitempty" doc:"A PEM file with the client certificate sent over https; with key_file."`
	KeyFile      string   `json:"key_file,omitempty" doc:"A PEM file with cert_file's private key."`

	At Value `json:"-" fit:"self"`
}

// URLs returns e's endpoints, their default when left out.
func (e *EtcdStorage) URLs() []string {
	if len(e.Endpoints) == 0 {
		return []string{DefaultEtcdEndpoint}
	}
	return e.Endpoints
}

// ConsulStorage is a Consul cluster certificates are kept in.
type ConsulStorage struct {
	Endpoint   string `json:"endpoint,omitempty" default:"\"http://127.0.0.1:8500\"" doc:"Where Consul's HTTP API is, as of the local agent: http:// or https:// and a host and port. http://127.0.0.1:8500 when left out."`
	Datacenter string `json:"datacenter,omitempty" doc:"The datacenter whose KV store is used; the agent's when left out."`
	TokenFile  string `json:"token_file,omitempty" doc:"A file holding the ACL token to send, read when fit starts; none when left out. The token needs to read and write the keys under prefix and to make sessions."`
	CAFile     string `json:"ca_file,omitempty" doc:"A PEM file with the CAs Consul's certificate is verified with, over https; the system's when left out."`
	CertFile   string `json:"cert_file,omitempty" doc:"A PEM file with the client certificate sent over https; with key_file."`
	KeyFile    string `json:"key_file,omitempty" doc:"A PEM file with cert_file's private key."`

	At Value `json:"-" fit:"self"`
}

// URL returns c's endpoint, its default when left out.
func (c *ConsulStorage) URL() string {
	if c.Endpoint == "" {
		return DefaultConsulEndpoint
	}
	return c.Endpoint
}

func checkSharedStorage(s *SharedStorage, add func(Value, string, ...any)) {
	kinds := 0
	for _, set := range []bool{s.Redis != nil, s.Etcd != nil, s.Consul != nil} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		add(s.At, "set exactly one kind of shared storage: redis, etcd or consul")
	}
	if s.LockTTL != 0 && s.LockTTL.Std() < 10*time.Second {
		add(s.At.Field("lock_ttl"), "%s is too short: at least 10s, which Consul's sessions need, and more than a renewal takes", s.LockTTL.Std())
	}
	if r := s.Redis; r != nil {
		if r.Address != "" {
			if _, _, err := net.SplitHostPort(r.Address); err != nil {
				add(r.At.Field("address"), "%q is not a host and port", r.Address)
			}
		}
		if r.Username != "" && r.PasswordFile == "" {
			add(r.At.Field("username"), "username needs password_file")
		}
		if !r.TLS && r.CAFile != "" {
			add(r.At.Field("ca_file"), "ca_file needs tls")
		}
		if !r.TLS && r.CertFile != "" {
			add(r.At.Field("cert_file"), "cert_file needs tls")
		}
		checkTLSFiles(r.At, r.CertFile, r.KeyFile, add)
	}
	if e := s.Etcd; e != nil {
		for i, ep := range e.Endpoints {
			checkEndpoint(e.At.Field("endpoints").Index(i), ep, add)
		}
		checkTLSFiles(e.At, e.CertFile, e.KeyFile, add)
		switch {
		case e.Username != "" && e.PasswordFile == "":
			add(e.At.Field("username"), "username needs password_file")
		case e.PasswordFile != "" && e.Username == "":
			add(e.At.Field("password_file"), "password_file needs username")
		}
	}
	if c := s.Consul; c != nil {
		checkEndpoint(c.At.Field("endpoint"), c.Endpoint, add)
		checkTLSFiles(c.At, c.CertFile, c.KeyFile, add)
	}
}
