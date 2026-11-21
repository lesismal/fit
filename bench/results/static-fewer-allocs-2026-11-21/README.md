# I231: no garbage for a file read whole, a header block, fib's Watch without a lock

2026-11-21, benchmark host 192.168.100.51, the setup of
[static-small-reads-2026-11-21](../static-small-reads-2026-11-21/README.md) (server on CPUs
0-3,8-11, wrk on 4-7,12-15). Main: fit `35b37690` (I230). I231: this change, on fib `047fd40`.
Three rounds alternating main / I231 (`run4.sh`), a fresh server each run, 5 s warm-up, 20 s
measured; nginx's numbers are I230's run. `run4.sh` served on port 8090; the experiments after it on
8082 (see *Port 8090*).

## Where the garbage came from (I230, rand-256, `profile/fit-i230.allocs.top.txt`)

42 collections a second, about 4 GB/s allocated, 16 KB a request: 91.6% of the bytes were
`bufferpool.Get` in the small-file read. `WriteResponseOwned` hands a body larger than 4 KiB to
HTTP/1's queue by reference and leaves it to the collector rather than the pool, so every 8 KiB file
cost a new 16 KiB buffer. By count, `fileHeader` made 6 objects a request (the map, its slices,
Last-Modified and ETag strings), `resolve` cleaned `"/" + name` (a copy and a cleaned copy of every
path, which starts with "/" already).

I231: `WriteResponseCopy`, whose pooled copy the send path gives back, and the read buffer back to
the pool at once; the header as a `HeaderBlock` built in a pooled buffer; `resolve` cleans the path as
it is. fib `047fd40`: `Watch` of a watched directory without the Watcher's mutex.

## Throughput (median of three rounds)

| Scenario | Main QPS | I231 QPS | I231/main | I231/nginx (main) |
| --- | ---: | ---: | ---: | ---: |
| rand-256 | 262,808 | 324,501 | **+23.5%** | 99.6% (80.7%) |
| rand-512 | 265,439 | 327,360 | **+23.3%** | 100.4% (81.4%) |
| hot-256 | 578,853 | 580,369 | +0.3% | 121% |
| mid-256 | 209,817 | 211,472 | +0.8% | 82% (nginx client bound) |

Rounds agree within 1.1%. CPU a request at rand-512 29.3 → 24.0 µs (nginx 24.4). Collections 42 → 3
a second (`profile/gctrace-tail.txt`); RSS 271 → 234 MB at rand-512. System calls a request 6.0 →
5.8.

## Latency

At saturation (closed loop, as above):

| Scenario | p50 main → I231 | p90 | p99 |
| --- | --- | --- | --- |
| rand-256 | 0.88 → 0.76 ms | 1.29 → 0.92 | 2.45 → **5.60** (5.22-5.92) |
| rand-512 | 1.77 → 1.52 | 2.56 → 1.77 | 3.83 → 2.86 |
| rand-128 (`exp/exp5`) | 0.43 → 0.38 | 0.66 → 0.55 | 2.1-2.4 → **7.8-8.4** |

Below saturation (`exp6.sh`: 360 connections, each waiting 1 ms after a response, `paced.lua`,
about 226k requests a second for every build, two rounds):

| Build | p50 | p75 | p90 | p99 | max |
| --- | --- | --- | --- | --- | --- |
| main | 0.37-0.38 ms | 0.66-0.68 | 1.05-1.07 | 1.65-1.66 | 7.5-11.0 |
| I231 | **0.28** | **0.34** | **0.49-0.51** | **1.23-1.38** | 6.2-7.6 |
| I231 on fib's old `Watch` | 0.28 | 0.34 | 0.50 | 1.30-1.32 | 5.2-7.4 |

## The tail at saturation (`exp/`)

| Run (rand-128 / rand-256, p99 in ms, two rounds) | QPS | 128 | 256 |
| --- | ---: | --- | --- |
| main | 260k | 2.1-2.4 | 2.3-2.5 |
| I231 | 323k | 7.8-8.6 | 5.2-6.6 |
| I231, GOGC=off / GOGC=400 (`exp3`) | 323-327k | — | 6.1-6.9 / 5.3-6.0 |
| a: I231 on the old fib (`exp4`) | 320k | 6.4 | 2.5-3.6 |
| b: I231 with `WriteResponseOwned` | 276k | 6.5-6.8 | 3.3 |
| c: I231, a file pool that never grows | 320k | 8.6-8.8 | 6.1-6.5 |
| d: I231 with the header map (`exp5`) | 308k | 5.9-7.0 | 3.5-3.6 |
| I231, `loop_wait block` (`exp2`) | 297-299k | — | 7.7 (8.9 at 512) |

No one change makes it: not the collector, not the copy, not the pool's growth, not the block. Every
build past about 300k requests a second has it, at 128 and 256 connections but not 512, where every
loop always has events. Execution traces (`trace2.sh`, `profile/*.sched.top.txt`) show the delay at
fib's workers woken by `taskpool.kick` (52%), loops resuming from `runReady`'s Gosched (25%) and the
file pool's `Submit` (21%). A loop that finds no event parks in Go's network poller, which the
runtime looks at when a P runs out of work, or every 10 ms from sysmon; a server this efficient
leaves no P idle at saturation. The slower builds' wasted CPU (and the old `Watch` mutex, which made
pool workers park) left Ps idle. `loop_wait block` wakes the loop in its thread, which then waits for
a P, and is worse. Below saturation I231 is better everywhere. Requested of fib as A99.

## Port 8090

.51 keeps HttpArena's `ip_local_port_range` of 1024-65535, and 8090 is not among its
`ip_local_reserved_ports`. The readiness probe (`curl` to 127.0.0.1:8090 before the server listened)
then now and then drew 8090 as its own port and connected to itself (a socket `127.0.0.1:8090 →
127.0.0.1:8090` in TIME-WAIT), and the server could not bind: the runs that failed with "TCP :8090
is taken". The experiments moved to 8082, which is reserved.
