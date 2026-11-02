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
// TURN server and the media port of WHIP and WHEP, roles of package app.
// See docs/decisions/029-webrtc.md.
type WebRTC struct {
	TURN  *TURNServer  `json:"turn,omitempty" doc:"Run a TURN server (RFC 8656), which relays UDP for WebRTC peers and other ICE agents that cannot reach each other directly. None when left out."`
	Media *WebRTCMedia `json:"media,omitempty" doc:"Run fit as a WebRTC media server: the UDP port the PeerConnections of the whip and whep handlers share, the ICE servers told to their peers, and their limits. Routes with whip or whep handlers answer 503 without it. None when left out."`
	Proxy *WebRTCProxy `json:"proxy,omitempty" doc:"Put media servers behind fit: the UDP port the webrtc_proxy handler's answers name, which relays each peer to the media server that answered it, found by the ICE ufrag of the peer's first STUN request. Routes with webrtc_proxy handlers answer 503 without it. None when left out."`

	At Value `json:"-" fit:"self"`
}

// The defaults of TURNServer.
const (
	DefaultTURNMaxAllocations        = 1024
	DefaultTURNMaxAllocationsPerUser = 16
	DefaultTURNUserBandwidth         = 2 << 20
	DefaultTURNLifetime              = 10 * time.Minute
	DefaultTURNMaxPermissions        = 64
	DefaultTURNMaxChannels           = 64
	DefaultTURNMaxStreamMessage      = 8 << 10
	DefaultTURNStreamIdleTimeout     = 30 * time.Second
	// MinTURNStreamMessage is the least max_stream_message may be: a
	// ChannelData message carrying a datagram of a common MTU.
	MinTURNStreamMessage = 2048
)

// The defaults of WebRTCMedia.
const (
	DefaultMediaMaxPeerConnections = 1024
	DefaultMediaMaxStreams         = 64
	DefaultMediaMaxViewers         = 256
	DefaultMediaMaxBitrate         = 8_000_000
	DefaultMediaMaxSDPBytes        = 32 << 10
	DefaultMediaConnectTimeout     = 30 * time.Second
	DefaultMediaICETimeout         = 30 * time.Second
	DefaultMediaKeyFrameInterval   = 500 * time.Millisecond
	DefaultMediaTURNCredentialTTL  = time.Hour
)

// MediaCodecs are the codecs webrtc.media's codecs may name: fib's default
// codecs, by encoding name.
var MediaCodecs = []string{"opus", "VP8", "VP9", "H264", "AV1"}

