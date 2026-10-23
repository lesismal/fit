A route's own limit on request bodies, below the listeners' `limits.max_body_bytes`.

```json route
{
  "match": {"path": ["/upload"]},
  "middleware": [{"module": "body_limit", "params": {"max_bytes": 100000000}}],
  "handler": {"module": "respond", "params": {"body": "ok"}}
}
```
