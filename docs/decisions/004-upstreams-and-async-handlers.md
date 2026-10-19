# 004: Named upstreams, a client engine, and pools that drain

[English](004-upstreams-and-async-handlers.md) | [简体中文](004-upstreams-and-async-handlers.zh-CN.md)

Status: accepted, in I15 (2026-10-19).

## Context

I15's proxy handler needs three things the code did not settle: where an upstream is written in
the config, which fib engine its connections are dialed through, and how a handler that answers
after it returns keeps what it uses alive, since the listener releases the graph when the handler
returns.

## Decision

- **Upstreams are a top-level, named list**, `upstreams`, and the proxy names one with
  `"upstream": "app"`, as nginx's `upstream` blocks and Traefik's services do. Health, load
  balancing and connection limits (I19, I20, I32) belong to the upstream, and several routes can
  share one pool. `config.Upstream` is the type, checked by `Config.Validate`.
- **`runtime.Compile(c, Env{Engine})`** makes one `upstream.Pool` per upstream before it provisions
  the modules, and hands them to modules through `module.Env`: `Provision` finds a pool with
  `ctx.Upstream(name)`. A nil engine makes pools that fail every request, for `fit validate`.
- **Upstream connections have an engine of their own**, started by `fit run` with
  `upstream.StartEngine` and stopped after the graph is released. fib v1.2.0 makes the listeners'
  engine from its bindings in one call (`fib.Bind`), after the graph is compiled; it cannot add a
  binding to a running engine. A separate engine costs a few idle goroutines and moves a proxied
  request from one event loop to another. When fit moves to a fib with `fib.New` and bindings that
  come and go, one engine for both is worth measuring.
- **A pool drains before it closes.** The graph's cleanup closes its pools after its modules, and a
  pool's `Close` waits for the requests in flight on it: it closes its client when the last callback
  returns. The dispatcher keeps the graph until the handler returns, by which time the handler has
  called `Pool.Do`, so a response that arrives after a graph swap still finds its pool open. The
  proxy holds the request itself with `Context.Retain` until it has written the response, and keeps
  nothing else of the graph.

Update, I22 (2026-10-19): fit moved to fib v1.3.0, whose engines take bindings while they run, so
one engine could serve both. The upstream engine stays separate anyway: since I16 a proxied
download waits in `Context.Write` for a slow client on the upstream engine's worker, and a separate
engine keeps those waits off the listeners' pool.

Update, I26 (2026-10-19): a handler that answers later now holds the graph itself, through
`module.Context.Graph()`, for as long as it serves the request: the proxy takes a hold when an
exchange starts and gives it back when the exchange is over, so a download that outlasts a reload
stays on the graph it began on. A graph swapped out is drained after the runtime's drain timeout:
its modules that implement `module.Drainer` end what they still have, as the proxy does by
cancelling its exchanges' contexts. Pools are shared: a graph takes over the previous graph's pool
for an upstream whose config and CA file are unchanged, and each pool counts the graphs holding it.

## Consequences

- A handler that answers later must keep alive what it uses after returning, as the proxy does with
  its pool; the graph does not wait for it. A per-request hold on the graph would need a hook when
  fib finishes a retained request, which fib v1.2.0 offers only through `Context.Context()`, and
  watching that takes a goroutine per request.
- A reload (I22) makes new pools, so connections are not carried over from one graph to the next.
  I26 can share pools between graphs whose upstream did not change.
