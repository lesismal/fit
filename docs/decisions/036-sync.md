# 036: Sync: records, pipelines and checkpoints

[English](036-sync.md) | [简体中文](036-sync.zh-CN.md)

Status: accepted (2026-11-08), with the planning decisions below pending review. Planned in the
[dev-plan's sync track](../dev-plan.md#sync-moving-data-from-producers-to-consumers) (I163–I194);
I163 builds the core.

## Context

Users want fit to move data from producers to consumers, as Filebeat does for files and Canal,
Debezium or DataX do for databases: tail files, follow a MySQL binlog or a PostgreSQL slot, copy a
table once, and write what is read to Elasticsearch, ClickHouse, Kafka, another database or S3.
Every kind of infrastructure should be both a source and a sink, any pair joinable, and what the
built-in connectors do not cover added by WASM plugins. Such a tool is judged first on whether it
loses data, so the guarantees come before the connectors, and I163 builds them with test connectors
only: the record, the pipeline, the queue, acknowledgements and the checkpoint.

## Decisions

- **Principles, in the order they bind.** Nothing is lost, and the checkpoint proves it: a
  source's position is saved only after every sink of the pipeline has acknowledged every record up
  to it, and a restart, crash or `kill -9` resumes from the last saved position. Transactions stay
  whole: a checkpoint never falls inside a source transaction. Bounded everything (rule 11): the
  queue in records and bytes, a batch in records, bytes and time. Off fib's workers: a pipeline runs
  on goroutines of its own, a few per pipeline. Lean dependencies: wire protocols are written in
  fit. One record model. Credentials as everywhere in fit. Off by default: no `sync`, no connections
  out, no state on disk.
- **One record model.** Every source makes, and every sink takes, `fitsync.Record`: its source and
  namespace (`db.table`, an index, a collection, a file, a topic), an operation (`insert`, `update`,
  `delete`, `ddl`, `message`), the key, the row before and after, the commit time, the transaction
  (ID, first, last) and the position. Values are typed: null, bool, int, uint, float, decimal as
  text, string, bytes, time (with its offset), date, JSON, array and map, a map keeping its fields'
  order as a row keeps its columns'. No connector's types leak into another.
- **A canonical JSON form that round-trips every type.** null, booleans, strings and arrays are
  JSON's own; every other kind is an object of one member named after the kind: `{"int": -5}`,
  `{"uint": 5}`, `{"float": 1.5}` (`"NaN"`, `"+Inf"`, `"-Inf"` as strings), `{"decimal": "12.30"}`,
  `{"bytes": "<base64>"}`, `{"time": "<RFC 3339, nanoseconds, offset>"}`, `{"date": "2026-11-08"}`,
  `{"json": <the document, compacted>}`, `{"map": {...}}`. A bare number is refused, since its kind
  would be a guess. A record is an object of its parts, those empty left out. Transforms, WASM
  plugins (I165), the stdout sink and dead letters see this form.
- **At least once, with idempotent sinks.** Records after the last saved checkpoint are written
  again after a crash; sinks write by key (upserts, document IDs, deduplication tokens), so a replay
  converges. Exactly once only where the sink has transactions (I191).
- **The position is the source's, opaque and JSON.** A source sets `Position` on the records it can
  resume after; the runtime keeps the last one acknowledged that ends a transaction (a record with no
  transaction ID is one of its own) and hands it back as `Run.From`. It must be JSON so the
  checkpoint file stays readable and settable (I188).
- **Acknowledgements make a watermark of contiguous positions.** Each source record gets a sequence
  number at `Emit`; what transforms make of it inherits the number; it is done once every sink has
  acknowledged every record it became (a dropped record at once). The watermark moves over the
  contiguous run of done records from the head of the queue, to the last of them that ends a
  transaction; so the checkpoint is the minimum all sinks have acknowledged, never inside a
  transaction, never past a record still owed.
- **One bound for the queue: records read and not yet acknowledged.** `queue.max_records` and
  `queue.max_bytes` count a record from `Emit` until its turn at the head of the watermark; `Emit`
  waits while they are full. A stalled sink therefore stops the source, and memory stays within the
  bounds, whatever the sinks' batches hold. A record larger than `max_bytes` waits for an empty queue
  and goes through alone, so the bound is `max_bytes` or one record, whichever is larger.
- **The checkpoint file.** `<state_dir>/<pipeline>.json`: `{"version": 1, "pipeline", "position",
  "saved"}`, written to a temporary file in the same directory, `fsync`ed, renamed over the last, and
  the directory synced where the system can. It is written at most every `checkpoint_interval` (5s)
  when it moved, and when the pipeline stops; temporary files a crash left are removed at start. A
  file of a later version, or one that does not parse, keeps the pipeline from starting (it retries
  and reports why), rather than starting it from scratch.
- **Sources are told the checkpoint once it is saved.** A source implementing `Confirmer` gets each
  checkpoint after its file is written, not when the watermark moves: a PostgreSQL slot told an LSN
  releases the WAL before it, so confirming a position the file does not hold yet would lose data
  across a crash.
- **Failures.** A sink's error is tried again with a doubling backoff (`retry`: 5 attempts, 200ms to
  30s), from the record a `BatchError` names or from the batch's start; a record still failing, or
  one a transform fails on, goes as `on_error` says: `stop` the pipeline (its checkpoint before the
  record), `skip` it with a log line, or write it to the `dead_letter` sink before going on. A source
  that fails restarts the whole pipeline from its checkpoint after the same backoff, for as long as
  it fails; a stopped pipeline waits for the config to change or fit to restart.
