# WebRTC

[English](webrtc.md) | [简体中文](webrtc.zh-CN.md)

fit serves WebRTC with fib's WebRTC packages, beside the gateway on its engine
([decision 029](decisions/029-webrtc.md)): a TURN server, and WHIP and WHEP with fit as the media
server. fit in front of other media servers is planned (I110 in the
[development plan](dev-plan.md#webrtc)).

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

A browser is given it as an ICE server, `{urls: "turn:turn.example.com:3478", username,
credential}`. The server also answers STUN Binding requests on its port, without credentials, so
the same address serves as a `stun:` server. Every field is in the
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

### Addresses and firewalls

Each allocation's relayed socket binds to `relay_ip`, which must be one of the host's addresses.
Behind a 1:1 NAT, as on most clouds, set `public_ip` to the address that maps to it: clients are
told that one. `port_min` and `port_max` keep the relayed ports in a range, for a firewall that
opens one; the kernel picks them when both are left out. Each allocation holds a socket on every
event loop of fit's engine (`engine.event_loops`).

### Peers fit will not relay to

A relay that sends wherever a client asks is a way into the network it runs in. Permissions and
channels for peers on loopback, private (RFC 1918 and unique local), link-local, multicast, shared
(RFC 6598), and other internal or reserved ranges are refused, as fib's `DeniedByDefault` lists
them. `allowed_peers` lets some of them through, for peers on a private network, and
`denied_peers` refuses more. Two clients of the same server relay to each other's relayed
addresses, on the server's own address: when `relay_ip` is private, list it in `allowed_peers`.
The relay then reaches every port of that address, fit's own listeners included: keep only the
relay range open on it to the clients' peers, or listen on another address. fib cannot yet allow
an address's relay ports alone ([A57](fib-api-requests.md#a57-a-turn-servers-limits-and-hooks)).

### Limits

| Field | Default | Bounds |
| --- | --- | --- |
| `max_allocations` | 1024 | allocations held; one more is refused with 508 |
| `max_allocations_per_user` | 16 | allocations of one username; one more is refused with 486 |
| `user_bandwidth` | 2 MiB | bytes a second relayed for one username each way, with a second's burst; past it, datagrams are dropped |
| `default_lifetime`, `max_lifetime` | 10m, 1h | an allocation's lifetime, which its client refreshes |
| `nonce_lifetime` | 10m | how long a nonce is good for before a 438 |

An allocation's permissions last five minutes unless refreshed, and its channels ten; fib does not
yet cap how many one allocation holds (A57). fit's `limits.max_conns` counts the clients' and peers'
UDP addresses with every other connection.

### Reloading

A reload that changes `users`, `secret`, `allowed_peers`, `denied_peers` or `user_bandwidth`
applies them to the next request; the allocations held go on. A change of any other field starts
the server again: its allocations end, and browsers restart ICE. A change of `listen` binds the new
addresses before the old ones close, unless they share one.

### Metrics and logs

The Admin API's `/metrics`, and OpenTelemetry's export, carry `fit_turn_allocations`,
`fit_turn_relayed_bytes_total{direction="to_peer"|"from_peer"}`,
`fit_turn_dropped_total{reason="bandwidth"}`, `fit_turn_peers_refused_total` and
`fit_turn_restarts_total`. The server's messages go to the `webrtc` logger.

### Not yet

These wait on fib ([fib API requests](fib-api-requests.md)):

- TURN over TCP and TLS, for clients on networks that block UDP
  ([A55](fib-api-requests.md#a55-turn-over-tcp-and-tls)).
- Binding a relayed address and opening a peer without a fib worker waiting
  ([A56](fib-api-requests.md#a56-a-turn-server-that-never-waits-on-a-worker)).
- Caps on an allocation's permissions and channels, a peer rule with ports, and events for logs
  and metrics ([A57](fib-api-requests.md#a57-a-turn-servers-limits-and-hooks)).
- Lifetimes and quotas changed without a restart, and a restart that drains its allocations
  ([A58](fib-api-requests.md#a58-a-turn-server-changed-in-place-and-drained)).

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
`stream` param, which fixes it. A session's URL is the path of its POST followed by the session's
ID, so the route must match it too: `/whip/*` does, and a route of `/live` alone needs `/live/*`
beside it. Every field is in the [config reference](reference/config.md#webrtc).

### The HTTP API

| Request | Answer |
| --- | --- |
| `POST` an `application/sdp` offer to the endpoint | `201 Created`, fit's answer as `application/sdp`, `Location` the session's URL (the endpoint's path and the session's ID), and a `Link` header for each ICE server |
| `DELETE` the session's URL | `200`; a publisher's stream ends, and its viewers' sessions with it; `404` for a session that has ended |
| `OPTIONS` | `204`, with `Allow` and `Accept-Post: application/sdp` |
| `PATCH` the session's URL | `405`: see below |

Refusals: `415` for an offer that is not `application/sdp`; `413` past `max_sdp_bytes`; `400` for
one that does not parse or cannot be answered; `422` for one with no audio or video in a codec fit
forwards, or a viewer's that receives none of the stream's tracks; `409` for a stream published
already; `404` for a viewer of a stream not published; `503` with `Retry-After` past
`max_peer_connections`, `max_streams` or `max_viewers`, and while `webrtc.media` does not run.

Auth is the route's middleware: RFC 9725's bearer token is an `Authorization` field, which `jwt`
verifies, or `basic_auth`, `forward_auth` and the rest. A DELETE goes through the same route, and
its session's ID, 128 random bits, is known only to whoever made the session.

fit answers without trickle ICE, and PATCH is not supported: fit is an ICE lite agent (RFC 8445
section 2.5) on the Mux, whose candidates are the port's own addresses, all in the answer, and it
learns a peer's address from the peer's checks, so the candidates a peer would trickle add nothing.
An ICE restart is a new session: a client DELETEs the old one and posts a new offer. Clients
answered 405 do not trickle, as RFC 9725 says.

### Media

Each publisher's offer is answered with one codec of each kind, the first of its own that
`codecs` lists (opus, VP8, VP9, H264 and AV1 when left out), and a viewer is answered with the
same, which its offer must accept. A viewer gets the stream's audio and video on the sections of
its offer, from one TrackLocal for each of the publisher's: its packets are protected again under
the viewer's own SSRC and keys, their sequence numbers and timestamps as the publisher sent them.

- **Key frames.** A viewer that joins, or loses packets, asks for a key frame (PLI or FIR); fit
  asks the publisher with a PLI, at most once a `key_frame_interval` (500ms) whatever the number of
  viewers, and once more when a viewer connects.
- **Bitrate.** fit tells each publisher `max_bitrate` (8 Mbit/s) once a second with REMB, which
  browsers keep under, and drops its packets past it, with two seconds of burst.
- **Leaving.** A publisher's DELETE, its PeerConnection failing, or its peer silent for
  `ice_timeout` (30s) ends the stream and its viewers' sessions; a viewer's ends only its own. A
  session not connected within `connect_timeout` (30s) of its answer ends too. A viewer cut or
  slow does not hold the publisher or the other viewers: fit sends UDP and never waits for it.
- **Not forwarded.** Data channels, simulcast layers past the first (a simulcast publisher's first
  layer to arrive is forwarded), RTX and FEC, and transport-wide congestion control, which fib's
  PeerConnection does not do. Each viewer is sent the publisher's packets as they come: there is no
  per-viewer bandwidth estimate or layer selection.

### ICE servers

fit needs no STUN or TURN server of its own: its port is reached at its addresses, or at
`public_ips` behind a 1:1 NAT. A peer behind a firewall that blocks UDP to it needs a TURN server
on its side; the answer's `Link` headers tell it of `ice_servers` and, as `turn_urls`, of fit's own
`webrtc.turn`, with credentials made from its `secret` for `turn_credential_ttl` (1h):

```
Link: <stun:stun.example.com:3478>; rel="ice-server"
Link: <turn:turn.example.com:3478>; rel="ice-server"; username="1767225600:fit-5f1c..."; credential="..."; credential-type="password"
```

fit's TURN server relays to the media port only when `allowed_peers` lets it reach that address,
as a private `relay_ip` needs.

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
`max_viewers` bounds that work too. fit's `limits.max_conns` counts each peer's UDP address with
every other connection.

### Reloading

Sessions belong to `webrtc.media`, not to the routes: a reload that keeps its `listen` and
`public_ips` keeps every session, and its other fields apply to the next offer (`max_bitrate` and
`key_frame_interval` at once). A change of `listen` or `public_ips` starts the media server again:
its sessions end, each peer told with a DTLS close, and publishers and viewers post new offers.
Leaving `webrtc.media` out ends them all, and the routes answer 503.

### Metrics and logs

The Admin API's `/metrics` carries `fit_webrtc_media_peer_connections{role}`,
`fit_webrtc_media_streams`, `fit_webrtc_media_sessions_total{role}`,
`fit_webrtc_media_sessions_ended_total{reason}`, `fit_webrtc_media_refused_total{reason}`,
`fit_webrtc_media_received_packets_total`, `fit_webrtc_media_received_bytes_total`,
`fit_webrtc_media_dropped_total{reason="bitrate"}`,
`fit_webrtc_media_key_frame_requests_total{result="sent"|"held"}` and
`fit_webrtc_media_restarts_total`. The media server's messages go to the `webrtc` logger.
