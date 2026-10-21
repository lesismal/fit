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
	"slices"
	"strings"
	"time"
)

// Config is a whole fit config.
type Config struct {
	Listeners []Listener `json:"listeners,omitempty" doc:"The addresses fit listens on, and the protocols each one serves. When left out, HTTPS by default makes them from the routes' host names; see auto_https."`
	Routes    []Route    `json:"routes,omitempty" doc:"How requests are matched, and what handles them."`
	Upstreams []Upstream `json:"upstreams,omitempty" doc:"The servers requests can be proxied to, by name."`
	Services  []Service  `json:"services,omitempty" doc:"Layer 4 services: the upstreams the connections of tcp listeners are proxied to, byte for byte, and the datagrams of udp listeners, one for one."`
	AccessLog *AccessLog `json:"access_log,omitempty" doc:"Where a line is written for each request, as JSON; no access log when left out."`
	Limits    Limits     `json:"limits,omitzero" doc:"Limits that apply to every listener."`
	Admin     *Admin     `json:"admin,omitempty" doc:"The Admin API, through which the running config is read and replaced, and which serves metrics in Prometheus's text format at /metrics; off when left out. Its settings are read when fit starts: a change to them takes effect when fit restarts."`
	AutoHTTPS *AutoHTTPS `json:"auto_https,omitempty" doc:"HTTPS by default: a config that writes no listeners is served over HTTPS on port 443, with certificates obtained with acme for the host names its routes match, and over HTTP on port 80 only to be redirected; a listener whose tls is empty gets certificates for the host names of the routes it serves. On when left out."`
	Metrics   *Metrics   `json:"metrics,omitempty" doc:"Count and time requests by listener and route, for the Admin API's /metrics, which serves the rest of fit's metrics without it; off when left out, since it costs every request a little. Needs admin."`
	ACME      *ACME      `json:"acme,omitempty" doc:"How certificates for the names listeners list in tls.acme are obtained and renewed; the defaults when left out. Its settings are read when fit starts: a change to them takes effect when fit restarts. The names follow every reload."`
}

// Metrics turns on the counting of requests. It has no settings yet.
type Metrics struct {
	At Value `json:"-" fit:"self"`
}

// The defaults of ACME.
const (
	DefaultACMEDirectory     = "https://acme-v02.api.letsencrypt.org/directory"
	DefaultACMEMaxConcurrent = 2
)

// The defaults of ACME's durations.
var (
	DefaultACMETimeout        = Duration(10 * time.Minute)
	DefaultACMERequestTimeout = Duration(30 * time.Second)
)

// ACME says where certificates are obtained, and where what is obtained is
// kept. Certificates are renewed once a third of their lifetime is left,
// give or take a little, so that instances sharing a storage do not all
// renew at once; a failure is tried again later and later, up to an hour
// apart.
type ACME struct {
	Directory      string    `json:"directory,omitempty" default:"\"https://acme-v02.api.letsencrypt.org/directory\"" doc:"The URL of the CA's ACME directory. Let's Encrypt's when left out; https://acme-staging-v02.api.letsencrypt.org/directory is its staging CA, for tests."`
	Email          string    `json:"email,omitempty" doc:"The address the CA may write to about the account's certificates; none when left out."`
	CAFile         string    `json:"ca_file,omitempty" doc:"A PEM file of the certificates the directory's is checked against, in place of the system's roots, for a CA of one's own."`
	Storage        string    `json:"storage,omitempty" doc:"The directory accounts, keys and certificates are kept in, which instances of fit may share. $XDG_DATA_HOME/fit, or ~/.local/share/fit, when left out."`
	MaxConcurrent  int       `json:"max_concurrent,omitempty" min:"0" default:"2" doc:"How many certificates are obtained at once; the rest wait their turn. 2 when left out or 0."`
	Timeout        Duration  `json:"timeout,omitempty" default:"\"10m\"" doc:"How long obtaining one certificate may take, challenges included, before it is given up and tried again later. 10m when left out."`
	RequestTimeout Duration  `json:"request_timeout,omitempty" default:"\"30s\"" doc:"How long each request to the CA may take. 30s when left out."`
	OnDemand       *OnDemand `json:"on_demand,omitempty" doc:"How certificates are obtained during handshakes, for listeners whose tls sets on_demand: whom to ask whether a name may have one, and how fast and how many."`
	Challenges     []string  `json:"challenges,omitempty" enum:"http-01,tls-alpn-01" default:"[\"http-01\",\"tls-alpn-01\"]" doc:"The challenges answered, in the order tried: http-01, which the CA checks on port 80 at the name, answered by any listener of fit's in the clear, and tls-alpn-01, which it checks on port 443, answered by the TLS listener that lists the name. A failed attempt is tried again with the next. Both, http-01 first, when left out."`

	At Value `json:"-" fit:"self"`
}

