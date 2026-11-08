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
restarts only those whose settings changed. `mode: "once"` stops at the source's end with the
checkpoint written, as a batch job. See [decision 036](../decisions/036-sync.md).

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
