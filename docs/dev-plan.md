# fit development plan

[English](dev-plan.md) | [简体中文](dev-plan.zh-CN.md)

This plan breaks the [roadmap](roadmap.md) into small iterations. Each iteration is one piece of
work that fits in one working session and one commit or pull request: small enough that the code it
touches, the docs it needs and its tests fit in one context, and done before the session runs out
of room.

- [How to work an iteration](#how-to-work-an-iteration)
- [Package layout](#package-layout)
- [Index](#index)
- [M0, foundations](#m0-foundations)
- [M1, the proxy core](#m1-the-proxy-core)
- [M1 review fixes](#m1-review-fixes)
- [M2, dynamic config and TLS](#m2-dynamic-config-and-tls)
- [M3, gateway features](#m3-gateway-features)
- [M4, platform integration](#m4-platform-integration)
- [M5, caching and extensions](#m5-caching-and-extensions)
- [1.0](#10)

## How to work an iteration

**Pick one.** [Progress](progress.md) says where the work stands and which iteration is next.
Otherwise, take the first iteration in the [index](#index) whose status is `todo` and whose
dependencies are all `done`. Iterations with no dependency between them may run in parallel, in
separate branches.

**Read only what it needs.** A session starts from:

1. [progress](progress.md), this file's [index](#index) and the iteration's own section;
2. the [design principles](roadmap.md#design-principles) in the roadmap;
3. the packages the iteration lists under **Touches**, and the fib docs or packages under **Read**.

There is no need to read the whole repository, the comparison, or other iterations' sections.

**Stay small.** Aim for at most about 1,500 changed lines, tests included. If an iteration turns
out bigger, stop at a working, tested point, split the rest into a new iteration (`I17b` and so
on) in this file, and finish the current one.

**Finish properly.** An iteration is done when:

- its **Done when** items hold;
- `go vet ./...` and `go test -race ./...` pass, and new code has tests;
- nothing on the request path blocks a fib worker (see principle 1);
- the change follows every rule in [the development rules](rules.md);
- its status in the index is set to `done`, in both the English and the Chinese file, in the same
  commit;
- [progress](progress.md) and its Chinese twin record what was built, how it was checked, where it
  differs from the plan, and which iteration is next;
- any decision it made (a library choice, a format) is recorded as a short note in
  `docs/decisions/NNN-title.md`.

**Session prompt.** A session can be started with:

> Implement iteration IXX from docs/dev-plan.md. Read docs/progress.md, the index and that
> iteration's section in docs/dev-plan.md, and the design principles in docs/roadmap.md; then only
> the packages it lists. Keep the change within the iteration's scope, add tests, and when done
> mark it done in both dev-plan files and record it in both progress files.

**fib.** fit depends on `github.com/lesismal/fib`, at a released version. A gap found in fib goes
into [fib-api-requests](fib-api-requests.md) for the maintainer, who changes fib and releases it;
fit then moves to that version (rule 15 of the [development rules](rules.md)). Until then the
iteration does what does not depend on the gap, and records any stopgap in the progress files.

## Package layout

The layout set up in I01 and filled in by later iterations. Names may change, but each iteration
should touch as few of these as it can.

| Package | Contents |
| --- | --- |
| `cmd/fit` | the CLI: `run`, `validate`, `adapt`, `build`, `version` |
| `config` | native config types, strict JSON loading |
| `schema` | the JSON Schema, generated from the config types and the registered modules |
| `module` | the module registry and the core interfaces |
| `modules/standard` | imports every module fit ships with |
| `runtime` | compiling config into an immutable graph, atomic swaps, graph reference counts |
| `listener` | listeners on fib bindings, protocol entry points, listener diffing |
| `router` | routers and matchers |
| `handler/...` | terminal handlers: `respond`, `static`, `proxy`, `fastcgi` |
| `middleware/...` | rewrite, headers, limits, auth, compression, cache, retry, breaker |
| `upstream` | upstream pools, load balancing, passive and active health |
| `l4` | TCP and UDP proxying, SNI routing, PROXY protocol |
| `tlsmgr` | the certificate manager, storage, ACME, OCSP |
| `admin` | the Admin API and config versions |
| `provider/...` | file, docker, kubernetes, consul, etcd |
| `observe` | access logs, metrics, tracing |
| `plugin/...` | compile-time modules and WASM |
| `adapter/...` | config adapters: friendly format, nginx |
| `bench` | the benchmark harness, configs for nginx, Caddy, Traefik and fit, results |
| `test/e2e` | end-to-end and conformance suites |
| `internal/fibtest` | running fib handlers in tests without a network |
| `examples` | example configs |

## Index

Status: `todo`, `doing` or `done`.

| ID | Iteration | Depends on | Status |
| --- | --- | --- | --- |
| **M0** | | | |
| I01 | Repository skeleton and CI | — | done |
| I02 | Config types v0 and strict loading | I01 | done |
| I03 | JSON Schema and `fit validate` | I02 | done |
| I04 | Module registry and core interfaces | I02 | done |
| I05 | Runtime graph and atomic swap | I04 | done |
| I06 | HTTP entry points and `fit run` | I05 | done |
| I07 | Benchmark harness: throughput | I06 | done |
| I08 | Benchmark harness: idle connection memory and baselines | I07 | done |
| **M1** | | | |
| I09 | TLS entry from certificate files | I06 | done |
| I10 | HTTP/3 entry | I09 | done |
| I11 | Router v1: host and path | I06 | done |
| I12 | Static files I | I11 | done |
| I13 | Static files II | I12 | done |
| I14 | Upstream pool v1 (HTTP/1.1) | I05 | done |
| I15 | Reverse proxy I: forwarding and headers | I11, I14 | done |
| I16 | Reverse proxy II: streaming and backpressure | I15 | done |
| I17 | Reverse proxy III: timeouts, cancellation, errors | I16 | done |
| I18 | HTTP/2 upstreams | I17 | done |
| I19 | Load balancing v1 | I14 | done |
| I20 | Passive health checks | I19 | done |
| I21 | Access logs | I06 | done |
| I22 | Reload on SIGHUP | I05, I06 | done |
| I23 | M1 conformance and benchmarks | I13, I18, I20, I21, I22 | done |
| **M1 review fixes** | | | |
| R01 | Bounded waits on slow clients | I13, I17 | done |
| R02 | Safe default limits | I13, I20 | done |
| R03 | fib APIs for forwarding without waiting (in fib) | — | done |
| R04 | Proxy downloads without waiting | R01, R03 | done |
| R05 | Static files without blocking | R01, R03 | done |
| R05b | Files sent by sendfile paced and bounded | R05, A16 | done |
| R06 | Connection and queue limits through fib | R02, R03 | done |
| R07a | Hot path costs in fit | I23 | done |
| R07b | Proxy allocations | I23 | done |
| R07c | Hot path costs through fib | R03, R07a, R07b | done |
| R07d | Access log cost | R07c | done |
| R07e | Hooks and files through fib (A16–A18) | R05b, R07d, I34 | done |
| R07f | The proxy's CPU per request, profiled (A20–A22) | R07e, I35 | done |
| **M2** | | | |
| I24 | Config versions and Admin API skeleton | I22 | done |
| I25 | Admin API: patch, ETag, rollback | I24 | done |
| I26 | Listener diffing and graph draining | I24 | done |
| I27 | File provider | I24 | done |
| I28 | Certificate manager and storage; ACME decision | I09 | done |
| I29 | ACME HTTP-01 and renewal | I28 | done |
| I30 | TLS-ALPN-01, OCSP stapling | I29 | done |
| I30b | HTTPS by default | I30 | done |
| I30c | ACME Renewal Information (ARI) | I29 | done |
| I31 | On-demand TLS | I30 | done |
| I32 | Active health checks | I20 | done |
| I33 | Load balancing v2 | I19 | done |
| I34 | Prometheus metrics | I23 | done |
| I35 | M2 exit tests | I25, I26, I27, I30b, I31, I32, I33, I34 | done |
| I35b | Certificate files watched | I27, I28, I34 | done |
| I35c | On-demand names dropped when idle | I31, I35b | done |
| **M3** | | | |
| I36 | Matchers v2 and priorities | I11 | done |
| I37 | Expression matcher | I36 | done |
| I38 | Rewrites, redirects and headers | I36 | done |
| I39 | Rate, concurrency and size limits | I36 | done |
| I40 | Basic auth and client mTLS | I36 | done |
| I41 | ForwardAuth | I40 | done |
| I42 | JWT and JWKS | I40 | todo |
| I43 | Retries and per-route timeouts | I17 | todo |
| I44 | Circuit breakers | I43 | todo |
| I45 | Traffic splitting: weights, mirroring, failover | I19 | todo |
| I46 | Compression | I36 | todo |
| I47 | WebSocket proxying | I17 | todo |
| I48 | WebSocket over HTTP/2 and HTTP/3 entries | I47, I10 | todo |
| I49 | gRPC proxying | I18 | todo |
| I50 | TCP proxying | I05 | todo |
| I51 | UDP proxying | I50 | todo |
| I52 | SNI routing and PROXY protocol | I50 | todo |
| I53 | One port, many protocols; SOCKS and CONNECT | I52 | todo |
| I54 | Upstream TLS | I18 | todo |
| I55 | M3 exit tests | I37–I54 | todo |
| **M4** | | | |
| I56 | Provider framework and effective config | I27 | todo |
| I57 | Docker provider | I56 | todo |
| I58 | Kubernetes Gateway API I: HTTPRoute | I56 | todo |
| I59 | Kubernetes Gateway API II: other routes and status | I58 | todo |
| I60 | Kubernetes Ingress | I58 | todo |
| I61 | Gateway API conformance | I59 | todo |
| I62 | Consul and etcd providers | I56 | todo |
| I63 | Shared certificate storage | I30 | todo |
| I64 | Dashboard | I56 | todo |
| I65 | OpenTelemetry | I34 | todo |
| I66 | Packaging and examples | I57 | todo |
| **M5** | | | |
| I67 | Cache I: HTTP semantics and memory tier | I23 | todo |
| I68 | Cache II: disk tier | I67 | todo |
| I69 | Cache III: locks, stale, purge | I68, I25 | todo |
| I70 | `fit build` and compile-time modules | I04 | todo |
| I71 | WASM I: runtime and plugin pool | I36 | todo |
| I72 | WASM II: Proxy-Wasm subset | I71 | todo |
| I73 | Friendly config format | I03 | todo |
| I74 | nginx config converter | I73 | todo |
| I75 | FastCGI | I17 | todo |
| **1.0** | | | |
| I76 | Continuous fuzzing | I55 | todo |
| I77 | Config and Admin API v1 freeze | I69, I74 | todo |
| I78 | Reference docs and migration guides | I77 | todo |

## M0, foundations

### I01 Repository skeleton and CI

- **Do**: `go.mod` for `github.com/lesismal/fit` on the Go version fib uses; the empty packages from
  the [layout](#package-layout) that M0 needs; `cmd/fit version`; a Makefile; CI running
  `go vet`, staticcheck and `go test -race`; a short README pointing at the docs.
- **Done when**: CI is green on `main`; `fit version` prints the version and the fib version.
- **Touches**: repository root, `cmd/fit`.

### I02 Config types v0 and strict loading

- **Do**: Go types for the native config: `listeners` (address, protocols), `routes` (match, a
  handler chain), `handlers` by module name with raw JSON params. Loading rejects unknown fields
  and reports errors with their JSON path. Only a `respond` handler (status, headers, body) for now.
- **Done when**: table tests cover valid configs, unknown fields, wrong types and missing fields,
  each with a precise error path.
- **Touches**: `config`.

### I03 JSON Schema and `fit validate`

- **Do**: a JSON Schema for the native config, published under `schema`; a test that keeps
  the schema and the Go types in sync; `fit validate -c file` that exits non-zero with readable
  errors.
- **Done when**: the sync test fails if a field is added to the types but not the schema, and the
  other way round.
- **Touches**: `config`, `cmd/fit`.

### I04 Module registry and core interfaces

- **Do**: interfaces for handler, middleware, matcher, service and upstream; a registry keyed by
  namespaced names (`http.handlers.respond`) with a provision step that decodes params and
  validates them; register `respond`.
- **Done when**: unknown module names and bad params fail at provision time with the config path.
- **Touches**: `module`, `handler/respond`.
- **Read**: Caddy's module model as summarised in [the comparison](comparison.md#caddy).

### I05 Runtime graph and atomic swap

- **Do**: compile a config into an immutable graph (listeners → routes → provisioned modules);
  hold the current graph in an atomic pointer; reference-count graphs so connections pin the graph
  they started on and modules get a cleanup call when the last reference goes.
- **Done when**: tests swap graphs while goroutines hold references and check that cleanup runs
  exactly once per graph, under the race detector.
- **Touches**: `runtime`.

### I06 HTTP entry points and `fit run`

- **Do**: start a fib engine, bind listeners from the graph, serve HTTP/1.1 and h2c, dispatch to the
  route's handler chain; `fit run -c file`; graceful shutdown on SIGINT and SIGTERM.
- **Done when**: an e2e test starts `fit run` with a `respond` route and gets the response over
  HTTP/1.1 and h2c; shutdown waits for in-flight requests.
- **Touches**: `listener`, `cmd/fit`, `test/e2e`.
- **Read**: fib `docs/http1.md`, `docs/http2.md`, `engine.go`, `binding.go`.

### I07 Benchmark harness: throughput

- **Do**: `bench/` with equivalent configs for nginx, Caddy, Traefik and fit (fixed response, small
  static file, proxy to a fixed backend); scripts that run wrk and h2load and write results as
  JSON with the hardware and versions.
- **Done when**: one command runs all four servers through all scenarios on a Linux host.
- **Touches**: `bench`.

### I08 Benchmark harness: idle connection memory and baselines

- **Do**: a Go tool that opens N idle connections (HTTP/1.1 keep-alive, TLS, WebSocket) and reads the
  server's RSS; add it to the harness; commit baseline results for nginx, Caddy and Traefik.
- **Done when**: `bench/results/baseline-*.json` exists and the README in `bench/` says how to
  reproduce it.
- **Touches**: `bench`.

## M1, the proxy core

### I09 TLS entry from certificate files

- **Do**: TLS listeners on `fib/tls` with certificates from files, chosen by SNI; ALPN for h2 and
  http/1.1; sensible TLS defaults (TLS 1.2+, modern cipher suites).
- **Done when**: e2e tests cover two host names with different certificates over HTTP/1.1 and h2.
- **Touches**: `listener`, `tlsmgr` (static certificates only).
- **Read**: fib `tls`, `GetCertificate` and the handshake pool.

### I10 HTTP/3 entry

- **Do**: HTTP/3 listeners on fib `http3`, sharing the certificate source with TLS; `Alt-Svc`
  advertised on the TCP listeners for the same host.
- **Done when**: an e2e test reaches the same route over h3 and sees `Alt-Svc` on h2.
- **Touches**: `listener`.
- **Read**: fib `docs/http3.md`.

### I11 Router v1: host and path

- **Do**: routers with host (exact and wildcard) and path (exact and prefix) matchers; explicit
  priority, then most specific first; 404 when nothing matches.
- **Done when**: table tests cover ordering and ties; lookups do not allocate on the hot path.
- **Touches**: `router`.

### I12 Static files I

- **Do**: a `static` handler on `http.FileCache`: root, index files, ETag, `Last-Modified`,
  conditional requests, range requests, MIME types; path traversal rejected.
- **Done when**: tests cover conditional and range requests and traversal attempts.
- **Touches**: `handler/static`.
- **Read**: fib `http/file_cache*.go`.

### I13 Static files II

- **Do**: precompressed twins (`.br`, `.zst`, `.gz`) chosen by `Accept-Encoding`; `try_files`-style
  fallbacks (for single-page apps); sendfile for large files; optional directory listing off by
  default.
- **Done when**: tests cover encoding negotiation and fallbacks; a benchmark scenario uses it.
- **Touches**: `handler/static`, `bench`.

### I14 Upstream pool v1 (HTTP/1.1)

- **Do**: upstream config (addresses, max connections, idle timeout, connect timeout); a pool per
  upstream on fib's asynchronous HTTP/1.x client; connection reuse and idle eviction.
- **Done when**: tests show reuse, limits and eviction against a test backend.
- **Touches**: `upstream`.
- **Read**: fib `http/client.go`.

### I15 Reverse proxy I: forwarding and headers

- **Do**: a `proxy` handler that forwards method, path, query and headers to one upstream and
  writes the response back; removes hop-by-hop headers; sets `X-Forwarded-For/Proto/Host` and
  `Forwarded`; a trusted-proxy list decides whether incoming forwarded headers are kept.
- **Done when**: header tests pass; small bodies round-trip.
- **Touches**: `handler/proxy`.

### I16 Reverse proxy II: streaming and backpressure

- **Do**: stream request and response bodies in both directions without buffering whole bodies;
  link backpressure (`HoldReads` on the side that is ahead when the other side's pending bytes pass
  a watermark).
- **Done when**: tests with a slow client and a slow upstream keep memory bounded while moving
  hundreds of megabytes; chunked and content-length bodies both work.
- **Touches**: `handler/proxy`.
- **Read**: fib `Connection.HoldReads`, `backpressure_test.go`, `http/body*.go`.

### I17 Reverse proxy III: timeouts, cancellation, errors

- **Do**: connect, response-header and idle timeouts; cancel the upstream request through
  `c.Context()` when the client goes away; map failures to 502 and 504; forward trailers.
- **Done when**: tests cover each timeout, a client abort mid-body and trailers.
- **Touches**: `handler/proxy`.
- **Read**: fib `http/context.go`.

### I18 HTTP/2 upstreams

- **Do**: upstreams over h2 (TLS) and h2c on fib's HTTP/2 client, with stream multiplexing over
  pooled connections and limits per connection.
- **Done when**: proxy tests from I15–I17 also pass against h2 and h2c backends.
- **Touches**: `upstream`, `handler/proxy`.
- **Read**: fib `http/h2_client.go`.

### I19 Load balancing v1

- **Do**: a selection interface and round robin, weighted round robin (smooth) and least
  connections; several addresses per upstream.
- **Done when**: distribution tests hold within tolerance; selection does not allocate.
- **Touches**: `upstream`.

### I20 Passive health checks

- **Do**: mark an address down after N failures within a window, skip it while down, retry it after
  a cooldown; like nginx's `max_fails` / `fail_timeout`.
- **Done when**: tests with a failing backend show it leave and rejoin rotation.
- **Touches**: `upstream`.

### I21 Access logs

- **Do**: structured JSON access logs with the usual fields (time, client, host, method, path,
  status, sizes, durations, upstream); buffered and written off the event loop; stdout or file.
- **Done when**: a benchmark shows logging does not block workers; fields are tested.
- **Touches**: `observe`.

### I22 Reload on SIGHUP

- **Do**: on SIGHUP, load and compile the config file; on success swap the graph; keep listeners
  whose address is unchanged, add and remove the rest; on failure keep the running graph and log
  the error.
- **Done when**: an e2e test reloads under load with no failed requests; a broken config leaves the
  old one serving.
- **Touches**: `cmd/fit`, `runtime`, `listener`.
- **Read**: fib `AddBinding`, `RemoveBinding`, `ShutdownBinding`.

### I23 M1 conformance and benchmarks

- **Do**: run h2spec on the h2 entry; a proxy conformance suite in `test/e2e`; run the harness for
  static and proxy scenarios and idle memory at 10k and 100k; fix what they find or split fixes into
  new iterations.
- **Done when**: results are committed under `bench/results/` and compared with the baselines in a
  short report.
- **Touches**: `test/e2e`, `bench`.

## M1 review fixes

A review of M0 and M1 against the [development rules](rules.md) on 2026-10-19 found code that breaks
them. These iterations fix it in three steps: R01 and R02 change fit alone and come before M2; R03
is work in fib, specified in [APIs fit needs from fib](fib-api-requests.md); R04–R06 and R07c use those
APIs. R07a and R07b are the part of R07, the hot path, that needs nothing from fib.

What the review found, and where each finding is fixed:

| Finding | Rules | Where | Fixed in |
| --- | --- | --- | --- |
| A proxied download waits in `Context.Write` on the worker that reads the upstream connection, with no time limit. One client that stops reading stalls every request sharing its h2c upstream connection, and a few exhaust `max_conns`; both were reproduced | 1, 8, 10 | `handler/proxy/exchange.go`, `onBody` | R01 bounds it, R04 fixes it |
| Static files are stat'ed, read and opened on the worker, and sent through writes that wait without a time limit; a directory listing reads the whole directory | 1, 4, 10, 11 | `handler/static` | R01 and R02 bound it, R05 fixes it |
| Listeners set no `WriteTimeout` or stall timeout | 10 | `listener/listener.go`, `httpConfig` and `http3Config` | R01 |
| An upstream without `max_conns` has no connection limit; requests waiting for a connection are not limited in number; a response has no total timeout | 10, 11 | `upstream/upstream.go`, `config` | R02, R06 |
| The engine limits neither total queued output nor connections | 11 | `listener/listener.go`, `Start` | R02, R06 |
| An upload with trailers holds a goroutine blocked in a pipe read | 1, 4 | `handler/proxy/exchange.go`, `trailerPipe` | R04 |
| A lock per access log line; a copy of the request and a context per request for the log entry; a scan of the graph's listeners per request; a lock per weighted round robin pick | 12 | `observe`, `listener`, `upstream/balance.go` | R07a, R07c |
| No test that a client that stops reading leaves other requests alone; no slow-client tests for static files | 13 | tests | R01, R05 |

The rest of M0 and M1 follows the rules: config, the module registry, the runtime graph, the router,
`respond`, the proxy's upload direction and the access log's writer.

### R01 Bounded waits on slow clients

- **Do**: a listener setting, `write_stall_timeout` (30s when left out), that bounds each wait for a
  client to take output. The proxy's download path and the static handler call
  `Context.SetWriteDeadline(now + stall)` before writing, refreshed at most once a second to spare
  system calls, so a client that stops reading loses its response (its HTTP/1 connection closed, its
  HTTP/2 or HTTP/3 stream reset) and the goroutine waiting on it is let go; the proxy then abandons
  the upstream response. Regression tests that reproduce the review: a non-reading client on a
  shared h2c upstream connection, and non-reading clients against `max_conns`, over HTTP/1.1, h2c
  and h3.
- **Done when**: in those tests other requests are served while the stuck clients are there, and
  the stuck ones end within the stall timeout; the streaming tests still pass.
- **Touches**: `config`, `schema`, `listener`, `handler/proxy`, `handler/static`, `test/e2e`.
- **Read**: fib `Context.SetWriteDeadline`, `Config.WriteTimeout`; the evidence in
  [fib-api-requests](fib-api-requests.md#evidence).

### R02 Safe default limits

- **Do**: a default for every limit the review found open, each settable in the config:
  `max_conns` per upstream address (256 when left out); `max_pending` per upstream, the requests
  waiting for a connection (1024 when left out; past it, 503 at once, counted by fit until R06);
  `response_timeout` per upstream, the total time a response may take (1h when left out); a
  top-level `limits` object with the engine's `max_pending_bytes` (256MB) and the listeners'
  `max_header_bytes` and `max_body_bytes` (fib's 1MB and 16MB, now visible); and
  `browse_max_entries` for directory listings (10000). Every default is in the schema.
- **Done when**: each limit has a test that reaches it; the schema and the examples show the
  defaults.
- **Touches**: `config`, `schema`, `upstream`, `handler/proxy`, `handler/static`, `listener`.

### R03 fib APIs for forwarding without waiting

- **Do**: the maintainer adds A1, A2 and A3 from [fib-api-requests](fib-api-requests.md) to fib,
  then A4–A7 and A11–A13 as fib schedules them, and releases a new fib version; fit moves to it
  with no change in behaviour.
- **Done when**: fit builds and passes its tests on the new fib release, and fib-api-requests says
  of each API whether it was delivered, changed or declined.
- **Touches**: fib; in fit, `go.mod` and the docs.

### R04 Proxy downloads without waiting

- **Do**: rewrite the download path with `WriteNoWait`, `OnWritable` and `ClientResponse.HoldBody`,
  as the upload path already uses `HoldBody` and `OnDrain`; replace `trailerPipe` with trailers on
  `BodyWriter` (A6) once fib has them.
- **Done when**: no proxy code calls a write that waits; R01's tests pass with the stall timeout
  raised to an hour, so nothing relies on it; a slow client holds back only its own upstream stream;
  memory stays bounded in the 256MB streaming tests.
- **Touches**: `handler/proxy`.

### R05 Static files without blocking

- **Do**: serve from fib's `http.FileCache`, with no stat per request; open, stat, read and list the
  files it misses on a bounded file I/O pool of fit's own, with a timeout; send large files with A3.
  Range and conditional requests keep working.
- **Done when**: no static code calls a file operation or a waiting write on a worker; tests with
  slow and non-reading clients over HTTP/1.1, h2 and h3, and with a slow disk (a test hook on the
  pool); benchmarks before and after.
- **Touches**: `handler/static`.

### R05b Files sent by sendfile paced and bounded

- **Do**: once fib counts a file queued by `SendFile` in a connection's unsent output (A16), move
  to that fib release, and run `TestStuckClient/http/1.1`; if fib's `SendFile` bounds its own
  waits, hand it whole ranges instead of pieces.
- **Done when**: a client that stops reading a file sent by sendfile over plain HTTP/1 is cut off
  after the stall timeout, and the skipped test runs.
- **Touches**: `handler/static`, `go.mod`.

### R06 Connection and queue limits through fib

- **Do**: the engine's connection limit (A4), as `limits.max_conns`; the client's pending limit (A5)
  in place of R02's count in fit.
- **Done when**: tests reach both limits, and R02's interim count is gone.
- **Touches**: `listener`, `upstream`, `config`.

### R07a Hot path costs in fit

R07 split in three: what fit can do alone (R07a, R07b) and what needs fib (R07c).

- **Do**, guided by I23's numbers: per-shard access log buffers, each with its own lock; the log
  entry and its context in one allocation; each listener's routes found without scanning the graph;
  smooth weighted round robin without a lock, as a precomputed schedule and an atomic index;
  benchmarks of the dispatcher and the log from several cores.
- **Done when**: nanoseconds and allocations per request, before and after, are in the progress
  record, and throughput does not regress on the benchmark host.
- **Touches**: `observe`, `listener`, `upstream`.

### R07b Proxy allocations

- **Do**: fewer allocations per proxied request in fit's own code: the outgoing request, its URL
  and the forwarding fields' values in the exchange's allocation; the client's IPv4 address
  without formatting it; a response that arrived whole read in one allocation; a benchmark of the
  proxy with a raw client. What fib's client allocates goes to fib-api-requests.
- **Done when**: as R07a, for the proxy scenario.
- **Touches**: `handler/proxy`.

### R07c Hot path costs through fib

- **Do**: the log entry in the Context's value slot (A7) instead of `r.WithContext`; a response
  that arrived whole written without a copy (A11); the client's per-request timers and contexts
  (A12), with the proxy's response header timer handed to fib's client; no header copy for a
  logged response (A13); then, on the benchmark host, the proxy's 83% of nginx over HTTP/1.1 and
  its p99 tail, as I23 left them.
- **Done when**: as R07a; the proxy's allocations per request, before and after, are in the
  progress record.
- **Touches**: `observe`, `listener`, `handler/proxy`, `upstream`.

### R07d Access log cost

- **Do**: R07c left fixed h2c about 30% slower with the access log on than off. Profile it on the
  benchmark host and take out what fit pays for each line: header lookups that canonicalize a
  key already canonical, durations formatted as floats, the date formatted on every line, a JSON
  string scanned a byte at a time, and contention on the log's shards; what is fib's goes to
  fib-api-requests.
- **Done when**: as R07a, for the fixed response with the access log on, over HTTP/1.1 and h2c.
- **Touches**: `observe`.

### R07e Hooks and files through fib (A16–A18)

Done in fib by fit's own session, with the maintainer's leave, after the M2 report.

- **Do**: in fib, a Context that keeps its response writer for the next request and a value slot
  without a lock (A17); `Context.Status` (A18); `SendFileStall` (A16). In fit, count the requests
  of a handler that answers before it returns without an OnFinish hook, and send a file's range
  whole through `SendFileStall`; then move to the fib release.
- **Done when**: fixed and static over h2c, with the access log and with metrics, before and after,
  on the benchmark host; `TestStuckClient` passes over every protocol with whole ranges.
- **Touches**: fib's `http`; fit's `module`, `runtime`, `listener`, `handler/respond`,
  `handler/static`.

### R07f The proxy's CPU per request, profiled (A20–A22)

The M2 report put fit's proxy at 86% of nginx over HTTP/1.1, with 1.5 times nginx's CPU per
request.

- **Do**: profile the proxy against nginx on the benchmark host; take out what is fit's, and write
  up what is fib's in fib-api-requests with the evidence.
- **Done when**: the CPU per request is split between the kernel, fib, fit and the runtime; fit's
  part is measured before and after with a micro-benchmark and the harness; fib's requests are
  written.
- **Touches**: `handler/proxy`; `docs/fib-api-requests`.

## M2, dynamic config and TLS

### I24 Config versions and Admin API skeleton

- **Do**: keep a history of applied configs with version numbers; an Admin API on localhost or a
  Unix socket by default, optional bearer token: `GET /config`, `POST /load`; apply through the
  same compile-and-swap path as SIGHUP.
- **Done when**: e2e tests load a config through the API and read it back.
- **Touches**: `admin`, `runtime`.

### I25 Admin API: patch, ETag, rollback

- **Do**: `GET/PUT/PATCH/DELETE /config/<path>` by JSON pointer; ETags and `If-Match` against lost
  updates; `POST /rollback/<version>`.
- **Done when**: tests cover concurrent writers, patches that fail validation and rollbacks.
- **Touches**: `admin`.

### I26 Listener diffing and graph draining

- **Do**: diff listeners between graphs: add with `AddBinding`, drain removed ones with
  `ShutdownBinding`; long-lived connections stay on the old graph until they end or a drain
  timeout passes.
- **Done when**: a churn test (thousands of swaps while traffic runs, including WebSocket-like
  long connections) drops nothing.
- **Touches**: `listener`, `runtime`.

### I27 File provider

- **Do**: watch the config file, debounce changes, apply through the Admin API's apply path; report
  errors without stopping.
- **Done when**: tests edit the file and see the change applied; a broken edit is reported and
  ignored.
- **Touches**: `provider/file`.

### I28 Certificate manager and storage; ACME decision

- **Do**: a certificate manager interface behind `GetCertificate` (static files move under it); a
  storage interface with a file system implementation and locks; a decision note choosing CertMagic,
  lego or our own ACME client.
- **Done when**: static certificates work through the manager; the ACME decision note exists in
  `docs/decisions`.
- **Touches**: `tlsmgr`, `docs/decisions`.

### I29 ACME HTTP-01 and renewal

- **Do**: obtain certificates with the HTTP-01 challenge; answer challenges on the HTTP listener;
  renew ahead of expiry with jitter and backoff; store account keys and certificates.
- **Done when**: tests against Pebble (the ACME test server) issue and renew certificates.
- **Touches**: `tlsmgr`, `listener`.

### I30 TLS-ALPN-01, OCSP stapling

- **Do**: the TLS-ALPN-01 challenge, and a choice of challenges; OCSP stapling with refresh, for
  certificates from files and obtained ones.
- **Done when**: tests obtain a certificate from Pebble through TLS-ALPN-01 alone, answered by fib's
  TLS; responses are stapled over TLS 1.3 and 1.2, refreshed, and dropped once they expire.
- **Touches**: `tlsmgr`, `runtime`.

I30 was split in three, to keep each part within the size an iteration aims for: I30b and I30c
are the rest of it.

### I30b HTTPS by default

- **Do**: every host name in the config gets a managed certificate and an HTTP→HTTPS redirect
  unless turned off.
- **Done when**: a config with only a host name serves HTTPS against Pebble; a test against Let's
  Encrypt staging is documented and can be run by hand.
- **Touches**: `config`, `runtime`, `listener`.

### I30c ACME Renewal Information (ARI)

- **Do**: renew when the CA's suggested window (RFC 9773) says, checked again as its Retry-After
  says, and by lifetime when the CA has none; decision 007 leaves ARI to fit.
- **Done when**: tests against Pebble and a CA that suggests a window in the past renew in it.
- **Touches**: `tlsmgr`.

### I31 On-demand TLS

- **Do**: issue certificates during the handshake for unknown names, guarded by an "ask" URL and
  rate limits; the handshake waits on the handshake pool, not a worker.
- **Done when**: tests cover allowed, refused and rate-limited names.
- **Touches**: `tlsmgr`.

### I32 Active health checks

- **Do**: HTTP and TCP probes run by the control plane with interval, timeout, thresholds and
  jitter; results update upstream state that the data plane reads.
- **Done when**: tests show a backend leaving and rejoining rotation within the configured window.
- **Touches**: `upstream`.

### I33 Load balancing v2

- **Do**: random, P2C, consistent hashing on IP, header, cookie or URI, sticky sessions by cookie.
- **Done when**: distribution and stickiness tests pass; hashing stays stable when an address is
  added.
- **Touches**: `upstream`.

### I34 Prometheus metrics

- **Do**: the Prometheus text format written by fit (no heavy client library unless a decision note
  says otherwise); counters and histograms per router, service and upstream; connection and pool
  gauges; reload and certificate expiry metrics; served on the Admin listener.
- **Done when**: tests scrape the endpoint; a benchmark shows the cost per request.
- **Touches**: `observe`, `admin`.

### I35 M2 exit tests

- **Do**: the churn test, the auto-HTTPS test and the health check test from the roadmap's M2 exit
  criteria, together in `test/e2e`, run in CI.
- **Done when**: all three pass in CI.
- **Touches**: `test/e2e`.

### I35b Certificate files watched

Left by I09 to I28 and by I28 to I29–I30, and done by none of them; found when M2 was reviewed.

- **Do**: apply the running config again when the certificate or key files it loaded change, as a
  renewal tool outside fit changes them, polling as the file provider does; a pair that does not
  load leaves the running certificates. List the certificates taken on demand in the certificate
  expiry metric too.
- **Done when**: a renewed pair is served without a reload by hand, and a certificate written
  before its key is reported and changes nothing, in an end-to-end test.
- **Touches**: `tlsmgr`, `runtime`, `admin`, `cmd/fit`.

### I35c On-demand names dropped when idle

Left by I31, which keeps every name taken on demand until a restart; found when M2 was reviewed.

- **Do**: drop a name taken on demand that no handshake has asked for in `acme.on_demand`'s
  `idle_timeout`, with a safe default, so that `max_names` is not used up for good; its
  certificate stays in the storage, and the certificate expiry metric stops listing it.
- **Done when**: time-driven tests show an idle name dropped, no longer served, listed or
  counted, and taken on again from the storage, while a name asked for meanwhile is kept.
- **Touches**: `config`, `tlsmgr`.

## M3, gateway features

### I36 Matchers v2 and priorities

- **Do**: method, header, query, remote IP and CIDR, and regexp path matchers; explicit route
  priorities with a documented tie-break.
- **Done when**: table tests cover each matcher and ordering.
- **Touches**: `router`.

### I37 Expression matcher

- **Do**: a small expression language combining matchers with `&&`, `||`, `!` and parentheses
  (`Host("a.com") && PathPrefix("/api")`); compiled at provision time.
- **Done when**: parser tests and fuzzing of the parser; evaluation does not allocate.
- **Touches**: `router`.

### I38 Rewrites, redirects and headers

- **Do**: middlewares for path rewrite (prefix strip, regexp replace), redirects, and request and
  response header set, add and delete.
- **Done when**: table tests per middleware.
- **Touches**: `middleware/rewrite`, `middleware/headers`.

### I39 Rate, concurrency and size limits

- **Do**: token bucket rate limits keyed by client IP, header or route; concurrency (in-flight)
  limits; request body size limits; 429 and 413 responses.
- **Done when**: tests and benchmarks; the limiter's memory is bounded under many keys.
- **Touches**: `middleware/limit`.
- **Read**: fib `middleware/limiter`.

### I40 Basic auth and client mTLS

- **Do**: Basic auth with hashed passwords; client certificate verification on TLS listeners, with
  the certificate subject available to later middleware.
- **Done when**: tests cover good and bad credentials and certificates.
- **Touches**: `middleware/auth`, `listener`.

### I41 ForwardAuth

- **Do**: send a subrequest to an auth service and continue, deny or copy headers based on its
  answer; the request waits for the answer without holding a worker.
- **Done when**: tests cover allow, deny and a slow auth service under load with no worker
  starvation.
- **Touches**: `middleware/auth`.

### I42 JWT and JWKS

- **Do**: verify JWTs (signature, expiry, audience, issuer); JWKS fetched and refreshed by the
  control plane; claims available to later middleware.
- **Done when**: tests cover key rotation and invalid tokens.
- **Touches**: `middleware/auth`.

### I43 Retries and per-route timeouts

- **Do**: retries for idempotent requests and connection failures, with a retry budget; per-route
  overall timeouts.
- **Done when**: tests show retries stop at the budget and never resend a non-idempotent request
  whose body was sent.
- **Touches**: `handler/proxy`, `middleware/retry`.

### I44 Circuit breakers

- **Do**: breakers per upstream on error ratio and latency, with closed, open and half-open states.
- **Done when**: state transition tests.
- **Touches**: `middleware/breaker`.

### I45 Traffic splitting: weights, mirroring, failover

- **Do**: weighted services, mirroring a share of traffic to a second service (responses dropped),
  failover to a backup service.
- **Done when**: tests for each; mirroring does not slow the main request.
- **Touches**: `upstream`.

### I46 Compression

- **Do**: response compression with gzip, zstd and brotli, negotiated by `Accept-Encoding`, skipping
  small and already compressed responses; a decision note on the zstd and brotli libraries.
- **Done when**: tests and a benchmark.
- **Touches**: `middleware/compress`, `docs/decisions`.
- **Read**: fib `middleware/compress`.

### I47 WebSocket proxying

- **Do**: detect upgrades in the proxy, upgrade the upstream connection, then join both sides with
  `Context.Upgrade` and `Tunnel`, with backpressure both ways.
- **Done when**: echo and large-message tests through fit; idle WebSocket memory measured.
- **Touches**: `handler/proxy`.
- **Read**: fib `http/tunnel.go`, `websocket`.

### I48 WebSocket over HTTP/2 and HTTP/3 entries

- **Do**: accept extended CONNECT WebSockets on h2 and h3 entries and proxy them to HTTP/1.1
  upstreams.
- **Done when**: e2e tests over h2 and h3.
- **Touches**: `handler/proxy`.
- **Read**: fib `http/h2_tunnel.go`, `http3/tunnel.go`.

### I49 gRPC proxying

- **Do**: proxy gRPC over h2 and h2c upstreams: trailers, `grpc-timeout` carried as a deadline,
  streaming in both directions, gRPC status on proxy errors.
- **Done when**: unary, server, client and bidirectional streaming tests pass.
- **Touches**: `handler/proxy`.
- **Read**: fib `grpc`.

### I50 TCP proxying

- **Do**: TCP listeners and TCP services in config; proxy bytes both ways with backpressure;
  connect and idle timeouts; load balancing reused from `upstream`.
- **Done when**: throughput and slow-peer tests.
- **Touches**: `l4`, `config`.

### I51 UDP proxying

- **Do**: UDP listeners with sessions keyed by client address, idle timeouts and limits.
- **Done when**: tests with many clients; sessions expire.
- **Touches**: `l4`.

### I52 SNI routing and PROXY protocol

- **Do**: read the ClientHello and route TLS by SNI without terminating it; PROXY protocol v1/v2
  accepted on listeners and sent to upstreams.
- **Done when**: tests cover passthrough by SNI and both PROXY protocol versions.
- **Touches**: `l4`, `listener`.

### I53 One port, many protocols; SOCKS and CONNECT

- **Do**: a listener option that uses fib `mux` to split HTTP, TLS (terminated or passthrough), SOCKS
  and custom protocols on one port; SOCKS and CONNECT forward proxy handlers with access rules.
- **Done when**: one port serves HTTP/1.1, h2, TLS passthrough and SOCKS in an e2e test.
- **Touches**: `listener`, `l4`.
- **Read**: fib `mux`, `multiproto`, `socks`, `http/connect_proxy.go`.

### I54 Upstream TLS

- **Do**: TLS to upstreams with custom CAs, client certificates (mTLS) and SNI overrides.
- **Done when**: tests against backends that require client certificates.
- **Touches**: `upstream`.

### I55 M3 exit tests

- **Do**: the M3 exit criteria in `test/e2e` and the harness; a check (benchmark or test hook) that
  no middleware blocks a worker.
- **Done when**: all pass in CI; results committed.
- **Touches**: `test/e2e`, `bench`.

## M4, platform integration

### I56 Provider framework and effective config

- **Do**: a provider interface; merging configs from several providers with written precedence
  rules; record where each part came from; `GET /config/effective` on the Admin API.
- **Done when**: tests merge file and fake providers and show sources.
- **Touches**: `provider`, `admin`.

### I57 Docker provider

- **Do**: watch the Docker API (events and containers) over its Unix socket and turn labels into
  routes and services; a decision note on using the Docker SDK or plain HTTP.
- **Done when**: an e2e test with Docker Compose follows containers as they start and stop.
- **Touches**: `provider/docker`.

### I58 Kubernetes Gateway API I: HTTPRoute

- **Do**: a Kubernetes client (decision note on client-go versus something lighter); watch
  GatewayClass, Gateway and HTTPRoute, and translate them into config.
- **Done when**: tests with a fake API server and a kind-based e2e test.
- **Touches**: `provider/kubernetes`.

### I59 Kubernetes Gateway API II: other routes and status

- **Do**: GRPCRoute, TLSRoute, TCPRoute and UDPRoute; write status conditions back to resources.
- **Done when**: tests per route type; status reflects accepted and refused routes.
- **Touches**: `provider/kubernetes`.

### I60 Kubernetes Ingress

- **Do**: translate Ingress resources and a small set of annotations.
- **Done when**: tests per Ingress feature supported.
- **Touches**: `provider/kubernetes`.

### I61 Gateway API conformance

- **Do**: run the Gateway API conformance suite for the core profile; fix failures or split them
  into iterations.
- **Done when**: the claimed profile passes in CI.
- **Touches**: `test/e2e`, `provider/kubernetes`.

### I62 Consul and etcd providers

- **Do**: services from the Consul catalog; config from Consul KV and etcd.
- **Done when**: tests against local Consul and etcd.
- **Touches**: `provider/consul`, `provider/etcd`.

### I63 Shared certificate storage

- **Do**: storage implementations on Redis, etcd and Consul, with distributed locks so one instance
  issues and the others reuse.
- **Done when**: two instances share issuance in a test against Pebble.
- **Touches**: `tlsmgr`.

### I64 Dashboard

- **Do**: a read-only web UI embedded in the binary, reading the Admin API: routers, services,
  upstream health, certificates, config versions.
- **Done when**: the pages render against a running fit; no write actions exist.
- **Touches**: `admin`.

### I65 OpenTelemetry

- **Do**: tracing with W3C trace context propagated to upstreams; spans exported over OTLP; metrics
  over OTLP as well; a decision note on the OTel SDK and its dependency weight.
- **Done when**: tests against an OTLP collector; a benchmark shows the cost with sampling off and
  on.
- **Touches**: `observe`.

### I66 Packaging and examples

- **Do**: a container image, a Helm chart, systemd units, and examples for Docker Compose and
  Kubernetes.
- **Done when**: the examples run as documented.
- **Touches**: `deploy`, `examples`.

## M5, caching and extensions

### I67 Cache I: HTTP semantics and memory tier

- **Do**: a cache middleware with RFC 9111 rules (what is storable, freshness, `Vary`,
  revalidation with validators, `Age`); a memory tier with a size bound and LRU-style eviction.
- **Done when**: tests per caching rule; a benchmark of hits.
- **Touches**: `middleware/cache`.

### I68 Cache II: disk tier

- **Do**: a disk tier with an index in memory; disk reads and writes on a pool of their own; entries
  promoted from disk to memory.
- **Done when**: tests survive restarts with the index rebuilt; workers never wait on disk.
- **Touches**: `middleware/cache`.

### I69 Cache III: locks, stale, purge

- **Do**: cache locks so a miss fetches once; stale-while-revalidate and stale-if-error; purge by key
  and tag through the Admin API.
- **Done when**: the stampede test makes one upstream request; hit-ratio scenarios compared with
  nginx `proxy_cache` in the harness.
- **Touches**: `middleware/cache`, `admin`, `bench`.

### I70 `fit build` and compile-time modules

- **Do**: `fit build --with module@version` that generates a main package importing extra modules
  and builds a binary, like xcaddy; a documented module template.
- **Done when**: a sample external module builds into fit and runs.
- **Touches**: `cmd/fit`, `plugin/build`.

### I71 WASM I: runtime and plugin pool

- **Do**: load WASM modules with a pure-Go runtime (decision note, wazero by default); run calls on a
  pool of their own; a minimal ABI for reading and changing headers.
- **Done when**: a header-editing plugin works; its cost per request is measured and bounded.
- **Touches**: `plugin/wasm`, `docs/decisions`.

### I72 WASM II: Proxy-Wasm subset

- **Do**: implement the Proxy-Wasm HTTP filter calls needed by common filters, if I71's numbers
  allow; otherwise record why not.
- **Done when**: an existing Proxy-Wasm filter runs unchanged, or the decision note explains the
  alternative.
- **Touches**: `plugin/wasm`.

### I73 Friendly config format

- **Do**: an adapter from a human-friendly format (decision note: YAML, a Caddyfile-like syntax or
  other) to native JSON; `fit adapt`.
- **Done when**: round-trip tests on the example configs.
- **Touches**: `adapter`, `cmd/fit`.

### I74 nginx config converter

- **Do**: parse nginx configs and convert a common subset (server, location, proxy_pass, root,
  return, rewrite, upstream, ssl_certificate, limit_req) to native JSON; list everything skipped.
- **Done when**: sample configs convert, and each skipped directive is reported.
- **Touches**: `adapter/nginx`.

### I75 FastCGI

- **Do**: a FastCGI client over fib connections, pooled; a `fastcgi` handler with the usual PHP
  parameters and split path info.
- **Done when**: e2e tests against PHP-FPM.
- **Touches**: `handler/fastcgi`.

## 1.0

### I76 Continuous fuzzing

- **Do**: fuzz targets for config loading, the expression parser, the nginx parser, header
  handling in the proxy and the PROXY protocol parser; run them in CI on a schedule.
- **Done when**: the targets run in CI and their corpora are committed.
- **Touches**: fuzz tests across packages.

### I77 Config and Admin API v1 freeze

- **Do**: review the schema and the API for names and shapes worth keeping; mark them v1; write the
  compatibility policy; add deprecation warnings for anything renamed.
- **Done when**: `docs/compatibility.md` exists and the schema carries its version.
- **Touches**: `config`, `admin`, `docs`.

### I78 Reference docs and migration guides

- **Do**: a getting started guide; a config reference generated from the schema; migration guides
  from nginx, Caddy and Traefik, in English and Chinese.
- **Done when**: the docs build and every config field has a description.
- **Touches**: `docs`.

The external security review in the roadmap's 1.0 section is not an iteration: it is arranged
outside the code once I77 is done.
