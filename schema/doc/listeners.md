A listener is an address and the protocols served there. Routes and services refer to listeners
by `name`; a route that names none serves every listener.

- **Protocols.** `http/1.1` and `h2c` (HTTP/2 in the clear) without TLS; with `tls`, `h2` (HTTP/2,
  chosen through ALPN) and `h3` (HTTP/3 over QUIC, on the same port in UDP, announced with
  Alt-Svc). `tcp` proxies raw connections to a [service](#services), `udp` datagrams, and `socks`
  is a SOCKS5 forward proxy. A listener that names none serves `http/1.1`, and `http/1.1` and `h2`
  with TLS. Several may share a port: fit tells them apart by what a client sends first.
- **TLS.** `tls` makes the listener's HTTP HTTPS, with certificates from files, names to obtain
  certificates for with [ACME](#acme), or certificates obtained during the handshake
  (`on_demand`). An empty `tls`, `{}`, gets certificates for the host names of the routes the
  listener serves. `client_auth` asks clients for certificates (mutual TLS).
- **Behind a proxy.** `proxy_protocol` takes the PROXY protocol header from the proxies it trusts,
  and serves each connection as from the client the header names.
- **Multipath TCP.** `mptcp` carries the listener's TCP connections over Multipath TCP where the
  kernel offers it (Linux 5.6+, with `net.mptcp.enabled`), falling back to plain TCP where it does
  not; a client without Multipath TCP falls back too. Off by default: a listener is plain TCP, which
  is also what the hardening after CVE-2026-98432's published exploit (`24dba02`) left, an MPTCP
  listener being what that exploit of a use-after-free in the kernel's IPv4 TCP handshake needs on its
  target; the kernel bug is the kernel's to fix, and setting `mptcp` on an unpatched kernel takes that
  exposure. `udp` listeners and HTTP/3 take none, having no paths.
- **One address, one server.** Two listeners may not listen on one address and transport, nor a
  listener and any other part of the config that listens: the Admin API, a console, a role's
  links listener or API, a tunnel forward, a streaming or WebRTC port. `:443`, `0.0.0.0:443` and
  `[::]:443` are one address, which takes in `127.0.0.1:443`. A listener also fails to bind an
  address another process holds. See [one address, one server](../configuration.md#one-address-one-server).

```json
{
  "listeners": [
    {"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]},
    {"name": "secure", "address": ":443", "protocols": ["http/1.1", "h2", "h3"],
     "tls": {"certificates": [{"cert_file": "/etc/fit/example.pem", "key_file": "/etc/fit/example.key"}]}},
    {"name": "internal", "address": "127.0.0.1:8081", "proxy_protocol": {"trusted": ["10.0.0.0/8"]}}
  ],
  "routes": [
    {"listeners": ["secure"], "handler": {"module": "respond", "params": {"body": "secure"}}},
    {"listeners": ["web", "internal"], "handler": {"module": "respond", "params": {"body": "plain"}}}
  ]
}
```
