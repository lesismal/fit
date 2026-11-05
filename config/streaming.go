package config

import (
	"net"
	"slices"
	"strings"
	"time"
)

// The defaults of Streaming.
const (
	DefaultStreamMaxStreams    = 64
	DefaultStreamMaxPlayers    = 256
	DefaultStreamGOPCacheBytes = 8 << 20
	DefaultStreamMaxConns      = 1024

	DefaultRTMPHandshakeTimeout = 10 * time.Second
	DefaultRTMPIdleTimeout      = 30 * time.Second
	DefaultRTMPMaxMessageBytes  = 8 << 20

	DefaultRTSPSessionTimeout = 60 * time.Second
	DefaultRTSPIdleTimeout    = 60 * time.Second
	DefaultRTSPRealm          = "fit"

	DefaultSRTLatency         = 120 * time.Millisecond
	DefaultSRTPeerIdleTimeout = 5 * time.Second

	DefaultStreamWebRTCMaxSessions    = 256
	DefaultStreamWebRTCConnectTimeout = 15 * time.Second
	DefaultStreamWebRTCMaxOfferBytes  = 32 << 10
	// MaxStreamWebRTCOfferBytes is the most max_offer_bytes may be: a
	// larger body streams to its handler rather than arriving whole, which
	// fib's WHIP and WHEP server reads it as.
	MaxStreamWebRTCOfferBytes = 64 << 10
)

// RTSPAuthMethods are the schemes an RTSP listener may offer.
var RTSPAuthMethods = []string{"digest", "basic"}

// Streaming is fit's streaming role: fib's media hub, where every stream is
// published once and played over every protocol, with RTMP, RTSP and SRT
// listeners of its own and the WebRTC port of the stream_whip and
// stream_whep handlers; httpflv and hls play its streams on routes. See
// docs/decisions/030-media-auth-and-pages.md and docs/streaming.md.
type Streaming struct {
	MaxStreams    int `json:"max_streams,omitempty" min:"0" default:"64" doc:"The most streams at once, each a path with a publisher or players waiting for one; one more publisher is refused. 64 when left out or 0. A change of max_streams, max_players or gop_cache_bytes starts the hub again, ending every stream and connection."`
	MaxPlayers    int `json:"max_players,omitempty" min:"0" default:"256" doc:"The most players of one stream at once, over every protocol, HLS counting once a stream and format; one more is refused. Each packet a publisher sends is handed to every player on the worker that received it. 256 when left out or 0."`
	GOPCacheBytes int `json:"gop_cache_bytes,omitempty" min:"0" default:"8388608" doc:"The bytes each stream keeps of its latest group of pictures, from its last key frame on, so that a new player starts at once with a picture; a group that outgrows it is dropped and caching starts again at the next key frame. 8 MiB when left out or 0."`

	RTMP   *StreamRTMP   `json:"rtmp,omitempty" doc:"Take and play streams over RTMP, as rtmp://host/app/stream: OBS and ffmpeg publish, players play. None when left out."`
	RTSP   *StreamRTSP   `json:"rtsp,omitempty" doc:"Take and play streams over RTSP 1.0 with interleaved RTP (RTP/AVP/TCP), as rtsp://host/app/stream. None when left out."`
	SRT    *StreamSRT    `json:"srt,omitempty" doc:"Take and play streams over SRT, MPEG-TS on UDP, the stream named by the caller's stream ID. None when left out."`
	WebRTC *StreamWebRTC `json:"webrtc,omitempty" doc:"The UDP port the PeerConnections of the stream_whip and stream_whep handlers share, which publish into the hub with WHIP and play from it with WHEP. Routes with those handlers answer 503 without it. None when left out."`

	At Value `json:"-" fit:"self"`
}

