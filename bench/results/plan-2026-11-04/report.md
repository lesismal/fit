# Load test plan, 2026-11-04: the report

[English](report.md) | [简体中文](report.zh-CN.md)

The plan of [the fourth 2026-11-03 run](../plan-2026-11-03d/report.md) run again with fit at 82dfdda on fib
c7b854c, after work on the API gateway over HTTP/1.1. 232 runs on the benchmark host, 2026-11-03 15:36 to
17:09 UTC (23:36 to 01:09 in the host's Asia/Hong_Kong): none failed, none hung, no run had a single error.
Tables in [tables.md](tables.md), the plan in [plan.txt](plan.txt), the log in [plan.log](plan.log); the
host as before, `strata.service` stopped for the run. fit's go.mod requires fib ea27938 (c7b854c and a
test fix) from d92789b.

## In short

- **The API gateway over HTTP/1.1 is 2-4% faster again:** W1 +2.8% at 100, +3.6% at 500 (274,721 a
  second, 21.68 µs a request), +2.8% at 2,000, +1.8% at 5,000, +2.6% at 10,000; W5 +3.0%; W6 3% less CPU a
  request. Against nginx: **87-91% from 100 to 5,000 and 99% at 10,000** (83-91% before). The other
  servers ran 0.9-1.3% slower this time (their medians), so fit's gain against them is that much larger.
- **Over h2 too:** W1 at 5,000 +3.1%.
- **Nothing slower.** The one run of fit's past -3%, downloads over HTTP/1.1 (-6.4%), is +3.9% in a
  5-round A/B (and -0.7% over h2); its figure has ranged 17.7k-18.9k a second between plans.

## What changed

fib c492d7e to c7b854c (A/B per commit in the messages):

| Commit | Change | W1 HTTP/1.1 at 500 |
| --- | --- | --- |
| d6c1cbd | an HTTP/1 client request is timed by its connection's timer, left set from one request to the next and re-armed when it fires early, rather than a timer of its own; requests waiting for a connection and on HTTP/2 keep their own | +2.2% (h2 +2.0%) |
| 5b7b109 | a field name read up to its colon as it is checked | +0.8% |
| e51b8f5 | two fewer allocations a client request | +0.4% (with fit 82dfdda) |
| 2e2fef1, 534f420 | header fields stored without a lookup first (a map that did not grow had the name, whose values are gathered again from the lines before); CRLFs checked in the main pass | +0.5% |
| ba7ca84 | `\r\n\r\n` no longer allocated per request under PGO; a recycled request block keeps its header map | +0.4% |

fit 82dfdda makes the exchange's response callback once rather than per request. Together, against main
at 500 in flight: 263.5k → 273.5k (+4.3%), user instructions a request 53.3k → 48.7k, allocations 14 → 7.6.
Rejected: one allocation for the response and its body (no gain), parsing client responses in place
(+0.2%, p99 +2%), turning off write coalescing on HTTP/1 client connections (p99 -22% but h2 -4.4%).

## Against the fourth 2026-11-03 run, server by server

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | -0.1% | +0.2% | -0.1% | 3 | 1 |
| nginx | 58 | -1.2% | +1.2% | -0.0% | 4 | 7 |
| Caddy | 52 | -1.2% | +1.1% | +0.6% | 0 | 3 |
| Envoy | 44 | -1.3% | +1.4% | -0.3% | 1 | 8 |
| Traefik | 20 | -0.9% | +0.9% | -1.0% | 0 | 1 |

## Fixed and found in passing

- **fit `upstream` TestLeaveAndRejoin** (1ad369d): the test assumed 20 requests finish within the
  backend's 300ms `fail_timeout`; under load they did not, the window ended and the backend was rightly
  tried again. It now checks each request against the window it ran in.
- **fib `media/hls` TestHandlerPlayWaitExpires** (ea27938): it read the waiters' count as the answer
  arrived, before the waiter was let go.
- **fit `test/e2e` TestProxyWasmResponse failed once** in a full local run, h2c with a 300 KiB body: the
  status, the length and the header were right, but the body was not all what the wasm filter makes. 53
  runs since passed; the test now says where the body first differs (cb068b7), and it is being looked into
  on its own.

## Where fit stands

First over h2c, h2 and HTTP/3 at every level; first over h2 in the API gateway from 500 to 5,000 in flight
and with idle connections; level with nginx over HTTP/1.1 for plain responses; **87-99% of nginx in the
API gateway over HTTP/1.1**; first for the website over HTTPS/1.1 and h2, second over HTTP/3.
