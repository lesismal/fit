# fit benchmark harness

[English](README.md) | [简体中文](README.zh-CN.md)

The harness runs nginx, Caddy, Traefik and fit with equivalent configs under the same load
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
| `-servers` | `nginx,caddy,traefik,fit` | the servers to run |
| `-scenarios` | `fixed,static,proxy` | the throughput scenarios; empty for none |
| `-protocols` | `http/1.1,h2c` | |
| `-idle` | `http1,tls,ws` | the kinds of idle connection to measure memory for; empty for none |
| `-duration`, `-warmup` | `30s`, `5s` | each measured run, and the unmeasured load before it |
| `-connections` | `256` | wrk's connections |
| `-h2-connections`, `-h2-streams` | `64`, `32` | h2load's connections and streams per connection |
| `-idle-connections`, `-idle-settle` | `10000`, `5s` | |
| `-network` | `host` on Linux, `bridge` elsewhere | |
| `-server-cpus`, `-client-cpus`, `-backend-cpus` | split from what Docker has | CPU lists, as in `0-7` |
| `-nginx-image`, `-caddy-image`, `-traefik-image`, `-base-image` | pinned versions | |
| `-out` | `bench/results/run-<time>.json` | |

On macOS or Windows the harness runs in Docker Desktop's VM on a bridge network. The numbers are
useful for comparing the servers with each other on that machine, not for publishing: the VM, the
bridge and the shared CPUs all cost something that a Linux host with `-network host` does not
pay.

## What runs where

Every process runs in a container pinned to CPUs of its own, with `nofile` raised to 1048576:

- **the server under test**: one at a time, from its official image (fit from the binary built for
  the run, on `ubuntu:24.04`);
- **the load generators**: wrk and h2load, Ubuntu's glibc builds, with curl and `idleconn`;
- **the proxy backend**: fit answering every request with the fixed response, on port 9000;
- **the WebSocket backend**: `idleconn -serve-ws`, which accepts upgrades and holds them, on port
  9001.

With N CPUs, the backends get an eighth (at least one) and the server and the load generators
half each of the rest. nginx gets one worker per server CPU; the Go servers see the CPU set
through their affinity.

## Scenarios

Every server's config, in [`configs`](configs), serves the same routes on two ports: 8080 for
HTTP/1.1, driven by wrk, and 8081 for h2c (HTTP/2 with prior knowledge), driven by h2load.

| Scenario | Path | Response |
| --- | --- | --- |
| `fixed` | `/fixed` | 200, `text/plain`, `hello, world` |
| `static` | `/static/small.html` | [`www/static/small.html`](www/static/small.html), 1 KiB |
| `proxy` | `/proxy` | proxied to the backend over HTTP/1.1 with keep-alive; `hello, world` |

Every proxy keeps up to 4096 idle connections to the backend, more than the load generators have
requests in flight. With their defaults (32 for Caddy, 200 for Traefik) they close and reopen
backend connections under load until the ports run out, which measures the default rather than
the proxy.

Before each run, the harness fetches the path once over the protocol and checks the status, the
protocol and the body against the expected ones, so a server whose config drifted fails rather
than being measured doing something else. A server that cannot serve a scenario is recorded as
skipped: Traefik only proxies, and fit gains `static` in I12 and `proxy` in I15.

Each measured run records requests per second, latency (percentiles from wrk; mean, standard
deviation and maximum from h2load, which reports no percentiles), errors by kind, the CPU the
server used (from its cgroup's `cpu.stat`, in cores) and its memory at the end of the run (cgroup
`memory.current`, and `anon` from `memory.stat`).

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

[`results`](results) holds the runs that are checked in: `baseline-*.json` are the baselines M0
set for nginx, Caddy and Traefik, which later milestones compare fit against. To reproduce one,
run the harness with the `params` recorded in it, on comparable hardware:

```bash
go run ./bench/cmd/fitbench -servers nginx,caddy,traefik,fit -out bench/results/baseline-$(date +%Y%m%d).json
```

A run's JSON holds `host` (CPU, kernel, memory), `versions` and `images` (with digests),
`params`, one entry in `results` per server, scenario and protocol, and one in `idle` per server
and kind.
