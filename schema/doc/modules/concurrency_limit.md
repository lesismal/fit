Bounds how many of a route's requests are under way at once, as nginx's `limit_conn` does.

```json route
{
  "middleware": [{"module": "concurrency_limit", "params": {"max": 100, "status": 503}}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
