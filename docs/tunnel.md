# The tunnel

[English](tunnel.md) | [简体中文](tunnel.zh-CN.md)

Config examples, each in .conf, YAML and JSON: [tunnel-server](../examples/tunnel-server/fit.conf), [tunnel-node](../examples/tunnel-node/fit.conf), [tunnel-client](../examples/tunnel-client/fit.conf).

fit's tunnel reaches TCP services on machines that only a host beside them can reach, through a
server anyone can reach. It was a program of its own, fibhub; it is now three roles of fit's config,
which run on the listeners' fib engine beside the gateway's routes, or without any
([decision 028](decisions/028-agent-and-tunnel.md)).

```text
 local app ──TCP──▶ tunnel.client ══link══▶ tunnel.server ══link══▶ tunnel.node ──TCP──▶ service
 (ssh, browser)     a local port per        routes tunnels,         beside the          10.0.0.5:22
                    forward                 passes their frames     services
```

| Role | Member | Runs on | Does |
|------|--------|---------|------|
| server | `tunnel.server` | a host nodes and clients can reach | registers nodes and clients, routes each tunnel a client opens to the node it names, and passes the tunnel's frames between them |
| node | `tunnel.node` | a host on the services' network | links to one or more servers, registers with the services it reaches, and connects to a service for each tunnel routed to it, to no other address |
| client | `tunnel.client` | the user's machine | links to servers, learns their nodes and services, and listens on a local address per forward, each connection there a tunnel |

fibhub called a node a *pod*, and a service a *node*. Nodes and clients link **out** to servers, so
neither needs an open port. A process may run any of the roles, all three included, with the
gateway or alone.

The tunnel relays every byte through a server, and works today wherever nodes and clients can
reach one. The planned [P2P milestone](decisions/025-p2p.md) is the other way: ICE connects two
hosts behind NAT directly, through holes punched in both NATs, carries TCP and UDP services, and
falls back to a TURN relay only where no hole can be punched.

## Running a role

A role runs while its member is set. A config with roles and no routes needs no listener:

```json
{
  "tunnel": {
    "server": {
      "name": "hub",
      "password": "a-registration-password",
      "body_password": "a-body-password",
      "listeners": [
        {"protocol": "tcp", "address": ":7000"},
        {"protocol": "ws", "address": ":7001"},
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
      "servers": [{
        "name": "hub",
        "endpoints": [
          {"address": "quic://hub.example.com:7443"},
          {"address": "ws://hub.example.com:7001/fibhub"},
          {"address": "hub.example.com:7000"}
        ],
        "password": "a-registration-password",
        "body_password": "a-body-password",
        "quic_pin": "the fingerprint on the server's console"
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
    "client": {
      "name": "alice",
      "servers": [{
        "name": "hub",
        "endpoints": [{"address": "hub.example.com:7000"}],
        "password": "a-registration-password",
        "body_password": "a-body-password"
      }],
      "forwards": [
        {"listen": "127.0.0.1:2222", "node": "lab", "service": "ssh"}
      ]
    }
  }
}
```

Then `ssh -p 2222 127.0.0.1` reaches 10.0.0.5:22. `fit validate` checks the members; the
[config reference](reference/config.md) lists every field, its default and its bounds.

### tunnel.server

| Field | Default | What it is |
|-------|---------|------------|
| `name` | the host's name | the name nodes and clients are told |
| `password` | none: no registration is taken | what nodes and clients register with, proven with an HMAC, never sent |
| `body_password` | none | AES-256-GCM on every frame's body; nodes and clients need the same |
| `listeners` | none: the pages add them | `{protocol: tcp\|ws\|quic, address, path}`; a ws listener's path is `/fibhub` by default, and only a ws listener has one |
| `console_socket` | none | `{address, password, max_conns}`: the console over an encrypted WebSocket, for clients' `web_proxies`, answering on any path; it needs `console`, its password is the server's when left out, and `max_conns` is 32 |
| `quic` | | `{cert, key, idle_timeout, keep_alive, handshake_timeout, max_handshakes}`, 30s, 10s, 10s and 64 when left out; the server makes a self-signed certificate and writes it here when a quic listener needs one; `cert` and `key` go together |
| `register_timeout` | 10s | how long a link may take to register |
| `call_timeout` | 15s | how long a node may take to answer: to open a tunnel, list or set its services |
| `max_links`, `max_links_per_peer`, `max_peers`, `max_tunnels` | 1024, 128, 1024, 16384 | the bounds of what the server keeps |
| `console` | none | the server's pages (see [Consoles](#consoles)) |

### tunnel.node

