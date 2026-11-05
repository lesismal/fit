Proxies to an [upstream](#upstreams) by name; or, with `upstreams`, splits requests over several
by weight, as a canary release does. `failover` names an upstream that takes over when none of
the first's addresses is in rotation, `mirror` copies a share of the requests elsewhere, and
`retry` sends a failed request again, within a budget. WebSockets and gRPC are proxied too,
streamed both ways. Of HTTP/1.1 upgrades only websocket is tunnelled, and what
`upgrade_protocols` adds; one to any other protocol goes on as an ordinary request, and h2c is
never tunnelled, nor HTTP2-Settings sent on, since the upstream's HTTP/2 would carry requests the
proxy never sees.

```json route
{
  "match": {"host": ["example.com"], "path": ["/api/*"]},
  "handler": {"module": "proxy", "params": {
    "upstreams": [{"upstream": "app", "weight": 95}, {"upstream": "app-canary", "weight": 5}],
    "failover": "app-backup",
    "host_header": "api.internal",
    "retry": {"attempts": 2, "statuses": [502, 503]},
    "timeout": "30s"
  }}
}
```
