# Sync benchmarks, 2026-11-10: the whole plan with the second round's scenarios

[English](report.md) | [简体中文](report.zh-CN.md)

The sync plan's nine items on the benchmark host (Ryzen 7 7800X3D, 8 cores and 16 threads, Linux 7.0, Docker
29.1.3), with the harness and rules of [the 2026-11-09 plan](../sync-2026-11-09/report.md): one mover at a time
on the same four physical cores, 3 rounds each, every table with its weight and every row's score in
[tables.md](tables.md), the ranking in [scores.md](scores.md), each item's JSON, logs and fit's profiles beside
them. S6 to S9 are new (I222): the scenarios of sync's second round of connectors.

Two runs make the plan:

- **S6 to S9** ran first, fit at `6cae0fd0` (branch `sync2`; fit main's later commits do not touch these
  paths), with the host's LLM service (strata) left running, idle; those scenarios need about 5 GB.
- **S1 to S5** ran again after, on fit main `20482001`, with strata stopped for the run and started again after
  it, as on 11-09. They check that the stable record identities (`Record.Origin`, made for the second round's
  sinks) cost the first round's paths nothing: see [S1 to S5 against 11-09](#s1-to-s5-against-11-09).

The new items' movers:

| Item | Scenario | fit against |
| --- | --- | --- |
| S6 | files to Loki, 2M lines | Promtail 3.5.5 |
| S7 | RabbitMQ (a durable classic queue) to Kafka, 500k messages | Kafka Connect with Apache Camel's `spring-rabbitmq-source` 4.14.5 |
| S8 | MQTT (Mosquitto) to Kafka, 500k messages | Kafka Connect with Apache Camel's `mqtt5-source` 4.14.5 |
| S9 | SQL Server 2022 (CDC) to Kafka, 500k rows | Debezium 3.0.8's SQL Server connector |

## In short

- **fit ranks first, 98.8 out of 100** (11-09: 98.1 over S1 to S5 alone), and has the best score in every item.
  After it: DataX 72.4 (the batch copy alone), Promtail 70.7 (S6 alone), Filebeat 43.0, Debezium 38.2, Canal
  30.4, Kafka Connect with Camel 16.3.
- The new items, against each one's other mover:
  - **Files to Loki (S6)**: 279k lines a second, 1.39 times Promtail's 201k, with a third of its lag (p99 464 ms
    to 1,384) and the same memory (40 MiB to 43); 17% more lines a CPU-second.
  - **RabbitMQ to Kafka (S7)**: 140k messages a second, 2.8 times Kafka Connect's 50k, on 1.34 cores to its
    1.95, in 17 MiB to its 597.
  - **MQTT to Kafka (S8)**: 102k messages a second in 18 MiB. Kafka Connect's Camel MQTT 5 source **did not
    deliver every message in any round**: 499,999, 499,995 and 499,998 of 500,000 after the 30-minute limit, so
    it scores 0 there.
  - **SQL Server to Kafka (S9)**: 294k rows a second, 3.2 times Debezium's 91k, on 1.52 cores to its 2.47, in
    51 MiB to its 1,091.
- **S1 to S5 are where they were**: fit's throughput is within 0.7% of 11-09's in S1 to S4, at the same CPU and
  memory; S5 is 5.8% faster (and DataX 6.7% slower), within the batch copy's spread between runs.
- **Where fit is behind or not clean:**
  - S5's CPU: DataX copies 367k rows a CPU-second to fit's 338k (92%; 11-09: 86%).
  - S3's lag: Canal's p99 is 121 ms to fit's 299 (fit gathers records into batches, Canal sends a transaction at
    a time; see 11-09).
  - S9's lag: Debezium's p99 is 1,518 ms to fit's 1,597 (fit 95% of it). Both poll CDC tables by LSN, so their
    lag is mostly the capture job's interval.
  - S9's copies: in round 3 fit wrote 10 rows twice (500,010 at the sink, none missing), with no kill. Kafka is an
    at-least-once sink and the source's snapshot hand-over replays the rows written during it (as I175 does);
    whether that is the cause was not checked. Rounds 1 and 2 were exact.
  - S7's rounds spread 19% for fit (29% for Kafka Connect); the cause was not looked into.

## The ranking

