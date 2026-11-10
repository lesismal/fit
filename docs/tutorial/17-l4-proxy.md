# L4 proxy

[English](17-l4-proxy.md) | [简体中文](17-l4-proxy.zh-CN.md)

## What it does

The L4 proxy does not look at the application layer: `services` forward the connections of `tcp`
listeners byte for byte to an upstream, and the datagrams of `udp` listeners one for one. A `service`
says three things — which listeners (`listeners`), which upstream (`upstream`), and optionally
`server_names`, `proxy_protocol`, `access`, `idle_timeout` and `max_conns`. It terminates no TLS.

The upstream's address is picked by the upstream's own `load_balancing`: `ip_hash` by the client's
address; `header`, `cookie` and `uri_hash` have nothing to look at on a connection, so they are tried
in turn. The passive and active health checks, `max_fails` and `connect_timeout` are used as they are
for HTTP; a connect that fails is tried on the next address picked, up to three addresses. The
upstream's `protocol` and its HTTP settings are not used.

`server_names` splits one port between several services: only a TLS connection whose ClientHello asks
for a name (the SNI) that lies in a service's `server_names` goes to that service; `*.example.com`
takes the names one label below `example.com`. A `tcp` listener may be listed by several services: a
connection goes to the one whose names hold the name it asks for, or else to the service without
`server_names` (which also takes a connection that is not TLS or asks for no name), and if there is
none, the connection is closed. The ClientHello must come within 10s. These TLS connections are
proxied as they are: fit does not decrypt them, so the certificates that answer them are the
upstream's side's business.

`proxy_protocol` tells the upstream who the client was: each connection to the upstream first sends a
PROXY protocol header of version v1 (text) or v2 (binary), naming the client's address and the one it
connected to. For `tcp` listeners only. A listener's `proxy_protocol` reads the header, a service's
writes one, and the two can be stacked: read a header from a trusted address, then send one to the
upstream.

Services and routes do not touch each other: on one port, an HTTP request goes to a route and a TCP
connection to a service. A service looks at no method, path or header, so the middleware on routes
does nothing to an L4 connection; and a service has no `match` — it only knows its listener and its
upstream.

When to use a service: the backend is not HTTP — Postgres, DNS, the TCP ports of a game or an IM
server — or the TLS must pass through as it is, not decrypted on fit (MQTT over TLS, splitting to
different backends by SNI). A service parses no protocol, so it knows nothing about content: it cannot
split by path, rewrite headers, or cache.

## Pages

### Layer 4 services

The L4 proxy module has one page in the menu: Layer 4 services. It edits the `services` member of the
config.

- Each service is a row: its name, with the small tags `@` for the listeners it takes, `→` for its
  upstream, and a badge when `proxy_protocol` is set.
- "Add service" makes a service whose `listeners` is the first `tcp` or `udp` listener in the draft,
  whose `upstream` is the first upstream, and whose name is `service-1`, `service-2`, …
- A row opens its form: `name`, `listeners`, `upstream`, `idle_timeout`, `max_conns`, `server_names`,
  `access` and `proxy_protocol`, edited field by field.
- The page edits the shared draft: the form and the `.conf`, `.yaml` and `.json` tabs are the same
  draft, and Save and apply checks and applies it.
- The listeners themselves are edited on the [Listeners](05-listeners.md) page and the upstreams on the
  [Reverse proxy](15-reverse-proxy.md) page; this page only joins the two.

### On the Engine page

The [Engine](06-engine.md) page lists the features a port serves: under this port it lists "Layer 4
services" with a count, which links back to this page. The tags of the port row name the protocols and
the `proxy_protocol`; when a port is used by HTTP, tcp and socks at once, the page lists each of them.

### What the page does not do

- The page listens on nothing and picks no address: `listeners` is only names pointing at listeners
  edited elsewhere, and writing a listener's `protocols` as `tcp` or `udp` is done on the
  [Listeners](05-listeners.md) page.
- The page shows no running counts: the connections fit holds are on the [Overview](04-overview.md)
  page, and the services themselves on the [Overview](04-overview.md) and [Engine](06-engine.md)
  pages.

## Key parameters