| Field | Default | What it is |
|-------|---------|------------|
| `name` | the host's name | the name clients reach the node by |
| `meta` | none | anything else the node tells the servers |
| `servers` | none | the servers it links to (below) |
| `services` | none | `{name, address, description}`: what the node reaches, and the only addresses it connects to |
| `dial_timeout` | 5s | a connect to a service |
| `idle_timeout` | 2h | a tunnel that carries nothing either way for this long is closed |
| `max_tunnels` | 4096 | the tunnels open at once, over every server |
| `console` | none | the node's pages |

### tunnel.client

| Field | Default | What it is |
|-------|---------|------------|
| `name`, `meta`, `servers` | | as a node's |
| `forwards` | none | `{listen, node, service, server}`: without `server`, the tunnels spread over every server that has the node, more of them through the servers whose path to the node is faster |
| `web_proxies` | none | `{name, listen, url, password, max_conns}`: a server's console served locally through its `console_socket`, whose `ws://host:port/` is `url`; `max_conns` is 32 |
| `open_timeout` | 20s | the opening of a tunnel, through the server to the service |
| `idle_timeout` | 2h | as a node's |
| `max_tunnels` | 4096 | the tunnels open at once; a connection past it is closed as it is accepted |
| `console` | none | the client's pages |

### A server a node or client links to

| Field | Default | What it is |
|-------|---------|------------|
| `name` | the first endpoint's address | what forwards and the pages name it by |
| `endpoints` | required | `{address, disabled}`, tried in order: `host:port` or `tcp://` for TCP, `ws://host:port/path`, `quic://host:port` |
| `password` | required | the server's |
| `body_password` | none | the server's, when it has one |
| `quic_pin`, `quic_insecure` | | how a quic endpoint checks the server's certificate: its SHA-256, as the server's page shows it, or not at all, for tests; the system's roots without either |
| `pool_size` | 10, at most 100 | how many links to the server carry tunnels |
| `dial_timeout` | 5s | a link's dial |
| `call_timeout` | 10s | the server's answer to a registration or a call |
| `retry_interval` | 3s | the wait after a failed dial, or a lost server, before a link dials again |
| `ping_interval`, `ping_timeout` | 5s, 5s | how often the round trip to the server is measured, and how long a ping may go unanswered before it counts as lost |

## Links

A node or client keeps `pool_size` links to each server. Each dials the server's first endpoint
not disabled, falls back to the next when that fails, registers, and dials again `retry_interval`
after a failed dial or after it loses the server. The links of one name and type, from however many processes, are one
peer to the server. Each tunnel goes over one link throughout, so that its frames keep their
order; a new tunnel takes the link that carries the fewest, and a link carries at most 64, so that
a peer that needs more opens a larger pool.

- **TCP** and **WebSocket** links are connections of the engine. A WebSocket link is for networks
  that let only HTTP through, as through nginx's `proxy_pass` with `Upgrade`; there is no `wss://`,
  so set `body_password`.
- **QUIC** links run on a UDP binding of the same engine, with fib's QUIC, TLS 1.3 and the ALPN
  `fibhub`. A server makes a self-signed certificate when a quic listener needs one and keeps it in
  its member, so its fingerprint, which nodes and clients pin, outlives a restart.
- **Registration** is an HMAC-SHA256 of the password over the peer's type, name, a time and a
  nonce; the time must be within 5 minutes of the server's clock, and a nonce is taken once.
- **Body encryption**, with `body_password` on every side, seals each frame's body with AES-256-GCM,
  its header authenticated too; a frame that is not sealed, or does not open, closes the link.

The wire protocol is fibhub's, names included: a node registers as a `pod`, and its services are
its `nodes`, so that fit's roles and fibhub's can link to each other while a setup moves over.

### Flow, limits and timeouts

Each end of a tunnel sends at most 4 MiB the other end has not acknowledged, and holds its TCP
connection's reads once it has; the other end acknowledges what its own connection has taken. A
slow service holds back the client that writes to it, and a slow client the service, one tunnel at
a time, with no more than a window and the sockets' buffers between them. What waits for a link is
bounded by the windows of its tunnels. The console socket and web proxies hold one side's reads
while the other side's backlog passes 256 KiB.

Every dial, registration and call has its timeout; a link's TCP connection is probed after 30s of
quiet, and closed when its peer stops answering; a QUIC connection pings every `keep_alive` and
closes after `idle_timeout`; a tunnel idle past `idle_timeout` closes. The server bounds its links,
peers and tunnels, a peer's links, the QUIC handshakes under way, and the frames of a link that has
not registered, to 64 KiB.

## Consoles

