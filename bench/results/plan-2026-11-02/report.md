# Load test plan, 2026-11-02: the report

[English](report.md) | [简体中文](report.zh-CN.md)

nginx, Caddy, Traefik, Envoy and fit under the seven workloads of [the load test
plan](../../README.md#the-load-test-plan), at 100 to 10,000 requests in flight, the server under test on 6
CPUs, the backends on 4 and the load generators on 6 threads: the plan of 2026-10-30 run again with fit
at c8a44e8 on fib 4b24cf3, after the W1 regression's fix and the proxy's allocation work. 232 runs on
the benchmark host, 2026-11-02 09:13 to 10:45 UTC (17:13 to 18:45 in the host's Asia/Hong_Kong): none
failed, none hung, and no run had a single error; they took 1 h 32 min against the 1 h 33 min
estimated. Every table, with each row's change from 2026-10-30, is in [tables.md](tables.md); the plan
derived is [plan.txt](plan.txt), the progress log [plan.log](plan.log).

## In short

- **The W1 regression is gone, and fit's API gateway is faster than it has been.** W1 over HTTP/1.1
  +2.8% to +7.0% at every level (223,853 a second at 500 in flight, 26.58 µs a request from 27.23), over
  h2 +10.0% to +13.6% (226,103 at 500, 26.38 µs from 28.96). W5, the same traffic beside 40,000 idle TLS
  connections, +2.6% and +11.3%, and W6's 200k a second costs 2.5% and 8.6% less CPU a request.
- **Nothing of fit's got slower.** Of its 58 runs, 14 are more than 3% faster than on 2026-10-30 and none
  more than 3% slower; the median is +0.7% in throughput, -0.8% in CPU a request, -0.4% in peak memory.
  The other servers' medians are +0.0% (Envoy) to +1.6% (nginx), so the host compares.
- **Elsewhere:** proxied uploads over HTTP/1.1 +8.9% (9,678 a second, peak memory 109 MiB from 132);
  the website and its cache over HTTP/3 +2.9% to +4.4%; plain responses (W0), connection churn (W3) and
  the other large transfers (W4) within 2.5%.
- **Where fit stands:** first in W0 over h2c and h2 (3.1 to 6.8 times nginx), level with nginx over
  HTTP/1.1 (98-101%) and over HTTP/3 at 100 and 500 in flight; first for the website and its cache over
  HTTPS/1.1 and h2 (122-205% of nginx), for downloads and proxied downloads, and for h2 uploads; second
  to nginx in the API gateway (W1, W5, W6), and behind nginx over HTTP/3 from 2,000 in flight up.
- **What is left for fit:** W1 over HTTP/1.1 is 69-81% of nginx's (26.6 against 19.8 µs a request at
  500); HTTP/3 at 5,000 and 10,000 in flight is 73-75% of nginx's (4.2 against 3.0 µs), and the website
  over HTTP/3 52-68%; W6 over h2 at 30k a second has a p99.9 of 9.64 ms against 3.20 ms before, though its
  p99 fell from 2.30 to 1.50 ms.

## What changed since 2026-10-30

| | 2026-10-30 | 2026-11-02 |
| --- | --- | --- |
| fit | 24987e5 | c8a44e8, 48 commits on |
| fib | 7dcb41da | 4b24cf3, 199 commits on |
| Go | 1.27.3 | 1.27.3 |
| Beside the plan on the host | a kind node and an etcd container, about 0.05 of a CPU | nothing: see below |

The changes that reach the plan's paths, from the [W1 regression's
bisection](../plan-2026-10-30/report.md#the-w1-regression-found-and-fixed-2026-10-31) on:

- fib `46827ea7`: a small whole response body is taken from the pool and handed back once sent
  (`ClientResponse.TakeBody`, `Context.WriteResponseOwned`), and a buffered request body is taken as it is
  ([A48](../../../docs/fib-api-requests.md)); fit `ff5e02d` passes whole bodies through fib's client
  without a copy.
- fit `d62fe07`: a proxy exchange is reused once its request is done, the 21% of the bytes a proxied
  request allocated that the 2026-10-31 fix left.
- fib: an HTTP/2 stream gives its pooled body back once framed (fit `e5e00ce`); h2c response bodies are
  sent beside their DATA frames (`11e984b`); an HTTP/3 stream is held until its writable callback has run,
  QPACK's decoder remembers each string by its place, and a long header literal a peer repeats unindexed
  is decoded once.
- The rest, WebRTC, media streaming, the TURN server, the workbench console, the llm and tunnel roles, is
  outside the paths the plan runs.

## The test environment

The machine, the servers' images (at the same digests), the load generators and the host's kernel
settings are the 2026-10-30 plan's: `host-settings.sh -check` from that plan found every setting the
same but `rmem_max` and `wmem_max` (16 MiB), which `plan.sh` sets back to 7,500,000 before every run, as
the log records. fit was built from a clean tree at c8a44e8 for linux/amd64.

Before the run the host was cleared of what earlier work had left on it:

- 81 test servers from fib's and fit's A/B runs a week before (`/tmp/sm-*`, `/tmp/lb-*`,
  `/tmp/fib-stallmem/*`, `/root/dsh/ab/*`), idle, and an ACME test server (`pebble-challtestsrv`);
- the kind node `fitconf-control-plane` and the etcd container the 2026-10-30 plan ran beside (stopped;
  the etcd container, started with `--rm`, is gone);
- two fibhub processes started by hand 15 days before;
- `strata.service`, an LLM inference server holding 49 GiB of the host's 62 GiB and listening on 0.0.0.0:8080,
  the port the servers under test bind; it was stopped for the run and started again after it.

What ran beside the plan was the desktop session (gnome-shell, about 1.5% of a CPU) and `fit` as an llm
node on 192.168.100.51:9100, idle with its inference server stopped.

## Against 2026-10-30, server by server

Each row compares the runs both plans made (skipped scenarios left out); a change is counted past ±3%.

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | +0.7% | -0.8% | -0.4% | 14 | 0 |
| nginx | 58 | +1.6% | -1.6% | -0.0% | 11 | 5 |
| Caddy | 52 | +0.7% | -0.7% | +0.6% | 0 | 0 |
| Envoy | 44 | +0.0% | +0.1% | +0.2% | 9 | 0 |
| Traefik | 20 | +0.9% | -0.9% | +1.0% | 0 | 0 |

nginx's outliers are again at its low-concurrency h2c and h2 (+38% and +14% at 100 in flight) and its
W2 and W6 over h2 (-7% to -12% and +13.7%), the runs that moved most between earlier plans too; its
HTTP/1.1 and its many-connection runs are within 3.5%.

## fit, run by run

Throughput, CPU a request and peak memory against 2026-10-30; the last column is the run's p99 (wrk,
HTTP/1.1) or mean (h2load) latency.

