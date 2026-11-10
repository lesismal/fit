# Tunnel roles

[English](24-tunnel.md) | [简体中文](24-tunnel.zh-CN.md)

## What it does

The tunnel lets people reach TCP services on machines only a host beside them can connect to, through a
hub anyone can reach. It is three roles in fit's config, run on the engine of the listeners, beside the
gateway's routes or on their own:

```text
  local app ──TCP──▶ tunnel.proxy ══link══▶ tunnel.hub ══link══▶ tunnel.node ──TCP──▶ service
  (ssh, browser)     one local port per        routes tunnels,      beside the service      10.0.0.5:22
                     forward                   forwards tunnel frames
```

The **hub** registers nodes and proxies, routes each tunnel a proxy opens to the node it names, and
forwards the tunnel's frames between the two ends. A **node** links to one or more hubs, registers with the
services it can reach, and for each tunnel routed to it connects that service — it connects no other
address. A **proxy** links to hubs, learns their nodes and services, and listens on a local address per
forward: every connection there is a tunnel. Nodes and proxies both dial out to the hub, so neither needs
an open port. Every byte of a tunnel goes through the hub.

The links are TLS, WSS or QUIC; plain TCP and WebSocket are only for a loopback address, or need
`body_password`. Each node and proxy registers with a `token` the hub issued: the hub's `tokens` keep only
the hashes, and the token itself is shown once, when it is made (`fit token create -role tunnel.hub -name
lab`, or the hub's **Tokens** page). The shared `password` is still taken in this release but deprecated,
and the pages warn while a peer still uses it.

## Pages

A role's pages appear in the menu once the role runs: set its member on the **Tunnel roles** page, and the
module shows under the Tunnel group when the config is applied. They show in the `admin.console` workbench
with no more setting; a role's own `console` serves the same pages alone, at an address and with users of its
own, for a machine without `admin.console`. What these pages change is written back
into the role's member and applies at once, without a restart, and is kept encrypted when fit keeps its
config in `admin.store`; without a store a change holds only until fit restarts or reads the config file
again, and the pages say so. The pages that show status refresh every 3 s; the settings forms load once and
after a change. The **Overview** and **Tunnels** pages carry a **Tunnel map** tool, and a tunnel's
**Details** opens its whole path there.

### Tunnel roles

Edits the `tunnel` member: a form each for `hub`, `node` and `proxy`, with the raw tabs (UI, .conf, .yaml,
.json) holding the whole member. The page notes that a role set here starts when the config is applied,
stops when it is left out, and that its own pages show in the menu once it runs.

### tunnel.hub

#### Overview

Four stats: the nodes (with how many services they have), the proxies (with how many are forwarding), the
tunnels (the bytes up and down), and the listeners (how many are failing, or "all open"). The **This hub**
card gives the name, the version, the peer addresses, the console socket, the body encryption, the
registered links and the uptime. The **Listeners** table gives each listener's protocol and the address
peers dial, with its error when it failed; a row links to the **Listeners** page. Below are the last ten
tunnels. While a peer still registers with the shared password the page warns and links to **Tokens**.

#### Nodes

One row per registered node: its ID, name, address (and its links), latency, host (with its OS and
version), its services (name → address, description and the connect time), its tunnels and how long it has
been linked. The actions are **Query** (ask the node to report its services again), **Edit services**
(which writes into the node's own member only when its `allow_hub_services` allows it) and **Kick**.

#### Proxies

One row per registered proxy: its ID, name, address and links, latency, host, the meta it told the hubs,
its tunnels and how long it has been linked; the action is **Kick**.

#### Tunnels

Each tunnel: its ID, its proxy, its node, its service, its address, the latency of the whole path (proxy to
hub, hub to node, plus the node's connects to the service, the parts in the tooltip), the bytes up and
down, its age, and **Details** (the tunnel's whole path) and **Close**.

#### Listeners

Add, edit and remove a listener: the protocol (`wss`, `tls`, `quic`, `ws` or `tcp`), the address, and the
path of a ws or wss listener (`/tunnel` by default). Changes apply at once, without a restart; removing a
QUIC listener closes the links it carries, and other listeners' links stay open until they close. Each row
shows the listener's fingerprint (SHA-256), with a badge for the hub's own certificate. The page notes that
TLS, WSS and QUIC listeners authenticate the hub to nodes and proxies that pin its certificate, and that
TCP and WS carry the links in the clear: off a loopback address they need the body password.

#### Tokens

The tokens this hub issued. **Issue token** takes a name, a note and a lifetime (`720h`, `30d`) or an expiry
time, none for never; the token is shown once, to copy into a node's or proxy's `token`. The table lists each
token's name, ID, expiry, the last use since fit started and its address, its bound members and the links using
it now; **Expiry** changes the expiry, and **Revoke** closes its links at once and frees its members. A member is
bound to the first token it links with, and no other token may link as it. A warning shows while a member still
registers with the shared password.

#### Settings

**Passwords and console socket**: the shared registration password (deprecated, with **Remove the shared
password**: only tokens register), the body AES password (removable), the console socket's password (the
registration password when left out) and its address (empty for none, as `:7002`); **Save settings** applies
them. A password already set never comes back: leaving it
blank keeps it. Registered peers stay linked; a new body password applies to the links opened from now on.
**Hub certificate**: the fingerprint (SHA-256), with copy — nodes and proxies pin this fingerprint as their
hub's certificate pin; it is made, and written into the config, when a TLS, WSS or QUIC listener is added.

### tunnel.node

#### Overview

Four stats: the hubs (registered of the total), the services, the tunnels (in and out), and the open
tunnels (now of the max). The **This node** card gives its name, version, tunnels and meta; the hubs table
gives each hub's name, state, latency and tunnels, and a row links to the **Hubs** page.

#### Hubs

One row per hub: its name, its endpoints (in the order they are tried, those disabled struck out), its
state with the error under it, the protocols its links took, whether its bodies are encrypted, its latency,
the hub's name, its Peer ID and pool (now of the size), and its tunnels; the actions are **Edit** and
**Remove**. The editor fills in the name, the pool size, the token the hub issued (a token replaces the
password, and a password the token), the body password, the hub certificate pin (SHA-256), the addresses by
protocol in the order they are tried with each one turned on, and the testing-only "accept any QUIC
certificate".

#### Services

The services this node reaches: name, address and description, edited in place, each row showing its
connect time. **Save and tell hubs** writes them into the node's own member and tells every hub;
**Revert** drops the unsaved changes, and a refresh does not overwrite what has been changed. These
addresses are also the only ones the node opens tunnels to.

#### Tunnels

Each tunnel: its hub, ID, proxy, service, address, connect time, its local address, the bytes in and out,
its age, and **Details** and **Close**.

### tunnel.proxy

#### Overview

Four stats: the hubs (registered of the total), the nodes (and their services), the forwards (how many are
failing, or "all listening"), and the tunnels (sent and received). The **This proxy** card gives its name,
version, tunnels and the info it tells the hubs; the **Forwards** table lists each forward.

#### Hubs

A card per hub: its endpoints, its state and the protocols its links took, its latency, the hub's name, its
Peer ID, its pool, its nodes, its tunnels and its body encryption, and its **Console** — the local address
its web proxy serves, the URL it goes through, its connections and **Open**. A card's buttons are **Query
nodes** (ask the hub for its nodes again), **Edit** and **Remove**; **Add hub** in the top bar opens the same
editor as the node's. A web proxy that is no hub's gets a card of its own.

#### Nodes and services

One row per node on each hub: the hub, the node, the latency to it through that hub, the share of that
node's tunnels this hub takes, and its services (name → address, description and connect time), each with a
**Forward…** button that fills in the form, and **Edit services**, which the hub and the node take only when
both the hub's `allow_proxy_services` and the node's `allow_hub_services` allow it.

#### Forwards

Each forward: its local address, its node, its service, its hub (when left out the tunnels are spread over
every hub that has the node, more of them through the faster ones) and its state. Add a forward, **Open**
its local address, or **Remove** it.

#### Tunnels

Each tunnel: its hub, ID, local address, node, service, address, latency, the local proxy (the address the
connection came from), the bytes sent and received, its age, and **Details** and **Close**.

#### Proxy info

The info told to the hubs, as key/value rows; **Update hubs** sends it, and the hubs show these pairs on
their Proxies page.

## Key parameters

The key parameters; the full tables are in [the config reference](../reference/config.md#tunnel).

### tunnel.hub

| field | default | description |
| --- | --- | --- |
| `name` | the host's name | The name the hub tells the nodes and proxies |
| `tokens` | none | The tokens the hub issued, kept as hashes only (`{id, name, hash, created_at, expires_at, note, members}`); made, listed, changed and revoked by the pages, the Admin API or `fit token` — not written by hand |
| `password` | none | The deprecated shared registration password; still taken in this release, with a warning while a peer uses it, and refused in the next. Without it, only tokens register |
| `body_password` | none | Encrypts the body of every frame with AES-256-GCM and authenticates the frame; a `tcp` or `ws` listener on an address that is not a loopback one needs it. A link registered with a token derives its keys from the token and this, by HKDF |
| `listeners` | none | `{protocol, address, path, cert_file, key_file}`: `tls`, `wss`, `quic`, or the plain `tcp` and `ws`; the path of a ws or wss listener is `/tunnel`; without `cert_file` the listener presents the hub's own certificate |
| `access` | by the global `access` | The client addresses the hub's listeners and its `console_socket` take |
| `console_socket` | none | `{address, password, max_conns}`: the console carried over an encrypted WebSocket, for a proxy's `web_proxies`; needs `console` |
| `quic` | see the reference | The hub's certificate (`cert`, `key`) and the QUIC limits and timeouts; when a listener needs a certificate the hub makes a self-signed one and keeps it, so its fingerprint outlives a restart |
| `register_timeout`, `call_timeout` | 10s, 15s | How long a link may take to register, and how long a node may take to answer |
| `max_links`, `max_links_per_peer`, `max_peers`, `max_tunnels` | 1024, 128, 1024, 16384 | The resources the hub bounds |
| `max_unregistered`, `max_unregistered_per_ip` | 256, 32 | The links open that have not registered yet, in total and from one IP address |
| `allow_proxy_services` | false | Lets registered proxies set nodes' services through the hub; the node still needs its own `allow_hub_services` |
| `console` | none | A console of the hub's own: its pages alone, at their own `address` with their own `users`; not needed where `admin.console` runs |

### tunnel.node

| field | default | description |
| --- | --- | --- |
| `name` | the host's name | The name proxies use to find this node |
| `meta` | none | Anything else the node tells the hubs |
| `hubs` | none | The hubs the node links to (see below) |
| `services` | none | `{name, address, description}`: the services the node reaches, and the only addresses it opens tunnels to |
| `allow_hub_services` | false | Lets its hubs set `services` and write them into this member |
| `dial_timeout`, `idle_timeout`, `max_tunnels` | 5s, 2h, 4096 | The time a connect to a service may take, the idle time past which a tunnel is closed, and the tunnels open at once |
| `console` | none | A console of the node's own, as the hub's |

### tunnel.proxy

| field | default | description |
| --- | --- | --- |
| `name`, `meta`, `hubs` | | As the node's |
| `forwards` | none | `{listen, node, service, hub}`: without a `hub` the tunnels are spread over every hub that has the node |
| `web_proxies` | none | `{name, listen, url, password, max_conns}`: serves a hub's console pages locally through its `console_socket` |
| `access` | by the global `access` | The client addresses the proxy's forwards and web proxies take |
| `open_timeout`, `idle_timeout`, `max_tunnels` | 20s, 2h, 4096 | The time opening a tunnel may take, the idle time past which a tunnel is closed, and the tunnels open at once |
| `console` | none | A console of the proxy's own, as the hub's |

### A hub a node or proxy links to (`hubs[]`)

| field | default | description |
| --- | --- | --- |
| `name` | the first endpoint's address | The name the forwards and the pages use for it |
| `endpoints` | required | `{address, disabled}`, tried in order: `tls://`, `wss://` (path `/tunnel` by default) and `quic://`; the plain `host:port`, `tcp://` and `ws://` need `body_password` to a host that is not a loopback one |
| `token` | one of the two | The token the hub issued for this member, `fitt1.<id>.<secret>`; set `token` or `password`, not both |
| `body_password` | none | The hub's body password, when it has one |
| `pin` | none | The SHA-256 fingerprint of the hub's certificate, as its pages show it, checked in place of the chain and name checks; not with `ca_file` |
| `ca_file` | none | A PEM file of the CAs that sign the hub's certificate, checked against, its name too |
| `quic_insecure` | false | Takes any QUIC certificate: for tests only; a non-loopback host needs `body_password` |
| `pool_size` | 10, at most 100 | The links to the hub that carry tunnels; each tunnel goes over one throughout |
| `dial_timeout`, `call_timeout`, `retry_interval`, `ping_interval`, `ping_timeout` | 5s, 10s, 3s, 5s, 5s | The dial, the answer, the wait before dialing again, and how often the round trip is measured and when a ping counts as lost |

## In .conf

```fit
tunnel {
	hub {
		listeners { protocol wss; address :7443 }
		tokens lab {
			id fdd44d906b8e6ce2
			hash 1c994a095de7b566dcce0f229b829af96d85911b6d3e786f217c77340bc36aa4
			created_at 1793900000
		}
	}
	node {
		name lab
		hubs hub {
			endpoints { address wss://hub.example.com:7443/tunnel }
			token fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ
			pin 02e5420d38831f019c363d82b2d7ee2ff7db1a143401587e3b9a92f0011e8261
		}
		services ssh { address 10.0.0.5:22 }
	}
}
```

Do not write `tokens` or `pin` by hand: the hub's **Tokens** page issues each token (shown once) and the
hub's **Settings** page shows its certificate fingerprint; put both into the node's `token` and `pin`. A
config with only the roles and no routes needs no listener.

A proxy on a laptop, with a token of its own from the same hub, and one forward:

```fit
tunnel {
	proxy {
		name laptop
		hubs hub {
			endpoints { address wss://hub.example.com:7443/tunnel }
			token fitt1.3b7a0c9e1d2f4a56.q8Xc2Lr0VwN5bTz7Ka1MdYp4Hs9GfJu3EoWi6RnBtQk
			pin 02e5420d38831f019c363d82b2d7ee2ff7db1a143401587e3b9a92f0011e8261
		}
		forwards { listen 127.0.0.1:2222; node lab; service ssh }
	}
}
```

`ssh -p 2222 127.0.0.1` then reaches 10.0.0.5:22 through the hub. A forward on a host that is not a loopback one
serves the LAN too; the proxy's `access` limits who may use it. The full fields are in
[the config reference](../reference/config.md#config-tunnelproxy).

## See also

- [The tunnel](../tunnel.md): running a role, the links (TLS, WSS, QUIC and the plain ones),
  [tokens](../tunnel.md#tokens), [what protects a link](../tunnel.md#what-protects-a-link), the
  [consoles](../tunnel.md#consoles), and the flow, limits and timeouts.
- [The config reference: Tunnel](../reference/config.md#tunnel) and
  [the console and the store](../console.md#the-pages).
- [The function navigation bar](03-navigation.md): the roles' dynamic modules.
- [WebRTC](23-webrtc.md): the TURN server, which relays the UDP datagrams of ICE agents; a tunnel forwards
  TCP services instead.
- [LLM roles](25-llm.md): the same three roles of the menu, over the same kind of links and tokens.
- The page code: [app/workbench/web/tunnel/hub.js](../../app/workbench/web/tunnel/hub.js),
  [node.js](../../app/workbench/web/tunnel/node.js), [proxy.js](../../app/workbench/web/tunnel/proxy.js)
  and [shared.js](../../app/workbench/web/tunnel/shared.js).
