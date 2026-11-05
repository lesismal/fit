package config

import (
	"crypto/tls"
	"encoding/hex"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/lesismal/fit/internal/certpin"
)

// The LLM feature's roles link over a mesh of WebSocket connections: each role may
// listen for links and dial any number of peers, which authenticate with a
// password (HMAC challenge-response, both ways; the password never crosses
// the wire) that a listener may set per role. Off loopback the links go over
// TLS, the dialer checking the listener's certificate by a pin or a CA. What
// their pages change is written back into their members here.
// See docs/llm.md.

// The bounds of the LLM feature's members.
const (
	MaxLLMPeers  = 64
	MaxLLMModels = 256
	MaxLLMKeys   = 256
)

// The defaults of the LLM feature's members.
const (
	DefaultLLMMaxConns         = 1024
	DefaultLLMMaxInflight      = 1024
	DefaultLLMMaxNodes         = 1024
	DefaultLLMNodeMaxInflight  = 256
	DefaultLLMMaxBodyBytes     = 32 << 20
	DefaultLLMProxyMaxConns    = 256
	DefaultLLMProxyMaxInflight = 256
)

// The defaults of the LLM feature's durations.
var (
	DefaultLLMPingInterval     = Duration(15 * time.Second)
	DefaultLLMSlowTimeout      = Duration(30 * time.Second)
	DefaultLLMRetryInterval    = Duration(5 * time.Second)
	DefaultLLMStreamIdle       = Duration(2 * time.Minute)
	DefaultLLMRefreshInterval  = Duration(30 * time.Second)
	DefaultLLMJobTimeout       = Duration(time.Hour)
	DefaultLLMConnectTimeout   = Duration(10 * time.Second)
	DefaultLLMFirstByteTimeout = Duration(5 * time.Minute)
	DefaultLLMIdleTimeout      = Duration(5 * time.Minute)
	DefaultLLMCallTimeout      = Duration(30 * time.Minute)
)

// LLMListen is where a role takes links from its peers.
//
// The password a dialer proves decides the role it may link as: a role's own
// password (node_password, proxy_password, hub_password) or, for a role
// without one, password. A password shared by several roles lets whoever
// holds it link as any of them.
type LLMListen struct {
	Address       string `json:"address" fit:"required" doc:"The host and port the role takes links on, as a binding of its own on the listeners' engine; the WebSocket path is /ws. An empty host listens on every address."`
	Password      string `json:"password,omitempty" doc:"The password of every role the listener links with that has no password of its own: peers prove they know it when they link, and it never crosses the network. Whoever holds it may link as any of those roles, so give each role its own instead. None when left out."`
	NodePassword  string `json:"node_password,omitempty" doc:"The password nodes prove to link as a node, on a hub's or a proxy's listener; password when left out."`
	ProxyPassword string `json:"proxy_password,omitempty" doc:"The password proxies prove to link as a proxy, on a hub's or a node's listener; password when left out."`
	HubPassword   string `json:"hub_password,omitempty" doc:"The password hubs prove to link as a hub, on a node's or a proxy's listener; password when left out."`
	CertFile      string `json:"cert_file,omitempty" doc:"A PEM file with the certificate the listener presents, followed by any intermediates; with key_file, links are taken over TLS (wss://), and peers check it with their ca_file or pin. A listener on an address other than a loopback one needs TLS: this, or self_signed."`
	KeyFile       string `json:"key_file,omitempty" doc:"A PEM file with the private key of cert_file."`
	SelfSigned    bool   `json:"self_signed,omitempty" doc:"Take links over TLS (wss://) with a self-signed certificate the role makes and keeps in cert and key, so that it outlives a restart; its pages and its log show its SHA-256 fingerprint, which peers set as their pin. For a listener without cert_file."`
	Cert          string `json:"cert,omitempty" doc:"The PEM certificate self_signed made; with key. Written by the role; delete both for a new one, which every peer must pin again."`
	Key           string `json:"key,omitempty" doc:"The PEM private key of cert."`

	At Value `json:"-" fit:"self"`
}

