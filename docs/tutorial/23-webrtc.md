# WebRTC

[English](23-webrtc.md) | [简体中文](23-webrtc.zh-CN.md)

## What it does

fit serves WebRTC with fib's WebRTC packages, running on its engine beside the gateway. It does three things,
each one member of `webrtc`:

- **`turn`**: a TURN server (RFC 8656). Two WebRTC peers behind NATs that will not allow hole punching, or on
  networks that block UDP between them, connect through a relay: each asks for an *allocation*, and the server
  forwards datagrams between that address and the peers it permits. The credentials are the static `users`, or
  `secret`, coturn's shared secret for time-limited credentials; both may be set at once. Over UDP on
  `listen`, over TCP on `listen_tcp` and over TLS on `listen_tls`.
- **`media`**: fit itself is a small media server (an SFU): the `whip` and `whep` handlers serve the HTTP side
  on routes, and every PeerConnection shares one UDP port, where fit is an ICE lite agent.
- **`proxy`**: other media servers behind fit (another fit's `whip` and `whep`, or MediaMTX, Janus, LiveKit):
  the `webrtc_proxy` handler passes their signaling to an upstream of media servers, and `webrtc.proxy` relays
  each peer to the media server that answered it, found by the ICE ufrag of that peer's first STUN request.

`webrtc.turn` is fit's only TURN server; the planned P2P tunnels will use it as any other ICE agent does when
they cannot punch a hole. [Tunnel](24-tunnel.md) is a different thing: its roles forward services over links they
connect out on, not the UDP datagrams of standard ICE agents.

## Pages

### WebRTC

The WebRTC module in the menu has one page, which edits the `webrtc` member of the config: `turn`, `media` and
`proxy`. The page has the UI, `.conf`, `.yaml` and `.json` tabs, and edits the part of the shared draft that
belongs to it; **Save and apply** in the top bar saves the whole draft, **Check** takes the changes through the
whole apply path (loading, compiling, trying the new listener addresses) without applying them, and **Discard**
drops the draft. A problem of a save links back to this page.

The HTTP side is not on this page: `whip`, `whep` and `webrtc_proxy` are handlers on routes, set on the
[Reverse proxy](15-reverse-proxy.md) page, and they reference a [Media auth](21-media-auth.md) policy with their
`auth`. Routes with `whip` or `whep` handlers answer `503` without `webrtc.media`, and routes with
`webrtc_proxy` answer `503` without `webrtc.proxy`.

What can be done on the page:

- Write `turn`'s addresses (`listen`, `listen_tcp`, `listen_tls`), its `realm`, `relay_ip` and `public_ip`, the
  relay port range, and the credentials: `users`, each with a `password` or the `key` derived from it (what
  coturn's `turnadmin -k` prints), or `secret`.
- Write `turn`'s quotas and bandwidth: `max_allocations`, `max_allocations_per_user`, `max_permissions`,
  `max_channels`, `user_bandwidth`, the lifetimes, and `allowed_peers` with `denied_peers`.
- Write `media`'s port, `public_ips`, `ice_servers` and `turn_urls` (which the answers' `Link` headers tell to
  the peers), its `access`, and the limits and timeouts for PeerConnections, streams, viewers and bitrate.
- Write `proxy`'s port, `public_ips`, the session and relay limits, the idle timeout, and the source address
  `relay_ip` and the port range of the sockets toward the media servers.

On reload, `turn`'s `users`, `secret`, `access`, peer lists, quotas, bandwidth, lifetimes and certificates change
in place, with the allocations kept. A change of its addresses, `realm`, `relay_ip`, `public_ip`, port range,
`nonce_secret`, `mptcp`, `max_stream_message` or `stream_idle_timeout` starts a new server: beside the old one,
which drains its allocations for the listeners' drain timeout, or, when the two share an address, after the old
one has closed, which ends its allocations. A reload of `media` that keeps `listen` and `public_ips` keeps every
session, and the rest applies to the next session; changing them starts the media server again, ending the
sessions and telling each peer. A reload of `proxy` that keeps `listen` keeps its sessions and relays; changing
`listen` ends them.

The roles' metrics are `fit_turn_*`, `fit_webrtc_media_*` and `fit_webrtc_proxy_*`: allocations, sessions,
streams, relayed bytes, refusals by code and restarts.

### The handlers on routes

| Handler | Params | What it does |
| --- | --- | --- |
| `whip` | `stream`, `auth` | A `POST` of an `application/sdp` offer publishes a stream; the name is `stream`, or the path's last segment (`cam1` in `/whip/cam1` on a route of `/whip/*`) |
| `whep` | `stream`, `auth` | A `POST` of an offer plays a published stream, named as `whip`'s |
| `webrtc_proxy` | `upstream` (required), `media_addrs`, `timeout` (10s), `max_sdp_bytes` (32 KiB), `auth`, `action` | Sends each offer to a media server of `upstream` and answers with its answer, the candidates replaced by `webrtc.proxy`'s port; `action` (`publish` or `play`) is required with `auth` |

A `POST` is answered `201` with the answer and a `Location`, the session's URL (the path followed by the session's
32-hex-digit ID), which the route must match too; a `DELETE` of that URL ends the session without credentials,
the ID being the capability. `PATCH` (trickle ICE, ICE restarts) is answered `405`: an ICE restart is a new
session. Other refusals: `409` for a stream published already, `404` for a viewer of a stream not published,
`415` for a body that is not `application/sdp`, `422` for an offer with no codec fit forwards. `auth` checks a
bearer token or a user and password for `publish` or `play` on the stream's name: `401` without credentials,
`403` without the right.