// WebRTCMedia is the media side of fit's WHIP and WHEP: one UDP port that
// every PeerConnection shares, an ICE lite agent's host candidates on it,
// the ICE servers peers are told of, and the limits.
type WebRTCMedia struct {
	Listen             string      `json:"listen" fit:"required" doc:"The UDP address every peer's media reaches fit at, one port all PeerConnections share, as in \":8189\". A wildcard host advertises the address of every interface that is up, unless public_ips is set. A change starts the media server again, ending its sessions."`
	PublicIPs          []string    `json:"public_ips,omitempty" doc:"The addresses peers are told to reach the port at, with its port, instead of the interfaces' own: a server behind a 1:1 NAT, or a load balancer that forwards the port unchanged. A change starts the media server again."`
	ICEServers         []ICEServer `json:"ice_servers,omitempty" doc:"STUN and TURN servers the peers are told of in Link headers (RFC 9725), for their side of ICE; fit's side needs none, as it is an ICE lite agent on its own address."`
	TURNURLs           []string    `json:"turn_urls,omitempty" doc:"URLs of fit's own TURN server, webrtc.turn, as peers reach it, as in \"turn:turn.example.com:3478\": each answer's Link headers carry them with time-limited credentials made from webrtc.turn's secret, which must be set."`
	TURNCredentialTTL  Duration    `json:"turn_credential_ttl,omitempty" default:"\"1h\"" doc:"How long the TURN credentials an answer carries are good for. 1h when left out."`
	Codecs             []string    `json:"codecs,omitempty" default:"[\"opus\",\"VP8\",\"VP9\",\"H264\",\"AV1\"]" doc:"The codecs publishers may send, by encoding name: opus, VP8, VP9, H264 and AV1, every one when left out. Each publisher's offer is answered with the first of its codecs of each kind that is listed here, which its viewers must accept."`
	MaxPeerConnections int         `json:"max_peer_connections,omitempty" min:"0" default:"1024" doc:"The most PeerConnections at once, publishers' and viewers'; one more offer is answered 503. 1024 when left out or 0."`
	MaxStreams         int         `json:"max_streams,omitempty" min:"0" default:"64" doc:"The most streams published at once; one more publisher is answered 503. 64 when left out or 0."`
	MaxViewers         int         `json:"max_viewers,omitempty" min:"0" default:"256" doc:"The most viewers of one stream; one more is answered 503. Each packet a publisher sends is sent again to every viewer, on the worker that received it. 256 when left out or 0."`
	MaxBitrate         int64       `json:"max_bitrate,omitempty" min:"0" default:"8000000" doc:"The most bits a second a publisher may send: told to it as REMB feedback, which browsers keep under, and enforced with two seconds of burst, past which its packets are dropped. 8000000 when left out or 0."`
	MaxSDPBytes        int         `json:"max_sdp_bytes,omitempty" min:"0" default:"32768" doc:"The largest offer taken; a larger one is answered 413. 32 KiB when left out or 0."`
	ConnectTimeout     Duration    `json:"connect_timeout,omitempty" default:"\"30s\"" doc:"How long a session may take from its answer to a connected PeerConnection before it is ended. 30s when left out."`
	ICETimeout         Duration    `json:"ice_timeout,omitempty" default:"\"30s\"" doc:"How long a connected peer may be silent, its media and ICE consent checks alike, before its session is ended: a publisher's ends its stream and its viewers. 30s when left out."`
	KeyFrameInterval   Duration    `json:"key_frame_interval,omitempty" default:"\"500ms\"" doc:"The least time between two key frame requests (PLI) passed to a publisher, for the viewers that join or lose packets: the rest are dropped. 500ms when left out."`

	At Value `json:"-" fit:"self"`
}

// The defaults of WebRTCProxy.
const (
	DefaultProxyMaxSessions    = 4096
	DefaultProxyMaxRelays      = 4096
	DefaultProxySessionTimeout = 30 * time.Second
	DefaultProxyIdleTimeout    = 30 * time.Second
)

