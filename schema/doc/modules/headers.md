Changes request fields before the handler sees them, and response fields just before they are
sent.

```caddyfile
example.com {
	header {
		Strict-Transport-Security "max-age=31536000"
		-Server
	}
	request_header X-Env prod
	respond "hello"
}
```

```json route
{
  "middleware": [{"module": "headers", "params": {
    "request": {"set": {"X-Env": "prod"}},
    "response": {"set": {"Strict-Transport-Security": "max-age=31536000"}, "delete": ["Server"]}
  }}],
  "handler": {"module": "respond", "params": {"body": "hello"}}
}
```