// TLS reports whether the listener takes links over TLS.
func (l *LLMListen) TLS() bool { return l.CertFile != "" || l.Cert != "" || l.SelfSigned }

// LLMPeer is a peer a role dials and keeps linked to, redialing with
// backoff whenever the link is lost.
type LLMPeer struct {
	Address    string `json:"address" fit:"required" doc:"The peer's host and port, as its listen.address says."`
	Password   string `json:"password" fit:"required" doc:"The password of the dialing role on the peer's listener: a node dials with the peer's listen.node_password, a proxy with its listen.proxy_password, a hub with its listen.hub_password, or with its listen.password when it has none for the role."`
	Role       string `json:"role" fit:"required" enum:"hub,node,proxy" doc:"The role the peer must announce; a link to a peer of another role is refused. Never the dialing role's own."`
	Pin        string `json:"pin,omitempty" doc:"The SHA-256 fingerprint of the certificate the peer's listener presents, 64 hex digits (colons allowed), as its pages and its log show it: the link is made over TLS (wss://) and only to a listener with exactly that certificate. A peer at an address other than a loopback one needs pin or ca_file."`
	CAFile     string `json:"ca_file,omitempty" doc:"A PEM file with the certificates of the authorities the peer listener's certificate must chain to: the link is made over TLS (wss://), and the certificate must name server_name, or the address's host. Or pin, one of the two."`
	ServerName string `json:"server_name,omitempty" doc:"The name the peer listener's certificate must have with ca_file, and the TLS server name sent; the address's host when left out."`

	At Value `json:"-" fit:"self"`
}

