# fit development progress

[English](progress.md) | [简体中文](progress.zh-CN.md)

This file records what has actually been built, iteration by iteration, and where the next
session picks up. The [development plan](dev-plan.md) says what each iteration is meant to do;
this file says what was done, and where the work differs from the plan.

## Where things stand

| | |
| --- | --- |
| Last updated | 2026-10-19 |
| Milestone | M0, foundations: done (I01–I08). M1, the proxy core: I11, I12, I14–I17, I19, I21 and I22 done |
| fib | `github.com/lesismal/fib v1.3.0`, from the module proxy |
| What works | `fit run` serves fixed responses and static files, and proxies to HTTP/1.1 upstreams, over HTTP/1.1 and h2c, writes JSON access logs, reloads its config on SIGHUP, and shuts down gracefully; routes match by host and path, by priority and then specificity; `fit validate`, `fit schema`, `fit version`; the benchmark harness runs nginx, Caddy, Traefik and fit |
| What does not | no TLS; no health checks, so a failing address stays in rotation (I20); no HTTP/2 upstreams (I18); no precompressed files, fallbacks or directory listings (I13); no method, header or other matchers (I36) |

**M0 is done.** Its exit criteria hold, with one caveat: `fit run -c fit.json` serves a fixed
response on HTTP/1.1 and HTTP/2 (h2c; HTTP/2 over TLS comes with I09), and baselines for nginx,
Caddy and Traefik are checked in, but they were measured on Docker Desktop on a Mac, not on a Linux
host; see I08.

**Next iteration: I20, passive health checks**, which I19 unblocked, or I18, HTTP/2 upstreams.
I09, I13 and I24 are also ready to start, unless another session has taken them.

To continue, start a session with:

> Implement iteration I20 from docs/dev-plan.md. Read docs/progress.md, the index and that
> iteration's section in docs/dev-plan.md, and the design principles in docs/roadmap.md; then only
> the packages it lists. Keep the change within the iteration's scope, add tests, and when done
> mark it done in both dev-plan files and record it in both progress files.

## Code as built

| Package | Contents |
| --- | --- |
| `cmd/fit` | the CLI: `run` (with reload on SIGHUP), `validate`, `schema`, `version`, `help` |
| `config` | the native config types; a strict JSON parser and decoder with error paths and positions; `Value`; `Duration`; semantic checks |
| `module` | the module registry; `Handler`, `Middleware`, `Matcher`, and provisional `Service` and `Upstream` interfaces; `Provisioner`, `Cleaner`; `Env`, which gives `Provision` the config's upstream pools |
| `handler/respond` | the `respond` handler |
| `handler/proxy` | the `proxy` handler: forwards to a named upstream, drops hop-by-hop fields, sets `X-Forwarded-*` and `Forwarded` with a trusted-proxy list |
| `handler/static` | the `static` handler: files under a root, with a cache of small files, validators, conditional and range requests |
| `modules/standard` | imports every module fit ships with |
| `runtime` | `Compile(config, Env)` to an immutable `Graph` with a router per listener and a pool per upstream; `Runtime` with atomic swaps and graph reference counts |
| `listener` | binds a graph's listeners on one fib engine; dispatches each request to a route of the current graph; `Reload` to a new graph, keeping unchanged listeners and draining removed ones; graceful shutdown |
| `upstream` | `Pool`, which picks a backend by the upstream's policy (round robin, smooth weighted round robin, least connections) and sends requests to it over HTTP/1.1 through fib's asynchronous client, and drains before it closes; `StartEngine` for the engine pools dial through |
| `observe` | the access log: one JSON line per request, buffered and written by a goroutine of its own, dropping lines rather than blocking |
| `router` | `Router[T]`: picks a route by host and path, by priority and then specificity, without allocating |
| `schema` | JSON Schema generation; `fit.schema.json`, kept up to date by a test |
| `internal/fibtest` | runs fib handlers in tests without a network: `Serve`, `ServeRequest` |
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

## Notes for later iterations

- **fib version.** fit uses fib v1.3.0, tagged for I16 and I22 on fib's `main` of 2026-10-19: it
  brings `fib.New(config, bindings...)` with `AddBinding`, `RemoveBinding` and `ShutdownBinding`,
  `Connection.NotifyOutput`, `Context.HoldBody` and `BodyWriter`. If an iteration needs something
  newer, tag a fib release rather than committing a `replace` directive.
- **SO_REUSEPORT.** fib v1.3.0 listens with `ReusePort` on by default, which would let a second
  process bind a listener's address too. `listener` checks each address with a plain bind first and
  fails if it is taken.
- **Graph lifetime and async handlers.** `listener`'s dispatcher releases the graph when the
  handler returns. A handler that answers later keeps alive what it uses itself: the proxy holds
  the request with `Context.Retain` and relies on its pool draining before it closes (decision 004).
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