## Key parameters

Only the key parameters are listed here; the full field tables are in
[the config reference: WebRTC](../reference/config.md#webrtc)
([`webrtc.turn`](../reference/config.md#config-turnserver),
[`webrtc.media`](../reference/config.md#config-webrtcmedia),
[`webrtc.proxy`](../reference/config.md#config-webrtcproxy)), and the handlers in
[Handlers](../reference/config.md#handlers).

### turn

| Field | Default | Description |
| --- | --- | --- |
| `listen` | — | Required. The UDP addresses clients reach the server at, as `:3478`, the port `turn:` URIs default to; the same address also answers STUN Binding requests without credentials, so it works as a `stun:` server |
| `listen_tcp`, `listen_tls` | none | TURN over TCP and TLS (`turn:host:port?transport=tcp`, `turns:`); TLS's certificates come from `tls.certificates` and are watched as a listener's are. A TLS address may not equal a TCP address; a UDP and a TCP address may, as `:3478` |
| `mptcp` | off | Carries `listen_tcp` and `listen_tls` over Multipath TCP where the kernel offers it (Linux 5.6+); `listen`'s UDP sockets have none |
| `realm` | — | Required. The realm of the credentials, sent in every challenge |
| `users` | none | The static users, each with a `password` or the `key` it derives; with `secret` set too, a name is looked up in `users` first |
| `secret` | none | coturn's `use-auth-secret`: a username `<expiry in Unix seconds>:<anything>` whose password is `base64(HMAC-SHA1(secret, username))`, accepted until the expiry |
| `relay_ip` | — | Required. The address the relayed sockets bind to, one of the host's, never a wildcard |
| `public_ip` | none | The address clients are told instead of `relay_ip`, behind a 1:1 NAT |
| `port_min`, `port_max` | none | The range of the relayed ports, for a firewall that opens a range; the kernel picks them when both are left out |
| `max_allocations` | 1024 | The allocations held at once; one more is refused with `508` |
| `max_allocations_per_user` | 16 | The allocations of one username; one more is refused with `486` |
| `max_permissions` | 64 | The peer addresses one allocation permits, channels' peers counted; one more is refused with `508` |
| `max_channels` | 64 | The channels one allocation binds; one more is refused with `508` |
| `user_bandwidth` | 2 MiB | The bytes relayed for one username each way per second, with a second's worth of burst; datagrams past it are dropped |
| `default_lifetime`, `max_lifetime` | 10m, 1h | An allocation's lifetime, refreshed by its client |
| `nonce_lifetime`, `nonce_secret` | 10m, a random one | How long a nonce is good for (438 past it), and the key nonces are made with — shared by the servers of one load balancer, since any of them may see a client's requests |
| `max_stream_message` | 8 KiB | The largest message a TCP or TLS client may send; a connection whose next message claims more is closed |
| `stream_idle_timeout` | 30s | How long a TCP or TLS connection without an allocation stays open |
| `allowed_peers`, `denied_peers` | none | Loopback, private, link-local, multicast and other internal ranges are refused by default; `allowed_peers` lets some of them in. Both take addresses with a port, as `10.0.0.5:8189`, which names that port alone |
| `access` | none | The client addresses the server serves, deciding before the global `access`: a refused connection is closed as soon as it is accepted, its datagrams dropped |

Each allocation holds a relayed socket on every event loop of the engine; these sockets are opened on the fib
server's own goroutines, never on a worker serving requests.

### media

| Field | Default | Description |
| --- | --- | --- |
| `listen` | — | Required. The UDP port every PeerConnection shares, as `:8189`; a wildcard host advertises the address of every interface that is up, unless `public_ips` is set |
| `public_ips` | none | The addresses peers are told to reach the port at (behind a 1:1 NAT, or a load balancer that forwards the port unchanged) |
| `access` | by the global `access` | The peer addresses the port serves, deciding before the global `access`; a refused address's datagrams are dropped, so its ICE checks go unanswered |
| `ice_servers` | none | The STUN and TURN servers peers are told of in `Link` headers; fit needs none itself, as it is an ICE lite agent on its own address |
| `turn_urls` | none | fit's own `webrtc.turn`, told to peers as `turn_urls`; then its `secret` must be set |
| `turn_credential_ttl` | 1h | How long the TURN credentials an answer carries are good for |
| `codecs` | opus, VP8, VP9, H264, AV1 | The codecs publishers may send; each publisher's offer is answered with the first of its codecs that is listed |
| `max_peer_connections` | 1024 | The PeerConnections at once, publishers' and viewers'; one more offer is answered `503` |
| `max_streams` | 64 | The streams published at once; one more publisher is answered `503` |
| `max_viewers` | 256 | The viewers of one stream; one more is answered `503` |
| `max_bitrate` | 8000000 | The bits a second a publisher may send, told to it as REMB and enforced, with two seconds of burst |
| `max_sdp_bytes` | 32 KiB | The size of an offer; a larger one is answered `413` |
| `connect_timeout` | 30s | From the answer to a connected PeerConnection |
| `ice_timeout` | 30s | How long a connected peer may be silent |
| `key_frame_interval` | 500ms | The least time between two key frame requests passed to a publisher |

Data channels, simulcast layers past the first, RTX and FEC, and transport-wide congestion control are not
forwarded. The sessions belong to `webrtc.media`, not to the routes.

### proxy

| Field | Default | Description |
| --- | --- | --- |
| `listen` | — | Required. The UDP port peers reach the media servers at, as `:8190`: the proxied answers name it as their only candidate |
| `public_ips` | none | The addresses peers are told to reach the port at |
| `max_sessions` | 4096 | The sessions at once, counted from the offer; one more is answered `503` |
| `max_relays` | 4096 | The peer addresses relayed at once, each with a UDP socket of its own; past it a new address is dropped |
| `session_timeout` | 30s | How long a session with nothing relayed for it is kept, then forgotten and its URL answered `404` |
| `idle_timeout` | 30s | How long a relayed peer address may send nothing before its relay is closed |
| `relay_ip` | none | The address the sockets toward the media servers leave from; of the media servers' family |
| `port_min`, `port_max` | none | The local ports of those sockets, for a firewall that admits a range; a port in use is skipped, and a peer address that finds the range full is dropped |
| `access` | none | The peer addresses the port relays, deciding before the global `access` |

A new peer address must first send a STUN Binding request whose USERNAME begins with one of the session's
ufrags (`<ufrag>:<the peer's ufrag>`), with MESSAGE-INTEGRITY signed by that session's ice-pwd; anything that
arrives before that is dropped. A session relays at most 8 of its peer's addresses and keeps at most 4 ufrags.

## In .conf

```fit
listener web :8080

webrtc {
	turn {
		listen :3478
		listen_tcp :3478
		realm example.com
		secret a-secret-shared-with-the-web-service
		users alice { password a-password }
		relay_ip 10.0.0.5
		public_ip 203.0.113.5
		port_min 49152
		port_max 49999
		max_allocations 1024
		max_permissions 64
		max_channels 64
		user_bandwidth 2097152
		allowed_peers 10.0.0.5:8189
	}

	media {
		listen :8189
		public_ips 203.0.113.5
		ice_servers { urls stun:stun.example.com:3478 }
		turn_urls turn:203.0.113.5:3478
		max_peer_connections 1024
		max_streams 16
		max_viewers 100
		max_bitrate 4000000
		connect_timeout 30s
		ice_timeout 30s
		key_frame_interval 500ms
	}
}

route whip { match { path /whip/* }; handler whip }
route whep { match { path /whep/* }; handler whep }
```

`listen_tls :5349` and its `tls.certificates` read the certificate files when the config is applied, so the
example leaves them out; the watched files serve the next handshake after a renewal, with nothing restarted. A
browser uses the server as an ICE server: `{urls: ["turn:turn.example.com:3478",
"turn:turn.example.com:3478?transport=tcp", "turns:turn.example.com:5349?transport=tcp"], username,
credential}`, with the time-limited credentials made by the web service that knows the `secret`. With
`turn_urls`, `media` hands them out itself: each WHIP or WHEP answer's `Link` headers carry those URLs with
credentials good for `turn_credential_ttl`. `allowed_peers 10.0.0.5:8189` lets the relay reach `media`'s own port
on `relay_ip`, which the default denial of private addresses would refuse.

Media servers behind fit, two hosts of an upstream, each peer relayed through fit's UDP port `:8190`:

```fit
listener web :8080

upstream {
	name media-servers
	addresses 10.0.0.2:8080 10.0.0.3:8080
	health_check { path /healthz }
}

route publish {
	match { path /whip/* }
	handler webrtc_proxy {
		upstream media-servers
		media_addrs { 10.0.0.3:8080 10.0.0.3:8189 }
	}
}

route play {
	match { path /whep/* }
	handler webrtc_proxy { upstream media-servers }
}

webrtc {
	proxy {
		listen :8190
		public_ips 203.0.113.5
		relay_ip 10.0.0.1
		port_min 40000
		port_max 40999
	}
}
```

`media_addrs` names where a media server's peers are relayed to, by its address in the upstream; an IP alone takes
the port of its answer's candidate. Without it, fit relays to the answer's first host candidate, or its
server-reflexive one, IPv4 first.

## See also

- [WebRTC](../webrtc.md): [the TURN server](../webrtc.md#the-turn-server) and its
  [credentials](../webrtc.md#credentials), [WHIP and WHEP](../webrtc.md#whip-and-whep) and its
  [auth](../webrtc.md#auth), and [proxying media servers](../webrtc.md#proxying-media-servers).
- [the config reference: WebRTC](../reference/config.md#webrtc): every field;
  [`webrtc_proxy`](../reference/config.md#http-handlers-webrtc_proxy),
  [`whip`](../reference/config.md#http-handlers-whip),
  [`whep`](../reference/config.md#http-handlers-whep).
- [Media auth](21-media-auth.md): the policy `auth` names, and the key ring.
- [Streaming](20-streaming.md): the other port `stream_whip` and `stream_whep` use (`streaming.webrtc`).
- [Reverse proxy](15-reverse-proxy.md): the routes these handlers sit on.
- [decision 029](../decisions/029-webrtc.md): the design of the WebRTC roles.
