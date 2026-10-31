package config

// The apps fit runs beside its gateway: the agent's roles, which share
// models among machines, and the tunnel's, which reach services behind NAT.
// Each role is on when its member is set, any number of them in one process,
// all on the listeners' fib engine. What a role's web pages change is
// written back into its member here, and so into admin.store when the
// config is kept there. See docs/decisions/028-agent-and-tunnel.md.

// Agent is the agent's roles: a hub that nodes and clients meet at, a node
// that serves models, and a client that calls them.
type Agent struct {
	Hub    *AgentHub    `json:"hub,omitempty" doc:"Run a hub: nodes and clients link to it, and it passes calls between them."`
	Node   *AgentNode   `json:"node,omitempty" doc:"Run a node: it serves models from local or remote upstreams to the hubs and clients linked to it."`
	Client *AgentClient `json:"client,omitempty" doc:"Run a client: an OpenAI- and Anthropic-compatible API that calls the models of the nodes it reaches."`

	At Value `json:"-" fit:"self"`
}

// Tunnel is the tunnel's roles: a server that nodes and clients link to, a
// node beside the services it exposes, and a client that reaches them.
type Tunnel struct {
	Server *TunnelServer `json:"server,omitempty" doc:"Run a server: nodes and clients link to it, and it joins a client's tunnels to a node's."`
	Node   *TunnelNode   `json:"node,omitempty" doc:"Run a node: it links to servers and opens the tunnels they ask for to the services it exposes."`
	Client *TunnelClient `json:"client,omitempty" doc:"Run a client: local ports and web proxies forwarded through a server to the services of a node."`

	At Value `json:"-" fit:"self"`
}

// HasApps reports whether c runs any role: the agent's, the tunnel's or
// WebRTC's.
func (c *Config) HasApps() bool {
	a, t, w := c.Agent, c.Tunnel, c.WebRTC
	return a != nil && (a.Hub != nil || a.Node != nil || a.Client != nil) ||
		t != nil && (t.Server != nil || t.Node != nil || t.Client != nil) ||
		w != nil && w.TURN != nil
}

// RoleConsole is a role's web pages, behind logins as admin.console's are,
// served on the listeners' engine.
type RoleConsole struct {
	Address         string        `json:"address" fit:"required" doc:"The host and port the pages listen on. A host that is not a loopback address needs cert_file and key_file, so that passwords never cross a network in the clear."`
	CertFile        string        `json:"cert_file,omitempty" doc:"A PEM file with the pages' certificate, followed by any intermediates; with key_file, the pages are served over TLS."`
	KeyFile         string        `json:"key_file,omitempty" doc:"A PEM file with the certificate's private key."`
	Users           []ConsoleUser `json:"users" fit:"required" doc:"Who may log in: at least one user."`
	SessionTimeout  Duration      `json:"session_timeout,omitempty" default:"\"30m\"" doc:"How long a login lasts without a request. 30m when left out."`
	SessionLifetime Duration      `json:"session_lifetime,omitempty" default:"\"12h\"" doc:"How long a login lasts at most, however busy. 12h when left out."`
	MaxSessions     int           `json:"max_sessions,omitempty" min:"0" default:"64" doc:"The most logins kept at once; one more ends the oldest. 64 when left out or 0."`
	MaxConns        int           `json:"max_conns,omitempty" min:"0" default:"32" doc:"The most connections the pages keep open at once; one past it is closed as soon as it is accepted. 32 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Console returns c as an admin.console, for what the two share.
func (c *RoleConsole) Console() *Console {
	return &Console{Address: c.Address, CertFile: c.CertFile, KeyFile: c.KeyFile, Users: c.Users,
		SessionTimeout: c.SessionTimeout, SessionLifetime: c.SessionLifetime, MaxSessions: c.MaxSessions, MaxConns: c.MaxConns, At: c.At}
}

// checkRoleConsole checks a role's console as admin.console is checked.
func checkRoleConsole(c *RoleConsole, add func(Value, string, ...any)) {
	if c != nil {
		checkConsole(c.Console(), add)
	}
}