Each role's `console` serves its pages, behind logins as `admin.console`'s are
([the console](console.md)): `{address, cert_file, key_file, users, session_timeout,
session_lifetime, max_sessions, max_conns}`, users with bcrypt hashes from `fit hash-password`, a
session cookie and a CSRF token, and a loopback address unless `cert_file` and `key_file` are set.

- **server**: the nodes, with their services and latency, and the clients; the tunnels, with their
  bytes each way and their paths' latency. Query a node, set its services, kick a peer, close a
  tunnel. Add, change and remove listeners; set the passwords and the console socket; copy the
  QUIC certificate's fingerprint. Services set here are written into the node's own member.
- **node**: its servers, with the protocols each link took, and add, edit and remove them; its
  services, and save them, every server told; its tunnels, and close one.
- **client**: its servers, edited as a node's, their nodes and services, with each server's share of
  a node's tunnels; query a node, and set its services through a server; add and remove forwards;
  its tunnels, and close one; the meta it tells the servers; and each server's console through its
  web proxy.

What a page changes is written into the role's member of the config, with `app.Host.Edit`, and
applied from there as any config is: it is in the config's history, and with `admin.store` it is
kept, encrypted, in the store, and read back at the next start ([the console](console.md)
describes the store). Without a store, the change runs until fit restarts or reads its files
again, and the page says so. Every page has a tunnel map,
**Details**, and the theme and language pickers.

A server's console can be reached where it cannot be directly, through an encrypted WebSocket: set
the server's `console_socket`, and a client's `web_proxies` entry with its URL and password serves
the console on a local address. The WebSocket's messages are AES-256-GCM sealed with keys from the
password; the console's own login still applies.

## Moving from fibhub

| fibhub | fit |
|--------|-----|
| `fibhub -mode=server\|pod\|client` | the member `tunnel.server`, `tunnel.node` or `tunnel.client`, in any combination |
| a pod, its nodes | a node, its services |
| a config file of the web page's settings, the rest in the `FIBSTORE` store | every setting in the member, and in fit's store with `admin.store` |
| `webAddr`, `webAuth`, `/login` | `console`, fit's logins; a forward's or web proxy's `webAuth` is gone |
| `listeners[].proto`, `addr`, `path`; `addr`, `linkWsAddr`, `listen` | `listeners[].protocol`, `address`, `path` |
| `wsAddr`, `wsPassword` | `console_socket.address`, `password` |
| `quicCert`, `quicKey` | `quic.cert`, `quic.key` |
| a server's `endpoints[].proto`, `addr`, `enabled`; `addr` | `endpoints[].address` with its scheme, `disabled` |
| `quicPin`, `quicInsecure`, `bodyPassword`, `poolSize` | `quic_pin`, `quic_insecure`, `body_password`, `pool_size` |
| `forwards[].pod`, `node` | `forwards[].node`, `service` |
| `webProxies` | `web_proxies` |
| `-print-config`, flags, `FIBHUB_*` | none: the config is written as any of fit's is |

To move a setup over, write each role's member from its fibhub settings, as above, and load it, or
put it in a store with `fit store import`. A server's `quicCert` and `quicKey` copied into `quic`
keep its fingerprint, so nodes and clients pinned to it link on. Since the wire protocol is
fibhub's, a fit server can serve fibhub's pods and clients, and a fit node or client link to
fibhub's server, while the rest move; fit's tests run fit's roles against each other only.

What behaves differently:

- Everything runs on fit's engine: no engine, socket or goroutine of the tunnel's own for QUIC, and
  the gateway's `limits` apply to the roles' connections too.
- A role moves to a new member without dropping what is unchanged: a server's links, a node's or
  client's links to a server whose settings are the same, a forward on the same address.
- The pages write into the config; a listener or forward that cannot listen is kept in the config,
  shown with its error, and tried again with the next config.
- New bounds and timeouts: the server's `max_links`, `max_links_per_peer`, `max_peers` and
  `max_tunnels`, 64 tunnels a link, the roles' `max_tunnels` and `idle_timeout`, TCP keep-alive on
  links and tunnels, a QUIC connection that carries no link closed after its handshake timeout.
- The pages are behind fit's logins and its Content-Security-Policy, and no longer show or keep the
  logins of other pages.

## Testing

`go test -race ./tunnel/...` runs the roles through fit's config, as `fit run` does, on a real
engine: tunnels over TCP, WebSocket and QUIC, with and without body encryption, large transfers,
slow services and clients, cut connections, the pages' changes written back into the config, and a
restart that reads them back from a store. `test/docker/tunnel/fit-tunnel-docker.sh test` runs
servers, nodes and clients in containers of their own, with devices on networks only their node
reaches, tests every feature through the consoles and the tunnels, then restarts every instance
and checks that it comes back from its store.