// WebRTCProxy is the media side of the webrtc_proxy handler: one UDP port
// that peers of every media server behind fit reach, and its limits.
type WebRTCProxy struct {
	Listen         string   `json:"listen" fit:"required" doc:"The UDP address peers reach the media servers behind fit at, one port for them all, as in \":8190\": the proxied answers name it as their only candidate. A wildcard host advertises the address of every interface that is up, unless public_ips is set. A change ends every relay and session."`
	PublicIPs      []string `json:"public_ips,omitempty" doc:"The addresses peers are told to reach the port at, with its port, instead of the interfaces' own: a server behind a 1:1 NAT. Applies to the next answer."`
	MaxSessions    int      `json:"max_sessions,omitempty" min:"0" default:"4096" doc:"The most sessions proxied at once; one more offer is answered 503 before it reaches a media server. 4096 when left out or 0."`
	MaxRelays      int      `json:"max_relays,omitempty" min:"0" default:"4096" doc:"The most peer addresses relayed at once, each with a UDP socket of its own toward its media server; past it, a new address's STUN request is dropped. A session relays at most 8 of its peer's addresses. 4096 when left out or 0."`
	SessionTimeout Duration `json:"session_timeout,omitempty" default:"\"30s\"" doc:"How long a session is kept with nothing relayed for it: from its answer until its peer's first request, and after its last relay ends. Its ufrags are forgotten then, and its URL answers 404. 30s when left out."`
	IdleTimeout    Duration `json:"idle_timeout,omitempty" default:"\"30s\"" doc:"How long a relayed peer address may send nothing before its relay is closed, whatever its media server sends it; ICE's consent checks, every 5 seconds or so, keep a live one open. 30s when left out."`
	RelayIP        string   `json:"relay_ip,omitempty" doc:"The address the sockets toward the media servers leave from, one of the host's, for a firewall in front of them that admits one source address, or a host with several interfaces. Of the media servers' family: a media address of the other family is not relayed. The route's pick when left out. Applies to the next relay."`
	PortMin        int      `json:"port_min,omitempty" min:"0" max:"65535" doc:"The lowest local port of the sockets toward the media servers, for a firewall in front of them that admits a range; with port_max. A port in use is skipped, and a peer address that finds the range full is dropped. Ports the kernel picks when both are left out. Applies to the next relay."`
	PortMax        int      `json:"port_max,omitempty" min:"0" max:"65535" doc:"The highest local port of the sockets toward the media servers; with port_min. The range bounds the relays at once, beside max_relays."`

	At Value `json:"-" fit:"self"`
}

// Sessions returns max_sessions, or its default.
func (p *WebRTCProxy) Sessions() int {
	return int(orDefault(int64(p.MaxSessions), DefaultProxyMaxSessions))
}

// Relays returns max_relays, or its default.
func (p *WebRTCProxy) Relays() int {
	return int(orDefault(int64(p.MaxRelays), DefaultProxyMaxRelays))
}

// SessionIdle returns session_timeout, or its default.
func (p *WebRTCProxy) SessionIdle() time.Duration {
	return durationOr(p.SessionTimeout, DefaultProxySessionTimeout)
}

// RelayAddr returns relay_ip with the port range as the local end of a
// dial, the zero AddrPort and 0, 0 for the kernel's pick. The member must
// have been checked.
func (p *WebRTCProxy) RelayAddr() (local netip.AddrPort, portMin, portMax uint16) {
	if a, err := netip.ParseAddr(p.RelayIP); err == nil {
		local = netip.AddrPortFrom(a.Unmap(), 0)
	}
	return local, uint16(p.PortMin), uint16(p.PortMax)
}

// Idle returns idle_timeout, or its default.
func (p *WebRTCProxy) Idle() time.Duration { return durationOr(p.IdleTimeout, DefaultProxyIdleTimeout) }

// ICEServer is a STUN or TURN server, as RTCIceServer has it.
type ICEServer struct {
	URLs       []string `json:"urls" fit:"required" doc:"The server's stun:, stuns:, turn: or turns: URLs."`
	Username   string   `json:"username,omitempty" doc:"A TURN server's username."`
	Credential string   `json:"credential,omitempty" doc:"A TURN server's password."`

	At Value `json:"-" fit:"self"`
}

// MaxPCs returns max_peer_connections, or its default.
func (m *WebRTCMedia) MaxPCs() int {
	return int(orDefault(int64(m.MaxPeerConnections), DefaultMediaMaxPeerConnections))
}

// MaxStreamCount returns max_streams, or its default.
func (m *WebRTCMedia) MaxStreamCount() int {
	return int(orDefault(int64(m.MaxStreams), DefaultMediaMaxStreams))
}

// MaxViewerCount returns max_viewers, or its default.
func (m *WebRTCMedia) MaxViewerCount() int {
	return int(orDefault(int64(m.MaxViewers), DefaultMediaMaxViewers))
}

// Bitrate returns max_bitrate, or its default.
func (m *WebRTCMedia) Bitrate() int64 { return orDefault(m.MaxBitrate, DefaultMediaMaxBitrate) }

