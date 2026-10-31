package config

import (
	"encoding/hex"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// WebRTC is what fit runs for WebRTC beside the gateway, on its engine: the
// TURN server, a role of package app. See docs/decisions/029-webrtc.md.
type WebRTC struct {
	TURN *TURNServer `json:"turn,omitempty" doc:"Run a TURN server (RFC 8656), which relays UDP for WebRTC peers and other ICE agents that cannot reach each other directly. None when left out."`

	At Value `json:"-" fit:"self"`
}

// The defaults of TURNServer.
const (
	DefaultTURNMaxAllocations        = 1024
	DefaultTURNMaxAllocationsPerUser = 16
	DefaultTURNUserBandwidth         = 2 << 20
	DefaultTURNLifetime              = 10 * time.Minute
)

// TURNServer is a TURN server: where it listens, whom it lets in, where it
// relays from, and its limits.
type TURNServer struct {
	Listen                []string   `json:"listen" fit:"required" doc:"The UDP addresses clients reach the server at, as in \":3478\", the port TURN's turn: URIs default to. TURN over TCP and TLS is not served yet."`
	Realm                 string     `json:"realm" fit:"required" doc:"The realm of the credentials, sent in every challenge: clients derive their key from their name, the realm and their password. A change starts the server again, ending its allocations."`
	Users                 []TURNUser `json:"users,omitempty" doc:"The users with fixed credentials. With secret, or alone; at least one of the two."`
	Secret                string     `json:"secret,omitempty" doc:"The shared secret of time-limited credentials, as coturn's static-auth-secret (use-auth-secret) has it: a username \"<expiry in Unix seconds>:<anything>\" whose password is the base64 of HMAC-SHA1(secret, username), valid until the expiry. A web service that knows the secret hands them out."`
	RelayIP               string     `json:"relay_ip" fit:"required" doc:"The address the relayed sockets bind to, one of the host's, not a wildcard; clients are told it, unless public_ip is set. Its family is every allocation's."`
	PublicIP              string     `json:"public_ip,omitempty" doc:"The address clients are told for their relayed sockets instead of relay_ip, for a server behind a 1:1 NAT that maps it to relay_ip. Of relay_ip's family."`
	PortMin               int        `json:"port_min,omitempty" min:"0" max:"65535" doc:"The lowest port of the relayed sockets, as a firewall that opens a range expects; with port_max. Ports the kernel picks when both are left out."`
	PortMax               int        `json:"port_max,omitempty" min:"0" max:"65535" doc:"The highest port of the relayed sockets; with port_min."`
	MaxAllocations        int        `json:"max_allocations,omitempty" min:"0" default:"1024" doc:"The most allocations held at once; one more is refused with 508. Each holds a relayed socket on every event loop. 1024 when left out or 0. A change starts the server again."`
	MaxAllocationsPerUser int        `json:"max_allocations_per_user,omitempty" min:"0" default:"16" doc:"The most allocations of one username; one more is refused with 486. 16 when left out or 0. A change starts the server again."`
	UserBandwidth         int64      `json:"user_bandwidth,omitempty" min:"0" default:"2097152" doc:"The most bytes a second relayed for one username each way, to peers and from them, with a second's worth of burst; datagrams past it are dropped. 2 MiB when left out or 0."`
	DefaultLifetime       Duration   `json:"default_lifetime,omitempty" default:"\"10m\"" doc:"The lifetime of an allocation whose client asks for less, or for none; clients refresh it before it ends. 10m when left out. A change starts the server again."`
	MaxLifetime           Duration   `json:"max_lifetime,omitempty" default:"\"1h\"" doc:"The longest lifetime a client may ask for. 1h when left out. A change starts the server again."`
	NonceLifetime         Duration   `json:"nonce_lifetime,omitempty" default:"\"10m\"" doc:"How long a nonce is good for; a request with an older one is answered 438 with a fresh one. 10m when left out. A change starts the server again."`
	NonceSecret           string     `json:"nonce_secret,omitempty" doc:"The key nonces are made with; a random one when left out. Servers behind one load balancer, which may each see a client's requests, share one. A change starts the server again."`
	AllowedPeers          []string   `json:"allowed_peers,omitempty" doc:"The addresses and prefixes, as in \"10.1.0.0/16\", peers may be at even when denied_peers or the default denial cover them: loopback, private, link-local and other internal ranges are denied unless listed here, so that the relay cannot reach into the network it runs in."`
	DeniedPeers           []string   `json:"denied_peers,omitempty" doc:"Addresses and prefixes peers may not be at, beyond the internal ranges denied by default."`

	At Value `json:"-" fit:"self"`
}

// TURNUser is a user of a TURN server, with a password or the key it
// derives.
type TURNUser struct {
	Name     string `json:"name" fit:"required" doc:"The username."`
	Password string `json:"password,omitempty" doc:"The password; or key."`
	Key      string `json:"key,omitempty" doc:"Instead of the password, the long-term key it derives, as coturn's turnadmin -k prints it: MD5(name \":\" realm \":\" password) in hex, optionally after 0x, so that the password is not kept."`

	At Value `json:"-" fit:"self"`
}

// MaxAllocs returns max_allocations, or its default.
func (t *TURNServer) MaxAllocs() int {
	return int(orDefault(int64(t.MaxAllocations), DefaultTURNMaxAllocations))
}

// MaxAllocsPerUser returns max_allocations_per_user, or its default.
func (t *TURNServer) MaxAllocsPerUser() int {
	return int(orDefault(int64(t.MaxAllocationsPerUser), DefaultTURNMaxAllocationsPerUser))
}

// Bandwidth returns user_bandwidth, or its default.
func (t *TURNServer) Bandwidth() int64 { return orDefault(t.UserBandwidth, DefaultTURNUserBandwidth) }

// KeyBytes returns u's key decoded, or nil without one or with one that is
// not 16 bytes of hex.
func (u TURNUser) KeyBytes() []byte {
	k, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(u.Key, "0x"), "0X"))
	if err != nil || len(k) != 16 {
		return nil
	}
	return k
}

