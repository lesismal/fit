# fit development rules

[English](rules.md) | [简体中文](rules.zh-CN.md)

Every change to fit follows these rules. They are what the
[roadmap's design principles](roadmap.md#design-principles) require of the code.

## 1. Never block a worker

1. **Nothing on a fib worker blocks.** No synchronous network or disk I/O, `time.Sleep`, waiting on
   a channel, `Future.Wait`, holding a lock for long, or calling a third-party library that blocks.
2. **Upstream requests go through fib's asynchronous client, `Do(req, callback)`.** No
   `Go().Wait()` and no `net/http.Client` on the request path.
3. **A handler that answers after it returns** takes `c.Retain()` and calls `Release()` once it has
   answered. It uses `OnCancel` to learn that the client left, and cancels the upstream request.
4. **Work that can only block** (disk caching, DNS, ACME, WASM, blocking third-party libraries) runs
   on a bounded pool of its own, with a timeout. Waits between pools go one way only, so that the
   pools cannot deadlock each other.

## 2. Bodies are streamed and bounded

5. **Request bodies are taken with `OnBody` and forwarded as they arrive.** No `c.Body()`,
   `io.ReadAll` or `Request.Body` to gather a whole body. Upstream response bodies are streamed back
   the same way.
6. **The `data` an `OnBody` callback gets is valid only during that call**; copy it to keep it past
   the call. Borrow buffers from fib's bufferpool where possible.
7. **Only a feature that really needs the whole body buffers it, and always with a limit.** Past
   the limit it answers 413 or falls back to passing the body through.
8. **Backpressure is linked.** When one side's pending bytes pass the watermark, `HoldReads` the
   other side, and resume once they drain. No queue or buffer anywhere is unbounded.

## 3. Other protocols alike

9. **WebSocket, gRPC streams, CONNECT and TCP/UDP tunnels are driven by callbacks on both sides**,
   forwarded with `Tunnel` or connection callbacks, with backpressure both ways. They do not block,
   and do not buffer whole streams.

## 4. Timeouts and limits

10. **Every exchange with the outside has timeouts**: connect, first byte, idle and total.
11. **Every resource has a limit**: connections, streams, sessions, header size, body size and
    concurrency. Every limit has a safe default, so a config that sets none is still bounded.

## 5. The hot path

12. **What can be done when the config is compiled is not left to request time.** Middleware
    chains, matchers and fixed headers are built at compile time. Requests only read the immutable
    `Graph`: no global locks, and as few allocations as possible.

## 6. Verification

13. **Every data plane feature has tests** for slow clients, slow upstreams, large bodies and
    connections cut midway, all passing under the race detector. A change that affects throughput
    or memory is compared before and after with the benchmark harness.
14. **A new dependency comes with a short decision note in `docs/decisions/`.**
