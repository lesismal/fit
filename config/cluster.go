package config

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The defaults and bounds of Cluster.
const (
	DefaultClusterPollInterval     = Duration(10 * time.Second)
	DefaultClusterTimeout          = Duration(10 * time.Second)
	DefaultClusterMaxNodes         = 256
	DefaultClusterMaxConcurrency   = 8
	DefaultClusterMaxResponseBytes = 4 << 20
	// MinClusterPollInterval and MinClusterTimeout keep a manager from
	// polling its members as fast as it can.
	MinClusterPollInterval = Duration(time.Second)
	MinClusterTimeout      = Duration(time.Second)
	// MinClusterResponseBytes is the least max_response_bytes: enough for
	// GET /node and a small /status.
	MinClusterResponseBytes = 16 << 10
	// DefaultClusterEnrollTTL is how long an enrollment token lasts when
	// the page asks for no lifetime; MaxClusterEnrollTTL the longest one
	// may.
	DefaultClusterEnrollTTL = Duration(24 * time.Hour)
	MaxClusterEnrollTTL     = Duration(30 * 24 * time.Hour)
	// ClusterLinkPath is the path a manager's link listener takes members'
	// WebSockets on.
	ClusterLinkPath = "/cluster/link"
	// ClusterEnrollPrefix starts an enrollment token, which a manager
	// makes for a member to link with once: the prefix, the token's ID (16
	// hex digits), a dot and its secret.
	ClusterEnrollPrefix = "fite1."
)

// Cluster turns this fit into a manager of other fits, its members: it
// keeps their addresses and credentials in admin.store, polls each one's
// state, and serves them to the workbench as the console module cluster.
// See docs/decisions/033-cluster-management.md.
type Cluster struct {
	PollInterval     Duration `json:"poll_interval,omitempty" default:"\"10s\"" doc:"How often each member's GET /node, /status and /config/check are read. 10s when left out; 1s at least."`
	Timeout          Duration `json:"timeout,omitempty" default:"\"10s\"" doc:"The longest one call to a member may take, from its connect to the last byte of its answer; the connect and the wait for the answer's header are bounded by it too. 10s when left out; 1s at least."`
	MaxNodes         int      `json:"max_nodes,omitempty" min:"0" max:"10000" default:"256" doc:"The most members the manager keeps; one more is refused. 256 when left out or 0."`
	MaxConcurrency   int      `json:"max_concurrency,omitempty" min:"0" max:"256" default:"8" doc:"The most members polled at once; the others wait for their turn. Tests from the pages run beside them, 4 at most. 8 when left out or 0."`
	MaxResponseBytes int64    `json:"max_response_bytes,omitempty" min:"0" max:"67108864" default:"4194304" doc:"The largest answer read from a member; a larger one fails its call, and is reported as too large. 4 MiB when left out or 0; 16 KiB at least."`
	LinkAddress      string   `json:"link_address,omitempty" doc:"Where members that link out to the manager reach it, as \":7444\" or \"0.0.0.0:7444\": a WebSocket listener over TLS, with a self-signed certificate the manager makes once and keeps in the store, which each member pins when it enrolls. Members behind NAT, or with no port open, link here with the config line Add by enrollment gives, and are managed through their link as the others are over HTTP. Off when left out: no member can enroll."`
	LinkURL          string   `json:"link_url,omitempty" doc:"The URL members reach link_address at, as \"wss://manager.example.com:7444/cluster/link\", which the config line Add by enrollment gives names; when left out, the host the page was opened at and link_address's port. Only a wss URL."`

	At Value `json:"-" fit:"self"`
}

// ClusterMember links this fit out to a manager of many fits (admin.cluster
// on the manager), which then manages it through the link: for a fit behind
// NAT, or with no port open to the manager. See
// docs/decisions/033-cluster-management.md.
type ClusterMember struct {
	Manager     string `json:"manager" fit:"required" doc:"The manager's link URL, as its Add by enrollment page gives it: wss://host:port/cluster/link."`
	Fingerprint string `json:"fingerprint" fit:"required" doc:"The SHA-256 of the manager's link certificate, 64 hex digits, with or without colons, as the page gives it: the only certificate this fit links to."`
	Token       string `json:"token,omitempty" doc:"The enrollment token the manager made for this fit, fite1.<ID>.<secret>, used once: this fit proves it to the manager, which answers a credential of this fit's own, kept in the store, or without one in a file beside the config (fit.cluster-member beside fit.json); the token is not used again. Left out once this fit has a credential; a new token enrolls this fit again."`
	TokenFile   string `json:"token_file,omitempty" doc:"A file holding the enrollment token, in place of token."`

	At Value `json:"-" fit:"self"`
}

