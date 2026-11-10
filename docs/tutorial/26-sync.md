# Sync

[English](26-sync.md) | [简体中文](26-sync.zh-CN.md)

## What it does

Sync is a role fit runs beside the gateway, on the same engine but off the workers that serve
requests: a **pipeline** reads **records** from a **source**, passes them through **transforms**, and
writes every one to one or more **sinks**, each in batches of its own. It is fit's answer to Filebeat
for files and to Canal, Debezium or DataX for databases ([decision 036](../decisions/036-sync.md)):
the wire protocols are written in fit, so no driver is installed. Off by default: without the `sync`
member of the config, no connection is made and no state is written to disk.

A record is the same whatever made it: the connector, its **namespace** (`db.table`, an index, a
collection, a file's path, a topic), an operation (`insert`, `update`, `delete`, `ddl` or `message`),
the **key**, the row before and after as typed values (a decimal as text, never rounded), the commit
time, its transaction and the source's **position**. Transforms may change a record, drop it or split
it; codecs turn the bytes a source reads or a sink writes into records and back (`line`, `json`,
`csv`, `raw`, `canal_json`, `debezium`, `ticdc_simple`, `wasm`).

A pipeline keeps one **checkpoint**, `<state_dir>/<name>.json`: the position of the last record **every
sink has acknowledged** that ends a source transaction, never one in the middle of one. It is written
at most every `checkpoint_interval` (5s) and when the pipeline stops — to a temporary file, synced and
renamed into place — and the pipeline starts from it. Delivery is therefore **at least once**: after a
crash or `kill -9` the records after the last checkpoint are read and written again, never lost, and
sinks write idempotently (upserts and deletes by key, documents by `_id`, deduplication tokens, object
names made from the first record of a batch), so a replay converges on the same state. The `mysql`,
`postgres` and `mssql` sinks add **exactly once** with `checkpoint sink`, which writes the position to
a table of theirs in the same transaction as the records it covers
([delivery guarantees](../sync.md#delivery-guarantees)). The checkpoint also holds a **lineage**, a
random ID for a line of positions that a reset, a position set by hand or a new job starts another of;
sinks that name what they write by a position (`doris`'s labels, `http`'s `Idempotency-Key`) put it in
the name, so the records a reset reads again are written again rather than dropped as repeats.

Everything is bounded: `queue` bounds the records read and not yet acknowledged by every sink (10,000
records and 64 MiB), so a slow sink slows its source down instead of growing memory, and each sink's
`batch` closes at `max_records` (500), `max_bytes` (4 MiB) or `max_wait` (1s) after its first record.
A write that fails is tried `retry.max_attempts` (5) times with a doubling backoff; a record still
failing, or one a transform fails on, goes as `on_error` says: `stop` the pipeline, `skip` the record,
or `dead_letter` it to the pipeline's `dead_letter` sink. With the `dead_letters` sink those records
are kept in a file that the **Dead letters** page lists and replays. A source that fails restarts the
pipeline from its checkpoint for as long as it fails.

A pipeline of `mode once` is a **batch job**: it reads its source to the end and stops with its
checkpoint written. A source that can split its snapshot is read in chunks, `job.max_parallel` (4) at
a time, a key always going to the same writer; the checkpoint keeps the chunks done, so an
interrupted job goes on from them without reading them again. A finished job does not run again when
fit restarts; `job.schedule` (cron's five fields, in `job.time_zone`) starts it again, a new job each
time, never over a run still going.

A pipeline runs on one fit: its checkpoint is a local file, so moving a pipeline to another fit means
stopping it, copying `<state_dir>/<name>.json` and its dead letters and adding it there — never running
one pipeline on two fits, since a PostgreSQL slot or a MySQL `server_id` belongs to the pipeline. A
password or a token is never in the config: a connector names the environment variable that holds it
(`password_env`, `secret_access_key_env`, `bearer_token_env`, `headers_file`), read when the pipeline
loads, and TLS is verified, never silently skipped.

The config member is `sync`: `state_dir` and `pipelines`, nothing else — the bounds are each
pipeline's `queue` and `batch`, and what the pipelines report is fit's metrics and logs. Every page of
this module edits the member in the console's **shared draft** — the same draft every config page
edits — and nothing is applied until **Save and apply** in the top bar applies the whole draft as a new
config version (**Check** takes the changes through the whole apply path without applying them,
**Discard** drops them). Applying a config restarts only the pipelines whose settings changed: it
starts those added, stops those removed (each writing what it holds and its checkpoint first) and
leaves the rest running untouched. A pipeline paused by hand stays paused across a reload that keeps it
and runs again when fit restarts. A save's problems link to the pipeline's **Edit** page, or to **Sync
config** for the member itself.

The connectors cover every kind of system fit speaks, so any pair can be joined. Files (`file`); the
databases (`mysql` for MySQL, MariaDB and TiDB, `tidb` through TiCDC, `postgres`, `mssql` through CDC
or change tracking, `mongo`); the search and analytics stores (`elasticsearch` for Elasticsearch and
OpenSearch, `clickhouse`, `doris` for Doris and StarRocks); the queues and logs (`kafka` for Kafka and
Redpanda, `mqtt`, `rabbitmq`, `nats` JetStream, `pulsar`, `redis` for Redis and Valkey, `s3` for S3 and
compatibles); the observability stores (`loki`, `otlp_logs`, `influxdb`, `tdengine`); the vector
databases (`qdrant`, `milvus`, fed by the `embed` transform); `http`, as a source behind a route's
`sync_ingest` handler; and the trials `generate`, `memory` and `stdout`. Sources and sinks are modules
of the namespaces `sync.sources` and `sync.sinks`, transforms of `sync.transforms` (`filter`, `route`,
`fields`, `key`, `cast`, `mask`, `envelope`, `embed`, `wasm`) and codecs of `sync.codecs`; what the
built-ins do not cover is a WebAssembly plugin in any of those four places.

`fit validate` checks every pipeline as it checks the rest of the config: the modules named exist in
their namespaces, `state_dir` is set, each pipeline and sink has a valid and unique name, at least one
sink is there, `on_error dead_letter` has its `dead_letter` sink and no other policy has one, and a
`job` appears only with `mode once`.

The same pipelines can be watched and steered from outside the pages: the Admin API's `/sync/…`
endpoints (state, pause and resume, snapshot, checkpoint, test, dry run, dead letters), `fit sync` on
top of them (`status`, `pause`, `resume`, `snapshot`, `checkpoint`, `test`, `dry-run`,
`dead-letters`, and `run` to run one pipeline alone), the `fit_sync_*` metrics and the `sync` logger,
and the assistant's four read-only tools `sync_pipelines`, `sync_test`, `sync_dry_run` and
`sync_dead_letters`, with the skill `sync-pipeline` that the Ask AI buttons run
([the console's Sync](../console.md#sync), [the guide](../sync.md#the-admin-api-fit-sync-and-the-workbench)).

What a pipeline does is reported where fit's other roles are reported. The `sync` logger says when a pipeline
starts (from its checkpoint or its source's start), waits for its schedule, is paused, resumed, killed,
finishes or stops, and its errors at most once each 10s, counting those held back. The `fit_sync_*`
metrics carry what the pages show — records and bytes read, acknowledged and failed, errors, the queue,
the lag and behind the source — labelled by `pipeline`, and by `sink` for each sink's own.

## Pages

Sync is in the menu after LLM roles and before BitTorrent, tagged *pipelines*, with two pages:
**Pipelines** and **Sync config**. A pipeline has four tabs of its own: **Overview**, **Edit**,
**Checkpoint** and **Dead letters**. Each page has its own address — `#/sync/pipelines`,
`#/sync/config`, `#/sync/pipeline/<name>`, `#/sync/edit/<name>` and the tab's suffix — so a pipeline
can be linked to directly. The module belongs to the console, so it is listed as soon as the console
runs, before its role is configured; the pages then say what to set and start the pipelines when the
config is applied.

### Pipelines

The list of every pipeline, read from `GET /sync/pipelines` and refreshed every two seconds while the
page is open, with the last 60 polls kept for the sparklines. A row per pipeline:

- **Pipeline**, the name, linking to its **Overview**;
- **State**: `starting`, `running`, `paused`, `retrying`, `finished`, `stopped`, `scheduled` or
  `killed`, with `paused` shown whenever Pause holds the source, whatever the state;
- **Throughput**, a sparkline and the records acknowledged a second over the last ten seconds;
- **Lag**, a sparkline and the time from a record's commit time to its acknowledgement, then how far
  behind its source's server the source said it is, where the source tells it;
- **Read / acknowledged**, the records the source read and those every sink has;
- **Errors**, the count since the pipeline started and its last error;
- **Queue**, a meter of the records read and not yet acknowledged against `queue.max_records`;
- **Dead letters**, how many a `dead_letters` sink keeps, linking to that tab.

Pipelines that exist in the draft but do not run — not saved yet, or the role off — are listed too,
with a **not applied** badge; when the role does not run at all, a note above the table says so and
that **Save and apply** starts them. With nothing in the draft either, the page says to make one with
**New pipeline** and then save and apply.

**New pipeline** in the top bar asks for a name (letters, digits, `_`, `.` and `-`, unique; suggested
`pipeline-1`), a **source** (every `sync.sources` module, `generate` preselected), a **sink** (every
`sync.sinks` module except `dead_letters`, `memory` preselected) and, when the draft has no `sync`
member yet, the **state directory** (`sync.state_dir`, `/var/lib/fit/sync` suggested). **Make it** adds
the pipeline to the draft and opens its **Edit** page: it is a real pipeline only after **Save and
apply** starts it.

### Sync config

The whole `sync` member in one form made from the schema — `state_dir` and every pipeline with its
source, transforms, sinks, batches, retry, job and dead letters — with the **UI**, **.conf**, **.yaml**
and **.json** tabs of every config page. All four edit the same shared draft: the raw tabs' text is
parsed into it, and **Save and apply** checks and applies it as one. This page is the one to use to set
`state_dir` before any pipeline, or to change several pipelines at once; a save's problem in a pipeline
links to that pipeline's **Edit** page, a problem of the member itself to this page.

### Overview

A pipeline's landing page, read from `GET /sync/pipelines/{name}` every two seconds, with its four
tabs. It shows the state badge, when a scheduled job starts next, and the last error with its time,
with an **Ask AI** button that runs the `sync-pipeline` skill on it. The counters: **Throughput**,
**Lag**, **Read** with the bytes read, **Acknowledged** with the last commit time, **Errors** with the
records **Failed**, and **Behind the source** where the source tells it. Then the **Queue** against its
bounds in records and bytes; the **Job** card for a pipeline of `mode once` (started and finished, read
and written, failed, the chunks done against the plan, the time left); the **Sinks** table (records,
bytes, batches, retries, errors and records failed, for each sink); and **Dead letters** (written, not
written, replayed, and the ones kept against their bounds, linking to that tab).

The buttons steer the running pipeline:

- **Pause** holds the source while what it read is still written and the checkpoint saved; **Resume**
  lets it go on, and starts again a pipeline stopped on an error.
- **New snapshot** asks first: it reads the source again from its start, removing the checkpoint and
  restarting the pipeline, so the sinks get every record again over what they hold.
- **Test connection** tests the connections of the source, the sinks and the `dead_letter` sink, writing
  no record. The card lists each part, its module, the result (**passed**, **failed**, **no test: try a
  dry run**) and the time it took, with a warning beside a test that passed but would lose data.
- **Dry run** reads the first records through the transforms and shows each one as read and what it
  became — dropped by the transforms, or the records handed to the sinks — writing nothing and saving
  no position. Its form sets how many records (10, at most 1000), how long to wait for them (5s) and,
  here, whether to read **from the checkpoint** or **from the start**; it says when the source ended,
  when the wait ended first, or when no record came. The records a dry run reads and a dead letter
  holds are shown to the person signed in, never kept.

A pipeline that does not run (it is not in the running config) says so, with a link to its **Edit**
page and the reminder to save and apply.

### Edit

The pipeline's fields, made from the schema at its place in the draft: the name and `mode`, the
source's own fields and params, the transforms in order, each sink's fields with its `batch`, and
`queue`, `retry`, `checkpoint_interval`, `on_error` and the `dead_letter` sink. As on every config
page there are **UI**, **.conf**, **.yaml** and **.json** tabs for this pipeline's part of the draft.

A note on the page says that edits go into the shared draft, and that **Test connection** and **Dry
run** check the draft as it stands, before Save and apply (they post the pipeline's config to
`POST /sync/test` and `POST /sync/dry-run`), so a pipeline can be tried before it is ever applied. The
assistant's page tools draft into the same form when it is asked to (page_get reads it, page_edit
writes it, page_revert takes the draft back), and **Save and apply** stays the person's click.
**Remove the pipeline** asks first, then removes it from the draft: **Save and apply** stops it, and its
checkpoint file stays, to be used if a pipeline of the same name comes back.

### Checkpoint

The pipeline's checkpoint file: when it was **Saved**, its **Lineage** (the ID a reset, a position set
by hand or a new job starts another of), a job's progress, and the **Position** itself as JSON in a
text box, in the source's own form. The page reads it when it is opened or refreshed — not on a timer,
because the checkpoint moves while the pipeline runs.

**Set position** saves the edited JSON after a confirm that says records before it are skipped, or
those after it written again; the pipeline stops and starts again from it. **Reset** removes the
checkpoint after a confirm that says records may be read and written again, so the pipeline starts as
its source's params say. Before the first checkpoint is saved the page says so and offers no reset.
This is the checkpoint `fit sync checkpoint -c fit.conf <name>` shows, and `-set … -confirm` or
`-reset -confirm` write, with the same confirmations.

### Dead letters

The records a `dead_letters` sink kept, oldest first, 50 at a time with **More**, and above them how
many are kept against `max_records` and `max_bytes`, and the file's path. The file is
`<state_dir>/<name>.dead_letters.jsonl`, one JSON line per letter, synced before it counts as written.
A row holds a checkbox, the **ID**, when it was **Saved**, **Where** it failed (the sink, or the
transform), its **Attempts**, the **Error**, and the **Record** in its canonical JSON, folded open. The
first error has an **Ask AI** button for the `sync-pipeline` skill.

**Replay selected** and **Replay all** ask first; a replay stops the pipeline, writes each record to the
sink it failed at — through the transforms again, to every sink, when a transform failed it — as it was,
over any later change of its key, drops the ones written, keeps and reports the ones failing again, and
starts the pipeline again. **Drop selected** and **Drop all** ask first too, and their records are lost.
A pipeline whose `dead_letter` sink is not a `dead_letters` sink has nothing to replay here, and the
page says how to set one: `on_error dead_letter` with a `dead_letter` sink whose module is
`dead_letters`. `fit sync dead-letters` lists them from the command line, and its `-replay -all` and
`-drop` do the same as the buttons.

## Key parameters

Only the key fields are listed here; every field with its default is in
[the config reference: Sync](../reference/config.md#sync), and the modules are described in the schema's
[sync](../../schema/doc/sync.md), [sources](../../schema/doc/sync_sources.md),
[sinks](../../schema/doc/sync_sinks.md), [transforms](../../schema/doc/sync_transforms.md) and
[codecs](../../schema/doc/sync_codecs.md) chapters.

| Field | Default | Description |
| --- | --- | --- |
| `sync.state_dir` | — | Required. The directory of `<name>.json` checkpoints, made when missing; moving a pipeline to another fit means moving its file |
| `pipelines[].name` | — | Required. Unique; letters, digits, `_`, `.` and `-`; it names the checkpoint file and the dead letters' |
| `pipelines[].source` | — | Required. A module of `sync.sources` and its `params` |
| `pipelines[].transforms` | none | The modules of `sync.transforms`, in order; each may change, drop or split a record |
| `pipelines[].sinks` | — | Required, at least one. Each a name, a module of `sync.sinks` and its `params`; every sink takes every record |
| `source.codec`, `sink.codec` | `line` | How bytes become records and records become bytes, on the connectors that read or write bytes: `line`, `json`, `csv`, `raw`, `canal_json`, `debezium`, `ticdc_simple`, `wasm` |
| `pipelines[].mode` | `stream` | `stream` follows the source for good; `once` reads it to its end, a batch job |
| `pipelines[].on_error` | `stop` | What a record still failing does: `stop` the pipeline, `skip` it, or `dead_letter` it to the `dead_letter` sink |
| `pipelines[].dead_letter` | none | The sink the dead letters go to; needed with `on_error dead_letter`, and only with it |
| `pipelines[].queue.max_records`, `.max_bytes` | 10000, 64 MiB | The bound on records read and not yet acknowledged by every sink |
| `pipelines[].sinks[].batch.max_records`, `.max_bytes`, `.max_wait` | 500, 4 MiB, 1s | When a batch closes: records, bytes, or time after its first record |
| `pipelines[].checkpoint_interval` | 5s | At most how often the checkpoint is written when it has moved; it is also written when the pipeline stops |
| `pipelines[].retry.max_attempts`, `.backoff`, `.max_backoff` | 5, 200ms, 30s | The attempts of a failed write, and the wait before each, doubled up to the maximum |
| `pipelines[].job.max_parallel`, `.schedule`, `.time_zone` | 4, none, `UTC` | A job's chunks read at once and its cron schedule; `mode once` only |
| `dead_letters` sink's `max_records`, `max_bytes`, `when_full` | 10000, 64 MiB, `refuse` | The dead letters kept, and whether a full file refuses one (`refuse`, stopping the pipeline) or drops the oldest (`drop_oldest`) |

## In .conf

The member is a `sync` block: `state_dir`, then a `pipelines` block per pipeline. This one tails a
directory of JSON log lines and writes them to a rotated file, with dead letters for the records that
still fail after their retries; its paths and `state_dir` are the ones a reader would type, and must
be writable by the fit user.

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
			batch { max_records 1000; max_wait 1s }
		}
		on_error dead_letter
		dead_letter { name errors; sink dead_letters { max_records 50000 } }
		retry { max_attempts 5; backoff 200ms; max_backoff 30s }
	}
}
```

`fit run -c fit.conf` serves it beside the gateway. `fit sync run -c fit.conf orders-log` runs this one
pipeline alone, without serving anything else, until it is interrupted, its checkpoint saved; add
`--once` to read it to the end as a job. For a first try with nothing to tail, swap the source for
`source generate { records 100 }` and the sink for `sinks out { sink stdout }` — the same `.conf` shape,
and it prints the records.

## See also

- [Sync](../sync.md): the record model, every connector and what it needs on its server, the types
  between systems, the checkpoints, [delivery guarantees](../sync.md#delivery-guarantees), plugins,
  jobs, metrics, dead letters and operations.
- [The console's Sync](../console.md#sync): the pages as the console describes them, the assistant's
  tools and the skill, and what it may send to the model.
- [The config reference: Sync](../reference/config.md#sync): every field with its default.
- [Decision 036](../decisions/036-sync.md): the design of the role.
- [BitTorrent](27-bittorrent.md): the module after this one.
- [Assistant](14-assistant.md#skills): the tool groups, the skills and the `sync-pipeline` skill.
- [Global settings](07-global-settings.md): the shared draft, the import and export of the whole
  config, `sync` included.