// SDPBytes returns max_sdp_bytes, or its default.
func (m *WebRTCMedia) SDPBytes() int {
	return int(orDefault(int64(m.MaxSDPBytes), DefaultMediaMaxSDPBytes))
}

// durationOr returns d, or def when d is not positive.
func durationOr(d Duration, def time.Duration) time.Duration {
	if d.Std() <= 0 {
		return def
	}
	return d.Std()
}

// Connect returns connect_timeout, or its default.
func (m *WebRTCMedia) Connect() time.Duration {
	return durationOr(m.ConnectTimeout, DefaultMediaConnectTimeout)
}

// ICE returns ice_timeout, or its default.
func (m *WebRTCMedia) ICE() time.Duration { return durationOr(m.ICETimeout, DefaultMediaICETimeout) }

// KeyFrames returns key_frame_interval, or its default.
func (m *WebRTCMedia) KeyFrames() time.Duration {
	return durationOr(m.KeyFrameInterval, DefaultMediaKeyFrameInterval)
}

// CredentialTTL returns turn_credential_ttl, or its default.
func (m *WebRTCMedia) CredentialTTL() time.Duration {
	return durationOr(m.TURNCredentialTTL, DefaultMediaTURNCredentialTTL)
}

// TURNServer is a TURN server: where it listens, whom it lets in, where it
// relays from, and its limits.
type TURNServer struct {
	Listen                []string   `json:"listen" fit:"required" doc:"The UDP addresses clients reach the server at, as in \":3478\", the port TURN's turn: URIs default to. A change of listen, listen_tcp or listen_tls starts a new server and drains the old one."`
	ListenTCP             []string   `json:"listen_tcp,omitempty" doc:"TCP addresses clients reach the server at, as in \":3478\", for clients on networks that block UDP: turn:host:3478?transport=tcp. Their allocations relay UDP, and end when their connection closes."`
	ListenTLS             []string   `json:"listen_tls,omitempty" doc:"TLS addresses clients reach the server at, as in \":5349\" or \":443\", with tls's certificates, for clients that only TLS gets through for: turns:host:5349?transport=tcp. As listen_tcp otherwise."`
	TLS                   *TURNTLS   `json:"tls,omitempty" doc:"The certificates of listen_tls, from files, as a listener's tls.certificates has them, and watched as theirs are: a change applies to the next handshake."`
	Realm                 string     `json:"realm" fit:"required" doc:"The realm of the credentials, sent in every challenge: clients derive their key from their name, the realm and their password. A change starts a new server, ending the allocations of the old one when they share an address and draining it otherwise."`
	Users                 []TURNUser `json:"users,omitempty" doc:"The users with fixed credentials. With secret, or alone; at least one of the two."`
	Secret                string     `json:"secret,omitempty" doc:"The shared secret of time-limited credentials, as coturn's static-auth-secret (use-auth-secret) has it: a username \"<expiry in Unix seconds>:<anything>\" whose password is the base64 of HMAC-SHA1(secret, username), valid until the expiry. A web service that knows the secret hands them out."`
	RelayIP               string     `json:"relay_ip" fit:"required" doc:"The address the relayed sockets bind to, one of the host's, not a wildcard; clients are told it, unless public_ip is set. Its family is every allocation's. Peers on it, and on public_ip, are relayed to on the ports of the server's own relayed addresses alone, and on those allowed_peers lists with their port. A change starts a new server."`
	PublicIP              string     `json:"public_ip,omitempty" doc:"The address clients are told for their relayed sockets instead of relay_ip, for a server behind a 1:1 NAT that maps it to relay_ip. Of relay_ip's family. A change starts a new server."`
	PortMin               int        `json:"port_min,omitempty" min:"0" max:"65535" doc:"The lowest port of the relayed sockets, as a firewall that opens a range expects; with port_max. Ports the kernel picks when both are left out. A change starts a new server."`
	PortMax               int        `json:"port_max,omitempty" min:"0" max:"65535" doc:"The highest port of the relayed sockets; with port_min."`
	MaxAllocations        int        `json:"max_allocations,omitempty" min:"0" default:"1024" doc:"The most allocations held at once; one more is refused with 508. Each holds a relayed socket on every event loop. 1024 when left out or 0. A change applies to the next allocation; those held past a lowered one are kept until they end."`
	MaxAllocationsPerUser int        `json:"max_allocations_per_user,omitempty" min:"0" default:"16" doc:"The most allocations of one username; one more is refused with 486. 16 when left out or 0. Applies to the next allocation."`
	MaxPermissions        int        `json:"max_permissions,omitempty" min:"0" default:"64" doc:"The most permissions, peer addresses, one allocation holds; one more is refused with 508. A channel's peer counts. 64 when left out or 0. Applies to the next request."`
	MaxChannels           int        `json:"max_channels,omitempty" min:"0" default:"64" doc:"The most channels one allocation binds; one more is refused with 508. 64 when left out or 0. Applies to the next request."`
	MaxStreamMessage      int        `json:"max_stream_message,omitempty" min:"0" default:"8192" doc:"The largest message a TCP or TLS client may send, and so what its connection buffers of one not yet whole; a connection whose next message claims more is closed. At least 2048. 8 KiB when left out or 0. A change starts a new server."`
	StreamIdleTimeout     Duration   `json:"stream_idle_timeout,omitempty" default:"\"30s\"" doc:"How long a TCP or TLS client's connection may stay open without an allocation, before its first Allocate or after its allocation ended, before it is closed. 30s when left out. A change starts a new server."`
	UserBandwidth         int64      `json:"user_bandwidth,omitempty" min:"0" default:"2097152" doc:"The most bytes a second relayed for one username each way, to peers and from them, with a second's worth of burst; datagrams past it are dropped. 2 MiB when left out or 0."`
	DefaultLifetime       Duration   `json:"default_lifetime,omitempty" default:"\"10m\"" doc:"The lifetime of an allocation whose client asks for less, or for none; clients refresh it before it ends. 10m when left out. Applies to the next allocation or refresh."`
	MaxLifetime           Duration   `json:"max_lifetime,omitempty" default:"\"1h\"" doc:"The longest lifetime a client may ask for. 1h when left out. Applies to the next allocation or refresh."`
	NonceLifetime         Duration   `json:"nonce_lifetime,omitempty" default:"\"10m\"" doc:"How long a nonce is good for; a request with an older one is answered 438 with a fresh one. 10m when left out. Applies to the next nonce."`
	NonceSecret           string     `json:"nonce_secret,omitempty" doc:"The key nonces are made with; a random one when left out. Servers behind one load balancer, which may each see a client's requests, share one. A change starts a new server."`
	AllowedPeers          []string   `json:"allowed_peers,omitempty" doc:"The addresses and prefixes, as in \"10.1.0.0/16\", peers may be at even when denied_peers or the default denial cover them: loopback, private, link-local and other internal ranges are denied unless listed here, so that the relay cannot reach into the network it runs in. An address with a port, as in \"10.0.0.5:8189\", allows that port alone, which is how a peer on relay_ip itself, such as webrtc.media, is reached."`
	DeniedPeers           []string   `json:"denied_peers,omitempty" doc:"Addresses and prefixes peers may not be at, beyond the internal ranges denied by default; an address with a port denies that port alone."`

	At Value `json:"-" fit:"self"`
}