| Run | Scenario | Protocol | 10-30 | 11-02 | Change | µs a request | Peak MiB | Latency ms |
| --- | --- | --- | --: | --: | --: | --: | --: | --: |
| W0 100 | fixed | HTTP/1.1 | 666,772 | 679,893 | +2.0% | 8.88→8.69 | 13→14 | 0.23 |
| W0 100 | fixed | h2c | 1,495,103 | 1,499,691 | +0.3% | 3.84→3.84 | 12→12 | 0.06 |
| W0 100 | fixed | h2 | 1,339,072 | 1,351,063 | +0.9% | 4.18→4.17 | 11→12 | 0.06 |
| W0 100 | fixed | HTTP/3 | 1,102,324 | 1,108,503 | +0.6% | 4.30→4.31 | 18→18 | 0.09 |
| W0 500 | fixed | HTTP/1.1 | 671,426 | 683,740 | +1.8% | 8.84→8.68 | 15→21 | 0.94 |
| W0 500 | fixed | h2c | 2,565,803 | 2,553,234 | -0.5% | 2.33→2.34 | 20→18 | 0.17 |
| W0 500 | fixed | h2 | 2,408,449 | 2,405,186 | -0.1% | 2.48→2.48 | 18→18 | 0.18 |
| W0 500 | fixed | HTTP/3 | 1,693,696 | 1,684,797 | -0.5% | 3.48→3.51 | 24→22 | 0.29 |
| W0 10,000 | fixed | HTTP/1.1 | 608,012 | 619,382 | +1.9% | 8.79→8.58 | 39→40 | 16.48 |
| W0 10,000 | fixed | h2c | 3,450,552 | 3,405,132 | -1.3% | 1.73→1.75 | 68→60 | 2.68 |
| W0 10,000 | fixed | h2 | 3,238,237 | 3,202,187 | -1.1% | 1.84→1.86 | 65→61 | 2.78 |
| W0 10,000 | fixed | HTTP/3 | 1,413,512 | 1,405,500 | -0.6% | 4.20→4.23 | 150→147 | 5.27 |
| W1 100 | api | HTTP/1.1 | 200,928 | 212,094 | **+5.6%** | 29.42→28.00 | 20→18 | 2.95 |
| W1 100 | api | h2 | 182,170 | 206,961 | **+13.6%** | 32.52→28.77 | 22→24 | 0.47 |
| W1 500 | api | HTTP/1.1 | 217,839 | 223,853 | +2.8% | 27.23→26.58 | 65→49 | 7.65 |
| W1 500 | api | h2 | 205,586 | 226,103 | **+10.0%** | 28.96→26.38 | 64→69 | 2.20 |
| W1 2,000 | api | HTTP/1.1 | 209,064 | 215,901 | **+3.3%** | 28.35→27.50 | 174→149 | 20.08 |
| W1 2,000 | api | h2 | 204,532 | 228,054 | **+11.5%** | 29.14→26.17 | 164→172 | 8.73 |
| W1 5,000 | api | HTTP/1.1 | 176,781 | 189,185 | **+7.0%** | 33.46→31.32 | 438→352 | 56.19 |
| W1 5,000 | api | h2 | 192,597 | 216,810 | **+12.6%** | 30.91→27.51 | 305→304 | 22.99 |
| W1 10,000 | api | HTTP/1.1 | 158,328 | 166,461 | **+5.1%** | 37.29→35.54 | 664→652 | 136.95 |
| W1 10,000 | api | h2 | 172,711 | 193,607 | **+12.1%** | 34.40→30.70 | 643→664 | 51.43 |
| W2 500 | site | HTTPS/1.1 | 98,552 | 99,846 | +1.3% | 58.32→57.54 | 191→192 | 18.39 |
| W2 500 | site | h2 | 113,411 | 113,468 | +0.0% | 52.20→52.16 | 251→250 | 4.11 |
| W2 500 | site | HTTP/3 | 64,767 | 67,456 | **+4.2%** | 91.54→87.87 | 210→207 | 7.38 |
| W2 500 | edge | HTTPS/1.1 | 131,712 | 132,943 | +0.9% | 45.18→44.74 | 1,368→1,371 | 9.00 |
| W2 500 | edge | h2 | 131,878 | 132,578 | +0.5% | 44.96→44.70 | 1,084→1,052 | 3.31 |
| W2 500 | edge | HTTP/3 | 71,032 | 73,088 | +2.9% | 82.73→80.59 | 925→872 | 6.81 |
| W2 2,000 | site | HTTPS/1.1 | 95,408 | 96,272 | +0.9% | 59.92→59.46 | 229→234 | 63.30 |
| W2 2,000 | site | h2 | 114,327 | 113,359 | -0.8% | 51.75→52.11 | 456→465 | 17.01 |
| W2 2,000 | site | HTTP/3 | 67,481 | 70,392 | **+4.3%** | 87.83→84.20 | 346→360 | 28.34 |
| W2 2,000 | edge | HTTPS/1.1 | 95,408 | 93,832 | -1.7% | 51.81→51.83 | 1,437→1,429 | 62.23 |
| W2 2,000 | edge | h2 | 125,129 | 125,552 | +0.3% | 47.32→47.19 | 885→877 | 15.07 |
| W2 2,000 | edge | HTTP/3 | 73,355 | 76,564 | **+4.4%** | 80.84→77.43 | 491→490 | 26.10 |
| W3 100 | churn-1 | HTTPS/1.1 | 19,207 | 19,255 | +0.2% | 244.86→243.05 | 17→16 | 2.67 |
| W3 100 | churn-10 | HTTPS/1.1 | 131,712 | 132,374 | +0.5% | 33.62→33.57 | 17→14 | 1.12 |
| W3 500 | churn-1 | HTTPS/1.1 | 19,558 | 19,558 | +0.0% | 236.62→235.91 | 21→28 | 8.24 |
| W3 500 | churn-10 | HTTPS/1.1 | 136,052 | 135,850 | -0.1% | 32.42→32.39 | 21→19 | 3.37 |
| W4 100 | download | HTTP/1.1 | 17,676 | 18,127 | +2.5% | 146.27→146.72 | 12→14 | 5.54 |
| W4 100 | download | h2 | 12,344 | 12,281 | -0.5% | 479.63→481.92 | 69→71 | 7.42 |
| W4 100 | proxy-download | HTTP/1.1 | 14,411 | 14,539 | +0.9% | 414.31→408.49 | 34→34 | 15.82 |
| W4 100 | proxy-download | h2 | 9,443 | 9,480 | +0.4% | 633.27→630.89 | 69→70 | 10.21 |
| W4 100 | proxy-upload | HTTP/1.1 | 8,884 | 9,678 | **+8.9%** | 665.99→606.42 | 132→109 | 25.65 |
| W4 100 | proxy-upload | h2 | 7,103 | 7,182 | +1.1% | 841.66→832.58 | 178→184 | 13.14 |
| W5 500 | api + idle | HTTP/1.1 | 214,146 | 219,792 | +2.6% | 27.76→27.03 | 475→438 | 10.07 |
| W5 500 | api + idle | h2 | 199,432 | 221,954 | **+11.3%** | 29.90→26.88 | 484→453 | 2.24 |

