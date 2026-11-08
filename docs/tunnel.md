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
      "tokens": [
        {"id": "fdd44d906b8e6ce2", "name": "lab", "hash": "1c994a095de7b566dcce0f229b829af96d85911b6d3e786f217c77340bc36aa4"},
        {"id": "5a3716dd56e5e458", "name": "alice", "hash": "4e6349fdb79b1b48fb57cc61b5aa9593490f288f28f722e0245c04a9df6c66df"}
      ],
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
        "token": "fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ",
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
        "token": "fitt1.5a3716dd56e5e458.nz51QI_A3avzPo4tagvevbtlJ-dNi-Us-k4-il9gAV4",
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
log prints when it makes the certificate. The hub's `tokens` are not written by hand: its Tokens
page, its Admin API or `fit token create -role tunnel.hub -name lab` issues each, keeps its hash
there, and shows the token itself once, for the node's or proxy's `token` (see
[Tokens](#tokens)). Then `ssh -p 2222 127.0.0.1` reaches 10.0.0.5:22.
`fit validate` checks the members; the
[config reference](reference/config.md) lists every field, its default and its bounds.

### tunnel.hub

| Field | Default | What it is |
|-------|---------|------------|
| `name` | the host's name | the name nodes and proxies are told |
| `tokens` | none | the tokens the hub issued, each `{id, name, hash, created_at, expires_at, note, members}`, which nodes and proxies register with (see [Tokens](#tokens)); made by the pages, the Admin API and `fit token`, not by hand |
| `password` | none: only tokens register | deprecated: a password every node and proxy may register with, proven with an HMAC, never sent. Taken in this release, with a warning on the pages while a member uses it; refused in the next |
| `body_password` | none | AES-256-GCM on every frame's body, which authenticates every frame; nodes and proxies need the same. A link that registers with a token keys it from the token and this by HKDF |
| `listeners` | none: the pages add them | `{protocol: tls\|wss\|quic\|tcp\|ws, address, path, cert_file, key_file}`; a ws or wss listener's path is `/tunnel` by default, and only those have one. A `tcp` or `ws` listener on an address that is not a loopback one (`127.0.0.0/8`, `::1`, `localhost`; an empty host is every address) is refused unless `body_password` is set (see [What protects a link](#what-protects-a-link)). A tls, wss or quic listener presents the certificate of its `cert_file` and `key_file`, read each time the member is applied and again when they change on disk (`fit run -watch-certs`, on by default), or the hub's own (below) |
| `access` | none: the global `access` decides | `{allow, deny, allow_files, deny_files}`: the client addresses the listeners and `console_socket` take, deciding before the global [`access`](reference/config.md#access); a connection refused is closed as it is accepted, and a quic listener drops the datagrams of an address refused. A change applies to new connections without listening anew; those open stay. The console's pages take `console.access`. |
| `console_socket` | none | `{address, password, max_conns}`: the console over an encrypted WebSocket, for proxies' `web_proxies`, answering on any path; it needs `console`, its password is the hub's when left out, and `max_conns` is 32. A WebSocket that has not proven the password within 10s is closed, and one IP address may have a quarter of `max_conns` open that have not |
| `quic` | | `{cert, key, idle_timeout, keep_alive, handshake_timeout, max_handshakes}`, 30s, 10s, 10s and 64 when left out; past half of `max_handshakes`, a client proves its address with a QUIC Retry before its handshake starts. `cert` and `key` are the hub's own certificate, which its tls, wss and quic listeners without `cert_file` present; they go together. When such a listener needs one and none is set, the hub makes a self-signed one and keeps it: here, with `admin.store`; or else in a file beside the config, mode 0600, named after it (`fit.tunnel.hub.pem` beside `fit.json`, or in a directory of configs) |
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
| `access` | none: the global `access` decides | as a hub's, for the connections `forwards` and `web_proxies` accept. A node listens on nothing but its console, so it has none |
| `open_timeout` | 20s | the opening of a tunnel, through the hub to the service |
| `idle_timeout` | 2h | as a node's |
| `max_tunnels` | 4096 | the tunnels open at once; a connection past it is closed as it is accepted |
| `console` | none | the proxy's pages |

### A hub a node or proxy links to

| Field | Default | What it is |
|-------|---------|------------|
| `name` | the first endpoint's address | what forwards and the pages name it by |
| `endpoints` | required | `{address, disabled}`, tried in order: `tls://host:port`, `wss://host:port/path` (443 without a port), `quic://host:port`; and `host:port` or `tcp://` for TCP and `ws://host:port/path`, in the clear, which are refused to a host that is not a loopback one unless `body_password` is set. An address is only where to dial: one with a query, a fragment or a user (`?`, `#`, `@`) is refused, by the config and by the pages, since `pin`, `ca_file` and `quic_insecure` alone say how the hub's certificate is checked |
| `token` | | the token the hub issued for this node or proxy, `fitt1.<id>.<secret>`; `token` or `password` is required, not both |
| `password` | | deprecated: the hub's shared password, until the hub issues a token |
| `body_password` | none | the hub's, when it has one |
| `pin` | | the SHA-256 of the hub's certificate, as the hub's page shows it, which tls, wss and quic endpoints check the certificate against in place of the chain and the name: for the hub's self-signed certificate. Its old name, `quic_pin`, is still read, with a warning |
| `ca_file` | | a PEM file of the CAs that sign the hub's certificate, checked with its name, in place of the system's roots; read at each dial; not with `pin` |
| `quic_insecure` | false | a quic endpoint takes any certificate: anyone on the path can then read and change the links. For tests only; tls and wss have no such switch. Refused for a `quic://` endpoint whose host is not a loopback one unless `body_password` is set, as `tcp://` is; `pin` and `ca_file` override it |
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
  needs one and keeps it, so its fingerprint, which nodes and proxies pin, outlives a restart: with
  `admin.store`, in its member (`quic.cert`, `quic.key`), which the store keeps; without one, in a
  file beside the config, mode 0600, named after it (`/etc/fit/fit.tunnel.hub.pem` beside
  `/etc/fit/fit.json`; `fit.tunnel.hub.pem` in a directory of configs), which the hub reads at each
  start and its log names. Back that file up with the config; delete it for a new certificate, which
  every node and proxy must pin again. Where it cannot be written, as beside a read-only config, the
  hub logs that its certificate will change at the next start. Its console shows the fingerprint,
  and each listener's. A listener with `cert_file` and `key_file` presents that certificate instead,
  for nodes and proxies that check it with `ca_file` or the system's roots; the files are read again
  when they change on disk (`fit run -watch-certs`), and the listener opens anew with the new
  certificate. A node or proxy checks the certificate by `pin`, by `ca_file`, or by the system's
  roots, the name included unless pinned. These are given to the dial beside the address, never
  read from it: an endpoint with a query, a fragment or a user is refused, so no address, one
  pasted from elsewhere included, can change how the certificate is checked. It keeps no TLS
  session tickets, so each handshake checks the certificate in full; the pin is checked again in
  the handshake's last check, which a resumed session runs too.
- **Registration with a token** is mutual and bound to the link. Each link of a node or proxy first
  calls `challenge` with its token's ID and a fresh 32-byte nonce of its own, and the hub answers with
  a fresh nonce of its own; the link then registers with a proof of the token over a transcript of
  the token's ID, both nonces and the peer's type and name, and the hub's answer carries the hub's
  proof of the token over the same transcript and the answer, under another key and a label of the
  other direction ([decision 035](decisions/035-link-tokens.md) has the keys). A proof replayed on
  another link, answered to another nonce, or sent back to its sender does not check; the hub keeps
  only a hash of the token, which cannot be used to register. A hub of an earlier release, which
  takes no token, is named in the node's or proxy's status and log as one to upgrade.
- **Registration with the shared password** (deprecated) is mutual too. Each link of a node or proxy registers with an HMAC-SHA256 of the
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
  own, so upgrade them together. A link that registers with a token sends a hello of version 4,
  which names the token's ID after its random bytes; the hub keys the link from that token and the
  body password, by HKDF, with no PBKDF2 and no shared salt, and refuses a registration with another
  token than the hello's. A hub of an earlier release refuses such a hello.

### Tokens

A hub issues a token for each node or proxy that registers with it, in place of a password every
member shares ([decision 035](decisions/035-link-tokens.md)):

- **Each token its own credential.** A token is `fitt1.<id>.<secret>`, a 16-hex-digit ID and 256
  random bits. The hub keeps, in its member's `tokens`, the ID, a name, when it was made, an
  optional expiry, an optional note and a hash, never the token: it is shown once, when it is made.
- **Of no role.** Any node or proxy may register with a valid token; what it may do follows from
  its type and the hub's rules (`allow_proxy_services`) and the node's (`allow_hub_services`).
- **Bound to its members.** The first time a node or proxy registers with a token, the hub binds
  its `node/<name>` or `proxy/<name>` to that token and writes it into the token's `members`, so
  that no other token registers under that name, across restarts. Revoking the token frees it: to
  move a member to a new token, issue the new one, revoke the old, and give the member the new one.
- **Expiry.** A token may expire at a set time or after a duration (`30d`, `720h`); an expired
  token is refused, and the hub closes its links at its expiry (at most a minute late, should the
  wall clock jump). Revoking a token closes its links at once.
- **Managed three ways**, each change a config edit of `tunnel.hub`, applied and kept as any page's
  edit is, and logged to the `admin` logger: the hub's **Tokens** page; the Admin API,
  `GET /tokens/tunnel.hub`, `POST /tokens/tunnel.hub` `{name, note, ttl | expires_at}`,
  `PATCH /tokens/tunnel.hub/{id}` `{ttl | expires_at}` (neither: never) and
  `DELETE /tokens/tunnel.hub/{id}`; and the CLI:

  ```sh
  fit token create -c fit.json -role tunnel.hub -name lab -ttl 90d   # prints the token, once
  fit token list   -c fit.json -role tunnel.hub                      # expiry, last use, members, links
  fit token expiry -c fit.json -role tunnel.hub -id fdd44d906b8e6ce2 -never
  fit token revoke -c fit.json -role tunnel.hub -id fdd44d906b8e6ce2
  ```

  The list gives each token's last use and the address it came from since fit started, its
  members and the links that use it now.

### Upgrading: tokens in place of the password

The shared `password` still registers in this release, beside tokens, and is refused in the next.
The hub's status lists the peers that registered with it (`password_peers`), and its pages warn
while any does. To move:

1. Upgrade the hub first: a node or proxy with a token refuses a hub of an earlier release.
2. Issue a token for each node and proxy, and set it as that member's `token` in place of its
   `password` (its hub editor does both).
3. Once the warning is gone, remove the hub's password (its Settings page, **Remove the shared
   password**), so that only tokens register.

### What protects a link

| Endpoint | The hub proven by | The frames after registration | What an attacker on the path, or at the hub's address, can do |
|----------|-------------------|-------------------------------|------------------------------------------------------------------|
| `tls://`, `wss://`, `quic://` with `pin` or `ca_file` | its certificate (TLS 1.3), and the registration's proof | authenticated and encrypted by TLS | cut the link, see its timing and sizes |
| `tls://`, `wss://`, `quic://` with neither | its certificate, by the system's roots: a hub with a public CA's certificate | as above | as above, as long as no CA the system trusts signs another certificate for the hub's name |
| `tcp://`, `ws://` with `body_password` | the registration's proof, and each frame sealed under the password | each authenticated and its body encrypted (AES-256-GCM); forged, replayed, reordered and crossed frames close the link | as above, and see the frames' kinds and headers |
| `quic://` with `quic_insecure` | the registration's proof, once | not authenticated | read every frame and send its own, through a relay: for tests only, and refused unless the hub's address is a loopback one or `body_password` is set |
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
- An endpoint whose address has a query, a fragment or a user (`?`, `#`, `@`) no longer loads,
  nor can the pages write one: set `pin` or `ca_file` instead of anything in the address.
- `quic_insecure` with a `quic://` endpoint whose host is not a loopback one no longer loads
  unless `body_password` is set (or `pin` or `ca_file`, which override it): pin the hub's
  certificate instead.
- A hub without `admin.store` keeps the self-signed certificate it makes in a file beside the
  config (see [Links](#links)), and so presents the same one after a restart; before, it made a new
  one at each start, and its nodes and proxies had to pin it again. The first start of this release
  makes one more new certificate: pin it once more.
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
not registered, to 64 KiB; its tokens, to 1,024, and a token's members, to 256.

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
  tunnel. Add, change and remove listeners; set the passwords and the console socket, or remove the
  shared password; copy the fingerprint of the hub's certificate, which nodes and proxies pin, and
  see each listener's. **Tokens**: issue one (shown once, to copy), list them with their expiry,
  last use, members and links, change a token's expiry, revoke one; the overview and the Tokens
  page warn while a peer registers with the shared password. Services set here are written into
  the node's own member, when the node's `allow_hub_services` lets them.
- **node**: its hubs, with the protocols each link took, and add, edit and remove them, each with
  the token the hub issued (which replaces a password) or, until the next release, the password; its
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
- `access`, a role's own and the global one, is set again on every apply, the role's member changed
  or not, without a listener opened anew; a connection already open is not checked again. Its
  `allow_files` and `deny_files` are watched as certificate files are.
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
back into the config, and a restart that reads them back from a store. Tokens are tested over TLS,
WebSocket over TLS, QUIC and sealed TCP: a token of any role registers, another token cannot take
a member over, an unknown, forged or replayed proof is refused, an expired token's links close, and
a revoked token's at once. A relay at the hub's address
is tested too: one that passes the bytes on cannot inject a frame over tls or wss, and one with a
certificate of its own does not complete the handshake with a node that pins the hub's. `test/docker/tunnel/fit-tunnel-docker.sh test` runs
hubs, nodes and proxies in containers of their own, with devices on networks only their node
reaches, tests every feature through the consoles and the tunnels, then restarts every instance
and checks that it comes back from its store.
