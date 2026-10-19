# fit development progress

[English](progress.md) | [简体中文](progress.zh-CN.md)

This file records what has actually been built, iteration by iteration, and where the next
session picks up. The [development plan](dev-plan.md) says what each iteration is meant to do;
this file says what was done, and where the work differs from the plan.

## Where things stand

| | |
| --- | --- |
| Last updated | 2026-10-19 |
| Milestone | M0, foundations: done (I01–I08). M1, the proxy core: done (I09–I23); its review fixes R01–R06, R05b, R07a and R07b are done; R07c is next. M2, dynamic config and TLS: under way (I24–I27, I32, I33) |
| fib | `github.com/lesismal/fib v1.5.2`, from the module proxy |
| What works | `fit run` serves fixed responses and static files, and proxies to HTTP/1.1, h2c and h2 upstreams, with passive and active (HTTP and TCP) health checks, nine load balancing policies (round robin, weighted, least connections, random, P2C, consistent hashing on IP, URI, header or cookie) and sticky cookies, over HTTP/1.1, h2c, TLS (HTTP/1.1 and h2, certificates chosen by SNI) and HTTP/3, writes JSON access logs, reloads its config on SIGHUP, when the file changes (`-watch`), or through the Admin API (read, load, edit by JSON pointer and merge patch, ETags against lost updates, rollback to a kept version), and shuts down gracefully; routes match by host and path, by priority and then specificity; `fit validate`, `fit schema`, `fit version`; the benchmark harness runs nginx, Caddy, Traefik and fit |
| What does not | no ACME or certificate management beyond files (I28–I31); no method, header or other matchers (I36) |

**M0 is done.** Its exit criteria hold, with one caveat: `fit run -c fit.json` serves a fixed
response on HTTP/1.1 and HTTP/2 (h2c; HTTP/2 over TLS comes with I09), and baselines for nginx,
Caddy and Traefik are checked in, but they were measured on Docker Desktop on a Mac, not on a Linux
host; see I08. R07b measured M0's fit on the Linux host: within 1.3% (HTTP/1.1) and 4.2% (h2c) of
today's fixed throughput.

**M1 is done.** I23's conformance runs and benchmarks are in the
[M1 report](../bench/results/m1-report-2026-10-19.md): h2spec passes everywhere, and on a Linux host
fit meets every M1 target in the roadmap, with its proxy above Caddy and Traefik but at 83% of
nginx's throughput over HTTP/1.1.

**Next: I28 (certificate manager and storage; the ACME decision) and I34 (Prometheus metrics).**
M2 has I24 and I25, the Admin API with config versions, patches, ETags and rollback, I26, graph
draining and shared pools, I27, the file provider, I32, active health checks, and I33, load
balancing v2.
R01 and R02, the review fixes that came before M2, are done, and so are R07a and R07b, the part of the
hot path work that needs nothing from fib, and R03–R06: fib has the APIs, and the proxy's downloads, the
static handler and the limits use them, sendfile included since R05b. R07c is next.

**Review against the rules (2026-10-19).** M0 and M1 were reviewed against the
[development rules](rules.md). The proxy's download path and the static handler wait on fib workers
without a time limit (reproduced: one client that stops reading stalls every request sharing its
h2c upstream connection), and several limits have no safe default. The fixes are iterations
R01–R07 in the [development plan](dev-plan.md#m1-review-fixes); what fit needs from fib is in
[fib-api-requests](fib-api-requests.md). **R01 and R02 change fit alone and come before M2**, so
before I24 and I32.

To continue, start a session with:

> Implement iteration I28 from docs/dev-plan.md. Read docs/progress.md, docs/rules.md, the index
> and that iteration's section in docs/dev-plan.md, and the design principles in docs/roadmap.md;
> then only the packages it lists. Keep the change within the iteration's scope, add tests, and when
> done mark it done in both dev-plan files and record it in both progress files.

## Code as built

| Package | Contents |
| --- | --- |
| `cmd/fit` | the CLI: `run` (with reload on SIGHUP, the Admin API and, with `-watch`, a change to the file), `validate`, `schema`, `version`, `help` |
| `admin` | `Configs`, the one path every new config takes to the running server (load, compile, `listener.Server.Reload`), one at a time, with the history of configs applied, each with an ETag; `Update`, a change made from the running config under the same lock, with If-Match; `Rollback`; the Admin API on net/http: `GET/PUT/PATCH/DELETE /config[/<pointer>]`, `POST /load`, `POST /rollback/{n}`, `GET /versions`, `GET /versions/{n}` |
| `config` | the native config types; a strict JSON parser and decoder with error paths and positions; `Value`; `Duration`; semantic checks |
| `module` | the module registry; `Handler`, `Middleware`, `Matcher`, and provisional `Service` and `Upstream` interfaces; `Provisioner`, `Cleaner`; `Env`, which gives `Provision` the config's upstream pools |
| `handler/respond` | the `respond` handler |
| `handler/proxy` | the `proxy` handler: forwards to a named upstream, drops hop-by-hop fields, sets `X-Forwarded-*` and `Forwarded` with a trusted-proxy list; streams both directions without waiting, paced by `HoldBody` and `OnDrain` up and `WriteNoWait` and `OnWritable` down |
| `handler/static` | the `static` handler: files under a root, with a cache of what paths resolve to and of small files' contents, every file operation on a bounded file pool, large files through `SendFile`; validators, conditional and range requests, precompressed twins, `try_files`, directory listings |
| `modules/standard` | imports every module fit ships with |
| `runtime` | `Compile(config, Env)` to an immutable `Graph` with a router per listener and a pool per upstream, sharing the previous graph's pools for unchanged upstreams; `Runtime` with atomic swaps, graph reference counts, and a drain timeout for graphs swapped out |
| `listener` | binds a graph's listeners on one fib engine, TCP and, for HTTP/3, UDP; dispatches each request to a route of the current graph; `Reload` to a new graph, keeping unchanged listeners and draining removed ones; graceful shutdown |
| `upstream` | `Pool`, which picks a backend by the upstream's sticky cookie and policy (round robin, smooth weighted round robin, least connections, random, P2C, consistent hashing on the client's IP, the URI, a header or a cookie) and sends requests to it over HTTP/1.1, h2c or h2 through fib's asynchronous client, and drains before it closes; passive health (`max_fails`) and active health checks, HTTP and TCP probes on timers, sent through fib's asynchronous client and dialer; `StartEngine` for the engine pools dial through |
| `provider/file` | `Watcher`, which polls the config file and applies it once it has stopped changing and its contents differ from what was applied last |
| `observe` | the access log: one JSON line per request, buffered in shards with a lock each and written by a goroutine of its own, dropping lines rather than blocking; `Track` and `EntryOf` for a request's entry |
| `tlsmgr` | the TLS configuration of a listener: certificates from files, TLS 1.2+, AEAD suites, ALPN from its protocols |
| `router` | `Router[T]`: picks a route by host and path, by priority and then specificity, without allocating |
| `schema` | JSON Schema generation; `fit.schema.json`, kept up to date by a test |
| `internal/certtest` | self-signed certificates for tests |
| `internal/fibtest` | runs fib handlers in tests without a network, as fib's servers run them, waiting for one that answers later: `Serve`, `ServeRequest` |
| `test/e2e` | builds the fit binary and drives `fit run` over the network |
| `bench` | the benchmark harness: `cmd/fitbench`, `cmd/idleconn`, configs for the four servers, results |
| `examples` | `hello.json`, `proxy.json` |

A config today looks like this:

```json
{
  "listeners": [
    {"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}
  ],
  "routes": [
    {
      "name": "hello",
      "listeners": ["web"],
      "priority": 10,
      "match": {"host": ["example.com"], "path": ["/hello", "/api/*"]},
      "middleware": [],
      "handler": {"module": "respond", "params": {"status": 200, "body": "hello\n"}}
    }
  ]
}
```

## Iterations

### I01 Repository skeleton and CI — done

- `go.mod` for `github.com/lesismal/fit`, Go 1.27 (fib's version), requiring fib v1.2.0.
- `cmd/fit` with `version`, which prints fit's version (settable with
  `-ldflags "-X main.version=..."`, otherwise the one Go records), fib's version from the build
  info, and the Go version and platform.
- `Makefile`: `build`, `test`, `race`, `vet`, `lint`, `fmt`, `fmt-check`, `schema`, `fuzz`, and
  `check` (fmt-check, vet, staticcheck, race tests).
- `.github/workflows/ci.yml`: gofmt; go vet and staticcheck for linux, darwin and windows; tests
  with the race detector on ubuntu and macOS; a 30-second run of `FuzzLoad`.
- `README.md` and `README.zh-CN.md`.

Differences from the plan: the packages are created as iterations fill them rather than up front,
since Go has no use for empty packages. CI has not run yet: it runs on the first push.

### I02 Config types v0 and strict loading — done

- Types: `Config`, `Listener` (name, address, protocols), `Route` (name, listeners, priority, match,
  middleware, handler), `Match` (host, path), `ModuleRef` (module, params).
- A decoder of fit's own, recorded in
  [decision 001](decisions/001-strict-config-decoding.md): unknown fields, wrong types, missing
  required fields, out-of-range numbers, values outside an enum and duplicate keys are all errors,
  every one reported (not only the first) with its path and line:column. A field whose name
  differs only in case gets a "did you mean" hint.
- `Config.Validate`: unique listener and route names, valid addresses (port 0 allowed, for tests),
  no two listeners on one address, no protocol listed twice, route listeners that exist, valid
  hosts (exact, `*.` wildcard or an IP) and paths (starting with `/`, `*` only at the end).
- A `Config` keeps what was written, without defaults, and marshals back to the same JSON; params
  keep their member order.
- Tests: table tests for every error kind, round-tripping, and `FuzzLoad`, which ran four million
  inputs without a failure.

Differences from the plan: a route has `middleware` (a list) and `handler` (one module) rather
than a single handler chain, which follows the roadmap's router → middleware → handler model and
makes the namespace of each module plain. Protocols accept `http/1.1` and `h2c` for now; I09 and
I10 add `h2` and `h3`.

### I03 JSON Schema and `fit validate` — done

- `schema.Generate` builds a draft 2020-12 JSON Schema from the config types' tags (through
  `config.Fields`, the same descriptions the decoder uses) and from the registered modules: a
  module ref's `module` is an enum of the namespace's modules, and its `params` follow that
  module's schema.
- `schema/fit.schema.json` is committed; `TestSchemaUpToDate` fails when it is stale, and
  `make schema` (or `go test ./schema -update`) rewrites it. The example config was checked
  against it with Python's `jsonschema`, which also rejected a bad config for each of its three
  errors.
- `fit validate -c file` loads the config, then compiles it, so module params are checked too;
  errors print as `file:line:col: path: message`, exit code 1. `fit schema` prints the schema.

Differences from the plan: the schema lives in a top-level `schema` package, not `config/schema`,
because it needs the module registry, which itself depends on `config`.

### I04 Module registry and core interfaces — done

- `module.Register`, `Lookup`, `Namespace`, `Load`, `Cleanup`. IDs are namespaced
  (`http.handlers.respond`); a config names the last part, and the namespace comes from where the
  name appears (`handler` → `http.handlers`, `middleware` → `http.middleware`).
- `Load` decodes params strictly into the module's struct, then calls `Provision` with a context
  whose `Errorf` points at a param; plain errors are given the params' path and position. An
  unknown module's error lists the modules the namespace has.
- `module.Handler` is fib's `http.Handler`, so fib's handlers and middleware can be reused.
  `Middleware.Wrap` is called once at compile time. `Service` and `Upstream` are marked
  provisional, to be settled in I14 and I19.
- `respond`: status (default 200, 1xx refused), headers (names and values checked) and body
  (refused on 204 and 304); the response is built once and shared, which fib allows.

### I05 Runtime graph and atomic swap — done

- `runtime.Compile` provisions every module, wraps middleware around handlers, gives each listener
  its routes (all routes, or those naming it) ordered by priority with ties in written order, and
  applies default protocols. It reports every failing module, and cleans up the modules it had
  provisioned when anything fails.
- `Runtime.Current` is lock-free: it retains the current graph with a CAS that refuses a graph
  whose count already reached zero, so a reader racing a swap never revives a graph being cleaned
  up. `Runtime.Swap` numbers graphs and releases the old one; the last `Release` cleans up its
  modules in reverse order.
- Tests, under the race detector: graphs swapped 300 times while 8 readers serve through whatever
  is current; no reader ever sees a cleaned-up module, and every graph is cleaned up exactly once.

Differences from the plan: route matching is not implemented; `Route` carries hosts and paths as
written, for I11's router.

### I06 HTTP entry points and `fit run` — done

- `listener.Start` binds every listener of the runtime's current graph as a binding of one fib
  engine, so they share one event loop, task pool and buffer pool. Each binding's handler is a
  `fibhttp.ServerHandler` around a dispatcher that takes the current graph per request, finds its
  listener by name, matches a route and calls the route's handler; it releases the graph when the
  handler returns. No route: 404. No graph: 503.
- Protocols map onto fib's HTTP config: `http/1.1` alone sets `DisableHTTP2` (which also refuses
  `Upgrade: h2c`), `h2c` alone sets `HTTP2Only`, and both serve HTTP/1.1, h2c with prior knowledge
  and `Upgrade: h2c` on the same port.