W0 at 2,000 and 5,000 in flight, left out above, are within 1.6% of 2026-10-30 for every protocol
([tables.md](tables.md)); fit's peak memory over HTTP/1.1 at 5,000 was 87 MiB against 27, the
largest rise of its runs in proportion, still half nginx's 172.

## W6, latency at a fixed rate

The rates calibrated from W1 at 500 came out as on 2026-10-30, 30,000 and 200,000 a second: Caddy the
slowest at 58,359, Caddy, Traefik and Envoy at most 118,616, fit and nginx at least 223,853.

| Rate | Protocol | fit p50 | fit p99 (10-30) | fit p99.9 (10-30) | fit µs a request (10-30) | nginx p99 | Envoy p99 |
| --- | --- | --: | --: | --: | --: | --: | --: |
| 30,000 | HTTP/1.1 | 1.16 ms | 2.92 (3.14) | 4.73 (4.32) | 33.84 (33.58) | 1.69 | 6.41 |
| 30,000 | h2 | 0.44 ms | **1.50** (2.30) | **9.64** (3.20) | 33.87 (36.50) | 2.00 | 3.69 |
| 200,000 | HTTP/1.1 | 1.53 ms | 6.14 (6.08) | 10.40 (10.58) | 26.83 (27.52) | 2.12 | (92,386 a second) |
| 200,000 | h2 | 1.48 ms | 5.19 (5.68) | 7.87 (7.64) | 26.69 (29.21) | 3.78 (176,581 a second) | (118,587 a second) |

