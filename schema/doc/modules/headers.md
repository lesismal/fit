Changes request fields before the handler sees them, and response fields just before they are
sent.

```json route
{
  "middleware": [{"module": "headers", "params": {
    "request": {"set": {"X-Env": "prod"}},
    "response": {"set": {"Strict-Transport-Security": "max-age=31536000"}, "delete": ["Server"]}
  }}],
  "handler": {"module": "respond", "params": {"body": "hello"}}
}
```
