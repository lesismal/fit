# Forward proxy

[English](16-forward-proxy.md) | [简体中文](16-forward-proxy.zh-CN.md)

## What it does

A forward proxy does not answer its own sites; it connects elsewhere for its clients. A route whose
`handler` is `forward_proxy` proxies HTTP CONNECT for the clients `clients` lists: the client sends
`CONNECT host:port`, fit dials the destination, answers `200 Connection established`, and the
connection becomes a two-way stream of bytes, which fit no longer looks inside. CONNECT is served over
HTTP/1.1, HTTP/2 and HTTP/3. The page calls it HTTP CONNECT (and plain HTTP) proxying; the handler
proxies CONNECT only: a request that is not a CONNECT, an extended CONNECT among them, is answered 405
with `Allow: CONNECT`. So a client uses it as an HTTPS proxy — `curl -x`, a browser's proxy settings
and `HTTPS_PROXY` all tunnel through CONNECT.

The destinations are decided by `allow` and `deny`. `allow` names host names (`*.example.com` for every
name below `example.com`), IP addresses and CIDR ranges, and a range holds the address a host name
resolves to. Whatever `allow` holds, loopback, link-local (`169.254.169.254`, the metadata service of
most clouds, among them), unspecified and multicast addresses, and the addresses of the host fit runs
on, are refused by default: otherwise a client would reach the Admin API on `127.0.0.1:2019` and every
other service the host keeps to itself through the proxy. To allow them, `allow` must name them with a
range inside their own (`127.0.0.0/8` does, `0.0.0.0/0` does not), or write the host's own address
alone. `deny` refuses whatever it holds, whatever `allow` holds.

A listener can also serve SOCKS5 (and SOCKS4) with the `socks` protocol: `listeners.socks` says which
clients may use it and where it connects them. It serves CONNECT only, without authentication, and
refuses BIND and UDP ASSOCIATE. The `forward_proxy` route and `listeners.socks` are the same object, so
`clients`, `allow`, `deny`, `ports` and the two timeouts are written exactly the same way; the only
difference is how a refused client is refused: the route answers 403, while SOCKS answers with a
SOCKS failure reply (code 2, "connection not allowed by ruleset"). A port that serves both HTTP and
socks is told apart by what the client sends first: a SOCKS greeting goes to socks, an HTTP request
in the clear to HTTP. Of SOCKS5's commands only CONNECT is served, and SOCKS4 has connect only.
Neither authenticates, so `clients` is the only gate.

## Pages

### Forward proxy

The Forward proxy module has one page in the menu, `#/gw.forward/routes`. It edits the `routes` member of the config, but shows
only the routes whose `handler` is `forward_proxy`; the callout at the top says so, and reminds that
listeners can serve SOCKS5 with the `socks` protocol too.

- When the draft has a listener that sets `socks` (or names it in `protocols`), the page lists
  "Listeners serving SOCKS": each badge holds the listener's name and address and opens the
  [Listeners](05-listeners.md) page. This row is read only; `socks` itself is edited on the Listeners
  page.
- Each route is a row: its name, with small tags for its clients (the `clients` entries, joined by
  commas), `:ports` and `@listeners` (which listeners the route serves; a route that names none serves
  every listener).
- "Add forward proxy route" makes a route with `match { method CONNECT }`, `clients` `127.0.0.1/32`
  and `ports` `443`, named `forward-1`, `forward-2`, …
- A route row opens its form: `match` and the handler's params are edited field by field, `clients`,
  `allow`, `deny`, `ports` and the two timeouts among them.
- The page lists only the forward proxy routes, but its `.conf`, `.yaml` and `.json` tabs are the
  whole `routes` member, the other routes included; the form and the tabs are the same draft, and
  Save and apply checks and applies it. The problems the consistency check and the JSON page report
  for any route come from this draft too.

### On the Engine page

The [Engine](06-engine.md) page lists the features a port serves: under this port it lists "Forward
proxy routes", which links back to this page, and if the listener has `socks`, "SOCKS forward proxy",
which links to the Listeners page, where the SOCKS rules are edited. The tags of the port row name the protocols and the `proxy_protocol`.

## Key parameters