Only the key parameters; the full field table is in
[the config reference: Services](../reference/config.md#services)
([`services[]`](../reference/config.md#config-service)).

| field | default | description |
| --- | --- | --- |
| `name` | required | A name unique among the services. |
| `listeners` | required | The names of the listeners whose connections the service takes: those that serve `tcp`, alone or beside other protocols, and `udp` listeners, each one service's. |
| `upstream` | required | The name of the upstream connections go to: one of its addresses, picked by its `load_balancing`, out of rotation for its `max_fails` and health checks, dialed within its `connect_timeout`; a failed connect is tried on the next address picked, up to three. |
| `server_names` | none | The server names (SNI) of the TLS connections the service takes from its `tcp` listeners; `*.example.com` takes the names one label below example.com. Proxied as they are, without terminating their TLS. |
| `proxy_protocol` | none | Send each upstream connection a PROXY protocol header of this version (`v1`, text, or `v2`, binary) first, naming the client's address and the one it connected to. For `tcp` listeners. |
| `idle_timeout` | `10m` for a connection, `1m` for a `udp` session | How long a connection or a session may carry nothing either way before both sides are closed. |
| `max_conns` | `10000` | The most connections and `udp` sessions the service proxies at once; a connection past it is closed as soon as it is accepted, and the datagrams of a session past it are dropped. |
| `access` | inherited | The client addresses the service takes, deciding before its listener's `access` and the global one: a connection it refuses is closed once its PROXY header and ClientHello are read when the listener needs them, and the datagrams of a `udp` session it refuses are dropped. |

### Counts and limits

`max_conns` counts the connections and sessions one service proxies at once: a connection past it is
closed as soon as it is accepted, and the datagrams of a session past it are dropped. `idle_timeout`
counts the time with nothing either way: while either side still sends, the connection is not idle. The page shows none of these
numbers: the connections fit holds are counted on the [Overview](04-overview.md) page.

### What validation refuses

- a `tcp` or `udp` listener that no service lists, and a service listener that is neither: a service takes
  listeners whose `protocols` include `tcp`, or are `udp` alone;
- a `udp` listener listed by two services, or by a service with `server_names` or `proxy_protocol`;
- two services without `server_names` on one `tcp` listener, or one server name in two services on it;
- an IP address in `server_names`: a ClientHello names host names;
- an `upstream` or a listener that does not exist.

### What is not used

The upstream's `protocol`, its headers, its HTTP timeouts and its `sticky_cookie` play no part: a
connection has no HTTP fields to look at. Only the picking strategy, `max_fails`, the health checks and
`connect_timeout` are used.

### Health checks at layer 4

The health checks are used as they are for HTTP, but their `type` can be `tcp`: a probe passes once a
connection opens. For an upstream whose backends are only TCP (Postgres, DNS), `type: tcp` is the probe
that makes sense; an `http` probe sends a GET, which means nothing to such a backend. The passive
`max_fails` counts failed dials.

### A udp session

One `udp` listener belongs to one service. A session is a client's address: the datagrams it sends go
to one upstream address, from a socket of the session's own, and what comes back to that socket goes to
the client. That is why `idle_timeout` is much shorter for a session than for a connection, why
`max_conns` counts sessions, and why a session past it has its datagrams dropped rather than being
closed.

### One port, several services

A port whose `protocols` name `tcp` is told apart by what the client sends first: a TLS ClientHello
goes to the service whose `server_names` hold its name, or else, with the listener's `tls`, is served
HTTPS, or else goes to the service without `server_names`; a SOCKS greeting goes to socks, an HTTP
request in the clear to HTTP, and whatever else to the service without `server_names`, or it is closed.
A client that waits for the server to speak first is closed after 10s: such a protocol needs a listener
of its own.

## In .conf

Three listeners, four upstreams and four services: Postgres with a TCP health check, DNS over `udp`, and
one port, `:8443`, split by SNI between a TLS pass-through for `mail.example.com` and a catch-all service
that sends a PROXY header. This is a self-contained complete config, and it passes `fit validate`:

```fit
listener pg :5432 tcp
listener dns :53 udp
listener tls :8443 tcp

upstream db 10.0.0.5:5432 10.0.0.6:5432 {
	health_check {
		type tcp
		interval 10s
	}
}
upstream resolvers 10.0.0.53:53 10.0.0.54:53
upstream mail 10.0.0.25:8443
upstream web 10.0.0.30:8443 10.0.0.31:8443 {
	load_balancing ip_hash
}

service postgres { listeners pg; upstream db; idle_timeout 30m }
service dns { listeners dns; upstream resolvers }
service mail { listeners tls; upstream mail; server_names mail.example.com }
service web { listeners tls; upstream web; proxy_protocol v2; max_conns 20000 }
```

A config that writes no listener — and no route matching a host name, for which HTTPS by default
would make listeners — fails validation: an upstream does not listen by itself.

On `:8443`, a connection whose ClientHello asks for `mail.example.com` is proxied as it is to
`10.0.0.25:8443`; every other one — another name, no name, or not TLS at all — goes to `web`, the
listener's one service without `server_names`, which keeps a client on one of its two addresses by its IP
address and sends each connection a PROXY v2 header first. The upstream must expect that header (nginx's
`listen ... proxy_protocol`, HAProxy's `accept-proxy`): a server that does not, as Postgres, reads it as
garbage, so `postgres` sends none.

In form, `listener pg :5432 tcp` is the short way of writing `listener { name pg; address :5432;
protocols tcp }`, and `upstream db 10.0.0.5:5432 10.0.0.6:5432 { ... }` takes its `addresses` as
arguments; a `service` takes only its name as the argument, and its other fields go in the block.

## See also

- [Listeners](05-listeners.md): the `tcp` and `udp` protocols, a listener's `proxy_protocol` and `access`.
- [Reverse proxy](15-reverse-proxy.md): the upstream's `load_balancing`, `max_fails` and health checks.
- [Engine](06-engine.md): where a port lists the services.
- [the config reference: Services](../reference/config.md#services): every field of `services`.
- [the config reference: Upstreams](../reference/config.md#upstreams): every field of an upstream.
- [the config reference: Upstreams' health check](../reference/config.md#config-healthcheck): a `type: tcp` probe.
- [Overview](04-overview.md): the counts of connections and the layer 4 services.
- [the services module doc](../../schema/doc/services.md): what the member does, with an example.
- [the complete config template](../../templates/conf/conf.d/40-services.conf): every field, commented.
