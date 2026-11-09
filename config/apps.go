package config

// The apps fit runs beside its gateway: the LLM feature's roles, which
// share models among machines, and the tunnel's, which reach services behind NAT.
// Each role is on when its member is set, any number of them in one process,
// all on the listeners' fib engine. What a role's web pages change is
// written back into its member here, and so into admin.store when the
// config is kept there. See docs/decisions/028-llm-and-tunnel.md.

// LLM is the LLM feature's roles: a hub that nodes and proxies meet at, a
// node that serves models, and a proxy that calls them for agents.
type LLM struct {
	Hub   *LLMHub   `json:"hub,omitempty" doc:"Run a hub: nodes and proxies link to it, and it passes calls between them."`
	Node  *LLMNode  `json:"node,omitempty" doc:"Run a node: it serves models from local or remote upstreams to the hubs and proxies linked to it."`
	Proxy *LLMProxy `json:"proxy,omitempty" doc:"Run a proxy: an OpenAI- and Anthropic-compatible API, for agents, that calls the models of the nodes it reaches."`

	At Value `json:"-" fit:"self"`
}

// Tunnel is the tunnel's roles: a hub that nodes and proxies link to, a
// node beside the services it exposes, and a proxy that reaches them.
type Tunnel struct {
	Hub   *TunnelHub   `json:"hub,omitempty" doc:"Run a hub: nodes and proxies link to it, and it joins a proxy's tunnels to a node's."`
	Node  *TunnelNode  `json:"node,omitempty" doc:"Run a node: it links to hubs and opens the tunnels they ask for to the services it exposes."`
	Proxy *TunnelProxy `json:"proxy,omitempty" doc:"Run a proxy: local ports and web proxies forwarded through a hub to the services of a node."`

	At Value `json:"-" fit:"self"`
}

// HasApps reports whether c runs any role: the LLM feature's, the tunnel's,
// WebRTC's, the streaming role, the sync role or the BitTorrent client.
func (c *Config) HasApps() bool {
	a, t, w := c.LLM, c.Tunnel, c.WebRTC
	return a != nil && (a.Hub != nil || a.Node != nil || a.Proxy != nil) ||
		t != nil && (t.Hub != nil || t.Node != nil || t.Proxy != nil) ||
		w != nil && (w.TURN != nil || w.Media != nil || w.Proxy != nil) ||
		c.Streaming != nil || c.Sync != nil || c.BitTorrent != nil && (c.BitTorrent.Client != nil || c.BitTorrent.Tracker != nil)
}

// RoleConsole is a role's web pages, behind logins as admin.console's are,
// served on the listeners' engine.
type RoleConsole struct {
	Address         string        `json:"address" fit:"required" doc:"The host and port the pages listen on. A host that is not a loopback address needs cert_file and key_file, so that passwords never cross a network in the clear."`
	MPTCP           bool          `json:"mptcp,omitempty" doc:"Listen the pages with Multipath TCP where the kernel offers it (Linux 5.6+, net.mptcp.enabled), falling back to plain TCP where it does not. Off by default."`
	CertFile        string        `json:"cert_file,omitempty" doc:"A PEM file with the pages' certificate, followed by any intermediates; with key_file, the pages are served over TLS."`
	KeyFile         string        `json:"key_file,omitempty" doc:"A PEM file with the certificate's private key."`
	Users           []ConsoleUser `json:"users" fit:"required" doc:"Who may log in: at least one user."`
	SessionTimeout  Duration      `json:"session_timeout,omitempty" default:"\"30m\"" doc:"How long a login lasts without a request. 30m when left out."`
	SessionLifetime Duration      `json:"session_lifetime,omitempty" default:"\"12h\"" doc:"How long a login lasts at most, however busy. 12h when left out."`
	MaxSessions     int           `json:"max_sessions,omitempty" min:"0" default:"64" doc:"The most logins kept at once; one more ends the oldest. 64 when left out or 0."`
	MaxConns        int           `json:"max_conns,omitempty" min:"0" default:"32" doc:"The most connections the pages keep open at once, a quarter of them kept for those with a login. When they are taken, a new connection closes the oldest of those without one, of the client holding the most, and is closed as it is accepted only when every one has a login; one past a client's share, a quarter of them, is closed too, but for loopback clients. 32 when left out or 0."`
	Access          *Access       `json:"access,omitempty" doc:"The client addresses the pages serve, deciding before the global access, which applies to the pages too: a connection either refuses is closed as soon as it is accepted. It follows every config applied, without restarting the pages. Set allow to \"127.0.0.1\" (and \"::1\") to keep local access whatever the global access says. Every address is served, unless the global access says otherwise, when left out."`

	At Value `json:"-" fit:"self"`
}

// Console returns c as an admin.console, for what the two share.
func (c *RoleConsole) Console() *Console {
	return &Console{Address: c.Address, MPTCP: c.MPTCP, CertFile: c.CertFile, KeyFile: c.KeyFile, Users: c.Users,
		SessionTimeout: c.SessionTimeout, SessionLifetime: c.SessionLifetime, MaxSessions: c.MaxSessions, MaxConns: c.MaxConns, Access: c.Access, At: c.At}
}

// checkRoleConsole checks a role's console as admin.console is checked.
func checkRoleConsole(c *RoleConsole, add func(Value, string, ...any)) {
	if c != nil {
		checkConsole(c.Console(), add)
	}
}

// RoleConsoles returns the roles' pages c sets: those of the llm and the
// tunnel roles, and the BitTorrent client's.
func (c *Config) RoleConsoles() []*RoleConsole {
	var out []*RoleConsole
	add := func(rc *RoleConsole) {
		if rc != nil {
			out = append(out, rc)
		}
	}
	if l := c.LLM; l != nil {
		if l.Hub != nil {
			add(l.Hub.Console)
		}
		if l.Node != nil {
			add(l.Node.Console)
		}
		if l.Proxy != nil {
			add(l.Proxy.Console)
		}
	}
	if t := c.Tunnel; t != nil {
		if t.Hub != nil {
			add(t.Hub.Console)
		}
		if t.Node != nil {
			add(t.Node.Console)
		}
		if t.Proxy != nil {
			add(t.Proxy.Console)
		}
	}
	if b := c.BitTorrent; b != nil && b.Client != nil {
		add(b.Client.Console)
	}
	if b := c.BitTorrent; b != nil && b.Tracker != nil {
		add(b.Tracker.Console)
	}
	return out
}