- `Server.Shutdown` is fib's `Engine.Shutdown`: listeners stop accepting, idle connections close,
  busy ones close after their response (HTTP/2 with GOAWAY); then the engine is closed.
- `fit run -c file [-shutdown-timeout 30s]` loads and compiles the config, prints one line per
  listener with the address it bound, and on SIGINT or SIGTERM shuts down, exiting 0, or 1 if the
  timeout passed first. A second signal kills the process. Config errors print as `fit validate`
  prints them, through a shared helper.
- Tests: `listener` serves two routes over HTTP/1.1 and h2c (host and path matching, 404),
  refuses the wrong protocol on single-protocol listeners, picks up a swapped graph, and fails on
  an address in use; a shutdown test holds a request in a blocking handler, checks that `Shutdown`
  waits for it over both HTTP/1.1 and h2c, that the request completes, and that the graph's module
  is cleaned up once. `test/e2e` builds the binary, runs `fit run`, gets responses over HTTP/1.1
  and h2c, sends SIGTERM with an idle connection open and expects a clean exit; a bad config exits
  1 with its error. Checked by hand with curl: prior knowledge, `Upgrade: h2c`, and SIGTERM.

Differences from the plan:

- Route matching was a linear stand-in in `listener/match.go` (exact and `*.` wildcard hosts,
  exact and prefix paths, first match in priority order, no allocations), so that routes mean what
  they say before I11. I11 replaced it with package `router`.
- The listeners are fixed when the server starts. A swapped graph changes the routes of listeners
  it shares by name; a listener it lacks answers 404, and a new one is not bound. I26 diffs
  listeners.
- A request pins the graph until its handler returns, not until its response is finished. That is
  enough for `respond`; a handler that answers later (the proxy, from I15) needs the graph held
  until the response finishes or is cancelled (`Context.OnFinish` and `OnCancel`, or a `Retain`).
- The e2e test does not cover in-flight requests, since no shipped module can hold one; the
  in-process `listener` test does. The e2e tests do not build on Windows, which has no SIGTERM.

### I07 Benchmark harness: throughput — done

- `bench/cmd/fitbench`, a Go program using the standard library and the `docker` CLI, recorded in
  [decision 002](decisions/002-benchmark-harness.md). `go run ./bench/cmd/fitbench` (or
  `make bench`) builds fit for Linux, builds the load generator image (wrk, h2load and curl on
  Ubuntu), runs each server from a pinned official image (nginx 1.31.4, Caddy 2.10.2, Traefik
  3.5.0; fit on `ubuntu:24.04`), and writes `bench/results/run-<time>.json`.
- Configs for the four servers in `bench/configs`, with the same routes on port 8080 (HTTP/1.1,
  wrk) and 8081 (h2c, h2load): `fixed`, a 1 KiB `static` file, and `proxy` to a fit backend
  answering the same fixed response.
- Before each run, curl checks status, protocol and body; then a warm-up, then the measured run.
  Each result records requests per second, latency (wrk percentiles through a Lua `done` hook that
  prints JSON; h2load mean, standard deviation and maximum), errors by kind, the server's CPU in
  cores and its memory, from its cgroup. The report records the CPU model, kernel, memory, versions,
  image digests and parameters. Containers are pinned to CPUs of their own; nginx gets one worker
  per server CPU.
- Tests: the wrk and h2load parsers on real output shapes, cgroup parsing, the CPU split, and name
  selection. The harness itself was run end to end several times, short and at full length.

Differences from the plan:

- A Go program rather than scripts, so the parsing is tested and the harness runs wherever Docker
  does.
- Traefik serves neither fixed responses nor files, so it runs only `proxy`; fit runs only `fixed`
  until I12 (`static`) and I15 (`proxy`) add theirs to `servers.go` and `configs/fit/fit.json`.
- Static files are served from a Docker volume: Docker Desktop's bind mounts held nginx to 14k
  requests a second against 350k from the volume.
- Every proxy keeps up to 4096 idle backend connections. With their defaults, Caddy (32) and
  Traefik (200) churned backend connections under 256 or 2048 requests in flight until ports ran
  out: Caddy fell to 1,600 requests a second and answered 502.
- It has been run on Docker Desktop (bridge network), not yet on a Linux host with
  `-network host`, which is the path published numbers should take.

### I08 Benchmark harness: idle connection memory and baselines — done

- `bench/cmd/idleconn` opens N connections, 64 at a time, makes one request on each and holds them
  until its standard input closes: `http1` (keep-alive after a 404 for `/idle`), `tls` (the same
  over TLS on 8443) and `ws` (a WebSocket the server proxies to a backend). `idleconn -serve-ws` is
  that backend.
- `fitbench -idle http1,tls,ws` measures each kind on a freshly started server: anonymous memory
  from the server's cgroup before the connections open and after they have sat for
  `-idle-settle`, divided by the number of connections. The TLS listeners use a throwaway ECDSA
  P-256 certificate the harness writes.
- [`bench/results/baseline-2026-10-19-macos-docker.json`](../bench/results/baseline-2026-10-19-macos-docker.json),
  with the defaults (30-second runs, 256 wrk connections, 64×32 h2load streams, 10,000 idle
  connections). `bench/README.md` says how to reproduce it.

Requests per second, and anonymous memory per idle connection (10,000 of each kind):

| | nginx | Caddy | Traefik | fit |
| --- | ---: | ---: | ---: | ---: |
| fixed, HTTP/1.1 | 811,923 | 249,350 | — | 1,018,822 |
| fixed, h2c | 914,776 | 163,137 | — | 3,651,820 |
| static, HTTP/1.1 | 724,292 | 155,336 | — | — |
| static, h2c | 663,958 | 112,823 | — | — |
| proxy, HTTP/1.1 | 170,101 | 97,915 | 142,490 | — |
| proxy, h2c | 158,202 | 69,446 | 85,954 | — |
| idle HTTP/1.1 | 531 B | 21,733 B | 22,812 B | 956 B |
| idle TLS | 29,943 B | 51,561 B | 51,928 B | — |
| idle WebSocket (proxied) | 22,281 B | 131,578 B | 130,963 B | — |

Run to run on this machine, the HTTP/1.1 numbers move by up to a quarter (nginx's fixed response
gave 1.11M and 0.81M in two full runs, with a few dozen wrk timeouts in the slower one); the h2c
and memory numbers move by a few percent. nginx's memory before any connection is 176 MB, its
preallocated connection tables; Caddy's is 13 MB, Traefik's 22 MB and fit's 9 MB.

Differences from the plan:

- Memory is the cgroup's anonymous memory rather than RSS: it covers every process in the
  container without counting shared pages twice, and leaves the page cache out.
- The baseline was measured on Docker Desktop on an Apple M3 Max (14 CPUs to Docker, bridge
  network), the only machine this session had. The ratios between servers are meaningful; the
  absolute numbers are not what a Linux host would give. Re-run it on a dedicated Linux host and
  check that in as the baseline to publish.
- The roadmap's WebSocket and HTTP/3 load generators are not in M0; see decision 002.

### I11 Router v1: host and path — done

- Package `router`: `New` builds a `Router[T]` from routes (hosts, paths, priority and a value
  of any type), and `Match(host, path)` returns the value of the route that serves a request, or
  false, which the listener answers with 404. Hosts are exact or `*.` wildcards (one label),
  ignoring case, a port, IPv6 brackets and a trailing dot; paths are exact or `*` prefixes; a route
  with no hosts or no paths matches all of them.
- When several routes match, the rule is: the highest priority; then the most specific host (exact,
  then wildcard, then none); then the most specific path (exact, then the longest prefix, then
  none); then the route written first. Host before path follows nginx and Caddy, which pick the
  server or site by name before looking at the path. The rule is in the package doc and in the
  `priority` field's description, so the schema says it too.
- How it is built: a map from exact host and one from wildcard suffix (`.example.com`) to a node,
  plus one node for routes with no hosts. A node keeps exact paths in a map and prefixes in a map
  per prefix length, tried longest first. A lookup is at most three host lookups, each one map
  lookup for the exact path and one per distinct prefix length, stopping as soon as nothing left
  can beat what was found. A mixed-case host is lower-cased into a stack buffer and looked up
  through `m[string(b)]`, which Go does without allocating.
- `runtime.Compile` builds a router for each listener (`Listener.Router`) from its routes;
  `Listener.Routes` keeps the routes in priority order, as before. `listener`'s dispatcher calls
  `Router.Match` with the request's `Host` and `URL.Path`; `listener/match.go` and its tests are
  gone.
- Tests: table tests for hosts and paths, and for ordering and ties (priority over specificity, a
  lower priority still serving what a higher one does not, exact host over wildcard over none, host
  over path, exact path over longer prefix over shorter prefix over none, ties to the route written
  first, a later route with a higher priority replacing an earlier one for the same pattern, and
  each pattern of a route matched on its own); `TestMatchAllocs` checks with `AllocsPerRun` that
  `Match` allocates nothing, for lower- and mixed-case hosts, wildcards, IPv6 and misses, on a
  router of 103 routes. `BenchmarkMatch`: about 60 ns and 0 allocations a lookup with 101 routes
  (Apple M3 Max). `runtime`'s test checks each listener's router picks among its own routes, and
  `listener`'s serve test now has a wildcard route written last that beats a route with no host,
  over HTTP/1.1 and h2c. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- Equal priorities used to go to the route written first; they now go to the most specific route,
  as the plan says. A config whose broader route came first, at the same priority, now has its
  narrower route win.
- `Router` is generic over the route's value, so that `runtime` can build it without `router`
  importing `runtime`. Host and path matching is built into the router rather than made of
  `module.Matcher` modules, which stay unused until I36 adds the matchers that need a request.

### I12 Static files I — done

- Package `handler/static`, module `static`, with params `root` (required), `index` (default
  `["index.html"]`, `[]` for none), `strip_prefix`, `max_file_bytes` (default 1 MiB) and
  `max_cache_bytes` (default 64 MiB). Registered in `modules/standard`; the schema is regenerated.
- A request: GET and HEAD only, else 405 with `Allow`; `strip_prefix` removed (404 when the path
  lacks it); a path with a `..` segment, a NUL or a backslash gets 400; the rest is cleaned and
  joined to the root, and stat'ed. A directory asked for without a trailing slash is redirected
  (301, query kept) to the path with one; with one, the first index file that is a regular file is
  served, else 404. A file asked for with a trailing slash, or anything not a regular file, is 404;
  a stat refused for permissions is 403.
- Files up to `max_file_bytes` are kept in memory while `max_cache_bytes` allows, with a header
  built once (`Content-Type` from the extension or sniffed, `Last-Modified`, `ETag`,
  `Accept-Ranges`) and shared by every response. A request without conditions or a range leaves
  through `WriteResponse` with that header and the cached bytes; one with them goes through
  `net/http.ServeContent`, which answers `If-None-Match`, `If-Modified-Since`, `If-Match`,
  `If-Unmodified-Since`, `Range` (multipart too) and `If-Range`. A larger file, or one past the
  cache's room, is served from disk through `ServeContent`. The cached copy stands while the
  file's size and modification time do; a file that is gone is dropped from the cache.
- `internal/fibtest.ServeRequest` runs a handler for a request built by the test, and finishes a
  response written through `Write` when the handler returns, as the server does.
