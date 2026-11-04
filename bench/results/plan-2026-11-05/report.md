# Load test plan, 2026-11-05: the report

[English](report.md) | [简体中文](report.zh-CN.md)

The plan of [the third 2026-11-04 run](../plan-2026-11-04c/report.md) run again with fit baf933e on fib f5b63c7,
after a round of CI fixes in both, and the first report with scores: every table of the plan scored, the
servers ranked by their weighted overall score ([rule 24](../../../docs/rules.md#11-load-test-reports)). 232 runs
on the benchmark host, 2026-11-04 18:40 to 20:12 UTC: none failed, none hung, no run had a single error. Tables,
each with its weight and every row's score, in [tables.md](tables.md), the plan in [plan.txt](plan.txt), the log
in [plan.log](plan.log); the host as before, its LLM service stopped for the run and started again after it.

## In short

- **fit ranks first, 92.7 out of 100**, ahead of nginx (75.0), Envoy (41.6), Caddy (29.6) and Traefik (29.2):
  the order of the third 11-04 run, every server's score within 0.3 of it. fit leads every workload but W3,
  where Envoy and Caddy win single requests on new connections; nginx comes closest in the API gateway
  (89.7 to fit's 93.2) and at a fixed rate (83.3 to 88.4).
- **Nothing moved, as nothing on the data path changed.** fit's medians against the third 11-04 run +0.5%
  throughput, -0.5% CPU a request, -0.5% peak memory; the other servers' +0.2% to +0.5%. fib's changes were
  a lost QUIC NEW_TOKEN sent again and tests; fit's, a CI coverage gap closed and fixes off the hot paths.
- **Still open where fit is behind:** HTTP/1.1 in the API gateway, 86-98% of nginx; HTTP/3 for the website,
  53-88% of nginx; and HTTP/1.1 latency at 200,000 a second, p99 4.80 ms to nginx's 1.68.

## The ranking

| Rank | Server | Score | W0 | W1 | W2 | W3 | W4 | W5 | W6 | Coverage | Score at 11-04c |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | fit | **92.7** | 99.1 | 93.2 | 83.7 | 98.1 | 94.8 | 94.1 | 88.4 | 100% | 92.6 (1) |
| 2 | nginx | 75.0 | 48.7 | 89.7 | 80.5 | 76.6 | 53.6 | 77.1 | 83.3 | 100% | 74.9 (2) |
| 3 | Envoy | 41.6 | 16.4 | 34.8 | — | 87.3 | 48.0 | 32.5 | 49.3 | 85% | 41.5 (3) |
| 4 | Caddy | 29.6 | 12.3 | 20.5 | 16.3 | 80.6 | 58.5 | 16.8 | 30.7 | 100% | 29.3 (4) |
| 5 | Traefik | 29.2 | — | 25.2 | — | — | 41.8 | 19.9 | 33.6 | 60% | 29.0 (5) |

Each table of the plan, a scenario over a protocol in one results file, is an item, scored out of 100 from the
server's shares of the table's best throughput, CPU efficiency, memory efficiency and latency, times the share of
its requests that did not fail; a failed run, or a scenario the server cannot serve, scores 0. The workloads weigh
W1 25%, W0, W2 and W6 15% each, W3, W4 and W5 10% each, shared equally by their items; W5 counts memory more and
W6 the p99 latency. A server's score is the weighted mean over the items it ran in, and its coverage the share of
the plan's weight those hold: the plan runs Envoy without W2, and Traefik in W1, W4, W5 and W6 only. The full
method and every item's weight and scores are at the end of [tables.md](tables.md#scores) and in
[bench/README.md](../../README.md#scores). The 11-04c column is that run's results scored the same way.

nginx's W0 score is low for its h2c and h2, where fit answers 2.5 to 6.8 times as many requests, and for its
memory, 170 MiB or more against fit's tens of MiB; over HTTP/1.1 it scores close to fit (86-89 against 94-98 in W0)
and wins W1's HTTP/1.1 items on throughput and latency.

## Where fit stands

| Workload | Protocol | fit's place | Against the best, or the next |
| --- | --- | --- | --- |
| W0, the raw ceiling | HTTP/1.1 | first at 500 to 10,000, second at 100 | level with nginx (99-101%) |
| | h2c, h2 | first at every level | 2.5-6.8 times nginx |
| | HTTP/3 | first at every level | +10% to +32% over nginx |
| W1, the API gateway | HTTP/1.1 | second, to nginx | 86% (100), 90% (500), 91% (2,000), 88% (5,000), 98% (10,000) |
| | h2 | first at every level | +5% to +25% over nginx |
| W2, the website | HTTPS/1.1, h2 | first, site and cache alike | +23% to +93% over nginx |
| | HTTP/3 | second, to nginx | site 53% (500) and 71% (2,000), cache 63% and 88% |
| W3, connection churn | HTTPS/1.1 | first with 10 requests a connection at 100; otherwise second or third | 99-100% of the best |
| W4, large transfers | HTTP/1.1, h2 | first in all six | download +12%/+96% over nginx; proxied download +7%/+44% over Envoy; upload +3% over Caddy, +6% over nginx |
| W5, idle and active | HTTP/1.1 | second, to nginx | 91%, at a third of its memory (432 against 1,304 MiB with 40,000 idle) |
| | h2 | first | +8% over nginx |
| W6, at 30,000 a second | HTTP/1.1 | second, to nginx | p99 2.41 ms to 2.21 |
| | h2 | first | p99 1.25 ms to nginx's 2.31 |
| W6, at 200,000 a second | HTTP/1.1 | second, to nginx | both keep up; p99 4.80 ms to 1.68 |
| | h2 | first | the only server that keeps up: 199,862 a second at p99 4.16 ms, nginx 167,521 |

First over h2c, h2 and HTTP/3 at every level of W0, over h2 in the API gateway, idle and active, and at both
fixed rates, and over HTTPS/1.1 and h2 for the website; level with nginx over HTTP/1.1 for plain responses;
first in every large transfer; second to nginx over HTTP/1.1 in the API gateway and its latency, and for the
website over HTTP/3.

## Against the third 2026-11-04 run, server by server

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | +0.5% | -0.5% | -0.5% | 2 | 1 |
| nginx | 58 | +0.3% | -0.8% | -0.0% | 8 | 4 |
| Caddy | 52 | +0.2% | -0.4% | -0.2% | 0 | 0 |
| Envoy | 44 | +0.3% | -0.5% | +0.2% | 6 | 5 |
| Traefik | 20 | +0.5% | -0.7% | -1.1% | 0 | 0 |

fit's three rows past 3% are its known spread: the proxied download over HTTP/1.1 (-8.1%, 16,070 → 14,773)
has come out at 14.7k or 16.1k a second in turn since 11-03d (14,663, 16,077, 14,719, 16,070, 14,773); the upload
over HTTP/1.1 (+7.6%, 9,374 → 10,083) and the API gateway over HTTP/1.1 at 10,000 (+5.2%, 193,565 → 203,535) are
within the 191-204k and 9.2-10.1k those have ranged over. nginx's h2c at 100 in flight (+32%) and its h2 in the
API gateway at 100 and 500 (-10%, -11%) move as much between plans.

## What changed since 11-04c

fib, its CI green again (four runs had failed on four tests):

- **A lost NEW_TOKEN is sent again** (f5b63c7): a server's NEW_TOKEN frame went once and was gone with its
  packet if lost, so the client's next connection had no token and was retried; it is now sent again, as a new
  token, as RFC 9000 section 13.3 has it. The Coverage job's TestNewTokenSkipsRetry lost that one datagram.
- Tests that had timing guesses or races fixed in the test: the RTSP relay test read packets without its sink's
  lock; the HTTP/2 client's PING flood was absorbed by the refill of a slow reader; the recycled-streams test
  had net/http dial a connection for each of 1,000 requests, past macOS's listen queue of 128, which reset them;
  the HTTP/3 client timeout case left the next request 200 ms.

fit, its CI green again (red since four pushes, the coverage floor at 97.6% against 98.0%, and a macOS failure):

- **The WebRTC proxy's relays outlive a DELETE by a second** (aa1df47): the media server's DTLS close_notify
  comes through a relay after the DELETE's answer may have, and was dropped, so the peer ended only when ICE
  consent ran out, 30 seconds on.
- **A reconcile's deletes go from the later array element down** (cb20ba6): `/listeners/0` went before
  `/listeners/1/tls/acme/0`, which then hit the wrong listener; indices compared as strings too.
- **A chat file no room could hold is refused before eviction** (de298fc), rather than after evicting the
  room's files.
- Tests for the consistency check, config transfer, console, store, media tokens, media pages and media auth:
  coverage 97.6% → 98.2%.
- `benchreport -plan` and `-scores`, the scores and ranking of this report (32bf4c4), and rule 24, that every
  plan's report says where fit stands and ranks the servers (bc894e5).

The plan ran fit baf933e; the three fixes above came after it and touch none of the plan's paths (WebRTC, the
Admin API's reconcile, the media chat).
