What fit runs for WebRTC beside the gateway, on its engine. `turn` is a TURN server (RFC 8656),
fib's, which relays UDP for browsers and other ICE agents that cannot reach each other directly:
over UDP on the `listen` addresses, over TCP on `listen_tcp` and over TLS on `listen_tls` with
`tls`'s certificates, with fixed `users`, coturn's time-limited credentials from `secret`, or
both. Each allocation's relayed socket binds to `relay_ip`, told to clients as `public_ip` behind
a 1:1 NAT, in `port_min`-`port_max` when set; it holds at most `max_permissions` peers and
`max_channels` channels. Peers on loopback, private and other internal addresses are refused
unless `allowed_peers` lists them, and `relay_ip` itself is reached on the relayed ports alone,
or on a port `allowed_peers` names with it. Users, the secret, the peer lists, the quotas, the
lifetimes, `user_bandwidth` and the certificates change on reload with the allocations kept; a
change of the addresses starts a new server and drains the old one, and a change of the rest
starts the server again.

`media` makes fit a media server, an SFU, for the [`whip`](#whip) and [`whep`](#whep) handlers:
one UDP port, `listen`, that every PeerConnection shares, told to peers as `public_ips` behind a
1:1 NAT; the STUN and TURN servers peers are told of, `ice_servers`, and fit's own `turn` as
`turn_urls`, with credentials made from its `secret`; and the limits: PeerConnections, streams,
viewers a stream, a publisher's bitrate, and timeouts. A reload keeps the sessions unless it
changes `listen` or `public_ips`.

`proxy` puts other media servers behind fit, for the [`webrtc_proxy`](#webrtc_proxy) handler: one
UDP port, `listen`, told to peers as `public_ips`, where each peer's first STUN request names the
session, by its ufrag, whose media server it is relayed to from a socket of its own, leaving
from `relay_ip` and a port of `port_min`-`port_max` when set; the limits on
sessions and relays, and how long an idle relay and a session with none are kept. A reload keeps
the sessions unless it changes `listen`. See [WebRTC](../webrtc.md) and
[decision 029](../decisions/029-webrtc.md).

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
    },
    "media": {
      "listen": ":8189",
      "public_ips": ["203.0.113.5"],
      "ice_servers": [{"urls": ["stun:stun.example.com:3478"]}],
      "turn_urls": ["turn:203.0.113.5:3478"],
      "max_streams": 16,
      "max_viewers": 100,
      "max_bitrate": 4000000
    },
    "proxy": {
      "listen": ":8190",
      "public_ips": ["203.0.113.5"],
      "max_sessions": 4096,
      "idle_timeout": "30s",
      "relay_ip": "10.0.0.5",
      "port_min": 40000,
      "port_max": 40999
    }
  }
}
```