// The defaults of OnDemand.
const (
	DefaultOnDemandRate     = 10
	DefaultOnDemandMaxNames = 1000
)

// The defaults of OnDemand's durations.
var (
	DefaultOnDemandInterval = Duration(time.Minute)
	DefaultOnDemandTimeout  = Duration(10 * time.Second)
	DefaultOnDemandIdle     = Duration(7 * 24 * time.Hour)
)

// OnDemand is how certificates are obtained during handshakes, for names no
// listener lists: only for names the ask URL allows, at most Rate every
// Interval, and MaxNames in all, each dropped once no handshake has asked for
// it for IdleTimeout. A handshake for such a name waits for its
// certificate, on fib's handshake pool and not on a worker, for at most
// Timeout; one that gives up leaves it being obtained for the next.
type OnDemand struct {
	Ask         string   `json:"ask" fit:"required" doc:"A URL fit asks, with the name in the query as domain=<name>, whether a name may have a certificate: an answer of 2xx allows it, and any other refuses it. Refusals are remembered for a minute."`
	Rate        int      `json:"rate,omitempty" min:"0" default:"10" doc:"How many certificates may be obtained every interval; a handshake past it fails. 10 when left out or 0."`
	Interval    Duration `json:"interval,omitempty" default:"\"1m\"" doc:"The interval rate counts over. 1m when left out."`
	MaxNames    int      `json:"max_names,omitempty" min:"0" default:"1000" doc:"How many names may have certificates obtained on demand, in all; past it, a handshake for another fails. 1000 when left out or 0."`
	Timeout     Duration `json:"timeout,omitempty" default:"\"10s\"" doc:"How long a handshake waits for its name's certificate, as long as fib's handshake timeout by default. 10s when left out."`
	IdleTimeout Duration `json:"idle_timeout,omitempty" default:"\"168h\"" doc:"How long a name taken on demand is kept, and renewed, with no handshake asking for it; then it is dropped and no longer counts against max_names, and its certificate stays in the storage for the next handshake that asks for it, which the ask URL must allow again. 168h, a week, when left out."`

	At Value `json:"-" fit:"self"`
}

// Limit, Every, Names, Wait and Idle return o's settings, their defaults when
// left out.
func (o *OnDemand) Limit() int {
	if o.Rate <= 0 {
		return DefaultOnDemandRate
	}
	return o.Rate
}

func (o *OnDemand) Every() time.Duration {
	if o.Interval <= 0 {
		return DefaultOnDemandInterval.Std()
	}
	return o.Interval.Std()
}

func (o *OnDemand) Names() int {
	if o.MaxNames <= 0 {
		return DefaultOnDemandMaxNames
	}
	return o.MaxNames
}

func (o *OnDemand) Wait() time.Duration {
	if o.Timeout <= 0 {
		return DefaultOnDemandTimeout.Std()
	}
	return o.Timeout.Std()
}

func (o *OnDemand) Idle() time.Duration {
	if o.IdleTimeout <= 0 {
		return DefaultOnDemandIdle.Std()
	}
	return o.IdleTimeout.Std()
}

// The challenges ACME answers.
const (
	ChallengeHTTP01    = "http-01"
	ChallengeTLSALPN01 = "tls-alpn-01"
)

// DefaultACMEChallenges are the challenges answered when acme names none.
var DefaultACMEChallenges = []string{ChallengeHTTP01, ChallengeTLSALPN01}

// ChallengeTypes returns the challenges a answers, in order.
func (a *ACME) ChallengeTypes() []string {
	if a == nil || len(a.Challenges) == 0 {
		return DefaultACMEChallenges
	}
	return a.Challenges
}

// DirectoryURL, Concurrency, OrderTimeout and HTTPTimeout return a's
// settings, their defaults when a is nil or they are left out.
func (a *ACME) DirectoryURL() string {
	if a == nil || a.Directory == "" {
		return DefaultACMEDirectory
	}
	return a.Directory
}

func (a *ACME) Concurrency() int {
	if a == nil || a.MaxConcurrent <= 0 {
		return DefaultACMEMaxConcurrent
	}
	return a.MaxConcurrent
}

func (a *ACME) OrderTimeout() time.Duration {
	if a == nil || a.Timeout <= 0 {
		return DefaultACMETimeout.Std()
	}
	return a.Timeout.Std()
}

