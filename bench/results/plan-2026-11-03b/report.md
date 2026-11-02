# Load test plan, 2026-11-03 (second run): the report

[English](report.md) | [简体中文](report.zh-CN.md)

The plan of [the first 2026-11-03 run](../plan-2026-11-03/report.md) run again with fit at af047ce on fib
c3f2c70, after the work on what that report left: the proxy's HTTP/1 header passthrough, HTTP/3's
per-request allocations, and the W6 stall and high-concurrency memory. 232 runs on the benchmark host,
2026-11-02 21:01 to 22:33 UTC (2026-11-03 05:01 to 06:33 in the host's Asia/Hong_Kong): none failed, none
hung, no run had a single error. Every table is in [tables.md](tables.md), the plan in [plan.txt](plan.txt),
the log in [plan.log](plan.log). The host was as before: the same images, kernel settings and CPU split,
nothing else running but the desktop session and the idle llm node, `strata.service` stopped for the run.
fit was built against fib c3f2c70 before it was pushed (`go.work` outside the repository); fit's go.mod
requires the same fib from 612ddda.

**The harness changed** (fit 46259c0): h2load's per-request log is now on a tmpfs, which removes a ~10 ms
stall every 5 seconds from every server's W6 latencies (below). W6's p99.9 and maxima are not comparable
with earlier plans; its throughput and every other workload are.

## In short

- **HTTP/3 is now fit's fastest lead.** CPU a request over HTTP/3 fell from 3.1-3.9 to 2.2 µs: W0 over
  HTTP/3 +27.5% to +31.3% at 500 to 10,000 in flight (2.41M to 2.50M a second), +13.5% at 100. fit is
  first at every level, **128-165% of nginx**, at a fifth of its memory or less (56 against 366 MiB at
  5,000).
- **The API gateway over HTTP/1.1 is 7-9% faster again:** W1 +7.3% to +9.0% at every level (263,684 a
  second at 500, 22.57 µs a request from 24.49), W5 +8.7%, W6 2-7% less CPU a request. Against nginx W1
  over HTTP/1.1 is now 83-91% (77-87%); over h2 92-101%, first at 10,000.
- **Memory with many requests in flight is halved.** W1 at 5,000 in flight 193 MiB over HTTP/1.1 (385) and
  245 over h2 (415), at 10,000 317 and 373 (650 and 649): below nginx's 221 at 5,000 over HTTP/1.1 and
  its 415 over h2 at 10,000.
- **The W6 stall was the load generator's.** With the fix, at 30,000 a second fit has the lowest tail of
  the five over h2 (p99.9 1.83 ms, max 2.78; nginx 2.14 and 2.59) and over HTTP/1.1 p99.9 3.13 ms (10.67
  in the first run).
- **Nothing of fit's got slower.** 13 of its 58 runs are more than 3% faster than the first 11-03 run; the
  one past -3%, W4's download over HTTP/1.1 (-3.2%), is +2.9% in a 3-round A/B. The other servers'
  medians moved -0.8% to -0.5%.

## What changed

fib 3664a1e to c3f2c70:

| Commit | Change |
| --- | --- |
| 13cd32d | HTTP/1 header blocks through a proxy: `Context.RequestHeaderBlock`, `Client.StartFramed` with `Framing{HeaderBlock, ResponseHeaderBlock}`, `ClientResponse.HeaderBlock`, `Response.HeaderBlock` ([A60](../../../docs/fib-api-requests.md)); a hook that may change the header still gets a map |
| c6aac3c | HTTP/3 field names looked up in a small fixed table (`internal/strtab`) rather than maps |
| 1beb87b | a `quic.Stream` reused once nothing refers to it: no queued handler call, no sent packet's record (`sentRefs`), not in the send queue; 81% of the bytes fit allocated serving HTTP/3 |
| 1bc6d52, 9766b14 | Host kept out of the header map, Content-Length and Expect read from the decoded list; a recycled request stream keeps its header and body state |
| 12e1829, 97f0c53 | a connection's requests and QUIC's open streams in 64 slots rather than maps |
| be65d3f, 8fe5d67 | a response's header gone over once; the request path's deferred calls open-coded |

fit: 4184592 (the proxy passes HTTP/1 header blocks both ways when no middleware wraps it: the hop-by-hop
fields, those Connection names, Content-Length, Host and the client's forwarding fields dropped in any case,
the forwarding fields and Traceparent appended; a trusted client, Expect, Trailer, gRPC and mirrors keep
the map), 46259c0 (h2load's log on a tmpfs), af047ce (each engine's workers capped at 100 a CPU by default,
from fib's 1,000; `workers` still sets it).

## The W6 stall: the load generator's

In the load generator's container h2load wrote its per-request log to overlayfs on ext4. ext4 commits its
journal every 5 seconds, and about once a commit one h2load thread's write waited ~10 ms for a buffer
under I/O while the others queued on the file's position lock: no request sent, no response read. The
slow requests came in bursts exactly 5.000 or 10.01 s apart; a runtime trace of fit showed its loops
parked in netpoll with nothing to read for 8.7 ms at those moments (no GC pause above 186 µs); tcpdump
showed no traffic at all during them; bpftrace caught h2load's threads in `io_schedule` under the ext4
journal and on `fdget_pos`; and nginx in the same harness showed the same stalls. fit 46259c0 mounts a
tmpfs on the load generators' `/tmp`.

| W6 at 30,000 a second | p99.9 first run → this run (ms) | max (ms) |
| --- | --- | --- |
| fit, h2 | 6.17 → **1.83** | 11.06 → **2.78** |
| fit, HTTP/1.1 | 10.67 → 3.13 | 12.54 → 5.14 |
| nginx, h2 | 2.73 → 2.14 | 10.88 → 2.59 |
| nginx, HTTP/1.1 | 3.32 → 1.89 | 11.70 → 3.05 |
| Envoy, h2 | 10.87 → 4.55 | 12.93 → 5.24 |

At 200,000 a second fit's tail stays above nginx's (p99 4.9-5.1 ms against 3.1-4.2, maximum 16-20 ms
against 8), the one latency left behind.

## The memory: workers waiting on a lock

At 5,000 in flight fit had 7,700-8,900 goroutines: about 3,400 workers parked on the lock of fib's HTTP
client (`Client.release` from a connection's data, `Client.enqueue` from a send), each inside a read
round with its 16 KiB read buffer and a send buffer, 95 of the 160 MB live at the last collection, and the
rest idle workers. fib's default lets a pool grow to 1,000 workers a CPU, starting one for each task that
finds the others busy, so contention on the lock became thousands of goroutines holding buffers. fit now
caps each engine at 100 a CPU (af047ce); tasks past it wait in the pool's queue holding nothing. A/B at
5,000 in flight: HTTP/1.1 361-392 → 200-221 MiB, h2 378-403 → 239-258, goroutines ~8,000 → 1,280-2,000,
throughput within noise. The lock itself is still contended.

## Against the first 2026-11-03 run, server by server

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | +0.3% | -0.4% | -1.2% | 13 | 1 |
| nginx | 58 | -0.5% | +0.7% | +0.0% | 4 | 5 |
| Caddy | 52 | -0.7% | +0.8% | -0.2% | 0 | 2 |
| Envoy | 44 | -0.8% | +0.8% | +0.0% | 2 | 6 |
| Traefik | 20 | -0.5% | +0.6% | +1.2% | 0 | 0 |

## fit, where it changed

| Run | Scenario | Protocol | 11-03 | 11-03b | Change | µs a request | Peak MiB |
| --- | --- | --- | --: | --: | --: | --: | --: |
| W0 100 | fixed | HTTP/3 | 1,169,368 | 1,326,687 | +13.5% | 3.92→3.12 | 18→14 |
| W0 500 | fixed | HTTP/3 | 1,889,559 | 2,409,273 | +27.5% | 3.12→2.23 | 23→19 |
| W0 2,000 | fixed | HTTP/3 | 1,928,897 | 2,475,758 | +28.4% | 3.08→2.18 | 42→31 |
| W0 5,000 | fixed | HTTP/3 | 1,927,661 | 2,501,629 | +29.8% | 3.08→2.16 | 94→56 |
| W0 10,000 | fixed | HTTP/3 | 1,869,112 | 2,454,605 | +31.3% | 3.17→2.18 | 152→98 |
| W1 100 | api | HTTP/1.1 | 236,459 | 253,750 | +7.3% | 25.20→23.45 | 28→21 |
| W1 500 | api | HTTP/1.1 | 243,014 | 263,684 | +8.5% | 24.49→22.57 | 60→54 |
| W1 2,000 | api | HTTP/1.1 | 235,658 | 256,898 | +9.0% | 25.24→23.05 | 159→136 |
| W1 5,000 | api | HTTP/1.1 | 202,253 | 219,417 | +8.5% | 29.33→27.03 | **385→193** |
| W1 5,000 | api | h2 | 226,836 | 227,779 | +0.4% | 26.29→26.20 | **415→245** |
| W1 10,000 | api | HTTP/1.1 | 175,170 | 187,943 | +7.3% | 33.71→31.42 | **650→317** |
| W1 10,000 | api | h2 | 200,289 | 204,109 | +1.9% | 29.68→29.15 | **649→373** |
| W2 2,000 | edge | HTTPS/1.1 | 96,498 | 101,647 | +5.3% | 50.69→49.89 | 1,482→1,457 |
| W4 100 | proxy-download | HTTP/1.1 | 15,696 | 16,769 | +6.8% | 378.99→333.33 | 31→31 |
| W4 100 | download | HTTP/1.1 | 18,482 | 17,892 | -3.2% | 128.16→144.75 | 16→13 |
| W5 500 | api + idle | HTTP/1.1 | 241,721 | 262,780 | +8.7% | 24.59→22.64 | 442→445 |
| W6 200,000 | api | HTTP/1.1 | rate | rate | | 24.95→23.24 | 75→48 |
| W6 30,000 | api | HTTP/1.1 | rate | rate | | 29.60→28.04 | 41→34 |

The rest is within ±3%: W1 and W5 over h2 -1.5% to +1.9%, W0 over HTTP/1.1, h2c and h2 -1.7% to +1.6%,
W2 -0.0% to +1.9%, W3 -1.2% to +0.2%. Two runs were checked with `bench/ab.sh` against the build of the
first 11-03 run, 3 rounds each unless said: W4's download over HTTP/1.1, +2.9% (141.28 → 146.64 µs; the
first run's 128 µs was its own outlier, 146-147 in the plans before it), and proxied downloads over h2,
-1.6% in 3 rounds and -0.5% in 5, within noise; W4's peak memory the same in both builds.

## Where fit stands

| Workload | fit's place | fit against the best other |
| --- | --- | --- |
| W0, HTTP/1.1 | 1st or 2nd of 4 | 99-100% of nginx |
| W0, h2c and h2 | 1st | 2.2-6.8 times nginx |
| W0, HTTP/3 | **1st at every level** | **128-165% of nginx** (98-104% in the first run) |
| W1, HTTP/1.1 | 2nd of 5 | 83-91% of nginx (22.6 against 19.6 µs a request at 500) |
| W1, h2 | 1st at 10,000, 2nd elsewhere | 92-101% of nginx |
| W2, HTTPS/1.1 and h2 | 1st | 120-192% of nginx |
| W2, HTTP/3 | 2nd | 53-87% of nginx |
| W3 | 1st or 2nd | 100-102% of the best |
| W4 | 1st, but uploads: 2nd over h2, 3rd over HTTP/1.1 | 109-223% of the best other; uploads 94% of nginx over h2 (fit unchanged, nginx +18%), 98% of Caddy over HTTP/1.1 |
| W5 | 1st over h2, 2nd over HTTP/1.1 | 110% and 87% of nginx, at a third of its memory |
| W6 | the only server keeping 200,000 a second over both | the lowest tail over h2 at 30,000 |

## What is left

- **W1 over HTTP/1.1, 83-91% of nginx:** 3 µs a request at 500 in flight. The lock of fib's HTTP client is
  still contended at many requests in flight (now bounded by the worker cap rather than paid in memory);
  sharding a host's idle and waiting lists, or handing a blocked operation to the holder, is the next step.
- **W6 at 200,000 a second:** fit's p99 and maximum are above nginx's.
- **The website over HTTP/3, 53-87% of nginx,** is AES-GCM (no VAES path in Go 1.27's amd64 GCM) and UDP
  send with GSO, per byte.

## Notes and limits

- One machine, loopback, one run of each; the HTTP/3, W1 and memory changes hold at every level.
- W6's latencies before this plan include the load generator's stall, for every server.
