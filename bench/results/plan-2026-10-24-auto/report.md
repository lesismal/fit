# Load test plan derived from the machine, 2026-10-24: the report

[English](report.md) | [简体中文](report.zh-CN.md)

nginx, Caddy, Traefik, Envoy and fit under the seven workloads of [the load test
plan](../../README.md#the-load-test-plan), at 100 to 10,000 requests in flight, each server on 4 CPUs,
the first run of the plan with its sizes [derived from the machine](../../README.md#deriving-the-plan)
by `fitbench plan` rather than set by hand. 232 runs on the benchmark host, 2026-10-24 14:51 to
16:23 UTC: none failed, none hung, and no run had a single error. Every table is in
[tables.md](tables.md), rendered by benchreport from the runs' JSON in this folder, one file for each
workload and level; the plan derived is [plan.txt](plan.txt) (and `plan-derived.json`, which every
run's `params.derived` holds too).

## In short

- **The derived plan is the hand-tuned one.** On this machine `fitbench plan` chose the same CPU
  split, levels, workloads, aligned settings and 232 runs as the plan tuned by hand for it, and its
  calibration chose W6's 20,000 and 100,000 a second from this run's own W1 results. The run took
  1 h 32 min against the 1 h 33 min estimated.
- **It reproduces the earlier run.** Against [the second run of 2026-10-24](../plan-2026-10-24-646/report.md),
  made with the same settings 6 hours before, the median change in throughput over all 232 runs is
  0.0%, the mean change 1.1%, 207 runs are within 2% and 224 within 5%. Every ranking is the same.
  The few larger changes are nginx with only 10 connections over 4 workers (below).
- **Plain responses (W0):** nginx leads HTTP/1.1 (504k a second) and HTTP/3 (1.36M), fit is close
  behind on HTTP/1.1 (472k, 93%) and second on HTTP/3 (987k, 72%), and fit leads h2c and h2 by far
  (2.30M and 2.21M, against nginx's 327k and 480k), in the least memory at every level.
- **API gateway (W1):** nginx leads (202k a second over HTTP/1.1, 162k over h2), fit is second
  (136k, 114k: 67-70%), then Envoy, Traefik and Caddy at 24-49%.
- **Website (W2):** over HTTPS/1.1 and h2 fit leads both serving files (64-71k a second) and as a
  cache (81-87k), against nginx's 46-53k; over HTTP/3 nginx leads (65-87k), fit reaches about half.
- **Connection churn (W3):** a TLS handshake for every request puts Envoy and fit ahead (19k a
  second); one every ten requests puts fit ahead (124-134k), then nginx (92-96k).
- **1 MiB transfers (W4):** fit leads downloads and proxied downloads over h2, and trails in
  proxied downloads and uploads over HTTP/1.1 (50-67% of the best) and proxied uploads over h2
  (47%), still its weakest results.
- **40,000 idle TLS connections (W5):** every server kept all of them at W1's throughput; fit in
  479-499 MiB, Envoy 857, nginx 1,250, Caddy and Traefik 2.2-2.4 GiB.
- **Latency at a fixed rate (W6):** at 20k a second nginx has the lowest p99 (2.39 ms over
  HTTP/1.1, 1.25 over h2), then fit (4.49, 2.46); at 100k only nginx and fit keep up.
- **The backends never set the pace:** the proxy backend used at most 2.18 of its 6 CPUs, the cache
  origin at most 0.01.

## The test environment

The machine, the CPU split and the software are those of the earlier run, as its
report's [test environment](../plan-2026-10-24-646/report.md#the-test-environment) describes, but
for fit's version:

| | |
| --- | --- |
| Machine | AMD Ryzen 7 7800X3D, 8 cores of 2 threads (CPUs n and n+8 share core n), up to 5.05 GHz, 62 GiB; Ubuntu 24.04.5, Linux 7.0.0-38-generic, bare metal; every container on the host's network |
| Kernel settings | `somaxconn` 2,048; `rmem_max`, `wmem_max` 7,500,000 (re-applied before every run); local ports 1024-65535; `nr_open` 2,000,500; `tcp_mem` pressure from 1 GiB |
| CPUs | the server under test `0-1,8-9` (4 workers), the backends `2-4,10-12` (6), the load generators `5-7,13-15` (6 threads) |
| Servers | nginx 1.31.4, Caddy 2.10.2, Traefik 3.5.0, Envoy 1.40.0, at the same image digests as the earlier run |
| fit | e5c2941 (fib 0e63ae8, Go 1.27.3); it reads `e5c2941+dirty`, built from a tree whose only changes were the harness's, committed as c8ee767 |
| Load generators | wrk 4.1.0; h2load 1.59.0, and 1.68.0 with HTTP/3 |

## How the plan was derived

`fitbench plan` read the machine (`plan.txt` lists it) and set each of the plan's sizes from it:

| Setting | Derived | From |
| --- | --- | --- |
| CPU split | server 2 cores, backends 3, load generators 3 | a quarter of the 8 cores to the server, the rest halved, by physical core |
| Top level | 10,000 in flight | the least of what each input allows: the server's CPUs 10,000 (2,500 each), `somaxconn` 10,240 (5 times it), memory 31,715, TCP memory 32,768, ports 51,609, descriptors 262,144 |
| Levels | 100, 500, 2,000, 5,000, 10,000 | 100, 500, and a fifth and a half of the top; h2load 10 × 10, then 25 streams a connection |
| W2, W3, W4, W5, W6 | 500 and 2,000; 100 and 500 connections; 100; 500; 500 | the levels near 500; W4 bounded by TCP memory (128) |
| W5's idle connections | 40,000 | 4 for each request in flight at the top, within memory (249,722) and descriptors (523,288) |
| Aligned settings | backend connections and waiting requests 16,384, nginx 4,096 idle a worker, backlog 2,048, `worker_connections` 65,536 | half again the top, rounded up to a power of two; divided by the workers; `somaxconn` |
| W6's rates | 20,000 and 100,000 a second | calibrated from W1 at 500 (below) |

The two inputs nearest their bounds are the server's CPUs and `somaxconn`: a machine with a larger
`somaxconn` and more server CPUs would get a higher top level, one with less memory a lower one.

**W6's calibration.** Each server's ceiling is the least it answered over HTTP/1.1 and h2 in W1 at
500 in flight: Caddy 39,472, Traefik 46,428, Envoy 63,465, fit 113,050, nginx 162,226. The low rate
is half the slowest, 19,736, rounded to 20,000; the widest gap between two ceilings is Envoy's to
fit's, so the high rate is the roundest number between the slower side's most (Envoy over h2,
79,657) and fit's 113,050: 100,000. These are the rates chosen by hand for the earlier run.

A 1s and 3s trial of the whole plan ran first (30 minutes), to check every workload with the derived
settings: no errors, and the backends at most 2.17 of their 6 CPUs.

## What the tables show

The tables, their columns and their ranking are as in the earlier report's
[What the tables show](../plan-2026-10-24-646/report.md#what-the-tables-show): TPS is successful
responses a second, CPU EER TPS for each core used, MEM EER TPS for each MiB of peak anonymous
memory, ranked by [↓1], [↓2], [↓3], each value with its share of the best.

## The results

### W0, the raw ceiling

| Protocol | Best | Then | Memory at 10,000 |
| --- | --- | --- | --- |
| HTTP/1.1 | nginx 504k | fit 472k (93%), Envoy 176k, Caddy 173k | nginx 120, fit 32, Envoy 268, Caddy 308 MiB |
| h2c | fit 2.30M | nginx 327k (14%), Envoy 300k, Caddy 104k | fit 54, nginx 120, Envoy 119, Caddy 179 MiB |
| h2 | fit 2.21M | nginx 480k (22%), Envoy 298k, Caddy 110k | fit 66, nginx 136, Envoy 118, Caddy 166 MiB |
| HTTP/3 | nginx 1.36M | fit 987k (72%), Caddy 167k, Envoy 115k | fit 116, Caddy 323, nginx 822 MiB, Envoy 4.8 GiB |

As in the earlier run, most servers hold their throughput from 100 to 10,000 in flight, Envoy over
HTTP/1.1 drops most (176k to 117k), fit's h2c and h2 rise with the streams in flight (1.2M at
10 × 10 to 2.3M), and Envoy's HTTP/3 memory grows to 4.8 GiB at 10,000.

### W1, the API gateway

| Protocol | 100 | 500 | 2,000 | 5,000 | 10,000 |
| --- | --- | --- | --- | --- | --- |
| HTTP/1.1 | nginx 202k, fit 129k, Traefik 66k, Envoy 60k, Caddy 50k | nginx 199k, fit 136k | nginx 196k, fit 131k | nginx 172k, fit 110k | nginx 135k, fit 107k, Traefik 50k, Envoy 48k, Caddy 37k |
| h2 | nginx 162k, fit 105k, Envoy 72k, Traefik 45k, Caddy 38k | nginx 159k, fit 113k | nginx 161k, fit 114k | nginx 156k, fit 106k | nginx 134k, fit 101k, Envoy 57k, Traefik 40k, Caddy 33k |

nginx leads at every level, fit is second at 64-79% of it, ahead of the others by 1.5 to 3 times.
Peak memory at 10,000 over HTTP/1.1: nginx 213 MiB, fit 497, Envoy 625, Caddy 953, Traefik
2,371 MiB.

### W2, the website

| | HTTPS/1.1 | h2 | HTTP/3 |
| --- | --- | --- | --- |
| `site`, 500 | fit 67.5k, nginx 52.6k, Caddy 28.0k | fit 71.0k, nginx 49.0k, Caddy 19.2k | nginx 87.0k, fit 35.3k, Caddy 19.1k |
| `site`, 2,000 | fit 63.6k, nginx 50.5k, Caddy 26.9k | fit 71.3k, nginx 49.4k, Caddy 18.9k | nginx 70.7k, fit 35.7k, Caddy 17.4k |
| `edge`, 500 | fit 85.0k, nginx 49.3k | fit 87.0k, nginx 46.1k | nginx 77.0k, fit 37.3k |
| `edge`, 2,000 | fit 80.7k, nginx 47.4k | fit 82.3k, nginx 46.7k | nginx 64.6k, fit 36.4k |

The caches answered 99.5-100% of requests themselves. fit keeps files and cache entries in memory
of its own (281-603 MiB serving files over HTTPS/1.1 and h2, up to 1.85 GiB over HTTP/3; 0.8-1.9
GiB as a cache), nginx in the page cache, which the memory columns do not count.

### W3, connection churn

| | 100 connections | 500 connections |
| --- | --- | --- |
| `churn-1` | Envoy 19.0k, fit 18.6k, Caddy 15.6k, nginx 13.2k | Envoy 19.1k, fit 19.0k, Caddy 15.9k, nginx 13.2k |
| `churn-10` | fit 124.5k, nginx 92.2k, Envoy 82.8k, Caddy 82.6k | fit 133.9k, nginx 96.2k, Envoy 88.8k, Caddy 84.8k |

### W4, large transfers

| | HTTP/1.1 | h2 |
| --- | --- | --- |
| `download` | nginx 16.2k, fit 14.3k, Caddy 9.1k | fit 8.1k, nginx 3.9k, Caddy 2.7k |
| `proxy-download` | Envoy 9.0k, Traefik 7.5k, Caddy 6.8k, fit 4.5k, nginx 3.4k | fit 4.7k, Envoy 4.3k, nginx 2.5k, Traefik 2.4k, Caddy 2.3k |
| `proxy-upload` | Traefik 7.1k, Caddy 6.9k, Envoy 6.3k, fit 4.8k, nginx 3.5k | nginx 5.7k, Envoy 4.6k, Traefik 3.7k, Caddy 3.6k, fit 2.7k |

fit's proxied transfers over HTTP/1.1 and its proxied uploads over h2 (47% of the best, 269 MiB of
peak memory) remain the clearest work left for it in this plan. nginx's download over HTTP/1.1, on
0.87 of its CPUs through `sendfile`, is set by the load generators.

### W5, 40,000 idle TLS connections

| Server | HTTP/1.1 | h2 | Peak memory (HTTP/1.1 / h2) |
| --- | --- | --- | --- |
| nginx | 196k | 162k | 1,249 / 1,253 MiB |
| fit | 134k | 112k | 499 / 479 MiB |
| Envoy | 64k | 79k | 856 / 857 MiB |
| Traefik | 61k | 43k | 2,408 / 2,326 MiB |
| Caddy | 50k | 39k | 2,202 / 2,179 MiB |

Every server held all 40,000 and closed none, at W1's throughput.

### W6, latency at a fixed rate

| | 20k a second, p99 | 100k a second, TPS and p99 |
| --- | --- | --- |
| HTTP/1.1 | nginx 2.39 ms, fit 4.49, Envoy 9.34, Traefik 10.16, Caddy 13.01 | nginx 100k 3.17 ms, fit 100k 5.93, Envoy 64k 8.95, Traefik 65k 16.79, Caddy 49k 22.54 |
| h2 | nginx 1.25 ms, fit 2.46, Envoy 3.46, Traefik 5.94, Caddy 6.34 | nginx 100k 4.52 ms, fit 100k 7.59, Envoy 78k 9.20, Traefik 46k 23.61, Caddy 39k 30.60 |

At 100k nginx and fit held the rate on 2.1-3.5 CPUs; the others ran at their ceilings on all 4.

## Compared with the earlier run

Every one of the 232 runs has its twin in the earlier run, made with the same settings:

| Workload | Runs | Median change | Range | Within 5% |
| --- | ---: | ---: | --- | ---: |
| W0 | 80 | −0.1% | −18.6% to +8.3% | 75 |
| W1 | 50 | −0.3% | −3.4% to +12.2% | 49 |
| W2 | 30 | −0.1% | −2.1% to +1.5% | 30 |
| W3 | 16 | +0.1% | −2.6% to +0.4% | 16 |
| W4 | 26 | +0.1% | −1.7% to +22.2% | 24 |
| W5 | 10 | +0.3% | −1.8% to +3.2% | 10 |
| W6 | 20 | 0.0% | −0.4% to +3.5% | 20 |

fit's 58 runs are all within 5% (median −0.3%), Traefik's 20 too. The changes past 5% are nginx's
h2 at 100 in flight and its h2 transfers in W4, all on 10 connections: nginx keeps each connection
on one worker, and 10 connections spread over 4 workers differently from one run to the next. W0's
h2 at 100 used 2.87 of its 4 CPUs here against 3.98 before (−18.6%), W4's proxied h2 transfers 3.99
against 2.99 (+15-22%). The others are Caddy's HTTP/1.1 at 100 (−10%, on 3.42 CPUs against 3.87),
nginx's HTTP/3 at 500 (+8%) and Envoy's h2c at 500 (−7.5%). A table with few connections measures
nginx's spread as much as nginx; the plan's levels from 500 up do not.

## Notes and limits

- **One machine, loopback.** No network latency or NIC limits; the results compare the servers'
  own costs on 4 CPUs.
- **The load generators can be the limit** where a server answers very fast: nginx's HTTP/1.1
  download, fit's 2.2-2.3M a second over h2c and h2, nginx's 1.36M over HTTP/3.
- **Latency over h2c, h2 and HTTP/3** outside W6 is a mean and a maximum, since h2load reports no
  percentiles.
- **The derivation was checked on this machine only.** Its rules are tested over fake machines of
  3 to 64 threads, but this is the only machine it has run on.
