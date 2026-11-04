# Load test plan, 2026-11-04 (third run): the report

[English](report.md) | [简体中文](report.zh-CN.md)

The plan of [the second 2026-11-04 run](../plan-2026-11-04b/report.md) run again with fit on fib 60d67aa:
proxied HTTP/1.1 uploads moved from the client's socket to the upstream's by `splice(2)`. 232 runs on the
benchmark host, 2026-11-04 10:38 to 12:11 UTC (18:38 to 20:11 in the host's Asia/Hong_Kong): none failed,
none hung, no run had a single error. Tables in [tables.md](tables.md), the plan in [plan.txt](plan.txt),
the log in [plan.log](plan.log); the host as before, its LLM services stopped for the run. fit's go.mod
requires fib 60d67aa from 6337046.

## In short

- **Proxied uploads over HTTP/1.1 cost fit 35% less CPU:** 623.5 → 407.8 µs a request, 5.89 → 3.82 of its
  6 cores, peak memory 50 → 28 MiB, p99 24.6 → 17.2 ms. Throughput did not move (9,448 → 9,374 a second):
  **the upstream is now the limit**, its 4 CPUs at 3.94 cores.
- **The upstream pays more to read the body.** With splice nothing touches the body between the client's
  write and the upstream's read; the upstream, now the bottleneck, finds it cold at the end of a long
  receive queue, where before the proxy read it cold and the upstream read the proxy's fresh copy. At a
  fixed 7,000 a second, with nothing saturated, the upstream's CPU a request rose about 5% (196 → 206 µs),
  mostly from the 4 KiB pieces a socket-to-pipe splice delivers; saturated, about 45%. Together fit and
  its upstream spend about 9% less a request (786 against 861 µs). `splice_uploads` stays on by default,
  its documentation now says what it costs the upstream, and `false` turns it off.
- **Nothing else moved:** fit's medians against the second 11-04 run +0.2% throughput, -0.2% CPU a
  request, -2.6% peak memory; the other servers' -0.0% to +0.5%. W2's cache over HTTPS/1.1 at 2,000
  (-4.6%, never spliced, TLS) has ranged 96-102k a second between plans.

## The change

fib 1262648, 60d67aa ([A62](../../../docs/fib-api-requests.md)): `Connection.SpliceTo` moves the next n
bytes that arrive on a connection to another's socket through a pipe, on Linux, plain TCP on both sides;
`Context.SpliceBody` offers a streamed HTTP/1 body framed by Content-Length to fib's client, which takes it
on an HTTP/1 connection it can splice to, or else has the caller push the body as before. The bytes the
server had read already go first, then exactly the rest of the body; a pipelined next request is not
touched. Pipes are pooled per poller, 128 KiB, at most 256 open. fit 4f6dbf4 offers uploads of 128 KB or
more with a length and no trailers.

Tried to cut the upstream's cost, none of which did (one profiled run each): `SPLICE_F_MORE`; handing the
upstream 64 KiB at a time; smaller receive and send buffers on the proxy's sockets (worse). Segment sizes
and wake-ups at the upstream were the same with splice and without, and its reads fewer and larger (2.9 a
request of 512 KiB-1 MiB against 8.5): the cost is the copy of the same bytes, cold.

| W4 proxy-upload, HTTP/1.1, 100 in flight | fit req/s | fit µs a request | fit cores | upstream cores |
| --- | ---: | ---: | ---: | ---: |
| 11-04b, copied | 9,448 | 623.5 | 5.89 | 2.75 |
| 11-04c, spliced | 9,374 | 407.8 | 3.82 | 3.94 |
| Caddy, 11-04c | 9,955 | 579.7 | 5.77 | 2.06 |

## Against the second 2026-11-04 run, server by server

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | +0.2% | -0.2% | -2.6% | 2 | 1 |
| nginx | 58 | +0.3% | -0.5% | +0.0% | 4 | 4 |
| Caddy | 52 | +0.3% | -0.4% | +0.9% | 0 | 0 |
| Envoy | 44 | +0.5% | -0.6% | +0.3% | 5 | 3 |
| Traefik | 20 | +0.0% | -0.2% | -0.0% | 0 | 0 |