- **Connectors and transforms are modules.** Namespaces `sync.sources`, `sync.sinks` and
  `sync.transforms`; a config names one with `module` and `params`, decoded as strictly as a
  handler's, checked by `fit validate` (the role's check loads every module), and described in the
  schema and the reference. I163 ships the test connectors `generate`, `memory` and `stdout`.
- **Transforms copy a record once, and deletes keep their keys** (I164, pending review with its
  plan section). A transform changes a record only through `Record.Own`, which returns the record
  when a transform before it made it with `Clone`, and a clone otherwise, so the source's record
  stays as read and a chain copies it once. The built-in `filter`, `route`, `fields`, `key`, `cast`,
  `mask` and `envelope` never drop a record's key; a `key` transform built from a field an earlier
  transform removes is refused when the config is checked, and one whose delete lacks the fields
  keeps the key the delete has.
- **The sync role.** `sync` is an app role (decision 028's registry): the manager starts it with a
  config that sets `sync`, applies each config to it, and stops it with one that leaves it out. A
  reload starts the pipelines added, stops those removed and restarts only those whose settings
  changed, comparing each pipeline's settings and `state_dir`; a stopped pipeline writes what it
  holds and its checkpoint first, within the drain timeout. A config whose modules do not load
  changes nothing. Its log is a logger of its own, `sync`.
- **Protocols written in fit, no drivers** (planning decision 1 below): connectors to MySQL,
  PostgreSQL, MongoDB, Kafka, Redis and S3 implement their protocols in `sync/*/wire`, each checked
  against the real server in Docker. No dependency is added by I163.

## Decisions made while building I163 (pending review)

1. **The package's directory is `sync`, its name `fitsync`.** The plan's paths (`sync/file`,
   `sync/mysql/wire`) and the config's `sync` stay as they are, and the name does not shadow the
   standard library's `sync`, which the runtime itself imports. Alternative: a directory `datasync`.
2. **A sink is `{"name", "sink": {"module", "params"}, "batch"}`**, the module in a reference of its
   own rather than `type` beside the sink's settings as the plan's sketch had, so that the schema,
   the decoder and `module.Load` treat it as every other module reference. Alternative: flat
   `{"name", "type", ...params, "batch"}`, which needs a decoder and schema of its own.
3. **`dead_letter` is a sink of its own**, not one of `sinks`, which would then take every record as
   well as the dead letters. A dead letter is a `message` record whose `after` holds `error`, `at`
   (the sink or transform), `attempts` and `record` (the record's canonical JSON, as a JSON value),
   with the record's namespace, key and position.
4. **`state_dir` is required** when `sync` is set, rather than defaulting to a directory under
   `$XDG_DATA_HOME`: where checkpoints live is worth saying, and nothing is written without it.
5. **Retry settings are the pipeline's**, shared by its sinks, its dead letter sink and its source's
   restarts, rather than each sink's.
6. **A batch failing with an error that names no record** is tried again whole; once its attempts
   are spent, its first record is the one that failed, `on_error` takes it, and the rest is tried
   again with fresh attempts. A sink that is down therefore costs each skipped record its attempts,
   rather than skipping a whole batch at once.
7. **The transform interface is per record** (`Apply(r, emit)`), on the dispatcher goroutine; a
   batch interface for WASM is I165's to add if its benchmark asks.
8. **A source's error restarts the pipeline for as long as it fails**, after the backoff, while a
   sink's error past its attempts follows `on_error`: a source that loses its connection is expected
   to come back, and stopping a stream for it would need a person.
9. **Byte sizes are plain integers** in the config (`max_bytes: 4194304`), as `rotate` and the
   media library's are, not strings such as `"4MiB"` as the plan's sketch had.
10. **Crash tests use a hook, not a subprocess.** `Options.Crash` is asked at named points
    (emitted, dispatched, written, acknowledged, checkpoint written, checkpoint saved) and kills the
    pipeline there as `kill -9` would: nothing more is saved, the checkpoint's temporary file left
    behind. Later connectors' tests use the same hook.

## Decisions made while building I165 (pending review)

The plan's I165 section lists them all; these change the record above.

1. **Transforms may take a batch.** `BatchTransform` adds `ApplyBatch(in, emit(i, o), fail(i, err))`
   beside `Apply`; the dispatcher runs the transforms one at a time over the records it holds, and
   takes failures in the source's order, as before.
2. **The WASM ABI for sync** is the host module `fit_sync` beside fit's: exports
   `fit_sync_transform`, `fit_sync_decode`, `fit_sync_encode`, `fit_sync_poll` and
   `fit_sync_write`, each given its input's length, which the plugin reads with `input`; results
   through `record_emit`, `fail`, `output` and `checkpoint`; records as their canonical JSON; config
   and log as for request plugins. A call that fails, traps or runs past its time or memory fails
   its records, which `on_error` takes; the checkpoint does not pass them.
3. **Connector plugins reach the outside only through `http_request`**, on fib's asynchronous
   client, to the hosts their `allow_hosts` names, and `sleep`; transforms and codecs get neither.
   A poll's records are a unit: the checkpoint the poll returns goes on its last record.
4. **Codecs** are modules of `sync.codecs`, implementing `Codec`; the sources and sinks that read
   and write bytes take one, from I166.
5. **JSON is slow in a Go plugin**: locally, 46.5 µs a record against the built-in's 81 ns, so the
   binary form of planning decision 8 is asked for, with the benchmark host's run (I165b).

## Decisions made while building I165b (pending review)

The plan's I165b section lists them all; these change the record above.

1. **Records cross in a binary form when a plugin takes it** (planning decision 8): a plugin that
   exports `fit_sync_form(offered)` is offered form 1 and may take it; it then reads its batches
   in `fitsync`'s binary form (lengths and tags; a value's first byte tells its form) and hands
   records back through `records_emit`, many at once. One that exports none, every plugin built
   before, keeps the canonical JSON and `record_emit`. The host reads what comes back out of one
   copy, and takes a value a plugin handed back unchanged from the batch's record.
2. **A Go plugin's batch is its call's**: the guest SDK reuses a call's input and records in the
   next call, and TinyGo plugins are built with `-scheduler=none`.

## Decisions made while building I166 (pending review)

The plan's I166 section lists them all; these change the record above.

1. **Built-in codecs.** `line`, `json` (a row as plain JSON, or a whole record in its canonical
   form) and `csv` are modules of `sync.codecs` in `fitsync`; a connector names one in its params
   as a module reference, `line` when it names none. A line a codec refuses becomes a message
   record holding the line and the error, not a lost line nor a source error.
2. **A file source's position** is every file's device and inode (on Windows the volume's serial
   number and file index), a fingerprint of its head, its offset and its records' key, and the heads
   of files truncated lately, so that a copy taken before a truncation is read on from where its
   original was. Files closed and missed by scans for `close_after` are forgotten, so it stays bounded. The source sets a
   position only between lines, every pass or 1,000 records, rather than on every record.
3. **Records read from files have keys**: the file's device, inode and a hash of its first line,
   and the offset, so that a line read again after a crash has the key it had and an idempotent
   sink folds the replay.
4. **The file sink writes through the logs' file sink** (decision 034), shared with the process's
   log files: one sink per path, rotation, retention and compression as a log's; a batch is
   acknowledged once written and synced.

## Decisions made while building I167 (pending review)

The plan's I167 section lists them all; these change the record above.

1. **Jobs and chunked snapshots.** A pipeline of `mode: "once"` is a job. A source that can split
   its snapshot implements `Chunker`: `Chunks` plans the chunks when a job starts (a namespace,
   `lo` and `hi` key bounds in the source's own JSON, the rows reckoned), the plan kept in the
   checkpoint; `ReadChunk` reads one, `job.max_parallel` at a time. The job's position holds the
   chunks done and where each under way is; a chunk's last record carries the position marking it
   done, so through the watermark a chunk is done only once every sink has acknowledged all its
   records, and a job interrupted reads no finished chunk again. Snapshot records carry no
   transactions.
2. **Parallel writers** only for a chunked snapshot, and only for sinks implementing
   `ParallelSink`: each gets `job.max_parallel` writers, a chunk's records sent to the writer of
   the reader that read them (since I193's S5, 2026-11-09: by their key's hash before, which
   gave every writer's batch keys from across the table, contending for the same B-tree pages in
   a database, a copy into MySQL 25% slower), so that a key, read once, is never written out of
   order; otherwise one writer per sink, as before. While the source waits for room in the queue,
   the writer holding the oldest record writes its batch unfilled, since only that frees room.
3. **The progress is kept in the checkpoint** (`job`: when it started and finished, records read,
   written and failed, the rows reckoned, an ETA, the chunks done and the plan), whose version
   stays 1. A finished job is not run again when fit restarts or reloads; a schedule's next time or
   `fit sync run` starts a new job, a chunked source from its start and any other from where the
   last ended.
4. **Schedules**: `job.schedule` in cron's five fields or a descriptor, no seconds, in
   `job.time_zone` (`UTC` by default), parsed by fit's own parser (`internal/cron`), no dependency;
   runs never overlap, times missed are not made up, and a job interrupted goes on at once when fit
   starts.
5. **`fit sync run <pipeline> [--once]`** runs one pipeline on its own, without a server, and exits
   with its result: 0 all written, 3 records failed, 1 stopped or interrupted, 2 used wrongly; a
   source failing past `retry.max_attempts` in a row stops it rather than restarting it for ever.

## Decisions made while building I168 (pending review)

The plan's I168 section lists them all; these change the record above.

1. **Permanent errors.** `fitsync.Permanent(err)` marks an error that trying again cannot cure (a
   document a mapping refuses); a sink's write failing with one, alone or as a `BatchError`'s, goes
   to `on_error` at once, without the pipeline's retries.
2. **A sink may retry within a write.** The `elasticsearch` sink sends items the cluster answered
   429 or 5xx again itself, with the later writes of the same documents, so a document's last change
   stays last; its `BatchError` names the first record not written, and the runtime writes the batch
   again from there, records the cluster had taken after it included, in order, acknowledging each
   once.
3. **A 429 is backpressure, not a failure.** It is waited out for as long as it lasts, holding the
   sink and through the queue the source, and never counts against `retry.max_attempts`, so no
   record is skipped or dead-lettered because a cluster is busy.
4. **A sink without the role's engine runs one of its own** for fib's HTTP client, so `fit sync run`
   reaches HTTP-based sinks.

## Decisions made while building I169 (pending review)

The plan's I169 section lists them all; these change the record above.

1. **Elasticsearch as a source** (planning decision 6): a snapshot through a point in time, then
   polls by a cursor field, sorted by the cursor alone (Elasticsearch 8 does not sort on `_id`), the
   `_id`s already read at the last value excluded; the position is that value and those `_id`s. A
   stream's snapshot interrupted starts over; documents written below the checkpoint after it passed
   are missed, and date cursors stay `delay` behind the cluster's clock. Deletes are not seen, but a
   soft-delete field (`deleted_field`) turns a document into a delete.
2. **A job's chunks are slices of the `_id` read through scrolls**, not points in time, whose slices
   are not the same in another point in time; a chunk interrupted is read again whole. A job with a
   cursor is not split, so its next run reads only what changed.
3. **`fitsync.ParsePlain`** reads a plain JSON document by the `json` codec's rules, for sources
   whose servers hold JSON.
## Decisions made while building I187 (pending review)

The plan's I187 section lists them all; these change the record above.

1. **SigV4 is fit's own** (`syncs3.Signer`), as the principles have it: nothing in fit or fib spoke
   S3 to reuse. Credentials are an access key and a secret named by an environment variable, read
   once; AWS's default credential chain is not read.
2. **A sink that writes files of batches writes one per batch.** The `s3` sink puts each batch as an
   object (several past `max_object_bytes`) and keeps nothing across batches, so objects roll by
   the sink's `batch` limits; holding lines across batches would acknowledge records not yet
   stored.
3. **An object is named by its first record** (its commit time and a hash of its position), so a
   batch written again is the same object with the same bytes; a replay cut into other batches
   writes other objects, at least once as every sink.
4. **A source of objects reads keys in order and resumes inside one**: its position is the key and
   the offset past the line; keys must grow, as the sink's do.
## Decisions made while building I171 (pending review)

The plan's I171 section lists them all; these change the record above.

1. **Sources may hear of acknowledgements before the checkpoint.** `fitsync.Acknowledger` is told
   each position the watermark moves to, as it moves, before the checkpoint is saved; the `http`
   source answers its clients then, since what every sink has is safe, and `Confirmer` stays the
   hook for sources whose server must not release data before the checkpoint holds it.
2. **A source that takes records in is found by its pipeline's name.** `fitsync.Run` carries the
   pipeline's name; the `http` source registers under it while it runs, and the `sync_ingest`
   handler looks it up, answering 503 while there is none.
3. **What `on_error` took counts as acknowledged** for a source answering its clients: an HTTP
   request whose record was skipped or dead-lettered is answered 200.
4. **An HTTP sink's idempotency key** covers the request's records' positions and its body, since
   most records carry no position; a resend carries the same key, a replay batched otherwise
   another.
## Decisions made while building I170 (pending review)

The plan's I170 section lists them all; these change the record above.

1. **Versions come from the record, not the clock.** A sink that keeps a key's last state by a
   version (ClickHouse's `ReplacingMergeTree`) takes it from the commit time or a field of the row,
   since with deduplication a replay may write an older change again under a new token while a
   later batch is dropped, and a clock version would make the older change win.
2. **Deduplication and replayed deletes do not mix.** Where a sink writes deletes as statements
   (ClickHouse's lightweight `DELETE`), its inserts are not deduplicated: a delete replayed would
   remove a row that a deduplicated insert would not bring back.
3. **A source polled by a cursor reads by keyset**, `(cursor, key) > (last)`, with the last row's
   values as the position, so rows that share a cursor value are read once each, and in a job its
   snapshot splits by ranges of the key (`Chunker`).

## Decisions made while building I172 (pending review)

The plan's I172 section lists them all; these change the record above.

1. **A database's connection is a blocking `net.Conn` on its goroutine**, not one of fib's: MySQL's
   client (`sync/mysql/wire`) reads its answers in order on the goroutine that asked, with
   `crypto/tls`, every wait bounded by a timeout and every call by its context; HTTP-based systems stay
   on fib's asynchronous client.
2. **TLS to a database is required when configured, never preferred**, and verified; a server that
   does not offer it is refused rather than spoken to in plain text.
## Decisions made while building I176 (pending review)

The plan's I176 section lists them all; these change the record above.

1. **A sink may hold a source transaction open across batches**, acknowledging its records while
   the server's transaction is open: the checkpoint never passes a transaction's end before every
   sink has its last record, so a crash replays it whole. The sink keeps what the open transaction
   holds and writes it again on a new connection; one that groups several source transactions
   commits at a batch's last record that ends one, so that what it holds open is never a whole
   source transaction the watermark could pass.
2. **A record refused for good inside a source transaction** is failed alone, what came before it
   committed: the transaction is then in the sink in part, until `on_error: "stop"` replays it, or
   for good with `skip` and `dead_letter`.

## Decisions made while building I177 (pending review)

The plan's I177 section lists them all; these change the record above.

1. **Database wire clients are blocking connections** on `net.Conn` and `crypto/tls`, each on a
   goroutine of its own, not fib's asynchronous connections: request and answer, COPY and the
   replication stream want a blocking write's backpressure, and channel binding needs
   `crypto/tls`'s view of the server's certificate. fib stays the way out for HTTP-based systems.
2. **No `sslmode` that falls back silently**: `disable`, `require`, `verify-ca`, `verify-full` (the
   default); channel binding `prefer` by default.
## Decisions made while building I178 (pending review)

The plan's I178 section lists them all; this changes the record above.

1. **A slot may be told more than the checkpoint, where nothing is owed.** "A source whose server
   keeps data for its readers is told the position only once every sink has it" stands; besides
   it, while every transaction the PostgreSQL source handed on is in the saved checkpoint, the slot
   is told the latest position before which the server sent nothing for the pipeline (the end of a
   transaction with no published change, a keepalive's position outside a transaction), so that
   writes to tables not published do not make the server keep WAL until the next published change.
   The checkpoint file keeps the last position with records; resuming from it is the same, since
   nothing lies between.
## Decisions made while building I181 (pending review)

The plan's I181 section lists them all; these change the record above.

1. **Database clients block, off fib's workers.** A wire client for a database (MongoDB's first) is
   a `net.Conn` read and written in lockstep by the connector's own goroutine, which "off fib's
   workers" above allows; fib's asynchronous clients stay the way out for HTTP-based systems.
2. **Types a record has no kind for travel as canonical extended JSON**: a one-field map named as
   MongoDB names the type (`{"$oid": ...}`, `{"$timestamp": ...}`), written back as that type, so
   MongoDB to MongoDB loses nothing but an int32's width (ints are written as int64).
3. **A command may be sent twice.** The client sends a command once more to a new primary after a
   connection's error or a not-primary answer, without sessions; sync's idempotent writes are what
   make that safe.
## Decisions made while building I186 (pending review)

The plan's I186 section lists them all; these change the record above.

1. **Sync has a RESP client of its own**, `sync/redis/wire` (RESP2 and RESP3, TLS, ACL users,
   pipelining, bounded replies), rather than `tlsmgr/sharedstore`'s unexported RESP2 client; moving
   `sharedstore` onto it is left for later.
2. **A stream's checkpoint is the only cursor**: the `redis` source reads with `XREAD` from the last
   entry IDs, makes no consumer group and acknowledges nothing on the server.
3. **A sink may count a server's refusal of a duplicate as written**: with entry IDs from the keys,
   the `redis` sink treats `XADD`'s refusal of an ID at or below the stream's last as an entry the
   stream has, which makes a stream copy exactly once.
## Decisions made while building I184 (pending review)

The plan's I184 section lists them all; these change the record above.

1. **Kafka's connections are blocking `net.Conn`s**, one goroutine reading each, fetches on
   connections of their own: "few and long, each on a goroutine" applies to Kafka as to the
   databases, and no fib worker is held by a long-poll.
2. **A dependency fit already has may serve a protocol**: snappy and zstd come from
   `klauspost/compress` (fit's HTTP compression); LZ4, which no dependency has, is written in fit.
3. **The Kafka client is at least once**: not idempotent, so a produce retried after a lost answer
   may write its records twice, which sinks downstream of a Kafka source already tolerate.

## Decisions made while building I185 (pending review)

The plan's I185 section lists them all; these change the record above.

1. **A source whose server could keep its readers' positions keeps them in the checkpoint
   alone.** The kafka source commits no offsets to a consumer group, for visibility or otherwise;
   it reports its lag in offsets instead (I189's `ReportLag`).
2. **A codec may decode keys** (`synckafka.KeyDecoder`), as Debezium's key events hold the row's
   key; a message's several records are one transaction, so the checkpoint never falls inside a
   message.
3. **A source that reads TiDB's changes from Kafka takes TiDB's snapshot at a TSO first**, and
   drops the changes committed at or before it; the changefeed must predate the snapshot.

## Decisions made fixing what I192's matrix found (pending review)

The plan's I192 section lists them (decisions 5 and 6); these change the record above.

1. **A source's start is the checkpoint before its first record.** `Run.SaveStart(p)` (the runtime
   fills `Run.Start` for pipelines that are not jobs) saves, at once and before any record, the
   position a run started again resumes from, when the pipeline has no checkpoint. A source of a
   snapshot then a stream gives its stream's start with the snapshot not begun (PostgreSQL the
   exported point with no chunk done, MySQL the binlog position, MongoDB the start token, TiDB the
   snapshot's TSO), so a run killed before its first checkpoint reads the snapshot again and replays
   the stream from that start over it, rather than streaming from a later start and losing the
   changes between. It goes through the crash points `checkpoint_written` and `checkpoint_saved`;
   `Confirmer` sources are not told it.
2. **A sink versioning rows by commit time gives a record without one the lowest version** (the
   ClickHouse sink: 0), so that a snapshot's row read again never outranks a change.

## Planning decisions (2026-11-08, pending review)

1. **Protocols written in fit, no drivers.** Alternative: go-mysql, pgx, the MongoDB driver and
   franz-go behind fit's interfaces: far less work now, against design principle 6.
2. **At least once, idempotent sinks; exactly once only where the sink has transactions (I191).**
3. **The checkpoint in a local file per pipeline**, not in `admin.store` or a sink, by default; a
   pipeline runs on one fit.
4. **One checkpoint per pipeline, at the slowest sink.** Alternative: one pipeline per sink.
5. **MySQL snapshots without locks, converging through replays** (I175), rather than DBLog's
   watermarks.
6. **ClickHouse and Elasticsearch as sources by cursor polling**, deletes not seen.
7. **TiDB's changes through TiCDC via Kafka**, not TiDB's deprecated binlog.
8. **The WASM ABI passes records as JSON first**, binary only if I165's benchmark asks; connector
   plugins get HTTP and timers, not raw sockets.
9. **DDL is reported, not replayed into SQL sinks.**
10. **Order:** core, transforms, WASM and files first, then the HTTP-based systems, then the
    databases, then Kafka, Redis and S3, then operations.

## Consequences

- Every later connector is a module implementing `Source`, `Sink` or `Transform`, and gets
  checkpoints, batches, retries, `on_error`, reloads and the crash tests from the runtime.
- Sinks must be idempotent; the docs of each say how it is.
- The runtime's goroutines per pipeline are bounded: a supervisor, the source, a dispatcher, a
  checkpoint ticker, one per sink and one waiting for the sinks; a job reading a chunked snapshot
  adds `job.max_parallel` chunk readers, and as many writers for each `ParallelSink` (I167); all
  stop on `Close`.
