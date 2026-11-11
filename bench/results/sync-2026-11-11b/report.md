# Sync benchmarks, 2026-11-11 (second run): after the record and Loki optimizations

[English](report.md) | [简体中文](report.zh-CN.md)

The whole plan again, on fit `28ce0b65` (fib `aabd7c2`), the same host, harness, rules and weights as
[the first 11-11 run](../sync-2026-11-11/report.md), which found fit's CPU per record up 8 to 20% since 11-10
and Alloy ahead in S6. Between the two runs fit changed in two ways (I219's and I222's notes in the dev-plan):

- **A record is 504 bytes again.** The pipeline's name and lineage, added to every record since 11-10, had pushed
  `sync.Record` from 504 to 536 bytes, past 512, where Go's allocator stops marking objects span by span and
  the garbage collector scans each one: they now sit in one per-run struct a record points to. The emit path
  no longer reads the clock per record. A pipeline benchmark: 505 ns a record back to 426 (11-10: 427).
- **The Loki sink and the file source allocate little.** The sink built a `strings.Replacer` for every label
  value of every line (17% of the CPU, 68% of the bytes allocated): values needing no escape are now used as
  they are, a stream's labels are reused across its run of records, and lines, streams, the protobuf body and
  the snappy output are encoded into pooled buffers. The file source makes a line's record in one allocation.

Every table with its weight and scores is in [tables.md](tables.md), the ranking in [scores.md](scores.md).
strata and a kind node were stopped for the run and started again after it.

## In short

- **fit ranks first, 98.8, and leads every item** (first run: 96.6, behind Alloy in S6). After it: DataX 74.5,
  Alloy 73.4, Telegraf 67.9, Filebeat 42.7, Promtail 39.6, Debezium 37.7, Canal 30.0, Kafka Connect with Camel
  17.4.
- **S6, files to Loki**: fit 427.9k lines a second (first run 278.9k, +53%) on 0.48 cores (1.38), against
  Alloy's 431.6k on 0.83: 99% of its throughput on 58% of its CPU, 1.71 times its lines a CPU-second, with a
  third of its memory (40 MiB to 129) and of its lag (p99 546 ms to 1,344). All three movers end near Loki's
  own ceiling on the servers' cores (about 430k lines a second): bigger batches from fit did not raise it.
- **fit's CPU per record is back near 11-10's**, at the same throughput:

  | Item | 11-10 | first 11-11 run | this run |
  | --- | ---: | ---: | ---: |
  | s1-files-128b-1f | 0.99 | 1.18 | 1.05 |
  | s1-files-128b-16f | 1.02 | 1.20 | 1.06 |
  | s1-files-1k-1f | 1.26 | 1.48 | 1.37 |
  | s1-files-1k-16f | 1.26 | 1.41 | 1.34 |
  | s3-mysql-kafka | 2.23 | 2.40 | 2.28 |
  | s5-mysql-batch | 0.70 | 0.76 | 0.70 |

  (cores; S3 at 318k rows a second here to 299k on 11-10.) S1's 1 KiB lines still take 6 to 9% more than on
  11-10.
- **S9's lag** is a p99 of 1,557 ms here (first run 2,408, 11-10 1,597): it varies from run to run with where
  fit's one-second poll falls against SQL Server's one-second capture job (nine rounds of each build ran
  1.4 to 2.4 s), not with fit's code. A 500 ms `poll_interval` (Debezium's default) would narrow it, at the cost
  of more queries while idle: left as it is, for the user to decide.
- **S8**: Kafka Connect's Camel MQTT 5 source lost messages again in every round.

## The ranking

| Rank | Mover | Score | S1 | S2 | S3 | S4 | S5 | S6 | S7 | S8 | S9 | S10 | Coverage |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | fit | 98.8 | 100.0 | 100.0 | 94.0 | 100.0 | 98.1 | 99.6 | 100.0 | 100.0 | 99.6 | 100.0 | 100% |
| 2 | DataX | 74.5 | — | — | — | — | 74.5 | — | — | — | — | — | 15% |
| 3 | Alloy | 73.4 | — | — | — | — | — | 73.4 | — | — | — | — | 5% |
| 4 | Telegraf | 67.9 | — | — | — | — | — | — | — | — | — | 67.9 | 5% |
| 5 | Filebeat | 42.7 | 42.7 | — | — | — | — | — | — | — | — | — | 15% |
| 6 | Promtail | 39.6 | — | — | — | — | — | 39.6 | — | — | — | — | 5% |
| 7 | Debezium | 37.7 | — | 36.7 | 26.2 | 61.9 | — | — | — | — | 29.8 | — | 47% |
| 8 | Canal | 30.0 | — | 31.9 | 28.1 | — | — | — | — | — | — | — | 30% |
| 9 | Kafka Connect (Camel) | 17.4 | — | — | — | — | — | — | 32.3 | 0.0 | — | — | 13% |

## Where fit stands

| Item | fit's place | fit / best other | Note |
| --- | --- | --- | --- |
| S1 files to a file | 1st | 1.3 to 3.1× Filebeat's lines/s | 1 KiB lines' CPU 6–9% over 11-10 |
| S2 MySQL to Elasticsearch | 1st | 1.98× Debezium's rows/s | |
| S3 MySQL to Kafka | 1st | 2.9× Debezium's rows/s | p99 lag 2.5× Canal's |
| S4 PostgreSQL to Kafka | 1st | 1.07× Debezium's rows/s, 45% of its CPU | |
| S5 MySQL batch copy | 1st | 1.15× DataX's rows/s | |
| S6 files to Loki | 1st | 0.99× Alloy's lines/s, 1.71× its lines a CPU-second | was 0.65× and 0.39× |
| S7 RabbitMQ to Kafka | 1st | 2.5× Kafka Connect's messages/s | |
| S8 MQTT to Kafka | 1st | the only mover to deliver every message | |
| S9 SQL Server to Kafka | 1st | 3.2× Debezium's rows/s | p99 lag 1,557 ms, Debezium's 1,489 |
| S10 MQTT to InfluxDB | 1st | 1.33× Telegraf's readings/s | |

## Open

- S1's 1 KiB lines: 6 to 9% more CPU than on 11-10.
- S9: a 500 ms `poll_interval` default (the user's call).
- S3's lag against Canal (batching), S5's CPU against DataX.