func (a *ACME) HTTPTimeout() time.Duration {
	if a == nil || a.RequestTimeout <= 0 {
		return DefaultACMERequestTimeout.Std()
	}
	return a.RequestTimeout.Std()
}

// The defaults of Admin.
const (
	DefaultAdminAddress        = "127.0.0.1:2019"
	DefaultAdminHistory        = 10
	DefaultAdminMaxConfigBytes = 4 << 20
	DefaultAdminMaxConns       = 16
)

// AdminUnixPrefix starts an Admin address that is a Unix socket's path.
const AdminUnixPrefix = "unix/"

// Admin is where the Admin API listens, and who may use it.
type Admin struct {
	Address        string `json:"address,omitempty" default:"\"127.0.0.1:2019\"" doc:"Where the Admin API listens: a host and port, or \"unix/\" and the path of a Unix socket, as in \"unix//run/fit/admin.sock\". The host must be a loopback address or localhost unless token_file is set. 127.0.0.1:2019 when left out."`
	TokenFile      string `json:"token_file,omitempty" doc:"A file holding a token that every request must carry, as \"Authorization: Bearer <token>\"; no token is asked for when left out."`
	History        int    `json:"history,omitempty" min:"0" default:"10" doc:"How many of the configs applied, the running one included, are kept to be read back. 10 when left out or 0."`
	MaxConfigBytes int64  `json:"max_config_bytes,omitempty" min:"0" default:"4194304" doc:"The largest config the Admin API takes; a larger one gets 413. 4 MiB when left out or 0."`
	MaxConns       int    `json:"max_conns,omitempty" min:"0" default:"16" doc:"The most connections the Admin API keeps open at once; one past it is closed as soon as it is accepted. 16 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Addr returns the address, its default when left out.
func (a *Admin) Addr() string {
	if a.Address == "" {
		return DefaultAdminAddress
	}
	return a.Address
}

// Network returns the network and address to listen on: "unix" and a path
// for an address with AdminUnixPrefix, and "tcp" and the address otherwise.
func (a *Admin) Network() (network, address string) {
	addr := a.Addr()
	if path, ok := strings.CutPrefix(addr, AdminUnixPrefix); ok {
		return "unix", path
	}
	return "tcp", addr
}

// HistoryLen, ConfigBytes and Conns return the limits, their defaults when
// left out.
func (a *Admin) HistoryLen() int    { return int(orDefault(int64(a.History), DefaultAdminHistory)) }
func (a *Admin) ConfigBytes() int64 { return orDefault(a.MaxConfigBytes, DefaultAdminMaxConfigBytes) }
func (a *Admin) Conns() int         { return int(orDefault(int64(a.MaxConns), DefaultAdminMaxConns)) }

// The defaults of Limits.
const (
	DefaultWriteStallTimeout = Duration(30 * time.Second)
	DefaultMaxPendingBytes   = 256 << 20
	DefaultMaxHeaderBytes    = 1 << 20
	DefaultMaxBodyBytes      = 16 << 20
	DefaultMaxConns          = 100000
)

// Limits bound what clients can make fit hold for them.
type Limits struct {
	MaxPendingBytes int64 `json:"max_pending_bytes,omitempty" min:"0" default:"268435456" doc:"How many bytes of responses may wait, across every connection, for clients to take them; past it, every connection stops reading until some go. 256 MiB when left out or 0."`
	MaxHeaderBytes  int   `json:"max_header_bytes,omitempty" min:"0" default:"1048576" doc:"The largest request header a listener takes; a larger one gets 431. 1 MiB when left out or 0."`
	MaxConns        int   `json:"max_conns,omitempty" min:"0" default:"100000" doc:"The most client connections the listeners keep open at once, across all of them; one past it is closed as soon as it is accepted. Takes effect at start, not on a reload. 100000 when left out or 0."`
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

// Conns returns the connection limit, its default when left out.
func (l Limits) Conns() int { return int(orDefault(int64(l.MaxConns), DefaultMaxConns)) }

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

// Service proxies the connections of tcp listeners, and the sessions of udp
// listeners, to an upstream.
type Service struct {
	Name          string   `json:"name" fit:"required" doc:"A name unique among the services."`
	Listeners     []string `json:"listeners" fit:"required" doc:"The names of the listeners whose connections the service takes: those that serve tcp, alone or beside other protocols, and udp listeners, each one service's. A udp listener's session is a client's address: the datagrams it sends go to one upstream address, from a socket of the session's own, and what comes back to that socket goes to the client."`
	Upstream      string   `json:"upstream" fit:"required" doc:"The name of the upstream connections go to: one of its addresses, picked by its load_balancing (ip_hash by the client's address; header, cookie and URI hashes, which a connection has none of, in turn), out of rotation for its max_fails and health checks as for HTTP, dialed within its connect_timeout. A connect that fails is tried on the next address picked, up to three addresses. Its protocol and HTTP settings are not used."`
	IdleTimeout   Duration `json:"idle_timeout,omitempty" doc:"How long a connection or a udp session may carry nothing either way before both sides are closed; 10m for a connection and 1m for a udp session when left out."`
	MaxConns      int      `json:"max_conns,omitempty" min:"0" default:"10000" doc:"The most connections and udp sessions the service proxies at once; a connection past it is closed as soon as it is accepted, and the datagrams of a session past it are dropped. 10000 when left out or 0."`
	ServerNames   []string `json:"server_names,omitempty" doc:"The server names (SNI) of the TLS connections the service takes from its tcp listeners, which it proxies as they are, without terminating their TLS; \"*.example.com\" takes the names one label below example.com. A tcp listener may then be listed by several services: a connection goes to the one whose names hold the name its ClientHello asks for, or else to the listener's service without server_names, if it has one, as one that is not TLS or asks for no name does; otherwise it is closed. The ClientHello must come within 10s."`
	ProxyProtocol string   `json:"proxy_protocol,omitempty" enum:"v1,v2" doc:"Send each upstream connection a PROXY protocol header of this version (v1, text, or v2, binary) first, naming the client's address and the one it connected to. For tcp listeners."`

	At Value `json:"-" fit:"self"`
}

// The PROXY protocol versions a service can send.
const (
	ProxyProtocolV1 = "v1"
	ProxyProtocolV2 = "v2"
)

// The defaults Service's fields stand for when left out.
const (
	DefaultServiceIdleTimeout    = Duration(10 * time.Minute)
	DefaultServiceUDPIdleTimeout = Duration(time.Minute)
	DefaultServiceMaxConns       = 10000
)

// Idle returns the service's idle timeout for a connection, or its default.
func (s *Service) Idle() time.Duration {
	if s.IdleTimeout <= 0 {
		return DefaultServiceIdleTimeout.Std()
	}
	return s.IdleTimeout.Std()
}

// UDPIdle returns the service's idle timeout for a udp session, or its
// default.
func (s *Service) UDPIdle() time.Duration {
	if s.IdleTimeout <= 0 {
		return DefaultServiceUDPIdleTimeout.Std()
	}
	return s.IdleTimeout.Std()
}

// Conns returns the service's connection limit, or its default.
func (s *Service) Conns() int { return int(orDefault(int64(s.MaxConns), DefaultServiceMaxConns)) }

// Protocols a listener can serve.
const (
	ProtocolHTTP1 = "http/1.1"
	ProtocolH2C   = "h2c"
	ProtocolH2    = "h2"
	ProtocolH3    = "h3"
	// ProtocolTCP is raw TCP, which a service proxies to its upstream as
	// it is. A listener that serves it beside HTTP or SOCKS tells them
	// apart by what a client sends first.
	ProtocolTCP = "tcp"
	// ProtocolUDP is UDP, whose datagrams a service proxies to its upstream
	// as they are; a listener that serves it serves nothing else.
	ProtocolUDP = "udp"
	// ProtocolSOCKS is SOCKS5, SOCKS4 and SOCKS4a: a forward proxy, which
	// connects its clients where they ask, as the listener's socks allows.
	ProtocolSOCKS = "socks"
)

// ServesHTTP reports whether a listener of protocols serves HTTP: one that
// names none serves the default protocols.
func ServesHTTP(protocols []string) bool {
	return len(protocols) == 0 || slices.ContainsFunc(protocols, func(p string) bool {
		return p == ProtocolHTTP1 || p == ProtocolH2C || p == ProtocolH2 || p == ProtocolH3
	})
}

// Sniffs reports whether a listener of protocols serves more than one kind
// of them on its TCP port, HTTP, raw TCP and SOCKS, which it tells apart by
// what each client sends first.
func Sniffs(protocols []string) bool {
	kinds := 0
	if slices.ContainsFunc(protocols, func(p string) bool { return p == ProtocolHTTP1 || p == ProtocolH2C || p == ProtocolH2 }) {
		kinds++
	}
	if slices.Contains(protocols, ProtocolTCP) {
		kinds++
	}
	if slices.Contains(protocols, ProtocolSOCKS) {
		kinds++
	}
	return kinds > 1
}

// DefaultProtocols are what a listener that names none serves, and
// DefaultTLSProtocols what one with TLS does.
var (
	DefaultProtocols    = []string{ProtocolHTTP1}
	DefaultTLSProtocols = []string{ProtocolHTTP1, ProtocolH2}
)

// Listener is an address fit listens on.
type Listener struct {
	Name          string         `json:"name" fit:"required" doc:"A name unique among the listeners, by which routes refer to this one."`
	Address       string         `json:"address" fit:"required" doc:"The host and port to listen on, as in \":8080\" or \"127.0.0.1:8080\"."`
	Protocols     []string       `json:"protocols,omitempty" enum:"http/1.1,h2c,h2,h3,tcp,udp,socks" doc:"The protocols served: http/1.1, h2c (HTTP/2 in the clear) and, with tls, h2 (HTTP/2 over TLS, chosen through ALPN) and h3 (HTTP/3 over QUIC, on the same port in UDP, announced to HTTP/1 and HTTP/2 clients with Alt-Svc); tcp, raw TCP, whose connections the services that list the listener proxy to their upstreams; socks, a SOCKS5 and SOCKS4 forward proxy, as socks says; or udp alone, whose datagrams a service proxies. A listener that serves more than one of HTTP, tcp and socks tells them apart on its port by what a client sends first: a TLS ClientHello goes to the service whose server_names hold its name, or else, with tls, is served HTTPS, or else goes to the service without server_names; a SOCKS greeting to socks; an HTTP request, in the clear, to HTTP; and whatever else to the service without server_names, or it is closed. A client that waits for the server to speak first is closed after 10s: such a protocol needs a listener of its own. http/1.1 alone when left out, or http/1.1 and h2 with tls."`
	TLS           *TLS           `json:"tls,omitempty" doc:"Serve TLS, with these certificates: the listener's HTTP is HTTPS."`
	SOCKS         *ForwardProxy  `json:"socks,omitempty" doc:"Who may use the listener's socks, and where it connects them: needed with socks. It serves CONNECT, without authentication, and refuses BIND and UDP ASSOCIATE."`
	ProxyProtocol *ProxyProtocol `json:"proxy_protocol,omitempty" doc:"Take a PROXY protocol header, of version 1 or 2, at the start of each connection from a trusted address, and serve the connection as from the client it names. For listeners of tcp and of HTTP over TCP; with tls the header comes before TLS. HTTP/3 takes none."`

	At Value `json:"-" fit:"self"`
}

// ForwardProxy is who may use a forward proxy, SOCKS or HTTP CONNECT, and
// where it may connect them to.
type ForwardProxy struct {
	Clients        []string `json:"clients" fit:"required" doc:"The addresses and CIDR ranges of the clients that may use the proxy, as \"10.0.0.0/8\"; others are refused."`
	Allow          []string `json:"allow,omitempty" doc:"The destinations the proxy connects to: host names, \"*.example.com\" for every name below example.com, IP addresses and CIDR ranges, which also hold the address a host name resolves to. Every destination deny does not hold when left out."`
	Deny           []string `json:"deny,omitempty" doc:"Destinations refused, written as in allow, whatever allow holds: a host name that resolves to an address deny holds is refused once it is connected, before anything is sent."`
	Ports          []int    `json:"ports,omitempty" doc:"The destination ports allowed; any when left out."`
	ConnectTimeout Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long resolving and connecting to a destination may take. 10s when left out."`
	IdleTimeout    Duration `json:"idle_timeout,omitempty" default:"\"5m\"" doc:"How long a proxied connection may carry nothing either way before both sides are closed. 5m when left out."`

	At Value `json:"-" fit:"self"`
}

