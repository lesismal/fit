# The sync benchmark

[English](README.md) | [简体中文](README.zh-CN.md)

The sync benchmark (dev-plan I193) measures fit's [sync](../../docs/sync.md) against the tools it
stands in for: Filebeat for files, Promtail for files to Loki, Canal and Debezium for MySQL's,
PostgreSQL's and SQL Server's changes, Kafka Connect for RabbitMQ and MQTT into Kafka, DataX for a
batch copy. Every mover runs in Docker against fresh servers of the same pinned images and
settings, pinned to the same CPUs, and is measured the same way: the records it moves are counted
where it writes them.

| Scenario | Source → sink | Movers |
| --- | --- | --- |
| `files` | lines in files (`LINE_BYTES`, `FILES`) → one file | fit (`file` → `file`), Filebeat 8.17.4 (`filestream` → `output.file`) |
| `files-loki` | lines in files → Loki 3.5 | fit (`file` → `loki`), Promtail 3.5.5 (Filebeat has no Loki output) |
| `mysql-es` | MySQL 8.0's binlog → Elasticsearch 7.17 | fit (`mysql` → `elasticsearch`), Canal 1.1.8 (canal-server + client adapter `es7`), Debezium 3.0.8 (MySQL connector → Kafka → Confluent's Elasticsearch sink 14.1.2, in one Kafka Connect) |
| `mysql-kafka` | MySQL 8.0's binlog → Kafka 3.9 | fit (`mysql` → `kafka`, Debezium's envelope), Canal 1.1.8 (canal-server in Kafka mode, flat messages), Debezium 3.0.8 (MySQL connector, JSON without schemas) |
| `pg-kafka` | PostgreSQL 17's logical replication → Kafka 3.9 | fit (`postgres` → `kafka`), Debezium 3.0.8 (PostgreSQL connector, `pgoutput`) |
| `mysql-batch` | a MySQL table → another MySQL server | fit (a job: `mysql` snapshot → `mysql`), DataX 202309 (`mysqlreader` → `mysqlwriter`) |
| `rabbitmq-kafka` | messages of a RabbitMQ 4.1 queue → Kafka 3.9 | fit (`rabbitmq` → `kafka`, the codec `raw`), Kafka Connect (Debezium's 3.0.8 image) with Apache Camel's `spring-rabbitmq-source` connector 4.14.5 |
| `mqtt-kafka` | messages of a Mosquitto 2.0 topic at QoS 1 → Kafka 3.9 | fit (`mqtt` → `kafka`, the codec `raw`), Kafka Connect with Apache Camel's `mqtt5-source` connector 4.14.5 |
| `mssql-kafka` | SQL Server 2022's CDC tables → Kafka 3.9 | fit (`mssql` → `kafka`, Debezium's envelope), Debezium 3.0.8 (SQL Server connector, JSON without schemas) |

The rows are sysbench's (`id`, `k`, `c CHAR(120)`, `pad CHAR(60)`, about 200 bytes), written as
transactions of `TX_ROWS` single-row INSERTs; the lines are a log line's head and filler; the
messages are the rows as JSON, published by syncbench, persistent and confirmed (RabbitMQ) or at QoS 1
(MQTT), while the mover is paused as for the streams. SQL Server's CDC capture job polls every second
and takes up to 5,000 transactions a scan, so that it bounds neither mover.

## Running it

On a Linux host with Docker (or Docker Desktop), from the repository:

```sh
bench/sync/run.sh files                 # one scenario, every mover, one round
bench/sync/run.sh -n 3 mysql-kafka      # three rounds, the movers' order alternating
SIZE=smoke bench/sync/run.sh pg-kafka   # a tiny run, to check it works (a few minutes; its numbers mean nothing)
bench/sync/plan.sh 2026-11-09           # the plan: every scenario, 3 rounds each, and the report
```

`run.sh [-n ROUNDS] [-movers LIST] [-o DIR] [-name NAME] SCENARIO` writes `DIR/NAME.json`
(`bench/results/sync/<scenario>.json` by default), and in `DIR/NAME/` each round's result
(`r<round>-<mover>.json`), fit's CPU and heap profiles (`profiles-r<round>/`) and each mover's log.
Its environment sets the sizes and the CPUs; the head of [`run.sh`](run.sh) lists every variable:

