fit's sync role: pipelines that read records from a source, put them through transforms in order,
and write them to one or more sinks, as Filebeat does for files and Canal, Debezium or DataX do for
databases. Every source makes, and every sink takes, the same record: its source and namespace, an
operation (`insert`, `update`, `delete`, `ddl`, `message`), the key, the row before and after as
typed values, the commit time, the transaction and the source's position. Sources, sinks and
transforms are modules, of the namespaces `sync.sources`, `sync.sinks` and `sync.transforms`, named
by `module` with their `params`, as handlers are.

Nothing is lost: a pipeline's checkpoint, `<state_dir>/<name>.json`, is the position of the last
record every sink has acknowledged that ends a source transaction, never one inside a transaction.
It is written at most every `checkpoint_interval` and when the pipeline stops, to a temporary file,
synced and renamed into place, and a pipeline starts from it; so after a crash or `kill -9` the
records after it are written again, and sinks write idempotently. A write that fails is tried
again, `retry.max_attempts` in all with a doubling backoff, from the record that failed; a record
still failing, or one a transform fails on, goes as `on_error` says: `stop` the pipeline, `skip` it,
or write it to the `dead_letter` sink. A source that fails restarts the pipeline from its
checkpoint, after the same backoff.

Everything is bounded: `queue` bounds the records read and not yet acknowledged by every sink, in
records and bytes, so a slow sink holds the source back rather than growing memory; each sink's
`batch` bounds a write in records, bytes and time. Pipelines run on goroutines of their own, a few
for each, never on fib's workers. A reload starts the pipelines it adds, stops those it removes and
restarts only those whose settings changed. See [decision 036](../decisions/036-sync.md).

A pipeline of `mode: "once"` is a batch job, as DataX runs them: it stops at the source's end with
the checkpoint written. A source that can split its snapshot (by ranges of a table's primary key,
or `generate`'s `chunk_records`) is read in chunks, `job.max_parallel` at a time, and each sink that
takes writes at once gets as many writers, a record's key always going to the same one. The
checkpoint keeps the job's progress: its chunks, those done, where each under way is, and the
records read, written and failed, with an ETA; so a job interrupted, by a crash or a restart, goes
on from its finished chunks without reading them again. A finished job does not run again when fit
restarts; `job.schedule`, in cron's five fields, starts it again, a new job each time, never while a
run is still going. `fit sync run <pipeline> --once` runs one on its own from the command line and
exits with its result.

The Admin API shows and steers the running pipelines, and so does the console under
`/api/m/admin/`: `GET /sync/pipelines` and `/sync/pipelines/{name}` give each one's state, its
checkpoint, its queue against its bounds, the records read, acknowledged and failed, the throughput,
the lag from a record's commit time to its acknowledgement, and its errors with the last.
`POST /sync/pipelines/{name}` with `{"action": "pause"}` holds the source while what it read is
written and the checkpoint saved; `resume` lets it go on, and starts again a pipeline stopped on an
error; `snapshot` reads the source again from its start. `.../checkpoint` is the checkpoint file,
set with `PUT {"position"}` and removed with `DELETE`, the pipeline restarted from there; a change
that would skip or replay records (another position, a reset or a snapshot while there is a
checkpoint) is answered 409 unless it says `confirm`. `POST .../test` tests the connections of the
connectors that have a test, and `POST .../dry-run` reads the first records (`limit`, 10, within
`timeout`, 5s) from the checkpoint or, with `"from": "start"`, the source's start, through the
transforms, and returns them with what each became, writing nothing and saving no position;
`POST /sync/test` and `/sync/dry-run` do the same for a pipeline's config not applied yet,
`{"pipeline": {...}}`. `fit sync status`, `pause`, `resume`, `snapshot`, `checkpoint`, `test` and
`dry-run` call these through the Admin API the bootstrap config names, as `fit token` does.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "trial",
        "mode": "once",
        "source": {"module": "generate", "params": {"records": 1000, "tx_size": 10}},
        "sinks": [
          {"name": "out", "sink": {"module": "stdout"}, "batch": {"max_records": 100, "max_wait": "200ms"}},
          {"name": "kept", "sink": {"module": "memory", "params": {"store": "trial"}}}
        ],
        "on_error": "dead_letter",
        "dead_letter": {"name": "errors", "sink": {"module": "stdout"}},
        "checkpoint_interval": "1s",
        "queue": {"max_records": 5000, "max_bytes": 16777216},
        "retry": {"max_attempts": 5, "backoff": "200ms", "max_backoff": "30s"}
      }
    ]
  }
}
```
