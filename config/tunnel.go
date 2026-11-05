package config

import (
	"crypto/tls"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// The tunnel's roles: a hub that nodes and proxies link to, a node beside
// the services it exposes, and a proxy that forwards local ports and web
// proxies through a hub to them; see docs/tunnel.md.

// The protocols a tunnel listener or endpoint may take.
const (
	TunnelTCP  = "tcp"
	TunnelWS   = "ws"
	TunnelQUIC = "quic"
)

// The defaults of the tunnel's roles.
const (
	// DefaultTunnelWSPath is the path of a ws listener.
	DefaultTunnelWSPath          = "/tunnel"
	DefaultTunnelPoolSize        = 10
	DefaultTunnelMaxLinks        = 1024
	DefaultTunnelMaxLinksPerPeer = 128
	DefaultTunnelMaxPeers        = 1024
	DefaultTunnelHubTunnels      = 16384
	DefaultTunnelTunnels         = 4096
	DefaultTunnelMaxHandshakes   = 64
	DefaultTunnelProxyConns      = 32
)

// The defaults of the tunnel's durations.
var (
	DefaultTunnelRegisterTimeout = Duration(10 * time.Second)
	DefaultTunnelHubCall         = Duration(15 * time.Second)
	DefaultTunnelCallTimeout     = Duration(10 * time.Second)
	DefaultTunnelDialTimeout     = Duration(5 * time.Second)
	DefaultTunnelRetryInterval   = Duration(3 * time.Second)
	DefaultTunnelPingInterval    = Duration(5 * time.Second)
	DefaultTunnelPingTimeout     = Duration(5 * time.Second)
	DefaultTunnelOpenTimeout     = Duration(20 * time.Second)
	DefaultTunnelIdleTimeout     = Duration(2 * time.Hour)
	DefaultTunnelQUICIdle        = Duration(30 * time.Second)
	DefaultTunnelQUICKeepAlive   = Duration(10 * time.Second)
	DefaultTunnelQUICHandshake   = Duration(10 * time.Second)
)

// TunnelHub is the tunnel's hub role.
type TunnelHub struct {
	Name               string               `json:"name,omitempty" doc:"The name the hub tells the nodes and proxies that register. The host's name when left out."`
	Password           string               `json:"password,omitempty" doc:"The password nodes and proxies register with, which they prove they know without sending it. The hub refuses every registration while it is left out."`
	BodyPassword       string               `json:"body_password,omitempty" doc:"When set, the body of every frame on the links is encrypted with AES-256-GCM under a key made from it, and nodes and proxies need the same one. It applies to the links that open after it changes."`
	Listeners          []TunnelListener     `json:"listeners,omitempty" doc:"Where nodes and proxies link to the hub, each over its protocol. None when left out: the console's pages add them."`
	ConsoleSocket      *TunnelConsoleSocket `json:"console_socket,omitempty" doc:"An encrypted WebSocket that carries the hub's console pages to the web_proxies of proxies that cannot reach them otherwise. Needs console. None when left out."`
	QUIC               *TunnelHubQUIC       `json:"quic,omitempty" doc:"The certificate and the connections of the quic listeners. The hub makes a self-signed certificate and writes it here when a quic listener needs one and none is set."`
	RegisterTimeout    Duration             `json:"register_timeout,omitempty" default:"\"10s\"" doc:"How long a link may take to register once it is open; it is closed past it. 10s when left out."`
	CallTimeout        Duration             `json:"call_timeout,omitempty" default:"\"15s\"" doc:"How long a node may take to answer the hub: to open a tunnel, or to list or set its services. 15s when left out."`
	MaxLinks           int                  `json:"max_links,omitempty" min:"0" default:"1024" doc:"The most links the hub keeps open at once, registered or not; one past it is closed as soon as it opens. 1024 when left out or 0."`
	MaxLinksPerPeer    int                  `json:"max_links_per_peer,omitempty" min:"0" default:"128" doc:"The most links one node or proxy may register, from however many processes of the same name. 128 when left out or 0."`
	MaxPeers           int                  `json:"max_peers,omitempty" min:"0" default:"1024" doc:"The most nodes and proxies registered at once. 1024 when left out or 0."`
	MaxTunnels         int                  `json:"max_tunnels,omitempty" min:"0" default:"16384" doc:"The most tunnels the hub passes at once; a proxy's request for one more is refused. 16384 when left out or 0."`
	AllowProxyServices bool                 `json:"allow_proxy_services,omitempty" doc:"Let registered proxies set nodes' services through the hub, as a proxy's pages do; a node takes them only with its allow_hub_services. Every proxy that has the password could then do it. Off when left out: only the hub's own pages ask nodes to change their services."`
	Console            *RoleConsole         `json:"console,omitempty" doc:"The hub's web pages; none when left out."`

	At Value `json:"-" fit:"self"`
}

// TunnelListener is where nodes and proxies link to a hub.
type TunnelListener struct {
	Protocol string `json:"protocol" fit:"required" enum:"tcp,ws,quic" doc:"The protocol: tcp, a WebSocket (ws), for networks that let only HTTP through, or quic, over UDP."`
	Address  string `json:"address" fit:"required" doc:"The host and port it listens on, as in \":7000\"; an empty host is every address."`
	Path     string `json:"path,omitempty" default:"\"/tunnel\"" doc:"A ws listener's path. /tunnel when left out."`

	At Value `json:"-" fit:"self"`
}

// TunnelConsoleSocket is the encrypted WebSocket that carries a hub's
// console to proxies.
type TunnelConsoleSocket struct {
	Address  string `json:"address" fit:"required" doc:"The host and port the WebSocket listens on; it answers on any path."`
	Password string `json:"password,omitempty" doc:"The password the WebSocket's messages are encrypted with, which a proxy's web_proxy needs. The hub's password when left out."`
	MaxConns int    `json:"max_conns,omitempty" min:"0" default:"32" doc:"The most WebSockets open at once; one past it is closed as soon as it opens. 32 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// TunnelHubQUIC is the QUIC of a hub's quic listeners.
type TunnelHubQUIC struct {
	Cert             string   `json:"cert,omitempty" doc:"The PEM certificate the quic listeners present, which nodes and proxies pin by its SHA-256 fingerprint; with key. The hub makes a self-signed one and writes it here when a quic listener needs one and none is set."`
	Key              string   `json:"key,omitempty" doc:"The PEM private key of cert."`
	IdleTimeout      Duration `json:"idle_timeout,omitempty" default:"\"30s\"" doc:"How long a QUIC connection may hear nothing from its peer before it is closed. 30s when left out."`
	KeepAlive        Duration `json:"keep_alive,omitempty" default:"\"10s\"" doc:"How often a quiet QUIC connection is pinged, so that it neither idles out nor loses its NAT mapping. 10s when left out."`
	HandshakeTimeout Duration `json:"handshake_timeout,omitempty" default:"\"10s\"" doc:"How long a QUIC handshake may take. 10s when left out."`
	MaxHandshakes    int      `json:"max_handshakes,omitempty" min:"0" default:"64" doc:"The most QUIC handshakes under way at once; a new connection past it is dropped, and its proxy tries again. 64 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// TunnelNode is the tunnel's node role, beside the services it exposes.
type TunnelNode struct {
	Name             string            `json:"name,omitempty" doc:"The node's name, which proxies name to reach its services. The host's name when left out."`
	Meta             map[string]string `json:"meta,omitempty" doc:"Anything else the node tells the hubs, which their pages show."`
	Hubs             []TunnelUplink    `json:"hubs,omitempty" doc:"The hubs the node links to and registers with. None when left out: the console's pages add them."`
	Services         []TunnelService   `json:"services,omitempty" doc:"The services the node reaches, and the only addresses it opens tunnels to."`
	AllowHubServices bool              `json:"allow_hub_services,omitempty" doc:"Let the hubs the node registers with set its services, which are the addresses it opens tunnels to, and write them into this member. Off when left out: only this member and the node's own pages set them."`
	DialTimeout      Duration          `json:"dial_timeout,omitempty" default:"\"5s\"" doc:"How long a connect to a service may take; shorter than a hub's call_timeout, so that the hub hears either way. 5s when left out."`
	IdleTimeout      Duration          `json:"idle_timeout,omitempty" default:"\"2h\"" doc:"How long a tunnel may carry nothing either way before it is closed. 2h when left out."`
	MaxTunnels       int               `json:"max_tunnels,omitempty" min:"0" default:"4096" doc:"The most tunnels open at once, over every hub; a hub's request for one more is refused. 4096 when left out or 0."`
	Console          *RoleConsole      `json:"console,omitempty" doc:"The node's web pages; none when left out."`

	At Value `json:"-" fit:"self"`
}

// TunnelService is a service a node reaches.
type TunnelService struct {
	Name        string `json:"name" fit:"required" doc:"The service's name among the node's, which proxies' forwards name."`
	Address     string `json:"address" fit:"required" doc:"The service's TCP address, host and port."`
	Description string `json:"description,omitempty" doc:"What the service is, for the pages."`

	At Value `json:"-" fit:"self"`
}

// TunnelProxy is the tunnel's proxy role.
type TunnelProxy struct {
	Name        string            `json:"name,omitempty" doc:"The proxy's name, which the hubs and nodes show. The host's name when left out."`
	Meta        map[string]string `json:"meta,omitempty" doc:"Anything else the proxy tells the hubs, which their pages show."`
	Hubs        []TunnelUplink    `json:"hubs,omitempty" doc:"The hubs the proxy links to and registers with. None when left out: the console's pages add them."`
	Forwards    []TunnelForward   `json:"forwards,omitempty" doc:"Local addresses whose connections are tunneled to a node's service."`
	WebProxies  []TunnelWebProxy  `json:"web_proxies,omitempty" doc:"Local addresses that serve a hub's console pages through its encrypted console_socket."`
	OpenTimeout Duration          `json:"open_timeout,omitempty" default:"\"20s\"" doc:"How long the opening of a tunnel may take, through the hub to the node and on to the service. 20s when left out."`
	IdleTimeout Duration          `json:"idle_timeout,omitempty" default:"\"2h\"" doc:"How long a tunnel may carry nothing either way before it is closed. 2h when left out."`
	MaxTunnels  int               `json:"max_tunnels,omitempty" min:"0" default:"4096" doc:"The most tunnels open at once, over every forward; a connection past it is closed as soon as it is accepted. 4096 when left out or 0."`
	Console     *RoleConsole      `json:"console,omitempty" doc:"The proxy's web pages; none when left out."`

	At Value `json:"-" fit:"self"`
}

// TunnelForward is a local address whose connections are tunneled to a
// node's service.
type TunnelForward struct {
	Listen  string `json:"listen" fit:"required" doc:"The local host and port the forward listens on."`
	Node    string `json:"node" fit:"required" doc:"The node whose service the tunnels go to."`
	Service string `json:"service" fit:"required" doc:"The node's service the tunnels go to."`
	Hub     string `json:"hub,omitempty" doc:"The name of the hub the tunnels go through. When left out, they are spread over every hub that has the node, more of them through the faster ones."`

	At Value `json:"-" fit:"self"`
}

// TunnelWebProxy serves a hub's console pages locally, through the
// hub's console_socket.
type TunnelWebProxy struct {
	Name     string `json:"name,omitempty" doc:"The hub's name, for the pages."`
	Listen   string `json:"listen" fit:"required" doc:"The local host and port the pages are served on."`
	URL      string `json:"url" fit:"required" doc:"The hub's console_socket, as ws://host:port/."`
	Password string `json:"password" fit:"required" doc:"The console_socket's password."`
	MaxConns int    `json:"max_conns,omitempty" min:"0" default:"32" doc:"The most connections open at once; one past it is closed as soon as it is accepted. 32 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// TunnelUplink is a hub a node or proxy links to.
type TunnelUplink struct {
	Name          string           `json:"name,omitempty" doc:"The hub's name here, which forwards and the pages name it by. The first endpoint's address when left out."`
	Endpoints     []TunnelEndpoint `json:"endpoints" fit:"required" doc:"The hub's addresses, in the order each link tries them: it dials the first one not disabled, and the next when that fails."`
	Password      string           `json:"password" fit:"required" doc:"The hub's password."`
	BodyPassword  string           `json:"body_password,omitempty" doc:"The hub's body_password, when it has one."`
	QUICPin       string           `json:"quic_pin,omitempty" doc:"The SHA-256 fingerprint of the hub's QUIC certificate, as its pages show it, which a quic endpoint checks the certificate against. Without it, or quic_insecure, the certificate is checked against the system's roots."`
	QUICInsecure  bool             `json:"quic_insecure,omitempty" doc:"Take any QUIC certificate, for tests only."`
	PoolSize      int              `json:"pool_size,omitempty" min:"0" max:"100" default:"10" doc:"How many links to the hub carry tunnels; each tunnel goes over one throughout. 10 when left out or 0."`
	DialTimeout   Duration         `json:"dial_timeout,omitempty" default:"\"5s\"" doc:"How long a link's dial may take. 5s when left out."`
	CallTimeout   Duration         `json:"call_timeout,omitempty" default:"\"10s\"" doc:"How long the hub may take to answer a registration or a call. 10s when left out."`
	RetryInterval Duration         `json:"retry_interval,omitempty" default:"\"3s\"" doc:"How long a link waits after a failed dial, or a lost hub, before it dials again. 3s when left out."`
	PingInterval  Duration         `json:"ping_interval,omitempty" default:"\"5s\"" doc:"How often the round trip to the hub is measured. 5s when left out."`
	PingTimeout   Duration         `json:"ping_timeout,omitempty" default:"\"5s\"" doc:"How long a ping may go unanswered before it counts as lost. 5s when left out."`

	At Value `json:"-" fit:"self"`
}

// TunnelEndpoint is one address of a hub.
type TunnelEndpoint struct {
	Address  string `json:"address" fit:"required" doc:"The hub's address: host:port or tcp://host:port for TCP, ws://host:port/path for a WebSocket, quic://host:port for QUIC."`
	Disabled bool   `json:"disabled,omitempty" doc:"Leave the endpoint out of the dials, keeping it for later."`

	At Value `json:"-" fit:"self"`
}

// tunnelHostName is the default name of a role.
func tunnelHostName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "fit"
}

// The settings of a hub, their defaults when left out.

func (s *TunnelHub) HubName() string {
	if s.Name == "" {
		return tunnelHostName()
	}
	return s.Name
}
func (s *TunnelHub) Register() time.Duration {
	return time.Duration(orDefault(int64(s.RegisterTimeout), int64(DefaultTunnelRegisterTimeout)))
}
func (s *TunnelHub) Call() time.Duration {
	return time.Duration(orDefault(int64(s.CallTimeout), int64(DefaultTunnelHubCall)))
}
func (s *TunnelHub) Links() int { return int(orDefault(int64(s.MaxLinks), DefaultTunnelMaxLinks)) }
func (s *TunnelHub) LinksPerPeer() int {
	return int(orDefault(int64(s.MaxLinksPerPeer), DefaultTunnelMaxLinksPerPeer))
}
func (s *TunnelHub) Peers() int { return int(orDefault(int64(s.MaxPeers), DefaultTunnelMaxPeers)) }
func (s *TunnelHub) Tunnels() int {
	return int(orDefault(int64(s.MaxTunnels), DefaultTunnelHubTunnels))
}

// SocketPassword returns the console socket's password: its own, or the
// hub's.
func (s *TunnelHub) SocketPassword() string {
	if s.ConsoleSocket != nil && s.ConsoleSocket.Password != "" {
		return s.ConsoleSocket.Password
	}
	return s.Password
}

// Conns returns the most WebSockets open at once.
func (c *TunnelConsoleSocket) Conns() int {
	return int(orDefault(int64(c.MaxConns), DefaultTunnelProxyConns))
}

// The settings of a hub's QUIC, their defaults when left out; q may be
// nil.
func (q *TunnelHubQUIC) Idle() time.Duration {
	if q == nil {
		return DefaultTunnelQUICIdle.Std()
	}
	return time.Duration(orDefault(int64(q.IdleTimeout), int64(DefaultTunnelQUICIdle)))
}
func (q *TunnelHubQUIC) Ping() time.Duration {
	if q == nil {
		return DefaultTunnelQUICKeepAlive.Std()
	}
	return time.Duration(orDefault(int64(q.KeepAlive), int64(DefaultTunnelQUICKeepAlive)))
}
func (q *TunnelHubQUIC) Handshake() time.Duration {
	if q == nil {
		return DefaultTunnelQUICHandshake.Std()
	}
	return time.Duration(orDefault(int64(q.HandshakeTimeout), int64(DefaultTunnelQUICHandshake)))
}
func (q *TunnelHubQUIC) Handshakes() int {
	if q == nil {
		return DefaultTunnelMaxHandshakes
	}
	return int(orDefault(int64(q.MaxHandshakes), DefaultTunnelMaxHandshakes))
}

// URL returns the address the listener is dialed on, its scheme naming its
// protocol: host:port for tcp, ws://host:port/path, quic://host:port.
func (l TunnelListener) URL() string {
	switch l.Protocol {
	case TunnelTCP:
		return l.Address
	case TunnelWS:
		return "ws://" + l.Address + l.WSPath()
	}
	return l.Protocol + "://" + l.Address
}

// WSPath returns a ws listener's path, its default when left out.
func (l TunnelListener) WSPath() string {
	if l.Path == "" {
		return DefaultTunnelWSPath
	}
	return l.Path
}

// Key returns what tells listeners apart: two with the same key are the
// same listener.
func (l TunnelListener) Key() string {
	if l.Protocol == TunnelWS {
		return l.Protocol + " " + l.Address + " " + l.WSPath()
	}
	return l.Protocol + " " + l.Address
}

// The settings of a node, their defaults when left out.

func (n *TunnelNode) NodeName() string {
	if n.Name == "" {
		return tunnelHostName()
	}
	return n.Name
}
func (n *TunnelNode) Dial() time.Duration {
	return time.Duration(orDefault(int64(n.DialTimeout), int64(DefaultTunnelDialTimeout)))
}
func (n *TunnelNode) Idle() time.Duration {
	return time.Duration(orDefault(int64(n.IdleTimeout), int64(DefaultTunnelIdleTimeout)))
}
func (n *TunnelNode) Tunnels() int { return int(orDefault(int64(n.MaxTunnels), DefaultTunnelTunnels)) }

// The settings of a proxy, their defaults when left out.

func (c *TunnelProxy) ProxyName() string {
	if c.Name == "" {
		return tunnelHostName()
	}
	return c.Name
}
func (c *TunnelProxy) Open() time.Duration {
	return time.Duration(orDefault(int64(c.OpenTimeout), int64(DefaultTunnelOpenTimeout)))
}
func (c *TunnelProxy) Idle() time.Duration {
	return time.Duration(orDefault(int64(c.IdleTimeout), int64(DefaultTunnelIdleTimeout)))
}
func (c *TunnelProxy) Tunnels() int {
	return int(orDefault(int64(c.MaxTunnels), DefaultTunnelTunnels))
}

// Conns returns the most connections a web proxy keeps open at once.
func (w *TunnelWebProxy) Conns() int {
	return int(orDefault(int64(w.MaxConns), DefaultTunnelProxyConns))
}

// The settings of an uplink, their defaults when left out.

// HubName returns the uplink's name: its own, or its first endpoint's
// address.
func (u *TunnelUplink) HubName() string {
	if u.Name != "" {
		return u.Name
	}
	if len(u.Endpoints) > 0 {
		return u.Endpoints[0].Address
	}
	return ""
}
func (u *TunnelUplink) Pool() int { return int(orDefault(int64(u.PoolSize), DefaultTunnelPoolSize)) }
func (u *TunnelUplink) Dial() time.Duration {
	return time.Duration(orDefault(int64(u.DialTimeout), int64(DefaultTunnelDialTimeout)))
}
func (u *TunnelUplink) Call() time.Duration {
	return time.Duration(orDefault(int64(u.CallTimeout), int64(DefaultTunnelCallTimeout)))
}
func (u *TunnelUplink) Retry() time.Duration {
	return time.Duration(orDefault(int64(u.RetryInterval), int64(DefaultTunnelRetryInterval)))
}
func (u *TunnelUplink) Ping() time.Duration {
	return time.Duration(orDefault(int64(u.PingInterval), int64(DefaultTunnelPingInterval)))
}
func (u *TunnelUplink) PingWait() time.Duration {
	return time.Duration(orDefault(int64(u.PingTimeout), int64(DefaultTunnelPingTimeout)))
}

// Protocol returns the protocol of the endpoint's address: its scheme, or
// tcp for host:port.
func (e TunnelEndpoint) Protocol() string {
	if s, _, ok := strings.Cut(e.Address, "://"); ok {
		return s
	}
	return TunnelTCP
}

// checkTunnelEndpoint returns what is wrong with an endpoint's address, or
// "".
func checkTunnelEndpoint(address string) string {
	switch p := (TunnelEndpoint{Address: address}).Protocol(); p {
	case TunnelTCP:
		hostport := strings.TrimPrefix(address, "tcp://")
		if host, port, err := net.SplitHostPort(hostport); err != nil || host == "" || port == "" || port == "0" {
			return "\"" + address + "\" is not a host and port to dial, as in \"hub.example:7000\""
		}
	case TunnelWS, TunnelQUIC:
		u, err := url.Parse(address)
		if err != nil || u.Hostname() == "" || p == TunnelQUIC && u.Port() == "" {
			return "\"" + address + "\" is not a " + p + ":// URL with a host and port"
		}
		if p == TunnelQUIC && u.Path != "" && u.Path != "/" {
			return "\"" + address + "\" has a path, which a quic:// address does not"
		}
	default:
		return "\"" + address + "\" has the scheme " + p + ": use host:port, ws:// or quic://"
	}
	return ""
}

// checkTunnelName returns what is wrong with a role's name, or "".
func checkTunnelName(name string) string {
	if name != "" && strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\n\r\t") {
		return "\"" + name + "\" is not a name: no spaces around it, and no '/' or control characters"
	}
	return ""
}

// checkTunnel checks the tunnel's roles beyond what decoding does.
func (c *Config) checkTunnel(add func(Value, string, ...any)) {
	r := c.Tunnel
	if r == nil {
		return
	}
	if s := r.Hub; s != nil {
		checkTunnelHub(s, add)
	}
	if n := r.Node; n != nil {
		checkRoleConsole(n.Console, add)
		if msg := checkTunnelName(n.Name); msg != "" {
			add(n.At.Field("name"), "%s", msg)
		}
		checkTunnelUplinks(n.Hubs, n.At.Field("hubs"), add)
		for i, s := range n.Services {
			at := n.At.Field("services").Index(i)
			switch msg := checkTunnelName(s.Name); {
			case s.Name == "":
				add(at.Field("name"), "the name is empty")
			case msg != "":
				add(at.Field("name"), "%s", msg)
			case slices.IndexFunc(n.Services, func(o TunnelService) bool { return o.Name == s.Name }) < i:
				add(at.Field("name"), "another service is already named %q", s.Name)
			}
			if host, port, err := net.SplitHostPort(s.Address); err != nil || host == "" || port == "" || port == "0" {
				add(at.Field("address"), "%q is not a host and port to connect to, as in \"10.0.0.5:22\"", s.Address)
			}
		}
	}
	if cl := r.Proxy; cl != nil {
		checkTunnelProxy(cl, add)
	}
}

func checkTunnelHub(s *TunnelHub, add func(Value, string, ...any)) {
	checkRoleConsole(s.Console, add)
	if msg := checkTunnelName(s.Name); msg != "" {
		add(s.At.Field("name"), "%s", msg)
	}
	seen := map[string]int{}
	for i, l := range s.Listeners {
		at := s.At.Field("listeners").Index(i)
		if _, msg := checkAddress(l.Address); msg != "" {
			add(at.Field("address"), "%s", msg)
		}
		switch {
		case l.Path != "" && l.Protocol != TunnelWS:
			add(at.Field("path"), "only a ws listener has a path")
		case l.Path != "" && !strings.HasPrefix(l.Path, "/"):
			add(at.Field("path"), "%q does not start with '/'", l.Path)
		}
		// tcp and ws listen on TCP, quic on UDP; port 0 picks a free one.
		network := "tcp"
		if l.Protocol == TunnelQUIC {
			network = "udp"
		}
		if _, port, _ := net.SplitHostPort(l.Address); port != "0" {
			key := network + " " + l.Address
			if j, ok := seen[key]; ok {
				add(at.Field("address"), "listener %d listens on %s already", j, l.Address)
			}
			seen[key] = i
		}
	}
	if cs := s.ConsoleSocket; cs != nil {
		if s.Console == nil {
			add(cs.At, "console_socket carries the console, which this hub lacks")
		}
		if _, msg := checkAddress(cs.Address); msg != "" {
			add(cs.At.Field("address"), "%s", msg)
		}
		if s.SocketPassword() == "" {
			add(cs.At.Field("password"), "a password is needed, its own or the hub's")
		}
	}
	if q := s.QUIC; q != nil {
		switch {
		case (q.Cert == "") != (q.Key == ""):
			add(q.At, "cert and key go together")
		case q.Cert != "":
			if _, err := tls.X509KeyPair([]byte(q.Cert), []byte(q.Key)); err != nil {
				add(q.At.Field("cert"), "the certificate and key do not load: %v", err)
			}
		}
	}
}

func checkTunnelUplinks(ups []TunnelUplink, at Value, add func(Value, string, ...any)) {
	for i, u := range ups {
		uat := at.Index(i)
		if msg := checkTunnelName(u.Name); msg != "" {
			add(uat.Field("name"), "%s", msg)
		}
		if slices.IndexFunc(ups, func(o TunnelUplink) bool { return o.HubName() == u.HubName() }) < i {
			add(uat.Field("name"), "another hub is already named %q", u.HubName())
		}
		if len(u.Endpoints) == 0 {
			add(uat.Field("endpoints"), "list at least one endpoint")
		}
		on := 0
		for j, e := range u.Endpoints {
			if msg := checkTunnelEndpoint(e.Address); msg != "" {
				add(uat.Field("endpoints").Index(j).Field("address"), "%s", msg)
			}
			if slices.IndexFunc(u.Endpoints, func(o TunnelEndpoint) bool { return o.Address == e.Address }) < j {
				add(uat.Field("endpoints").Index(j).Field("address"), "%q is listed twice", e.Address)
			}
			if !e.Disabled {
				on++
			}
		}
		if len(u.Endpoints) > 0 && on == 0 {
			add(uat.Field("endpoints"), "every endpoint is disabled")
		}
		if u.Password == "" {
			add(uat.Field("password"), "the password is empty")
		}
	}
}

func checkTunnelProxy(cl *TunnelProxy, add func(Value, string, ...any)) {
	checkRoleConsole(cl.Console, add)
	if msg := checkTunnelName(cl.Name); msg != "" {
		add(cl.At.Field("name"), "%s", msg)
	}
	checkTunnelUplinks(cl.Hubs, cl.At.Field("hubs"), add)
	listens := map[string]string{}
	listen := func(at Value, address, what string) {
		if _, msg := checkAddress(address); msg != "" {
			add(at, "%s", msg)
			return
		}
		if _, port, _ := net.SplitHostPort(address); port == "0" {
			return
		}
		if other, ok := listens[address]; ok {
			add(at, "%s listens on %s already", other, address)
		}
		listens[address] = what
	}
	for i, f := range cl.Forwards {
		at := cl.At.Field("forwards").Index(i)
		listen(at.Field("listen"), f.Listen, "a forward")
		if f.Hub != "" && !slices.ContainsFunc(cl.Hubs, func(u TunnelUplink) bool { return u.HubName() == f.Hub }) {
			add(at.Field("hub"), "there is no hub named %q", f.Hub)
		}
		if f.Node == "" {
			add(at.Field("node"), "the node is empty")
		}
		if f.Service == "" {
			add(at.Field("service"), "the service is empty")
		}
	}
	for i, w := range cl.WebProxies {
		at := cl.At.Field("web_proxies").Index(i)
		listen(at.Field("listen"), w.Listen, "a web proxy")
		if u, err := url.Parse(w.URL); err != nil || u.Scheme != TunnelWS || u.Host == "" {
			add(at.Field("url"), "%q is not a ws:// URL", w.URL)
		}
		if w.Password == "" {
			add(at.Field("password"), "the password is empty")
		}
	}
}
