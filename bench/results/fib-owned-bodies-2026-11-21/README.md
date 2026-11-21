# fib A100 and A101: pooled bodies go back to the pool once sent

2026-11-21, benchmark host 192.168.100.51, the setup of
[fib-loop-nudge-2026-11-21](../fib-loop-nudge-2026-11-21/README.md) (fit on CPUs 0-3,8-11, wrk on
4-7,12-15, port 8082). A: fit `99181244` on fib `3354267`. B: the same fit on fib `415e755`
(A100 `2db63ea`, A101 `415e755`). C: B with the static handler's read files sent with
`WriteResponseOwned` instead of `WriteResponseCopy` (not committed; see below). Proxy scenarios:
`proxy.conf`, nginx 1.31.4 as the backend on port 9090 (`nginx-backend.conf`, 4 workers, on the
client's CPUs) serving one 8 KiB and one 32 KiB file. Three rounds alternating (`exp8.sh`), a fresh
server each run, 5 s warm-up, 20 s measured, `GODEBUG=gctrace=1` counting collections.

**The changes.** A100: `WriteResponseOwned` on HTTP/1 let a body above 4 KiB, which goes out by
reference, go to the collector; core's `SendPartsOwned` now queues it as the whole buffer, marked
owned, and gives it back once sent or dropped. A101: fib's client copied a whole body above 4 KiB
into a buffer of its own size, so fit's proxy got `pooled=false` from `TakeBody` and wrote it with
`WriteResponse`; the client now pools bodies up to 1 MiB, and `BodyBytes` hands out a copy of its
own size above 4 KiB. A100 alone (one round, `a100-alone-round1.txt`, where B was fib A100 without A101) left the
proxy's collections unchanged (2705 → 2608 in 20 s at 8 KiB): the garbage was the client's copy.

## Medians of three rounds

| Scenario | QPS A → B (C) | collections/s | p90 | p99 (rounds) | CPU/request |
| --- | --- | --- | --- | --- | --- |
| proxy 8 KiB | 230,110 → 240,327 (+4.4%) | 132.9 → 17.4 | 1.64 → 1.26 ms | 4.00 → 3.30 (3.11-3.31) | 33.9 → 31.9 µs |
| proxy 32 KiB | 181,604 → 217,517 (+19.8%) | 151.7 → 7.0 | 2.25 → 1.40 | 4.76 → 3.03 (2.83-3.10) | 43.0 → 36.2 |
| static rand-256 | 323,962 → 325,629 (+0.5%; C +1.8%) | 3.0 | 0.89 | 2.74 → 2.67 (C 3.70: 2.94-4.01) | 24.2 → 24.1 (C 23.8) |
| static rand-512 | 325,847 → 327,334 (+0.5%; C +1.7%) | 3.0 | 1.78 → 1.77 | 3.10 → 3.21 (C 3.32) | 24.1 → 24.0 (C 23.7) |

Rounds agree within 0.5%. No socket errors or non-2xx responses.

**C not taken.** The static handler sending its read files with `WriteResponseOwned` saves the
copy `WriteResponseCopy` makes: +1.7-1.8% at saturation, but the p99 at 256 connections rose in
every round (2.74 → 3.70 ms median). Mixed, so the handler keeps `WriteResponseCopy`.
