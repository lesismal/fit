What fit runs for WebRTC beside the gateway, on its engine. `turn` is a TURN server (RFC 8656),
fib's, which relays UDP for browsers and other ICE agents that cannot reach each other directly:
over UDP on the `listen` addresses, with fixed `users`, coturn's time-limited credentials from
`secret`, or both. Each allocation's relayed socket binds to `relay_ip`, told to clients as
`public_ip` behind a 1:1 NAT, in `port_min`-`port_max` when set. Peers on loopback, private and
other internal addresses are refused unless `allowed_peers` lists them. Users, the secret, the
peer lists and `user_bandwidth` change on reload with the allocations kept; a change of the rest
starts the server again. See [WebRTC](../webrtc.md) and
[decision 029](../decisions/029-webrtc.md).

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