| Variable | Default | |
| --- | --- | --- |
| `SIZE` | `full` | `smoke` for tiny sizes |
| `RECORDS` | files 2,000,000; streams 500,000; batch 2,000,000 | the records measured |
| `LINE_BYTES`, `FILES` | `128`, `1` | the files scenario's lines and files |
| `TX_ROWS` | `10` | rows a source transaction |
| `STEADY_RATE`, `STEADY_FOR` | `2000`, `20s` | the steady phase, which measures the lag |
| `PAUSE` | `1` | pause a stream's mover while its rows are written |
| `MOVER_CORES`, `AGENT_CORES` | `4`, `2` | physical cores for the mover and syncbench; the servers get the rest |
| `MOVER_CPUS`, `AGENT_CPUS`, `SERVER_CPUS` | | CPU lists, in place of the split |
| `BIN` | built with Go | a folder with `fit` and `syncbench` for the Docker host's Linux |
| `CACHE` | `~/.cache/fit-syncbench` | downloads, and fit and syncbench when built |
| `PREFIX` | `fsb` | names the containers, volumes and network |
| `KEEP` | | `1` leaves the last run's containers |

Nothing binds a host port: the servers, the movers and syncbench share a Docker network of their own
(`fsb-net`), fit's Admin API is a Unix socket in a volume, and everything the run made (containers,
labelled `fit-syncbench=fsb`, its volumes and network) is removed at its end, or when it is
interrupted. Two runs at once need different `PREFIX`es; one at a time uses the benchmark host
([rule 23](../../docs/rules.md)).

### On the benchmark host

The host (192.168.100.51) has Docker and no Go: build here, copy, run there under the bench lock.

```sh
# here
host="ssh -S $HOME/.ssh/fitbench.sock root@192.168.100.51"
dir=/root/fitbench/sync
mkdir -p /tmp/fsb/bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/fsb/bin/fit ./cmd/fit
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/fsb/bin/syncbench ./bench/sync/syncbench
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/fsb/bin/benchreport ./bench/cmd/benchreport
$host "mkdir -p $dir/bench"
COPYFILE_DISABLE=1 tar cf - bench/sync bench/cmd | $host "tar xf - -C $dir"
COPYFILE_DISABLE=1 tar cf - -C /tmp/fsb bin | $host "tar xf - -C $dir"

# there: the plan, in the background, holding the bench lock
$host "cd $dir && FIT_REV=$(git rev-parse --short HEAD) BIN=$dir/bin BENCHREPORT=$dir/bin/benchreport \
  nohup flock /root/bench.lock sh bench/sync/plan.sh $(date +%F) </dev/null >plan.out 2>&1 &"

# progress, then the results
$host "cat $dir/bench/results/sync-*/plan.log"
$host "cd $dir/bench/results && tar cf - sync-$(date +%F)" | tar xf - -C bench/results
```

Before the plan, clean the host as for fit's plan (no other benchmark, the LLM service stopped and
restored after). The first run downloads Canal's adapter (291 MB), DataX (1.7 GB, of which only the
engine and the MySQL plugins are kept), Confluent's Elasticsearch sink (43 MB) and Camel's RabbitMQ
and MQTT connectors (45 MB, from Maven Central) into `CACHE`, checked against their pinned sha256,
and builds four images from them ([`images/`](images)); later runs reuse them. Where the host cannot
reach a download (the benchmark host gets 504s from GitHub), fetch it elsewhere and copy it into
`CACHE` under the name `run.sh` gives it. The plan takes about two hours at the full sizes with 3
rounds.

## What is measured

`syncbench` ([`syncbench/`](syncbench)) runs in a container beside the servers, on CPUs of its own,
with the Docker socket mounted:

- `prepare` makes the tables (and the batch job's rows and target table), the index (one shard, no
  replica, refreshed every second, the same mapping for every mover) and the files.
- `measure`: for a **stream**, warm-up transactions until the sink's count rises (the mover follows
  the source; rows written before it started are not read, as each mover starts from the source's
  position then), then, with the mover's containers **paused**, the `RECORDS` rows; unpaused, the
  mover reads them as a backlog at its own pace, not the writer's. For **files** and the **batch**
  the records are there before the mover starts: `run.sh` only makes its containers, and syncbench
  starts them once it has counted the sink.
- **Throughput** is the sink's count's rise from the first measured record it holds to the last,
  over that time (records a second); **CPU** the mover's containers' CPU over the same time, from the
  Docker API's stats (in cores); **peak RSS** the most anonymous memory (cgroup v2's `anon`) all its
  containers held at once, sampled every 100ms (500ms for the batch) from the start.
