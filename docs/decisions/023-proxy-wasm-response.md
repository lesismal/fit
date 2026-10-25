# 023: Proxy-Wasm's response callbacks, on the plugin's pool

[English](023-proxy-wasm-response.md) | [简体中文](023-proxy-wasm-response.zh-CN.md)

Status: accepted, in I81 (2026-10-25). It completes [decision 018](018-proxy-wasm-subset.md),
which left the response's callbacks out.

## Context

I72 gave Proxy-Wasm filters their request phase and left the response's callbacks out
(decision 018): fit runs plugins on a pool of their own (rule 4), and a response's header and body
are settled and written on whichever goroutine writes them, often a fib worker, by hooks that must
decide then and there. Calling the plugin there would run it on the worker.

fib's A41 (`OnHeaderAsync`) holds a response's header until an asynchronous call says to go on,
which is what `proxy_on_response_headers` needs. The body was still out of reach: an
`OnBodyFilter` filter runs on the goroutine writing the body and must not block, and
`proxy_on_response_body` would have to run in it, as would the trailer's callback at the body's
end. What fit needed from fib for that is A46, `OnBodyFilterAsync`: a filter whose call is done
elsewhere and comes back through a `done` callback, one at a time, with what is written meanwhile
held within the response's 64 KB bound and the writer paced as by a slow client, and whose last call
carries the trailer, which it may change. It was added to fib by fib's standards (rule 15).

## Decision

- **A plugin that exports a response callback follows the response** of each request it lets go
  on (one it answers itself, or pauses, does not): the request's context stays open on the instance
  that made it, since a Proxy-Wasm context belongs to its instance, and the response's callbacks are
  calls queued for that instance's goroutine, run before new requests, never on the goroutine
  writing the response. The response of `CONNECT` or an upgrade, which goes at once, is not followed.
- **The header** goes to `proxy_on_response_headers` while `OnHeaderAsync` holds it: the plugin
  reads it with `:status`, and changes its fields; the changes made to the response's fields in the
  request phase are applied first. The status cannot change, nor can the header once it has gone.
- **The body** goes through `proxy_on_response_body` as it is written, through `OnBodyFilterAsync`:
  each call is given what was written since the last, the plugin reads and replaces it with
  `proxy_get_buffer_bytes` and `proxy_set_buffer_bytes`, and what it lets go is sent. A plugin that
  pauses holds the body, and is called again with all it holds as more comes, as Envoy does, up to
  `max_body_bytes` (1 MiB by default). At the end, the body's last call carries the trailer, which
  `proxy_on_response_trailers` is given when there is one; a plugin may add one at the end of a body
  that had none. A response through a plugin given the body goes without `Content-Length`.
- **Bounds**: each call has the plugin's `timeout`; an instance follows at most `max_streams`
  responses at once (1024 by default), past which a request is answered 503; the queue of each
  instance's response calls is bounded by it. What fib holds for a call under way is 64 KB, and the
  client paces the plugin's output.
- **What fails**: a callback that fails, traps or runs past its timeout, a body held past
  `max_body_bytes`, and a local response sent once the response has begun (fib cannot replace a
  response whose header is decided) fail the response, closing its HTTP/1 connection or resetting
  its stream, as `on_error: fail` says; `on_error: skip` lets the rest of the response go on without
  the plugin, with what it held.
- **The context closes** once the body has ended (or the header, for a response without one), or
  when the request ends first: done, logged and deleted on its instance. The config the request was
  served with is held until then, so that a reload does not close the pool under a response.
- **`response`** says which callbacks a plugin is given: `all` (the default), `headers` (the body
  goes by untouched, and keeps its length), or `none` (the request phase alone, as before).
- Still not made: the request's body and trailer callbacks, calls out (`proxy_http_call`), shared
  data and queues, ticks, foreign functions, and resuming a paused stream.

## Consequences

- The Proxy-Wasm Go SDK's `http_headers` example, which adds its response fields in
  `OnHttpResponseHeaders`, and its `http_body` example, which holds the response's body until its end
  and appends, prepends or replaces it, run unchanged in I81's tests, beside a filter of fit's own
  that rewrites each part of a streamed body and its trailer, over HTTP/1.1, h2c, h2 and HTTP/3.
- A followed response costs more than the request phase alone: a call on the pool for its header,
  one for each part of its body that comes while none is under way, and one to close its context.
  A filter that only acts on requests can be given `response: none`.
- fit's own ABI (I71) still acts on requests only; its plugins change the response's fields as the
  header is settled.