| Rank | Mover | Score | S1 | S2 | S3 | S4 | S5 | S6 | S7 | S8 | S9 | Coverage |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | fit | 98.8 | 100.0 | 100.0 | 94.0 | 100.0 | 98.0 | 100.0 | 100.0 | 100.0 | 99.5 | 100% |
| 2 | DataX | 72.4 | — | — | — | — | 72.4 | — | — | — | — | 15% |
| 3 | Promtail | 70.7 | — | — | — | — | — | 70.7 | — | — | — | 5% |
| 4 | Filebeat | 43.0 | 43.0 | — | — | — | — | — | — | — | — | 20% |
| 5 | Debezium | 38.2 | — | 37.6 | 27.2 | 61.2 | — | — | — | — | 30.4 | 47% |
| 6 | Canal | 30.4 | — | 31.2 | 29.7 | — | — | — | — | — | — | 30% |
| 7 | Kafka Connect (Camel) | 16.3 | — | — | — | — | — | — | 30.3 | 0.0 | — | 13% |

The weights changed with I222 (S1 20%, S2 15%, S3 15%, S4 10%, S5 15%, S6 5%, S7 7%, S8 6%, S9 7%; before, S1
to S5 alone), so 11-09's 98.1 and today's 98.8 are not over the same items.

## Where fit stands

| Item | fit's place | fit / best other | Note |
| --- | --- | --- | --- |
| S1 files to a file | 1st | 1.3 to 3.1× Filebeat's lines/s | as on 11-09 |
| S2 MySQL to Elasticsearch | 1st | 1.9× Debezium's rows/s | |
| S3 MySQL to Kafka | 1st | 2.7× Debezium's rows/s | p99 lag 2.5× Canal's |
| S4 PostgreSQL to Kafka | 1st | 1.07× Debezium's rows/s, 44% of its CPU | |
| S5 MySQL batch copy | 1st | 1.21× DataX's rows/s | 92% of its rows a CPU-second |
| S6 files to Loki | 1st | 1.39× lines/s, 0.34× p99 lag | |
| S7 RabbitMQ to Kafka | 1st | 2.8× messages/s, 3% of the memory | rounds spread 19% |
| S8 MQTT to Kafka | 1st | the only mover to deliver every message | |
| S9 SQL Server to Kafka | 1st | 3.2× rows/s, 5% of the memory | p99 lag 5% behind; 10 copies in one round |

## S1 to S5 against 11-09

fit's records a second, CPU and peak memory, 11-09 (fit `0b94708d` to `6afaa355`) against today (`20482001`):

| Item | 11-09 | 11-10 | Change | CPU (cores) | Peak RSS |
| --- | ---: | ---: | ---: | --- | --- |
| s1-files-128b-1f | 913,065 | 912,029 | −0.1% | 1.04 → 0.99 | 38 → 38 MiB |
| s1-files-128b-16f | 912,868 | 912,150 | −0.1% | 1.04 → 1.02 | 38 → 38 MiB |
| s1-files-1k-1f | 559,063 | 558,966 | −0.0% | 1.31 → 1.26 | 56 → 57 MiB |
| s1-files-1k-16f | 559,195 | 559,864 | +0.1% | 1.32 → 1.26 | 54 → 58 MiB |
| s2-mysql-es | 50,365 | 50,471 | +0.2% | 0.44 → 0.44 | 44 → 44 MiB |
| s3-mysql-kafka | 298,165 | 299,313 | +0.4% | 2.25 → 2.23 | 41 → 43 MiB |
| s4-pg-kafka | 130,013 | 130,959 | +0.7% | 1.76 → 1.75 | 20 → 16 MiB |
| s5-mysql-batch | 224,562 | 237,677 | +5.8% | 0.69 → 0.70 | 44 → 45 MiB |

The other movers moved by −6.7% to +6.8% between the two days (DataX −6.7% in S5, Canal +6.8% in S3), the
host's spread from one day to the next.

## What the runs found and fixed

The smoke runs before the plan fixed the harness: RabbitMQ died at start when the wait loop's `rabbitmqctl`
(run as root) made the Erlang cookie before the server could (now run as the `rabbitmq` user; CI's interop
scripts had the same race); Camel declared the benchmark's queue non-durable against the durable one the driver
made (`autoDeclare` off); an interrupted run waited out its 10-minute timeout before cleaning up; cleanup left 3
to 5 anonymous volumes per mover per run. benchreport now counts missing records net of copies: S9's row read
"−10 / 10" before.

## Open

- S9's 10 copies without a kill.
- S5's CPU against DataX, and S3's lag against Canal, as on 11-09.
- S6, S7 and S8 against more movers (Vector, Fluent Bit; Benthos/Redpanda Connect), if wanted.
