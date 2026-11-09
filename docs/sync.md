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
[PostgreSQL to Kafka](https://github.com/lesismal/fit-examples/tree/main/sync-postgres-to-kafka) and
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
| `doris` | the same label for the same records: the server refuses the load as already loaded; rows of a unique or primary key table by key otherwise |
| `loki` | the same entry (stream, time, line) is kept once; lines without a time get the time of the write, and are stored again |
| `redis` | the same keys; stream entries with `entry_id key` are refused as already there |
| `s3` | the same batch makes the same object, written over itself; a replay cut into other batches makes other objects |
| `kafka`, `http`, `otlp_logs`, `file`, `stdout` | written again: Kafka's producer is not idempotent, nor is OTLP; `http` sends the same `Idempotency-Key` for the same records |

**Transactions stay whole.** A checkpoint never falls inside a source transaction; the records of one
carry its ID, and the `mysql` and `postgres` sinks write each source transaction as one of their own
(`transactions source`), so a reader of the sink never sees half of one.

**Order is kept per key**, from the source to each sink: a sink may write keys in parallel (a job's
writers), never one key out of order.

**Exactly once** into MySQL, MariaDB, TiDB and PostgreSQL: with `checkpoint sink` the position is also
written to a table of the sink's (`_fit_sync_checkpoints`), in the same transaction as the records it
covers, and read back when the pipeline starts in preference to the file. A crash between the sink's
commit and the file's next checkpoint then writes no record twice. It holds while every sink of the
pipeline keeps the checkpoint, at the same position: a pipeline with a sink of another kind beside
them falls back to the file, at least once. What can still be written twice: a source transaction
larger than the MySQL sink's `max_transaction_bytes` (committed in parts), and a job read in chunks,
which keeps to its file. A reset, a position set by hand and a new job start a new lineage, so an
older position in the sink does not undo them; deleting the pipeline's row and its file starts it
over.

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
| [Doris, StarRocks](#doris-and-starrocks) | `doris`: a table or query by key over MySQL's protocol, then a cursor column polled | `doris`: Stream Load, labelled by positions, delete signs |
| [Loki](#loki) | | `loki`: the push API, protobuf or JSON, streams from configured fields |
| [OpenTelemetry logs](#opentelemetry-logs) | | `otlp_logs`: OTLP/HTTP log records, protobuf or JSON |
| [HTTP](#http) | `http`: records posted to a `sync_ingest` route | `http`: batches posted as NDJSON or JSON |
| [MySQL, MariaDB, TiDB](#mysql-mariadb-and-tidb) | `mysql`: the binlog as a replica, a snapshot without locks | `mysql`: upserts and deletes, the source's transactions kept |
| [TiDB through TiCDC](#tidb-through-ticdc) | `tidb`: a snapshot at a timestamp, then a changefeed from Kafka | (the `mysql` sink) |
| [PostgreSQL](#postgresql) | `postgres`: logical replication with `pgoutput`, an exact snapshot | `postgres`: `ON CONFLICT` upserts, `COPY` |
| [MongoDB](#mongodb) | `mongo`: a snapshot by `_id`, then the change stream | `mongo`: bulk upserts and deletes by `_id` |
| [Kafka, Redpanda](#kafka-and-redpanda) | `kafka`: every partition from offsets in the checkpoint | `kafka`: keyed messages, batched, compressed |
| [Redis, Valkey](#redis-and-valkey) | `redis`: streams with `XREAD` | `redis`: `XADD`, or a key per record |
| [S3 and compatibles](#s3-and-compatibles) | `s3`: objects under a prefix, in key order, as lines | `s3`: objects of lines, rolled by size and time |
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
rows, and a `label` made from the run's first and last positions: a run loaded again after a crash
is refused as already loaded, so a batch is stored once even in a duplicate key table. Updates and
deletes need a table of the unique key model with merge on write (Doris) or the primary key model
(StarRocks): a delete is the row, or its key, with `__DORIS_DELETE_SIGN__` (or `__op`) set to 1.
`partial_columns` updates only the columns the records have. A frontend's redirect to a backend is
followed with the credentials, so the backend's address must be one fit reaches; `urls` may name the
backends' HTTP ports directly. A load refused for its rows is split until the record it cannot take
is found: that one goes to `on_error`, the records before it are loaded.

Labels outlive tables: Doris keeps them `label_keep_max_second` (three days by default), so after a
pipeline is reset or its tables are made afresh, change `label_prefix`, or the records loaded before
are refused as already loaded.

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
when its records have times.

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
when the same records are sent again; `2xx` acknowledges, `5xx`, `408`, `429` and timeouts are tried
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
| `kafka` | retention longer than a pipeline may stop | `Describe` and `Read` on the topics (no group rights: no group is joined) |
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

The sinks need write rights only: `INSERT`, `UPDATE` and `DELETE` on the tables (MySQL, PostgreSQL),
`CREATE` too for `checkpoint sink`'s table; `readWrite` on the database (MongoDB); `Write` and, with
`create_topics`, `Create` (Kafka); `LOAD` on the tables (Doris; `INSERT` on StarRocks). Tables and indexes in SQL and ClickHouse sinks are made by you: DDL
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
document of a change (flat with metadata, before and after, or Debezium's). A delete keeps its key
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
| `fit_sync_source_lag` | gauge | behind the source's server, in its unit: a PostgreSQL slot's bytes of WAL, MySQL's binlog bytes, Kafka's offsets |
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
sets), Kafka 3.9 and Redpanda, Redis 7 and Valkey 8, MinIO and SeaweedFS. Each test kills its pipelines at
random points and checks the sink against the source at the end; `test/sync/exactly_once_test.go`
counts every row written, to check exactly once.

**The interoperability and recovery matrix** (`TestMatrix`, `test/sync/matrix_test.go`) runs each source
into one fixed set of sinks at once: PostgreSQL 17, MySQL 8.4, MongoDB 8, Elasticsearch, ClickHouse
(the sink's defaults: a `ReplacingMergeTree(_version, _deleted)` versioned by commit time, deletes
flagged) and Redis (a key per row). The sources are PostgreSQL, MySQL and MariaDB, MongoDB, TiDB through TiCDC, and Debezium's events
in Kafka. Each starts with a table of rows (its snapshot), then a mixed load of inserts, updates,
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
| `matrix` | one of each: PostgreSQL 17, MySQL 8.4, MongoDB 8, Elasticsearch, ClickHouse, Redis, Kafka, TiDB with TiCDC | `TestMatrix`, every source once |

The workflow `sync-matrix.yml` runs the matrix whole every night, a job per source on every server of
its kind (PostgreSQL 13 to 17, MySQL 5.7 to MariaDB 11, MongoDB 6 to 8, TiDB, Kafka and Redpanda),
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

The second round's connectors have scripts of their own until CI's families take them (I222):
`test/sync/olap.sh` starts Doris 2.1 (a frontend and a backend) and StarRocks 3.4 for `TestDoris*`,
`test/sync/obs.sh` Loki 3.5 and the OpenTelemetry Collector for `TestLoki*` and `TestOTLP*`:

```sh
eval "$(test/sync/olap.sh up)"; eval "$(test/sync/obs.sh up)"
go test ./test/sync -run 'TestDoris|TestLoki|TestOTLP' -v
test/sync/olap.sh down; test/sync/obs.sh down
```
