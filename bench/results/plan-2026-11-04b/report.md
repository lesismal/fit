# Load test plan, 2026-11-04 (second run): the report

[English](report.md) | [简体中文](report.zh-CN.md)

The plan of [the first 2026-11-04 run](../plan-2026-11-04/report.md) run again with fit on fib 07ee9e0,
after work on the API gateway's latency over HTTP/1.1 and on memory. 232 runs on the benchmark host,
2026-11-04 06:14 to 07:47 UTC (14:14 to 15:47 in the host's Asia/Hong_Kong): none failed, none hung, no run
had a single error. Tables in [tables.md](tables.md), the plan in [plan.txt](plan.txt), the log in
[plan.log](plan.log); the host as before, with `strata.service` and `qwen27b.service` (which holds port
8081 when it runs) stopped for the run. fit's go.mod requires fib 07ee9e0 from 54e1c68.

## In short

- **The API gateway's tail over HTTP/1.1 is 18% shorter:** W1's p99 2.70 → 2.22 ms at 100 in flight and
  7.23 → 5.96 ms at 500, 42.8 → 41.3 ms at 5,000; W5's 9.35 → 8.49 ms. Throughput: -1.5% at 100 (the price
  of it, below), -0.9% at 500, +1.7% at 5,000.
- **Less memory:** proxied uploads' peak 147 → 50 MiB; W1 at 10,000 in flight over HTTP/1.1 329 → 308 MiB
  (nginx 263; repeated A/Bs put fit at 255-301), at 5,000 197 → 189.
- **Nothing else moved:** fit's median throughput, CPU a request and peak memory against the first 11-04
  run are 0.0%, 0.0% and +0.4%; the other servers' medians -0.3% to 0.0%.
- **One run to watch:** proxied downloads over HTTP/1.1 -8.4% (16,077 → 14,719). This workload lands at
  either about 14.5-15.0k or 16.0-16.7k a run, whichever build runs; a 5-round A/B gave -3.1%, within
  noise, with equal medians (16,091 against 16,200) but the new build in the lower cluster in 2 of 5
  rounds against 0 of 5.
- **Measured and not taken:** TLS churn (W3 churn-1) is bound by the load generators — their six threads at
  100% doing client handshakes while the server's CPUs sat 20% idle; every server measures 19.0-19.5k a
  second whatever its CPU. Further memory cuts (smaller first send buffers, smaller read buffers, a
  smaller bufferpool reserve, a memory limit) each cost 1-6% throughput at 500 in flight.

## What changed

| Commit | Change | A/B |
| --- | --- | --- |
| fib 07ee9e0, fit a510ae8, cfa37b3 | `Framing.Flush`: a request a caller marks is written at once rather than held in its backend connection's cork until the round ends. fib's client hands a connection back mid-round, so the next request usually landed on one whose round was still writing the last response, and waited for it. fit marks requests that arrive over HTTP/1 while their backend has fewer than 1,000 under way; above that, flushing each request cost p99 (+11% at 5,000, all 5 rounds), and over h2 the cork's batching is worth more (no cork at all cost h2 4%) | HTTP/1.1 p99 -18% at 100, -15% at 500, equal at 5,000; throughput -1.1% at 100 (5 of 5 rounds), within noise above |
| fib 53372a8, b44cbff | `drive`'s deferred calls open-coded (the parse loop moved to `serveParsed`); a client request's address taken from its URL as it is | +0.8% at 500, below noise; one allocation fewer a request |
| fit cd50821 | the proxy's header buffers go back to fib's pool once fib's client has copied them, rather than staying with the exchange through the wait for the backend | W1 at 10,000 peak 317 → 301 MiB (later runs 255-270), throughput within noise |
| fit 41ccd15 | an upload's client is held after one 64 KiB read waits rather than 256 KiB, and the streamed body's buffer is 64 KiB | proxy-upload peak 99-134 → 46-60 MiB, throughput within noise |

## Where the bytes are at 10,000 in flight

Anonymous memory is about the heap in use, which Go's collector lets reach twice the live heap
(GOGC=100): about 126 MB live at 10,000 in flight over HTTP/1.1. Of it, about 7 KB a request in flight —
fib's copy of the request sent upstream, kept for a retry on a stale connection, the request block and
its header map, the proxy's exchange, the header text — and about 2 KB a pair of connections; goroutine
stacks only 6-9.5 MB. `GOMEMLIMIT=200MiB` takes the peak to 187 MiB at -5.8% throughput, for whoever wants
that trade.

## Proxied uploads over HTTP/1.1

87% of fit's CPU on W4's proxied uploads is the kernel's: copying a 1 MiB body in from the client and out to
the backend, and zeroing the fresh pages the backend's send buffer takes. fit spends 40 µs a request less
than Caddy in user space and 77 µs more in the kernel. Splicing a plaintext HTTP/1.1 body from the client's
socket to the backend's through a pipe, on Linux, would save both copies and the zeroing, about 45% of the
CPU there; it is a feature of its own in fib, not done.

## Against the first 2026-11-04 run, server by server

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | -0.0% | +0.0% | +0.4% | 0 | 1 |
| nginx | 58 | -0.0% | +0.2% | +0.0% | 7 | 4 |
| Caddy | 52 | -0.3% | +0.3% | -0.9% | 0 | 0 |
| Envoy | 44 | -0.2% | +0.0% | -0.5% | 6 | 8 |
| Traefik | 20 | -0.0% | +0.1% | -1.7% | 0 | 0 |

## Where fit stands

First over h2c, h2 and HTTP/3 at every level; first over h2 in the API gateway from 500 to 5,000 in flight
and with idle connections; level with nginx over HTTP/1.1 for plain responses and in connection churn;
85-95% of nginx in the API gateway over HTTP/1.1, now with an 18% shorter tail; level with nginx on memory
at 10,000 in flight.