// Interval, CallTimeout, Nodes, Concurrency and ResponseBytes return the
// settings, their defaults when left out.
func (c *Cluster) Interval() time.Duration {
	return time.Duration(orDefault(int64(c.PollInterval), int64(DefaultClusterPollInterval)))
}
func (c *Cluster) CallTimeout() time.Duration {
	return time.Duration(orDefault(int64(c.Timeout), int64(DefaultClusterTimeout)))
}
func (c *Cluster) Nodes() int { return int(orDefault(int64(c.MaxNodes), DefaultClusterMaxNodes)) }
func (c *Cluster) Concurrency() int {
	return int(orDefault(int64(c.MaxConcurrency), DefaultClusterMaxConcurrency))
}
func (c *Cluster) ResponseBytes() int64 {
	return orDefault(c.MaxResponseBytes, DefaultClusterMaxResponseBytes)
}

// checkCluster checks admin.cluster: it keeps its members' credentials in
// the store, and serves them in the console.
func checkCluster(a *Admin, add func(Value, string, ...any)) {
	c := a.Cluster
	if c == nil {
		return
	}
	if a.Store == nil {
		add(c.At, "a manager keeps its members' credentials in the store: set admin.store too")
	}
	if a.Console == nil {
		add(c.At, "a manager's members are served in the console's workbench: set admin.console too")
	}
	if c.PollInterval != 0 && c.PollInterval < MinClusterPollInterval {
		add(c.At.Field("poll_interval"), "%s is below %s", c.PollInterval, MinClusterPollInterval)
	}
	if c.Timeout != 0 && c.Timeout < MinClusterTimeout {
		add(c.At.Field("timeout"), "%s is below %s", c.Timeout, MinClusterTimeout)
	}
	if c.MaxResponseBytes != 0 && c.MaxResponseBytes < MinClusterResponseBytes {
		add(c.At.Field("max_response_bytes"), "%d is below %d", c.MaxResponseBytes, MinClusterResponseBytes)
	}
	if c.LinkAddress != "" {
		if _, port, err := net.SplitHostPort(c.LinkAddress); err != nil || port == "" {
			add(c.At.Field("link_address"), "%q is not a host and a port, as \":7444\"", c.LinkAddress)
		}
	}
	if c.LinkURL != "" {
		if msg := CheckClusterLinkURL(c.LinkURL); msg != "" {
			add(c.At.Field("link_url"), "%s", msg)
		} else if c.LinkAddress == "" {
			add(c.At.Field("link_url"), "link_url names where members reach link_address: set link_address too")
		}
	}
}

// CheckClusterLinkURL checks a manager's link URL: wss, a host, no user, query or
// fragment; "" when it is one.
func CheckClusterLinkURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return strconv.Quote(raw) + " is not a wss:// URL without a user, a query or a fragment, as wss://manager.example.com:7444" + ClusterLinkPath
	}
	return ""
}

// ValidFingerprint reports whether s is a SHA-256 written as fit writes pins:
// 64 hex digits, in either case, with or without colons between the bytes.
func ValidFingerprint(s string) bool {
	h := strings.ReplaceAll(strings.TrimSpace(s), ":", "")
	if len(h) != 64 {
		return false
	}
	for _, r := range h {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// CheckClusterEnrollToken checks an enrollment token's form,
// ClusterEnrollPrefix, 16 hex digits, a dot and a secret of base64url; ""
// when it is one.
func CheckClusterEnrollToken(tok string) string {
	rest, ok := strings.CutPrefix(tok, ClusterEnrollPrefix)
	id, secret, dot := strings.Cut(rest, ".")
	if !ok || !dot || !ValidLinkTokenID(id) || len(secret) < 22 || len(secret) > 86 ||
		strings.IndexFunc(secret, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) >= 0 {
		return "not an enrollment token: one is " + ClusterEnrollPrefix + "<16 hex digits>.<secret>, as the manager's Add by enrollment makes it"
	}
	return ""
}

// checkClusterMember checks admin.cluster_member.
func checkClusterMember(m *ClusterMember, add func(Value, string, ...any)) {
	if m == nil {
		return
	}
	// Left out, they are refused as required.
	if msg := CheckClusterLinkURL(m.Manager); m.Manager != "" && msg != "" {
		add(m.At.Field("manager"), "%s", msg)
	}
	if m.Fingerprint != "" && !ValidFingerprint(m.Fingerprint) {
		add(m.At.Field("fingerprint"), "%q is not the SHA-256 of a certificate: 64 hex digits, with or without colons", m.Fingerprint)
	}
	switch {
	case m.Token != "" && m.TokenFile != "":
		add(m.At.Field("token"), "token and token_file are two places for one token: set one")
	case m.Token != "":
		if msg := CheckClusterEnrollToken(m.Token); msg != "" {
			add(m.At.Field("token"), "%s", msg)
		}
	}
}
