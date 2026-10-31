package config

import (
	"encoding/hex"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
)

// The agent's roles link over a mesh of WebSocket connections: each role may
// listen for links and dial any number of peers, which authenticate with a
// shared password (HMAC challenge-response; the password never crosses the
// wire). What their pages change is written back into their members here.
// See docs/agent.md.

// The bounds of the agent's members.
const (
	MaxAgentPeers  = 64
	MaxAgentModels = 256
	MaxAgentKeys   = 256
)

// The defaults of the agent's members.
const (
	DefaultAgentMaxConns          = 1024
	DefaultAgentMaxInflight       = 1024
	DefaultAgentMaxNodes          = 1024
	DefaultAgentNodeMaxInflight   = 256
	DefaultAgentMaxBodyBytes      = 32 << 20
	DefaultAgentClientMaxConns    = 256
	DefaultAgentClientMaxInflight = 256
)

// The defaults of the agent's durations.
var (
	DefaultAgentPingInterval     = Duration(15 * time.Second)
	DefaultAgentSlowTimeout      = Duration(30 * time.Second)
	DefaultAgentRetryInterval    = Duration(5 * time.Second)
	DefaultAgentStreamIdle       = Duration(2 * time.Minute)
	DefaultAgentRefreshInterval  = Duration(30 * time.Second)
	DefaultAgentJobTimeout       = Duration(time.Hour)
	DefaultAgentConnectTimeout   = Duration(10 * time.Second)
	DefaultAgentFirstByteTimeout = Duration(5 * time.Minute)
	DefaultAgentIdleTimeout      = Duration(5 * time.Minute)
	DefaultAgentCallTimeout      = Duration(30 * time.Minute)
)

// AgentListen is where a role takes links from its peers.
type AgentListen struct {
	Address  string `json:"address" fit:"required" doc:"The host and port the role takes links on, as a binding of its own on the listeners' engine; the WebSocket path is /ws. An empty host listens on every address."`
	Password string `json:"password" fit:"required" doc:"The password peers prove they know when they link; it never crosses the network."`

	At Value `json:"-" fit:"self"`
}

// AgentPeer is a peer a role dials and keeps linked to, redialing with
// backoff whenever the link is lost.
type AgentPeer struct {
	Address  string `json:"address" fit:"required" doc:"The peer's host and port, as its listen.address says."`
	Password string `json:"password" fit:"required" doc:"The peer's listen.password."`
	Role     string `json:"role" fit:"required" enum:"hub,node,client" doc:"The role the peer must announce; a link to a peer of another role is refused. Never the dialing role's own."`

	At Value `json:"-" fit:"self"`
}

// AgentLimits bound a role's links.
type AgentLimits struct {
	MaxConns      int      `json:"max_conns,omitempty" min:"0" default:"1024" doc:"The most links the role takes from its peers at once; one past it is refused, and its dialer backs off. 1024 when left out or 0."`
	MaxInflight   int      `json:"max_inflight,omitempty" min:"0" default:"1024" doc:"The most calls and streams a peer may have running here on one link at once; one past it fails at once. 1024 when left out or 0."`
	PingInterval  Duration `json:"ping_interval,omitempty" default:"\"15s\"" doc:"How long a link may hear nothing before it is pinged; one that hears nothing for three of them is closed. 15s when left out."`
	SlowTimeout   Duration `json:"slow_timeout,omitempty" default:"\"30s\"" doc:"How long a ping may wait for its pong, behind everything sent before it, before the link is taken for too slow and closed. 30s when left out."`
	RetryInterval Duration `json:"retry_interval,omitempty" default:"\"5s\"" doc:"The most a dialer waits between attempts at a peer it cannot reach; the waits grow to it, at random within each step. 5s when left out."`
	StreamIdle    Duration `json:"stream_idle,omitempty" default:"\"2m\"" doc:"How long a stream, as a model call relayed between roles, may carry nothing either way before it ends. 2m when left out."`

	At Value `json:"-" fit:"self"`
}

// Conns, Inflight, Ping, Slow, Retry and Idle return l's settings, their
// defaults when l or they are left out.
func (l *AgentLimits) Conns() int {
	if l == nil || l.MaxConns <= 0 {
		return DefaultAgentMaxConns
	}
	return l.MaxConns
}

func (l *AgentLimits) Inflight() int {
	if l == nil || l.MaxInflight <= 0 {
		return DefaultAgentMaxInflight
	}
	return l.MaxInflight
}

