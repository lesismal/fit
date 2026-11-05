# The tunnel

[English](tunnel.md) | [简体中文](tunnel.zh-CN.md)

Config examples, in .conf, in [fit-examples](https://github.com/lesismal/fit-examples): [tunnel-hub](https://github.com/lesismal/fit-examples/blob/main/tunnel-hub/fit.conf), [tunnel-node](https://github.com/lesismal/fit-examples/blob/main/tunnel-node/fit.conf), [tunnel-proxy](https://github.com/lesismal/fit-examples/blob/main/tunnel-proxy/fit.conf).

fit's tunnel reaches TCP services on machines that only a host beside them can reach, through a
hub anyone can reach. It is three roles of fit's config, which run on the listeners' fib engine
beside the gateway's routes, or without any
([decision 028](decisions/028-llm-and-tunnel.md)).

```text
 local app ──TCP──▶ tunnel.proxy ══link══▶ tunnel.hub ══link══▶ tunnel.node ──TCP──▶ service
 (ssh, browser)     a local port per        routes tunnels,         beside the          10.0.0.5:22
                    forward                 passes their frames     services
```

| Role | Member | Runs on | Does |
|------|--------|---------|------|
| hub | `tunnel.hub` | a host nodes and proxies can reach | registers nodes and proxies, routes each tunnel a proxy opens to the node it names, and passes the tunnel's frames between them |
| node | `tunnel.node` | a host on the services' network | links to one or more hubs, registers with the services it reaches, and connects to a service for each tunnel routed to it, to no other address |
| proxy | `tunnel.proxy` | the user's machine | links to hubs, learns their nodes and services, and listens on a local address per forward, each connection there a tunnel |

Nodes and proxies link **out** to hubs, so neither needs an open port. A process may run any of the roles, all three included, with the
gateway or alone.

The tunnel relays every byte through a hub, and works today wherever nodes and proxies can
reach one. The planned [P2P milestone](decisions/025-p2p.md) is the other way: ICE connects two
hosts behind NAT directly, through holes punched in both NATs, carries TCP and UDP services, and
falls back to a TURN relay only where no hole can be punched.

## Running a role

A role runs while its member is set. A config with roles and no routes needs no listener:

```json
{
  "tunnel": {
    "hub": {
      "name": "hub",
      "password": "a-registration-password",
      "body_password": "a-body-password",
      "listeners": [
        {"protocol": "tls", "address": ":7000"},
        {"protocol": "wss", "address": ":7001"},
        {"protocol": "quic", "address": ":7443"}
      ],
      "console": {
        "address": "127.0.0.1:7080",
        "users": [{"username": "admin", "password_hash": "$2a$10$..."}]
      }
    }
  }
}
```

```json
{
  "tunnel": {
    "node": {
      "name": "lab",
      "hubs": [{
        "name": "hub",
        "endpoints": [
          {"address": "quic://hub.example.com:7443"},
          {"address": "wss://hub.example.com:7001/tunnel"},
          {"address": "tls://hub.example.com:7000"}
        ],
        "password": "a-registration-password",
        "body_password": "a-body-password",
        "pin": "02e5420d38831f019c363d82b2d7ee2ff7db1a143401587e3b9a92f0011e8261"
      }],
      "services": [
        {"name": "ssh", "address": "10.0.0.5:22", "description": "the lab's gateway"}
      ]
    }
  }
}
```

```json
{
  "tunnel": {
    "proxy": {
      "name": "alice",
      "hubs": [{
        "name": "hub",
        "endpoints": [{"address": "tls://hub.example.com:7000"}],
        "password": "a-registration-password",
        "body_password": "a-body-password",
        "pin": "02e5420d38831f019c363d82b2d7ee2ff7db1a143401587e3b9a92f0011e8261"
      }],
      "forwards": [
        {"listen": "127.0.0.1:2222", "node": "lab", "service": "ssh"}
      ]
    }
  }
}
```

`pin` is the SHA-256 fingerprint of the hub's certificate, which the hub's console shows and its
log prints when it makes the certificate. Then `ssh -p 2222 127.0.0.1` reaches 10.0.0.5:22.
`fit validate` checks the members; the
[config reference](reference/config.md) lists every field, its default and its bounds.

### tunnel.hub

| Field | Default | What it is |
|-------|---------|------------|
| `name` | the host's name | the name nodes and proxies are told |
| `password` | none: no registration is taken | what nodes and proxies register with, proven with an HMAC, never sent |
| `body_password` | none | AES-256-GCM on every frame's body, which authenticates every frame; nodes and proxies need the same |
| `listeners` | none: the pages add them | `{protocol: tls\|wss\|quic\|tcp\|ws, address, path, cert_file, key_file}`; a ws or wss listener's path is `/tunnel` by default, and only those have one. A `tcp` or `ws` listener on an address that is not a loopback one (`127.0.0.0/8`, `::1`, `localhost`; an empty host is every address) is refused unless `body_password` is set (see [What protects a link](#what-protects-a-link)). A tls, wss or quic listener presents the certificate of its `cert_file` and `key_file`, read each time the member is applied, or the hub's own (below) |
| `console_socket` | none | `{address, password, max_conns}`: the console over an encrypted WebSocket, for proxies' `web_proxies`, answering on any path; it needs `console`, its password is the hub's when left out, and `max_conns` is 32. A WebSocket that has not proven the password within 10s is closed, and one IP address may have a quarter of `max_conns` open that have not |
| `quic` | | `{cert, key, idle_timeout, keep_alive, handshake_timeout, max_handshakes}`, 30s, 10s, 10s and 64 when left out; past half of `max_handshakes`, a client proves its address with a QUIC Retry before its handshake starts. `cert` and `key` are the hub's own certificate, which its tls, wss and quic listeners without `cert_file` present; the hub makes a self-signed one and writes it here when such a listener needs one; they go together |
| `register_timeout` | 10s | how long a link may take to register |
| `call_timeout` | 15s | how long a node may take to answer: to open a tunnel, list or set its services |
| `max_links`, `max_links_per_peer`, `max_peers`, `max_tunnels` | 1024, 128, 1024, 16384 | the bounds of what the hub keeps |
| `max_unregistered`, `max_unregistered_per_ip` | 256, 32 | the links open that have not registered yet, in all and from one IP address; one past either closes as it opens, and its node or proxy links again after its `retry_interval` |
| `allow_proxy_services` | false | let registered proxies set nodes' services through the hub, as a proxy's pages do; every proxy with the password could then. A node still takes them only with its `allow_hub_services` |
| `console` | none | the hub's pages (see [Consoles](#consoles)) |

### tunnel.node

| Field | Default | What it is |
|-------|---------|------------|
| `name` | the host's name | the name proxies reach the node by |
| `meta` | none | anything else the node tells the hubs |
| `hubs` | none | the hubs it links to (below) |
| `services` | none | `{name, address, description}`: what the node reaches, and the only addresses it connects to |
| `allow_hub_services` | false | let its hubs set `services`, written into this member; off, only the node's member and pages set them |
| `dial_timeout` | 5s | a connect to a service |
| `idle_timeout` | 2h | a tunnel that carries nothing either way for this long is closed |
| `max_tunnels` | 4096 | the tunnels open at once, over every hub |
| `console` | none | the node's pages |

### tunnel.proxy

| Field | Default | What it is |
|-------|---------|------------|
| `name`, `meta`, `hubs` | | as a node's |
| `forwards` | none | `{listen, node, service, hub}`: without `hub`, the tunnels spread over every hub that has the node, more of them through the hubs whose path to the node is faster |
| `web_proxies` | none | `{name, listen, url, password, max_conns}`: a hub's console served locally through its `console_socket`, whose `ws://host:port/` is `url`; `max_conns` is 32 |
| `open_timeout` | 20s | the opening of a tunnel, through the hub to the service |
| `idle_timeout` | 2h | as a node's |
| `max_tunnels` | 4096 | the tunnels open at once; a connection past it is closed as it is accepted |
| `console` | none | the proxy's pages |

### A hub a node or proxy links to

| Field | Default | What it is |
|-------|---------|------------|
| `name` | the first endpoint's address | what forwards and the pages name it by |
| `endpoints` | required | `{address, disabled}`, tried in order: `tls://host:port`, `wss://host:port/path` (443 without a port), `quic://host:port`; and `host:port` or `tcp://` for TCP and `ws://host:port/path`, in the clear, which are refused to a host that is not a loopback one unless `body_password` is set |
| `password` | required | the hub's |
| `body_password` | none | the hub's, when it has one |
| `pin` | | the SHA-256 of the hub's certificate, as the hub's page shows it, which tls, wss and quic endpoints check the certificate against in place of the chain and the name: for the hub's self-signed certificate. Its old name, `quic_pin`, is still read, with a warning |
| `ca_file` | | a PEM file of the CAs that sign the hub's certificate, checked with its name, in place of the system's roots; read at each dial; not with `pin` |
| `quic_insecure` | false | a quic endpoint takes any certificate: anyone on the path can then read and change the links. For tests only; tls and wss have no such switch |
| `pool_size` | 10, at most 100 | how many links to the hub carry tunnels |
| `dial_timeout` | 5s | a link's dial |
| `call_timeout` | 10s | the hub's answer to a registration or a call |
| `retry_interval` | 3s | the wait after a failed dial, or a lost hub, before a link dials again |
| `ping_interval`, `ping_timeout` | 5s, 5s | how often the round trip to the hub is measured, and how long a ping may go unanswered before it counts as lost |

## Links

A node or proxy keeps `pool_size` links to each hub. Each dials the hub's first endpoint
not disabled, falls back to the next when that fails, registers, and dials again `retry_interval`
after a failed dial or after it loses the hub. The links of one name and type, from however many processes, are one
peer to the hub. Each tunnel goes over one link throughout, so that its frames keep their
order; a new tunnel takes the link that carries the fewest, and a link carries at most 64, so that
a peer that needs more opens a larger pool.

- **TLS** links (`tls://`) are TCP connections of the engine with fib's TLS 1.3 (package `fib/tls`)
  under the link's frames, and the ALPN `fit-tunnel`. **WebSocket** links (`wss://`, and `ws://` in the
  clear) are for networks that let only HTTP(S) through: a wss link is TLS 1.3 with the ALPN
  `http/1.1`, then a WebSocket whose binary frames carry the link; through nginx it takes
  `proxy_pass` with `Upgrade`, and a proxy that ends the TLS itself needs `ca_file` or the system's
  roots to check its certificate, and `body_password` past it. **TCP** links (`tcp://`) are the
  frames in the clear. Handshakes run on fib's state machines, a verification of the certificate
  on fib's handshake pool, never on a worker.
