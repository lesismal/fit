Limits that bound what clients can make fit hold, on every listener. Each has a safe default, so a
config that writes none is still bounded: connections, the sizes of request headers and bodies,
the bytes of responses waiting for slow clients, and how long a client may stall a response.
A Caddyfile's `request_body { max_size }` limits a route's bodies instead, with the
[`body_limit`](#http-middleware-body_limit) middleware.

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "respond"}}],
  "limits": {
    "max_conns": 50000,
    "max_header_bytes": 65536,
    "max_body_bytes": 104857600,
    "max_pending_bytes": 536870912,
    "write_stall_timeout": "1m"
  }
}
```
