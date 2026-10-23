# Connection sweep, 2026-10-23

[English](sweep-report-2026-10-23.md) | [简体中文](sweep-report-2026-10-23.zh-CN.md)

Every server through every scenario and protocol at seven connection counts, from 16 to 32,768, on
the benchmark host. fit at e079c83 on fib b0809b4 (with A42 and A43), against nginx, Caddy, Traefik
and Envoy, with the v1.0.0 report's harness and settings.

| Level | HTTP/1.1 connections (wrk) | h2c, h2, HTTP/3 connections × streams (h2load) | Requests in flight, h2c/h2/HTTP/3 |
| --- | ---: | ---: | ---: |
| 1 | 16 | 8 × 32 | 256 |
| 2 | 64 | 16 × 32 | 512 |
| 3 | 256 | 64 × 32 | 2,048 (the harness's default) |
| 4 | 1,024 | 256 × 32 | 8,192 |
| 5 | 4,096 | 1,024 × 32 | 32,768 |
| 6 | 16,384 | 4,096 × 32 | 131,072 |
| 7 | 32,768 | 8,192 × 32 | 262,144 |

In summary:

- **fixed, static and the cache: fit leads at every level** but fixed over HTTP/1.1 and HTTP/3,
  where nginx leads: by 1–4% over HTTP/1.1 from 64 connections up (14% at 16), and over HTTP/3 from
  64×32 up (fit leads at 8×32 and 16×32). Over h2c and h2, fit answers 2.6 to 7 times what the next
  server does at every level.
- **Every server peaks between levels 3 and 5, and loses at 6 and 7.** fit's h2c and h2 peak at
  256×32 to 1,024×32 (fixed 3.93M and 3.66M) and lose 25–30% by 8,192×32; its HTTP/1.1 holds 770k up
  to 4,096 connections and falls to 460k at 32,768, as nginx's does. nginx keeps more of its peak
  over h2c and h2 (it rises to 660k and 1.01M there); Envoy loses a third of its own.
- **fit's proxy breaks down past 5,120 requests in flight, and stalls past 131,072.** The bench
  config caps it there (`max_conns` 4,096 to the backend and the default `max_pending` of 1,024),
  and it answers the rest 503, as it should; but its successful responses then fall far below what
  the backend can give (77k a second at 8,192 in flight over h2c, against nginx's 206k), and over
  h2c and h2 at levels 6 and 7 it answers nothing at all in 30 s, with its CPU idle (over h2 already
  at 1,024×32). Over HTTP/1.1 the same happens past 4,096 connections.
- **Every proxy fails at levels 6 and 7 over h2c and h2**: nginx, Caddy, Traefik and Envoy answer a
  few hundred to a few thousand requests a second, nearly all 5xx, or hang the load generator (eight
  runs were ended as hung). Over HTTP/1.1 nginx keeps 201k at 32,768 connections, Envoy 109k.
- **Memory under load**: at level 7 fit peaks at 83 MiB over HTTP/1.1 and 1.0–1.4 GiB over the
  multiplexed protocols on fixed; nginx stays at 214–599 MiB but reaches 7.1 GiB over HTTP/3; Envoy
  reaches 14.1 GiB over HTTP/3, Traefik 12.4 GiB on proxy over h2c.
- **fit's goroutines** grow with streams in flight, not connections: on fixed, 110–350 over HTTP/1.1
  at every level; 590 at 8×32 to 20,635 at 8,192×32 over h2c.

Results: one JSON per level, `sweep-2026-10-23-l<N>-c<C>-h<H>.json` (level, HTTP/1.1 connections,
and the others' connections), from [`sweep-2026-10-23-l1-c16-h8.json`](sweep-2026-10-23-l1-c16-h8.json)
to [`sweep-2026-10-23-l7-c32768-h8192.json`](sweep-2026-10-23-l7-c32768-h8192.json). Every level's
full tables are in [the appendix](sweep-2026-10-23-levels.md).

## Setup

| | |
| --- | --- |
| Host | AMD Ryzen 7 7800X3D, 8 cores, 16 threads; 62 GiB; Ubuntu 24.04, Linux 7.0.0-38 |
| Docker | 29.1.3, host networking |
| CPUs | server 0-6, load generators 7-13, backends 14-15 |
| Runs | 30s after a 5s warm-up, each on a freshly started server; every scenario (fixed, static, proxy, cache-hot, cache-zipf) and protocol (HTTP/1.1, h2c, h2, HTTP/3); no idle connection runs |
| Servers | nginx 1.31.4, Caddy 2.10.2, Traefik 3.5.0, Envoy 1.40.0, fit at e079c83 on fib b0809b4, Go 1.27.3 |
| Load generators | wrk 4.1.0 (HTTP/1.1), h2load 1.59.0 (h2c, h2), h2load 1.68.0 built with HTTP/3 |
| Time | 2026-10-23 11:35 to 18:34 UTC, about 7 hours |

The servers' configs are the v1.0.0 report's. The ones that matter for the proxy at high counts:
nginx keeps up to 4,096 idle connections to the backend (`keepalive`) and opens as many as it needs;
Caddy and Traefik keep 4,096 idle and open as many; Envoy's circuit breakers allow a million; fit
opens at most 4,096 (`max_conns`) and queues 1,024 more (`max_pending`, its default), answering 503
past that. The backend is fit on 2 CPUs, answering about 260k requests a second.

**What happened during the run.**

- **Hung runs.** The harness waits for each load generator without a time limit. At level 6,
  Traefik's proxy over h2 wedged h2load for 1 h 40 min; from then on a watchdog ended any wrk or
  h2load past 300 s, and the run counts as hung. Eight runs hung, all proxy or static over h2c or h2
  at levels 6 and 7 (listed [below](#failures)). They were not run again.
- **Servers that did not start.** The host's ephemeral port range was 1024–65535, which covers the
  servers' own ports: at high counts an outgoing connection could hold one, and the next server
  could not bind it. Traefik did not start for the proxy at levels 4 and 5 (port 8444), nor fit at
  any run of level 6 (its Admin API's 127.0.0.1:9005). With the user's agreement the host now
  reserves those ports (`net.ipv4.ip_local_reserved_ports = 8080-8081,8443-8445,9000-9005`, in
  `/etc/sysctl.d/91-fitbench-reserved-ports.conf`), set during level 6; level 7 had no such failure.
  Those servers ran again at their levels once the sweep was done, and their results replace the
  failed ones in the level's JSON (its `reruns` field says which).
- **The host.** `somaxconn` and `tcp_max_syn_backlog` are 2,048, which caps every server's accept
  queue; SYN cookies are on, so connections that overflow it still open, later. The `fitconf` kind
  node and an idle etcd container stayed up. Nothing else ran on the host.

## Scaling

One table for each scenario and protocol, with a row for each server and a column for each level. A
level's cell is the server's **successful** responses a second there, in thousands, and their share
of that level's best: responses the load generator counted as errors (5xx, timeouts) are not
counted. The rows are ranked by the most the server answered at any level (Peak TPS), then by its
CPU efficiency there (successful responses per CPU-second). **hung**: the load generator was ended
after 300 s; **0**: it finished with nothing answered; —: the server does not serve the scenario.

### fixed, HTTP/1.1

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| nginx  |  795,372 100% |         125,387 100% | 562k 100% | 785k 100% | 795k 100% | 792k 100% | 785k 100% | 669k 100% | 465k 100% |
| fit    |  770,809  96% |         125,022  99% | 483k  85% | 752k  95% | 765k  96% | 767k  96% | 771k  98% | 661k  98% | 458k  98% |
| envoy  |  410,423  51% |          58,850  46% | 224k  39% | 369k  47% | 390k  48% | 410k  51% | 365k  46% | 237k  35% | 245k  52% |
| caddy  |  367,082  46% |          54,597  43% | 308k  54% | 333k  42% | 343k  43% | 367k  46% | 351k  44% | 301k  44% | 294k  63% |

### fixed, h2c

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| fit    | 3,931,497 100% |         564,181 100% | 1,993k 100% | 2,941k 100% | 3,809k 100% | 3,881k 100% | 3,931k 100% | 3,068k 100% | 2,884k 100% |
| envoy  |   897,971  22% |         128,605  22% |   689k  34% |   828k  28% |   898k  23% |   873k  22% |   668k  16% |   611k  19% |   601k  20% |
| nginx  |   659,968  16% |          94,816  16% |   267k  13% |   430k  14% |   624k  16% |   633k  16% |   628k  15% |   646k  21% |   660k  22% |
| caddy  |   226,358   5% |          33,825   5% |   226k  11% |   213k   7% |   220k   5% |   223k   5% |   212k   5% |   201k   6% |   189k   6% |

### fixed, h2

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| fit    | 3,662,259 100% |         526,311 100% | 1,793k 100% | 2,715k 100% | 3,533k 100% | 3,532k 100% | 3,662k 100% | 2,871k 100% | 2,657k 100% |
| nginx  | 1,010,984  27% |         147,018  27% |   267k  14% |   457k  16% |   925k  26% |   931k  26% |   925k  25% |   893k  31% | 1,011k  38% |
| envoy  |   875,861  23% |         125,395  23% |   556k  30% |   727k  26% |   876k  24% |   843k  23% |   643k  17% |   591k  20% |   575k  21% |
| caddy  |   230,570   6% |          33,698   6% |   229k  12% |   216k   7% |   224k   6% |   231k   6% |   215k   5% |   207k   7% |   195k   7% |

### fixed, HTTP/3

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| nginx  | 2,278,627 100% |         344,100 100% |   267k  20% |   533k  32% | 2,133k 100% | 2,279k 100% | 1,478k 100% | 1,282k 100% | 1,181k 100% |
| fit    | 1,768,651  77% |         259,932  75% | 1,279k 100% | 1,655k 100% | 1,769k  82% | 1,635k  71% | 1,422k  96% | 1,119k  87% | 1,033k  87% |
| caddy  |   381,836  16% |          55,975  16% |   323k  25% |   347k  20% |   382k  17% |   348k  15% |   353k  23% |   325k  25% |   309k  26% |
| envoy  |   322,457  14% |          46,110  13% |   284k  22% |   314k  18% |   322k  15% |   307k  13% |   303k  20% |   303k  23% |   285k  24% |

### static, HTTP/1.1

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| fit    |  758,011 100% |         109,981 100% | 456k 100% | 737k 100% | 758k 100% | 751k 100% | 725k 100% | 611k 100% | 441k 100% |
| nginx  |  436,996  57% |          62,519  56% | 334k  73% | 437k  59% | 436k  57% | 435k  57% | 433k  59% | 400k  65% | 384k  87% |
| caddy  |  160,532  21% |          23,545  21% | 153k  33% | 161k  21% | 160k  21% | 156k  20% | 146k  20% | 133k  21% | 134k  30% |

### static, h2c

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| fit    | 2,493,318 100% |         357,596 100% | 1,533k 100% | 2,005k 100% | 2,444k 100% | 2,493k 100% | 2,381k 100% | 1,924k 100% | 1,788k 100% |
| nginx  |   374,049  15% |          53,637  14% |   219k  14% |   342k  17% |   368k  15% |   365k  14% |   374k  15% |   370k  19% |   374k  20% |
| caddy  |   125,235   5% |          18,427   5% |   123k   8% |   120k   5% |   125k   5% |   125k   5% |   120k   5% |   115k   5% |   109k   6% |

### static, h2

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| fit    | 2,122,361 100% |         304,358 100% | 1,288k 100% | 1,715k 100% | 2,085k 100% | 2,122k 100% | 2,001k 100% | 1,677k 100% | 1,560k 100% |
| nginx  |   468,701  22% |          67,248  22% |   206k  15% |   353k  20% |   452k  21% |   457k  21% |   460k  22% |   464k  27% |   469k  30% |
| caddy  |   123,683   5% |          17,998   5% |   122k   9% |   119k   6% |   124k   5% |   123k   5% |   119k   5% |   114k   6% |        hung |

### static, HTTP/3

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |       16×32 |       64×32 |    256×32 |  1,024×32 |  4,096×32 |  8,192×32 |
| ------ | -------------: | -------------------: | --------: | ----------: | ----------: | --------: | --------: | --------: | --------: |
| fit    | 1,119,827 100% |         162,795 100% | 873k 100% | 1,102k 100% | 1,120k 100% | 938k 100% | 856k 100% | 782k 100% | 745k 100% |
| nginx  |   767,976  68% |         109,965  67% | 267k  30% |   477k  43% |   768k  68% | 757k  80% | 643k  75% | 589k  75% | 548k  73% |
| caddy  |   160,616  14% |          23,322  14% | 143k  16% |   153k  13% |   161k  14% | 144k  15% | 148k  17% | 143k  18% | 139k  18% |

### proxy, HTTP/1.1

| Server  | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------- | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| nginx   |  258,527 100% |          54,811 100% | 161k 100% | 255k 100% | 258k 100% | 259k 100% | 246k 100% | 208k 100% | 201k 100% |
| fit     |  246,835  95% |          42,300  77% | 132k  81% | 202k  78% | 243k  94% | 247k  95% | 224k  90% |  76k  36% |  14k   6% |
| envoy   |  156,808  60% |          24,459  44% |  96k  59% | 118k  46% | 138k  53% | 157k  60% | 129k  52% | 107k  51% | 109k  54% |
| traefik |  155,397  60% |          23,840  43% | 121k  74% | 137k  53% | 151k  58% | 155k  60% | 133k  53% |  94k  45% |  41k  20% |
| caddy   |  127,445  49% |          19,174  34% | 105k  65% | 119k  46% | 127k  49% | 126k  48% | 100k  40% |  76k  36% |  36k  17% |

### proxy, h2c

| Server  | Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |     64×32 |    256×32 |  1,024×32 | 4,096×32 | 8,192×32 |
| ------- | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | -------: | -------: |
| nginx   |  252,889 100% |          46,005 100% | 210k  90% | 253k 100% | 252k 100% | 206k 100% | 201k 100% |        0 |     hung |
| fit     |  237,387  93% |          38,575  83% | 232k 100% | 235k  92% | 237k  94% |  77k  37% |  19k   9% |        0 |        0 |
| envoy   |  233,334  92% |          33,968  73% | 159k  68% | 173k  68% | 233k  92% | 175k  84% | 148k  73% |        0 |        0 |
| traefik |  120,946  47% |          17,555  38% | 121k  52% | 115k  45% | 112k  44% |  98k  47% |  30k  14% |  3k  52% |  4k 100% |
| caddy   |  103,970  41% |          15,412  33% | 104k  44% | 100k  39% |  93k  36% |  82k  39% |  28k  14% |  5k 100% |     hung |

### proxy, h2

| Server  | Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |     64×32 |    256×32 |  1,024×32 | 4,096×32 | 8,192×32 |
| ------- | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | -------: | -------: |
| nginx   |  280,991 100% |          58,161 100% | 203k  89% | 281k 100% | 242k 100% | 200k 100% | 198k 100% |  1k   8% |     hung |
| fit     |  231,082  82% |          37,258  64% | 227k 100% | 231k  82% | 229k  94% | 177k  88% |   0k   0% |        0 |        0 |
| envoy   |  226,198  80% |          33,438  57% | 160k  70% | 167k  59% | 226k  93% | 172k  86% | 149k  75% |  0k   1% |     hung |
| traefik |  120,673  42% |          17,454  30% | 121k  53% | 114k  40% | 110k  45% |  97k  48% |  33k  16% |     hung |     hung |
| caddy   |  101,823  36% |          15,080  25% | 102k  44% |  97k  34% |  91k  37% |  80k  40% |  29k  14% |  6k 100% |     hung |

### proxy, HTTP/3

| Server  | Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |     64×32 |    256×32 |  1,024×32 |  4,096×32 |  8,192×32 |
| ------- | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| nginx   |  266,358 100% |          83,403 100% | 201k  93% | 234k  94% | 266k 100% | 214k 100% | 183k 100% | 117k 100% |  93k  84% |
| fit     |  245,864  92% |          48,129  57% | 216k 100% | 246k 100% | 239k  89% |  89k  41% |  18k   9% |         0 |         0 |
| traefik |  137,760  51% |          19,932  23% | 131k  60% | 138k  56% | 136k  51% | 133k  62% | 123k  67% | 116k  98% | 111k 100% |
| caddy   |  112,820  42% |          16,794  20% | 109k  50% | 113k  45% | 105k  39% |  82k  38% | 102k  55% |  98k  83% |  97k  87% |
| envoy   |   74,919  28% |          10,733  12% |  51k  23% |  55k  22% |  74k  27% |  73k  34% |  75k  40% |  75k  63% |  66k  59% |

### cache-hot, HTTP/1.1

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| fit    |  688,599 100% |          99,758 100% | 421k 100% | 665k 100% | 685k 100% | 689k 100% | 682k 100% | 558k 100% | 419k 100% |
| nginx  |  330,374  47% |          47,321  47% | 258k  61% | 330k  49% | 330k  48% | 327k  47% | 324k  47% | 306k  54% | 298k  71% |

### cache-hot, h2c

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |  8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | --------: |
| fit    | 1,643,692 100% |         236,070 100% | 1,091k 100% | 1,321k 100% | 1,562k 100% | 1,644k 100% | 1,400k 100% | 1,207k 100% | 802k 100% |
| nginx  |   291,635  17% |          41,771  17% |   184k  16% |   263k  19% |   285k  18% |   283k  17% |   292k  20% |   289k  23% | 287k  35% |

### cache-hot, h2

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |       64×32 |      256×32 |    1,024×32 |  4,096×32 |  8,192×32 |
| ------ | -------------: | -------------------: | --------: | --------: | ----------: | ----------: | ----------: | --------: | --------: |
| fit    | 1,189,318 100% |         171,245 100% | 807k 100% | 986k 100% | 1,148k 100% | 1,189k 100% | 1,043k 100% | 924k 100% | 856k 100% |
| nginx  |   315,456  26% |          45,191  26% | 211k  26% | 287k  29% |   312k  27% |   315k  26% |   313k  30% | 312k  33% | 309k  36% |

### cache-hot, HTTP/3

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |     64×32 |    256×32 |  1,024×32 |  4,096×32 |  8,192×32 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| fit    |  720,439 100% |         107,164 100% | 595k 100% | 720k 100% | 637k 100% | 547k 100% | 504k 100% | 476k 100% | 448k 100% |
| nginx  |  500,388  69% |          71,660  66% | 267k  44% | 375k  52% | 500k  78% | 473k  86% | 432k  85% | 401k  84% | 377k  84% |

### cache-zipf, HTTP/1.1

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| fit    |  644,014 100% |          92,893 100% | 385k 100% | 625k 100% | 644k 100% | 630k 100% | 584k 100% | 501k 100% | 399k 100% |
| nginx  |  312,461  48% |          44,721  48% | 213k  55% | 312k  50% | 312k  48% | 309k  49% | 301k  51% | 281k  56% | 274k  68% |

## Notes

**fixed, static and the cache.** Every server is fastest between levels 3 and 5: 16 HTTP/1.1
connections or 8×32 streams cannot keep 7 CPUs busy (fit used 4.1–6.9 cores at level 1, nginx
0.6–5.7), and past 4,096 connections or 1,024×32 streams the work of so many connections and streams
grows faster than what they carry. On fixed by level 7, nginx and fit both lose about 41% of their
HTTP/1.1 peak; over h2 nginx loses nothing, rising to its best at 8,192×32, while fit loses 27%.
Over HTTP/3, nginx overtakes fit from 64×32 on fixed, while fit leads at every level on static and
the cache.

**The proxy.** At levels 1–3 the backend, about 260k requests a second on its 2 CPUs, is what every
fast proxy reaches. Past that:

- nginx keeps 200k over HTTP/1.1 from 64 connections up, and over h2c and h2 up to 1,024×32; at
  4,096×32 its few answers are 5xx, and at 8,192×32 it hangs h2load.
- Envoy keeps 150k up to 1,024×32 over h2c and h2, then answers only 5xx; over HTTP/3 it holds
  51–75k at every level.
- Caddy and Traefik lose three quarters of their rate by 1,024×32 and answer a few thousand a second
  at 4,096×32; over HTTP/3 they hold 82–138k at every level, as nginx holds 93k or more.
- fit: below.

## fit's proxy past its limits

fit's proxy may hold 4,096 connections to the backend and 1,024 requests waiting for one: 5,120
requests in flight. Past that it answers 503 at once, which is its rule 11 at work, and which nginx,
holding no such bound, does not do. What goes wrong is what happens to the rest:

| Run | In flight | Successful a second | Answered 5xx | CPU | Goroutines |
| --- | ---: | ---: | ---: | ---: | ---: |
| HTTP/1.1, level 5 | 4,096 | 224k | 0% | 5.7 | 1,030 |
| h2c, level 4 | 8,192 | 77k | 75% | 5.7 | 8,086 |
| h2, level 5 | 32,768 | 30 | 99.98% | 2.0 | 14,749 |
| HTTP/1.1, level 6 | 16,384 | 76k | 68% | 4.5 | 11,624 |
| h2c, level 6 | 131,072 | none answered | — | 0.02 | 21,191 |
| h2, level 7 | 262,144 | none answered | — | 0.08 | 25,223 |

With 4,096 connections to a backend that answers 260k a second, fit should go on answering about
that many while it turns the rest away. Instead its successful responses fall to a third, then to
none, and over h2c and h2 from level 6 it answers nothing for 30 seconds, not even 503, with its CPU
idle and 21–25k goroutines alive: requests waiting on something that does not come.

The load generators count status classes, not codes; by fit's config, its 5xx past the bound are the
503s of `max_pending`.

## Failures

**Hung** (the load generator ended after 300 s; recorded, not run again):

| Level | Server | Runs |
| --- | --- | --- |
| 6 | traefik | proxy over h2 |
| 7 | nginx | proxy over h2c and h2 |
| 7 | caddy | static over h2; proxy over h2c and h2 |
| 7 | traefik | proxy over h2 |
| 7 | envoy | proxy over h2 |

**No responses** (the load generator finished with nothing answered): fit's proxy over h2c and h2 at
levels 6 and 7, and Envoy's over h2c at level 7.

**Did not start, run again**: Traefik's proxy at levels 4 and 5 and every fit run at level 6, as
above.

## What to look at in fit

1. **The proxy's stall over h2c and h2** past 131,072 streams in flight: no response, not even a
   503, in 30 s, with 21–25k goroutines and no CPU. A request that cannot be served must be answered
   (rules 1 and 11); find what they wait on. A first suspect is backpressure: the 256 MiB bound on
   responses waiting for clients (`max_pending_bytes`), which stops every connection's reads once
   passed, never draining.
2. **The proxy's successful rate under overflow**: with its 5,120 requests in flight, fit should
   keep serving the backend's rate while it answers the rest 503, not fall to a third or less.
   Measure where the time goes once `max_pending` overflows (perf on the host at level 4 over h2c).
3. **The harness**: give each load generator run a time limit, so that a hung run fails on its own
   rather than by a watchdog; and say in `bench/README.md` that the host must keep the servers'
   ports out of its ephemeral range.
4. **The bench config's upstream bound** differs from nginx's, which bounds only idle connections:
   for a comparison of the proxies themselves at high counts, a run with fit's `max_pending` raised
   to match what the others queue would separate the bound from the proxy.
