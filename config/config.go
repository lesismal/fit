// Package config defines fit's native config, and loads it strictly: an
// unknown field, a value of the wrong type, a missing required field or a
// duplicate key is an error, reported with its path and its line and column.
//
// The native format is JSON. A Config holds what was written, without
// defaults filled in, so that it can be written back out unchanged; package
// runtime applies the defaults when it compiles one.
package config

import (
	"os"
	"reflect"
	"time"
)

// Config is a whole fit config.
type Config struct {
	Listeners []Listener `json:"listeners" fit:"required" doc:"The addresses fit listens on, and the protocols each one serves."`
	Routes    []Route    `json:"routes,omitempty" doc:"How requests are matched, and what handles them."`
	Upstreams []Upstream `json:"upstreams,omitempty" doc:"The servers requests can be proxied to, by name."`
	AccessLog *AccessLog `json:"access_log,omitempty" doc:"Where a line is written for each request, as JSON; no access log when left out."`
	Limits    Limits     `json:"limits,omitzero" doc:"Limits that apply to every listener."`
}

// The defaults of Limits.
const (
	DefaultWriteStallTimeout = Duration(30 * time.Second)
	DefaultMaxPendingBytes   = 256 << 20
	DefaultMaxHeaderBytes    = 1 << 20
	DefaultMaxBodyBytes      = 16 << 20
)

// Limits bound what clients can make fit hold for them.
type Limits struct {
	MaxPendingBytes int64 `json:"max_pending_bytes,omitempty" min:"0" default:"268435456" doc:"How many bytes of responses may wait, across every connection, for clients to take them; past it, every connection stops reading until some go. 256 MiB when left out or 0."`
	MaxHeaderBytes  int   `json:"max_header_bytes,omitempty" min:"0" default:"1048576" doc:"The largest request header a listener takes; a larger one gets 431. 1 MiB when left out or 0."`
	MaxBodyBytes    int64 `json:"max_body_bytes,omitempty" min:"0" default:"16777216" doc:"The largest request body a listener takes, whether it arrives whole or streams through; a larger one gets 413, or, once it has begun to stream, its connection or stream is ended. 16 MiB when left out or 0."`

	WriteStallTimeout Duration `json:"write_stall_timeout,omitempty" default:"\"30s\"" doc:"How long a response may wait for its client to take what it was sent, each time it waits, before the client loses it: its HTTP/1 connection closed, its HTTP/2 or HTTP/3 stream reset. A client that keeps reading, however slowly, is not cut off, and nor is a response that has nothing to send. 30s when left out."`

	At Value `json:"-" fit:"self"`
}

// PendingBytes, HeaderBytes and BodyBytes return the limits, their defaults
// when left out.
func (l Limits) PendingBytes() int64 { return orDefault(l.MaxPendingBytes, DefaultMaxPendingBytes) }
func (l Limits) HeaderBytes() int {
	return int(orDefault(int64(l.MaxHeaderBytes), DefaultMaxHeaderBytes))
}
func (l Limits) BodyBytes() int64 { return orDefault(l.MaxBodyBytes, DefaultMaxBodyBytes) }

func orDefault(v, def int64) int64 {
	if v <= 0 {
		return def
	}
	return v
}

// WriteStall returns the write stall timeout, its default when left out.
func (l Limits) WriteStall() time.Duration {
	if l.WriteStallTimeout == 0 {
		return DefaultWriteStallTimeout.Std()
	}
	return l.WriteStallTimeout.Std()
}

// AccessLog says where access log lines go. Lines are buffered and written
// by a goroutine of their own, so that a slow disk never holds up a request:
// lines past the buffer's limit are dropped and counted.
type AccessLog struct {
	Output        string   `json:"output,omitempty" default:"\"stdout\"" doc:"\"stdout\", \"stderr\", or the path of a file lines are appended to; \"stdout\" when left out."`
	FlushInterval Duration `json:"flush_interval,omitempty" default:"\"1s\"" doc:"How long a line may wait in the buffer before it is written; 1s when left out."`
	BufferBytes   int      `json:"buffer_bytes,omitempty" min:"0" default:"4194304" doc:"How many bytes of lines may wait to be written; lines past it are dropped. 4 MiB when left out."`

	At Value `json:"-" fit:"self"`
}

