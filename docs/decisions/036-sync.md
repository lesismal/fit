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
  checkpoint ticker, one per sink and one waiting for the sinks; all stop on `Close`.