// StreamRTMP is the streaming role's RTMP listener.
type StreamRTMP struct {
	Listen           string   `json:"listen" fit:"required" doc:"The TCP address RTMP clients connect to, as in \":1935\". A change of any field but auth starts the listener again, closing its connections."`
	MPTCP            bool     `json:"mptcp,omitempty" doc:"Listen the RTMP connections with Multipath TCP where the kernel offers it (Linux 5.6+, net.mptcp.enabled), falling back to plain TCP where it does not. Off by default."`
	Auth             string   `json:"auth,omitempty" doc:"The media_auth policy a publish and a play must satisfy, by the stream's path, app/stream: credentials go in the query of the stream name or the app, as rtmp://host/live/cam?token=T or ?user=U&pass=P. A refusal is answered with NetStream.Publish.Unauthorized or NetStream.Play.Failed and the connection closed. Everyone may publish and play when left out. A change applies to the next publish and play."`
	MaxConns         int      `json:"max_conns,omitempty" min:"0" default:"1024" doc:"The most connections at once; one more is closed as it opens. 1024 when left out or 0."`
	HandshakeTimeout Duration `json:"handshake_timeout,omitempty" default:"\"10s\"" doc:"How long a client may take from connecting to publishing or playing. 10s when left out."`
	IdleTimeout      Duration `json:"idle_timeout,omitempty" default:"\"30s\"" doc:"How long a publisher may send nothing, and a player's output not move, before its connection is closed. 30s when left out."`
	PlayWait         Duration `json:"play_wait,omitempty" doc:"How long a player may wait for a stream with no publisher yet, or for the next one after its publisher leaves. A play of a stream without a publisher fails at once when left out."`
	MaxMessageBytes  int      `json:"max_message_bytes,omitempty" min:"0" default:"8388608" doc:"The largest RTMP message a client may send, a video frame among them; a larger one closes its connection. 8 MiB when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// StreamRTSP is the streaming role's RTSP listener.
type StreamRTSP struct {
	Listen         string   `json:"listen" fit:"required" doc:"The TCP address RTSP clients connect to, as in \":8554\". RTP goes interleaved on the same connection (RTP/AVP/TCP); a SETUP asking for UDP is answered 461, which ffmpeg and most players then retry over TCP. A change of any field but auth starts the listener again, closing its connections."`
	MPTCP          bool     `json:"mptcp,omitempty" doc:"Listen the RTSP connections with Multipath TCP where the kernel offers it (Linux 5.6+, net.mptcp.enabled), falling back to plain TCP where it does not. Off by default."`
	Auth           string   `json:"auth,omitempty" doc:"The media_auth policy RTSP requests must satisfy, by the stream's path: ANNOUNCE publish and DESCRIBE play on the stream their URL names, RECORD publish and PLAY play on the stream of the connection's session (one whose URL names another stream is refused with 455), and a SETUP play on its stream, or once a session is set up, play or publish on the session's. Credentials are a user with a plaintext password by Digest, a user by Basic when auth_methods offers it, or a token in the URL's query, as rtsp://host/live/cam?token=T. A refusal is 401 with the challenges, or 403. Everyone may publish and play when left out. A change applies to the next request."`
	AuthMethods    []string `json:"auth_methods,omitempty" enum:"digest,basic" doc:"The schemes a 401 offers for users: digest, which needs a user's plaintext password, and basic, which sends the password in the clear and is the only one a password_hash user can use. [\"digest\"] when left out."`
	Realm          string   `json:"realm,omitempty" default:"\"fit\"" doc:"The realm of the challenges, which Digest's response is computed with. fit when left out."`
	MaxConns       int      `json:"max_conns,omitempty" min:"0" default:"1024" doc:"The most connections at once; one more is closed as it opens. 1024 when left out or 0."`
	SessionTimeout Duration `json:"session_timeout,omitempty" default:"\"60s\"" doc:"How long a session may go without a request, RTCP or, while recording, RTP before it and its connection end; clients are told so in the Session header and send keep-alives. 60s when left out."`
	IdleTimeout    Duration `json:"idle_timeout,omitempty" default:"\"60s\"" doc:"How long a connection without a session may go without a request before it is closed. 60s when left out."`
	PlayWait       Duration `json:"play_wait,omitempty" doc:"How long a player may outlive its stream's publisher, waiting for the next one. A player ends with its publisher when left out."`

	At Value `json:"-" fit:"self"`
}

// StreamSRT is the streaming role's SRT listener.
type StreamSRT struct {
	Listen          string   `json:"listen" fit:"required" doc:"The UDP address SRT callers reach, as in \":8890\". A change of any field but auth starts the listener again, ending its sessions."`
	Auth            string   `json:"auth,omitempty" doc:"The media_auth policy a caller must satisfy, publish for m=publish and play otherwise, by the stream ID's resource: a token as s= (#!::r=live/cam,m=publish,s=T), a user and password as u= and s=, or token, or user and pass, in the resource's query (live/cam?token=T). A refusal rejects the handshake with 1401 (unauthorized) or 1403 (forbidden). Everyone may publish and play when left out. A change applies to the next caller."`
	MaxConns        int      `json:"max_conns,omitempty" min:"0" default:"1024" doc:"The most sessions at once; one more caller is rejected with 1402. 1024 when left out or 0."`
	Latency         Duration `json:"latency,omitempty" default:"\"120ms\"" doc:"The receive latency asked for, the time a packet may take to be sent again when lost; the caller's wins when larger. 120ms when left out."`
	Passphrase      string   `json:"passphrase,omitempty" doc:"Encrypt every session with this passphrase, 10 to 79 characters, which callers must have too (pbkeylen 16). Unencrypted when left out."`
	PeerIdleTimeout Duration `json:"peer_idle_timeout,omitempty" default:"\"5s\"" doc:"How long a peer may send nothing, keep-alives included, before its session ends. 5s when left out."`
	PlayWait        Duration `json:"play_wait,omitempty" doc:"How long a player may wait for a stream with no publisher yet, or for the next one after its publisher leaves. A caller playing a stream without a publisher is rejected with 1404 when left out."`

	At Value `json:"-" fit:"self"`
}

// StreamWebRTC is the WebRTC port of the streaming role's stream_whip and
// stream_whep handlers.
type StreamWebRTC struct {
	Listen         string      `json:"listen" fit:"required" doc:"The UDP address every peer's media reaches fit at, one port all the role's PeerConnections share, as in \":8188\"; not webrtc.media's. A wildcard host advertises every interface's address that is up, unless public_ips is set. A change of any field starts it again, ending its sessions."`
	PublicIPs      []string    `json:"public_ips,omitempty" doc:"The addresses peers are told to reach the port at instead of the interfaces' own, as behind a 1:1 NAT."`
	ICEServers     []ICEServer `json:"ice_servers,omitempty" doc:"STUN and TURN servers the peers are told of in Link headers (RFC 9725), for their side of ICE; fit's is an ICE lite agent on its own address."`
	MaxSessions    int         `json:"max_sessions,omitempty" min:"0" default:"256" doc:"The most WHIP and WHEP sessions at once; one more offer is answered 503. 256 when left out or 0."`
	ConnectTimeout Duration    `json:"connect_timeout,omitempty" default:"\"15s\"" doc:"How long a session may take from its answer to a connected PeerConnection before it is ended; once connected, ICE consent bounds it. 15s when left out."`
	PlayWait       Duration    `json:"play_wait,omitempty" doc:"How long a WHEP offer may wait for a stream with no publisher yet, its request held, before it is answered 404. Answered 404 at once when left out."`
	MaxOfferBytes  int         `json:"max_offer_bytes,omitempty" min:"0" max:"65536" default:"32768" doc:"The largest offer taken; a larger one is answered 400. At most 64 KiB. 32 KiB when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Streams returns max_streams, or its default.
func (s *Streaming) Streams() int {
	return int(orDefault(int64(s.MaxStreams), DefaultStreamMaxStreams))
}

// Players returns max_players, or its default.
func (s *Streaming) Players() int {
	return int(orDefault(int64(s.MaxPlayers), DefaultStreamMaxPlayers))
}

// GOPCache returns gop_cache_bytes, or its default.
func (s *Streaming) GOPCache() int {
	return int(orDefault(int64(s.GOPCacheBytes), DefaultStreamGOPCacheBytes))
}

// Conns returns max_conns, or its default.
func (r *StreamRTMP) Conns() int { return int(orDefault(int64(r.MaxConns), DefaultStreamMaxConns)) }

// Handshake returns handshake_timeout, or its default.
func (r *StreamRTMP) Handshake() time.Duration {
	return durationOr(r.HandshakeTimeout, DefaultRTMPHandshakeTimeout)
}

// Idle returns idle_timeout, or its default.
func (r *StreamRTMP) Idle() time.Duration { return durationOr(r.IdleTimeout, DefaultRTMPIdleTimeout) }

// MessageBytes returns max_message_bytes, or its default.
func (r *StreamRTMP) MessageBytes() int {
	return int(orDefault(int64(r.MaxMessageBytes), DefaultRTMPMaxMessageBytes))
}

// Conns returns max_conns, or its default.
func (r *StreamRTSP) Conns() int { return int(orDefault(int64(r.MaxConns), DefaultStreamMaxConns)) }

// Session returns session_timeout, or its default.
func (r *StreamRTSP) Session() time.Duration {
	return durationOr(r.SessionTimeout, DefaultRTSPSessionTimeout)
}

// Idle returns idle_timeout, or its default.
func (r *StreamRTSP) Idle() time.Duration { return durationOr(r.IdleTimeout, DefaultRTSPIdleTimeout) }

// RealmName returns realm, or its default.
func (r *StreamRTSP) RealmName() string {
	if r.Realm == "" {
		return DefaultRTSPRealm
	}
	return r.Realm
}

// Methods returns auth_methods, or its default.
func (r *StreamRTSP) Methods() []string {
	if len(r.AuthMethods) == 0 {
		return []string{"digest"}
	}
	return r.AuthMethods
}

// Conns returns max_conns, or its default.
func (s *StreamSRT) Conns() int { return int(orDefault(int64(s.MaxConns), DefaultStreamMaxConns)) }

// LatencyOr returns latency, or its default.
func (s *StreamSRT) LatencyOr() time.Duration { return durationOr(s.Latency, DefaultSRTLatency) }

// PeerIdle returns peer_idle_timeout, or its default.
func (s *StreamSRT) PeerIdle() time.Duration {
	return durationOr(s.PeerIdleTimeout, DefaultSRTPeerIdleTimeout)
}

// Sessions returns max_sessions, or its default.
func (w *StreamWebRTC) Sessions() int {
	return int(orDefault(int64(w.MaxSessions), DefaultStreamWebRTCMaxSessions))
}

// Connect returns connect_timeout, or its default.
func (w *StreamWebRTC) Connect() time.Duration {
	return durationOr(w.ConnectTimeout, DefaultStreamWebRTCConnectTimeout)
}

// OfferBytes returns max_offer_bytes, or its default.
func (w *StreamWebRTC) OfferBytes() int {
	return int(orDefault(int64(w.MaxOfferBytes), DefaultStreamWebRTCMaxOfferBytes))
}

func (c *Config) checkStreaming(add func(Value, string, ...any)) {
	s := c.Streaming
	if s == nil {
		return
	}
	if s.RTMP == nil && s.RTSP == nil && s.SRT == nil && s.WebRTC == nil {
		add(s.At, "nothing takes streams in: set rtmp, rtsp, srt or webrtc")
	}
	policy := func(at Value, name string) {
		if name != "" && c.MediaAuthPolicy(name) == nil {
			add(at, "there is no media_auth policy named %q", name)
		}
	}
	// TCP and UDP ports of the role's own, which may not clash with each
	// other; listeners and other roles are checked when they bind.
	tcp, udp := map[string]string{}, map[string]string{}
	address := func(at Value, a, name string, ports map[string]string) {
		port, msg := checkAddress(a)
		if msg != "" {
			add(at, "%s", msg)
			return
		}
		if other, ok := ports[a]; ok && port != 0 {
			add(at, "%s listens on %s already", other, a)
			return
		}
		ports[a] = name
	}
	if r := s.RTMP; r != nil {
		address(r.At.Field("listen"), r.Listen, "rtmp", tcp)
		policy(r.At.Field("auth"), r.Auth)
	}
	if r := s.RTSP; r != nil {
		address(r.At.Field("listen"), r.Listen, "rtsp", tcp)
		policy(r.At.Field("auth"), r.Auth)
		for i, m := range r.AuthMethods {
			if slices.Index(r.AuthMethods, m) < i {
				add(r.At.Field("auth_methods").Index(i), "%q is listed twice", m)
			}
		}
		if strings.ContainsAny(r.Realm, "\"\\\x00\r\n") {
			add(r.At.Field("realm"), "the realm may not hold a double quote, a backslash or a control character")
		}
	}
	if r := s.SRT; r != nil {
		address(r.At.Field("listen"), r.Listen, "srt", udp)
		policy(r.At.Field("auth"), r.Auth)
		if n := len(r.Passphrase); n != 0 && (n < 10 || n > 79) {
			add(r.At.Field("passphrase"), "the passphrase is %d characters: SRT takes 10 to 79", n)
		}
	}
	if w := s.WebRTC; w != nil {
		address(w.At.Field("listen"), w.Listen, "webrtc", udp)
		checkPublicIPs(w.At.Field("public_ips"), w.PublicIPs, add)
		checkICEServers(w.At.Field("ice_servers"), w.ICEServers, add)
		if c.WebRTC != nil {
			for _, other := range []struct {
				name   string
				listen string
				on     bool
			}{
				{"webrtc.media", listenOf(c.WebRTC.Media), c.WebRTC.Media != nil},
				{"webrtc.proxy", listenOfProxy(c.WebRTC.Proxy), c.WebRTC.Proxy != nil},
			} {
				if other.on && samePort(other.listen, w.Listen) {
					add(w.At.Field("listen"), "%s listens on %s already: the role needs a UDP port of its own", other.name, w.Listen)
				}
			}
		}
	}
}

func listenOf(m *WebRTCMedia) string {
	if m == nil {
		return ""
	}
	return m.Listen
}

func listenOfProxy(p *WebRTCProxy) string {
	if p == nil {
		return ""
	}
	return p.Listen
}

// samePort reports whether two addresses bind the same port: the same port,
// not 0, on hosts of which one is a wildcard or which are the same.
func samePort(a, b string) bool {
	ha, pa, err1 := net.SplitHostPort(a)
	hb, pb, err2 := net.SplitHostPort(b)
	if err1 != nil || err2 != nil || pa != pb || pa == "0" {
		return false
	}
	wild := func(h string) bool { return h == "" || h == "0.0.0.0" || h == "::" }
	return ha == hb || wild(ha) || wild(hb)
}
