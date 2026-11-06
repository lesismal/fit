# Load test plan, 2026-11-06: the report

[English](report.md) | [简体中文](report.zh-CN.md)

The plan of [2026-11-05](../plan-2026-11-05/report.md) run again with the release, fit v1.0.0 (61b5a6c4) on
fib v1.7.2. Between the two runs came the security review in both, fib v1.7.1 and v1.7.2 and fit's round
after them. 232 runs on the benchmark host, 2026-11-06 00:31 to 02:03 UTC: none failed, none hung, and no run
had a single error. [tables.md](tables.md) has every table with its weight and every row's score. The plan is
in [plan.txt](plan.txt) and the log in [plan.log](plan.log). The host was the same as before; its LLM service
(qwen27b) was stopped for the run and started again after it. Every kernel and CPU setting was as the
2026-10-30 plan's (`host-settings.sh -check`).

## In short

- **fit v1.0.0 ranks first, 92.8 out of 100**, ahead of nginx (74.6), Envoy (41.9), Caddy (29.5) and
  Traefik (28.9). That is the order of 11-05, and every server's score is within 0.4 of it. fit leads every
  workload but W3, where Envoy and Caddy win single requests on new connections. nginx comes closest in the
  API gateway (90.7 to fit's 93.2) and at a fixed rate (81.4 to 89.8).
- **The security release costs fit 3-5% on W0's h2c and h2 above 100 in flight, and about 1% elsewhere.**
  - fit's medians against 11-05: -1.2% throughput and +1.5% CPU a request.
  - The other servers, which did not change, moved -0.3% to -0.8%. That is the host's drift between the runs.
  - The W0 loss is outside every earlier plan's spread: h2c at 5,000 in flight has been 3.39-3.43M a second
    since 11-03d and is now 3.26M. It is I158, fib v1.7.1's HTTP/2 request checks: `bench/ab.sh` measured
    it at -2.4% on 11-05. fib cb9bbbe, on fit's main since 35a700cc, brings it back within noise. No
    release carries that fix yet.
  - fit is still 3.3 to 6.7 times nginx in those tables, so its scores there do not move.
- **fit is still behind in three places:**
  - HTTP/1.1 in the API gateway: 86-96% of nginx.
  - HTTP/3 for the website: 53-87% of nginx.
  - HTTP/1.1 latency at 200,000 a second: p99 5.33 ms to nginx's 2.17.

## The ranking

| Rank | Server | Score | W0 | W1 | W2 | W3 | W4 | W5 | W6 | Coverage | Score at 11-05 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | fit | **92.8** | 99.0 | 93.2 | 83.7 | 97.4 | 95.1 | 93.6 | 89.8 | 100% | 92.7 (1) |
| 2 | nginx | 74.6 | 48.5 | 90.7 | 80.6 | 75.6 | 50.8 | 77.7 | 81.4 | 100% | 75.0 (2) |
| 3 | Envoy | 41.9 | 16.5 | 35.1 | — | 87.0 | 48.7 | 32.3 | 50.3 | 85% | 41.6 (3) |
| 4 | Caddy | 29.5 | 12.3 | 20.7 | 16.4 | 80.1 | 57.5 | 16.8 | 30.5 | 100% | 29.6 (4) |
| 5 | Traefik | 28.9 | — | 25.6 | — | — | 40.5 | 19.6 | 32.8 | 60% | 29.2 (5) |

The scores, weights and method are unchanged since 11-05, so the two columns compare:

- **Items.** Each table of the plan is an item: a scenario over a protocol in one results file.
- **An item's score**, out of 100, comes from four shares of that table's best: throughput, CPU efficiency,
  memory efficiency and latency. The weighted sum is multiplied by the share of the server's requests that
  did not fail. A failed run, or a scenario the server cannot serve, scores 0.
- **Workload weights.** W1 is 25%; W0, W2 and W6 are 15% each; W3, W4 and W5 are 10% each. A workload's items
  share its weight equally. W5 counts memory more, and W6 counts the p99 latency more.
- **A server's score** is the weighted mean over the items it ran in. Its coverage is the share of the plan's
  weight those items hold: the plan runs Envoy without W2, and Traefik in W1, W4, W5 and W6 only.

The full method and every item's scores are at the end of [tables.md](tables.md#scores).

Scores are shares of the table's best, so a loss in a table fit leads by far hardly moves its score: W0's h2c
and h2 lost 3-5% and fit's W0 went from 99.1 to 99.0. A change where the servers are close shows more:
fit's W3 went from 98.1 to 97.4 because Envoy's p99 for single requests at 100 connections fell from 2.68 to
1.73 ms, though fit moved up a place there. nginx went from 83.3 to 81.4 in W6, where its p99 at 200,000 a
second over HTTP/1.1 rose from 1.68 to 2.17 ms.

## Where fit stands

| Workload | Protocol | fit's place | Against the best, or the next |
| --- | --- | --- | --- |
| W0, the raw ceiling | HTTP/1.1 | first at 500 and 2,000, second at 100, 5,000 and 10,000 | level with nginx (98-101%) |
| | h2c, h2 | first at every level | 3.3-6.7 times nginx (h2c 4.5-6.7, h2 3.3-4.3) |
| | HTTP/3 | first at every level | +17% to +33% over nginx |
| W1, the API gateway | HTTP/1.1 | second, to nginx | 86% (100), 90% (500), 88% (2,000), 87% (5,000), 96% (10,000) |
| | h2 | first at every level | +3% to +10% over nginx |
| W2, the website | HTTPS/1.1, h2 | first, site and cache alike | +21% to +94% over nginx |
| | HTTP/3 | second, to nginx | site 53% (500) and 69% (2,000), cache 70% and 87% |
| W3, connection churn | HTTPS/1.1 | first with 10 requests a connection, at 100 and 500 (11-05: first and second); second at 100 and third at 500 with one (11-05: third at both) | 99-100% of Caddy with one request a connection |
| W4, large transfers | HTTP/1.1, h2 | first in all six | download +8%/+125% over nginx; proxied download +16%/+43% over Envoy; upload +2.5% over Caddy (HTTP/1.1), +9% over Envoy (h2) |
| W5, idle and active | HTTP/1.1 | second, to nginx | 91%, at a third of its memory (438 against 1,304 MiB with 40,000 idle) |
| | h2 | first | +6% over nginx |
| W6, at 30,000 a second | HTTP/1.1 | second, to nginx | p99 2.31 ms to 2.27 |
| | h2 | first | p99 1.16 ms to nginx's 2.20 |
| W6, at 200,000 a second | HTTP/1.1 | second, to nginx | both keep up; p99 5.33 ms to 2.17 |
| | h2 | first | the only server that keeps up: 199,856 a second at p99 4.20 ms, nginx 156,921 |

In short:

- **First:** over h2c, h2 and HTTP/3 at every level of W0; over h2 in the API gateway, in idle and active,
  and at both fixed rates; over HTTPS/1.1 and h2 for the website; in every large transfer; and with ten
  requests a connection in W3.
- **Level with nginx:** over HTTP/1.1 for plain responses.
- **Second to nginx:** over HTTP/1.1 in the API gateway and in its latency, and for the website over HTTP/3.

## Against 11-05, server by server

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | -1.2% | +1.5% | +0.0% | 1 | 11 |
| nginx | 58 | -0.5% | +0.5% | +0.0% | 1 | 9 |
| Caddy | 52 | -0.6% | +0.6% | +1.3% | 0 | 1 |
| Envoy | 44 | -0.3% | +0.9% | -0.0% | 2 | 5 |
| Traefik | 20 | -0.8% | +0.7% | +2.0% | 0 | 0 |

fit's eleven rows below -3%:

- **Eight are W0's h2c and h2 at 500 to 10,000 in flight**, each at +3.1% to +5.2% CPU a request:

  | In flight | h2c | h2 |
  | ---: | ---: | ---: |
  | 500 | -3.0% | -3.1% |
  | 2,000 | -3.7% | -3.9% |
  | 5,000 | -4.9% | -3.2% |
  | 10,000 | -5.0% | -3.7% |

  At 100 in flight they lost -1.8% and -1.1%. Every earlier plan since 11-03d had them within about 1% of
  one another, so this is the release and not noise. HTTP/3 in the same tables lost 1.3-1.8%.
- **Three are within the ranges the plans have shown, or just below them:**
  - The API gateway over HTTP/1.1 at 2,000, -3.6%. It reached 263,902; it has been 260,710 to 273,843 since 11-03d.
  - The download over HTTP/1.1, -4.0%. It reached 17,736; the range is 17,696 to 18,899.
  - The website over HTTPS/1.1 at 500, -3.6%. It reached 99,197, a little under the 101,113 to 102,890 of
    the last five plans. That table's p99 and memory are unchanged.

fit's one row above +3% is the proxied download over HTTP/1.1, +10.6%: 16,346 against 14,773. It is the one
that comes out near 14.7k or 16.1k in turn.

nginx's nine rows below -3% are in tables that have always moved that much between plans. Most are over h2:
W0's h2 at 100 (-24%), the proxied h2 download (-19%), and its h2 at 200,000 a second (-6%).

## What changed since 11-05

The plan's fit and fib against 11-05's: fit baf933e..61b5a6c4, 97 commits; fib f5b63c7..v1.7.2, 68 commits.

**fib v1.7.1 and v1.7.2** came from the security review. Most of the changes bound memory or check input on
paths the plan does not run: gRPC, WebRTC, the media protocols, SOCKS, DTLS, and QUIC's out-of-order data. The
changes on the plan's paths:

- **HTTP/2** (dabe569, 46af102): each request's `:method`, `:scheme`, `:authority` and `:path` are checked
  byte by byte, and the bodies one connection collects whole are counted. These are I158's cost, the h2c and
  h2 loss in W0.
- **HTTP/1.1** (72294ce, 938f812, 678ab45): a bare LF in a head or trailer section and obs-fold are refused,
  chunk lines are bounded, and a body is scanned once.
- **TLS** (8492f33, 20eee13, 166e85f): a ticket's binder is checked before its client chain, what a handshake
  holds ahead of a ClientHello is bounded, and CBC MACs are summed in constant time. W3's churn, which is all
  handshakes, moved -0.3% to +0.1%.
- **HTTP/3** (223b562, d111519, 3198ae7): pseudo-headers are checked for their own syntax, a declared body
  length no longer reserves it all, and out-of-order data is held once.
- **A connection's own idle timeout** (27f5c88): new API that fit's console uses. The plan does not run
  the console.

**fit's security round after fib v1.7.1** made these changes on the plan's paths:

- Every route and handler goes by the request's canonical path (114dc5ec).
- A client's `Connection` field names only the fields it sent (44aaac54).
- An identity field spelled with `_` cannot pass for the one auth sets (bff8f08d). ef2a5750 made that check
  cheaper by listing the spellings once.
- A cached response to an authenticated request is shared only if it says so (787e565f).
- Dotfiles answer 404 (0d865694), and the static lookup cache is bounded by bytes (fcb77e9d).
- A refused upgrade's answer streams back (5fc520b4).

The rest are off the plan's paths:

- The Admin API over TLS, which it now needs off loopback.
- The tunnel's and the LLM mesh's links over TLS off loopback.
- The tunnel's and mesh's authentication.
- Bounds on on-demand TLS.
- RTSP authorization.
- Listeners' opt-in Multipath TCP, off by default.
- Log rotation (I155-I157).

Together they are the +1.5% CPU a request in fit's median, about +1% beyond the host's drift.

## What follows

- **I158, done on main and not released.** fib cb9bbbe brings fit's `fixed` and `static` over h2c back within
  noise of fib 5c0e5b9, the commit before v1.7.1 (`bench/ab.sh`, 2026-11-06). fit's main (35a700cc) requires
  it. A plan on that commit, or on the next release, should show W0's h2c and h2 back at 11-05's 3.0-3.5M a
  second.
- **The open items stand as at 11-05:** HTTP/1.1 in the API gateway, the website over HTTP/3, and HTTP/1.1
  latency at 200,000 a second. fit's p99 there, 5.33 ms against 4.80 on 11-05, moved with nginx's (1.68 to
  2.17 ms).