At 200,000 a second fit is the only server that keeps the rate over both protocols: nginx keeps it over
HTTP/1.1 but answers 176,581 over h2, and Caddy, Traefik and Envoy fall far behind it (58k to 119k). At
30,000 fit has the lowest p99 over h2 of any server, 1.50 ms; its p99.9 there, 9.64 ms against 3.20 ms
on 2026-10-30, is the one figure in this plan that is worse, from a single 15s run, and worth an A/B
before it is called a regression.

## Where fit stands

fit against the fastest other server in each table (throughput; at its level).

| Workload | fit's place | fit against the best other |
| --- | --- | --- |
| W0, HTTP/1.1 | 1st or 2nd of 4 | 98-101% of nginx, at 14-87 MiB against nginx's 170-175 |
| W0, h2c and h2 | 1st | 2.1-6.8 times nginx's |
| W0, HTTP/3 | 1st at 500, 2nd elsewhere | 95-100% of nginx at 100 and 500; 73-85% at 2,000 to 10,000 (4.2 against 3.0 µs a request) |
| W1, HTTP/1.1 | 2nd of 5 | 69-81% of nginx's (26.6 against 19.8 µs a request at 500); 2.1-2.5 times Traefik's, the third |
| W1, h2 | 2nd of 5 | 93-95% of nginx's; 1.9-2.5 times Envoy's, the third |
| W2, site and edge, HTTPS/1.1 and h2 | 1st | 122-205% of nginx's |
| W2, HTTP/3 | 2nd | 52-82% of nginx's |
| W3, churn | 1st or 2nd | 99-103% of the best (nginx or Caddy), at less CPU a request (236 against 290 µs for nginx at 500) |
| W4, downloads, proxied downloads | 1st | 105-247% of the best other |
| W4, proxied uploads | 3rd over HTTP/1.1, 1st over h2 | 98% of Caddy's; 107% of nginx's |
| W5, with 40,000 idle TLS connections | 2nd of 5 | 73% and 87% of nginx's, at a third of its memory (438 and 453 against 1,304 and 1,313 MiB) |

fit holds the least memory, or close to it, nearly everywhere. The exceptions are the cache scenario
(`edge`, 0.9-1.4 GiB, its 1 GiB cache filling as designed) and W1 at 5,000 and 10,000 in flight, where
fit's 352-664 MiB is above nginx's 217-437.

## Notes and limits

- **One machine, loopback.** No network latency or NIC limits; the results compare the servers' own costs
  on 6 CPUs, which are 3 physical cores with both their threads.
- **One run of each.** A change of a few percent in a single run is within run-to-run noise. The W1 and
  W5 gains over h2 (10-14%, at every level) and over HTTP/1.1 (3-7%, at every level) are well past it.
- **The host was quieter than on 2026-10-30.** The kind node and the etcd container no longer ran beside
  the plan. Their 0.05 of a CPU was on no pinned set, and the other servers' medians moved by 0.0% to
  1.6%, so this changes little; nginx's +1.6% is the most it could account for.
- **The load generators can be the limit** where a server answers very fast: fit's h2c and h2 at
  3.2-3.4M a second, nginx's HTTP/1.1 download, and W3's `churn-1`.
- **Latency over h2c, h2 and HTTP/3** outside W6 is a mean and a maximum, since h2load reports no
  percentiles.