// The defaults ForwardProxy's fields stand for when left out.
const (
	DefaultForwardConnectTimeout = Duration(10 * time.Second)
	DefaultForwardIdleTimeout    = Duration(5 * time.Minute)
)

// Connect returns the proxy's connect timeout, or its default.
func (f *ForwardProxy) Connect() time.Duration {
	return time.Duration(orDefault(int64(f.ConnectTimeout), int64(DefaultForwardConnectTimeout)))
}

// Idle returns the proxy's idle timeout, or its default.
func (f *ForwardProxy) Idle() time.Duration {
	return time.Duration(orDefault(int64(f.IdleTimeout), int64(DefaultForwardIdleTimeout)))
}

// ProxyProtocol is what a listener takes PROXY protocol headers from.
type ProxyProtocol struct {
	Trusted []string `json:"trusted" fit:"required" doc:"The addresses and CIDR ranges of the proxies that send the header, as \"10.0.0.0/8\": a connection from one of them must begin with a header, within 10s, or it is closed, and one from elsewhere is served as it is, with no header."`

	At Value `json:"-" fit:"self"`
}

// networks are the transports the listener takes its address in: "tcp",
// "udp" or both.
func (l *Listener) networks() []string {
	var tcp, udp bool
	for _, p := range l.Protocols {
		switch p {
		case ProtocolUDP, ProtocolH3:
			udp = true
		default:
			tcp = true
		}
	}
	switch {
	case udp && tcp:
		return []string{"tcp", "udp"}
	case udp:
		return []string{"udp"}
	}
	return []string{"tcp"}
}

