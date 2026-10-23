Sends each request's fields to an auth service, as Authelia or oauth2-proxy are: a 2xx lets it
through with fields copied from the answer, anything else is the client's answer.

```caddyfile
example.com {
	forward_auth auth:9091 {
		uri /api/verify
		copy_headers Remote-User Remote-Groups
	}
	reverse_proxy app:8080
}
```

```json route
{
  "middleware": [{"module": "forward_auth", "params": {
    "upstream": "auth", "uri": "/api/verify", "copy_headers": ["Remote-User", "Remote-Groups"]
  }}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
