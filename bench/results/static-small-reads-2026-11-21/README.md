# I230: small files read whole, an elastic file pool

2026-11-21, benchmark host 192.168.100.51 (Ryzen 7 7800X3D, 8 cores / 16 threads), over loopback,
the setup of [static-lookup-shards-2026-11-21](../static-lookup-shards-2026-11-21/README.md): server
on CPUs 0-3,8-11, wrk on 4-7,12-15, nginx 1.31.4 as there. Old: fit `8424dba0` (I229). New: the
I230 change. Three rounds alternating old / new / nginx, a fresh server for each run, 5 s warm-up,
20 s measured. Files: 2.1 M random 8 KiB files (`rand-N`, `rand.lua`), `hot.bin` (`hot-256`), and
20,000 random 64 KiB files under `/m` (`mid-256`, `mid.lua`), on a tmpfs of their own. Scripts:
`run2.sh` (this A/B), `run3.sh` (the short read, below), `exp.sh` (the pool experiments), `prof.sh`
(the profile that started it); every run in `summary.txt` and `*.wrk`.

## Where the CPU went (I229, rand-512, `profile/`)

System calls a request, from `perf stat` on the server's CPUs, divided by the requests:

| | I229 | I230 | nginx |
| --- | ---: | ---: | ---: |
| all | 17 | 7 (6 with the short read) | 7.7 |
| file | stat, lstat, openat, 4 fcntl, epoll_ctl, fstat, 3 lseek, 2 pread, close | lstat, openat, pread (2 before the short read), close | openat, fstat, sendfile, close |

`os.Open` makes four `fcntl` calls and an `epoll_ctl` that fails for every regular file (it tries
to make it pollable); `ServeContent` seeks three times and reads 512 bytes to sniff a type the
file's name does not give; `resolve` stats and then lstats. On this CPU a system call is dear
(`entry_SYSRETQ` alone was 10% of the server's CPU); 67% of fit's time was in the kernel.

## Throughput and latency (median of three rounds)

| Scenario | I229 QPS | I230 QPS | I230/I229 | nginx QPS | I230/nginx (I229) |
| --- | ---: | ---: | ---: | ---: | ---: |
| rand-256 | 220,040 | 257,024 | **+16.8%** | 325,743 | 78.9% (67.5%) |
| rand-512 | 221,695 | 258,576 | **+16.6%** | 326,059 | 79.3% (68.0%) |
| hot-256 | 578,667 | 577,703 | −0.2% | 479,231 | 121% (121%) |
| mid-256 | 194,915 | 210,858 | **+8.2%** | 257,795 (client bound) | 81.8% (75.6%) |

| Scenario | p50 I229 → I230 (nginx) | p90 | p99 |
| --- | --- | --- | --- |
| rand-256 | 1.07 → 0.90 ms (0.75) | 1.89 → 1.32 (0.94) | 4.36 → **2.38** (1.22) |
| rand-512 | 2.18 → 1.81 (1.50) | 3.69 → 2.62 (1.86) | 6.94 → **3.94** (2.17) |
| mid-256 | 1.12 → 1.07 (0.49) | 2.47 → 1.94 (0.92) | 6.12 → **3.36** (1.27) |

Rounds agree within 2.1%. CPU per request at rand-512: 35.8 → 30.0 µs (nginx 24.4). Peak RSS
244 → 272 MB at rand-512 (pooled 16 KiB read buffers held until their responses are sent); nginx
285 MB. No socket errors or non-2xx responses.

## The tail, and the pool (`exp/`, rand-256 / rand-512, one run each)

The small-file reads alone (first cut, the pool still 32 workers) gave 264k QPS but a p99 of
10.3-11.4 ms against I229's 4.4-6.9. Variants of that build:

| File pool | rand-256 QPS, p90, p99 | rand-512 QPS, p90, p99 |
| --- | --- | --- |
| 32 workers (4 × GOMAXPROCS) | 264,274, 2.25 ms, 10.34 ms | 264,611, 2.91 ms, 11.37 ms |
| 32, `Gosched` after each job | 260,626, 5.38, 10.77 | 257,719, 5.48, 11.82 |
| 16 workers | 262,720, 1.40, 7.39 | 261,051, 2.62, 5.98 |
| 8 workers (= GOMAXPROCS) | 259,525, 1.30, 2.80 | 258,933, 2.58, 4.56 |

An execution trace of the 32-worker build (`profile/fit-i230-pool32.sched.top.txt`) puts 68% of
the scheduling delay on file pool workers woken by `Submit` waiting to run (about 21 at a time) and
16% on fib's workers. With more runnable goroutines than CPUs no P is ever idle, and Go's runtime
then looks at its network poller only from sysmon, every 10 ms, so fib's event loops, parked there,
wait that long: the p99 at 10 ms. `Gosched` keeps the workers runnable and made it worse. The pool
now keeps GOMAXPROCS workers and adds one for each held in a job past 1 ms (blocked on a disk), up
to its bound: the final build above.

## The short read (`run3.sh`, two rounds)

Reading whole asked for one byte more than the stat's size and read again until a read returned
nothing: two `pread` a request. A short read of a regular file is its end, so the loop stops there.
The three-round build above against the same with that change (`shortread/`, two rounds each):

| Scenario | Before QPS (rounds) | After QPS (rounds) | Change | p99 before → after |
| --- | --- | --- | ---: | --- |
| rand-256 | 258,054 / 258,173 | 263,235 / 263,987 | **+2.1%** | 2.31-2.51 → 2.32-2.46 ms |
| rand-512 | 260,355 / 259,616 | 265,329 / 263,885 | **+1.8%** | 3.97-3.98 → 3.78-3.91 ms |

System calls a request 6.94 → 5.98 (nginx 7.65). With it, rand-512 is about 264.6k QPS: +19% on
I229, 81% of nginx.
