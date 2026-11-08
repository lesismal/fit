Reads the documents of `index` (a name, an alias, a pattern such as `logs-*`, or several joined by
commas) from Elasticsearch 7.12 and later or OpenSearch 2.4 and later, through fib's asynchronous
HTTP client, with the sink's connection params (`urls` with failover, credentials, TLS). A
record's namespace is the document's index, its key `{"_id": "..."}` (which the `elasticsearch`
sink turns back into the same `_id`), and its `after` the `_source` as typed values, numbers as the
`json` codec reads them: without a fraction an int (a uint past int64), otherwise a float.

**The snapshot** (`snapshot: "pit"`, the default) reads every document through a point in time,
in pages of `search_after` on `_shard_doc` (on `_id` in OpenSearch, which has no `_shard_doc`), as
inserts. A snapshot interrupted, by a restart or a point in time gone (one outlives `keep_alive`
between two pages when the sinks hold the queue that long), starts again from its beginning: a
new point in time's sort values are not the old one's.

**Polling.** With `cursor`, a field every document written or changed gets a later value of (an
update time, a sequence number; a date, date_nanos, a number or keyword), the source then polls
every `poll_interval` for documents whose cursor reaches past the last value read, ordered by it,
as inserts (a document's first version) or updates. The checkpoint is the cursor's last value and
the `_id`s read at it, so documents sharing a value, across pages or written after a restart, are
read once each; past 1,000 at one value, the rest of it is read at once through a point in time.
Polling starts from the cursor's highest value before the snapshot, so what changes during the
snapshot is read after it. `snapshot: "cursor"` reads the existing documents by the cursor instead,
from its lowest value, resumed where a restart left off (but documents without the field are not
read); `snapshot: "none"` starts at the cursor's value now.

What polling cannot see: **deletes**, and a document whose cursor is written at or below the last
value read once the source has read past it (a write that lands late, or a clock behind). For a
date cursor, polls stay `delay` (2s) behind the cluster's clock, so that a document becomes
searchable (after the index refreshes, 1s by default) before polling passes its time. Keep deleted
documents with a soft-delete field and name it in `deleted_field`: a document with it set, to
anything but null or false, is read as a delete of its `_id`.

**Jobs.** In a job (`mode: "once"`) without a cursor, the snapshot is split into slices of the
`_id`'s hash, one per `chunk_documents` documents (at most 1,024), read `job.max_parallel` at a
time, each through a sliced scroll of its own (Elasticsearch does not slice a point in time by the
`_id`, and its other slices are not the same in another point in time), so a job interrupted reads
again only the slices under way, from their start. Each slice is read as the cluster has it when
its scroll opens. A job with a cursor is not split: it reads as a stream does, up to now, and the
next job reads only what changed since.

Pages hold at most `page_size` documents, and fewer while answers run past `max_page_bytes`; an
answer past 64 MiB is asked again for half as many. A search the cluster answers 429 is sent again
for as long as it does; one that fails on a shard fails the run, which the pipeline restarts from
its checkpoint, rather than leaving a gap. Points in time and scrolls are closed when they end or
fail; at most one is open for a stream, `job.max_parallel` for a job.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-to-file",
        "source": {"module": "elasticsearch", "params": {
          "urls": ["https://es1:9200", "https://es2:9200"], "ca_file": "/etc/fit/es-ca.pem",
          "username": "fit", "password_env": "ES_PASSWORD",
          "index": "orders-*", "cursor": "updated_at", "deleted_field": "deleted"}},
        "sinks": [{"name": "out", "sink": {"module": "file", "params": {"path": "/var/lib/fit/orders.jsonl", "codec": {"module": "json"}}}}]
      }
    ]
  }
}
```
