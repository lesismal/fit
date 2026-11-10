# Sync benchmarks, 2026-11-11: the whole plan on fib aabd7c2, with S10 and Alloy

[English](report.md) | [简体中文](report.zh-CN.md)

The sync plan's ten items on the benchmark host (Ryzen 7 7800X3D, 8 cores and 16 threads, Linux 7.0, Docker
29.1.3), with the harness and rules of [the 2026-11-09 plan](../sync-2026-11-09/report.md): one mover at a time
on the same four physical cores, 3 rounds each, every table with its weight and every row's score in
[tables.md](tables.md), the ranking in [scores.md](scores.md), each item's JSON, logs and fit's profiles beside
them. fit was built at `1f794c1b` (fit main `3881490b` on fib `aabd7c2`; main's later commits up to `a9fbe573`
touch no path these items run). The host's LLM service (strata) and a kind node were stopped for the run and
started again after it. New since [11-10](../sync-2026-11-10/report.md): S10, MQTT to InfluxDB against
Telegraf 1.40.3 (weight 5%, taken from S1, now 15%), and Grafana Alloy 1.21.3 beside Promtail in S6.

## In short

- **fit ranks first, 96.6 out of 100** (11-10: 98.8, over other weights and without S10), ahead of Alloy (85.6,
  S6 alone), DataX (72.2), Telegraf (70.6, S10 alone), Promtail (45.1), Filebeat (43.4), Debezium (37.5), Canal
  (30.5) and Kafka Connect with Camel (17.8). fit has the best score in every item but S6.
- **S10, MQTT to InfluxDB (new)**: 81.9k readings a second, 1.31 times Telegraf's 62.5k, on 1.76 cores to its
  1.81, in 18 MiB to its 53; the same lag.
