# Load test plan, 2026-10-27: the report

[English](report.md) | [简体中文](report.zh-CN.md)

nginx, Caddy, Traefik, Envoy and fit under the seven workloads of [the load test
plan](../../README.md#the-load-test-plan), at 100 to 10,000 requests in flight, the server under test on 6
CPUs, the backends on 4 and the load generators on 6 threads: the first run of the plan with the CPU
split that reserves what the load generators and the backends need and gives the server every core
left. 232 runs on the benchmark host, 2026-10-26 18:51 to 20:23 UTC (2026-10-27 02:51 to 04:23 in the
host's Asia/Hong_Kong): none failed, none hung, and no run had a single error. The 232 runs took
1 h 32 min against the 1 h 33 min estimated. Every table is in
[tables.md](tables.md), rendered from the runs' JSON in this folder, one file for each workload and
level; the plan derived is [plan.txt](plan.txt) (and `plan-derived.json`, which every run's
`params.derived` holds too).

## In short

- **The split gives the server every core the others do not need.** On the host's 8 cores (16
  threads) it gives 3 cores (6 CPUs) to the server under test, 2 (4 CPUs) to the backends and 3 (6
  threads) to the load generators: the server up from 2 cores (4 CPUs), the backends down from 3
  cores (6 CPUs), which used at most 2.18 before, and the load generators unchanged. `fitbench
  plan` derives the reservations from the top level: a load generator thread for each 1,667 requests
  in flight, a backend CPU for each 4,000, and every core left to the server.
- **The extra CPUs are used.** At the same levels as the 2026-10-24 plan, throughput rises by a
  median of 47% in W0, 49% in W1, 50% in W2, 31% in W3, 51% in W4 and 49% in W5, and the servers
  averaged 5.4-6.0 of their 6 CPUs in W0-W2, W4 and W5. Throughput per CPU is within 1% of the
  4-CPU plan (median -0.8%), so 50% more CPUs buy about 50% more work: the change measures the new
  6-CPU reference, not a speedup of fit.
- **Plain responses (W0):** nginx and fit are nearly tied over HTTP/1.1 (675k and 671k a second at
  100 in flight, 620k and 616k at 10,000); fit leads h2c (3.46M) and h2 (3.29M), nginx leads HTTP/3
  (1.92M, fit 1.45M); fit uses the least memory at 10,000 in flight (45, 75, 80 and 154 MiB over
  HTTP/1.1, h2c, h2 and HTTP/3, against nginx's 175, 174, 191 and 534).
- **API gateway (W1):** nginx leads (300k over HTTP/1.1 at 100, 202k at 10,000; 247k over h2 at 500,
  201k at 10,000); fit is second (225k and 163k over HTTP/1.1, 207k and 176k over h2), at 71-81% of
  nginx over HTTP/1.1 and 84-90% over h2, against 64-79% and 65-75% before.
- **Website (W2):** fit leads serving files (92k over HTTPS/1.1 and 101k over h2 at 500, against
  nginx's 82k and 67k) and as a cache (132k and 134k at 500, 95k and 122k at 2,000); nginx leads
  HTTP/3 (130k site, 114k edge at 500), fit reaching 48-77% of it.
- **Connection churn (W3):** a TLS handshake for every request puts every server at about 19k a
  second (the handshake sets the pace, not the server); one every ten requests puts fit ahead
  (132-137k), then nginx (129-134k), Envoy (117-133k) and Caddy (118-124k).
- **1 MiB transfers (W4):** fit's weakest results before are now its strongest: first on downloads
  (17.4k over HTTP/1.1, 11.6k over h2), first on proxied downloads over h2 (8.8k), second over
  HTTP/1.1 (12.8k), and level with nginx on proxied uploads over h2 (6.8k); it rises 22-42% on
  downloads, which the load generators limit, and 84-183% on proxied transfers.
- **40,000 idle TLS connections (W5):** every server kept all 40,000; nginx leads at 298k and 249k
  a second, fit is second at 215k and 198k in 519-530 MiB, Envoy 858 MiB, nginx 1,304-1,311 MiB, Caddy and
  Traefik 2,252-2,345 MiB.
- **Latency at a fixed rate (W6):** the calibration raises the rates from 20k and 100k to 30k and
  200k a second. At 30k every server keeps the rate (p99: nginx 1.69 and 1.58 ms, fit 2.73 and
  1.92); at 200k nginx and fit keep it (199.9k and 196.5k for nginx, 199.9k and 199.7k for fit;
  p99 2.49 and 3.00 ms, 5.85 and 5.85 ms), while Envoy, Traefik and Caddy fall to 58-114k.
- **The backends did not set the pace.** They used at most 3.07 of their 4 CPUs (W1 at 100, nginx
  over HTTP/1.1), 2.99 in W5 and 2.94 in W4; before, 6 CPUs peaked at 2.18. The margin is smaller
  but still enough.

## The test environment

The machine, the servers and the load generators are those of the earlier plan, but for fit's
version and the CPU split:

| | |
| --- | --- |
| Machine | AMD Ryzen 7 7800X3D, 8 cores of 2 threads (CPUs n and n+8 share core n), up to 5.05 GHz, 62 GiB; Ubuntu 24.04.5, Linux 7.0.0-38-generic, bare metal; every container on the host's network |
| Kernel settings | `somaxconn` and `tcp_max_syn_backlog` 2,048; `rmem_max`, `wmem_max` 7,500,000 (re-applied before every run); `tcp_rmem` 4096 131072 6291456; `tcp_wmem` 4096 16384 4194304; local ports 1024-65535; `nr_open` 2,000,500; `file-max` 2,000,500; `tcp_mem` pressure from 1 GiB |
| CPUs | the server under test `0-2,8-10` (6 workers), the backends `3-4,11-12` (4 CPUs), the load generators `5-7,13-15` (6 threads) |
| Servers | nginx 1.31.4, Caddy 2.10.2, Traefik 3.5.0, Envoy 1.40.0, at the same image digests as the earlier run |
| fit | c08eb0d (fib 33a039f1, Go 1.27.3); it reads `c08eb0d+dirty`, `+dirty` being the harness's uncommitted changes, whose only effect on the binary is its version string |
| Load generators | wrk 4.1.0; h2load 1.59.0, and 1.68.0 with HTTP/3 |

The host's `/etc/sysctl.d/99-fib-test.conf`, set after the earlier plan, had raised `somaxconn` and
`tcp_max_syn_backlog` to 32,768 and the TCP buffers to 16 MiB. For this run they were set back to the
earlier plan's values (the table above), the kind node the host also runs
(`fitconf-control-plane`) was stopped, and both were restored after it; the runs' `host` records
`somaxconn` 2,048 and `rmem_max` 7,500,000. `fs.file-max` is 2,000,500 now rather than unlimited,
which adds a top-level bound of 250,062, well above the 10,000 used.

## How the plan was derived

`fitbench plan` read the machine (`plan.txt` lists it) and set each of the plan's sizes from it:

| Setting | Derived | From |
| --- | --- | --- |
| CPU split | server 3 cores (6 CPUs), backends 2 (4 CPUs), load generators 3 (6 threads) | a load generator thread for each 1,667 requests in flight and a backend CPU for each 4,000, then every core left to the server, at least one; the load generators' reservation is capped at half the cores and the backends' at a quarter |
| Top level | 10,000 in flight | the least of what each input allows: the load generators 10,002 (6 threads at 1,667), `somaxconn` 10,240 (5 times it), the server's 6 CPUs 15,000, the backends 16,000, memory 31,715, TCP memory 32,768, ports 51,609, descriptors 262,144, `file-max` 250,062 |
| Levels | 100, 500, 2,000, 5,000, 10,000 | 100, 500, and a fifth and a half of the top; h2load 10 × 10, then 25 streams a connection |
| W2, W3, W4, W5, W6 | 500 and 2,000; 100 and 500 connections; 100; 500; 500 | the levels near 500; W4 bounded by TCP memory (128) |
| W5's idle connections | 40,000 | 4 for each request in flight at the top, within memory (249,722) and descriptors (523,288) |
| Aligned settings | backend connections and waiting requests 16,384, nginx 2,731 idle a worker, backlog 2,048, `worker_connections` 65,536 | half again the top, rounded up to a power of two; divided by the workers; `somaxconn` |
| W6's rates | 30,000 and 200,000 a second | calibrated from W1 at 500 (below) |

The split and the top level size each other: the top level sets the load generators' and backends'
reservations, and those reservations bound the top level. The plan starts from the machine's own
bounds and settles the two in a few rounds. The nearest bounds are now the load generators (10,002) and
`somaxconn` (10,240), not the server's CPUs (15,000): the load generators are what the plan is sized
to drive, while the server's 6 CPUs allow 15,000, 50% above the top level, and the results show
them used.

**W6's calibration.** Each server's ceiling is the least it answered over HTTP/1.1 and h2 in W1 at
500 in flight: Caddy 58,010, Traefik 68,937, Envoy 95,306, fit 207,144, nginx 247,042. The low rate is
half the slowest, 29,005, rounded to 30,000; the widest gap between two ceilings is Envoy's to fit's,
so the high rate is the roundest number between the slower side's most (Envoy over h2, 113,057) and
fit's 207,144: 200,000. The estimate before the calibration, 6 CPUs scaled against the earlier plan's
4 at 5,053 MHz, was also 30,000 and 200,000: 1.5 times the 4-CPU plan's 20,000 and twice its 100,000.

## What the tables show

The tables, their columns and their ranking are as in the earlier report's [What the tables
show](../plan-2026-10-24-auto/report.md#what-the-tables-show): TPS is successful responses a second,
CPU EER TPS for each core used, MEM EER TPS for each MiB of peak anonymous memory, ranked by [↓1],
[↓2], [↓3], each value with its share of the best. W0 and W1 merge their levels into columns; the
other workloads have a table for each level.

## The results

### W0, the raw ceiling

| Protocol | Best | Then | Memory at 10,000 |
| --- | --- | --- | --- |
| HTTP/1.1 | nginx 675k | fit 674k (99.8%), Envoy 259k, Caddy 258k | nginx 175, fit 45, Envoy 269, Caddy 307 MiB |
| h2c | fit 3.46M | nginx 492k (14%), Envoy 428k, Caddy 150k | fit 75, nginx 174, Envoy 113, Caddy 173 MiB |
| h2 | fit 3.29M | nginx 717k (22%), Envoy 430k, Caddy 157k | fit 80, nginx 191, Envoy 119, Caddy 190 MiB |
| HTTP/3 | nginx 1.92M | fit 1.45M (75%), Caddy 216k, Envoy 162k | nginx 534, fit 154, Caddy 297 MiB, Envoy 3,555 MiB |

nginx and fit hold their HTTP/1.1 throughput from 100 to 10,000 in flight (675k to 620k, 671k to
616k); the others fall off more (Envoy 253k to 167k, Caddy 248k to 221k). Fit's h2c and h2 rise with
the streams in flight (1.50M and 1.36M at 10 × 10, 3.46M and 3.29M at 400 × 25), and its h2c rises
only 0.7% from 5,000 to 10,000 in flight while its CPU stays at 5.95 of 6: the load generators are
near their limit there. Fit's HTTP/3 peaks at 500 in flight (1.45M) and then falls to 1.21M at
10,000, as before. Fit uses the least memory at 10,000 in flight; Envoy's HTTP/3 memory still grows to
3,555 MiB at 10,000.

### W1, the API gateway

| Protocol | 100 | 500 | 2,000 | 5,000 | 10,000 |
| --- | --- | --- | --- | --- | --- |
| HTTP/1.1 | nginx 300k, fit 216k, Traefik 98k, Envoy 88k, Caddy 73k | nginx 300k, fit 225k | nginx 296k, fit 209k | nginx 257k, fit 183k | nginx 202k, fit 163k, Traefik 75k, Envoy 63k, Caddy 54k |
| h2 | nginx 211k, fit 190k, Envoy 107k, Traefik 67k, Caddy 56k | nginx 247k, fit 207k | nginx 248k, fit 207k | nginx 231k, fit 194k | nginx 201k, fit 176k, Envoy 80k, Traefik 60k, Caddy 48k |

nginx leads at every level; fit is second at 71-81% of it over HTTP/1.1 and 84-90% over h2, up from
64-79% and 65-75%. The others are 1.5 to 4 times behind fit. Peak memory at 10,000 over HTTP/1.1:
nginx 263 MiB, fit 1,310 MiB, Envoy 607, Caddy 985, Traefik 2,160 MiB; over h2 nginx 404 MiB, fit 981,
Envoy 498, Caddy 922, Traefik 2,258 MiB. The proxy backend used at most 3.07 of its 4 CPUs.

### W2, the website

| | HTTPS/1.1 | h2 | HTTP/3 |
| --- | --- | --- | --- |
| `site`, 500 | fit 92.1k, nginx 81.8k, Caddy 42.2k | fit 100.8k, nginx 66.9k, Caddy 28.5k | nginx 130.0k, fit 62.8k, Caddy 27.9k |
| `site`, 2,000 | fit 88.5k, nginx 77.5k, Caddy 40.1k | fit 101.8k, nginx 75.8k, Caddy 27.9k | nginx 99.7k, fit 62.6k, Caddy 26.2k |
| `edge`, 500 | fit 131.9k, nginx 76.2k | fit 133.9k, nginx 70.3k | nginx 114.1k, fit 69.3k |
| `edge`, 2,000 | fit 94.6k, nginx 72.3k | fit 121.9k, nginx 71.5k | nginx 92.8k, fit 71.4k |

The caches answered 99.6-100% of requests themselves. fit keeps files and cache entries in memory of
its own (263-1,414 MiB serving files over HTTPS/1.1 and h2, 512-1,414 MiB as a cache), nginx in
the page cache, which the memory columns do not count.

### W3, connection churn

| | 100 connections | 500 connections |
| --- | --- | --- |
| `churn-1` | Caddy 19.3k, fit 19.3k, Envoy 19.1k, nginx 19.0k | fit 19.6k, Caddy 19.5k, nginx 19.5k, Envoy 18.9k |
| `churn-10` | fit 132.5k, nginx 128.6k, Caddy 117.5k, Envoy 116.6k | fit 136.6k, nginx 134.4k, Envoy 133.0k, Caddy 123.8k |

With a TLS handshake for every request every server reaches about 19k a second, on 4.0-5.7 of its 6
CPUs: the handshake sets the pace, not the server. With one handshake every ten requests fit leads,
at 133-137k, and nginx is close behind.

### W4, large transfers

| | HTTP/1.1 | h2 |
| --- | --- | --- |
| `download` | fit 17.4k, nginx 16.2k, Caddy 14.0k | fit 11.6k, nginx 5.9k, Caddy 4.1k |
| `proxy-download` | Envoy 13.6k, fit 12.8k, Traefik 11.7k, Caddy 10.3k, nginx 5.1k | fit 8.8k, Envoy 5.2k, nginx 3.7k, Traefik 3.6k, Caddy 3.5k |
| `proxy-upload` | Traefik 9.9k, Caddy 9.7k, fit 8.8k, Envoy 7.1k, nginx 5.4k | fit 6.8k, nginx 6.8k, Envoy 6.6k, Traefik 5.3k, Caddy 5.1k |

fit is first on both downloads, first on the proxied download over h2 and second over HTTP/1.1, and
level with nginx on the proxied upload over h2. It uses 2.4 of its 6 CPUs on the HTTP/1.1 download
(nginx 0.96, through `sendfile`) and 5.8-6.0 on the others; the backend used at most 2.94 of its 4
CPUs, on fit's proxied upload over HTTP/1.1.

### W5, 40,000 idle TLS connections

| Server | HTTP/1.1 | h2 | Peak memory (HTTP/1.1 / h2) |
| --- | --- | --- | --- |
| nginx | 298k | 249k | 1,304 / 1,311 MiB |
| fit | 215k | 198k | 519 / 530 MiB |
| Envoy | 95k | 118k | 858 / 860 MiB |
| Traefik | 91k | 64k | 2,345 / 2,317 MiB |
| Caddy | 73k | 58k | 2,299 / 2,252 MiB |

Every server held all 40,000 and closed none, at W1's throughput. fit's memory grew by 4-11%
against the earlier plan (519-530 MiB from 479-499), and nginx's by 4% (1,304-1,311 MiB from
1,249-1,253).

### W6, latency at a fixed rate

| | 30k a second, p99 | 200k a second, TPS and p99 |
| --- | --- | --- |
| HTTP/1.1 | nginx 1.69 ms, fit 2.73, Envoy 6.01, Traefik 7.09, Caddy 8.89 | nginx 199.9k 2.49 ms, fit 199.9k 5.85, Traefik 97.1k 12.93, Envoy 95.2k 6.14, Caddy 72.4k 18.35 |
| h2 | nginx 1.58 ms, fit 1.92, Envoy 3.45, Traefik 5.15, Caddy 5.63 | fit 199.7k 5.85 ms, nginx 196.5k 3.00, Envoy 113.7k 10.79, Traefik 68.8k 18.29, Caddy 57.9k 26.23 |

At 30k every server keeps the rate on 0.7-3.0 CPUs. At 200k nginx and fit hold it on 4.0-5.8 CPUs;
the others run at their ceilings on 5.0-5.9 of their 6. At twice the earlier plan's high rate, nginx's
p99 is 2.49 and 3.00 ms against 3.17 and 4.52 at 100k, and fit's 5.85 and 5.85 against 5.93 and 7.59.

## Compared with the 2026-10-24 plan

212 of the 232 runs have a counterpart in [the 2026-10-24 plan](../plan-2026-10-24-auto/report.md)
at the same workload and level; W6's 20 are not compared, because the calibration raised its rates
from 20,000 and 100,000 to 30,000 and 200,000 a second. Against that plan the server has 50% more
CPUs:

| Workload | Runs | Median change | Range | Within 5% |
| --- | ---: | ---: | --- | ---: |
| W0 | 80 | +47.2% | +10.6% to +72.6% | 0 |
| W1 | 50 | +48.6% | +30.2% to +83.2% | 0 |
| W2 | 30 | +50.0% | +17.2% to +96.2% | 0 |
| W3 | 16 | +31.4% | -1.1% to +49.7% | 5 |
| W4 | 26 | +50.8% | -0.3% to +182.6% | 1 |
| W5 | 10 | +49.1% | +47.1% to +77.5% | 0 |

Every server gains about as much as its CPUs: Caddy's 48 runs +47.1% (median), Envoy's 40 +47.3%,
fit's 54 +48.4%, nginx's 54 +49.5%, Traefik's 16 +48.1%. The mean CPU at peak rises from 3.90 to
5.73 in W0, 3.94 to 5.92 in W1, 3.93 to 5.79 in W2, 3.86 to 5.03 in W3, 3.84 to 5.43 in W4 and 3.96
to 5.96 in W5, and the throughput each CPU delivers is within 1% of the old plan's (median -0.8%).
The gains are therefore the extra CPUs at work, not a different measurement of the same work.

The exceptions are where something else sets the pace: in W3's `churn-1` fit and Envoy stay at
about 19k (0-4% up, the TLS handshake), while Caddy and nginx reach 19k from 13-16k (+23% to +44%)
and the workload's median is +31%; nginx's HTTP/1.1 download in W4 is -0.3% (the load generators,
at 0.96 of nginx's 6 CPUs). The largest gains are fit's proxied transfers in W4 (+84% to +183%),
which were CPU-bound on 4 CPUs before.

The comparison also includes fit's changes since e5c2941 (fib 33a039f1, the 64 KiB reads, A47 and
the logging work); the other four servers are unchanged, and their medians of +47% to +50% show that
the split, not fit's code, is the dominant change. W6's rates changed with the split, so its latency
is compared within this run, not run for run.

## Notes and limits

- **One machine, loopback.** No network latency or NIC limits; the results compare the servers' own
  costs on 6 CPUs, which are 3 physical cores with both their threads.
- **The load generators can be the limit** where a server answers very fast: fit's h2c and h2 at
  3.3-3.5M a second (its h2c rises 0.7% from 5,000 to 10,000 in flight while the server keeps 5.95
  of 6 CPUs), nginx's HTTP/1.1 download, and W3's `churn-1`. W3's churn and W4's downloads are why
  their medians are lower than the other workloads'.
- **The backends have less headroom than before.** 4 CPUs against 6 gave at most 3.07 used (77%),
  where 6 CPUs against 4 gave 2.18 (36%). They did not set the pace in this run, but a workload that
  proxies more, or a machine where the server gets more of the cores, would need them to grow.
- **Latency over h2c, h2 and HTTP/3** outside W6 is a mean and a maximum, since h2load reports no
  percentiles.
- **The derivation was checked on this machine only.** Its rules are tested over fake machines of 3
  to 64 threads, but this is the first real run of the new split.
