# Listeners

[English](05-listeners.md) | [简体中文](05-listeners.zh-CN.md)

## What it does

A listener is **an address and the protocols served on it**. Routes and layer 4 services refer to
listeners by `name`; a route that names no listener serves every listener.

A listener says three things: the address it listens on (`address`), the protocols served there
(`protocols`: `http/1.1`, `h2c`, `h2`, `h3`, `tcp`, `udp`, `socks`), and TLS (`tls`: certificates
from files, names to obtain them for with ACME, or certificates obtained during the handshake).
`client_auth` asks for and verifies client certificates (mTLS); `proxy_protocol` reads the PROXY
protocol header from the proxies it trusts; `socks` makes the `socks` protocol a SOCKS5 forward
proxy; `mptcp` listens with Multipath TCP where the kernel offers it.

A config that writes no listener at all is served by [HTTPS by default](../reference/config.md#https-by-default),
which makes a 443 and a redirecting 80 from the host names its routes match; a listener whose `tls`
is empty (`{}`) gets certificates for the host names of the routes it serves.

The listeners divide the work with the rest of the config: a route names the listeners it serves with
`listeners`, and a route that names none serves every one; a layer 4 service lists the `tcp` or `udp`
listener whose connections it proxies; the `socks` protocol decides who may use it by `socks`'s
`clients`. This page edits the listeners themselves; each running port is expanded, in the
[Engine](06-engine.md) menu, into the features it serves.

## Pages

### Listeners

The **Listeners** module has one page of its own, `#/gw.listeners/listeners`. It edits the config's
`listeners` member — the listeners themselves; the features of each port (handlers, routes, cache,
streaming, WebRTC, layer 4, SOCKS, TLS) are grouped per port under the [Engine](06-engine.md) menu.
The note above the page says: each port's features are grouped under the Engine menu in the sidebar;
this page edits the listeners themselves.

- **Each row** shows the listener's name, its `address`, and a badge for every protocol in
  `protocols`; a row with `tls` adds a `tls` badge, one with `socks` a `socks` badge. Opening a row
  is its form.
- **Add listener** makes a new row: `name` `web-N`, `address` `:8080`, `protocols` `http/1.1` by
  default. Every row can be **removed**, **moved up** or **moved down**.
- The form's fields are `name`, `address`, `protocols`, `mptcp`, `tls` (with `certificates`, `acme`,
  `on_demand` and `client_auth`), `socks`, `access` and `proxy_protocol`.
- Like every page that edits the config, this page shares one draft: the top bar's **Save and apply**
  saves it whole (`PUT /config`), and **Check** runs the whole apply path without applying, saying
  which listeners would be added or removed; besides **UI** there are `.conf`, `.yaml` and `.json`
  tabs, editing this page's `listeners` member as text in that format.
