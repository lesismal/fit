# Sync benchmarks, 2026-11-10: the second round's scenarios

[English](report.md) | [简体中文](report.zh-CN.md)

I222's plan items S6 to S9, the scenarios of sync's second round of connectors, on the benchmark host
(Ryzen 7 7800X3D, 8 cores and 16 threads, Linux 7.0, Docker 29.1.3), with the same harness and rules as
[the 2026-11-09 plan](../sync-2026-11-09/report.md): one mover at a time on the same four physical cores, 3
rounds each, every table with its weight and every row's score in [tables.md](tables.md), the ranking in
[scores.md](scores.md), each item's JSON, logs and fit's profiles beside them. fit was built at `6cae0fd0`
(branch `sync2`, now in fit main `1fdb2407`, whose later commits do not touch these paths).

Unlike 11-09, the host's LLM service (strata) was left running: it was idle (load 0) but holds 47 GB, and S1
to S5 need more memory than the rest leaves, so **S1 to S5 were not run again**: their scores in the ranking
are 11-09's. The movers:

| Item | Scenario | fit against |
| --- | --- | --- |
| S6 | files to Loki, 2M lines | Promtail 3.5.5 |
| S7 | RabbitMQ (a durable classic queue) to Kafka, 500k messages | Kafka Connect with Apache Camel's `spring-rabbitmq-source` 4.14.5 |
| S8 | MQTT (Mosquitto) to Kafka, 500k messages | Kafka Connect with Apache Camel's `mqtt5-source` 4.14.5 |
| S9 | SQL Server 2022 (CDC) to Kafka, 500k rows | Debezium 3.0.8's SQL Server connector |

## In short

- **fit has the best score in every new item**, and ranks first overall at **98.6** (11-09: 98.1), ahead of DataX
  (78.7, batch copy only), Promtail (70.7, S6 only), Filebeat (42.4), Debezium (38.2, now with S9), Canal (29.9)
  and Kafka Connect with Camel (16.3).
- Against each item's other mover:
  - **Files to Loki (S6)**: 279k lines a second, 1.39 times Promtail's 201k, with a third of its lag (p99 464 ms
    to 1,384) and the same memory (40 MiB to 43); 17% more lines a CPU-second.
  - **RabbitMQ to Kafka (S7)**: 140k messages a second, 2.8 times Kafka Connect's 50k, on 1.34 cores to its
    1.95, in 17 MiB to its 597.
  - **MQTT to Kafka (S8)**: 102k messages a second in 18 MiB. Kafka Connect's Camel MQTT 5 source **did not
    deliver every message in any round**: 499,999, 499,995 and 499,998 of 500,000 after the 30-minute limit, so
    it scores 0 there.
  - **SQL Server to Kafka (S9)**: 294k rows a second, 3.2 times Debezium's 91k, on 1.52 cores to its 2.47, in
    51 MiB to its 1,091.
- **Where fit is behind or not clean:**
  - S9's lag: Debezium's p99 is 1,518 ms to fit's 1,597 (fit 95% of it). Both read CDC tables polled by LSN,
    so their lag is mostly the capture job's interval.
  - S9's copies: in round 3 fit wrote 10 rows twice (500,010 at the sink, none missing), with no kill. Kafka is
    an at-least-once sink and the source's snapshot hand-over replays the rows written during it (as I175
    does); whether that is the cause was not checked. Rounds 1 and 2 were exact.
  - S7's rounds spread 19% for fit (29% for Kafka Connect); the cause was not looked into.
    The table's numbers are the medians.

## The ranking

| Rank | Mover | Score | S1 | S2 | S3 | S4 | S5 | S6 | S7 | S8 | S9 | Coverage |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | fit | 98.6 | 100.0 | 100.0 | 94.0 | 100.0 | 96.5 | 100.0 | 100.0 | 100.0 | 99.5 | 100% |
| 2 | DataX | 78.7 | — | — | — | — | 78.7 | — | — | — | — | 15% |
| 3 | Promtail | 70.7 | — | — | — | — | — | 70.7 | — | — | — | 5% |
| 4 | Filebeat | 42.4 | 42.4 | — | — | — | — | — | — | — | — | 20% |
| 5 | Debezium | 38.2 | — | 38.5 | 26.7 | 60.6 | — | — | — | — | 30.4 | 47% |
| 6 | Canal | 29.9 | — | 31.0 | 28.8 | — | — | — | — | — | — | 30% |
| 7 | Kafka Connect (Camel) | 16.3 | — | — | — | — | — | — | 30.3 | 0.0 | — | 13% |

S1 to S5 are 11-09's runs. The weights changed with I222 (S1 20%, S2 15%, S3 15%, S4 10%, S5 15%, S6 5%, S7 7%,
S8 6%, S9 7%; before, S1 to S5 alone), so 11-09's 98.1 and today's 98.6 are not over the same items.

## Where fit stands

| Item | fit's place | fit / best other | Note |
| --- | --- | --- | --- |
| S6 files to Loki | 1st | 1.39× lines/s, 0.34× p99 lag | |
| S7 RabbitMQ to Kafka | 1st | 2.8× messages/s, 3% of the memory | rounds spread 19% |
| S8 MQTT to Kafka | 1st | the only mover to deliver every message | |
| S9 SQL Server to Kafka | 1st | 3.2× rows/s, 5% of the memory | p99 lag 5% behind; 10 copies in one round |

## What the runs found and fixed

The smoke runs before the plan fixed the harness: RabbitMQ died at start when the wait loop's
`rabbitmqctl` (run as root) made the Erlang cookie before the server could (now run as the `rabbitmq` user;
CI's interop scripts had the same race); Camel declared the benchmark's queue non-durable against the durable
one the driver made (`autoDeclare` off); an interrupted run waited out its 10-minute timeout before cleaning up;
cleanup left 3 to 5 anonymous volumes per mover per run. benchreport now counts missing records net of copies:
S9's row read "−10 / 10" before.

## Open

- S1 to S5 again on today's fit with strata stopped, to see whether the stable record identities (`Record.Origin`)
  cost anything on the first round's paths.
- S9's 10 copies without a kill.
- S6, S7 and S8 against more movers (Vector, Fluent Bit; Benthos/Redpanda Connect), if wanted.
