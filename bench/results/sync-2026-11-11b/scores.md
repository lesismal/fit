### Overall ranking

| Rank | Server        | Score |    S1 |    S2 |   S3 |    S4 |   S5 |   S6 |    S7 |    S8 |   S9 |   S10 | Coverage |
| ---- | ------------- | ----: | ----: | ----: | ---: | ----: | ---: | ---: | ----: | ----: | ---: | ----: | -------: |
| 1    | fit           |  98.8 | 100.0 | 100.0 | 94.0 | 100.0 | 98.1 | 99.6 | 100.0 | 100.0 | 99.6 | 100.0 |     100% |
| 2    | datax         |  74.5 |     — |     — |    — |     — | 74.5 |    — |     — |     — |    — |     — |      15% |
| 3    | alloy         |  73.4 |     — |     — |    — |     — |    — | 73.4 |     — |     — |    — |     — |       5% |
| 4    | telegraf      |  67.9 |     — |     — |    — |     — |    — |    — |     — |     — |    — |  67.9 |       5% |
| 5    | filebeat      |  42.7 |  42.7 |     — |    — |     — |    — |    — |     — |     — |    — |     — |      15% |
| 6    | promtail      |  39.6 |     — |     — |    — |     — |    — | 39.6 |     — |     — |    — |     — |       5% |
| 7    | debezium      |  37.7 |     — |  36.7 | 26.2 |  61.9 |    — |    — |     — |     — | 29.8 |     — |      47% |
| 8    | canal         |  30.0 |     — |  31.9 | 28.1 |     — |    — |    — |     — |     — |    — |     — |      30% |
| 9    | kafka-connect |  17.4 |     — |     — |    — |     — |    — |    — |  32.3 |   0.0 |    — |     — |      13% |

The score is the weighted mean of a server's item scores over the items it ran in, out of 100; each workload's column is the same over that workload's items. Coverage is the share of the plan's weight those items hold: a server the plan does not run in a workload has no score there, and one that cannot serve a scenario it is run in scores 0 for it.

### How the items are scored

| Workload                   | Weight | Items | Weight an item | TPS | CPU EER | MEM EER | Latency |
| -------------------------- | -----: | ----: | -------------: | --: | ------: | ------: | ------: |
| S1, files to a file        |    15% |     4 |          3.75% | 50% |     25% |     15% |     10% |
| S2, MySQL to Elasticsearch |    15% |     1 |         15.00% | 50% |     25% |     15% |     10% |
| S3, MySQL to Kafka         |    15% |     1 |         15.00% | 50% |     25% |     15% |     10% |
| S4, PostgreSQL to Kafka    |    10% |     1 |         10.00% | 50% |     25% |     15% |     10% |
| S5, a MySQL batch copy     |    15% |     1 |         15.00% | 55% |     25% |     20% |      0% |
| S6, files to Loki          |     5% |     1 |          5.00% | 50% |     25% |     15% |     10% |
| S7, RabbitMQ to Kafka      |     7% |     1 |          7.00% | 50% |     25% |     15% |     10% |
| S8, MQTT to Kafka          |     6% |     1 |          6.00% | 50% |     25% |     15% |     10% |
| S9, SQL Server to Kafka    |     7% |     1 |          7.00% | 50% |     25% |     15% |     10% |
| S10, MQTT to InfluxDB      |     5% |     1 |          5.00% | 50% |     25% |     15% |     10% |

An item is one table: a scenario's variant in one results file, its servers the movers. A mover's score for it is 100 times the weighted sum of its shares of the table's best throughput (records a second, counted at the sink), CPU efficiency (records a CPU-second), memory efficiency (records a second a MiB of peak anonymous memory) and lag (the best's p99 over its own, in the steady phase). A run that failed, its records not all at the sink in time, scores 0.

### Every item

| Item                                                 | Weight | filebeat |       fit | canal | debezium | datax | alloy | promtail | kafka-connect | telegraf |
| ---------------------------------------------------- | -----: | -------: | --------: | ----: | -------: | ----: | ----: | -------: | ------------: | -------: |
| s1-files-128b-1f: files, s1-files-128b-1f            |  3.75% |     29.3 | **100.0** |     — |        — |     — |     — |        — |             — |        — |
| s1-files-128b-16f: files, s1-files-128b-16f          |  3.75% |     45.5 | **100.0** |     — |        — |     — |     — |        — |             — |        — |
| s1-files-1k-1f: files, s1-files-1k-1f                |  3.75% |     36.9 | **100.0** |     — |        — |     — |     — |        — |             — |        — |
| s1-files-1k-16f: files, s1-files-1k-16f              |  3.75% |     59.0 | **100.0** |     — |        — |     — |     — |        — |             — |        — |
| s2-mysql-es: mysql-es, s2-mysql-es                   | 15.00% |        — | **100.0** |  31.9 |     36.7 |     — |     — |        — |             — |        — |
| s3-mysql-kafka: mysql-kafka, s3-mysql-kafka          | 15.00% |        — |  **94.0** |  28.1 |     26.2 |     — |     — |        — |             — |        — |
| s4-pg-kafka: pg-kafka, s4-pg-kafka                   | 10.00% |        — | **100.0** |     — |     61.9 |     — |     — |        — |             — |        — |
| s5-mysql-batch: mysql-batch, s5-mysql-batch          | 15.00% |        — |  **98.1** |     — |        — |  74.5 |     — |        — |             — |        — |
| s6-files-loki: files-loki, s6-files-loki             |  5.00% |        — |  **99.6** |     — |        — |     — |  73.4 |     39.6 |             — |        — |
| s7-rabbitmq-kafka: rabbitmq-kafka, s7-rabbitmq-kafka |  7.00% |        — | **100.0** |     — |        — |     — |     — |        — |          32.3 |        — |
| s8-mqtt-kafka: mqtt-kafka, s8-mqtt-kafka             |  6.00% |        — | **100.0** |     — |        — |     — |     — |        — |           0.0 |        — |
| s9-mssql-kafka: mssql-kafka, s9-mssql-kafka          |  7.00% |        — |  **99.6** |     — |     29.8 |     — |     — |        — |             — |        — |
| s10-mqtt-influxdb: mqtt-influxdb, s10-mqtt-influxdb  |  5.00% |        — | **100.0** |     — |        — |     — |     — |        — |             — |     67.9 |

Each item's best in bold; — where the plan does not run the server.

