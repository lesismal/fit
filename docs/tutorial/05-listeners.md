# Listeners

[English](05-listeners.md) | [简体中文](05-listeners.zh-CN.md)

## What it does

A listener is **an address and the protocols served on it**. Routes and layer 4 services refer to
listeners by `name`.

A listener says three things: the address it listens on (`address`), the protocols served there
(`protocols`: `http/1.1`, `h2c`, `h2`, `h3`, `tcp`, `udp`, `socks`), and TLS (`tls`: certificates
from files, names to obtain them for with ACME, or certificates obtained during the handshake).
`client_auth` asks for and verifies client certificates (mTLS); `proxy_protocol` reads the PROXY
protocol header from the proxies it trusts; `socks` makes the `socks` protocol a SOCKS5 and SOCKS4
forward proxy; `mptcp` listens with Multipath TCP where the kernel offers it; `access` decides which
client addresses the listener serves.

The listeners divide the work with the rest of the config:

- a route names the listeners it serves with `listeners`; a route that names none serves every
  listener that serves HTTP. A route may not name a listener that serves no HTTP (`tcp`, `udp` or
  `socks` alone);
- a layer 4 service lists the `tcp` or `udp` listeners whose connections it proxies, and a `tcp` or
  `udp` listener that no service lists is refused;
- the `socks` protocol decides who may use it by the listener's `socks.clients`.

A config that writes no listener at all is served by
[HTTPS by default](../reference/config.md#https-by-default): a 443 with certificates from ACME for
the host names its routes match, and an 80 that redirects to it (the redirect can be turned off in
`auto_https`). Such a config needs a route that matches a host name a public CA certifies, or it is
refused: "at least one listener is needed, or a route that matches a host name for HTTPS by
default". A listener whose `tls` is empty (`{}`) gets certificates for the host names of the routes
it serves.

This page edits the listeners themselves; each port, and the features it serves, is listed in the
[Engine](06-engine.md) menu.

## Pages

### Listeners

**Listeners** is a menu entry with one page, `#/gw.listeners/listeners`. It edits the config's
`listeners` member. The note above the list says: each port's features are grouped under the Engine
menu in the sidebar; this page edits the listeners themselves.

- **Each row** is one listener, in the order the config writes them: its place and name (`#0 web`),
  its `address`, a badge for every protocol in `protocols`, and a `tls` badge when it has `tls`, a `socks` badge when it has
  `socks`. Opening a row shows its form.
- **Add listener** makes a new row: `name` `web-1` (the first `web-N` not taken), `address` `:8080`,
  `protocols` `http/1.1`. Each row has **Move up**, **Move down** and **Remove** buttons. Removing
  is not asked about and takes effect only when saved; removing the last listener leaves `listeners`
  out, and HTTPS by default then applies.
- The form's fields are `name`, `address`, `protocols`, `mptcp`, `tls` (with `certificates`, `acme`,
  `on_demand` and `client_auth`), `socks`, `access` and `proxy_protocol`. Each field shows its
  description; an optional member has a checkbox that sets it or leaves it out.
