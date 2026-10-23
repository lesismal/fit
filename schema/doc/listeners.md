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
