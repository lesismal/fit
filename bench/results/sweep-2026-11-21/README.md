# Parameter sweep of the static handler against nginx

2026-11-21, benchmark host 192.168.100.51 (Ryzen 7 7800X3D, 8 cores / 16 threads), over loopback.
fit `d16c696c` (fib `415e755`) against nginx 1.31.4, as in
[fib-owned-bodies-2026-11-21](../fib-owned-bodies-2026-11-21/README.md): server on CPUs 0-3,8-11
unless a point says otherwise, wrk (or h2load for h2c) on 4-7,12-15. `sweep.sh`; two rounds of
15 s after 5 s of warm-up, a fresh server for each; medians in `table.txt` (`sweep-sum.py
summary.txt`), every run in `*.out`. Random files are a set of their own per point, larger than
fit's caches (200,000 files, or 2 GiB, of one size), drawn at random by each wrk thread.

| Point | fit QPS | nginx QPS | fit/nginx | p99 fit / nginx | server CPUs fit / nginx |
| --- | ---: | ---: | ---: | --- | --- |
| 8 KiB, 16 connections | 275,032 | 259,514 | 106.0% | 0.34 / 0.12 ms | 6.99 / 6.12 |
| 64 | 318,899 | 338,535 | 94.2% | 2.43 / 0.37 | 7.61 / 7.94 |
| 256 | 329,496 | 335,040 | 98.3% | 2.30 / 1.16 | 7.82 / 7.92 |
| 1024 | 329,861 | 333,564 | 98.9% | 5.79 / 3.95 | 7.80 / 7.90 |
| 4096 | 323,490 | 323,055 | 100.1% | 19.62 / 14.18 | 7.83 / 7.88 |
| HTTPS/1.1, 8 KiB random, 256 | 289,304 | 245,649 | 117.8% | 2.76 / 1.39 | 7.81 / 7.93 |
| HTTPS/1.1, one 8 KiB file | 470,004 | 356,841 | 131.7% | 1.86 / 1.15 | 7.89 / 7.92 |
| h2c, 8 KiB random, 256×1 | 379,723 | 375,654 | 101.1% | mean 0.57 / 0.43 | 7.85 / 7.74 |
| h2c, one 8 KiB file, 64×16 | 1,887,329 | 385,994 | 489.0% | mean 0.44 / 2.66 | 7.92 / 7.93 |
| 2 server CPUs (0,8) | 86,607 | 85,675 | 101.1% | 4.08 / 3.30 | 1.96 / 1.98 |
| 4 server CPUs (0-1,8-9) | 160,199 | 170,505 | 94.0% | 2.58 / 1.79 | 3.67 / 3.96 |
| 1 KiB | 446,117 | 419,032 | 106.5% | 1.40 / 0.82 | 7.78 / 7.92 |
| 4 KiB | 348,127 | 291,643 | 119.4% | 1.58 / 1.11 | 7.80 / 7.94 |
| 8 KiB | 329,483 | 334,469 | 98.5% | 2.36 / 1.17 | 7.82 / 7.92 |
| 12 KiB | 314,082 | 335,844 | 93.5% | 2.78 / 1.22 | 7.80 / 7.91 |
| 16 KiB | 301,634 | 332,490 | 90.7% | 3.23 / 1.24 | 7.79 / 7.89 |
| **17 KiB** | **231,346** | 330,552 | **70.0%** | 2.87 / 1.25 | 7.88 / 7.91 |
| 20 KiB | 231,928 | 333,078 | 69.6% | 2.85 / 1.36 | 7.88 / 7.82 |
| 32 KiB | 229,347 | 325,511 | 70.5% | 2.92 / 1.25 | 7.85 / 7.91 |
| 64 KiB | 201,403 | 242,217 | 83.1% | 3.50 / 1.38 | 7.80 / 5.88 |
| 128 KiB | 148,959 | 153,452 | 97.1% | 3.44 / 1.81 | 6.79 / 4.72 |
| 512 KiB | 41,694 | 37,695 | 110.6% | 6.33 / 6.76 | 4.31 / 2.29 |
| 1 MiB | 19,222 | 17,053 | 112.7% | 13.39 / 14.94 | 3.12 / 1.75 |

Rounds agree within 1-2% at every point but nginx's 16 connections (250k and 269k).

## What it shows

1. **A cliff past 16 KiB.** Up to 16 KiB fit reads a file whole (open, pread, close); above it, it
   goes through `ServeContent` and fib's `SendFile`. 16 → 17 KiB costs fit 23% of its throughput,
   nginx nothing: fit serves 17-32 KiB files at 70% of nginx. Above 64 KiB both are bound by the
   bytes, and fit uses more CPU for them (128 KiB: 6.79 against 4.72 CPUs).
2. **The read-whole path loses ground with size**: 98.5% of nginx at 8 KiB, 90.7% at 16 KiB, the
   copies into the buffer and into fib's queue growing with the file.
3. **The tail at every connection count** is longer than nginx's, by the most at 64 connections
   (2.43 against 0.37 ms) where nginx still has a little CPU to spare and fit does not.
4. **Four CPUs**: 94% of nginx with 0.33 of a CPU idle; fib's default of one event loop for every
   four CPUs leaves one loop there.
5. Where fit leads: small files (1 and 4 KiB), HTTPS (Go's TLS against OpenSSL, +18-32%), h2c with
   multiplexing (4.9 times), and the largest files.

Not covered: real disks (tmpfs only, so files are always in memory), machines with more CPUs,
HTTP/3, Range and conditional requests.
