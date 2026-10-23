A service proxies at layer 4: the connections of `tcp` listeners, byte for byte, and the datagrams
of `udp` listeners, a session per client, to an upstream's addresses, picked by its load balancing
and health. TLS connections can be told apart by the name they ask for (`server_names`) and passed
through without being terminated; `proxy_protocol` tells the upstream who the client was. A
Caddyfile has no services.

```json
{
  "listeners": [
    {"name": "postgres", "address": ":5432", "protocols": ["tcp"]},
    {"name": "dns", "address": ":53", "protocols": ["udp"]},
    {"name": "tls", "address": ":8443", "protocols": ["tcp"]}
  ],
  "upstreams": [
    {"name": "db", "addresses": ["10.0.0.5:5432"]},
    {"name": "resolvers", "addresses": ["10.0.0.53:53", "10.0.0.54:53"]},
    {"name": "mail", "addresses": ["10.0.0.25:8443"]}
  ],
  "services": [
    {"name": "postgres", "listeners": ["postgres"], "upstream": "db", "idle_timeout": "30m"},
    {"name": "dns", "listeners": ["dns"], "upstream": "resolvers"},
    {"name": "mail", "listeners": ["tls"], "upstream": "mail", "server_names": ["mail.example.com"]}
  ]
}
```
