# WebRTC

[English](webrtc.md) | [简体中文](webrtc.zh-CN.md)

Config examples, in .conf, in [fit-examples](https://github.com/lesismal/fit-examples): [webrtc-turn](https://github.com/lesismal/fit-examples/blob/main/webrtc-turn/fit.conf), [whip-whep](https://github.com/lesismal/fit-examples/blob/main/whip-whep/fit.conf), [webrtc-proxy](https://github.com/lesismal/fit-examples/blob/main/webrtc-proxy/fit.conf).

fit serves WebRTC with fib's WebRTC packages, beside the gateway on its engine
([decision 029](decisions/029-webrtc.md)): a TURN server, and WHIP and WHEP with fit as the media
server; and fit in front of other media servers, proxying their WHIP and WHEP and relaying their
media through one UDP port.

## The TURN server

Two WebRTC peers behind NATs that will not let a hole be punched, or on a network that blocks UDP
between them, reach each other through a TURN relay (RFC 8656): each asks the server for a relayed
address, an *allocation*, and the server passes datagrams between that address and the peers its
client has given permission to. `webrtc.turn` runs fib's TURN server as a role of fit's config, on
the listeners' engine, beside the gateway's routes or without any:

```fit
webrtc {
	turn {
		listen :3478
		listen_tcp :3478
		listen_tls :5349
		tls {
			certificates {
				cert_file /etc/fit/turn.example.com.pem
				key_file /etc/fit/turn.example.com.key
			}
		}
		realm example.com
		secret a-secret-shared-with-the-web-service
		users alice { password a-password }
		relay_ip 10.0.0.5
		public_ip 203.0.113.5
		port_min 49152
		port_max 49999
	}
}
```

The same in JSON:

```json
{
  "webrtc": {
    "turn": {
      "listen": [":3478"],
      "listen_tcp": [":3478"],
      "listen_tls": [":5349"],
      "tls": {"certificates": [{"cert_file": "/etc/fit/turn.example.com.pem", "key_file": "/etc/fit/turn.example.com.key"}]},
      "realm": "example.com",
      "secret": "a-secret-shared-with-the-web-service",
      "users": [{"name": "alice", "password": "a-password"}],
      "relay_ip": "10.0.0.5",
      "public_ip": "203.0.113.5",
      "port_min": 49152,
      "port_max": 49999
    }
  }
}
```

A browser is given it as an ICE server, `{urls: ["turn:turn.example.com:3478",
"turn:turn.example.com:3478?transport=tcp", "turns:turn.example.com:5349?transport=tcp"], username,
credential}`. The server also answers STUN Binding requests on its UDP port, without credentials,
so the same address serves as a `stun:` server. Every field is in the
[config reference](reference/config.md#webrtc).

### Credentials

- **`users`**: fixed names, each with a `password`, or the `key` it derives, as coturn's
  `turnadmin -k` prints it (MD5 of `name:realm:password` in hex), so that the password itself is
  not kept. With `admin.store`, the config, and so the users, are kept encrypted.
- **`secret`**: the time-limited credentials of coturn's `use-auth-secret`, which most WebRTC
  services hand out. The web service that knows the secret makes, for each session, the username
  `<expiry in Unix seconds>:<anything>` and the password `base64(HMAC-SHA1(secret, username))`;
  the server takes them until the expiry and needs no list of users. In Go, fib's
  `turn.EphemeralCredentials(secret, user, expiry)` makes them; in a shell:

  ```sh
  user="$(($(date +%s) + 3600)):alice"
  pass=$(printf %s "$user" | openssl dgst -sha1 -hmac "$secret" -binary | base64)
  ```

Both may be set; a name in `users` is looked up first. Passwords are taken as bytes: one that is
not ASCII must be given already in the form RFC 8265's OpaqueString makes.

### TCP and TLS

Networks that block UDP, and those that let only TLS on port 443 through, are why WebRTC services
run TURN at all. `listen_tcp` serves TURN over TCP (`turn:host:port?transport=tcp`) and
`listen_tls` over TLS (`turns:host:port?transport=tcp`, often on 5349 or 443), with the
certificates of `tls.certificates`, from files, which are watched as a listener's are: a renewed
file is the next handshake's, with nothing restarted. The relay to peers is UDP whatever the
client's transport. A connection is one client: its allocation ends when it closes, a connection
with no allocation is closed after `stream_idle_timeout`, and one whose next message claims more
than `max_stream_message` is closed at once. A TCP and a TLS address may not be the same; a UDP
and a TCP one may, as `:3478` above.

### Addresses and firewalls

Each allocation's relayed socket binds to `relay_ip`, which must be one of the host's addresses.
Behind a 1:1 NAT, as on most clouds, set `public_ip` to the address that maps to it: clients are
told that one. `port_min` and `port_max` keep the relayed ports in a range, for a firewall that
opens one; the kernel picks them when both are left out. Each allocation holds a socket on every
event loop of fit's engine (`engine.event_loops`). The sockets are opened on goroutines of fib's
server, never on a worker that serves requests.

### Peers fit will not relay to

A relay that sends wherever a client asks is a way into the network it runs in. Permissions and
channels for peers on loopback, private (RFC 1918 and unique local), link-local, multicast, shared
(RFC 6598), and other internal or reserved ranges are refused, as fib's `DeniedByDefault` lists
them. `allowed_peers` lets some of them through, for peers on a private network, and
`denied_peers` refuses more; either takes an address with a port, as `10.0.0.9:8189`, for that
port alone.

The server's own addresses, `relay_ip` and `public_ip`, are a case of their own. Two clients of the
same server relay to each other's relayed addresses, which are on them, so the server relays to
its own addresses on the ports of the relayed addresses it holds, whether or not `allowed_peers`
covers them, and on a port `allowed_peers` names with them, as `10.0.0.5:8189` for
`webrtc.media` on the same address. It never relays to their other ports, fit's own listeners
among them, whatever the prefixes in `allowed_peers` say. The check sees each datagram's port: a
permission is for an address alone, and the datagrams it lets through are checked port by port.

### Limits

| Field | Default | Bounds |
| --- | --- | --- |
| `max_allocations` | 1024 | allocations held; one more is refused with 508 |
| `max_allocations_per_user` | 16 | allocations of one username; one more is refused with 486 |
| `max_permissions` | 64 | peer addresses one allocation permits, a channel's peer counting; one more is refused with 508 |
| `max_channels` | 64 | channels one allocation binds; one more is refused with 508 |
| `user_bandwidth` | 2 MiB | bytes a second relayed for one username each way, with a second's burst; past it, datagrams are dropped |
| `default_lifetime`, `max_lifetime` | 10m, 1h | an allocation's lifetime, which its client refreshes |
| `nonce_lifetime` | 10m | how long a nonce is good for before a 438 |
| `max_stream_message` | 8 KiB | a TCP or TLS client's largest message, and what its connection buffers |
| `stream_idle_timeout` | 30s | a TCP or TLS connection without an allocation |

An allocation's permissions last five minutes unless refreshed, and its channels ten. fit's
`limits.max_conns` counts the TCP and TLS connections with every other connection the listeners
accept; the clients' and peers' UDP addresses are not counted there, and are bounded by the
allocations and their permissions above.

### Reloading

A reload changes `users`, `secret`, `allowed_peers`, `denied_peers`, `user_bandwidth`, the quotas
(`max_allocations`, `max_allocations_per_user`, `max_permissions`, `max_channels`), the lifetimes
and the certificates in place, from the next request or handshake on: the allocations held go on,
and one held past a lowered quota is kept until it ends.

A change of `listen`, `listen_tcp` or `listen_tls` starts a new server on the new addresses, and
the old one drains: it takes no new allocation (508) while those it holds go on relaying and
refreshing, until they end or fit's drain timeout (`fit run -shutdown-timeout`, 30s) is up, when it
closes. A change of `realm`, `relay_ip`, `public_ip`, the port range, `nonce_secret` or the stream
bounds starts a new server too: beside the old one, draining, when the addresses change with it,
and in its place, its allocations ended, when they do not, since two servers cannot share an
address; browsers then restart ICE. Leaving `webrtc.turn` out, or stopping fit, closes it at once.

### Metrics and logs

The Admin API's `/metrics`, and OpenTelemetry's export, carry:

- gauges: `fit_turn_allocations`, `fit_turn_pending_allocations` (waiting for their relayed
  socket), `fit_turn_permissions`, `fit_turn_channels` and `fit_turn_draining_servers`;
- `fit_turn_relayed_bytes_total{direction="to_peer"|"from_peer"}`;
- `fit_turn_allocated_total`, `fit_turn_refreshed_total` and
  `fit_turn_freed_total{reason="deleted"|"expired"|"closed"}`;
- `fit_turn_refused_total{code}`, requests refused after their credentials passed, by error code
  (403 a peer refused, 486 and 508 the quotas, caps and drains; 400, 420, 437, 441, 442 and 443,
  and `other`, malformed or misplaced requests), and `fit_turn_peers_refused_total`, the 403s;
  `fit_turn_auth_failures_total`, credentials that failed;
- `fit_turn_dropped_total{reason="bandwidth"|"unsent"}`, datagrams past `user_bandwidth` and
  those with no connection to send them on, and `fit_turn_unrelayed_total`, every datagram the
  server did nothing with: malformed, without an allocation, a permission or a channel, to a port
  the peer rules refuse, or past `user_bandwidth`, which the first counts too;
- `fit_turn_restarts_total`, the new servers reloads started.

The server's messages go to the `webrtc` logger, after `webrtc.turn: `: the addresses it listens
on and relays from at `info`, a restart that ends its allocations at `warn`, and each allocation
made, refreshed and freed, each refusal and each failed credential, at `debug`.

### TURN, P2P and the tunnel

`webrtc.turn` is the one TURN server fit has: the TURN server of
[decision 025](decisions/025-p2p.md) is this one. The planned P2P tunnels
([P2P, NAT traversal](dev-plan.md#p2p-nat-traversal)), whose STUN client, TURN client and ICE agent
are fib's `webrtc/stun`, `webrtc/turn` and `webrtc/ice`, will use it as their relay where no hole
can be punched between two hosts behind NAT, as any other ICE agent does. The [tunnel](tunnel.md)
is another thing: its roles forward TCP services through a `tunnel.hub` that nodes and proxies
link out to, every byte relayed over fit's own links; a TURN server relays the UDP datagrams of
standard ICE agents, browsers among them, only where they find no direct path, and knows nothing of
the services behind them.

## WHIP and WHEP

fit is a small media server, an SFU: a publisher sends a stream with WHIP (RFC 9725), as OBS, a
browser or GStreamer do, and viewers play it with WHEP (draft-ietf-wish-whep); fit passes each RTP
packet the publisher sends on to every viewer, without decoding it. `webrtc.media` is the UDP port
every PeerConnection shares, and the handler modules `whip` and `whep` serve the HTTP side on any
route:

```fit
route {
	match { path /whip/* }
	middleware jwt {
		jwks_url https://auth.example.com/.well-known/jwks.json
		audience whip
	}
	handler whip
}

route {
	match { path /whep/* }
	handler whep
}

webrtc {
	media {
		listen :8189
		public_ips 203.0.113.5
		ice_servers { urls stun:stun.example.com:3478 }
		max_streams 16
		max_viewers 100
		max_bitrate 4000000
	}
}
```

The same in JSON:

```json
{
  "routes": [
    {"match": {"path": ["/whip/*"]},
     "middleware": [{"module": "jwt", "params": {"jwks_url": "https://auth.example.com/.well-known/jwks.json", "audience": ["whip"]}}],
     "handler": {"module": "whip"}},
    {"match": {"path": ["/whep/*"]}, "handler": {"module": "whep"}}
  ],
  "webrtc": {
    "media": {
      "listen": ":8189",
      "public_ips": ["203.0.113.5"],
      "ice_servers": [{"urls": ["stun:stun.example.com:3478"]}],
      "max_streams": 16,
      "max_viewers": 100,
      "max_bitrate": 4000000
    }
  }
}
```

A publisher posts its offer to `https://example.com/whip/cam1`, and viewers theirs to
`https://example.com/whep/cam1`: the stream's name is the path's last segment, or the handler's
`stream` param, which fixes it; a name is 1 to 128 letters, digits, `.`, `_`, `~` and `-`, and a
path whose last segment is not one is answered `404`. A session's URL is the path of its POST
followed by the session's ID, so the route must match it too: `/whip/*` does, and a route of
`/live` alone needs `/live/*` beside it. Every field is in the [config reference](reference/config.md#webrtc).

### The HTTP API

| Request | Answer |
| --- | --- |
| `POST` an `application/sdp` offer to the endpoint | `201 Created`, fit's answer as `application/sdp`, `Location` the session's URL (the endpoint's path and the session's ID), and a `Link` header for each ICE server |
| `DELETE` the session's URL | `200`; a publisher's stream ends, and its viewers' sessions with it; `404` for a session that has ended |
| `OPTIONS` the endpoint | `204`, with `Allow: POST, OPTIONS` and `Accept-Post: application/sdp` |
| `OPTIONS` the session's URL | `204`, with `Allow: DELETE, OPTIONS` |
| `PATCH` the session's URL | `405`: see below |

Refusals: `415` for an offer that is not `application/sdp`; `413` past `max_sdp_bytes`; `400` for
one that does not parse or cannot be answered; `422` for one with no audio or video in a codec fit
forwards, or a viewer's that receives none of the stream's tracks; `409` for a stream published
already; `404` for a viewer of a stream not published, or one that ended while it was answered;
`503` with `Retry-After: 5` past `max_peer_connections`, `max_streams` or `max_viewers`, and `503`
while `webrtc.media` does not run. The endpoint answers other methods `405`.

Auth is the handlers' `auth`, a [`media_auth`](reference/config.md#media-auth) policy, or the
route's middleware, or both; see [Auth](#auth) below.

fit answers without trickle ICE, and PATCH is not supported: fit is an ICE lite agent (RFC 8445
section 2.5) on the Mux, whose candidates are the port's own addresses, all in the answer, and it
learns a peer's address from the peer's checks, so the candidates a peer would trickle add nothing.
An ICE restart is a new session: a client DELETEs the old one and posts a new offer. Clients
answered 405 do not trickle, as RFC 9725 says.

### Auth

`auth` names a policy of the config's `media_auth`, which says who may publish and play which
streams; the handler checks a POST against it before taking the offer's body:

```fit
media_auth live {
	secret a-secret-of-at-least-16-bytes
	public play
	tokens studio {
		token_sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
		publish live/*
	}
	users alice { password alice-password; publish alice-*; play * }
}

route {
	match { path /whip/* }
	handler whip { auth live }
}

route {
	match { path /whep/* }
	handler whep { auth live }
}
```

- **Credentials.** A static token from `tokens`, by its value or its `token_sha256`; a token fit
  signs with the policy's `secret`, which `fit media-token -policy live -publish 'live/*' -ttl 1h`
  prints, carrying its subject, expiry and streams; or a user of `users` and their password. A
  token is sent as RFC 9725's `Authorization: Bearer`, as a `token` query parameter (for clients
  that cannot set a header; the access log writes the URI with `token=REDACTED`), or as the
  `fit_media_token` cookie; a user as `Authorization: Basic`.
- **Rights.** Each token and user lists the stream patterns it may `publish` and `play`: `*`
  matches any run of characters, so `live/*`, `room1-*` and `*` work, and an empty list grants
  nothing. `public: ["play"]` lets anyone watch while publishing needs credentials.
- **Answers.** `401` with `WWW-Authenticate: Bearer realm="fit"` without credentials, and with
  `error="invalid_token"` for an unknown, expired or tampered token or a wrong password; `403`
  for credentials that do not allow the action on the stream; `503` with `Retry-After: 1` when
  bcrypt checks are queued past their bounds.
- **A session's URL.** Its DELETE and OPTIONS need no credentials: the session's ID, 128 random
  bits, is known only to whoever made the session and is the capability RFC 9725 relies on, and a
  token that expires during a long stream must not keep its publisher from hanging up.
- **Never on a worker.** Tokens are looked up by their SHA-256 or their signature checked, and
  plaintext passwords compared in constant time, on the worker; a `password_hash` is checked with
  bcrypt on a small pool of its own, a quarter of the CPUs with 256 waiting at most, while the
  request waits, and a password found right is remembered as a keyed SHA-256, so the next check
  is at once.
- **Reloading.** A reload changes a policy in place, for the next offer; sessions already made
  are kept. A handler whose `auth` names a policy the config lacks fails the config.

The route's middleware still run first, so a policy can sit behind `jwt`, `forward_auth` or an IP
allow list. Every field is in the [config reference](reference/config.md#media-auth), and the
design in [decision 030](decisions/030-media-auth-and-pages.md).

### Media

Each publisher's offer is answered with one codec of each kind, the first of its own that
`codecs` lists (opus, VP8, VP9, H264 and AV1 when left out), and a viewer is answered with the
same, which its offer must accept. A viewer gets the stream's audio and video on the sections of
its offer, from one TrackLocal for each of the publisher's: its packets are protected again under
the viewer's own SSRC and keys, their sequence numbers and timestamps as the publisher sent them.

- **Key frames.** A viewer that joins, or loses packets, asks for a key frame (PLI or FIR); fit
  asks the publisher with a PLI, at most once a `key_frame_interval` (500ms) whatever the number of
  viewers. It asks on its own too when a viewer connects and when a publisher's video first
  arrives, within the same interval.
- **Bitrate.** fit tells each publisher `max_bitrate` (8 Mbit/s) once a second with REMB, which
  browsers keep under, and drops its packets past it, with two seconds of burst.
- **Leaving.** A publisher's DELETE, its PeerConnection failing, or its peer silent for
  `ice_timeout` (30s) ends the stream and its viewers' sessions; a viewer's ends only its own. A
  session not connected within `connect_timeout` (30s) of its answer ends too. A viewer cut or
  slow does not hold the publisher or the other viewers: fit sends UDP and never waits for it.
- **Not forwarded.** Data channels, simulcast layers past the first (a simulcast publisher's first
  layer to arrive is forwarded), RTX and FEC, and transport-wide congestion control, which fib's
  PeerConnection does not do. Each viewer is sent the publisher's packets as they come: there is no
  per-viewer bandwidth estimate or layer selection. A publisher's first 8 audio and video sections
  are forwarded, and it may send 16 RTP streams at most, simulcast layers counted.

### ICE servers

fit needs no STUN or TURN server of its own: its port is reached at its addresses, or at
`public_ips` behind a 1:1 NAT. A peer behind a firewall that blocks UDP to it needs a TURN server
on its side; the answer's `Link` headers tell it of `ice_servers` and, as `turn_urls`, of fit's own
`webrtc.turn`, with credentials made from its `secret`, which must then be set, for
`turn_credential_ttl` (1h):

```
Link: <stun:stun.example.com:3478>; rel="ice-server"
Link: <turn:turn.example.com:3478>; rel="ice-server"; username="1767225600:fit-5f1c..."; credential="..."; credential-type="password"
```

fit's TURN server relays to the media port as to any peer: when the port is on its own `relay_ip`,
`allowed_peers` lists it with its port, as `10.0.0.5:8189`.

### Limits

| Field | Default | Bounds |
| --- | --- | --- |
| `max_peer_connections` | 1024 | PeerConnections, publishers' and viewers'; one more offer is answered 503 |
| `max_streams` | 64 | streams published; one more publisher is answered 503 |
| `max_viewers` | 256 | viewers of one stream; one more is answered 503 |
| `max_bitrate` | 8000000 | bits a second a publisher may send, told with REMB and enforced |
| `max_sdp_bytes` | 32 KiB | an offer's size; a larger one is answered 413 |
| `connect_timeout` | 30s | from the answer to a connected PeerConnection |
| `ice_timeout` | 30s | how long a connected peer may be silent |
| `key_frame_interval` | 500ms | between two key frame requests passed to a publisher |

A publisher's packet is sent to each of its viewers on the fib worker it arrived on, so
`max_viewers` bounds that work too. fit's `limits.max_conns` does not count the peers' UDP
addresses: `max_peer_connections` bounds them.

### Reloading

Sessions belong to `webrtc.media`, not to the routes: a reload that keeps its `listen` and
`public_ips` keeps every session, and its other fields apply to the next offer (`max_bitrate` and
`key_frame_interval` at once). A change of `listen` or `public_ips` starts the media server again:
its sessions end, each peer told with a DTLS close, and publishers and viewers post new offers.
Leaving `webrtc.media` out ends them all, and the routes answer 503.

### Metrics and logs

The Admin API's `/metrics`, and OpenTelemetry's export, carry
`fit_webrtc_media_peer_connections{role="publisher"|"viewer"}`, `fit_webrtc_media_streams`,
`fit_webrtc_media_sessions_total{role}`,
`fit_webrtc_media_sessions_ended_total{reason="delete"|"publisher_left"|"failed"|"connect_timeout"|"reload"}`,
`fit_webrtc_media_refused_total{reason="peer_connections"|"streams"|"viewers"|"conflict"|"no_stream"|"offer"}`,
`fit_webrtc_media_received_packets_total`, `fit_webrtc_media_received_bytes_total`,
`fit_webrtc_media_dropped_total{reason="bitrate"}`,
`fit_webrtc_media_key_frame_requests_total{result="sent"|"held"}` and
`fit_webrtc_media_restarts_total`. The media server's messages go to the `webrtc` logger, after
`webrtc.media: `: its addresses at `info`, and a restart that ends its sessions at `warn`.

## The media pages

fit ships pages for its media, embedded in the binary: the handler `media_pages` serves, under its
`prefix`, an index with a login, a room call, a publish page and a play page, over the WHIP and
WHEP endpoints of the section above (or of the streaming role, on this server or another, by
`whip` and `whep`). The [media-pages](https://github.com/lesismal/fit-examples/blob/main/media-pages/fit.conf) example wires it all:

```fit
media_auth media {
	secret a-long-random-secret-for-the-logins
	public play
	users alice { password_hash $2a$10$Fq1ONPcWafh7L8Rqw/wBlOX87AVAV7hZrin/rWwd7vUZv6MlW.v.G; publish *; play * }
	users guest { password guest-password; publish lobby-*; play lobby-* }
}

route pages {
	match { path /media /media/* }
	handler media_pages { auth media; login true; title "fit media" }
}

route publish { match { path /whip/* }; handler whip { auth media } }
route play { match { path /whep/* }; handler whep { auth media } }

webrtc { media { listen :8189 } }
```

Browsers open the camera and the microphone only on a secure page: over HTTPS, or on
`localhost`.

- **Pages.** `/media/` is the index: the endpoints, whether credentials are needed and what the
  logged-in user may do. `/media/call` is a room call: each member publishes `<room>-<name>` with
  WHIP and plays every other member with WHEP. `/media/publish` sends a camera, a microphone or a
  screen to a stream and shows its URLs, WHIP, WHEP, `hls` and the `links` given for tools outside
  the browser (`{stream}`, `{token}` and `{host}` filled in); `/media/play` plays a stream with
  WHEP, or with HLS where the browser plays it itself. The pages load no script, style or font
  from elsewhere, and are answered with a Content Security Policy that lets them reach this server
  and the endpoints' origins alone.
- **Login.** With `auth` and `login`, the pages log the policy's users in: `POST
  /media/api/login` checks the password (a `password_hash` on mediaauth's bcrypt pool) and answers
  with a token the policy signs for its `token_ttl`, set as the cookie `fit_media_token`
  (`HttpOnly`, `SameSite=Strict`, `Secure` over TLS, `Path=/`), which `whip`, `whep` and the room
  API take, and also handed to the page, which sends it as `Authorization: Bearer` to endpoints on
  another server. A token pasted in, as `fit media-token` prints, works the same. Without a
  `secret` the tokens are signed with a key of the process, which a restart forgets. The pages'
  HTML can sit behind the route's own middleware too, as `basic_auth`, `forward_auth` or an IP
  allow list; a `rate_limit` on the route bounds login attempts.
- **Switches.** The microphone, the camera and the screen switch on and off at any time, with or
  without the device. A publisher always offers one audio and one video transceiver; a switch only
  replaces the track its sender sends, so it never renegotiates nor restarts the PeerConnection.
  Switched on, a sender sends the device, or without one a synthetic track: silence, or a frame
  with the member's name at 2 frames a second. Switched off, it sends nothing and the device is
  released, its light off.
- **The room API.** Each member announces through the handler's room API whether it publishes,
  its switches and whether it has the devices: `POST /media/api/rooms/<room>/join`, `POST
  .../members/<name>` (a change, and every `member_ttl`/3 a heartbeat), `POST .../leave`, and `GET
  .../events`, Server-Sent Events with the room's members on every change. The others show the
  member's avatar while its camera is off, "microphone off", "no microphone" or "no camera", and
  play a member's stream only once it is announced as published, so that nobody asks for a stream
  that is not there; a viewer whose request fails anyway tries again at most 6 times, waiting 1, 2,
  4, 8 and 15 seconds. A join needs `play` on `<room>-*`, announcing that one publishes `publish`
  on `<room>-<name>`; a member's changes need the key its join was answered with.
- **Bounds.** `max_rooms` rooms (64), `max_members` members a room (16) and twice as many event
  streams, `max_message_bytes` a request's body (1024), `member_ttl` without a heartbeat (30s),
  and `stall_timeout` (30s) for a browser to take its events: a browser that does not keep up is
  sent only the room's latest state once it has taken the last, and is cut off past the timeout.
  Rooms belong to the handler's prefix on the engine and survive reloads; an event stream of a
  config swapped out ends after the drain timeout, and the browser follows the room again.
- **Silence.** The media server keeps a publisher whose switches are all off, and its viewers:
  ICE consent keeps a session alive, not media, so a publisher may send nothing for as long as it
  likes. A publisher of audio alone, or of video alone, is played by a viewer that offers both
  kinds, its other section answered inactive; a viewer that asks for a kind the publisher does not
  send, and nothing else, is answered `422`.

## Proxying media servers

Many media servers can sit behind one fit: fit's own `whip` and `whep` on other hosts, or any WHIP
or WHEP server, such as MediaMTX, Janus or LiveKit's WHIP ingress. Publishers and viewers know one
HTTP endpoint and one UDP port, fit's. The handler `webrtc_proxy` passes their signaling to an
upstream of media servers, and `webrtc.proxy` relays each peer's media to the media server that
answered it:

```fit
upstream {
	name media-servers
	addresses 10.0.0.2:8080 10.0.0.3:8080
	health_check {
		path /healthz
	}
}

route {
	match { path /whip/* /whep/* }
	handler webrtc_proxy {
		upstream media-servers
		media_addrs {
			10.0.0.3:8080 10.0.0.3:8189
		}
	}
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

The same in JSON:

```json
{
  "upstreams": [{"name": "media-servers", "addresses": ["10.0.0.2:8080", "10.0.0.3:8080"],
                 "health_check": {"path": "/healthz"}}],
  "routes": [
    {"match": {"path": ["/whip/*", "/whep/*"]},
     "handler": {"module": "webrtc_proxy",
                 "params": {"upstream": "media-servers", "media_addrs": {"10.0.0.3:8080": "10.0.0.3:8189"}}}}
  ],
  "webrtc": {"proxy": {"listen": ":8190", "public_ips": ["203.0.113.5"],
                       "relay_ip": "10.0.0.1", "port_min": 40000, "port_max": 40999}}
}
```

### How a session goes

1. A publisher posts its offer to fit. fit takes it whole, up to `max_sdp_bytes`, removes its
   candidates, and sends it to the media server the upstream picks: its load balancing and its
   health checks apply, and the request's own fields, `Authorization` among them, are passed on,
   with `X-Forwarded-For` set to the client's address.
2. The media server answers `201`, with a `Location`. fit reads its `a=ice-ufrag` and
   `a=ice-pwd`, and the address its peers are to be relayed to: `media_addrs`' entry for the media
   server, an IP and a port, or an IP alone, which takes the port of the answer's candidate; or
   else the answer's first host candidate, or else server-reflexive one, IPv4 first and
   link-local ones skipped. It replaces the answer's
   candidates, and its `c=` lines, with `webrtc.proxy`'s addresses, keeps the session under an ID
   of its own, and answers with its `Location` a URL of fit's: the POST's path followed by that ID.
3. The publisher's ICE checks reach fit's port. A new peer address must start with a STUN Binding
   request whose USERNAME begins with a session's ufrag (`<ufrag>:<peer's ufrag>`) and whose
   MESSAGE-INTEGRITY that session's ice-pwd signs; fit then opens a UDP socket of the address's
   own toward the media server and relays every datagram both ways, STUN, DTLS and SRTP alike.
   Anything else that comes first is dropped.
4. The media server sees fit's socket as its peer, a peer-reflexive candidate, as ICE allows. It
   was given no candidate of the peer's, so it never sends past fit.
5. The DELETE of fit's URL goes to the media server that answered, and frees the session at fit
   whatever the media server says.

Each peer address has a socket of its own toward the media server because a media server on one
port, as fit's `webrtc.media` is, tells its peers apart by their address: one socket shared by
every relay would make them one peer to it. A socket's datagrams from the media server go back to
the one peer address it was opened for.

### Signaling

- **Location and Link.** The media server's `Location`, relative or absolute, is kept as the
  session's path there; an absolute one's host is not used, since requests go to the media server
  that answered. `Link` fields for ICE servers (`rel="ice-server"`) are passed on as they are;
  one to a URL below the session's is rewritten below fit's URL for it, and the rest are dropped.
- **The session's URL.** DELETE, PATCH, OPTIONS and any request to a URL up to three segments
  below the session's go to the media server that answered, sticky by fit's ID, not by a cookie. A
  session the media server's upstream no longer lists is answered `502`; its DELETE still frees
  it. The endpoint's own OPTIONS goes to a media server the upstream picks.
- **PATCH.** A PATCH that only trickles candidates is answered `204` by fit: the media server
  reaches the peer through fit whatever the peer's candidates are. An ICE restart, a PATCH with an
  `a=ice-ufrag`, goes to the media server without its candidates, and the ufrag of its answer is
  routed as the first one was, its candidates replaced too.
- **Statuses.** `415` for an offer that is not `application/sdp`, `413` for a request past
  `max_sdp_bytes`, `503` with `Retry-After: 1` past `max_sessions`, and `503` while `webrtc.proxy`
  does not run; `502` for a media server that cannot be reached, an answer past `max_sdp_bytes`, a
  `201` with no `Location`, or one with no ufrag or no candidate fit can relay to (whose session
  fit then DELETEs at the media server), and `504` for one that does not answer within `timeout`;
  `404` for the URL of a session fit no longer has, and `405` for a method other than POST and
  OPTIONS at the endpoint. Every other answer, a media server's refusal of an offer among them, is
  passed on as it is, without its hop-by-hop fields.

With `auth`, a [`media_auth`](#auth) policy, and `action`, `publish` on a WHIP route or `play` on
a WHEP one, fit checks an offer on the last segment of its path before any media server sees it,
and answers `401` or `403` itself; a session's requests go on without credentials, and the
request's `Authorization` goes to the media server as it came, for one that checks it too.

Nothing waits: the offer goes upstream from its last body callback through fib's asynchronous
client, and the answer is written from the client's callback; a client that leaves cancels it.

### Limits

| Field | Default | Bounds |
| --- | --- | --- |
| `max_sessions` | 4096 | sessions at once, counted from the offer; one more is answered `503` |
| `max_relays` | 4096 | peer addresses relayed at once, each a socket; past it, a new address is dropped |
| `session_timeout` | 30s | a session with nothing relayed, from its answer or its last relay's end, is forgotten, and its URL answers `404` |
| `idle_timeout` | 30s | a peer address that sends nothing loses its relay, whatever the media server sends |
| `webrtc_proxy`'s `max_sdp_bytes` | 32 KiB | a request and an answer |
| `webrtc_proxy`'s `timeout` | 10s | from a request's last byte to its answer read whole |

The sockets toward the media servers leave from `relay_ip`, one of the host's addresses, and from
a port of `port_min` to `port_max`, for a firewall in front of the media servers that admits one
source address and a range, or a host whose media servers are on another interface; the route
and the kernel pick them when left out. A port in use is skipped, and a peer address that finds
every port of the range in use is dropped (`no_port`), its next request trying again: the range
bounds the relays at once beside `max_relays`. `relay_ip` is of the media servers' family: a media
address of the other family is not relayed. Both apply to the next relay after a reload.

A session relays at most 8 of its peer's addresses: one for each candidate a peer checks from,
and more after it moves. A session keeps at most 4 ufrags, its ICE restarts' among them. A relay
holds up to 8 datagrams while its socket opens, which may take 5 seconds before the relay is
closed. fit's `limits.max_conns` counts neither the peers' UDP addresses nor the sockets toward the
media servers: `max_relays` bounds both.

### ICE restarts, consent and a peer that moves

ICE's consent checks, every 5 seconds or so, are relayed as any datagram and keep the relay open.
A peer that moves, to another network or another NAT binding, checks again from its new address,
which opens a relay of its own to the same media server; the old one ends at `idle_timeout`. An
ICE restart through PATCH keeps its session; one made as a new session, as fit's own whip does, is
a new POST.

What fit cannot do:

- A media server whose candidates arrive only by trickle, after its answer, is answered `502`,
  unless `media_addrs` gives its address with the port.
- An answer with candidates on several addresses is relayed to one of them; ICE over TCP, and a
  media server reached only through TURN, are not relayed.
- A URL below the session's is proxied with its body and answer whole, up to `max_sdp_bytes`:
  extensions that stream, such as WHEP's server-sent events, do not work through fit.
- The media server sees fit's address, not the peer's.

### Reloading

The sessions belong to `webrtc.proxy`, not to the routes: a reload that keeps `listen` keeps them
and their relays, and applies its other fields to what comes next, `public_ips` to the next
answer. A change of `listen` ends every relay and session, and peers post new offers. A media
server a reload takes out of the upstream keeps its relays, while its sessions' requests are
answered `502`.

### Metrics and logs

The Admin API's `/metrics`, and OpenTelemetry's export, carry `fit_webrtc_proxy_sessions`,
`fit_webrtc_proxy_relays`, `fit_webrtc_proxy_sessions_total`,
`fit_webrtc_proxy_refused_total`, `fit_webrtc_proxy_sessions_ended_total{reason="delete"|"expired"|"reload"}`,
`fit_webrtc_proxy_relays_total`,
`fit_webrtc_proxy_dropped_total{reason="not_stun"|"unknown_ufrag"|"integrity"|"limit"|"no_port"}`,
`fit_webrtc_proxy_relayed_bytes_total{direction="to_backend"|"to_client"}` and
`fit_webrtc_proxy_restarts_total`. The upstream's own metrics count the signaling requests. The
proxy's messages go to the `webrtc` logger, after `webrtc.proxy: `: its port at `info`, and a
change of `listen` that ends its sessions at `warn`.
