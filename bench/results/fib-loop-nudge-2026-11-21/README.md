# fib A99: workers wake a loop parked too long with events waiting

2026-11-21, benchmark host 192.168.100.51, the setup of
[static-fewer-allocs-2026-11-21](../static-fewer-allocs-2026-11-21/README.md) (server on CPUs
0-3,8-11, wrk on 4-7,12-15, port 8082). Main: fit `29492d8d` on fib `047fd40`. A99: the same fit
on fib `9dcfc6b`. Also a fib hello server (`hellosrv.go`, `Respond` of 6 bytes, fib's defaults)
on each fib. Three rounds alternating (`exp7.sh`), a fresh server each run, 5 s warm-up, 20 s
measured; every run in `summary.txt` and `*.wrk`.

**The change** (fib `core/poller_epoll.go`): a loop records when it parks in the runtime's poller;
a worker finishing a connection's round (`Connection.RunTask`) looks at the engine's loops, and one
parked past 100 µs, looked at at most once each 100 µs, whose epoll instance a zero-timeout `ppoll`
finds readable, is woken by passing a deadline to its relay's `Read`, which readies it on the
worker's P. Without it, with every P busy, a parked loop waited for the runtime's monitor, up to
10 ms.

## Medians of three rounds

| Scenario | QPS main → A99 | p50 | p90 | p99 (rounds) |
| --- | --- | --- | --- | --- |
| static rand-128 | 322,582 → 323,930 (+0.4%) | 0.378 → 0.377 ms | 0.550 → 0.457 | **8.34 → 4.78** (8.27-8.35 / 4.58-4.91) |
| static rand-256 | 323,811 → 325,466 (+0.5%) | 0.763 → 0.760 | 0.930 → 0.890 | **5.87 → 3.23** (5.74-6.67 / 2.73-3.57) |
| static rand-512 | 325,870 → 326,660 (+0.2%) | 1.53 → 1.52 | 1.78 → 1.78 | 2.90 → 3.04 (2.82-2.93 / 2.87-3.07) |
| static paced-360 | 225,823 → 228,162 (+1.0%) | 0.276 → 0.271 | 0.499 → 0.458 | 1.34 → 1.17 |
| static hot-256 | 578,351 → 581,168 (+0.5%) | 0.395 → 0.393 | 0.777 → 0.772 | 1.33 → 1.32 |
| static mid-256 | 210,907 → 211,450 (+0.3%) | 1.08 → 1.08 | 1.95 → 1.95 | 3.38 → 3.38 |
| hello keep-alive 64 | 833,757 → 836,078 (+0.3%) | 0.062 → 0.062 | 0.088 → 0.087 | 0.68 → 0.58 |
| hello keep-alive 256 | 873,800 → 869,749 (−0.5%) | 0.144 → 0.144 | 0.299 → 0.302 | 1.18 → 1.24 |
| hello keep-alive 4096 | 863,726 → 862,553 (−0.1%) | 2.34 → 2.34 | 2.62 → 2.60 | 6.04 → 5.89 |
| hello pipelined 256×16 | 5,841,820 → 5,865,405 (+0.4%) | 0.414 → 0.411 | 1.05 → 1.04 | 2.10 → 1.91 |

Server CPU equal within 0.1 of a CPU in every scenario. The hello keep-alive runs have the client
at 7.8-7.9 of its 8 CPUs, so their −0.5..+0.3% is the client's; keep-alive 256 was lower in all
three rounds (−0.3..−0.6%), under the 1% a change counts at. The p99 at 128 and 256 connections is
still above the 2.2-2.5 ms of fit `35b37690`, which wasted enough CPU to leave Ps idle: what is
left is loops that a worker reaches only after 100 µs and fib's workers waiting in the run queue.
