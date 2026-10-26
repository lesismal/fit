Limits that bound what clients can make fit hold, on every listener. Each has a safe default, so a
config that writes none is still bounded: connections, the sizes of request headers and bodies,
the bytes of responses waiting for slow clients, and how long a client may stall a response.
The bytes waiting for slow clients, the backpressure across every connection, are a number of
bytes or a share of memory, such as `"25%"`: of the machine's, or of the container's memory limit
where that is lower, half by default; the rest have fixed defaults. One connection's send queue is
capped at 32 MiB by default, above what the response paths queue, so a peer that stops reading is
closed rather than holding a share of the budget forever. The
[`body_limit`](#http-middleware-body_limit) middleware limits a route's bodies further.

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "respond"}}],
  "limits": {
    "max_conns": 50000,
    "max_header_bytes": 65536,
    "max_body_bytes": 104857600,
    "max_pending_bytes": "25%",
    "max_write_queue_bytes": 33554432,
    "write_stall_timeout": "1m"
  }
}
```
