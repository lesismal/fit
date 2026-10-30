# Load test plan, 2026-10-30: the report

[English](report.md) | [简体中文](report.zh-CN.md)

nginx, Caddy, Traefik, Envoy and fit under the seven workloads of [the load test
plan](../../README.md#the-load-test-plan), at 100 to 10,000 requests in flight, the server under test on 6
CPUs, the backends on 4 and the load generators on 6 threads: the plan of 2026-10-27 run again with fit
at 24987e5 (fib 7dcb41da), after the memory and throughput work recorded in
[the 2026-10-27 analysis](../plan-2026-10-27/analysis.md). 232 runs on the benchmark host, 2026-10-30
11:25 to 12:57 UTC (19:25 to 20:57 in the host's Asia/Hong_Kong): none failed, none hung, and no run
had a single error; they took 1 h 32 min against the 1 h 33 min estimated. Unlike the 2026-10-27 plan,
the host ran with its kernel settings as they are (below), and [host-settings.sh](host-settings.sh)
sets a host to them. Every table is in [tables.md](tables.md), rendered from the runs' JSON in this
folder, one file for each workload and level; the plan derived is [plan.txt](plan.txt) (and
`plan-derived.json`, which every run's `params.derived` holds too).

## In short

- **fit holds less memory nearly everywhere, at the same throughput or more.** Its peak memory falls
  by a median of 35% over its 58 runs against the 2026-10-27 plan (50 of them by more than 5%), while
  the other four servers' median is 0.0%: W1 at 10,000 in flight over HTTP/1.1 664 MiB from 1,310, the
  website over HTTPS/1.1 at 500 191 MiB from 304, proxied downloads 34 and 69 MiB from 72 and 151,
  proxied uploads over h2 178 MiB from 335.
- **fit is faster where the work was aimed:** HTTP/3 plain responses +16% to +21% (1.69M at 500 in
  flight, now ahead of nginx's 1.62M there), the website over h2 +12% (113k and 114k), over HTTPS/1.1
  +7% to +8% (99k and 95k), proxied downloads +13% over HTTP/1.1 (14.4k, now first) and +8% over h2
  (9.4k), downloads over h2 +7% (12.3k).
- **One regression: fit's API gateway over HTTP/1.1 (W1) is 3% slower** at 500, 5,000 and 10,000 in
  flight and 7% at 100, at 3-7% more CPU a request; over h2 it is 0.5-4% slower. nginx's own W1 over
  HTTP/1.1 is within 1.3% of before, so this is fit's, not the host's. A/B runs of each change measured W1 over
  HTTPS/1.1 and h2 within noise but not over plain HTTP/1.1, where the small costs added up.
- **The other servers measure as before.** Their 178 runs against the 2026-10-27 plan's have a median
  change of -0.3% (Caddy -0.4%, Envoy +0.2%, nginx -0.4%, Traefik -0.5%), so the host's different
  kernel settings did not move the results that can be compared; nginx's few outliers are at its
  low-concurrency h2c and h2 and its HTTP/3 at 500 (-10% to -17%, +12% to +15% elsewhere).
- **Plain responses (W0):** fit and nginx tie over HTTP/1.1 (671k and 668k at their peaks, 608k and
  614k at 10,000); fit leads h2c (3.45M) and h2 (3.24M); nginx leads HTTP/3 at its peak (1.91M, fit
  1.69M at 500, where fit leads); fit holds the least memory at 10,000 in flight (39, 67, 64 and 149
  MiB over HTTP/1.1, h2c, h2 and HTTP/3, against nginx's 174, 174, 191 and 533).
- **API gateway (W1):** nginx leads (302k over HTTP/1.1 at 100, 202k at 10,000; 249k over h2 at 500,
  200k at 10,000); fit is second at 66-78% of it over HTTP/1.1 and 75-86% over h2, holding less than a
  third of Traefik's memory and less than Caddy's at 10,000.
- **Website (W2):** fit leads serving files (99k over HTTPS/1.1 and 113k over h2 at 500, against
  nginx's 82k and 67k), now in less memory than nginx over HTTPS/1.1 (191 against 200 MiB at 500, 228
  against 278 at 2,000), and as a cache (132k and 132k at 500); nginx leads HTTP/3 (128k site, 115k
  edge at 500), fit reaching 51-78% of it.
- **Connection churn (W3):** a TLS handshake for every request holds every server at about 19k; one
  every ten requests puts fit ahead (132-136k), nginx close behind (128-135k).
- **1 MiB transfers (W4):** fit is first on both downloads (17.7k and 12.3k), both proxied downloads
  (14.4k and 9.4k) and the proxied upload over h2 (7.1k), and third on the proxied upload over HTTP/1.1
  (8.9k, Traefik 9.8k, Caddy 9.7k).
- **40,000 idle TLS connections (W5):** every server kept all 40,000; nginx leads (295k and 246k), fit is
  second (214k and 199k) in 475-483 MiB, from 519-530, against nginx's 1,304-1,311.
- **Latency at a fixed rate (W6):** the calibration gives 30,000 and 200,000 again. At 30k every server
  keeps the rate (p99: nginx 1.69 and 1.98 ms, fit 3.14 and 2.30); at 200k nginx and fit keep it (p99
  2.05 and 3.14 ms, 6.08 and 5.68), while Envoy, Traefik and Caddy fall to 58-116k.

## The test environment

The machine, the servers, their images and the load generators are those of the 2026-10-27 plan; fit
and the host's kernel settings are not:

| | |
| --- | --- |
| Machine | AMD Ryzen 7 7800X3D, 8 cores of 2 threads (CPUs n and n+8 share core n), up to 5.05 GHz, 62 GiB; Ubuntu 24.04.5, Linux 7.0.0-38-generic, bare metal; every container on the host's network; Docker 29.1.3, cgroup v2 |
| CPUs | the server under test `0-2,8-10` (6 workers), the backends `3-4,11-12` (4 CPUs), the load generators `5-7,13-15` (6 threads); `amd-pstate-epp`, governor `powersave` with energy preference `balance_performance`, boost on; SMT on; transparent huge pages `madvise` |
| Servers | nginx 1.31.4, Caddy 2.10.2, Traefik 3.5.0, Envoy 1.40.0, at the same image digests as the 2026-10-27 plan |
| fit | 24987e5 (fib 7dcb41da, Go 1.27.3), built from a clean tree |
| Load generators | wrk 4.1.0; h2load 1.59.0, and 1.68.0 with HTTP/3 |
| Also on the host | a kind node (`fitconf-control-plane`, 4.7% of a CPU and 616 MiB when checked before the run) and an etcd container (0.1%, 11 MiB), neither pinned to a CPU set |

The kernel settings, as the host had them for every run; [host-settings.sh](host-settings.sh) sets a
host to them (`-check` compares, changing nothing, and `-persist` also writes them to
`/etc/sysctl.d`):

| Setting | Value | 2026-10-27 plan |
| --- | --- | --- |
| `net.core.somaxconn`, `net.ipv4.tcp_max_syn_backlog` | 32,768 | 2,048 |
| `net.ipv4.tcp_rmem` | 4096 262144 16777216 | 4096 131072 6291456 |
| `net.ipv4.tcp_wmem` | 4096 262144 16777216 | 4096 16384 4194304 |
| `net.core.rmem_default`, `wmem_default` | 262,144 | |
| `net.core.rmem_max`, `wmem_max` | 7,500,000, set again before every run by `bench/plan.sh` | the same |
| `net.core.netdev_max_backlog` | 250,000 | |
| `net.core.optmem_max` | 131,072 | |
| `net.ipv4.tcp_mem` | 131072 262144 524288 pages (pressure from 1 GiB) | the same |
| `net.ipv4.udp_rmem_min`, `udp_wmem_min` | 16,384 | |
| `net.ipv4.ip_local_port_range` | 1024 65535 | the same |
| `net.ipv4.ip_local_reserved_ports` | 8080-8081, 8443-8445, 9000-9005 | the same |
| `net.ipv4.tcp_tw_reuse`, `tcp_fin_timeout` | 1, 60 | |
| `net.ipv4.tcp_congestion_control`, `net.core.default_qdisc` | cubic, fq_codel | |
| `net.ipv4.tcp_slow_start_after_idle`, `tcp_autocorking`, `tcp_syncookies` | 1, 1, 1 | |
| `net.ipv4.tcp_timestamps`, `tcp_sack`, `tcp_window_scaling` | 1, 1, 1 | |
| `net.netfilter.nf_conntrack_max` | 2,000,500 | |
| `fs.file-max`, `fs.nr_open` | 2,000,500 | the same |
| `vm.swappiness`, `vm.overcommit_memory` | 60, 1 | |
| `kernel.pid_max` | 4,194,304 | |

The 2026-10-27 plan set `somaxconn`, `tcp_max_syn_backlog` and the TCP buffers back to an earlier
plan's values and stopped the kind node for its run; this run left the host as it was. Its settings
come from `/etc/sysctl.d/99-fib-test.conf` (the backlog, the TCP buffers and the defaults),
`90-fitbench-udp-buffers.conf` (`rmem_max`, `wmem_max`) and `91-fitbench-reserved-ports.conf`; the rest
are Ubuntu's.

**Every server gets the same listen backlog.** `fitbench plan` derives the backlog from `somaxconn`,
32,768 here, and renders it into nginx's `backlog=`, Envoy's `tcp_backlog_size` and fit's
`engine.backlog`; Caddy and Traefik, which have no such setting, take `somaxconn` itself, as Go does.
Pinning the backlog to the 2026-10-27 plan's 2,048 without lowering `somaxconn` would have given nginx,
Envoy and fit 2,048 and Caddy and Traefik 32,768, so it was not. The TCP buffers are the host's
defaults for every socket: none of the five servers sets a socket's buffer in these configurations.

## How the plan was derived

`fitbench plan` derived the plan of 2026-10-27 again, with two differences, both from `somaxconn`:

| Setting | Derived | 2026-10-27 plan |
| --- | --- | --- |
| CPU split, levels, W2-W6, W5's idle connections | as the 2026-10-27 plan: server 3 cores (6 CPUs), backends 2 (4 CPUs), load generators 3 (6 threads); 100, 500, 2,000, 5,000, 10,000 | the same |
| Top level | 10,000 in flight, bounded by the load generators (10,002) | 10,000, bounded by the load generators and `somaxconn` (10,240) |
| Aligned settings | backend connections and waiting requests 16,384, nginx 2,731 idle a worker, backlog 32,768, `worker_connections` 65,536 | backlog 2,048 |
| W6's rates | 30,000 and 200,000 a second | the same |

`somaxconn` no longer bounds the top level (163,840 against 10,240); the load generators do, at 10,002.

**W6's calibration.** Each server's ceiling is the least it answered over HTTP/1.1 and h2 in W1 at
500 in flight: Caddy 57,897, Traefik 68,518, Envoy 96,000, fit 205,586, nginx 249,301. The low rate is
half the slowest, 28,949, rounded to 30,000; the widest gap is Envoy's to fit's, so the high rate is the
roundest number between Envoy's most (118,594 over h2) and fit's 205,586: 200,000.

## What the tables show

The tables, their columns and their ranking are as in the 2026-10-24 plan's [What the tables
show](../plan-2026-10-24-auto/report.md#what-the-tables-show): TPS is successful responses a second,
CPU EER TPS for each core used, MEM EER TPS for each MiB of peak anonymous memory, ranked by [↓1],
[↓2], [↓3], each value with its share of the best. W0 and W1 merge their levels into columns; the
other workloads have a table for each level.

## The results

### W0, the raw ceiling

| Protocol | Best | Then | Memory at 10,000 |
| --- | --- | --- | --- |
| HTTP/1.1 | fit 671k | nginx 668k (99.6%), Envoy 261k, Caddy 257k | fit 39, nginx 174, Envoy 269, Caddy 309 MiB |
| h2c | fit 3.45M | nginx 489k (14%), Envoy 447k, Caddy 152k | fit 67, nginx 174, Envoy 109, Caddy 151 MiB |
| h2 | fit 3.24M | nginx 714k (22%), Envoy 448k, Caddy 161k | fit 64, nginx 191, Envoy 112, Caddy 192 MiB |
| HTTP/3 | nginx 1.91M | fit 1.69M (89%), Caddy 242k, Envoy 168k | fit 149, nginx 533, Caddy 309 MiB, Envoy 3,746 MiB |

fit and nginx hold their HTTP/1.1 throughput from 100 to 10,000 in flight (667k to 608k, 667k to 614k).
fit's h2c and h2 rise with the streams in flight to 3.45M and 3.24M at 10,000. fit's HTTP/3 peaks at
500 in flight, 1.69M against nginx's 1.62M, and falls to 1.41M at 10,000 (nginx 1.88M); it is 16-21%
above the 2026-10-27 plan at 500 to 10,000. At 100 in flight fit holds 11-17 MiB, nginx 169-177.

### W1, the API gateway

| Protocol | 100 | 500 | 2,000 | 5,000 | 10,000 |
| --- | --- | --- | --- | --- | --- |
| HTTP/1.1 | nginx 302k, fit 201k, Traefik 97k, Envoy 89k, Caddy 72k | nginx 297k, fit 218k | nginx 295k, fit 209k | nginx 254k, fit 177k | nginx 202k, fit 158k, Traefik 74k, Envoy 63k, Caddy 54k |
| h2 | nginx 242k, fit 182k, Envoy 107k, Traefik 66k, Caddy 56k | nginx 249k, fit 206k | nginx 244k, fit 205k | nginx 229k, fit 193k | nginx 200k, fit 173k, Envoy 82k, Traefik 59k, Caddy 48k |

nginx leads at every level; fit is second at 66-78% of it over HTTP/1.1 and 75-86% over h2. Against
the 2026-10-27 plan fit's HTTP/1.1 is 3.1-3.2% slower at 500, 5,000 and 10,000 and 6.9% at 100, with
3.0-3.3% and 6.7% more CPU a request (27.2 µs at 500 from 26.4); its h2 is 0.5-1.9% slower but 4.0% at
100. Its memory fell by a third to a half: at 10,000 664 MiB over HTTP/1.1 from 1,310 and 642 MiB over
h2 from 981, against nginx's 263 and 423, Envoy's 613 and 486, Caddy's 1,074 and 892, Traefik's 2,160 and
2,633. The proxy backend used at most 2.99 of its 4 CPUs.

### W2, the website

| | HTTPS/1.1 | h2 | HTTP/3 |
| --- | --- | --- | --- |
| `site`, 500 | fit 98.6k, nginx 81.5k, Caddy 41.8k | fit 113.4k, nginx 66.5k, Caddy 28.6k | nginx 128.1k, fit 64.8k, Caddy 28.0k |
| `site`, 2,000 | fit 95.4k, nginx 77.8k, Caddy 40.1k | fit 114.3k, nginx 75.4k, Caddy 28.0k | nginx 100.3k, fit 67.5k, Caddy 26.2k |
| `edge`, 500 | fit 131.9k, nginx 76.0k | fit 131.9k, nginx 69.4k | nginx 114.9k, fit 71.0k |
| `edge`, 2,000 | fit 95.4k, nginx 72.1k | fit 125.2k, nginx 71.2k | nginx 93.9k, fit 73.4k |

fit serves the website over HTTPS/1.1 in 191 MiB at 500 and 228 MiB at 2,000, from 304 and 358, now
below nginx's 200 and 278; over h2 in 251 and 456 MiB, from 268 and 553. The caches answered nearly
every request themselves; fit keeps cache entries in memory of its own (884-1,437 MiB), nginx in the
page cache, which the memory columns do not count.

### W3, connection churn

| | 100 connections | 500 connections |
| --- | --- | --- |
| `churn-1` | Caddy 19.2k, fit 19.2k, Envoy 19.1k, nginx 19.0k | Caddy 19.6k, fit 19.6k, nginx 19.5k, Envoy 19.0k |
| `churn-10` | fit 132.2k, nginx 127.8k, Envoy 117.9k, Caddy 117.4k | fit 135.8k, nginx 134.7k, Envoy 132.1k, Caddy 122.4k |

With a TLS handshake for every request every server reaches about 19k a second: the handshake sets the
pace. With one every ten requests fit leads, nginx close behind. fit holds 16-21 MiB, from 32-36.

### W4, large transfers

| | HTTP/1.1 | h2 |
| --- | --- | --- |
| `download` | fit 17.7k, nginx 16.3k, Caddy 14.0k | fit 12.3k, nginx 5.9k, Caddy 4.1k |
| `proxy-download` | fit 14.4k, Envoy 13.9k, Traefik 11.6k, Caddy 10.2k, nginx 5.1k | fit 9.4k, Envoy 5.3k, nginx 3.7k, Traefik 3.6k, Caddy 3.4k |
| `proxy-upload` | Traefik 9.8k, Caddy 9.7k, fit 8.9k, Envoy 7.1k, nginx 5.3k | fit 7.1k, nginx 5.9k, Envoy 5.8k, Traefik 5.3k, Caddy 5.2k |

fit is first on both downloads and both proxied downloads, now ahead of Envoy over HTTP/1.1, and first
on the proxied upload over h2. Its memory: downloads 11 and 68 MiB (from 18 and 121), proxied downloads
34 and 69 MiB (from 72 and 151, against Caddy's 32 and 36), proxied uploads 132 and 177 MiB (from 114
and 335). nginx's and Envoy's proxied uploads over h2 are 12% below the 2026-10-27 plan, the largest
change of any other server in W4.

### W5, 40,000 idle TLS connections

| Server | HTTP/1.1 | h2 | Peak memory (HTTP/1.1 / h2) |
| --- | --- | --- | --- |
| nginx | 295k | 246k | 1,304 / 1,311 MiB |
| fit | 214k | 199k | 475 / 483 MiB |
| Envoy | 94k | 117k | 858 / 865 MiB |
| Traefik | 89k | 64k | 2,367 / 2,295 MiB |
| Caddy | 73k | 58k | 2,321 / 2,253 MiB |

Every server held all 40,000 and closed none. fit's memory fell 8-9% (475-483 MiB from 519-530); the
others' moved by at most 1%.

### W6, latency at a fixed rate

| | 30k a second, p99 | 200k a second, TPS and p99 |
| --- | --- | --- |
| HTTP/1.1 | nginx 1.69 ms, fit 3.14, Envoy 4.94, Traefik 7.92, Caddy 8.35 | nginx 200.0k 2.05 ms, fit 200.0k 6.08, Traefik 97.1k 12.94, Envoy 94.2k 6.30, Caddy 72.0k 18.35 |
| h2 | nginx 1.98 ms, fit 2.30, Envoy 3.38, Traefik 6.08, Caddy 6.96 | nginx 200.0k 3.14 ms, fit 200.0k 5.68, Envoy 116.2k 8.75, Traefik 68.7k 18.10, Caddy 57.8k 26.35 |

At 30k every server keeps the rate on 0.7-3.1 CPUs; at 200k nginx and fit keep it, on 3.9-4.8 and
5.5-5.8 CPUs, the others at their ceilings. fit's p99 is 3.14 and 2.30 ms at 30k (2.73 and 1.92 before)
and 6.08 and 5.68 at 200k (5.85 and 5.85 before); its memory 32 and 59-62 MiB, from 51-58 and 81-114.

## Compared with the 2026-10-27 plan

All 232 runs have a counterpart in [the 2026-10-27 plan](../plan-2026-10-27/report.md) at the same
workload, level and rates. Two things changed between them: fit (c08eb0d with fib 33a039f1, to 24987e5
with fib 7dcb41da), and the host's kernel settings (above). The other servers, unchanged, show what the
second did:

| Workload | Runs | Median change | Range | Within 5% | fit's median | the others' median |
| --- | ---: | ---: | --- | ---: | ---: | ---: |
| W0 | 80 | -0.4% | -17.0% to +20.5% | 69 | -0.5% | -0.3% |
| W1 | 50 | -0.5% | -6.9% to +14.5% | 47 | -2.5% | -0.3% |
| W2 | 30 | +0.3% | -1.8% to +12.5% | 25 | +2.9% | -0.2% |
| W3 | 16 | -0.1% | -1.3% to +1.3% | 16 | -0.3% | +0.0% |
| W4 | 26 | -0.1% | -12.2% to +12.5% | 21 | +5.5% | -0.3% |
| W5 | 10 | -0.7% | -1.5% to +0.7% | 10 | +0.0% | -0.9% |
| W6 | 20 | -0.0% | -1.0% to +2.1% | 20 | -0.0% | -0.0% |

The other servers' 178 runs have a median change of -0.3%, 164 of them within 5%; Caddy's 48 are all
within 5% (median -0.4%) and Traefik's 16 too (-0.5%). nginx's outliers are at 100 in flight over h2c
and h2 (-17% and -10%), over HTTP/3 at 500 (-10%) and over h2c at 500 and h2 in W1 at 100 (+12% and
+15%), where the 2026-10-27 plan's nginx was itself uneven between levels; and nginx's and Envoy's
proxied uploads over h2 are 12% lower. The larger backlog and TCP buffers did not, then, move what is
compared here in one direction.

fit's own changes, by what the 2026-10-27 analysis set out to do:

| | Change in throughput | Change in peak memory |
| --- | --- | --- |
| W0 HTTP/3 (500 to 10,000) | +16.4% to +20.5% | -3% to -63% |
| W0 HTTP/1.1, h2c, h2 | -1.8% to +0.0% | -10% to -54% |
| W1 HTTP/1.1 | -6.9% (100), 0.0% (2,000), -3.1% to -3.2% (500, 5,000, 10,000) | -28% to -49% |
| W1 h2 | -4.0% (100), -0.5% to -1.9% | -34% to -56% |
| W2 `site` | +3.2% to +12.5% | -6% to -37% (HTTP/3 at 2,000 +17%) |
| W2 `edge` | -1.5% to +2.7% | -13% to +2% |
| W3 | -0.6% to +0.0% | -38% to -54% |
| W4 downloads, proxied downloads | +1.4% to +12.5% | -33% to -54% |
| W4 proxied uploads | +0.9% (HTTP/1.1), +4.3% (h2) | +16%, -47% |
| W5 | -0.6% and +0.7% | -8% and -9% |
| W6 | within 0.1% (the rate) | -28% to -46% |

fit's memory falls by a median of 35% over its 58 runs while the others' moves by 0.0%: the read
round that starts small, the pooled body chunks, the queues that grow with their backlog and the
other changes in the analysis. The website, proxied downloads and HTTP/3 gained throughput as their
A/B runs did. W1 over HTTP/1.1 is the exception, 3% slower at 3% more CPU a request, which the A/B runs
of each change, measuring W1 over HTTPS/1.1 and h2, did not see; finding which change costs it is
the next step.

## Notes and limits

- **One machine, loopback.** No network latency or NIC limits; the results compare the servers' own
  costs on 6 CPUs, which are 3 physical cores with both their threads.
- **The host is not the 2026-10-27 plan's host to the setting.** Its backlog and TCP buffers are larger
  and a kind node and an etcd container ran beside the plan, using about 0.05 of a CPU; the other
  servers' median of -0.3% says the comparison holds, but a single run's change of a few percent is
  within what the two hosts' differences and run-to-run noise allow.
- **The load generators can be the limit** where a server answers very fast: fit's h2c and h2 at
  3.2-3.5M a second, nginx's HTTP/1.1 download, and W3's `churn-1`.
- **The backends have less headroom than the server.** 4 CPUs gave at most 2.99 used (75%); they did not
  set the pace.
- **Latency over h2c, h2 and HTTP/3** outside W6 is a mean and a maximum, since h2load reports no
  percentiles.
