# I229: the static handler's lookup cache in shards, with a doorkeeper

2026-11-21, benchmark host 192.168.100.51 (Ryzen 7 7800X3D, 8 cores / 16 threads, 61 GiB), over
loopback. Server on CPUs 0-3,8-11 (8 threads, GOMAXPROCS 8), wrk 4.2.0 (`wrk:local`, 8 threads) on
4-7,12-15. Old: fit `4d507110` (main). New: the I229 change. nginx 1.31.4 (docker, host network,
8 workers, `sendfile on`, `tcp_nopush on`, `open_file_cache max=16384 inactive=20s`, valid 1s,
`keepalive_requests` 10^8). Each run on a server started fresh: 5 s warm-up, 20 s measured, three
rounds alternating old / new / nginx.

Files: 2,099,200 random files of 8 KiB (2560 directories of 820, 16 GiB) on a tmpfs of their own,
plus `hot.bin`. `rand-N`: N connections, each wrk thread drawing files at random with its own seed
(`rand.lua`); `hot-N`: N connections all asking for `hot.bin`. Server CPU is the busy share of the
server's 8 CPUs from `/proc/stat`. Scripts: `run.sh`, `gen.go`, `fit.conf`, `nginx.conf`; every run
in `summary.txt` and `*.wrk`.

## Throughput (median of three rounds)

| Scenario | Old QPS | New QPS | New/old | nginx QPS | New/nginx (old/nginx) |
| --- | ---: | ---: | ---: | ---: | ---: |
| rand-256 | 114,190 | 218,790 | **1.92x** | 329,048 | 66.5% (34.7%) |
| rand-512 | 113,157 | 220,229 | **1.95x** | 325,678 | 67.6% (34.7%) |
| hot-256 | 580,353 | 578,924 | −0.2% (spread of rounds) | 473,543 | 122% (123%) |

Rounds agree within 1.2%. At rand-512: server CPU old 5.28-5.32 of 8, new 7.93, nginx 7.93; CPU
time per request old 46.9 µs, new 36.0 µs, nginx 24.3 µs. Latency at rand-512: p50 4.29 → 2.19 ms,
p90 5.30 → 3.72 ms, p99 6.84 → 6.90 ms (unchanged); nginx 1.54 / 1.90 / 2.23 ms. Peak RSS about
250 MB for both builds. No socket errors or non-2xx responses in any run.

## Lock waits and holds (rand-512, `-tags fit_lockstat`, cumulative to 20 s of the run)

| Build | Write locks of the lookup cache | Hold mean / p99 / max | Wait mean / p50 / p90 / p99 |
| --- | ---: | --- | --- |
| Old (one lock) | 2,177,427 | 1.11 µs / ≤2 µs / 193 µs | 261 µs / ≤4 µs / ≤1.05 ms / ≤4.2 ms |
| New (64 shards) | 125,421 | 1.03 µs / ≤2 µs / 371 µs | 1.4 µs / ≤63 ns / ≤63 ns / ≤127 ns |

The lock was held briefly; the time went to waiting in line for it, which left the old build's
CPUs idle. The doorkeeper keeps about 6% of first sightings (its false positives, bounded at 1/16), so
17x fewer write locks, each on one of 64 shards. The instrumented builds ran within 1% of the plain
ones (112,440 and 221,520 QPS). Quantiles are upper bounds of power-of-two buckets.

What is left against nginx is CPU per request: the hand-off to the file pool, and an open, fstat,
pread and close for every request (8 KiB is below fib's 16 KiB sendfile threshold), against nginx's
sendfile from its cached descriptor.