// ParsePeerPrefix parses one of allowed_peers or denied_peers: a prefix, or
// an address, which is a prefix of its whole length.
func ParsePeerPrefix(s string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), true
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), true
	}
	return netip.Prefix{}, false
}

func (c *Config) checkWebRTC(add func(Value, string, ...any)) {
	if c.WebRTC == nil || c.WebRTC.TURN == nil {
		return
	}
	t := c.WebRTC.TURN
	if len(t.Listen) == 0 {
		add(t.At.Field("listen"), "at least one address is needed")
	}
	for i, a := range t.Listen {
		if _, msg := checkAddress(a); msg != "" {
			add(t.At.Field("listen").Index(i), "%s", msg)
		} else if _, port, _ := net.SplitHostPort(a); port != "0" && slices.Index(t.Listen, a) < i {
			add(t.At.Field("listen").Index(i), "%s is listed already", a)
		}
	}
	if strings.TrimSpace(t.Realm) == "" {
		add(t.At.Field("realm"), "the realm is empty")
	}
	if len(t.Users) == 0 && t.Secret == "" {
		add(t.At, "no one could log in: set users, secret or both")
	}
	for i, u := range t.Users {
		at := t.At.Field("users").Index(i)
		switch {
		case u.Name == "":
			add(at.Field("name"), "the name is empty")
		case slices.IndexFunc(t.Users, func(o TURNUser) bool { return o.Name == u.Name }) < i:
			add(at.Field("name"), "another user is already named %q", u.Name)
		}
		switch {
		case (u.Password == "") == (u.Key == ""):
			add(at, "set password or key, one of the two")
		case u.Key != "" && u.KeyBytes() == nil:
			add(at.Field("key"), "the key is not 32 hex digits")
		}
	}
	relay, err := netip.ParseAddr(t.RelayIP)
	switch {
	case err != nil || relay.Zone() != "":
		add(t.At.Field("relay_ip"), "%q is not an IP address", t.RelayIP)
	case relay.IsUnspecified():
		add(t.At.Field("relay_ip"), "%q is a wildcard: name one of the host's addresses, which clients are told", t.RelayIP)
	}
	if t.PublicIP != "" {
		pub, err := netip.ParseAddr(t.PublicIP)
		switch {
		case err != nil || pub.Zone() != "":
			add(t.At.Field("public_ip"), "%q is not an IP address", t.PublicIP)
		case pub.IsUnspecified():
			add(t.At.Field("public_ip"), "%q is a wildcard", t.PublicIP)
		case relay.IsValid() && pub.Unmap().Is4() != relay.Unmap().Is4():
			add(t.At.Field("public_ip"), "%s is not of relay_ip's family", t.PublicIP)
		}
	}
	switch {
	case (t.PortMin == 0) != (t.PortMax == 0):
		add(t.At, "set port_min and port_max together")
	case t.PortMin > t.PortMax:
		add(t.At.Field("port_min"), "port_min %d is above port_max %d", t.PortMin, t.PortMax)
	}
	def := t.DefaultLifetime.Std()
	if def <= 0 {
		def = DefaultTURNLifetime
	}
	if t.MaxLifetime > 0 && t.MaxLifetime.Std() < def {
		add(t.At.Field("max_lifetime"), "max_lifetime %s is below default_lifetime", t.MaxLifetime)
	}
	for _, f := range []struct {
		name string
		list []string
	}{{"allowed_peers", t.AllowedPeers}, {"denied_peers", t.DeniedPeers}} {
		for i, p := range f.list {
			if _, ok := ParsePeerPrefix(p); !ok {
				add(t.At.Field(f.name).Index(i), "%q is not an address or a prefix, as in \"10.1.0.0/16\"", p)
			}
		}
	}
}
