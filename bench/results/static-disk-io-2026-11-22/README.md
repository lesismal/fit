# disk_io: the file pool's hand-over, and where the rest of the gap stands

2026-11-22, benchmark host 192.168.100.51 (Ryzen 7 7800X3D, 8 cores / 16 threads), over loopback,
the setup of [static-open-stat-2026-11-22](../static-open-stat-2026-11-22/README.md): server on
CPUs 0-3,8-11, wrk on 4-7,12-15, 256 connections, random files of one size, more than fit's caches
hold, `default_type application/octet-stream` in fit (`fit-dt.conf`) as in nginx's config. Three
rounds of 15 s after 5 s of warm-up unless said otherwise, a fresh server for each; medians in each
directory's `table.txt`, every run in `*.out`. "Paced" runs (`sweep-paced.lua`) wait 1 ms after each
response, which keeps the load below what the server can serve: 512 connections is nine tenths of
it, 256 half; their lines are in `paced.txt`.

`fit-all` is fit as I234 left it (2707b1e5), every request that needs the disk on the file pool.

## The hand-over to the pool

What I234 left of the 20-32 KiB gap was the hand-over: a fib worker queues the request for the file
pool, whose goroutine wakes and serves it. Serving it on fib's worker instead, as nginx serves a
file in its event loop, was 4% at 20 KiB in I234's `inline_disk`, and the event loops then park
far less often: `epoll_pwait` and `epoll_ctl` 0.08 a request, against 0.78.

**On fib's worker for every request** (`auto/`, `fit-auto`, the first build, bounded at the CPUs):

| Size | fit-all | fit-auto | Change | nginx | p99 (ms) all / auto / nginx |
| --- | ---: | ---: | ---: | ---: | --- |
| 8 KiB | 357,342 | 352,541 | −1.3% | 335,974 | 2.07 / 1.97 / 1.14 |
| 16 KiB | 326,314 | 324,515 | −0.6% | 330,669 | 2.28 / 2.09 / 1.78 |
| 20 KiB | 316,917 | 330,148 | +4.2% | 334,252 | 2.37 / 3.53 / 1.22 |
| 32 KiB | 301,419 | 308,099 | +2.2% | 315,980 | 2.99 / 3.31 / 2.64 |
| 64 KiB | 236,409 | 238,544 | +0.9% | 243,297 | 2.79 / 3.01 / 1.36 |

At saturation the 99th percentile at 20 KiB went half as long again: with every P busy the runtime
seldom looks at its poller, where fib's event loops are parked (fib's nudge of a loop parked past
100 µs bounds it, not more). Paced, the percentile was level within 3-5%.

**How many at once** (`imax/`, `fit-autox-N` with N on fib's workers at most, 2 rounds, 20 KiB):

| Bound | Saturated | p99 | Paced, 512 connections | p99 |
| --- | ---: | ---: | ---: | ---: |
| 0 (fit-all) | 316,670 | 2.45 ms | 301,339 | 2.32 ms |
| 2 | 324,732 (+2.5%) | 2.58 ms | 317,546 (+5.4%) | 2.34 ms |
| 4 | 328,855 (+3.9%) | 2.99 ms | 314,289 (+4.3%) | 2.40 ms |
| 8 | 332,934 (+5.1%) | 3.53 ms | 310,450 (+3.0%) | 2.36 ms |

Each step up buys throughput at saturation for a longer tail; near saturation the smallest bound
served most. **auto's bound is a quarter of the CPUs.**

## As committed (`auto2/`)

`fit-auto2` is the change with `disk_io` left out (auto, a bound of 2 here), which also makes a
lookup the cache does not hold one allocation with its stat and its file, and looks at the
contents the cache holds once rather than twice for a file it does not hold; `fit-auto2-inline` the
same with `disk_io inline` (`fit-dt-inline.conf`).

| Size | fit-all | auto | Change | inline | nginx | auto of nginx | inline of nginx |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 8 KiB | 357,663 | 361,118 | +1.0% | 354,735 | 335,695 | 107.6% | 105.7% |
| 16 KiB | 328,341 | 332,626 | +1.3% | 326,987 | 329,507 | 100.9% | 99.2% |
| 20 KiB | 316,193 | 324,560 | +2.6% | 332,743 | 334,519 | 97.0% | 99.5% |
| 32 KiB | 309,018 | 315,734 | +2.2% | 321,336 | 326,107 | 96.8% | 98.5% |
| 64 KiB | 230,638 | 233,138 | +1.1% | 233,920 | 244,406 | 95.4% | 95.7% |

p99 (ms), fit-all / auto / inline / nginx: 8 KiB 1.93 / 2.15 / 1.97 / 1.15, 20 KiB 2.34 / 2.56 /
8.46 / 1.23, 32 KiB 2.70 / 2.89 / 5.09 / 1.38, 64 KiB 3.13 / 3.52 / 3.55 / 1.93.

Paced at 20 KiB, 512 connections, two rounds: fit-all 283,387, auto 291,119 (+2.7%, 98.3% of
nginx), inline 291,147, nginx 296,238; p99 2.25 / 2.47 / 2.41 / 2.86 ms.

System calls a request at 20 KiB: fit-all 6.56, auto 6.17, inline 5.82, nginx 7.39.

## What is left

By default 20-32 KiB is 3% below nginx at saturation and under 2% at nine tenths of it; with
`disk_io inline`, 0.5-1.5% at saturation with the tail much longer. fit now makes fewer system
calls a request than nginx and spends less time in the kernel; what remains is Go's: a goroutine a
request, the scheduler's look at its poller, the collector (a GC every 150 ms at this rate), and
fib's header serialization (2%).

## Files

`auto.sh`, `imax.sh`, `auto2.sh` (the A/Bs; the first round at 20 KiB also counts system calls by
name, `*.syscalls.csv`), `sweep-paced.lua`, `fit-dt.conf`, `fit-dt-inline.conf`, `cliff-sum.py`.
