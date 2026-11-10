# Sync

[English](sync.md) | [简体中文](sync.zh-CN.md)

Sync moves data from producers to consumers: pipelines that read records from a **source** (files, a
database's changes, a topic, an index, a bucket, an HTTP endpoint), put them through **transforms**,
and write them to one or more **sinks**, as Filebeat does for files and Canal, Debezium or DataX do
for databases ([decision 036](decisions/036-sync.md)). Every kind of infrastructure fit speaks is a
source and a sink, so any pair can be joined; a pipeline follows its source for good (a stream) or
reads it once and stops (a batch job); and what the built-in connectors do not cover is added by
[WebAssembly plugins](#plugins). The wire protocols (MySQL with its replication stream, PostgreSQL
with logical replication, MongoDB, Kafka, Redis, S3's signatures) are written in fit: no driver is
imported.

Off by default: without `sync`, no connections are made and nothing is written to disk.

This page covers the concepts, every connector with a config that works, what each source needs on
its server, how types travel between systems, plugins, jobs, the Admin API, `fit sync` and the
workbench, metrics and dead letters, and running pipelines. The
[config reference](reference/config.md#sync) lists every field, and the
[fit-examples](https://github.com/lesismal/fit-examples) repository has
[files to Elasticsearch](https://github.com/lesismal/fit-examples/tree/main/sync-files-to-elasticsearch),
[MySQL to PostgreSQL](https://github.com/lesismal/fit-examples/tree/main/sync-mysql-to-postgres),
[PostgreSQL to Kafka](https://github.com/lesismal/fit-examples/tree/main/sync-postgres-to-kafka),
[RabbitMQ to Kafka](https://github.com/lesismal/fit-examples/tree/main/sync-rabbitmq-to-kafka),
[PostgreSQL to NATS](https://github.com/lesismal/fit-examples/tree/main/sync-postgres-to-nats),
[Pulsar to Elasticsearch](https://github.com/lesismal/fit-examples/tree/main/sync-pulsar-to-elasticsearch),
[MQTT to InfluxDB](https://github.com/lesismal/fit-examples/tree/main/sync-mqtt-to-influxdb),
[MQTT to TDengine](https://github.com/lesismal/fit-examples/tree/main/sync-mqtt-to-tdengine),
[SQL Server to Kafka](https://github.com/lesismal/fit-examples/tree/main/sync-mssql-to-kafka),
[MySQL to Doris](https://github.com/lesismal/fit-examples/tree/main/sync-mysql-to-doris),
[files to Loki](https://github.com/lesismal/fit-examples/tree/main/sync-files-to-loki),
[PostgreSQL to vector databases](https://github.com/lesismal/fit-examples/tree/main/sync-postgres-to-vectors) and
[a WASM transform](https://github.com/lesismal/fit-examples/tree/main/sync-wasm-transform).

## A first pipeline

`generate` makes numbered records of every value type and `stdout` prints them, so this runs
anywhere:

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines trial {
		mode once
		source generate { records 1000; tx_size 10 }
		sinks out { sink stdout }
	}
}
```

`fit sync run -c fit.conf trial` runs it on its own and exits with its result; `fit run` runs it
beside everything else the config serves. A pipeline that does real work, log files shipped to
Elasticsearch:

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines app-logs {
		source file { paths /var/log/app/*.log; codec json }
		sinks search {
			sink elasticsearch {
				urls https://es1:9200 https://es2:9200
				username fit
				password_env ES_PASSWORD
				index app-logs-{yyyy.MM.dd}
			}
			batch { max_records 1000; max_wait 1s }
		}
	}
}
```

## Concepts

### Records

Every source makes, and every sink takes, the same record, so no connector's types leak into
another:

| Part | What it holds |
| --- | --- |
| `source` | the connector that read it: `mysql`, `postgres`, `file`, ... |
| `namespace` | where it came from: `db.table`, `schema.table`, an index, a collection, a file's path, a topic |
| `op` | `insert`, `update`, `delete`, `ddl` (a schema change, reported) or `message` (a line, an event) |
| `key` | what identifies the row: a map of the primary key's columns, a document's `_id`, a message's key |
| `before`, `after` | the row before and after the change, as typed values; a delete has only `before` (or only its key) |
| `commit_time` | when the source committed it, where the source knows |
| `tx` | its transaction's ID, and whether it is the first or last record of it |
| `position` | where the source resumes after it: opaque JSON, the source's own |

A value is one of null, bool, int, uint, float, decimal (as text, never rounded), string, bytes,
time (with its offset), date, JSON, array and map, a map keeping its fields' order as a row keeps its
columns'. Transforms, plugins, the `stdout` sink and dead letters see a record in its canonical JSON,
which round-trips every kind: null, booleans, strings and arrays are JSON's own, every other kind an
object of one member named after it.

```json record
{"source": "mysql", "namespace": "shop.orders", "op": "update",
 "key": {"map": {"id": {"int": 7}}},
 "before": {"map": {"id": {"int": 7}, "status": "paid", "total": {"decimal": "12.30"}}},
 "after": {"map": {"id": {"int": 7}, "status": "shipped", "total": {"decimal": "12.30"},
   "shipped_at": {"time": "2026-11-09T08:00:00.123456Z"}}},
 "commit_time": "2026-11-09T08:00:00Z", "tx": {"id": "3E11FA47-71CA-11E1-9E33-C80AA9429562:23", "first": true, "last": true}}
```

### Pipelines

A pipeline is one source, its transforms in order and one or more sinks, with one checkpoint. The
source feeds a queue; each record goes through the transforms, which may change, drop or split it;
every sink takes every record, in order, in batches of its own, and acknowledges a batch once it is
written. Sinks run keys in parallel only where a key's records stay in order.

- **Bounded.** `queue` bounds the records read and not yet acknowledged by every sink, in records
  (10,000) and bytes (64 MiB): past it the source waits, so a slow sink slows the source rather than
  growing memory. Each sink's `batch` closes at `max_records` (500), `max_bytes` (4 MiB) or
  `max_wait` (1s) after its first record, whichever comes first.
- **Off fib's workers.** A pipeline runs on goroutines of its own, a few each; HTTP-based systems
  go out through fib's asynchronous HTTP client, and database connections are few and long, each on
  a goroutine. Nothing in sync waits on a worker that serves requests.
- **Stream or once.** `mode stream` (the default) follows the source for good; `mode once` reads it
  to its end and stops: a [batch job](#batch-jobs).
- **Reloads.** A reload starts the pipelines it adds, stops those it removes (each writing what it
  holds and its checkpoint first) and restarts only those whose settings changed; the others run on
  untouched.

### Checkpoints

A pipeline's checkpoint is `<state_dir>/<name>.json`: the position of the last record every sink has
acknowledged that ends a source transaction, never one inside a transaction and never past a record
still owed. It is written at most every `checkpoint_interval` (5s) and when the pipeline stops, to a
temporary file, synced and renamed into place, and a pipeline starts from it. A file that does not
parse keeps the pipeline from starting, rather than starting it from scratch.

A source whose server keeps data for its readers is told the position only once the file holds it:
a PostgreSQL slot is told an LSN, which lets the server drop the WAL before it, only after the
checkpoint is saved. Kafka's offsets, Redis's entry IDs and MongoDB's resume token live in the
checkpoint too: fit joins no consumer group and commits nothing to the brokers.

A pipeline runs on one fit: its checkpoint is a local file. Moving a pipeline to another fit means
moving its file (with the pipeline stopped).

**Lineage.** The checkpoint also holds a lineage, a random ID made when a pipeline first starts and
again by a reset of its checkpoint, a position set by hand and a new job (a job run again from its
start); restarts keep it. Sinks whose servers drop what they already had by a name made from
positions put it in the name: `doris`'s labels and `http`'s `Idempotency-Key`, so that the records a
reset reads again are written again rather than dropped as repeats (see [Delivery
guarantees](#delivery-guarantees)). A checkpoint saved by a fit older than lineages keeps none, and
its sinks' names stay as they were, until it is reset.

**Quiet tables.** A source's position moves with its own tables' records. One reading a quiet table
from a busy log (a binlog, a WAL, SQL Server's change tables) would keep an old checkpoint while the
server moves on, and a restart after the server dropped that part of its log (binlogs purged, CDC's
cleanup retention passed) would fail asking for a snapshot, though nothing of the table was lost. So
the `mysql`, `postgres` and `mssql` sources hand on position-only records after a stretch of other
tables' changes: each moves the checkpoint once the records before it are written, and no transform
or sink sees one, nor do they count as records read or written. One is taken at most once per
`checkpoint_interval`, and only when no record came in that time, so a busy table's records move the
checkpoint themselves. The `postgres` source's slot is also told the position between transactions,
so that the server keeps no WAL for the other tables.

### Delivery guarantees

**At least once.** After a crash, a restart or `kill -9`, the records after the last checkpoint are
read and written again; nothing before it is lost, since the checkpoint never moved past what every
sink had. Sinks therefore write idempotently, so that a replay converges on the same state:

| Sink | What a record written again does |
| --- | --- |
| `mysql`, `postgres` | upserts and deletes by key: the same rows |
| `mongo` | upserts and deletes by `_id`: the same documents |
| `elasticsearch` | index and delete by `_id` from the key: the same documents (a record without a key gets an `_id` the cluster makes, which a replay repeats) |
| `clickhouse` | the same deduplication token for the same records: the server drops the insert; rows of a `ReplacingMergeTree` by version otherwise |
| `doris` | the same label for the same records (of the same lineage): the server refuses the load as already loaded; rows of a unique or primary key table by key otherwise |
| `loki` | the same entry (stream, time, line) is kept once; lines without a time get the time of the write, and are stored again |
| `mssql` | upserts (`MERGE`) and deletes by key: the same rows |
| `redis` | the same keys; stream entries with `entry_id key` are refused as already there |
| `s3` | the same batch makes the same object, written over itself; a replay cut into other batches makes other objects |
| `influxdb`, `tdengine` | the same point: a series (a subtable) and a time, written over itself; deletes are left out |
| `qdrant`, `milvus` | upserts and deletes by an ID from the key: the same points and entities |
| `nats` | the same `Nats-Msg-Id` for the same records: the stream's duplicate window drops it (2 minutes by default) |
| `pulsar` | the same `fit-id` property for the same records: with `dedup read_back`, a record the producer stored lately is dropped |
| `rabbitmq` | published again, with the same `message_id`, by which a consumer can drop it |
| `kafka`, `http`, `otlp_logs`, `file`, `stdout` | written again: Kafka's producer is not idempotent, nor is OTLP; `http` sends the same `Idempotency-Key` for the same records (of the same lineage) |

**Record IDs.** The `nats`, `pulsar` and `rabbitmq` sinks give each record an ID (`Nats-Msg-Id`,
`fit-id`, `message_id`), and `http`'s `Idempotency-Key`, `doris`'s labels and `s3`'s object names
are made the same way, so that a record written again is known. A source that has a name for each
record of its own, the same however a run reads it, gives the record that name, its origin: `kafka`
the topic, partition and offset; `pulsar` the partition and message ID (ledger, entry, batch index);
`nats` the stream sequence; `rabbitmq` a stream's offset; `mysql` the transaction's GTID (or binlog
file and offset) and the row's place in it; `postgres` the commit LSN and the change's place;
`mssql` (CDC) the transaction's LSN and the change's place; `mongo` the resume token. With a
message's records (a codec's) and a transform's records of one record numbered by their place, the
ID follows the record, not the position: a replay after a crash gets the IDs it had, whatever order
the run met the partitions in (a `kafka` or `pulsar` source writes every partition's place in each
position, in that order). An origin's ID also holds the pipeline's name, so two pipelines whose
sources give equal origins (a topic of one name on two Kafka clusters, one binlog file and offset on
two MySQL servers) feeding one stream do not take each other's records for repeats. A record without an origin (a queue's message, MQTT, files, snapshots,
change tracking) is named by the position at or after it and its distance from it, which repeats
when the source reads the record again at the same position: a database's log, files, a single
partition. `doris`'s labels and `http`'s keys also hold the checkpoint's lineage, so that the records
a reset reads again are taken again (a Doris label lives `label_keep_max_second`, three days, past a
table dropped and made again); the record IDs and `s3`'s object names do not: a message is the same
message after a reset, which its consumers may drop, and an object written again is written over
itself.

**Transactions stay whole.** A checkpoint never falls inside a source transaction; the records of one
carry its ID, and the `mysql` and `postgres` sinks write each source transaction as one of their own
(`transactions source`), so a reader of the sink never sees half of one.

**Order is kept per key**, from the source to each sink: a sink may write keys in parallel (a job's
writers), never one key out of order.

**Exactly once** into MySQL, MariaDB, TiDB, PostgreSQL and SQL Server: with `checkpoint sink` the position is also
written to a table of the sink's (`_fit_sync_checkpoints`), in the same transaction as the records it
covers, and read back when the pipeline starts in preference to the file. A crash between the sink's
commit and the file's next checkpoint then writes no record twice. It holds while every sink of the
pipeline keeps the checkpoint, at the same position: a pipeline with a sink of another kind beside
them falls back to the file, at least once. What can still be written twice: a source transaction
larger than the MySQL sink's `max_transaction_bytes` (committed in parts), and a job read in chunks,
which keeps to its file. A reset, a position set by hand and a new job start a new lineage, so an
older position in the sink does not undo them; deleting the pipeline's row and its file starts it
over. Position-only records (see [Checkpoints](#checkpoints)) move the file past the position the
sink holds, which they do not write: the file keeps that position too (`wrote`), and the sink holding
it starts the pipeline from the file's.

### Errors and retries

A write that fails is tried again, `retry.max_attempts` (5) in all, the wait doubling from
`retry.backoff` (200ms) to `retry.max_backoff` (30s), from the record that failed. A sink tells a
record it refuses for good (a mapping error, a constraint, a value too long) from an error that may
pass (a lost connection, a 429, a deadlock), and takes the first to `on_error` at once. A record still
failing, or one a transform fails on, goes as `on_error` says:

- `stop` (the default) stops the pipeline, its checkpoint before the record, until it is resumed,
  the config changes or fit restarts;
- `skip` leaves it out of that sink, with a log line;
- `dead_letter` writes it, with its error, to the `dead_letter` sink before going on; with the
  `dead_letters` sink they are kept in `state_dir`, listed and [replayed](#dead-letters).

A source that fails (a connection lost, a server restarting) restarts the pipeline from its checkpoint
after the same backoff, for as long as it fails: a source is expected to come back.

## Connectors

| System | As a source | As a sink |
| --- | --- | --- |
| [Files](#files) | `file`: paths and globs tailed across rotations, multiline | `file`: lines by a codec or a template, rotated as a log |
| [Elasticsearch, OpenSearch](#elasticsearch-and-opensearch) | `elasticsearch`: a point-in-time snapshot, then a cursor field polled | `elasticsearch`: `_bulk` by `_id` |
| [ClickHouse](#clickhouse) | `clickhouse`: a table or query by key, then a cursor column polled | `clickhouse`: `JSONEachRow` inserts, deduplicated, `ReplacingMergeTree` |
| [Doris, StarRocks](#doris-and-starrocks) | `doris`: a table or query by key over MySQL's protocol, then a cursor column polled | `doris`: Stream Load, labelled by origins or positions, delete signs |
| [Loki](#loki) | | `loki`: the push API, protobuf or JSON, streams from configured fields |
| [OpenTelemetry logs](#opentelemetry-logs) | | `otlp_logs`: OTLP/HTTP log records, protobuf or JSON |
| [HTTP](#http) | `http`: records posted to a `sync_ingest` route | `http`: batches posted as NDJSON or JSON |
| [MySQL, MariaDB, TiDB](#mysql-mariadb-and-tidb) | `mysql`: the binlog as a replica, a snapshot without locks | `mysql`: upserts and deletes, the source's transactions kept |
| [TiDB through TiCDC](#tidb-through-ticdc) | `tidb`: a snapshot at a timestamp, then a changefeed from Kafka | (the `mysql` sink) |
| [PostgreSQL](#postgresql) | `postgres`: logical replication with `pgoutput`, an exact snapshot | `postgres`: `ON CONFLICT` upserts, `COPY` |
| [SQL Server](#sql-server) | `mssql`: CDC's change tables by LSN, or change tracking; a snapshot without locks | `mssql`: `MERGE` upserts, deletes, bulk loads, exactly once |
| [MongoDB](#mongodb) | `mongo`: a snapshot by `_id`, then the change stream | `mongo`: bulk upserts and deletes by `_id` |
| [Kafka, Redpanda](#kafka-and-redpanda) | `kafka`: every partition from offsets in the checkpoint | `kafka`: keyed messages, batched, compressed |
| [MQTT](#mqtt) | `mqtt`: a persistent session, QoS 1 and 2 acknowledged after the sinks | `mqtt`: a topic template, a QoS, retained or not |
| [RabbitMQ](#rabbitmq) | `rabbitmq`: queues acknowledged after the sinks, stream queues from offsets in the checkpoint | `rabbitmq`: persistent messages, a batch held for publisher confirms |
| [NATS JetStream](#nats-jetstream) | `nats`: a durable pull consumer, acknowledged after the sinks; core subjects | `nats`: JetStream publishes deduplicated by `Nats-Msg-Id`; core |
| [Apache Pulsar](#apache-pulsar) | `pulsar`: every partition as a reader from message IDs in the checkpoint, or a durable subscription | `pulsar`: keyed messages, batched, compressed, deduplicated |
| [Redis, Valkey](#redis-and-valkey) | `redis`: streams with `XREAD` | `redis`: `XADD`, or a key per record |
| [S3 and compatibles](#s3-and-compatibles) | `s3`: objects under a prefix, in key order, as lines | `s3`: objects of lines, rolled by size and time |
| [InfluxDB](#influxdb) | | `influxdb`: line protocol to InfluxDB 1, 2 and 3, by series and time |
| [TDengine](#tdengine) | | `tdengine`: schemaless lines, or `INSERT ... USING` a super table |
| [Qdrant and Milvus](#qdrant-and-milvus) | | `qdrant`, `milvus`: upserts and deletes by key, vectors from `embed` |
| [Plugins](#plugins) | `wasm`: a plugin polling through fit's HTTP client | `wasm`: a plugin writing through it |
| [Trials](#trials-generate-memory-and-stdout) | `generate` | `memory`, `stdout` |

Secrets are never in the config: each connector names the environment variable holding its password
or token (`password_env`, `secret_access_key_env`, `bearer_token_env`, `headers_file`), read when the pipeline
loads, so `fit validate` needs it set too. TLS is verified against the system's roots or `ca_file`,
and cannot be turned off; a client certificate is `cert_file` and `key_file`. Every connector's
connection can be tested without writing a record ([Test connection](#the-admin-api-fit-sync-and-the-workbench)),
but the `wasm` ones, whose reach is the plugin's.

### Files

The `file` source tails files as Filebeat does: globs matched again every `scan_interval` (10s), each
file known by its device, inode and a fingerprint of its head, so a rename is followed, a truncation
read again from its start, a copy taken before a truncation read on from where the original was, and
a deleted file finished first. A line, or a `multiline` event, goes through a [codec](#codecs) into a
record; the checkpoint holds every file's identity and offset. Bounds: `max_line_bytes` (1 MiB, a
longer line cut and counted), `max_open_files` (256), `max_files` (100,000). The `file` sink writes
lines through fit's log file sink, by a codec or a `template`, rotated, kept and compressed as
`rotate` says.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-log {
		source file {
			paths /var/log/shop/*.log
			codec json
			multiline { start "^\\{" }
		}
		sinks archive {
			sink file {
				path /var/lib/fit/orders.jsonl
				codec json
				rotate { max_bytes 104857600; max_files 10 }
			}
		}
	}
}
```

The fit user needs read access to the files and their directories.

### Elasticsearch and OpenSearch

The sink writes with `_bulk`: inserts, updates and messages as an `index` of the row, deletes as a
`delete`, by an `_id` made from the record's key (`{"order": 7, "line": 2}` is `7:2`). `index` is
`{namespace}` or a template with the commit time's date. Each item's answer decides what follows: a
429 is sent again for as long as the cluster answers so (holding the pipeline, and through its queue
the source), a 5xx five times, and any other refusal fails its record for good. Elasticsearch 7.12
and later, OpenSearch 2.4 and later.

The source reads every document through a point in time (`snapshot pit`), then, with `cursor`, polls
every `poll_interval` for documents whose cursor field (an update time, a sequence) passed the last
value read. Polling sees no deletes: keep them with a soft-delete field named in `deleted_field`.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-copy {
		source elasticsearch {
			urls https://old-es:9200
			username reader
			password_env OLD_ES_PASSWORD
			index orders-*
			cursor updated_at
			deleted_field deleted
		}
		sinks new {
			sink elasticsearch {
				urls https://es1:9200 https://es2:9200
				username fit
				password_env ES_PASSWORD
				index "{namespace}"
			}
		}
	}
}
```

The sink's user needs `index` and `create_index` (or `write`) on the indices; the source's, `read`
and `view_index_metadata`.

### ClickHouse

The sink inserts each run of a batch's records for one table with `INSERT ... FORMAT JSONEachRow`, and
an `insert_deduplication_token` made from the run, so the same records written again are dropped by
the server. Updates and deletes become rows of a `ReplacingMergeTree(_version, _deleted)` ordered by
the key, which, read with `FINAL`, holds each key's last state. A row's version is its commit time,
in Unix nanoseconds, or `version_field`'s value; a record without a commit time, a snapshot's row, is
version 0, so that it never outranks a change, and rows of one version keep the one written last. The
table is yours:

```sql
CREATE TABLE shop.orders (id UInt64, status String, total Decimal(12, 2),
  _version UInt64, _deleted UInt8)
ENGINE = ReplacingMergeTree(_version, _deleted) ORDER BY id
SETTINGS non_replicated_deduplication_window = 1000
```

The source reads a table or a query in pages ordered by `cursor` and `key`, then polls past the last
row read; rows deleted from ClickHouse are not seen, but a `deleted_column` reads as deletes.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines events-to-ch {
		source clickhouse {
			urls http://ch-old:8123
			username fit
			password_env CH_OLD_PASSWORD
			table app.events
			key id
			cursor updated_at
			poll_interval 10s
		}
		sinks ch {
			sink clickhouse {
				urls http://ch1:8123 http://ch2:8123
				username fit
				password_env CH_PASSWORD
				table shop.orders
			}
			batch { max_records 10000; max_wait 1s }
		}
	}
}
```

### Doris and StarRocks

One connector, `doris`, serves Apache Doris (2.1 and later) and StarRocks (3 and later); `flavor`
names the system, whose only differences here are the delete column and the partial update's header.
The sink loads each run of a batch's records for one table with Stream Load over HTTP, JSON (or CSV)
rows, and a `label` made from the run's first and last records' origins, or positions for a source
without them (see [Delivery guarantees](#delivery-guarantees)): a run loaded again after a crash
is refused as already loaded, so a batch is stored once even in a duplicate key table. Updates and
deletes need a table of the unique key model with merge on write (Doris) or the primary key model
(StarRocks): a delete is the row, or its key, with `__DORIS_DELETE_SIGN__` (or `__op`) set to 1.
`partial_columns` updates only the columns the records have. A frontend's redirect to a backend is
followed with the credentials, so the backend's address must be one fit reaches, but never from https
to http, which would send them and the rows in the clear; `urls` may name the backends' HTTP ports
directly. A load refused for its rows is split until the record it cannot take
is found: that one goes to `on_error`, the records before it are loaded.

Labels outlive tables: Doris keeps them `label_keep_max_second` (three days by default). A label
holds the lineage of the pipeline's checkpoint (see [Checkpoints](#checkpoints)), which a reset
changes, so a pipeline reset to load tables made afresh loads its records again with no change to
`label_prefix`. Tables made afresh without a reset need one, or a new `label_prefix`, or the records
loaded before are refused as already loaded; so does a checkpoint saved by a fit older than lineages,
whose labels stay as they were until it is reset.

The source reads a table or a query over MySQL's protocol (a frontend's query port), in pages
ordered by `cursor` and `key`, then polls past the last row read, as the clickhouse source does; rows
deleted from the table are not seen, but a `deleted_column` reads as deletes.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-doris {
		source mysql {
			address mysql:3306
			username fit_repl
			password_env MYSQL_PASSWORD
			server_id 4101
			tables shop.orders
		}
		sinks doris {
			sink doris {
				urls http://fe1:8030 http://fe2:8030
				username fit
				password_env DORIS_PASSWORD
				table dw.{namespace}
			}
			batch { max_records 20000; max_wait 2s }
		}
	}
	pipelines starrocks-events {
		source doris {
			address sr-fe:9030
			username fit
			password_env SR_PASSWORD
			table app.events
			key id
			cursor updated_at
		}
		sinks out { sink stdout }
	}
}
```

The table for `orders-to-doris`, on Doris:

```sql
CREATE TABLE dw.orders (id BIGINT NOT NULL, status VARCHAR(20), total DECIMAL(12, 2))
UNIQUE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 8
PROPERTIES ("enable_unique_key_merge_on_write" = "true");
```

### Loki

The sink `loki` pushes records to Loki's push API as log lines, snappy-compressed protobuf (as
Promtail and Alloy send them) or JSON. A record's line is what its `codec` makes, the line codec by
default, which keeps a file's lines as they are. Its stream's `labels` are templates of the record's
fields, `{"job": "fit", "namespace": "{$namespace}"}` by default: only the fields you name become
labels, so the streams are as many as their values, never one per record; put values of many kinds
in `structured_metadata` (Loki 3) instead. A line's time is `timestamp_field`'s, else the record's
commit time, else the time of the write. `tenant` (or `tenant_field`) is sent as `X-Scope-OrgID`.
Each stream's entries go in time order; a request Loki refuses (an entry too old, a line too long)
is split until the record it refuses is found, which goes to `on_error`. Loki keeps one copy of an
entry with the same stream, time and line, so a batch written again after a crash is stored once
when its records have times. The connection test pushes a stream without entries, which Loki stores
nothing of, so credentials that may push but not query pass it.

### OpenTelemetry logs

The sink `otlp_logs` exports records as OpenTelemetry log records over OTLP/HTTP (`/v1/logs`) to an
OpenTelemetry Collector or any OTLP receiver, protobuf or JSON, gzipped. The resource is the one fit's
own telemetry sends (`service.name`, `service.version`), with `resource` attributes from templates of
the record's fields; a log record's body is the line its `codec` makes, or its row as a typed map
(`body row`), its time `timestamp_field`'s or the commit time, its severity `severity_field`'s, and
its `attributes` templates (`fit.namespace` and `fit.op` by default). `headers_file` keeps a vendor's
key out of the config. OTLP has no deduplication: a batch written again after a crash is exported
again.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines app-logs {
		source file { paths /var/log/app/*.log }
		sinks loki {
			sink loki {
				urls http://loki:3100
				labels { job app; host web1 }
			}
			batch { max_wait 1s }
		}
		sinks otel {
			sink otlp_logs {
				urls http://collector:4318
				service_name app
				resource { host.name web1 }
			}
			batch { max_wait 1s }
		}
	}
}
```

### HTTP

The `http` source takes the records clients post to a route whose handler is
[`sync_ingest`](reference/config.md#sync_ingest): JSON, NDJSON, or any format through a codec. A
request is answered `200` once every sink has its records, `503` with `Retry-After` while the
pipeline is not running or its pending requests are full, `400` for a body it cannot read, of which
nothing is written. A client that sends a request again until it is answered `200` loses nothing.
The handler never waits on a fib worker. Anyone who reaches the route writes into the pipeline, so
put an authenticating middleware before it.

The `http` sink posts each batch as NDJSON or a JSON array, with an `Idempotency-Key` that is the same
when the same records are sent again, and new after a reset of the checkpoint (its lineage); `2xx` acknowledges, `5xx`, `408`, `429` and timeouts are tried
again, any other answer fails the records for good.

```fit
listener web :8080 http/1.1 h2c

route ingest {
	match { path /ingest/events }
	middleware basic_auth {
		users {
			username collector
			password_hash $2a$10$gkDJyGagxp1q7R24F0rSjulsgZOFL55Zm6JK2RcxTCLDk23szhZA2
		}
	}
	handler sync_ingest { pipeline events; timeout 10s }
}

sync {
	state_dir /var/lib/fit/sync
	pipelines events {
		source http { namespace events; max_pending_requests 256 }
		transforms key { fields event_id }
		sinks collector {
			sink http {
				url https://collector.example.com/v1/events
				bearer_token_env COLLECTOR_TOKEN
				format ndjson
			}
		}
	}
}
```

### MySQL, MariaDB and TiDB

The `mysql` sink writes to MySQL 5.7 and later, MariaDB 10.11 and later, and TiDB: inserts, updates
and messages as `INSERT ... ON DUPLICATE KEY UPDATE`, deletes by the key's columns, each run of a
batch's records for one table a multi-row statement. `transactions source` (the default) writes each
source transaction as one of the server's, held open across batches when a batch ends inside it;
`transactions batch` groups several. `ddl report` (the default) logs DDL records and applies none;
`ddl stop` fails them, so that `on_error stop` stops the pipeline for a person to change the table.
`checkpoint sink` gives [exactly once](#delivery-guarantees). Against TiDB, transactions are bounded
by `max_transaction_bytes` (16 MiB) and `AUTO_RANDOM` columns take the source's values. The tables
are yours, each with a primary or unique key on the record's key columns; the session's time zone is
UTC. The rows go as text statements, their values as literals escaped for the session's `sql_mode`
(one round trip a statement); in a job each of `job.max_parallel` writers has a connection of its own.

The `mysql` source follows MySQL 5.7 and later or MariaDB 10.11 and later as a replica, registered
with its own `server_id`; it checks when it starts that the binlog is on (`log_bin`), in
`binlog_format = ROW` with `binlog_row_image = FULL`. Every insert, update and delete of the tables
`tables` names (globs of `db.table`, every table but the system databases' when left out) and
`exclude` does not becomes a record of namespace `db.table`, keyed by the primary key's columns (or
those `keys` names); an update that changed the key comes as a delete of the old key and an insert.
Column names come from the binlog where the server writes them (`binlog_row_metadata = FULL`), else
from `information_schema`, read again after each DDL. A DDL of a table read (`CREATE`, `ALTER`,
`DROP`, `RENAME`, `TRUNCATE`) is a `ddl` record holding the `statement` and the `database`, which the
SQL sinks report and do not apply.

Records come a transaction at a time, in commit order, with the transaction's GTID (or binlog file
and offset) and commit time. The position, at each transaction's end, is
`{"gtids": "...", "file": "mysql-bin.000042", "pos": 1234}`: a pipeline resumes from it by GTIDs where
the server has them (MySQL's `gtid_mode = ON`, MariaDB's GTIDs), else by file and offset. A stream cut,
as by a server restarting, is read again from the last transaction handed on. A position whose
binlogs the server has purged stops the pipeline with an error saying a new snapshot is needed.

With `snapshot initial` (the default) a pipeline without a checkpoint first reads the tables' rows,
then the binlog; `always` reads them at every start of the source; `never` reads the binlog alone,
from the checkpoint or from now. The binlog's position is read first, without a lock, then each table
by its key, `batch_size` (1,000) rows a query, each query in a `REPEATABLE READ` transaction with a
consistent snapshot; the stream then starts from that position, so what changed meanwhile comes
again and the sinks, writing by key, converge. A snapshot interrupted goes on after the last key
saved; a table without a primary key is refused unless `keys` names one. In a job (`mode once`) the
snapshot alone is read, each table split into chunks of `chunk_rows` (100,000) rows of its key. The
lag the source reports is the binlog bytes the server holds past its position (`SHOW BINARY LOGS`),
none once a `heartbeat` (10s) says it is caught up.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines shop-replica {
		source mysql {
			address mysql.internal:3306
			username fit_repl
			password_env MYSQL_PASSWORD
			tls true
			server_id 4101
			tables shop.*
			snapshot initial
		}
		sinks replica {
			sink mysql {
				address replica.internal:3306
				username fit_sync
				password_env MYSQL_SYNC_PASSWORD
				tls true
				checkpoint sink
				database shop
			}
			batch { max_records 500; max_wait 200ms }
		}
	}
}
```

### TiDB through TiCDC

The `tidb` source reads a snapshot of `tables` at one timestamp over MySQL's protocol, then the
changes a TiCDC changefeed writes to Kafka after it (`protocol canal-json`, created with
`enable-tidb-extension=true`, or `simple`), leaving out those committed at or before the snapshot.
TiDB as a sink is the `mysql` sink.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines tidb-to-pg {
		source tidb {
			address tidb:4000
			username fit_sync
			password_env TIDB_PASSWORD
			tables shop.orders shop.order_items
			protocol canal-json
			kafka { brokers kafka-1:9092; topics ticdc-shop }
		}
		sinks pg {
			sink postgres {
				address pg:5432
				user fit
				password_env PG_PASSWORD
				database replica
			}
		}
	}
}
```

The changefeed must exist before the snapshot is taken, its topic must keep the messages since, and
the snapshot must finish within `tidb_gc_life_time`.

### PostgreSQL

The `postgres` source follows PostgreSQL 13 to 17 as a logical replica: a `publication` names the
tables, a `slot` keeps the server's WAL until fit confirms it, and the stream is read with the
built-in `pgoutput`. With `create true` a missing publication (`FOR TABLE` the `tables`) and slot are
made; neither is changed or dropped later. With `snapshot initial` (the default) a pipeline without
a checkpoint first reads every row from the snapshot the slot exports, in chunks of `chunk_rows`,
`snapshot_parallel` at a time, and the stream starts at that very point: no change is in both and
none in neither. Records come a transaction at a time, in commit order; the slot is told an LSN only
once every sink has it and the checkpoint is saved. Large transactions the server streams are spooled
to `spool_dir` until they commit. The slot's lag is reported, and logged past `lag_warning` (1 GiB):
a stopped pipeline makes the server keep WAL.

The `postgres` sink upserts with `INSERT ... ON CONFLICT (key) DO UPDATE` and deletes by key, a
statement per run of up to `max_statement_rows`, and copies a run of `copy_min_rows` (100) upserts or
more with `COPY` into a temporary table merged in one statement. `table` is `{namespace}`, split at
its first dot into schema and table, so a MySQL source's `shop.orders` is the table `orders` of the
schema `shop`. `transactions batch` (the default) or `source`; `checkpoint sink` gives
[exactly once](#delivery-guarantees) and makes `source` the default.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-out {
		source postgres {
			address db.internal:5432
			user repl
			password_env PG_REPL_PASSWORD
			database shop
			create true
			publication fit_orders
			slot fit_orders
			tables public.orders public.order_items
		}
		sinks copy {
			sink postgres {
				address replica.internal:5432
				user sync
				password_env PG_PASSWORD
				database shop
				checkpoint sink
			}
			batch { max_records 1000 }
		}
	}
}
```

Both use TLS with `sslmode verify-full` by default (`verify-ca`, `require`, `disable`) and
SCRAM-SHA-256, with channel binding when the server offers it; MD5 and cleartext passwords are
refused. Drop the slot (`SELECT pg_drop_replication_slot('fit_orders')`) when a pipeline goes for
good.

### SQL Server

The `mssql` source follows SQL Server 2016 and later (2019 and 2022 tested) through its CDC change
tables, which SQL Server Agent's capture job fills from the transaction log. With `snapshot initial`
(the default) a pipeline without a checkpoint first reads CDC's highest LSN, then every row of the
tables by key, `batch_size` (1000) at a time, each batch in a transaction of snapshot isolation when
the database allows it (`ALLOW_SNAPSHOT_ISOLATION ON`), read committed otherwise; the changes then
start at that LSN, so what changed during the snapshot comes again, as the MySQL source's does. The
change tables are polled every `poll_interval` (1s) once caught up, up to `window_transactions`
(1000) transactions at a time, and the changes of each transaction are handed on together, in the
order the server made them, across the tables: an update with its row before and after (a key
changed too), a delete with the row it removed, a DDL that CDC recorded in `cdc.ddl_history` as a
`ddl` record. The checkpoint is the LSN of the last transaction every sink has. A window of more than
`window_rows` (10000) changed rows is read again smaller, and a single transaction of more is read in
pages. Records are of namespace `schema.table`, keyed by the primary key (or the unique index CDC was
enabled with, or `keys`).

A table named whole in `tables` must be captured, or the pipeline fails with the
`sys.sp_cdc_enable_table` call that captures it; a glob such as `dbo.*` matches the captured tables,
and a table CDC begins to capture while the pipeline runs is read whole, then followed. A second
capture instance of a table (made after a schema change, with the new columns) is followed from its
start, the older one up to there; an older instance dropped (`sys.sp_cdc_disable_table`) while the
pipeline runs, before it read that instance's changes up to the newer one's start, stops the pipeline
asking for a new snapshot (a drop while it is stopped leaves no trace to find), and a table no longer
captured at all is logged and no longer read. A checkpoint older than what CDC's cleanup keeps (three days by
default) stops the pipeline: a new snapshot is needed. The lag reported is the transactions of
`cdc.lsn_time_mapping` after the position (with `mode change_tracking`, the versions). `mode change_tracking` reads SQL Server's change
tracking instead, lighter on the server: the keys changed since the version in the checkpoint, each
row read back as it is then (a row gone a delete), every table's changes of one poll a transaction;
changes between two polls are merged into the last, and updates have no row before.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-from-mssql {
		source mssql {
			address mssql.internal:1433
			username fit_cdc
			password_env MSSQL_PASSWORD
			database shop
			tables dbo.orders sales.*
			ca_file /etc/fit/mssql-ca.pem
		}
		sinks search {
			sink elasticsearch { urls https://es:9200; username fit; password_env ES_PASSWORD }
		}
	}
}
```

The connection is TLS by default (`encrypt on`, negotiated in TDS's pre-login, the server's
certificate checked against `ca_file` or the system's roots), `strict` for TDS 8 (SQL Server 2022),
or `off`; SQL logins only, not Windows or Entra ID authentication.

The `mssql` sink writes upserts by the record's key with `MERGE ... WITH (HOLDLOCK)` of each run of a
batch's rows for one table (`upsert update_insert`: an `UPDATE`, then an `INSERT` when it found no
row), and deletes by the key; a run of `bulk_rows` (1000) inserts or more without a source
transaction, a snapshot's, is bulk loaded (`INSERT BULK`) into a temporary table and merged from it.
Each source transaction is one of the server's (`transactions source`), or a batch's are together
(`batch`), never committed in part; a statement the server refuses for good fails its record alone,
the records before it committed. With `checkpoint sink` the position is written to
`_fit_sync_checkpoints` in the same transaction, for exactly once. `identity_insert` writes the
source's values into `IDENTITY` columns. Tables are named `schema.table` (or `db.schema.table`) from
the namespace: a MySQL source's `shop.orders` is the schema `shop`'s `orders`, so rename it with a
`route` transform (`rules { match shop.*; to dbo.{rest} }`) or give one table by name (`dbo.{namespace}`
serves only namespaces without a dot, such as a topic's). The tables are yours, with a primary key or
unique index on the key's columns. A NULL is sent as its column's type, read once a connection; a
statement refused (a column's type changed meanwhile) reads the types again before its record fails.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-mssql {
		source mysql {
			address mysql:3306
			username fit_repl
			password_env MYSQL_PASSWORD
			server_id 4102
			tables shop.orders
		}
		sinks mssql {
			sink mssql {
				address mssql.internal:1433
				username fit_writer
				password_env MSSQL_PASSWORD
				database dw
				table dbo.orders
				checkpoint sink
				ca_file /etc/fit/mssql-ca.pem
			}
		}
	}
}
```

### MongoDB

The `mongo` source reads MongoDB 6.0 and later, a replica set or a sharded cluster through `mongos`:
a snapshot of the collections by `_id` (`snapshot initial`), then their change stream from the resume
token taken before it, the two overlapping so that the sinks' upserts converge. Inserts, updates,
replaces and deletes are records of their kind; a dropped or renamed collection is a `ddl` record.
The changes of a transaction carry its ID, so the checkpoint never falls inside one. A token the oplog
no longer holds needs a new snapshot, which the error says. The `mongo` sink writes unordered bulk
upserts and deletes by the `_id` the key makes, a batch's last record of each document only, with the
write concern you set (never `w: 0`). Both take the same connection params: `uri` (its password, if
any, refused) or `hosts`, `password_env`, `tls` and `ca_file`.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines shop-copy {
		source mongo {
			uri "mongodb://sync@db1:27017,db2:27017,db3:27017/?replicaSet=rs0&tls=true"
			password_env MONGO_SYNC_PASSWORD
			database shop
			collections orders customers
		}
		sinks archive {
			sink mongo {
				uri "mongodb://archive1:27017,archive2:27017/?replicaSet=rs1"
				username fit
				password_env MONGO_PASSWORD
				database shop_archive
				write_concern majority
			}
		}
	}
}
```

### Kafka and Redpanda

The `kafka` source reads every partition of `topics` (Kafka 3.x and later, Redpanda) from the offsets
in the pipeline's checkpoint: no consumer group is joined and no offset committed, so the checkpoint
is the only cursor. Each message goes through `codec` (`raw` by default; `json`, `canal_json`,
`debezium`, `ticdc_simple` or a plugin read the formats); a null value is a delete of its key. A
checkpoint offset that retention deleted stops the pipeline unless `offset_reset` says otherwise. The
lag reported is the offsets behind the partitions' high watermarks.

The `kafka` sink produces a message per record to `topic` (`{namespace}`), keyed by the record's key,
which the Java client's partitioner (murmur2) places, so a key's messages stay in order; deletes are
tombstones; batches are compressed (`lz4`) and acknowledged by every in-sync replica. TLS and SASL
PLAIN, SCRAM-SHA-256 and SCRAM-SHA-512 authenticate.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-search {
		source kafka {
			brokers kafka-1:9093 kafka-2:9093
			topics shop.orders
			tls true
			sasl SCRAM-SHA-512
			username fit
			password_env KAFKA_PASSWORD
			codec debezium
		}
		sinks search {
			sink elasticsearch { urls https://es:9200; username fit; password_env ES_PASSWORD }
		}
	}
}
```

### MQTT

The `mqtt` source subscribes to topic filters on an MQTT broker (3.1.1 or 5: EMQX, Mosquitto, HiveMQ,
VerneMQ) in a persistent session named by its `client_id`, which the broker keeps, with the QoS 1 and
2 messages for it, while fit is away (`session_expiry`, 24h, in version 5). A QoS 1 or 2 message is
acknowledged (`PUBACK`, or `PUBREC` then `PUBCOMP`) only once every sink has its records, in the order
the messages came, so a pipeline stopped or killed before its sinks wrote a message gets it again from
the broker: at least once into idempotent sinks, a QoS 2 message taken once within a connection. The
checkpoint holds nothing to resume from: the session is the position. A message is a record of the
topic's namespace (or `namespace`), keyed by the topic unless the codec sets a key; without a `codec`
its after holds `topic`, `payload` (bytes), `qos`, `retain` and, in version 5, the properties it has
(`content_type`, `response_topic`, `correlation_data`, `user_properties`). In a job the source reads
until no message comes for `idle` (5s).

The `mqtt` sink publishes each record to `topic` (`{namespace}`, with `{key}` too) at `qos` (1),
retained with `retain`; a batch's messages go out in order, at most `max_inflight` (64)
unacknowledged, and the batch is written once the broker acknowledged each. The payload is the codec's
encoding, or, without one, after's `payload`, so a topic copied through the source and the sink stays
byte for byte, with its version 5 properties. A delete with `retain` is an empty retained message,
which clears the topic. A publish a version 5 broker refuses for good (topic name invalid, not
authorized, packet too large) goes to `on_error` at once.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines sensors {
		source mqtt {
			servers emqx1:1883 emqx2:1883
			client_id fit-sensors
			topics sensors/+/temperature devices/#
			qos 1
			codec json
		}
		sinks archive {
			sink mqtt {
				servers mosquitto:8883
				tls true
				username fit
				password_env MQTT_PASSWORD
				topic archive/{namespace}
				qos 2
				retain true
			}
		}
	}
}
```

A message is acknowledged only once the sinks have it, so what a broker sends unacknowledged bounds a
batch: EMQX's `mqtt.max_inflight` is 32 and Mosquitto's `max_inflight_messages` 20 by default; raise
them, and the source's `receive_maximum` (128) in version 5, above the sinks' `batch.max_records`, or
keep `batch.max_wait` short. Each pipeline needs a `client_id` of its own: a second client with the
same ID takes the session over.

### RabbitMQ

The `rabbitmq` source consumes `queues` (classic and quorum, RabbitMQ 3.13 and 4.x) over AMQP 0-9-1
with a `prefetch` (`basic.qos`, 1000 by default), and acknowledges each delivery only once every sink
has its records: a queue's place is the broker's, nothing of it is in the checkpoint, and what was not
acknowledged when fit stopped is delivered again. `streams` are stream queues, read from the offset
after the last one in the checkpoint (or `stream_start`, `first` by default, without one). Each body
goes through `codec` (`raw` by default); a message record holds `exchange`, `routing_key`, `queue`,
`redelivered`, `headers` and the properties set beside its `value`, and `metadata_field` puts them in
a field of the rows a codec makes. The record's key is the `fit-key` header the `rabbitmq` sink
writes. In a job (`mode once`) the source ends once no message has come for `idle_end` (2s), every
record acknowledged. The lag reported is the queues' ready messages (asked every 10s; a stream queue
has no such count).

The `rabbitmq` sink publishes a persistent message per record to `exchange` (the default exchange
when left out) with `routing_key` (`{namespace}`; `{key}` too), its `message_id` the same each time
the record is written again, and holds a batch until the broker confirms all of it (publisher
confirms). A message no queue takes fails its record (`unroutable error`, the default; `drop` lets
the broker drop it). Delivery is at least once: a consumer can drop repeats by `message_id`. TLS
(`ca_file`, `cert_file`), PLAIN and EXTERNAL authenticate; heartbeats, and the broker's memory and
disk alarms, which hold publishing for up to `timeout`, are honoured.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-queue {
		source rabbitmq {
			addresses rabbit-1:5671 rabbit-2:5671
			tls true
			username fit
			password_env RABBITMQ_PASSWORD
			queues orders
			prefetch 500
			codec json
		}
		sinks archive {
			sink rabbitmq {
				addresses archive:5672
				username fit
				password_env RABBITMQ_PASSWORD
				exchange archive
				routing_key orders.{namespace}
				content_type application/json
				codec json
			}
		}
	}
}
```

### NATS JetStream

The `nats` source reads a JetStream `stream` (nats-server 2.10 and later) through a durable pull
`consumer`, acknowledging each message only once every sink has it, so a message is never lost to a
crash: at least once. With `create_consumer` fit makes the consumer (ack policy all, `subjects` as its
filter, `start` all or new, `ack_wait`, `max_ack_pending`); the stream sequence is in each record's
position and in the checkpoint, and a consumer fit makes is made again from the checkpoint when it is
lost, or when a run finds it holding messages delivered and not acknowledged, so that they come again
first and in order. A consumer of your own is read from its acknowledgement floor and needs ack policy
`explicit`. Each message goes through `codec` (`raw` by default); a message record holds the subject,
the headers, the stream, the sequence and the time beside its `value`, and `metadata_field` puts them
in rows a codec makes. The lag reported is the consumer's pending messages. `mode core` subscribes to
core NATS `subjects` instead (a `queue` group to share them), at most once, for telemetry: what arrives
while fit is down, or faster than it keeps up (`buffer`), is lost.

The `nats` sink publishes a message per record to `subject` (`{namespace}`), its data from `codec`,
with a `Nats-Msg-Id` made from the record's origin (see [Delivery guarantees](#delivery-guarantees)), so that a batch written again within the stream's
duplicate window (`duplicate_window`, 2 minutes by default) is stored once; up to `max_in_flight`
publishes wait for the stream's acknowledgement at once, and a batch is acknowledged only once each of
its messages is. `mode core` publishes to core NATS, confirmed as far as the server reading them.

Both connect to `servers` (the cluster's others are learnt, and the client moves to another when one
fails), over TLS with `tls`, and authenticate with a token (`token_env`), a user and password
(`username`, `password_env`), an NKey seed (`nkey_file`) or a creds file of a JWT and its seed
(`creds_file`).

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-search {
		source nats {
			servers nats-1:4222 nats-2:4222 nats-3:4222
			tls true
			username fit
			password_env NATS_PASSWORD
			stream ORDERS
			consumer fit-orders-to-search
			create_consumer true
			subjects orders.>
			codec json
		}
		transforms key { fields order_id }
		sinks search {
			sink elasticsearch { urls https://es:9200; username fit; password_env ES_PASSWORD }
		}
	}
}
```

### Apache Pulsar

The `pulsar` source reads every partition of `topics` (Pulsar 2.10 and later; a bare name is
`public/default`'s). By default (`mode reader`) each partition is read by a non-durable reader from the
message IDs in the pipeline's checkpoint, so nothing is kept on the broker, and messages that retention
removed before they were read are lost; `mode subscription` reads through the durable `subscription`
(`exclusive` or `failover`), acknowledged cumulatively once every sink has the records, the message
IDs still in the checkpoint so that what the broker sends again is passed over. Each message goes
through `codec` (`raw` by default); a message record also holds the message's `topic`, `message_id`,
`properties`, `producer`, `publish_time` and `event_time`, and a row a codec makes holds them under
`metadata_field` when it is set. A null value is a delete of the message's key. Encrypted and chunked
messages are not read.

The `pulsar` sink sends a message per record to `topic` (`{namespace}`), keyed by the record's key,
which the Java client's default router (the key's Java string hash) places in a partition, so a key's
messages stay in order; deletes have a null value, for topic compaction. A partition's messages go out
as Pulsar batches (`max_batch_messages`, `max_batch_bytes`), compressed (`lz4`; `zlib`, `zstd`,
`snappy`), from producers named `producer_name`, whose sequence IDs carry on from the broker's. Each
message carries the property `fit-id`, made from the record's origin (see [Delivery guarantees](#delivery-guarantees)):
with `dedup read_back` (the default) the sink reads back the last 64 entries of each partition when it
makes its producer, and drops the records it finds there stored, so a batch written again after a crash
is stored once (this needs the permission to consume the topic; `dedup off` sends everything). A
`pulsar+ssl://` URL is TLS; `token_env` authenticates with a token.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-pulsar {
		source kafka {
			brokers kafka-1:9092
			topics shop.orders
			codec debezium
		}
		sinks events {
			sink pulsar {
				url pulsar+ssl://pulsar.internal:6651
				token_env PULSAR_TOKEN
				topic shop/orders/{namespace}
				producer_name orders-to-pulsar
				codec json
			}
		}
	}
}
```

### Redis and Valkey

The `redis` source reads `streams` with `XREAD` from the last entry IDs read (no consumer group, nothing
acknowledged or removed), each entry a `message` of its fields, or with `form record` the record the
`redis` sink wrote. The `redis` sink adds stream entries with `XADD` (with `entry_id key`, IDs from the
keys, for an exact copy), or writes a key per record (`mode keys`: a JSON value, the whole record or a
hash, with a `ttl`), deletes deleting it. Redis 6 and later, Valkey; a standalone server or a
primary, not Redis Cluster.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-cache {
		source redis {
			address redis.internal:6379
			username sync
			password_env REDIS_SYNC_PASSWORD
			streams events:orders
		}
		transforms key { fields order_id }
		sinks cache {
			sink redis {
				address cache.internal:6379
				password_env REDIS_PASSWORD
				mode keys
				key order:{key}
				value hash
				ttl 24h
			}
		}
	}
}
```

### S3 and compatibles

The `s3` sink writes each batch as an object of lines (by a codec, gzipped if asked), named by a
`key` template from its first record, so a batch written again is the same object; objects roll by the
sink's `batch`, so give it larger batches than the defaults. The `s3` source reads the objects under
`prefix` in key order, as lines, in ranges, resumed inside an object, and lists again every
`poll_interval` for keys past the last: keys must grow as objects are added, as the sink's do. AWS
without `endpoint`; MinIO, SeaweedFS, Ceph, R2 and other compatibles with one. Requests are signed
with Signature Version 4.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines logs-to-s3 {
		source file { paths /var/log/app/*.log }
		queue { max_records 200000; max_bytes 268435456 }
		sinks archive {
			sink s3 {
				endpoint https://minio.internal:9000
				bucket logs
				access_key_id fit-writer
				secret_access_key_env S3_SECRET
				codec json
				gzip true
				key app/{yyyy}/{mm}/{dd}/{timestamp}-{position}{ext}
			}
			batch { max_records 100000; max_bytes 134217728; max_wait 5m }
		}
	}
}
```

The sink's credentials need `s3:PutObject` on the prefix; the source's `s3:ListBucket` and
`s3:GetObject`.

### InfluxDB

The sink writes each insert and update as a point of the line protocol: the measurement from a template
(`{namespace}`, or a field's value), `tags` from fields of the row, the other fields (or `fields`) as
the point's fields, and the time from `time_field` or the commit time, cut to `precision`. A point is
its series (measurement and tags) and its time, so a record written again writes the same point over
itself, and an update is the point's new value. The line protocol has no deletes: they are left out
(`deletes fail` sends them to `on_error`). `api` picks the endpoint: `v2` (`/api/v2/write`, `org` and
`bucket`, a token), `v3` (`/api/v3/write_lp`, `database`, a bearer token), `v1` (`/write`, `database`
and `retention_policy`, a user and password or a token). Requests are gzipped and bounded by
`max_request_bytes`; a 429 or 5xx is sent again with a backoff, and a point the server refuses (a
field of another type than its column's) fails its record alone: the records of a refused request are
sent again one by one to find it. InfluxDB 2.7 and 3 Core, and InfluxDB 2's v1 API.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines readings {
		source kafka { brokers kafka:9092; topics readings; codec json }
		sinks influx {
			sink influxdb {
				url http://influxdb:8086
				org iot
				bucket readings
				token_env INFLUX_TOKEN
				measurement "{kind}"
				tags device site
				time_field at
				precision ms
			}
			batch { max_records 5000; max_wait 1s }
		}
	}
}
```

Keep tags to fields of bounded cardinality: each tag value is a series of its own. A record without a
`time_field` or a commit time gets the time it is written, which a replay does not repeat.

### TDengine

The sink writes through taosAdapter's REST interface (port 6041). `mode schemaless` (the default)
sends the same line protocol to `/influxdb/v1/write`: the measurement is the super table and the tags
its subtable's, which TDengine makes, with their columns, as they are first written. `mode sql` writes
`INSERT INTO db.sub USING db.stable (tags) TAGS (...) (ts, columns) VALUES (...)` into the super table
you made, the subtable named by `subtable` (a template, as `d_{device}`) or by a hash of the tags. A row
is its subtable and its timestamp: written again, it is written over itself. `precision` must be the
database's (`ms` by default). A row TDengine refuses (a type mismatch, a syntax error) fails its record
alone; a busy server is tried again.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines meters {
		source generate { namespace meters; records 1000 }
		sinks td {
			sink tdengine {
				url http://tdengine:6041
				username root
				password_env TD_PASSWORD
				database power
				mode sql
				measurement meters
				subtable "m_{id}"
				fields price count
				precision ms
			}
		}
	}
}
```

### Qdrant and Milvus

The `qdrant` sink upserts each insert and update as a point of `collection` (a template, made by you),
its vector from `vector_field` (an array of numbers, `embedding` by default; `vector_name` for a named
vector) and the rest of the row, or `payload`'s fields, as its payload, and deletes deletes the point.
The point's ID comes from the record's key: an unsigned integer or a UUID as it is, any other key a
UUID made from its text, the same each time. Writes wait for Qdrant to apply them (`wait=true`); an API
key goes in `api_key_env`.

The `milvus` sink upserts the row as an entity of `collection` through the RESTful API v2 (Milvus 2.4
and later, Zilliz Cloud), its `primary_key` (`id`) set from the key: an `Int64` from a single integer,
a `VarChar` from any other key's text; deletes delete by it. Milvus converts an upsert's key to the
field's type, but a delete's filter must hold it as that type, so the sink describes the collection at
its first delete and writes the key as its primary key's type (an integer key quoted for a `VarChar`, a
string of digits as a number for an `Int64`). The collection is made by you, with a
primary key that is not `auto_id`, and the row's fields (or `fields`) must be in its schema or its
dynamic field. A token goes in `token_env`, or `username` and `password_env`.

Both write each key's last record of a batch, deletes and upserts, so a request holds no key twice; a
429, a 5xx or a busy Milvus is tried again, and an entity refused (a vector of another size, a field the
schema lacks) fails its record alone.

The `embed` transform computes the vectors: it joins the text of `fields` and asks an OpenAI-compatible
embeddings API (`url`, `/embeddings` added; this config's [llm.proxy](llm.md) when left out) for its
vector, which it sets in `into` (`embedding`). A batch's texts go in requests of `batch_records`,
`max_parallel` at once, each text once; vectors are cached by a hash of the model and the text, within
`cache_bytes`, so a replay or an update of other fields asks for nothing. A request that fails after
`attempts`, or a text the API refuses or longer than `max_text_bytes`, fails its records, which
`on_error` takes. Deletes and records without the fields pass as they are.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines docs-to-vectors {
		source postgres {
			address db.internal:5432
			user repl
			password_env PG_REPL_PASSWORD
			database app
			create true
			publication fit_docs
			slot fit_docs
			tables public.docs
		}
		transforms embed {
			url https://api.openai.com/v1
			model text-embedding-3-small
			api_key_env OPENAI_API_KEY
			fields title body
		}
		sinks qdrant {
			sink qdrant { url http://qdrant:6333; collection docs; payload id title }
			batch { max_records 256 }
		}
		sinks milvus {
			sink milvus { url http://milvus:19530; token_env MILVUS_TOKEN; collection docs; fields title embedding }
			batch { max_records 256 }
		}
	}
}
```

### Trials: generate, memory and stdout

`generate` makes numbered records of every value type, the same each time, in transactions of
`tx_size`, ending after `records` (or never); `chunk_records` splits it for trying out jobs. `memory`
keeps records in a named store, bounded, for tests; `stdout` prints each as a line of canonical JSON.
They are the [first pipeline](#a-first-pipeline)'s, and a quick way to try a transform or a sink.

## What each source needs on its server

| Source | Server settings | The user's rights |
| --- | --- | --- |
| `mysql` | `log_bin` on, `binlog_format = ROW`, `binlog_row_image = FULL`; `gtid_mode = ON` and `enforce_gtid_consistency = ON` to resume by GTID (MySQL); `binlog_row_metadata = FULL` recommended; binlogs kept longer than a pipeline may stop (`binlog_expire_logs_seconds`) | `REPLICATION SLAVE`, `REPLICATION CLIENT` and `SELECT` on the tables; a `server_id` no other replica has |
| `postgres` | `wal_level = logical`; a free `max_replication_slots` and `max_wal_senders` per pipeline; a primary key or `REPLICA IDENTITY` on each table whose updates or deletes are published | `REPLICATION`, `SELECT` on the tables, and with `create` the right to create the publication (the tables' owner, or superuser for `FOR ALL TABLES`) |
| `mongo` | a replica set or a sharded cluster (change streams need one); `changeStreamPreAndPostImages` on the collections for `full_document whenAvailable` or `full_document_before_change`; an oplog window longer than a pipeline may stop | `read` on the database (`readAnyDatabase` for every database) |
| `tidb` | a TiCDC changefeed to Kafka (`canal-json` with `enable-tidb-extension=true`, or `simple`) started before the snapshot; `tidb_gc_life_time` longer than the snapshot | `SELECT` on the tables; the Kafka source's rights on the topic |
| `mssql` | CDC on for the database and the tables (`sys.sp_cdc_enable_db`, `sys.sp_cdc_enable_table`), SQL Server Agent running (its capture job fills the change tables), a cleanup retention longer than a pipeline may stop (three days by default); `ALLOW_SNAPSHOT_ISOLATION ON` for snapshots without locks; with `mode change_tracking`, change tracking on for the database and the tables | `SELECT` on the tables and on the `cdc` schema (`db_owner`, or the gating role given to `sp_cdc_enable_table`); `VIEW CHANGE TRACKING` on the tables with change tracking |
| `mqtt` | persistent sessions kept long enough (EMQX's `session_expiry_interval`, Mosquitto's `persistence true` and `persistent_client_expiration`) and room for the messages queued while fit is away (`max_mqueue_len`, `max_queued_messages`); not Mosquitto's `per_listener_settings true`, which drops them | subscribe on the topics |
| `kafka` | retention longer than a pipeline may stop | `Describe` and `Read` on the topics (no group rights: no group is joined) |
| `rabbitmq` | durable queues (classic or quorum) or stream queues, made by you; a stream's retention longer than a pipeline may stop | `read` on the queues (`configure` is not needed: nothing is declared but passively) |
| `nats` | a JetStream stream, made by you; with `create_consumer`, the consumer is fit's | publish to `$JS.API.STREAM.INFO.<stream>`, `$JS.API.CONSUMER.*.<stream>.>`, `$JS.API.CONSUMER.MSG.NEXT.<stream>.<consumer>` and `$JS.ACK.<stream>.>`, subscribe to `_INBOX.>` |
| `pulsar` | retention longer than a pipeline may stop (`mode reader` keeps nothing on the broker) | `consume` on the topics |
| `redis` | streams trimmed no faster than the pipeline reads | `+xread` (or `+@stream`) on the streams' keys |
| `elasticsearch` | a cursor field every write sets later than the last (an update time), and a soft-delete field for deletes | `read` and `view_index_metadata` on the indices |
| `clickhouse` | a cursor column that grows with each write | `SELECT` on the table |
| `doris` | a cursor column that grows with each write | `SELECT` on the table |
| `s3` | keys that grow as objects are added | `s3:ListBucket` and `s3:GetObject` |
| `file` | rotation by rename or copy and truncation | read on the files and their directories |

For MySQL, in `my.cnf` and SQL:

```ini
[mysqld]
server_id = 1
log_bin = mysql-bin
binlog_format = ROW
binlog_row_image = FULL
binlog_row_metadata = FULL
gtid_mode = ON
enforce_gtid_consistency = ON
```

```sql
CREATE USER 'fit_repl'@'%' IDENTIFIED BY '...';
GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'fit_repl'@'%';
GRANT SELECT ON shop.* TO 'fit_repl'@'%';
```

For PostgreSQL, `wal_level = logical` in `postgresql.conf` (a restart), then:

```sql
CREATE ROLE repl WITH LOGIN REPLICATION PASSWORD '...';
GRANT SELECT ON public.orders, public.order_items TO repl;
CREATE PUBLICATION fit_orders FOR TABLE public.orders, public.order_items;  -- or create true
```

For SQL Server, in the database, with SQL Server Agent running:

```sql
ALTER DATABASE shop SET ALLOW_SNAPSHOT_ISOLATION ON;
USE shop;
EXEC sys.sp_cdc_enable_db;
EXEC sys.sp_cdc_enable_table @source_schema = N'dbo', @source_name = N'orders', @role_name = N'fit_cdc_reader';
CREATE LOGIN fit_cdc WITH PASSWORD = '...';
CREATE USER fit_cdc FOR LOGIN fit_cdc;
ALTER ROLE fit_cdc_reader ADD MEMBER fit_cdc;
GRANT SELECT ON SCHEMA::dbo TO fit_cdc;
GRANT SELECT ON SCHEMA::cdc TO fit_cdc;
```

The sinks need write rights only: `INSERT`, `UPDATE` and `DELETE` on the tables (MySQL, PostgreSQL),
`CREATE` too for `checkpoint sink`'s table; `readWrite` on the database (MongoDB); `Write` and, with
`create_topics`, `Create` (Kafka); `LOAD` on the tables (Doris; `INSERT` on StarRocks); `write` on the exchange (RabbitMQ); publish on the subjects and subscribe to `_INBOX.>` (NATS); `produce`, and `consume` too with `dedup read_back` (Pulsar), whose broker deduplication (`brokerDeduplicationEnabled`) carries the producers' sequence IDs across restarts. Tables and indexes in SQL and ClickHouse sinks are made by you: DDL
is reported, never replayed.

## Types between systems

A source reads its server's types into the record's values, and a sink writes the values as its
server's types:

| Value | From MySQL | From PostgreSQL | From MongoDB | To MySQL | To PostgreSQL | To Elasticsearch, ClickHouse, JSON |
| --- | --- | --- | --- | --- | --- | --- |
| null | `NULL` | `NULL` | null | `NULL` | `NULL` | `null` |
| bool | (`BOOL` is `TINYINT`: an int) | `boolean` | bool | 1 or 0 | `boolean` | `true`, `false` |
| int | signed integers, `YEAR` | `smallint`, `integer`, `bigint`, `oid` | int32, int64 | integers | integers, `numeric` | a number |
| uint | unsigned integers, `BIT` | | | integers | integers, `numeric` | a number |
| float | `FLOAT`, `DOUBLE` | `real`, `double precision` | double | `FLOAT`, `DOUBLE` | `real`, `double precision` | a number |
| decimal | `DECIMAL` | `numeric` | decimal128 | `DECIMAL` | `numeric` | a number |
| string | `CHAR`, `VARCHAR`, `TEXT`, `ENUM` and `SET` names, `TIME` | `text`, `varchar`, and every other type as the server's text (`uuid`, `time`, `interval`, `inet`, enums, ranges) | string | text columns | `text`, `varchar`, enums, `uuid`, `inet`, ... | a string |
| bytes | `BINARY`, `VARBINARY`, `BLOB`, `GEOMETRY` | `bytea` | binary of subtype 0 | binary columns | `bytea` | base64 |
| time | `DATETIME`, `TIMESTAMP` (UTC) | `timestamp` (UTC), `timestamptz` | datetime (UTC) | `DATETIME`, `TIMESTAMP` in UTC | `timestamptz`, `timestamp` | RFC 3339 |
| date | `DATE` | `date` | | `DATE` | `date` | `YYYY-MM-DD` |
| JSON | `JSON` | `json`, `jsonb` | | `JSON` (as text) | `json`, `jsonb` | the document |
| array | | arrays | array | `JSON` (as text) | arrays | an array |
| map | | | document | `JSON` (as text) | `json`, `jsonb` | an object |

MongoDB's types without a kind of their own (ObjectId, timestamps, regular expressions, binary of
other subtypes, ...) are kept as tagged maps named as MongoDB's extended JSON names them,
`{"$oid": "..."}`, so that the `mongo` sink writes them back as they were; the MongoDB sink writes ints
as int64, decimals as decimal128 (exactly, or the record fails), and times and dates as datetimes to
the millisecond. ClickHouse writes decimals as numbers and reads times with `best_effort`; Kafka,
HTTP, S3 and the file sink write what their codec makes. A `cast` transform changes fields' types
between these where a sink needs another.

## Transforms

`transforms` change records on their way to the sinks, in order: `filter` keeps or drops records by
namespace, operation and fields' values; `route` renames namespaces (tables to indices, topics);
`fields` unnests, nests, renames, drops, keeps and sets fields; `key` builds the key from fields; `cast`
changes types; `mask` replaces fields by a keyed HMAC-SHA256, a SHA-256 or null; `envelope` makes a
document of a change (flat with metadata, before and after, or Debezium's); `embed` sets a vector computed from
text fields by an embeddings API ([Qdrant and Milvus](#qdrant-and-milvus)). A delete keeps its key
through all of them. The [reference](reference/config.md#sync-transforms) has each one's params.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines shop {
		source generate { namespace shop.orders; records 100 }
		transforms filter { namespaces shop.*; exclude_namespaces shop.tmp_* }
		transforms route { rules { match shop.*; to shop-{rest} } }
		transforms key { fields id; format join }
		transforms mask { fields name; key_env SYNC_MASK_KEY }
		transforms envelope { meta { deleted __deleted }; deletes rewrite }
		sinks out { sink stdout }
	}
}
```

### Codecs

A codec turns the bytes a source reads (a file's lines, a Kafka message, an HTTP body, an S3 object)
into records, and records into the bytes a sink writes: `line` (the default), `json`, `csv`, `raw`,
`canal_json` (Canal's and TiCDC's), `debezium` (read typed by the events' schema, written without),
`ticdc_simple` (read), and `wasm`. A line a codec refuses still becomes a record holding the line and
the error, so nothing read is lost.

## Plugins

What the built-in connectors do not cover is a WebAssembly plugin, a WASI reactor run by wazero in
fit: a `wasm` transform (over each batch: drop, change, split, route), a `wasm` codec (a format of your
own), a `wasm` source (polling an API from its checkpoint) and a `wasm` sink (writing each batch).
Records cross in a binary form of lengths and tags, which plugins built on the guest SDKs take, or as
their canonical JSON for a plugin that does not take it, one built before it among them. A source's and a sink's only way out is fit's asynchronous HTTP
client, to the hosts `allow_hosts` names, and a sleep: no sockets. Each plugin runs on `instances` of
its own memory, bounded by `max_memory_bytes` and `timeout`; a call that traps or runs past them fails
its records, which `on_error` takes.

Package `plugin/wasm/guest` is the Go side of the calls (`OnTransform`, `OnDecode`, `OnEncode`,
`OnPoll`, `OnWrite`, `Emit`, `Fail`, `Row`), built by Go or by TinyGo
(`tinygo build -target=wasip1 -buildmode=c-shared -scheduler=none`: TinyGo's default scheduler costs
each call hundreds of microseconds), and the crate `plugin/wasm/guest/rust`
(`fit-guest`) the Rust one; fit's tests run a plugin of each. fit-examples has a transform
(`sync-wasm-transform`), a codec (`sync-wasm-codec`), a source (`sync-wasm-source`) and a sink
(`sync-wasm-sink`). A transform that drops test accounts:

```go
//go:build wasip1

package main

import (
	"strings"

	"github.com/lesismal/fit/plugin/wasm/guest"
)

func init() {
	guest.OnTransform(func(batch []guest.Record) error {
		for i := range batch {
			row, err := guest.ParseRow(batch[i].After)
			if err != nil {
				guest.Fail(i, err.Error())
				continue
			}
			v, _ := row.Get("email")
			if email, _ := guest.AsString(v); strings.HasSuffix(email, "@example.test") {
				continue // dropped: nothing emitted for it
			}
			guest.Emit(i, &batch[i])
		}
		return nil
	})
}

func main() {}
```

Built with `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o users.wasm`, it runs as
`transforms wasm { path /etc/fit/plugins/users.wasm; config "{}" }`. The whole example, which also
hides emails, is in [fit-examples](https://github.com/lesismal/fit-examples/tree/main/sync-wasm-transform).

## Batch jobs

A pipeline of `mode once` is a batch job, as DataX runs them: it reads its source to the end and
stops with its checkpoint written. A source that can split its snapshot (the databases by ranges of
the key, Elasticsearch by slices, `generate` by `chunk_records`) is read in chunks, `job.max_parallel`
(4) at a time, and each sink that takes writes at once gets as many writers, a key always going to
the same one. The checkpoint keeps the job's progress, so a job interrupted by a crash or a restart
goes on from its finished chunks. A finished job does not run again when fit restarts; `job.schedule`
(cron's five fields or `@daily`, in `job.time_zone`) starts it again, a new job each time, never over a
run still going.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines nightly-copy {
		mode once
		source postgres {
			address db.internal:5432
			user reader
			password_env PG_PASSWORD
			database shop
			publication fit_orders
			slot fit_orders
			tables public.orders
		}
		sinks warehouse {
			sink clickhouse { urls http://ch1:8123; username fit; password_env CH_PASSWORD; table shop.orders }
			batch { max_records 10000 }
		}
		job { max_parallel 8; schedule "0 3 * * *"; time_zone Asia/Shanghai }
	}
}
```

`fit sync run -c fit.conf --once nightly-copy` runs a job from the command line, without serving
anything else, and exits 0 when every record was written, 3 when records failed (`skip` or
`dead_letter`), 1 when the pipeline stopped.

## The Admin API, fit sync and the workbench

The Admin API, and the console under `/api/m/admin/`, show and steer the running pipelines:

| Endpoint | What it does |
| --- | --- |
| `GET /sync/pipelines`, `/sync/pipelines/{name}` | each one's state, checkpoint, queue against its bounds, records read, acknowledged and failed, throughput, lag, errors and the last, per sink counts, dead letters |
| `POST /sync/pipelines/{name}` `{"action": "pause"}` | holds the source while what it read is written and the checkpoint saved |
| `{"action": "resume"}` | lets it go on; also starts again a pipeline stopped on an error, from its checkpoint |
| `{"action": "snapshot", "confirm": true}` | reads the source again from its start (the checkpoint removed) |
| `GET`, `PUT {"position", "confirm"}`, `DELETE ?confirm=1` `.../checkpoint` | shows, sets or resets the checkpoint, the pipeline restarted from there |
| `POST .../test` | tests the source's and sinks' connections, writing no record |
| `POST .../dry-run` `{"limit", "timeout", "from": "start"}` | reads the first records (10 within 5s) through the transforms and returns what each became, writing nothing and saving no position |
| `POST /sync/test`, `/sync/dry-run` `{"pipeline": {...}}` | the same for a pipeline's config not applied yet |
| `GET`, `POST .../dead-letters/replay`, `DELETE` `.../dead-letters` | lists, replays and drops [dead letters](#dead-letters) |

A change that would skip or replay records (a checkpoint set to another position, a reset or a
snapshot while there is a checkpoint) is answered 409 unless it says `confirm`. The same through `fit
sync`, which finds the Admin API from the bootstrap config, as `fit token` does:

```sh
fit sync status -c fit.conf
fit sync status -c fit.conf orders-out
fit sync pause -c fit.conf orders-out
fit sync resume -c fit.conf orders-out
fit sync test -c fit.conf orders-out
fit sync dry-run -c fit.conf -limit 5 -from-start orders-out
fit sync checkpoint -c fit.conf orders-out
fit sync checkpoint -c fit.conf -reset -confirm orders-out
fit sync snapshot -c fit.conf -confirm orders-out
fit sync dead-letters -c fit.conf -replay -all orders-out
```

The workbench's **Sync** menu has the same: **Pipelines**, each with its state, throughput and lag as
sparklines, and **New pipeline**; a pipeline's **Overview** (pause, resume, new snapshot, test
connection, dry run), **Edit** (its form made from the schema, whose test and dry run check the draft
before it is saved), **Checkpoint** and **Dead letters**; and **Sync config** for the whole `sync`.
Each change that skips, replays or loses records asks first. The assistant reads pipelines, tests
them, dry-runs them and lists dead letters, and changes none: see
[the console's Sync](console.md#sync).

## Metrics and logs

The Admin API's `/metrics` carries each pipeline's as `fit_sync_*`, labelled `pipeline` (and `sink`):

| Metric | Type | |
| --- | --- | --- |
| `fit_sync_pipeline_state{state}` | gauge | 1 for the state it is in: `starting`, `running`, `paused`, `retrying`, `finished`, `stopped`, `scheduled`, `killed` |
| `fit_sync_records_read_total`, `fit_sync_read_bytes_total` | counter | records, and their size, the source read |
| `fit_sync_records_acknowledged_total`, `fit_sync_records_failed_total` | counter | records every sink has, and records failed |
| `fit_sync_errors_total` | counter | failed writes, transform failures, dead letters not written, source failures |
| `fit_sync_queue_records`, `fit_sync_queue_bytes`, `fit_sync_queue_max_records`, `fit_sync_queue_max_bytes` | gauge | the queue against its bounds |
| `fit_sync_lag_seconds` | gauge | from the last acknowledged record's commit time to its acknowledgement |
| `fit_sync_last_commit_timestamp_seconds` | gauge | the last acknowledged record's commit time |
| `fit_sync_source_lag` | gauge | behind the source's server, in its unit: a PostgreSQL slot's bytes of WAL, MySQL's binlog bytes, Kafka's offsets, the messages a RabbitMQ queue holds ready or a JetStream consumer has pending, SQL Server's CDC transactions or change tracking versions |
| `fit_sync_sink_records_total`, `fit_sync_sink_bytes_total`, `fit_sync_sink_batches_total`, `fit_sync_sink_errors_total`, `fit_sync_sink_retries_total`, `fit_sync_sink_failed_total` | counter | per sink: written, batches tried, failed, tried again, records failed |
| `fit_sync_dead_letters_total`, `fit_sync_dead_letters_replayed_total`, `fit_sync_dead_letters_dropped_total` | counter | dead letters written, replayed and dropped |
| `fit_sync_dead_letters_kept`, `fit_sync_dead_letters_kept_bytes` | gauge | dead letters kept |

Throughput is `rate()` of the counters. The `sync` logger says when a pipeline starts (from its
checkpoint or its source's start), waits for its schedule, is paused, resumed, killed, finishes or
stops, and its errors at most once each 10s, counting those held back.

## Dead letters

With `on_error dead_letter` and the `dead_letters` sink, each record that still fails is kept in
`<state_dir>/<pipeline>.dead_letters.jsonl` with its error, where it failed (the sink or the
transform) and its attempts, synced before it counts as written. The file is bounded: `max_records`
(10,000) and `max_bytes` (64 MiB); when full a letter is refused and the pipeline stops, nothing lost,
unless `when_full drop_oldest`.

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders {
		source generate { records 100 }
		sinks out { sink stdout }
		on_error dead_letter
		dead_letter { name errors; sink dead_letters { max_records 50000; when_full refuse } }
	}
}
```

Once the cause is fixed (a mapping, a column, a constraint), `fit sync dead-letters -replay -all
orders` (or the workbench's **Dead letters**) stops the pipeline, writes each letter to the sink it
failed at (through the transforms again, to every sink, when a transform failed it), drops those
written, keeps and reports those failing again, and starts the pipeline again. A replay writes a
record as it was, over any later change of its key the sink has. `-drop` drops letters.

## Operations

**New snapshots.** A database source reads its tables again with a new snapshot: `fit sync snapshot
-confirm`, or the workbench's **New snapshot**, removes the checkpoint and restarts the pipeline, so
its source starts as its params say (a snapshot, then the changes); a job starts anew. Needed when a
stream cannot resume: a binlog purged or an oplog rolled past the checkpoint, a PostgreSQL slot
dropped, a MongoDB stream invalidated. Idempotent sinks take the rows again over what they hold;
rows deleted at the source while the pipeline could not follow stay in the sink.

**Resets and positions.** `fit sync checkpoint -reset -confirm` removes the checkpoint (a scheduled
job then waits for its next time); `-set '{"lsn": "16/B374D848"}' -confirm` sets one, in the form `fit
sync checkpoint` shows. Both stop the pipeline, write, and restart it. A position set past records
skips them; one set before replays them.

**Moving a pipeline** to another fit: stop it there (remove it from the config, or stop fit), copy
`<state_dir>/<name>.json` (and its dead letters) to the new fit's `state_dir`, and add it there. A
PostgreSQL slot or a MySQL `server_id` is the pipeline's: never run one pipeline on two fits.

**Removing a pipeline** leaves its checkpoint file, which a pipeline of the same name added later
starts from; remove the file to start afresh. Drop a PostgreSQL slot it made, since a slot no one
reads keeps WAL without end.

**Failures.** A sink that is down holds its pipeline: the queue fills, the source waits, the lag grows
(`fit_sync_lag_seconds`, `fit_sync_source_lag`), and the pipeline goes on where it was once the sink
answers. A pipeline stopped by `on_error stop` waits for `fit sync resume` (after the fix), a config
change or a restart; the status and the log say which record and why. A source failing is restarted
from the checkpoint for as long as it fails, each failure counted and logged. A crash or `kill -9`
loses nothing; the records after the checkpoint are written again.

**Limits.** Everything a pipeline holds is bounded: the queue (records and bytes), each batch, a
line or event (`max_line_bytes`), an HTTP source's pending requests and bytes, a plugin's memory and
time, the dead letters kept. What a source's server keeps meanwhile is not fit's to bound: a
PostgreSQL slot keeps WAL and MySQL keeps binlogs while a pipeline is stopped, so watch
`fit_sync_source_lag` and the `lag_warning` log, and drop what you no longer read. Raise `queue` for
sinks with large batches (S3, ClickHouse), and `job.max_parallel` for jobs a server can take faster.

**Security.** Passwords and tokens come from environment variables, never the config or its history;
TLS is verified; an HTTP source is reached through a route, so put authentication and a rate limit
before `sync_ingest`. The users a pipeline logs in as need only the rights in the tables above.

## Interop

`test/sync/run.sh` starts the servers in Docker and runs the connectors' interop tests against them:
Elasticsearch 8 and OpenSearch 2, ClickHouse 25.8, MySQL 5.7, 8.0 and 8.4, MariaDB 10.11 and 11, TiDB
(standalone, and a cluster of PD, TiKV, TiDB and TiCDC), PostgreSQL 13 to 17, MongoDB 6, 7 and 8 (replica
sets), Kafka 3.9 and Redpanda, Redis 7 and Valkey 8, MinIO and SeaweedFS; the second round's RabbitMQ
3.13 and 4.1, NATS 2.10 and 2.11 (clusters of three), Pulsar 3 and 4, Mosquitto 2 and EMQX 5, SQL Server
2019 and 2022, Doris 2.1 and StarRocks 3.4, Loki 3.5, the OpenTelemetry Collector, InfluxDB 2.7 and 3
Core, TDengine 3.3, Qdrant 1.15 and Milvus 2.5. Each test kills its pipelines at
random points and checks the sink against the source at the end; `test/sync/exactly_once_test.go`
counts every row written, to check exactly once.

**The interoperability and recovery matrix** (`TestMatrix`, `test/sync/matrix_test.go`) runs each source
into one fixed set of sinks at once: PostgreSQL 17, MySQL 8.4, MongoDB 8, Elasticsearch, ClickHouse
(the sink's defaults: a `ReplacingMergeTree(_version, _deleted)` versioned by commit time, deletes
flagged) and Redis (a key per row). The sources are PostgreSQL, MySQL and MariaDB, MongoDB, TiDB through TiCDC, and Debezium's events
in Kafka; and the second round's: Debezium's events in RabbitMQ, NATS JetStream, Pulsar and MQTT (the
key built from the id, which their messages carry no key of), SQL Server through CDC, and Doris polled
by a cursor column (its deletes a deleted flag). Each starts with a table of rows (its snapshot), then a mixed load of inserts, updates,
deletes, key changes, NULLs and transactions of several changes is written while the pipeline is
killed at random crash points again and again, the first time before its first checkpoint, with rows
in the sinks; at the end every sink must hold the source's rows,
row for row, or the test names the rows missing, extra or different in each.

Servers come in families, each a CI job of its own, run in parallel (`SYNC_FAMILIES`):

| Job | Servers | Tests |
| --- | --- | --- |
| `search` | Elasticsearch, OpenSearch, ClickHouse, Redis, Valkey, MinIO, SeaweedFS | `TestElasticsearch*`, `TestClickHouse*`, `TestRedis*`, `TestS3*` |
| `mysql` | MySQL 5.7, 8.0, 8.4, MariaDB 10.11, 11, TiDB standalone | `TestMySQL*` |
| `postgres` | PostgreSQL 13 to 17 | `TestPostgres*` |
| `mongo` | MongoDB 6, 7 and 8 | `TestMongo*` |
| `kafka` | Kafka (three brokers), Redpanda | `TestKafka*` |
| `tidb` | PD, TiKV, TiDB, TiCDC, on Kafka; PostgreSQL 17 | `TestTiDB*` |
| `mq` | RabbitMQ 3.13 and 4.1, NATS 2.10 and 2.11, Pulsar 3 and 4, Mosquitto 2, EMQX 5 | `TestRabbitMQ*`, `TestNATS*`, `TestPulsar*`, `TestMQTT*` |
| `mssql` | SQL Server 2022 and 2019 (SQL Server Agent, CDC and change tracking on) | `TestMSSQL*` |
| `olap` | Doris 2.1 (a frontend and a backend), StarRocks 3.4 | `TestDoris*` |
| `obs` | Loki, the OpenTelemetry Collector, InfluxDB 2 and 3, TDengine, Qdrant, Milvus; PostgreSQL 17 and Mosquitto | `TestLoki*`, `TestOTLP*`, `TestInfluxDB*`, `TestTDengine*`, `TestQdrant*`, `TestMilvus*`, `TestEmbed*` |
| `matrix` | one of each: PostgreSQL 17, MySQL 8.4, MongoDB 8, Elasticsearch, ClickHouse, Redis, Kafka, TiDB with TiCDC | `TestMatrix`, every source once |
| `matrix-mq` | the matrix's sinks; RabbitMQ 4.1, NATS 2.11, Pulsar 4, Mosquitto | `TestMatrix`, the queues' sources |
| `matrix-db` | the matrix's sinks; SQL Server 2022, Doris | `TestMatrix`, SQL Server's and Doris's sources |

`matrix-mq` and `matrix-db` are `SYNC_FAMILIES=matrix` with `MATRIX_SERVERS` naming `mq`, or `mssql` and
`olap`: a second round's family named runs its sources alone. In `obs`, `TestEmbedPostgresAcrossKills`
runs the matrix's PostgreSQL source through `embed` into Qdrant and Milvus, and `TestInfluxDBFromMQTT`
is the IoT pipeline, MQTT to InfluxDB, both across kills.

The workflow `sync-matrix.yml` runs the matrix whole every night, a job per source on every server of
its kind (PostgreSQL 13 to 17, MySQL 5.7 to MariaDB 11, MongoDB 6 to 8, TiDB, Kafka and Redpanda,
RabbitMQ 3.13 and 4.1, NATS 2.10 and 2.11, Pulsar 3 and 4, Mosquitto and EMQX, SQL Server 2019 and 2022,
Doris),
with a larger load and more kills (`FIT_SYNC_MATRIX=full`). To run them locally:

```sh
test/sync/run.sh -v                          # every family, every test, the matrix too
SYNC_FAMILIES=postgres test/sync/run.sh -v   # one family
SYNC_FAMILIES=matrix test/sync/run.sh -v     # the matrix on one server of each kind
SYNC_FAMILIES=matrix MATRIX_SERVERS="search mysql postgres mongo" PG_VERSIONS="13 14 15 16 17" \
  FIT_SYNC_MATRIX=full FIT_SYNC_MATRIX_SOURCES=postgres test/sync/run.sh -v   # a nightly job
```

`SYNC_PREFIX` and the `*_PORT` variables the script lists run a second set beside the first; `KEEP=1`
keeps the containers for the next run.

Each family's servers have a script of their own, which `run.sh` calls and which runs alone as well:
`eval "$(test/sync/<script>.sh up)"` starts them and prints the variables the tests read, `down` removes
them. `mq` is `rabbitmq.sh`, `nats.sh`, `pulsar.sh` and `mqtt.sh` (`MQ_SYSTEMS` picks some), `mssql` is
`mssql.sh` (amd64 images, emulated on arm64), `olap` is `olap.sh` (Doris takes its ports as they are:
one set at a time), and `obs` is `obs.sh`, which also runs its tests alone:

```sh
SYNC_FAMILIES="mq obs" test/sync/run.sh -v
eval "$(test/sync/mssql.sh up)" && go test ./test/sync -run 'TestMSSQL' -v
test/sync/obs.sh -v
```
