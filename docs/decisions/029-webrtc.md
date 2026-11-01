# 029: WebRTC in fit, on fib's WebRTC packages

[English](029-webrtc.md) | [简体中文](029-webrtc.zh-CN.md)

Status: accepted (2026-11-01). Amends [decision 025](025-p2p.md).

## Context

fib now has WebRTC (`fib` `80c7f76`): a `PeerConnection` and a `Mux` that shares one UDP port among
any number of them, and the sans-IO protocols under them, `webrtc/stun`, `webrtc/ice`,
`webrtc/turn` (a client and a server core), `webrtc/sdp`, `webrtc/sctp`, `webrtc/srtp` and
`webrtc/rtp`, with DTLS in fib's `tls`. `webrtc/turn/turnserver` runs the TURN server core on a fib
engine: its listening address is a UDP binding, and each allocation's relayed address a binding of
its own, opened with `Engine.AddBinding` and spoken from with `Engine.OpenUDPPeer`
([A49](../fib-api-requests.md#a49-a-udp-peer-opened-toward-an-address-on-a-listeners-socket)).

Users asked for fit to serve WebRTC: a TURN server for browsers and other agents, WHIP and WHEP
ingest and playback with fit as the media server, and fit in front of other media servers as a
proxy of their signaling and their media.

Decision 025 planned STUN, TURN and ICE as packages of fit's own, `p2p/stun`, `p2p/turn` and
`p2p/ice`, because pion reads its sockets and runs its timers on goroutines of its own. fib's
packages are what 025 asked for, sans-IO and on fib's engine, and rule 15 prefers fib for what
fib can hold.

## Decision

- **fit's WebRTC is fib's.** fit uses fib's `webrtc` packages and adds no dependency; pion stays
  out, for 025's reasons.
- **025 is amended.** Its relay and its ICE come from fib: I86 (the STUN client) uses
  `webrtc/stun`'s transactions, I87 (the TURN server) `webrtc/turn/turnserver`, I88 (the TURN
  client) `turn.Client`, I89 (the ICE agent) `webrtc/ice`. `p2p/turn` and `p2p/ice` are not written.
  `p2p/stun` stays, for now, as fit's STUN server on any listener, over UDP and TCP, which fib does
  not have (fib's TURN server answers Binding requests only on its own UDP port). When I86 runs,
  its messages move to `webrtc/stun`'s and it keeps only the server, so that one STUN codec serves
  the process and fixes reach both. 025's other decisions hold: QUIC over the selected pair,
  signaling on the rendezvous fit, everything bounded.
- **The config's `webrtc`.** A top-level member holds what fit runs for WebRTC beside the gateway,
  as `agent` and `tunnel` do ([decision 028](028-agent-and-tunnel.md)): `webrtc.turn`, the TURN
  server, is a role of package `app`, on the listeners' engine, applied, moved and stopped with
  every config. WHIP and WHEP (I109) are handler modules on routes, and the UDP port their peers
  share is a role beside it, `webrtc.media`.
- **The TURN server** (`webrtc/turn`, I87, I111): UDP addresses to listen on, one binding, and
  TCP and TLS ones (I111, A55), the TLS certificates from files as a listener's; a realm; users
  with a password or coturn's long-term key, and coturn's `use-auth-secret` for time-limited
  credentials, either or both; the relay address, the public address behind a 1:1 NAT, and a port
  range; allocations in all and per user, relayed bytes a second per user and direction, lifetimes;
  permissions and channels per allocation (A57); peers allowed and denied, fib's denial of
  internal addresses being the default, and the server's own address reached on its relayed ports
  alone (A57). Users, the secret, the peer lists, the bandwidth, the lifetimes and the quotas apply
  to the next request after a reload, without touching the allocations held (A58); a change of the
  addresses starts the new server beside the old one, which drains for the listeners' drain
  timeout; a change of the realm or the relay addresses on the same addresses starts the server
  again, freeing its allocations. Its metrics, from fib's events and stats, are in the Admin API's
  `/metrics`, through `app`.
- **WHIP and WHEP** (I109): modules `whip` and `whep` terminate WebRTC in fit on the shared
  `webrtc.Mux` of `webrtc.media`: a stream by name from the route, one publisher forwarded to many
  viewers as RTP with key frame requests relayed, the route's middleware for auth, limits on
  streams, viewers, PeerConnections and bitrate, DELETE and PATCH (trickle) per RFC 9725 and the
  WHEP draft, ICE servers in `Link` headers, everything closed on reload and stop.
- **Proxying media servers** (I110): the handler module `webrtc_proxy` proxies WHIP and WHEP
  signaling to an upstream of media servers through fit's upstream pools, the offer without its
  candidates, the answer's candidates rewritten to fit's UDP port and its ufrag and ice-pwd
  remembered against the media server's address; the role `webrtc.proxy` holds that port, routes a
  new peer address by the STUN USERNAME of its first datagram, checked with the session's ice-pwd,
  and then relays it from a socket of its own toward the media server, so that many media servers
  sit behind one fit port. It is a role beside `webrtc.media`, not an L4 `service` mode: its
  sessions are long-lived and must outlast reloads, which drain an L4 service's connections with
  their graph; its routes are written by a handler at request time, which only a process-wide
  role both sides find (by engine, as `whip` finds `webrtc.media`) can share; and its destinations
  are the addresses answers name, not an upstream's. A socket per peer address, not one shared,
  because a media server on one port tells its peers apart by their address.

## What fib needs

All five are done, in fib `84a67f7`, and fit uses them since I111.

- [A55](../fib-api-requests.md#a55-turn-over-tcp-and-tls): TURN over TCP and TLS to the server,
  which 025 required and browsers behind UDP-blocking firewalls use.
- [A56](../fib-api-requests.md#a56-a-turn-server-that-never-waits-on-a-worker): the TURN server
  binds relayed addresses and opens peers without its worker waiting: today `AddBinding` and
  `OpenUDPPeer` wait on locks and on event loops inside a datagram's `OnData`.
- [A57](../fib-api-requests.md#a57-a-turn-servers-limits-and-hooks): per-allocation caps on
  permissions and channels, a peer check that sees the port, and events for metrics.
- [A58](../fib-api-requests.md#a58-a-turn-server-changed-in-place-and-drained): lifetimes and quotas
  changed in place, and a drain that takes no new allocation while the held ones end.
- [A59](../fib-api-requests.md#a59-a-dial-from-a-chosen-local-address-and-port-range): a dial
  from a chosen local address and port range, for the proxy's sockets toward media servers behind
  a firewall.

## Consequences

- fit has one WebRTC stack, fib's, which fib's tests and fuzzing cover; fixes go to fib (rule 15).
- Until A56, the TURN server's worker waited briefly on fib's binding lock and event loop when it
  bound a relayed address or opened a peer: a known breach of rule 1, bounded by the allocation
  quotas, gone since I111. Until A57, an allocation's permissions were bounded only by its requests
  and its channels by the 4096 numbers RFC 8656 has; since I111, by `max_permissions` and
  `max_channels`.
- Until A55, clients that could not use UDP could not reach the TURN server; since I111 they reach
  it over TCP and TLS.
- Since A59 (I111), the proxy's sockets toward media servers leave from `relay_ip` and a port
  range, for a firewall in front of them.