// LLMLimits bound a role's links.
type LLMLimits struct {
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
func (l *LLMLimits) Conns() int {
	if l == nil || l.MaxConns <= 0 {
		return DefaultLLMMaxConns
	}
	return l.MaxConns
}

func (l *LLMLimits) Inflight() int {
	if l == nil || l.MaxInflight <= 0 {
		return DefaultLLMMaxInflight
	}
	return l.MaxInflight
}

func (l *LLMLimits) Ping() time.Duration {
	return orDuration(l, func() Duration { return l.PingInterval }, DefaultLLMPingInterval)
}
func (l *LLMLimits) Slow() time.Duration {
	return orDuration(l, func() Duration { return l.SlowTimeout }, DefaultLLMSlowTimeout)
}
func (l *LLMLimits) Retry() time.Duration {
	return orDuration(l, func() Duration { return l.RetryInterval }, DefaultLLMRetryInterval)
}
func (l *LLMLimits) Idle() time.Duration {
	return orDuration(l, func() Duration { return l.StreamIdle }, DefaultLLMStreamIdle)
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

// LLMHub is the LLM feature's hub role: nodes and proxies link to it, it keeps
// the table of its nodes and their models, and it relays the proxies' model
// calls to the least loaded node that serves the model.
type LLMHub struct {
	ID       string       `json:"id,omitempty" doc:"The hub's stable ID, which its peers know it by. Made when left out, and written back here."`
	Name     string       `json:"name,omitempty" doc:"The name the hub shows its peers; the host's name when left out. Its pages change it."`
	Console  *RoleConsole `json:"console,omitempty" doc:"The hub's web pages; none when left out."`
	Listen   *LLMListen   `json:"listen,omitempty" doc:"Where the hub takes links from nodes and proxies; none when left out. Its pages change it."`
	Peers    []LLMPeer    `json:"peers,omitempty" doc:"The nodes and proxies the hub dials. Its pages change them."`
	Limits   *LLMLimits   `json:"limits,omitempty" doc:"Bounds on the hub's links; the defaults when left out."`
	MaxNodes int          `json:"max_nodes,omitempty" min:"0" default:"1024" doc:"The most nodes the hub keeps in its table, offline ones included; past it the offline node seen longest ago is forgotten; online nodes are bounded by limits.max_conns and the hub's peers. 1024 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Nodes returns MaxNodes, its default when left out.
func (h *LLMHub) Nodes() int {
	if h.MaxNodes <= 0 {
		return DefaultLLMMaxNodes
	}
	return h.MaxNodes
}

// LLMNode is the LLM feature's node role: it serves models from upstream APIs to
// its hubs and proxies, reports its hardware, and runs fixed management
// actions as jobs.
type LLMNode struct {
	ID          string           `json:"id,omitempty" doc:"The node's stable ID, which its peers know it by. Made when left out, and written back here."`
	Name        string           `json:"name,omitempty" doc:"The name the node shows its peers; the host's name when left out. Its pages, and a hub that settings.allow_hub_manage lets, change it."`
	Description string           `json:"description,omitempty" doc:"A note about the node its peers show. Its pages, and a hub that settings.allow_hub_manage lets, change it."`
	Console     *RoleConsole     `json:"console,omitempty" doc:"The node's web pages; none when left out."`
	Listen      *LLMListen       `json:"listen,omitempty" doc:"Where the node takes links from hubs and proxies; none when left out. Its pages change it."`
	Peers       []LLMPeer        `json:"peers,omitempty" doc:"The hubs and proxies the node dials. Its pages change them."`
	Limits      *LLMLimits       `json:"limits,omitempty" doc:"Bounds on the node's links; the defaults when left out."`
	Models      []LLMModel       `json:"models,omitempty" doc:"The models the node serves: each an upstream API endpoint. Its pages, and a hub that settings.allow_hub_manage lets, change them."`
	Settings    *LLMNodeSettings `json:"settings,omitempty" doc:"The node's own settings; the defaults when left out. Only its own pages change them."`
	Proxy       *LLMNodeProxy    `json:"proxy,omitempty" doc:"Bounds on the calls the node forwards to its models' upstreams; the defaults when left out."`

	At Value `json:"-" fit:"self"`
}

// LLMModel is a model a node serves: an upstream endpoint of one of the
// APIs LLMAPIs lists. Calls are forwarded byte for byte, with the model's
// key, and the model they name set to upstream.
type LLMModel struct {
	Name        string `json:"name" fit:"required" doc:"The name agents ask for the model by: 1 to 128 characters, no spaces. A name is unique among a node's models of one agent API: an azure_openai model shares openai's."`
	API         string `json:"api" fit:"required" enum:"openai,anthropic,gemini,azure_openai,ollama,cohere" doc:"The API the upstream speaks: openai (OpenAI and every OpenAI-compatible server, as DeepSeek, Qwen, Mistral, Groq, xAI, OpenRouter, vLLM, llama.cpp), anthropic, gemini (Google's Gemini API), azure_openai (Azure OpenAI deployments, which agents call as openai), ollama (Ollama's own API) or cohere (Cohere's v2 API). It sets the agent endpoints the model is reached through, the upstream URL and the auth header."`
	BaseURL     string `json:"base_url" fit:"required" doc:"The upstream's base URL, http or https, without a query or a trailing slash. openai, anthropic and cohere calls go to it with their path appended, as base_url/chat/completions with http://127.0.0.1:8000/v1; gemini's to base_url/models/{upstream}:{method}, with https://generativelanguage.googleapis.com/v1beta; azure_openai's to base_url/openai/deployments/{upstream}/{path}, with https://{resource}.openai.azure.com; ollama's to base_url/api/chat, with http://127.0.0.1:11434."`
	Upstream    string `json:"upstream,omitempty" doc:"The model ID sent upstream in place of name: in the body's model, a form's model field, Gemini's path, or as an azure_openai model's deployment. name when left out."`
	APIKey      string `json:"api_key,omitempty" doc:"The key sent upstream: as Authorization: Bearer for openai, ollama and cohere, as x-api-key for anthropic, x-goog-api-key for gemini, api-key for azure_openai. None when left out. The pages never show it whole."`
	APIVersion  string `json:"api_version,omitempty" doc:"The api-version query every call of an azure_openai model carries, as 2024-10-21: required for azure_openai, and only taken there."`
	Disabled    bool   `json:"disabled,omitempty" doc:"Keep the model without serving it."`
	Description string `json:"description,omitempty" doc:"A note about the model its peers show."`
	MaxInflight int    `json:"max_inflight,omitempty" min:"0" max:"100000" doc:"The most calls to the model the node forwards at once; a call past it goes to another node. No bound but proxy.max_inflight's when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// LLMAPIs lists the APIs a model's upstream may speak.
var LLMAPIs = []string{"openai", "anthropic", "gemini", "azure_openai", "ollama", "cohere"}

// LLMProtocol returns the API agents call a model of the given api with:
// azure_openai's is openai's.
func LLMProtocol(api string) string {
	if api == "azure_openai" {
		return "openai"
	}
	return api
}

// LLMNodeSettings are a node's own settings.
type LLMNodeSettings struct {
	AllowHubManage  bool     `json:"allow_hub_manage,omitempty" doc:"Let linked hubs manage the node: edit its models, change GPU settings, run its fixed actions and follow their jobs. Never its own settings, and never a command of their own. Off when left out."`
	GPUSudo         *bool    `json:"gpu_sudo,omitempty" default:"true" doc:"Run GPU settings, tool installs and actions that need root with sudo -n when fit does not run as root; sudo must not ask for a password. On when left out."`
	RefreshInterval Duration `json:"refresh_interval,omitempty" default:"\"30s\"" doc:"How often the node reads its hardware again, 5s to 1h. 30s when left out."`
	JobTimeout      Duration `json:"job_timeout,omitempty" default:"\"1h\"" doc:"How long a job, an action or an install, may run before it is killed, 10s to 168h. 1h when left out."`
	GPUDataDir      string   `json:"gpu_data_dir,omitempty" doc:"A directory the install scripts of vendors whose drivers are downloaded by hand look for the packages in, under the vendor's name; the working directory when left out."`

	At Value `json:"-" fit:"self"`
}

// Sudo, Refresh and Job return s's settings, their defaults when s or they
// are left out.
func (s *LLMNodeSettings) Sudo() bool { return s == nil || s.GPUSudo == nil || *s.GPUSudo }
func (s *LLMNodeSettings) Refresh() time.Duration {
	return orDuration(s, func() Duration { return s.RefreshInterval }, DefaultLLMRefreshInterval)
}
func (s *LLMNodeSettings) Job() time.Duration {
	return orDuration(s, func() Duration { return s.JobTimeout }, DefaultLLMJobTimeout)
}

// LLMNodeProxy bounds the calls a node forwards to its upstreams.
type LLMNodeProxy struct {
	ConnectTimeout   Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long a connection to an upstream may take. 10s when left out."`
	FirstByteTimeout Duration `json:"first_byte_timeout,omitempty" default:"\"5m\"" doc:"How long an upstream may take to begin its response, from when the call starts, its connect included, as a model reading a long prompt does. 5m when left out."`
	IdleTimeout      Duration `json:"idle_timeout,omitempty" default:"\"5m\"" doc:"How long an upstream may go silent in the middle of its response before the call is cut short. 5m when left out."`
	Timeout          Duration `json:"timeout,omitempty" default:"\"30m\"" doc:"The most one call may take, from its start to the end of its response. 30m when left out."`
	MaxInflight      int      `json:"max_inflight,omitempty" min:"0" default:"256" doc:"The most calls the node forwards at once, across its models; a call past it goes to another node. 256 when left out or 0."`
	MaxBodyBytes     int64    `json:"max_body_bytes,omitempty" min:"0" default:"33554432" doc:"The largest request body a call may carry; one past it fails. A body is held whole only when the model's upstream differs from the name the call gives and the model is named in the body (its JSON model, or a form's model field), to set it. 32 MiB when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Connect, FirstByte, Idle, Total, Inflight and Body return p's settings,
// their defaults when p or they are left out.
func (p *LLMNodeProxy) Connect() time.Duration {
	return orDuration(p, func() Duration { return p.ConnectTimeout }, DefaultLLMConnectTimeout)
}
func (p *LLMNodeProxy) FirstByte() time.Duration {
	return orDuration(p, func() Duration { return p.FirstByteTimeout }, DefaultLLMFirstByteTimeout)
}
func (p *LLMNodeProxy) Idle() time.Duration {
	return orDuration(p, func() Duration { return p.IdleTimeout }, DefaultLLMIdleTimeout)
}
func (p *LLMNodeProxy) Total() time.Duration {
	return orDuration(p, func() Duration { return p.Timeout }, DefaultLLMCallTimeout)
}
func (p *LLMNodeProxy) Inflight() int {
	if p == nil || p.MaxInflight <= 0 {
		return DefaultLLMNodeMaxInflight
	}
	return p.MaxInflight
}
func (p *LLMNodeProxy) Body() int64 {
	if p == nil || p.MaxBodyBytes <= 0 {
		return DefaultLLMMaxBodyBytes
	}
	return p.MaxBodyBytes
}

// LLMProxy is the LLM feature's proxy role: it links to hubs and nodes, near
// or across the internet, and serves the model APIs (OpenAI, Anthropic,
// Gemini, Ollama, Cohere) to agents on its machine or its LAN, calling the
// models of those hubs and nodes.
type LLMProxy struct {
	ID      string        `json:"id,omitempty" doc:"The proxy's stable ID, which its peers know it by. Made when left out, and written back here."`
	Name    string        `json:"name,omitempty" doc:"The name the proxy shows its peers; the host's name when left out. Its pages change it."`
	Console *RoleConsole  `json:"console,omitempty" doc:"The proxy's web pages; none when left out."`
	Listen  *LLMListen    `json:"listen,omitempty" doc:"Where the proxy takes links from hubs and nodes; none when left out. Its pages change it."`
	Peers   []LLMPeer     `json:"peers,omitempty" doc:"The hubs and nodes the proxy dials. Its pages change them."`
	Limits  *LLMLimits    `json:"limits,omitempty" doc:"Bounds on the proxy's links; the defaults when left out."`
	API     *LLMProxyAPI  `json:"api,omitempty" doc:"The API agents on this machine or the LAN call, /v1/chat/completions, /v1/messages, /v1beta/models/{model}:generateContent, /api/chat, /v2/chat and the rest; none when left out."`
	APIKeys []LLMProxyKey `json:"api_keys,omitempty" doc:"The keys agents call the API with, each kept as its SHA-256 alone. Its pages make and delete them."`

	At Value `json:"-" fit:"self"`
}

// LLMProxyAPI is the proxy's API for agents, served on a binding of its
// own on the listeners' engine: on a loopback address for agents on the
// proxy's machine, or on a LAN address, with TLS, for agents on other
// machines of the LAN.
type LLMProxyAPI struct {
	Address          string   `json:"address" fit:"required" doc:"The host and port the API listens on: a loopback address serves agents on this machine; a LAN address, or an empty host, serves agents on other machines too, and needs cert_file and key_file, so that keys never cross a network in the clear."`
	CertFile         string   `json:"cert_file,omitempty" doc:"A PEM file with the API's certificate, followed by any intermediates; with key_file, the API is served over TLS."`
	KeyFile          string   `json:"key_file,omitempty" doc:"A PEM file with the certificate's private key."`
	MaxBodyBytes     int64    `json:"max_body_bytes,omitempty" min:"0" default:"33554432" doc:"The largest request body a call may carry; one past it gets 413. It is held whole, to read the model it names and to send it again when a route cannot serve it. 32 MiB when left out or 0."`
	MaxConns         int      `json:"max_conns,omitempty" min:"0" default:"256" doc:"The most connections the API keeps open at once; one past it is closed as soon as it is accepted. 256 when left out or 0."`
	MaxInflight      int      `json:"max_inflight,omitempty" min:"0" default:"256" doc:"The most calls the API forwards at once; one past it gets 429. 256 when left out or 0."`
	FirstByteTimeout Duration `json:"first_byte_timeout,omitempty" default:"\"5m\"" doc:"How long a call may wait for its response to begin. 5m when left out."`
	Timeout          Duration `json:"timeout,omitempty" default:"\"30m\"" doc:"The most one call may take, to the end of its response. 30m when left out."`

	At Value `json:"-" fit:"self"`
}

// Body, Conns, Inflight, FirstByte and Total return a's settings, their
// defaults when they are left out.
func (a *LLMProxyAPI) Body() int64 {
	if a.MaxBodyBytes <= 0 {
		return DefaultLLMMaxBodyBytes
	}
	return a.MaxBodyBytes
}
func (a *LLMProxyAPI) Conns() int {
	if a.MaxConns <= 0 {
		return DefaultLLMProxyMaxConns
	}
	return a.MaxConns
}
func (a *LLMProxyAPI) Inflight() int {
	if a.MaxInflight <= 0 {
		return DefaultLLMProxyMaxInflight
	}
	return a.MaxInflight
}
func (a *LLMProxyAPI) FirstByte() time.Duration {
	return orDuration(a, func() Duration { return a.FirstByteTimeout }, DefaultLLMFirstByteTimeout)
}
func (a *LLMProxyAPI) Total() time.Duration {
	return orDuration(a, func() Duration { return a.Timeout }, DefaultLLMCallTimeout)
}

// LLMProxyKey is an API key agents call a proxy with. Only its SHA-256 is
// kept: the key itself is shown once, when the pages make it.
type LLMProxyKey struct {
	ID        string `json:"id" fit:"required" doc:"The key's ID, 16 hex digits."`
	Name      string `json:"name" fit:"required" doc:"What the key is for: 1 to 64 characters."`
	Prefix    string `json:"prefix,omitempty" doc:"The key's first characters, to tell it by on the pages."`
	Hash      string `json:"hash" fit:"required" doc:"The SHA-256 of the key, in hex."`
	CreatedAt int64  `json:"created_at,omitempty" min:"0" doc:"When the key was made, in seconds since 1970."`

	At Value `json:"-" fit:"self"`
}

// checkLLM checks the LLM feature's roles beyond what decoding does.
func (c *Config) checkLLM(add func(Value, string, ...any)) {
	r := c.LLM
	if r == nil {
		return
	}
	if h := r.Hub; h != nil {
		checkRoleConsole(h.Console, add)
		checkLLMMesh("hub", h.At, h.ID, h.Name, h.Listen, h.Peers, h.Limits, add)
	}
	if n := r.Node; n != nil {
		checkRoleConsole(n.Console, add)
		checkLLMMesh("node", n.At, n.ID, n.Name, n.Listen, n.Peers, n.Limits, add)
		checkLLMNode(n, add)
	}
	if cl := r.Proxy; cl != nil {
		checkRoleConsole(cl.Console, add)
		checkLLMMesh("proxy", cl.At, cl.ID, cl.Name, cl.Listen, cl.Peers, cl.Limits, add)
		checkLLMProxy(cl, add)
	}
}

// LLMPeerRoles returns the roles a role of the LLM feature links with.
func LLMPeerRoles(role string) []string {
	switch role {
	case "hub":
		return []string{"node", "proxy"}
	case "node":
		return []string{"hub", "proxy"}
	case "proxy":
		return []string{"hub", "node"}
	}
	return nil
}

func noSpace(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func checkLLMMesh(role string, at Value, id, name string, listen *LLMListen, peers []LLMPeer, limits *LLMLimits, add func(Value, string, ...any)) {
	if len(id) > 128 || !noSpace(id) {
		add(at.Field("id"), "the ID is up to 128 characters without spaces")
	}
	if len([]rune(name)) > 64 || strings.TrimSpace(name) != name || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		add(at.Field("name"), "the name is up to 64 characters, without control characters or spaces around it")
	}
	if l := listen; l != nil {
		if _, msg := checkAddress(l.Address); msg != "" {
			add(l.At.Field("address"), "%s", msg)
		} else if host, _, _ := net.SplitHostPort(l.Address); !isLoopback(host) && !l.TLS() {
			add(l.At.Field("address"), "%q is not a loopback address: set cert_file and key_file, or self_signed, so that links are taken over TLS (wss://) and peers can check whose listener they reach", l.Address)
		}
		checkLLMListenTLS(l, add)
		// A role's own password is only for a role the listener links with.
		accept := LLMPeerRoles(role)
		var fields []string
		own := false
		for _, f := range []struct{ role, v string }{{"node", l.NodePassword}, {"proxy", l.ProxyPassword}, {"hub", l.HubPassword}} {
			if slices.Contains(accept, f.role) {
				fields = append(fields, f.role+"_password")
				own = own || f.v != ""
			} else if f.v != "" {
				add(l.At.Field(f.role+"_password"), "a %s takes no links from %ss", role, f.role)
			}
		}
		if l.Password == "" && !own {
			add(l.At.Field("password"), "a listener needs a password: password, or a role's own, %s", strings.Join(fields, " or "))
		}
	}
	if len(peers) > MaxLLMPeers {
		add(at.Field("peers"), "at most %d peers", MaxLLMPeers)
	}
	accept := LLMPeerRoles(role)
	for i, p := range peers {
		pat := at.Field("peers").Index(i)
		host, _, err := net.SplitHostPort(p.Address)
		if _, msg := checkAddress(p.Address); msg != "" || err != nil || host == "" {
			add(pat.Field("address"), "%q is not a host and port, as in \"10.0.0.1:9100\"", p.Address)
		} else if slices.IndexFunc(peers, func(o LLMPeer) bool { return o.Address == p.Address }) < i {
			add(pat.Field("address"), "%q is listed twice", p.Address)
		}
		if p.Password == "" {
			add(pat.Field("password"), "a peer needs its password")
		}
		if !slices.Contains(accept, p.Role) {
			add(pat.Field("role"), "a %s links with %s, not %q", role, strings.Join(accept, " and "), p.Role)
		}
		checkLLMPeerTLS(p, pat, host, add)
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

// checkLLMListenTLS checks the certificate of a listener.
func checkLLMListenTLS(l *LLMListen, add func(Value, string, ...any)) {
	if (l.CertFile == "") != (l.KeyFile == "") {
		add(l.At, "cert_file and key_file go together")
	}
	if (l.Cert == "") != (l.Key == "") {
		add(l.At, "cert and key go together")
	}
	if l.CertFile != "" && (l.SelfSigned || l.Cert != "") {
		add(l.At.Field("cert_file"), "cert_file, or self_signed with its cert and key: one of the two")
	}
	if l.Cert != "" && l.Key != "" {
		if _, err := tls.X509KeyPair([]byte(l.Cert), []byte(l.Key)); err != nil {
			add(l.At.Field("cert"), "the certificate and key do not load: %v", err)
		}
	}
}

// checkLLMPeerTLS checks how a peer's listener is verified: a peer off
// loopback is dialed over TLS, its certificate checked by pin or ca_file.
func checkLLMPeerTLS(p LLMPeer, at Value, host string, add func(Value, string, ...any)) {
	if p.Pin != "" {
		if _, err := certpin.Parse(p.Pin); err != nil {
			add(at.Field("pin"), "%v", err)
		}
	}
	if p.Pin != "" && p.CAFile != "" {
		add(at.Field("pin"), "pin or ca_file, one of the two")
	}
	if p.ServerName != "" && p.CAFile == "" {
		add(at.Field("server_name"), "server_name goes with ca_file")
	}
	if host != "" && !isLoopback(host) && p.Pin == "" && p.CAFile == "" {
		add(at.Field("address"), "%q is not a loopback address: set pin to the fingerprint of the peer listener's certificate, as its pages show it, or ca_file, so that the link is made over TLS (wss://) to that listener alone", p.Address)
	}
}

// CheckLLMModel says what is wrong with m, or returns "" and the field it
// is about.
func CheckLLMModel(m *LLMModel) (field, msg string) {
	switch {
	case m.Name == "" || len(m.Name) > 128 || !noSpace(m.Name):
		return "name", "the name needs 1 to 128 characters without spaces"
	case !slices.Contains(LLMAPIs, m.API):
		return "api", "api is " + strings.Join(LLMAPIs, ", ")
	case len(m.Upstream) > 256 || !noSpace(m.Upstream):
		return "upstream", "the upstream model ID is up to 256 characters without spaces"
	case len(m.APIKey) > 4096 || !noSpace(m.APIKey):
		return "api_key", "the API key is up to 4096 characters without spaces"
	case len(m.Description) > 1024:
		return "description", "the description is longer than 1024 bytes"
	case m.MaxInflight < 0 || m.MaxInflight > 100000:
		return "max_inflight", "max_inflight is 0 to 100000"
	case m.API == "azure_openai" && m.APIVersion == "":
		return "api_version", "an azure_openai model needs its api_version, as 2024-10-21"
	case m.API != "azure_openai" && m.APIVersion != "":
		return "api_version", "api_version is only for azure_openai models"
	case len(m.APIVersion) > 64 || strings.IndexFunc(m.APIVersion, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_')
	}) >= 0:
		return "api_version", "api_version is up to 64 letters, digits, dots, dashes and underscores"
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(m.BaseURL, "/") {
		return "base_url", "base_url is an http or https URL without a query or a trailing slash, as in http://127.0.0.1:8000/v1"
	}
	return "", ""
}

func checkLLMNode(n *LLMNode, add func(Value, string, ...any)) {
	if len(n.Description) > 1024 {
		add(n.At.Field("description"), "the description is longer than 1024 bytes")
	}
	if len(n.Models) > MaxLLMModels {
		add(n.At.Field("models"), "at most %d models", MaxLLMModels)
	}
	for i := range n.Models {
		m := &n.Models[i]
		if f, msg := CheckLLMModel(m); msg != "" {
			add(m.At.Field(f), "%s", msg)
		} else if j := slices.IndexFunc(n.Models, func(o LLMModel) bool {
			return o.Name == m.Name && LLMProtocol(o.API) == LLMProtocol(m.API)
		}); j < i {
			if n.Models[j].API == m.API {
				add(m.At.Field("name"), "%s (%s) is listed twice", m.Name, m.API)
			} else {
				add(m.At.Field("name"), "%s is listed twice for agents' %s API, as %s and %s", m.Name, LLMProtocol(m.API), n.Models[j].API, m.API)
			}
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

func checkLLMProxy(c *LLMProxy, add func(Value, string, ...any)) {
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
	if len(c.APIKeys) > MaxLLMKeys {
		add(c.At.Field("api_keys"), "at most %d keys", MaxLLMKeys)
	}
	for i, k := range c.APIKeys {
		at := c.At.Field("api_keys").Index(i)
		if b, err := hex.DecodeString(k.ID); err != nil || len(b) != 8 {
			add(at.Field("id"), "the ID is 16 hex digits")
		} else if slices.IndexFunc(c.APIKeys, func(o LLMProxyKey) bool { return o.ID == k.ID }) < i {
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