// TURNTLS is the TLS of a TURN server's listen_tls: certificates from files.
type TURNTLS struct {
	Certificates []Certificate `json:"certificates" fit:"required" doc:"Certificates from files. A client is given the first whose names match the one it asks for, or the first of all when none does or it asks for none."`

	At Value `json:"-" fit:"self"`
}

// Config returns t as a listener's TLS, with its certificates alone, for
// package tlsmgr to load.
func (t *TURNTLS) Config() *TLS { return &TLS{Certificates: t.Certificates, At: t.At} }

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

// MaxPerms returns max_permissions, or its default.
func (t *TURNServer) MaxPerms() int {
	return int(orDefault(int64(t.MaxPermissions), DefaultTURNMaxPermissions))
}

// MaxChans returns max_channels, or its default.
func (t *TURNServer) MaxChans() int {
	return int(orDefault(int64(t.MaxChannels), DefaultTURNMaxChannels))
}

// StreamMessage returns max_stream_message, or its default.
func (t *TURNServer) StreamMessage() int {
	return int(orDefault(int64(t.MaxStreamMessage), DefaultTURNMaxStreamMessage))
}

// StreamIdle returns stream_idle_timeout, or its default.
func (t *TURNServer) StreamIdle() time.Duration {
	return durationOr(t.StreamIdleTimeout, DefaultTURNStreamIdleTimeout)
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

// PeerRule is one of allowed_peers or denied_peers: a prefix, and a port
// when the entry names one.
type PeerRule struct {
	Prefix netip.Prefix
	// Port is the entry's port, 0 for every port.
	Port uint16
}

// ParsePeerRule parses one of allowed_peers or denied_peers: a prefix, an
// address, which is a prefix of its whole length, or an address and a port.
func ParsePeerRule(s string) (PeerRule, bool) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return PeerRule{Prefix: p.Masked()}, true
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		return PeerRule{Prefix: netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())}, true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil && ap.Addr().Zone() == "" && ap.Port() != 0 {
		a := ap.Addr().Unmap()
		return PeerRule{Prefix: netip.PrefixFrom(a, a.BitLen()), Port: ap.Port()}, true
	}
	return PeerRule{}, false
}