- The page shares the one draft every config page edits. The top bar shows the config version (or
  **Unsaved changes**) with **Discard**, **Check** and **Save and apply**. **Save and apply** checks
  the draft and applies it as a new config version; **Check** runs the same request with
  `?dry_run=1`: it loads and compiles the config, tries the new listeners' addresses, applies
  nothing, and says which listeners would be added or removed ("Listener web-2 on :8081 would be
  added.").
- Besides **UI**, the `.conf`, `.yaml` and `.json` tabs edit this page's `listeners` member as text
  in that format, with **Export** and **Import** buttons beside them for that member only. Saved
  from a text tab, when nothing else in the draft is unsaved, only `listeners` is sent, and a
  problem is shown at its line and column; otherwise the whole draft is sent.
- A problem a save or a check finds links to the listener and field it is about. The address shown
  is the one the config writes; the address the kernel actually bound is in the port's tooltip in
  the [Engine](06-engine.md) menu.

## Key parameters

Only the key parameters are listed; the full field tables are in
[the config reference: Listeners](../reference/config.md#listeners), with a table for each object:
[`listeners[]`](../reference/config.md#config-listener),
[`listeners[].tls`](../reference/config.md#config-tls),
[`listeners[].tls.certificates[]`](../reference/config.md#config-certificate),
[`listeners[].tls.client_auth`](../reference/config.md#config-clientauth),
[`listeners[].socks`](../reference/config.md#config-forwardproxy),
[`listeners[].access`](../reference/config.md#config-access),
[`listeners[].proxy_protocol`](../reference/config.md#config-proxyprotocol).

| field | default | description |
| --- | --- | --- |
| `name` | required | A name unique among the listeners, by which routes and layer 4 services refer to this one: letters, digits, `_`, `.` and `-`, starting with a letter or digit. |
| `address` | required | The host and port to listen on, as in `:8080` or `127.0.0.1:8080`. |
| `protocols` | `http/1.1`, or `http/1.1` and `h2` with `tls` | The protocols served: `http/1.1`; `h2c`, HTTP/2 in the clear, only without `tls`; with `tls`, `h2` (chosen through ALPN) and `h3` (HTTP/3 over QUIC, on the same port in UDP, announced to HTTP/1 and HTTP/2 clients with Alt-Svc); `tcp`, raw TCP, whose connections the [layer 4 services](17-l4-proxy.md) that list the listener proxy to their upstreams; `udp`, datagrams, listed alone; `socks`, a SOCKS5 and SOCKS4 forward proxy, which needs the listener's `socks`. A protocol may be listed once. |
| `mptcp` | off | Listen the listener's TCP connections with Multipath TCP where the kernel offers it (Linux 5.6+, `net.mptcp.enabled`), falling back to plain TCP where it does not; a client without Multipath TCP falls back too. Off, a listener is plain TCP, which is also what the hardening after CVE-2026-98432's published exploit left: exploiting that kernel use-after-free needs an MPTCP listener on its target, so set it only on a kernel with the fix. `udp` listeners and HTTP/3 take none. |
| `tls` | none | With `tls` the listener's HTTP is HTTPS: `certificates` are certificate files (a client gets the first whose names match the one it asks for), `acme` the host names to obtain certificates for, `on_demand` certificates obtained during the handshake (which needs the top-level `acme.on_demand`); an empty `tls` (`{}`) gets certificates for the routes' host names. A listener that serves no HTTP takes no `tls`: `tcp` and `udp` are proxied as they are, TLS and all. |
| `tls.client_auth` | `mode` is `require` | Ask clients for certificates and verify them (mTLS): `ca_files` are the CA files a client's certificate must chain to. `require` refuses a client without a valid certificate in the handshake; `verify_if_given` lets a client send none, but one it sends must be valid, and a route that needs one says so with the `client_cert` middleware. A listener that requires certificates cannot answer ACME's TLS-ALPN-01 challenge, so with `acme` it needs `verify_if_given` or the HTTP-01 challenge allowed. |
| `socks` | none (required with `socks`) | Who may use the SOCKS proxy (`clients`, required) and where it connects them (`allow`, `deny`, `ports`, `connect_timeout` 10s, `idle_timeout` 5m): it serves CONNECT, without authentication, and refuses BIND and UDP ASSOCIATE. Loopback, link-local (the metadata service 169.254.169.254 among them), unspecified and multicast addresses and the host's own are refused unless `allow` names a range inside theirs. |
| `access` | every address | The client addresses the listener serves (`allow`/`deny`, or `allow_files`/`deny_files`), deciding before the global `access`; a connection it refuses is closed as soon as it is accepted, or, for HTTP/3 and `udp`, its datagrams dropped — unless a route or service of the listener sets an `access` of its own, which then decides first. With `proxy_protocol` the address is the one the header names. |
| `proxy_protocol` | none | Take a PROXY protocol header, version 1 or 2, from a `trusted` address and serve the connection as from the client it names: a connection from one of them must begin with a header within 10s, or it is closed, and one from elsewhere is served as it is, with no header. For `tcp` listeners and HTTP over TCP; with `tls` the header comes before TLS. HTTP/3 and `udp` take none. |

The defaults in the table are what fit fills in when a field is left out. The certificate files and
`client_auth`'s `ca_files` are watched: a change loads the config again; `access`'s `allow_files` and
`deny_files` are read when the config is applied and applied again when one changes, each at most
64 MiB.

### Several protocols on one port

A listener serving more than one of HTTP, `tcp` and `socks` tells them apart by what a client sends
first:

- a TLS ClientHello goes to the service whose `server_names` hold its name; with no such service, a
  listener with `tls` serves it as HTTPS, and otherwise it goes to the service without `server_names`;
- a SOCKS greeting goes to `socks`;
- an HTTP request in the clear goes to HTTP;
- and whatever else goes to the service without `server_names`, or the connection is closed.

A client that waits for the server to speak first is closed after 10s: such a protocol needs a
listener of its own.

### One address, one server

Two listeners may not listen on one address and transport, nor a listener and any other part of the
config that listens: the Admin API, a console, a role's links listener or API, a tunnel forward, a
streaming or WebRTC port. A listener also fails to bind an address another process holds. `:443`,
`0.0.0.0:443` and `[::]:443` are one address, which takes in `127.0.0.1:443`. A TCP and a UDP port of
the same number do not conflict, so HTTP and HTTP/3 may share a port. See
[one address, one server](../configuration.md#one-address-one-server).

## In .conf

```fit
# Plain HTTP/1.1 and HTTP/2 in the clear, with Multipath TCP where the kernel has it.
listener web :8080 http/1.1 h2c {
	mptcp true
}

# HTTPS with HTTP/2 and HTTP/3, certificates from ACME, open to two networks.
listener secure :443 http/1.1 h2 h3 {
	tls {
		acme example.com www.example.com
		# Files must exist for fit validate to read them:
		# certificates {
		# 	cert_file /etc/fit/example.com.pem
		# 	key_file /etc/fit/example.com.key
		# }
		# client_auth {
		# 	ca_files /etc/fit/clients-ca.pem
		# 	mode verify_if_given
		# }
	}
	access {
		allow 10.0.0.0/8 192.168.0.0/16
		deny 10.9.0.0/16
	}
}

# HTTP behind a load balancer that sends the PROXY protocol header.
listener internal 127.0.0.1:8081 {
	proxy_protocol { trusted 10.0.0.0/8 }
}

# A SOCKS5/SOCKS4 forward proxy for the office network, to port 443 only.
listener proxy :1080 socks {
	socks {
		clients 10.0.0.0/8
		allow *.example.com
		deny 169.254.169.254
		ports 443
	}
}

# Raw TCP, proxied byte for byte by the service that lists the listener.
listener postgres :5432 tcp
upstream db 10.0.0.5:5432
service postgres { listeners postgres; upstream db }

route { match { host example.com www.example.com }; listeners secure; handler respond { body "hello over HTTPS\n" } }
route { listeners web internal; handler respond { body "hello from fit\n" } }
```

This config passes `fit validate -c`. `fit validate` reads the certificate and CA files, so they must
exist: they are commented out above. `internal` names no protocols, so it serves `http/1.1`.

## See also

- [The config reference: Listeners](../reference/config.md#listeners) (every field), and the
  [chapter guide in Chinese](../reference/config.zh-CN.md#各章内容)
- [One address, one server](../configuration.md#one-address-one-server)
- [The .conf format](../reference/fit.md)
- [Engine](06-engine.md): the running features per port
- [TLS](18-tls.md), [Forward proxy](16-forward-proxy.md), [L4 proxy](17-l4-proxy.md)
- The commented template with every field:
  [templates/conf/conf.d/10-listeners.conf](../../templates/conf/conf.d/10-listeners.conf)