// TLS is a listener's TLS: the certificates it serves, chosen by the name the
// client asks for (SNI), from files or obtained with ACME. It allows TLS 1.2
// and 1.3, and only AEAD cipher suites with forward secrecy for 1.2.
type TLS struct {
	Certificates []Certificate `json:"certificates,omitempty" doc:"Certificates from files. A client is given the first whose names match the one it asks for, or the first of all when none does or it asks for none."`
	ACME         []string      `json:"acme,omitempty" doc:"Host names to obtain certificates for with ACME, as the top-level acme says, through its challenges: the CA must reach fit at each name on port 80, where any listener of fit's in the clear answers HTTP-01, or on port 443, where this listener answers TLS-ALPN-01. A client asking for one of these names is given its certificate once there is one; until then, and for other names, the certificates from files."`
	OnDemand     bool          `json:"on_demand,omitempty" doc:"Obtain a certificate during the handshake for a name a client asks for that neither the files nor acme have, as the top-level acme.on_demand allows; needs it."`
	ClientAuth   *ClientAuth   `json:"client_auth,omitempty" doc:"Ask clients for certificates and verify them (mutual TLS)."`

	At Value `json:"-" fit:"self"`
}

// Client authentication modes.
const (
	ClientAuthRequire       = "require"
	ClientAuthVerifyIfGiven = "verify_if_given"
)