- The address written is the one in the config; the address the kernel actually bound, and the
  features each port serves, are listed on the port's row in the [Engine](06-engine.md) menu.

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
| `name` | required | A name unique among the listeners, by which routes and layer 4 services refer to this one. |
| `address` | required | The host and port to listen on, as in `:8080` or `127.0.0.1:8080`. |
| `protocols` | `http/1.1`, or `http/1.1` and `h2` with `tls` | The protocols served: `http/1.1`, `h2c` (HTTP/2 in the clear), and with `tls` `h2` (chosen through ALPN) and `h3` (HTTP/3 over QUIC, on the same port in UDP, announced to HTTP/1 and HTTP/2 clients with Alt-Svc); `tcp`, raw TCP, whose connections the [layer 4 services](17-l4-proxy.md) that list the listener proxy to their upstreams; `udp`, datagrams; `socks`, a SOCKS5 and SOCKS4 forward proxy. A listener serving more than one of HTTP, `tcp` and `socks` tells them apart by what a client sends first: a TLS ClientHello goes to the service whose `server_names` hold its name, or else, with `tls`, is served HTTPS; a SOCKS greeting to `socks`; an HTTP request in the clear to HTTP; and whatever else to the service without `server_names`, or it is closed. A client that waits for the server to speak first is closed after 10s. |
| `mptcp` | off | Listen the listener's TCP connections with Multipath TCP where the kernel offers it (Linux 5.6+, `net.mptcp.enabled`), falling back to plain TCP where it does not; a client without Multipath TCP falls back too. `udp` listeners and HTTP/3 take none. |
| `tls` | none | With `tls` the listener's HTTP is HTTPS: `certificates` are certificate files, `acme` the names to obtain certificates for, `on_demand` the certificates obtained during the handshake (which needs the top-level `acme.on_demand`); an empty `tls` (`{}`) gets certificates for the routes' host names. |
| `tls.client_auth` | `mode` is `require` | Ask clients for certificates and verify them (mTLS): `ca_files` are the CA files a client's certificate must chain to, watched as certificate files are. `verify_if_given` lets a client send none, but one it sends must be valid. A listener that requires certificates cannot answer ACME's TLS-ALPN-01 challenge. |
| `socks` | none (required with `socks`) | Who may use the SOCKS proxy (`clients`) and where it connects them (`allow`, `deny`, `ports`): it serves CONNECT, without authentication, and refuses BIND and UDP ASSOCIATE. |
| `access` | every address | The client addresses the listener serves (`allow`/`deny`, or `allow_files`/`deny_files`), deciding before the global `access`; a connection it refuses is closed as soon as it is accepted. With `proxy_protocol` the address is the one the header names. |
| `proxy_protocol` | none | Take a PROXY protocol header, version 1 or 2, from a `trusted` address and serve the connection as from the client it names: a connection from one of them must begin with a header within 10s, or it is closed. For `tcp` listeners and HTTP over TCP; with `tls` the header comes before TLS. HTTP/3 takes none. |

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
listener of its own. A TCP and a UDP port of the same number do not conflict, so HTTP and HTTP/3 may
share a port; a port 0 picked by the kernel never conflicts.

### One address, one server

Two listeners may not listen on one address and transport, nor a listener and any other part of the
config that listens: the Admin API, a console, a role's links listener or API, a tunnel forward, a
streaming or WebRTC port. A listener also fails to bind an address another process holds. `:443`,
`0.0.0.0:443` and `[::]:443` are one address, which takes in `127.0.0.1:443`. A TCP and a UDP port of
the same number do not conflict, so HTTP and HTTP/3 may share a port. See
[one address, one server](../configuration.md#one-address-one-server).

## In .conf

```fit
# The listeners: the addresses fit listens on and the protocols served on each.
listener web :8080 http/1.1 h2c {
	mptcp true
}

listener secure :443 http/1.1 h2 h3 {
	tls {
		# Certificates from files, and the CA files of client_auth, must exist for
		# fit validate to read them, so they are commented out here.
		# certificates {
		# 	cert_file /etc/fit/example.com.pem
		# 	key_file /etc/fit/example.com.key
		# }
		acme example.com www.example.com
		# on_demand true needs the top-level acme.on_demand, which says whom to ask.
		# client_auth {
		# 	ca_files /etc/fit/clients-ca.pem
		# 	mode verify_if_given
		# }
	}
	access {
		allow 10.0.0.0/8
		deny 203.0.113.0/24
	}
}

# A tcp listener needs a service that lists it, so it is commented out here:
# listener internal 127.0.0.1:8081 tcp { proxy_protocol { trusted 10.0.0.0/8 } }

listener proxy :1080 socks {
	socks {
		clients 10.0.0.0/8
		allow *.example.com
		deny 169.254.169.254
		ports 443
	}
}

route { handler respond { body "hello from fit\n" } }
```

This config passes `./bin/fit validate -c`. The certificate and CA files must exist for `fit validate`
to read them, so they are commented out in the example; a `tcp` listener needs a service that lists
it, and that listener is commented out too.

## See also

- [The config reference: Listeners](../reference/config.md#listeners) (every field), and the
  [chapter guide in Chinese](../reference/config.zh-CN.md#各章内容)
- [One address, one server](../configuration.md#one-address-one-server)
- [The .conf format](../reference/fit.md)
- [Engine](06-engine.md): the running features per port
- [TLS](18-tls.md), [Forward proxy](16-forward-proxy.md), [L4 proxy](17-l4-proxy.md)
- The commented template with every field:
  [templates/conf/conf.d/10-listeners.conf](../../templates/conf/conf.d/10-listeners.conf)
