# Sync benchmarks, 2026-11-09: the report

[English](report.md) | [简体中文](report.zh-CN.md)

I193's first plan: on the benchmark host (Ryzen 7 7800X3D, 8 cores and 16 threads, Linux 7.0, Docker 29.1.3),
fit's sync against Filebeat, Canal, Debezium and DataX, each moving the same records between the same servers,
one mover at a time. Every mover gets the same four physical cores (a cpuset); the servers and the driver get
the rest. Every item ran 3 rounds; [tables.md](tables.md) has every table with its weight and every row's score,
[scores.md](scores.md) the ranking, and each item's JSON and logs are beside them, with fit's CPU and heap
profiles for every round. The harness is [bench/sync](../../sync/README.md). The host's LLM service (strata) was
stopped for each run and started again after it.

S2 to S5 were run again after the first run's findings were fixed (below); the numbers here are each item's last
run, with fit at `a0425fcd` (S2), `18c4d8de` (S3, S5) and `6afaa355` (S4). S1 is the first run's (fit
`0b94708d`): nothing since touched the file path.

## In short

- **fit ranks first, 98.1 out of 100**, ahead of DataX (78.7, in the batch copy alone), Filebeat (42.4), Debezium
  (40.2) and Canal (29.9). fit has the best score in every item.
- **fit moves more records on less CPU and a small share of the memory.** Against the best other mover of each
  workload:
  - Files (S1): 1.3 to 3.1 times Filebeat's lines a second, on a quarter to a half of its CPU.
  - MySQL to Elasticsearch (S2): 50.4k rows a second, 1.9 times Debezium and 3 times Canal, on 0.44 cores and
    44 MiB (Debezium 1.62 cores and 1.26 GB, Canal 2.4 GB).
  - MySQL to Kafka (S3): 298k rows a second, 2.8 times Debezium and 3.7 times Canal.
  - PostgreSQL to Kafka (S4): 130k rows a second, 9% more than Debezium, on 45% of its CPU and 20 MiB.
  - MySQL batch copy (S5): 225k rows a second, 6% more than DataX, in 44 MiB to its 429.
- **Where fit is behind:**
  - S5's CPU: DataX copies 380k rows a CPU-second to fit's 328k (86%).
  - S3's lag: Canal's p99 is 121 ms to fit's 300. fit gathers records into batches (`batch.max_records`, or
    `max_wait`, 1s, after the first, whichever comes first) and Canal sends a transaction at a time; at 2,000 rows
    a second fit's lag is mostly that gathering.
  - S4's throughput did not move with fit's cheaper encoder (130k, as before): its limit is not fit's CPU (1.76
    cores); the source's reading of pgoutput is the next to look at.

## The ranking

| Rank | Mover | Score | S1 | S2 | S3 | S4 | S5 | Coverage |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | fit | **98.1** | 100.0 | 100.0 | 94.0 | 100.0 | 96.5 | 100% |
| 2 | DataX | 78.7 | — | — | — | — | 78.7 | 20% |
| 3 | Filebeat | 42.4 | 42.4 | — | — | — | — | 25% |
| 4 | Debezium | 40.2 | — | 38.5 | 26.7 | 60.6 | — | 55% |
| 5 | Canal | 29.9 | — | 31.0 | 28.8 | — | — | 40% |

This is the first sync plan, so there is no earlier ranking to compare with. Weights: S1 25%, S2 20%, S3 20%, S4
15%, S5 20%; an item scores throughput 50%, CPU efficiency 25%, memory efficiency 15% and p99 lag 10% (S5 55%, 25%,
20%, no lag), each as a share of the item's best ([scores.md](scores.md)).

## Where fit stands

| Workload | fit's place | The others |
| --- | --- | --- |
| S1, files (4 items: 128 B and 1 KiB lines, 1 and 16 files) | first in each | Filebeat 28.7 to 56.3 |
| S2, MySQL to Elasticsearch | first | Debezium 38.5, Canal 31.0 |
| S3, MySQL to Kafka | first (94.0: Canal's lag is lower) | Canal 28.8, Debezium 26.7 |
| S4, PostgreSQL to Kafka | first | Debezium 60.6 |
| S5, MySQL batch copy | first (96.5: DataX uses less CPU a row) | DataX 78.7 |

## What the runs found, and what was fixed

The plan asks for the costs found to be fixed or recorded; the I193 section of the
[dev-plan](../../../docs/dev-plan.md) has each, with its numbers.

- **The harness**: syncbench spoke Docker API 1.41, which Docker 29 refuses (`279aa357`); Canal's Elasticsearch
  adapter parsed the `._items.yml` macOS added in the copy and wrote nothing (`f16b3b56`).
- **fit's batch copy (S5)**, 140k rows a second on 0.84 cores in the first run, DataX 173k:
  - about a quarter of its CPU was work nobody needed: parsing each prepared insert's 4,000 parameter
    definitions, snapshot positions through `json.Marshal`, plain strings through `json.Marshal` (`f16b3b56`);
  - then it waited: the mysql sink had one writer; a job's writers now follow its chunk readers, a batch's
    prepare, execute and close is one text statement, and the writer holding the queue's oldest record writes at
    once when the queue is full (`18c4d8de`). 122k rows a second went to 225k.
- **The Debezium codec (S3, S4)** built each event as maps through `encoding/json`, 35% of fit's CPU in S3; it is
  written by hand now, byte for byte the same (`6afaa355`): S3 went from 129k rows a second to 298k.
- **A full disk**: in S2's second run the host's disk filled with earlier runs' anonymous volumes; Elasticsearch
  went read-only and answered 429, which fit's sink waited out silently. A 429 that lasts 30s now warns
  (`a0425fcd`); the volumes were removed and S2 run again. S3's and S5's last runs ran while the disk was filling
  (MySQL and Kafka kept working; their numbers stand).
- **Recorded, not done**: cached prepared statements in place of text inserts (not done, to keep the client's
  promise to leave no statement open); canal-json's encoder, the same pattern as Debezium's but on no
  benchmark's path; S4's source.
