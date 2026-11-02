# Load test plan, 2026-11-03: the report

[English](report.md) | [简体中文](report.zh-CN.md)

The plan of [2026-11-02](../plan-2026-11-02/report.md) run again, unchanged but for fit: d6bdf04 on fib
3664a1e, after the work on the three items that report left open. 232 runs on the benchmark host,
2026-11-02 16:51 to 18:23 UTC (2026-11-03 00:51 to 02:23 in the host's Asia/Hong_Kong): none failed,
none hung, no run had a single error. Every table, with each row's change from 2026-11-02, is in
[tables.md](tables.md); the plan derived is [plan.txt](plan.txt), the progress log [plan.log](plan.log).
The host was as the 2026-11-02 run left it: the same images, kernel settings and CPU split, nothing
else running but the desktop session and the idle llm node, `strata.service` stopped for the run.

fit was built with fib at 3664a1e before it was pushed (from a `go.work` outside the repository), so its
build records fib as `(devel)`; the same fib is what fit's go.mod now requires, 825d909.

## In short

- **HTTP/3 now scales with connections.** fit's CPU a request over HTTP/3 stays at 3.1-3.2 µs from 500
  to 10,000 in flight, where on 2026-11-02 it rose from 3.5 to 4.2: W0 over HTTP/3 +12% at 500, +19% at
  2,000, **+36% at 5,000 and +33% at 10,000** (1.93M and 1.87M a second), level with nginx at 500 to
  5,000 (99-100%) and 98% of it at 10,000, from 73-85%. At 100 in flight fit is now first (+5.5%).
- **The API gateway is faster again over both protocols.** W1 over HTTP/1.1 +5% to +11.5% at every level
  (243,014 a second at 500, 24.49 µs a request from 26.58), over h2 +3.5% to +6%; W5 +10.0% and +5.7%;
  W6 at a fixed rate 2-12.5% less CPU a request. Against nginx, W1 over HTTP/1.1 is 77-87% (69-81%
  before), over h2 91-99% (93-95%).
- **Nothing of fit's got slower.** Of its 58 runs, 19 are more than 3% faster than on 2026-11-02 and none
  more than 3% slower; median +1.2% throughput, -2.0% CPU a request. The other servers' medians are
  +0.6% to +1.2%.
- **One cost, found and fixed:** fit's peak memory with many requests in flight through the proxy rose
  (W1 at 5,000 over h2 415 MiB from 304). An A/B traced it to fit's d6bdf04, whose timer held recycled
  exchanges past the pool; fit 6305218 holds them weakly and brings HTTP/1.1 at 5,000 back to 328-371
  MiB from 372-399 (336-344 before d6bdf04), at the same throughput. This plan ran without 6305218.
- **The W6 tail of 2026-11-02 is not a regression.** fit's p99.9 over h2 at 30k a second (9.64 ms) is one
  of two modes, about 3 and about 10 ms, that the 2026-10-30 build shows as well (A/B below).

## What changed

fib 4b24cf3 to 3664a1e, two branches merged:

| Commit | Change |
| --- | --- |
| de62b0b | HTTP/1 request framing and response heads walk the header map once; the response parse notes the fields it needs as it goes |
| e17bf98 | field values checked for control bytes eight at a time |
| 540385b | the client looks for Transfer-Encoding only where lines start |
| c92842d | header fields ordered by an eight-byte prefix first |
| 2adf91e | a read's byte count boxed for its trace only when tracing |
| 5693fff | a connection's deadline timer left set while the deadline is cleared or moved later, so the HTTP server moves it once per idle timeout rather than twice per request |
| d5a8804, 48914bd | QUIC holds a due acknowledgement, and the credit sent with it, for a burst's answers as long as max_ack_delay allows instead of 1ms; under load the 1ms hold expired for 15% of bursts and every answer left in a datagram of its own (1.82 sent per one received at 5,000 in flight, 0.65 after). Data of an upload in progress keeps the 1ms hold |
| 248ca81, 84f0931, 69bdf4d | HTTP/3: a response's header checked and weighed in one pass, the clock read once per run of QUIC's calls, a request's own fields looked up without canonicalizing |
| 9258b9a | a sent packet's frame record takes a 64-frame array from a pool past its inline room, no longer four allocations a packet answering 25 requests |

fit: e5b8675 (Connection's `keep-alive`/`close` without canonicalizing, the Forwarded host quoted by hand),
d6bdf04 (a recycled exchange keeps its deadline timer), and 6305218 (that timer holds the exchange weakly).

Each was measured alone with `bench/ab.sh` before it was kept; the numbers are in the commit messages.

## Against 2026-11-02, server by server

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | +1.2% | -2.0% | +0.8% | 19 | 0 |
| nginx | 58 | +0.6% | -0.6% | +0.0% | 4 | 5 |
| Caddy | 52 | +0.7% | -0.8% | -1.4% | 0 | 0 |
| Envoy | 44 | +1.2% | -1.5% | +0.0% | 5 | 4 |
| Traefik | 20 | +0.8% | -0.9% | +0.2% | 0 | 0 |

## fit, where it changed

Throughput, CPU a request and peak memory against 2026-11-02. Every run not listed is within ±3%
([tables.md](tables.md)).

| Run | Scenario | Protocol | 11-02 | 11-03 | Change | µs a request | Peak MiB |
| --- | --- | --- | --: | --: | --: | --: | --: |
| W0 100 | fixed | HTTP/3 | 1,108,503 | 1,169,368 | +5.5% | 4.31→3.92 | 18→18 |
| W0 500 | fixed | HTTP/3 | 1,684,797 | 1,889,559 | +12.2% | 3.51→3.12 | 22→23 |
| W0 2,000 | fixed | HTTP/3 | 1,626,805 | 1,928,897 | +18.6% | 3.63→3.08 | 55→42 |
| W0 5,000 | fixed | HTTP/3 | 1,418,916 | 1,927,661 | **+35.9%** | 4.18→3.08 | 94→94 |
| W0 10,000 | fixed | HTTP/3 | 1,405,500 | 1,869,112 | **+33.0%** | 4.23→3.17 | 147→152 |
| W1 100 | api | HTTP/1.1 | 212,094 | 236,459 | +11.5% | 28.00→25.20 | 18→28 |
| W1 100 | api | h2 | 206,961 | 219,297 | +6.0% | 28.77→27.14 | 24→28 |
| W1 500 | api | HTTP/1.1 | 223,853 | 243,014 | +8.6% | 26.58→24.49 | 49→60 |
| W1 500 | api | h2 | 226,103 | 238,388 | +5.4% | 26.38→25.04 | 69→72 |
| W1 2,000 | api | HTTP/1.1 | 215,901 | 235,658 | +9.2% | 27.50→25.24 | 149→159 |
| W1 2,000 | api | h2 | 228,054 | 239,743 | +5.1% | 26.17→24.90 | 172→174 |
| W1 5,000 | api | HTTP/1.1 | 189,185 | 202,253 | +6.9% | 31.32→29.33 | 352→385 |
| W1 5,000 | api | h2 | 216,810 | 226,836 | +4.6% | 27.51→26.29 | 304→415 |
| W1 10,000 | api | HTTP/1.1 | 166,461 | 175,170 | +5.2% | 35.54→33.71 | 652→650 |
| W1 10,000 | api | h2 | 193,607 | 200,289 | +3.5% | 30.70→29.68 | 664→649 |
| W4 100 | proxy-download | HTTP/1.1 | 14,539 | 15,696 | +8.0% | 408.49→378.99 | 34→31 |
| W4 100 | download | HTTP/1.1 | 18,127 | 18,482 | +2.0% | 146.72→128.16 | 14→16 |
| W5 500 | api + idle | HTTP/1.1 | 219,792 | 241,721 | +10.0% | 27.03→24.59 | 438→442 |
| W5 500 | api + idle | h2 | 221,954 | 234,672 | +5.7% | 26.88→25.39 | 453→456 |
| W6 30,000 | api | HTTP/1.1 | 30,000 | 30,000 | rate | 33.84→29.60 | 31→41 |
| W6 200,000 | api | HTTP/1.1 | 199,886 | 199,900 | rate | 26.83→24.95 | 46→75 |
| W6 200,000 | api | h2 | 199,772 | 199,780 | rate | 26.69→25.47 | 64→69 |

W2 (the website and its cache) moved -2.8% to +3.0%, W3 (churn) -0.2% to +1.3%, W0 over HTTP/1.1, h2c
and h2 -1.6% to +1.9%. The peak memory rises in W1 and W6 are the proxy timer's (next section).

## The proxy's memory, found and fixed

A rerun of W1 at 5,000 in flight, 3 rounds each, `bench/ab.sh`:

| fit build | HTTP/1.1 req/s | HTTP/1.1 peak MiB | h2 req/s | h2 peak MiB |
| --- | --: | --: | --: | --: |
| e497491 on fib 4b24cf3 (2026-11-02) | 191k | 336-344 | 216k | 307-354 |
| e497491 on fib 3664a1e | 199k | 324-332 (one 429) | | |
| d6bdf04 on fib 4b24cf3 | 192k | 363-398 | | |
| d6bdf04 on fib 3664a1e (this plan) | 203k | 372-399 | 227k | 325-373 |
| 6305218 on fib 3664a1e | 204k | 328-371 | 228k | 371-376 |

The rise was fit's d6bdf04: a recycled exchange kept its timer set, and the timer's function held the
exchange, so one that `sync.Pool` let go of stayed until the timer fired, up to the route's 60s
timeout. 6305218 has the timer hold it through a weak pointer. Over h2 the peaks spread too widely in 3
rounds to tell 6305218 from d6bdf04; the next plan will show it.

## W6: the tail at 30k a second

`bench/ab.sh`, W6 at 30,000 a second, 500 connections, 5 rounds each, fit 24987e5 (2026-10-30) and
c8a44e8 (2026-11-02):

| Build | h2 p99 (ms) | h2 p99.9 (ms) | h2 µs a request | HTTP/1.1 p99.9 (ms) |
| --- | --- | --- | --: | --- |
| 24987e5 | 1.75-2.38 | 9.60, 9.39, 3.35, 3.42, 3.20 | 36.5-37.3 | 4.5-12.2 |
| c8a44e8 | 1.19-1.80 | 2.76, 2.33, 3.08, 9.91, 3.23 | 33.5-34.3 | 3.7-10.8 |

Both builds land a run's p99.9 near 3 ms or near 10 ms, over either protocol, with the maximum always
11-13 ms; the 2026-11-02 plan's 9.64 ms was a draw from the higher mode. Something stalls about one
request in a thousand for about 10 ms in fit since before 2026-10-30, perhaps a collection or a periodic
timer; it is left to find. In this plan fit's h2 p99 at 30,000 is 1.22 ms, the lowest of the five
servers (nginx 1.66).

## Where fit stands

| Workload | fit's place | fit against the best other |
| --- | --- | --- |
| W0, HTTP/1.1 | 1st or 2nd of 4 | 99-101% of nginx |
| W0, h2c and h2 | 1st | 2.5-6.7 times nginx |
| W0, HTTP/3 | 1st at 100 to 2,000, 2nd at 5,000 and 10,000 | 98-104% of nginx (73-85% at 2,000 to 10,000 on 2026-11-02) |
| W1, HTTP/1.1 | 2nd of 5 | 77-87% of nginx (24.5 against 19.5 µs a request at 500) |
| W1, h2 | 2nd of 5 | 91-99% of nginx |
| W2, HTTPS/1.1 and h2 | 1st | 120-190% of nginx |
| W2, HTTP/3 | 2nd | 52-83% of nginx |
| W3 | 1st or 2nd | 99-103% of the best |
| W4 | 1st, but HTTP/1.1 uploads 3rd | 111-219% of the best other; 96% of Caddy's uploads |
| W5 | 2nd of 5 | 79% and 95% of nginx, at a third of its memory |
| W6 | at 200,000 the only server keeping the rate over both protocols | lowest h2 p99 at 30,000 |

## What is left

- **W1 over HTTP/1.1, 77-87% of nginx.** The kernel's share is the same as nginx's (about 70k cycles a
  request each, and fit makes fewer system calls); the gap is fit's user space, 61k cycles and 84k
  instructions a request against nginx's 26k and 34k, spread thin: header maps (8%), marshalling and
  parsing heads (5% each), timers, pools, allocation and collection (2-3% each). The next step is
  structural: pass header blocks through the proxy as bytes rather than building, sorting and writing
  a map on each side, about 5% by estimate, which needs new fib API.
- **The website over HTTP/3, 52-83% of nginx,** is per byte, not per connection: AES-GCM is a quarter of
  fit's CPU there, and Go 1.27's amd64 GCM has no VAES path, which OpenSSL uses on Zen 4; UDP send with
  GSO another fifth.
- **W1's peak memory at 5,000 to 10,000 in flight,** 385-650 MiB against nginx's 221-411, before
  6305218.
- **The 10ms stall** behind W6's p99.9 (above).

## Notes and limits

- One machine, loopback, one run of each: a few percent in a single run is within noise; the HTTP/3 and
  W1 gains hold at every level.
- The proxy timer's memory fix, 6305218, was measured by A/B, not in this plan.
- Latency over h2c, h2 and HTTP/3 outside W6 is a mean and a maximum.
