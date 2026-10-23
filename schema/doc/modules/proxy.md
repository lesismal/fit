Proxies to an [upstream](#upstreams) by name; or, with `upstreams`, splits requests over several
by weight, as a canary release does. `failover` names an upstream that takes over when none of
the first's addresses is in rotation, `mirror` copies a share of the requests elsewhere, and
`retry` sends a failed request again, within a budget. WebSockets and gRPC are proxied too,
streamed both ways.

```caddyfile
example.com {
	reverse_proxy /api/* 10.0.0.1:8080 10.0.0.2:8080 {
		lb_retries 2
		header_up Host api.internal
	}
}
```

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