func (c *Config) checkWebRTC(add func(Value, string, ...any)) {
	if c.WebRTC == nil {
		return
	}
	c.checkMedia(add)
	c.checkProxy(add)
	if c.WebRTC.TURN == nil {
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
	// TCP and TLS share the port space: an address is one or the other.
	stream := slices.Concat(t.ListenTCP, t.ListenTLS)
	for i, a := range stream {
		at := t.At.Field("listen_tcp").Index(i)
		if i >= len(t.ListenTCP) {
			at = t.At.Field("listen_tls").Index(i - len(t.ListenTCP))
		}
		if _, msg := checkAddress(a); msg != "" {
			add(at, "%s", msg)
		} else if _, port, _ := net.SplitHostPort(a); port != "0" && slices.Index(stream, a) < i {
			add(at, "%s is listed already in listen_tcp or listen_tls", a)
		}
	}
	switch {
	case len(t.ListenTLS) > 0 && (t.TLS == nil || len(t.TLS.Certificates) == 0):
		add(t.At.Field("tls"), "listen_tls needs certificates: set tls.certificates")
	case len(t.ListenTLS) == 0 && t.TLS != nil:
		add(t.At.Field("tls"), "tls is for listen_tls, which is empty")
	}
	if t.MaxStreamMessage != 0 && t.MaxStreamMessage < MinTURNStreamMessage {
		add(t.At.Field("max_stream_message"), "max_stream_message %d is below %d, too small for a datagram", t.MaxStreamMessage, MinTURNStreamMessage)
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
			if _, ok := ParsePeerRule(p); !ok {
				add(t.At.Field(f.name).Index(i), "%q is not an address, an address and port, or a prefix, as in \"10.1.0.0/16\"", p)
			}
		}
	}
}

