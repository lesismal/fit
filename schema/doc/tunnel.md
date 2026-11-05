The tunnel's roles, run beside the gateway on its engine: a `hub` that nodes and proxies link
to over TLS, WebSocket over TLS (wss) or QUIC, a `node` beside the services it exposes, and a
`proxy` that forwards local ports and web proxies through a hub to a node's service, so that
services behind NAT are reached. Nodes and proxies check the hub's certificate by `pin`, its
fingerprint on the hub's pages, or by `ca_file`; plain `tcp` and `ws` are for the loopback, or
need `body_password`. A role runs while its member is set; any of them, in any number of
processes. What a role's pages, behind `console`, change is written back into its member, and
kept in `admin.store` when there is one. See [the tunnel](../tunnel.md).

```json
{
  "tunnel": {
    "hub": {
      "password": "a-registration-password",
      "listeners": [{"protocol": "wss", "address": ":7443"}]
    },
    "node": {
      "name": "lab",
      "hubs": [{"name": "hub", "endpoints": [{"address": "wss://hub.example.com:7443/tunnel"}], "password": "a-registration-password",
        "pin": "02e5420d38831f019c363d82b2d7ee2ff7db1a143401587e3b9a92f0011e8261"}],
      "services": [{"name": "ssh", "address": "10.0.0.5:22"}]
    },
    "proxy": {
      "hubs": [{"name": "hub", "endpoints": [{"address": "wss://hub.example.com:7443/tunnel"}], "password": "a-registration-password",
        "pin": "02e5420d38831f019c363d82b2d7ee2ff7db1a143401587e3b9a92f0011e8261"}],
      "forwards": [{"listen": "127.0.0.1:2222", "node": "lab", "service": "ssh"}]
    }
  }
}
```
