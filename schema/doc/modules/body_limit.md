A route's own limit on request bodies, below the listeners' `limits.max_body_bytes`.

```caddyfile
example.com {
	request_body /upload {
		max_size 100MB
	}
	respond "ok"
}
```

```json route
{
  "match": {"path": ["/upload"]},
  "middleware": [{"module": "body_limit", "params": {"max_bytes": 100000000}}],
  "handler": {"module": "respond", "params": {"body": "ok"}}
}
```