func (c *Config) checkProxy(add func(Value, string, ...any)) {
	p := c.WebRTC.Proxy
	if p == nil {
		return
	}
	if _, msg := checkAddress(p.Listen); msg != "" {
		add(p.At.Field("listen"), "%s", msg)
	} else if m := c.WebRTC.Media; m != nil && m.Listen == p.Listen && !strings.HasSuffix(p.Listen, ":0") {
		add(p.At.Field("listen"), "webrtc.media listens on %s already", p.Listen)
	}
	checkPublicIPs(p.At.Field("public_ips"), p.PublicIPs, add)
	if p.RelayIP != "" {
		a, err := netip.ParseAddr(p.RelayIP)
		switch {
		case err != nil || a.Zone() != "":
			add(p.At.Field("relay_ip"), "%q is not an IP address", p.RelayIP)
		case a.IsUnspecified():
			add(p.At.Field("relay_ip"), "%q is a wildcard: leave relay_ip out for the route's pick", p.RelayIP)
		}
	}
	switch {
	case (p.PortMin == 0) != (p.PortMax == 0):
		add(p.At, "set port_min and port_max together")
	case p.PortMin > p.PortMax:
		add(p.At.Field("port_min"), "port_min %d is above port_max %d", p.PortMin, p.PortMax)
	}
}

// checkPublicIPs checks public_ips: addresses, none a wildcard.
func checkPublicIPs(at Value, ips []string, add func(Value, string, ...any)) {
	for i, p := range ips {
		a, err := netip.ParseAddr(p)
		switch {
		case err != nil || a.Zone() != "":
			add(at.Index(i), "%q is not an IP address", p)
		case a.IsUnspecified():
			add(at.Index(i), "%q is a wildcard", p)
		}
	}
}

func (c *Config) checkMedia(add func(Value, string, ...any)) {
	m := c.WebRTC.Media
	if m == nil {
		return
	}
	if _, msg := checkAddress(m.Listen); msg != "" {
		add(m.At.Field("listen"), "%s", msg)
	}
	checkPublicIPs(m.At.Field("public_ips"), m.PublicIPs, add)
	for i, s := range m.ICEServers {
		at := m.At.Field("ice_servers").Index(i)
		if len(s.URLs) == 0 {
			add(at.Field("urls"), "at least one URL is needed")
		}
		for j, u := range s.URLs {
			if !iceURL(u) {
				add(at.Field("urls").Index(j), "%q is not a stun:, stuns:, turn: or turns: URL", u)
			}
		}
		if !linkSafe(s.Username) || !linkSafe(s.Credential) {
			add(at, "the username and credential may not hold a double quote, a backslash or a control character")
		}
	}
	for i, u := range m.TURNURLs {
		if !iceURL(u) || !strings.HasPrefix(strings.ToLower(u), "turn") {
			add(m.At.Field("turn_urls").Index(i), "%q is not a turn: or turns: URL", u)
		}
	}
	if len(m.TURNURLs) > 0 && (c.WebRTC.TURN == nil || c.WebRTC.TURN.Secret == "") {
		add(m.At.Field("turn_urls"), "fit makes their credentials from webrtc.turn's secret: set webrtc.turn with a secret")
	}
	for i, name := range m.Codecs {
		if !slices.ContainsFunc(MediaCodecs, func(c string) bool { return strings.EqualFold(c, name) }) {
			add(m.At.Field("codecs").Index(i), "%q is not one of %s", name, strings.Join(MediaCodecs, ", "))
		}
	}
}

// iceURL reports whether u is a stun:, stuns:, turn: or turns: URL with a
// host, as RFC 7064 and RFC 7065 write them, which a Link header can carry.
func iceURL(u string) bool {
	scheme, rest, ok := strings.Cut(u, ":")
	switch strings.ToLower(scheme) {
	case "stun", "stuns", "turn", "turns":
	default:
		return false
	}
	return ok && rest != "" && !strings.HasPrefix(rest, "/") && !strings.ContainsAny(rest, "<> \t\r\n\"")
}

// linkSafe reports whether s can be a quoted parameter of a Link header.
func linkSafe(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r == '"' || r == '\\' || r < 0x20 || r == 0x7f })
}