Only the key parameters; the full field tables are in
[the config reference: Handlers](../reference/config.md#http-handlers-forward_proxy) and, for
`listeners.socks`, in [the config reference: `listeners[].socks`](../reference/config.md#config-forwardproxy). Both are the
same object, so the fields are the same.

| field | default | description |
| --- | --- | --- |
| `clients` | required | The addresses and CIDR ranges of the clients that may use the proxy, as `10.0.0.0/8`; others are refused (the route answers 403, SOCKS answers a failure reply). |
| `allow` | everything, but for the addresses refused by default | The destinations the proxy connects to: host names, `*.example.com`, IP addresses and CIDR ranges; a range also holds the address a host name resolves to. |
| `deny` | none | Destinations refused, written as in `allow`, whatever `allow` holds. |
| `ports` | any | The destination ports allowed, as `443`. |
| `connect_timeout` | `10s` | How long resolving and connecting to a destination may take; past it the request is answered 504. |
| `idle_timeout` | `5m` | How long a tunnel may carry nothing either way before it is closed. |

### The addresses refused by default

These are refused whatever `allow` holds; to allow them `allow` must name them:

- loopback: `127.0.0.0/8` and `::1`;
- "this network" and unspecified: `0.0.0.0/8` (a connection to `0.0.0.0` reaches the host itself) and `::`;
- link-local: `169.254.0.0/16` — of which `169.254.169.254` is the metadata service of most clouds —
  and `fe80::/10`;
- multicast and broadcast: `224.0.0.0/4`, `ff00::/8` and `255.255.255.255`;
- the cloud metadata services outside those: `100.100.100.200` (Alibaba Cloud) and `fd00:ec2::254`
  (AWS's, over IPv6);
- the addresses of the host fit runs on: only writing that address alone names it, a wider range does not.

### What gets refused, and how

- a client not in `clients`: the route answers 403, SOCKS answers a failure reply (code 2);
- a destination port `ports` does not hold, a destination `allow` does not hold or `deny` holds, or an
  IP address refused by default above: the route answers 403, SOCKS code 2;
- a host name that resolves to an address refused by default, or to one `deny` holds (or, when `allow`
  holds the name only through a range, that the range does not hold), and a destination that turns out to
  be the host's own address: refused once it is connected, before anything is sent, and the route answers
  502 (SOCKS code 2);
- a resolve or connect past `connect_timeout`: the route answers 504, SOCKS a failure reply.

### Points that are easy to misread

- `clients` names the clients, not the destinations; the destinations are `allow` and `deny`.
- A range in `allow` also holds the address a host name resolves to, and a name `allow` names is still
  checked by the address it resolves to: one that resolves to `127.0.0.1` is refused unless `allow` also
  names `127.0.0.0/8`.
- `deny` wins over `allow`: an address `allow` holds is refused by `deny` just the same.
- `ports` bounds the destination port, not the port the proxy listens on.

### Two required fields, two kinds of "who may use"

`clients` is required: a route without it fails validation with a missing field. When a listener's
`protocols` names `socks`, `listeners.socks` is required too: the protocol only says the port serves
SOCKS, and `socks` says who may use it and where it connects them.

`clients` says who may use the proxy; who may reach the listener at all is decided by `access` — a
connection `access` refuses never gets to the route, so `clients` never has a say.

## In .conf

One listener serves SOCKS5, one route serves HTTP CONNECT, and both use the same rules. This is a
self-contained complete config, and it passes `fit validate`:

```fit
listener proxy :8080 http/1.1 socks {
	socks {
		clients 10.0.0.0/8
		allow *.example.com
		ports 443
	}
}

route {
	match { method CONNECT }
	handler forward_proxy {
		clients 10.0.0.0/8
		allow *.example.com
		ports 443
	}
}
```

In the short form, the block after the arguments is the listener's own fields, so `clients` goes inside
the `socks` block, not in the listener block. `allow` holds no `127.0.0.0/8`, so a client cannot reach
the host itself; to allow that, write it in `allow`. `ports 443` allows only destination port 443 — the
port the proxy goes to, not the port it listens on; leave `ports` out and any port is allowed.

To try it, from a client in `10.0.0.0/8` (`proxy.example.net` standing for fit's address):

```sh
curl -x http://proxy.example.net:8080 https://www.example.com/
curl --socks5-hostname proxy.example.net:8080 https://www.example.com/
```

The first tunnels through the route with CONNECT, the second through SOCKS5, the name resolved by fit;
a plain `http://` URL through `-x` is sent as a GET, not a CONNECT: this route, which matches CONNECT
only, does not take it (no route does here, so 404), and a `forward_proxy` route that did would answer
405; `curl -p` tunnels it with CONNECT instead, to port 80, which `ports 443` refuses.

The page edits the `routes` member: in JSON and YAML the same route is `routes[].handler.params`,
and this route is the one in the JSON example of
[the forward_proxy module doc](../../schema/doc/modules/forward_proxy.md).

## See also

- [Listeners](05-listeners.md): the `socks` protocol and `listeners.socks`.
- [Engine](06-engine.md): where a port lists this route and SOCKS.
- [Reverse proxy](15-reverse-proxy.md): a route's `match`, `handler` and `middleware`.
- [the config reference: Handlers](../reference/config.md#http-handlers-forward_proxy): every field of `forward_proxy`.
- [the config reference: `listeners[].socks`](../reference/config.md#config-forwardproxy): every field of `listeners.socks`.
- [the forward_proxy module doc](../../schema/doc/modules/forward_proxy.md): the addresses refused by default and a route example.
- [who may connect](../configuration.md#who-may-connect): `access` and the client addresses.
- [the complete config template](../../templates/conf/conf.d/10-listeners.conf): every field, commented.
