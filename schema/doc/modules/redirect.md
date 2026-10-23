Redirects requests, every one or those whose path matches `regexp`, to a URL with placeholders.

```json route
{
  "middleware": [{"module": "redirect", "params": {"regexp": "^/old/(.*)$", "to": "/new/$1", "status": 301}}],
  "handler": {"module": "respond", "params": {"body": "hello"}}
}
```
