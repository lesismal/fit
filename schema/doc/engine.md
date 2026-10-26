How [fib](https://github.com/lesismal/fib), the network engine fit serves and dials on, is set up.
fit runs two engines: the listeners', which serves clients, and one that upstream connections are
dialed through, so that a response waiting for a slow client does not hold up the workers that
read upstreams. Each takes these settings. What is left out keeps fib's own default, so most
configs write no `engine` at all; it is for tuning a busy server on the host it runs on.

The engines are made when fit starts: a reload that changes `engine` is applied, with a warning
that the change waits for a restart. How many bytes may wait for clients across every
connection, the engine's budget, is [`limits.max_pending_bytes`](#limits), half the machine's
memory by default; `write_watermark` is the same backpressure on one connection, sized from the
read buffer and the machine when it is left out. At startup fit prints the effective limits and
the settings of both engines, so what it actually runs with can be read from its log.

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "respond"}}],
  "engine": {
    "event_loops": 4,
    "workers": 512,
    "read_buffer_size": 32768,
    "write_watermark": 131072
  }
}
```