- Tests: `handler/static` covers MIME types (by extension and sniffed), HEAD, empty files, escaped
  names, index files in order, redirects with and without a query, 404s, 405 with `Allow`; the
  traversal attempts `/../`, `/a/../../`, `%2e%2e`, `..%2f`, `..%5c`, `%00` and `/..` all get 400
  and never the file outside, while a name like `..dots` is served; every conditional and range
  case above, from memory and from disk; a changed file served anew with a new `ETag`, a removed one
  404 and dropped; the cache's limits; `strip_prefix`; `"index": []`; 403; and the params' errors.
  `test/e2e` serves a small and a 1 MiB file from the built binary over HTTP/1.1 and h2c, with
  HEAD, 304, ranges, an index, a redirect and a raw `..` path. `BenchmarkServeCached`: about
  1.8 µs, 672 B and 8 allocations a request for a cached 1 KiB file, the test context included
  (Apple M3 Max). `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The handler does not use fib's `http.FileCache`, which in v1.2.0 sends no `ETag` and answers
  the request itself, so knows nothing of index files and writes its own 404; see
  [decision 003](decisions/003-static-file-cache.md). The handler stats the file on each
  request, as nginx does by default, where `FileCache` on Linux would not.
- Any `..` segment is refused with 400, even one that would stay inside the root, as nginx
  refuses a path that climbs above the root; symbolic links are followed wherever they lead, as
  nginx and Caddy do by default.

### I14 Upstream pool v1 (HTTP/1.1) — done

- `config.Duration`: a length of time written as a string `time.ParseDuration` takes (`"10s"`,
  `"1m30s"`), never negative. The decoder and the schema know it as they know `Value`; the schema
  gives it a pattern, which a test checks against the decoder.
- Package `upstream`: `Config` has `addresses` (required, `host:port` each), `max_conns` (per
  address, no limit by default), `max_idle_conns` (per address, 32 by default, 0 keeps none),
  `idle_timeout` (60s) and `connect_timeout` (10s). `Config.Check` refuses an empty list, a URL
  (with a hint), a missing host or port, a port out of range and a repeated address, each with its
  path and position.
- `New(engine, config)` makes a `Pool` around a `fibhttp.Client` on a running engine, with the
  limits mapped onto `MaxConnsPerHost`, `MaxIdleConnsPerHost`, `IdleConnTimeout` and
  `DialTimeout`, and no overall `Timeout` (I17). fib's client already pools per `host:port`: it
  reuses keep-alive connections, queues requests past the cap, keeps some idle and closes them on
  a timer, so the pool adds the config, the defaults and the choice of address. `Pool.Do` takes the
  addresses in turn (an atomic counter, no lock), sets the request URL's scheme and host, leaves
  its `Host` alone, and hands it to the client, whose callback gets the response or the error.
  `Close` closes the idle connections and fails the waiting requests.
- Tests, against `httptest` backends that count their connections: 20 requests in turn over one
  connection; `max_idle_conns: 0` opens one per request; with `max_conns: 2` and two requests held
  by the backend, six more wait, no third connection opens, and all eight finish with at most two
  connections and two requests in flight; `idle_timeout: "100ms"` closes the idle connection and
  the next request opens a new one; `Close` closes the idle connection and refuses requests; two
  addresses take five of ten requests each over one connection each; a refused connect and a
  connect to TEST-NET-1 with `connect_timeout: "200ms"` fail promptly; and every config error with
  its message and position. `config` and `schema` test `Duration`. Ten runs of the package under
  `-race` pass; `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The upstream config is not in the config file yet: `upstream.Config` is a type the decoder
  takes, ready for I15 to place, either as a top-level `upstreams` list named by routes (as
  nginx's `upstream` blocks and Traefik's services are) or inline in the proxy's params (as
  Caddy's are). The schema is unchanged until then.
- `New` takes the engine to dial through. Where it comes from is I15's to settle: the listener
  creates the engine after the graph is compiled, so either the graph's pools are made once the
  engine exists, or a client engine of its own serves them. Sharing the server's engine keeps a
  proxied request on the event loops it arrived on, as nginx does.
- The pool spreads requests over several addresses in turn already, since one address per
  upstream would make `addresses` a list of one; I19 replaces that with its selection interface.
  `module.Upstream` and `module.Service` stay provisional until then.

### I15 Reverse proxy I: forwarding and headers — done

- Config: a top-level `upstreams` list of `config.Upstream` (`name` plus I14's fields), checked by
  `Config.Validate` (names, and the address checks that were `upstream.Config.Check`). The type
  moved from `upstream` to `config`, which `upstream` imports. See
  [decision 004](decisions/004-upstreams-and-async-handlers.md).
- `runtime.Compile(c, Env)` makes a pool per upstream through `Env.Engine` before provisioning the
  modules, passes them in `module.Env` (`module.Load` takes it; `Context.Upstream(name)` finds
  one), and closes them after cleaning up the modules. `fit run` starts a client engine with
  `upstream.StartEngine` and stops it after the graph goes; `fit validate` compiles with no engine,
  whose pools fail every request. `Pool.Close` now drains: it closes the client once the requests
  in flight have had their callbacks.
- Package `handler/proxy`, module `proxy`, params `upstream` (required), `trusted_proxies` (IPs and
  CIDRs) and `host_header`. It builds the upstream request from the client's method, path (escaped
  as received), query, headers and body (`c.Body()`, which fib reads whole by default), retains the
  request, calls `Pool.Do`, and in the callback writes the status, headers and body back, then
  releases. Hop-by-hop fields (`Connection` and what it names, `Keep-Alive`, `Proxy-Connection`,
  `Proxy-Authenticate`, `Proxy-Authorization`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`) are
  dropped both ways. The client's `Host` goes upstream unless `host_header` is set. No
  `User-Agent` is made up for a client that sent none. A client in `trusted_proxies` has itself
  appended to `X-Forwarded-For` and `Forwarded` and its `X-Forwarded-Proto` and `X-Forwarded-Host`
  kept; anyone else has all four replaced. `Forwarded` quotes IPv6 addresses and hosts with a port.
  A failed upstream request gets 502.
- Tests, through a real listener and client engine: method, an escaped path and query, the `Host`,
  repeated and custom headers and an 8 KB body reach the upstream over HTTP/1.1 and h2c, and the
  status and headers come back; hop-by-hop fields are dropped from a request and, with a raw
  backend, from a response; untrusted and trusted forwarded fields; `host_header`; 502 for a
  refused connect; HEAD keeps the upstream's `Content-Length`; 64 concurrent requests over both
  protocols; `Forwarded` elements for IPv4, IPv6 and unknown; config errors. `config` tests the
  upstream checks, `upstream` the draining and the engineless pool, `cmd/fit` validates
  `examples/proxy.json`, and `test/e2e` proxies through the built binary. Five runs of the proxy
  tests under `-race` pass; `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The plan's "one upstream" is a named upstream from the config's `upstreams`, which may have
  several addresses taken in turn (I14).
- `TE` is dropped like the other hop-by-hop fields, `TE: trailers` included; gRPC (I49) needs it
  kept, with trailers (I17).
- The proxy holds the request with `Retain`, not the graph; decision 004 explains why the pool's
  draining is enough.

### I21 Access logs — done

- Config: a top-level `access_log` with `output` (`"stdout"`, the default, `"stderr"` or a file
  appended to), `flush_interval` (1s) and `buffer_bytes` (4 MiB). `fit run` opens it, hands it to
  the graph through `runtime.Env.AccessLog`, and closes it after the graph, which writes out what is
  buffered. `fit validate` does not open it.
- Package `observe`: `AccessLog.Log` formats one JSON line (time in UTC with milliseconds,
  listener, route, client IP, proto, method, host, URI, status, bytes in and out, duration,
  upstream and upstream duration, user agent, referer) into a stack buffer and appends it to the
  log's buffer under a short lock; it allocates nothing. A goroutine of the log's own swaps the
  buffer out and writes it every `flush_interval`, or as soon as 64 KB wait. When `buffer_bytes`
  are waiting, because the output is slow or blocked, lines are dropped and counted, and the count
  goes to stderr once the output catches up: a request never waits for the disk. Strings are
  escaped by hand, invalid UTF-8 becoming U+FFFD, so every line is valid JSON.
- `listener`'s dispatcher, when the graph has a log, makes an `observe.Entry` (start, listener,
  route, bytes in), attaches it to the request's context with `observe.Attach`, and registers a
  `Context.OnFinish` hook that logs the line with the status and size fib sends. The proxy fills in
  the entry's upstream address and time before writing its response. 404s are logged too.
- Tests: `observe` checks a line's fields and escaping, IPv6 and missing fields, JSON string
  escaping against `encoding/json`, the flush interval, the flush when 64 KB wait, a blocked
  output (10,000 lines logged within a second with the writer stuck, every line either written or
  counted as dropped, the drops reported), `Close` flushing and dropping later lines, and file
  output. `handler/proxy` logs proxied and unmatched requests through a real listener over HTTP/1.1
  and h2c and checks every field; `test/e2e` runs `fit run` with a file log and finds the lines
  after SIGTERM. `BenchmarkLog`: about 416 ns and no allocation a line with 14 goroutines logging
  at once (Apple M3 Max). `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The plan's "a benchmark shows logging does not block workers" is shown by the blocked-output
  test and `BenchmarkLog`. A load test with logging on and off was to run on the benchmark host,
  which refused the session's ssh key; it can be run there with key-based login.
- The log belongs to the process, not the graph, so that a response finished after a graph swap
  still has a log to go to. I22 decides what a reload that changes `access_log` does, and whether
  SIGHUP reopens the file for log rotation.
- One log for every listener; per-listener or per-route logs, sampling and other formats can come
  when someone needs them. Under heavy contention the one lock shows (416 ns a line at 14
  goroutines); per-worker buffers are the fix if I23's numbers call for it.

### I16 Reverse proxy II: streaming and backpressure — done

- fit moved to fib v1.3.0 first (its own commit), which has what forwarding without waiting needs:
  `Context.HoldBody`, `BodyWriter` with `OnDrain`, and a client that sends a pushed body as the
  connection has room. fib's reference proxy, `fib-examples/gateway`, was the model.
- Listeners set `StreamRequestBody` with a threshold of 64KB (`listener.StreamThreshold`), and
  upstream pools `StreamResponseBody` with the same (`upstream.StreamThreshold`): smaller bodies
  still arrive whole and take I15's path, a single `WriteResponse`.
- Upload: a request whose body streams (`c.RequestBody() != nil`) goes upstream with a
  `fibhttp.BodyWriter` as its body and the client's `ContentLength` (-1 for chunked). The body comes
  through `c.OnBody`; each piece is written to the `BodyWriter`, which copies it and returns; past
  its limit (256KB) the callback holds the body with `c.HoldBody(true)`, which stops reading the
  client once 256KB wait there, and `OnDrain` resumes it when the upstream has taken the backlog.
  Nothing waits. `Content-Length` and `Expect` are no longer copied into the upstream request's
  header: the length is the request's own, and fib answers `100 Continue` itself.
- Download: a response whose body streams is taken with `resp.OnBody`; the first piece sends the
  status and header (with the upstream's `Content-Length` when it had one, so the client gets
  chunked only when the upstream sent it so), each piece is written with `c.Write` and flushed, and
  the last one finishes the response. The callback runs on the upstream engine's worker; when the
  client is slower, `Write` waits once 64KB are queued for it (HTTP/1) or the stream window is spent
  (HTTP/2), and while it waits the upstream connection is not read, so the upstream is slowed by
  TCP or by its own stream window. This is fib's intended pacing ("what holds the connection back
  is fn itself"), and it waits on the upstream engine's pool, never the listeners'. An upstream that
  fails mid-body closes an HTTP/1 client connection, so the truncation shows; one that fails before
  the first piece gets 502. A client that goes away mid-body ends the upstream response.
- Tests (`handler/proxy/stream_test.go`): 256MB down and 256MB up, each over HTTP/1.1 and h2c, with
  a `Content-Length` and chunked, through a real listener: downloads to a client that pauses 20ms
  after every 4MB, uploads to an upstream that does the same. Every byte is checked against a
  pattern with a prime period, so a lost, repeated or moved byte shows; lengths and framing are
  checked. The live heap (`/gc/heap/live:bytes`, sampled every 10ms) rose at most 10MB in any case,
  against a bound of 16MB; at 1GB it rose the same, so memory does not grow with the body. Under
  `-race` the tests move 64MB. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The plan's `HoldReads` link is `HoldBody` and `BodyWriter.OnDrain` for the upload, which hold the
  client's reads through fib, and fib's own pacing of `Context.Write` for the download, which holds
  the upstream's. fit never calls `HoldReads` itself.
- The download direction waits in `Context.Write` on a worker of the upstream engine. fib v1.3.0
  offers no write on a `Context` that reports a full queue instead of waiting; if I23's numbers
  show many slow clients tying up the upstream engine's pool, that is the API to ask fib for.
- An HTTP/2 client whose upstream fails mid-body gets its stream finished rather than reset; I17
  handles failures properly.

### I22 Reload on SIGHUP — done

- `listener.Server` now makes its engine with no bindings and adds each listener with
  `Engine.AddBinding`. `Server.Reload(g)` moves it to a new graph: a listener whose name, address and
  protocols are all unchanged keeps its binding, its port and its connections; the others are bound
  before the swap; then `Runtime.Swap(g)`; then the listeners `g` lacks, or that changed, are drained
  with `Engine.ShutdownBinding` in the background, for at most the drain timeout (`fit run`'s
  `-shutdown-timeout`). If a listener cannot be bound, the bindings made for `g` are removed, `g` is
  released, and the running graph stays. A listener replaced on the same address (new protocols, a
  new name) is bound beside the old one, which fib's SO_REUSEPORT allows, so there is no moment with
  nothing listening; where that fails, the old one stops listening first, its connections left to
  finish. `Addrs` reports the current listeners.
- `fit run` turns SIGHUP into a reload: it loads and compiles the file with the same upstream
  engine and access log, calls `Reload`, prints `fit: reloading`, a line per listener removed and
  added, and `fit: reloaded <file> (config version N)`. A config that fails to load, compile or bind
  prints `fit: reload failed; the running config stays` and its errors to stderr. Signals that arrive
  during a reload fold into one more. A changed `access_log` is reported as taking effect on
  restart.
- Tests: `listener` reloads with a listener kept (same port, and a keep-alive connection open across
  the reload sees the new routes), one changed (protocols), one added and one removed (it stops
  taking connections), and checks the graph version; a reload with an address already taken fails,
  unbinds the listener it had bound, and leaves the old graph serving; a listener on a fixed port
  changes its protocols in place. `test/e2e` runs eight clients against `fit run` — keep-alive and a
  new connection per request, HTTP/1.1 and h2c — through a reload that changes the route and adds a
  listener, and a reload of a broken config, which leaves the running one serving and prints its
  error: about 36,000 requests, none failed, in each of five runs. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- Listeners are kept by name, address and protocols together, not by address alone, so that two
  listeners on port 0 stay apart and a listener whose protocols change is served with the new ones.
- The access log is not reopened or replaced on reload; a changed `access_log` waits for a restart.
  Reopening the file for log rotation (nginx does it on USR1) is left for when someone needs it.
- Upstream pools are made anew for each graph, so a reload drops the idle upstream connections of
  the old one once it drains. I26 can carry pools over.

### I17 Reverse proxy III: timeouts, cancellation, errors — done

- `config.Upstream` gains `response_header_timeout` and `response_idle_timeout` (60s each by
  default); `connect_timeout` was there already. `upstream.Pool.Timeouts` hands the first two to the
  proxy, which enforces them, since only it knows when a response begins and a body goes quiet.
- The proxy's request handling moved to `handler/proxy/exchange.go`. Each upstream request carries a
  context the proxy cancels with a cause: the client going away (`Context.OnCancel`), the header
  timer (started at `Do`, stopped at the response), or the idle timer (started at a streamed
  response, reset on each piece of body). Cancelling makes fib's client abandon the request and end
  a body that is streaming.
- Failures before the response begins: a header or idle timeout, a connect timeout
  (`os.ErrDeadlineExceeded` from fib's dial) and a body held past `MaxBodyHold` give 504; a client
  body that failed gives 400, 408 for its read timeout, 413 past `MaxStreamedBodyBytes`; anything
  else 502. A client that went away gets nothing. After the response began: a body with a declared
  length is finished short, which fib turns into a closed HTTP/1 connection or a reset HTTP/2
  stream; a chunked one closes the HTTP/1 connection; on HTTP/2 a body of no declared length ends
  with a `Fit-Error` trailer, as fib's gateway does, since fib has no way to reset such a stream.
- Trailers: a response's trailers are forwarded, whole (in `Response.Trailer`) or streamed (the
  `Trailer` announcement kept through the hop-by-hop filter, the values set with
  `http.TrailerPrefix` before `Finish`). A request's trailers are forwarded too: a body with
  trailers goes through the `BodyWriter`, chunked, even when it arrived whole, and the values are
  copied into the upstream request's `Trailer` once the body ends — on HTTP/2 the names are first
  known then.
- Tests (`handler/proxy/failure_test.go`, not on Windows), over HTTP/1.1 and h2c: a connect to a
  listener whose backlog is full times out to 504; a backend that never answers hits
  `response_header_timeout` (504 after about 150ms); a backend that sends 200KB and goes quiet hits
  `response_idle_timeout`, and the client can tell the body was cut short, with and without a
  length; the same for a backend that drops the connection mid-body; a client that gives up before
  the header, one that closes a download after 256KB, and one whose upload fails all have the
  upstream request cancelled (the backend sees its request context end); trailers go both ways for
  small and streamed bodies; `statusOf` maps each cause. Three runs of the package under `-race`
  pass; `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The timeouts live on the upstream rather than the route; I43 adds per-route ones.
