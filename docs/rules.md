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
    or memory is compared before and after on the benchmark host, as rule 21 sizes it.
14. **A new dependency comes with a short decision note in `docs/decisions/`.**

## 7. When fib falls short

15. **What fib cannot do yet is added to fib, not worked around in fit.** Write down what fib should
    add or improve in [docs/fib-api-requests.md](fib-api-requests.md) and its Chinese twin: the
    problem, the evidence, the suggested API and how fit would use it. fit's sessions may then make
    the change in the fib repository themselves, by fib's own standards: tests, the race detector,
    vet and staticcheck for every OS fib builds for, its size and allocation checks, and a
    benchmark before and after for a change on the hot path. The change is pushed to fib's `main`,
    and the request's status records the version; fit then moves to that version (rule 19), never
    to a `replace` directive or a local `go.work` committed in fit, and carries on.
    fib's CI must pass on the commit before fit pushes what depends on it. Meanwhile, work that does
    not depend on it may go ahead; a stopgap in fit must still follow every other rule, and is
    recorded in the progress files with the fib request that will replace it.

## 8. Load testing

16. **Load tests cover Linux only, and the benchmark host's Linux is the reference.** Every load
    test and benchmark result fit goes by comes from the benchmark host. fit is not load tested,
    nor benchmarked, on Windows, macOS or other Unix systems, nor in Docker on Windows or macOS
    (Docker Desktop's VM and bridge network), and results from those are not used.
17. **While the benchmark host is available, load tests run there and nowhere else.** The local
    machine runs only ordinary tests: unit tests, the race detector, vet and the like.
18. **Only when the benchmark host is unavailable** may a load test run elsewhere, and then only
    on another Linux machine, with the machine recorded beside its results; otherwise it waits
    for the host.

## 9. The fib version

19. **During development fit depends on fib by commit hash, not by tag.** Moving to a fib change
    means `go get github.com/lesismal/fib@<commit hash>`, which puts the commit's pseudo-version in
    `go.mod`; fib need not tag a release for it. A tag is used only when the fib version wanted
    already has one.

## 10. Verification sized to the change

Every check costs waiting; each change gets the checks its kind needs, no more.

20. **Tests by reach.** While working, run the race detector on the packages a change touches and
    on those that depend on them: `make test-changed` (`scripts/test-changed.sh`; `test/e2e` comes
    with `cmd/fit`). The full `go test -race ./...` runs once, before a push. A change to docs or
    to tests alone runs no local suite; CI checks it.
21. **Benchmarks by kind.**

    | Change | Benchmark |
    | --- | --- |
    | Docs, tests, the control plane (config, Admin API, certificates, health checks, providers) | none |
    | The data plane off the hot path | none, or micro-benchmarks of the package |
    | The hot path | `bench/ab.sh` with the scenarios and protocols it touches, 10s runs, 3 alternating rounds; micro-benchmarks with `bench/microbench.sh` |
    | A milestone's end | the full harness, every server, and a report |

    A micro-benchmark comparison is `bench/microbench.sh` (10 alternating runs on the host,
    benchstat here); a change counts where benchstat says it is significant, and in `bench/ab.sh`
    where its summary says so: past 1% and the spread of the rounds.
22. **Benchmarks do not block work.** `bench/ab.sh` starts on the host and returns; the next work
    goes on while it runs, and its result is read with `bench/ab.sh -report` before the change it
    measures is pushed. A slowdown found then is fixed or recorded before anything builds on it.
23. **Fewer waits on CI and on each other.** A test that fails intermittently is fixed before
    more work is piled on its CI. fib changes are batched into one push, and fit checks its side
    on a branch while fib's CI runs. At most two sessions run full test suites at once, and one
    at a time uses the benchmark host.
