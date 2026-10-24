# fit benchmark harness

[English](README.md) | [简体中文](README.zh-CN.md)

The harness runs nginx, Caddy, Traefik, Envoy and fit with equivalent configs under the same load
generators, and writes the results as JSON along with the hardware, the versions and the
settings they were measured with. It is how fit checks itself against the
[roadmap's](../docs/roadmap.md#performance-targets) performance targets.

## Running it

One command, from the repository root, on a Linux host with Docker and Go:

```bash
go run ./bench/cmd/fitbench
```

It builds fit and `idleconn` for Linux, builds the load generator image, pulls the server images,
runs every server through every scenario, and writes `bench/results/run-<time>.json`. A full run
takes about 20 minutes with the defaults. `go run ./bench/cmd/fitbench -h` lists the flags; the
common ones are:

| Flag | Default | |
| --- | --- | --- |
| `-servers` | `nginx,caddy,traefik,envoy,fit` | the servers to run |
| `-scenarios` | `fixed,static,proxy` | the throughput scenarios; empty for none |
| `-protocols` | `http/1.1,h2c,h2,h3` | |
| `-idle` | `http1,tls,ws` | the kinds of idle connection to measure memory for; empty for none |
| `-duration`, `-warmup` | `30s`, `5s` | each measured run, and the unmeasured load before it |
| `-connections` | `256` | wrk's connections |
| `-h2-connections`, `-h2-streams` | `64`, `32` | h2load's connections and streams per connection, for h2c, h2 and h3 alike |
| `-idle-connections`, `-idle-settle` | `10000`, `5s` | |
| `-network` | `host` on Linux, `bridge` elsewhere | |
| `-server-cpus`, `-client-cpus`, `-backend-cpus` | split from what Docker has | CPU lists, as in `0-7` |
| `-nginx-image`, `-caddy-image`, `-traefik-image`, `-envoy-image`, `-base-image` | pinned versions | |
| `-out` | `bench/results/run-<time>.json` | |

fit's results come only from the benchmark host, a Linux machine with `-network host`
([rule 16](../docs/rules.md#8-load-testing)). The harness can still start under Docker Desktop on
macOS or Windows, in its VM on a bridge network, but those runs are not used: the VM, the bridge
and the shared CPUs all cost something a Linux host does not pay. The M0 baseline measured that
way is kept for the record only; the Linux results from M1 on are the reference.

## Comparing two builds

Most changes need far less than the full harness ([rules 20-23](../docs/rules.md#10-verification-sized-to-the-change)).
Both scripts below build here for linux/amd64 and run on the benchmark host through its ssh
master.

- `bench/ab.sh [options] A B` compares two git refs of fit (or `work`, the working tree): fit
  alone, only the scenarios and protocols given (`-scenarios`, `-protocols`), 10s runs after a 2s
  warm-up, 3 rounds alternating A and B, with `-extra` appended to fit's config, `bench/configs/fit/fit.konf`
  (an access log, metrics). It starts on the host and returns an id; `bench/ab.sh -report ID` fetches what has
  finished and summarizes requests a second and CPU per request, marking a change only past 1%
  (`AB_NOISE`) and the spread of the rounds; `-wait ID` waits for the end. One scenario over one
  protocol takes about a minute and a half, the default set about eight.
- `bench/microbench.sh [-C repo] -pkg PKG -bench REGEX A B` runs a package's micro-benchmarks
  for two refs, alternating, 10 times each, on the host, and compares them with benchstat here;
  `-C ../fib` does it for fib. About a minute.

## What runs where

Every process runs in a container pinned to CPUs of its own, with `nofile` raised to 1048576:

- **the server under test**: one at a time, from its official image (fit from the binary built for
  the run, on `ubuntu:24.04`);
- **the load generators**: wrk and h2load, Ubuntu's glibc builds, with curl and `idleconn`; and
  for HTTP/3, h2load 1.68 built with HTTP/3 (on ngtcp2 1.16 and nghttp3 1.12 over OpenSSL 3.5's
  QUIC API, [`loadgen/Dockerfile.h3`](loadgen/Dockerfile.h3)) with Debian trixie's curl, in a
  container of its own on the same CPUs; no distribution packages an h2load that speaks HTTP/3,
  so it is built from source the first time, in a few minutes;
- **the proxy backend**: nginx answering every request with the fixed response, but the API, the
  large download's and the upload's paths, on port 9000
  ([`configs/backends/proxy.conf`](configs/backends/proxy.conf));
- **the cache origin**: nginx answering every path under `/cache/` with 4 KiB and under `/edge/`
  with the website's files, fresh for 10 minutes (`Cache-Control: max-age=600`), on port 9003,
  counting what it answers with `stub_status` at 127.0.0.1:9004
  ([`configs/backends/origin.conf`](configs/backends/origin.conf));
- **the WebSocket backend**: `idleconn -serve-ws`, which accepts upgrades and holds them, on port
  9001.

With N CPUs, the backends get an eighth (at least one) and the server and the load generators
half each of the rest. nginx gets one worker per server CPU; the Go servers see the CPU set
through their affinity.

## Scenarios

Every server's config, in [`configs`](configs), serves the same routes on five ports: 8080 for
HTTP/1.1, driven by wrk; 8443 for HTTP/1.1 over TLS 1.3 (`https/1.1`), driven by wrk; 8081 for
h2c (HTTP/2 with prior knowledge), driven by h2load; 8445 for h2 (HTTP/2 over TLS 1.3, by ALPN),
driven by h2load; and UDP 8444 for HTTP/3, over TLS 1.3, driven by the HTTP/3 h2load. fit's
configs are in its own format, `.konf`. h2 and HTTP/3 use the harness's throwaway certificate, and the same
connections and streams as h2c.

QUIC servers that size their own UDP socket buffers (Caddy and Traefik, through quic-go, ask for
about 7 MB) get no more than the host's `net.core.rmem_max` and `wmem_max`. The benchmark host
sets both to 7500000, in `/etc/sysctl.d/90-fitbench-udp-buffers.conf`, since 2026-10-23.

| Scenario | Path | Response |
| --- | --- | --- |
| `fixed` | `/fixed` | 200, `text/plain`, `hello, world` |
| `static` | `/static/small.html` | [`www/static/small.html`](www/static/small.html), 1 KiB |
| `proxy` | `/proxy` | proxied to the backend over HTTP/1.1 with keep-alive; `hello, world` |
| `cache-hot` | `/cache/hot` | through the server's cache to the cache backend: one path, asked again and again; 4 KiB |
| `cache-zipf` | `/cache/1` to `/cache/100000` | the same, 100,000 paths, the n-th most popular asked for as often as 1/n (Zipf's law, by [`loadgen/zipf.lua`](loadgen/zipf.lua)); HTTP/1.1 only |

The cache scenarios are not run unless named (`-scenarios cache-hot,cache-zipf`). Each server keeps
64 MiB of responses: nginx's `proxy_cache` on disk (and so in the page cache), with a lock, and
fit's `cache` in memory; 4 KiB responses to 100,000 paths are about 430 MB, so the Zipf scenario
measures what each keeps of the popular paths as much as its speed. Their results add the requests
that reached the cache origin during the run, from its `stub_status`, and the hit ratio, the share
of the requests the server answered without it. Only Linux host networking reaches the origin's
counters.

The backends are nginx, in configs of their own, [`configs/backends`](configs/backends): no server
under test, fast on their 2 CPUs, and the same for every server, so that a proxy's results are its
own and not its backend's.

The settings the servers share are aligned, as [Aligned settings](#aligned-settings) says, so that
no server is held back by a limit of its own config the others do not have.

Before each run, the harness fetches the path once over the protocol and checks the status, the
protocol and the body against the expected ones, so a server whose config drifted fails rather
than being measured doing something else. A server that cannot serve a scenario is recorded as
skipped: Traefik only proxies, Envoy serves no files, Caddy, Traefik and Envoy have no cache of
their own here, and fit gains
`static` in I12, `proxy` in I15 and the cache scenarios in I69.

Each measured run records requests per second, latency (percentiles from wrk; mean, standard
deviation and maximum from h2load, which reports no percentiles), errors by kind, the CPU the
server used (from its cgroup's `cpu.stat`, in cores) and its memory at the end of the run (cgroup
`memory.current`, and `anon` from `memory.stat`).

Each run gets a server started for it, so that what one run leaves in memory is not counted in the
next. Its anonymous memory is read once it has settled (`anon_start_bytes`) and every 100ms during
the warm-up and the run, of which the most is kept (`anon_peak_bytes`), from the cgroup's files on
the host, which costs the server nothing. From these, each run's efficiency:

| Field | |
| --- | --- |
| `cpu_us_per_request` | the server's CPU time per request, in µs |
| `requests_per_sec_per_mib` | requests a second for each MiB of peak anonymous memory |
| `bytes_per_in_flight` | peak less starting anonymous memory, for each request in flight (`in_flight`: wrk's 256 connections, or 64 connections of 32 streams for h2c, h2 and HTTP/3) |

fit's goroutines are counted from `go_goroutines` on its Admin API, which its config has listen at
127.0.0.1:9005 (only with host networking does the harness reach it): once a second after it is
ready (`startup_goroutines`), every 100ms during each warm-up and measured run, on its own freshly
started server, of which the most is kept (`goroutines_max`), and before and with the idle connections (`goroutines_before`,
`goroutines_after`). The count includes the Admin API's own few goroutines; a spike shorter than
100ms can fall between two samples.

## Workloads

The scenarios of [the load test plan](#the-load-test-plan) model what a proxy is used for; their
payloads and headers are in [`cmd/fitbench/workloads.go`](cmd/fitbench/workloads.go), and wrk's
scripts in [`loadgen`](loadgen).

| Scenario | Path | What it asks for |
| --- | --- | --- |
| `api` | `/api/item` | an API gateway's traffic, proxied: four of five requests a GET answered with 1 KiB of JSON, the fifth a POST of 2 KiB answered with 256 bytes, all with an API client's headers (about 700 bytes: a bearer token, cookies). wrk mixes them in [`api.lua`](loadgen/api.lua); h2load, which sends one kind of request, runs as two groups, four fifths of the connections with GET and the rest with POST |
| `site` | `/site/...` | a website's 10,000 files, written by fitbench the same every time: a tenth pages of about 30 KiB, a fifth stylesheets and scripts of 10 to 100 KiB, the rest images of 5 to 300 KiB, about 600 MB in all, the n-th most popular asked for as often as 1/n, with a browser's headers; over HTTPS only (HTTP/1.1 over TLS, h2, HTTP/3) |
| `edge` | `/edge/...` | the same files through the server's cache, from the cache backend, which serves them fresh for 10 minutes; with the hit ratio |
| `churn-1`, `churn-10` | `/fixed` | a connection closed after every request, or every ten, so that each request or ten has its own TLS handshake (wrk resumes no session); HTTP/1.1 over TLS |
| `download` | `/site/large.bin` | a 1 MiB file |
| `proxy-download` | `/proxy/large` | 1 MiB from the backend, proxied |
| `proxy-upload` | `/proxy/upload` | a POST of 1 MiB, proxied, answered with 256 bytes |

`-rate N` keeps each run to N requests a second, with h2load over every protocol (it speaks
HTTP/1.1 too), and reads the latency percentiles from its per-request log; benchreport then ranks
the servers by their p99. `-hold-idle N` holds N idle TLS connections open to the server during
each run, and records how many the server closed.

## Aligned settings

Each server's bench config sets what the servers have in common to the same value, so that within
the plan's largest load, 7,168 requests in flight, no server is held back by a limit of its own:

| Setting | Value | How |
| --- | --- | --- |
| Backend connections | at most 8,192 | nginx `max_conns` with a shared `zone`, Caddy `max_conns_per_host`, Envoy's circuit breakers, fit `max_conns`; Traefik has no such setting |
| Requests waiting for a backend connection | at most 8,192 | fit `max_pending`, Envoy `max_pending_requests`; nginx cannot queue, Caddy and Traefik do without a bound |
| Idle backend connections kept | 8,192 in all | nginx 1,170 for each of its 7 workers, Caddy, Traefik and fit 8,192; Envoy has no setting |
| Request body size | 16 MiB | nginx `client_max_body_size` (1 MiB by default), Caddy `request_body`, fit's default; Traefik and Envoy, streaming, set none |
| Proxied bodies | streamed, or buffered in memory | nginx buffers responses in memory but never in temporary files, and streams uploads; the others stream |
| Waiting for the backend | 5s to connect, 60s for a response | nginx `proxy_connect_timeout`, `proxy_read_timeout`; Caddy `dial_timeout`, `response_header_timeout`; Traefik `forwardingTimeouts`; Envoy `connect_timeout` and its route's (15s by default); fit `connect_timeout` and the proxy's `timeout` |
| Idle client connections | closed after 120s | nginx `keepalive_timeout` (65s by default), Caddy `timeouts idle`, Traefik `idleTimeout`, Envoy `idle_timeout`; fit's, fib's default, cannot be set |
| HTTP/2 and HTTP/3 streams a connection | 250 | the Go servers' and fib's default; nginx and Envoy set to it |
| HTTP/2 stream receive window | 1 MiB | nginx `http2_body_preread_size` (64 KiB by default), Envoy (256 MiB by default); the others have it |
| HTTP/2 connection receive window | 16 MiB where it can be set | Envoy, fit; Caddy and Traefik, in Go, keep 1 MiB, which can slow their large uploads over h2 |
| Workers | 7, one a CPU | nginx `worker_processes`, Envoy `--cpuset-threads`, the Go servers by the CPUs they are pinned to |
| Cache | 1 GiB, entries of up to 2 MiB | nginx `proxy_cache_path`, fit's `cache` |
| Static files | what is learned of a path trusted for 1s | fit's static handler (`check_interval`), which also keeps files of up to 1 MiB in memory, 64 MiB at most; nginx `open_file_cache` for 10,000 paths, `open_file_cache_valid 1s`, the bytes from the page cache by `sendfile`; Caddy reads each file through the page cache |

None writes an access log, and none compresses, on the fly or from precompressed files: nginx's
official image could serve only gzip from precompressed files, the others gzip, brotli and zstd,
so none does. The workloads still send a browser's and an API client's `Accept-Encoding`, which
every server ignores.

## The load test plan

[`plan.sh`](plan.sh) runs the plan on the benchmark host, its sizes set by what the host can carry:
16 threads split 7 for the server, 7 for the load generators and 2 for the backends; about 33 GB/s of loopback TCP with that split; and a kernel
that pressures TCP buffers past 1 GiB across all sockets, which keeps the bytes in flight of every
run well below. Each run is a 5s warm-up and 15s measured; the 206 runs take about 1 h 35 min.

| Workload | Scenarios | Protocols | Requests in flight | Servers | Runs |
| --- | --- | --- | --- | --- | ---: |
| W1, the API gateway | `api` | HTTP/1.1, h2 | 112, 448, 1,792, 7,168 | all five | 40 |
| W2, the website | `site`, `edge` | HTTP/1.1 over TLS, h2, HTTP/3 | 448, 1,792 | nginx, Caddy, fit (`edge`: nginx, fit) | 30 |
| W3, connection churn | `churn-1`, `churn-10` | HTTP/1.1 over TLS | 112, 448 connections | nginx, Caddy, Envoy, fit | 16 |
| W4, large transfers | `download`, `proxy-download`, `proxy-upload` | HTTP/1.1, h2 | 112 | all that serve them | 26 |
| W5, idle and active | `api` with 40,000 idle TLS connections | HTTP/1.1, h2 | 448 | all five | 10 |
| W6, latency at a fixed rate | `api` at 50k and 150k requests a second | HTTP/1.1, h2 | 448 connections | all five | 20 |
| W7, the raw ceiling | `fixed` | HTTP/1.1, h2c, h2, HTTP/3 | 28, 448, 1,792, 7,168 | nginx, Caddy, Envoy, fit | 64 |

Over HTTP/1.1 each connection has one request in flight; over h2c, h2 and HTTP/3 the same number of
requests is kept by connections of up to 32 streams (112 as 7 × 16, 448 as 14 × 32, 7,168 as
224 × 32), and every count is a multiple of the load generators' 7 threads.

## Idle connections

`idleconn` opens `-idle-connections` connections, each making one request first, and holds them
idle:

| Kind | Connection |
| --- | --- |
| `http1` | HTTP/1.1 keep-alive on 8080, after a request for `/idle`, which every server answers 404 |
| `tls` | the same over TLS 1.3 on 8443, with a throwaway ECDSA P-256 certificate |
| `ws` | a WebSocket on 8080 at `/ws`, which the server proxies to the WebSocket backend |

Each kind runs against a freshly started server. The harness reads the server's anonymous memory
before the connections open and after they have sat for `-idle-settle`, and divides the
difference by the number of connections. Anonymous memory is what the server's processes hold,
across every process in the container (nginx's workers included), without the page cache; it is
what RSS measures less the file-backed pages. A WebSocket's cost includes the server's
connection to the backend, since every server here proxies them.

## Results

[`results`](results) holds the runs that are checked in. `baseline-2026-10-19-macos-docker.json` is
M0's baseline for nginx, Caddy and Traefik, measured on Docker Desktop and kept for the record only
(above); the reference is the Linux runs from M1 on, `m1-*` and `m2-*` with their reports. To
reproduce a run, run the harness with the `params` recorded in it, on the benchmark host:

```bash
go run ./bench/cmd/fitbench -servers nginx,caddy,traefik,envoy,fit -out bench/results/baseline-$(date +%Y%m%d).json
```

The reports' tables are rendered from a run's JSON by `benchreport`: a table for each scenario and
protocol, a row for each server, ranked by requests a second (TPS), then CPU efficiency (requests
per CPU-second), then memory efficiency (requests a second per MiB), each ranked value followed by
its share of the best; and a table of memory per idle connection, lowest first. `-prev` adds each
row's throughput change from an earlier run:

```bash
go run ./bench/cmd/benchreport -in bench/results/run.json -prev bench/results/earlier.json -prev-name M5
```

Throughput counts successful responses only: what the load generator counted as errors (5xx,
timeouts) is left out, and the Errors column gives their number and share. A run with no result
says why: **hung** (the load generator did not finish), **did not start**, or **no responses**.
`-sweep` takes runs made at rising connection counts (`-connections`, `-h2-connections`) and
renders, for each scenario and protocol, a row for each server and a column for each run, ranked by
the most the server answered at any of them:

```bash
go run ./bench/cmd/benchreport -sweep bench/results/sweep-l1.json,bench/results/sweep-l2.json
```

[`sweep.sh`](sweep.sh) runs the whole sweep on the benchmark host, seven levels from 16 HTTP/1.1
connections (8×32 streams) to 32,768 (8,192×32), as the
[2026-10-23 sweep](results/sweep-report-2026-10-23.md) did; the proxy runs at levels 1-5 only,
since past them it measures the load generator and the proxy backend rather than the proxies.

A run's JSON holds `host` (CPU, kernel, memory), `versions` and `images` (with digests),
`params`, one entry in `results` per server, scenario and protocol, and one in `idle` per server
and kind.