- An HTTP/2 response of no declared length whose upstream fails mid-body ends with a trailer rather
  than a stream reset, which fib v1.3.0 does not let a handler ask for.

### I19 Load balancing v1 — done

- Config: `config.Upstream` gains `weights`, one per address (1 to 1000), and `load_balancing`:
  `round_robin`, `weighted_round_robin` or `least_conn`. Left out, it is `round_robin`, or
  `weighted_round_robin` when weights are given. `Validate` refuses a weight count that does not
  match the addresses, and weights with `round_robin`.
- `upstream.Balancer` is the selection interface, `Pick() *Backend`; a `Backend` is an address, its
  weight and its count of requests under way. Round robin is an atomic counter. Weighted round robin
  is nginx's smooth algorithm under a mutex: weights 5, 1, 1 give `a a b a c a a`. Least connections
  scans every backend for the fewest requests under way for its weight (compared by
  cross-multiplying, without dividing), from a starting point that moves with each pick, so that
  idle backends share the load; it reads the counts without a lock.
- `Pool.Pick` picks and counts the request under way, `Pool.Send` sends it to that backend, and
  `Pool.Done` ends it; `Pool.Do` is the three for callers that are done when the callback returns.
  The proxy picks at the start of an exchange and calls `Done` when it is over, body and all, so a
  long download counts as busy for as long as it lasts. The pool's draining (I15) now counts from
  `Pick` to `Done` too, so a pool waits for streamed bodies before it closes. The access log's
  `upstream` field is the picked backend's address.
- Tests: round robin gives `abcabc` and exactly 1,000 picks each of 3,000; weighted round robin gives
  nginx's sequence and exact shares (5,000/1,000/1,000 of 7,000; 3,000/2,000 of 5,000); least
  connections shares idle backends evenly, skips busy ones until the others catch up, and keeps
  requests under way in proportion to weights 2:1; 8 goroutines pick concurrently under `-race` for
  each policy; `AllocsPerRun` finds no allocation in any `Pick`. Through a pool, weights 3:1 send 30
  and 10 of 40 requests, and with `least_conn` an address holding a slow request gets none of the
  next 20. `config` tests the new checks. `BenchmarkPick`, 14 goroutines (Apple M3 Max): 52 ns round
  robin, 107 ns weighted, 67 ns least connections, no allocations. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- Weights are a list beside `addresses`, as Caddy has them, rather than objects in place of the
  address strings, which keeps `addresses` as simple as I14 made it.
- Weighted least connections came for free, so `least_conn` takes weights too, as nginx's does.

### I20 Passive health checks — done

- `config.Upstream` gains `max_fails` (1 by default, 0 for none) and `fail_timeout` (10s), as
  nginx's: `max_fails` failures of an address within `fail_timeout` take it out of rotation for
  `fail_timeout`; after that it takes requests again, and one success clears its record.
- A `Backend` keeps its state beside its counters: `downUntil` (atomic, Unix nanoseconds, 0 when up)
  and, under a mutex only failures and the first success after them take, the failures in the
  current window. A pick reads `downUntil` and only reads the clock when it is set, so picking costs
  nothing more while everything is healthy. Every policy passes over backends that are down; round
  robin and least connections move their turn past them, so the load spreads evenly over the rest.
  If every backend is down, health is ignored rather than failing every request, since an address
  that may answer is better than none (nginx ignores health for a single server for the same reason).
- `Pool.Done(b, failed)` reports how the request ended. The proxy counts a failure when the
  upstream could not be reached, broke off, or timed out (502 and 504, and a response cut short);
  not when the client went away or failed to send its body (400, 408, 413). HTTP statuses are not
  failures. `Pool.Do` counts an error other than cancellation.
- Tests: two failures within the window take a backend down, a success brings it back, failures in
  different windows do not add up, `max_fails: 0` never takes it down; every policy skips a down
  backend and shares its load evenly, without allocating, ignores health when all are down, and
  takes the backends back after the window. Through a pool, an address whose port starts refusing
  costs one failed request and then gets none; the port listening again, the address rejoins after
  `fail_timeout` and takes half the requests. Through the proxy, an upstream with a refused address
  answers 1 of 20 requests with 502 and the rest from the live one. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- No retry: the request that finds an address failing still fails. Retrying it on another address
  is I43's.
- Transitions are not logged yet; metrics (I34) will report them.

### I18 HTTP/2 upstreams — done

- `config.Upstream` gains `protocol`: `http/1.1` (the default), `h2c` (HTTP/2 in the clear with
  prior knowledge) or `h2` (TLS, offering h2 and http/1.1 through ALPN, so a server that does not
  choose h2 is still reached), with `tls_server_name` and `tls_ca_file` for `h2`. `Validate` refuses
  the TLS fields without `h2`; `upstream.New` now returns an error, for a CA file that cannot be read
  or holds no certificate, which `Compile` reports with its position (and stops there, rather than
  also reporting every route that names the upstream).
- The pool sets fib's `UnencryptedHTTP2` for h2c, and a `tls.Config` (TLS 1.2 at least, the CA file
  in place of the system roots when given) for h2, whose requests go to `https://`. fib's client
  multiplexes requests over each HTTP/2 connection up to the server's `SETTINGS_MAX_CONCURRENT_STREAMS`
  and opens another past it, within `max_conns`.
- Proxy fixes the h2 tests found: an absent `User-Agent` is now sent as a present, empty field, which
  both fib's HTTP/1 and HTTP/2 clients take as "send none" (`[""]` made the HTTP/2 client send an
  empty one). A request's announced trailers are read from its `Trailer` header too, since an
  HTTP/2 client's arrive only with the end of the body, and Go's servers keep only announced
  trailers. And a race the HTTP/2 tests showed: fib's client reads a request's `Trailer` when it
  sends the header, which may be after the body has ended, so a whole body's trailers are now set
  before the request goes, and a streamed upload with trailers goes through a `trailerPipe` that
  fib's client reads on its own goroutine, filling the values in just before the end.
- Tests: the proxy tests of I15–I17 run as subtests against an HTTP/1.1, an h2c and an h2 (TLS)
  backend each — forwarding (which checks the upstream saw HTTP/2), hop-by-hop request fields,
  forwarded fields, `host_header`, 64 concurrent requests, HEAD, passive health, 256MB streamed each
  way with the heap bounded, the header and idle timeouts, an upstream that breaks mid-body (now an
  `http.ErrAbortHandler` panic, which closes an HTTP/1 connection and resets an HTTP/2 stream), client
  aborts, and trailers both ways. `config` and `upstream` test the TLS settings' errors. The stream
  tests' heap bound went from 16MB to 32MB: the heap is the test's HTTP/2 client's and backends' too.
  Two runs of the proxy package under `-race` pass; `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- There is no per-connection stream limit of fit's own: fib's client honours the server's, and
  `max_conns` bounds the connections. fib v1.3.0 has no setting to lower it.
- The rest of upstream TLS (client certificates, skipping verification, cipher choices) is I54's.
- A streamed upload with trailers costs a goroutine, the one exception to forwarding without one; a
  `BodyWriter` that takes the trailer with its close would remove it, and is worth asking fib for.

### I09 TLS entry from certificate files — done

- Config: a listener gains `tls: {"certificates": [{"cert_file", "key_file"}, ...]}`, and protocols
  gain `h2`, HTTP/2 over TLS. A listener with TLS serves `http/1.1` and `h2` when it names no
  protocols. `Validate` refuses `h2` without TLS, `h2c` with it, and TLS without certificates.
- Package `tlsmgr` (static certificates only, as the plan says): `ServerConfig` loads the pairs and
  returns a `tls.Config` with TLS 1.2 at least, the TLS 1.2 suites cut to ECDHE with AES-GCM or
  ChaCha20-Poly1305 (TLS 1.3's are all fine and not configurable), and ALPN from the protocols
  (`fibhttp.ConfigureTLS` for both, `h2` or `http/1.1` alone otherwise); and a fingerprint of the
  certificates and the ALPN list. A certificate that cannot be loaded is a config error at its path.
- `runtime.Compile` builds each TLS listener's config, so `fit validate` checks the files too.
  `listener` wraps the HTTP handler in `fibtls.NewServer`; its HTTP config treats `h2` like `h2c`
  for allowing HTTP/2. The certificates go in `Config.Certificates`, from which fib's handshake takes
  the first that suits the client's hello, SNI first, wildcards included, and the first of all for a
  name none has: fib runs such handshakes as state machines, with no goroutine, which a
  `GetCertificate` would have given up. A listener's reload identity includes the fingerprint, so a
  reload after the certificate files changed binds the listener anew, beside the old one, and new
  connections get the new certificate.
- Tests: `tlsmgr` checks the config for each protocol set, that fingerprints differ by protocols and
  by certificate, and the errors for a missing file, a file with no PEM and a key that does not match
  (with their paths); and the validation rules. `listener` serves two certificates by SNI (exact and
  wildcard) over HTTP/1.1 and HTTP/2, gives an unknown name the first, refuses TLS 1.1, serves only
  the protocols named, and picks up a new certificate on reload. `test/e2e` runs `fit run` with two
  host names, each with its own certificate, over HTTP/1.1 and h2, one answered by `respond` and one
  proxied, whose upstream sees `X-Forwarded-Proto: https`. `internal/certtest` writes self-signed
  certificates for the tests. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The certificates are chosen by fib's handshake from `Config.Certificates` rather than by a
  `GetCertificate` of fit's own; the certificate manager (I28) will need `GetCertificate` for
  certificates that change while the listener runs, and should keep the static ones on this path.
- A listener whose certificate files change is only re-read on reload; watching the files is
  I28's.

### I13 Static files II — done

- `static` gains `precompressed` (any of `br`, `zstd`, `gzip`; none by default), `try_files` and
  `browse` (off by default).
- Precompressed twins: for a file the handler would serve, it looks for `name.br`, `name.zst` and
  `name.gz`, in that order of preference among the encodings listed, ranked by the client's
  `Accept-Encoding` q values (wildcards and `q=0` refusals honoured, ties to the order listed), and
  serves the best twin on disk with `Content-Encoding`, the original's `Content-Type`, and its own
  `ETag` and `Last-Modified`, so conditions and ranges apply to that representation. With
  `precompressed` set, every file response carries `Vary: Accept-Encoding`. Twins are cached like
  files, by their own path; a twin too large to keep comes from disk with its `Content-Length`. It
  costs a stat per encoding tried, as nginx's `gzip_static` costs an open.
- `try_files` takes paths with `{path}` for the request's path, as nginx's does; one ending in `/`
  is a directory, served by its index file (or listed, with `browse`). The first that resolves is
  served, else 404; a directory asked for without its slash is not redirected under `try_files`.
  `["{path}", "{path}/", "/index.html"]` serves a single-page app. Each candidate passes the same
  traversal check as a request path.
- `browse` lists a directory with no index file: an HTML table, directories first, each group by
  name, dotfiles left out, with size and modification time; names are HTML-escaped and hrefs
  path-escaped, so `?`, `#`, `&` and `<` stay names.