// ClientAuth is how a TLS listener authenticates its clients by their
// certificates.
type ClientAuth struct {
	CAFiles []string `json:"ca_files" fit:"required" doc:"PEM files of the certificate authorities a client's certificate must chain to. They are watched as certificate files are: a change loads the config again."`
	Mode    string   `json:"mode,omitempty" enum:"require,verify_if_given" default:"\"require\"" doc:"require: a client without a valid certificate is refused in the handshake; verify_if_given: a client may send none, and a route that needs one says so with the client_cert middleware, but one it sends must be valid. require when left out. A listener that requires certificates cannot answer ACME's TLS-ALPN-01 challenge, whose CA sends none."`

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
	Priority   int         `json:"priority,omitempty" doc:"Routes with a higher priority are picked first; among equal ones, the most specific host, then path, then the one with the most other matchers set (method, header, query, remote_ip, path_regexp, expr), then the one written first."`
	Match      *Match      `json:"match,omitempty" doc:"What a request must match; every request when left out."`
	Middleware []ModuleRef `json:"middleware,omitempty" module:"http.middleware" doc:"Middleware a request passes through, in order, before the handler."`
	Handler    ModuleRef   `json:"handler" fit:"required" module:"http.handlers" doc:"What answers the request."`

	At Value `json:"-" fit:"self"`
}

// Match is what a request must match for a route to take it. Each field
// that is set must match; within a field, any one entry may.
type Match struct {
	Host       []string            `json:"host,omitempty" doc:"Host names, exact or with a leading \"*.\" wildcard."`
	Path       []string            `json:"path,omitempty" doc:"Paths, exact or ending in \"*\" to match a prefix."`
	PathRegexp []string            `json:"path_regexp,omitempty" doc:"Regular expressions in Go's syntax, matched against the path anywhere in it unless anchored with ^ and $."`
	Method     []string            `json:"method,omitempty" doc:"Methods, as in \"GET\"; compared as written, since methods are case-sensitive."`
	Header     map[string][]string `json:"header,omitempty" doc:"Headers the request must have, each with one of the values listed as the value of one of its fields, or with any value when none are listed. Names ignore case; values do not."`
	Query      map[string][]string `json:"query,omitempty" doc:"Query parameters the request must have, each with one of the values listed, or with any value when none are listed; compared once unescaped."`
	RemoteIP   []string            `json:"remote_ip,omitempty" doc:"IP addresses and CIDR prefixes, as in \"10.0.0.0/8\", the client's address must be in: the address of the connection, not of X-Forwarded-For."`
	Expr       string              `json:"expr,omitempty" doc:"An expression the request must match, combining matchers with &&, ||, ! and parentheses, as in Host(\"a.example\") && (PathPrefix(\"/api/\") || Method(\"POST\")). Its matchers are Host, Path, PathPrefix, PathRegexp, Method, Header(name, value...), Query(key, value...) and RemoteIP, whose arguments are strings quoted as in Go. It counts as one matcher in the order of routes, and its hosts and paths do not make a route more specific: give it a priority."`

	At Value `json:"-" fit:"self"`
}

