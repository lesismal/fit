Bridges a WebSocket to a TCP service, as [websockify](https://github.com/novnc/websockify) does, for
the browser clients of services that speak plain TCP: noVNC for a VNC server, a web terminal for SSH
or telnet, a game or MUD client. The handshake is taken over HTTP/1.1, and as an extended CONNECT over
h2 and h3. Each binary message the client sends goes to the target as its bytes, frame by frame, and
what the target sends comes back as binary messages; a client that offers the `base64` subprotocol,
as older noVNC does, sends and gets text messages of base64 instead. A client that offers `binary`
gets it, and one that offers none gets binary messages without a subprotocol.

The target is never one the client names: it is `address`, an address of `upstream` (picked by the
upstream's `load_balancing`, a connect that fails tried on the next address, up to 3, with its
`deny_internal`, `max_fails` and health checks), or the address `tokens` lists for the token in the
query parameter `token_param` (`token`), as websockify's token plugin keeps them: a request without a
token, or with one not listed, is answered `403`. The target is dialed before the WebSocket is
accepted, so one that is down is answered `502`, or `504` past `connect_timeout`, and a target that
speaks first, as a VNC server does, has its greeting sent once the WebSocket is open. A request that
is not a WebSocket handshake is answered `426`; one past `max_conns` (1024) `503`; one from a page of
another site `403`, unless `origins` lists it.

Nothing waits on a fib worker, and each side paces the other: while the client has more than 64 KiB
of the target's messages unsent, the target's reads are held, and while the target has more than 64
KiB of the client's bytes unsent, the WebSocket's reads are, so a slow end slows the other by TCP's
flow control (or the stream's, over h2 and h3) rather than by memory in fit. A frame from the client
is bounded by `max_frame_bytes` (1 MiB; a larger one closes the WebSocket with `1009`). When one side
closes, the other is closed once what it was sent has gone: a target that hangs up closes the
WebSocket with `1000`. A bridge that carries nothing either way for `idle_timeout` (10m) is closed
with `1001`, and at once if the client has not taken that close by the next timeout. The bridges of a
config swapped out are closed once its drain timeout passes.

The route's middleware applies as to any handler: put an authenticating middleware (`basic_auth`,
`jwt`, `forward_auth`, `client_cert`) before it, since anyone who reaches the route reaches the
target, and a route's `access` lists the clients. The metrics `fit_websockify_connections_open`,
`fit_websockify_requests_total` (by `result`: `bridged`, `refused`, `busy`, `failed`) and
`fit_websockify_bytes_total` (by `direction`) carry the label `bridge`, `name` or the target.

```json route
{
  "match": {"path": ["/websockify"]},
  "middleware": [{"module": "basic_auth", "params": {
    "users": [{"username": "alice", "password_hash": "$2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG"}]
  }}],
  "handler": {"module": "websockify", "params": {
    "tokens": {"desk1": "10.0.0.5:5900", "desk2": "10.0.0.6:5900"},
    "idle_timeout": "30m"
  }}
}
```
