# WebRTC

[English](webrtc.md) | [简体中文](webrtc.zh-CN.md)

fit serves WebRTC with fib's WebRTC packages, beside the gateway on its engine
([decision 029](decisions/029-webrtc.md)). What runs today is a TURN server; WHIP and WHEP, with
fit as the media server and in front of other media servers, are planned (I109 and I110 in the
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