- Sendfile: a file too large for the cache goes through `net/http.ServeContent` to fib's
  `Context.ReadFrom`, which on HTTP/1 sends it, whole or a range, by sendfile(2) without reading it
  into memory (since I12; now documented and in the code's comments). HTTP/2 frames it instead.
- Resolution is now its own step (`resolve`), shared by plain requests and `try_files`, and the
  cache and headers take a `variant` (the name the type comes from, the encoding, whether to vary).
- Benchmarks: fit now serves the harness's `static` (with `precompressed` on, which a 1KiB
  `small.html` with no twins exercises as a stat per encoding the client accepts) and `proxy`
  scenarios, as nginx and Caddy do; `bench/configs/fit/fit.json` has the upstream with 4,096 idle
  connections, as the others keep. The config validates, and a local `fit run` of it serves the
  scenario's body over HTTP/1.1 and h2c. The harness run itself is for I23, on the benchmark host.
- Tests: encoding negotiation (preference, q values, ties, `*`, refusals, a missing twin, no
  `Accept-Encoding`), distinct ETags and a 304 for a twin, a range of a twin, a twin from disk, twins
  as plain files without `precompressed`; `acceptQ`; `try_files` for a single-page app (a file, a
  directory with and without its slash, a client route, a directory without an index, traversal),
  with nothing found, and with `browse`; listings (escaping, order, dotfiles, the parent link, 404
  without `browse`); the new params' errors. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The benchmark scenario is wired and checked but not run: the benchmark host refused the session's
  ssh key again, so the numbers come with I23.

### I10 HTTP/3 entry — done

- Config: protocols gain `h3`, which needs `tls`. It is opt-in, as nginx has it: a TLS listener that
  names no protocols serves HTTP/1.1 and h2.
- `listener`: a listener's binding is now up to two of the engine's, one TCP for its HTTP/1.1, h2c
  and h2, and one UDP for h3, on the port TCP got (so an address with port 0 gets the same port for
  both), served by `http3.NewHandlerWithConfig` with the listener's TLS config through
  `http3.ConfigureTLS` (TLS 1.3, ALPN `h3`) and request bodies streamed past 64KB as on TCP. The same
  certificates serve both. An `h3`-only listener binds UDP alone. The UDP port is checked with a
  plain bind first, as TCP's is. Reload and retire take both bindings; `Addr` reports the UDP address
  in `UDPAddr`. The fingerprint now hashes the protocols, so adding or dropping `h3` rebinds.
- Alt-Svc: the TCP side's dispatcher registers an `OnHeader` hook on every response that adds
  `Alt-Svc: h3=":port"; ma=86400` (fib's `http3.AltSvc`). The hook and the value are made once per
  listener, so registering it allocates nothing, and listeners without `h3` register none. fib hands
  the hook a copy of a header a handler passed to `WriteResponse`, so the shared headers of `respond`
  and of the static cache are left as they are; a test checks that.
- Tests: `listener` serves a route over h3 (fib's HTTP/3 client, `Proto` HTTP/3.0) on the TCP port,
  sees `Alt-Svc` naming it on HTTP/1.1 and h2 responses, 404s included; an h3-only listener takes no
  TCP; no `Alt-Svc` without `h3`; a reload that drops `h3` lets the UDP port go; and a `respond`
  shared by a listener with `h3` and one without gives `Alt-Svc` only to the first. `test/e2e` runs
  `fit run` with h3, reaches `/hello` over h2 and finds `Alt-Svc`, reaches it over h3 on the port
  Alt-Svc names, and shuts down cleanly. `go vet ./...` (also for Windows) and `go test -race ./...`
  pass.

Differences from the plan:

- A listener replaced on the same address binds a second UDP socket beside the old one for the time
  the old one drains; datagrams then split between the two by the kernel's hash, so a QUIC
  connection caught in that window may break, and its client reconnects. TCP connections are not
  affected that way.

### I23 M1 conformance and benchmarks — done

The [M1 report](../bench/results/m1-report-2026-10-19.md) has the details.

- Conformance (on the development Mac): h2spec 2.0.0 against the h2c and TLS h2 entries, answering
  with `respond` and through the proxy, 145 of 145 in each of the four runs; and
  `test/e2e/conformance_test.go`, a proxy conformance suite through the `fit` binary with requests
  written byte by byte (request smuggling with both lengths, conflicting `Content-Length`s,
  whitespace before a colon, missing and doubled `Host`, a bad chunk size, a NUL in a field, a header
  past the limit, obs-fold, `Expect: 100-continue`, an HTTP/1.0 client, pipelining, an absolute-form
  target, 304, 204 and 1xx from the upstream, an upstream that does not speak HTTP, and
  `Connection: close` from either side).
- Found and fixed: fib v1.3.0 let `X-A : one` through as a field named `X-A `, where RFC 9112 asks
  for 400. The proxy refuses a request with a field name that is not a token, so it never reaches an
  upstream. The fix in fib is asked for as A9 in
  [fib-api-requests](fib-api-requests.md), with a proposed patch on a local fib branch,
  `fix-field-name-whitespace`, not pushed; the proxy's check is the stopgap until then.
- Benchmarks (on the benchmark host: Ryzen 7 7800X3D, Ubuntu 24.04, host networking): `fitbench`
  with its defaults for nginx 1.31.4, Caddy 2.10.2, Traefik 3.5.0 and fit, with idle connections at
  10k and 100k; the results are `bench/results/m1-2026-10-19-linux.json` and
  `m1-2026-10-19-linux-idle100k.json`. fit, in thousands of requests a second over HTTP/1.1 and h2c:
  fixed 783 and 3,811 (nginx 812 and 643); static 580 and 1,328 (nginx 440 and 373, Caddy 164 and
  125); proxy 215 and 230 (nginx 258 and 249, Caddy 129 and 94, Traefik 152 and 113). Memory per
  idle connection: HTTP/1.1 1.5 KiB at 10k and 1.1 at 100k (nginx 0.5, Caddy and Traefik 20 to 22);
  TLS 9.1 and 7.4 KiB (nginx 29, Caddy and Traefik 43 to 46). p99 below Caddy's and Traefik's in
  every scenario, above nginx's over HTTP/1.1. Every target of the roadmap for M1 holds.
- To run there, the harness gained `-root` and `-bin` (prebuilt binaries for a host without Go);
  `idleconn` opens connections from several loopback addresses (`-sources`, one per 40k), binds them
  with `IP_BIND_ADDRESS_NO_PORT`, and closes them with a reset, so 100k fit in the ports and leave no
  TIME_WAIT for the next run; fit serves the harness's TLS idle connections.
- `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- No fix was split into a new iteration: the review that came in meanwhile planned R01–R07, and the
  report's leads go to them. The proxy's 83% of nginx over HTTP/1.1, with 6.1 cores to nginx's 4.7,
  and fit's HTTP/1.1 p99 tail are R07's to look into, with the download path's waits, which R04
  removes.
- WebSocket idle memory at 100k could not be measured for any server: the proxy's connections to the
  one WebSocket backend address run out of ports at 64,504. The harness needs several backend
  addresses for it by I47, when fit proxies WebSockets. nginx closed about 45k of its 100k idle
  connections by its 75s `keepalive_timeout` while the rest were opening, so its 10k figures are the
  ones to compare.
- Against the M0 baseline (Docker Desktop on an M3 Max), fit's fixed HTTP/1.1 throughput went from
  1.25 times nginx's to 0.96, and its idle HTTP/1.1 memory from 0.9 to 1.5 KiB. The machines differ;
  measuring both commits on this host, for R07, settles whether fit moved.

### R01 Bounded waits on slow clients — done

- Config: `limits.write_stall_timeout` (30s when left out), in a new top-level `limits` object, with
  `Limits.WriteStall()` for the default. `runtime.Compile` hands it to modules through `module.Env`;
  `module.Context.WriteStallTimeout()` reads it at provision time.
- The proxy's download path writes each piece through `exchange.write`: `SetWriteDeadline(now +
  stall)`, `Write`, `FlushError`, then the deadline lifted. A client that stops reading fails the
  write once the deadline passes (fib closes its HTTP/1 connection or resets its HTTP/2 or HTTP/3
  stream), the upstream response is given up with `ClientResponse.Close`, and the worker goes back to
  the upstream connection. `Finish` gets a deadline too.
- The static handler hands `ServeContent` a `stallWriter` over the Context: each `Write` under the
  same set-and-lift deadline; `ReadFrom` keeps fib's `ReadFrom` (sendfile, which does not wait) on a
  plain HTTP/1 connection, and copies through the bounded `Write` over TLS, HTTP/2 and HTTP/3,
  where fib would copy through writes that wait. A Context that takes no deadline
  (`http.ErrNotSupported`) is written to without one.
- Tests (`handler/proxy/stall_test.go`), with a stall timeout of 1s and clients over HTTP/1.1, h2c
  and h3: the review's two cases, one client that stops reading a 200MB download from an h2c
  upstream with `max_conns: 1`, and eight such clients against an HTTP/1.1 upstream with
  `max_conns: 4`; in both, ten other requests are all served (within 1s and 2s of the stall
  respectively) and every stuck client's response is cut short. Run once without the deadlines, the
  other requests time out after 10s, as the review measured. A client that reads slowly and an
  upstream that pauses for twice the stall timeout keep their response. `handler/static`: a client
  that stops reading a 64MB file over h2c and over h3 has its response cut short (and keeps all of
  it without the deadline). `config` tests the new limit. `go vet ./...` and `go test -race ./...`
  pass; the streaming tests still pass.

Differences from the plan:

- `write_stall_timeout` is in a top-level `limits` object rather than on each listener: the handlers
  that bound their writes by it are shared by every listener a route serves, and R02 puts the
  listeners' other limits in `limits` too.
- The deadline is set before each write and lifted after it, rather than refreshed at most once a
  second: fib resets an HTTP/2 or HTTP/3 stream when its deadline passes even if nothing waits, so a
  deadline left in place would cut off a response whose upstream pauses, as a server-sent events
  stream does. As with nginx's `send_timeout`, only the time a write waits for the client counts.
- Neither the listener's `WriteTimeout` nor fib's HTTP/3 one is set: they bound a whole response,
  which would cut off long downloads.
- A file sent by sendfile on a plain HTTP/1 connection gets no deadline: no worker waits for it,
  and the kernel holds what the client has not taken. R05 (asynchronous file sending) gives it one.
- No benchmark before and after: the harness's scenarios take the paths R01 leaves alone (a small
  proxied body answered in one write, a cached file without conditions).

### R02 Safe default limits — done

- Upstreams: `max_conns` defaults to 256 per address (it had no limit); `max_pending` (1024) bounds
  the requests waiting for a connection, and one past it gets 503 at once; `response_timeout` (1h)
  bounds a whole response, through fib's client `Timeout`, so it gives 504 before the response
  begins and cuts it short after. Until fib counts waiting requests (R06), the pool counts requests
  under way from `Pick` to `Done` against `max_conns × addresses × streams + max_pending`, with one
  stream per HTTP/1.1 connection and 100, RFC 9113's floor, per HTTP/2 one; `Pick` returns nil past
  it, `Pool.Do` reports `upstream.ErrBusy`.
- `limits` gains `max_pending_bytes` (256 MiB), set on the listeners' engine as fib's
  `MaxPendingBytes`; and `max_header_bytes` (1 MiB) and `max_body_bytes` (16 MiB), set on every
  listener's HTTP/1, HTTP/2 and HTTP/3 config. `max_body_bytes` bounds streamed bodies too
  (`MaxStreamedBodyBytes`): since I16 every body past 64 KB streams, and fib's `MaxBodyBytes` alone
  left those unbounded. A reload that changes the header or body limit rebinds the listener; the
  engine's budget takes a restart. `listener.Server.Stats` returns the engine's counters.
- `static` gains `browse_max_entries` (10000): a listing reads at most that many entries, sorts
  those, and says when the directory has more, instead of reading it whole.
- Every default is in the schema: a `default` struct tag, checked as JSON when the type is first
  read, which the schema emits as JSON Schema's `default`; 23 fields carry one (the upstream's and
  the access log's fields, `limits`, `respond`'s status, `static`'s). Defaults that depend on other
  fields (a listener's protocols, `load_balancing`) stay in the descriptions. `examples/proxy.json`
  writes the limits and the upstream's settings out at their defaults.
- Tests that reach each limit: 256 connections open to one address and, past `max_pending`, a
  request refused at once (`upstream`); 503 past `max_pending` through the proxy, `response_timeout`
  before and during a response over HTTP/1.1 and h2c, and a streamed upload past `max_body_bytes`
  refused with 413 for a declared length and ended for a chunked one (`handler/proxy`); a header past
  `max_header_bytes` gets 431, and bodies past `max_body_bytes` 413 whole and streamed, over HTTP/1.1
  and h2c (`listener`); `max_pending_bytes` spent by a client that reads nothing, which fib's counters
  show as pending bytes past the budget and reads paused for it, until the client reads
  (`listener`); a listing cut at `browse_max_entries` (`handler/static`); and the schema's defaults
  (`schema`). The proxy tests' shared config raises `max_body_bytes` for the 256 MB streaming tests.
  `go vet ./...` and `go test -race ./...` pass.
- Benchmarks before and after, on the benchmark host, fit alone, two rounds each: static 577k and
  1.33M requests a second over HTTP/1.1 and h2c, proxy 215k and 228k, the same within 0.5% before
  and after. The harness's fit config now sets `max_conns: 4096` for its backend, as the others
  keep 4096 connections: the h2c proxy scenario has 2,048 requests in flight, past the new default.

Differences from the plan:

- The limits test found nothing in fit or fib but a test helper of I14's, `newBackend`, whose
  connection hook blocked once more than 64 connections had closed, stalling the backend: any test
  with a burst of more than about 96 connections hung. Its send no longer blocks.
- `max_pending_bytes` is fib's budget, which fib checks once a read round is done, so a request
  that arrives in one read is still served while the budget is spent; it holds back reading on, as
  the test shows through fib's counters. fib warns that a few peers that do not read can hold it.
  A peer that streams is cut off by R01's stall timeout; one whose responses were queued whole is
  not, and R06's connection limits are what bound how many such peers there can be.
- An explicit 0 for `max_conns`, `max_pending` or a `limits` field means its default, since the
  config cannot tell 0 from left out for these fields; no limit can be turned off.

### I24 Config versions and Admin API skeleton — done

- `admin.Configs` is the one path a new config takes, from SIGHUP or the Admin API (and later the
  file provider): `Apply(data, source)` loads, compiles and moves the server to it with
  `listener.Server.Reload`, under one lock, so that applies follow one another; on any failure the
  running config stays. It keeps the last `admin.history` configs (10) as they were written, with
  the version `runtime.Runtime.Swap` gave each graph, when it was applied and where it came from.
  `fit run`'s SIGHUP reload now goes through it; its output is unchanged.
- The Admin API (`admin.Server`), on net/http and off unless the config has `admin`:
  `GET /config` returns the running config as written, with its version in `Fit-Config-Version`;
  `POST /load` applies its body and answers the new version, the listeners added and removed, and
  warnings (changes to `access_log` or `admin`, which take a restart), or 400 with each config
  error's path, line, column and message, or 500 when a listener cannot be bound; `GET /versions`
  lists the configs kept and `GET /versions/{n}` returns one. `fit run` prints
  `fit: admin API on <address>`, and a line for each config the API applies.
- Config: a top-level `admin` with `address` (127.0.0.1:2019, or `unix/<path>` for a Unix socket,
  made 0600; a socket left by a process that is gone is replaced, one in use is refused),
  `token_file` (a bearer token every request must carry, compared in constant time), `history`,
  `max_config_bytes` (4 MiB) and `max_conns` (16). The address must be loopback or a socket unless
  `token_file` is set. Requests with `Origin` get 403, `POST /load` takes only `application/json`,
  and on a TCP address without a token the `Host` field must name a loopback host, so that neither
  a web page nor DNS rebinding reaches the API. Timeouts: 10s for the header, 60s for the body and
  the response, 60s idle; 64 KiB of header.
- Tests: `admin` loads a config and reads it back, and the listener serves it; refuses a config
  that does not decode, compile or bind (400, 400, 500), one sent as `text/plain` (415) and one past
  `max_config_bytes`, with a length (413 before reading) and chunked (413 at the limit), leaving
  the running config and history alone; the token (missing, wrong, another scheme, right; an empty
  token file); Origin and Host checks; history trimmed to `history`; eight concurrent loads, each
  applied whole with a version of its own; a client that stops sending its config, cut off at the
  read timeout while others are served, and one cut midway, which applies nothing; connections past
  `max_conns` closed at once and served again once others go; the Unix socket, its mode, a stale
  socket replaced and one in use refused; and the warnings. `config` tests the new checks;
  `test/e2e/admin_test.go` runs fit with the API, loads a config that adds a listener, reads it
  back and from `/versions/1`, is refused a broken one, and sees SIGHUP apply the file again as
  version 3. `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- No benchmark: the data plane is unchanged; the API runs beside it on goroutines of its own.
- Decision note [005](decisions/005-admin-api.md): net/http for the API, off by default, the token
  in a file, configs kept as written.

Differences from the plan:

- The Admin API is off unless the config has `admin` (`"admin": {}` turns it on at its default
  address), where Caddy's is on by default; see decision 005.
- `runtime` is unchanged: `Runtime.Swap` already numbers graphs, and the history, being control
  plane, lives in `admin` with the apply path.
- `GET /versions` and `GET /versions/{n}`, not in the plan, show the history; `/versions` rather
  than `/config/versions`, since I25 gives paths under `/config` to JSON pointers.
- The history is in memory only; SIGHUP applies the file again over a config loaded through the
  API, and a restart starts from the file.
- A listener at port 0 is kept by a new config only if it is still written as port 0: listeners
  are matched by the address as written, so the e2e test keeps `127.0.0.1:0` for the kept one.

### I32 Active health checks — done

- Config: an upstream's `health_check`, with `type` (`http`, the default, or `tcp`), `path` (`/`),
  `host`, `port`, `expect_status` (200–399 when left out), `interval` (5s), `timeout` (2s, no
  longer than the interval), `jitter` (a tenth of the interval, less than it), and `fails` and
  `passes` (2 each): the probes in a row that take an address out of rotation and put it back.
  `path`, `host` and `expect_status` on a `tcp` check are refused.
- `upstream`'s prober, made by `upstream.New` for a pool with an engine and stopped by `Pool.Close`
  (so `fit validate` probes nothing): each backend's probes follow one another, each started by a
  `time.AfterFunc` timer and finished in fib's callback, the next scheduled `interval` after the
  last began plus a random jitter, the first within a jitter of the start. No goroutine waits for
  a probe and none runs on a listener's worker. An http probe is a GET through a `fibhttp.Client` of
  the pool's own protocol and TLS settings but its own connections (two per address, none kept
  idle), so that it sees whether the backend takes new connections and never waits behind the
  pool's requests; its callback runs once the header has arrived (`StreamResponseBody`, threshold
  0) and the body is given up with `ClientResponse.Close`. A tcp probe is `Engine.DialWithHandler`,
  closed as soon as it opens.