func (l *AgentLimits) Ping() time.Duration {
	return orDuration(l, func() Duration { return l.PingInterval }, DefaultAgentPingInterval)
}
func (l *AgentLimits) Slow() time.Duration {
	return orDuration(l, func() Duration { return l.SlowTimeout }, DefaultAgentSlowTimeout)
}
func (l *AgentLimits) Retry() time.Duration {
	return orDuration(l, func() Duration { return l.RetryInterval }, DefaultAgentRetryInterval)
}
func (l *AgentLimits) Idle() time.Duration {
	return orDuration(l, func() Duration { return l.StreamIdle }, DefaultAgentStreamIdle)
}

func orDuration[T any](p *T, get func() Duration, def Duration) time.Duration {
	if p == nil {
		return def.Std()
	}
	if d := get(); d > 0 {
		return d.Std()
	}
	return def.Std()
}

// AgentHub is the agent's hub role: nodes and clients link to it, it keeps
// the table of its nodes and their models, and it relays the clients' model
// calls to the least loaded node that serves the model.
type AgentHub struct {
	ID       string       `json:"id,omitempty" doc:"The hub's stable ID, which its peers know it by. Made when left out, and written back here."`
	Name     string       `json:"name,omitempty" doc:"The name the hub shows its peers; the host's name when left out. Its pages change it."`
	Console  *RoleConsole `json:"console,omitempty" doc:"The hub's web pages; none when left out."`
	Listen   *AgentListen `json:"listen,omitempty" doc:"Where the hub takes links from nodes and clients; none when left out. Its pages change it."`
	Peers    []AgentPeer  `json:"peers,omitempty" doc:"The nodes and clients the hub dials. Its pages change them."`
	Limits   *AgentLimits `json:"limits,omitempty" doc:"Bounds on the hub's links; the defaults when left out."`
	MaxNodes int          `json:"max_nodes,omitempty" min:"0" default:"1024" doc:"The most nodes the hub keeps in its table, offline ones included; past it the offline node seen longest ago is forgotten; online nodes are bounded by limits.max_conns and the hub's peers. 1024 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Nodes returns MaxNodes, its default when left out.
func (h *AgentHub) Nodes() int {
	if h.MaxNodes <= 0 {
		return DefaultAgentMaxNodes
	}
	return h.MaxNodes
}

// AgentNode is the agent's node role: it serves models from upstream APIs to
// its hubs and clients, reports its hardware, and runs fixed management
// actions as jobs.
type AgentNode struct {
	ID          string             `json:"id,omitempty" doc:"The node's stable ID, which its peers know it by. Made when left out, and written back here."`
	Name        string             `json:"name,omitempty" doc:"The name the node shows its peers; the host's name when left out. Its pages, and a hub that settings.allow_hub_manage lets, change it."`
	Description string             `json:"description,omitempty" doc:"A note about the node its peers show. Its pages, and a hub that settings.allow_hub_manage lets, change it."`
	Console     *RoleConsole       `json:"console,omitempty" doc:"The node's web pages; none when left out."`
	Listen      *AgentListen       `json:"listen,omitempty" doc:"Where the node takes links from hubs and clients; none when left out. Its pages change it."`
	Peers       []AgentPeer        `json:"peers,omitempty" doc:"The hubs and clients the node dials. Its pages change them."`
	Limits      *AgentLimits       `json:"limits,omitempty" doc:"Bounds on the node's links; the defaults when left out."`
	Models      []AgentModel       `json:"models,omitempty" doc:"The models the node serves: each an upstream API endpoint. Its pages, and a hub that settings.allow_hub_manage lets, change them."`
	Settings    *AgentNodeSettings `json:"settings,omitempty" doc:"The node's own settings; the defaults when left out. Only its own pages change them."`
	Proxy       *AgentNodeProxy    `json:"proxy,omitempty" doc:"Bounds on the calls the node forwards to its models' upstreams; the defaults when left out."`

	At Value `json:"-" fit:"self"`
}

// AgentModel is a model a node serves: an upstream endpoint of an OpenAI- or
// Anthropic-compatible API. Calls are forwarded byte for byte, with the
// model's key, and the body's model set to upstream.
type AgentModel struct {
	Name        string `json:"name" fit:"required" doc:"The name agents ask for the model by: 1 to 128 characters, no spaces. A name and api are unique among a node's models."`
	API         string `json:"api" fit:"required" enum:"openai,anthropic" doc:"The API the upstream speaks, and the agent endpoints the model is reached through: openai or anthropic."`
	BaseURL     string `json:"base_url" fit:"required" doc:"The upstream's base URL, http or https, without a query, as in http://127.0.0.1:11434/v1; calls go to it with their path appended, as base_url/chat/completions."`
	Upstream    string `json:"upstream,omitempty" doc:"The model ID sent upstream in place of name; name when left out."`
	APIKey      string `json:"api_key,omitempty" doc:"The key sent upstream: as Authorization: Bearer for openai, as x-api-key for anthropic. None when left out. The pages never show it whole."`
	Disabled    bool   `json:"disabled,omitempty" doc:"Keep the model without serving it."`
	Description string `json:"description,omitempty" doc:"A note about the model its peers show."`
	MaxInflight int    `json:"max_inflight,omitempty" min:"0" max:"100000" doc:"The most calls to the model the node forwards at once; a call past it goes to another node. No bound but proxy.max_inflight's when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// AgentNodeSettings are a node's own settings.
type AgentNodeSettings struct {
	AllowHubManage  bool     `json:"allow_hub_manage,omitempty" doc:"Let linked hubs manage the node: edit its models, change GPU settings, run its fixed actions and follow their jobs. Never its own settings, and never a command of their own. Off when left out."`
	GPUSudo         *bool    `json:"gpu_sudo,omitempty" default:"true" doc:"Run GPU settings, tool installs and actions that need root with sudo -n when fit does not run as root; sudo must not ask for a password. On when left out."`
	RefreshInterval Duration `json:"refresh_interval,omitempty" default:"\"30s\"" doc:"How often the node reads its hardware again, 5s to 1h. 30s when left out."`
	JobTimeout      Duration `json:"job_timeout,omitempty" default:"\"1h\"" doc:"How long a job, an action or an install, may run before it is killed, 10s to 168h. 1h when left out."`
	GPUDataDir      string   `json:"gpu_data_dir,omitempty" doc:"A directory the install scripts of vendors whose drivers are downloaded by hand look for the packages in, under the vendor's name; the working directory when left out."`

	At Value `json:"-" fit:"self"`
}

// Sudo, Refresh and Job return s's settings, their defaults when s or they
// are left out.
func (s *AgentNodeSettings) Sudo() bool { return s == nil || s.GPUSudo == nil || *s.GPUSudo }
func (s *AgentNodeSettings) Refresh() time.Duration {
	return orDuration(s, func() Duration { return s.RefreshInterval }, DefaultAgentRefreshInterval)
}
func (s *AgentNodeSettings) Job() time.Duration {
	return orDuration(s, func() Duration { return s.JobTimeout }, DefaultAgentJobTimeout)
}

// AgentNodeProxy bounds the calls a node forwards to its upstreams.
type AgentNodeProxy struct {
	ConnectTimeout   Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long a connection to an upstream may take. 10s when left out."`
	FirstByteTimeout Duration `json:"first_byte_timeout,omitempty" default:"\"5m\"" doc:"How long an upstream may take to begin its response, from when the call starts, its connect included, as a model reading a long prompt does. 5m when left out."`
	IdleTimeout      Duration `json:"idle_timeout,omitempty" default:"\"5m\"" doc:"How long an upstream may go silent in the middle of its response before the call is cut short. 5m when left out."`
	Timeout          Duration `json:"timeout,omitempty" default:"\"30m\"" doc:"The most one call may take, from its start to the end of its response. 30m when left out."`
	MaxInflight      int      `json:"max_inflight,omitempty" min:"0" default:"256" doc:"The most calls the node forwards at once, across its models; a call past it goes to another node. 256 when left out or 0."`
	MaxBodyBytes     int64    `json:"max_body_bytes,omitempty" min:"0" default:"33554432" doc:"The largest request body a call may carry; one past it fails. A body is held whole only when the model's upstream differs from its name, to set the model it names. 32 MiB when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Connect, FirstByte, Idle, Total, Inflight and Body return p's settings,
// their defaults when p or they are left out.
func (p *AgentNodeProxy) Connect() time.Duration {
	return orDuration(p, func() Duration { return p.ConnectTimeout }, DefaultAgentConnectTimeout)
}
func (p *AgentNodeProxy) FirstByte() time.Duration {
	return orDuration(p, func() Duration { return p.FirstByteTimeout }, DefaultAgentFirstByteTimeout)
}
func (p *AgentNodeProxy) Idle() time.Duration {
	return orDuration(p, func() Duration { return p.IdleTimeout }, DefaultAgentIdleTimeout)
}
func (p *AgentNodeProxy) Total() time.Duration {
	return orDuration(p, func() Duration { return p.Timeout }, DefaultAgentCallTimeout)
}
func (p *AgentNodeProxy) Inflight() int {
	if p == nil || p.MaxInflight <= 0 {
		return DefaultAgentNodeMaxInflight
	}
	return p.MaxInflight
}
func (p *AgentNodeProxy) Body() int64 {
	if p == nil || p.MaxBodyBytes <= 0 {
		return DefaultAgentMaxBodyBytes
	}
	return p.MaxBodyBytes
}

// AgentClient is the agent's client role: it links to hubs and nodes, and
// serves an OpenAI- and Anthropic-compatible API that calls their models.
type AgentClient struct {
	ID      string           `json:"id,omitempty" doc:"The client's stable ID, which its peers know it by. Made when left out, and written back here."`
	Name    string           `json:"name,omitempty" doc:"The name the client shows its peers; the host's name when left out. Its pages change it."`
	Console *RoleConsole     `json:"console,omitempty" doc:"The client's web pages; none when left out."`
	Listen  *AgentListen     `json:"listen,omitempty" doc:"Where the client takes links from hubs and nodes; none when left out. Its pages change it."`
	Peers   []AgentPeer      `json:"peers,omitempty" doc:"The hubs and nodes the client dials. Its pages change them."`
	Limits  *AgentLimits     `json:"limits,omitempty" doc:"Bounds on the client's links; the defaults when left out."`
	API     *AgentClientAPI  `json:"api,omitempty" doc:"The API agents call, /v1/chat/completions, /v1/messages and the rest; none when left out."`
	APIKeys []AgentClientKey `json:"api_keys,omitempty" doc:"The keys agents call the API with, each kept as its SHA-256 alone. Its pages make and delete them."`

	At Value `json:"-" fit:"self"`
}

// AgentClientAPI is the client's API for agents, served on a binding of its
// own on the listeners' engine.
type AgentClientAPI struct {
	Address          string   `json:"address" fit:"required" doc:"The host and port the API listens on. A host that is not a loopback address needs cert_file and key_file, so that keys never cross a network in the clear."`
	CertFile         string   `json:"cert_file,omitempty" doc:"A PEM file with the API's certificate, followed by any intermediates; with key_file, the API is served over TLS."`
	KeyFile          string   `json:"key_file,omitempty" doc:"A PEM file with the certificate's private key."`
	MaxBodyBytes     int64    `json:"max_body_bytes,omitempty" min:"0" default:"33554432" doc:"The largest request body a call may carry; one past it gets 413. It is held whole, to read the model it names. 32 MiB when left out or 0."`
	MaxConns         int      `json:"max_conns,omitempty" min:"0" default:"256" doc:"The most connections the API keeps open at once; one past it is closed as soon as it is accepted. 256 when left out or 0."`
	MaxInflight      int      `json:"max_inflight,omitempty" min:"0" default:"256" doc:"The most calls the API forwards at once; one past it gets 429. 256 when left out or 0."`
	FirstByteTimeout Duration `json:"first_byte_timeout,omitempty" default:"\"5m\"" doc:"How long a call may wait for its response to begin. 5m when left out."`
	Timeout          Duration `json:"timeout,omitempty" default:"\"30m\"" doc:"The most one call may take, to the end of its response. 30m when left out."`

	At Value `json:"-" fit:"self"`
}

// Body, Conns, Inflight, FirstByte and Total return a's settings, their
// defaults when they are left out.
func (a *AgentClientAPI) Body() int64 {
	if a.MaxBodyBytes <= 0 {
		return DefaultAgentMaxBodyBytes
	}
	return a.MaxBodyBytes
}
func (a *AgentClientAPI) Conns() int {
	if a.MaxConns <= 0 {
		return DefaultAgentClientMaxConns
	}
	return a.MaxConns
}
func (a *AgentClientAPI) Inflight() int {
	if a.MaxInflight <= 0 {
		return DefaultAgentClientMaxInflight
	}
	return a.MaxInflight
}
func (a *AgentClientAPI) FirstByte() time.Duration {
	return orDuration(a, func() Duration { return a.FirstByteTimeout }, DefaultAgentFirstByteTimeout)
}
func (a *AgentClientAPI) Total() time.Duration {
	return orDuration(a, func() Duration { return a.Timeout }, DefaultAgentCallTimeout)
}

// AgentClientKey is an API key agents call a client with. Only its SHA-256 is
// kept: the key itself is shown once, when the pages make it.
type AgentClientKey struct {
	ID        string `json:"id" fit:"required" doc:"The key's ID, 16 hex digits."`
	Name      string `json:"name" fit:"required" doc:"What the key is for: 1 to 64 characters."`
	Prefix    string `json:"prefix,omitempty" doc:"The key's first characters, to tell it by on the pages."`
	Hash      string `json:"hash" fit:"required" doc:"The SHA-256 of the key, in hex."`
	CreatedAt int64  `json:"created_at,omitempty" min:"0" doc:"When the key was made, in seconds since 1970."`

	At Value `json:"-" fit:"self"`
}

// checkAgent checks the agent's roles beyond what decoding does.
func (c *Config) checkAgent(add func(Value, string, ...any)) {
	r := c.Agent
	if r == nil {
		return
	}
	if h := r.Hub; h != nil {
		checkRoleConsole(h.Console, add)
		checkAgentMesh("hub", h.At, h.ID, h.Name, h.Listen, h.Peers, h.Limits, add)
	}
	if n := r.Node; n != nil {
		checkRoleConsole(n.Console, add)
		checkAgentMesh("node", n.At, n.ID, n.Name, n.Listen, n.Peers, n.Limits, add)
		checkAgentNode(n, add)
	}
	if cl := r.Client; cl != nil {
		checkRoleConsole(cl.Console, add)
		checkAgentMesh("client", cl.At, cl.ID, cl.Name, cl.Listen, cl.Peers, cl.Limits, add)
		checkAgentClient(cl, add)
	}
}

// AgentPeerRoles returns the roles a role of the agent links with.
func AgentPeerRoles(role string) []string {
	switch role {
	case "hub":
		return []string{"node", "client"}
	case "node":
		return []string{"hub", "client"}
	case "client":
		return []string{"hub", "node"}
	}
	return nil
}

func noSpace(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func checkAgentMesh(role string, at Value, id, name string, listen *AgentListen, peers []AgentPeer, limits *AgentLimits, add func(Value, string, ...any)) {
	if len(id) > 128 || !noSpace(id) {
		add(at.Field("id"), "the ID is up to 128 characters without spaces")
	}
	if len([]rune(name)) > 64 || strings.TrimSpace(name) != name || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		add(at.Field("name"), "the name is up to 64 characters, without control characters or spaces around it")
	}
	if l := listen; l != nil {
		if _, msg := checkAddress(l.Address); msg != "" {
			add(l.At.Field("address"), "%s", msg)
		}
		if l.Password == "" {
			add(l.At.Field("password"), "a listener needs a password")
		}
	}
	if len(peers) > MaxAgentPeers {
		add(at.Field("peers"), "at most %d peers", MaxAgentPeers)
	}
	accept := AgentPeerRoles(role)
	for i, p := range peers {
		pat := at.Field("peers").Index(i)
		host, _, err := net.SplitHostPort(p.Address)
		if _, msg := checkAddress(p.Address); msg != "" || err != nil || host == "" {
			add(pat.Field("address"), "%q is not a host and port, as in \"10.0.0.1:9100\"", p.Address)
		} else if slices.IndexFunc(peers, func(o AgentPeer) bool { return o.Address == p.Address }) < i {
			add(pat.Field("address"), "%q is listed twice", p.Address)
		}
		if p.Password == "" {
			add(pat.Field("password"), "a peer needs its password")
		}
		if !slices.Contains(accept, p.Role) {
			add(pat.Field("role"), "a %s links with %s, not %q", role, strings.Join(accept, " and "), p.Role)
		}
	}
	if l := limits; l != nil {
		for _, d := range []struct {
			name string
			v    Duration
			min  time.Duration
		}{{"ping_interval", l.PingInterval, 100 * time.Millisecond}, {"slow_timeout", l.SlowTimeout, 0},
			{"retry_interval", l.RetryInterval, 100 * time.Millisecond}, {"stream_idle", l.StreamIdle, time.Second}} {
			if d.v < 0 || d.v != 0 && d.v.Std() < d.min {
				add(l.At.Field(d.name), "%s is too short: at least %s", d.v, d.min)
			}
		}
	}
}

// CheckAgentModel says what is wrong with m, or returns "" and the field it
// is about.
func CheckAgentModel(m *AgentModel) (field, msg string) {
	switch {
	case m.Name == "" || len(m.Name) > 128 || !noSpace(m.Name):
		return "name", "the name needs 1 to 128 characters without spaces"
	case m.API != "openai" && m.API != "anthropic":
		return "api", "api is openai or anthropic"
	case len(m.Upstream) > 256 || !noSpace(m.Upstream):
		return "upstream", "the upstream model ID is up to 256 characters without spaces"
	case len(m.APIKey) > 4096 || !noSpace(m.APIKey):
		return "api_key", "the API key is up to 4096 characters without spaces"
	case len(m.Description) > 1024:
		return "description", "the description is longer than 1024 bytes"
	case m.MaxInflight < 0 || m.MaxInflight > 100000:
		return "max_inflight", "max_inflight is 0 to 100000"
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(m.BaseURL, "/") {
		return "base_url", "base_url is an http or https URL without a query or a trailing slash, as in http://127.0.0.1:11434/v1"
	}
	return "", ""
}

func checkAgentNode(n *AgentNode, add func(Value, string, ...any)) {
	if len(n.Description) > 1024 {
		add(n.At.Field("description"), "the description is longer than 1024 bytes")
	}
	if len(n.Models) > MaxAgentModels {
		add(n.At.Field("models"), "at most %d models", MaxAgentModels)
	}
	for i := range n.Models {
		m := &n.Models[i]
		if f, msg := CheckAgentModel(m); msg != "" {
			add(m.At.Field(f), "%s", msg)
		} else if slices.IndexFunc(n.Models, func(o AgentModel) bool { return o.Name == m.Name && o.API == m.API }) < i {
			add(m.At.Field("name"), "%s (%s) is listed twice", m.Name, m.API)
		}
	}
	if s := n.Settings; s != nil {
		if d := s.RefreshInterval; d != 0 && (d.Std() < 5*time.Second || d.Std() > time.Hour) {
			add(s.At.Field("refresh_interval"), "refresh_interval is 5s to 1h")
		}
		if d := s.JobTimeout; d != 0 && (d.Std() < 10*time.Second || d.Std() > 7*24*time.Hour) {
			add(s.At.Field("job_timeout"), "job_timeout is 10s to 168h")
		}
	}
	if p := n.Proxy; p != nil {
		for _, d := range []struct {
			name string
			v    Duration
		}{{"connect_timeout", p.ConnectTimeout}, {"first_byte_timeout", p.FirstByteTimeout}, {"idle_timeout", p.IdleTimeout}, {"timeout", p.Timeout}} {
			if d.v < 0 {
				add(p.At.Field(d.name), "%s is negative", d.name)
			}
		}
	}
}

func checkAgentClient(c *AgentClient, add func(Value, string, ...any)) {
	if a := c.API; a != nil {
		if _, msg := checkAddress(a.Address); msg != "" {
			add(a.At.Field("address"), "%s", msg)
		} else if host, _, _ := net.SplitHostPort(a.Address); !isLoopback(host) && (a.CertFile == "" || a.KeyFile == "") {
			add(a.At.Field("address"), "%q is not a loopback address: set cert_file and key_file, so that keys are not sent in the clear", a.Address)
		}
		if (a.CertFile == "") != (a.KeyFile == "") {
			add(a.At, "cert_file and key_file go together")
		}
	}
	if len(c.APIKeys) > MaxAgentKeys {
		add(c.At.Field("api_keys"), "at most %d keys", MaxAgentKeys)
	}
	for i, k := range c.APIKeys {
		at := c.At.Field("api_keys").Index(i)
		if b, err := hex.DecodeString(k.ID); err != nil || len(b) != 8 {
			add(at.Field("id"), "the ID is 16 hex digits")
		} else if slices.IndexFunc(c.APIKeys, func(o AgentClientKey) bool { return o.ID == k.ID }) < i {
			add(at.Field("id"), "%q is listed twice", k.ID)
		}
		if n := len([]rune(strings.TrimSpace(k.Name))); n == 0 || n > 64 {
			add(at.Field("name"), "the name needs 1 to 64 characters")
		}
		if b, err := hex.DecodeString(k.Hash); err != nil || len(b) != 32 {
			add(at.Field("hash"), "the hash is a SHA-256 in hex: 64 hex digits")
		}
		if len(k.Prefix) > 16 {
			add(at.Field("prefix"), "the prefix is up to 16 characters")
		}
	}
}
