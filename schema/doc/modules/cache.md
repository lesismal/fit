Keeps responses by RFC 9111's rules, in memory and, with `disk`, on disk, in a zone that routes
may share; answers stale while refreshing, or when the upstream fails, if told to. Purged through
the Admin API.

```json route
{
  "match": {"path": ["/assets/*"]},
  "middleware": [{"module": "cache", "params": {
    "zone": "assets",
    "max_bytes": 268435456,
    "default_ttl": "10m",
    "stale_while_revalidate": "1m",
    "disk": {"path": "/var/cache/fit/assets", "max_bytes": 10737418240}
  }}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
