Limits the rate of requests, by client address, a header, or the whole route, with a burst.

```json route
{
  "match": {"path": ["/login"]},
  "middleware": [{"module": "rate_limit", "params": {"rate": 1, "burst": 5, "key": "ip", "trusted_proxies": ["10.0.0.0/8"]}}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