// Upstream is a set of servers requests can be proxied to, and how the
// connections to them are kept.
type Upstream struct {
	Name           string        `json:"name" fit:"required" doc:"A name unique among the upstreams, by which handlers refer to this one."`
	Addresses      []string      `json:"addresses" fit:"required" doc:"The addresses of the upstream's servers, each a host and a port, as in \"10.0.0.1:8080\" or \"backend:80\"."`
	Weights        []int         `json:"weights,omitempty" min:"1" max:"1000" doc:"Each address's weight, in the order of addresses: its share of requests under weighted_round_robin, and how many requests it takes to be as busy as an address of weight 1 under least_conn. 1 each when left out."`
	LoadBalancing  string        `json:"load_balancing,omitempty" enum:"round_robin,weighted_round_robin,least_conn,random,p2c,ip_hash,uri_hash,header_hash,cookie_hash" doc:"How requests are spread over the addresses: round_robin, in turn; weighted_round_robin, in turn by weight (smoothly, as nginx does); least_conn, to the address with the fewest requests under way for its weight; random, at random by weight; p2c, to the less busy for its weight of two addresses chosen at random; ip_hash, uri_hash, header_hash and cookie_hash, by a consistent hash of the client's IP address, the request's path and query, the header hash_key names or the cookie it names, so that the same key goes to the same address while it is up, and adding or removing an address moves only the keys that went to it or will. A request without the header or cookie goes in turn. round_robin when left out, or weighted_round_robin when weights are given."`
	HashKey        string        `json:"hash_key,omitempty" doc:"With header_hash, the header, and with cookie_hash, the cookie, whose value requests are hashed by."`
	StickyCookie   *StickyCookie `json:"sticky_cookie,omitempty" doc:"Keep each client on one address with a cookie: a request whose cookie names an address that is up goes there, and any other gets the cookie for the address it went to. No cookie when left out."`
	MaxConns       int           `json:"max_conns,omitempty" min:"0" default:"256" doc:"The most connections open, or being opened, to one address; a request past it waits for one to be free. 256 when left out or 0."`
	MaxPending     int           `json:"max_pending,omitempty" min:"0" default:"1024" doc:"How many requests may wait for a connection to one address once its max_conns are busy; one past it is answered 503 at once. 1024 when left out or 0."`
	MaxIdleConns   *int          `json:"max_idle_conns,omitempty" min:"0" default:"32" doc:"How many idle connections to one address are kept for later requests; 32 when left out, and 0 keeps none."`
	IdleTimeout    Duration      `json:"idle_timeout,omitempty" default:"\"60s\"" doc:"How long an idle connection is kept before it is closed; 60s when left out."`
	ConnectTimeout Duration      `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long opening a connection may take; 10s when left out. A request whose connect times out gets 504."`

	Protocol      string `json:"protocol,omitempty" enum:"http/1.1,h2c,h2" default:"\"http/1.1\"" doc:"How requests reach the upstream: http/1.1 in the clear, h2c (HTTP/2 in the clear, which the servers must expect), or h2 (TLS, with HTTP/2 when the server chooses it through ALPN and HTTP/1.1 otherwise). http/1.1 when left out."`
	TLSServerName string `json:"tls_server_name,omitempty" doc:"With h2: the name the servers' certificates are checked against, and sent in SNI; each address's host when left out."`
	TLSCAFile     string `json:"tls_ca_file,omitempty" doc:"With h2: a PEM file of the certificates the servers' are checked against, in place of the system's roots."`

	MaxFails    *int     `json:"max_fails,omitempty" min:"0" default:"1" doc:"How many failed requests to an address within fail_timeout take it out of rotation for fail_timeout, as nginx's max_fails does: a connect that failed, an upstream that broke off or timed out. 1 when left out; 0 keeps every address in rotation."`
	FailTimeout Duration `json:"fail_timeout,omitempty" default:"\"10s\"" doc:"The window max_fails are counted in, and how long an address that reached them stays out; 10s when left out."`

	ResponseHeaderTimeout Duration `json:"response_header_timeout,omitempty" default:"\"60s\"" doc:"How long the upstream may take, once the request is sent, to begin its response; 60s when left out. A request past it gets 504."`
	ResponseIdleTimeout   Duration `json:"response_idle_timeout,omitempty" default:"\"60s\"" doc:"How long the upstream may go silent in the middle of a response body; 60s when left out."`
	ResponseTimeout       Duration `json:"response_timeout,omitempty" default:"\"1h\"" doc:"How long a whole response may take, from sending the request to the end of its body; 1h when left out. A request past it before its response began gets 504, and one past it later is cut short."`

	HealthCheck *HealthCheck `json:"health_check,omitempty" doc:"Probe each address on a schedule, and take one that fails its probes out of rotation until it passes them again; no probes when left out. Probes and max_fails take an address out each on their own account."`

	At Value `json:"-" fit:"self"`
}