- **Lag**: then a steady phase writes `STEADY_RATE` records a second for `STEADY_FOR`, and each
  write's lag is from its commit to the sink's count reaching it (p50, p99, max). The index's
  one-second refresh is in every mover's lag to Elasticsearch.
- The count is watched a few seconds more: records counted twice show as **extra**.
- fit's CPU profile (10s from the start of the drain) and heap profile (at the end) are saved
  through its Admin API.
- `merge` puts the rounds of an item into one results file, each mover's median round by
  throughput, with the rounds' spread and any failures.

Where the sink is counted: the files sink's lines; the lines Loki's distributor took
(`loki_distributor_lines_received_total`, from its `/metrics`); Elasticsearch's `_count`; Kafka's offsets (a
message a row for fit and Debezium), or, for Canal's flat messages, which hold a transaction's rows
each, the rows in their `data`; the target table's `COUNT(*)`.

### Settings that are aligned

Each mover runs with its defaults but where the comparison needs the same:

- Filebeat looks for new lines every 250ms (fit's `poll_interval`) and its queue flushes after 1s
  (fit's batch `max_wait`), not after up to 10s each, so that their lags compare.
- Promtail and fit push to Loki in snappy protobuf, requests of up to 1 MiB of lines sent after at
  most 1s (Promtail's defaults, fit's `max_request_bytes` and `max_wait`), the lines as they are,
  labelled `job=bench` (Promtail adds each file's `filename`); Loki's rate limits are raised past
  what either reaches (`configs/loki/loki.yaml`).
- fit writes Debezium's envelope to Kafka (`codec debezium`), as Debezium does; Canal its flat
  messages; every topic one partition, made on first write.
- The batch: four readers and writers each (DataX's `channel`, fit's `job.max_parallel`), batches of
  1,000 rows (DataX's `batchSize`, fit's sink `batch.max_records`), plain inserts.
- No TLS anywhere; the servers' durability relaxed alike (`innodb_flush_log_at_trx_commit=2`,
  `sync_binlog=0`, PostgreSQL's `synchronous_commit=off`) so that the writer is not the bound.
- The JVMs keep their images' heaps (Canal 2-3 GiB, its adapter the same, Kafka Connect up to 2 GiB,
  DataX 1 GiB), as each ships; that is what their RSS shows. canal-server runs without the image's
  node_exporter.

On arm64 Docker hosts with SVE (Apple's M4), Java 21 and 22 die at start: Kafka Connect and
Elasticsearch are told not to use it there, which changes nothing on the amd64 benchmark host.

## The report

`benchreport` renders a sync results file as a table per scenario: records a second, records a
CPU-second, records a second a MiB, the lag, CPU, peak RSS, startup, rounds and their spread, missing
and extra records. With `-plan` each table gets its weight and each row its score; `-scores` ranks
the movers over the plan (rule 24):

```sh
go run ./bench/cmd/benchreport -in bench/results/sync-DATE/s3-mysql-kafka.json -plan bench/results/sync-DATE
go run ./bench/cmd/benchreport -scores bench/results/sync-DATE
```

| Workload | Weight | Items | Throughput | CPU | Memory | Lag |
| --- | ---: | --- | ---: | ---: | ---: | ---: |
| S1, files | 20% | 128 B and 1 KiB lines, 1 and 16 files | 50% | 25% | 15% | 10% |
| S2, MySQL to Elasticsearch | 15% | | 50% | 25% | 15% | 10% |
| S3, MySQL to Kafka | 15% | | 50% | 25% | 15% | 10% |
| S4, PostgreSQL to Kafka | 10% | | 50% | 25% | 15% | 10% |
| S5, a MySQL batch copy | 15% | | 55% | 25% | 20% | — |
| S6, files to Loki | 5% | | 50% | 25% | 15% | 10% |
| S7, RabbitMQ to Kafka | 7% | | 50% | 25% | 15% | 10% |
| S8, MQTT to Kafka | 6% | | 50% | 25% | 15% | 10% |
| S9, SQL Server to Kafka | 7% | | 50% | 25% | 15% | 10% |

A mover's score for an item is 100 times the weighted sum of its shares of the item's best; one whose
records did not all reach the sink in time scores 0.
