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
| `-protocols` | `http/1.1,h2c,h3` | |
| `-idle` | `http1,tls,ws` | the kinds of idle connection to measure memory for; empty for none |
| `-duration`, `-warmup` | `30s`, `5s` | each measured run, and the unmeasured load before it |
| `-connections` | `256` | wrk's connections |
| `-h2-connections`, `-h2-streams` | `64`, `32` | h2load's connections and streams per connection, for h2c and h3 alike |
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
  warm-up, 3 rounds alternating A and B, with `-extra` merged into fit's config (an access log,
  metrics). It starts on the host and returns an id; `bench/ab.sh -report ID` fetches what has
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
- **the proxy backend**: fit answering every request with the fixed response, on port 9000;
- **the cache backend**: fit answering every path under `/cache/` with 4 KiB fresh for 10
  minutes (`Cache-Control: max-age=600`), on port 9003, counting what it answers on its Admin API
  at 127.0.0.1:9004;
- **the WebSocket backend**: `idleconn -serve-ws`, which accepts upgrades and holds them, on port
  9001.

With N CPUs, the backends get an eighth (at least one) and the server and the load generators
half each of the rest. nginx gets one worker per server CPU; the Go servers see the CPU set
through their affinity.

## Scenarios

Every server's config, in [`configs`](configs), serves the same routes on three ports: 8080 for
HTTP/1.1, driven by wrk; 8081 for h2c (HTTP/2 with prior knowledge), driven by h2load; and UDP
8444 for HTTP/3, over TLS 1.3 with the harness's throwaway certificate, driven by the HTTP/3
h2load with the same connections and streams as h2c.

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
that reached the cache backend during the run, from its counters, and the hit ratio, the share of
the requests the server answered without it. Only Linux host networking reaches the backend's
counters.

Every proxy keeps up to 4096 idle connections to the backend, more than the load generators have
requests in flight. With their defaults (32 for Caddy, 200 for Traefik) they close and reopen
backend connections under load until the ports run out, which measures the default rather than
the proxy.

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

fit's goroutines are counted from `go_goroutines` on its Admin API, which its config has listen at
127.0.0.1:9005 (only with host networking does the harness reach it): once a second after it is
ready (`startup_goroutines`), every 100ms during each warm-up and measured run, of which the most is
kept (`goroutines_max`), and before and with the idle connections (`goroutines_before`,
`goroutines_after`). The count includes the Admin API's own few goroutines; a spike shorter than
100ms can fall between two samples.

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

A run's JSON holds `host` (CPU, kernel, memory), `versions` and `images` (with digests),
`params`, one entry in `results` per server, scenario and protocol, and one in `idle` per server
and kind.
