Writes records to ClickHouse over its HTTP interface, through fib's asynchronous HTTP client. Each
run of a batch's records for one table is one `INSERT INTO ... FORMAT JSONEachRow`, its rows the
records' rows as plain JSON (numbers bare, decimals as numbers, times in RFC 3339, read with
`date_time_input_format=best_effort`, bytes in base64), with an `insert_deduplication_token` made
from the run's table, first and last positions and count: written again after a crash, the same
records make the same token and the server drops them. The token works on a replicated table, and
on a plain `MergeTree` only with `non_replicated_deduplication_window` set.

An insert, an update or a message is its row (`after`) with two more columns: `_version`
(`version_column`), the commit time in Unix nanoseconds or `version_field`'s value, and `_deleted`
(`deleted_column`) set to 0. A delete is the row as it was (`before`), or its key, with `_deleted`
set to 1. So a `ReplacingMergeTree(_version, _deleted)` ordered by the key, read with `FINAL`, holds
each key's last state, however often a replay writes the records again. Two changes of one key with
the same version (a source whose commit times are seconds) leave the one inserted last: set
`version_field` to a column that tells them apart. `deletes: "delete"` writes deletes as lightweight
`DELETE`s by the key instead, in order between the inserts, which are then not deduplicated; `plain`
writes rows as they are, for an append-only table. DDL is not written. The table is not made.

A row the server cannot parse fails its record for good (`on_error` takes it), the rows before it
in its insert written again without it; any other failure is retried by the pipeline. A
connection that fails, or a 502, 503 or 504, moves on to the next of `urls`. In a job, the sink
has `job.max_parallel` writers, each key going to one of them.

```sql
CREATE TABLE shop.orders (id UInt64, status String, total Decimal(12, 2),
  _version UInt64, _deleted UInt8)
ENGINE = ReplacingMergeTree(_version, _deleted) ORDER BY id
SETTINGS non_replicated_deduplication_window = 1000
```

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-to-ch",
        "source": {"module": "generate"},
        "sinks": [{"name": "ch", "sink": {"module": "clickhouse", "params": {
          "urls": ["http://ch1:8123", "http://ch2:8123"], "username": "fit", "password_env": "CH_PASSWORD",
          "table": "shop.orders"}},
          "batch": {"max_records": 10000, "max_wait": "1s"}}]
      }
    ]
  }
}
```