- **QUIC** links run on a UDP binding of the same engine, with fib's QUIC, TLS 1.3 and the ALPN
  `fit-tunnel`.
- **The hub's certificate.** A hub makes a self-signed certificate when a tls, wss or quic listener
  needs one and keeps it in its member (`quic.cert`, `quic.key`), so its fingerprint, which nodes and
  proxies pin, outlives a restart; its console shows the fingerprint, and each listener's. A listener
  with `cert_file` and `key_file` presents that certificate instead, for nodes and proxies that check
  it with `ca_file` or the system's roots. A node or proxy checks the certificate by `pin`, by
  `ca_file`, or by the system's roots, the name included unless pinned. It keeps no TLS session
  tickets, so each handshake checks the certificate in full; the pin is checked again in the
  handshake's last check, which a resumed session runs too.
- **Registration** is mutual. Each link of a node or proxy registers with an HMAC-SHA256 of the
  password over the peer's type, name, a time and a fresh random nonce; the time must be within
  5 minutes of the hub's clock, and a nonce is taken once. The hub's answer proves the hub knows
  the password in turn: an HMAC-SHA256 of the password over that registration, its nonce
  included, and over the answer, under a label of its own. The node or proxy checks it before it
  serves anything the hub sends on the link, and closes the link when the proof is missing or
  wrong: an answer without the password, the link's own registration sent back, or an answer
  replayed from another link. The proof is made once, at registration: it does not cover the
  frames after it. Over plain `tcp://` or `ws://` without `body_password`, an attacker who takes
  over the hub's address (DNS or IP) can relay a node's registration to the real hub and the
  proven answer back, and then send the node any frame of its own: open a tunnel to its services,
  set its services, kick it. That is why such links are refused off the loopback (see
  [What protects a link](#what-protects-a-link)).
  **Upgrade hubs first.** A hub of this release takes the nodes and proxies of earlier ones, but
  a node or proxy of this release refuses a hub of an earlier release, which sends no proof:
  its status and log say that the hub runs a fit without hub authentication, older than this
  side's version, and to upgrade it.
- **Body encryption**, with `body_password` on every side, seals each frame's body with AES-256-GCM,
  its header authenticated too. Each link opens with both sides sending fresh random bytes; each
  direction of the link then has a key of its own, derived with HKDF from the password and both
  sides' bytes, and numbers its frames, the number being the nonce. A frame that is not sealed, or
  does not open as the next one, closes the link: one replayed, reordered, sent back to its sender
  or carried over from another link, a registration among them, so an attacker on the path of a
  `tcp://` or `ws://` link can neither forge a frame, nor repeat what went through it (a service
  command, a tunnel's close, a change of a node's services), nor register with a node's captured
  registration. The keys name the direction too, dialer to listener or back, beside both sides'
  random bytes, so that a frame a node sealed never opens at another node whose link an attacker
  gave the same random bytes the other way round. The attacker can still see the frames' lengths
  and timing, and cut the link. This is the link protocol's version 3: a hub, node or proxy with
  `body_password` closes a link to a peer of version 2 or 1, logging the peer's version and its
  own, so upgrade them together.

### What protects a link

| Endpoint | The hub proven by | The frames after registration | What an attacker on the path, or at the hub's address, can do |
|----------|-------------------|-------------------------------|------------------------------------------------------------------|
| `tls://`, `wss://`, `quic://` with `pin` or `ca_file` | its certificate (TLS 1.3), and the registration's proof | authenticated and encrypted by TLS | cut the link, see its timing and sizes |
| `tls://`, `wss://`, `quic://` with neither | its certificate, by the system's roots: a hub with a public CA's certificate | as above | as above, as long as no CA the system trusts signs another certificate for the hub's name |
| `tcp://`, `ws://` with `body_password` | the registration's proof, and each frame sealed under the password | each authenticated and its body encrypted (AES-256-GCM); forged, replayed, reordered and crossed frames close the link | as above, and see the frames' kinds and headers |
| `quic://` with `quic_insecure` | the registration's proof, once | not authenticated | read every frame and send its own, through a relay: for tests only |
| `tcp://`, `ws://` without `body_password` | the registration's proof, once | not authenticated, in the clear | read every frame, and through a relay send its own: refused unless the hub's address is a loopback one |

A relay that does not have the hub's key cannot complete TLS with a node that pins the hub's
certificate, or checks it by a CA: the node closes the connection in the handshake, saying the
certificate does not match the pin, and the relay sees neither the registration nor anything after
it. One that only passes the bytes on cannot change them: TLS closes the link at the first record
the relay wrote.

### Upgrading: the links are TLS, or sealed (breaking)

This release refuses links that only the registration authenticates:

- A hub whose `tcp` or `ws` listener is on an address that is not a loopback one, and that has no
  `body_password`, no longer loads; nor does a node or proxy whose `tcp://`, `host:port` or `ws://`
  endpoint names a host that is not a loopback one without `body_password`. `fit validate` names
  each and what to set. Move them to `tls` or `wss` listeners (the hub makes the certificate and
  shows its fingerprint) and `tls://` or `wss://` endpoints with `pin`, or to `quic://`; or set
  `body_password` on the hub and on every node and proxy. A hub can serve both for a while: add a
  tls or wss listener, move the nodes and proxies, then remove the old listener.
- `quic_pin` is now `pin`, and checks tls and wss endpoints as well as quic ones. The old name
  still loads, with a warning; the pages write `pin`.
- With `body_password`, the link protocol is version 3, whose keys name the direction: a hub,
  node or proxy of an earlier release and one of this release refuse each other's sealed links,
  each logging both versions. Upgrade them together.

### Flow, limits and timeouts

Each end of a tunnel sends at most 4 MiB the other end has not acknowledged, and holds its TCP
connection's reads once it has; the other end acknowledges what its own connection has taken. A
slow service holds back the proxy that writes to it, and a slow proxy the service, one tunnel at
a time, with no more than a window and the sockets' buffers between them. What waits for a link is
bounded by the windows of its tunnels. The console socket and web proxies hold one side's reads
while the other side's backlog passes 256 KiB.

Every dial, registration and call has its timeout; a link's TCP connection is probed after 30s of
quiet, and closed when its peer stops answering; a QUIC connection pings every `keep_alive` and
closes after `idle_timeout`; a tunnel idle past `idle_timeout` closes. The hub bounds its links,
peers and tunnels, a peer's links, the QUIC handshakes under way, and the frames of a link that has
not registered, to 64 KiB.

## Consoles

Each role's `console` serves its pages, the workbench of [the console guide](console.md#the-pages)
with the role's module alone in its menu; when fit also runs `admin.console`, they show there too,
as the module `tunnel.hub`, `tunnel.node` or `tunnel.proxy` under Tunnel, with no `console` member
needed. A role's own console is behind logins as `admin.console`'s are
([the console](console.md)): `{address, cert_file, key_file, users, session_timeout,
session_lifetime, max_sessions, max_conns}`, users with bcrypt hashes from `fit hash-password`, a
session cookie and a CSRF token, and a loopback address unless `cert_file` and `key_file` are set.

- **hub**: the nodes, with their services and latency, and the proxies; the tunnels, with their
  bytes each way and their paths' latency. Query a node, set its services, kick a peer, close a
  tunnel. Add, change and remove listeners; set the passwords and the console socket; copy the
  fingerprint of the hub's certificate, which nodes and proxies pin, and see each listener's.
  Services set here are written into the node's own member, when
  the node's `allow_hub_services` lets them.
- **node**: its hubs, with the protocols each link took, and add, edit and remove them; its
  services, and save them, every hub told; its tunnels, and close one.
- **proxy**: its hubs, edited as a node's, their nodes and services, with each hub's share of
  a node's tunnels; query a node, and set its services through a hub whose `allow_proxy_services`
  and node's `allow_hub_services` let it; add and remove forwards;
  its tunnels, and close one; the meta it tells the hubs; and each hub's console through its
  web proxy.

What a page changes is written into the role's member of the config, with `app.Host.Edit`, and
applied from there as any config is: it is in the config's history, and with `admin.store` it is
kept, encrypted, in the store, and read back at the next start ([the console](console.md)
describes the store). Without a store, the change runs until fit restarts or reads its files
again, and the page says so. Every page has a tunnel map,
**Details**, and the theme and language pickers.

A hub's console can be reached where it cannot be directly, through an encrypted WebSocket: set
the hub's `console_socket`, and a proxy's `web_proxies` entry with its URL and password serves
the console on a local address. The WebSocket's messages are AES-256-GCM sealed with keys from the
password; the console's own login still applies.

## Reloads and bounds

- Everything runs on fit's engine: no engine, socket or goroutine of the tunnel's own, QUIC
  included, and the gateway's `limits` apply to the roles' connections too.
- A role moves to a new member without dropping what is unchanged: a hub's links, a node's or
  proxy's links to a hub whose settings are the same, a forward on the same address.
- A listener or forward that cannot listen is kept in the config, shown on the pages with its
  error, and tried again with the next config.
- Every resource is bounded: the hub's `max_links`, `max_links_per_peer`, `max_peers` and
  `max_tunnels`, 64 tunnels a link, the roles' `max_tunnels` and `idle_timeout`, TCP keep-alive on
  links and tunnels, and a QUIC connection that carries no link closed after its handshake timeout.
- Each tunnel's flow control is checked, by the other end and by the hub: an end that sends more than
  its 4 MiB window allows, with 1 MiB to spare for the read that spends it, or acknowledges more
  than it was sent, has its tunnel closed, so that it cannot fill the links other tunnels share.
  A tunnel whose service or proxy connection fails to take what arrived closes too.
- The pages are behind fit's logins and its Content-Security-Policy.

## Testing

`go test -race ./tunnel/...` runs the roles through fit's config, as `fit run` does, on a real
engine: tunnels over TCP, TLS, WebSocket, WebSocket over TLS and QUIC, with and without body
encryption, large transfers, slow services and proxies, cut connections, the pages' changes written
back into the config, and a restart that reads them back from a store. A relay at the hub's address
is tested too: one that passes the bytes on cannot inject a frame over tls or wss, and one with a
certificate of its own does not complete the handshake with a node that pins the hub's. `test/docker/tunnel/fit-tunnel-docker.sh test` runs
hubs, nodes and proxies in containers of their own, with devices on networks only their node
reaches, tests every feature through the consoles and the tunnels, then restarts every instance
and checks that it comes back from its store.