- Results go to the backend's `downUntil`, the one field `Pick` reads, which is now
  `math.MaxInt64` while probes have the backend down, and otherwise what passive health set:
  probes and `max_fails` take a backend out each on their own account, and a request's success
  does not put back one its probes took out. `Pick` is unchanged; when every backend is down, health
  is still ignored. `Backend.ProbedDown` reports the probes' verdict.
- Tests (`upstream/probe_test.go`): a backend whose health endpoint turns to 503 leaves rotation
  and rejoins it within the configured window (two probes' intervals and jitter and the second's
  timeout, doubled for a loaded host), and requests through the pool avoid it meanwhile; a single
  failure under `fails: 2` keeps it; tcp probes against a port that closes and opens again; slow
  upstreams (answering after the timeout, or taking the connection and never answering) and a cut
  connection (a header broken off) are taken out, and a 1 GiB body is not waited for; `expect_status`,
  `host` and `port`; probes over h2c and h2 with a CA file; probes stop when the pool closes; probe
  connections are not kept; probes and passive health together. `config` tests the new checks and
  defaults. `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- `examples/proxy.json` has a health check and the Admin API.
- No harness benchmark: requests take the same path as before (`Pick` reads the same field), and a
  config without `health_check`, as the harness's, starts no probes. `go test -bench BenchmarkPick`
  (parallel, 14 threads, this Mac) before and after: round robin 54 and 57 ns, weighted round robin
  106 and 107, least connections 66 and 66, within the benchmark's run-to-run spread.

Differences from the plan:

- A reload makes new pools (as since I22), so health starts again with every backend up after a
  reload or an Admin API load, and a backend that was down rejoins until `fails` probes fail
  again. I26, which can share pools between graphs whose upstream did not change, would keep it.
- Each address may have two probe connections, not one: under the race detector, the connection
  of a probe that gave up on a large body took up to 750ms to finish closing in fib's client, and
  the next probe waited for it past its timeout. Nothing else in fit sees this.
- Nothing is logged when a backend leaves or rejoins rotation; I34's metrics will show it.

### I25 Admin API: patch, ETag, rollback — done

- `GET /config/<pointer>` returns the value a JSON pointer (RFC 6901) names in the running config;
  `PUT` sets it to the body (replacing a member or an element, adding a member, appending at `-`),
  `PATCH` merges the body into it as a JSON merge patch (RFC 7396, sent as
  `application/merge-patch+json` or `application/json`), and `DELETE` removes it. `/config` alone
  is the whole config: `PUT /config` replaces it as `POST /load` does, and `DELETE /config` is
  refused. A missing member or index gets 404, a body that is not JSON (or holds a key twice) 400.
- Every change applies a whole config through `Configs.Update`, which takes the running config,
  makes the change and applies the result under the lock that orders applies, so that changes from
  many writers each start from the one before. A change that leaves the config invalid is refused
  with its errors, and nothing changes.
- Each version has an ETag, the first 96 bits of the SHA-256 of the config as written, sent as
  `ETag` on every answer about a config and listed by `GET /versions`. A change with `If-Match`
  (one ETag, a list of them, or `*`) is made only if the running config is still that one, and is
  answered 412 otherwise; `POST /load` and rollbacks take it too. `GET /config` answers 304 to an
  `If-None-Match` that names the running config.
- `POST /rollback/{n}` applies version `n`'s config again, as a new version from the source
  `rollback`; a version no longer in the history gets 404.
- An edited config is written out by fit (`admin/jsonptr.go`: a tree of the document that keeps
  members in order and numbers as written, two spaces to a level); a config sent whole is kept as
  written.
- Tests (`admin/edit_test.go`): pointers and their escapes; getting, putting, appending,
  removing and merging in a document, and errors for missing members, indexes past the end or
  with leading zeros, and scalars; order, numbers and characters kept; reading a value, `PUT`,
  `PATCH`, appending and deleting a route, each served at once, and the config written out;
  edits that fail validation (an address, a module, a required field, an unknown field), that are
  not JSON or point nowhere, refused with nothing changed; ETags, `If-None-Match`, `If-Match` with
  the right, a stale, a listed and `*` ETag, a stale one refused for every kind of change; eight
  writers that read, change and write back with `If-Match`, retrying on 412, beside eight that
  append without it, all sixteen routes in the config once and as many versions; rollbacks, to a
  version still kept, with `If-Match`, and to one gone. `go vet ./...`, `GOOS=windows go vet ./...`
  and `go test -race ./...` pass.
- Decision note [005](decisions/005-admin-api.md) gains the formats: pointers, merge patch, the
  ETag, rollback.

Differences from the plan:

- `PATCH` is a JSON merge patch at the pointer rather than a replacement of the value there, which
  `PUT` already does; a JSON Patch (RFC 6902) document is not taken.
- The ETag is a hash of the config, not its version, since versions start again on a restart.
- An edited config's layout is fit's, not the one it was written in.

### I33 Load balancing v2 — done

- `load_balancing` gains `random` (by weight), `p2c` (of two backends drawn at random, the one
  with fewer requests under way for its weight), and `ip_hash`, `uri_hash`, `header_hash` and
  `cookie_hash`, consistent hashing on the client's IP address (without its port), the path and
  query, or the header or cookie `hash_key` names. A request without the header or cookie goes in
  turn. `sticky_cookie` (`name`, `path`, `max_age`, `secure`; always `HttpOnly` and
  `SameSite=Lax`) keeps a client on one address on top of any policy: a request whose cookie names
  an address that is up goes there, and any other is answered with the cookie of the address it
  went to, after the upstream's own `Set-Cookie` fields. The cookie's value is the first 64 bits of
  the SHA-256 of the address, so it is the same in every fit with the same addresses and says
  nothing more about them.
- The hash ring has 160 points per unit of weight for each backend, placed by its address alone
  (FNV-1a with MurmurHash3's finish), as nginx's `hash ... consistent` has; a key goes to the
  first point at or after its hash, or, when that backend is down, to the next point whose backend
  is up, so the keys of a backend that is down move and come back with it, and no others do.
  Every policy passes over backends that are down, unless all are, as before.
- `Balancer.Pick` and `Pool.Pick` take the request (nil when there is none, as for `Pool.Do`'s
  callers); `Pool.SetCookie` says what the proxy adds to the response. Nothing on the request path
  allocates: keys are read from the request in place, header names canonicalised and the
  `Set-Cookie` fields made when the pool is.
- Tests: distribution of `random` (even and by weight) and `p2c` (never the busiest of three,
  level as requests stay, by weight, one backend up); hash distribution for each key, even and by
  weight; the same key to the same backend; adding a fourth backend moves only keys to it, a
  quarter of them; a backend down moves only its own keys, which come back with it; the port not
  counting for `ip_hash`, the query counting for `uri_hash`; cookie parsing; requests without a key
  going in turn; no allocation a pick for the new policies and the sticky cookie; down backends
  passed over; sticky cookies (set without one, followed with one, replaced when its backend is
  down or it names nothing, attributes) in `upstream`, and through the proxy, with the upstream's
  own cookie kept, and `header_hash` keeping each user on one backend (`handler/proxy`); config
  errors. `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- Benchmarks before and after, on the benchmark host, fit alone, the proxy scenario (round robin,
  as the harness's config has it), two rounds each, alternating: over HTTP/1.1 215k and 216k
  requests a second before, 215k and 213k after; over h2c 228k and 228k before, 228k and 227k after,
  within 1%, about as far apart as two rounds of the same binary. `go test -bench BenchmarkPickNew`
  (parallel, 14 threads, this Mac): 1.5 to 4 ns a pick for the new policies, cookie_hash the most,
  none allocating.

Differences from the plan:

- Sticky sessions are a `sticky_cookie` beside `load_balancing` rather than a policy of their own,
  so that a client without the cookie, or whose address is down, is placed by any policy.
- Consistent hashing is by a ring, as nginx's, rather than rendezvous hashing: lookups take a
  binary search however many addresses there are.

### I26 Listener diffing and graph draining — done

- Listener diffing was I22's: unchanged listeners kept, new ones added with `AddBinding` before the
  swap, removed ones drained with `ShutdownBinding`. I26 adds what keeps long requests on the graph
  they began on, and what ends them.
- A handler that serves a request past its return holds its graph for it: `module.Env` carries the
  graph, `module.Context.Graph()` gives a `module.Holder`, and the proxy takes a hold when an
  exchange starts and gives it back in `exchange.release`. The graph, its modules and its pools stay
  until the last exchange on it is over.
- `Runtime.SetDrainTimeout` (30s by default; `listener.Server.SetDrainTimeout`, so `fit run`'s
  `-shutdown-timeout`, sets it too): `Swap` starts a timer on the graph it swaps out, stopped when
  the graph is cleaned up; when it fires, the modules that implement `module.Drainer` end what they
  have. The proxy derives each exchange's context from one of 32 drain contexts, picked at random so
  that exchanges starting at once do not share one lock, and `Drain` cancels them all: a response
  not yet begun gets 503, one under way is cut short, and neither counts against the backend.
- Pools are shared between graphs: `runtime.Env.Previous` names the graph being replaced, and an
  upstream whose config (as JSON) and CA file contents are unchanged takes over its pool with
  `Pool.Acquire`; `Pool.Close` gives a hold back and the last one closes it. Connections and active
  health (I32) carry over a reload; an upstream that changes gets a pool of its own. `admin.Configs`
  passes the running graph.
- Tests: `runtime` drains a graph a request still holds after the timeout and not before, cleans
  it up once the request lets go, never drains one whose requests ended first or with a negative
  timeout; shares the pool of an unchanged upstream and its CA file, not of a changed one or one
  whose CA file changed. `upstream`: a pool held twice. `handler/proxy`: a download that outlasts a
  reload keeps streaming through the shared pool until the drain timeout and is then cut off, while
  one begun after it goes on, and the backend stays in rotation; a request waiting for its response
  when drained gets 503. `test/e2e/churn_test.go` swaps the config 2,000 times through the Admin
  API, paced over six seconds, while eight clients send short requests (HTTP/1.1 and h2c, kept and
  new connections, a fixed response and the proxy) and four download second-long streams; the
  swaps change the fixed response each time, the upstream every 50 and add or remove a listener
  every 100: in each of three runs, about 337,000 short requests and 32 downloads, none failed.
  `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- Benchmarks before and after, on the benchmark host, fit alone, the proxy scenario: 216k and 214k
  requests a second over HTTP/1.1, 228k and 229k over h2c, within 1%. Only one round counts: in the
  second, another benchmark had started on the host (a WebSocket server at four cores), and both
  binaries fell by 8% to 33%; the rerun was stopped so as not to disturb it. To be measured again
  when the host is free.

Differences from the plan:

- The churn test's long connections are streamed downloads through the proxy, not WebSockets,
  which come with I47.
- Drained requests are those of handlers that answer after returning, the proxy's; a request whose
  handler is still running (a static file being sent) holds its graph through the dispatcher and
  ends as it would.
- Each proxied exchange registers its context with one of the proxy's drain contexts, one lock
  more a request; shared over 32 contexts, the benchmark does not show it.

### I27 File provider — done

- `provider/file.Watcher` looks at the config file's size and modification time every interval
  (1s), on a goroutine of its own; once they have stayed the same for the debounce time (500ms)
  after a change, it reads the file (at most 64 MiB) and, if its SHA-256 differs from what was
  applied last, applies it. An editor's several writes make one reload, a touch none, and a file
  renamed into place is seen as one written in place. A config that fails, a file that is gone or
  too large, is reported once, and the watcher goes on: the next edit is tried, a file that comes
  back is applied. `Applied` records contents applied another way.
- `fit run -watch` (and `-watch-interval`) starts it, printing `fit: watching <file>`; a change
  prints `fit: <file> changed; reloading` and is applied through `admin.Configs.Apply`, as SIGHUP
  is, with the same output; a SIGHUP tells the watcher what it applied. The watcher stops before
  shutdown begins, so that no config is applied meanwhile.
- Tests (`provider/file`): an edit applied, one renamed into place applied, a touch not; four writes
  in a row applied once, as the last; a broken edit reported once, then a fix applied; a file
  removed reported once and applied when it is back; a file past the limit refused; contents
  applied by SIGHUP not applied again. `test/e2e/watch_test.go` runs `fit run -watch`, edits the
  file and sees the new route served, a broken edit reported on stderr with the running config
  kept, and a fix applied. `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...`
  pass.
- Decision note [006](decisions/006-file-provider-polls.md): polling rather than file system
  events.
- No benchmark: nothing on the data plane changed.

Differences from the plan:

- The watcher polls rather than taking file system events; see decision 006.
- It is turned on by a flag of `fit run`, not by the config.

### R07a Hot path costs in fit — done

R07 is split in three ([dev-plan](dev-plan.md#r07a-hot-path-costs-in-fit)): R07a and R07b need
nothing from fib, and R07c waits for A7 and A11–A13.

- Access log (`observe`): the buffer is split into shards, a power of two up to GOMAXPROCS (at most
  64), each with its own lock, padded apart. A line goes to the shard of its client's address
  (`maphash` of `RemoteAddr`), so one connection's lines keep their order. A shard hands its
  buffer to the writer through a bounded channel once it holds 64 KB, under its lock, and the
  writer queues each shard's partial buffer the same way at each flush, so the channel holds every
  shard's buffers in the order they were filled. `buffer_bytes` bounds what is queued for the
  writer plus what the shards hold. A shard checks that against an atomic counter it updates once
  per buffer handed over, not once per line, so each shard can hold up to 64 KB more than the
  bound. Buffers written out are reused through a bounded free list. A line's timestamp is
  formatted by hand rather than by `Time.AppendFormat`, and JSON string escaping checks a byte
  table.
- `observe.Track(r)` replaces `Attach`: the entry and the context that carries it are one
  allocation (`tracked`, a `context.Context` whose `Value` answers for the entry).
- `listener`: each dispatcher keeps where its listener was in the last graph it looked in (graph
  version and index, behind an atomic pointer), so a request scans the graph's listeners only once
  per graph.
- `upstream`: smooth weighted round robin is worked out once per balancer as a schedule of
  `total weight / gcd` entries (at most 65,536; past that it stays under the lock), and a pick takes
  the next entry with one atomic add. A backend that is down is passed over by drawing another
  entry, so its share goes to the others by weight; when all are down, health is ignored as before.
- Tests: `observe` `TestShards` (16 clients logging at once at GOMAXPROCS 8; every line written or
  counted as dropped, each client's in order, nothing left counted), `TestTrack`,
  `TestAppendTime`; `listener` `TestDispatchFollowsReload` (the listener moving between positions
  across reloads, and a graph where it has no route); `upstream` `TestWeightedRoundRobinDown`,
  `TestWeightedRoundRobinSchedule` (gcd, and weights too many to precompute),
  `TestWeightedRoundRobinConcurrent` (exact shares over 8 × 7000 picks). The new
  `BenchmarkDispatch` serves a `respond` route through the dispatcher, with the access log on,
  from each P as a client of its own; `BenchmarkLog` now does the same. `go vet ./...` and `go test
  -race ./...` pass.
- Micro-benchmarks on the development Mac (M3 Max), before and after, median of five, ns/op at
  GOMAXPROCS 1, 8 and 16: `BenchmarkLog` 359, 335, 424 → 223, 49, 34; `BenchmarkDispatch` 1009,
  750, 852 → 969, 485, 387, with 13 → 12 allocations; `BenchmarkPick/weighted_round_robin` 17.2,
  116, 115 → 2.5, 45, 57, as plain round robin, where the shared counter is now the cost.
- On the benchmark host, fit alone, two rounds of each build alternating, thousands of requests a
  second (HTTP/1.1 and h2c): without an access log, fixed 781 and 3,768 → 780 and 3,770, proxy 212
  and 228 → 212 and 229, all within 0.4%; with the access log to `/dev/null`, fixed 732 and 2,108
  → 752 and 2,296 (+2.7% and +8.9%), proxy 207 and 226 → 208 and 228.

Differences from the plan:

- The dispatcher's listener lookup is a cache per graph version rather than an index built at
  compile time: the dispatcher outlives graphs, and a graph's listeners keep their slice, so the
  index is the position, found once.
- The schedule is not rebuilt when health changes: drawing again past a backend that is down gives
  the same shares by weight among those up, without a rebuild on the probe's goroutine.
- The access log still costs fixed h2c about 40% of its throughput (3.77M without, 2.30M with).
  Three of `BenchmarkDispatch`'s 12 allocations are fib cloning the response header because an
  `OnFinish` hook is registered, which cannot change it: asked of fib as
  [A13](fib-api-requests.md#a13-no-header-copy-for-a-response-that-only-onfinish-hooks). The
  request copy that `r.WithContext` makes waits for A7. Both are R07c's.

### R07b Proxy allocations — done

- `handler/proxy`: the exchange is allocated first and holds what the outgoing request is built
  from: the request itself (`base`, of which `out` is the copy `WithContext` makes), its URL and the
  four forwarding fields' values, whose header slices point into it. `clientAddr` returns the
  client's address as a string too, a part of `RemoteAddr` for an IPv4 address (netip parses only
  the canonical form, so it is that already) and formatted only for IPv6, once instead of twice;
  `forwardedElement` builds the element in one sized `strings.Builder`. A response that arrived
  whole is read into a buffer of its length with `io.ReadFull`, rather than through a
  `bytes.Buffer` that grew past it; the 400 and 503 bodies are package variables.
- `BenchmarkProxy` (`handler/proxy/bench_test.go`): small GETs through the proxy to an HTTP/1.1
  backend over keep-alive connections, from a raw client that allocates little. The test helpers
  `startFit` and `serveBackend` take a `testing.TB` for it. `TestForwardedElement` checks
  `clientAddr` on IPv4, mapped, IPv6 and zoned addresses and that an IPv4 one allocates nothing.
  `go vet ./...` and `go test -race ./...` pass.
- On the development Mac: 95 → 81 allocations and 13.0 → 11.3 KB per proxied request, net/http
  backend included; what fit's own code allocates is now the exchange, the header map, the
  `OnCancel`, timer and `onResponse` closures and the response header timer, the context and its
  copy of the request, the Forwarded element and the body read. The rest is fib's client, the
  backend and the client: a memory profile is the evidence for
  [A11](fib-api-requests.md#a11-the-body-of-a-client-response-that-arrived-whole-without-a-copy)
  and [A12](fib-api-requests.md#a12-fewer-timers-and-contexts-per-client-request), four timers a
  request among them.
- On the benchmark host, R07a's build against this one, two rounds each alternating, thousands of
  requests a second: proxy over HTTP/1.1 211.7 → 219.0 (+3.4%), p99 3.91 → 3.51 ms, over h2c
  228.4 → 229.6; with the access log, 204.5 → 211.8 (+3.6%) and 226.3 → 228.1.
- The M0 question from I23, settled on the benchmark host: fit at `f96aa03` (M0, fib v1.2.0) and
  this build, with M0's config, two rounds each: fixed 792 and 3,929 → 782 and 3,764 over
  HTTP/1.1 and h2c (−1.3% and −4.2%), with 19 → 28 MB resident. So fit's fall from 1.25 times
  nginx's fixed throughput to 0.96 between the M0 baseline and I23 is the change of machine
  (Docker Desktop on an M3 Max to the Linux host), not fit; the few percent that are fit's go to
  R07c.

Differences from the plan: none beyond what goes to fib. The proxy's response header timer stays
until fib's client has a setting for it (A12).

### R03 fib APIs for forwarding without waiting — done

- fib v1.4.0 (tag on `ecd62b0`) has A1–A7, A9, A11 and A13, and parts of A8 and A12. fit moves to
  it with no change of its own: `go.mod` and `go.sum` only. `go vet ./...` and `go test -race ./...`
  pass on it.
- [fib-api-requests](fib-api-requests.md) and its twin give each request's status, its commits,
  and how the API as built differs from what was asked. The differences that matter to the next
  iterations: `fib.Config.MaxConns` is engine-wide, not per binding (R06); `MaxPendingPerHost` is
  in `http` only (R06); `ResponseHeaderTimeout` is a client setting, which fits one client per
  upstream pool (R07c); `OnWritable` reports false until a response streams (R04); `SendFile` is
  not tied to `FileCache`, and leaves 206 and `Content-Range` to the handler (R05).
- Not done in fib: `WriteStallTimeout` (A8), which `WriteNoWait` makes unneeded; the
  `context.AfterFunc` per cancellable request (A12), which needs a cancel handle from `Do`. fib
  noted that a request requeued after its reused connection closed does not restart its header
  timeout.

Differences from the plan: none. fib's own session did the work in fib, as rule 15 asks.

### R04 Proxy downloads without waiting — done

- `handler/proxy`: the download path never waits. Each piece of the upstream body goes to the
  client with `Context.WriteNoWait`, flushed unless it is the last; once it reports more than the
  response's limit (64 KB) queued for the client, the upstream body is held with
  `ClientResponse.HoldBody(true)` and `Context.OnWritable` resumes it once half has gone. While it
  is held, an HTTP/1 upstream connection is read no further, and an HTTP/2 or HTTP/3 one gives
  this stream alone no more window, so a client that reads slowly holds back its own download
  and nothing else. `write_stall_timeout` still bounds each wait: the write deadline is set when
  the body is held and lifted when it resumes, and `OnWritable`'s function, told of the deadline
  passing or the client going, gives the response up. A mutex in the exchange orders the body's
  callbacks, which run on the upstream worker or fib's side pool once resumed, with
  `OnWritable`'s.
- An upload with trailers is an ordinary pushed body: a `BodyWriter`, sent chunked with the
  announced names in its `Trailer`, ended with `CloseWithTrailer` and the values the client's body
  ended with. `trailerPipe`, and the goroutine fib's client spent reading it, are gone.
- Tests: `TestStuckClientWithoutStallTimeout`, with the stall timeout at an hour, a client over
  HTTP/1.1, h2c or h3 that stops reading a 200 MB download from an h2c upstream with
  `max_conns: 1`: ten other requests are served within a second, fit's heap grows by less than
  32 MB, and the stuck client gets the whole body once it reads. On the R03 code, the others time
  out after 10 s. `TestStuckClientOnSharedConnection` now expects the others at once and waits
  out the stall timeout from when the client got stuck, not after the others. R01's other stall
  tests, the 256 MB streaming tests, the trailer tests and the SSE test pass unchanged.
  `BenchmarkProxyDownload` streams 64 MB through the proxy: on the development Mac, median of
  four, 1,915 → 2,260 MB/s over HTTP/1.1 and 1,275 → 1,290 MB/s over h2c. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- R01's tests do not all pass with the stall timeout at an hour, and cannot: with an HTTP/1.1
  upstream, a stuck download keeps the upstream connection it is read from, as it does in nginx,
  so under `max_conns` (`TestStuckClientsPastMaxConns`) it is the stall timeout that frees the
  connections for others. With an h2c upstream, where a stream can be held alone, nothing relies
  on it, which the new test shows.
- No benchmark host run: the harness's proxy scenario answers with a small body that arrives
  whole, which goes out in one `WriteResponse`, a path R04 leaves alone.

### R06 Connection and queue limits through fib — done

- `limits.max_conns` (100,000 when left out) is fib's `Config.MaxConns` on the listeners' engine: a
  connection past it is closed as it is accepted, and counted in `Stats().ConnsRefused`. Like
  `max_pending_bytes`, it is the engine's, set at start; a reload does not change it.
- An upstream's `max_pending` is fib's `ClientConfig.MaxPendingPerHost`, and `upstream.ErrBusy` is
  fib's `ErrTooManyPending`, which the proxy answers with 503 and which counts against no
  backend. R02's count of requests in flight, and its guess of 100 streams per HTTP/2 connection,
  are gone: `Pool.Pick` always picks.
- The benchmark config sets `max_conns` to 1,000,000 for the 100k idle connection runs;
  `examples/proxy.json` shows the default.
- Tests: `listener` `TestConnLimit` (`max_conns` 3 across two listeners: a fourth connection is
  closed unanswered and counted, and one opened after another closes is served); `upstream`
  `TestDefaultMaxConns` and `handler/proxy` `TestMaxPending` reach fib's cap. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- `max_pending` is per address, not per upstream: fib's cap is per host. Its doc says so.
- fib v1.4.0's cap also counted the requests waiting for a connection being dialed for them, so a
  cold burst past `max_pending` was refused while the connections `max_conns` allows were still
  opening: 179 of 256 opened in `TestDefaultMaxConns`'s burst. Asked of fib as
  [A15](fib-api-requests.md#a15-maxpendingperhost-not-to-count-requests-whose-connection-is-being-dialed),
  done in v1.5.1; the test sends its 256 at once again.
- No benchmark: neither limit is reached in the harness's scenarios, and `Pick` lost an atomic
  add and a compare.

### R05 Static files without blocking — done

- `handler/static`: no file operation runs on a fib worker. The cache keeps what a request path
  resolved to, a file with its index and its compressed twins, a directory to list or redirect,
  or nothing to serve, for `check_interval` (a new parameter, 1s when left out), along with the
  contents of small files as before. A request it can answer from that is answered on the worker
  without a system call; a conditional or ranged one goes through `ServeContent` over the bytes in
  memory, gathered and written whole. Anything else, a path not seen within the interval, a file
  to read, open or list, goes to a file pool of the package's own: `max(16, 4 × GOMAXPROCS)`
  goroutines and a queue of 4,096, shared by every static handler. A request the queue cannot
  take gets 503 at once, and one the pool has not answered within 10 s, waiting or on a disk
  that hangs, gets 503 then, its work's own answer dropped (`queued.claim`). The lookups are
  bounded at 16,384 paths, so a client asking for paths that do not exist cannot grow them past
  that.
- A file too large to keep goes out through fib's `SendFile`: `ServeContent` runs against a writer
  that takes the range it would copy (`rangeWriter`, `offsetFile`) instead of copying it, and
  `transfer` hands `SendFile` the range a piece at a time. Each piece is 64 KB where fib copies, so
  that fib never waits within one, and 256 KB on plain HTTP/1, where sendfile only queues it.
  Between pieces it waits for `OnWritable` under a write deadline of the stall timeout, as the
  proxy's downloads do. A request for several ranges of such a file gets the whole file, since
  `ServeContent` would copy a multipart body through a pipe. `stall.go` and its writes that wait
  are gone.
- `internal/fibtest.ServeRequest` serves through `fibhttp.Serve`, as fib's servers do, and waits up
  to `fibtest.Timeout` for a handler that answers later, which fib v1.5.1 allows (A14).
- Tests: `TestNoDiskOnHit` (a known path within `check_interval` is answered without a pool job,
  four requests to one job, and looked at again after it; a file too large to keep takes a job
  each time); `TestSlowDisk` (a pool whose one worker is stuck: a path that needs the disk gets
  503 after the pool's timeout, a cached one is answered at once, and past the full queue 503 at
  once); `TestStuckClient` over h2c and h3 now through `SendFile`; `TestSlowReader` (3 MB read at
  about 1.3 MB/s over HTTP/1.1 and h2c, past the 1s stall timeout, arrives whole);
  `TestConditional`'s several ranges from disk get the whole file; `TestChangedFile` sets the
  interval to 0. `go vet ./...` and `go test -race ./...` pass.
- On the benchmark host, fit alone, R06's build against this one, two rounds each, thousands of
  requests a second: static over HTTP/1.1 587 → 772 (+31%), over h2c 1,327 → 2,457 (+85%), with
  26 MB resident against 31.

Differences from the plan:

- Not fib's `FileCache`: it reads a file it misses on the caller's goroutine, which would be a fib
  worker, stats every request where it has no inotify, and sends large files through
  `ServeContent`'s waiting writes; nor does it have fit's index files, try_files, listings,
  zstd twins or ETags. The cache is fit's, and every file operation goes through the pool.
- A change on disk shows within `check_interval`, not at once, as nginx's `open_file_cache_valid`
  has it.
- Over plain HTTP/1 a file sent by sendfile is neither paced nor cut off by the stall timeout:
  fib v1.5.1 does not count a queued file in the connection's unsent output, so `OnWritable`
  always reports room (as R01 noted). Asked of fib as
  [A16](fib-api-requests.md#a16-a-file-queued-by-sendfile-counted-in-a-connections-unsent-output);
  `TestStuckClient/http/1.1` is skipped until R05b moves to the fib release that has it.

### R05b Files sent by sendfile paced and bounded — done

- fib v1.5.2 counts a file `SendFile` queued in the connection's backlog (A16, `6f66bcc`). fit moves
  to it and changes nothing else: the static handler's transfer, which waits on `OnWritable`
  between pieces under the stall timeout's deadline, now waits on plain HTTP/1 too.
- `TestStuckClient/http/1.1` runs: a client that stops reading a 64 MB file sent by sendfile is cut
  off after the 1 s stall timeout, as over h2c and h3; `TestSlowReader` still gets its 3 MB whole
  over HTTP/1.1 and h2c. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan: fib did not add a stall bound inside `SendFile`, so ranges still go a
piece at a time, 256 KB over plain HTTP/1 and 64 KB elsewhere. No benchmark: the harness's static
scenario serves a cached file, which does not take this path.

## Notes for later iterations

- **fib version.** fit uses fib v1.5.2. v1.4.0 brought the APIs of R03 (A1–A7, A9, A11, A13, and
  parts of A8 and A12), v1.5.0 the rest of A12 (`Client.Start`), v1.5.1 A14 and A15, and v1.5.2
  the first part of A16; see [fib-api-requests](fib-api-requests.md). v1.3.0, tagged for I16 and I22 on fib's `main` of
  2026-10-19, brought `fib.New(config, bindings...)` with `AddBinding`, `RemoveBinding` and
  `ShutdownBinding`, `Connection.NotifyOutput`, `Context.HoldBody` and `BodyWriter`; v1.3.1 adds
  `ClientConfig.StreamUnknownLengthAtHeader` (A10) and nothing else. If an iteration needs something
  newer, tag a fib release rather than committing a `replace` directive.
- **SO_REUSEPORT.** fib v1.3.0 listens with `ReusePort` on by default, which would let a second
  process bind a listener's address too. `listener` checks each address with a plain bind first and
  fails if it is taken.
- **Graph lifetime and async handlers.** `listener`'s dispatcher releases the graph when the
  handler returns. A handler that answers later holds the graph itself with
  `module.Context.Graph()` until it is done, and implements `module.Drainer` to end what it has when
  the graph is drained, as the proxy does (decision 004).
- **Proxied bodies of no length.** Until fib v1.3.1, fib's client handed the proxy a response body
  of no declared length only once 64 KB of it had arrived (`StreamResponseBodyThreshold`), so a
  slow trickle, as server-sent events are, reached the client late: in `handler/proxy/sse_test.go`
  the first event came after about 610 ms, when the stream ended, over HTTP/1.1, h2c and h2
  upstreams. fib v1.3.1 adds `StreamUnknownLengthAtHeader`
  ([A10](fib-api-requests.md#a10-stream-a-client-response-of-unknown-length-from-its-header)),
  which `upstream.New` turns on: such a body is handed over from its header, and bodies with a
  length are kept whole up to 64 KB as before. The test runs and passes on every upstream protocol.
  No benchmark: the harness's proxy backend answers with a `Content-Length`, which takes the same
  path as before.
- **Test hosts.** Unit tests and `go test` micro-benchmarks run locally. Load tests (the
  `fitbench` harness, wrk, h2load) run on the benchmark host, reached with `ssh -p 10013
  127.0.0.1`.
- **Benchmarks.** A new scenario or module that fit can serve goes into
  `bench/cmd/fitbench/servers.go` and `bench/configs/fit/fit.json`. nginx preallocates its
  connection tables (about 29 MB per worker with `worker_connections 65536`), which shows in its
  memory before any connection opens, not per connection.
- **staticcheck.** A staticcheck built with an older Go cannot read Go 1.27 export data. Build it
  as CI does (against the latest `golang.org/x/tools`) and pass it to make:
  `make check STATICCHECK=/path/to/staticcheck`.
- **Testing handlers.** `internal/fibtest.Serve` and `ServeRequest` run a fib handler through
  `fibhttp.NewStreamContext` with a recording stream, with no network. `Context.Respond` with a
  content type ignores `c.Header()`; a response with other fields goes through `WriteResponse`.
- **Error positions.** Error tests compare whole messages with line:column. Work out expected
  columns from the document rather than counting by hand.
- **License.** The repository has no LICENSE file yet (fib is MIT); that is the maintainer's call.
