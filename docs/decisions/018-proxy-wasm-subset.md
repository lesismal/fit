# 018: Proxy-Wasm's request phase, without its response callbacks yet

[English](018-proxy-wasm-subset.md) | [简体中文](018-proxy-wasm-subset.zh-CN.md)

Status: accepted, in I72 (2026-10-23).

## Context

I72 was to implement the Proxy-Wasm HTTP filter calls that common filters need, if I71's numbers
allowed, so that an existing filter runs unchanged. They do: a request through a plugin costs about
5 µs (I71), and through a Proxy-Wasm filter built with the Proxy-Wasm Go SDK about 8 µs, both
bounded. What a filter may do falls in two phases: as a request's headers arrive
(`proxy_on_request_headers`), and as the response's do (`proxy_on_response_headers`), with body
and trailer callbacks after each.

fit runs plugins on a pool of their own, never on a fib worker (rule 4). A request's callbacks fit
that: the request is retained, handed to the pool, and goes on from there. A response's do not: its
header is settled on whichever goroutine writes it, often a fib worker, by hooks that run then and
there (`OnHeader`, `OnBodyFilter`). Calling a plugin there would run it on a worker, or the header
would have to wait while the call runs on the pool, which fib cannot do: every way a response is
written, whole or streamed, over HTTP/1, HTTP/2 or HTTP/3, by sendfile or through filters, settles
its header at once.

## Decision

- **fit implements Proxy-Wasm v0.2.x's request phase**: a plugin that exports
  `proxy_abi_version_0_2_0` or `_0_2_1` is started as Proxy-Wasm (its root context, `vm_config`
  and plugin `config`), and each request is a context of its own: created, its request headers
  handed to the plugin, then done, logged and deleted. The header maps (request and response, with
  the pseudo-headers `:method`, `:path`, `:authority`, `:scheme`), local responses, the VM and
  plugin configuration buffers, properties (the request's own, and ones the config sets), logging
  and per-instance metrics work. A filter's changes to the response's headers, made then, apply
  when the response's header is settled.
- **The response's callbacks are not made**, nor are body and trailer callbacks; calls out
  (`proxy_http_call`), shared data and queues, ticks, foreign functions and setting properties or
  buffers answer `Unimplemented`. A filter that pauses a request without answering it, waiting to be
  resumed, fails as `on_error` says.
- **What the response phase needs from fib is asked for as A41**: a hook that holds a response's
  header back until an asynchronous call says to go on, its body held meanwhile within the
  response's bound. With it, `proxy_on_response_headers` can run on the pool as the request's
  callbacks do.

## Consequences

- Filters that act on requests run unchanged: I72's tests run the SDK's `http_routing` and
  `properties` examples as they are. Filters that add response headers in `on_request_headers` work
  too; those that do it in `on_response_headers`, as the SDK's `http_headers` example does, run, but
  what they do there does not happen.
- fit's own ABI (I71) has the same limit, and the same way out.