// Protocols a listener can serve.
const (
	ProtocolHTTP1 = "http/1.1"
	ProtocolH2C   = "h2c"
	ProtocolH2    = "h2"
	ProtocolH3    = "h3"
)

// DefaultProtocols are what a listener that names none serves, and
// DefaultTLSProtocols what one with TLS does.
var (
	DefaultProtocols    = []string{ProtocolHTTP1}
	DefaultTLSProtocols = []string{ProtocolHTTP1, ProtocolH2}
)

// Listener is an address fit listens on.
type Listener struct {
	Name      string   `json:"name" fit:"required" doc:"A name unique among the listeners, by which routes refer to this one."`
	Address   string   `json:"address" fit:"required" doc:"The host and port to listen on, as in \":8080\" or \"127.0.0.1:8080\"."`
	Protocols []string `json:"protocols,omitempty" enum:"http/1.1,h2c,h2,h3" doc:"The protocols served: http/1.1, h2c (HTTP/2 in the clear) and, with tls, h2 (HTTP/2 over TLS, chosen through ALPN) and h3 (HTTP/3 over QUIC, on the same port in UDP, announced to HTTP/1 and HTTP/2 clients with Alt-Svc). http/1.1 alone when left out, or http/1.1 and h2 with tls."`
	TLS       *TLS     `json:"tls,omitempty" doc:"Serve TLS, with these certificates."`

	At Value `json:"-" fit:"self"`
}

// TLS is a listener's TLS: the certificates it serves, chosen by the name the
// client asks for (SNI). It allows TLS 1.2 and 1.3, and only AEAD cipher
// suites with forward secrecy for 1.2.
type TLS struct {
	Certificates []Certificate `json:"certificates" fit:"required" doc:"The certificates served. A client is given the first whose names match the one it asks for, or the first of all when none does or it asks for none."`

	At Value `json:"-" fit:"self"`
}

// Certificate is a certificate and its key, from PEM files.
type Certificate struct {
	CertFile string `json:"cert_file" fit:"required" doc:"A PEM file with the certificate, followed by any intermediates."`
	KeyFile  string `json:"key_file" fit:"required" doc:"A PEM file with the certificate's private key."`

	At Value `json:"-" fit:"self"`
}

// Route matches requests and says what handles them: its middleware in
// order, then its handler.
type Route struct {
	Name       string      `json:"name,omitempty" doc:"A name unique among the routes, for logs and metrics."`
	Listeners  []string    `json:"listeners,omitempty" doc:"The names of the listeners the route serves; every listener when left out."`
	Priority   int         `json:"priority,omitempty" doc:"Routes with a higher priority are picked first; among equal ones, the most specific host, then path, then the one written first."`
	Match      *Match      `json:"match,omitempty" doc:"What a request must match; every request when left out."`
	Middleware []ModuleRef `json:"middleware,omitempty" module:"http.middleware" doc:"Middleware a request passes through, in order, before the handler."`
	Handler    ModuleRef   `json:"handler" fit:"required" module:"http.handlers" doc:"What answers the request."`

	At Value `json:"-" fit:"self"`
}

// Match is what a request must match for a route to take it. Each field
// that is set must match; within a field, any one entry may.
type Match struct {
	Host []string `json:"host,omitempty" doc:"Host names, exact or with a leading \"*.\" wildcard."`
	Path []string `json:"path,omitempty" doc:"Paths, exact or ending in \"*\" to match a prefix."`

	At Value `json:"-" fit:"self"`
}

