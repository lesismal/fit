### Overall ranking

| Rank | Server   | Score |    S1 |    S2 |   S3 |    S4 |   S5 | Coverage |
| ---- | -------- | ----: | ----: | ----: | ---: | ----: | ---: | -------: |
| 1    | fit      |  98.1 | 100.0 | 100.0 | 94.0 | 100.0 | 96.5 |     100% |
| 2    | datax    |  78.7 |     — |     — |    — |     — | 78.7 |      20% |
| 3    | filebeat |  42.4 |  42.4 |     — |    — |     — |    — |      25% |
| 4    | debezium |  40.2 |     — |  38.5 | 26.7 |  60.6 |    — |      55% |
| 5    | canal    |  29.9 |     — |  31.0 | 28.8 |     — |    — |      40% |

The score is the weighted mean of a server's item scores over the items it ran in, out of 100; each workload's column is the same over that workload's items. Coverage is the share of the plan's weight those items hold: a server the plan does not run in a workload has no score there, and one that cannot serve a scenario it is run in scores 0 for it.

### How the items are scored

| Workload                   | Weight | Items | Weight an item | TPS | CPU EER | MEM EER | Latency |
| -------------------------- | -----: | ----: | -------------: | --: | ------: | ------: | ------: |
| S1, files to a file        |    25% |     4 |          6.25% | 50% |     25% |     15% |     10% |
| S2, MySQL to Elasticsearch |    20% |     1 |         20.00% | 50% |     25% |     15% |     10% |
| S3, MySQL to Kafka         |    20% |     1 |         20.00% | 50% |     25% |     15% |     10% |
| S4, PostgreSQL to Kafka    |    15% |     1 |         15.00% | 50% |     25% |     15% |     10% |
| S5, a MySQL batch copy     |    20% |     1 |         20.00% | 55% |     25% |     20% |      0% |

An item is one table: a scenario's variant in one results file, its servers the movers. A mover's score for it is 100 times the weighted sum of its shares of the table's best throughput (records a second, counted at the sink), CPU efficiency (records a CPU-second), memory efficiency (records a second a MiB of peak anonymous memory) and lag (the best's p99 over its own, in the steady phase). A run that failed, its records not all at the sink in time, scores 0.

### Every item

| Item                                        | Weight | filebeat |       fit | canal | debezium | datax |
| ------------------------------------------- | -----: | -------: | --------: | ----: | -------: | ----: |
| s1-files-128b-1f: files, s1-files-128b-1f   |  6.25% |     28.7 | **100.0** |     — |        — |     — |
| s1-files-128b-16f: files, s1-files-128b-16f |  6.25% |     46.9 | **100.0** |     — |        — |     — |
| s1-files-1k-1f: files, s1-files-1k-1f       |  6.25% |     37.8 | **100.0** |     — |        — |     — |
| s1-files-1k-16f: files, s1-files-1k-16f     |  6.25% |     56.3 | **100.0** |     — |        — |     — |
| s2-mysql-es: mysql-es, s2-mysql-es          | 20.00% |        — | **100.0** |  31.0 |     38.5 |     — |
| s3-mysql-kafka: mysql-kafka, s3-mysql-kafka | 20.00% |        — |  **94.0** |  28.8 |     26.7 |     — |
| s4-pg-kafka: pg-kafka, s4-pg-kafka          | 15.00% |        — | **100.0** |     — |     60.6 |     — |
| s5-mysql-batch: mysql-batch, s5-mysql-batch | 20.00% |        — |  **96.5** |     — |        — |  78.7 |

Each item's best in bold; — where the plan does not run the server.