- **S8**: Kafka Connect's Camel MQTT 5 source again lost messages in every round (39, 2 and 23 of 500,000).
- **Where fit is behind, or went back since 11-10:**
  - **S6, files to Loki: Alloy is ahead.** 431.6k lines a second to fit's 278.9k (fit 65%), on 0.84 cores to
    fit's 1.38 (fit 39% of its lines a CPU-second); fit's memory (40 MiB to 129) and lag (p99 464 ms to 1,363) are
    better. fit scores 67.1 there, the only item it does not lead.
  - **fit's CPU per record rose 8 to 20% since 11-10** at the same throughput: S1 0.99 → 1.18 cores at 913k lines
    a second (128-byte lines), 1.26 → 1.48 (1 KiB), S3 2.23 → 2.40, S5 0.70 → 0.76, S9 1.52 → 1.83 (at 17% more
    rows). The other movers' CPU did not move (Filebeat 2.17 → 2.18), so it is fit's. The profiles put the extra
    in the garbage collector (`runtime.scanObject`, about a fifth of S1's CPU more): the records grew with the
    fields added since 11-10 (`Record.Origin`, the pipeline name and lineage, the transform numbering).
  - **S9's lag went from a p99 of 1,597 ms to 2,408** (Debezium 1,498), while its throughput rose to 344k rows a
    second (3.7 times Debezium's 94k). Not yet explained.
- The rest is where it was: throughput within ±2% of 11-10 in S2, S4, S5, S6, S7 and S8, S1 up to +6%, S3 +6%.

## The ranking

| Rank | Mover | Score | S1 | S2 | S3 | S4 | S5 | S6 | S7 | S8 | S9 | S10 | Coverage |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | fit | 96.6 | 100.0 | 100.0 | 94.0 | 100.0 | 96.0 | 67.1 | 100.0 | 100.0 | 96.2 | 100.0 | 100% |
| 2 | Alloy | 85.6 | — | — | — | — | — | 85.6 | — | — | — | — | 5% |
| 3 | DataX | 72.2 | — | — | — | — | 72.2 | — | — | — | — | — | 15% |
| 4 | Telegraf | 70.6 | — | — | — | — | — | — | — | — | — | 70.6 | 5% |
| 5 | Promtail | 45.1 | — | — | — | — | — | 45.1 | — | — | — | — | 5% |
| 6 | Filebeat | 43.4 | 43.4 | — | — | — | — | — | — | — | — | — | 15% |
| 7 | Debezium | 37.5 | — | 37.0 | 26.2 | 61.7 | — | — | — | — | 28.3 | — | 47% |
| 8 | Canal | 30.5 | — | 32.5 | 28.6 | — | — | — | — | — | — | — | 30% |
| 9 | Kafka Connect (Camel) | 17.8 | — | — | — | — | — | — | 33.1 | 0.0 | — | — | 13% |

Weights: S1 15%, S2 15%, S3 15%, S4 10%, S5 15%, S6 5%, S7 7%, S8 6%, S9 7%, S10 5% (S10 added at 5%, taken
from S1, as the user ruled on 2026-11-10).

## Where fit stands

| Item | fit's place | fit / best other | Note |
| --- | --- | --- | --- |
| S1 files to a file | 1st | 1.3 to 3.0× Filebeat's lines/s | CPU +19% since 11-10 |
| S2 MySQL to Elasticsearch | 1st | 1.97× Debezium's rows/s | |
| S3 MySQL to Kafka | 1st | 2.9× Debezium's rows/s | p99 lag 2.5× Canal's; CPU +8% |
| S4 PostgreSQL to Kafka | 1st | 1.07× Debezium's rows/s, 45% of its CPU | |
| S5 MySQL batch copy | 1st | 1.21× DataX's rows/s | 92% of its rows a CPU-second |
| S6 files to Loki | **2nd** | 0.65× Alloy's lines/s, 0.39× its lines a CPU-second | memory 31%, p99 lag 34% of Alloy's |
| S7 RabbitMQ to Kafka | 1st | 2.4× Kafka Connect's messages/s | |
| S8 MQTT to Kafka | 1st | the only mover to deliver every message | |
| S9 SQL Server to Kafka | 1st | 3.7× Debezium's rows/s | p99 lag 2,408 ms, was 1,597 |
| S10 MQTT to InfluxDB | 1st | 1.31× Telegraf's readings/s | new |

## fit since 11-10

| Item | rows or lines/s, 11-10 → 11-11 | CPU (cores) | Peak RSS |
| --- | --- | --- | --- |
| s1-files-128b-1f | 912,029 → 912,962 (+0.1%) | 0.99 → 1.18 | 38 → 40 MiB |
| s1-files-128b-16f | 912,150 → 913,204 (+0.1%) | 1.02 → 1.20 | 38 → 40 MiB |
| s1-files-1k-1f | 558,966 → 592,944 (+6.1%) | 1.26 → 1.48 | 57 → 58 MiB |
| s1-files-1k-16f | 559,864 → 558,684 (−0.2%) | 1.26 → 1.41 | 58 → 59 MiB |
| s2-mysql-es | 50,471 → 51,471 (+2.0%) | 0.44 → 0.46 | 44 → 45 MiB |
| s3-mysql-kafka | 299,313 → 317,818 (+6.2%) | 2.23 → 2.40 | 43 → 42 MiB |
| s4-pg-kafka | 130,959 → 130,806 (−0.1%) | 1.75 → 1.77 | 16 → 16 MiB |
| s5-mysql-batch | 237,677 → 236,822 (−0.4%) | 0.70 → 0.76 | 45 → 49 MiB |
| s6-files-loki | 278,767 → 278,943 (+0.1%) | 1.25 → 1.38 | 40 → 40 MiB |
| s7-rabbitmq-kafka | 140,045 → 139,981 (−0.0%) | 1.34 → 1.36 | 17 → 17 MiB |
| s8-mqtt-kafka | 102,181 → 100,204 (−1.9%) | 1.71 → 1.79 | 18 → 18 MiB |
| s9-mssql-kafka | 293,980 → 344,026 (+17.0%) | 1.52 → 1.83 | 51 → 50 MiB |

## Open

- fit's CPU per record since 11-10 (the garbage collector scanning larger records) and S9's lag: being looked
  into.
- S6: Alloy's lines a CPU-second, 2.5 times fit's.
- S5's CPU against DataX, and S3's lag against Canal, as before.
