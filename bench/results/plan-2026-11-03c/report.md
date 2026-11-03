# Load test plan, 2026-11-03 (third run): the report

[English](report.md) | [简体中文](report.zh-CN.md)

The plan of [the second 2026-11-03 run](../plan-2026-11-03b/report.md) run again with fit at c3cfc1d on fib
a9493a8, after the work on the two items that report left open: the lock of fib's HTTP client, and fit's
tail at 200,000 requests a second. 232 runs on the benchmark host, 2026-11-03 06:04 to 07:37 UTC (14:04 to
15:37 in the host's Asia/Hong_Kong): none failed, none hung, no run had a single error. Tables in
[tables.md](tables.md), the plan in [plan.txt](plan.txt), the log in [plan.log](plan.log); the host as
before, `strata.service` stopped for the run. fit's go.mod requires fib ff5761a from cfa9fb0, a9493a8
and a test fix after it.

This run is a check, not a gain: neither change alters what fit does by default, and the plan shows they
cost nothing.

## In short

- **Nothing moved past noise.** fit's median against the second 11-03 run is +0.2% throughput and -0.3%
  CPU a request; the other servers' are -0.0% to +0.4%. Two of fit's 58 runs were past -3%, and an A/B of
  each set showed noise: W4's proxied downloads over HTTP/1.1 (-12% in the plan; -1.0% in 5 rounds,
  383 against 387 µs a request, both builds about 15k a second, the 16.8k of the second run its own
  outlier) and W2's cache over HTTPS/1.1 (-3.9%, back to the first 11-03 run's 97k from the second's
  101.6k).
- **The client's lock: one a host now** (fib a9493a8). At W1, with one backend, it is within noise
  (+0.3% to +1.1% over HTTP/1.1), as expected; a proxy with several upstream hosts no longer serializes
  them on one lock. A second change, idle connections in lock-free lists by P, was measured and **left
  out**: it took the lock's wait at 5,000 in flight from 31.5 to 0.18 seconds a second and gave W1 +1% to
  +2.5% and 6-27 MiB less peak memory, but its p99 over HTTP/1.1 at 500 and 5,000 in flight was 10-18%
  worse, for reasons not found. The lock was under 1% of CPU even before.
- **The 200k tail: found, an option rather than a default.** fib parks its event loops in the Go runtime's
  network poller, which looks for their events only when a P has nothing to run or every 10 ms; at 200,000
  a second fit's 6 Ps are nearly always busy, so a loop learns late that its sockets are ready. fib's new
  `Config.BlockingWait` (e7a43a6) has the loops wait blocked in `epoll_wait`, and fit's `engine {
  loop_wait block }` (c3cfc1d) turns it on: at 200,000 a second over HTTP/1.1, p99 4.3 → 2.9-3.9 ms and
  p99.9 5.5-6.3 → 4.5-5.4 ms, but the median 0.95 → 1.57 ms, h2 and the maximum unchanged, and W1 over h2
  0.9% slower at saturation. The default stays `park`.
- **A flaky test fixed** (fib ff5761a): `http3/qpack`'s `TestDynamicRoundTrip` failed for the seeds that
  put two of its fields in one slot of the encoder's filter of fields seen lately; such fields overwrite
  each other each request and neither is ever indexed. The test now picks a seed that keeps them apart;
  the filter itself is being reworked separately.

## The 200k tail

From a packet capture at 200,000 a second over HTTP/1.1, the nginx backend answered fit at p50 20 µs and
p99 0.71 ms, and fit's own time, request in to response out on the wire, was p50 1.14, p99 5.2, p99.9 6.4
ms, about what h2load reports: the tail is fit's. Its slow requests are spread out, not in bursts (over 4
ms in 1,132 of 1,726 10 ms windows; nginx's in 14 of 927). A runtime trace of 8 seconds showed fit's busy
event loops parked for more than 6.4 ms 118 and 144 times, some for 2-3.5 ms with no P idle; collections
were not it (windows with one and without had the same longest scheduling delay, 1.95 and 1.87 ms); the
client's lock blocked 3.6% of acquisitions for up to 1.2-2.9 ms, a part of p99.9 but not of p99. Raising or
lowering the worker cap, GOGC=400 and two adaptive mixes of parking and blocking did not help; only
always blocking did. GOMAXPROCS=5 on the 6 CPUs cut the maximum to 9-13 ms and CPU a request by 7.5%,
worth its own look.

| W6 200,000 a second, `bench/ab.sh` | p99 / p99.9 / max (ms), each round | µs a request | p50 (ms) |
| --- | --- | --: | --: |
| HTTP/1.1, `park` | 4.28/5.53/12.38, 4.36/6.33/15.90 | 23.33 | 0.95 |
| HTTP/1.1, `block` | 3.78/5.19/16.62, 2.88/4.46/13.08, 3.88/5.38/17.33 | 23.05 | 1.57 |
| h2, `park` | 4.89/6.57/14.14, 4.80/6.31/15.00 | 25.94 | 1.47 |
| h2, `block` | 4.82/6.49/20.08, 4.76/6.57/18.36, 4.80/6.70/17.09 | 25.37 | 1.50 |

## Against the second 2026-11-03 run, server by server

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | +0.2% | -0.3% | +1.9% | 0 | 2 |
| nginx | 58 | -0.0% | -0.3% | +0.0% | 4 | 8 |
| Caddy | 52 | +0.4% | -0.4% | +0.9% | 3 | 0 |
| Envoy | 44 | +0.2% | -0.3% | +0.1% | 5 | 5 |
| Traefik | 20 | +0.4% | -0.4% | +0.1% | 0 | 0 |

One memory figure to watch: W1 at 5,000 in flight over HTTP/1.1 peaked at 239 MiB, against 193 in the
second run and 200-221 in the worker cap's A/B; W1 over h2 at 5,000 was 246 (245), at 10,000 319 and 388
(317 and 373).

## Where fit stands

As in [the second 11-03 run](../plan-2026-11-03b/report.md#where-fit-stands): first over h2c, h2 and
HTTP/3 at every level (HTTP/3 128-165% of nginx), level with nginx over HTTP/1.1 for plain responses,
second to nginx in the API gateway over HTTP/1.1 (83-91%) and level over h2.

## What is left

- **W1 over HTTP/1.1, 83-91% of nginx.** With the client's lock gone as a cost, the rest is spread over
  the request path; the largest single lock left at many requests in flight is the HTTP/2 server
  connection's in `h2ServerStream.respond` (h2 at 5,000), then bufferpool's.
- **The tail at 200,000 a second,** for those who do not choose `loop_wait block`, and GOMAXPROCS one below
  the CPUs as a lead.
- **The website over HTTP/3,** per byte in Go's AES-GCM.
