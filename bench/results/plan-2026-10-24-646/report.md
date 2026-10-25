# Load test plan, 2026-10-24 (second run): the report

[English](report.md) | [简体中文](report.zh-CN.md)

nginx, Caddy, Traefik, Envoy and fit under the seven workloads of [the load test
plan](../../README.md#the-load-test-plan), at 100 to 10,000 requests in flight, each server on 4 CPUs.
232 runs on the benchmark host, 2026-10-24 07:47 to 09:19 UTC: none failed, none hung, and no run
had a single error. Every table is in [tables.md](tables.md),
rendered by benchreport from the runs' JSON in this folder, one file for each workload and level.

## In short

- **Plain responses (W0):** nginx leads HTTP/1.1 (509k a second) and HTTP/3 (1.32M), fit is close
  behind on HTTP/1.1 (473k, 93%) and second on HTTP/3 (987k, 74%), and fit leads h2c and h2 by far
  (2.31M and 2.22M, against nginx's 327k and 478k). fit uses the least memory at every level: 33 MiB
  at 10,000 HTTP/1.1 connections, where Envoy and Caddy use 269 and 308 MiB.
- **API gateway (W1):** nginx leads (201k a second over HTTP/1.1, 165k over h2), fit is second
  (136k, 116k: 68-70%), then Envoy, Traefik and Caddy at 23-48%. At 10,000 in flight fit keeps 82%
  of nginx over HTTP/1.1. Traefik and Caddy grow to 2.6 and 1 GiB there.
- **Website (W2):** over HTTPS/1.1 and h2, fit leads both serving files (65-72k a second, against
  nginx's 48-53k and Caddy's 19-28k) and as a cache (81-88k, against nginx's 46-50k). Over HTTP/3
  nginx leads (65-87k), and fit reaches about half of it (35-37k).
- **Connection churn (W3):** with a TLS handshake for every request, Envoy and fit lead (19k a
  second) ahead of Caddy (16k) and nginx (13k); with one every ten requests fit leads (125-134k)
  ahead of nginx (92-96k).
- **1 MiB transfers (W4):** fit leads downloads over h2 and proxied downloads over h2, but trails
  Envoy, Traefik and Caddy in proxied downloads and uploads over HTTP/1.1 (50-66% of the best) and
  is last in proxied uploads over h2 (53%, with 311 MiB). These are fit's weakest results.
- **40,000 idle TLS connections (W5):** every server kept all 40,000, and its throughput is what it
  was without them. Peak memory: fit 477-495 MiB, Envoy 857 MiB, nginx 1.25 GiB, Caddy and Traefik
  2.2-2.4 GiB.
- **Latency at a fixed rate (W6):** at 20k a second every server keeps up, and nginx has the lowest
  p99 (2.5 ms over HTTP/1.1, 1.1 ms over h2), then fit (6.0, 2.1 ms). At 100k only nginx and fit keep
  up; Envoy, Traefik and Caddy fall to their ceilings (39-75k).
- **The backends never set the pace.** The proxy backend used at most 2.21 of its 6 CPUs, the cache
  origin at most 0.02, while the servers under test used all 4 of their CPUs in nearly every run
  (W6 and two of nginx's W4 runs aside).

## The test environment

### The machine

| | |
| --- | --- |
| CPU | AMD Ryzen 7 7800X3D: 8 cores, 2 threads each (16 logical CPUs), up to 5.05 GHz, 96 MiB L3 shared by all cores, 1 MiB L2 per core |
| Memory | 64 GB (62 GiB usable) |
| System | Ubuntu 24.04.5 LTS, Linux 7.0.0-38-generic, on bare metal (no virtual machine) |
| CPU frequency | `amd-pstate` with the `powersave` governor, boost on |
| Containers | Docker 29.1.3, every container on the host's network (`-network host`): all traffic is loopback TCP and UDP |
| Kernel settings | `somaxconn` and `tcp_max_syn_backlog` 2,048; `rmem_max`, `wmem_max` 7,500,000 (re-applied before every run: something on the host sets them back to 16 KiB); local ports 1024-65535; `nofile` 1,048,576 in every container; `tcp_mem` 131,072 / 262,144 / 524,288 pages (TCP memory pressure from 1 GiB) |

Everything runs on this one machine, so there is no network between the processes: what the
results measure is each server's own cost, without network latency or a NIC's limits.

### Who gets which CPUs

The 16 logical CPUs are split by physical core (CPUs n and n+8 are the two threads of core n), so
that no role shares a core, and so a core's caches and execution units, with another:

| Role | Cores | CPUs | |
| --- | --- | --- | --- |
| the server under test | 0-1 | `0-1,8-9` | 4 CPUs; nginx and Envoy run 4 workers, the Go servers and fit see 4 CPUs |
| the backends | 2-4 | `2-4,10-12` | 6 CPUs; the proxy backend and the cache origin, each nginx with 6 workers |
| the load generators | 5-7 | `5-7,13-15` | 6 CPUs; wrk and h2load with 6 threads |

The backends get as many CPUs as the load generators, so that they can answer more than any
server under test can forward: a proxy's result is then its own and not its backend's. The
results record the backends' CPU in every run, and they stayed well below 6 (above).

### The software

| | Version | Image |
| --- | --- | --- |
| nginx | 1.31.4 | `nginx@sha256:b34848ef…` |
| Caddy | 2.10.2 | `caddy@sha256:c3d7ee5d…` |
| Traefik | 3.5.0 | `traefik@sha256:4e7175cf…` |
| Envoy | 1.40.0 (BoringSSL) | `envoyproxy/envoy@sha256:459f2a64…` |
| fit | 326b2b8 (bench changes on top, committed as d50f321 and e6e8f09), fib at 0e63ae8, Go 1.27.3 | on `ubuntu@sha256:68ee2200…` |
| wrk | 4.1.0 (Ubuntu's build) | the load generator image |
| h2load | nghttp2 1.59.0 (Ubuntu's build) for h2c and h2; 1.68.0 built with HTTP/3 (ngtcp2 1.16, nghttp3 1.12, OpenSSL 3.5) for HTTP/3 | |
| backends | nginx, same image as above | |

The full digests are in each JSON's `images`. Every TLS run agreed on TLS 1.3,
`TLS_AES_128_GCM_SHA256` and X25519 with an ECDSA P-256 certificate, as each result's `tls`
records.

## How the plan is designed

### The servers and how they are made comparable

The five servers run from their official images, one at a time, each freshly started for every
run (so that one run's memory is not counted in the next), with configs in
[`bench/configs`](../../configs) that serve the same routes on the same ports: 8080 HTTP/1.1, 8081 h2c,
8443 HTTP/1.1 over TLS, 8445 h2 over TLS, UDP 8444 HTTP/3.

Their shared settings are aligned so that no server is held back by a limit of its own config that
the others do not have, within the plan's largest load, 10,000 in flight with 4 workers. The full
list is the README's [Aligned settings](../../README.md#aligned-settings); in short: at most 16,384
backend connections and 16,384 requests waiting for one, 16,384 idle backend connections kept
(nginx 4,096 for each of its 4 workers), no passive health checks, a listen backlog of 2,048, the
same forwarding headers, no limit on the requests a client connection may make (nginx's raised to
1,000,000,000), no compression and no access log, 250 streams an h2 or HTTP/3 connection, a 1 MiB
stream window, the same timeouts, and the same TLS.

Before every run the harness fetches the path once over the protocol and checks the status, the
protocol and the body, so that a server whose config drifted fails instead of being measured doing
something else. A server that cannot serve a scenario is recorded as not run: Traefik only proxies,
Envoy serves no files, and only nginx and fit have a cache.

### The workloads

| Workload | What it models | Scenarios | Protocols | Requests in flight | Servers |
| --- | --- | --- | --- | --- | --- |
| W0, the raw ceiling | the most a server can answer: no backend, no files | `fixed`: 200 with `hello, world` | HTTP/1.1, h2c, h2, HTTP/3 | 100, 500, 2,000, 5,000, 10,000 | nginx, Caddy, Envoy, fit |
| W1, the API gateway | an API gateway's traffic, proxied | `api`: four of five requests a GET answered with 1 KiB of JSON, the fifth a POST of 2 KiB answered with 256 bytes, all with about 700 bytes of an API client's headers | HTTP/1.1, h2 | 100, 500, 2,000, 5,000, 10,000 | all five |
| W2, the website | a website served from disk, and the same behind a cache | `site`: 10,000 files of 5 to 300 KiB (pages, scripts, images; about 600 MB), the n-th most popular asked for as often as 1/n, with a browser's headers; `edge`: the same files through the server's cache from the cache origin | HTTPS/1.1, h2, HTTP/3 | 500, 2,000 | nginx, Caddy, fit (`edge`: nginx, fit) |
| W3, connection churn | clients that do not keep connections | `churn-1`: a new TLS connection for every request; `churn-10`: for every ten | HTTPS/1.1 | 100, 500 connections | nginx, Caddy, Envoy, fit |
| W4, large transfers | downloads and uploads | `download`: a 1 MiB file; `proxy-download`: 1 MiB from the backend; `proxy-upload`: a POST of 1 MiB, proxied, answered with 256 bytes | HTTP/1.1, h2 | 100 | all that serve them |
| W5, idle and active | many idle clients beside the active ones | `api` with 40,000 idle TLS connections held open to the server throughout | HTTP/1.1, h2 | 500 | all five |
| W6, latency at a fixed rate | latency when the load is below or near a server's ceiling | `api` at 20,000 and 100,000 requests a second | HTTP/1.1, h2 | 500 connections | all five |

**The levels of load.** Over HTTP/1.1 each connection has one request in flight, so a level is
wrk's connections. wrk gives each of its 6 threads `connections / threads`, so it opens a multiple
of 6 just under the level: 96, 498, 1,998, 4,998 and 9,996, which each result records as
`in_flight`. Over h2c, h2 and HTTP/3 a level is h2load's connections times streams: 100 as 10 × 10,
then 25 streams a connection (500 as 20 × 25, 2,000 as 80 × 25, 5,000 as 200 × 25, 10,000 as
400 × 25), never fewer connections than the load generators' 6 threads. 100 is a light load, 500
and 2,000 typical busy servers, 5,000 and 10,000 overload, where queues and memory show.

**W6's rates** come from what the servers answered at most on their 4 CPUs at 500 in flight in the
trial before the plan, about 40k (Caddy over h2) to 200k a second (nginx over HTTP/1.1). At 20k
every server runs below its ceiling, so its latency is its own and not a queue's. At 100k only
nginx and fit can keep up, and the others' latencies show how a saturated server falls behind.
wrk cannot hold a rate, so W6 uses h2load over HTTP/1.1 too (`--h1`), and reads the percentiles
from its per-request log.

**W5** holds the 40,000 idle connections (TLS 1.3, each after one request) during each run, and
records how many the server closed.

**The load generators' requests.** wrk mixes the API's GETs and POSTs in a Lua script; h2load,
which sends one kind of request, runs as two groups, four fifths of the connections with GET and
the rest with POST. The website's URLs follow Zipf's law over the 10,000 files.

### How each run goes

1. The server under test is started fresh, and its memory read once it has settled.
2. The path is fetched and checked (above).
3. A 5s warm-up of the same load, not measured.
4. 15s measured. The server's CPU (from its cgroup's `cpu.stat`) and the backends' CPU are read
   before and after; the server's anonymous memory is sampled every 100ms, of which the peak is kept.
5. The load generator's report is parsed.

A watchdog ends any load generator still running after 300s (none was); the kind node the host
also runs (`fitconf-control-plane`) is stopped during the plan and started after it, so that
nothing else runs on the machine.

## What the tables show

Every table is for one scenario and protocol, a row for each server, ranked by the column marked
[↓1], then [↓2], then [↓3]. A value followed by a percentage is that value and its share of the
best in the table.

| Column | Meaning |
| --- | --- |
| TPS | successful responses a second during the measured 15s: what the load generator counted, less what it counted as errors (5xx, timeouts, resets) |
| Peak TPS | in the scaling tables (W0, W1), the most TPS the server reached at any level; the columns after it are its TPS at each level |
| CPU | the CPU the server used, in cores (4.00 is all of its 4 CPUs) |
| CPU EER | CPU efficiency: TPS for each core it used (TPS ÷ CPU), the responses one CPU-second buys |
| Peak memory, Memory at peak | the server's peak anonymous memory during the run: what its processes hold (heap, stacks, buffers), across every process in its container, without the page cache. In the scaling tables, at the level of its peak TPS, and after it a table of the peak at each level |
| MEM EER | memory efficiency: TPS for each MiB of peak memory |
| KiB/in-flight | the memory the load added (peak less the memory at start), for each request in flight: what one more concurrent request costs |
| p99 | over HTTP/1.1 (wrk), the latency 99% of responses beat |
| Mean / max | over h2c, h2 and HTTP/3 (h2load, which reports no percentiles), the mean and the longest latency |
| p50 / p99 / p99.9 | in W6, from h2load's per-request log, for every protocol; W6's tables are ranked by p99 |
| Backend CPU | the proxy backend's CPU in cores, of its 6; well below 6 means the backend did not limit the result |
| Hits | in `edge`, the share of responses the server's cache answered without asking the origin |
| Idle held / closed | in W5, the idle connections held open during the run, and how many the server closed |
| Errors | what the load generator counted as errors, and their share |

The scaling tables' columns are the levels: over HTTP/1.1 the level asked for (the results record
the real count, 96 to 9,996), over the others connections × streams.

## The results

### W0, the raw ceiling

| Protocol | Best | Then | Memory at 10,000 |
| --- | --- | --- | --- |
| HTTP/1.1 | nginx 509k | fit 473k (93%), Envoy 174k, Caddy 172k | nginx 120, fit 33, Envoy 269, Caddy 308 MiB |
| h2c | fit 2.31M | nginx 327k (14%), Envoy 304k, Caddy 103k | fit 53, nginx 120, Envoy 116, Caddy 158 MiB |
| h2 | fit 2.22M | nginx 478k (21%), Envoy 296k, Caddy 109k | fit 58, nginx 136, Envoy 112, Caddy 222 MiB |
| HTTP/3 | nginx 1.32M | fit 987k (74%), Caddy 167k, Envoy 117k | fit 117, Caddy 314, nginx 876 MiB, Envoy 4.9 GiB |

Most servers hold their throughput from 100 to 10,000 in flight; Envoy over HTTP/1.1 drops most
(174k to 118k), and nginx over HTTP/3 at 10,000 (1.32M to 1.12M). fit's h2c and h2 rise with the streams in
flight, from 1.2M at 10 × 10 to 2.3M; fit's memory grows least with load (13 to 33 MiB over
HTTP/1.1). nginx's memory starts at about 115 MiB at every level: it preallocates its connection
tables (`worker_connections 65536` for each worker). Envoy's HTTP/3 memory grows to 4.9 GiB at
10,000 in flight.

### W1, the API gateway

| Protocol | 100 | 500 | 2,000 | 5,000 | 10,000 |
| --- | --- | --- | --- | --- | --- |
| HTTP/1.1 | nginx 201k, fit 130k, Traefik 66k, Envoy 61k, Caddy 50k | nginx 199k, fit 136k | nginx 196k, fit 132k | nginx 171k, fit 114k | nginx 135k, fit 111k, Traefik 50k, Envoy 47k, Caddy 37k |
| h2 | nginx 145k, fit 107k, Envoy 73k, Traefik 45k, Caddy 38k | nginx 163k, fit 115k | nginx 165k, fit 116k | nginx 151k, fit 107k | nginx 135k, fit 102k, Envoy 59k, Traefik 41k, Caddy 33k |

nginx leads at every level, fit is second at 64-82% of it, ahead of Envoy, Traefik and Caddy by
1.5 to 3 times. Past 2,000 in flight every server slows, nginx most (201k to 135k over
HTTP/1.1), so at 10,000 fit's share rises to 82%. Peak memory at 10,000 in flight over HTTP/1.1:
nginx 213 MiB, fit 481, Envoy 635, Caddy 975, Traefik 2,560 MiB; at its peak TPS fit has the best
MEM EER over HTTP/1.1. The proxy backend used at most 1.98 of its 6 CPUs (nginx over HTTP/1.1).

### W2, the website

| | HTTPS/1.1 | h2 | HTTP/3 |
| --- | --- | --- | --- |
| `site`, 500 | fit 67.7k, nginx 52.7k, Caddy 28.0k | fit 70.9k, nginx 48.3k, Caddy 19.1k | nginx 87.2k, fit 35.4k, Caddy 18.9k |
| `site`, 2,000 | fit 65.0k, nginx 50.5k, Caddy 26.8k | fit 71.6k, nginx 49.3k, Caddy 18.9k | nginx 70.0k, fit 36.0k, Caddy 17.5k |
| `edge`, 500 | fit 85.1k, nginx 49.5k | fit 87.6k, nginx 45.8k | nginx 76.6k, fit 37.2k |
| `edge`, 2,000 | fit 81.5k, nginx 47.2k | fit 82.2k, nginx 46.6k | nginx 65.1k, fit 36.6k |

Over HTTPS/1.1 and h2 fit leads nginx by 28-91%; over HTTP/3 nginx leads by about two to one. The
caches answered 99.4-100% of requests themselves. fit keeps files of up to 1 MiB, and the cache's
entries, in memory of its own (at 500 in flight, 282-603 MiB serving files, 1.0-1.35 GiB as a
cache), where nginx serves from the page cache, which the memory columns do not count (121-209
MiB).

### W3, connection churn

| | 100 connections | 500 connections |
| --- | --- | --- |
| `churn-1` | Envoy 18.9k, fit 18.6k, Caddy 15.6k, nginx 13.2k | Envoy 19.1k, fit 19.0k, Caddy 15.8k, nginx 13.3k |
| `churn-10` | fit 124.6k, nginx 91.9k, Envoy 82.7k, Caddy 82.4k | fit 134.2k, nginx 95.9k, Envoy 91.2k, Caddy 84.8k |

With a full TLS 1.3 handshake for every request, each request costs 200-300 µs of CPU in every
server, so all are within 30% of each other; fit and Envoy are the fastest. At 500 connections the p99 shows how
each queues the handshakes: Envoy 8.5 ms, fit 17.8, Caddy 35.6, nginx 57.0 ms.

### W4, large transfers

| | HTTP/1.1 | h2 |
| --- | --- | --- |
| `download` | nginx 16.3k, fit 14.2k, Caddy 9.1k | fit 8.1k, nginx 3.9k, Caddy 2.7k |
| `proxy-download` | Envoy 8.9k, Traefik 7.5k, Caddy 6.8k, fit 4.5k, nginx 3.4k | fit 4.7k, Envoy 4.3k, Traefik 2.3k, Caddy 2.3k, nginx 2.1k |
| `proxy-upload` | Traefik 7.2k, Caddy 6.8k, Envoy 6.3k, fit 4.7k, nginx 3.5k | nginx 5.0k, Envoy 4.6k, Traefik 3.7k, Caddy 3.6k, fit 2.7k |

A response a second here is 1 MiB a second: nginx's 16.3k downloads over HTTP/1.1 are about 16 GiB
a second, using only 0.90 of its CPUs through `sendfile`, which means the load generators, not
nginx, set that number. fit is behind the Go servers and Envoy in proxied transfers over HTTP/1.1
(50-66% of the best), and last in proxied uploads over h2 (53%, with 311 MiB of peak memory and a
268 ms maximum): the clearest work left for fit in this plan. nginx used only 2.99 of its 4 CPUs over
h2 with its 10 connections: it keeps each connection on one worker, and 10 connections spread
unevenly across 4 workers.

### W5, 40,000 idle TLS connections

| Server | HTTP/1.1 | h2 | Peak memory (HTTP/1.1 / h2) |
| --- | --- | --- | --- |
| nginx | 197k | 160k | 1,249 / 1,255 MiB |
| fit | 135k | 114k | 495 / 477 MiB |
| Envoy | 66k | 77k | 856 / 857 MiB |
| Traefik | 60k | 43k | 2,398 / 2,321 MiB |
| Caddy | 49k | 39k | 2,194 / 2,158 MiB |

Every server held all 40,000 connections and closed none, and its throughput is within 7% of
W1's at 500 in flight: idle connections cost memory, not throughput. fit holds them in
the least memory, about a fifth of Traefik's and Caddy's.

### W6, latency at a fixed rate

| | 20k a second, p99 | 100k a second, TPS and p99 |
| --- | --- | --- |
| HTTP/1.1 | nginx 2.52 ms, fit 5.95, Envoy 7.56, Traefik 10.28, Caddy 13.58 | nginx 100k 3.39 ms, fit 100k 5.82, Envoy 64k 9.07, Traefik 65k 17.05, Caddy 49k 22.46 |
| h2 | nginx 1.10 ms, fit 2.11, Envoy 3.51, Caddy 5.55, Traefik 5.77 | nginx 100k 5.08 ms, fit 100k 7.81, Envoy 75k 9.59, Traefik 46k 23.18, Caddy 39k 29.80 |

At 20k a second every server held the rate using 0.5 (nginx) to 2.1 (Caddy) of its CPUs; nginx
has the lowest latency, fit the second lowest. At 100k nginx and fit held the rate, on 2.1-3.5
CPUs; Envoy, Traefik and Caddy ran at their ceilings on all 4 CPUs, so their latencies are those
of a queue.

## Notes and limits

- **One machine, loopback.** No network latency or NIC limits; the results compare the servers'
  own costs on 4 CPUs, not what a deployment sees end to end.
- **The load generators can be the limit** where a server answers very fast: nginx's HTTP/1.1
  download (0.90 CPU) is one such run, and fit's 2.2-2.3M a second over h2c and h2 and nginx's
  1.3M over HTTP/3 may be close to what 6 threads of h2load can send. The load generators' own CPU
  is not recorded.
- **Latency over h2c, h2 and HTTP/3** outside W6 is a mean and a maximum, since h2load reports no
  percentiles; it is not comparable with wrk's p99 over HTTP/1.1.
- **The plan was restarted once.** Its first start (07:38 UTC) ran nginx with
  `keepalive_requests 1000000`: with only 10 connections at 100 in flight, nginx closed each after a
  million requests, and h2load, which does not reconnect, stopped early (exactly 10,000,000 requests
  over HTTP/3, 22 errors over h2). The limit was raised (e6e8f09), those results discarded, and
  the plan started again at 07:47.
- **fit's version** reads `326b2b8+dirty`: it was built from a tree whose only changes were the
  harness's, committed right after as d50f321 and e6e8f09; fit's own code is 326b2b8's.
