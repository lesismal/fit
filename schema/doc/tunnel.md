The tunnel's roles, run beside the gateway on its engine: a `hub` that nodes and proxies link
to over TCP, WebSocket or QUIC, a `node` beside the services it exposes, and a `proxy` that
forwards local ports and web proxies through a hub to a node's service, so that services
behind NAT are reached. A role runs while its member is set; any of them, in any number of
processes. What a role's pages, behind `console`, change is written back into its member, and
kept in `admin.store` when there is one. See [the tunnel](../tunnel.md).

```json
{
  "tunnel": {
    "hub": {
      "password": "a-registration-password",
      "listeners": [{"protocol": "tcp", "address": ":7000"}]
    },
    "node": {
      "name": "lab",
      "hubs": [{"name": "hub", "endpoints": [{"address": "127.0.0.1:7000"}], "password": "a-registration-password"}],
      "services": [{"name": "ssh", "address": "10.0.0.5:22"}]
    },
    "proxy": {
      "hubs": [{"name": "hub", "endpoints": [{"address": "127.0.0.1:7000"}], "password": "a-registration-password"}],
      "forwards": [{"listen": "127.0.0.1:2222", "node": "lab", "service": "ssh"}]
    }
  }
}
```
