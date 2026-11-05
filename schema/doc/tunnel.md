The tunnel's roles, run beside the gateway on its engine: a `hub` that nodes and proxies link
to over TLS, WebSocket over TLS (wss) or QUIC, a `node` beside the services it exposes, and a
`proxy` that forwards local ports and web proxies through a hub to a node's service, so that
services behind NAT are reached. Nodes and proxies check the hub's certificate by `pin`, its
fingerprint on the hub's pages, or by `ca_file`; plain `tcp` and `ws` are for the loopback, or
need `body_password`. Each node and proxy registers with a `token` the hub issued, from its
pages, its Admin API or `fit token`, which the hub keeps as a hash in its `tokens`; the shared
`password` still registers in this release, with a warning. A role runs while its member is set;
any of them, in any number of processes. What a role's pages, behind `console`, change is written
back into its member, and kept in `admin.store` when there is one. See [the tunnel](../tunnel.md).

```json
{
  "tunnel": {
    "hub": {
      "listeners": [{"protocol": "wss", "address": ":7443"}],
      "tokens": [
        {"id": "fdd44d906b8e6ce2", "name": "lab", "hash": "1c994a095de7b566dcce0f229b829af96d85911b6d3e786f217c77340bc36aa4", "created_at": 1793900000},
        {"id": "5a3716dd56e5e458", "name": "alice", "hash": "4e6349fdb79b1b48fb57cc61b5aa9593490f288f28f722e0245c04a9df6c66df", "created_at": 1793900000,
          "expires_at": 1825436000}
      ]
    },
    "node": {
      "name": "lab",
      "hubs": [{"name": "hub", "endpoints": [{"address": "wss://hub.example.com:7443/tunnel"}],
        "token": "fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ",
        "pin": "02e5420d38831f019c363d82b2d7ee2ff7db1a143401587e3b9a92f0011e8261"}],
      "services": [{"name": "ssh", "address": "10.0.0.5:22"}]
    },
    "proxy": {
      "name": "alice",
      "hubs": [{"name": "hub", "endpoints": [{"address": "wss://hub.example.com:7443/tunnel"}],
        "token": "fitt1.5a3716dd56e5e458.nz51QI_A3avzPo4tagvevbtlJ-dNi-Us-k4-il9gAV4",
        "pin": "02e5420d38831f019c363d82b2d7ee2ff7db1a143401587e3b9a92f0011e8261"}],
      "forwards": [{"listen": "127.0.0.1:2222", "node": "lab", "service": "ssh"}]
    }
  }
}
```
