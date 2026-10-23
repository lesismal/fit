A fixed answer: a health check, a maintenance page, a 404 for what no other route takes.

```caddyfile
:8080 {
	respond /health "ok" 200
}
```

```json route
{
  "match": {"path": ["/health"]},
  "handler": {"module": "respond", "params": {"status": 200, "body": "ok", "headers": {"Cache-Control": "no-store"}}}
}
```