// Upstream is a set of servers requests can be proxied to, and how the
// connections to them are kept.
type Upstream struct {
	Name           string   `json:"name" fit:"required" doc:"A name unique among the upstreams, by which handlers refer to this one."`
	Addresses      []string `json:"addresses" fit:"required" doc:"The addresses of the upstream's servers, each a host and a port, as in \"10.0.0.1:8080\" or \"backend:80\"."`
	Weights        []int    `json:"weights,omitempty" min:"1" max:"1000" doc:"Each address's weight, in the order of addresses: its share of requests under weighted_round_robin, and how many requests it takes to be as busy as an address of weight 1 under least_conn. 1 each when left out."`
	LoadBalancing  string   `json:"load_balancing,omitempty" enum:"round_robin,weighted_round_robin,least_conn" doc:"How requests are spread over the addresses: in turn, in turn by weight (smoothly, as nginx does), or to the address with the fewest requests under way for its weight. round_robin when left out, or weighted_round_robin when weights are given."`
	MaxConns       int      `json:"max_conns,omitempty" min:"0" default:"256" doc:"The most connections open, or being opened, to one address; a request past it waits for one to be free. 256 when left out or 0."`
	MaxPending     int      `json:"max_pending,omitempty" min:"0" default:"1024" doc:"How many requests may wait for a connection, across the addresses, once max_conns are busy; one past it is answered 503 at once. 1024 when left out or 0."`
	MaxIdleConns   *int     `json:"max_idle_conns,omitempty" min:"0" default:"32" doc:"How many idle connections to one address are kept for later requests; 32 when left out, and 0 keeps none."`
	IdleTimeout    Duration `json:"idle_timeout,omitempty" default:"\"60s\"" doc:"How long an idle connection is kept before it is closed; 60s when left out."`
	ConnectTimeout Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long opening a connection may take; 10s when left out. A request whose connect times out gets 504."`

	Protocol      string `json:"protocol,omitempty" enum:"http/1.1,h2c,h2" default:"\"http/1.1\"" doc:"How requests reach the upstream: http/1.1 in the clear, h2c (HTTP/2 in the clear, which the servers must expect), or h2 (TLS, with HTTP/2 when the server chooses it through ALPN and HTTP/1.1 otherwise). http/1.1 when left out."`
	TLSServerName string `json:"tls_server_name,omitempty" doc:"With h2: the name the servers' certificates are checked against, and sent in SNI; each address's host when left out."`
	TLSCAFile     string `json:"tls_ca_file,omitempty" doc:"With h2: a PEM file of the certificates the servers' are checked against, in place of the system's roots."`

	MaxFails    *int     `json:"max_fails,omitempty" min:"0" default:"1" doc:"How many failed requests to an address within fail_timeout take it out of rotation for fail_timeout, as nginx's max_fails does: a connect that failed, an upstream that broke off or timed out. 1 when left out; 0 keeps every address in rotation."`
	FailTimeout Duration `json:"fail_timeout,omitempty" default:"\"10s\"" doc:"The window max_fails are counted in, and how long an address that reached them stays out; 10s when left out."`

	ResponseHeaderTimeout Duration `json:"response_header_timeout,omitempty" default:"\"60s\"" doc:"How long the upstream may take, once the request is sent, to begin its response; 60s when left out. A request past it gets 504."`
	ResponseIdleTimeout   Duration `json:"response_idle_timeout,omitempty" default:"\"60s\"" doc:"How long the upstream may go silent in the middle of a response body; 60s when left out."`
	ResponseTimeout       Duration `json:"response_timeout,omitempty" default:"\"1h\"" doc:"How long a whole response may take, from sending the request to the end of its body; 1h when left out. A request past it before its response began gets 504, and one past it later is cut short."`

	At Value `json:"-" fit:"self"`
}

// ModuleRef names a module and holds its params, which the module decodes
// itself.
type ModuleRef struct {
	Module string `json:"module" fit:"required" doc:"The module's name within its namespace."`
	Params Value  `json:"params,omitzero" doc:"The module's params."`

	At Value `json:"-" fit:"self"`
}

// Load parses and checks a config. Its error, when there is one, is Errors.
func Load(data []byte) (*Config, error) {
	root, err := parse(data)
	if err != nil {
		return nil, Errors{err.(*Error)}
	}
	var c Config
	var d decoder
	d.decode(root, "", reflect.ValueOf(&c).Elem(), nil)
	if len(d.errs) > 0 {
		return nil, d.errs
	}
	if errs := c.Validate(); len(errs) > 0 {
		return nil, errs
	}
	return &c, nil
}

// LoadFile loads the config in the file at path.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(data)
}