// The default of StickyCookie.Name.
const DefaultStickyCookieName = "fit_sticky"

// StickyCookie is the cookie that keeps a client on one of an upstream's
// addresses. Its value is a hash of the address, the same in every fit with
// the same addresses.
type StickyCookie struct {
	Name   string   `json:"name,omitempty" default:"\"fit_sticky\"" doc:"The cookie's name; fit_sticky when left out."`
	Path   string   `json:"path,omitempty" default:"\"/\"" doc:"The cookie's Path; / when left out."`
	MaxAge Duration `json:"max_age,omitempty" doc:"How long the client keeps the cookie, as its Max-Age; until the browser closes when left out."`
	Secure bool     `json:"secure,omitempty" doc:"Mark the cookie Secure, so that it is sent only over HTTPS. It is always HttpOnly and SameSite=Lax."`

	At Value `json:"-" fit:"self"`
}

// Health check types.
const (
	HealthHTTP = "http"
	HealthTCP  = "tcp"
)

// The defaults of HealthCheck.
const (
	DefaultHealthInterval  = Duration(5 * time.Second)
	DefaultHealthTimeout   = Duration(2 * time.Second)
	DefaultHealthThreshold = 2
)

// HealthCheck is an upstream's active health check: a probe sent to each of
// its addresses every interval.
type HealthCheck struct {
	Type         string   `json:"type,omitempty" enum:"http,tcp" default:"\"http\"" doc:"http sends a GET request, over the upstream's protocol, and passes on an expected status; tcp passes once a connection opens. http when left out."`
	Path         string   `json:"path,omitempty" default:"\"/\"" doc:"With http: the path, and query, the probe asks for; / when left out."`
	Host         string   `json:"host,omitempty" doc:"With http: the Host field the probe sends; the address's host when left out."`
	Port         int      `json:"port,omitempty" min:"1" max:"65535" doc:"The port probes go to, in place of each address's own."`
	ExpectStatus []int    `json:"expect_status,omitempty" min:"100" max:"599" doc:"With http: the statuses that pass; any from 200 to 399 when left out."`
	Interval     Duration `json:"interval,omitempty" default:"\"5s\"" doc:"How long from the start of one probe of an address to the start of the next; 5s when left out."`
	Timeout      Duration `json:"timeout,omitempty" default:"\"2s\"" doc:"How long a probe may take, connecting included, before it fails; 2s when left out. No longer than interval."`
	Jitter       Duration `json:"jitter,omitempty" doc:"Up to how much longer than interval each wait between probes may be, chosen at random each time, so that probes from many upstreams spread out; a tenth of interval when left out. Less than interval."`
	Fails        int      `json:"fails,omitempty" min:"0" default:"2" doc:"How many probes in a row must fail to take an address out of rotation; 2 when left out or 0."`
	Passes       int      `json:"passes,omitempty" min:"0" default:"2" doc:"How many probes in a row must pass to put an address that was out back in; 2 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Every, Wait, Spread, FailsNeeded and PassesNeeded return the settings,
// their defaults when left out.
func (h *HealthCheck) Every() time.Duration {
	if h.Interval == 0 {
		return DefaultHealthInterval.Std()
	}
	return h.Interval.Std()
}
func (h *HealthCheck) Wait() time.Duration {
	if h.Timeout == 0 {
		return DefaultHealthTimeout.Std()
	}
	return h.Timeout.Std()
}
func (h *HealthCheck) Spread() time.Duration {
	if h.Jitter == 0 {
		return h.Every() / 10
	}
	return h.Jitter.Std()
}
func (h *HealthCheck) FailsNeeded() int {
	return int(orDefault(int64(h.Fails), DefaultHealthThreshold))
}
func (h *HealthCheck) PassesNeeded() int {
	return int(orDefault(int64(h.Passes), DefaultHealthThreshold))
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
