Rewrites the path the handler sees, after the route is picked: strips or adds a prefix, or
replaces what a regular expression matches.

```caddyfile
example.com {
	handle_path /api/* {
		reverse_proxy app:8080
	}
}
```

```json route
{
  "match": {"path": ["/api/*"]},
  "middleware": [{"module": "rewrite", "params": {"strip_prefix": ["/api"], "add_prefix": "/v2"}}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
